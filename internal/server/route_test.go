// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// 号的代理连不上（RouteError）的处理，见 upstream/route.go。

func proxyDown() error {
	return &upstream.RouteError{Proxy: "甲", Err: errors.New("dial tcp 203.0.113.7:8080: connect: connection refused")}
}

func proxyBlocked() error {
	return &upstream.RouteError{Proxy: "乙", Blocked: true, Err: errors.New("号 u1 现在不能出站：代理「乙」已到期")}
}

func chatReq(h *Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	return rec
}

// proxyNames 号走的代理名（Config.ProxyName）：u1、a 走甲，u2、b 走乙。
func proxyNames(uid string) string {
	switch uid {
	case "u1", "a":
		return "甲"
	case "u2", "b":
		return "乙"
	}
	return ""
}

// TestChatRouteErrorRotates 号的代理连不上：不罚号、不喂连败，换个号接着发；请求记录写最后那个号走的代理。
func TestChatRouteErrorRotates(t *testing.T) {
	fastRotate(t)
	up := fullClient(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "Bearer at1" {
			return nil, proxyDown()
		}
		return fullSSEResp(sseOK), nil
	})
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	p.SetCredits("u1", 5000, 0) // u1 先被选中
	rl := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{Pool: p, Upstream: up, RequestLog: rl, ProxyName: proxyNames})
	if rec := chatReq(h); rec.Code != http.StatusOK {
		t.Fatalf("应换号成功: code=%d body=%s", rec.Code, rec.Body)
	}
	if ev := rl.Snapshot().Recent[0]; ev.Proxy != "乙" {
		t.Fatalf("请求记录应写 u2 走的乙: %+v", ev)
	}
	if st, _ := p.Status("u1"); st.ErrTotal != 0 || st.ConsecutiveFails != 0 || st.Cooling {
		t.Fatalf("号的代理连不上不罚号: %+v", st)
	}
}

// TestChatRouteErrorAllAccounts 号的代理都连不上：回 503，客户端看不到代理名；请求记录写上是哪个代理（IP 遮掉），
// 被拦着没发出去的不算一次尝试。
func TestChatRouteErrorAllAccounts(t *testing.T) {
	fastRotate(t)
	up := fullClient(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "Bearer at1" {
			return nil, proxyBlocked()
		}
		return nil, proxyDown()
	})
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	p.SetCredits("u1", 5000, 0)
	rl := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{Pool: p, Upstream: up, RequestLog: rl, MaxRotate: 2})
	rec := chatReq(h)
	msg, code, _ := fullErrorBody(t, rec)
	if rec.Code != http.StatusServiceUnavailable || code != "no_healthy_account" || !strings.Contains(msg, "proxy") || strings.Contains(msg, "甲") {
		t.Fatalf("code=%d msg=%q errcode=%q", rec.Code, msg, code)
	}
	snap := rl.Snapshot()
	if len(snap.Recent) != 1 {
		t.Fatalf("请求日志 = %+v", snap)
	}
	ev := snap.Recent[0]
	if !strings.Contains(ev.Error, "连不上号的代理「甲」") || !strings.Contains(ev.Error, "<IP>:8080") || strings.Contains(ev.Error, "203.0.113.7") || ev.Attempts != 1 {
		t.Fatalf("请求记录应写代理名、遮掉 IP、只记发出去的那次: %+v", ev)
	}
	for _, uid := range []string{"u1", "u2"} {
		if st, _ := p.Status(uid); st.ErrTotal != 0 || st.ConsecutiveFails != 0 {
			t.Fatalf("%s 不应被罚: %+v", uid, st)
		}
	}
}

// TestChatRefreshRouteError 刷新令牌时号的代理连不上：不罚号（对照 TestFull8ChatRefreshServerError）。
func TestChatRefreshRouteError(t *testing.T) {
	fastRotate(t)
	up := fullClient(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/v2/plugin/auth/token/refresh") {
			return nil, proxyDown()
		}
		return fullSSEResp(sseOK), nil
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Minute).Unix()})
	h := NewHandler(Config{Pool: p, Upstream: up})
	if rec := chatReq(h); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if st, _ := p.Status("u1"); st.ErrTotal != 0 || st.Disabled {
		t.Fatalf("刷新时代理连不上不罚号: %+v", st)
	}
}

// TestTestAccountProxyBlocked 被代理池拦着的号测不了：直接报原因，不发请求。
func TestTestAccountProxyBlocked(t *testing.T) {
	u := &livenessUpstream{}
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	p.SetProxyBlocked("u1", "代理「甲」连不上（11-01 09:30 起）")
	h := NewHandler(Config{Pool: p, Upstream: u.client()})
	_, _, err := h.TestAccount(context.Background(), "u1", "m-cheap", false)
	if err == nil || !strings.Contains(err.Error(), "代理「甲」连不上") {
		t.Fatalf("应报拦截原因: %v", err)
	}
	if tokens, _, _ := u.calls(); len(tokens) != 0 {
		t.Fatalf("不应发请求: %v", tokens)
	}
}

