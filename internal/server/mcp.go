// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

// mcp.go /mcp：MCP（Model Context Protocol）服务器，只有一个工具 web_search，让 Codex 这类能接 MCP 服务器的
// 客户端用上网关的联网搜索。用官方 Go SDK（modelcontextprotocol/go-sdk）的 Streamable HTTP 无状态模式：每个
// POST 自成一次会话，网关不记会话 id，重启、多实例都不影响；回应直接是 JSON，不开 SSE。
//
// 鉴权同 /v1/*（管理员密钥或客户端密钥，Authorization: Bearer 或 x-api-key）。客户端密钥每调一次搜索计一次
// 请求、不看模型白名单，没搜成的退还。只有调了 web_search 才记一行请求记录（路径 /mcp），握手、列工具不记。

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// mcpRequestKey 工具里取原始 HTTP 请求（客户端 IP、UA、会话头）用的上下文键。SDK 交给工具的上下文
// 从 HTTP 请求的上下文派生，值都在。
type mcpRequestKey struct{}

// mcpSearchArgs web_search 的入参，同官方 WebSearch 工具；说明是给模型看的，照官方原文。
type mcpSearchArgs struct {
	Query          string   `json:"query" jsonschema:"The search query to use"`
	AllowedDomains []string `json:"allowed_domains,omitempty" jsonschema:"Only include search results from these domains."`
	BlockedDomains []string `json:"blocked_domains,omitempty" jsonschema:"Never include search results from these domains"`
	Freshness      string   `json:"freshness,omitempty" jsonschema:"Filter search results by time range. Supports d[N], m[N], and y[N] for the last N calendar days, months, and years: d[N] N is an integer from 1 to 30; m[N] N is an integer from 1 to 12; y[N] N is an integer from 1 to 5. N may be omitted and defaults to 1. The current partial calendar unit counts as 1. For example, d1 means today, m1 means the current month to date, and y1 means the current year to date. Omit this parameter to disable time filtering."`
}

func (h *Handler) mcpHandler() http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: ServiceName, Version: h.cfg.Version}, nil)
	openWorld := true
	mcp.AddTool(srv, &mcp.Tool{
		Name: "web_search",
		Description: "Search the web and get the top results (title, link and snippet, up to 5). " +
			"Use it for current events, recent releases and anything beyond your knowledge cutoff.",
		Annotations: &mcp.ToolAnnotations{Title: "Web Search", ReadOnlyHint: true, OpenWorldHint: &openWorld},
	}, h.mcpWebSearch)
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		PropagateRequestCancellation: true,
		// 网关常在本机反代后面（反代连 127.0.0.1，Host 是对外的域名），SDK 默认的 DNS 重绑定防护会把这类请求
		// 全回 403；/mcp 本来就要密钥，关掉它。
		DisableLocalhostProtection: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mcpRequestKey{}, r)))
	})
}

// mcpWebSearch web_search 工具。出错返回 error，SDK 包成 isError 的工具结果交给模型。
func (h *Handler) mcpWebSearch(ctx context.Context, _ *mcp.CallToolRequest, in mcpSearchArgs) (*mcp.CallToolResult, any, error) {
	r, _ := ctx.Value(mcpRequestKey{}).(*http.Request)
	p := principalFrom(ctx)
	tr := &requestTrace{id: reqlog.NewRequestID(), start: time.Now(), path: "/mcp", apiKey: p.label()}
	if h.loadLive().RecordClientInfo {
		tr.captureClientInfo(r)
	}
	st := newSearchStat(tr, false)
	h.cfg.RequestLog.Begin()
	defer func() {
		st.done()
		h.cfg.RequestLog.Record(tr.event(st.status, nil))
	}()
	fail := func(status int, msg string) error {
		st.status, st.outcome, st.errMsg = status, reqlog.OutcomeHTTPError, msg
		return errors.New(msg)
	}

	q := upstream.SearchQuery{Query: in.Query, AllowedDomains: in.AllowedDomains, BlockedDomains: in.BlockedDomains, Freshness: in.Freshness}
	if err := q.Check(); err != nil {
		return nil, nil, fail(http.StatusBadRequest, err.Error())
	}
	settle, d := h.searchAdmit(p)
	if d != nil {
		return nil, nil, fail(d.Status, d.Message)
	}
	var hdr http.Header
	if r != nil {
		hdr = r.Header
	}
	out, f := h.runSearch(ctx, q, "", searchMeta(hdr), st)
	settle(f == nil)
	if f != nil {
		return nil, nil, fail(f.status, "web search failed: "+f.message())
	}
	st.status, st.outcome = http.StatusOK, reqlog.OutcomeSuccess
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: formatSearch(in.Query, out, time.Now())}}}, nil, nil
}
