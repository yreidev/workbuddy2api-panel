// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

// login_failover_test.go 登录与刷新令牌的超时与换 IP：auth/state 与 refresh 换 IP，
// 轮询、取账号不换；国际版不加超时、不换。测试走假 RoundTripper，不连外网。

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const (
	loginFoStatePath   = "/v2/plugin/auth/state"
	loginFoRefreshPath = "/v2/plugin/auth/token/refresh"
	loginFoAccountPath = "/v2/plugin/account"

	loginFoStateBody   = `{"code":0,"data":{"state":"st-1","authUrl":"https://auth.example/x"}}`
	loginFoTokenBody   = `{"code":0,"data":{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":3600}}`
	loginFoAccountBody = `{"code":0,"data":{"uid":"u-1","nickname":"一号"}}`
	loginFoRefreshBody = `{"code":0,"data":{"accessToken":"new-at","refreshToken":"new-rt","expiresIn":3600}}`
)

// loginFoReq 假登录上游收到的一次请求。
type loginFoReq struct {
	path        string
	pinned      bool
	requestID   string
	body        string
	hasDeadline bool
	remaining   time.Duration
}

// loginFoFake 假登录上游：按 (路径, 是否钉 IP) 交给 handler。
type loginFoFake struct {
	mu      sync.Mutex
	reqs    []loginFoReq
	pinIPs  []string
	handler func(path string, pinned bool, r *http.Request) (*http.Response, error)
}

func (f *loginFoFake) roundTrip(r *http.Request) (*http.Response, error) {
	return f.serve(r, false)
}

func (f *loginFoFake) pin(ip string) http.RoundTripper {
	return rtFunc(func(r *http.Request) (*http.Response, error) {
		f.mu.Lock()
		f.pinIPs = append(f.pinIPs, ip)
		f.mu.Unlock()
		return f.serve(r, true)
	})
}

func (f *loginFoFake) serve(r *http.Request, pinned bool) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	rec := loginFoReq{path: r.URL.Path, pinned: pinned,
		requestID: r.Header.Get("X-Request-ID"), body: string(body)}
	if deadline, ok := r.Context().Deadline(); ok {
		rec.hasDeadline = true
		rec.remaining = time.Until(deadline)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, rec)
	f.mu.Unlock()
	return f.handler(r.URL.Path, pinned, r)
}

func (f *loginFoFake) seen() []loginFoReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]loginFoReq(nil), f.reqs...)
}

// loginFoClient 地址层生效的测试客户端（查询恒返回一个测试 IP）。
func loginFoClient(f *loginFoFake) *Client {
	return &Client{
		HTTP: &http.Client{Transport: rtFunc(f.roundTrip)},
		HTTPDNSQuery: func(context.Context, string) []string {
			return []string{dnsIPv4(1)}
		},
		PinIP: f.pin,
	}
}

// 国内版 auth/state 502：钉 HTTPDNS 的第一个 IP 重发，头与体原样。
func TestLoginFailoverAuthStatePinnedRetry(t *testing.T) {
	f := &loginFoFake{handler: func(path string, pinned bool, _ *http.Request) (*http.Response, error) {
		if path != loginFoStatePath {
			t.Errorf("意外请求 %s", path)
		}
		if !pinned {
			return jsonResp(http.StatusBadGateway, `{"code":502}`), nil
		}
		return jsonResp(http.StatusOK, loginFoStateBody), nil
	}}
	c := loginFoClient(f)
	st, err := c.LoginAuthState("cn", "")
	if err != nil || st.State != "st-1" {
		t.Fatalf("LoginAuthState = %+v, %v", st, err)
	}
	reqs := f.seen()
	if len(reqs) != 2 {
		t.Fatalf("请求数 = %d，应为 2", len(reqs))
	}
	if reqs[0].pinned || !reqs[1].pinned {
		t.Fatalf("pinned = %v/%v，应为先不钉、后钉", reqs[0].pinned, reqs[1].pinned)
	}
	if reqs[0].requestID == "" || reqs[0].requestID != reqs[1].requestID {
		t.Errorf("两次 X-Request-ID = %q/%q，应原样复用", reqs[0].requestID, reqs[1].requestID)
	}
	if reqs[0].body != "{}" || reqs[1].body != "{}" {
		t.Errorf("请求体 = %q/%q，应原样", reqs[0].body, reqs[1].body)
	}
	if len(f.pinIPs) != 1 || f.pinIPs[0] != dnsIPv4(1) {
		t.Errorf("钉的 IP = %v", f.pinIPs)
	}
}

