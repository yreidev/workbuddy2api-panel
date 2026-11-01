// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

// 第八轮补齐：handler.go、logging.go、resolve_model.go 的剩余分支。

type fullTimeoutErr struct{}

func (fullTimeoutErr) Error() string   { return "i/o timeout" }
func (fullTimeoutErr) Timeout() bool   { return true }
func (fullTimeoutErr) Temporary() bool { return true }

func fullSSEResp(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func fullJSONResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func fullClient(rt func(*http.Request) (*http.Response, error)) *upstream.Client {
	return &upstream.Client{
		HTTP:          &http.Client{Transport: roundTripFunc(rt)},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
}

func fastRotate(t *testing.T) {
	t.Helper()
	old := rotateBackoffBase
	rotateBackoffBase = 0
	t.Cleanup(func() { rotateBackoffBase = old })
}

func fullErrorBody(t *testing.T, rec *httptest.ResponseRecorder) (msg, code, hint string) {
	t.Helper()
	var body struct {
		Error struct {
			Message     string `json:"message"`
			Code        string `json:"code"`
			GatewayHint string `json:"gateway_hint"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %v (%s)", err, rec.Body.String())
	}
	return body.Error.Message, body.Error.Code, body.Error.GatewayHint
}

func TestFull8ServeHTTPStatusDefault(t *testing.T) {
	rl := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{RequestLog: rl})
	// 处理器什么都没写就返回时，net/http 会补一个 200，请求日志也按 200 记。
	h.mux = http.NewServeMux()
	h.mux.HandleFunc("POST /v1/chat/completions", func(http.ResponseWriter, *http.Request) {})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost,
		"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)))
	snap := rl.Snapshot()
	if snap.Completed != 1 || len(snap.Recent) != 1 || snap.Recent[0].Status != http.StatusOK {
		t.Fatalf("请求日志 = %+v", snap)
	}
}

func TestFull8BareModelEntriesPlain(t *testing.T) {
	out := bareModelEntries([]map[string]any{{"id": "plain"}})
	if len(out) != 0 {
		t.Fatalf("无前缀条目不应生成裸名: %+v", out)
	}
}

func TestFull8ChatUsageTotalFallback(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10}}\n\n" +
		"data: [DONE]\n\n"
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, body, true })
	u := usage.New("")
	h := NewHandler(Config{Pool: p, Upstream: up, Usage: u})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	st, _ := p.Status("u1")
	if st.TokenUsage.PromptTokens != 10 {
		t.Fatalf("prompt tokens = %d", st.TokenUsage.PromptTokens)
	}
	if snap := u.Snapshot(0, nil); snap.Totals.Requests != 1 {
		t.Fatalf("usage requests = %d", snap.Totals.Requests)
	}
}

func TestFull8ChatCrossRoutedRate(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	cn := &auth.Auth{UID: "cn1", AccessToken: "at", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	gl := &auth.Auth{UID: "g1", AccessToken: "at", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
	p := testPoolWith(cn, gl)
	p.SetCredits("cn1", 100, 0)
	p.SetCredits("g1", 1000, 0)
	fake := &crossRealmUpstream{}
	up := fake.client(t, []string{"m-both"}, []string{"m-both"})
	if _, err := up.FetchModels(cn); err != nil {
		t.Fatalf("cn 目录: %v", err)
	}
	if _, err := up.FetchModels(gl); err != nil {
		t.Fatalf("global 目录: %v", err)
	}
	u := usage.New("")
	h := NewHandler(Config{
		Pool: p, Upstream: up, Usage: u, GlobalEnabled: true,
		Live: livecfg.New(livecfg.Snapshot{CrossRealm: livecfg.CrossRealmMerge}),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m-both","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if snap := u.Snapshot(0, nil); snap.Totals.Requests != 1 {
		t.Fatalf("跨域请求应记录用量: %+v", snap.Totals)
	}
}

func TestFull8ChatConvReqHeader(t *testing.T) {
	var got string
	up := fullClient(func(r *http.Request) (*http.Response, error) {
		got = r.Header.Get("X-Conversation-Request-ID")
		return fullSSEResp(sseOK), nil
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("X-Conversation-Request-ID", "conv-req-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if got != "conv-req-1" {
		t.Fatalf("上游收到 X-Conversation-Request-ID = %q", got)
	}
}

func TestFull8ChatAcquireFailure(t *testing.T) {
	fastRotate(t)
	old := acquireInFlight
	acquireInFlight = func(*pool.Pool, string) bool { return false }
	t.Cleanup(func() { acquireInFlight = old })

	u1 := &auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}
	u2 := &auth.Auth{UID: "u2", AccessToken: "at", ExpiresAt: 9999999999}
	p := testPoolWith(u1, u2)
	store := newBindStore()
	sess := session.New(session.Config{
		TTL: time.Minute, Store: store,
		Available:         p.AvailableUIDs,
		AvailableForModel: p.WeightedAvailableUIDsForModelRealm,
	})
	sess.Bind("conv-1", "u1")
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{Pool: p, Upstream: up, Session: sess})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"conversation_id":"conv-1"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if store.delCnt == 0 {
		t.Fatal("粘性号被抢时应解绑")
	}
}

func TestFull8ChatRefreshSessionDead(t *testing.T) {
	fastRotate(t)
	up := fullClient(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/v2/plugin/auth/token/refresh") {
			return fullJSONResp(401, `{"code":12153,"msg":"Offline user session not found"}`), nil
		}
		return fullSSEResp(sseOK), nil
	})
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	p := testPoolWith(a)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	st, _ := p.Status("u1")
	if !st.Disabled {
		t.Fatalf("session dead 应禁用账号: %+v", st)
	}
}

func TestFull8ChatRefreshServerError(t *testing.T) {
	fastRotate(t)
	up := fullClient(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/v2/plugin/auth/token/refresh") {
			return fullJSONResp(500, `{"code":10000,"msg":"boom"}`), nil
		}
		return fullSSEResp(sseOK), nil
	})
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	p := testPoolWith(a)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	st, _ := p.Status("u1")
	if st.ErrTotal != 1 {
		t.Fatalf("refresh 5xx 应 NoteError: %+v", st)
	}
}

func TestFull8ChatRefreshCanceled(t *testing.T) {
	fastRotate(t)
	up := fullClient(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/v2/plugin/auth/token/refresh") {
			return fullJSONResp(500, `{"code":10000,"msg":"boom"}`), nil
		}
		return fullSSEResp(sseOK), nil
	})
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	p := testPoolWith(a)
	h := NewHandler(Config{Pool: p, Upstream: up})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestFull8ChatRefreshSaveError(t *testing.T) {
	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	up := fullClient(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/v2/plugin/auth/token/refresh") {
			return fullJSONResp(200, `{"code":0,"data":{"accessToken":"new","refreshToken":"rt2","expiresIn":3600}}`), nil
		}
		if strings.HasSuffix(r.URL.Path, "/v2/plugin/account") { // 刷新后核对账号
			return fullJSONResp(200, `{"code":0,"data":{"uid":"u1"}}`), nil
		}
		return fullSSEResp(sseOK), nil
	})
	a := &auth.Auth{
		UID: "u1", AccessToken: "at", RefreshToken: "rt",
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
		FilePath:  filepath.Join(t.TempDir(), "missing", "auth.json"),
	}
	p := testPoolWith(a)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(buf.String(), "save auth failed") {
		t.Fatalf("应记录落盘失败日志，实际 %q", buf.String())
	}
}

func TestFull8ChatUpstreamTimeout(t *testing.T) {
	up := fullClient(func(*http.Request) (*http.Response, error) { return nil, fullTimeoutErr{} })
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if _, code, _ := fullErrorBody(t, rec); code != "upstream_timeout" {
		t.Fatalf("code = %q", code)
	}
	if !isUpstreamTimeout(context.DeadlineExceeded, false) {
		t.Fatal("DeadlineExceeded 应判超时")
	}
}

func TestFull8ChatImmediateErrors(t *testing.T) {
	fastRotate(t)
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{"prompt too long", 400, `{"code":11115,"msg":"prompt is too long: 200000 tokens > 128000"}`, 400, "prompt_too_long", "prompt is too long"},
		{"image invalid", 400, `{"code":11135,"msg":"invalid image data"}`, 400, "image_invalid", "invalid image data"},
		{"bad params", 400, `{"code":11101,"msg":"Unmarshal chat params failed"}`, 400, "bad_params", "Unmarshal chat params failed"},
		{"model blocked", 400, `{"code":11102,"msg":"model not found on this backend"}`, 400, "model_unavailable", "model not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := newFakeUpstream(t, func(string) (int, string, bool) { return tc.status, tc.body, false })
			p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
			h := NewHandler(Config{Pool: p, Upstream: up})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
			if rec.Code != tc.wantStatus {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
			}
			msg, code, _ := fullErrorBody(t, rec)
			if code != tc.wantCode || !strings.Contains(msg, tc.wantMsg) {
				t.Fatalf("msg=%q code=%q", msg, code)
			}
		})
	}
}

func TestFull8ChatWafIPFailFast(t *testing.T) {
	fastRotate(t)
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 403, "<html>waf blocked</html>", false })
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if _, code, _ := fullErrorBody(t, rec); code != "waf_ip_blocked" {
		t.Fatalf("code = %q", code)
	}
}

func TestFull8ChatSoftRateEndpoint(t *testing.T) {
	fastRotate(t)
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 429, `{"code":10000,"msg":"too many requests"}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if _, code, _ := fullErrorBody(t, rec); code != "rate_limit_exceeded" {
		t.Fatalf("code = %q", code)
	}
}

func TestFull8ChatModelBlockedHint(t *testing.T) {
	a := &auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}
	p := testPoolWith(a)
	p.BlockModelBackoff("u1", "m", "11102 model not found")
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		t.Error("模型已全池阻塞，不应再打上游")
		return 500, "", false
	})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	msg, code, hint := fullErrorBody(t, rec)
	if code != "model_unavailable" || !strings.Contains(msg, "model is unavailable on every account") {
		t.Fatalf("msg=%q code=%q", msg, code)
	}
	if !strings.Contains(hint, "model_blocked") || !strings.Contains(hint, "11102") {
		t.Fatalf("hint = %q", hint)
	}
}

func TestFull8ChatEmptyStream(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, "", true })
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if !strings.Contains(rec.Body.String(), "empty upstream stream") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

