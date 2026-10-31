// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// capqueue_test.go 上游回 6020（排进容量队列）时在原号上排队、到号原样重发；没等到号回 503，不罚号、
// 不换号；6022（这个号排队的数量到上限）换号、不罚号。
package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

const (
	queued6020  = `{"code":6020,"msg":"queue.waiting","data":{"queue_position":2,"retry_after":0}}`
	queueWait   = `{"code":0,"data":{"status":"waiting","queue_position":1}}`
	queueReady  = `{"code":0,"data":{"status":"ready"}}`
	userLimit22 = `{"code":6022,"msg":"queue.user_limit"}`
)

// queueReply 假上游的一个回应。
type queueReply struct {
	status int
	body   string
}

// queueUpstream 假上游：对话按 chats 依次回、排队查询按 statuses 依次回（用到最后一个就一直回它），
// 取消回 cancel（没设回 code 0），别的路径回 code 0；记下每个请求。onStatus 不为 nil 时每次查询前调用；
// ctxAware 为 true 时请求的 context 已取消就回传输错误（同真实的 Transport）。
type queueUpstream struct {
	mu       sync.Mutex
	chats    []queueReply
	statuses []queueReply
	cancel   *queueReply
	onStatus func()
	ctxAware bool
	reqs     []*http.Request
	bodies   []string
	ctxErrs  []error // 请求到达时它的 context 有没有被取消
}

func (u *queueUpstream) client() *upstream.Client {
	next := func(list *[]queueReply) queueReply {
		r := (*list)[0]
		if len(*list) > 1 {
			*list = (*list)[1:]
		}
		return r
	}
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(r.Body)
			u.mu.Lock()
			u.reqs, u.bodies, u.ctxErrs = append(u.reqs, r), append(u.bodies, string(b)), append(u.ctxErrs, r.Context().Err())
			rep, hook := queueReply{200, `{"code":0}`}, func() {}
			switch r.URL.Path {
			case "/v2/chat/completions":
				rep = next(&u.chats)
			case "/v2/chat/queue/status":
				rep = next(&u.statuses)
				if u.onStatus != nil {
					hook = u.onStatus
				}
			case "/v2/chat/queue/cancel":
				if u.cancel != nil {
					rep = *u.cancel
				}
			}
			ctxAware := u.ctxAware
			u.mu.Unlock()
			hook()
			if err := r.Context().Err(); ctxAware && err != nil {
				return nil, err
			}
			ct := "application/json"
			if rep.body == sseOK {
				ct = "text/event-stream"
			}
			return &http.Response{StatusCode: rep.status, Header: http.Header{"Content-Type": {ct}},
				Body: io.NopCloser(strings.NewReader(rep.body))}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
}

// calls 发到 path 的请求和请求体。
func (u *queueUpstream) calls(path string) (reqs []*http.Request, bodies []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for i, r := range u.reqs {
		if r.URL.Path == path {
			reqs, bodies = append(reqs, r), append(bodies, u.bodies[i])
		}
	}
	return reqs, bodies
}

// ctxErrsOf 发到 path 的请求到达时各自 context 的 Err()。
func (u *queueUpstream) ctxErrsOf(path string) []error {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []error
	for i, r := range u.reqs {
		if r.URL.Path == path {
			out = append(out, u.ctxErrs[i])
		}
	}
	return out
}

// chatAuths 每次对话请求用的 Authorization。
func (u *queueUpstream) chatAuths() []string {
	reqs, _ := u.calls("/v2/chat/completions")
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Header.Get("Authorization"))
	}
	return out
}

// queueHandler 两个号的网关，a 积分更高、先被选中；查询间隔换成 1 毫秒，换号不退避。
func queueHandler(t *testing.T, u *queueUpstream, wait time.Duration) (*Handler, *pool.Pool) {
	t.Helper()
	old := queuePollInterval
	queuePollInterval = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { queuePollInterval = old })
	fastRotate(t)
	p := testPoolWith(
		&auth.Auth{UID: "a", AccessToken: "at-a", ExpiresAt: 9999999999},
		&auth.Auth{UID: "b", AccessToken: "at-b", ExpiresAt: 9999999999},
	)
	p.SetCredits("a", 2000, 0)
	p.SetCredits("b", 1000, 0)
	p.SetInFlightWait(wait)
	return NewHandler(Config{Pool: p, Upstream: u.client()}), p
}

