// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

// search_test.go 联网搜索：请求的地址、请求体、请求头照官方 WebSearch 工具（2.165.0），结果怎么整理，
// 失败怎么归类，入参怎么校验。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// searchTestClient 搜索请求都交给 fn；国际版入口单独给一个地址。
func searchTestClient(fn rtFunc) *Client {
	c := testClient(fn)
	c.ChatBaseGlobal = "https://global.example"
	c.GlobalEnabled = true
	return c
}

const searchOK = `{"query":"x","type":"text2text","provider":"p1","results":[{"title":"T","url":"https://a.example/1","snippet":"S"}],"total_results":1,"response_time_ms":12}`

// TestSearchRequestShape 地址、请求体和整份请求头。
func TestSearchRequestShape(t *testing.T) {
	var got *http.Request
	var body string
	var deadline time.Duration
	c := searchTestClient(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		got, body = r, string(b)
		if d, ok := r.Context().Deadline(); ok {
			deadline = time.Until(d)
		}
		return jsonResp(200, searchOK), nil
	})
	a := &auth.Auth{UID: "u1", AccessToken: "at-1", Domain: "www.workbuddy.cn"}
	q := SearchQuery{Query: "go 1.26 & <泛型>", AllowedDomains: []string{"go.dev"}, BlockedDomains: []string{"a.com", "b.com"}, Freshness: "m3"}
	if _, err := c.Search(context.Background(), a, q, ChatMeta{ConversationID: "conv-1", ConversationRequestID: "rid-1"}); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.URL.String() != "https://chat.example/agenttool/v1/search" {
		t.Errorf("请求 = %s %s", got.Method, got.URL)
	}
	// 黑名单拼进 query，字段顺序同官方，<、>、& 不转义。
	if want := `{"query":"go 1.26 & <泛型> (-site:a.com -site:b.com)","type":"text2text","max_results":5,"allowed_domains":["go.dev"],"freshness":"m3"}`; body != want {
		t.Errorf("请求体 = %s\n应为 %s", body, want)
	}
	if deadline <= 0 || deadline > searchTimeout {
		t.Errorf("超时剩 %v，应在 20 秒内", deadline)
	}
	msgID := got.Header.Get("X-Request-ID")
	if len(msgID) != 32 || strings.Contains(msgID, "-") {
		t.Errorf("X-Request-ID = %q，应为 32 位去连字符的 UUID", msgID)
	}
	want := http.Header{
		"Accept":                    {"application/json"},
		"Content-Type":              {"application/json;charset=UTF-8"},
		"X-Requested-With":          {"XMLHttpRequest"},
		"User-Agent":                {"CLI/" + defaultClientVersionCN},
		"Authorization":             {"Bearer at-1"},
		"X-Agent-Intent":            {"craft"},
		"X-Agent-Runtime":           {"local-agent"},
		"X-Conversation-Id":         {"conv-1"},
		"X-Conversation-Request-Id": {"rid-1"},
		"X-Conversation-Message-Id": {msgID},
		"X-Request-Id":              {msgID},
		"X-Product":                 {"SaaS"},
		"X-Ide-Type":                {"WorkBuddy"},
		"X-Ide-Name":                {"WorkBuddy"},
		"X-Ide-Version":             {defaultClientVersionCN},
		"X-User-Id":                 {"u1"},
		"X-Department-Info":         {""},
		"Accept-Language":           {"*"},
		"Sec-Fetch-Mode":            {"cors"},
		"Accept-Encoding":           {"br, gzip, deflate"},
	}
	if !reflect.DeepEqual(got.Header, want) {
		t.Errorf("请求头 = %v\n应为 %v", got.Header, want)
	}

	// 没有白名单、时间范围时请求体不带这两个字段；没给会话 id 时新生成一个（带连字符的 UUID）。
	// 国际版号发到国际版入口，同样带 X-Agent-Runtime，版本取国际版的；企业号带部门全名，不带企业头。
	g := &auth.Auth{UID: "g1", AccessToken: "at-g", Domain: "www.workbuddy.ai", EnterpriseID: "e1", DepartmentFullName: "研发部"}
	if _, err := c.Search(context.Background(), g, SearchQuery{Query: "ab"}, ChatMeta{}); err != nil {
		t.Fatal(err)
	}
	if want := `{"query":"ab","type":"text2text","max_results":5}`; body != want {
		t.Errorf("最简请求体 = %s", body)
	}
	h := got.Header
	if got.URL.String() != "https://global.example/agenttool/v1/search" || h.Get("X-Agent-Runtime") != "local-agent" ||
		h.Get("User-Agent") != "CLI/"+defaultClientVersionGlobal || h.Get("X-IDE-Version") != defaultClientVersionGlobal {
		t.Errorf("国际版请求 = %s，头 %v", got.URL, h)
	}
	if h.Get("X-Department-Info") != "研发部" || h.Get("X-Enterprise-Id") != "" || h.Get("X-Domain") != "" {
		t.Errorf("企业号的头 = %v", h)
	}
	if id := h.Get("X-Conversation-ID"); len(id) != 36 || h.Get("X-Conversation-Request-ID") != "" {
		t.Errorf("没给会话时：会话 id = %q，轮次 id = %q", id, h.Get("X-Conversation-Request-ID"))
	}

	// user_agent 整体覆盖 UA；client_version 覆盖版本；client_name=SaaS 不带 X-IDE-*。
	c.ClientVersion = "9.9.9"
	if _, err := c.Search(context.Background(), a, SearchQuery{Query: "ab"}, ChatMeta{}); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("User-Agent") != "CLI/9.9.9" || got.Header.Get("X-IDE-Version") != "9.9.9" {
		t.Errorf("client_version 覆盖后 = %v", got.Header)
	}
	c.UserAgent, c.ClientName = "custom-ua", "SaaS"
	if _, err := c.Search(context.Background(), a, SearchQuery{Query: "ab"}, ChatMeta{}); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("User-Agent") != "custom-ua" || got.Header.Get("X-IDE-Name") != "" || got.Header.Get("X-IDE-Type") != "" || got.Header.Get("X-IDE-Version") != "" {
		t.Errorf("user_agent、client_name=SaaS 之后 = %v", got.Header)
	}
}

