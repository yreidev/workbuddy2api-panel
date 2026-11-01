// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package scheduler

import (
	"context"
	"errors"
	"log"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
)

// livenessRecorder 假的测活：记下每次调用，按 UID 决定通、不通还是报错；gate 不为 nil 时等它关上才返回。
type livenessRecorder struct {
	mu    sync.Mutex
	calls []livenessCall
	bad   map[string]bool // 不通
	fails map[string]bool // 报错（号不存在、挑不出模型）
	gate  chan struct{}
}

type livenessCall struct {
	uid, model string
	scheduled  bool
}

func (r *livenessRecorder) fn(_ context.Context, uid, model string, scheduled bool) (pool.LivenessResult, string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, livenessCall{uid, model, scheduled})
	gate := r.gate
	r.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if r.fails[uid] {
		return pool.LivenessResult{}, "", errors.New("account " + uid + " not found")
	}
	return pool.LivenessResult{OK: !r.bad[uid]}, "hi", nil
}

func (r *livenessRecorder) snapshot() []livenessCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]livenessCall(nil), r.calls...)
	sort.Slice(out, func(i, j int) bool { return out[i].uid < out[j].uid })
	return out
}

func captureSchedLog(t *testing.T) *lockedBuffer {
	t.Helper()
	logs := &lockedBuffer{}
	old := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(old) })
	return logs
}

// TestLivenessUnavailable 没接上测活：单测、全部测活报 ErrLivenessUnavailable，定时测活什么都不做。
func TestLivenessUnavailable(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Cooldown("u1", pool.CoolSoft, time.Hour, "429")
	s := New(Config{Pool: p})
	if _, _, err := s.TestAccount(context.Background(), "u1", ""); !errors.Is(err, ErrLivenessUnavailable) {
		t.Fatalf("TestAccount err = %v", err)
	}
	if _, err := s.StartLivenessAll(); !errors.Is(err, ErrLivenessUnavailable) {
		t.Fatalf("StartLivenessAll err = %v", err)
	}
	s.RunLivenessNow()
	if s.livenessRunning.Load() {
		t.Fatal("没接上测活不应占着批次")
	}
}

// TestLivenessSingle 单测直接转给测活，scheduled=false，不受批次互斥。
func TestLivenessSingle(t *testing.T) {
	rec := &livenessRecorder{}
	s := New(Config{Pool: pool.New("")})
	s.SetLiveness(rec.fn)
	s.livenessRunning.Store(true) // 有一批在跑也能单测
	res, reply, err := s.TestAccount(context.Background(), "u1", "m")
	if err != nil || !res.OK || reply != "hi" {
		t.Fatalf("res=%+v reply=%q err=%v", res, reply, err)
	}
	if got := rec.snapshot(); len(got) != 1 || got[0] != (livenessCall{"u1", "m", false}) {
		t.Fatalf("calls = %+v", got)
	}
}

// TestStartLivenessAll 全部测活：后台测所有没被人工禁用的号（自动禁用、暂停选号的也测），模型自动挑；
// 跑的时候再点回 ErrLivenessBusy，定时测活也跳过这一轮；跑完记一行汇总。
func TestStartLivenessAll(t *testing.T) {
	logs := captureSchedLog(t)
	p := pool.New("")
	for _, uid := range []string{"a-ok", "b-auto", "c-manual", "d-paused", "e-missing"} {
		p.Add(&auth.Auth{UID: uid})
	}
	p.Disable("b-auto", "session dead x3")
	p.Disable("c-manual", pool.ManualDisableReason)
	p.Pause("d-paused")
	p.Cooldown("a-ok", pool.CoolSoft, time.Hour, "429") // 定时测活本来会挑它
	gate := make(chan struct{})
	rec := &livenessRecorder{bad: map[string]bool{"b-auto": true}, fails: map[string]bool{"e-missing": true}, gate: gate}
	s := New(Config{Pool: p})
	s.SetLiveness(rec.fn)

	n, err := s.StartLivenessAll()
	if err != nil || n != 4 {
		t.Fatalf("StartLivenessAll = %d, %v；应测 4 个号", n, err)
	}
	if _, err := s.StartLivenessAll(); !errors.Is(err, ErrLivenessBusy) {
		t.Fatalf("跑的时候再点应回 ErrLivenessBusy，got %v", err)
	}
	waitUntil(t, "并发测两个号", func() bool { return len(rec.snapshot()) == livenessAllConcurrency })
	s.RunLivenessNow() // 有一批在跑：定时测活跳过
	close(gate)
	waitUntil(t, "全部测活结束", func() bool { return strings.Contains(logs.String(), "全部测活结束") })
	waitUntil(t, "批次放开", func() bool { return !s.livenessRunning.Load() })

	want := []livenessCall{{"a-ok", "", false}, {"b-auto", "", false}, {"d-paused", "", false}, {"e-missing", "", false}}
	if got := rec.snapshot(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("calls = %+v，应为 %+v（不测人工禁用的，定时测活不插队）", got, want)
	}
	out := logs.String()
	for _, s := range []string{"liveness 全部测活开始：4 个号", "liveness 全部测活结束：2 个通，2 个不通", "liveness WARN account e-missing not found"} {
		if !strings.Contains(out, s) {
			t.Errorf("日志缺 %q:\n%s", s, out)
		}
	}
}

