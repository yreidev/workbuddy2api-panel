package scheduler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// fastActivity 关闭活跃上报账号间限速，避免测试白等 800ms。
func fastActivity(t *testing.T) {
	t.Helper()
	old := activityAccountDelay
	activityAccountDelay = 0
	t.Cleanup(func() { activityAccountDelay = old })
}

// reportStub 记录 /v2/report 调用次数与 userId。
type reportStub struct {
	calls  atomic.Int32
	uids   atomic.Int32
	bodies atomic.Int32
}

func (s *reportStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/report" {
			s.calls.Add(1)
			uid := r.Header.Get("X-User-Id")
			if uid != "" {
				s.uids.Add(1)
			}
			s.bodies.Add(1) // 标记收到 body（断言数组含 userId 在 upstream 包单测覆盖）
			w.Write([]byte(`{"code":0,"msg":"OK"}`))
			return
		}
		http.Error(w, "not found", 404)
	})
}

// TestRunActivityNowReportsEachAccount 遍历池内每个可用账号上报一次。
func TestRunActivityNowReportsEachAccount(t *testing.T) {
	fastActivity(t)
	stub := &reportStub{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "u2", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})

	s.RunActivityNow()

	if n := stub.calls.Load(); n != 2 {
		t.Errorf("report calls=%d want 2（每号上报一次）", n)
	}
	if n := stub.uids.Load(); n != 2 {
		t.Errorf("report with X-User-Id=%d want 2（每号必带 userId）", n)
	}
}

// TestRunActivityNowSkipsDisabledAndNoToken 禁用账号与无 token 账号跳过。
func TestRunActivityNowSkipsDisabledAndNoToken(t *testing.T) {
	fastActivity(t)
	stub := &reportStub{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "ok", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "dis", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "notoken", AccessToken: "", RefreshToken: "", ExpiresAt: 9999999999})
	p.Disable("dis", "test")
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})

	s.RunActivityNow()

	if n := stub.calls.Load(); n != 1 {
		t.Errorf("report calls=%d want 1（仅 ok 账号）", n)
	}
}

// TestRunActivityNowErrorDoesNotAbort 单账号上报失败不影响后续遍历。
func TestRunActivityNowErrorDoesNotAbort(t *testing.T) {
	fastActivity(t)
	var okCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/report" {
			// streak 自检等非 report 请求直接回 200（不带计数，只统计 /v2/report）。
			w.Write([]byte(`{"code":0,"data":{}}`))
			return
		}
		uid := r.Header.Get("X-User-Id")
		if uid == "fail" {
			w.WriteHeader(500)
			w.Write([]byte(`boom`))
			return
		}
		okCalls.Add(1)
		w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "fail", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "ok", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})

	s.RunActivityNow() // 不应 panic

	if n := okCalls.Load(); n != 1 {
		t.Errorf("ok account report calls=%d want 1（失败账号不影响后续遍历）", n)
	}
}

// ---------------------------------------------------------------------------
// P1：活跃上报后回读 streak 自检
// ---------------------------------------------------------------------------

// activityStreakStub 模拟 /v2/report（200 成功）+ /activity/growth/streak（days 可配）。
type activityStreakStub struct {
	reportCalls atomic.Int32
	days        int  // streak 返回的连登天数
	streakErr   bool // 让 streak 返回 500
	noUserId    bool // 待测：上报不带 userId（服务端 200 但静默丢弃）
	streakHits  atomic.Int32
}

func (s *activityStreakStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/report":
			s.reportCalls.Add(1)
			w.Write([]byte(`{"code":0,"msg":"OK"}`))
		case "/activity/growth/streak":
			s.streakHits.Add(1)
			if s.streakErr {
				w.WriteHeader(500)
				w.Write([]byte(`boom`))
				return
			}
			fmt.Fprintf(w, `{"code":0,"data":{"streak":{"days":%d}}}`, s.days)
		default:
			http.Error(w, "not found", 404)
		}
	})
}

