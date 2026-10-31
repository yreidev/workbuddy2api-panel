// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
)

const claudeBody = `{"model":"glm-5.2","max_tokens":1024,"stream":%s,
"system":[{"type":"text","text":"You are Claude Code"}],
"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}]}`

const codexBody = `{"model":"glm-5.2","instructions":"You are Codex","stream":true,"store":false,
"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]}],
"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}}}},
{"type":"custom","name":"apply_patch","description":"patch","format":{"type":"grammar","syntax":"lark","definition":"start: x"}}]}`

func chunk(delta, finish string) string {
	f := ""
	if finish != "" {
		f = `,"finish_reason":"` + finish + `"`
	}
	return `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1753600000,"model":"glm-5.2","choices":[{"index":0,"delta":` + delta + f + `}]}` + "\n\n"
}

const usageTail = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1753600000,"model":"glm-5.2","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}` + "\n\ndata: [DONE]\n\n"

func toolSSE(name, args string) string {
	argsJSON, _ := json.Marshal(args)
	return chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"`+name+`","arguments":""}}]}`, "") +
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":`+string(argsJSON)+`}}]}`, "") +
		chunk(`{}`, "tool_calls") + usageTail
}

func bridgeHandler(t *testing.T, status int, body string, isStream bool) (*Handler, *int) {
	t.Helper()
	calls := new(int)
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		*calls++
		return status, body, isStream
	})
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})
	return h, calls
}

func post(h http.Handler, path, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func anthropicErr(t *testing.T, rec *httptest.ResponseRecorder) (typ, msg string) {
	t.Helper()
	var e struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Type != "error" {
		t.Fatalf("不是 Anthropic 错误格式：code=%d body=%s", rec.Code, rec.Body)
	}
	return e.Error.Type, e.Error.Message
}

func TestMessagesStream(t *testing.T) {
	h, _ := bridgeHandler(t, 200, sseOK, true)
	rec := post(h, "/v1/messages", strings.Replace(claudeBody, "%s", "true", 1))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("code=%d ct=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{"event: message_start", "event: content_block_delta", "你好", "event: message_stop"} {
		if !strings.Contains(body, want) {
			t.Errorf("缺 %q：%s", want, body)
		}
	}
	if strings.Contains(body, "[DONE]") || strings.Contains(body, "chat.completion.chunk") {
		t.Errorf("聊天格式的帧漏给了客户端：%s", body)
	}
	st, _ := h.cfg.Pool.Status("u1")
	if st.TokenUsage.RequestCount != 1 || st.TokenUsage.TotalTokens != 2 {
		t.Errorf("用量没走原链路记账：%+v", st.TokenUsage)
	}
}

func TestMessagesNonStream(t *testing.T) {
	h, _ := bridgeHandler(t, 200, sseOK, true)
	rec := post(h, "/v1/messages", strings.Replace(claudeBody, "%s", "false", 1))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("code=%d ct=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	var msg struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatalf("body=%s", rec.Body)
	}
	if msg.Type != "message" || msg.Role != "assistant" || len(msg.Content) != 1 || msg.Content[0].Text != "你好" ||
		msg.StopReason != "end_turn" || msg.Usage.InputTokens != 1 || msg.Usage.OutputTokens != 1 {
		t.Errorf("message=%+v", msg)
	}
}

func TestMessagesToolUse(t *testing.T) {
	h, _ := bridgeHandler(t, 200, toolSSE("Bash", `{"command":"ls"}`), true)
	rec := post(h, "/v1/messages", strings.Replace(claudeBody, "%s", "true", 1))
	body := rec.Body.String()
	for _, want := range []string{`"type":"tool_use"`, `"name":"Bash"`, `input_json_delta`, `"stop_reason":"tool_use"`, `"input_tokens":7`} {
		if !strings.Contains(body, want) {
			t.Errorf("缺 %q：%s", want, body)
		}
	}
}

func TestMessagesUpstreamErrors(t *testing.T) {
	h, calls := bridgeHandler(t, 400, `{"code":11101,"msg":"Unmarshal chat params failed"}`, false)
	rec := post(h, "/v1/messages", strings.Replace(claudeBody, "%s", "true", 1))
	typ, msg := anthropicErr(t, rec)
	if rec.Code != 400 || typ != "invalid_request_error" || !strings.Contains(msg, "11101") || *calls != 1 {
		t.Errorf("code=%d type=%s msg=%s calls=%d", rec.Code, typ, msg, *calls)
	}

	// 没有可用账号：503 → overloaded_error，Claude Code 会自己退避重试
	h = NewHandler(Config{Pool: testPoolWith(), Upstream: newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })})
	rec = post(h, "/v1/messages", strings.Replace(claudeBody, "%s", "false", 1))
	if typ, _ := anthropicErr(t, rec); rec.Code != 503 || typ != "overloaded_error" {
		t.Errorf("code=%d type=%s", rec.Code, typ)
	}
}

