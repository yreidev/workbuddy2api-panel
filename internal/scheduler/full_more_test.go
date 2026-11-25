// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package scheduler

// full_more_test.go 补齐 Run 的 slotRearm/补跑派发/宽限取消、Reconfigure 各小时列表、
// 余额刷新失败与后台刷新退出、旅行门控，以及 makeupYesterday 的固定时钟成功日志。

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// lockedBuffer 并发安全的日志缓冲（后台 goroutine 与测试 goroutine 同时读写）。
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Reconfigure 的六个小时列表都能覆盖原值（空列表保留原值）。
func TestReconfigureAllHourLists(t *testing.T) {
	s := New(Config{})
	s.Reconfigure(
		[]int{1}, []int{2}, []int{3}, []int{4}, []int{5}, []int{6},
		true, true, true, true, true, true,
	)
	s.schedMu.Lock()
	got := []int{
		s.cfg.CheckinHours[0], s.cfg.TravelHours[0], s.cfg.ActivityHours[0],
		s.cfg.KeepaliveHours[0], s.cfg.BlackcatHours[0], s.cfg.GrowthHours[0],
	}
	disabled := []bool{
		s.cfg.CheckinDisabled, s.cfg.TravelDisabled, s.cfg.ActivityDisabled,
		s.cfg.KeepaliveDisabled, s.cfg.BlackcatDisabled, s.cfg.GrowthDisabled,
	}
	s.schedMu.Unlock()
	for i, h := range got {
		if h != i+1 {
			t.Fatalf("第 %d 个小时列表未更新: %v", i+1, got)
		}
	}
	for i, d := range disabled {
		if !d {
			t.Fatalf("第 %d 个开关未更新: %v", i+1, disabled)
		}
	}
}

