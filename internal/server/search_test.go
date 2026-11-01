// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

// search_test.go 联网搜索：换号与罚号口径、选域、密钥计次，Claude Code 子请求的拦截与回应形状，
// /v1/search/messages，以及 /mcp 的工具调用。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/apikeys"
	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

const searchReply = `{"provider":"bocha","results":[
{"title":"Go 1.26 发布","url":"https://go.dev/blog/go1.26","snippet":"Go 1.26 带来了……"},
{"title":"没有链接的条目","snippet":"只有摘要"}],"total_results":2,"response_time_ms":30}`

// searchCall 假上游收到的一次请求。
type searchCall struct {
	path, token, body string
	header            http.Header
}

// searchRig 假上游：搜索请求按令牌交给 reply 决定回什么（没写的令牌回 searchReply），别的请求交给 other
// （没给或回 nil 时回对话的 sseOK）；每次请求都记下来。
type searchRig struct {
	mu    sync.Mutex
	calls []searchCall
	reply map[string]func() (*http.Response, error)
	other func(path string) *http.Response
}

func (s *searchRig) client() *upstream.Client {
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var b []byte
			if r.Body != nil { // GET 没有请求体
				b, _ = io.ReadAll(r.Body)
			}
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			s.mu.Lock()
			s.calls = append(s.calls, searchCall{path: r.URL.Path, token: tok, body: string(b), header: r.Header.Clone()})
			fn := s.reply[tok]
			s.mu.Unlock()
			if r.URL.Path != "/agenttool/v1/search" {
				if s.other != nil {
					if resp := s.other(r.URL.Path); resp != nil {
						return resp, nil
					}
				}
				return textResp(200, "text/event-stream", sseOK), nil
			}
			if fn != nil {
				return fn()
			}
			return textResp(200, "application/json", searchReply), nil
		})},
		ChatBaseCN:     "https://fake.example",
		BillingBaseCN:  "https://fake.example",
		ChatBaseGlobal: "https://global.example",
		GlobalEnabled:  true,
	}
}

// searches 搜索请求的令牌，按发出的顺序。
func (s *searchRig) searches() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		if c.path == "/agenttool/v1/search" {
			out = append(out, c.token)
		}
	}
	return out
}

func (s *searchRig) last() searchCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

func textResp(status int, ct, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader(body))}
}

func replyWith(status int, body string) func() (*http.Response, error) {
	return func() (*http.Response, error) { return textResp(status, "application/json", body), nil }
}

// newSearchHandler 两个国内版号，a 的积分高、先被选中；换号不退避。opts 在建 Handler 之前改配置。
func newSearchHandler(t *testing.T, rig *searchRig, opts ...func(*Config)) (*Handler, *pool.Pool) {
	t.Helper()
	old := rotateBackoffBase
	rotateBackoffBase = 0
	t.Cleanup(func() { rotateBackoffBase = old })
	p := testPoolWith(
		&auth.Auth{UID: "a", Nickname: "甲", AccessToken: "at-a", ExpiresAt: 9999999999},
		&auth.Auth{UID: "b", Nickname: "乙", AccessToken: "at-b", ExpiresAt: 9999999999},
	)
	p.SetCredits("a", 2000, 0)
	cfg := Config{Pool: p, Upstream: rig.client(), SoftCooldown: time.Minute}
	for _, o := range opts {
		o(&cfg)
	}
	return NewHandler(cfg), p
}

const searchMessagesBody = `{"model":"glm-5.2","max_tokens":1024,"stream":false,
"tools":[{"type":"web_search_20250305","name":"web_search","allowed_domains":["go.dev"],"blocked_domains":["spam.example"],"max_uses":8}],
"messages":[{"role":"user","content":"Perform a web search for the query: go 1.26 release notes"}]}`