func TestMessagesStreamErrorFrame(t *testing.T) {
	sse := chunk(`{"role":"assistant","content":"部分"}`, "") +
		`data: {"error":{"message":"upstream exploded","type":"api_error","code":"server_error"}}` + "\n\n" +
		chunk(`{"content":"之后"}`, "") + "data: [DONE]\n\n"
	h, _ := bridgeHandler(t, 200, sse, true)
	body := post(h, "/v1/messages", strings.Replace(claudeBody, "%s", "true", 1)).Body.String()
	if !strings.Contains(body, "部分") || !strings.Contains(body, "event: error") || !strings.Contains(body, "upstream exploded") {
		t.Errorf("body=%s", body)
	}
	if strings.Contains(body, "之后") || strings.Contains(body, "message_stop") {
		t.Errorf("错误之后不该再有内容或正常收尾：%s", body)
	}
}

func TestMessagesBadRequests(t *testing.T) {
	h, calls := bridgeHandler(t, 200, sseOK, true)
	for _, body := range []string{`{not json`, `{"messages":[]}`} {
		rec := post(h, "/v1/messages", body)
		if typ, _ := anthropicErr(t, rec); rec.Code != 400 || typ != "invalid_request_error" {
			t.Errorf("body=%s → code=%d type=%s", body, rec.Code, typ)
		}
	}
	if *calls != 0 {
		t.Errorf("坏请求不该打上游：calls=%d", *calls)
	}
}

func TestBridgeAuth(t *testing.T) {
	h, _ := bridgeHandler(t, 200, sseOK, true)
	h.cfg.Live = livecfg.New(livecfg.Snapshot{APIKey: "sk-test"})
	body := strings.Replace(claudeBody, "%s", "false", 1)
	if rec := post(h, "/v1/messages", body, "X-Api-Key", "sk-test"); rec.Code != 200 {
		t.Errorf("x-api-key 正确应放行：code=%d body=%s", rec.Code, rec.Body)
	}
	if rec := post(h, "/v1/messages", body, "Authorization", "Bearer sk-test"); rec.Code != 200 {
		t.Errorf("Bearer 正确应放行：code=%d", rec.Code)
	}
	rec := post(h, "/v1/messages", body, "X-Api-Key", "sk-wrong")
	if typ, _ := anthropicErr(t, rec); rec.Code != 401 || typ != "authentication_error" {
		t.Errorf("code=%d type=%s", rec.Code, typ)
	}
	rec = post(h, "/v1/responses", codexBody)
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), `"invalid_api_key"`) {
		t.Errorf("Responses 缺密钥：code=%d body=%s", rec.Code, rec.Body)
	}
}

