// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// Package proxies 号的出站代理（代理池）：管理员把代理分成几个池，号绑到池上以后分到池里绑号最少的
// 代理并固定下来，这个号发给官方的全部请求都从这个代理出去（线路选择见 upstream/route.go）。
//
// 规则（issue #4，2026-11-01 定案，见 docs/proxy.md）：
//   - 池就是分组：号绑池，池内按绑号最少分代理，之后固定不变；单个号可以直接指定代理（Manual）；
//     国内版、国际版各可设一个默认池，新加的号自动进去。
//   - 代理连不上（请求连不上代理、健康检查不通、到期）就标不可用，绑在上面的号暂停选号（不罚号），
//     复查恢复了就放开；连续不可用超过 RebindMinutes 而且同池有可用代理时改绑。绝不退回直连。
//   - 池可设每个代理最多几个号；池满时新号不分配也不接请求（等代理），有空位了自动补上。
//   - 「只许走号的代理」打开后，没绑代理的号不接请求、不跑定时任务。
//
// 落盘：代理、池、设置、健康状态在 data/proxies.json（0600，原子替换，含代理密码）；号绑哪个代理
// 记在号自己的凭证文件里（auth.ProxyBinding），命令行工具读同样两份文件。增删改立即写盘，写不成
// 就撤回这次改动；健康检查的结果一轮查完再写。
//
// 锁：mu 管全部内存状态；pushMu 串起「改状态 → 写凭证 → 把号的不可用原因推给号池」，推送在 mu 外
// 做。号池持着自己的锁会来问 ExitKey（取 mu），所以持 mu 时绝不调号池、不碰 auth 的锁。
//
// 日志不带 IP（logf）：没起名字的代理名字就是「IP:端口」，日志会被别人看到。面板里照常显示。
package proxies

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
)

const fileVersion = 1

// logf 记一行日志，IP 遮掉（logfmt.MaskIPs，见文件头）。
func logf(format string, args ...any) {
	log.Print(logfmt.MaskIPs(fmt.Sprintf(format, args...)))
}

// 设置的默认值。
const (
	DefaultHealthMinutes  = 5
	DefaultExitMinutes    = 60
	DefaultRebindMinutes  = 30
	DefaultExpiryWarnDays = 3
)

// loginHoldTTL 登录占位多久没用完就放掉（面板登录会话本身 15 分钟过期，占位多留几分钟）。
const loginHoldTTL = 20 * time.Minute

// NoPool 登录、导入账号时选「不绑代理」（HoldForLogin 的 pool 参数、面板的池选择）。
const NoPool = "-"

// Settings 代理池的设置。存在 proxies.json 里，命令行工具也读。
type Settings struct {
	// RequireBound 只许走号的代理：没绑代理的号不接请求、不跑定时任务，命令行工具也跳过。
	RequireBound bool `json:"require_bound"`
	// HealthMinutes 健康检查间隔（只测连通），ExitMinutes 查出口 IP 的间隔，RebindMinutes 代理连续
	// 不可用多久后把号改绑，ExpiryWarnDays 到期前几天开始提醒。<=0 用默认值。
	HealthMinutes  int `json:"health_minutes"`
	ExitMinutes    int `json:"exit_minutes"`
	RebindMinutes  int `json:"rebind_minutes"`
	ExpiryWarnDays int `json:"expiry_warn_days"`
	// ExitIPURLs 查出口 IP 的地址，按顺序试，第一个查到的为准；空用内置的（见 exit.go）。
	ExitIPURLs []string `json:"exit_ip_urls,omitempty"`
}

// normalized 补上默认值、去掉空地址。
func (s Settings) normalized() Settings {
	for _, f := range []struct {
		v   *int
		def int
	}{{&s.HealthMinutes, DefaultHealthMinutes}, {&s.ExitMinutes, DefaultExitMinutes},
		{&s.RebindMinutes, DefaultRebindMinutes}, {&s.ExpiryWarnDays, DefaultExpiryWarnDays}} {
		if *f.v <= 0 {
			*f.v = f.def
		}
	}
	var urls []string
	for _, u := range s.ExitIPURLs {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	s.ExitIPURLs = urls
	return s
}

// Pool 代理池，也就是号的分组。
type Pool struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// MaxPerProxy 每个代理最多绑几个号，0 = 不限。
	MaxPerProxy int `json:"max_per_proxy"`
	// DefaultFor 这个池是哪个域新号的默认池（"cn"、"global"），每个域最多一个池。
	DefaultFor []string `json:"default_for,omitempty"`
}