// TestSearchMessagesResponse /v1/search/messages 成功：上游请求带上域名白名单、黑名单和客户端给的会话头；
// 回应依次是 server_tool_use、web_search_tool_result（只放有链接的条目）、text（照官方的写法列出结果，
// 摘要另作 citations）。
func TestSearchMessagesResponse(t *testing.T) {
	rig := &searchRig{}
	h, _ := newSearchHandler(t, rig)
	rec := post(h, "/v1/search/messages", searchMessagesBody, "X-Conversation-ID", "conv-1", "X-Conversation-Request-ID", "rid-1")
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	c := rig.last()
	if c.body != `{"query":"go 1.26 release notes (-site:spam.example)","type":"text2text","max_results":5,"allowed_domains":["go.dev"]}` {
		t.Errorf("上游请求体 = %s", c.body)
	}
	if c.header.Get("X-Conversation-ID") != "conv-1" || c.header.Get("X-Conversation-Request-ID") != "rid-1" {
		t.Errorf("会话头没转过去: %v", c.header)
	}
	var m struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Role       string `json:"role"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   []struct {
				Type, Title, URL string
			} `json:"content"`
			Text      string `json:"text"`
			Citations []struct {
				Type      string `json:"type"`
				URL       string `json:"url"`
				CitedText string `json:"cited_text"`
			} `json:"citations"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(m.ID, "msg_") || m.Type != "message" || m.Role != "assistant" || m.Model != "glm-5.2" || m.StopReason != "end_turn" || len(m.Content) != 3 {
		t.Fatalf("回应 = %s", rec.Body)
	}
	use, result, text := m.Content[0], m.Content[1], m.Content[2]
	if use.Type != "server_tool_use" || use.Name != "web_search" || !strings.HasPrefix(use.ID, "srvtoolu_") || string(use.Input) != `{"query":"go 1.26 release notes"}` {
		t.Errorf("server_tool_use = %+v", use)
	}
	if result.Type != "web_search_tool_result" || result.ToolUseID != use.ID || len(result.Content) != 1 ||
		result.Content[0].Type != "web_search_result" || result.Content[0].URL != "https://go.dev/blog/go1.26" {
		t.Errorf("web_search_tool_result = %+v", result)
	}
	if text.Type != "text" || !strings.HasPrefix(text.Text, "# Search Results for \"go 1.26 release notes\"\n\n## 1. [Go 1.26 发布](https://go.dev/blog/go1.26)") ||
		len(text.Citations) != 1 || text.Citations[0].CitedText != "Go 1.26 带来了……" {
		t.Errorf("text = %+v", text)
	}
}

// TestFormatSearch 文字照官方 formatSearchResults；没有结果、结果都不安全时各有说法。
func TestFormatSearch(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 30, 0, 0, time.FixedZone("CST", 8*3600)) // UTC 还是 31 日 15:30
	out := &upstream.SearchOutcome{Provider: "p", Results: []upstream.SearchResult{
		{Title: "A", URL: "https://a.example", Snippet: "sa"}, {Title: "B", URL: "https://b.example", Snippet: "sb"}}}
	want := "# Search Results for \"q\"\n\n## 1. [A](https://a.example)\n\nsa\n\n**URL:** https://a.example\n\n---\n\n" +
		"## 2. [B](https://b.example)\n\nsb\n\n**URL:** https://b.example\n\n---\n\n*Found 2 results • Provider: p • Search performed on 2026-10-31*"
	if got := formatSearch("q", out, now); got != want {
		t.Errorf("两条结果:\n%s\n应为\n%s", got, want)
	}
	out.Results = out.Results[:1]
	if got := formatSearch("q", out, now); !strings.HasSuffix(got, "*Found 1 result • Provider: p • Search performed on 2026-10-31*") {
		t.Errorf("一条结果: %s", got)
	}
	if got := formatSearch("q", &upstream.SearchOutcome{}, now); got != `No search results found for query: "q"` {
		t.Errorf("没有结果: %s", got)
	}
	if got := formatSearch("q", &upstream.SearchOutcome{Filtered: true}, now); got != `No safe search results found for query: "q"` {
		t.Errorf("都不安全: %s", got)
	}
}

// TestSearchRotation 换号与罚号：余额、限流、登录失效、封号、WAF 这几类罚号再换号；上游故障、404、认不出的
// 错误只换号不罚；请求本身的问题、上游满了不换号；超时不罚号、不换号；连不上上游喂连败计数再换号。
func TestSearchRotation(t *testing.T) {
	cases := []struct {
		name      string
		bad       func() (*http.Response, error)
		calls     []string
		code      int
		penalized bool
		fails     int
	}{
		{"余额不足：罚号换号", replyWith(402, `{"code":14018,"msg":"insufficient credit"}`), []string{"at-a", "at-b"}, 200, true, 0},
		{"限流：罚号换号", replyWith(429, `{"code":429,"msg":"too many requests"}`), []string{"at-a", "at-b"}, 200, true, 0},
		{"上游故障：只换号", replyWith(502, `bad gateway`), []string{"at-a", "at-b"}, 200, false, 0},
		{"404：只换号", replyWith(404, `not found`), []string{"at-a", "at-b"}, 200, false, 0},
		{"认不出的业务码：只换号", replyWith(200, `{"code":15999,"msg":"search failed"}`), []string{"at-a", "at-b"}, 200, false, 0},
		{"请求体有问题：不换号", replyWith(400, `{"code":11101,"msg":"Unmarshal chat params failed"}`), []string{"at-a"}, 400, false, 0},
		{"上游满了：不换号", replyWith(429, `{"code":6021,"msg":"queue.full"}`), []string{"at-a"}, 503, false, 0},
		{"超时：不罚号不换号", func() (*http.Response, error) { return nil, context.DeadlineExceeded }, []string{"at-a"}, 503, false, 0},
		{"连不上：喂连败计数再换号", func() (*http.Response, error) { return nil, errors.New("connection reset") }, []string{"at-a", "at-b"}, 200, false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := &searchRig{reply: map[string]func() (*http.Response, error){"at-a": tc.bad}}
			h, p := newSearchHandler(t, rig)
			rec := post(h, "/v1/search/messages", searchMessagesBody)
			if rec.Code != tc.code || strings.Join(rig.searches(), ",") != strings.Join(tc.calls, ",") {
				t.Fatalf("code=%d calls=%v body=%s", rec.Code, rig.searches(), rec.Body)
			}
			if tc.code != 200 && rec.Header().Get("X-Should-Retry") != "false" {
				t.Errorf("失败时应带 X-Should-Retry: false")
			}
			st, _ := p.Status("a")
			if (st.Cooling || st.Disabled) != tc.penalized || st.ConsecutiveFails != tc.fails || st.InFlight != 0 {
				t.Errorf("a 的状态 = cooling %v disabled %v fails %d inflight %d", st.Cooling, st.Disabled, st.ConsecutiveFails, st.InFlight)
			}
		})
	}
}

