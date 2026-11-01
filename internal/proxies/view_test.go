// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package proxies

import (
	"slices"
	"testing"
	"time"
)

// TestView 「代理」页的统计和各种标记：打码地址、绑满、超上限、出口重复、和服务器出口一样、出口最近变过、
// 快到期、已到期、总览提醒数；账号池卡片用的每个号的代理情况。
func TestView(t *testing.T) {
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "一", MaxPerProxy: 2, DefaultCN: true})
	p2 := tr.pool(t, PoolInput{Name: "二"})
	a := tr.add(t, p1, "http://u:pw@h1:1 甲", "socks5://h2:1 乙")
	tr.add(t, p2, "http://h3:1 丙", "http://h4:1 丁")
	for _, uid := range []string{"u1", "u2", "u3", "u4", "u5"} {
		tr.Track(tr.acct(t, uid, "cn"), false)
	}
	for uid, px := range map[string]string{"u1": a[0], "u2": a[0], "u3": a[1]} {
		if _, err := tr.Bind([]string{uid}, BindTarget{Proxy: px}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.UpdatePool(p1, PoolInput{Name: "一", MaxPerProxy: 1, DefaultCN: true}); err != nil {
		t.Fatal(err)
	}
	if res, _ := tr.Bind([]string{"u4"}, BindTarget{Pool: p1}); !res[0].Waiting {
		t.Fatal("u4 应在等")
	}
	tr.mu.Lock()
	ps := tr.proxies
	ps[0].Health = Health{State: "ok", Exit: &Exit{IP: "9.9.9.9", Country: "中国", Region: "香港", ISP: "某运营商"}, ExitChangedAt: t0.Add(-time.Hour)}
	ps[1].Health = Health{State: "down", DownSince: t0.Add(-10 * time.Minute), Exit: &Exit{IP: "1.1.1.1"}}
	soon, past := t0.Add(48*time.Hour), t0.Add(-time.Hour)
	ps[2].Health, ps[2].ExpiresAt = Health{Exit: &Exit{IP: "9.9.9.9"}}, &soon
	ps[3].Health, ps[3].ExpiresAt = Health{ExitChangedAt: t0.Add(-25 * time.Hour)}, &past
	tr.defaultExit = &Exit{IP: "1.1.1.1"}
	tr.mu.Unlock()
	// 改一下设置，让门控重算一遍（上面直接改的健康状态不会自己推送）。
	if _, err := tr.UpdateSettings(Settings{ExitIPURLs: []string{"http://exit.example"}}); err != nil {
		t.Fatal(err)
	}

	v := tr.View()
	if v.DefaultExit.IP != "1.1.1.1" || v.Settings.ExitIPURLs[0] != "http://exit.example" {
		t.Fatalf("服务器出口、设置: %+v %+v", v.DefaultExit, v.Settings)
	}
	if len(v.Pools) != 2 || v.Pools[0].DefaultFor[0] != "cn" ||
		v.Pools[0].Proxies != 2 || v.Pools[0].Usable != 1 || v.Pools[0].Accounts != 4 || v.Pools[0].Waiting != 1 ||
		v.Pools[1].Proxies != 2 || v.Pools[1].Usable != 1 || v.Pools[1].Accounts != 0 {
		t.Fatalf("池统计 = %+v", v.Pools)
	}
	jia, yi, bing, ding := v.Proxies[0], v.Proxies[1], v.Proxies[2], v.Proxies[3]
	if jia.URL != "http://u:***@h1:1" || jia.Scheme != "http" || jia.Host != "h1:1" || !slices.Equal(jia.Accounts, []string{"u1", "u2"}) ||
		!jia.Full || !jia.Over || !jia.Usable || !slices.Equal(jia.DupExit, []string{"丙"}) || jia.SameAsDefault || !jia.ExitChanged {
		t.Fatalf("甲 = %+v", jia)
	}
	if yi.Scheme != "socks5" || !yi.Full || yi.Over || yi.Usable || !yi.SameAsDefault || yi.DupExit != nil {
		t.Fatalf("乙 = %+v", yi)
	}
	if bing.Accounts == nil || len(bing.Accounts) != 0 || bing.Full || !bing.Usable || !bing.Expiring || bing.Expired ||
		!slices.Equal(bing.DupExit, []string{"甲"}) {
		t.Fatalf("丙 = %+v", bing)
	}
	if !ding.Expired || ding.Expiring || ding.Usable || ding.ExitChanged {
		t.Fatalf("丁 = %+v", ding)
	}
	if v.Warnings != (Warnings{Down: 1, Expired: 1, Expiring: 1, Waiting: 1, Blocked: 2}) {
		t.Fatalf("提醒 = %+v", v.Warnings)
	}
	// 视图是拷贝，改了不影响登记表。
	v.Proxies[0].Health.Exit.IP = "x"
	v.DefaultExit.IP = "x"
	v.Settings.ExitIPURLs[0] = "x"
	if w := tr.View(); w.Proxies[0].Health.Exit.IP != "9.9.9.9" || w.DefaultExit.IP != "1.1.1.1" || w.Settings.ExitIPURLs[0] == "x" {
		t.Fatal("视图应是拷贝")
	}

	av := tr.AccountViews()
	if len(av) != 4 {
		t.Fatalf("没绑也没被拦的 u5 不在里面: %+v", av)
	}
	if u1 := av["u1"]; u1 != (AccountView{Pool: p1, PoolName: "一", Proxy: a[0], ProxyName: "甲", Manual: true,
		ExitIP: "9.9.9.9", ExitGeo: "中国 香港 · 某运营商"}) {
		t.Fatalf("u1 = %+v", u1)
	}
	if u3 := av["u3"]; u3.ExitIP != "1.1.1.1" || u3.ExitGeo != "" || u3.Blocked != "代理「乙」连不上（11-01 11:50 起）" {
		t.Fatalf("u3 = %+v", u3)
	}
	if u4 := av["u4"]; !u4.Waiting || u4.PoolName != "一" || u4.Proxy != "" || u4.Blocked != "等代理：池「一」里没有可用的空位" {
		t.Fatalf("u4 = %+v", u4)
	}
}
