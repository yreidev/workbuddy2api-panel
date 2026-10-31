// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

// search_messages.go 联网搜索的 Anthropic Messages 形状：/v1/messages 里认出 Claude Code WebSearch 的子请求，
// 以及 /v1/search/messages。回应照 Anthropic 服务端搜索（web_search_20250305）的形状：server_tool_use 块、
// web_search_tool_result 块、text 块，流式、非流式都回。
//
// Claude Code（2.1.x，读本机安装的程序确认）怎么读这个回应：流式读，server_tool_use 块的 input_json_delta
// 里取 "query" 显示进度；web_search_tool_result 块的 content 取每条的 title、url，拼成交给主模型的
// 「Links: [...]」；text 块的文字原样跟在后面。上游的结果没有摘要以外的正文，网关就在 text 块里照官方
// WorkBuddy 搜索工具的写法（formatSearch）列出标题、链接和摘要，主模型读得到摘要。
// /v1/search/messages 原来的客户端（DSH 的搜索插件，见上游 linguo2625469 的说明）要求至少一个
// web_search_tool_result 块、每条都有 url，摘要从 text 块的 citations 取，非流式回应照样带上。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"

	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// searchPromptPrefix Claude Code 写在搜索词前面的话（WebSearch 子请求唯一的用户消息）。
const searchPromptPrefix = "Perform a web search for the query:"