// TestSearchGaveUp 换完号仍没搜成：最后一次是上游的错就带上游原文（限流 429），没号可用 503，超时说明是超时。
func TestSearchGaveUp(t *testing.T) {
	rate := replyWith(429, `{"code":429,"msg":"too many requests"}`)
	rig := &searchRig{reply: map[string]func() (*http.Response, error){"at-a": rate, "at-b": rate}}
	h, _ := newSearchHandler(t, rig)
	rec := post(h, "/v1/search/messages", searchMessagesBody)
	if typ, msg := anthropicErr(t, rec); rec.Code != 429 || typ != "rate_limit_error" || !strings.Contains(msg, "too many requests") {
		t.Errorf("都限流: code=%d type=%s msg=%s", rec.Code, typ, msg)
	}
	h, _ = newSearchHandler(t, rig, func(c *Config) { c.Pool = testPoolWith() })
	rec = post(h, "/v1/search/messages", searchMessagesBody)
	if _, msg := anthropicErr(t, rec); rec.Code != 503 || !strings.Contains(msg, "all accounts are temporarily unavailable") {
		t.Errorf("没号可用: code=%d msg=%s", rec.Code, msg)
	}

	rig = &searchRig{reply: map[string]func() (*http.Response, error){"at-a": func() (*http.Response, error) { return nil, context.DeadlineExceeded }}}
	h, _ = newSearchHandler(t, rig)
	if _, msg := anthropicErr(t, post(h, "/v1/search/messages", searchMessagesBody)); !strings.Contains(msg, "timed out") {
		t.Errorf("超时的说明 = %s", msg)
	}
}

// postCtx 同 post，请求带上 ctx，用来模拟客户端中途断开。
func postCtx(h http.Handler, ctx context.Context, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(ctx))
	return rec
}

// TestSearchWaitsForSlot 号都在途满时搜索排队：有号放出名额就接着搜，醒来后名额被别的请求抢走不算换号、
// 接着等；等满 pool.in_flight_wait 回 503 并说明号都在忙。
func TestSearchWaitsForSlot(t *testing.T) {
	rig := &searchRig{}
	h, p := newSearchHandler(t, rig)
	p.SetMaxInFlight(1)
	p.SetInFlightWait(5 * time.Second)
	p.Acquire("a")
	p.Acquire("b")
	waiting := make(chan struct{})
	var once sync.Once
	oldStarted, oldAcquire := slotWaitStarted, acquireInFlight
	t.Cleanup(func() { slotWaitStarted, acquireInFlight = oldStarted, oldAcquire })
	slotWaitStarted = func() { once.Do(func() { close(waiting) }) }
	lost := 0
	acquireInFlight = func(p *pool.Pool, uid string) bool {
		if lost == 0 {
			lost++
			return false // 醒来后第一次抢名额被别的请求抢走了
		}
		return oldAcquire(p, uid)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- post(h, "/v1/search/messages", searchMessagesBody) }()
	waitFor(t, waiting, "开始等空位")
	p.Release("a")
	if rec := waitFor(t, done, "返回"); rec.Code != 200 || strings.Join(rig.searches(), ",") != "at-a" || lost != 1 {
		t.Fatalf("等到空位应接着用 a 搜: code=%d searches=%v body=%s", rec.Code, rig.searches(), rec.Body)
	}

	p.SetInFlightWait(30 * time.Millisecond)
	p.Acquire("a")
	rec := post(h, "/v1/search/messages", searchMessagesBody)
	if _, msg := anthropicErr(t, rec); rec.Code != 503 || !strings.Contains(msg, "all accounts are busy") || len(rig.searches()) != 1 {
		t.Errorf("等满上限: code=%d msg=%s searches=%v", rec.Code, msg, rig.searches())
	}
}

