// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package proxies

import (
	"net/url"
	"slices"
	"time"
)

// exitChangedRecent 出口 IP 变化在多久以内算「最近变过」，面板上标出来。
const exitChangedRecent = 24 * time.Hour

// View 面板「代理」页的全部数据。代理地址都是打码的。
type View struct {
	Settings Settings    `json:"settings"`
	Pools    []PoolView  `json:"pools"`
	Proxies  []ProxyView `json:"proxies"`
	// DefaultExit 没绑代理的号的出口（服务器自己的 IP 或环境变量里的代理）。
	DefaultExit *Exit    `json:"default_exit,omitempty"`
	Warnings    Warnings `json:"warnings"`
}

// PoolView 池和它的统计。
type PoolView struct {
	Pool
	Proxies  int `json:"proxies"`
	Usable   int `json:"usable"`
	Accounts int `json:"accounts"`
	Waiting  int `json:"waiting"`
}

// ProxyView 代理和它的状态。
type ProxyView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	URL       string     `json:"url"`
	Scheme    string     `json:"scheme"`
	Host      string     `json:"host"`
	Pool      string     `json:"pool"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	Health    Health     `json:"health"`
	// Accounts 绑在上面的号；Full 绑满了；Over 超过上限（调低上限以后）。
	Accounts []string `json:"accounts"`
	Full     bool     `json:"full,omitempty"`
	Over     bool     `json:"over,omitempty"`
	// Usable 能用（没到期、没测出不通）；Expired 已到期；Expiring 快到期（ExpiryWarnDays 以内）。
	Usable   bool `json:"usable"`
	Expired  bool `json:"expired,omitempty"`
	Expiring bool `json:"expiring,omitempty"`
	// DupExit 出口 IP 一样的别的代理（名字）；SameAsDefault 出口 IP 和没绑代理的号一样（代理可能没起作用）；
	// ExitChanged 出口 IP 最近 24 小时变过。
	DupExit       []string `json:"dup_exit,omitempty"`
	SameAsDefault bool     `json:"same_as_default,omitempty"`
	ExitChanged   bool     `json:"exit_changed,omitempty"`
}

// Warnings 要在总览上提醒的数。
type Warnings struct {
	Down     int `json:"down"`
	Expired  int `json:"expired"`
	Expiring int `json:"expiring"`
	Waiting  int `json:"waiting"`
	Blocked  int `json:"blocked"`
}

// AccountView 一个号的代理情况，给账号池卡片、详情用。
type AccountView struct {
	Pool      string `json:"pool,omitempty"`
	PoolName  string `json:"pool_name,omitempty"`
	Proxy     string `json:"proxy,omitempty"`
	ProxyName string `json:"proxy_name,omitempty"`
	Manual    bool   `json:"manual,omitempty"`
	ExitIP    string `json:"exit_ip,omitempty"`
	ExitGeo   string `json:"exit_geo,omitempty"`
	// Waiting 在等代理；Blocked 现在不能出站的原因。
	Waiting bool   `json:"waiting,omitempty"`
	Blocked string `json:"blocked,omitempty"`
}

// View 面板「代理」页的数据。
func (r *Registry) View() View {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	s := r.settings
	s.ExitIPURLs = slices.Clone(s.ExitIPURLs)
	v := View{Settings: s, Pools: []PoolView{}, Proxies: []ProxyView{}}
	if r.defaultExit != nil {
		e := *r.defaultExit
		v.DefaultExit = &e
	}
	load := r.loadLocked(now)
	byIP := map[string][]string{}
	for _, px := range r.proxies {
		if e := px.Health.Exit; e != nil && e.IP != "" {
			byIP[e.IP] = append(byIP[e.IP], px.Name)
		}
	}
	for _, pl := range r.pools {
		pv := PoolView{Pool: *pl}
		pv.DefaultFor = slices.Clone(pl.DefaultFor)
		for _, px := range r.proxies {
			if px.Pool == pl.ID {
				pv.Proxies++
				if r.usableLocked(px, now) {
					pv.Usable++
				}
			}
		}
		for _, acc := range r.accounts {
			if acc.b.Pool == pl.ID {
				pv.Accounts++
				if acc.b.ID == "" {
					pv.Waiting++
				}
			}
		}
		v.Pools = append(v.Pools, pv)
		v.Warnings.Waiting += pv.Waiting
	}
	warn := time.Duration(s.ExpiryWarnDays) * 24 * time.Hour
	for _, px := range r.proxies {
		pv := ProxyView{ID: px.ID, Name: px.Name, URL: Masked(px.URL), Pool: px.Pool, CreatedAt: px.CreatedAt,
			Health: px.Health, Accounts: r.boundLocked(px.ID), Usable: r.usableLocked(px, now), Expired: expired(px, now)}
		if pv.Accounts == nil {
			pv.Accounts = []string{}
		}
		if u, err := url.Parse(px.URL); err == nil {
			pv.Scheme, pv.Host = u.Scheme, u.Host
		}
		if px.Health.Exit != nil {
			e := *px.Health.Exit
			pv.Health.Exit = &e
			for _, name := range byIP[e.IP] {
				if name != px.Name {
					pv.DupExit = append(pv.DupExit, name)
				}
			}
			pv.SameAsDefault = e.IP != "" && r.defaultExit != nil && r.defaultExit.IP == e.IP
		}
		if px.ExpiresAt != nil {
			e := *px.ExpiresAt
			pv.ExpiresAt = &e
			pv.Expiring = !pv.Expired && e.Sub(now) <= warn
		}
		pv.ExitChanged = !px.Health.ExitChangedAt.IsZero() && now.Sub(px.Health.ExitChangedAt) < exitChangedRecent
		if pl := r.poolLocked(px.Pool); pl != nil && pl.MaxPerProxy > 0 {
			pv.Full = load[px.ID] >= pl.MaxPerProxy
			pv.Over = len(pv.Accounts) > pl.MaxPerProxy
		}
		switch {
		case pv.Expired:
			v.Warnings.Expired++
		case px.Health.State == "down":
			v.Warnings.Down++
		}
		if pv.Expiring {
			v.Warnings.Expiring++
		}
		v.Proxies = append(v.Proxies, pv)
	}
	v.Warnings.Blocked = len(r.gates)
	return v
}

// AccountViews 每个号的代理情况（没绑也没被拦的号不在里面）。
func (r *Registry) AccountViews() map[string]AccountView {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	out := map[string]AccountView{}
	for uid, acc := range r.accounts {
		v := AccountView{Pool: acc.b.Pool, Proxy: acc.b.ID, Manual: acc.b.Manual, Blocked: r.gateLocked(acc, now)}
		if pl := r.poolLocked(acc.b.Pool); pl != nil {
			v.PoolName = pl.Name
		}
		if px := r.proxyLocked(acc.b.ID); px != nil {
			v.ProxyName = px.Name
			if e := px.Health.Exit; e != nil {
				v.ExitIP, v.ExitGeo = e.IP, e.Geo()
			}
		}
		v.Waiting = acc.b.Pool != "" && acc.b.ID == ""
		if v != (AccountView{}) {
			out[uid] = v
		}
	}
	return out
}
