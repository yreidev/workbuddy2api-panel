// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

// bridge.go Claude Code（Anthropic Messages）与 Codex（Responses API）入口。
//
// 上游只认 OpenAI 聊天格式，这两个入口做「入口翻译」：请求翻成聊天格式后交给
// chatCompletions 走完整链路（选号、粘性、换号、日志、用量、出站对齐全部照旧），
// 写出时由 bridgeWriter 把聊天格式的 JSON / SSE 翻回客户端的协议。
// 协议翻译用 CLIProxyAPI 的 sdk/translator（router-for-me/CLIProxyAPI，MIT）。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
)

var translators = builtin.Registry()

// tracedPaths 请求日志按这些路径记账：对话链路的三个入口和 /v1/search/messages（/mcp 只在调了搜索工具时
// 记，见 mcp.go）。
var tracedPaths = map[string]bool{
	"/v1/chat/completions": true,
	"/v1/messages":         true,
	"/v1/responses":        true,
	"/v1/search/messages":  true,
}

func (h *Handler) registerBridgeRoutes() {
	h.mux.HandleFunc("POST /v1/messages", h.withKeyAuth(sdktranslator.FormatClaude, true, h.anthropicMessages))
	h.mux.HandleFunc("POST /v1/messages/count_tokens", h.withKeyAuth(sdktranslator.FormatClaude, true, h.countTokens))
	h.mux.HandleFunc("POST /v1/responses", h.withKeyAuth(sdktranslator.FormatOpenAIResponse, true, h.openaiResponses))
	h.mux.HandleFunc("POST /v1/search/messages", h.withKeyAuth(sdktranslator.FormatClaude, true, h.searchMessages))
	h.mux.Handle("/mcp", h.withKeyAuth(sdktranslator.FormatOpenAI, true, h.mcpHandler().ServeHTTP))
	h.mux.HandleFunc("/v1/v1/", h.doubledV1)
}

// doubledV1 Base URL 多带了 /v1 的请求：Claude Code 自己会拼 /v1/messages，ANTHROPIC_BASE_URL
// 写成 …/v1 就成了 /v1/v1/messages。照实回 404 的话 Claude Code 一律提示「所选模型不存在」，
// 看不到原因，所以回 400 并按入口协议说明怎么改。不校验密钥：地址不对时先改地址。
func (h *Handler) doubledV1(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/v1/messages") {
		writeBridgeError(w, sdktranslator.FormatClaude, http.StatusBadRequest, "invalid_base_url",
			"Base URL 不要带 /v1：Claude Code 会自己拼上 /v1/messages，请求成了 "+r.URL.Path+"。把 ANTHROPIC_BASE_URL 末尾的 /v1 去掉。")
		return
	}
	writeOpenAIError(w, http.StatusBadRequest, "invalid_base_url",
		"Base URL 多带了一个 /v1：请求成了 "+r.URL.Path+"。把 Base URL 末尾的 /v1 去掉。")
}

// anthropicMessages POST /v1/messages（Claude Code、Anthropic SDK）。Claude Code WebSearch 的子请求不交给
// 模型，网关直接搜（见 search_messages.go）。
func (h *Handler) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, err := readRequestBody(r)
	if err != nil {
		writeBridgeError(w, sdktranslator.FormatClaude, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}
	if req, tool, query, ok := claudeCodeSearch(body); ok {
		h.anthropicSearch(w, r, req, tool, query)
		return
	}
	h.bridgeBody(w, r, sdktranslator.FormatClaude, body)
}