// Proxy 一个代理。
type Proxy struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// URL 代理地址「协议://用户名:密码@主机:端口」，含密码；给面板列表的只有打码的（见 view.go）。
	URL  string `json:"url"`
	Pool string `json:"pool"`
	// ExpiresAt 到期时间，nil = 不管到期。到期后按不可用处理，续费后改日期即恢复。
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	Health    Health     `json:"health"`
}

// Health 代理的健康状态，落盘，重启不丢。
type Health struct {
	// State 连通："" 还没测、"ok" 通、"down" 不通。
	State     string    `json:"state,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	LatencyMs int64     `json:"latency_ms,omitempty"`
	Error     string    `json:"error,omitempty"`
	// DownSince 这次不通从什么时候开始，通了清零；连续不通超过 RebindMinutes 就把号改绑。
	DownSince time.Time `json:"down_since,omitzero"`
	Exit      *Exit     `json:"exit,omitempty"`
	// PrevExitIP、ExitChangedAt 出口 IP 上一次变化（动态 IP 的代理会一直变）。
	PrevExitIP    string    `json:"prev_exit_ip,omitempty"`
	ExitChangedAt time.Time `json:"exit_changed_at,omitzero"`
}

// Exit 出口 IP 和地区。
type Exit struct {
	IP      string `json:"ip,omitempty"`
	Country string `json:"country,omitempty"`
	Region  string `json:"region,omitempty"`
	City    string `json:"city,omitempty"`
	ISP     string `json:"isp,omitempty"`
	// Source 这次是从哪个地址查到的。
	Source    string    `json:"source,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	// Error 这次没查到的原因；IP 等字段保留上次查到的。
	Error string `json:"error,omitempty"`
}

// Geo 地区和运营商连成一句，如「中国 香港 · 某运营商」。
func (e *Exit) Geo() string {
	if e == nil {
		return ""
	}
	var parts []string
	for _, s := range []string{e.Country, e.Region, e.City} {
		if s != "" && !slices.Contains(parts, s) {
			parts = append(parts, s)
		}
	}
	geo := strings.Join(parts, " ")
	if e.ISP != "" {
		if geo != "" {
			geo += " · "
		}
		geo += e.ISP
	}
	return geo
}

// file proxies.json 的形状。
type file struct {
	Version  int      `json:"version"`
	Settings Settings `json:"settings"`
	Pools    []*Pool  `json:"pools"`
	Proxies  []*Proxy `json:"proxies"`
	// DefaultExit 没绑代理的号的出口（服务器自己的 IP，或环境变量里的代理）。
	DefaultExit *Exit `json:"default_exit,omitempty"`
}

// account 号池里的一个号。b 是这个号的绑定（以这里为准，凭证文件里那份由 pushMu 下写回）。
type account struct {
	uid   string
	realm string
	a     *auth.Auth
	b     auth.ProxyBinding
}

// loginHold 登录新号时先占的代理：登录请求就走它，登录成功后号绑到它上面。
type loginHold struct {
	proxy string
	until time.Time
}

// Tester 经代理连一次官方入口（host:443，照真实请求的 TLS 握手），返回用了多久。由 upstream 提供。
type Tester func(ctx context.Context, rawURL, host string) (time.Duration, error)

// Registry 代理池。零值不可用，用 Open / OpenReadOnly。
type Registry struct {
	pushMu sync.Mutex
	mu     sync.Mutex

	path     string
	readOnly bool

	settings    Settings
	pools       []*Pool
	proxies     []*Proxy
	defaultExit *Exit

	accounts map[string]*account
	logins   map[string]loginHold
	gates    map[string]string // 上次推给号池的不可用原因（"" 不记）

	// due 要在下一轮立刻查的代理（导入后、「全部重测」）。
	due  map[string]bool
	kick chan struct{}

	// onGate 号的不可用原因变了就推给号池（SetGateHook 设置）。
	onGate func(uid, reason string)
	// tester 测连通；hosts 各域健康检查连的官方入口；defaultProxy 没绑代理的号走的代理（环境变量，
	// 查服务器出口用），都由 main 装上（SetProbes）。
	tester       Tester
	hosts        map[string]string
	defaultProxy string
	// probe 查出口 IP，测试可换。
	probe func(ctx context.Context, rawURL string, urls []string) *Exit
	now   func() time.Time
}

