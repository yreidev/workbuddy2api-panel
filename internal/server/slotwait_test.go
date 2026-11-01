// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// slotwait_test.go 能接请求的号在途都满时排队等空位（见 handler.go 的 waitSlot）。
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
)

// newSlotWaitHandler 只有一个号、在途上限 1、唯一的名额先被别的请求占着的网关。waiting 在请求
// 开始等空位时关闭。
func newSlotWaitHandler(t *testing.T, wait time.Duration) (*Handler, *pool.Pool, <-chan struct{}) {
	t.Helper()
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at-u1", ExpiresAt: 9999999999})
	p.SetMaxInFlight(1)
	p.SetInFlightWait(wait)
	if !p.Acquire("u1") {
		t.Fatal("Acquire 应成功")
	}
	waiting := make(chan struct{})
	var once sync.Once
	old := slotWaitStarted
	slotWaitStarted = func() { once.Do(func() { close(waiting) }) }
	t.Cleanup(func() { slotWaitStarted = old })
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	return NewHandler(Config{Pool: p, Upstream: up}), p, waiting
}

// serveChatAsync 在后台发一个对话请求，返回拿结果的 channel。
func serveChatAsync(h *Handler, ctx context.Context) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
		h.ServeHTTP(rec, req)
		done <- rec
	}()
	return done
}

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("等了 5 秒还没%s", what)
	}
	var zero T
	return zero
}

func shortSlotPoll(t *testing.T, d time.Duration) {
	t.Helper()
	old := slotPoll
	slotPoll = d
	t.Cleanup(func() { slotPoll = old })
}

// TestChatWaitsForFreeSlot 号都在途满时请求排队，别的请求放出名额后接着转发；中间按 slotPoll 重看
// 多少次都不算换号。
func TestChatWaitsForFreeSlot(t *testing.T) {
	shortSlotPoll(t, 5*time.Millisecond)
	h, p, waiting := newSlotWaitHandler(t, 5*time.Second)
	done := serveChatAsync(h, context.Background())
	waitFor(t, waiting, "开始等空位")
	time.Sleep(50 * time.Millisecond) // 让它多重看几次，次数超过换号上限
	p.Release("u1")
	rec := waitFor(t, done, "返回")
	if rec.Code != http.StatusOK {
		t.Fatalf("放出名额后应转发成功: code=%d body=%s", rec.Code, rec.Body)
	}
	if st, _ := p.Status("u1"); st.InFlight != 0 {
		t.Fatalf("请求结束后应释放名额，在途 %d", st.InFlight)
	}
}

// TestChatSlotWaitTimesOut 等满上限仍没有空位回 503，文案说明号都在忙。
func TestChatSlotWaitTimesOut(t *testing.T) {
	shortSlotPoll(t, 20*time.Millisecond)
	h, _, _ := newSlotWaitHandler(t, 150*time.Millisecond)
	start := time.Now()
	rec := waitFor(t, serveChatAsync(h, context.Background()), "返回")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if took := time.Since(start); took < 150*time.Millisecond {
		t.Fatalf("应等满 150ms 再回错，实际 %v", took)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "all accounts are busy") || !strings.Contains(body, "in_flight_full") {
		t.Fatalf("文案应说明号都在忙: %s", body)
	}
}

// TestChatRPMFullTimesOut 号的每分钟请求数到上限（在途是空的）：同在途满一样排队，等满回 503，
// 文案和 gateway_hint 说明是 RPM 满了。
func TestChatRPMFullTimesOut(t *testing.T) {
	shortSlotPoll(t, 20*time.Millisecond)
	h, p, waiting := newSlotWaitHandler(t, 150*time.Millisecond)
	p.Release("u1")
	p.SetMaxRPM(1)
	if !p.Acquire("u1") {
		t.Fatal("Acquire 应成功")
	}
	p.Release("u1")
	rec := waitFor(t, serveChatAsync(h, context.Background()), "返回")
	waitFor(t, waiting, "开始等空位")
	body := rec.Body.String()
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(body, "requests-per-minute") || !strings.Contains(body, "rpm_full") {
		t.Fatalf("应排队后回 503 并说明 RPM 满了: code=%d body=%s", rec.Code, body)
	}
}

// TestWaitSlotAlreadyExpired 醒来再选号仍没选到、而合计已经等满：不再等，记下等满了和是不是 RPM 的原因。
func TestWaitSlotAlreadyExpired(t *testing.T) {
	h, p, waiting := newSlotWaitHandler(t, 50*time.Millisecond)
	p.Release("u1")
	p.SetMaxRPM(1)
	if !p.Acquire("u1") {
		t.Fatal("Acquire 应成功")
	}
	sw := slotWait{since: time.Now().Add(-time.Second)}
	if h.waitSlot(context.Background(), p.SlotFreed(), &sw, map[string]bool{}, "glm-5.2", "") {
		t.Fatal("合计已经等满，不应再等")
	}
	if !sw.timedOut || !sw.rpm {
		t.Fatalf("应记下等满了、u1 的 RPM 也到上限了: %+v", sw)
	}
	select {
	case <-waiting:
		t.Fatal("不应开始等")
	default:
	}
}

// TestChatNoSlotWait 设成不等，或者号用不了不是因为在途满（这里是暂停选号）：不排队，直接回 503。
func TestChatNoSlotWait(t *testing.T) {
	cases := []struct {
		name string
		wait time.Duration
		set  func(*pool.Pool)
	}{
		{"设成不等", 0, func(*pool.Pool) {}},
		{"号暂停了", 5 * time.Second, func(p *pool.Pool) { p.Pause("u1") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, p, waiting := newSlotWaitHandler(t, tc.wait)
			tc.set(p)
			rec := waitFor(t, serveChatAsync(h, context.Background()), "返回")
			if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "all accounts are busy") {
				t.Fatalf("应照旧回 503: code=%d body=%s", rec.Code, rec.Body)
			}
			select {
			case <-waiting:
				t.Fatal("不应排队等空位")
			default:
			}
		})
	}
}

// TestChatSlotWaitLostRace 几个排队的请求同时被叫醒、抢同一个名额没抢到：不算这个号失败，也不算
// 换号，接着排队，号放出名额后照样用它。
func TestChatSlotWaitLostRace(t *testing.T) {
	h, p, waiting := newSlotWaitHandler(t, 5*time.Second)
	var mu sync.Mutex
	calls := 0
	old := acquireInFlight
	acquireInFlight = func(p *pool.Pool, uid string) bool {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			return false // 醒来后第一次抢名额被别的请求抢走了
		}
		return old(p, uid)
	}
	t.Cleanup(func() { acquireInFlight = old })
	done := serveChatAsync(h, context.Background())
	waitFor(t, waiting, "开始等空位")
	p.Release("u1")
	rec := waitFor(t, done, "返回")
	if rec.Code != http.StatusOK {
		t.Fatalf("没抢到名额应接着排队、再用这个号: code=%d body=%s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("应抢两次名额，实际 %d", calls)
	}
}

// TestChatSlotWaitClientGone 客户端断开就不再等。
func TestChatSlotWaitClientGone(t *testing.T) {
	h, _, waiting := newSlotWaitHandler(t, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := serveChatAsync(h, ctx)
	waitFor(t, waiting, "开始等空位")
	cancel()
	if rec := waitFor(t, done, "在客户端断开后返回"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}
