// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const apisix401HTML = `<html><head><title>401 Authorization Required</title></head><body><center><h1>401 Authorization Required</h1></center><hr><center>openresty</center><p><em>Powered by <a href="https://apisix.apache.org/">APISIX</a>.</em></p></body></html>`

// TestRefreshAfter401BillingModelsAndDesktop 验证计费（三路并发）、模型目录（v3 + 企业）与桌面端上报
// 遇到 APISIX 401 HTML 时单飞刷新一次、原子落盘并用新 accessToken 原位重发成功。
func TestRefreshAfter401BillingModelsAndDesktop(t *testing.T) {
	var refreshCalls atomic.Int32
	f := newUpFake(t)
	f.onFunc(http.MethodPost, "/v2/plugin/auth/token/refresh", func(r *upReq) (*http.Response, error) {
		refreshCalls.Add(1)
		if got := r.header.Get("X-Refresh-Token"); got != "rt-1" {
			t.Errorf("X-Refresh-Token = %q, want rt-1", got)
		}
		return jsonResp(200, `{"code":0,"data":{"accessToken":"new-at","refreshToken":"rt-2","expiresIn":3600}}`), nil
	})
	f.on(http.MethodGet, "/v2/plugin/account", 200, `{"code":0,"data":{"uid":"u-401","nickname":"n"}}`)

	requireNewBearer := func(okBody string) func(*upReq) (*http.Response, error) {
		return func(r *upReq) (*http.Response, error) {
			if r.header.Get("Authorization") != "Bearer new-at" {
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Header:     http.Header{"Content-Type": []string{"text/html"}},
					Body:       http.NoBody,
				}, nil
			}
			return jsonResp(200, okBody), nil
		}
	}
	requireNewBearerHTML := func(okBody string) func(*upReq) (*http.Response, error) {
		return func(r *upReq) (*http.Response, error) {
			if r.header.Get("Authorization") != "Bearer new-at" {
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Header:     http.Header{"Content-Type": []string{"text/html"}},
					Body:       newStringReadCloser(apisix401HTML),
				}, nil
			}
			return jsonResp(200, okBody), nil
		}
	}

	f.onFunc(http.MethodPost, "/billing/meter/get-user-resource-summary",
		requireNewBearerHTML(`{"code":0,"data":{"Packages":[{"PackageCode":"TCACA_code_007","CycleTotalCapacity":"100","CycleRemainCapacity":"80"}]}}`))
	f.onFunc(http.MethodPost, "/billing/meter/get-user-resource-paid-packages",
		requireNewBearerHTML(`{"code":0,"data":{"Accounts":[]}}`))
	f.onFunc(http.MethodPost, "/billing/meter/get-user-resource-free-packages",
		requireNewBearerHTML(`{"code":0,"data":{"Accounts":[]}}`))
	f.onFunc(http.MethodGet, "/v3/config",
		requireNewBearerHTML(v3ModelsJSON(`{"id":"m-v3","maxInputTokens":1000}`)))
	f.onFunc(http.MethodGet, "/console/enterprises/ent-1/config/models",
		requireNewBearer(`{"code":0,"data":[{"id":"m-ent","tags":["chat"],"maxInputTokens":2000}]}`))
	f.onFunc(http.MethodPost, "/v2/report",
		requireNewBearerHTML(`{"code":0,"data":{}}`))

	dir := t.TempDir()
	fp := filepath.Join(dir, "workbuddy-u-401.json")
	a := &auth.Auth{
		UID:          "u-401",
		AccessToken:  "old-at",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(24 * time.Hour).Unix(),
		FilePath:     fp,
	}
	if err := a.SaveAtomic(); err != nil {
		t.Fatal(err)
	}

	c := upClient(f)

	// 1) UserResource（个人号三路并发）：撞 401 后单飞只刷 1 次并成功恢复，且落盘新 token。
	rem, tot, err := c.UserResource(a)
	if err != nil {
		t.Fatalf("UserResource should recover after 401 refresh: %v", err)
	}
	if rem != 80 || tot != 100 {
		t.Fatalf("credits = %v/%v, want 80/100", rem, tot)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refreshCalls = %d, want 1", got)
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
		t.Fatalf("disk tokens not persisted: %+v", disk)
	}

	// 2) 重置为旧 accessToken（模拟另一路径撞 401）：测试企业模型目录（v3 + 企业双路并发）与桌面端上报恢复。
	a.EnterpriseID = "ent-1"
	a.AccessToken = "old-at"
	a.RefreshToken = "rt-1"
	infos, err := c.FetchModels(a)
	if err != nil || len(infos) < 2 {
		t.Fatalf("FetchModels after 401: infos=%v err=%v", infos, err)
	}
	a.AccessToken = "old-at"
	a.RefreshToken = "rt-1"
	if err := c.ReportDesktopEvent(a, DesktopEvent{"eventCode": "ping"}); err != nil {
		t.Fatalf("ReportDesktopEvent after 401: %v", err)
	}
}

