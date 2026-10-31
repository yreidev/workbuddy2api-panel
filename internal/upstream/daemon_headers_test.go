// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// hex32 官方 X-Request-ID 的形状：去掉横线的 UUID。
var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// wantRequestID 请求框架给每个请求补的 X-Request-ID：32 位十六进制，每次新生成。
func wantRequestID(t *testing.T, h http.Header, prev string) string {
	t.Helper()
	id := h.Get("X-Request-ID")
	if !hex32.MatchString(id) || id == prev {
		t.Errorf("X-Request-ID = %q，应为每次新生成的 32 位十六进制（上一次 %q）", id, prev)
	}
	return id
}

// wantHeaders 逐项核对请求头：值为 "" 表示这个头不应出现。
func wantHeaders(t *testing.T, h http.Header, want map[string]string) {
	t.Helper()
	for k, v := range want {
		got, present := h.Get(k), len(h.Values(k)) > 0
		if got != v || present != (v != "") {
			t.Errorf("%s = %q（发了=%v），want %q", k, got, present, v)
		}
	}
}

// TestBillingHeadersOfficialShape 积分、签到、成长任务等请求是桌面端后台服务的形状：官方请求框架的头
// （含 X-Product、X-Request-ID）+ 后台服务的 UA（应用名段在前）；官方不发的头一个都不带，
// Accept-Language 与 X-Device-Token 由个别请求另加。
func TestBillingHeadersOfficialShape(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	cnEnt := cnAuth()
	cnEnt.EnterpriseID = "corp-cn"
	cases := []struct {
		name, method string
		a            *auth.Auth
		ua, domain   string
		ent, ctype   string
	}{
		{"国内版个人号 POST", http.MethodPost, cnAuth(), "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0", "www.codebuddy.cn", "", "application/json"},
		{"国内版企业号 POST", http.MethodPost, cnEnt, "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0", "www.codebuddy.cn", "corp-cn", "application/json"},
		{"国际版个人号 GET", http.MethodGet, globalAuth(), "workbuddy-ai/5.8.4 WorkBuddy/5.8.4 CLI/2.161.0", "www.workbuddy.ai", "", ""},
	}
	prevID := ""
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.a
			a.DeviceToken = "dev-tok"
			req, _ := http.NewRequest(tc.method, "https://x/v2/billing/meter/get-user-resource", nil)
			(&Client{}).BillingHeaders(req, a)
			wantHeaders(t, req.Header, map[string]string{
				"Accept":           "application/json, text/plain, */*",
				"Content-Type":     tc.ctype,
				"X-Requested-With": "XMLHttpRequest",
				"User-Agent":       tc.ua,
				"Authorization":    "Bearer tok",
				"X-User-Id":        a.UID,
				"X-Domain":         tc.domain,
				"X-Enterprise-Id":  tc.ent,
				"X-Tenant-Id":      tc.ent,
				"X-Product":        "SaaS",
				// 官方后台服务的请求里没有这些。
				"Origin": "", "Referer": "", "Accept-Language": "", "X-CodeBuddy-Request": "",
				"X-Machine-ID": "", "X-Session-ID": "", "X-Device-Token": "",
				"X-Refresh-Token": "", "X-Client-Platform": "",
			})
			prevID = wantRequestID(t, req.Header, prevID)
		})
	}
}

// TestFrameworkHeadersKeepsRequestID 调用方已经设了 X-Request-ID（比如任务中心的桌面端上报）时不覆盖。
func TestFrameworkHeadersKeepsRequestID(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://x/", nil)
	req.Header.Set("X-Request-ID", "keep-me")
	(&Client{}).frameworkHeaders(req, cnAuth(), "")
	if got := req.Header.Get("X-Request-ID"); got != "keep-me" {
		t.Errorf("X-Request-ID = %q, want keep-me", got)
	}
}

// TestBillingExtraHeaders 积分查询另带 Accept-Language（zh / en），签到另带 X-Device-Token。
func TestBillingExtraHeaders(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	for _, tc := range []struct {
		a    *auth.Auth
		lang string
	}{{cnAuth(), "zh"}, {globalAuth(), "en"}, {nil, "zh"}} {
		req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
		withBillingLanguage(tc.a)(req)
		if got := req.Header.Get("Accept-Language"); got != tc.lang {
			t.Errorf("Accept-Language = %q, want %q", got, tc.lang)
		}
	}
	a := cnAuth()
	a.DeviceToken = "dev-tok"
	req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{}).withDeviceToken(a)(req)
	if got := req.Header.Get("X-Device-Token"); got != "dev-tok" {
		t.Errorf("签到 X-Device-Token = %q, want dev-tok", got)
	}
}

