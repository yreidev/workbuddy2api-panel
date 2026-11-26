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

// TestRunBalanceRefresh401RecoveryAndRevokedSessionDead 验证余额刷新遇到 APISIX 401 时：
//  1. refreshToken 有效 → 按需刷新一次并更新池内积分；
//  2. refreshToken 已吊销（12153）→ 与保活一致按连续 3 次 12153 自动禁用。
func TestRunBalanceRefresh401RecoveryAndRevokedSessionDead(t *testing.T) {
	var refreshGood, refreshDead atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/plugin/auth/token/refresh":
			if r.Header.Get("X-Refresh-Token") == "rt-good" {
				refreshGood.Add(1)
				_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"at-fresh","refreshToken":"rt-good-2","expiresIn":3600}}`))
				return
			}
			refreshDead.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":12153,"msg":"12153:refresh token failed:idsvc token: invalid_grant: refresh token revoked (http 401)"}`))
		case "/v2/plugin/account":
			_, _ = w.Write([]byte(`{"code":0,"data":{"uid":"u-good"}}`))
		case "/billing/meter/get-user-resource-summary":
			if r.Header.Get("Authorization") == "Bearer at-fresh" {
				_, _ = w.Write([]byte(`{"code":0,"data":{"Packages":[{"PackageCode":"TCACA_code_007","CycleTotalCapacity":"200","CycleRemainCapacity":"150"}]}}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`<html><head><title>401 Authorization Required</title></head><body>openresty APISIX</body></html>`))
		case "/billing/meter/get-user-resource-paid-packages", "/billing/meter/get-user-resource-free-packages":
			if r.Header.Get("Authorization") == "Bearer at-fresh" {
				_, _ = w.Write([]byte(`{"code":0,"data":{"Accounts":[]}}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`<html><head><title>401 Authorization Required</title></head><body>openresty APISIX</body></html>`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u-good", AccessToken: "at-stale", RefreshToken: "rt-good", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	p.Add(&auth.Auth{UID: "u-dead", AccessToken: "at-dead", RefreshToken: "rt-dead", ExpiresAt: time.Now().Add(time.Hour).Unix()})

	up := &upstream.Client{
		HTTP:          srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
		WebBaseCN:     srv.URL,
	}
	s := New(Config{Pool: p, Upstream: up})

	// 第 1 轮：好号自愈拿到 150 积分，死号计第 1 次 12153（不禁用）。
	s.RunBalanceRefreshNow()
	stGood, _ := p.Status("u-good")
	if stGood.Credits != 150 || refreshGood.Load() != 1 {
		t.Fatalf("u-good should recover credits after 401 refresh: st=%+v refreshGood=%d", stGood, refreshGood.Load())
	}
	if stDead, _ := p.Status("u-dead"); stDead.Disabled {
		t.Fatalf("第 1 次 12153 不应禁用: %+v", stDead)
	}

	// 第 2 轮（命中 30s 负缓存，不再重复打 /token/refresh，仍记第 2 次 12153）。
	s.RunBalanceRefreshNow()
	if stDead, _ := p.Status("u-dead"); stDead.Disabled {
		t.Fatalf("第 2 次 12153 不应禁用: %+v", stDead)
	}

	// 第 3 轮：连续第 3 次 12153 → 自动禁用。
	s.RunBalanceRefreshNow()
	stDead, _ := p.Status("u-dead")
	if !stDead.Disabled || stDead.DisabledReason != "12153 session dead" {
		t.Fatalf("第 3 次 12153 应禁用: %+v", stDead)
	}
	if refreshDead.Load() != 1 {
		t.Fatalf("30s 负缓存窗口内 dead 号应只请求 1 次 /token/refresh，实际 %d", refreshDead.Load())
	}
}