// TestSearchSlotTaken 选中的号发请求前名额被别的请求占了：换下一个号；这时客户端已经断开就不再换，请求记录
// 记成中断。
func TestSearchSlotTaken(t *testing.T) {
	rig := &searchRig{}
	h, _ := newSearchHandler(t, rig)
	old := acquireInFlight
	t.Cleanup(func() { acquireInFlight = old })
	acquireInFlight = func(p *pool.Pool, uid string) bool { return uid != "a" && old(p, uid) }
	if rec := post(h, "/v1/search/messages", searchMessagesBody); rec.Code != 200 || strings.Join(rig.searches(), ",") != "at-b" {
		t.Fatalf("应换到 b: code=%d searches=%v body=%s", rec.Code, rig.searches(), rec.Body)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	acquireInFlight = func(*pool.Pool, string) bool { cancel(); return false }
	rl := reqlog.New(reqlog.Config{})
	h.cfg.RequestLog = rl
	if rec := postCtx(h, ctx, "/v1/search/messages", searchMessagesBody); rec.Code != 503 || len(rig.searches()) != 1 {
		t.Errorf("客户端断开: code=%d searches=%v", rec.Code, rig.searches())
	}
	if e := rl.Snapshot().Recent[0]; e.Outcome != reqlog.OutcomeInterrupted {
		t.Errorf("请求记录 = %+v", e)
	}
}

// TestSearchClientGone 搜索中途客户端断开：不罚号、不换号，请求记录记成中断；换号前的退避里断开也不再换。
func TestSearchClientGone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rig := &searchRig{reply: map[string]func() (*http.Response, error){"at-a": func() (*http.Response, error) {
		cancel()
		return textResp(429, "application/json", `{"code":429,"msg":"too many requests"}`), nil
	}}}
	h, p := newSearchHandler(t, rig)
	rl := reqlog.New(reqlog.Config{})
	h.cfg.RequestLog = rl
	postCtx(h, ctx, "/v1/search/messages", searchMessagesBody)
	if st, _ := p.Status("a"); st.Cooling || strings.Join(rig.searches(), ",") != "at-a" {
		t.Errorf("不该罚号、换号: cooling=%v searches=%v", st.Cooling, rig.searches())
	}
	if e := rl.Snapshot().Recent[0]; e.Outcome != reqlog.OutcomeInterrupted {
		t.Errorf("请求记录 = %+v", e)
	}

	rotateBackoffBase = time.Hour // 退避封顶 8 秒，靠客户端断开叫醒
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	slow := func() (*http.Response, error) {
		time.AfterFunc(50*time.Millisecond, cancel)
		return textResp(502, "text/plain", "bad gateway"), nil
	}
	rig.reply["at-a"], rig.reply["at-b"] = slow, slow
	start := time.Now()
	postCtx(h, ctx, "/v1/search/messages", searchMessagesBody)
	if n, took := len(rig.searches()), time.Since(start); n != 2 || took < 50*time.Millisecond || took > 5*time.Second {
		t.Errorf("退避里断开应立刻停、不换号: 搜索 %d 次，用时 %s", n, took)
	}
}

// TestSearchWafIPBlock 两个号接连被 WAF 拦（拦的是出口 IP）：不再换第三个号。
func TestSearchWafIPBlock(t *testing.T) {
	waf := func() (*http.Response, error) { return textResp(403, "text/html", "<html>blocked</html>"), nil }
	rig := &searchRig{reply: map[string]func() (*http.Response, error){"at-a": waf, "at-b": waf, "at-c": waf}}
	h, _ := newSearchHandler(t, rig, func(c *Config) {
		c.Pool = testPoolWith(
			&auth.Auth{UID: "a", AccessToken: "at-a", ExpiresAt: 9999999999},
			&auth.Auth{UID: "b", AccessToken: "at-b", ExpiresAt: 9999999999},
			&auth.Auth{UID: "c", AccessToken: "at-c", ExpiresAt: 9999999999},
		)
	})
	if rec := post(h, "/v1/search/messages", searchMessagesBody); rec.Code != 503 || len(rig.searches()) != 2 {
		t.Errorf("code=%d searches=%v body=%s", rec.Code, rig.searches(), rec.Body)
	}
}