// Open 读 path（不存在就是空的代理池），给网关用：增删改写回 path。
func Open(path string) (*Registry, error) {
	r := newRegistry(path)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读代理池 %s：%w", path, err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析代理池 %s：%w", path, err)
	}
	r.settings = f.Settings.normalized()
	r.pools, r.proxies, r.defaultExit = f.Pools, f.Proxies, f.DefaultExit
	for _, px := range r.proxies {
		if r.poolLocked(px.Pool) == nil {
			return nil, fmt.Errorf("解析代理池 %s：代理 %s 所在的池 %s 不存在", path, px.Name, px.Pool)
		}
	}
	return r, nil
}

// OpenReadOnly 同 Open，给命令行工具用：读到的东西只在内存里改，从不写盘，也不写凭证文件。
func OpenReadOnly(path string) (*Registry, error) {
	r, err := Open(path)
	if err != nil {
		return nil, err
	}
	r.readOnly = true
	return r, nil
}

func newRegistry(path string) *Registry {
	return &Registry{
		path:     path,
		settings: Settings{}.normalized(),
		accounts: map[string]*account{},
		logins:   map[string]loginHold{},
		gates:    map[string]string{},
		due:      map[string]bool{},
		kick:     make(chan struct{}, 1),
		probe:    probeExit,
		now:      time.Now,
	}
}

// SetGateHook 设置推送号的不可用原因的函数（号池的 SetProxyBlocked）。在 Track 之前设置。
func (r *Registry) SetGateHook(f func(uid, reason string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onGate = f
}

// SetProbes 装上测连通的函数、各域健康检查连的官方入口、没绑代理的号走的代理（可为空）。
func (r *Registry) SetProbes(t Tester, hosts map[string]string, defaultProxy string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tester, r.hosts, r.defaultProxy = t, hosts, defaultProxy
}

// Enabled 建过代理池或者开了「只许走号的代理」：号的请求要先问线路。
func (r *Registry) Enabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pools) > 0 || r.settings.RequireBound
}

// Settings 当前设置（已补默认值）。
func (r *Registry) Settings() Settings {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.settings
	s.ExitIPURLs = slices.Clone(s.ExitIPURLs)
	return s
}

// UpdateSettings 改设置，立即生效（RequireBound 改了会马上重算哪些号不能出站）。
func (r *Registry) UpdateSettings(s Settings) (Settings, error) {
	s = s.normalized()
	for _, raw := range s.ExitIPURLs {
		if u, err := url.Parse(raw); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Settings{}, fmt.Errorf("查出口 IP 的地址 %q 不对：要写 http:// 或 https:// 开头的完整地址", raw)
		}
	}
	err := r.change(func(t *tx) error {
		r.settings = s
		return nil
	})
	return s, err
}

// PoolInput 新建、修改池的参数。DefaultCN、DefaultGlobal 设为这个域新号的默认池（别的池随之取消）；
// AdoptUnbound 设默认池时把这个域现有没绑代理的号也放进来。
type PoolInput struct {
	Name          string `json:"name"`
	MaxPerProxy   int    `json:"max_per_proxy"`
	DefaultCN     bool   `json:"default_cn"`
	DefaultGlobal bool   `json:"default_global"`
	AdoptUnbound  bool   `json:"adopt_unbound"`
}

// ErrNotFound 池或代理不存在；ErrSave 写不进 proxies.json（这次改动已撤回）。
var (
	ErrNotFound = errors.New("不存在")
	ErrSave     = errors.New("写不进代理池文件")
)

// CreatePool 新建池，返回池 ID。
func (r *Registry) CreatePool(in PoolInput) (string, error) {
	var id string
	err := r.change(func(t *tx) error {
		pl := &Pool{ID: newID()}
		if err := r.applyPoolLocked(t, pl, in); err != nil {
			return err
		}
		r.pools = append(r.pools, pl)
		id = pl.ID
		r.adoptLocked(t, pl, in)
		return nil
	})
	return id, err
}

