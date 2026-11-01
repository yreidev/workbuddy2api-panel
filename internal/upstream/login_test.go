// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// login_test.go 登录三段请求（发起授权、轮询令牌、取账号）的形状、错误与取账号重试。
// 假上游用 blackcat_test.go 的 newUpFake，不连外网。
package upstream

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

const (
	loginTestStatePathCN   = "/v2/plugin/auth/state"
	loginTestTokenPath     = "/v2/plugin/auth/token"
	loginTestAccountCN     = "/v2/plugin/account"
	loginTestAccountGlobal = "/v2/plugin/login/account"

	loginTestStateBody = `{"code":0,"data":{"state":"st-1","authUrl":"https://auth.example/x"}}`
	loginTestTokenBody = `{"code":0,"data":{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":3600,"domain":"dom.example"}}`
	loginTestAcctBody  = `{"code":0,"data":{"uid":"u-1","nickname":"一号","enterpriseId":"ent-1"}}`
	loginTestPending   = `{"code":11217,"msg":"login ing"}`
	loginTestNotReady  = `{"code":12151,"msg":"account not ready"}`
)

// noLoginRetryDelay 取账号遇到 12151 时不必真等。
func noLoginRetryDelay(t *testing.T) {
	t.Helper()
	old := loginAccountRetryDelay
	loginAccountRetryDelay = 0
	t.Cleanup(func() { loginAccountRetryDelay = old })
}

func loginTestClient(f *upFake) *Client { return &Client{HTTP: f.client()} }

func loginTestHost(global bool) string {
	if global {
		return "www.workbuddy.ai"
	}
	return "www.workbuddy.cn"
}

func loginTestUA(global bool) string {
	v, cli := defaultClientVersionCN, defaultCliVersionCN
	app := "WorkBuddy"
	if global {
		v, cli = defaultClientVersionGlobal, defaultCliVersionGlobal
		app = "workbuddy-ai"
	}
	return app + "/" + v + " " + app + "/" + v + " CLI/" + cli
}

// loginWantHeaderAbsent 表里标「不带」的头必须为空。
func loginWantHeaderAbsent(t *testing.T, h http.Header, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v := h.Get(k); v != "" {
			t.Errorf("%s = %q，应不带", k, v)
		}
	}
}

// loginWantHeader 表里标「带」的头必须是给定值。
func loginWantHeader(t *testing.T, h http.Header, k, want string) {
	t.Helper()
	if got := h.Get(k); got != want {
		t.Errorf("%s = %q，应为 %q", k, got, want)
	}
}

// loginAssertHeaders 按请求种类逐项核对规格表里的头。
func loginAssertHeaders(t *testing.T, h http.Header, kind loginKind, global bool, tok LoginToken) {
	t.Helper()
	host := loginTestHost(global)
	loginWantHeader(t, h, "Accept", "application/json, text/plain, */*")
	loginWantHeader(t, h, "X-Requested-With", "XMLHttpRequest")
	loginWantHeader(t, h, "X-Product", "SaaS")
	loginWantHeader(t, h, "User-Agent", loginTestUA(global))
	if id := h.Get("X-Request-ID"); len(id) != 32 || strings.ToLower(id) != id || !isHexText(id) {
		t.Errorf("X-Request-ID = %q，应为 32 位小写十六进制", id)
	}
	loginWantHeaderAbsent(t, h, "Origin", "Referer", "X-User-Id", "X-Enterprise-Id", "X-Tenant-Id",
		"Accept-Language", "X-Device-Id-List")
	switch kind {
	case loginKindState:
		loginWantHeader(t, h, "Content-Type", "application/json")
		loginWantHeader(t, h, "X-Domain", host)
		loginWantHeader(t, h, "X-Custom-Channel-Id", "unknown")
		loginWantHeader(t, h, "X-No-Authorization", "true")
		loginWantHeader(t, h, "X-No-User-Id", "true")
		loginWantHeader(t, h, "X-No-Enterprise-Id", "true")
		loginWantHeader(t, h, "X-No-Department-Info", "true")
		loginWantHeaderAbsent(t, h, "Authorization")
	case loginKindToken:
		loginWantHeaderAbsent(t, h, "Content-Type", "X-Domain", "Authorization", "X-Custom-Channel-Id", "X-Device-Token")
		loginWantHeader(t, h, "X-No-Authorization", "true")
		loginWantHeader(t, h, "X-No-User-Id", "true")
		loginWantHeader(t, h, "X-No-Enterprise-Id", "true")
		loginWantHeader(t, h, "X-No-Department-Info", "true")
	case loginKindAccount:
		d := tok.Domain
		if d == "" {
			d = host
		}
		loginWantHeader(t, h, "Authorization", "Bearer "+tok.AccessToken)
		loginWantHeader(t, h, "X-Domain", d)
		loginWantHeaderAbsent(t, h, "Content-Type", "X-Custom-Channel-Id", "X-Device-Token", "X-No-Authorization")
		if global {
			loginWantHeader(t, h, "X-No-User-Id", "true")
			loginWantHeader(t, h, "X-No-Enterprise-Id", "true")
			loginWantHeader(t, h, "X-No-Department-Info", "true")
		} else {
			loginWantHeaderAbsent(t, h, "X-No-User-Id", "X-No-Enterprise-Id", "X-No-Department-Info")
		}
	}
}