// TestSearchRefresh 令牌快过期先刷新再搜：刷新失败的号换掉，登录失效的禁用；刷新成功但存不下新令牌只记
// 日志，照样用新令牌搜；刷新失败时客户端已经断开就不再换号。
func TestSearchRefresh(t *testing.T) {
	refreshed := func(path string) *http.Response {
		switch {
		case strings.HasSuffix(path, "/v2/plugin/auth/token/refresh"):
			return textResp(200, "application/json", `{"code":0,"data":{"accessToken":"at-new","refreshToken":"rt-new","expiresIn":3600}}`)
		case strings.HasSuffix(path, "/v2/plugin/account"): // 刷新后核对账号
			return textResp(200, "application/json", `{"code":0,"data":{"uid":"a"}}`)
		}
		return nil
	}
	sessionDead := func(path string) *http.Response {
		return textResp(401, "application/json", `{"code":12153,"msg":"Offline user session not found"}`)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cases := []struct {
		name     string
		other    func(path string) *http.Response // 没给：刷新接口回对话的 SSE，解析不出新令牌
		ctx      context.Context
		code     int
		searches string
		disabled bool
	}{
		{"解析不出新令牌：换号", nil, context.Background(), 200, "at-b", false},
		{"登录失效：禁用再换号", sessionDead, context.Background(), 200, "at-b", true},
		{"存不下新令牌：照样搜", refreshed, context.Background(), 200, "at-new", false},
		{"刷新失败时客户端断开：不再换号", func(string) *http.Response { cancel(); return nil }, ctx, 503, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			oldOut := log.Writer()
			log.SetOutput(&buf)
			t.Cleanup(func() { log.SetOutput(oldOut) })
			rig := &searchRig{other: tc.other}
			p := testPoolWith(
				&auth.Auth{UID: "a", AccessToken: "at-a", RefreshToken: "rt-a", ExpiresAt: time.Now().Unix() + 60,
					FilePath: filepath.Join(t.TempDir(), "missing", "auth.json")},
				&auth.Auth{UID: "b", AccessToken: "at-b", ExpiresAt: 9999999999},
			)
			p.SetCredits("a", 2000, 0)
			h, _ := newSearchHandler(t, rig, func(c *Config) { c.Pool = p })
			rec := postCtx(h, tc.ctx, "/v1/search/messages", searchMessagesBody)
			if rec.Code != tc.code || strings.Join(rig.searches(), ",") != tc.searches {
				t.Fatalf("code=%d searches=%v body=%s", rec.Code, rig.searches(), rec.Body)
			}
			if st, _ := p.Status("a"); st.Disabled != tc.disabled || st.InFlight != 0 {
				t.Errorf("a 的状态 = disabled %v inflight %d", st.Disabled, st.InFlight)
			}
			if saved := strings.Contains(buf.String(), "search refresh acct=a: save auth failed"); saved != (tc.searches == "at-new") {
				t.Errorf("存盘失败的日志 = %q", buf.String())
			}
		})
	}
}

// TestSearchRealm 选域：model 带前缀照前缀；不带前缀按 cross_realm；关了国际版只用国内版。
func TestSearchRealm(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "c", AccessToken: "x", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, GlobalEnabled: true, Live: livecfg.New(livecfg.Snapshot{})})
	check := func(model, wantRealm string, wantFirst bool) {
		t.Helper()
		if realm, first := h.searchRealm(model, nil); realm != wantRealm || first != wantFirst {
			t.Errorf("searchRealm(%q, nil) = %q, %v；应为 %q, %v", model, realm, first, wantRealm, wantFirst)
		}
	}
	check("global:glm-5.2", "global", false)
	check("cn:glm-5.2", "cn", false)
	check("glm-5.2", "cn", false)
	check("", "cn", false)
	h.cfg.Live.Store(livecfg.Snapshot{CrossRealm: livecfg.CrossRealmMerge})
	check("glm-5.2", "", false)
	h.cfg.Live.Store(livecfg.Snapshot{CrossRealm: livecfg.CrossRealmCNFirst})
	check("", "cn", true)
	h.cfg.GlobalEnabled = false
	check("", "cn", false)

	// 国内版优先：国内版没有能接的号时在国际版选，国际版的号搜的模型一列写 global:web_search。
	rig := &searchRig{}
	g := &auth.Auth{UID: "g", AccessToken: "at-g", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
	rl := reqlog.New(reqlog.Config{})
	h = NewHandler(Config{Pool: testPoolWith(g), Upstream: rig.client(), GlobalEnabled: true, RequestLog: rl,
		Live: livecfg.New(livecfg.Snapshot{CrossRealm: livecfg.CrossRealmCNFirst})})
	if rec := post(h, "/v1/search/messages", searchMessagesBody); rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if c := rig.last(); c.token != "at-g" {
		t.Errorf("国际版的号: %+v", c)
	}
	if e := rl.Snapshot().Recent[0]; e.Model != "global:web_search" || e.Path != "/v1/search/messages" || e.Attempts != 1 || !e.OK {
		t.Errorf("请求记录 = %+v", e)
	}
}