// UpdatePool 改池的名字、上限、默认池。上限调高后，等代理的号马上补上。
func (r *Registry) UpdatePool(id string, in PoolInput) error {
	return r.change(func(t *tx) error {
		pl := r.poolLocked(id)
		if pl == nil {
			return fmt.Errorf("池 %s %w", id, ErrNotFound)
		}
		if err := r.applyPoolLocked(t, pl, in); err != nil {
			return err
		}
		r.adoptLocked(t, pl, in)
		return nil
	})
}

// applyPoolLocked 校验并写入池的参数。
func (r *Registry) applyPoolLocked(t *tx, pl *Pool, in PoolInput) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return errors.New("池名字不能为空")
	}
	for _, o := range r.pools {
		if o != pl && o.Name == name {
			return fmt.Errorf("已经有叫「%s」的池", name)
		}
	}
	if in.MaxPerProxy < 0 {
		return errors.New("每个代理最多几个号不能是负数（0 = 不限）")
	}
	pl.Name, pl.MaxPerProxy = name, in.MaxPerProxy
	pl.DefaultFor = nil
	for realm, on := range map[string]bool{"cn": in.DefaultCN, "global": in.DefaultGlobal} {
		if !on {
			continue
		}
		for _, o := range r.pools {
			o.DefaultFor = slices.DeleteFunc(o.DefaultFor, func(s string) bool { return s == realm })
		}
		pl.DefaultFor = append(pl.DefaultFor, realm)
	}
	slices.Sort(pl.DefaultFor)
	t.saveFile = true
	return nil
}

// adoptLocked 设默认池时按需把这个域现有没绑代理的号放进来；上限可能变了，顺手补等代理的号。
func (r *Registry) adoptLocked(t *tx, pl *Pool, in PoolInput) {
	if in.AdoptUnbound {
		for _, acc := range r.sortedAccountsLocked() {
			if acc.b.Empty() && slices.Contains(pl.DefaultFor, acc.realm) {
				r.assignLocked(t, acc, pl.ID)
			}
		}
	}
	r.fillWaitingLocked(t)
}

// DeletePool 删池：池里还有代理就不让删；绑着这个池在等代理的号解绑。
func (r *Registry) DeletePool(id string) error {
	return r.change(func(t *tx) error {
		pl := r.poolLocked(id)
		if pl == nil {
			return fmt.Errorf("池 %s %w", id, ErrNotFound)
		}
		for _, px := range r.proxies {
			if px.Pool == id {
				return fmt.Errorf("池「%s」里还有代理，先删掉或移到别的池", pl.Name)
			}
		}
		r.pools = slices.DeleteFunc(r.pools, func(p *Pool) bool { return p.ID == id })
		for _, acc := range r.accounts {
			if acc.b.Pool == id {
				t.bind(acc, auth.ProxyBinding{})
			}
		}
		t.saveFile = true
		return nil
	})
}

// ProxyInput 修改代理的参数。URL 为空或等于打码后的地址就不改；ExpiresAt 为 nil 而 ClearExpiry
// 为 true 时去掉到期日。
type ProxyInput struct {
	Name        string     `json:"name"`
	URL         string     `json:"url"`
	Pool        string     `json:"pool"`
	ExpiresAt   *time.Time `json:"expires_at"`
	ClearExpiry bool       `json:"clear_expiry"`
}