// TestSearchRouteError 搜索时号的代理连不上：不罚号，换号；都连不上回 503，说是代理的问题。
func TestSearchRouteError(t *testing.T) {
	down := func() (*http.Response, error) { return nil, proxyDown() }
	rig := &searchRig{reply: map[string]func() (*http.Response, error){"at-a": down}}
	rl := reqlog.New(reqlog.Config{})
	h, _ := newSearchHandler(t, rig, func(c *Config) {
		c.Pool = testPoolWith(
			&auth.Auth{UID: "a", AccessToken: "at-a", ExpiresAt: 9999999999},
			&auth.Auth{UID: "b", AccessToken: "at-b", ExpiresAt: 9999999999},
		)
		c.Pool.SetCredits("a", 5000, 0)
		c.RequestLog, c.ProxyName = rl, proxyNames
	})
	if rec := post(h, "/v1/search/messages", searchMessagesBody); rec.Code != 200 || strings.Join(rig.searches(), ",") != "at-a,at-b" {
		t.Fatalf("应换号成功: code=%d searches=%v body=%s", rec.Code, rig.searches(), rec.Body)
	}
	if ev := rl.Snapshot().Recent[0]; ev.Proxy != "乙" {
		t.Fatalf("请求记录应写 b 走的乙: %+v", ev)
	}
	if st, _ := h.cfg.Pool.Status("a"); st.ConsecutiveFails != 0 || st.ErrTotal != 0 {
		t.Fatalf("不罚号: %+v", st)
	}

	rig2 := &searchRig{reply: map[string]func() (*http.Response, error){"at-a": down, "at-b": down}}
	h2, _ := newSearchHandler(t, rig2, func(c *Config) {
		c.Pool = testPoolWith(
			&auth.Auth{UID: "a", AccessToken: "at-a", ExpiresAt: 9999999999},
			&auth.Auth{UID: "b", AccessToken: "at-b", ExpiresAt: 9999999999},
		)
	})
	rec := post(h2, "/v1/search/messages", searchMessagesBody)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "proxy") || strings.Contains(rec.Body.String(), "甲") {
		t.Fatalf("都连不上应回 503 说代理的问题: code=%d body=%s", rec.Code, rec.Body)
	}
}

// TestSearchWafOtherExit 搜索时一个出口被 WAF 整个拦了：同出口的号不再试，换到别的出口的号。
func TestSearchWafOtherExit(t *testing.T) {
	waf := func() (*http.Response, error) { return textResp(403, "text/html", "<html>blocked</html>"), nil }
	rig := &searchRig{reply: map[string]func() (*http.Response, error){"at-a": waf, "at-b": waf, "at-d": waf}}
	h, _ := newSearchHandler(t, rig, func(c *Config) {
		c.Pool = testPoolWith(
			&auth.Auth{UID: "a", AccessToken: "at-a", ExpiresAt: 9999999999},
			&auth.Auth{UID: "b", AccessToken: "at-b", ExpiresAt: 9999999999},
			&auth.Auth{UID: "c", AccessToken: "at-c", ExpiresAt: 9999999999},
			&auth.Auth{UID: "d", AccessToken: "at-d", ExpiresAt: 9999999999},
		)
		for uid, cr := range map[string]int64{"a": 5000, "b": 4000, "d": 3000, "c": 1000} {
			c.Pool.SetCredits(uid, cr, 0)
		}
		c.Pool.SetExitKey(func(uid string) (string, string) {
			if uid == "c" {
				return "ip:B", "代理「乙」"
			}
			return "ip:A", "代理「甲」"
		})
	})
	if rec := post(h, "/v1/search/messages", searchMessagesBody); rec.Code != 200 || strings.Join(rig.searches(), ",") != "at-a,at-b,at-c" {
		t.Fatalf("出口 A 被拦后应换到 c: code=%d searches=%v body=%s", rec.Code, rig.searches(), rec.Body)
	}
}

// TestRouteErrorClientGone 号的代理连不上、换号退避时客户端走了：对话和搜索都不再换号。
func TestRouteErrorClientGone(t *testing.T) {
	old := rotateBackoffBase
	t.Cleanup(func() { rotateBackoffBase = old })
	newPool := func() []*auth.Auth {
		return []*auth.Auth{
			{UID: "a", AccessToken: "at-a", ExpiresAt: 9999999999},
			{UID: "b", AccessToken: "at-b", ExpiresAt: 9999999999},
		}
	}

	rotateBackoffBase = time.Hour // 退避封顶 8 秒，靠客户端断开叫醒
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	up := fullClient(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		time.AfterFunc(50*time.Millisecond, cancel)
		return nil, proxyDown()
	})
	h := NewHandler(Config{Pool: testPoolWith(newPool()...), Upstream: up})
	start := time.Now()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)).WithContext(ctx))
	if n, took := calls.Load(), time.Since(start); n != 1 || took > 5*time.Second {
		t.Fatalf("对话：退避里断开应立刻停、不换号: 发了 %d 次，用时 %s", n, took)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	down := func() (*http.Response, error) {
		time.AfterFunc(50*time.Millisecond, cancel2)
		return nil, proxyDown()
	}
	rig := &searchRig{reply: map[string]func() (*http.Response, error){"at-a": down, "at-b": down}}
	hs, _ := newSearchHandler(t, rig, func(c *Config) { c.Pool = testPoolWith(newPool()...) })
	rotateBackoffBase = time.Hour
	start = time.Now()
	postCtx(hs, ctx2, "/v1/search/messages", searchMessagesBody)
	if n, took := len(rig.searches()), time.Since(start); n != 1 || took > 5*time.Second {
		t.Fatalf("搜索：退避里断开应立刻停、不换号: 搜索 %d 次，用时 %s", n, took)
	}
}
