// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestChatHeadersOfficialShape chat 出站头对齐官方 CLI 的对话请求（10-19 生产逐项实测）：
// 该带的都带，官方对话请求里没有的一个都不带；企业号加 X-Enterprise-Id 与同值的 X-Tenant-Id；
// 两域（国内版 5.9.3、国际版 5.9.4，都内置 2.165.0）都带 X-Agent-Runtime: local-agent。
func TestChatHeadersOfficialShape(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	cnEnt := cnAuth()
	cnEnt.EnterpriseID = "corp-cn"
	glEnt := globalAuth()
	glEnt.EnterpriseID = "corp-g"
	cases := []struct {
		name   string
		a      *auth.Auth
		domain string
		ent    string
	}{
		{"国内版个人号", cnAuth(), "www.codebuddy.cn", ""},
		{"国内版企业号", cnEnt, "www.codebuddy.cn", "corp-cn"},
		{"国内版没有登录域", &auth.Auth{UID: "u-cn", AccessToken: "tok"}, "", ""},
		{"国际版个人号", globalAuth(), "www.workbuddy.ai", ""},
		{"国际版企业号", glEnt, "www.workbuddy.ai", "corp-g"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "https://x/v2/chat/completions", nil)
			(&Client{}).ChatHeaders(req, tc.a, "", ChatMeta{})
			want := map[string]string{
				"Content-Type":     "application/json",
				"Accept":           "application/json",
				"X-Requested-With": "XMLHttpRequest",
				"Authorization":    "Bearer tok",
				"X-User-Id":        tc.a.UID,
				"X-Domain":         tc.domain,
				"X-Enterprise-Id":  tc.ent,
				"X-Tenant-Id":      tc.ent,
				"X-Agent-Intent":   "craft",
				"X-Agent-Type":     "main",
				"X-Agent-Runtime":  "local-agent",
				"X-Agent-Purpose":  "conversation",
				"X-Private-Data":   "true",
				"X-Product":        "SaaS",
				// OpenAI SDK 的 8 个 X-Stainless-*（见 injectSDKHeaders）：两域内置 CLI 同一份 SDK，
				// 设备指纹是 Mac arm64、Electron 内置 v22.21.1，取值与域无关。
				"X-Stainless-Retry-Count":     "0",
				"X-Stainless-Timeout":         "1200",
				"X-Stainless-Lang":            "js",
				"X-Stainless-Package-Version": "6.25.0",
				"X-Stainless-OS":              "MacOS",
				"X-Stainless-Arch":            "arm64",
				"X-Stainless-Runtime":         "node",
				"X-Stainless-Runtime-Version": "v22.21.1",
			}
			for k, v := range want {
				got, present := req.Header.Get(k), len(req.Header.Values(k)) > 0
				if got != v || present != (v != "") {
					t.Errorf("%s = %q（发了=%v），want %q", k, got, present, v)
				}
			}
			for _, k := range []string{
				"Origin", "Referer", "Accept-Language", "X-CodeBuddy-Request", "X-Machine-ID", "X-Session-ID",
				"X-No-Enterprise-Id", "X-No-Department-Info", "X-No-User-Id", "X-No-Authorization", "X-Root-Request-ID",
				"X-Refresh-Token", "X-Expert-Id", "X-Device-Token", "X-Moderation-Type",
			} {
				if v := req.Header.Values(k); len(v) > 0 {
					t.Errorf("官方对话请求不带 %s，实际 %q", k, v)
				}
			}
			if req.Header.Get("User-Agent") == "" {
				t.Error("User-Agent 必发")
			}
		})
	}
}

// TestChatHeadersPrivateData X-Private-Data：默认一律 "true"（不允许做模型优化）；打开 model_optimization 后
// 个人版 "false"，企业版仍 "true"。企业版照官方 isEnterpriseEditionAccount：有企业 id，或账号类型是
// ultimate / exclusive。
func TestChatHeadersPrivateData(t *testing.T) {
	ent := cnAuth()
	ent.EnterpriseID = "corp-cn"
	typed := func(typ string) *auth.Auth {
		a := cnAuth()
		a.AccountType = &typ
		return a
	}
	for _, tc := range []struct {
		name string
		opt  bool
		a    *auth.Auth
		want string
	}{
		{"默认·个人号", false, cnAuth(), "true"},
		{"默认·企业号", false, ent, "true"},
		{"打开·个人号", true, cnAuth(), "false"},
		{"打开·企业号", true, ent, "true"},
		{"打开·ultimate", true, typed("ultimate"), "true"},
		{"打开·exclusive", true, typed("exclusive"), "true"},
		{"打开·其他类型的个人号", true, typed("personal"), "false"},
		{"默认·ultimate", false, typed("ultimate"), "true"},
	} {
		c := &Client{}
		c.ModelOptimization.Store(tc.opt)
		req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
		c.ChatHeaders(req, tc.a, "", ChatMeta{})
		if got := req.Header.Get("X-Private-Data"); got != tc.want {
			t.Errorf("%s: X-Private-Data = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := (*Client)(nil).privateDataValue(cnAuth()); got != "true" {
		t.Errorf("nil Client 应按默认发 true: %q", got)
	}
}

// TestChatHeadersExpertID 专家对话由 ChatMeta.ExpertID 带 X-Expert-Id；普通对话（空）
// 不带这个头（官方专家插件只在激活时追加）。
func TestChatHeadersExpertID(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{}).ChatHeaders(req, cnAuth(), "", ChatMeta{ExpertID: "ex_1"})
	if got := req.Header.Get("X-Expert-Id"); got != "ex_1" {
		t.Errorf("X-Expert-Id = %q，应为 ex_1", got)
	}
	plain, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{}).ChatHeaders(plain, cnAuth(), "", ChatMeta{})
	if v := plain.Header.Values("X-Expert-Id"); len(v) > 0 {
		t.Errorf("ExpertID 为空时不应带 X-Expert-Id，实际 %q", v)
	}
	if v := req.Header.Values("X-Expert-Team-Task"); len(v) > 0 {
		t.Errorf("单个专家不应带 X-Expert-Team-Task，实际 %q", v)
	}
	// X-Expert-Team-Task 只和 X-Expert-Id 一起发。
	orphan, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{}).ChatHeaders(orphan, cnAuth(), "", ChatMeta{ExpertTeamTask: true})
	if v := orphan.Header.Values("X-Expert-Team-Task"); len(v) > 0 {
		t.Errorf("没有专家 id 时不应带 X-Expert-Team-Task，实际 %q", v)
	}
	// X-Moderation-Type 只在专家云端 / 付费（Protected）时发；上面 ChatMeta{ExpertID:"ex_1"} 的 req 不带。
	if v := req.Header.Values("X-Moderation-Type"); len(v) > 0 {
		t.Errorf("Protected 为假时不应带 X-Moderation-Type，实际 %q", v)
	}
	prot, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{}).ChatHeaders(prot, cnAuth(), "", ChatMeta{ExpertID: "ex_1", Protected: true})
	if got := prot.Header.Get("X-Moderation-Type"); got != `["protected"]` {
		t.Errorf("Protected 为真时 X-Moderation-Type = %q，应为 [\"protected\"]", got)
	}
}