// UpdateProxy 改代理。换了池，绑在上面的号跟着换池；改了地址，健康状态清空重测。
func (r *Registry) UpdateProxy(id string, in ProxyInput) error {
	return r.change(func(t *tx) error {
		px := r.proxyLocked(id)
		if px == nil {
			return fmt.Errorf("代理 %s %w", id, ErrNotFound)
		}
		if name := strings.TrimSpace(in.Name); name != "" {
			px.Name = name
		}
		if u := strings.TrimSpace(in.URL); u != "" && u != Masked(px.URL) {
			p, err := parseOne(u, "http")
			if err != nil {
				return err
			}
			p.URL = keepPassword(p.URL, px.URL)
			if o := r.proxyByKeyLocked(dedupKey(p.URL)); o != nil && o != px {
				return fmt.Errorf("和已有的代理「%s」重复", o.Name)
			}
			if p.URL != px.URL {
				px.URL, px.Health = p.URL, Health{}
				r.due[px.ID] = true
				t.kick = true
			}
		}
		if in.Pool != "" && in.Pool != px.Pool {
			if r.poolLocked(in.Pool) == nil {
				return fmt.Errorf("池 %s %w", in.Pool, ErrNotFound)
			}
			px.Pool = in.Pool
			for _, acc := range r.accounts {
				if acc.b.ID == px.ID {
					t.bind(acc, auth.ProxyBinding{Pool: px.Pool, ID: px.ID, Manual: acc.b.Manual})
				}
			}
		}
		switch {
		case in.ExpiresAt != nil:
			e := *in.ExpiresAt
			px.ExpiresAt = &e
		case in.ClearExpiry:
			px.ExpiresAt = nil
		}
		t.saveFile = true
		r.fillWaitingLocked(t)
		return nil
	})
}

// DeleteProxies 删代理：绑在上面的号改绑到同池别的代理，没有空位就等代理。返回删了几个。
func (r *Registry) DeleteProxies(ids []string) (int, error) {
	n := 0
	err := r.change(func(t *tx) error {
		gone := map[string]bool{}
		for _, id := range ids {
			if r.proxyLocked(id) != nil {
				gone[id] = true
			}
		}
		n = r.removeProxiesLocked(t, gone)
		return nil
	})
	return n, err
}

// DeleteUnavailable 删掉所有不通或已到期的代理（导入后一键清理），返回删了几个。
func (r *Registry) DeleteUnavailable() (int, error) {
	n := 0
	err := r.change(func(t *tx) error {
		now := r.now()
		gone := map[string]bool{}
		for _, px := range r.proxies {
			if !r.usableLocked(px, now) {
				gone[px.ID] = true
			}
		}
		n = r.removeProxiesLocked(t, gone)
		return nil
	})
	return n, err
}

// removeProxiesLocked 删掉 gone 里的代理，绑在上面的号重新分配。
func (r *Registry) removeProxiesLocked(t *tx, gone map[string]bool) int {
	if len(gone) == 0 {
		return 0
	}
	r.proxies = slices.DeleteFunc(r.proxies, func(px *Proxy) bool { return gone[px.ID] })
	for id := range gone {
		delete(r.due, id)
	}
	for h, hold := range r.logins {
		if gone[hold.proxy] {
			delete(r.logins, h)
		}
	}
	for _, acc := range r.sortedAccountsLocked() {
		if gone[acc.b.ID] {
			t.bind(acc, auth.ProxyBinding{Pool: acc.b.Pool})
			r.assignLocked(t, acc, acc.b.Pool)
		}
	}
	t.saveFile = true
	return len(gone)
}

// tx 一次改动：要不要写 proxies.json、哪些号的绑定要写回凭证、要不要叫醒检查循环。
type tx struct {
	saveFile bool
	saves    map[string]*account
	kick     bool
}

// bind 改号的绑定（凭证文件在 change 结束时写回）。
func (t *tx) bind(acc *account, b auth.ProxyBinding) {
	if acc.b == b {
		return
	}
	acc.b = b
	t.touch(acc)
}

// touch 记下这个号的绑定要写回凭证（直接改了 acc.b 的地方用）。
func (t *tx) touch(acc *account) {
	if t.saves == nil {
		t.saves = map[string]*account{}
	}
	t.saves[acc.uid] = acc
}

// snapshot 改动前的状态，写盘失败时撤回。
type snapshot struct {
	settings Settings
	pools    []Pool
	proxies  []Proxy
	bindings map[string]auth.ProxyBinding
	logins   map[string]loginHold
}

func (r *Registry) snapshotLocked() snapshot {
	s := snapshot{settings: r.settings, bindings: map[string]auth.ProxyBinding{}, logins: map[string]loginHold{}}
	for _, pl := range r.pools {
		c := *pl
		c.DefaultFor = slices.Clone(pl.DefaultFor)
		s.pools = append(s.pools, c)
	}
	for _, px := range r.proxies {
		s.proxies = append(s.proxies, *px)
	}
	for uid, acc := range r.accounts {
		s.bindings[uid] = acc.b
	}
	for k, v := range r.logins {
		s.logins[k] = v
	}
	return s
}