func serveQueueChat(h *Handler, ctx context.Context) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true}`)).WithContext(ctx))
	return rec
}

// notCooling 两个号都没被冷却。
func notCooling(t *testing.T, p *pool.Pool) {
	t.Helper()
	for _, uid := range []string{"a", "b"} {
		if st, _ := p.Status(uid); st.Cooling {
			t.Errorf("%s 不应被冷却：%+v", uid, st)
		}
	}
}

// TestChatCapacityQueueReady 6020 后在同一个号上查排队状态：查询出错照样再查，waiting 接着等，ready 就用
// 同一个号原样重发。查询带的是这一轮的会话请求 id 和会话 id，请求体是实际发上游的模型；不取消、不罚号。
func TestChatCapacityQueueReady(t *testing.T) {
	u := &queueUpstream{
		chats:    []queueReply{{429, queued6020}, {200, sseOK}},
		statuses: []queueReply{{502, "bad gateway"}, {200, queueWait}, {200, queueReady}},
	}
	h, p := queueHandler(t, u, 5*time.Second)
	if rec := serveQueueChat(h, context.Background()); rec.Code != http.StatusOK {
		t.Fatalf("到号重发后应成功：code=%d body=%s", rec.Code, rec.Body)
	}
	if got := u.chatAuths(); len(got) != 2 || got[0] != "Bearer at-a" || got[1] != "Bearer at-a" {
		t.Fatalf("对话应在同一个号上发两次，实际 %v", got)
	}
	chats, _ := u.calls("/v2/chat/completions")
	statuses, bodies := u.calls("/v2/chat/queue/status")
	if len(statuses) != 3 {
		t.Fatalf("查询次数 = %d，应为 3", len(statuses))
	}
	for i, r := range statuses {
		if r.Header.Get("X-Conversation-Request-ID") != chats[0].Header.Get("X-Conversation-Request-ID") ||
			r.Header.Get("X-Conversation-ID") != chats[0].Header.Get("X-Conversation-ID") ||
			r.Header.Get("Authorization") != "Bearer at-a" || bodies[i] != `{"model":"glm-5.2"}` {
			t.Errorf("第 %d 次查询的头或请求体不对：%v %s", i+1, r.Header, bodies[i])
		}
	}
	if chats[0].Header.Get("X-Conversation-Request-ID") == "" ||
		chats[1].Header.Get("X-Conversation-Request-ID") != chats[0].Header.Get("X-Conversation-Request-ID") {
		t.Error("重发应沿用这一轮的会话请求 id")
	}
	if cancels, _ := u.calls("/v2/chat/queue/cancel"); len(cancels) != 0 {
		t.Errorf("到号了不应取消排队，取消了 %d 次", len(cancels))
	}
	notCooling(t, p)
	if st, _ := p.Status("a"); st.InFlight != 0 {
		t.Errorf("请求结束后应释放名额，在途 %d", st.InFlight)
	}
}

// TestChatCapacityQueueRequeue 重发又回 6020：接着排，到号再重发。
func TestChatCapacityQueueRequeue(t *testing.T) {
	u := &queueUpstream{
		chats:    []queueReply{{429, queued6020}, {429, queued6020}, {200, sseOK}},
		statuses: []queueReply{{200, queueReady}},
	}
	h, _ := queueHandler(t, u, 5*time.Second)
	if rec := serveQueueChat(h, context.Background()); rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if got := u.chatAuths(); len(got) != 3 || got[2] != "Bearer at-a" {
		t.Fatalf("应在同一个号上发三次，实际 %v", got)
	}
}

// TestChatCapacityQueueGivesUp 没等到号回 503（upstream_busy，带上游原文和 6020 的提示），不换号、不罚号：
// 上游说过期时不取消（同官方），等满上限、不排队时先取消。
func TestChatCapacityQueueGivesUp(t *testing.T) {
	cases := []struct {
		name     string
		chat     string
		statuses []queueReply
		wait     time.Duration
		polls    int
		cancels  int
	}{
		{"上游说排队过期", queued6020, []queueReply{{200, queueWait}, {200, `{"code":0,"data":{"status":"expired","message":"排队超时"}}`}}, 5 * time.Second, 2, 0},
		{"等满上限", queued6020, []queueReply{{200, queueWait}}, 30 * time.Millisecond, -1, 1},
		{"retry_after 比上限还长：不查，等满就取消", `{"code":6020,"data":{"retry_after":10}}`, []queueReply{{200, queueReady}}, 30 * time.Millisecond, 0, 1},
		{"in_flight_wait 为 0：不排队", queued6020, []queueReply{{200, queueReady}}, 0, 0, 1},
		{"取消请求失败也照样回 503", queued6020, []queueReply{{200, queueReady}}, 0, 0, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := &queueUpstream{chats: []queueReply{{429, tc.chat}}, statuses: tc.statuses}
			if tc.cancels < 0 {
				u.cancel = &queueReply{500, "boom"}
				tc.cancels = 1
			}
			h, p := queueHandler(t, u, tc.wait)
			rec := serveQueueChat(h, context.Background())
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
			}
			msg, code, hint := fullErrorBody(t, rec)
			if code != "upstream_busy" || !strings.Contains(msg, `"code":6020`) || !strings.Contains(hint, "6020") {
				t.Errorf("错误体 code=%q msg=%q hint=%q", code, msg, hint)
			}
			if got := u.chatAuths(); len(got) != 1 {
				t.Errorf("没等到号不应换号重试，对话发了 %v", got)
			}
			polls, _ := u.calls("/v2/chat/queue/status")
			if tc.polls >= 0 && len(polls) != tc.polls {
				t.Errorf("查询次数 = %d，应为 %d", len(polls), tc.polls)
			}
			cancels, bodies := u.calls("/v2/chat/queue/cancel")
			if len(cancels) != tc.cancels {
				t.Fatalf("取消次数 = %d，应为 %d", len(cancels), tc.cancels)
			}
			if tc.cancels > 0 && (bodies[0] != `{"model":"glm-5.2"}` || cancels[0].Header.Get("X-Conversation-ID") != "") {
				t.Errorf("取消的请求体 %s，X-Conversation-ID=%q", bodies[0], cancels[0].Header.Get("X-Conversation-ID"))
			}
			notCooling(t, p)
		})
	}
}

// TestChatCapacityQueueClientGone 排队期间客户端断开（查询途中断开，或两次查询之间断开）：取消排队
// （取消请求不随客户端一起被掐），不再查、不重发。
func TestChatCapacityQueueClientGone(t *testing.T) {
	for _, duringPoll := range []bool{true, false} {
		name := map[bool]string{true: "查询途中断开", false: "两次查询之间断开"}[duringPoll]
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			u := &queueUpstream{
				chats:    []queueReply{{429, queued6020}},
				statuses: []queueReply{{200, queueWait}},
				onStatus: cancel,
				ctxAware: duringPoll,
			}
			h, _ := queueHandler(t, u, 5*time.Second)
			if !duringPoll {
				// 查询照常回 waiting，下一次查询要等很久：断开只能在等的时候被发现。
				queuePollInterval = func(int) time.Duration { return time.Hour }
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- serveQueueChat(h, ctx) }()
			waitFor(t, done, "返回")
			if errs := u.ctxErrsOf("/v2/chat/queue/cancel"); len(errs) != 1 || errs[0] != nil {
				t.Fatalf("客户端断开应发一次取消，且取消请求不能被掐：%v", errs)
			}
			if polls, _ := u.calls("/v2/chat/queue/status"); len(polls) != 1 {
				t.Errorf("断开后不应再查，查了 %d 次", len(polls))
			}
			if got := u.chatAuths(); len(got) != 1 {
				t.Errorf("客户端断开后不应重发，对话发了 %v", got)
			}
		})
	}
}

// TestChat6022Rotates 6022（这个号排队的数量到上限）：换号，不罚号；所有号都是 6022 时回 503，带上游原文
// 和 6022 的提示。
func TestChat6022Rotates(t *testing.T) {
	u := &queueUpstream{chats: []queueReply{{429, userLimit22}, {200, sseOK}}}
	h, p := queueHandler(t, u, 5*time.Second)
	if rec := serveQueueChat(h, context.Background()); rec.Code != http.StatusOK {
		t.Fatalf("换号后应成功：code=%d body=%s", rec.Code, rec.Body)
	}
	if got := u.chatAuths(); len(got) != 2 || got[0] != "Bearer at-a" || got[1] != "Bearer at-b" {
		t.Fatalf("应先 a 后 b，实际 %v", got)
	}
	notCooling(t, p)

	u = &queueUpstream{chats: []queueReply{{429, userLimit22}}}
	h, p = queueHandler(t, u, 5*time.Second)
	rec := serveQueueChat(h, context.Background())
	msg, code, hint := fullErrorBody(t, rec)
	if rec.Code != http.StatusServiceUnavailable || code != "upstream_busy" || !strings.Contains(msg, `"code":6022`) || !strings.Contains(hint, "6022") {
		t.Fatalf("所有号都 6022：status=%d code=%q msg=%q hint=%q", rec.Code, code, msg, hint)
	}
	if got := u.chatAuths(); len(got) != 2 {
		t.Errorf("两个号各试一次，实际 %v", got)
	}
	notCooling(t, p)
}
