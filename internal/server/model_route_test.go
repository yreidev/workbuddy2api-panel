// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"context"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
)

// routeRig 国内版目录 glm-5.2、国际版目录 gpt-5.5，开跨域合池（目录外的名字走国内版），带一组路由规则。
func routeRig(t *testing.T, globalEnabled bool, routes ...livecfg.ModelRoute) (*Handler, *crossRealmUpstream) {
	t.Helper()
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	resetModelsCache(t)
	cu := &crossRealmUpstream{}
	up := cu.client(t, []string{"glm-5.2"}, []string{"gpt-5.5"})
	cn := &auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	gl := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
	h := NewHandler(Config{
		Pool: testPoolWith(cn, gl), Upstream: up, GlobalEnabled: globalEnabled,
		Live: livecfg.New(livecfg.Snapshot{CrossRealm: livecfg.CrossRealmMerge, ModelRoutes: routes}),
	})
	return h, cu
}

var testRoutes = []livecfg.ModelRoute{
	{Match: "claude-*haiku*", Target: "global:gpt-5.5"},
	{Match: "claude-*", Target: "glm-5.2"},
	{Match: "gpt-*", Target: "glm-5.2"}, // 目录里有的 gpt-5.5 不该被它改
	{Match: "codex-mini*", Target: "typo-model"},
	{Match: "typo*", Target: "glm-5.2"}, // 入口路由过的名字不再路由第二遍
}

// TestRouteChat /v1/chat/completions：目录外的名字按第一条匹配的规则改写，目录里有的、带前缀的不动。
func TestRouteChat(t *testing.T) {
	h, cu := routeRig(t, true, testRoutes...)
	cases := []struct{ model, wantModel, wantHost string }{
		{"glm-5.2", "glm-5.2", "cn.fake"},
		{"gpt-5.5", "gpt-5.5", "global.fake"},
		{"CLAUDE-HAIKU-4-5", "gpt-5.5", "global.fake"},
		{"claude-sonnet-4-5", "glm-5.2", "cn.fake"},
		{"gpt-9", "glm-5.2", "cn.fake"},
		{"cn:claude-x", "claude-x", "cn.fake"},
		{"qwen-max", "qwen-max", "cn.fake"},
		{"codex-mini-latest", "typo-model", "cn.fake"},
	}
	for _, c := range cases {
		body := `{"model":"` + c.model + `","messages":[{"role":"user","content":"hi"}],"stream":true}`
		rec := post(h, "/v1/chat/completions", body)
		if rec.Code != 200 {
			t.Fatalf("%s: code=%d body=%s", c.model, rec.Code, rec.Body)
		}
		if got, host := cu.lastModel(), cu.lastChat(); got != c.wantModel || host != c.wantHost {
			t.Errorf("%s: 上游收到 %s@%s want %s@%s", c.model, got, host, c.wantModel, c.wantHost)
		}
	}
}

// TestRouteBridge Claude Code / Codex 入口：先路由再查目录；目标写错时指出是哪条规则。
func TestRouteBridge(t *testing.T) {
	h, cu := routeRig(t, true, testRoutes...)
	claude := func(model string) string {
		return strings.Replace(strings.Replace(claudeBody, "%s", "false", 1), `"glm-5.2"`, `"`+model+`"`, 1)
	}
	if rec := post(h, "/v1/messages", claude("claude-opus-4-1")); rec.Code != 200 || cu.lastModel() != "glm-5.2" {
		t.Errorf("messages：code=%d 上游收到 %s body=%s", rec.Code, cu.lastModel(), rec.Body)
	}
	if rec := post(h, "/v1/responses", strings.Replace(codexBody, `"glm-5.2"`, `"gpt-9"`, 1)); rec.Code != 200 || cu.lastModel() != "glm-5.2" {
		t.Errorf("responses：code=%d 上游收到 %s body=%s", rec.Code, cu.lastModel(), rec.Body)
	}
	before := len(cu.chats)
	rec := post(h, "/v1/messages", claude("codex-mini-latest"))
	typ, msg := anthropicErr(t, rec)
	if rec.Code != 404 || typ != "not_found_error" || !strings.Contains(msg, "model route codex-mini-latest -> typo-model") {
		t.Errorf("目标写错：code=%d type=%s msg=%s", rec.Code, typ, msg)
	}
	if len(cu.chats) != before {
		t.Error("目标写错的请求不该打上游")
	}
}

func TestRouteModelSkips(t *testing.T) {
	ctx := context.Background()

	h, _ := routeRig(t, true)
	if got, ok := h.routeModel(ctx, "claude-x"); ok || got != "claude-x" {
		t.Errorf("没有规则：%s %v", got, ok)
	}

	h, _ = routeRig(t, true, livecfg.ModelRoute{Match: "*", Target: "glm-5.2"})
	if _, ok := h.routeModel(ctx, ""); ok {
		t.Error("空模型名不该路由")
	}
	if got, ok := h.routeModel(context.WithValue(ctx, routedKey{}, true), "claude-x"); ok || got != "claude-x" {
		t.Errorf("入口已路由过：%s %v", got, ok)
	}
	if got, ok := h.routeModel(ctx, "global:claude-x"); ok || got != "global:claude-x" {
		t.Errorf("带前缀：%s %v", got, ok)
	}

	// 国际版没启用：国际版目录不算，只在国际版有的名字也会被规则改写
	h, _ = routeRig(t, false, livecfg.ModelRoute{Match: "*", Target: "glm-5.2"})
	if got, ok := h.routeModel(ctx, "gpt-5.5"); !ok || got != "glm-5.2" {
		t.Errorf("国际版未启用：%s %v", got, ok)
	}

	// 没有上游客户端：不路由
	nh := NewHandler(Config{Pool: testPoolWith(), Live: livecfg.New(livecfg.Snapshot{ModelRoutes: []livecfg.ModelRoute{{Match: "*", Target: "x"}}})})
	if _, ok := nh.routeModel(ctx, "claude-x"); ok {
		t.Error("没有上游时不该路由")
	}
}

// TestRouteCatalogUnavailable 国内版目录拉不到时判断不了名字在不在目录里，不路由，原样交给上游。
func TestRouteCatalogUnavailable(t *testing.T) {
	resetModelsCache(t)
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{
		Pool:     testPoolWith(), // 池里没号：拉不到目录
		Upstream: up,
		Live:     livecfg.New(livecfg.Snapshot{ModelRoutes: []livecfg.ModelRoute{{Match: "*", Target: "glm-5.2"}}}),
	})
	if got, ok := h.routeModel(context.Background(), "claude-x"); ok || got != "claude-x" {
		t.Errorf("目录拉不到：%s %v", got, ok)
	}
}
