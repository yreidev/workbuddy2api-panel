// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package proxies

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
)

// importPoolName 凭证里带 proxy_url、又没有默认池可放时，自动建的池叫这个。
const importPoolName = "导入的代理"

// Track 号进了号池（启动加载、登录、导入）：记下它的绑定，依次看——
//   - 绑的代理还在：照旧；
//   - 凭证里带了 proxy_url（别处导出的凭证）：认出（或新建）这个代理，单独指定给它；
//   - 绑定指向的代理没了、池还在：在原池里重新分配；
//   - isNew（新登录、新导入的号）：放进这个域的默认池；
//   - 剩下的（池也没了，或凭证是别处拷来的）：解绑。
func (r *Registry) Track(a *auth.Auth, isNew bool) {
	r.track(a, isNew, "")
}

// TrackInto 导入账号时选了池：号都放进池 pool（已经绑在这个池里的照旧）；凭证里带 proxy_url 的认出
// 那个代理单独指定，新建的代理也建在这个池里。池不存在时同 Track(a, true)。
func (r *Registry) TrackInto(a *auth.Auth, pool string) {
	r.track(a, true, pool)
}

func (r *Registry) track(a *auth.Auth, isNew bool, pool string) {
	realm, b, hint := a.Realm(), a.ProxyBinding(), a.TakeProxyURL()
	_ = r.change(func(t *tx) error {
		acc := &account{uid: a.UID, realm: realm, a: a, b: b}
		r.accounts[a.UID] = acc
		// 号池那边可能是新加的条目：不管原因变没变，都重新推一遍。
		delete(r.gates, a.UID)
		if r.poolLocked(pool) == nil {
			pool = ""
		}
		if px := r.proxyLocked(b.ID); px != nil && (pool == "" || px.Pool == pool) {
			t.bind(acc, auth.ProxyBinding{Pool: px.Pool, ID: px.ID, Manual: b.Manual})
			return nil
		}
		if hint != "" && r.adoptURLLocked(t, acc, hint, pool) {
			return nil
		}
		target := pool
		if target == "" && r.poolLocked(b.Pool) != nil {
			target = b.Pool
		}
		if pl := r.defaultPoolLocked(realm); target == "" && isNew && pl != nil {
			target = pl.ID
		}
		switch {
		case target != "":
			r.assignLocked(t, acc, target)
		case !b.Empty():
			t.bind(acc, auth.ProxyBinding{})
		}
		return nil
	})
}

// adoptURLLocked 凭证里带的 proxy_url：已有同一个代理就单独指定给这个号，没有就新建（放进池 pool，没给
// 放进这个域的默认池，再没有就放进「导入的代理」池）。地址认不出来只记日志、不绑，返回 false。
func (r *Registry) adoptURLLocked(t *tx, acc *account, raw, pool string) bool {
	p, err := parseOne(raw, "http")
	if err != nil {
		logf("proxy WARN 号 %s 凭证里的 proxy_url 认不出来，没按它绑代理：%v", short(acc.uid), err)
		return false
	}
	px := r.proxyByKeyLocked(dedupKey(p.URL))
	if px == nil {
		pl := r.poolLocked(pool)
		if pl == nil {
			pl = r.defaultPoolLocked(acc.realm)
		}
		if pl == nil {
			for _, o := range r.pools {
				if o.Name == importPoolName {
					pl = o
				}
			}
		}
		if pl == nil {
			pl = &Pool{ID: newID(), Name: importPoolName}
			r.pools = append(r.pools, pl)
		}
		px = &Proxy{ID: newID(), Name: hostPort(p.URL), URL: p.URL, Pool: pl.ID, CreatedAt: r.now()}
		r.proxies = append(r.proxies, px)
		r.due[px.ID] = true
		t.saveFile, t.kick = true, true
		logf("proxy 号 %s 凭证里带的代理 %s 不在列表里，已加进池「%s」", short(acc.uid), Masked(p.URL), pl.Name)
	}
	t.bind(acc, auth.ProxyBinding{Pool: px.Pool, ID: px.ID, Manual: true})
	return true
}

// Untrack 号从号池里删掉了。
func (r *Registry) Untrack(uid string) {
	_ = r.change(func(t *tx) error {
		delete(r.accounts, uid)
		delete(r.gates, uid)
		r.fillWaitingLocked(t)
		return nil
	})
}

// BindTarget 批量绑定的目标：Pool（池内自动分配）、Proxy（单独指定）、Unbind（解绑）三选一。
type BindTarget struct {
	Pool   string `json:"pool"`
	Proxy  string `json:"proxy"`
	Unbind bool   `json:"unbind"`
}

