// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package proxies

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestTrack 号进号池时的几种情况：绑的代理还在、代理没了但池还在、池也没了、凭证带 proxy_url
// （认出已有的、建在默认池、建在「导入的代理」池、认不出）、新号进默认池。
func TestTrack(t *testing.T) {
	logs := captureLog(t)
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "海外", DefaultGlobal: true})
	ids := tr.add(t, p1, "http://u:p@h1:1")

	withBinding := func(uid string, b auth.ProxyBinding) *auth.Auth {
		a := tr.acct(t, uid, "cn")
		a.SetProxyBinding(b)
		return a
	}
	tr.Track(withBinding("keep", auth.ProxyBinding{Pool: "旧池", ID: ids[0]}), false)
	if b := tr.Binding("keep"); b.Pool != p1 || b.ID != ids[0] {
		t.Fatalf("池以代理所在的为准: %+v", b)
	}
	tr.Track(withBinding("lost", auth.ProxyBinding{Pool: p1, ID: "gone"}), false)
	if b := tr.Binding("lost"); b.Pool != p1 || b.ID != ids[0] {
		t.Fatalf("代理没了应在原池重新分配: %+v", b)
	}
	tr.Track(withBinding("orphan", auth.ProxyBinding{Pool: "gone", ID: "gone"}), false)
	if b := tr.Binding("orphan"); !b.Empty() {
		t.Fatalf("池也没了应解绑: %+v", b)
	}

	parse := func(uid, realm, proxyURL string) *auth.Auth {
		a, err := auth.Parse([]byte(`{"accessToken":"at","uid":"` + uid + `","proxy_url":"` + proxyURL + `"}`))
		if err != nil {
			t.Fatal(err)
		}
		a.FilePath = ""
		if _, err := auth.BackfillRealmFor(a, realm); err != nil {
			t.Fatal(err)
		}
		return a
	}
	tr.Track(parse("same", "cn", "http://u:changed@H1:1"), false)
	if b := tr.Binding("same"); b.ID != ids[0] || !b.Manual {
		t.Fatalf("同一个代理（只改了密码）应认出来并单独指定: %+v", b)
	}
	tr.Track(parse("g1", "global", "socks5://g:1"), false)
	b := tr.Binding("g1")
	if b.Pool != p1 || b.ID == "" || b.ID == ids[0] || !b.Manual {
		t.Fatalf("新代理应建在国际版默认池: %+v", b)
	}
	tr.Track(parse("c1", "cn", "socks5://c:1"), false)
	tr.Track(parse("c2", "cn", "socks5://c:2"), false)
	v := tr.View()
	imp := ""
	for _, pl := range v.Pools {
		if pl.Name == importPoolName {
			imp = pl.ID
		}
	}
	if imp == "" || tr.Binding("c1").Pool != imp || tr.Binding("c2").Pool != imp || len(v.Pools) != 2 {
		t.Fatalf("国内版没有默认池应放进同一个「导入的代理」池: %+v", v.Pools)
	}
	tr.Track(parse("bad", "cn", "ftp://x:1"), false)
	if !tr.Binding("bad").Empty() || !strings.Contains(logs.String(), "proxy_url 认不出来") {
		t.Fatal("认不出的 proxy_url 只记日志、不绑")
	}
	tr.Track(parse("ip", "cn", "http://u:secret@203.0.113.7:8080"), false)
	if s := logs.String(); !strings.Contains(s, "凭证里带的代理 http://u:***@<IP>:8080 不在列表里") || strings.Contains(s, "203.0.113.7") {
		t.Fatalf("日志不带 IP 和密码: %s", s)
	}
	if k, l := tr.ExitKey("ip"); k != "proxy:"+tr.Binding("ip").ID || l != "代理「<IP>:8080」" {
		t.Fatalf("出口标签不带 IP: %q %q", k, l)
	}

	tr.Track(tr.acct(t, "new-g", "global"), true)
	if tr.Binding("new-g").Pool != p1 {
		t.Fatal("新的国际版号应进默认池")
	}
	tr.Track(tr.acct(t, "new-c", "cn"), true)
	if !tr.Binding("new-c").Empty() {
		t.Fatal("国内版没有默认池，新号不绑")
	}

	// 凭证里带的 proxy_url 比指向别处的旧绑定优先（别处导出的凭证）。
	stale := parse("stale", "cn", "socks5://c:1")
	stale.SetProxyBinding(auth.ProxyBinding{Pool: p1, ID: "gone"})
	tr.Track(stale, false)
	if b := tr.Binding("stale"); b.Pool != imp || !b.Manual {
		t.Fatalf("应按凭证里的代理地址绑: %+v", b)
	}

	// 导入时选了池：都放进这个池；已经在这个池里的照旧，凭证带的代理建在这个池里。
	p2 := tr.pool(t, PoolInput{Name: "国内"})
	ids2 := tr.add(t, p2, "http://h2:2")
	tr.TrackInto(withBinding("into-keep", auth.ProxyBinding{ID: ids2[0]}), p2)
	tr.TrackInto(withBinding("into-move", auth.ProxyBinding{Pool: p1, ID: ids[0]}), p2)
	tr.TrackInto(parse("into-url", "cn", "socks5://n:9"), p2)
	tr.TrackInto(parse("into-bad", "cn", "ftp://x:1"), p2)
	for _, uid := range []string{"into-keep", "into-move", "into-bad"} {
		if b := tr.Binding(uid); b.Pool != p2 || b.ID == "" || b.Manual {
			t.Fatalf("%s 应进池「国内」: %+v", uid, b)
		}
	}
	if b := tr.Binding("into-url"); b.Pool != p2 || b.ID == ids2[0] || !b.Manual {
		t.Fatalf("凭证带的代理应建在选的池里并单独指定: %+v", b)
	}
	tr.TrackInto(tr.acct(t, "into-gone", "cn"), "nope")
	if !tr.Binding("into-gone").Empty() {
		t.Fatal("选的池不存在时同 Track(a, true)：国内版没有默认池，不绑")
	}
	tr.Untrack("new-g")
	if tr.Binding("new-g") != (auth.ProxyBinding{}) || tr.Blocked("new-g") != "" {
		t.Fatal("删掉的号不再有绑定")
	}
}