// activityStreakScheduler 构造带 streak 自检 stub 的调度器。
func activityStreakScheduler(t *testing.T, srv *httptest.Server) (*Scheduler, *pool.Pool) {
	t.Helper()
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	return New(Config{Pool: p, Upstream: up}), p
}

// TestRunActivityNowSelfCheckDaysNormal 上报成功后回读 streak：days>=1 → 无告警。
func TestRunActivityNowSelfCheckDaysNormal(t *testing.T) {
	fastActivity(t)
	stub := &activityStreakStub{days: 3}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})

	s.RunActivityNow()
	if stub.reportCalls.Load() != 1 || stub.streakHits.Load() != 1 {
		t.Errorf("report_calls=%d streak_hits=%d want 1/1", stub.reportCalls.Load(), stub.streakHits.Load())
	}
	// days>=1：checkActivityStreak 返回 false（无可疑）。
	if s.checkActivityStreak(p.AuthByUID("u1")) {
		t.Fatal("days>=1 不告警")
	}
}

// TestRunActivityNowSelfCheckSilentDrop 上报 200 但 streak.days=0 → 告警（silent drop?）。
func TestRunActivityNowSelfCheckSilentDrop(t *testing.T) {
	fastActivity(t)
	stub := &activityStreakStub{days: 0}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})

	if !s.checkActivityStreak(p.AuthByUID("u1")) {
		t.Fatal("days=0 应告警（上报 200 但 silent drop?）")
	}
}

// TestRunActivityNowSelfCheckGETFailure 回读 GET 失败 → 告警但不影响主流程（上报已成功）。
func TestRunActivityNowSelfCheckGETFailure(t *testing.T) {
	fastActivity(t)
	stub := &activityStreakStub{days: 1, streakErr: true}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})

	// 直接断言 checkActivityStreak：GET 失败 → 告警。
	if !s.checkActivityStreak(p.AuthByUID("u1")) {
		t.Fatal("streak GET 失败应告警（reported but unverifiable）")
	}
	// 回读是只读 oracle：GET 失败不影响已发生的上报本轮走通（遍历继续）。
	if stub.streakHits.Load() != 1 {
		t.Errorf("streak_hits=%d want 1（GET 失败也打了 streak 请求）", stub.streakHits.Load())
	}
}

// TestRunActivityNowSkipsSelfCheckOnReportFail 上报失败 → 不跑自检（SKIP，无意义回读）。
func TestRunActivityNowSkipsSelfCheckOnReportFail(t *testing.T) {
	fastActivity(t)
	var streakHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/report":
			w.WriteHeader(500)
			w.Write([]byte(`boom`))
		case "/activity/growth/streak":
			streakHits.Add(1)
			w.Write([]byte(`{"code":0,"data":{"streak":{"days":1}}}`))
		}
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})

	s.RunActivityNow() // 不上报成功 → 无自检
	if streakHits.Load() != 0 {
		t.Errorf("streak hits=%d want 0（上报失败不跑自检）", streakHits.Load())
	}
}

// TestRunCheckinDoesNotTriggerTravel 签到收尾不再跑旅行（旅行已剥离为独立排程）。
func TestRunCheckinDoesNotTriggerTravel(t *testing.T) {
	fastTravel(t)
	stub := &travelStub{buddy: "null"}
	srv := billingAndGrowthServer(stub)
	defer srv.Close()

	s, _ := newTravelScheduler(t, srv, "u1")
	s.RunCheckinNow()

	// 签到不再顺带跑旅行：buddy/info 不应被调用。
	if n := stub.infoCalls.Load(); n != 0 {
		t.Errorf("buddy/info calls=%d want 0（旅行已从签到剥离）", n)
	}
}