// TestSearchKeys 客户端密钥：搜索不看模型白名单，计一次请求；没搜成的退还；用完上限回 429。
func TestSearchKeys(t *testing.T) {
	rig := &searchRig{reply: map[string]func() (*http.Response, error){}}
	h, _ := newSearchHandler(t, rig)
	store, err := apikeys.Open("")
	if err != nil {
		t.Fatal(err)
	}
	h.cfg.Keys = store
	h.cfg.Live = livecfg.New(livecfg.Snapshot{})
	k := createKey(t, store, apikeys.Input{Name: "搜", Enabled: true, Models: []string{"only-this"}, DailyLimit: apikeys.Limits{Requests: 1}})
	used := func() int64 {
		got, _ := store.Get(k.ID, time.Now())
		return got.Today.Requests
	}
	// 失败的退还。
	fail := replyWith(502, `bad gateway`)
	rig.reply["at-a"], rig.reply["at-b"] = fail, fail
	if rec := post(h, "/v1/search/messages", searchMessagesBody, "x-api-key", k.Secret); rec.Code != 503 || used() != 0 {
		t.Fatalf("失败: code=%d used=%d body=%s", rec.Code, used(), rec.Body)
	}
	delete(rig.reply, "at-a")
	if rec := post(h, "/v1/search/messages", searchMessagesBody, "x-api-key", k.Secret); rec.Code != 200 || used() != 1 {
		t.Fatalf("成功: code=%d used=%d body=%s", rec.Code, used(), rec.Body)
	}
	rec := post(h, "/v1/search/messages", searchMessagesBody, "x-api-key", k.Secret)
	if typ, _ := anthropicErr(t, rec); rec.Code != 429 || typ != "rate_limit_error" || rec.Header().Get("X-Should-Retry") != "false" {
		t.Errorf("用完上限: code=%d type=%s", rec.Code, typ)
	}
	if rec := post(h, "/v1/search/messages", searchMessagesBody); rec.Code != 401 {
		t.Errorf("不带密钥: code=%d", rec.Code)
	}
}

// TestSearchMessagesBadRequests 没声明搜索工具、读不出搜索词、不是 JSON 都回 400，不发上游。
func TestSearchMessagesBadRequests(t *testing.T) {
	rig := &searchRig{}
	h, _ := newSearchHandler(t, rig)
	for name, body := range map[string]string{
		"没声明搜索工具": `{"model":"m","messages":[{"role":"user","content":"hello"}]}`,
		"搜索词太短":   `{"model":"m","tools":[{"name":"web_search"}],"messages":[{"role":"user","content":"Perform a web search for the query: a"}]}`,
		"没有用户消息":  `{"model":"m","tools":[{"type":"web_search"}],"messages":[{"role":"assistant","content":"hello there"}]}`,
		"不是 JSON": `{`,
	} {
		if rec := post(h, "/v1/search/messages", body); rec.Code != 400 {
			t.Errorf("%s: code=%d body=%s", name, rec.Code, rec.Body)
		}
	}
	if n := len(rig.searches()); n != 0 {
		t.Errorf("不该发上游，发了 %d 次", n)
	}
	// 用户消息是文本块：拼起来当搜索词，前缀照样去掉。
	if rec := post(h, "/v1/search/messages", `{"model":"m","tools":[{"type":"web_search"}],"messages":[
		{"role":"user","content":[{"type":"text","text":"perform a web search for the query:"},{"type":"image"},{"type":"text","text":"深圳 天气"}]}]}`); rec.Code != 200 {
		t.Fatalf("文本块: code=%d body=%s", rec.Code, rec.Body)
	}
	if body := rig.last().body; !strings.Contains(body, `"query":"深圳 天气"`) {
		t.Errorf("文本块拼出的搜索词: %s", body)
	}
}

// claudeCodeSearchBody Claude Code 2.1.x WebSearch 的子请求（形状读本机安装的程序确认）。
const claudeCodeSearchBody = `{"model":"glm-5.2","max_tokens":32000,"stream":true,
"system":[{"type":"text","text":"You run a web search for another Claude model, which is waiting on the result and can search again. Search once, then reply with a brief summary of the results."}],
"messages":[{"role":"user","content":[{"type":"text","text":"Perform a web search for the query: go 1.26 release notes"}]}],
"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":8}],
"tool_choice":{"type":"tool","name":"web_search"},"thinking":{"type":"disabled"}}`

// sseEvents 把 SSE 拆成（事件名，数据）。
func sseEvents(t *testing.T, body string) (names []string, data []map[string]any) {
	t.Helper()
	for _, frame := range strings.Split(strings.TrimSpace(body), "\n\n") {
		lines := strings.SplitN(frame, "\n", 2)
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "event: ") || !strings.HasPrefix(lines[1], "data: ") {
			t.Fatalf("不是 SSE 帧: %q", frame)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &d); err != nil {
			t.Fatal(err)
		}
		names, data = append(names, strings.TrimPrefix(lines[0], "event: ")), append(data, d)
	}
	return names, data
}