// Run 等待时收到 Reconfigure 的重排通知：消费通知并继续等待，不退出。
func TestRunRearmOnReconfigure(t *testing.T) {
	s := New(Config{
		Pool:           pool.New(""),
		CheckinHours:   []int{9},
		TravelDisabled: true, ActivityDisabled: true,
		KeepaliveDisabled: true, BlackcatDisabled: true, GrowthDisabled: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	s.Reconfigure([]int{10}, nil, nil, nil, nil, nil, false, true, true, true, true, true)
	waitUntil(t, "rearm 通知被消费", func() bool { return len(s.rearmSchedule) == 0 })
	select {
	case <-done:
		t.Fatal("重排不应让 Run 退出")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Run 未退出")
	}
}

// 到点补跑：一批任务并行派发（含 growth 回调），派发后继续等待下一次唤醒。
func TestRunFiresBatch(t *testing.T) {
	oldNow := schedNow
	calls := 0
	schedNow = func() time.Time {
		calls++
		if calls == 1 {
			return time.Date(2026, 1, 1, 8, 0, 0, 0, time.Local) // 下一整点已过 → 立即补跑
		}
		return time.Now().Add(2 * time.Hour) // 之后的时点在未来，进入等待
	}
	t.Cleanup(func() { schedNow = oldNow })
	oldGrace := wakeupGraceDelay
	wakeupGraceDelay = 0
	t.Cleanup(func() { wakeupGraceDelay = oldGrace })
	zeroTaskDelays(t)

	hooked := make(chan struct{}, 1)
	s := New(Config{
		Pool:           pool.New(""),
		CheckinHours:   []int{9},
		TravelHours:    []int{9},
		ActivityHours:  []int{9},
		KeepaliveHours: []int{9},
		BlackcatHours:  []int{9},
		GrowthHours:    []int{9},
		GrowthHook: func() {
			select {
			case hooked <- struct{}{}:
			default:
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	select {
	case <-hooked:
	case <-time.After(5 * time.Second):
		t.Fatal("到点后 growth 回调未被派发")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Run 未退出")
	}
}

// 迟到补跑的网络宽限期间 ctx 取消：放弃本批并退出。
func TestRunGraceCancelled(t *testing.T) {
	oldNow := schedNow
	schedNow = func() time.Time { return time.Date(2026, 1, 1, 8, 0, 0, 0, time.Local) }
	t.Cleanup(func() { schedNow = oldNow })

	s := New(Config{
		Pool:           pool.New(""),
		CheckinHours:   []int{9},
		TravelDisabled: true, ActivityDisabled: true,
		KeepaliveDisabled: true, BlackcatDisabled: true, GrowthDisabled: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 进入宽限前就已取消
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("宽限期间取消后 Run 未退出")
	}
}

// RunBalanceRefreshNow：上游报错只记日志，不更新积分。
func TestBalanceRefreshUpstreamError(t *testing.T) {
	var logs lockedBuffer
	oldLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	c := newSchedCounter()
	c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":400,"msg":"balance boom"}`))
		return true
	}
	srv := c.server(t)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", Nickname: "一号", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	s := schedWithPool(srv, p, Config{})
	s.RunBalanceRefreshNow()
	if c.count(upstreamtest.FreePath) == 0 {
		t.Fatal("应发起余额查询")
	}
	if !strings.Contains(logs.String(), "balance") || !strings.Contains(logs.String(), "balance boom") {
		t.Fatalf("日志 = %q", logs.String())
	}
	if st, _ := p.Status("u1"); st.Credits != 0 {
		t.Fatalf("失败不应写积分: %+v", st)
	}
}

// StartBalanceRefresh：ctx 取消后后台 goroutine 退出。
func TestStartBalanceRefreshStopsOnCancel(t *testing.T) {
	var logs lockedBuffer
	oldLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	before := runtime.NumGoroutine()
	s := New(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	s.StartBalanceRefresh(ctx, time.Hour)
	waitUntil(t, "后台刷新循环启动", func() bool {
		return strings.Contains(logs.String(), "余额后台刷新")
	})
	cancel()

	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("后台刷新 goroutine 未退出: before=%d now=%d", before, runtime.NumGoroutine())
		}
		<-time.After(time.Millisecond)
	}
}

// 余额刷新被热改暂停后，收到重排通知会回到循环顶部（cur<=0 分支的 continue）。
func TestBalanceRefreshPausedRearm(t *testing.T) {
	var logs lockedBuffer
	oldLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	s := New(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartBalanceRefresh(ctx, time.Hour)
	waitUntil(t, "余额刷新循环启动", func() bool { return strings.Contains(logs.String(), "余额后台刷新") })

	s.SetBalanceInterval(0)
	waitUntil(t, "暂停分支", func() bool { return strings.Contains(logs.String(), "每 0s") })
	s.SetBalanceInterval(0) // 此刻循环停在暂停分支的 select，通知必然被它消费
	waitUntil(t, "暂停分支消费重排通知", func() bool { return len(s.rearmBalance) == 0 })
	cancel()
}

// RunTravelNow：没有 refreshToken 的号与 global 号都直接跳过，不发上游请求。
func TestRunTravelSkipsEmptyTokenAndGlobal(t *testing.T) {
	c := newSchedCounter()
	srv := c.server(t)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "no-rt", AccessToken: "at", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "glb", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"})
	s := schedWithPool(srv, p, Config{})
	s.RunTravelNow()
	if got := c.count(""); got != 0 {
		t.Fatalf("跳过账号不应发请求，实际 %d", got)
	}
}

// makeupYesterday：漏签+有卡+补签成功时打印日期（固定时钟 → 补签前一天）。
func TestMakeupYesterdaySuccessLog(t *testing.T) {
	fixed := time.Date(2026, 10, 19, 12, 0, 0, 0, time.Local)
	old := streakNow
	streakNow = func() time.Time { return fixed }
	t.Cleanup(func() { streakNow = old })

	realYesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	fixedYesterday := fixed.AddDate(0, 0, -1).Format("2006-01-02")
	cells := `{"cells":[{"date":"` + realYesterday + `","score":0},{"date":"` + fixedYesterday + `","score":0}]}`
	var used atomic.Value
	c := newSchedCounter()
	c.respond = func(w http.ResponseWriter, r *http.Request, path string) bool {
		switch {
		case strings.HasSuffix(path, "/activity/growth/heatmap"):
			_, _ = w.Write([]byte(streakReply(cells)))
			return true
		case strings.HasSuffix(path, "/activity/growth/streak"):
			_, _ = w.Write([]byte(streakReply(`{"makeup_cards":{"balance":1},"redemption_status":{}}`)))
			return true
		case strings.HasSuffix(path, "/makeup-cards/use"):
			used.Store(readReqBody(t, r))
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			return true
		}
		return false
	}
	srv := c.server(t)
	s := schedWithPool(srv, pool.New(""), Config{})

	var logs bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	s.makeupYesterday(&auth.Auth{UID: "u1", AccessToken: "at"})
	body, _ := used.Load().(string)
	if !strings.Contains(body, fixedYesterday) {
		t.Fatalf("补签日期应为固定时钟的前一天 %s，实际 %s", fixedYesterday, body)
	}
	if !strings.Contains(logs.String(), "已用补签卡补签 "+fixedYesterday) {
		t.Fatalf("成功日志缺少补签日期: %q", logs.String())
	}
}