// TestNextWakeTravelIndependent 旅行有独立时点，与签到互不影响。
func TestNextWakeTravelIndependent(t *testing.T) {
	s := New(Config{
		CheckinHours:   []int{21},
		TravelHours:    []int{9},
		ActivityHours:  []int{10},
		KeepaliveHours: []int{22},
	})
	at, kinds := s.nextWake(time.Date(2026, 9, 11, 8, 0, 0, 0, time.Local))
	if want := time.Date(2026, 9, 11, 9, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("next=%v want %v（旅行 09:00 独立时点）", at, want)
	}
	if len(kinds) != 1 || kinds[0] != taskTravel {
		t.Errorf("kinds=%v want [travel]", kinds)
	}
}

// TestNextWakeActivityIndependent 活跃上报有独立时点。
func TestNextWakeActivityIndependent(t *testing.T) {
	s := New(Config{
		CheckinHours:   []int{21},
		TravelHours:    []int{9},
		ActivityHours:  []int{10},
		KeepaliveHours: []int{22},
	})
	at, kinds := s.nextWake(time.Date(2026, 9, 11, 9, 30, 0, 0, time.Local))
	if want := time.Date(2026, 9, 11, 10, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("next=%v want %v（活跃 10:00 独立时点）", at, want)
	}
	if len(kinds) != 1 || kinds[0] != taskActivity {
		t.Errorf("kinds=%v want [activity]", kinds)
	}
}

// TestNextWakeTravelDisabled 旅行禁用后排程里不再有旅行时点（签到照常）。
func TestNextWakeTravelDisabled(t *testing.T) {
	s := New(Config{
		CheckinHours:   []int{9, 21},
		TravelHours:    []int{9},
		TravelDisabled: true,
		KeepaliveHours: []int{22},
	})
	at, kinds := s.nextWake(time.Date(2026, 9, 11, 8, 0, 0, 0, time.Local))
	// 旅行禁用 → 09:00 旅行时点不应出现，最近的是 09:00 签到（同小时但签到未禁用）。
	if want := time.Date(2026, 9, 11, 9, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("next=%v want %v", at, want)
	}
	if !hasKind(kinds, taskCheckin) {
		t.Errorf("kinds=%v want 含 checkin", kinds)
	}
	if hasKind(kinds, taskTravel) {
		t.Errorf("kinds=%v 不应含 travel（已禁用）", kinds)
	}
}

// TestNextWakeActivityDisabled 活跃上报禁用后排程里不再有活跃时点。
func TestNextWakeActivityDisabled(t *testing.T) {
	s := New(Config{
		CheckinHours:     []int{9, 21},
		ActivityHours:    []int{10},
		ActivityDisabled: true,
		KeepaliveHours:   []int{22},
	})
	at, kinds := s.nextWake(time.Date(2026, 9, 11, 9, 30, 0, 0, time.Local))
	if want := time.Date(2026, 9, 11, 21, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("next=%v want %v（活跃禁用 → 跳过 10:00）", at, want)
	}
	if hasKind(kinds, taskActivity) {
		t.Errorf("kinds=%v 不应含 activity（已禁用）", kinds)
	}
}

// TestCheckinDisabledTravelStillRuns 签到禁用时旅行/活跃照跑（验收标准 2）。
func TestCheckinDisabledTravelStillRuns(t *testing.T) {
	s := New(Config{
		CheckinHours:    []int{9, 21},
		CheckinDisabled: true,
		TravelHours:     []int{9},
		ActivityHours:   []int{10},
		KeepaliveHours:  []int{22},
	})
	at, kinds := s.nextWake(time.Date(2026, 9, 11, 8, 0, 0, 0, time.Local))
	if want := time.Date(2026, 9, 11, 9, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("next=%v want %v（签到禁用，旅行 09:00 照跑）", at, want)
	}
	if hasKind(kinds, taskCheckin) {
		t.Errorf("kinds=%v 不应含 checkin（已禁用）", kinds)
	}
	if !hasKind(kinds, taskTravel) {
		t.Errorf("kinds=%v 应含 travel（签到禁用但旅行独立）", kinds)
	}
}