// openaiResponses POST /v1/responses（Codex CLI、OpenAI Responses SDK）。
func (h *Handler) openaiResponses(w http.ResponseWriter, r *http.Request) {
	body, err := readRequestBody(r)
	if err != nil {
		writeBridgeError(w, sdktranslator.FormatOpenAIResponse, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}
	h.bridgeBody(w, r, sdktranslator.FormatOpenAIResponse, body)
}

// bridgeBody 把读好的请求体翻成聊天格式交给 chatCompletions，写出时翻回客户端的协议。
func (h *Handler) bridgeBody(w http.ResponseWriter, r *http.Request, format sdktranslator.Format, body []byte) {
	var peek struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		writeBridgeError(w, format, http.StatusBadRequest, "invalid_request", "request body is not valid JSON: "+err.Error())
		return
	}
	if peek.Model == "" {
		writeBridgeError(w, format, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	// 先路由再判断：Claude Code / Codex 不配模型时发的官方默认名靠路由规则落到上游模型
	model, routed := h.routeModel(r.Context(), peek.Model)
	if msg := h.unknownModel(model, format); msg != "" {
		if routed {
			// 规则命中了但目标写错：指给用户改哪条规则，而不是让他再加规则
			msg = "model route " + peek.Model + " -> " + model + ": the target is not in the gateway model list (GET /v1/models); fix the route in the panel (模型与档位 → 模型路由)"
		}
		writeBridgeError(w, format, http.StatusNotFound, "model_not_found", msg)
		return
	}
	env := translators.TranslateRequestEnvelope(r.Context(), format, sdktranslator.FormatOpenAI, sdktranslator.RequestEnvelope{
		Format: format, Model: model, Stream: peek.Stream, Body: body,
	})
	if env.Err != nil {
		writeBridgeError(w, format, http.StatusBadRequest, "invalid_request", env.Err.Error())
		return
	}
	r2 := r.Clone(context.WithValue(r.Context(), routedKey{}, true))
	r2.Body = io.NopCloser(bytes.NewReader(env.Body))
	r2.ContentLength = int64(len(env.Body))
	bw := &bridgeWriter{ctx: r.Context(), w: w, format: format, model: model, orig: body, chat: env.Body}
	h.chatCompletions(bw, r2)
	bw.finish()
}

// unknownModel 模型目录里没有这个模型时返回给客户端的说明，有或判断不了时返回空串。
//
// Claude Code 没设模型变量时发的是 Anthropic 官方模型名，直接转上游会每个号都撞一次
// 「无此模型」再换号；这里先对一下已缓存的目录（不发上游请求），对不上就当场告诉
// 用户该设哪个变量。目录还没拉到时放行，交给上游判断。
func (h *Handler) unknownModel(model string, format sdktranslator.Format) string {
	if h.cfg.Upstream == nil || cachedModelsSnapshot() == nil {
		return ""
	}
	realm, bare := resolveModel(model)
	switch {
	case bare != model && realm == "global":
		// 国际版目录按需探测，没探测过时查不到，放行
		return ""
	case bare != model && h.cfg.Upstream.RealmHasModel("cn", bare):
		return ""
	case bare == model && (h.cfg.Upstream.RealmHasModel("cn", bare) || h.cfg.Upstream.RealmHasModel("global", bare)):
		return ""
	}
	msg := "model " + model + " is not in the gateway model list (GET /v1/models) and no model route matches it; add a route in the panel (模型与档位 → 模型路由)"
	if format == sdktranslator.FormatClaude {
		return msg + ", or set ANTHROPIC_MODEL / ANTHROPIC_DEFAULT_OPUS_MODEL / ANTHROPIC_DEFAULT_SONNET_MODEL / ANTHROPIC_DEFAULT_HAIKU_MODEL to an upstream model name"
	}
	return msg + ", or set model in ~/.codex/config.toml to an upstream model name"
}

// countTokens POST /v1/messages/count_tokens：按字数估算输入 token，不发上游。
// 估算口径：CJK 等宽字符每字 1 个，其余按 4 字节 1 个；只数文本，不数 JSON 结构。
func (h *Handler) countTokens(w http.ResponseWriter, r *http.Request) {
	body, err := readRequestBody(r)
	if err != nil {
		writeBridgeError(w, sdktranslator.FormatClaude, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		writeBridgeError(w, sdktranslator.FormatClaude, http.StatusBadRequest, "invalid_request", "request body is not valid JSON: "+err.Error())
		return
	}
	var n tokenEstimate
	for _, k := range []string{"system", "messages", "tools"} {
		n.walk(obj[k])
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": n.total()})
}

type tokenEstimate struct{ wide, narrowBytes int }

func (e *tokenEstimate) walk(v any) {
	switch t := v.(type) {
	case string:
		for _, c := range t {
			if c >= 0x2E80 {
				e.wide++
			} else {
				e.narrowBytes += utf8.RuneLen(c)
			}
		}
	case []any:
		for _, x := range t {
			e.walk(x)
		}
	case map[string]any:
		for k, x := range t {
			// 结构字段（type、role、id、cache_control 等）不是送进模型的文本
			if k == "type" || k == "role" || k == "id" || k == "tool_use_id" || k == "cache_control" || k == "signature" {
				continue
			}
			e.walk(x)
		}
	}
}

func (e *tokenEstimate) total() int { return e.wide + (e.narrowBytes+3)/4 }

// bridgeWriter 接住 chatCompletions 写出的聊天格式响应，翻成客户端协议再写给客户端。
//   - 200 + text/event-stream：逐行翻译，边收边写；
//   - 其余（非流式成功、各种错误）：先攒齐，finish 时一次翻译写出。
type bridgeWriter struct {
	ctx    context.Context
	w      http.ResponseWriter
	format sdktranslator.Format
	model  string
	orig   []byte // 客户端原始请求
	chat   []byte // 翻译后的聊天请求

	status  int
	stream  bool
	line    []byte       // 流式：还没凑满一行的残片
	buf     bytes.Buffer // 非流式：完整响应体
	param   any          // 翻译器的跨帧状态
	stopped bool         // 流式：已写出错误事件，之后的帧不再翻译
}

func (b *bridgeWriter) Header() http.Header { return b.w.Header() }

func (b *bridgeWriter) WriteHeader(code int) {
	if b.status != 0 {
		return
	}
	b.status = code
	if code == http.StatusOK && strings.HasPrefix(b.w.Header().Get("Content-Type"), "text/event-stream") {
		b.stream = true
		b.w.WriteHeader(code)
	}
}

func (b *bridgeWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.WriteHeader(http.StatusOK)
	}
	if !b.stream {
		return b.buf.Write(p)
	}
	b.line = append(b.line, p...)
	for {
		i := bytes.IndexByte(b.line, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimRight(b.line[:i], "\r")
		if err := b.streamLine(line); err != nil {
			return 0, err
		}
		b.line = b.line[i+1:]
	}
	return len(p), nil
}

func (b *bridgeWriter) Flush() {
	if f, ok := b.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (b *bridgeWriter) streamLine(line []byte) error {
	switch {
	case len(line) == 0:
		return nil
	case line[0] == ':':
		// SSE 注释两种协议都允许，原样转出
		_, err := b.w.Write(append(append([]byte{}, line...), '\n', '\n'))
		return err
	case !bytes.HasPrefix(line, []byte("data:")):
		return nil
	}
	if b.stopped {
		return nil
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if !bytes.Equal(payload, []byte("[DONE]")) {
		var peek struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(payload, &peek) == nil && len(peek.Error) > 0 && !bytes.Equal(peek.Error, []byte("null")) {
			// 流中途的上游错误帧：翻成对方协议的错误事件，流到此为止
			b.stopped = true
			code, msg := openAIErrorText(payload)
			_, err := b.w.Write(bridgeStreamError(b.format, code, msg))
			return err
		}
	}
	sseLine := append([]byte("data: "), payload...)
	for _, out := range translators.TranslateStream(b.ctx, sdktranslator.FormatOpenAI, b.format, b.model, b.orig, b.chat, sseLine, &b.param) {
		if _, err := b.w.Write(out); err != nil {
			return err
		}
	}
	return nil
}

// finish 在 chatCompletions 返回后调用：流式补完残行，非流式一次翻译写出。
func (b *bridgeWriter) finish() {
	if b.stream {
		if len(b.line) > 0 {
			_ = b.streamLine(bytes.TrimRight(b.line, "\r"))
			b.line = nil
		}
		b.Flush()
		return
	}
	if b.status == 0 {
		return
	}
	b.w.Header().Del("Content-Length")
	if b.status != http.StatusOK {
		code, msg := openAIErrorText(b.buf.Bytes())
		writeBridgeError(b.w, b.format, b.status, code, msg)
		return
	}
	out := translators.TranslateNonStream(b.ctx, sdktranslator.FormatOpenAI, b.format, b.model, b.orig, b.chat, b.buf.Bytes(), &b.param)
	if len(out) == 0 {
		writeBridgeError(b.w, b.format, http.StatusBadGateway, "upstream_parse", "cannot translate upstream response")
		return
	}
	b.w.Header().Set("Content-Type", "application/json")
	b.w.WriteHeader(http.StatusOK)
	_, _ = b.w.Write(out)
}

// openAIErrorText 从聊天格式的错误体取 code 和说明；网关提示（gateway_hint）接在说明后面。
func openAIErrorText(raw []byte) (code, msg string) {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
			Hint    string `json:"gateway_hint"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Error.Message == "" {
		return "upstream_error", strings.TrimSpace(string(raw))
	}
	msg = e.Error.Message
	if e.Error.Hint != "" {
		msg += " (" + e.Error.Hint + ")"
	}
	switch c := e.Error.Code.(type) {
	case string:
		code = c
	case float64:
		code = strconv.FormatFloat(c, 'f', -1, 64)
	}
	return code, msg
}

// writeBridgeError 按入口协议写错误：Anthropic 用 {"type":"error","error":{type,message}}，
// Responses 与聊天接口相同。
func writeBridgeError(w http.ResponseWriter, format sdktranslator.Format, status int, code, msg string) {
	if format != sdktranslator.FormatClaude {
		writeOpenAIError(w, status, code, msg)
		return
	}
	writeJSON(w, status, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": anthropicErrorType(status), "message": msg},
	})
}

// anthropicErrorType HTTP 状态 → Anthropic 错误类型（Claude Code 按类型决定是否重试）。
func anthropicErrorType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable, 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// bridgeStreamError 流已开始后的错误事件：Anthropic 发 error 事件，Responses 发 response.failed。
func bridgeStreamError(format sdktranslator.Format, code, msg string) []byte {
	var event string
	var data map[string]any
	if format == sdktranslator.FormatClaude {
		event = "error"
		data = map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": msg}}
	} else {
		if code == "" {
			code = "server_error"
		}
		event = "response.failed"
		data = map[string]any{"type": "response.failed", "response": map[string]any{
			"object": "response", "status": "failed",
			"error": map[string]any{"code": code, "message": msg},
		}}
	}
	raw, _ := json.Marshal(data)
	return []byte("event: " + event + "\ndata: " + string(raw) + "\n\n")
}