// TestRefreshAfter401SessionDeadAndCooldown 验证刷新返回 12153（refresh token revoked）时：
// 计费调用直接透出 ErrSessionDead，且负缓存窗口内不再重复请求 /token/refresh；更换 RT 后立即放行。
func TestRefreshAfter401SessionDeadAndCooldown(t *testing.T) {
	origCooldown := refreshAfter401Cooldown
	refreshAfter401Cooldown = 10 * time.Second
	t.Cleanup(func() { refreshAfter401Cooldown = origCooldown })

	var refreshCalls atomic.Int32
	f := newUpFake(t)
	f.onFunc(http.MethodPost, "/v2/plugin/auth/token/refresh", func(r *upReq) (*http.Response, error) {
		refreshCalls.Add(1)
		if r.header.Get("X-Refresh-Token") == "rt-fresh" {
			return jsonResp(200, `{"code":0,"data":{"accessToken":"fresh-at","refreshToken":"rt-fresh-2","expiresIn":3600}}`), nil
		}
		return jsonResp(401, `{"code":12153,"msg":"12153:refresh token failed:idsvc token: invalid_grant: refresh token revoked (http 401)"}`), nil
	})
	f.on(http.MethodGet, "/v2/plugin/account", 200, `{"code":0,"data":{"uid":"u-revoked"}}`)
	for _, p := range []string{
		"/billing/meter/get-user-resource-summary",
		"/billing/meter/get-user-resource-paid-packages",
		"/billing/meter/get-user-resource-free-packages",
	} {
		f.onFunc(http.MethodPost, p, func(r *upReq) (*http.Response, error) {
			if r.header.Get("Authorization") == "Bearer fresh-at" {
				if strings.HasSuffix(r.path, "summary") {
					return jsonResp(200, `{"code":0,"data":{"Packages":[{"PackageCode":"TCACA_code_007","CycleTotalCapacity":"50","CycleRemainCapacity":"50"}]}}`), nil
				}
				return jsonResp(200, `{"code":0,"data":{"Accounts":[]}}`), nil
			}
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body:       newStringReadCloser(apisix401HTML),
			}, nil
		})
	}

	a := &auth.Auth{
		UID:          "u-revoked",
		AccessToken:  "dead-at",
		RefreshToken: "rt-revoked",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	}
	c := upClient(f)

	_, _, err := c.UserResource(a)
	var ue *Error
	if !errors.As(err, &ue) || ue.Kind != ErrSessionDead || !strings.Contains(ue.Msg, "refresh token revoked") {
		t.Fatalf("want ErrSessionDead with refresh token revoked, got %v", err)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("first UserResource refreshCalls = %d, want 1", got)
	}

	// 负缓存窗口内再次触发 401：不重复打 /token/refresh，仍返回 ErrSessionDead。
	_, _, err2 := c.UserResource(a)
	if !errors.As(err2, &ue) || ue.Kind != ErrSessionDead {
		t.Fatalf("second UserResource want ErrSessionDead, got %v", err2)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("cooldown should suppress extra refresh call, got %d", got)
	}

	// 重新登录换上新 RT 后，立即绕过负缓存恢复成功。
	a.RefreshToken = "rt-fresh"
	rem, _, err3 := c.UserResource(a)
	if err3 != nil || rem != 50 {
		t.Fatalf("re-login RT should bypass negative cache: rem=%v err=%v", rem, err3)
	}
	if got := refreshCalls.Load(); got != 2 {
		t.Fatalf("refreshCalls after new RT = %d, want 2", got)
	}
}

func newStringReadCloser(s string) *stringRC { return &stringRC{Reader: strings.NewReader(s)} }

type stringRC struct{ *strings.Reader }

func (*stringRC) Close() error { return nil }