// TestAllFourDisabledNoSpin 四类任务全禁用：Run 不空转。
func TestAllFourDisabledNoSpin(t *testing.T) {
	s := New(Config{
		CheckinDisabled:   true,
		TravelDisabled:    true,
		ActivityDisabled:  true,
		KeepaliveDisabled: true,
		BlackcatDisabled:  true,
		CheckinHours:      []int{9, 21},
		TravelHours:       []int{9},
		ActivityHours:     []int{10},
		KeepaliveHours:    []int{22},
	})
	at, kinds := s.nextWake(time.Now())
	if !at.IsZero() || len(kinds) != 0 {
		t.Errorf("at=%v kinds=%v want zero/nil（五类全禁用）", at, kinds)
	}
}

// TestNextWakeSameHourTravelAndCheckin 旅行与签到配到同一小时时两类任务都要执行。
func TestNextWakeSameHourTravelAndCheckin(t *testing.T) {
	s := New(Config{
		CheckinHours:   []int{9, 21},
		TravelHours:    []int{9},
		ActivityHours:  []int{10},
		KeepaliveHours: []int{22},
	})
	at, kinds := s.nextWake(time.Date(2026, 9, 11, 8, 0, 0, 0, time.Local))
	if want := time.Date(2026, 9, 11, 9, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("next=%v want %v", at, want)
	}
	if !hasKind(kinds, taskCheckin) || !hasKind(kinds, taskTravel) {
		t.Errorf("kinds=%v want 含 checkin+travel（同 09:00 两任务）", kinds)
	}
}

// TestRunDispatchesActivityAndTravel Run 到点分发 activity 与 travel（不真打上游，用空池）。
func TestRunDispatchesActivityAndTravel(t *testing.T) {
	fastActivity(t)
	fastTravel(t)
	// 空 pool → RunActivityNow/RunTravelNow 遍历 0 账号即返回，不阻塞。
	p := pool.New("")
	up := &upstream.Client{}
	s := New(Config{
		Pool:           p,
		Upstream:       up,
		CheckinHours:   []int{},
		TravelHours:    []int{},
		ActivityHours:  []int{},
		KeepaliveHours: []int{},
	})
	// 四类全空 hours → nextWake 回落默认 → 会构造 timer，ctx 取消即返回。
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在 ctx 取消后返回")
	}
}

// ---------------------------------------------------------------------------
// 国际版活跃上报：global 账号不再被跳过（上游 a190252 同口径）
// ---------------------------------------------------------------------------

// withGlobalSwitch 临时打开 global realm 开关并复位（生产缺省即开，此辅助确保测试隔离）。
func withGlobalSwitch(t *testing.T) {
	t.Helper()
	old := auth.GlobalEnabled()
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(old) })
}

// globalAccount 构造一个 realm=global 的账号（BackfillRealmFor 落显式标识）。
func globalAccount(t *testing.T, uid string) *auth.Auth {
	t.Helper()
	a := &auth.Auth{UID: uid, AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999}
	if _, err := auth.BackfillRealmFor(a, "global"); err != nil {
		t.Fatalf("BackfillRealmFor(global): %v", err)
	}
	if !a.IsGlobal() {
		t.Fatalf("%s IsGlobal()=false want true", uid)
	}
	return a
}

// TestRunActivityNowReportsGlobalAccount 纯 global 池：账号必须上报（曾因
// 「D4 门控：global 无活跃体系」被跳过 → 国际版永远点不亮连登、拿不到每日积分）。
func TestRunActivityNowReportsGlobalAccount(t *testing.T) {
	withGlobalSwitch(t)
	fastActivity(t)
	var cnHits, globalHits atomic.Int32
	cnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/report" {
			cnHits.Add(1)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer cnSrv.Close()
	globalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/report" {
			globalHits.Add(1)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer globalSrv.Close()

	p := pool.New("")
	p.Add(globalAccount(t, "g1"))
	// global base 单独指向另一台 server：断言路由按 realm 切，不碰 CN 端点。
	up := &upstream.Client{
		HTTP:              cnSrv.Client(),
		GlobalEnabled:     true,
		ChatBaseCN:        cnSrv.URL,
		BillingBaseCN:     cnSrv.URL,
		ChatBaseGlobal:    globalSrv.URL,
		BillingBaseGlobal: globalSrv.URL,
	}
	s := New(Config{Pool: p, Upstream: up})

	s.RunActivityNow()

	if n := globalHits.Load(); n == 0 {
		t.Error("global 账号未上报：/v2/report 未打到 global base（国际版将永远点不亮连登）")
	}
	if n := cnHits.Load(); n != 0 {
		t.Errorf("CN base hits=%d want 0（global 账号不应打到 CN 端点）", n)
	}
}

