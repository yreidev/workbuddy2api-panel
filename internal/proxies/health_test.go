// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package proxies

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTick 一轮检查：到点才测、按绑定号的域测入口、不通就拦号并记日志、恢复了放开、出口 IP 变了记下来、
// 这次没查到保留上次的 IP、到期的不测、过期的登录占位清掉。
func TestTick(t *testing.T) {
	logs := captureLog(t)
	tr := newReg(t)
	ctx := context.Background()
	tr.tick(ctx)
	if tr.View().DefaultExit != nil {
		t.Fatal("没有代理时不查服务器出口")
	}
	p1 := tr.pool(t, PoolInput{Name: "池", DefaultGlobal: true})
	ids := tr.add(t, p1, "http://h1:1 甲", "http://h2:1 乙", "http://h3:1 丙")
	tr.Track(tr.acct(t, "u1", "cn"), false)
	tr.Track(tr.acct(t, "u2", "global"), false)
	if _, err := tr.Bind([]string{"u1", "u2"}, BindTarget{Proxy: ids[1]}); err != nil {
		t.Fatal(err)
	}
	past := t0.Add(-time.Hour)
	if err := tr.UpdateProxy(ids[2], ProxyInput{ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tr.HoldForLogin(p1, "global"); err != nil {
		t.Fatal(err)
	}
	tr.setFail("http://h2:1", true)
	tr.setExit("http://h1:1", "9.9.9.9")
	tr.setExit("", "1.1.1.1")

	tr.tick(ctx)
	// 甲没绑号按池的默认域测，乙绑了两个域的号、第一个入口就不通，丙到期了不测。
	if got := tr.takeTested(); !slices.Equal(got, []string{"http://h1:1@global.example", "http://h2:1@cn.example"}) {
		t.Fatalf("测了 %v", got)
	}
	v := tr.View()
	jia, yi := v.Proxies[0].Health, v.Proxies[1].Health
	if jia.State != "ok" || jia.LatencyMs != 120 || jia.Exit.IP != "9.9.9.9" || !jia.CheckedAt.Equal(t0) {
		t.Fatalf("甲 = %+v", jia)
	}
	if yi.State != "down" || yi.Error != "连 cn.example：连不上代理" || !yi.DownSince.Equal(t0) || yi.Exit.Error != "查不到" {
		t.Fatalf("乙 = %+v", yi)
	}
	if v.DefaultExit == nil || v.DefaultExit.IP != "1.1.1.1" {
		t.Fatalf("服务器出口 = %+v", v.DefaultExit)
	}
	if tr.gate("u1") != "代理「乙」连不上（11-01 12:00 起）" || tr.gate("u2") == "" {
		t.Fatalf("不通的代理上的号应推给号池: %q", tr.gate("u1"))
	}
	if !strings.Contains(logs.String(), "proxy WARN 代理「乙」连不上，绑在上面的 2 个号暂停选号：连 cn.example：连不上代理") {
		t.Fatalf("日志:\n%s", logs)
	}

	tr.advance(time.Minute)
	tr.tick(ctx)
	if got := tr.takeTested(); len(got) != 0 {
		t.Fatalf("没到点不测: %v", got)
	}

	tr.advance(4 * time.Minute)
	tr.setFail("http://h2:1", false)
	tr.tick(ctx)
	if got := tr.takeTested(); !slices.Equal(got, []string{"http://h1:1@global.example", "http://h2:1@cn.example", "http://h2:1@global.example"}) {
		t.Fatalf("5 分钟到点再测: %v", got)
	}
	if tr.gate("u1") != "" || tr.View().Proxies[1].Health.DownSince != (time.Time{}) {
		t.Fatal("恢复了应放开")
	}
	if !strings.Contains(logs.String(), "proxy 代理「乙」恢复了，绑在上面的 2 个号重新接请求") {
		t.Fatalf("日志:\n%s", logs)
	}

	tr.advance(56 * time.Minute)
	tr.setExit("http://h1:1", "8.8.8.8")
	tr.delExit("")
	tr.tick(ctx)
	v = tr.View()
	if jia := v.Proxies[0].Health; jia.Exit.IP != "8.8.8.8" || jia.PrevExitIP != "9.9.9.9" || !jia.ExitChangedAt.Equal(t0.Add(time.Hour+time.Minute)) {
		t.Fatalf("出口 IP 变了: %+v", jia)
	}
	if !strings.Contains(logs.String(), "proxy WARN 代理「甲」的出口 IP 变了（") || strings.Contains(logs.String(), "8.8.8.8") {
		t.Fatalf("日志:\n%s", logs)
	}
	if e := v.DefaultExit; e.IP != "1.1.1.1" || e.Error != "查不到" || e.Country != "中国" {
		t.Fatalf("这次没查到应保留上次的: %+v", e)
	}
	if len(tr.logins) != 0 {
		t.Fatal("过期的登录占位应清掉")
	}
}

// TestRebind 连续不可用（不通、到期）超过 30 分钟，号改绑到同池绑号最少的可用代理；同池没空位就继续等。
func TestRebind(t *testing.T) {
	logs := captureLog(t)
	tr := newReg(t)
	ctx := context.Background()
	big, small := tr.pool(t, PoolInput{Name: "大池"}), tr.pool(t, PoolInput{Name: "小池", MaxPerProxy: 1})
	a := tr.add(t, big, "http://a:1 A", "http://e:1 E")
	d := tr.add(t, small, "http://d:1 D", "http://f:1 F")
	for _, uid := range []string{"u1", "u2", "u3", "u4", "u5"} {
		tr.Track(tr.acct(t, uid, "cn"), false)
	}
	for uid, px := range map[string]string{"u1": a[0], "u2": a[0], "u3": a[1], "u4": d[0], "u5": d[1]} {
		if _, err := tr.Bind([]string{uid}, BindTarget{Proxy: px}); err != nil {
			t.Fatal(err)
		}
	}
	expire := t0.Add(-time.Minute)
	if err := tr.UpdateProxy(a[1], ProxyInput{ExpiresAt: &expire}); err != nil {
		t.Fatal(err)
	}
	tr.add(t, big, "http://b:1 B", "http://c:1 C")
	tr.setFail("http://a:1", true)
	tr.setFail("http://d:1", true)
	tr.tick(ctx)
	if tr.Binding("u1").ID != a[0] || tr.Binding("u3").ID != a[1] {
		t.Fatal("不到 30 分钟不改绑")
	}

	tr.advance(31 * time.Minute)
	tr.tick(ctx)
	ids := map[string]string{}
	for _, px := range tr.View().Proxies {
		ids[px.Name] = px.ID
	}
	// 改绑过去的按池分配，不再算单独指定。
	for uid, want := range map[string]string{"u1": "B", "u2": "C", "u3": "B", "u4": "D", "u5": "F"} {
		if b := tr.Binding(uid); b.ID != ids[want] || b.Manual != (want == "D" || want == "F") {
			t.Errorf("%s 应在 %s: %+v", uid, want, b)
		}
	}
	for _, line := range []string{
		"proxy 代理「A」连续不可用超过 30 分钟，号 u1 改绑到「B」",
		"proxy 代理「A」连续不可用超过 30 分钟，号 u2 改绑到「C」",
		"proxy 代理「E」连续不可用超过 30 分钟，号 u3 改绑到「B」",
	} {
		if !strings.Contains(logs.String(), line) {
			t.Errorf("缺日志 %q:\n%s", line, logs)
		}
	}
	if strings.Contains(logs.String(), "「D」连续不可用") {
		t.Fatal("小池没空位，u4 应继续等 D")
	}
	if tr.gate("u4") == "" || tr.gate("u1") != "" {
		t.Fatal("改绑后应放开，没改绑的继续拦")
	}
}

// TestTickDiscard 测的时候代理被改了地址或删了，这次结果作废；没配入口时按官方域名测。
func TestTickDiscard(t *testing.T) {
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "池", DefaultGlobal: true})
	p2 := tr.pool(t, PoolInput{Name: "无默认"})
	ids := tr.add(t, p1, "http://h1:1", "http://h2:1")
	tr.add(t, p2, "http://h3:1")
	var mu sync.Mutex
	var hosts []string
	tr.SetProbes(func(_ context.Context, rawURL, host string) (time.Duration, error) {
		mu.Lock()
		hosts = append(hosts, host)
		mu.Unlock()
		switch rawURL {
		case "http://h1:1":
			if err := tr.UpdateProxy(ids[0], ProxyInput{URL: "http://h9:1"}); err != nil {
				t.Error(err)
			}
		case "http://h2:1":
			if _, err := tr.DeleteProxies([]string{ids[1]}); err != nil {
				t.Error(err)
			}
		}
		return time.Millisecond, nil
	}, nil, "")
	tr.tick(context.Background())
	v := tr.View()
	if len(v.Proxies) != 2 || v.Proxies[0].Health.State != "" || v.Proxies[0].Health.Exit != nil || v.Proxies[1].Health.State != "ok" {
		t.Fatalf("改了地址、删了的结果应作废: %+v", v.Proxies)
	}
	tr.mu.Lock()
	due := tr.due[ids[0]]
	tr.mu.Unlock()
	if !due {
		t.Fatal("改了地址应排进下一轮")
	}
	slices.Sort(hosts)
	if !slices.Equal(hosts, []string{"www.workbuddy.ai", "www.workbuddy.ai", "www.workbuddy.cn"}) {
		t.Fatalf("没配入口按官方域名: %v", hosts)
	}
}