// 国内版 auth/state 200 但缺 authUrl：按无效响应换 IP。
func TestLoginFailoverAuthStateInvalidBodyRetry(t *testing.T) {
	f := &loginFoFake{handler: func(path string, pinned bool, _ *http.Request) (*http.Response, error) {
		if !pinned {
			return jsonResp(http.StatusOK, `{"code":0,"data":{"state":"st-1"}}`), nil
		}
		return jsonResp(http.StatusOK, loginFoStateBody), nil
	}}
	c := loginFoClient(f)
	st, err := c.LoginAuthState("cn", "")
	if err != nil || st.State != "st-1" {
		t.Fatalf("LoginAuthState = %+v, %v", st, err)
	}
	if reqs := f.seen(); len(reqs) != 2 || !reqs[1].pinned {
		t.Fatalf("请求 = %+v，应为两次且第二次钉 IP", reqs)
	}
}

// TestLoginFailoverNoRetryRequests 轮询令牌、取账号不换 IP；国际版不换也不加超时。
func TestLoginFailoverNoRetryRequests(t *testing.T) {
	t.Run("轮询令牌 502 不重发", func(t *testing.T) {
		f := &loginFoFake{handler: func(path string, _ bool, _ *http.Request) (*http.Response, error) {
			return jsonResp(http.StatusBadGateway, `{"code":502}`), nil
		}}
		c := loginFoClient(f)
		if _, _, err := c.LoginPollToken("cn", "st-1", LoginTrace{}); err == nil {
			t.Fatal("应返回错误")
		}
		if reqs := f.seen(); len(reqs) != 1 {
			t.Fatalf("请求数 = %d，应只发一次", len(reqs))
		}
	})

	t.Run("取账号 502 不重发", func(t *testing.T) {
		f := &loginFoFake{handler: func(path string, _ bool, _ *http.Request) (*http.Response, error) {
			return jsonResp(http.StatusBadGateway, `{"code":502}`), nil
		}}
		c := loginFoClient(f)
		_, err := c.LoginFetchAccount("cn", "st-1", LoginToken{AccessToken: "at"}, LoginTrace{})
		if err == nil {
			t.Fatal("应返回错误")
		}
		if reqs := f.seen(); len(reqs) != 1 {
			t.Fatalf("请求数 = %d，应只发一次", len(reqs))
		}
	})

	t.Run("国际版 502 不重发且无超时", func(t *testing.T) {
		f := &loginFoFake{handler: func(path string, _ bool, _ *http.Request) (*http.Response, error) {
			return jsonResp(http.StatusBadGateway, `{"code":502}`), nil
		}}
		c := loginFoClient(f)
		if _, err := c.LoginAuthState("global", ""); err == nil {
			t.Fatal("应返回错误")
		}
		reqs := f.seen()
		if len(reqs) != 1 {
			t.Fatalf("国际版请求数 = %d，应只发一次", len(reqs))
		}
		if reqs[0].hasDeadline {
			t.Errorf("国际版不应加 ctx 超时，实际剩余 %v", reqs[0].remaining)
		}
	})
}

// TestRefreshFailoverNoAccessToken 国内版 refresh 200 但缺 accessToken：钉 IP 重发成功。
func TestRefreshFailoverNoAccessToken(t *testing.T) {
	f := &loginFoFake{handler: func(path string, pinned bool, _ *http.Request) (*http.Response, error) {
		switch path {
		case loginFoRefreshPath:
			if !pinned {
				return jsonResp(http.StatusOK, `{"code":0,"data":{"refreshToken":"rt2"}}`), nil
			}
			return jsonResp(http.StatusOK, loginFoRefreshBody), nil
		case loginFoAccountPath:
			return jsonResp(http.StatusOK, loginFoAccountBody), nil
		}
		t.Errorf("意外请求 %s", path)
		return jsonResp(http.StatusNotFound, `{}`), nil
	}}
	c := loginFoClient(f)
	c.ChatBaseCN = cnProductEndpoint
	a := &auth.Auth{UID: "u-1", AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 111}
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if a.AccessToken != "new-at" || a.RefreshToken != "new-rt" {
		t.Fatalf("写回 = %+v", a)
	}
	reqs := f.seen()
	if len(reqs) != 3 {
		t.Fatalf("请求 = %+v，应为 refresh、钉 IP refresh、账号核对", reqs)
	}
	if reqs[0].pinned || !reqs[1].pinned || reqs[2].pinned {
		t.Errorf("pinned = %v/%v/%v，应为不钉、钉、不钉", reqs[0].pinned, reqs[1].pinned, reqs[2].pinned)
	}
}