// loginTraceparent 登录各请求的 traceparent：同一条 trace（loginSessionId 去掉连字符），
// 子 span 16 位 hex。
var loginTraceparent = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-01$`)

// loginAssertTrace 核对登录请求的链路头：国际版发起授权不带根 span 的头（官方 fetchAuthState
// 把 requestHeaders 放在了 headers 外面），其余请求都带；所有请求都带登录观测的 traceparent，
// 和根 span 同一条 trace。返回 traceparent 的子 span。
func loginAssertTrace(t *testing.T, h http.Header, tr LoginTrace, kind loginKind, global bool) string {
	t.Helper()
	traceID := strings.ReplaceAll(tr.SessionID, "-", "")
	m := loginTraceparent.FindStringSubmatch(h.Get("traceparent"))
	if m == nil || m[1] != traceID {
		t.Errorf("traceparent = %q，应为 trace %s 下的子 span", h.Get("traceparent"), traceID)
		return ""
	}
	if kind == loginKindState && global {
		loginWantHeaderAbsent(t, h, "X-Trace-ID", "b3", "X-B3-TraceId", "X-B3-SpanId", "X-B3-Sampled")
		if len(h.Values("X-B3-ParentSpanId")) > 0 {
			t.Error("国际版发起授权不应带 X-B3-ParentSpanId")
		}
		return m[2]
	}
	loginWantHeader(t, h, "X-Trace-ID", traceID)
	loginWantHeader(t, h, "b3", traceID+"-"+tr.SpanID+"-1-")
	loginWantHeader(t, h, "X-B3-TraceId", traceID)
	loginWantHeader(t, h, "X-B3-SpanId", tr.SpanID)
	loginWantHeader(t, h, "X-B3-Sampled", "1")
	if v := h.Values("X-B3-ParentSpanId"); len(v) != 1 || v[0] != "" {
		t.Errorf("X-B3-ParentSpanId = %q，应发空值", v)
	}
	if m[2] == tr.SpanID {
		t.Error("traceparent 应是子 span，不该等于根 span")
	}
	return m[2]
}

// isHexText 判断字符串是否全为小写十六进制。
func isHexText(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return s != ""
}

// TestLoginRequestsPerRealm 三个请求 × 两个域：方法、完整 URL（含 platform / state）、请求体与每个头。
func TestLoginRequestsPerRealm(t *testing.T) {
	cases := []struct {
		realm  string
		global bool
		token  LoginToken
	}{
		{"cn", false, LoginToken{AccessToken: "at-1", Domain: ""}},
		{"global", true, LoginToken{AccessToken: "at-1", Domain: "www.workbuddy.ai"}},
	}
	for _, tc := range cases {
		t.Run(tc.realm, func(t *testing.T) {
			f := newUpFake(t)
			host := loginTestHost(tc.global)
			platform := loginPlatformCN
			acctPath := loginTestAccountCN
			if tc.global {
				platform = loginPlatformGlobal
				acctPath = loginTestAccountGlobal
			}
			f.on(http.MethodPost, loginTestStatePathCN, 200, loginTestStateBody)
			f.on(http.MethodGet, loginTestTokenPath, 200, loginTestTokenBody)
			f.on(http.MethodGet, acctPath, 200, loginTestAcctBody)
			f.on(http.MethodGet, "/v2/plugin/accounts", 200, `{"code":0,"data":{"accounts":[{"uid":"u-1"}]}}`)
			c := loginTestClient(f)

			st, err := c.LoginAuthState(tc.realm, "")
			if err != nil || st.State != "st-1" || !strings.HasPrefix(st.AuthURL, "https://auth.example/x?version=") {
				t.Fatalf("发授权 = %+v, %v", st, err)
			}
			if st.Trace.SessionID == "" || st.Trace.OwnerID == "" || len(st.Trace.SpanID) != 16 {
				t.Fatalf("发起授权应返回这次登录的链路: %+v", st.Trace)
			}
			tok, pending, err := c.LoginPollToken(tc.realm, "st-1", st.Trace)
			if err != nil || pending || tok.AccessToken != "at-1" {
				t.Fatalf("轮询 = %+v pending=%v err=%v", tok, pending, err)
			}
			acct, err := c.LoginFetchAccount(tc.realm, "st-1", tok, st.Trace)
			if err != nil || acct.UID != "u-1" || acct.Nickname != "一号" || acct.EnterpriseID != "ent-1" {
				t.Fatalf("取账号 = %+v, %v", acct, err)
			}

			reqs := f.seen()
			want := 3
			if tc.global {
				want = 4 // 国际版取到账号后再取一次账号列表
			}
			if len(reqs) != want {
				t.Fatalf("应有 %d 次请求，实际 %d", want, len(reqs))
			}
			wantState := "https://" + host + "/v2/plugin/auth/state?platform=" + platform
			wantToken := "https://" + host + "/v2/plugin/auth/token?state=st-1"
			wantAcct := "https://" + host + acctPath
			if tc.global {
				wantAcct += "?state=st-1"
			}
			if reqs[0].method != http.MethodPost || reqs[0].url != wantState || string(reqs[0].body) != "{}" {
				t.Errorf("发起授权 = %q %q body=%q，want %q", reqs[0].method, reqs[0].url, reqs[0].body, wantState)
			}
			if reqs[1].method != http.MethodGet || reqs[1].url != wantToken || len(reqs[1].body) != 0 {
				t.Errorf("轮询令牌 = %q %q body=%q，want %q", reqs[1].method, reqs[1].url, reqs[1].body, wantToken)
			}
			if reqs[2].method != http.MethodGet || reqs[2].url != wantAcct || len(reqs[2].body) != 0 {
				t.Errorf("取账号 = %q %q body=%q，want %q", reqs[2].method, reqs[2].url, reqs[2].body, wantAcct)
			}
			spans := map[string]bool{}
			for i, kind := range []loginKind{loginKindState, loginKindToken, loginKindAccount} {
				loginAssertHeaders(t, reqs[i].header, kind, tc.global, tok)
				spans[loginAssertTrace(t, reqs[i].header, st.Trace, kind, tc.global)] = true
			}
			if tc.global {
				if reqs[3].method != http.MethodGet || reqs[3].url != "https://"+host+"/v2/plugin/accounts" {
					t.Errorf("取账号列表 = %q %q", reqs[3].method, reqs[3].url)
				}
				loginWantHeader(t, reqs[3].header, "Authorization", "Bearer at-1")
				loginWantHeaderAbsent(t, reqs[3].header, "X-No-User-Id", "X-No-Enterprise-Id", "X-No-Department-Info")
				spans[loginAssertTrace(t, reqs[3].header, st.Trace, loginKindAccounts, true)] = true
			}
			if len(spans) != want {
				t.Errorf("每类请求应各有自己的子 span，实际 %d 个不同的", len(spans))
			}
			if reqs[0].header.Get("X-Device-Token") != "" {
				t.Error("没有设备令牌时不应带 X-Device-Token")
			}

			// 授权页地址：两个域都追加 version；国内版另加登录观测参数。
			u, err := url.Parse(st.AuthURL)
			if err != nil {
				t.Fatalf("授权页地址解析失败: %v", err)
			}
			q := u.Query()
			wantVer := defaultClientVersionCN
			if tc.global {
				wantVer = defaultClientVersionGlobal
			}
			if q.Get("version") != wantVer {
				t.Errorf("version = %q, want %q", q.Get("version"), wantVer)
			}
			if tc.global {
				for _, k := range []string{"traceparent", "loginSessionId", "login_owner_id"} {
					if q.Has(k) {
						t.Errorf("国际版授权页不应带 %s", k)
					}
				}
				return
			}
			if q.Get("loginSessionId") != st.Trace.SessionID || q.Get("login_owner_id") != st.Trace.OwnerID {
				t.Errorf("loginSessionId/login_owner_id = %q/%q", q.Get("loginSessionId"), q.Get("login_owner_id"))
			}
			if m := loginTraceparent.FindStringSubmatch(q.Get("traceparent")); m == nil || m[1] != strings.ReplaceAll(st.Trace.SessionID, "-", "") || spans[m[2]] {
				t.Errorf("授权页 traceparent = %q，应是同一 trace 下打开授权页自己的子 span", q.Get("traceparent"))
			}
		})
	}
}

// TestLoginTraceStable 同一次登录的多次轮询共用同一个子 span；零值链路现生成一条完整的。
func TestLoginTraceStable(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, loginTestTokenPath, 200, loginTestPending)
	c := loginTestClient(f)
	tr := NewLoginTrace()
	for i := 0; i < 2; i++ {
		if _, pending, err := c.LoginPollToken("cn", "st-1", tr); err != nil || !pending {
			t.Fatalf("第 %d 次轮询 pending=%v err=%v", i, pending, err)
		}
	}
	reqs := f.seen()
	if len(reqs) != 2 || reqs[0].header.Get("traceparent") != reqs[1].header.Get("traceparent") {
		t.Fatalf("两次轮询的 traceparent 应相同: %d 次请求", len(reqs))
	}
	if reqs[0].header.Get("X-Request-ID") == reqs[1].header.Get("X-Request-ID") {
		t.Error("X-Request-ID 每次请求应不同")
	}
	if got := (LoginTrace{}).orNew(); got.SessionID == "" || got.OwnerID == "" || len(got.SpanID) != 16 {
		t.Errorf("零值链路应现生成: %+v", got)
	}
	if got := (LoginTrace{SessionID: "s", SpanID: "short"}).orNew(); got.SessionID == "s" {
		t.Error("span 不完整的链路应重新生成")
	}
}

// TestLoginTraceHelpers 链路的几个小函数：非 UUID 的会话 id 取哈希做 trace id；UUID 是 v4 格式；
// decorateAuthURL 保留原查询串和片段。
func TestLoginTraceHelpers(t *testing.T) {
	odd := LoginTrace{SessionID: "not-a-uuid", SpanID: "0123456789abcdef"}
	if d := odd.daemon(); !isHex(d.traceID, 32) || d.spanID != odd.SpanID {
		t.Errorf("非 UUID 会话的 trace = %+v", d)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(newUUID()) {
		t.Error("newUUID 应是 v4 格式")
	}
	if isHex("zz", 2) || isHex("abc", 2) || !isHex("ab", 2) {
		t.Error("isHex 判断不对")
	}
	if d := newDaemonTrace(); !isHex(d.traceID, 32) || !isHex(d.spanID, 16) {
		t.Errorf("newDaemonTrace = %+v", d)
	}
	c := &Client{}
	tr := NewLoginTrace()
	got := c.decorateAuthURL("https://auth.example/x?a=1#frag", "cn", tr)
	if !strings.HasPrefix(got, "https://auth.example/x?a=1&version=") || !strings.HasSuffix(got, "#frag") ||
		!strings.Contains(got, "&loginSessionId="+tr.SessionID+"&login_owner_id="+tr.OwnerID+"#") {
		t.Errorf("decorateAuthURL = %q", got)
	}
	if g := c.decorateAuthURL("https://auth.example/y", "global", tr); g != "https://auth.example/y?version="+url.QueryEscape(defaultClientVersionGlobal) {
		t.Errorf("国际版 decorateAuthURL = %q", g)
	}
}

// TestLoginDeviceTokenOnlyOnState 设备令牌只出现在发起授权上。
func TestLoginDeviceTokenOnlyOnState(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, loginTestStatePathCN, 200, loginTestStateBody)
	f.on(http.MethodGet, loginTestTokenPath, 200, loginTestPending)
	f.on(http.MethodGet, loginTestAccountCN, 200, loginTestAcctBody)
	c := loginTestClient(f)
	c.DeviceToken = "dev-123"

	if _, err := c.LoginAuthState("cn", ""); err != nil {
		t.Fatal(err)
	}
	_, pending, err := c.LoginPollToken("cn", "st-1", LoginTrace{})
	if err != nil || !pending {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	if _, err := c.LoginFetchAccount("cn", "st-1", LoginToken{AccessToken: "at-1"}, LoginTrace{}); err != nil {
		t.Fatal(err)
	}
	reqs := f.seen()
	if got := reqs[0].header.Get("X-Device-Token"); got != "dev-123" {
		t.Errorf("发起授权 X-Device-Token = %q", got)
	}
	if got := reqs[1].header.Get("X-Device-Token"); got != "" {
		t.Errorf("轮询不应带 X-Device-Token: %q", got)
	}
	if got := reqs[2].header.Get("X-Device-Token"); got != "" {
		t.Errorf("取账号不应带 X-Device-Token: %q", got)
	}
}

// TestLoginPollTokenStates 11217 → pending；成功 → 字段与 Raw；其他业务码 / HTTP 错误 / 解析失败 / 空令牌 → 错误。
func TestLoginPollTokenStates(t *testing.T) {
	newClient := func(t *testing.T, body string) *Client {
		t.Helper()
		f := newUpFake(t)
		f.on(http.MethodGet, loginTestTokenPath, 200, body)
		return loginTestClient(f)
	}

	t.Run("11217 pending", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, loginTestTokenPath, 200, loginTestPending)
		c := loginTestClient(f)
		tok, pending, err := c.LoginPollToken("cn", "st/1", LoginTrace{})
		if err != nil || !pending || !strings.Contains(tok.Msg, "11217") || !strings.Contains(tok.Msg, "login ing") {
			t.Fatalf("pending=%v tok=%+v err=%v", pending, tok, err)
		}
		// state 要转义。
		if got := f.seen()[0].url; got != "https://www.workbuddy.cn/v2/plugin/auth/token?state=st%2F1" {
			t.Fatalf("URL = %q", got)
		}
	})

	t.Run("成功带 Raw", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, loginTestTokenPath, 200, loginTestTokenBody)
		c := loginTestClient(f)
		tok, pending, err := c.LoginPollToken("cn", "st-1", LoginTrace{})
		if err != nil || pending {
			t.Fatalf("pending=%v err=%v", pending, err)
		}
		if tok.AccessToken != "at-1" || tok.RefreshToken != "rt-1" || tok.ExpiresIn != 3600 || tok.Domain != "dom.example" {
			t.Fatalf("令牌 = %+v", tok)
		}
		if string(tok.Raw) != `{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":3600,"domain":"dom.example"}` {
			t.Fatalf("Raw = %s", tok.Raw)
		}
	})

	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"其他业务码", `{"code":7,"msg":"nope"}`, "code=7 msg=nope"},
		{"解析失败", `not-json`, "parse failed"},
		{"数据形状不对", `{"code":0,"data":[]}`, "parse failed"},
		{"空 accessToken", `{"code":0,"data":{"accessToken":""}}`, "waiting for login"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, tc.body)
			if _, pending, err := c.LoginPollToken("cn", "st-1", LoginTrace{}); err == nil || pending ||
				!strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("pending=%v err=%v，want %q", pending, err, tc.wantErr)
			}
		})
	}

	t.Run("HTTP 500", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, loginTestTokenPath, 500, `{"code":500,"msg":"boom"}`)
		_, _, err := loginTestClient(f).LoginPollToken("cn", "st-1", LoginTrace{})
		var le *LoginError
		if !errors.As(err, &le) || le.Status != 500 || !strings.Contains(err.Error(), "upstream 500") {
			t.Fatalf("err=%v", err)
		}
	})
}

