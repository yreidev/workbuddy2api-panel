// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// fakeRouter 假的代理池：accounts 号 → 代理 ID，proxies 代理 ID → 地址，blocked 不能出站的号。
type fakeRouter struct {
	mu       sync.Mutex
	accounts map[string]string
	proxies  map[string]string
	blocked  map[string]error
	reports  []string
}

func (f *fakeRouter) RouteAccount(uid string) (string, string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.blocked[uid]; err != nil {
		return "", "", "", err
	}
	id := f.accounts[uid]
	if id == "" {
		return "", "", "", nil
	}
	return id, "代理" + id, f.proxies[id], nil
}

func (f *fakeRouter) RouteProxy(id string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.proxies[id]
	if !ok {
		return "", "", errors.New("没有这个代理")
	}
	return "代理" + id, u, nil
}

func (f *fakeRouter) ReportProxy(id string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := id + ":ok"
	if err != nil {
		s = id + ":fail"
	}
	f.reports = append(f.reports, s)
}

// takeReports 取出报过的结果（去重、排好序）并清空。
func (f *fakeRouter) takeReports() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Compact(slices.Sorted(slices.Values(f.reports)))
	f.reports = nil
	return out
}

func (f *fakeRouter) bind(uid, proxy string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[uid] = proxy
}

// routeFixture 号走代理的测试环境：假上游的域名是 upstream.test（解析不了，只有经假代理才连得上），每个
// 回应都带 Connection: close（每个请求都要重新经代理建隧道，代理收到几个 CONNECT 就是发了几个请求）；
// 假 CONNECT 代理把隧道一律接到假上游；默认线路拨号一律失败，没走号的代理的请求出不去。
type routeFixture struct {
	c        *Client
	router   *fakeRouter
	proxy    *fakeConnectProxy
	proxyURL string
	base     string

	mu           sync.Mutex
	unmarked     []string // 带了凭证却没标线路的请求
	defaultDials []string // 走了默认线路的拨号
}