// TestSearchResults 照官方整理结果：标题为空写 Untitled，链接不是 http/https 的去掉、链接为空的留着，
// 图标不是 http/https 的只去掉图标；去掉过条目时总数改成剩下的条数，全被去掉时标出来。
func TestSearchResults(t *testing.T) {
	cases := []struct {
		name string
		body string
		want SearchOutcome
	}{
		{"原样", searchOK, SearchOutcome{Provider: "p1", TotalResults: 1, ResponseTimeMs: 12,
			Results: []SearchResult{{Title: "T", URL: "https://a.example/1", Snippet: "S"}}}},
		{"整理", `{"provider":"p2","total_results":9,"results":[
			{"url":"http://b.example","snippet":"s1","site":"B","favicon":"ftp://b.example/i.ico"},
			{"title":"坏链接","url":"javascript:alert(1)"},
			{"title":"没有主机","url":"https://"},
			{"title":"没有链接","favicon":"https://c.example/i.png"}]}`,
			SearchOutcome{Provider: "p2", TotalResults: 2, Results: []SearchResult{
				{Title: "Untitled", URL: "http://b.example", Snippet: "s1", Site: "B"},
				{Title: "没有链接", Favicon: "https://c.example/i.png"}}}},
		{"全被去掉", `{"provider":"p3","total_results":1,"results":[{"title":"x","url":"file:///etc/passwd"}]}`,
			SearchOutcome{Provider: "p3", Results: []SearchResult{}, Filtered: true}},
		{"没有结果", `{"provider":"p4","results":[]}`, SearchOutcome{Provider: "p4", Results: []SearchResult{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := searchTestClient(func(*http.Request) (*http.Response, error) { return jsonResp(200, tc.body), nil })
			got, err := c.Search(context.Background(), &auth.Auth{AccessToken: "at"}, SearchQuery{Query: "ab"}, ChatMeta{})
			if err != nil || !reflect.DeepEqual(*got, tc.want) {
				t.Fatalf("= %+v, %v\n应为 %+v", got, err, tc.want)
			}
		})
	}
}

// TestSearchErrors HTTP 不是 2xx、回应带了非 0 的 code 返回带分类的 *Error（2xx 的业务码按回应体认，
// 认不出算 ErrClient）；传输出错、回应读不了、不是 JSON、地址不对返回普通 error。
func TestSearchErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		kind   ErrKind
	}{
		{"余额不足", 402, `{"code":14018,"msg":"insufficient credit"}`, ErrHardCredit},
		{"不支持的搜索类型", 400, `{"code":15003,"msg":"unsupported search type"}`, ErrClient},
		{"限流", 429, `{"code":429,"msg":"too many requests"}`, ErrSoftRate},
		{"上游故障", 502, `bad gateway`, ErrServer},
		{"2xx 带余额业务码", 200, `{"code":14018,"msg":"credit exhausted"}`, ErrHardCredit},
		{"2xx 带认不出的业务码", 200, `{"code":15999,"msg":"search failed"}`, ErrClient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := searchTestClient(func(*http.Request) (*http.Response, error) { return jsonResp(tc.status, tc.body), nil })
			_, err := c.Search(context.Background(), &auth.Auth{AccessToken: "at"}, SearchQuery{Query: "ab"}, ChatMeta{})
			var ue *Error
			if !errors.As(err, &ue) || ue.Kind != tc.kind || ue.Status != tc.status || !strings.Contains(ue.Msg, strings.TrimSpace(tc.body)) {
				t.Fatalf("err = %v，应为 %v（http %d）", err, tc.kind, tc.status)
			}
		})
	}

	plain := func(name string, c *Client, want string) {
		t.Helper()
		_, err := c.Search(context.Background(), &auth.Auth{AccessToken: "at"}, SearchQuery{Query: "ab"}, ChatMeta{})
		var ue *Error
		if err == nil || errors.As(err, &ue) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v，应为含 %q 的普通 error", name, err, want)
		}
	}
	boom := errors.New("boom")
	plain("传输出错", searchTestClient(func(*http.Request) (*http.Response, error) { return nil, boom }), "boom")
	plain("读回应出错", searchTestClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(errReader{})}, nil
	}), "read body")
	plain("不是 JSON", searchTestClient(func(*http.Request) (*http.Response, error) { return jsonResp(200, `<html>`), nil }), "parse response")
	bad := searchTestClient(func(*http.Request) (*http.Response, error) { return jsonResp(200, searchOK), nil })
	bad.ChatBaseCN = "://bad"
	plain("地址不对", bad, "missing protocol scheme")
}

// TestSearchQueryCheck 入参校验照官方 validateParams 与参数定义。
func TestSearchQueryCheck(t *testing.T) {
	for _, q := range []SearchQuery{
		{Query: "ab"}, {Query: "天气"}, {Query: " a"},
		{Query: "ab", Freshness: "d"}, {Query: "ab", Freshness: "d30"}, {Query: "ab", Freshness: "m12"}, {Query: "ab", Freshness: "y5"},
	} {
		if err := q.Check(); err != nil {
			t.Errorf("%+v 应通过，err=%v", q, err)
		}
	}
	for _, q := range []SearchQuery{
		{}, {Query: "   "}, {Query: "a"}, {Query: "天"},
		{Query: "ab", Freshness: "d0"}, {Query: "ab", Freshness: "d31"}, {Query: "ab", Freshness: "m13"},
		{Query: "ab", Freshness: "y6"}, {Query: "ab", Freshness: "w1"}, {Query: "ab", Freshness: "D1"},
	} {
		if err := q.Check(); err == nil {
			t.Errorf("%+v 应报错", q)
		}
	}
}