// TestRunActivityNowMixedPoolReportsBoth 混池：CN 与 global 各上报一次，
// 且分别路由到各自 realm 的 base。
func TestRunActivityNowMixedPoolReportsBoth(t *testing.T) {
	withGlobalSwitch(t)
	fastActivity(t)
	var cnHits, globalHits atomic.Int32
	cnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/report" {
			cnHits.Add(1)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer cnSrv.Close()
	globalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/report" {
			globalHits.Add(1)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer globalSrv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "cn1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(globalAccount(t, "g1"))
	up := &upstream.Client{
		HTTP:              cnSrv.Client(),
		GlobalEnabled:     true,
		ChatBaseCN:        cnSrv.URL,
		BillingBaseCN:     cnSrv.URL,
		ChatBaseGlobal:    globalSrv.URL,
		BillingBaseGlobal: globalSrv.URL,
	}
	s := New(Config{Pool: p, Upstream: up})

	s.RunActivityNow()

	if n := cnHits.Load(); n != 1 {
		t.Errorf("CN report hits=%d want 1", n)
	}
	if n := globalHits.Load(); n != 1 {
		t.Errorf("global report hits=%d want 1", n)
	}
}

// TestRunActivityNowGlobalErrorDoesNotSkipCN global report 失败（404）时
// CN 账号照常上报：单账号失败只 break 该号，不影响遍历。
func TestRunActivityNowGlobalErrorDoesNotSkipCN(t *testing.T) {
	withGlobalSwitch(t)
	fastActivity(t)
	var cnHits atomic.Int32
	cnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/report" {
			cnHits.Add(1)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer cnSrv.Close()
	// global 侧一律 404：模拟国际版端点不可用。
	globalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", 404)
	}))
	defer globalSrv.Close()

	p := pool.New("")
	p.Add(globalAccount(t, "g1"))
	p.Add(&auth.Auth{UID: "cn1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{
		HTTP:              cnSrv.Client(),
		GlobalEnabled:     true,
		ChatBaseCN:        cnSrv.URL,
		BillingBaseCN:     cnSrv.URL,
		ChatBaseGlobal:    globalSrv.URL,
		BillingBaseGlobal: globalSrv.URL,
	}
	s := New(Config{Pool: p, Upstream: up})

	s.RunActivityNow() // 不应 panic

	if n := cnHits.Load(); n != 1 {
		t.Errorf("CN report hits=%d want 1（global 失败不应影响 CN 账号）", n)
	}
}

// TestCheckinStillSkipsGlobal 回归护栏：本次只放开活跃上报，签到仍跳过 global
// （上游 a190252 明确「checkin/travel 门控不动」）。误删该 gate 会让国际版去打
// CN 专属签到端点。
func TestCheckinStillSkipsGlobal(t *testing.T) {
	withGlobalSwitch(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(globalAccount(t, "g1"))
	up := &upstream.Client{
		HTTP:              srv.Client(),
		GlobalEnabled:     true,
		ChatBaseCN:        srv.URL,
		BillingBaseCN:     srv.URL,
		ChatBaseGlobal:    srv.URL,
		BillingBaseGlobal: srv.URL,
	}
	s := New(Config{Pool: p, Upstream: up})

	s.RunCheckinNow()

	if n := hits.Load(); n != 0 {
		t.Errorf("checkin global 上游调用=%d want 0（签到仍应跳过 global）", n)
	}
}