// TestStartLivenessFor 批量测活：只测选中的号，人工禁用的也测，代理池拦着的、不在池里的跳过；
// 和全部测活共用「同时只跑一批」。
func TestStartLivenessFor(t *testing.T) {
	logs := captureSchedLog(t)
	p := pool.New("")
	for _, uid := range []string{"a", "b-manual", "c-blocked", "d-unpicked"} {
		p.Add(&auth.Auth{UID: uid})
	}
	p.Disable("b-manual", pool.ManualDisableReason)
	p.SetProxyBlocked("c-blocked", "等代理")
	gate := make(chan struct{})
	rec := &livenessRecorder{gate: gate}
	s := New(Config{Pool: p})
	s.SetLiveness(rec.fn)

	n, err := s.StartLivenessFor([]string{"a", "b-manual", "c-blocked", "gone"})
	if err != nil || n != 2 {
		t.Fatalf("StartLivenessFor = %d, %v；应测 2 个号", n, err)
	}
	if _, err := s.StartLivenessAll(); !errors.Is(err, ErrLivenessBusy) {
		t.Fatalf("批量测活在跑时全部测活应回 ErrLivenessBusy，got %v", err)
	}
	close(gate)
	waitUntil(t, "批量测活结束", func() bool {
		return strings.Contains(logs.String(), "liveness 批量测活结束：2 个通，0 个不通")
	})
	waitUntil(t, "批次放开", func() bool { return !s.livenessRunning.Load() })
	want := map[livenessCall]bool{{"a", "", false}: true, {"b-manual", "", false}: true}
	if got := rec.snapshot(); len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("calls = %+v，应只测 a 和 b-manual", got)
	}
}

// TestRunLivenessNow 定时测活只测 LivenessDue 挑出的号，scheduled=true；没有要测的号不记日志。
func TestRunLivenessNow(t *testing.T) {
	logs := captureSchedLog(t)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u-ok"})
	p.Add(&auth.Auth{UID: "u-soft"})
	rec := &livenessRecorder{}
	s := New(Config{Pool: p})
	s.SetLiveness(rec.fn)

	s.RunLivenessNow()
	if len(rec.snapshot()) != 0 || strings.Contains(logs.String(), "定时测活开始") {
		t.Fatal("没有要测的号不应测，也不记日志")
	}
	p.Cooldown("u-soft", pool.CoolSoft, time.Hour, "429")
	s.RunLivenessNow()
	if got := rec.snapshot(); len(got) != 1 || got[0] != (livenessCall{"u-soft", "", true}) {
		t.Fatalf("calls = %+v", got)
	}
	if !strings.Contains(logs.String(), "liveness 定时测活结束：1 个通，0 个不通") || s.livenessRunning.Load() {
		t.Fatalf("日志或批次状态不对:\n%s", logs)
	}
}

// TestStartLiveness 定时测活循环：0 = 先不跑，热改间隔立即按新值重排，改回 0 记一行关闭，ctx 取消即退出。
func TestStartLiveness(t *testing.T) {
	logs := captureSchedLog(t)
	before := runtime.NumGoroutine()
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Cooldown("u1", pool.CoolSoft, time.Hour, "429")
	rec := &livenessRecorder{}
	s := New(Config{Pool: p})
	s.SetLiveness(rec.fn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.StartLiveness(ctx, -time.Minute) // 负数按 0：先不跑
	s.SetLivenessInterval(10 * time.Millisecond)
	waitUntil(t, "定时测活跑了一轮", func() bool { return len(rec.snapshot()) >= 1 })
	if got := rec.snapshot()[0]; got != (livenessCall{"u1", "", true}) {
		t.Fatalf("定时测活 call = %+v", got)
	}
	s.SetLivenessInterval(time.Hour) // 计时中改间隔：停掉旧计时，按新值重算
	waitUntil(t, "改成每小时", func() bool { return strings.Contains(logs.String(), "定时测活每 1h0m0s") })
	s.SetLivenessInterval(0)
	waitUntil(t, "关闭", func() bool { return strings.Contains(logs.String(), "scheduler: 定时测活已关闭") })
	s.SetLivenessInterval(time.Hour)
	waitUntil(t, "再打开", func() bool { return strings.Count(logs.String(), "定时测活每 1h0m0s") == 2 })
	if strings.Count(logs.String(), "定时测活已关闭") != 1 || !strings.Contains(logs.String(), "定时测活每 10ms") {
		t.Fatalf("日志不对:\n%s", logs)
	}
	cancel()
	waitUntil(t, "循环退出", func() bool { return runtime.NumGoroutine() <= before })

	// 关着的时候取消也能退出
	ctx2, cancel2 := context.WithCancel(context.Background())
	s.StartLiveness(ctx2, 0)
	cancel2()
	waitUntil(t, "关着时循环退出", func() bool { return runtime.NumGoroutine() <= before })
}