// searchMessagesRequest Anthropic Messages 请求里搜索要用的字段。
type searchMessagesRequest struct {
	Model    string       `json:"model"`
	Stream   bool         `json:"stream"`
	Tools    []searchTool `json:"tools"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

// searchTool 请求声明的工具；Anthropic 服务端搜索工具带域名白名单、黑名单。
type searchTool struct {
	Type           string   `json:"type"`
	Name           string   `json:"name"`
	AllowedDomains []string `json:"allowed_domains"`
	BlockedDomains []string `json:"blocked_domains"`
}

// webSearchTool 请求声明的搜索工具。serverOnly 只认 Anthropic 的服务端工具（type 以 web_search_ 开头），
// /v1/messages 用：名字叫 web_search 的普通工具是客户端自己实现的，照常交给模型。/v1/search/messages 照
// 上游两种都认。
func (m *searchMessagesRequest) webSearchTool(serverOnly bool) (searchTool, bool) {
	for _, t := range m.Tools {
		if strings.HasPrefix(t.Type, "web_search_") || (!serverOnly && (t.Type == "web_search" || t.Name == "web_search")) {
			return t, true
		}
	}
	return searchTool{}, false
}

// lastUserText 最后一条用户消息的文字：content 是字符串，或者文本块拼起来（一条消息拆成几块是合法的）。
func (m *searchMessagesRequest) lastUserText() string {
	for i := len(m.Messages) - 1; i >= 0; i-- {
		msg := m.Messages[i]
		if msg.Role != "user" {
			continue
		}
		var s string
		if json.Unmarshal(msg.Content, &s) == nil {
			return strings.TrimSpace(s)
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(msg.Content, &blocks)
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}

// stripSearchPrompt 去掉 Claude Code 写在搜索词前面的话（大小写不敏感），ok 表示有这句话。
func stripSearchPrompt(text string) (query string, ok bool) {
	if len(text) < len(searchPromptPrefix) || !strings.EqualFold(text[:len(searchPromptPrefix)], searchPromptPrefix) {
		return text, false
	}
	return strings.TrimSpace(text[len(searchPromptPrefix):]), true
}

// claudeCodeSearch /v1/messages 的请求是不是 Claude Code WebSearch 的子请求：声明了服务端搜索工具，最后一条
// 用户消息以 searchPromptPrefix 开头。
func claudeCodeSearch(body []byte) (*searchMessagesRequest, searchTool, string, bool) {
	var req searchMessagesRequest
	if json.Unmarshal(body, &req) != nil {
		return nil, searchTool{}, "", false
	}
	tool, ok := req.webSearchTool(true)
	if !ok {
		return nil, searchTool{}, "", false
	}
	query, ok := stripSearchPrompt(req.lastUserText())
	return &req, tool, query, ok
}

// searchMessages POST /v1/search/messages。没声明搜索工具的请求是对话，打错了地方，回 400；搜索词取最后一条
// 用户消息，带 Claude Code 那句前缀的去掉。
func (h *Handler) searchMessages(w http.ResponseWriter, r *http.Request) {
	body, err := readRequestBody(r)
	if err != nil {
		writeBridgeError(w, sdktranslator.FormatClaude, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}
	var req searchMessagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeBridgeError(w, sdktranslator.FormatClaude, http.StatusBadRequest, "invalid_request", "request body is not valid JSON: "+err.Error())
		return
	}
	tool, ok := req.webSearchTool(false)
	if !ok {
		writeBridgeError(w, sdktranslator.FormatClaude, http.StatusBadRequest, "invalid_request",
			"request does not declare the web_search tool; this endpoint only serves web search, not chat (use /v1/messages for chat)")
		return
	}
	query, _ := stripSearchPrompt(req.lastUserText())
	h.anthropicSearch(w, r, &req, tool, query)
}

// anthropicSearch 搜一次，按 Anthropic 服务端搜索的形状回。失败时回 Anthropic 的错误并带 X-Should-Retry: false：
// Claude Code 遇到 5xx、429 默认自动重试好几次，搜索失败让它直接把错误交给主模型，要不要再搜由主模型定。
func (h *Handler) anthropicSearch(w http.ResponseWriter, r *http.Request, req *searchMessagesRequest, tool searchTool, query string) {
	st := newSearchStat(requestTraceFrom(r), req.Stream)
	defer st.done()
	fail := func(status int, code, msg string) {
		w.Header().Set("X-Should-Retry", "false")
		writeBridgeError(w, sdktranslator.FormatClaude, status, code, msg)
		st.status, st.outcome = status, reqlog.OutcomeHTTPError
	}
	q := upstream.SearchQuery{Query: query, AllowedDomains: tool.AllowedDomains, BlockedDomains: tool.BlockedDomains}
	if err := q.Check(); err != nil {
		fail(http.StatusBadRequest, "invalid_request", "could not read a search query from the request: "+err.Error())
		return
	}
	settle, d := h.searchAdmit(principalFrom(r.Context()))
	if d != nil {
		denialHeaders(w, d)
		fail(d.Status, d.Code, d.Message)
		return
	}
	out, f := h.runSearch(r.Context(), q, req.Model, searchMeta(r.Header), st)
	settle(f == nil)
	if f != nil {
		fail(f.status, f.code, f.message())
		if r.Context().Err() != nil {
			st.outcome = reqlog.OutcomeInterrupted
		}
		return
	}
	st.status, st.outcome = http.StatusOK, reqlog.OutcomeSuccess
	m := newSearchMessage(req.Model, query, out, time.Now())
	if req.Stream {
		m.writeStream(w)
		return
	}
	writeJSON(w, http.StatusOK, m.body())
}

// searchMessage 一次搜索按 Anthropic 服务端搜索写出的回应。
type searchMessage struct {
	id, toolID, model, query, text string
	results                        []searchResultItem
	citations                      []searchCitation
}

type searchResultItem struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type searchCitation struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	CitedText string `json:"cited_text"`
}

// newSearchMessage 结果块只放有链接的条目（老客户端要求每条都有 url），摘要另作 citations；文字照 formatSearch。
func newSearchMessage(model, query string, out *upstream.SearchOutcome, now time.Time) *searchMessage {
	m := &searchMessage{id: "msg_" + session.NewMessageID()[:24], toolID: "srvtoolu_" + session.NewMessageID()[:24],
		model: model, query: query, text: formatSearch(query, out, now), results: []searchResultItem{}}
	for _, r := range out.Results {
		if r.URL == "" {
			continue
		}
		m.results = append(m.results, searchResultItem{Type: "web_search_result", Title: r.Title, URL: r.URL})
		if r.Snippet != "" {
			m.citations = append(m.citations, searchCitation{Type: "web_search_result_location", URL: r.URL, Title: r.Title, CitedText: r.Snippet})
		}
	}
	return m
}

func (m *searchMessage) toolUse(input any) map[string]any {
	return map[string]any{"type": "server_tool_use", "id": m.toolID, "name": "web_search", "input": input}
}

func (m *searchMessage) toolResult() map[string]any {
	return map[string]any{"type": "web_search_tool_result", "tool_use_id": m.toolID, "content": m.results}
}

// body 非流式回应。
func (m *searchMessage) body() map[string]any {
	text := map[string]any{"type": "text", "text": m.text}
	if len(m.citations) > 0 {
		text["citations"] = m.citations
	}
	return map[string]any{
		"id": m.id, "type": "message", "role": "assistant", "model": m.model,
		"content":     []any{m.toolUse(map[string]string{"query": m.query}), m.toolResult(), text},
		"stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
	}
}

// writeStream 流式回应：搜完才开始写，一次写完。文字块不带 citations（Claude Code 不读，老客户端走非流式）。
func (m *searchMessage) writeStream(w http.ResponseWriter) {
	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("Connection", "keep-alive")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	event := func(name string, data map[string]any) {
		data["type"] = name
		raw, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, raw)
	}
	query, _ := json.Marshal(map[string]string{"query": m.query})
	event("message_start", map[string]any{"message": map[string]any{
		"id": m.id, "type": "message", "role": "assistant", "model": m.model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
	}})
	event("content_block_start", map[string]any{"index": 0, "content_block": m.toolUse(map[string]any{})})
	event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(query)}})
	event("content_block_stop", map[string]any{"index": 0})
	event("content_block_start", map[string]any{"index": 1, "content_block": m.toolResult()})
	event("content_block_stop", map[string]any{"index": 1})
	event("content_block_start", map[string]any{"index": 2, "content_block": map[string]any{"type": "text", "text": ""}})
	event("content_block_delta", map[string]any{"index": 2, "delta": map[string]any{"type": "text_delta", "text": m.text}})
	event("content_block_stop", map[string]any{"index": 2})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": 0}})
	event("message_stop", map[string]any{})
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
}