// BindResult 一个号绑定的结果。
type BindResult struct {
	UID string `json:"uid"`
	// Proxy、ProxyName 分到的代理；Waiting 池里没有空位，在等代理；Error 没绑成的原因。
	Proxy     string `json:"proxy,omitempty"`
	ProxyName string `json:"proxy_name,omitempty"`
	Waiting   bool   `json:"waiting,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Bind 批量绑定。绑池时已经在这个池里有代理的号不动，其余按绑号最少分；单独指定代理也守池的上限。
func (r *Registry) Bind(uids []string, target BindTarget) ([]BindResult, error) {
	var out []BindResult
	err := r.change(func(t *tx) error {
		var px *Proxy
		switch {
		case target.Unbind:
		case target.Proxy != "":
			if px = r.proxyLocked(target.Proxy); px == nil {
				return fmt.Errorf("代理 %s %w", target.Proxy, ErrNotFound)
			}
		case target.Pool != "":
			if r.poolLocked(target.Pool) == nil {
				return fmt.Errorf("池 %s %w", target.Pool, ErrNotFound)
			}
		default:
			return errors.New("要指定池、代理或者解绑")
		}
		for _, uid := range uids {
			res := BindResult{UID: uid}
			acc := r.accounts[uid]
			switch {
			case acc == nil:
				res.Error = "号不存在"
			case target.Unbind:
				t.bind(acc, auth.ProxyBinding{})
			case px != nil:
				if acc.b.ID != px.ID && r.fullLocked(px, r.now()) {
					res.Error = fmt.Sprintf("代理「%s」已经绑满了", px.Name)
					break
				}
				t.bind(acc, auth.ProxyBinding{Pool: px.Pool, ID: px.ID, Manual: true})
			default:
				if acc.b.Pool == target.Pool && acc.b.ID != "" {
					break
				}
				t.bind(acc, auth.ProxyBinding{Pool: target.Pool})
				r.assignLocked(t, acc, target.Pool)
			}
			if acc != nil && res.Error == "" {
				res.Proxy, res.Waiting = acc.b.ID, acc.b.Pool != "" && acc.b.ID == ""
				if p := r.proxyLocked(acc.b.ID); p != nil {
					res.ProxyName = p.Name
				}
			}
			out = append(out, res)
		}
		r.fillWaitingLocked(t)
		return nil
	})
	return out, err
}

// Rebalance 池内重新均分：池里不是单独指定的号全部重新按绑号最少分一遍（会改号的出口 IP）。
// 返回换了代理的号数。
func (r *Registry) Rebalance(pool string) (int, error) {
	moved := 0
	err := r.change(func(t *tx) error {
		if r.poolLocked(pool) == nil {
			return fmt.Errorf("池 %s %w", pool, ErrNotFound)
		}
		// 先把这些号全部放开（记下原来的代理），再一个个分，才分得匀。
		before := map[string]string{}
		var accs []*account
		for _, acc := range r.sortedAccountsLocked() {
			if acc.b.Pool == pool && !acc.b.Manual {
				before[acc.uid] = acc.b.ID
				acc.b.ID = ""
				accs = append(accs, acc)
			}
		}
		for _, acc := range accs {
			r.assignLocked(t, acc, pool)
			if acc.b.ID != before[acc.uid] {
				t.touch(acc)
				moved++
			}
		}
		if moved > 0 {
			logf("proxy 池内重新均分：%d 个号换了代理", moved)
		}
		return nil
	})
	return moved, err
}

// assignLocked 在池 pool 里给号分一个代理（可用的、没绑满的里面绑号最少的）；分不到就记成等代理。
func (r *Registry) assignLocked(t *tx, acc *account, pool string) {
	px := r.pickLocked(pool, r.now())
	if px == nil {
		t.bind(acc, auth.ProxyBinding{Pool: pool})
		return
	}
	t.bind(acc, auth.ProxyBinding{Pool: pool, ID: px.ID})
}

// fillWaitingLocked 给等代理的号补上代理（加了代理、调高上限、代理恢复、有号走了以后）。
func (r *Registry) fillWaitingLocked(t *tx) {
	for _, acc := range r.sortedAccountsLocked() {
		if acc.b.Pool != "" && acc.b.ID == "" && r.poolLocked(acc.b.Pool) != nil {
			r.assignLocked(t, acc, acc.b.Pool)
		}
	}
}

// pickLocked 池里该分给新号的代理：可用、没绑满，测过是通的优先于还没测的，再挑绑号最少的，
// 一样多就挑先加进来的。没有返回 nil。
func (r *Registry) pickLocked(pool string, now time.Time) *Proxy {
	var best *Proxy
	load := r.loadLocked(now)
	rank := func(px *Proxy) int {
		if px.Health.State == "ok" {
			return 0
		}
		return 1
	}
	for _, px := range r.proxies {
		if px.Pool != pool || !r.usableLocked(px, now) || r.fullLocked(px, now) {
			continue
		}
		if best == nil || rank(px) < rank(best) || (rank(px) == rank(best) && load[px.ID] < load[best.ID]) {
			best = px
		}
	}
	return best
}

// loadLocked 每个代理绑了几个号（加上还没用完的登录占位）。
func (r *Registry) loadLocked(now time.Time) map[string]int {
	load := map[string]int{}
	for _, acc := range r.accounts {
		if acc.b.ID != "" {
			load[acc.b.ID]++
		}
	}
	for _, h := range r.logins {
		if now.Before(h.until) {
			load[h.proxy]++
		}
	}
	return load
}

// fullLocked 代理是否已绑满（池没设上限就永远不满）。
func (r *Registry) fullLocked(px *Proxy, now time.Time) bool {
	pl := r.poolLocked(px.Pool)
	return pl != nil && pl.MaxPerProxy > 0 && r.loadLocked(now)[px.ID] >= pl.MaxPerProxy
}

// usableLocked 代理能不能用：没到期、没测出不通（还没测的算能用）。
func (r *Registry) usableLocked(px *Proxy, now time.Time) bool {
	return !expired(px, now) && px.Health.State != "down"
}

func expired(px *Proxy, now time.Time) bool {
	return px.ExpiresAt != nil && !now.Before(*px.ExpiresAt)
}

// unavailableSince 代理从什么时候开始不能用（到期时间或这次不通的开始时间），能用返回零值。
func unavailableSince(px *Proxy, now time.Time) time.Time {
	switch {
	case expired(px, now):
		return *px.ExpiresAt
	case px.Health.State == "down":
		return px.Health.DownSince
	}
	return time.Time{}
}

// gateLocked 号现在为什么不能出站，能出站返回 ""。面板按原因开头的几个字分类（「没绑代理」「等代理」
// 「代理「…」已到期」「代理「…」连不上」，见 web/src/lib/accounts.ts 的 blockedLabel），改文案要一起改。
func (r *Registry) gateLocked(acc *account, now time.Time) string {
	b := acc.b
	if b.Empty() {
		if r.settings.RequireBound {
			return "没绑代理（开着「只许走号的代理」）"
		}
		return ""
	}
	px := r.proxyLocked(b.ID)
	if px == nil {
		name := b.Pool
		if pl := r.poolLocked(b.Pool); pl != nil {
			name = pl.Name
		}
		return fmt.Sprintf("等代理：池「%s」里没有可用的空位", name)
	}
	if expired(px, now) {
		return fmt.Sprintf("代理「%s」已到期", px.Name)
	}
	if px.Health.State == "down" {
		return fmt.Sprintf("代理「%s」连不上（%s 起）", px.Name, px.Health.DownSince.Format("01-02 15:04"))
	}
	return ""
}

type gateChange struct{ uid, reason string }

// diffGatesLocked 重算所有号的不可用原因，返回和上次推给号池的不一样的那些。
func (r *Registry) diffGatesLocked() []gateChange {
	now := r.now()
	var out []gateChange
	for _, acc := range r.sortedAccountsLocked() {
		g := r.gateLocked(acc, now)
		if r.gates[acc.uid] != g {
			out = append(out, gateChange{acc.uid, g})
			if g == "" {
				delete(r.gates, acc.uid)
			} else {
				r.gates[acc.uid] = g
			}
		}
	}
	return out
}

// BlockedError 号现在不能出站（线路选择返回它，调用方当作传输层失败：不罚号、换号）。
type BlockedError struct {
	UID    string
	Reason string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("号 %s 现在不能出站：%s", short(e.UID), e.Reason)
}

// RouteAccount 号 uid 的出站线路：url 为空走默认线路（环境变量里的代理或直连）；号现在不能出站返回
// *BlockedError。不在号池里的号（比如刚登录还没加进来）按没绑处理。
func (r *Registry) RouteAccount(uid string) (id, name, url string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	acc := r.accounts[uid]
	if acc == nil {
		acc = &account{uid: uid}
	}
	if g := r.gateLocked(acc, r.now()); g != "" {
		return "", "", "", &BlockedError{UID: uid, Reason: g}
	}
	px := r.proxyLocked(acc.b.ID)
	if px == nil {
		return "", "", "", nil
	}
	return px.ID, px.Name, px.URL, nil
}

// RouteProxy 直接按代理 ID 取线路（登录新号时号还没生成，走登录占的代理）。代理不存在或到期报错；
// 测出不通的照样给（登录失败会报出来）。
func (r *Registry) RouteProxy(id string) (name, url string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	px := r.proxyLocked(id)
	if px == nil {
		return "", "", fmt.Errorf("代理 %s %w", id, ErrNotFound)
	}
	if expired(px, r.now()) {
		return "", "", fmt.Errorf("代理「%s」已到期", px.Name)
	}
	return px.Name, px.URL, nil
}

// ExitKey 号的出口，风控波按它分组：绑了代理的按代理的出口 IP（还没查到就按代理），没绑的共用默认
// 出口（查到了服务器出口 IP 就按 IP，这样和服务器同 IP 的代理也会归到一起）。label 是代理的名字或
// 「服务器出口」，写进日志和冷却原因，不带 IP（日志会被别人看到）。
func (r *Registry) ExitKey(uid string) (key, label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key, label = "default", "服务器出口"
	if e := r.defaultExit; e != nil && e.IP != "" {
		key = "ip:" + e.IP
	}
	if acc := r.accounts[uid]; acc != nil {
		if px := r.proxyLocked(acc.b.ID); px != nil {
			key, label = "proxy:"+px.ID, "代理「"+logfmt.MaskIPs(px.Name)+"」"
			if e := px.Health.Exit; e != nil && e.IP != "" {
				key = "ip:" + e.IP
			}
		}
	}
	return key, label
}

// HoldForLogin 登录新号前先在池里占一个代理，登录请求就走它；pool 为空用这个域的默认池，为 NoPool 不绑。
// 没有可用的池（也没开「只许走号的代理」）返回空的 proxy，登录走默认线路。返回的 hold 用完交给 ReleaseLogin。
func (r *Registry) HoldForLogin(pool, realm string) (hold, proxy string, err error) {
	err = r.change(func(t *tx) error {
		now := r.now()
		for k, h := range r.logins {
			if !now.Before(h.until) {
				delete(r.logins, k)
			}
		}
		pl := r.poolLocked(pool)
		switch {
		case pool == NoPool:
		case pool != "" && pl == nil:
			return fmt.Errorf("池 %s %w", pool, ErrNotFound)
		case pl == nil:
			pl = r.defaultPoolLocked(realm)
		}
		if pl == nil {
			if r.settings.RequireBound {
				return errors.New("开着「只许走号的代理」，登录新号要先选一个代理池")
			}
			return nil
		}
		px := r.pickLocked(pl.ID, now)
		if px == nil {
			return fmt.Errorf("池「%s」里没有可用的空位，先加代理或调高上限", pl.Name)
		}
		hold, proxy = newID(), px.ID
		r.logins[hold] = loginHold{proxy: px.ID, until: now.Add(loginHoldTTL)}
		return nil
	})
	return hold, proxy, err
}

// ReleaseLogin 放掉登录占位（登录成功、失败或放弃都要放），空出来的位子马上补给等代理的号。
func (r *Registry) ReleaseLogin(hold string) {
	if hold == "" {
		return
	}
	_ = r.change(func(t *tx) error {
		delete(r.logins, hold)
		r.fillWaitingLocked(t)
		return nil
	})
}

// Binding 号现在的绑定（不在号池里返回零值）。
func (r *Registry) Binding(uid string) auth.ProxyBinding {
	r.mu.Lock()
	defer r.mu.Unlock()
	if acc := r.accounts[uid]; acc != nil {
		return acc.b
	}
	return auth.ProxyBinding{}
}

// ProxyURL 号绑的代理的完整地址（导出凭证时带上），没绑返回 ""。
func (r *Registry) ProxyURL(uid string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if acc := r.accounts[uid]; acc != nil {
		if px := r.proxyLocked(acc.b.ID); px != nil {
			return px.URL
		}
	}
	return ""
}

// ProxyName 号绑的代理的名字（请求记录用），没绑或在等代理返回 ""。
func (r *Registry) ProxyName(uid string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if acc := r.accounts[uid]; acc != nil {
		if px := r.proxyLocked(acc.b.ID); px != nil {
			return px.Name
		}
	}
	return ""
}

// PoolByName 按名字或 ID 找池（命令行工具 -pool 参数用），找不到返回 ""。
func (r *Registry) PoolByName(s string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s = strings.TrimSpace(s)
	for _, pl := range r.pools {
		if pl.ID == s || pl.Name == s {
			return pl.ID
		}
	}
	return ""
}

// Blocked 号现在为什么不能出站（"" = 能），给命令行工具和面板用。
func (r *Registry) Blocked(uid string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	acc := r.accounts[uid]
	if acc == nil {
		acc = &account{uid: uid}
	}
	return r.gateLocked(acc, r.now())
}

// shortList 一组号的 UID 前 8 位，排好用顿号连起来（日志用）。
func shortList(uids []string) string {
	out := make([]string, 0, len(uids))
	for _, u := range uids {
		out = append(out, short(u))
	}
	slices.Sort(out)
	return strings.Join(out, "、")
}