type fullFailWriter struct{ h http.Header }

func (w *fullFailWriter) Header() http.Header {
	if w.h == nil {
		w.h = http.Header{}
	}
	return w.h
}
func (w *fullFailWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }
func (w *fullFailWriter) WriteHeader(int)           {}

func TestFull8ChatStreamWriteFailure(t *testing.T) {
	u1 := &auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}
	p := testPoolWith(u1)
	store := newBindStore()
	sess := session.New(session.Config{
		TTL: time.Minute, Store: store,
		Available:         p.AvailableUIDs,
		AvailableForModel: p.WeightedAvailableUIDsForModelRealm,
	})
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	u := usage.New("")
	h := NewHandler(Config{Pool: p, Upstream: up, Session: sess, Usage: u})
	w := &fullFailWriter{}
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"conversation_id":"conv-1"}`)))
	if snap := u.Snapshot(0, nil); snap.Totals.Requests != 1 {
		t.Fatalf("写失败仍应记录一次尝试: %+v", snap.Totals)
	}
	if uid, ok := sess.ResolveForModel("conv-1", "", "", nil); !ok || uid != "u1" {
		t.Fatalf("写失败分支应保持绑定: uid=%q ok=%v", uid, ok)
	}
}

func TestFull8ChatStreamCacheTokens(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1200,\"completion_tokens\":5,\"total_tokens\":1205,\"prompt_cache_hit_tokens\":0,\"prompt_cache_miss_tokens\":1200}}\n\n" +
		"data: [DONE]\n\n"
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, body, true })
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	u := usage.New("")
	h := NewHandler(Config{Pool: p, Upstream: up, Usage: u})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"cache-model","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if snap := u.Snapshot(0, nil); snap.Totals.CacheMissTokens != 1200 {
		t.Fatalf("cache miss = %d", snap.Totals.CacheMissTokens)
	}
}

func TestFull8ChatRepairRejectedWarn(t *testing.T) {
	pipe := "｜｜"
	block := "<" + pipe + "DSML" + pipe + " calls>" +
		"<" + pipe + "DSML" + pipe + ` invoke name="bad name">` +
		"</" + pipe + "DSML" + pipe + " invoke></" + pipe + "DSML" + pipe + " calls>"
	payload, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": "讨论：" + block}}},
	})
	body := "data: " + string(payload) + "\n\ndata: [DONE]\n\n"
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, body, true })
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "bad name") {
		t.Fatalf("拒绝的块应原文透传: %q", rec.Body.String())
	}
}

func TestFull8ChatNonStreamAggError(t *testing.T) {
	up := fullClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: errReadBody{}}, nil
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if _, code, _ := fullErrorBody(t, rec); code != "upstream_parse" {
		t.Fatalf("code = %q", code)
	}
}

func TestFull8ChatNonStreamCacheTokens(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1200,\"completion_tokens\":5,\"total_tokens\":1205,\"prompt_cache_hit_tokens\":900,\"prompt_cache_miss_tokens\":300}}\n\n" +
		"data: [DONE]\n\n"
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, body, true })
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	u := usage.New("")
	h := NewHandler(Config{Pool: p, Upstream: up, Usage: u})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"cache-model","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if snap := u.Snapshot(0, nil); snap.Totals.CacheHitTokens != 900 || snap.Totals.CacheMissTokens != 300 {
		t.Fatalf("cache = %+v", snap.Totals)
	}
}

// --- logging.go ------------------------------------------------------------

func TestFull8LoggingEdges(t *testing.T) {
	withChatLog(t)
	var buf bytes.Buffer
	oldOut := chatLogOut
	SetChatLogOutput(&buf)
	t.Cleanup(func() { SetChatLogOutput(oldOut) })

	s := &chatStat{toks: -1}
	s.done()
	s.done() // 第二次直接返回

	cases := []struct {
		body, bare string
		want       string
	}{
		{"", "x", ""},
		{`{"model":"m"}`, "", `{"model":"m"}`},
		{`{bad`, "x", `{bad`},
		{`{"model":5}`, "x", `{"model":5}`},
		{`{"model":"m"}`, "m", `{"model":"m"}`},
		{`{"model":"m"}`, "x", `{"model":"x"}`},
	}
	for _, tc := range cases {
		if got := string(rewriteModel([]byte(tc.body), tc.bare)); got != tc.want {
			t.Errorf("rewriteModel(%q,%q) = %q，应为 %q", tc.body, tc.bare, got, tc.want)
		}
	}

	delta := usageDeltaFromResponse(map[string]any{"usage": map[string]any{
		"prompt_tokens": float64(1), "completion_tokens": float32(2),
		"total_tokens": json.Number("3"), "credit": uint(4),
	}})
	if delta.PromptTokens != 1 || delta.CompletionTokens != 2 || delta.TotalTokens != 3 {
		t.Fatalf("delta = %+v", delta)
	}
	delta = usageDeltaFromResponse(map[string]any{"usage": map[string]any{
		"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": int64(3),
	}})
	if delta.PromptTokens != 1 || delta.CompletionTokens != 2 || delta.TotalTokens != 3 {
		t.Fatalf("整型 delta = %+v", delta)
	}
	if d := usageDeltaFromResponse(map[string]any{"usage": map[string]any{"prompt_tokens": 1}}); !d.HasPromptTokens || d.HasCompletionTokens || d.HasTotalTokens {
		t.Fatalf("缺失字段应保持 false: %+v", d)
	}
	if d := usageDeltaFromResponse(map[string]any{}); d.HasPromptTokens {
		t.Fatalf("无 usage 应为空: %+v", d)
	}

	(*requestTrace)(nil).captureClientInfo(nil)
	if requestTraceFrom(nil) != nil {
		t.Fatal("nil 请求应返回 nil trace")
	}
	req := httptest.NewRequest(http.MethodGet, "https://x/", nil)
	req.RemoteAddr = "1.2.3.4"
	if got := clientIPForLog(req); got != "1.2.3.4" {
		t.Fatalf("无端口 RemoteAddr = %q", got)
	}
	req.Header.Set("X-Forwarded-For", "5.6.7.8")
	if got := clientIPForLog(req); got != "5.6.7.8" {
		t.Fatalf("XFF = %q", got)
	}
	if got := dashIfEmpty("  "); got != "-" {
		t.Fatalf("dashIfEmpty = %q", got)
	}

	tr := &requestTrace{}
	if e := tr.event(200, nil); e.Outcome != reqlog.OutcomeSuccess {
		t.Fatalf("200 outcome = %q", e.Outcome)
	}
	if e := tr.event(500, nil); e.Outcome != reqlog.OutcomeHTTPError {
		t.Fatalf("500 outcome = %q", e.Outcome)
	}

	rec := httptest.NewRecorder()
	obs := &responseObserver{ResponseWriter: rec}
	obs.WriteHeader(201)
	obs.WriteHeader(500)
	if obs.status != 201 || rec.Code != 201 {
		t.Fatalf("WriteHeader 只应记第一次: obs=%d rec=%d", obs.status, rec.Code)
	}
	if obs.Unwrap() != rec {
		t.Fatal("Unwrap 应返回底层 writer")
	}

	toksRows := []struct {
		status  int
		outcome string
	}{
		{200, ""},
		{500, ""},
	}
	for _, tc := range toksRows {
		logChatRowEx(0, 0, "m", "sync", "uid12345", "昵称", tc.status, 10, "rid-1", tc.outcome, 1, 0, false, "", "")
	}
	if out := buf.String(); !strings.Contains(out, "0.0tok/s") || !strings.Contains(out, "out=success") || !strings.Contains(out, "out=http_error") {
		t.Fatalf("日志行 = %q", out)
	}

}

func TestFull8StatsReaderEdges(t *testing.T) {
	r := newChatStatsReaderSince(io.NopCloser(strings.NewReader("data: {\"usage\":{\"prompt_tokens\":1}}\n")), time.Now())
	one := make([]byte, 1)
	if n, err := r.Read(one); n != 1 || err != nil {
		t.Fatalf("首次读 = (%d,%v)", n, err)
	}
	if n, err := r.Read(one); n != 1 || err != nil {
		t.Fatalf("pend 读 = (%d,%v)", n, err)
	}
	if s := (&chatStatsReader{hasCache: true, cacheHit: 10}); true {
		hit, miss, ok := s.CacheTokens()
		if !ok || hit != 10 || miss != 0 {
			t.Fatalf("CacheTokens = (%d,%d,%v)", hit, miss, ok)
		}
	}
	r2 := newChatStatsReaderSince(io.NopCloser(strings.NewReader("data: {\"error\":{\"message\":\"x\"},\"usage\":{\"prompt_tokens\":1}}\n")), time.Now())
	line := make([]byte, 4096)
	_, _ = r2.Read(line)
	if !r2.SawErrorFrame() {
		t.Fatal("usage 帧携带 error 时应标记 errorFrame")
	}
}

func TestFull8ResolveModelBadPrefix(t *testing.T) {
	if realm, bare := resolveModel("foo:bar"); realm != "cn" || bare != "foo:bar" {
		t.Fatalf("未知前缀 = (%q,%q)", realm, bare)
	}
}

func TestFull8UsageDeltaUnsupportedType(t *testing.T) {
	d := usageDeltaFromResponse(map[string]any{"usage": map[string]any{"prompt_tokens": "x"}})
	if d.HasPromptTokens {
		t.Fatalf("非数值应忽略: %+v", d)
	}
}

func TestFull8ChatAcquireFailureCanceled(t *testing.T) {
	old := acquireInFlight
	acquireInFlight = func(*pool.Pool, string) bool { return false }
	t.Cleanup(func() { acquireInFlight = old })

	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{Pool: p, Upstream: up})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestFull8ChatTransportCanceled(t *testing.T) {
	fastRotate(t)
	ctx, cancel := context.WithCancel(context.Background())
	up := fullClient(func(*http.Request) (*http.Response, error) {
		cancel() // 上游返回前客户端断开
		return nil, errors.New("transport boom")
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestFull8ChatClassifiedCanceled(t *testing.T) {
	fastRotate(t)
	ctx, cancel := context.WithCancel(context.Background())
	up := fullClient(func(*http.Request) (*http.Response, error) {
		cancel()
		return fullJSONResp(429, `{"code":10000,"msg":"slow down"}`), nil
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestFull8TimeoutWrapped(t *testing.T) {
	if !isUpstreamTimeout(fmt.Errorf("x: %w", context.DeadlineExceeded), false) {
		t.Fatal("包装后的 DeadlineExceeded 应判超时")
	}
}

// fullDeadlineErr 模拟「不实现 net.Error、但 Is(DeadlineExceeded) 为真」的
// 显式 deadline 错误（isUpstreamTimeout 的第三态）。
type fullDeadlineErr struct{}

func (fullDeadlineErr) Error() string        { return "deadline" }
func (fullDeadlineErr) Is(target error) bool { return target == context.DeadlineExceeded }

func TestFull8TimeoutIsDeadline(t *testing.T) {
	if !isUpstreamTimeout(fullDeadlineErr{}, false) {
		t.Fatal("Is(DeadlineExceeded) 的错误应判超时")
	}
}