// TestLoginAuthStateErrors 缺 state / authUrl（含数据形状不对）、业务码错误、HTTP 错误。
func TestLoginAuthStateErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"缺 state", 200, `{"code":0,"data":{"authUrl":"https://a"}}`, "missing state or authUrl"},
		{"缺 authUrl", 200, `{"code":0,"data":{"state":"s"}}`, "missing state or authUrl"},
		{"数据形状不对", 200, `{"code":0,"data":[]}`, "missing state or authUrl"},
		{"业务码非 0", 200, `{"code":5,"msg":"no"}`, "code=5 msg=no"},
		{"HTTP 500", 500, `{"code":500,"msg":"boom"}`, "upstream 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodPost, loginTestStatePathCN, tc.status, tc.body)
			_, err := loginTestClient(f).LoginAuthState("cn", "")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v，want %q", err, tc.wantErr)
			}
		})
	}
	t.Run("缺字段用哨兵错误", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, loginTestStatePathCN, 200, `{"code":0,"data":{}}`)
		if _, err := loginTestClient(f).LoginAuthState("cn", ""); !errors.Is(err, ErrLoginAuthStateIncomplete) {
			t.Fatalf("err=%v", err)
		}
	})
}

// TestLoginAuthStateLicenseError 授权被拒的三个业务码随 HTTP 错误返回时，LoginError 带上业务码
// 和提示，LicenseReason 给中文原因；200 里的同一个码、其他码都不算授权被拒。
func TestLoginAuthStateLicenseError(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantCode   int
		wantReason string
	}{
		{"席位已满", 403, `{"code":12005,"msg":"seat"}`, 12005, "席位已满"},
		{"授权已过期", 403, `{"code":11212,"msg":"expired"}`, 11212, "授权已过期"},
		{"试用已过期", 400, `{"code":11216,"msg":"trial"}`, 11216, "试用已过期"},
		{"其他业务码", 403, `{"code":10001,"msg":"no"}`, 10001, ""},
		{"不是 JSON", 403, `forbidden`, 0, ""},
		{"200 里的授权码", 200, `{"code":12005,"msg":"seat"}`, 12005, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodPost, loginTestStatePathCN, tc.status, tc.body)
			_, err := loginTestClient(f).LoginAuthState("cn", "")
			var le *LoginError
			if !errors.As(err, &le) || le.Status != tc.status || le.Code != tc.wantCode || le.LicenseReason() != tc.wantReason {
				t.Fatalf("err=%v le=%+v", err, le)
			}
		})
	}
	t.Run("错误文本带业务码", func(t *testing.T) {
		e := &LoginError{Status: 403, Code: 12005, Msg: "seat"}
		if got := e.Error(); got != "http_error: upstream 403 code=12005 msg=seat" {
			t.Fatalf("Error()=%q", got)
		}
	})
}