// TestClaudeCodeSearch /v1/messages 里认出 Claude Code WebSearch 的子请求直接搜，不交给模型；流式回应的事件
// 照 Anthropic 服务端搜索的顺序，请求记录照记。
func TestClaudeCodeSearch(t *testing.T) {
	rig := &searchRig{}
	h, _ := newSearchHandler(t, rig)
	rl := reqlog.New(reqlog.Config{})
	h.cfg.RequestLog = rl
	rec := post(h, "/v1/messages", claudeCodeSearchBody)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("code=%d ct=%s body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if s := rig.searches(); len(s) != 1 || len(rig.calls) != 1 {
		t.Fatalf("应只发一次搜索、不发对话: %+v", rig.calls)
	}
	names, data := sseEvents(t, rec.Body.String())
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_stop", "content_block_start", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("事件 = %v", names)
	}
	for i, d := range data {
		if d["type"] != names[i] {
			t.Errorf("第 %d 帧 type=%v，应为 %s", i, d["type"], names[i])
		}
	}
	block := func(i int) map[string]any { return data[i]["content_block"].(map[string]any) }
	delta := func(i int) map[string]any { return data[i]["delta"].(map[string]any) }
	use := block(1)
	if use["type"] != "server_tool_use" || use["name"] != "web_search" || delta(2)["partial_json"] != `{"query":"go 1.26 release notes"}` {
		t.Errorf("server_tool_use: %v %v", use, delta(2))
	}
	result := block(4)
	if result["type"] != "web_search_tool_result" || result["tool_use_id"] != use["id"] || len(result["content"].([]any)) != 1 {
		t.Errorf("web_search_tool_result: %v", result)
	}
	if text := delta(7)["text"].(string); block(6)["type"] != "text" || !strings.Contains(text, "Go 1.26 带来了……") {
		t.Errorf("text: %v", text)
	}
	if d := delta(9); d["stop_reason"] != "end_turn" {
		t.Errorf("message_delta: %v", d)
	}
	if e := rl.Snapshot().Recent[0]; e.Path != "/v1/messages" || e.Model != "web_search" || e.Account != "甲(a)" || !e.OK {
		t.Errorf("请求记录 = %+v", e)
	}
	// 非流式同样直接搜。
	rec = post(h, "/v1/messages", strings.Replace(claudeCodeSearchBody, `"stream":true`, `"stream":false`, 1))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"type":"web_search_tool_result"`) || len(rig.searches()) != 2 {
		t.Errorf("非流式: code=%d body=%s", rec.Code, rec.Body)
	}
}

// TestClaudeCodeSearchNotIntercepted 不是搜索子请求的照常交给模型：声明了服务端搜索工具但消息不是那句话、
// 客户端自己实现的 web_search 函数工具。
func TestClaudeCodeSearchNotIntercepted(t *testing.T) {
	for name, body := range map[string]string{
		"普通对话带服务端搜索工具": `{"model":"glm-5.2","max_tokens":10,"messages":[{"role":"user","content":"今天有什么新闻"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
		"客户端自己的函数工具": `{"model":"glm-5.2","max_tokens":10,"messages":[{"role":"user","content":"Perform a web search for the query: x y"}],
			"tools":[{"name":"web_search","description":"d","input_schema":{"type":"object"}}]}`,
		"不是 JSON 交给对话报错": `{`,
	} {
		rig := &searchRig{}
		h, _ := newSearchHandler(t, rig)
		post(h, "/v1/messages", body)
		if len(rig.searches()) != 0 {
			t.Errorf("%s: 不该搜索", name)
		}
		if name != "不是 JSON 交给对话报错" && len(rig.calls) != 1 {
			t.Errorf("%s: 应交给对话，上游请求 %+v", name, rig.calls)
		}
	}
}

// mcpPost 发一条 JSON-RPC 到 /mcp（无状态模式下不必先初始化）。
func mcpPost(t *testing.T, h http.Handler, body string, hdr ...string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// toolResult 取 tools/call 回应里的文字和 isError。
func toolResult(t *testing.T, out map[string]any) (string, bool) {
	t.Helper()
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("没有 result: %v", out)
	}
	content := res["content"].([]any)
	isErr, _ := res["isError"].(bool)
	return content[0].(map[string]any)["text"].(string), isErr
}

const mcpCall = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"web_search","arguments":%s}}`

// TestMCPWebSearch /mcp：列工具、调搜索、入参不对和搜索失败回 isError；调搜索记一行请求记录，列工具不记；
// 要密钥；只收 POST。
func TestMCPWebSearch(t *testing.T) {
	rig := &searchRig{reply: map[string]func() (*http.Response, error){}}
	rl := reqlog.New(reqlog.Config{})
	h, _ := newSearchHandler(t, rig, func(c *Config) {
		c.RequestLog, c.Version = rl, "9.9.9"
		c.Live = livecfg.New(livecfg.Snapshot{APIKey: "sk-admin", RecordClientInfo: true})
	})
	admin := []string{"Authorization", "Bearer sk-admin"}

	rec, out := mcpPost(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, admin...)
	info, _ := out["result"].(map[string]any)["serverInfo"].(map[string]any)
	if rec.Code != 200 || info["name"] != ServiceName || info["version"] != "9.9.9" {
		t.Fatalf("initialize: code=%d body=%s", rec.Code, rec.Body)
	}
	_, out = mcpPost(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, admin...)
	tools := out["result"].(map[string]any)["tools"].([]any)
	tool := tools[0].(map[string]any)
	schema := tool["inputSchema"].(map[string]any)
	if len(tools) != 1 || tool["name"] != "web_search" || schema["required"].([]any)[0] != "query" || schema["properties"].(map[string]any)["freshness"] == nil {
		t.Fatalf("tools/list: %v", out)
	}

	_, out = mcpPost(t, h, strings.Replace(mcpCall, "%s", `{"query":"go 1.26","allowed_domains":["go.dev"],"freshness":"m1"}`, 1),
		append(admin, "User-Agent", "codex-test", "X-Conversation-ID", "conv-9")...)
	if text, isErr := toolResult(t, out); isErr || !strings.HasPrefix(text, `# Search Results for "go 1.26"`) {
		t.Fatalf("tools/call: %v", out)
	}
	if c := rig.last(); c.body != `{"query":"go 1.26","type":"text2text","max_results":5,"allowed_domains":["go.dev"],"freshness":"m1"}` || c.header.Get("X-Conversation-ID") != "conv-9" {
		t.Errorf("上游请求: %+v", c)
	}
	recent := rl.Snapshot().Recent
	if len(recent) != 1 || recent[0].Path != "/mcp" || recent[0].Model != "web_search" || recent[0].APIKey != "管理员" ||
		recent[0].UserAgent != "codex-test" || !recent[0].OK {
		t.Fatalf("请求记录（只记搜索）= %+v", recent)
	}

	if text, isErr := toolResult(t, func() map[string]any {
		_, out := mcpPost(t, h, strings.Replace(mcpCall, "%s", `{"query":"go","freshness":"w2"}`, 1), admin...)
		return out
	}()); !isErr || !strings.Contains(text, "freshness") {
		t.Errorf("时间范围不对: %s", text)
	}
	fail := replyWith(502, `bad gateway`)
	rig.reply["at-a"], rig.reply["at-b"] = fail, fail
	_, out = mcpPost(t, h, strings.Replace(mcpCall, "%s", `{"query":"go 1.26"}`, 1), admin...)
	if text, isErr := toolResult(t, out); !isErr || !strings.HasPrefix(text, "web search failed: bad gateway") {
		t.Errorf("搜索失败: %s", text)
	}
	if e := rl.Snapshot().Recent[0]; e.OK || e.Status != 503 || !strings.Contains(e.Error, "bad gateway") {
		t.Errorf("失败的请求记录 = %+v", e)
	}

	if rec, _ := mcpPost(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); rec.Code != 401 {
		t.Errorf("不带密钥: code=%d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer sk-admin")
	get := httptest.NewRecorder()
	h.ServeHTTP(get, req)
	if get.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: code=%d", get.Code)
	}
}

// TestMCPKeyLimit /mcp 的客户端密钥用完上限：工具结果报错说明原因，不发上游。
func TestMCPKeyLimit(t *testing.T) {
	rig := &searchRig{}
	h, _ := newSearchHandler(t, rig)
	store, _ := apikeys.Open("")
	h.cfg.Keys = store
	h.cfg.Live = livecfg.New(livecfg.Snapshot{})
	k := createKey(t, store, apikeys.Input{Name: "搜", Enabled: true, TotalLimit: apikeys.Limits{Requests: 1}})
	call := strings.Replace(mcpCall, "%s", `{"query":"go 1.26"}`, 1)
	if _, out := mcpPost(t, h, call, "Authorization", "Bearer "+k.Secret); func() bool { _, e := toolResult(t, out); return e }() {
		t.Fatalf("第一次应成功: %v", out)
	}
	_, out := mcpPost(t, h, call, "Authorization", "Bearer "+k.Secret)
	if text, isErr := toolResult(t, out); !isErr || len(rig.searches()) != 1 || text == "" {
		t.Errorf("用完上限: %s，搜索 %d 次", text, len(rig.searches()))
	}
}