// TestDoubledV1 Base URL 多带 /v1 时不打上游、不校验密钥，回 400 说明怎么改：Claude Code 的路径用
// Anthropic 格式（404 会被它说成模型不存在），其余用 OpenAI 格式。
func TestDoubledV1(t *testing.T) {
	h, calls := bridgeHandler(t, 200, sseOK, true)
	h.cfg.Live = livecfg.New(livecfg.Snapshot{APIKey: "sk-test"})
	for _, path := range []string{"/v1/v1/messages", "/v1/v1/messages/count_tokens"} {
		rec := post(h, path, strings.Replace(claudeBody, "%s", "false", 1))
		typ, msg := anthropicErr(t, rec)
		if rec.Code != 400 || typ != "invalid_request_error" || !strings.Contains(msg, "Base URL 不要带 /v1") ||
			!strings.Contains(msg, path) || !strings.Contains(msg, "ANTHROPIC_BASE_URL") {
			t.Errorf("%s → code=%d type=%s msg=%s", path, rec.Code, typ, msg)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/v1/chat/completions"}, {http.MethodPost, "/v1/v1/responses"}, {http.MethodGet, "/v1/v1/models"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`)))
		var e struct {
			Error struct{ Message, Code string } `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || rec.Code != 400 || e.Error.Code != "invalid_base_url" ||
			!strings.Contains(e.Error.Message, "多带了一个 /v1") || !strings.Contains(e.Error.Message, tc.path) {
			t.Errorf("%s %s → code=%d body=%s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
	if *calls != 0 {
		t.Errorf("不该打上游：calls=%d", *calls)
	}
}

func TestBridgeUnknownModel(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	resetModelsCache(t)

	cu := &crossRealmUpstream{}
	up := cu.client(t, []string{"glm-5.2"}, []string{"gpt-5.5"})
	cn := &auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	gl := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
	h := NewHandler(Config{Pool: testPoolWith(cn, gl), Upstream: up, GlobalEnabled: true})
	claude := func(model string) string {
		return strings.Replace(strings.Replace(claudeBody, "%s", "false", 1), `"glm-5.2"`, `"`+model+`"`, 1)
	}

	// 目录还没拉到：放行，交给上游判断
	if rec := post(h, "/v1/messages", claude("claude-sonnet-4-5")); rec.Code == 404 {
		t.Fatalf("目录未知时不该拦：%s", rec.Body)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/models", nil)) // 拉目录
	before := len(cu.chats)

	rec := post(h, "/v1/messages", claude("claude-sonnet-4-5"))
	typ, msg := anthropicErr(t, rec)
	if rec.Code != 404 || typ != "not_found_error" || !strings.Contains(msg, "ANTHROPIC_DEFAULT_SONNET_MODEL") {
		t.Errorf("code=%d type=%s msg=%s", rec.Code, typ, msg)
	}
	rec = post(h, "/v1/responses", strings.Replace(codexBody, `"glm-5.2"`, `"gpt-9"`, 1))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "config.toml") {
		t.Errorf("Codex 未知模型：code=%d body=%s", rec.Code, rec.Body)
	}
	if rec := post(h, "/v1/messages", claude("cn:gpt-5.5")); rec.Code != 404 {
		t.Errorf("cn 前缀但国内版没有：code=%d", rec.Code)
	}
	if len(cu.chats) != before {
		t.Errorf("被拦的请求不该打上游：%d → %d", before, len(cu.chats))
	}
	for _, model := range []string{"glm-5.2", "gpt-5.5", "cn:glm-5.2", "global:whatever"} {
		if rec := post(h, "/v1/messages", claude(model)); rec.Code == 404 {
			t.Errorf("%s 不该被拦：%s", model, rec.Body)
		}
	}
}

func TestResponsesStream(t *testing.T) {
	h, _ := bridgeHandler(t, 200, chunk(`{"role":"assistant","content":"你好"}`, "")+chunk(`{}`, "stop")+usageTail, true)
	rec := post(h, "/v1/responses", codexBody, "Authorization", "Bearer any")
	body := rec.Body.String()
	for _, want := range []string{"event: response.created", "event: response.output_text.delta", "你好", "event: response.completed", `"input_tokens":7`} {
		if !strings.Contains(body, want) {
			t.Errorf("缺 %q：%s", want, body)
		}
	}
	if strings.Contains(body, "[DONE]") {
		t.Errorf("[DONE] 漏给了客户端：%s", body)
	}
}

// Codex 的 apply_patch 是自定义工具：出站翻成带 input 字段的函数，回程要还原成 custom_tool_call。
func TestResponsesCustomToolRoundTrip(t *testing.T) {
	var sent []byte
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, toolSSE("apply_patch", `{"input":"*** Begin Patch\n*** End Patch"}`), true
	})
	inner := up.HTTP.Transport
	up.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent, _ = readAll(r)
		return inner.RoundTrip(r)
	})
	h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}), Upstream: up})
	body := post(h, "/v1/responses", codexBody).Body.String()
	if !bytes.Contains(sent, []byte(`"name":"apply_patch"`)) || bytes.Contains(sent, []byte(`"custom"`)) {
		t.Errorf("出站应是普通函数工具：%s", sent)
	}
	if !strings.Contains(body, `"type":"custom_tool_call"`) || !strings.Contains(body, `*** Begin Patch`) {
		t.Errorf("回程没还原成 custom_tool_call：%s", body)
	}
}

func TestResponsesStreamErrorFrame(t *testing.T) {
	sse := chunk(`{"role":"assistant","content":"部分"}`, "") +
		`data: {"error":{"message":"quota","code":14018}}` + "\n\ndata: [DONE]\n\n"
	h, _ := bridgeHandler(t, 200, sse, true)
	body := post(h, "/v1/responses", codexBody).Body.String()
	if !strings.Contains(body, "event: response.failed") || !strings.Contains(body, `"code":"14018"`) || strings.Contains(body, "response.completed") {
		t.Errorf("body=%s", body)
	}
}

func TestResponsesUpstreamError(t *testing.T) {
	h, _ := bridgeHandler(t, 400, `{"code":11101,"msg":"bad"}`, false)
	rec := post(h, "/v1/responses", codexBody)
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if rec.Code != 400 || json.Unmarshal(rec.Body.Bytes(), &e) != nil || !strings.Contains(e.Error.Message, "11101") {
		t.Errorf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestCountTokens(t *testing.T) {
	h, calls := bridgeHandler(t, 200, sseOK, true)
	rec := post(h, "/v1/messages/count_tokens",
		`{"model":"glm-5.2","system":"abcd","messages":[{"role":"user","content":[{"type":"text","text":"你好","cache_control":{"type":"ephemeral"}}]}]}`)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"input_tokens":3}` || *calls != 0 {
		t.Errorf("code=%d body=%s calls=%d", rec.Code, rec.Body, *calls)
	}
	if rec := post(h, "/v1/messages/count_tokens", `nope`); rec.Code != 400 {
		t.Errorf("坏 JSON：code=%d", rec.Code)
	}
}

func TestBridgeRequestLogPath(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	rl := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}), Upstream: up, RequestLog: rl})
	post(h, "/v1/messages", strings.Replace(claudeBody, "%s", "true", 1))
	post(h, "/v1/responses", codexBody)
	s := rl.Snapshot()
	if len(s.Recent) != 2 {
		t.Fatalf("recent=%+v", s.Recent)
	}
	paths := map[string]bool{s.Recent[0].Path: true, s.Recent[1].Path: true}
	if !paths["/v1/messages"] || !paths["/v1/responses"] || s.Succeeded != 2 {
		t.Errorf("paths=%v snapshot=%+v", paths, s)
	}
}

