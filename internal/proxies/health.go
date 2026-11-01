// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package proxies

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
)

// tickInterval 检查循环多久醒一次（到点的代理才真去测）。
const tickInterval = 30 * time.Second

// checkConcurrency 一轮最多同时测几个代理；testTimeout 测一个代理（连通加出口 IP）最多等多久。
const (
	checkConcurrency = 8
	testTimeout      = 30 * time.Second
)

// Run 检查循环：到点测连通、查出口 IP，处理到期、长时间不通的改绑、等代理的补位，直到 ctx 结束。
// 命令行工具不跑它。
func (r *Registry) Run(ctx context.Context) {
	for {
		r.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(tickInterval):
		case <-r.kick:
		}
	}
}

// TestAll 下一轮把所有代理都重测一遍（连通和出口 IP），马上开始。
func (r *Registry) TestAll() int {
	r.mu.Lock()
	for _, px := range r.proxies {
		r.due[px.ID] = true
	}
	n := len(r.proxies)
	r.mu.Unlock()
	r.wake()
	return n
}

// job 一个要测的代理：测哪些官方入口，查不查出口 IP。
type job struct {
	id, url string
	hosts   []string
	exit    bool
	// results
	latency time.Duration
	err     error
	tested  bool
	exitRes *Exit
}

// tick 查一轮：挑出到点的代理（锁外测），再把结果、到期、改绑、补位一次写进去。
func (r *Registry) tick(ctx context.Context) {
	now := r.now()
	r.mu.Lock()
	s, tester, urls, defProxy := r.settings, r.tester, slices.Clone(r.settings.ExitIPURLs), r.defaultProxy
	var jobs []*job
	for _, px := range r.proxies {
		if expired(px, now) {
			continue // 到期的不测，续费改了日期再测
		}
		due := r.due[px.ID]
		j := &job{id: px.ID, url: px.URL, hosts: r.hostsLocked(px)}
		testDue := due || now.Sub(px.Health.CheckedAt) >= time.Duration(s.HealthMinutes)*time.Minute
		j.exit = due || px.Health.Exit == nil || now.Sub(px.Health.Exit.CheckedAt) >= time.Duration(s.ExitMinutes)*time.Minute
		if (testDue && tester != nil) || j.exit {
			j.tested = testDue && tester != nil
			jobs = append(jobs, j)
		}
	}
	clear(r.due)
	defDue := len(r.proxies) > 0 && (r.defaultExit == nil ||
		now.Sub(r.defaultExit.CheckedAt) >= time.Duration(s.ExitMinutes)*time.Minute)
	r.mu.Unlock()

	r.runJobs(ctx, jobs, tester, urls)
	var defExit *Exit
	if defDue {
		defExit = r.probe(ctx, defProxy, urls)
	}
	if ctx.Err() != nil {
		// 停机打断了检查：测到一半的结果不算，不然代理会被记成不通写进文件，重启后绑在上面的号要等
		// 下一轮检查才放开。
		return
	}

	_ = r.change(func(t *tx) error {
		now := r.now()
		for _, j := range jobs {
			px := r.proxyLocked(j.id)
			if px == nil || px.URL != j.url {
				continue // 测的时候被删了或改了地址，结果作废
			}
			if j.tested {
				r.applyTestLocked(px, j.latency, j.err, now)
			}
			if j.exitRes != nil {
				r.applyExitLocked(px, j.exitRes, now)
			}
			t.saveFile = true
		}
		if defExit != nil {
			r.defaultExit = mergeExit(r.defaultExit, defExit, now)
			t.saveFile = true
		}
		for k, h := range r.logins {
			if !now.Before(h.until) {
				delete(r.logins, k) // 先清过期的登录占位，空出来的位子下面就补给等代理的号
			}
		}
		r.rebindLocked(t, now)
		r.fillWaitingLocked(t)
		return nil
	})
}

// runJobs 并发测（最多 checkConcurrency 个），结果写回 job。
func (r *Registry) runJobs(ctx context.Context, jobs []*job, tester Tester, urls []string) {
	sem := make(chan struct{}, checkConcurrency)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			cctx, cancel := context.WithTimeout(ctx, testTimeout)
			defer cancel()
			if j.tested {
				j.latency, j.err = testHosts(cctx, tester, j.url, j.hosts)
			}
			if j.exit {
				j.exitRes = r.probe(cctx, j.url, urls)
			}
		}()
	}
	wg.Wait()
}

// testHosts 依次测各个官方入口，有一个不通就算不通；返回第一个入口用的时间。
func testHosts(ctx context.Context, tester Tester, url string, hosts []string) (time.Duration, error) {
	var first time.Duration
	for i, h := range hosts {
		d, err := tester(ctx, url, h)
		if err != nil {
			return 0, fmt.Errorf("连 %s：%w", h, err)
		}
		if i == 0 {
			first = d
		}
	}
	return first, nil
}