// TestChatHeadersSDKHeaders OpenAI SDK 的 X-Stainless-* 与 Accept（injectSDKHeaders）：
//   - 国际版 Runtime-Version 与国内版一致（两域 Electron 同为 v22.21.1）；
//   - client_name="SaaS"（旧行为）不发 X-Stainless-*，Accept 回落 1.x 起的两值；
//   - stainlessOS / stainlessArch 复刻 SDK 的 normalizePlatform / normalizeArch。
func TestChatHeadersSDKHeaders(t *testing.T) {
	gl, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{}).ChatHeaders(gl, globalAuth(), "", ChatMeta{})
	if got := gl.Header.Get("X-Stainless-Runtime-Version"); got != "v22.21.1" {
		t.Errorf("国际版 X-Stainless-Runtime-Version = %q, want v22.21.1", got)
	}

	saas := &Client{ClientName: "SaaS"}
	req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	saas.ChatHeaders(req, cnAuth(), "", ChatMeta{})
	if got := req.Header.Get("Accept"); got != "application/json, text/event-stream" {
		t.Errorf("SaaS 旧行为 Accept = %q, want application/json, text/event-stream", got)
	}
	for _, k := range []string{
		"X-Stainless-Retry-Count", "X-Stainless-Timeout", "X-Stainless-Lang", "X-Stainless-Package-Version",
		"X-Stainless-OS", "X-Stainless-Arch", "X-Stainless-Runtime", "X-Stainless-Runtime-Version",
	} {
		if v := req.Header.Values(k); len(v) > 0 {
			t.Errorf("SaaS 旧行为不应发 %s，实际 %q", k, v)
		}
	}

	for _, tc := range []struct{ in, os, arch string }{
		{"darwin", "MacOS", ""}, {"win32", "Windows", ""}, {"linux", "Linux", ""},
		{"freebsd", "FreeBSD", ""}, {"openbsd", "OpenBSD", ""}, {"", "Unknown", ""}, {"sunos", "Other:sunos", ""},
	} {
		if got := stainlessOS(tc.in); got != tc.os {
			t.Errorf("stainlessOS(%q) = %q, want %q", tc.in, got, tc.os)
		}
	}
	for _, tc := range []struct{ in, arch string }{
		{"arm64", "arm64"}, {"aarch64", "arm64"}, {"x64", "x64"}, {"x86_64", "x64"},
		{"x32", "x32"}, {"arm", "arm"}, {"", "unknown"}, {"mips", "other:mips"},
	} {
		if got := stainlessArch(tc.in); got != tc.arch {
			t.Errorf("stainlessArch(%q) = %q, want %q", tc.in, got, tc.arch)
		}
	}
}

// TestChatHeadersTraceContext 官方同发 W3C traceparent 与单行 b3，和 X-B3-* 用同一组 trace/span id；
// 16 位的 B3 TraceId 进 traceparent 时左补零到 32 位。
func TestChatHeadersTraceContext(t *testing.T) {
	traceparent := regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)
	for _, tc := range []struct {
		name, turn, w3c string
	}{
		{"32 位轮次 id", "0123456789ABCDEF0123456789abcdef", "0123456789abcdef0123456789abcdef"},
		{"16 位轮次 id", "0123456789abcdef", "00000000000000000123456789abcdef"},
	} {
		req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
		(&Client{}).ChatHeaders(req, cnAuth(), "", ChatMeta{ConversationRequestID: tc.turn})
		span := req.Header.Get("X-B3-SpanId")
		tp := req.Header.Get("traceparent")
		if !traceparent.MatchString(tp) || tp != "00-"+tc.w3c+"-"+span+"-01" {
			t.Errorf("%s: traceparent = %q（span %q）", tc.name, tp, span)
		}
		if got, want := req.Header.Get("b3"), tc.w3c[32-len(tc.turn):]+"-"+span+"-1"; got != want {
			t.Errorf("%s: b3 = %q, want %q", tc.name, got, want)
		}
		if req.Header.Get("X-B3-TraceId") != tc.turn {
			t.Errorf("%s: X-B3-TraceId = %q", tc.name, req.Header.Get("X-B3-TraceId"))
		}
	}
}