// loginCountRoute 前 n 次回 body，之后回 next。
func loginCountRoute(n int, body, next string) upRoute {
	i := 0
	return func(*upReq) (*http.Response, error) {
		i++
		if i <= n {
			return jsonResp(200, body), nil
		}
		return jsonResp(200, next), nil
	}
}

// TestLoginFetchAccountRetries 12151 两次后成功（共 3 次）；一直 12151 → 6 次后报错；
// HTTP 401 立即报错；数据形状不对报解析错误；令牌 Domain 为空时 X-Domain 用域 host。
func TestLoginFetchAccountRetries(t *testing.T) {
	noLoginRetryDelay(t)

	t.Run("两次后成功", func(t *testing.T) {
		f := newUpFake(t)
		f.onFunc(http.MethodGet, loginTestAccountCN, loginCountRoute(2, loginTestNotReady, loginTestAcctBody))
		acct, err := loginTestClient(f).LoginFetchAccount("cn", "st-1", LoginToken{AccessToken: "at-1"}, LoginTrace{})
		if err != nil || acct.UID != "u-1" {
			t.Fatalf("acct=%+v err=%v", acct, err)
		}
		reqs := f.seen()
		if len(reqs) != 3 {
			t.Fatalf("应有 3 次请求，实际 %d", len(reqs))
		}
		// 每次重试都是新请求、新 X-Request-ID。
		if reqs[0].header.Get("X-Request-ID") == reqs[1].header.Get("X-Request-ID") ||
			reqs[1].header.Get("X-Request-ID") == reqs[2].header.Get("X-Request-ID") {
			t.Error("重试应生成新的 X-Request-ID")
		}
		// 国内版令牌 Domain 为空 → X-Domain 用域 host。
		for _, r := range reqs {
			if got := r.header.Get("X-Domain"); got != "www.workbuddy.cn" {
				t.Errorf("X-Domain = %q", got)
			}
		}
	})

	t.Run("一直 12151", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, loginTestAccountCN, 200, loginTestNotReady)
		_, err := loginTestClient(f).LoginFetchAccount("cn", "st-1", LoginToken{AccessToken: "at-1"}, LoginTrace{})
		if err == nil || !strings.Contains(err.Error(), "code=12151") {
			t.Fatalf("err=%v", err)
		}
		if got := len(f.seen()); got != 6 {
			t.Fatalf("应有首次 + 5 次重试 = 6 次请求，实际 %d", got)
		}
	})

	t.Run("HTTP 401 立即报错", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, loginTestAccountCN, 401, `{"code":401,"msg":"unauthorized"}`)
		_, err := loginTestClient(f).LoginFetchAccount("cn", "st-1", LoginToken{AccessToken: "at-1"}, LoginTrace{})
		var le *LoginError
		if !errors.As(err, &le) || le.Status != 401 {
			t.Fatalf("err=%v", err)
		}
		if got := len(f.seen()); got != 1 {
			t.Fatalf("401 不应重试，实际 %d 次", got)
		}
	})

	t.Run("其他业务码", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, loginTestAccountCN, 200, `{"code":9,"msg":"bad"}`)
		_, err := loginTestClient(f).LoginFetchAccount("cn", "st-1", LoginToken{AccessToken: "at-1"}, LoginTrace{})
		if err == nil || !strings.Contains(err.Error(), "code=9 msg=bad") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("解析失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, loginTestAccountCN, 200, `{"code":0,"data":[]}`)
		_, err := loginTestClient(f).LoginFetchAccount("cn", "st-1", LoginToken{AccessToken: "at-1"}, LoginTrace{})
		if err == nil || !strings.Contains(err.Error(), "parse failed") {
			t.Fatalf("err=%v", err)
		}
	})
}