// hostsLocked 测这个代理要连哪些官方入口：绑在上面的号是哪个域就测哪个，没有号按池的默认域，再没有
// 测国内版。
func (r *Registry) hostsLocked(px *Proxy) []string {
	var realms []string
	for _, acc := range r.accounts {
		if acc.b.ID == px.ID && !slices.Contains(realms, acc.realm) {
			realms = append(realms, acc.realm)
		}
	}
	if len(realms) == 0 {
		if pl := r.poolLocked(px.Pool); pl != nil {
			realms = slices.Clone(pl.DefaultFor)
		}
	}
	if len(realms) == 0 {
		realms = []string{"cn"}
	}
	slices.Sort(realms)
	hosts := make([]string, 0, len(realms))
	for _, realm := range realms {
		h := r.hosts[realm]
		if h == "" {
			h = map[string]string{"cn": "www.workbuddy.cn", "global": "www.workbuddy.ai"}[realm]
		}
		if h != "" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// TestNow 马上测一个代理（面板里点「测试」），等测完再返回。
func (r *Registry) TestNow(ctx context.Context, id string) error {
	r.mu.Lock()
	px := r.proxyLocked(id)
	if px == nil {
		r.mu.Unlock()
		return fmt.Errorf("代理 %s %w", id, ErrNotFound)
	}
	j := &job{id: id, url: px.URL, hosts: r.hostsLocked(px), exit: true, tested: r.tester != nil}
	tester, urls := r.tester, slices.Clone(r.settings.ExitIPURLs)
	r.mu.Unlock()
	r.runJobs(ctx, []*job{j}, tester, urls)
	if err := ctx.Err(); err != nil {
		return err // 被打断了：结果不算（同 tick）
	}
	return r.change(func(t *tx) error {
		now := r.now()
		px := r.proxyLocked(id)
		if px == nil || px.URL != j.url {
			return nil
		}
		if j.tested {
			r.applyTestLocked(px, j.latency, j.err, now)
		}
		r.applyExitLocked(px, j.exitRes, now)
		r.fillWaitingLocked(t)
		t.saveFile = true
		return nil
	})
}

// ReportProxy 真实请求经代理的结果：err 是连代理这一步的错误（连不上代理、代理认证失败、代理拒绝
// 转发），nil 是连上了。连不上就马上标不可用，绑在上面的号暂停选号；连上了就标可用。
func (r *Registry) ReportProxy(id string, err error) {
	r.mu.Lock()
	px := r.proxyLocked(id)
	same := px == nil || (err == nil && px.Health.State == "ok") || (err != nil && px.Health.State == "down")
	r.mu.Unlock()
	if same {
		return
	}
	_ = r.change(func(t *tx) error {
		// 两次加锁之间代理可能被删了。
		if px := r.proxyLocked(id); px != nil {
			lat := time.Duration(px.Health.LatencyMs) * time.Millisecond
			r.applyTestLocked(px, lat, err, r.now())
			if err == nil {
				r.fillWaitingLocked(t)
			}
			t.saveFile = true
		}
		return nil
	})
}

// applyTestLocked 记一次连通结果；通不通变了记日志。
func (r *Registry) applyTestLocked(px *Proxy, lat time.Duration, err error, now time.Time) {
	h := &px.Health
	h.CheckedAt = now
	n := len(r.boundLocked(px.ID))
	if err == nil {
		if h.State == "down" {
			logf("proxy 代理「%s」恢复了，绑在上面的 %d 个号重新接请求", px.Name, n)
		}
		h.State, h.LatencyMs, h.Error, h.DownSince = "ok", lat.Milliseconds(), "", time.Time{}
		return
	}
	if h.State != "down" {
		h.DownSince = now
		logf("proxy WARN 代理「%s」连不上，绑在上面的 %d 个号暂停选号：%s", px.Name, n, describe(err))
	}
	h.State, h.LatencyMs, h.Error = "down", 0, describe(err)
}

// applyExitLocked 记一次出口 IP；IP 变了记日志（动态 IP 的代理会让号的出口一直变；日志不写 IP，面板上看）。
func (r *Registry) applyExitLocked(px *Proxy, e *Exit, now time.Time) {
	old := px.Health.Exit
	if old != nil && old.IP != "" && e.IP != "" && old.IP != e.IP {
		px.Health.PrevExitIP, px.Health.ExitChangedAt = old.IP, now
		logf("proxy WARN 代理「%s」的出口 IP 变了（动态 IP 的代理会让绑在上面的号换 IP）", px.Name)
	}
	px.Health.Exit = mergeExit(old, e, now)
}

// mergeExit 这次没查到就保留上次的 IP 和地区，只更新时间和原因。
func mergeExit(old, e *Exit, now time.Time) *Exit {
	out := *e
	out.CheckedAt = now
	if out.IP == "" && old != nil {
		out.IP, out.Country, out.Region, out.City, out.ISP, out.Source = old.IP, old.Country, old.Region, old.City, old.ISP, old.Source
	}
	return &out
}

// boundLocked 绑在代理 id 上的号，按 UID 排好。
func (r *Registry) boundLocked(id string) []string {
	var out []string
	for _, acc := range r.accounts {
		if acc.b.ID == id {
			out = append(out, acc.uid)
		}
	}
	slices.Sort(out)
	return out
}

// rebindLocked 连续不可用（不通或到期）超过 RebindMinutes 的代理，把绑在上面的号改绑到同池可用的、
// 绑号最少的代理；同池没有空位就继续等。
func (r *Registry) rebindLocked(t *tx, now time.Time) {
	after := time.Duration(r.settings.RebindMinutes) * time.Minute
	for _, px := range r.proxies {
		since := unavailableSince(px, now)
		if since.IsZero() || now.Sub(since) < after {
			continue
		}
		moved := map[string][]string{}
		for _, acc := range r.sortedAccountsLocked() {
			if acc.b.ID != px.ID {
				continue
			}
			np := r.pickLocked(px.Pool, now)
			if np == nil {
				break
			}
			t.bind(acc, auth.ProxyBinding{Pool: np.Pool, ID: np.ID})
			moved[np.Name] = append(moved[np.Name], acc.uid)
		}
		for _, name := range sortedKeys(moved) {
			logf("proxy 代理「%s」连续不可用超过 %d 分钟，号 %s 改绑到「%s」", px.Name, r.settings.RebindMinutes, shortList(moved[name]), name)
		}
	}
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// describe 把错误压成一行（多行的上游错误只留第一行），IP 遮掉（logfmt.MaskIPs）：会进日志。
func describe(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return logfmt.MaskIPs(s)
}