func newRouteFixture(t *testing.T) *routeFixture {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "close")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{}}`)
	}))
	t.Cleanup(up.Close)
	_, port, _ := net.SplitHostPort(up.Listener.Addr().String())
	f := &routeFixture{proxy: &fakeConnectProxy{backend: up.Listener.Addr().String()}, base: "http://upstream.test:" + port}
	f.proxyURL = "http://u:p@" + f.proxy.start(t, nil)
	f.router = &fakeRouter{accounts: map[string]string{}, proxies: map[string]string{"px1": f.proxyURL}, blocked: map[string]error{}}

	deny := newTransport(nil)
	deny.DialContext = func(_ context.Context, _, addr string) (net.Conn, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.defaultDials = append(f.defaultDials, addr)
		return nil, fmt.Errorf("测试里默认线路不许出网：%s", addr)
	}
	deny.DialTLSContext = dialTLSMimic(deny.DialContext, nil)
	routes := newRouteTable(deny)
	t.Cleanup(routedTransport{routes}.CloseIdleConnections)
	strict := rtFunc(func(r *http.Request) (*http.Response, error) {
		_, marked := r.Context().Value(routeCtxKey{}).(routeRef)
		if !marked && (r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-User-Id") != "") {
			f.mu.Lock()
			f.unmarked = append(f.unmarked, r.Method+" "+r.URL.Path)
			f.mu.Unlock()
		}
		return routedTransport{routes}.RoundTrip(r)
	})
	f.c = &Client{
		HTTP:       &http.Client{Timeout: 10 * time.Second, Transport: strict},
		ChatHTTP:   &http.Client{Transport: strict},
		ChatBaseCN: f.base, BillingBaseCN: f.base, WebBaseCN: f.base,
		ChatBaseGlobal: f.base, BillingBaseGlobal: f.base,
		GlobalEnabled: true,
		routes:        routes,
	}
	f.c.PinIP = routes.pin
	f.c.SetRouter(f.router)
	return f
}

func (f *routeFixture) connects() int { return len(f.proxy.seen()) }

// routeAcct 造一个号，绑在代理 px1 上。
func (f *routeFixture) acct(t *testing.T, uid, realm string, enterprise bool) *auth.Auth {
	t.Helper()
	a := &auth.Auth{UID: uid, AccessToken: "at-" + uid, RefreshToken: "rt-" + uid, Nickname: "号" + uid}
	if enterprise {
		a.EnterpriseID = "ent-1"
	}
	if _, err := auth.BackfillRealmFor(a, realm); err != nil {
		t.Fatal(err)
	}
	f.router.bind(uid, "px1")
	return a
}

// routeExempt 带号参数但不发号的请求的方法：只拼请求头的，和取官方静态站点公开内容的（不带号，走默认线路）。
var routeExempt = []string{"BillingHeaders", "ChatHeaders", "RefreshHeaders", "HomeScenes", "PickPlaybookCase", "PickWorkingScenes"}

// TestRouteAllAccountMethods 每个带号参数的公开方法发出的请求都经这个号的代理，带凭证的请求都标了线路；
// 新加了带号参数的方法却没列进来，这个测试会失败。
func TestRouteAllAccountMethods(t *testing.T) {
	old := nightChatGap
	nightChatGap = 0
	t.Cleanup(func() { nightChatGap = old })
	f := newRouteFixture(t)
	a := f.acct(t, "u-cn", "cn", false)
	g := f.acct(t, "u-global", "global", false)
	e := f.acct(t, "u-ent", "cn", true)
	ctx := context.Background()
	chat := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	closeRC := func(rc io.ReadCloser, _ int, _ []byte, _ error) {
		if rc != nil {
			_ = rc.Close()
		}
	}
	cases := map[string]func(c *Client){
		"AcceptTasks":                func(c *Client) { _ = c.AcceptTasks(a, []string{"t1"}) },
		"AcceptTasksMP":              func(c *Client) { _ = c.AcceptTasksMP(a, []string{"t1"}) },
		"AccountEdition":             func(c *Client) { _, _ = c.AccountEdition(a) },
		"AppearanceThemes":           func(c *Client) { _, _ = c.AppearanceThemes(a) },
		"BlackcatNeed":               func(c *Client) { _, _ = c.BlackcatNeed(a) },
		"BuddyAgreement":             func(c *Client) { _ = c.BuddyAgreement(a) },
		"BuddyFirst":                 func(c *Client) { _ = c.BuddyFirst(a) },
		"BuddyInfo":                  func(c *Client) { _, _ = c.BuddyInfo(a) },
		"CancelCapacityQueue":        func(c *Client) { _ = c.CancelCapacityQueue(ctx, a, "glm-5.2", ChatMeta{}) },
		"CapacityQueueStatus":        func(c *Client) { _, _ = c.CapacityQueueStatus(ctx, a, "glm-5.2", ChatMeta{}) },
		"ChatStream":                 func(c *Client) { closeRC(c.ChatStream(a, chat, "", ChatMeta{})) },
		"ChatStreamContext":          func(c *Client) { closeRC(c.ChatStreamContext(ctx, g, chat, "", ChatMeta{})) },
		"ClaimAvailableCompensation": func(c *Client) { _, _ = c.ClaimAvailableCompensation(a) },
		"ClaimCompensation":          func(c *Client) { _, _ = c.ClaimCompensation(a) },
		"ClaimGift":                  func(c *Client) { _, _ = c.ClaimGift(a) },
		"ClaimReward":                func(c *Client) { _, _, _ = c.ClaimReward(a, "t1") },
		"ClaimRewardMP":              func(c *Client) { _, _, _ = c.ClaimRewardMP(a, "t1") },
		"ClaimTrial":                 func(c *Client) { _, _ = c.ClaimTrial(g) },
		"CreditPackages":             func(c *Client) { _, _, _, _ = c.CreditPackages(a) },
		"DailyCheckin":               func(c *Client) { _ = c.DailyCheckin(a) },
		"DesktopChatWithExpert":      func(c *Client) { _, _, _ = c.DesktopChatWithExpert(a, "e1", false, false) },
		"FetchAccountCatalog":        func(c *Client) { _, _ = c.FetchAccountCatalog(e) },
		"FetchAccountProfile":        func(c *Client) { _, _ = c.FetchAccountProfile(a) },
		"FetchGlobalModelInfos":      func(c *Client) { _ = c.FetchGlobalModelInfos(g) },
		"FetchGlobalModels": func(c *Client) {
			c.globalModels = fetchGlobalModelsCache{} // 上一个方法刚拉过，清掉缓存才会再发
			_ = c.FetchGlobalModels(g)
		},
		"FetchModels":                func(c *Client) { _, _ = c.FetchModels(a) },
		"GetCompensationStatus":      func(c *Client) { _, _ = c.GetCompensationStatus(a) },
		"GlobalCompleteRegistration": func(c *Client) { _, _ = c.GlobalCompleteRegistration(g) },
		"GlobalFetchCountries":       func(c *Client) { _, _ = c.GlobalFetchCountries(g, true) },
		"GlobalRegisterStatus":       func(c *Client) { _, _, _, _ = c.GlobalRegisterStatus(g) },
		"GlobalSubmitRegion":         func(c *Client) { _ = c.GlobalSubmitRegion(g, GlobalCountry{Code: "344", IOS2: "HK"}) },
		"GrowthRedeemTier":           func(c *Client) { _ = c.GrowthRedeemTier(a, "t1", 1) },
		"GrowthStreak":               func(c *Client) { _, _ = c.GrowthStreak(a) },
		"GrowthStreakFull":           func(c *Client) { _, _ = c.GrowthStreakFull(a) },
		"HeatmapYesterdayMissed":     func(c *Client) { _, _ = c.HeatmapYesterdayMissed(a) },
		"ListTasks":                  func(c *Client) { _, _ = c.ListTasks(a) },
		"ListTasksMP":                func(c *Client) { _, _ = c.ListTasksMP(a) },
		"LotteryChances":             func(c *Client) { _, _ = c.LotteryChances(a) },
		"LotteryDraw":                func(c *Client) { _, _ = c.LotteryDraw(a) },
		"MarketExpertList":           func(c *Client) { _, _ = c.MarketExpertList(a, "") },
		"MarketExpertListPage":       func(c *Client) { _, _ = c.MarketExpertListPage(a, "", 1) },
		"MarketExpertSearch":         func(c *Client) { _, _ = c.MarketExpertSearch(a, "", "关键词") },
		"RefreshToken":               func(c *Client) { _ = c.RefreshToken(a) },
		"ReportChatActivity":         func(c *Client) { _ = c.ReportChatActivity(a, "c1", "r1") },
		"ReportChatActivityModel":    func(c *Client) { _ = c.ReportChatActivityModel(a, "c1", "r1", "glm-5.2", "GLM-5.2") },
		"ReportDesktopEvent":         func(c *Client) { _ = c.ReportDesktopEvent(a, DesktopEvent{"eventCode": "x"}) },
		"ReportMPEvent":              func(c *Client) { _ = c.ReportMPEvent(a, map[string]any{"eventCode": "x"}) },
		"ReportWebEvent":             func(c *Client) { _ = c.ReportWebEvent(a, "x", "https://www.workbuddy.cn/", "id", "名字") },
		"RunNightChats":              func(c *Client) { _, _ = c.RunNightChats(a, 1) },
		"SchoolVouchers":             func(c *Client) { _, _ = c.SchoolVouchers(a) },
		"Search":                     func(c *Client) { _, _ = c.Search(ctx, a, SearchQuery{Query: "天气预报"}, ChatMeta{}) },
		"SetAppearanceTheme":         func(c *Client) { _ = c.SetAppearanceTheme(a, "dark") },
		"TravelClaim":                func(c *Client) { _, _ = c.TravelClaim(a) },
		"TravelDepart":               func(c *Client) { _ = c.TravelDepart(a, 1) },
		"TravelStatus":               func(c *Client) { _, _ = c.TravelStatus(a) },
		"UseMakeupCard":              func(c *Client) { _ = c.UseMakeupCard(a, "2026-10-31") },
		"UserResource":               func(c *Client) { _, _, _ = c.UserResource(a) },
		"UserResourceDetailed":       func(c *Client) { _, _, _, _ = c.UserResourceDetailed(a, time.Hour) },
		"UserResourceDetailedWithExpiry": func(c *Client) {
			_, _, _, _, _, _ = c.UserResourceDetailedWithExpiry(a, time.Hour)
		},
	}

	// 带号参数的公开方法都要在表里（或者在 routeExempt 里说明为什么不发号的请求）。
	acctType := reflect.TypeOf(&auth.Auth{})
	ct := reflect.TypeOf(&Client{})
	for i := 0; i < ct.NumMethod(); i++ {
		m := ct.Method(i)
		for j := 1; j < m.Type.NumIn(); j++ {
			if m.Type.In(j) != acctType {
				continue
			}
			if _, ok := cases[m.Name]; !ok && !slices.Contains(routeExempt, m.Name) {
				t.Errorf("带号参数的方法 %s 没有验证它走号的代理", m.Name)
			}
			break
		}
	}

	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		before := f.connects()
		cases[name](f.c)
		if f.connects() == before {
			t.Errorf("%s 没有经号的代理发请求", name)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.unmarked) != 0 {
		t.Errorf("带凭证却没标线路的请求: %v", f.unmarked)
	}
	for _, addr := range f.defaultDials {
		if strings.Contains(addr, "upstream.test") || strings.Contains(addr, "workbuddy") {
			t.Errorf("号的请求走了默认线路: %s", addr)
		}
	}
	if got := f.router.takeReports(); !slices.Contains(got, "px1:ok") || slices.Contains(got, "px1:fail") {
		t.Errorf("连上代理应报 ok: %v", got)
	}
	for _, r := range f.proxy.seen() {
		if r.Header.Get("Proxy-Authorization") == "" {
			t.Fatal("经代理应带上代理的用户名密码")
		}
	}
}

// TestRouteBlocked 号现在不能出站：不发请求、返回 Blocked 的 RouteError；国内版对话不切入口、不记熔断。
func TestRouteBlocked(t *testing.T) {
	f := newRouteFixture(t)
	a := f.acct(t, "u-cn", "cn", false)
	f.router.blocked["u-cn"] = errors.New("号 u-cn 现在不能出站：等代理")
	f.c.ChatBaseCN = cnProductEndpoint
	_, status, _, err := f.c.ChatStreamContext(context.Background(), a, []byte(`{"model":"m","messages":[]}`), "", ChatMeta{})
	var re *RouteError
	if !errors.As(err, &re) || !re.Blocked || status != 0 || !strings.HasSuffix(err.Error(), ": 号 u-cn 现在不能出站：等代理") {
		t.Fatalf("应返回 Blocked 的 RouteError: %v", err)
	}
	if f.connects() != 0 || len(f.defaultDials) != 0 {
		t.Fatal("不能出站的号不应发请求")
	}
	if len(f.c.failover.breakers) != 0 {
		t.Fatal("线路问题不记入口熔断")
	}
}

// TestRouteProxyDown 连不上号的代理：返回 RouteError 并报给代理池；不切入口、不记熔断、刷新令牌不换 IP；
// 调用方取消的不报。
func TestRouteProxyDown(t *testing.T) {
	f := newRouteFixture(t)
	a := f.acct(t, "u-cn", "cn", false)
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.router.proxies["px1"] = "http://" + dead.Addr().String()
	_ = dead.Close()
	f.c.ChatBaseCN = cnProductEndpoint
	f.c.HTTPDNSQuery = func(context.Context, string) []string {
		t.Error("线路问题不应换 IP")
		return nil
	}

	_, _, _, err = f.c.ChatStreamContext(context.Background(), a, []byte(`{"model":"m","messages":[]}`), "", ChatMeta{})
	var re *RouteError
	if !errors.As(err, &re) || re.Blocked || re.Proxy != "代理px1" || !strings.Contains(err.Error(), "连不上号的代理「代理px1」：连代理") {
		t.Fatalf("应返回 RouteError: %v", err)
	}
	if got := f.router.takeReports(); !slices.Equal(got, []string{"px1:fail"}) {
		t.Fatalf("应报连不上: %v", got)
	}
	if len(f.c.failover.breakers) != 0 {
		t.Fatal("线路问题不记入口熔断")
	}
	if err := f.c.RefreshToken(a); !IsRouteError(err) {
		t.Fatalf("刷新令牌: %v", err)
	}
	if !failoverIsInfraError(syscallRefused()) || failoverIsInfraError(re) {
		t.Fatal("连不上号的代理不算入口的基础设施失败")
	}
	if !loginAuthStateRetryable(0, nil, syscallRefused()) || loginAuthStateRetryable(0, nil, re) {
		t.Fatal("登录发起授权：线路问题不换 IP")
	}
	if at := f.c.configFetchOnce(a, f.base, nil, time.Second); at.retryable || !IsRouteError(at.err) {
		t.Fatalf("拉模型目录：线路问题不换入口: %+v", at)
	}
	f.router.takeReports()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := f.c.UserResource(a); err == nil {
		t.Fatal("应失败")
	}
	req, _ := newAccountRequest(ctx, a, http.MethodGet, f.base+"/x", nil)
	if _, err := (routedTransport{f.c.routes}).RoundTrip(req); err == nil || IsRouteError(err) {
		t.Fatalf("调用方取消不算线路问题: %v", err)
	}
	if got := f.router.takeReports(); !slices.Equal(got, []string{"px1:fail"}) {
		t.Fatalf("取消的不报（只有 UserResource 那次）: %v", got)
	}
}

// syscallRefused 一个「连接被拒」的传输错误。
func syscallRefused() error {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	_, err := net.Dial("tcp", addr)
	return err
}

// TestRoutePinned 钉 IP 重发经号的代理连那个 IP（SNI、Host 仍是原域名），结果不报给代理池；没绑代理的号
// 钉 IP 照官方直连。
func TestRoutePinned(t *testing.T) {
	srv, pool, port := hostSNIServer(t)
	fp := &fakeConnectProxy{backend: srv.Listener.Addr().String()}
	px := "http://" + fp.start(t, nil)
	router := &fakeRouter{accounts: map[string]string{"u1": "px1"}, proxies: map[string]string{"px1": px}}
	base := newTransport(nil)
	base.TLSClientConfig = &tls.Config{RootCAs: pool}
	routes := newRouteTable(base)
	routes.router = router
	t.Cleanup(routedTransport{routes}.CloseIdleConnections)

	target := "https://example.com:" + port + "/"
	req, _ := newAccountRequest(context.Background(), &auth.Auth{UID: "u1"}, http.MethodGet, target, nil)
	if got := proxyGet(t, rtReq{routes.pin("192.0.2.7"), req}, target); got != "example.com:"+port+" example.com" {
		t.Fatalf("上游看到 %q", got)
	}
	if reqs := fp.seen(); len(reqs) != 1 || reqs[0].Host != "192.0.2.7:"+port {
		t.Fatalf("应经代理 CONNECT 到钉的 IP: %+v", reqs)
	}
	if got := router.takeReports(); len(got) != 0 {
		t.Fatalf("钉 IP 的结果不报: %v", got)
	}
	// 代理连不上钉的 IP：RouteError，也不报。
	bad := &fakeConnectProxy{status: http.StatusBadGateway}
	router.proxies["px2"], router.accounts["u2"] = "http://"+bad.start(t, nil), "px2"
	req, _ = newAccountRequest(context.Background(), &auth.Auth{UID: "u2"}, http.MethodGet, target, nil)
	if _, err := routes.pin("192.0.2.8").RoundTrip(req); !IsRouteError(err) || len(router.takeReports()) != 0 || len(bad.seen()) != 1 {
		t.Fatalf("钉 IP 经代理失败: %v", err)
	}
	// 没绑代理的号钉 IP：直连那个 IP。
	ip := srv.Listener.Addr().(*net.TCPAddr).IP.String()
	req, _ = newAccountRequest(context.Background(), &auth.Auth{UID: "free"}, http.MethodGet, target, nil)
	if got := proxyGet(t, rtReq{routes.pin(ip), req}, target); got != "example.com:"+port+" example.com" || len(fp.seen()) != 1 {
		t.Fatalf("没绑代理的号钉 IP 应直连: %q", got)
	}
}

// rtReq 用 rt 发事先建好的 req（proxyGet 自己建的请求不带线路）。
type rtReq struct {
	rt  http.RoundTripper
	req *http.Request
}

func (r rtReq) RoundTrip(*http.Request) (*http.Response, error) { return r.rt.RoundTrip(r.req) }

// TestRouteTable 线路的 Transport 按「代理地址 + 钉的 IP」复用；一小时没用的在建新线路时清掉；代理地址
// 坏了按不能出站处理。
func TestRouteTable(t *testing.T) {
	routes := newRouteTable(newTransport(nil))
	if tr, _ := routes.transport("", ""); tr != routes.base {
		t.Fatal("默认线路就是 base")
	}
	p1, _ := routes.transport("http://h:1", "")
	p1b, _ := routes.transport("http://h:1", "")
	pin, _ := routes.transport("", "192.0.2.1")
	pin2, _ := routes.transport("http://h:1", "192.0.2.1")
	if p1 != p1b || p1 == pin || pin == pin2 || len(routes.cache) != 3 {
		t.Fatal("同一条线路应复用，不同的分开")
	}
	if _, err := routes.transport("ftp://h:1", ""); err == nil {
		t.Fatal("坏地址应报错")
	}
	routes.mu.Lock()
	for _, e := range routes.cache {
		e.used = time.Now().Add(-2 * time.Hour)
	}
	routes.mu.Unlock()
	if _, err := routes.transport("http://h:2", ""); err != nil || len(routes.cache) != 1 {
		t.Fatalf("一小时没用的应清掉: %d", len(routes.cache))
	}
	router := &fakeRouter{accounts: map[string]string{"u1": "px1"}, proxies: map[string]string{"px1": "ftp://bad"}}
	routes.router = router
	req, _ := newAccountRequest(context.Background(), &auth.Auth{UID: "u1"}, http.MethodGet, "http://upstream.test/", nil)
	var re *RouteError
	if _, err := (routedTransport{routes}).RoundTrip(req); !errors.As(err, &re) || !re.Blocked {
		t.Fatalf("代理地址坏了: %v", err)
	}
	// 没装代理池：标了号也走默认线路。
	if r := (*routeTable)(nil).getRouter(); r != nil {
		t.Fatal("nil 线路表没有代理池")
	}
	if id, _, _, err := newRouteTable(nil).resolve(req.Context()); id != "" || err != nil {
		t.Fatal("没装代理池走默认线路")
	}
	if id, _, _, err := routes.resolve(context.Background()); id != "" || err != nil {
		t.Fatal("没标线路走默认线路")
	}
	// 代理本身只认 localhost 直连，号的代理同样管不到本机地址。
	px := &outboundProxy{url: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}}
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "local") }))
	defer local.Close()
	tr := proxyTransport(newTransport(nil), px, "")
	defer tr.CloseIdleConnections()
	if got := proxyGet(t, tr, local.URL); got != "local" {
		t.Fatalf("本机地址直连: %q", got)
	}
	if _, err := tr.DialContext(context.Background(), "tcp", "no-port"); err == nil {
		t.Fatal("拨号地址没端口应报错")
	}
}

// TestRouteLogin 登录新号走定下的代理：发起授权时记进 Trace，轮询、取账号都走它；没定代理走默认线路；
// 代理没了按不能出站处理。
func TestRouteLogin(t *testing.T) {
	// 登录入口是官方域名：假代理把隧道接到假上游，TLS 握手失败，但请求确实经了代理（看 CONNECT）。
	f := newRouteFixture(t)
	if _, err := f.c.LoginAuthState("cn", "px1"); err == nil || f.connects() != 1 || f.proxy.seen()[0].Host != "www.workbuddy.cn:443" {
		t.Fatalf("发起授权应经代理: %v %d", err, f.connects())
	}
	tr := NewLoginTrace()
	tr.Proxy = "px1"
	_, _, _ = f.c.LoginPollToken("cn", "s", tr)
	_, _ = f.c.LoginFetchAccount("global", "s", LoginToken{AccessToken: "at"}, LoginTrace{Proxy: "px1"})
	if f.connects() != 3 {
		t.Fatalf("轮询、取账号都应经代理: %d", f.connects())
	}
	if got := (LoginTrace{Proxy: "px1"}).orNew(); got.SessionID == "" || got.Proxy != "px1" {
		t.Fatalf("补链路要保留代理: %+v", got)
	}
	if _, err := f.c.LoginAuthState("cn", ""); err == nil || f.connects() != 3 || len(f.defaultDials) == 0 {
		t.Fatalf("没定代理走默认线路: %v", err)
	}
	if _, err := f.c.LoginAuthState("cn", "gone"); !IsRouteError(err) || f.connects() != 3 {
		t.Fatalf("代理没了: %v", err)
	}
}

// TestProbeProxy 健康检查经代理连官方入口做完 TLS 握手；代理连不上、地址坏了报错。
func TestProbeProxy(t *testing.T) {
	srv, pool, _ := hostSNIServer(t)
	fp := &fakeConnectProxy{backend: srv.Listener.Addr().String()}
	addr := fp.start(t, nil)
	c := &Client{routes: newRouteTable(newTransport(nil))}
	c.routes.base.TLSClientConfig = &tls.Config{RootCAs: pool}
	d, err := c.ProbeProxy(context.Background(), "http://"+addr, "example.com")
	if err != nil || d <= 0 {
		t.Fatalf("测通: %v %v", d, err)
	}
	if reqs := fp.seen(); len(reqs) != 1 || reqs[0].Host != "example.com:443" {
		t.Fatalf("应 CONNECT 到入口的 443: %+v", reqs)
	}
	if _, err := c.ProbeProxy(context.Background(), "http://"+addr, "other.example"); err == nil {
		t.Fatal("证书对不上应报错")
	}
	deny := &fakeConnectProxy{status: http.StatusForbidden}
	if _, err := c.ProbeProxy(context.Background(), "http://"+deny.start(t, nil), "example.com"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("代理拒绝: %v", err)
	}
	if _, err := c.ProbeProxy(context.Background(), "ftp://x", "example.com"); err == nil {
		t.Fatal("坏地址应报错")
	}
}

// TestRouteClientSettings SetHeaderTimeout、EnvProxyURL、viaEnvProxy、RouteError 的文案。
func TestRouteClientSettings(t *testing.T) {
	c := &Client{routes: newRouteTable(newTransport(nil))}
	c.SetHeaderTimeout(7 * time.Second)
	if c.HeaderTimeout != 7*time.Second || c.routes.base.ResponseHeaderTimeout != 7*time.Second {
		t.Fatal("SetHeaderTimeout 应同时改线路的 Transport")
	}
	(&Client{}).SetHeaderTimeout(time.Second) // 零值 Client 不炸
	if tr, _ := c.routes.transport("http://h:1", ""); tr.ResponseHeaderTimeout != 7*time.Second {
		t.Fatal("新线路从 base 克隆，超时一样")
	}
	if c.EnvProxyURL() != "" || c.viaEnvProxy(&auth.Auth{UID: "u1"}, "wb.tencentbuddy.com") {
		t.Fatal("没设环境变量的代理")
	}
	c.proxy = &outboundProxy{url: &url.URL{Scheme: "http", Host: "env:8080", User: url.UserPassword("u", "p")}}
	if c.EnvProxyURL() != "http://u:p@env:8080" || !c.viaEnvProxy(&auth.Auth{UID: "u1"}, "wb.tencentbuddy.com") {
		t.Fatal("没装代理池：走环境变量的代理")
	}
	c.SetRouter(&fakeRouter{accounts: map[string]string{"u1": "px1"}, proxies: map[string]string{"px1": "http://h:1"}})
	if c.viaEnvProxy(&auth.Auth{UID: "u1"}, "wb.tencentbuddy.com") || !c.viaEnvProxy(&auth.Auth{UID: "u2"}, "wb.tencentbuddy.com") {
		t.Fatal("绑了代理的号不走环境变量的代理，没绑的走")
	}
	(&Client{}).SetRouter(&fakeRouter{}) // 手拼的 Client 没有线路表，装了也不起作用（不炸）
	global, _ := url.Parse(defaultGlobalBase)
	c.ChatBaseCN = "https://www.workbuddy.cn/v2"
	if h := c.EntryHosts(); h["cn"] != "www.workbuddy.cn" || h["global"] != global.Hostname() {
		t.Fatalf("EntryHosts = %v，要两个域对话主入口的主机名", h)
	}
	c.ChatBaseCN = "://bad"
	if c.EntryHosts()["cn"] != "" {
		t.Fatal("地址解析不了时主机名为空")
	}
	if (&RouteError{Proxy: "甲", Err: errors.New("x")}).Error() != "连不上号的代理「甲」：x" ||
		(&RouteError{Blocked: true, Err: errors.New("号 a 现在不能出站：y")}).Error() != "号 a 现在不能出站：y" {
		t.Fatal("RouteError 文案不对")
	}
	if (&RouteError{Proxy: "203.0.113.7:8080", Err: errors.New("dial tcp 203.0.113.7:8080: refused")}).Error() != "连不上号的代理「<IP>:8080」：dial tcp <IP>:8080: refused" ||
		(&RouteError{Blocked: true, Err: errors.New("号 a 现在不能出站：代理「203.0.113.7:8080」已到期")}).Error() != "号 a 现在不能出站：代理「<IP>:8080」已到期" {
		t.Fatal("RouteError 要遮掉 IP（没起名字的代理名字就是 IP:端口）")
	}
	if (&proxyDialError{errors.New("z")}).Error() != "z" || !errors.Is(&proxyDialError{context.Canceled}, context.Canceled) {
		t.Fatal("proxyDialError 文案、拆包不对（调用方取消时原样返回，要认得出 context.Canceled）")
	}
}