// TestTestNow 面板里点「测试」：马上测完；没装测连通的函数只查出口；测的时候被删了不报错。
func TestTestNow(t *testing.T) {
	tr := newReg(t)
	ctx := context.Background()
	p1 := tr.pool(t, PoolInput{Name: "池", MaxPerProxy: 1})
	ids := tr.add(t, p1, "http://h1:1 甲", "http://h2:1 乙")
	if err := tr.TestNow(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在: %v", err)
	}
	tr.setExit("http://h1:1", "9.9.9.9")
	tr.setFail("http://h1:1", true)
	tr.ReportProxy(ids[0], errTest)
	tr.Track(tr.acct(t, "u1", "cn"), false)
	tr.Track(tr.acct(t, "u2", "cn"), false)
	if _, err := tr.Bind([]string{"u1", "u2"}, BindTarget{Pool: p1}); err != nil {
		t.Fatal(err)
	}
	if tr.Binding("u2").ID != "" {
		t.Fatal("甲不通、乙被 u1 占了，u2 应在等")
	}
	tr.setFail("http://h1:1", false)
	if err := tr.TestNow(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if h := tr.View().Proxies[0].Health; h.State != "ok" || h.Exit.IP != "9.9.9.9" || tr.Binding("u2").ID != ids[0] {
		t.Fatalf("测通了应给等的号补上: %+v %+v", h, tr.Binding("u2"))
	}
	if got := tr.takeTested(); !slices.Equal(got, []string{"http://h1:1@cn.example"}) {
		t.Fatalf("测了 %v", got)
	}

	tr.SetProbes(nil, nil, "")
	if err := tr.TestNow(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	if h := tr.View().Proxies[1].Health; h.State != "" || h.Exit == nil || h.Exit.Error != "查不到" {
		t.Fatalf("只查出口: %+v", h)
	}

	tr.SetProbes(func(context.Context, string, string) (time.Duration, error) {
		if _, err := tr.DeleteProxies([]string{ids[1]}); err != nil {
			t.Error(err)
		}
		return time.Millisecond, nil
	}, nil, "")
	if err := tr.TestNow(ctx, ids[1]); err != nil || len(tr.View().Proxies) != 1 {
		t.Fatalf("测的时候被删了: %v", err)
	}
}

// TestCheckInterrupted 检查被打断（停机、面板请求取消）：测到一半的结果不算，代理不会被记成不通。
func TestCheckInterrupted(t *testing.T) {
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "池"})
	ids := tr.add(t, p1, "http://h1:1 甲")
	ctx, cancel := context.WithCancel(context.Background())
	tr.SetProbes(func(ctx context.Context, _, _ string) (time.Duration, error) {
		cancel()
		return 0, ctx.Err()
	}, nil, "")
	tr.tick(ctx)
	if err := tr.TestNow(ctx, ids[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("被打断应回 ctx 的错误: %v", err)
	}
	if h := tr.View().Proxies[0].Health; h.State != "" || !h.CheckedAt.IsZero() {
		t.Fatalf("被打断的结果不算: %+v", h)
	}
}

// TestReportProxy 真实请求的结果：连不上马上拦号，连上了放开并补位；状态没变走快路径；多行错误只留第一行。
func TestReportProxy(t *testing.T) {
	logs := captureLog(t)
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "池"})
	ids := tr.add(t, p1, "http://h1:1 甲")
	tr.Track(tr.acct(t, "u1", "cn"), false)
	if _, err := tr.Bind([]string{"u1"}, BindTarget{Pool: p1}); err != nil {
		t.Fatal(err)
	}
	tr.ReportProxy("nope", errTest)
	tr.ReportProxy(ids[0], nil)
	if h := tr.View().Proxies[0].Health; h.State != "ok" || !h.CheckedAt.Equal(t0) {
		t.Fatalf("连上了应标通: %+v", h)
	}
	tr.ReportProxy(ids[0], errors.New("代理认证失败\n详细信息"))
	if h := tr.View().Proxies[0].Health; h.State != "down" || h.Error != "代理认证失败" {
		t.Fatalf("连不上应标不通: %+v", h)
	}
	if !strings.Contains(tr.gate("u1"), "连不上") ||
		!strings.Contains(logs.String(), "proxy WARN 代理「甲」连不上，绑在上面的 1 个号暂停选号：代理认证失败") {
		t.Fatalf("应拦号并记日志: %q\n%s", tr.gate("u1"), logs)
	}
	tr.Track(tr.acct(t, "u2", "cn"), false)
	if res, _ := tr.Bind([]string{"u2"}, BindTarget{Pool: p1}); !res[0].Waiting {
		t.Fatal("唯一的代理不通，新绑的只能等")
	}
	tr.advance(time.Minute)
	tr.ReportProxy(ids[0], errTest)
	if h := tr.View().Proxies[0].Health; h.Error != "代理认证失败" || !h.CheckedAt.Equal(t0) {
		t.Fatalf("已经是不通，快路径不改: %+v", h)
	}
	tr.ReportProxy(ids[0], nil)
	if tr.Binding("u2").ID != ids[0] || tr.gate("u1") != "" || tr.gate("u2") != "" {
		t.Fatal("恢复了应放开并补位")
	}
	if !strings.Contains(logs.String(), "proxy 代理「甲」恢复了，绑在上面的 1 个号重新接请求") {
		t.Fatalf("日志:\n%s", logs)
	}
}

// TestRun 检查循环：起来先测一轮，「全部重测」马上再测，ctx 结束就退出。
func TestRun(t *testing.T) {
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "池"})
	tr.add(t, p1, "http://h1:1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tr.Run(ctx)
		close(done)
	}()
	waitFor(t, "第一轮", func() bool { return tr.testedN() == 1 })
	if n := tr.TestAll(); n != 1 {
		t.Fatalf("TestAll = %d", n)
	}
	waitFor(t, "全部重测", func() bool { return tr.testedN() == 2 })
	cancel()
	<-done
}