// TestBind 批量绑定：解绑、单独指定（守上限）、绑池（已在池里的不动）、各种报错。
func TestBind(t *testing.T) {
	tr := newReg(t)
	p1, p2 := tr.pool(t, PoolInput{Name: "一", MaxPerProxy: 1}), tr.pool(t, PoolInput{Name: "二"})
	ids := tr.add(t, p1, "http://h1:1 甲", "http://h2:1 乙")
	for _, uid := range []string{"u1", "u2", "u3"} {
		tr.Track(tr.acct(t, uid, "cn"), false)
	}
	if _, err := tr.Bind(nil, BindTarget{}); err == nil {
		t.Fatal("没有目标应报错")
	}
	if _, err := tr.Bind(nil, BindTarget{Proxy: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("代理不存在应报错")
	}
	if _, err := tr.Bind(nil, BindTarget{Pool: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("池不存在应报错")
	}
	res, err := tr.Bind([]string{"u1", "u2", "u3", "ghost"}, BindTarget{Pool: p1})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].ProxyName != "甲" || res[1].ProxyName != "乙" || !res[2].Waiting || res[3].Error != "号不存在" {
		t.Fatalf("绑池结果 = %+v", res)
	}
	if !strings.Contains(tr.gate("u3"), "等代理：池「一」") {
		t.Fatalf("等代理应推给号池: %q", tr.gate("u3"))
	}
	// 已在池里有代理的号再绑一次不动。
	if res, _ := tr.Bind([]string{"u1"}, BindTarget{Pool: p1}); res[0].Proxy != ids[0] {
		t.Fatalf("已在池里的不应动: %+v", res)
	}
	if res, _ := tr.Bind([]string{"u3"}, BindTarget{Proxy: ids[0]}); !strings.Contains(res[0].Error, "绑满") {
		t.Fatalf("单独指定也守上限: %+v", res)
	}
	if res, _ := tr.Bind([]string{"u1"}, BindTarget{Proxy: ids[0]}); res[0].Error != "" || !tr.Binding("u1").Manual {
		t.Fatalf("单独指定已绑的代理: %+v", res)
	}
	if _, err := tr.Bind([]string{"u2"}, BindTarget{Unbind: true}); err != nil {
		t.Fatal(err)
	}
	if !tr.Binding("u2").Empty() || tr.Binding("u3").ID != ids[1] {
		t.Fatal("u2 解绑后空出的代理应补给等代理的 u3")
	}
	if res, _ := tr.Bind([]string{"u2"}, BindTarget{Pool: p2}); !res[0].Waiting {
		t.Fatalf("空池只能等代理: %+v", res)
	}
}

// TestRebalance 池内重新均分：单独指定的不动，其余按绑号最少重新分，返回换了代理的号数。
func TestRebalance(t *testing.T) {
	logs := captureLog(t)
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "池"})
	ids := tr.add(t, p1, "http://h1:1")
	uids := []string{"u1", "u2", "u3", "u4"}
	for _, uid := range uids {
		tr.Track(tr.acct(t, uid, "cn"), false)
	}
	if _, err := tr.Bind(uids, BindTarget{Pool: p1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Bind([]string{"u4"}, BindTarget{Proxy: ids[0]}); err != nil {
		t.Fatal(err)
	}
	more := tr.add(t, p1, "http://h2:1", "http://h3:1")
	if _, err := tr.Rebalance("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal("池不存在应报错")
	}
	moved, err := tr.Rebalance(p1)
	if err != nil || moved != 2 {
		t.Fatalf("应换 2 个号: %d %v", moved, err)
	}
	load := map[string]int{}
	for _, uid := range uids {
		load[tr.Binding(uid).ID]++
	}
	if load[ids[0]] != 2 || load[more[0]] != 1 || load[more[1]] != 1 || tr.Binding("u4").ID != ids[0] {
		t.Fatalf("均分结果 = %v（u4 单独指定不动）", load)
	}
	if !strings.Contains(logs.String(), "2 个号换了代理") {
		t.Fatalf("应记日志:\n%s", logs)
	}
	if moved, _ := tr.Rebalance(p1); moved != 0 {
		t.Fatalf("已经均匀，再分不换: %d", moved)
	}
}

// TestPick 分代理：通的优先于还没测的，再挑绑号最少的；不通、到期、绑满的不分；登录占位算负载。
func TestPick(t *testing.T) {
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "池", MaxPerProxy: 2})
	ids := tr.add(t, p1, "http://h1:1", "http://h2:1", "http://h3:1", "http://h4:1")
	tr.mu.Lock()
	past := t0.Add(-time.Hour)
	tr.proxyLocked(ids[0]).Health.State = "down"
	tr.proxyLocked(ids[1]).ExpiresAt = &past
	tr.proxyLocked(ids[3]).Health.State = "ok"
	got := tr.pickLocked(p1, t0)
	tr.mu.Unlock()
	if got == nil || got.ID != ids[3] {
		t.Fatalf("通的优先: %+v", got)
	}
	hold1, _, _ := tr.HoldForLogin(p1, "cn")
	hold2, _, _ := tr.HoldForLogin(p1, "cn")
	tr.mu.Lock()
	got = tr.pickLocked(p1, t0)
	tr.mu.Unlock()
	if got == nil || got.ID != ids[2] {
		t.Fatalf("通的那个被两个登录占满后应轮到没测的: %+v", got)
	}
	tr.ReleaseLogin(hold1)
	tr.ReleaseLogin(hold2)
	tr.ReleaseLogin("")
	if len(tr.logins) != 0 {
		t.Fatal("占位应放掉")
	}
	tr.mu.Lock()
	if tr.pickLocked("nope", t0) != nil {
		t.Fatal("不存在的池分不到")
	}
	tr.mu.Unlock()
}

