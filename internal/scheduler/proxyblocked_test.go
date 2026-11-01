// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package scheduler

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// TestTasksSkipProxyBlocked 被代理池拦着的号（请求出不去）：签到、活跃上报、保活、余额刷新、夜猫子、旅行、
// 连登兑换、全部测活都跳过，不发请求。
func TestTasksSkipProxyBlocked(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "no upstream call expected", 404)
	}))
	defer srv.Close()
	night := nowForNightWindow
	nowForNightWindow = func() time.Time { return time.Date(2026, 10, 7, 23, 30, 0, 0, time.Local) }
	t.Cleanup(func() { nowForNightWindow = night })

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.SetProxyBlocked("u1", "代理「甲」连不上（11-01 09:30 起）")
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})
	s.RunCheckinNow()
	s.RunActivityNow()
	s.RunKeepaliveNow()
	s.RunBalanceRefreshNow()
	s.RunBlackcatNow()
	s.RunTravelNow()
	s.RunStreakBonusNow()
	if n := calls.Load(); n != 0 {
		t.Fatalf("被拦的号不应发请求，实际 %d 次", n)
	}
	s.SetLiveness((&livenessRecorder{}).fn)
	if n, err := s.StartLivenessAll(); err != nil || n != 0 {
		t.Fatalf("全部测活不测被拦的号: %d, %v", n, err)
	}
}