// bridgeWriter 的边角：不先 WriteHeader 直接写、残行、注释与无关行、错误体不是 JSON。
func TestBridgeWriterEdges(t *testing.T) {
	newBW := func(rec *httptest.ResponseRecorder) *bridgeWriter {
		return &bridgeWriter{ctx: context.Background(), w: rec, format: sdktranslator.FormatClaude, model: "m",
			orig: []byte(`{"model":"m","stream":true,"messages":[]}`), chat: []byte(`{"model":"m"}`)}
	}

	rec := httptest.NewRecorder()
	bw := newBW(rec)
	bw.Header().Set("Content-Type", "text/event-stream")
	_, _ = bw.Write([]byte(": keepalive\nevent: x\n" + strings.TrimSuffix(chunk(`{"role":"assistant","content":"尾"}`, ""), "\n\n")))
	bw.WriteHeader(500) // 已开流后的二次 WriteHeader 不生效
	bw.finish()
	if !strings.Contains(rec.Body.String(), ": keepalive\n\n") || strings.Contains(rec.Body.String(), "event: x") || !strings.Contains(rec.Body.String(), "尾") {
		t.Errorf("body=%q", rec.Body)
	}

	rec = httptest.NewRecorder()
	bw = newBW(rec)
	bw.WriteHeader(502)
	_, _ = bw.Write([]byte("plain text failure"))
	bw.finish()
	if typ, msg := anthropicErr(t, rec); rec.Code != 502 || typ != "api_error" || msg != "plain text failure" {
		t.Errorf("code=%d type=%s msg=%s", rec.Code, typ, msg)
	}

	rec = httptest.NewRecorder()
	newBW(rec).finish() // 什么都没写：不写响应
	if rec.Body.Len() != 0 {
		t.Errorf("body=%q", rec.Body)
	}

	// 非流式 200 但翻不出来（apply_patch 的参数不是 JSON，翻译器拒绝）→ 502
	rec = httptest.NewRecorder()
	bw = newBW(rec)
	bw.format = sdktranslator.FormatOpenAIResponse
	bw.orig = []byte(strings.Replace(codexBody, `"stream":true`, `"stream":false`, 1))
	_, _ = bw.Write([]byte(`{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"apply_patch","arguments":"not json"}}]}}]}`))
	bw.finish()
	if rec.Code != 502 {
		t.Errorf("code=%d body=%s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	bw = newBW(rec)
	bw.Header().Set("Content-Type", "text/event-stream")
	bw.w = failWriter{rec}
	if _, err := bw.Write([]byte(": c\n")); err == nil {
		t.Error("下游写失败应返回错误")
	}
	bw = newBW(rec)
	bw.Header().Set("Content-Type", "text/event-stream")
	bw.w = failWriter{rec}
	if _, err := bw.Write([]byte(chunk(`{"role":"assistant","content":"x"}`, ""))); err == nil {
		t.Error("翻译后的事件写失败应返回错误")
	}
}

// 读请求体失败：两个入口、count_tokens 和 /v1/search/messages 都按客户端协议回 400。
func TestBridgeReadBodyError(t *testing.T) {
	h, _ := bridgeHandler(t, 200, sseOK, true)
	for _, path := range []string{"/v1/messages", "/v1/responses", "/v1/messages/count_tokens", "/v1/search/messages"} {
		req := httptest.NewRequest("POST", path, io.NopCloser(errReader{}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "read body") {
			t.Errorf("%s: code=%d body=%s", path, rec.Code, rec.Body)
		}
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

type failWriter struct{ *httptest.ResponseRecorder }

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }

func TestAnthropicErrorTypes(t *testing.T) {
	want := map[int]string{400: "invalid_request_error", 401: "authentication_error", 403: "permission_error",
		404: "not_found_error", 413: "request_too_large", 429: "rate_limit_error", 503: "overloaded_error",
		529: "overloaded_error", 500: "api_error", 502: "api_error"}
	for status, typ := range want {
		if got := anthropicErrorType(status); got != typ {
			t.Errorf("%d → %s want %s", status, got, typ)
		}
	}
	if got := string(bridgeStreamError(sdktranslator.FormatOpenAIResponse, "", "x")); !strings.Contains(got, `"code":"server_error"`) {
		t.Errorf("缺省 code：%s", got)
	}
}

// readAll 读出请求体后放回去，让后面的 RoundTrip 照常读。
func readAll(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b, err
}