// TestRefreshHeadersOfficialShape 刷新令牌是内置 CLI 的 refreshSession：请求框架的头 + 登录域（没有就用
// 国内版产品入口域）+ X-Refresh-Token + X-Auth-Refresh-Source: plugin，UA 用 CLI 的拼法。
func TestRefreshHeadersOfficialShape(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	for _, tc := range []struct {
		name       string
		a          *auth.Auth
		ua, domain string
	}{
		{"国内版", cnAuth(), "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0", "www.codebuddy.cn"},
		{"国内版没有登录域", &auth.Auth{UID: "u-cn", AccessToken: "tok"}, "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0", "www.workbuddy.cn"},
		{"国际版", globalAuth(), "WorkBuddy/5.8.4 workbuddy-ai/5.8.4 CLI/2.161.0", "www.workbuddy.ai"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.a
			a.RefreshToken = "rt"
			req, _ := http.NewRequest(http.MethodPost, "https://x/v2/plugin/auth/token/refresh", nil)
			(&Client{}).RefreshHeaders(req, a)
			wantHeaders(t, req.Header, map[string]string{
				"Accept":                "application/json, text/plain, */*",
				"Content-Type":          "application/json",
				"X-Requested-With":      "XMLHttpRequest",
				"User-Agent":            tc.ua,
				"Authorization":         "Bearer tok",
				"X-User-Id":             a.UID,
				"X-Domain":              tc.domain,
				"X-Refresh-Token":       "rt",
				"X-Auth-Refresh-Source": "plugin",
				"X-Product":             "SaaS",
				"Origin":                "", "Referer": "", "Accept-Language": "", "X-CodeBuddy-Request": "",
				"X-Machine-ID": "", "X-Session-ID": "",
			})
			wantRequestID(t, req.Header, "")
		})
	}
}

// TestMiniProgramWebViewShape 小程序口径是「同一份网页嵌在微信 web-view 里」的形状：
// 发往官网域、Bearer 认人、X-Client-Platform: miniprogram、UA 为 miniProgramUA
// （显式 user_agent 优先），不带 Cookie 与旧版 CLI 头（X-CodeBuddy-Request、X-User-Id、
// X-Domain、X-Tenant-Id、X-Device-Token）。
func TestMiniProgramWebViewShape(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	cnEnt := cnAuth()
	cnEnt.EnterpriseID = "corp-cn"
	for _, tc := range []struct {
		name      string
		c         *Client
		a         *auth.Auth
		lang, ua  string
		ent, base string
	}{
		{"国内版企业号", &Client{GlobalEnabled: true}, cnEnt, "zh-CN", miniProgramUA, "corp-cn", cnProductEndpoint},
		{"国际版", &Client{GlobalEnabled: true}, globalAuth(), "en-US", miniProgramUA, "", defaultGlobalBase},
		{"显式 user_agent", &Client{GlobalEnabled: true, UserAgent: "MP/1"}, cnAuth(), "zh-CN", "MP/1", "", cnProductEndpoint},
		{"client_name=SaaS 不改变 UA", &Client{GlobalEnabled: true, ClientName: "SaaS"}, cnAuth(), "zh-CN", miniProgramUA, "", cnProductEndpoint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, tc.base+"/activity/growth/tasks/accept", strings.NewReader("{}"))
			tc.c.webMPHeaders(req, tc.a)
			wantHeaders(t, req.Header, map[string]string{
				"Accept":              "application/json, text/plain, */*",
				"Content-Type":        "application/json",
				"Accept-Language":     tc.lang,
				"User-Agent":          tc.ua,
				"Authorization":       "Bearer tok",
				"X-Enterprise-Id":     tc.ent,
				"X-Client-Platform":   mpPlatform,
				"Origin":              tc.base,
				"Referer":             tc.base + webPageGrowthCenter,
				"Sec-Fetch-Site":      "same-origin",
				"Sec-Fetch-Mode":      "cors",
				"Sec-Fetch-Dest":      "empty",
				"X-Tenant-Id":         "",
				"X-User-Id":           "",
				"X-Domain":            "",
				"X-Device-Token":      "",
				"X-CodeBuddy-Request": "",
				"Cookie":              "",
				"Sec-CH-UA":           "",
				"Sec-CH-UA-Mobile":    "",
				"Sec-CH-UA-Platform":  "",
			})
		})
	}
}