// TestGatesAndRoutes 不能出站的原因（推给号池、线路选择都用）：只许走代理、等代理、到期、不通。
func TestGatesAndRoutes(t *testing.T) {
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "池"})
	ids := tr.add(t, p1, "http://u:p@h1:1 甲")
	tr.Track(tr.acct(t, "free", "cn"), false)
	tr.Track(tr.acct(t, "u1", "cn"), false)
	if _, err := tr.Bind([]string{"u1"}, BindTarget{Pool: p1}); err != nil {
		t.Fatal(err)
	}
	if id, name, url, err := tr.RouteAccount("u1"); err != nil || id != ids[0] || name != "甲" || url != "http://u:p@h1:1" {
		t.Fatalf("绑了代理的线路: %q %q %q %v", id, name, url, err)
	}
	if id, _, url, err := tr.RouteAccount("free"); err != nil || id != "" || url != "" {
		t.Fatalf("没绑的走默认线路: %q %q %v", id, url, err)
	}
	if _, _, _, err := tr.RouteAccount("stranger"); err != nil {
		t.Fatalf("不在号池里的号按没绑处理: %v", err)
	}
	if _, err := tr.UpdateSettings(Settings{RequireBound: true}); err != nil {
		t.Fatal(err)
	}
	var be *BlockedError
	if _, _, _, err := tr.RouteAccount("free"); !errors.As(err, &be) || !strings.Contains(be.Reason, "没绑代理") ||
		!strings.Contains(err.Error(), "free") {
		t.Fatalf("只许走代理时没绑的不能出站: %v", err)
	}
	if !strings.Contains(tr.gate("free"), "没绑代理") || !tr.Enabled() {
		t.Fatal("应推给号池")
	}
	tr.fmu.Lock()
	delete(tr.gates, "free")
	tr.fmu.Unlock()
	tr.Track(tr.acct(t, "free", "cn"), false)
	if !strings.Contains(tr.gate("free"), "没绑代理") {
		t.Fatal("再登记一次（号池里可能是新条目）应重新推")
	}
	if _, err := tr.UpdateSettings(Settings{}); err != nil || tr.gate("free") != "" {
		t.Fatal("关掉后应放开")
	}

	tr.mu.Lock()
	tr.proxyLocked(ids[0]).Health = Health{State: "down", DownSince: t0}
	tr.mu.Unlock()
	if _, _, _, err := tr.RouteAccount("u1"); !errors.As(err, &be) || !strings.Contains(be.Reason, "连不上") {
		t.Fatalf("不通: %v", err)
	}
	tr.mu.Lock()
	past := t0.Add(-time.Minute)
	tr.proxyLocked(ids[0]).ExpiresAt = &past
	tr.mu.Unlock()
	if r := tr.Blocked("u1"); !strings.Contains(r, "已到期") {
		t.Fatalf("到期: %q", r)
	}
	if _, _, err := tr.RouteProxy(ids[0]); err == nil || !strings.Contains(err.Error(), "已到期") {
		t.Fatalf("按代理取线路，到期报错: %v", err)
	}
	if _, _, err := tr.RouteProxy("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在: %v", err)
	}
	tr.mu.Lock()
	tr.proxyLocked(ids[0]).ExpiresAt = nil
	tr.mu.Unlock()
	if name, url, err := tr.RouteProxy(ids[0]); err != nil || name != "甲" || url == "" {
		t.Fatalf("测出不通的照样给线路: %q %v", name, err)
	}

	// 等代理的号在池被删之前，池名字取不到时用 ID 兜底。
	tr.mu.Lock()
	acc := &account{uid: "x", b: auth.ProxyBinding{Pool: "gone"}}
	reason := tr.gateLocked(acc, t0)
	tr.mu.Unlock()
	if !strings.Contains(reason, "池「gone」") {
		t.Fatalf("池名字兜底: %q", reason)
	}
}

