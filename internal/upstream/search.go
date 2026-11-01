// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// search.go 联网搜索：POST {入口}/agenttool/v1/search。网关怎么选号、对外有哪些入口见 server/search.go。
//
// 依据：国内版桌面端 5.9.3 内置 CLI 2.165.0 的 WebSearch 工具（cli/dist/codebuddy-lite-wb.mjs 的
// performWebSearch、buildRequestBody），读安装包确认，见 docs/official-client.md「联网搜索」：
//   - 地址是产品入口（和对话同一个）加 /agenttool/v1/search，20 秒超时。工具用 Node 内置的 fetch 直接发，
//     不经 axios 请求框架，也不经 OpenAI SDK，请求头是工具自己的一份清单（见 searchHeaders）。
//   - 请求体 {"query","type":"text2text","max_results":5}，有白名单时加 allowed_domains，限了时间时加
//     freshness；黑名单不单独发，拼在 query 末尾：` (-site:a -site:b)`。
//   - 回应是扁平的 {provider, results[{title,url,snippet,site?,favicon?}], total_results, response_time_ms}，
//     不套 {code,msg,data}；HTTP 不是 2xx、或者 code 不是 0 都算失败。
//   - 拿到结果后去掉链接不是 http/https 的条目、图标不是 http/https 的去掉图标（官方 filterUnsafeResults；
//     它另查一份本地的恶意域名库，网关没有这份库）。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
)

const (
	searchPath = "/agenttool/v1/search"
	// searchTimeout 同官方 WEB_SEARCH_TIMEOUT_MS。
	searchTimeout = 20 * time.Second
	// searchMaxResults 官方 buildRequestBody 写死 5 条，不让调用方改。
	searchMaxResults = 5
)

// searchFreshness 官方入参 freshness 的取值：d、d1–d30、m、m1–m12、y、y1–y5。
var searchFreshness = regexp.MustCompile(`^(?:d(?:[1-9]|[12]\d|30)?|m(?:[1-9]|1[0-2])?|y[1-5]?)$`)

// SearchQuery 一次联网搜索的参数，同官方 WebSearch 工具的入参。
type SearchQuery struct {
	Query          string
	AllowedDomains []string // 只要这些域名的结果
	BlockedDomains []string // 不要这些域名的结果
	Freshness      string   // 时间范围，空 = 不限
}

// Check 照官方 validateParams 校验入参：query 去掉空白后不能为空、至少 2 个字，freshness 按官方的格式。
func (q SearchQuery) Check() error {
	if strings.TrimSpace(q.Query) == "" {
		return errors.New("the 'query' parameter cannot be empty")
	}
	if utf8.RuneCountInString(q.Query) < 2 {
		return errors.New("the 'query' parameter must be at least 2 characters long")
	}
	if q.Freshness != "" && !searchFreshness.MatchString(q.Freshness) {
		return errors.New("freshness must be d or d1-d30, m or m1-m12, or y or y1-y5")
	}
	return nil
}

// SearchResult 一条搜索结果。
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Site    string `json:"site,omitempty"`
	Favicon string `json:"favicon,omitempty"`
}

// SearchOutcome 一次搜索的结果。
type SearchOutcome struct {
	Provider       string
	Results        []SearchResult
	TotalResults   int
	ResponseTimeMs int64
	// Filtered 上游给了结果、但都因链接不是 http/https 被去掉了（官方这时说「没有安全的结果」）。
	Filtered bool
}

// searchBody 出站请求体。用结构体保持官方的字段顺序（query → type → max_results → allowed_domains → freshness）。
type searchBody struct {
	Query          string   `json:"query"`
	Type           string   `json:"type"`
	MaxResults     int      `json:"max_results"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	Freshness      string   `json:"freshness,omitempty"`
}

// Search 用 a 搜一次。meta 只用 ConversationID 和 ConversationRequestID：会话 id 为空时新生成一个
// （同 ChatHeaders），轮次 id 原样发（官方没有时发空值）。HTTP 不是 2xx 或回应带了非 0 的 code 时返回
// *Error，传输出错、回应读不了返回普通 error。不选号、不换号，那些在 server 里做。
func (c *Client) Search(ctx context.Context, a *auth.Auth, q SearchQuery, meta ChatMeta) (*SearchOutcome, error) {
	query := q.Query
	if len(q.BlockedDomains) > 0 {
		sites := make([]string, len(q.BlockedDomains))
		for i, d := range q.BlockedDomains {
			sites[i] = "-site:" + d
		}
		query += " (" + strings.Join(sites, " ") + ")"
	}
	// JSON.stringify 不转义 <、>、&，Go 默认会转成 < 这类写法：关掉，和官方逐字节一样。
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(searchBody{
		Query: query, Type: "text2text", MaxResults: searchMaxResults,
		AllowedDomains: q.AllowedDomains, Freshness: q.Freshness,
	})
	body := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))

	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	req, err := newAccountRequest(ctx, a, http.MethodPost, c.chatBase(a)+searchPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if meta.ConversationID == "" {
		meta.ConversationID = session.NewUUIDv7()
	}
	c.searchHeaders(req, a, meta)
	resp, err := doDecoded(c.HTTP, req)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("search: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{Kind: Classify(resp.StatusCode, string(raw)), Status: resp.StatusCode, Msg: truncate(string(raw), 200)}
	}
	var env struct {
		Code           int            `json:"code"`
		Provider       string         `json:"provider"`
		Results        []SearchResult `json:"results"`
		TotalResults   int            `json:"total_results"`
		ResponseTimeMs int64          `json:"response_time_ms"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("search: parse response: %w (body: %s)", err, truncate(string(raw), 120))
	}
	if env.Code != 0 {
		// 业务错误：状态码是 2xx 时按回应体认（余额、限流这些业务码 Classify 照样认得），认不出算 ErrClient。
		kind := Classify(resp.StatusCode, string(raw))
		if kind == ErrNone {
			kind = ErrClient
		}
		return nil, &Error{Kind: kind, Status: resp.StatusCode, Msg: truncate(string(raw), 200)}
	}
	return safeSearchResults(env.Provider, env.Results, env.TotalResults, env.ResponseTimeMs), nil
}