// TestLoginFailoverTimeouts 国内版超时：auth/state 8 秒、轮询 4 秒、取账号 4 秒、
// refresh 8 秒、刷新后账号核对 4 秒（读请求 ctx 的 deadline）。
func TestLoginFailoverTimeouts(t *testing.T) {
	f := &loginFoFake{handler: func(path string, _ bool, _ *http.Request) (*http.Response, error) {
		switch path {
		case loginFoStatePath:
			return jsonResp(http.StatusOK, loginFoStateBody), nil
		case "/v2/plugin/auth/token":
			return jsonResp(http.StatusOK, loginFoTokenBody), nil
		case loginFoRefreshPath:
			return jsonResp(http.StatusOK, loginFoRefreshBody), nil
		case loginFoAccountPath:
			return jsonResp(http.StatusOK, loginFoAccountBody), nil
		}
		return jsonResp(http.StatusNotFound, `{}`), nil
	}}
	c := loginFoClient(f)
	c.ChatBaseCN = cnProductEndpoint

	if _, err := c.LoginAuthState("cn", ""); err != nil {
		t.Fatalf("LoginAuthState: %v", err)
	}
	if _, _, err := c.LoginPollToken("cn", "st-1", LoginTrace{}); err != nil {
		t.Fatalf("LoginPollToken: %v", err)
	}
	if _, err := c.LoginFetchAccount("cn", "st-1", LoginToken{AccessToken: "at"}, LoginTrace{}); err != nil {
		t.Fatalf("LoginFetchAccount: %v", err)
	}
	a := &auth.Auth{UID: "u-1", AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 111}
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}

	var state, token, account, refresh, check loginFoReq
	seenCheck := false
	for _, r := range f.seen() {
		switch r.path {
		case loginFoStatePath:
			state = r
		case "/v2/plugin/auth/token":
			token = r
		case loginFoRefreshPath:
			refresh = r
		case loginFoAccountPath:
			if !seenCheck {
				account = r
				seenCheck = true
			} else {
				check = r
			}
		}
	}
	checkDeadline := func(name string, got loginFoReq, want time.Duration) {
		t.Helper()
		if !got.hasDeadline {
			t.Errorf("%s 没有 ctx 超时", name)
			return
		}
		if got.remaining > want || got.remaining < want-time.Second {
			t.Errorf("%s 剩余 %v，应约为 %v", name, got.remaining, want)
		}
	}
	checkDeadline("auth/state", state, 8*time.Second)
	checkDeadline("轮询令牌", token, 4*time.Second)
	checkDeadline("取账号", account, 4*time.Second)
	checkDeadline("refresh", refresh, 8*time.Second)
	if !seenCheck {
		t.Fatal("刷新后应做一次账号核对")
	}
	checkDeadline("刷新后账号核对", check, 4*time.Second)
}

// 国内版 refresh 502：钉 IP 重发（换 IP 条件与 auth/state 相同）。
func TestRefreshFailoverRetryOn503(t *testing.T) {
	f := &loginFoFake{handler: func(path string, pinned bool, _ *http.Request) (*http.Response, error) {
		switch path {
		case loginFoRefreshPath:
			if !pinned {
				return jsonResp(http.StatusServiceUnavailable, `{"code":503}`), nil
			}
			return jsonResp(http.StatusOK, loginFoRefreshBody), nil
		case loginFoAccountPath:
			return jsonResp(http.StatusOK, loginFoAccountBody), nil
		}
		return jsonResp(http.StatusNotFound, `{}`), nil
	}}
	c := loginFoClient(f)
	c.ChatBaseCN = cnProductEndpoint
	a := &auth.Auth{UID: "u-1", AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 111}
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	reqs := f.seen()
	if len(reqs) < 2 || reqs[0].pinned || !reqs[1].pinned {
		t.Fatalf("请求 = %+v，应为 refresh 503 后钉 IP 重发", reqs)
	}
}