func (r *Registry) restoreLocked(s snapshot) {
	r.settings = s.settings
	r.pools, r.proxies = nil, nil
	for i := range s.pools {
		pl := s.pools[i]
		r.pools = append(r.pools, &pl)
	}
	for i := range s.proxies {
		px := s.proxies[i]
		r.proxies = append(r.proxies, &px)
	}
	for uid, acc := range r.accounts {
		acc.b = s.bindings[uid]
	}
	r.logins = s.logins
}

// change 在锁里做一次改动：f 出错或写盘失败就整个撤回；成功后把改了绑定的号写回凭证文件，再把
// 不可用原因有变化的号推给号池（都在 mu 外做，见包注释）。
func (r *Registry) change(f func(t *tx) error) error {
	r.pushMu.Lock()
	defer r.pushMu.Unlock()
	r.mu.Lock()
	before := r.snapshotLocked()
	t := &tx{}
	err := f(t)
	if err == nil && t.saveFile {
		err = r.saveLocked()
	}
	if err != nil {
		r.restoreLocked(before)
		r.mu.Unlock()
		return err
	}
	gates := r.diffGatesLocked()
	hook, readOnly := r.onGate, r.readOnly
	r.mu.Unlock()

	if !readOnly {
		for _, acc := range t.saves {
			acc.a.SetProxyBinding(acc.b)
			if acc.a.FilePath == "" {
				continue
			}
			if err := acc.a.SaveAtomic(); err != nil {
				logf("proxy WARN 号 %s 的代理绑定写不进凭证文件（重启后按文件里的绑定）：%v", short(acc.uid), err)
			}
		}
	}
	if hook != nil {
		for _, g := range gates {
			hook(g.uid, g.reason)
		}
	}
	if t.kick {
		r.wake()
	}
	return nil
}

// saveLocked 写 proxies.json（0600，原子替换）。命令行工具（只读）不写。
func (r *Registry) saveLocked() error {
	if r.readOnly {
		return nil
	}
	raw, _ := json.MarshalIndent(file{Version: fileVersion, Settings: r.settings, Pools: r.pools,
		Proxies: r.proxies, DefaultExit: r.defaultExit}, "", "  ") // 字段都是可序列化的基本类型
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("%w %s：%w", ErrSave, r.path, err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return fmt.Errorf("%w %s：%w", ErrSave, r.path, err)
	}
	return nil
}

// wake 叫醒检查循环（Run）马上查一轮。
func (r *Registry) wake() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

func (r *Registry) poolLocked(id string) *Pool {
	for _, pl := range r.pools {
		if pl.ID == id {
			return pl
		}
	}
	return nil
}

func (r *Registry) proxyLocked(id string) *Proxy {
	for _, px := range r.proxies {
		if px.ID == id {
			return px
		}
	}
	return nil
}

// proxyByKeyLocked 按去重键找代理（同协议、同用户名、同主机端口算同一个）。
func (r *Registry) proxyByKeyLocked(key string) *Proxy {
	for _, px := range r.proxies {
		if dedupKey(px.URL) == key {
			return px
		}
	}
	return nil
}

// defaultPoolLocked 域 realm 的默认池，没有返回 nil。
func (r *Registry) defaultPoolLocked(realm string) *Pool {
	for _, pl := range r.pools {
		if slices.Contains(pl.DefaultFor, realm) {
			return pl
		}
	}
	return nil
}

// sortedAccountsLocked 号按 UID 排好，分配、日志的顺序固定。
func (r *Registry) sortedAccountsLocked() []*account {
	out := make([]*account, 0, len(r.accounts))
	for _, acc := range r.accounts {
		out = append(out, acc)
	}
	slices.SortFunc(out, func(a, b *account) int { return strings.Compare(a.uid, b.uid) })
	return out
}

// newID 随机 ID：10 个小写 base32 字符。
func newID() string {
	return strings.ToLower(rand.Text()[:10])
}

// short UID 取前 8 位，日志用。
func short(uid string) string {
	if len(uid) > 8 {
		return uid[:8]
	}
	return uid
}