// TestLookups ExitKey、HoldForLogin、ProxyURL、PoolByName 等查询。
func TestLookups(t *testing.T) {
	tr := newReg(t)
	if key, label := tr.ExitKey("nobody"); key != "default" || label != "服务器出口" {
		t.Fatalf("没绑、没查到服务器出口: %q %q", key, label)
	}
	if hold, proxy, err := tr.HoldForLogin("", "cn"); err != nil || hold != "" || proxy != "" {
		t.Fatalf("没有池时登录走默认线路: %q %q %v", hold, proxy, err)
	}
	if _, _, err := tr.HoldForLogin("nope", "cn"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("指定了不存在的池: %v", err)
	}
	p1 := tr.pool(t, PoolInput{Name: "海外", DefaultGlobal: true})
	if _, _, err := tr.HoldForLogin("", "global"); err == nil || !strings.Contains(err.Error(), "没有可用的空位") {
		t.Fatalf("默认池没代理: %v", err)
	}
	ids := tr.add(t, p1, "http://u:p@h1:1 甲")
	hold, proxy, err := tr.HoldForLogin("", "global")
	if err != nil || hold == "" || proxy != ids[0] {
		t.Fatalf("默认池占位: %q %q %v", hold, proxy, err)
	}
	if hold, proxy, err := tr.HoldForLogin(NoPool, "global"); err != nil || hold != "" || proxy != "" {
		t.Fatalf("选了不绑走默认线路: %q %q %v", hold, proxy, err)
	}
	tr.advance(loginHoldTTL + time.Second)
	if _, _, err := tr.HoldForLogin(p1, "global"); err != nil || len(tr.logins) != 1 {
		t.Fatalf("过期的占位应清掉: %v %d", err, len(tr.logins))
	}
	// 池满了：登录占位占着最后一个位子，新号只能等；放掉占位马上补上。
	if err := tr.UpdatePool(p1, PoolInput{Name: "海外", MaxPerProxy: 1, DefaultGlobal: true}); err != nil {
		t.Fatal(err)
	}
	tr.Track(tr.acct(t, "g1", "global"), true)
	if b := tr.Binding("g1"); b.Pool != p1 || b.ID != "" {
		t.Fatalf("位子被登录占着，新号应等代理: %+v", b)
	}
	tr.ReleaseLogin(func() string {
		for k := range tr.logins {
			return k
		}
		return ""
	}())
	if b := tr.Binding("g1"); b.ID != ids[0] {
		t.Fatalf("放掉占位后应补上: %+v", b)
	}
	tr.Untrack("g1")
	if err := tr.UpdatePool(p1, PoolInput{Name: "海外", DefaultGlobal: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.UpdateSettings(Settings{RequireBound: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tr.HoldForLogin("", "cn"); err == nil || !strings.Contains(err.Error(), "先选一个代理池") {
		t.Fatalf("只许走代理时没有池不能登录: %v", err)
	}
	if _, _, err := tr.HoldForLogin(NoPool, "global"); err == nil || !strings.Contains(err.Error(), "先选一个代理池") {
		t.Fatalf("只许走代理时不能选不绑: %v", err)
	}

	tr.Track(tr.acct(t, "u1", "cn"), false)
	if _, err := tr.Bind([]string{"u1"}, BindTarget{Proxy: ids[0]}); err != nil {
		t.Fatal(err)
	}
	if key, label := tr.ExitKey("u1"); key != "proxy:"+ids[0] || label != "代理「甲」" {
		t.Fatalf("还没查到出口按代理: %q %q", key, label)
	}
	if tr.ProxyName("u1") != "甲" || tr.ProxyName("nobody") != "" {
		t.Fatalf("代理名: %q %q", tr.ProxyName("u1"), tr.ProxyName("nobody"))
	}
	tr.mu.Lock()
	tr.proxyLocked(ids[0]).Health.Exit = &Exit{IP: "9.9.9.9"}
	tr.defaultExit = &Exit{IP: "1.1.1.1"}
	tr.mu.Unlock()
	if key, label := tr.ExitKey("u1"); key != "ip:9.9.9.9" || label != "代理「甲」" {
		t.Fatalf("按出口 IP，标签不带 IP: %q %q", key, label)
	}
	if key, label := tr.ExitKey("nobody"); key != "ip:1.1.1.1" || label != "服务器出口" {
		t.Fatalf("没绑的按服务器出口 IP: %q %q", key, label)
	}
	if tr.ProxyURL("u1") != "http://u:p@h1:1" || tr.ProxyURL("nobody") != "" {
		t.Fatal("ProxyURL 不对")
	}
	if tr.PoolByName(" 海外 ") != p1 || tr.PoolByName(p1) != p1 || tr.PoolByName("无") != "" {
		t.Fatal("PoolByName 不对")
	}
	if shortList([]string{"bbbbbbbbbb", "aaaaaaaaaa"}) != "aaaaaaaa、bbbbbbbb" {
		t.Fatal("shortList 不对")
	}
}