// safeSearchResults 照官方整理结果：标题为空写 Untitled；链接不是 http/https 的条目去掉（链接为空的留着），
// 图标不是 http/https 的只去掉图标；去掉过条目时总数改成剩下的条数。
func safeSearchResults(provider string, results []SearchResult, total int, ms int64) *SearchOutcome {
	out := &SearchOutcome{Provider: provider, Results: []SearchResult{}, TotalResults: total, ResponseTimeMs: ms}
	for _, r := range results {
		if r.Title == "" {
			r.Title = "Untitled"
		}
		if r.URL != "" && !httpURL(r.URL) {
			continue
		}
		if r.Favicon != "" && !httpURL(r.Favicon) {
			r.Favicon = ""
		}
		out.Results = append(out.Results, r)
	}
	if len(out.Results) != len(results) {
		out.TotalResults = len(out.Results)
		out.Filtered = len(out.Results) == 0
	}
	return out
}

// httpURL 链接能解析、是 http 或 https、有主机名。
func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

// searchHeaders 官方 WebSearch 工具的请求头。和对话不同：没有 X-Domain、企业头、X-Agent-Type、
// X-Private-Data、X-Agent-Purpose、X-Stainless-*、链路头，也不带设备令牌（官方只加在模型请求上）；
// UA 是 `CLI/<桌面端版本>`（工具自己拼的，不是请求框架那个三段的）。
//   - X-Agent-Intent 取会话的模式，桌面端没设时是 craft；X-Agent-Runtime 同对话（见 agentRuntime）。
//   - 会话 id、轮次 id 同这一轮的对话；X-Conversation-Message-ID 与 X-Request-ID 是同一个新生成的
//     去连字符 UUID v7。
//   - X-Department-Info 总是带，账号没有部门全名时是空值。官方另在腾讯内网版（IOA）上加企业头、
//     在专家会话里加专家头，SaaS 的普通搜索都没有。
//   - Accept-Language、Sec-Fetch-Mode、Accept-Encoding 是 Node 内置 fetch 自己补的（同网页登录码请求）。
//
// upstream.user_agent 照样整体覆盖 UA；client_name 配成 SaaS 时不带 X-IDE-*，同对话。
func (c *Client) searchHeaders(req *http.Request, a *auth.Auth, meta ChatMeta) {
	v := c.clientVersion(a)
	ua := "CLI/" + v
	if c.UserAgent != "" {
		ua = c.UserAgent
	}
	h := req.Header
	h.Set("Accept", "application/json")
	h.Set("Content-Type", "application/json;charset=UTF-8")
	h.Set("X-Requested-With", "XMLHttpRequest")
	h.Set("User-Agent", ua)
	if at := a.AccessTokenValue(); at != "" {
		h.Set("Authorization", "Bearer "+at)
	}
	h.Set("X-Agent-Intent", "craft")
	h.Set("X-Agent-Runtime", agentRuntime)
	h.Set("X-Conversation-ID", meta.ConversationID)
	h.Set("X-Conversation-Request-ID", meta.ConversationRequestID)
	id := session.NewUUIDv7Hex()
	h.Set("X-Conversation-Message-ID", id)
	h.Set("X-Request-ID", id)
	h.Set("X-Product", deploymentType)
	if name := c.attributionClientName(); name != "SaaS" {
		h.Set("X-IDE-Type", name)
		h.Set("X-IDE-Name", name)
		h.Set("X-IDE-Version", v)
	}
	h.Set("X-User-Id", a.UID)
	h.Set("X-Department-Info", a.DepartmentValue())
	h.Set("Accept-Language", "*")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Accept-Encoding", acceptEncodingNodeFetch)
}