// loginErrReader 读取即失败，覆盖读响应体失败的路径。
type loginErrReader struct{}

func (loginErrReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }
func (loginErrReader) Close() error             { return nil }

// TestLoginTransportAndHelpers 传输错误、读体失败、URL 非法、默认 client 与无设备令牌文件。
func TestLoginTransportAndHelpers(t *testing.T) {
	t.Run("传输错误", func(t *testing.T) {
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial boom")
		})}}
		if _, err := c.LoginAuthState("cn", ""); err == nil || !strings.Contains(err.Error(), "dial boom") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("读响应体失败", func(t *testing.T) {
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: loginErrReader{}}, nil
		})}}
		if _, err := c.LoginAuthState("cn", ""); err == nil || !strings.Contains(err.Error(), "read boom") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("URL 非法", func(t *testing.T) {
		c := loginTestClient(newUpFake(t))
		if _, _, _, _, err := c.loginDo(http.MethodGet, "http://exa\x00mple/", "cn", loginKindToken, LoginToken{}, LoginTrace{}); err == nil {
			t.Fatal("非法 URL 应报错")
		}
		if _, err := c.loginRequest(http.MethodGet, "http://exa\x00mple/", "cn", loginKindToken, LoginToken{}, LoginTrace{}); err == nil {
			t.Fatal("非法 URL 应报错")
		}
	})

	t.Run("未注入 HTTP 回落默认 client", func(t *testing.T) {
		if got := (&Client{}).loginClient(); got != http.DefaultClient {
			t.Fatalf("loginClient = %p，应为 http.DefaultClient", got)
		}
	})

	t.Run("无效 realm 按国内版", func(t *testing.T) {
		base, platform, global := loginEndpoint("weird")
		if base != cnProductEndpoint || platform != loginPlatformCN || global {
			t.Fatalf("loginEndpoint(weird) = %s %s %v", base, platform, global)
		}
	})
}

// 保证 io 包被使用（loginErrReader 只实现 Read/Close，不依赖 io.NopCloser）。
var _ io.Reader = loginErrReader{}
