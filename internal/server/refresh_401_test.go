// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

const serverAPISIX401HTML = `<html><head><title>401 Authorization Required</title></head><body><center>openresty</center><p>Powered by APISIX.</p></body></html>`

// TestChat401OnDemandRefreshAndRetry 验证对话请求撞上非 12153 的 HTTP 401 时，
// 按需单飞刷新令牌、原子落盘并在同一账号上原位重发成功。
func TestChat401OnDemandRefreshAndRetry(t *testing.T) {
	old := rotateBackoffBase
	rotateBackoffBase = 0
	t.Cleanup(func() { rotateBackoffBase = old })

	var chatCalls, refreshCalls atomic.Int32
	up := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/v2/chat/completions":
				chatCalls.Add(1)
				if r.Header.Get("Authorization") != "Bearer new-at" {
					return textResp(http.StatusUnauthorized, "text/html", serverAPISIX401HTML), nil
				}
				return textResp(http.StatusOK, "text/event-stream", sseOK), nil
			case "/v2/plugin/auth/token/refresh":
				refreshCalls.Add(1)
				return textResp(http.StatusOK, "application/json",
					`{"code":0,"data":{"accessToken":"new-at","refreshToken":"rt-2","expiresIn":3600}}`), nil
			case "/v2/plugin/account":
				return textResp(http.StatusOK, "application/json",
					`{"code":0,"data":{"uid":"u-chat-401","nickname":"甲"}}`), nil
			default:
				return textResp(http.StatusNotFound, "application/json", `{"code":404}`), nil
			}
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}

	fp := filepath.Join(t.TempDir(), "workbuddy-u-chat-401.json")
	acct := &auth.Auth{
		UID:          "u-chat-401",
		Nickname:     "甲",
		AccessToken:  "stale-at",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(24 * time.Hour).Unix(),
		FilePath:     fp,
	}
	if err := acct.SaveAtomic(); err != nil {
		t.Fatal(err)
	}

	p := testPoolWith(acct)
	h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", rec.Code, rec.Body.String())
	}
	if chatCalls.Load() != 2 || refreshCalls.Load() != 1 {
		t.Fatalf("chatCalls=%d refreshCalls=%d, want 2 and 1", chatCalls.Load(), refreshCalls.Load())
	}
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := auth.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if disk.AccessToken != "new-at" || disk.RefreshToken != "rt-2" {
		t.Fatalf("disk token not updated: %+v", disk)
	}
}

// TestChat401RefreshRevokedDisablesAndRotates 验证对话撞 401 后按需刷新返回 12153（refresh token revoked）时，
// 立即禁用该死号并轮换到下一个健康账号（若全部死号则末端透出 12153 原文而非 APISIX HTML）。
func TestChat401RefreshRevokedDisablesAndRotates(t *testing.T) {
	old := rotateBackoffBase
	rotateBackoffBase = 0
	t.Cleanup(func() { rotateBackoffBase = old })

	up := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/v2/chat/completions":
				if r.Header.Get("Authorization") == "Bearer good-at" {
					return textResp(http.StatusOK, "text/event-stream", sseOK), nil
				}
				return textResp(http.StatusUnauthorized, "text/html", serverAPISIX401HTML), nil
			case "/v2/plugin/auth/token/refresh":
				return textResp(http.StatusUnauthorized, "application/json",
					`{"code":12153,"msg":"12153:refresh token failed:idsvc token: invalid_grant: refresh token revoked (http 401)"}`), nil
			default:
				return textResp(http.StatusNotFound, "application/json", `{"code":404}`), nil
			}
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}

	dead := &auth.Auth{UID: "u-dead", Nickname: "死号", AccessToken: "dead-at", RefreshToken: "rt-revoked", ExpiresAt: 9999999999}
	good := &auth.Auth{UID: "u-good", Nickname: "好号", AccessToken: "good-at", RefreshToken: "rt-good", ExpiresAt: 9999999999}
	p := testPoolWith(dead, good)
	p.SetCredits("u-dead", 5000, 5000) // 先选死号

	h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("should rotate to healthy account, got %d: %s", rec.Code, rec.Body.String())
	}
	st, _ := p.Status("u-dead")
	if !st.Disabled || st.DisabledReason != "refresh session dead" {
		t.Fatalf("dead account should be disabled on 12153: %+v", st)
	}
}

// TestSearch401OnDemandRefreshAndRetry 验证联网搜索遇 401 按需刷新后原位重发成功。
func TestSearch401OnDemandRefreshAndRetry(t *testing.T) {
	rig := &searchRig{
		reply: map[string]func() (*http.Response, error){
			"at-stale": func() (*http.Response, error) {
				return textResp(http.StatusUnauthorized, "text/html", serverAPISIX401HTML), nil
			},
		},
		other: func(path string) *http.Response {
			switch path {
			case "/v2/plugin/auth/token/refresh":
				return textResp(http.StatusOK, "application/json",
					`{"code":0,"data":{"accessToken":"at-refreshed","refreshToken":"rt-2","expiresIn":3600}}`)
			case "/v2/plugin/account":
				return textResp(http.StatusOK, "application/json",
					`{"code":0,"data":{"uid":"s-1","nickname":"甲"}}`)
			}
			return nil
		},
	}
	old := rotateBackoffBase
	rotateBackoffBase = 0
	t.Cleanup(func() { rotateBackoffBase = old })

	p := testPoolWith(&auth.Auth{
		UID:          "s-1",
		Nickname:     "甲",
		AccessToken:  "at-stale",
		RefreshToken: "rt-1",
		ExpiresAt:    9999999999,
	})
	h := NewHandler(Config{Pool: p, Upstream: rig.client(), SoftCooldown: time.Minute})

	req := httptest.NewRequest(http.MethodPost, "/v1/search/messages", strings.NewReader(searchMessagesBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := rig.searches()
	if len(got) != 2 || got[0] != "at-stale" || got[1] != "at-refreshed" {
		t.Fatalf("search tokens = %v, want [at-stale at-refreshed]", got)
	}
}
