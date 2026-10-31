// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// errReadBody 读取请求体时报错的 io.ReadCloser。
type errReadBody struct{}

func (errReadBody) Read([]byte) (int, error) { return 0, errors.New("client gone") }
func (errReadBody) Close() error             { return nil }

// policyFixture 构造单账号池 + Handler（策略断言用）。
func policyFixture(t *testing.T) (*Handler, *pool.Pool) {
	t.Helper()
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, `{"code":0}`, false })
	return NewHandler(Config{Pool: p, Upstream: up}), p
}

// clearModelsCache 清空包级模型目录缓存（不登记还原）。
func clearModelsCache() {
	dynamicModelsCache.Lock()
	dynamicModelsCache.ids = nil
	dynamicModelsCache.fetched = time.Time{}
	dynamicModelsCache.lastFail = time.Time{}
	dynamicModelsCache.Unlock()
}

// resetModelsCache 清空包级模型目录缓存并登记还原。
func resetModelsCache(t *testing.T) {
	t.Helper()
	clearModelsCache()
	t.Cleanup(clearModelsCache)
}

// TestFmtCreditsPrefix 倍率原文前缀格式化。
func TestFmtCreditsPrefix(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"   ":              "",
		"credits":          "",
		"x0.05 credits":    "[x0.05 credit]",
		"x0.29":            "[x0.29 credit]",
		"  x0.00 credits ": "[x0.00 credit]",
	}
	for in, want := range cases {
		if got := fmtCreditsPrefix(in); got != want {
			t.Errorf("fmtCreditsPrefix(%q) = %q，应为 %q", in, got, want)
		}
	}
}

// TestPromptTooLongMessage 空 body 用兜底短文案，否则透传原文。
func TestPromptTooLongMessage(t *testing.T) {
	for _, body := range []string{"", "   ", "\n\t"} {
		if got := promptTooLongMessage(body); got != "prompt is too long" {
			t.Errorf("空 body %q → %q", body, got)
		}
	}
	raw := `{"code":11115,"msg":"prompt is too long: 200000 tokens > 128000"}`
	if got := promptTooLongMessage(raw); got != raw {
		t.Errorf("应原样透传，got %q", got)
	}
}

// TestHasImagePart 只认 messages[].content[] 里的 image_url；坏 JSON 与其他形态为 false。
func TestHasImagePart(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"坏 JSON", `{bad`, false},
		{"无 messages", `{"model":"m"}`, false},
		{"纯文本数组", `{"messages":[{"content":[{"type":"text","text":"hi"}]}]}`, false},
		{"content 是字符串", `{"messages":[{"content":"看图"}]}`, false},
		{"带图片", `{"messages":[{"content":[{"type":"text"},{"type":"image_url"}]}]}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasImagePart([]byte(tc.body)); got != tc.want {
				t.Errorf("hasImagePart = %v，应为 %v", got, tc.want)
			}
		})
	}
}

// TestHintContext 模型目录快照判定：空模型、命中、未收录、缓存过期。
func TestHintContext(t *testing.T) {
	resetModelsCache(t)
	h, _ := policyFixture(t)
	if ctx := h.hintContext("", true); ctx.Model != "" || ctx.ModelInCatalog || !ctx.HasImage {
		t.Errorf("空模型上下文 = %+v", ctx)
	}

	dynamicModelsCache.Lock()
	dynamicModelsCache.ids = []upstream.ModelInfo{
		{ID: "vision-model", SupportsImages: true},
		{ID: "text-model"},
	}
	dynamicModelsCache.fetched = time.Now()
	dynamicModelsCache.Unlock()

	if ctx := h.hintContext("vision-model", true); !ctx.ModelInCatalog || !ctx.ModelSupportsImages {
		t.Errorf("支持图片的模型 = %+v", ctx)
	}
	if ctx := h.hintContext("text-model", true); !ctx.ModelInCatalog || ctx.ModelSupportsImages {
		t.Errorf("纯文本模型 = %+v", ctx)
	}
	if ctx := h.hintContext("absent", true); ctx.ModelInCatalog {
		t.Errorf("目录未收录不应判定 = %+v", ctx)
	}

	// 缓存过期 → 不判定。
	dynamicModelsCache.Lock()
	dynamicModelsCache.fetched = time.Now().Add(-dynamicModelsTTL - time.Minute)
	dynamicModelsCache.Unlock()
	if ctx := h.hintContext("vision-model", true); ctx.ModelInCatalog {
		t.Errorf("过期缓存不应判定 = %+v", ctx)
	}
}

// TestApplyModelInfoFields 全字段写出与空值省略；不覆盖调用方基础字段。
func TestApplyModelInfoFields(t *testing.T) {
	base := map[string]any{"id": "cn:x", "object": "model"}
	out := applyModelInfoFields(base, upstream.ModelInfo{})
	if len(out) != 2 || out["id"] != "cn:x" || out["object"] != "model" {
		t.Errorf("空 ModelInfo 不应新增字段: %v", out)
	}
	if got := applyModelInfoFields(map[string]any{"id": "keep"}, upstream.ModelInfo{ID: "other"}); got["id"] != "keep" {
		t.Errorf("不应覆盖 id，got %v", got["id"])
	}

	full := upstream.ModelInfo{
		ID: "m", Name: "模型", Description: "中文描述", Credits: "x0.05 credits",
		Tags: []string{"a"}, Vendor: "zai", IsDefault: true,
		SupportsImages: true, SupportsReasoning: true, CanDisableThinking: true,
		SupportsToolCall: true, OnlyReasoning: true, MaxAllowedSize: 12345,
		ReasoningEffort: "high", ReasoningSummary: "auto",
	}
	got := applyModelInfoFields(map[string]any{"id": "cn:m"}, full)
	want := map[string]any{
		"id": "cn:m", "name": "模型", "description": "[x0.05 credit] 中文描述",
		"credits": "x0.05 credits", "tags": []string{"a"}, "vendor": "zai",
		"is_default": true, "supports_images": true, "supports_reasoning": true,
		"can_disable_thinking": true, "supports_tool_call": true, "only_reasoning": true,
		"max_allowed_size": int64(12345), "reasoning_effort": "high", "reasoning_summary": "auto",
	}
	for k, wantV := range want {
		if gotV, ok := got[k]; !ok || !jsonEqual(gotV, wantV) {
			t.Errorf("字段 %s = %v，应为 %v", k, gotV, wantV)
		}
	}

	noCredits := applyModelInfoFields(map[string]any{}, upstream.ModelInfo{Description: "描述"})
	if noCredits["description"] != "描述" {
		t.Errorf("无倍率时 description = %v", noCredits["description"])
	}
	noDisable := applyModelInfoFields(map[string]any{}, upstream.ModelInfo{SupportsReasoning: true})
	if _, ok := noDisable["can_disable_thinking"]; ok {
		t.Error("不可关闭思考时不应写 can_disable_thinking")
	}
}

func jsonEqual(a, b any) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}

// TestSoftCooldownPriority 热改值 > 配置值 > 默认 600s。
func TestSoftCooldownPriority(t *testing.T) {
	def := NewHandler(Config{})
	if got := def.softCooldown(); got != 600*time.Second {
		t.Errorf("默认软冷却 = %v，应为 600s", got)
	}

	static := NewHandler(Config{SoftCooldown: 90 * time.Second})
	if got := static.softCooldown(); got != 90*time.Second {
		t.Errorf("配置值 = %v", got)
	}

	live := livecfg.New(livecfg.Snapshot{SoftCooldown: 30 * time.Second})
	hot := NewHandler(Config{SoftCooldown: 90 * time.Second, Live: live})
	if got := hot.softCooldown(); got != 30*time.Second {
		t.Errorf("热改值应优先，got %v", got)
	}

	zeroLive := NewHandler(Config{SoftCooldown: 90 * time.Second, Live: livecfg.New(livecfg.Snapshot{})})
	if got := zeroLive.softCooldown(); got != 90*time.Second {
		t.Errorf("热改值为 0 应回落配置值，got %v", got)
	}
}

// TestRotateBackoff 退避为 0 按 ctx 状态；ctx 取消立即 false；正常返回 true（第 0 次）。
func TestRotateBackoff(t *testing.T) {
	old := rotateBackoffBase
	t.Cleanup(func() { rotateBackoffBase = old })

	rotateBackoffBase = 0
	if !rotateBackoff(0, context.Background()) {
		t.Error("退避 0 + 活跃 ctx 应返回 true")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if rotateBackoff(0, ctx) {
		t.Error("退避 0 + 已取消 ctx 应返回 false")
	}

	rotateBackoffBase = time.Hour
	if rotateBackoff(0, ctx) {
		t.Error("有退避但 ctx 已取消应返回 false")
	}
}

// TestApplyErrorPolicyKinds 十条错误策略逐条断言号池状态。
func TestApplyErrorPolicyKinds(t *testing.T) {
	t.Run("硬额度冷却到次日04:00", func(t *testing.T) {
		h, p := policyFixture(t)
		h.applyErrorPolicy("u1", upstream.ErrHardCredit, "", "m", nil)
		st, _ := p.Status("u1")
		if !st.Cooling || st.CoolKind != "hard_credit" || st.Until.Hour() != 4 {
			t.Errorf("硬冷却 = %+v", st)
		}
	})

	t.Run("6004 带重置时间写模型级", func(t *testing.T) {
		h, p := policyFixture(t)
		body := `{"code":6004,"msg":"模型限流，将在 2026-10-20 01:00:00 重置"}`
		h.applyErrorPolicy("u1", upstream.ErrSoftRate, body, "glm-5.2", nil)
		st, _ := p.Status("u1")
		if len(st.RateLimitedModels) != 1 || st.RateLimitedModels[0].Model != "glm-5.2" ||
			st.RateLimitedModels[0].Kind != "rate_limit" {
			t.Errorf("模型级限流 = %+v", st.RateLimitedModels)
		}
		if st.Cooling {
			t.Error("模型级限流不应产生账号级冷却")
		}
	})

	t.Run("非 6004 带重置时间账号级", func(t *testing.T) {
		h, p := policyFixture(t)
		body := `{"code":11140,"msg":"rate limit，将在 2026-10-20 01:00:00 重置"}`
		h.applyErrorPolicy("u1", upstream.ErrSoftRate, body, "glm-5.2", nil)
		st, _ := p.Status("u1")
		if !st.Cooling || st.CoolKind != "soft_rate" || len(st.RateLimitedModels) != 0 {
			t.Errorf("账号级限流 = %+v", st)
		}
	})

	t.Run("RetryAfter 优先", func(t *testing.T) {
		h, p := policyFixture(t)
		uerr := &upstream.Error{Kind: upstream.ErrSoftRate, RetryAfter: 45 * time.Second}
		h.applyErrorPolicy("u1", upstream.ErrSoftRate, `{"code":6004}`, "glm-5.2", uerr)
		st, _ := p.Status("u1")
		if !st.Cooling || st.CoolKind != "soft_rate" {
			t.Errorf("retry-after 冷却 = %+v", st)
		}
		if len(st.RateLimitedModels) != 1 || st.RateLimitedModels[0].Kind != "rate_limit" {
			t.Errorf("6004 应同时写模型审计 = %+v", st.RateLimitedModels)
		}
	})

	t.Run("无重置走有界退避", func(t *testing.T) {
		h, p := policyFixture(t)
		h.applyErrorPolicy("u1", upstream.ErrSoftRate, `{"code":6004}`, "glm-5.2", nil)
		st, _ := p.Status("u1")
		if !st.Cooling || st.CoolKind != "soft_rate" {
			t.Errorf("有界退避冷却 = %+v", st)
		}
		if len(st.RateLimitedModels) != 1 {
			t.Errorf("6004 应写模型审计 = %+v", st.RateLimitedModels)
		}
	})

	t.Run("WAF 只冷却不禁用", func(t *testing.T) {
		h, p := policyFixture(t)
		h.applyErrorPolicy("u1", upstream.ErrWafBlock, "blocked", "m", nil)
		st, _ := p.Status("u1")
		if !st.Cooling || st.Disabled || st.CoolKind != "soft_rate" {
			t.Errorf("WAF 冷却 = %+v", st)
		}

		h2, p2 := policyFixture(t)
		h2.applyErrorPolicy("u1", upstream.ErrWafBlock, "blocked", "m",
			&upstream.Error{Kind: upstream.ErrWafBlock, RetryAfter: 30 * time.Second})
		st2, _ := p2.Status("u1")
		if !st2.Cooling || st2.Disabled {
			t.Errorf("WAF retry-after = %+v", st2)
		}
	})

	t.Run("404 固定短冷却", func(t *testing.T) {
		h, p := policyFixture(t)
		h.applyErrorPolicy("u1", upstream.ErrNotFound, "not found", "m", nil)
		st, _ := p.Status("u1")
		if !st.Cooling || st.CoolKind != "soft_rate" || st.CoolRemaining == 0 || st.CoolRemaining > 61 {
			t.Errorf("404 冷却 = %+v", st)
		}
	})

	t.Run("会话失效禁用", func(t *testing.T) {
		h, p := policyFixture(t)
		h.applyErrorPolicy("u1", upstream.ErrSessionDead, "12153", "m", nil)
		st, _ := p.Status("u1")
		if !st.Disabled || !strings.Contains(st.DisabledReason, "12153") {
			t.Errorf("session dead = %+v", st)
		}
	})

	t.Run("账号故障按文案分野", func(t *testing.T) {
		h, p := policyFixture(t)
		h.applyErrorPolicy("u1", upstream.ErrAccountFault, "request illegal", "m", nil)
		st, _ := p.Status("u1")
		if !st.Disabled || !strings.Contains(st.DisabledReason, "appeal") {
			t.Errorf("11140 request illegal 应禁用并提示申诉: %+v", st)
		}

		h2, p2 := policyFixture(t)
		h2.applyErrorPolicy("u1", upstream.ErrAccountFault, "trial not activated", "m", nil)
		st2, _ := p2.Status("u1")
		if !st2.Cooling || st2.Disabled {
			t.Errorf("14017 应软冷却: %+v", st2)
		}
	})

	t.Run("11140 风控波暂停同域", func(t *testing.T) {
		p := testPoolWith(
			&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999},
			&auth.Auth{UID: "u2", AccessToken: "at", ExpiresAt: 9999999999},
			&auth.Auth{UID: "u3", AccessToken: "at", ExpiresAt: 9999999999},
		)
		up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, `{"code":0}`, false })
		h := NewHandler(Config{Pool: p, Upstream: up})
		h.applyErrorPolicy("u1", upstream.ErrAccountFault, "request illegal", "m", nil)
		if st, _ := p.Status("u3"); st.Cooling {
			t.Fatalf("一个号被限不应暂停同域: %+v", st)
		}
		h.applyErrorPolicy("u2", upstream.ErrAccountFault, "request illegal", "m", nil)
		if st, _ := p.Status("u3"); !st.Cooling || st.CoolKind != "restriction_wave" {
			t.Errorf("两个号被限应暂停同域其余的号: %+v", st)
		}
	})

	t.Run("已经禁用的号再回 11140 不记风控波，人工禁用不改原因", func(t *testing.T) {
		p := testPoolWith(
			&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999},
			&auth.Auth{UID: "u2", AccessToken: "at", ExpiresAt: 9999999999},
			&auth.Auth{UID: "u3", AccessToken: "at", ExpiresAt: 9999999999},
		)
		p.Disable("u1", "12153 session dead")
		p.Disable("u2", pool.ManualDisableReason)
		h := NewHandler(Config{Pool: p, Upstream: newFakeUpstream(t, func(string) (int, string, bool) { return 200, `{"code":0}`, false })})
		// 测活复测这两个号都回 11140：不是新的风控
		h.applyErrorPolicy("u1", upstream.ErrAccountFault, "request illegal", "m", nil)
		h.applyErrorPolicy("u2", upstream.ErrAccountFault, "request illegal", "m", nil)
		if st, _ := p.Status("u3"); st.Cooling {
			t.Fatalf("复测已禁用的号不应触发风控波: %+v", st)
		}
		if st, _ := p.Status("u1"); !strings.Contains(st.DisabledReason, "11140") {
			t.Errorf("自动禁用的号原因应更新成 11140: %q", st.DisabledReason)
		}
		if st, _ := p.Status("u2"); st.DisabledReason != pool.ManualDisableReason {
			t.Errorf("人工禁用的原因不应被改: %q", st.DisabledReason)
		}
	})

	t.Run("服务端错误计入熔断", func(t *testing.T) {
		h, p := policyFixture(t)
		for i := 0; i < 3; i++ { // 默认熔断阈值 3：第三次触发熔断期
			h.applyErrorPolicy("u1", upstream.ErrServer, "500", "m", nil)
		}
		st, _ := p.Status("u1")
		if !st.Cooling || st.CoolKind != "breaker" || st.ErrTotal != 3 || st.BreakerFails != 0 {
			t.Errorf("5xx 熔断 = %+v", st)
		}
	})

	t.Run("内容/参数/超长/图片零动作", func(t *testing.T) {
		for _, kind := range []upstream.ErrKind{
			upstream.ErrContentBlocked, upstream.ErrBadParams,
			upstream.ErrPromptTooLong, upstream.ErrImageInvalid,
		} {
			h, p := policyFixture(t)
			h.applyErrorPolicy("u1", kind, "body", "m", nil)
			st, _ := p.Status("u1")
			if st.Cooling || st.Disabled || st.ErrTotal != 0 || st.ConsecutiveFails != 0 {
				t.Errorf("%s 不应罚号: %+v", kind, st)
			}
		}
	})

	t.Run("模型拦截写负缓存", func(t *testing.T) {
		h, p := policyFixture(t)
		h.applyErrorPolicy("u1", upstream.ErrModelBlocked, `{"code":11102}`, "glm-5.2", nil)
		st, _ := p.Status("u1")
		if len(st.RateLimitedModels) != 1 || st.RateLimitedModels[0].Model != "glm-5.2" ||
			st.RateLimitedModels[0].Kind != "model_unavailable" {
			t.Errorf("11102 负缓存 = %+v", st.RateLimitedModels)
		}
	})

	t.Run("其他客户端错误只计连败", func(t *testing.T) {
		h, p := policyFixture(t)
		h.applyErrorPolicy("u1", upstream.ErrClient, "400", "m", nil)
		st, _ := p.Status("u1")
		if st.ConsecutiveFails != 1 || st.Cooling || st.ErrTotal != 0 {
			t.Errorf("ErrClient = %+v", st)
		}

		h2, p2 := policyFixture(t)
		h2.applyErrorPolicy("u1", upstream.ErrNone, "defensive", "m", nil)
		st2, _ := p2.Status("u1")
		if st2.ConsecutiveFails != 0 || st2.Cooling || st2.ErrTotal != 0 {
			t.Errorf("ErrNone 不应计数 = %+v", st2)
		}
	})
}

// TestFetchDynamicModelsBranches CN 目录缓存：命中、过期重拉、无账号、失败负缓存。
func TestFetchDynamicModelsBranches(t *testing.T) {
	resetModelsCache(t)
	var calls atomic.Int64
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls.Add(1)
		return 400, `{"code":400,"msg":"boom"}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	// 失败 → nil + 负缓存；冷却期内不再请求。
	if got := h.fetchDynamicModels(); got != nil {
		t.Errorf("失败应返回 nil，got %v", got)
	}
	first := calls.Load()
	if got := h.fetchDynamicModels(); got != nil || calls.Load() != first {
		t.Errorf("负缓存期内不应重试: calls %d → %d", first, calls.Load())
	}

	// 负缓存过期 → 重试。
	dynamicModelsCache.Lock()
	dynamicModelsCache.lastFail = time.Now().Add(-modelsFetchFailCooldown - time.Minute)
	dynamicModelsCache.Unlock()
	h.fetchDynamicModels()
	if calls.Load() <= first {
		t.Error("负缓存过期后应重试")
	}

	// 成功缓存过期 → 重新拉取。
	resetModelsCache(t)
	dynamicModelsCache.Lock()
	dynamicModelsCache.ids = []upstream.ModelInfo{{ID: "stale"}}
	dynamicModelsCache.fetched = time.Now().Add(-dynamicModelsTTL - time.Minute)
	dynamicModelsCache.Unlock()
	beforeExpired := calls.Load()
	h.fetchDynamicModels()
	if calls.Load() <= beforeExpired {
		t.Error("缓存过期后应重新拉取")
	}

	// 成功缓存命中：不再请求。
	resetModelsCache(t)
	dynamicModelsCache.Lock()
	dynamicModelsCache.ids = []upstream.ModelInfo{{ID: "cached"}}
	dynamicModelsCache.fetched = time.Now()
	dynamicModelsCache.Unlock()
	before := calls.Load()
	if got := h.fetchDynamicModels(); len(got) != 1 || got[0].ID != "cached" {
		t.Errorf("缓存命中 = %v", got)
	}
	if calls.Load() != before {
		t.Error("缓存命中不应请求上游")
	}

	// 无 CN 账号 → nil（零上游调用）。
	clearModelsCache()
	empty := NewHandler(Config{Pool: pool.New(""), Upstream: up})
	if got := empty.fetchDynamicModels(); got != nil {
		t.Errorf("无账号应返回 nil，got %v", got)
	}
}

// TestModelListGlobalAndCrossRealm global 动态分支（富字段/effort/跨域裸名）与
// 无 global 账号时的空名单。
func TestModelListGlobalAndCrossRealm(t *testing.T) {
	resetModelsCache(t)
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, `{"code":0,"data":{"models":[{"id":"g-model","maxInputTokens":1000,"maxOutputTokens":8192,
			"supportsImages":true,"reasoning":{"supportedEfforts":["low","high"],"defaultEffort":"high"}}]}}`, false
	})
	up.ChatBaseGlobal = up.ChatBaseCN // global 探测也落到假上游
	up.BillingBaseGlobal = up.BillingBaseCN
	up.GlobalEnabled = true
	auth.SetGlobalEnabled(true)
	p := testPoolWith(&auth.Auth{UID: "g1", AccessToken: "at", Domain: "www.workbuddy.ai", ExpiresAt: 9999999999})
	live := livecfg.New(livecfg.Snapshot{CrossRealm: livecfg.CrossRealmMerge})
	h := NewHandler(Config{Pool: p, Upstream: up, GlobalEnabled: true, Live: live})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var prefixed, bare map[string]any
	for _, m := range resp.Data {
		switch m["id"] {
		case "global:g-model":
			prefixed = m
		case "g-model":
			bare = m
		}
	}
	if prefixed == nil {
		t.Fatalf("缺 global:g-model: %v", resp.Data)
	}
	if prefixed["supports_images"] != true || prefixed["context_length"] != float64(1000) {
		t.Errorf("富字段 = %v", prefixed)
	}
	if efforts, ok := prefixed["reasoning_supported_efforts"].([]any); !ok || len(efforts) != 2 {
		t.Errorf("effort 字段 = %v", prefixed["reasoning_supported_efforts"])
	}
	if bare == nil || bare["realms"] == nil {
		t.Errorf("跨域裸名条目缺失: %v", resp.Data)
	}

	// 无 global 账号：global 分支不输出（也不探测）。
	resetModelsCache(t)
	cnOnly := testPoolWith(&auth.Auth{UID: "c1", AccessToken: "at", ExpiresAt: 9999999999})
	h2 := NewHandler(Config{Pool: cnOnly, Upstream: newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, `{"code":0,"data":{"models":[],"agents":[{"name":"cli","models":[]}]}}`, false
	}), GlobalEnabled: true})
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if got := strings.Count(rec.Body.String(), "global:"); got != 0 {
		t.Errorf("无 global 账号不应输出 global 模型: %s", rec.Body.String())
	}

	// 国际版目录拉取失败 → global 名单为空（不编造静态名单）。
	badUp := newFakeUpstream(t, func(string) (int, string, bool) { return 500, `{"code":500}`, false })
	badUp.ChatBaseGlobal = badUp.ChatBaseCN
	badUp.GlobalEnabled = true
	p3 := testPoolWith(&auth.Auth{UID: "g2", AccessToken: "at", Domain: "www.workbuddy.ai", ExpiresAt: 9999999999})
	h3 := NewHandler(Config{Pool: p3, Upstream: badUp, GlobalEnabled: true})
	rec = httptest.NewRecorder()
	h3.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if got := strings.Count(rec.Body.String(), "global:"); got != 0 {
		t.Errorf("探测失败不应输出 global 模型: %s", rec.Body.String())
	}
}

// TestChatCompletionsReadBodyError 读请求体失败 → 400 invalid_request。
func TestChatCompletionsReadBodyError(t *testing.T) {
	h, _ := policyFixture(t)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Body = errReadBody{}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "read body") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestSoftCooldownDefaultDirect 不经 NewHandler 归一化时回退 600s 默认。
func TestSoftCooldownDefaultDirect(t *testing.T) {
	h := &Handler{cfg: Config{}}
	if got := h.softCooldown(); got != 600*time.Second {
		t.Errorf("默认软冷却 = %v，应为 600s", got)
	}
}

// TestNewHandlerPanelMux 配置了面板时挂 /panel/ 前缀。
func TestNewHandlerPanelMux(t *testing.T) {
	h := NewHandler(Config{Pool: testPoolWith(), Panel: http.NotFoundHandler()})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("面板路由未挂载: %d", rec.Code)
	}
}

// TestStatusStickyCount /status 透出粘性会话数。
func TestStatusStickyCount(t *testing.T) {
	h := NewHandler(Config{
		Pool:        testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}),
		StickyCount: func() int { return 7 },
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"sticky_sessions":7`) {
		t.Errorf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestFetchGlobalModelsNoAccount 无 global 账号时不探测（零上游调用）。
func TestFetchGlobalModelsNoAccount(t *testing.T) {
	h, _ := policyFixture(t)
	names, acct := h.fetchGlobalModels()
	if names != nil || acct != nil {
		t.Errorf("无 global 账号应返回 nil/nil，got %v/%v", names, acct)
	}
}

// TestUsageCreditTotal usage 提取：缺 usage / 无 total / 合法。
func TestUsageCreditTotal(t *testing.T) {
	if _, _, ok := usageCreditTotal(map[string]any{}); ok {
		t.Error("缺 usage 应 false")
	}
	if _, _, ok := usageCreditTotal(map[string]any{"usage": map[string]any{"credit": 1.5, "total_tokens": 0.0}}); ok {
		t.Error("total<=0 应 false")
	}
	credit, total, ok := usageCreditTotal(map[string]any{"usage": map[string]any{"credit": 1.5, "total_tokens": 100.0}})
	if !ok || credit != 1.5 || total != 100 {
		t.Errorf("合法 = %v/%d/%v", credit, total, ok)
	}
}

// timeoutNetErr 模拟传输层超时错误。
type timeoutNetErr struct{}

func (timeoutNetErr) Error() string   { return "timeout" }
func (timeoutNetErr) Timeout() bool   { return true }
func (timeoutNetErr) Temporary() bool { return true }

// TestIsUpstreamTimeout 超时/取消三态判定。
func TestIsUpstreamTimeoutBranches(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		clientGone bool
		want       bool
	}{
		{"nil", nil, false, false},
		{"net 超时", timeoutNetErr{}, false, true},
		{"deadline", context.DeadlineExceeded, false, true},
		{"取消但客户端还在", context.Canceled, false, true},
		{"客户端断连的取消", context.Canceled, true, false},
		{"普通错误", errors.New("other"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUpstreamTimeout(tc.err, tc.clientGone); got != tc.want {
				t.Errorf("isUpstreamTimeout = %v，应为 %v", got, tc.want)
			}
		})
	}
}

// TestChatUsageWithoutTotal 上游 usage 缺 total_tokens 时按 prompt+completion 合成。
func TestChatUsageWithoutTotal(t *testing.T) {
	const sse = "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4}}\n\n" +
		"data: [DONE]\n\n"
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sse, true })
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	st, _ := p.Status("u1")
	if st.TokenUsage.TotalTokens != 7 {
		t.Errorf("total_tokens = %d，应为 7（prompt+completion 合成）", st.TokenUsage.TotalTokens)
	}
}

// TestChatEmptyModel 请求未带 model：仍走 CN 路由并正常转发（delta.Model 回退到原值）。
func TestChatEmptyModel(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestPromptModeBranches append 模式插入网关提示词；passthrough 降级期改用
// Degraded 提示词直达。
func TestPromptModeBranches(t *testing.T) {
	newHandler := func(t *testing.T, mode string) (*Handler, *pool.Pool) {
		up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
		p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
		return NewHandler(Config{Pool: p, Upstream: up, PromptMode: mode, PromptText: "网关提示词"}), p
	}
	body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`

	t.Run("append 模式", func(t *testing.T) {
		h, p := newHandler(t, "append")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
		}
		if st, _ := p.Status("u1"); st.TokenUsage.RequestCount != 1 {
			t.Errorf("请求未走完: %+v", st.TokenUsage)
		}
	})

	t.Run("passthrough 降级期", func(t *testing.T) {
		h, p := newHandler(t, "passthrough")
		h.degrade.Trigger()
		if !h.degrade.Active() {
			t.Fatal("降级期应激活")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
		}
		if st, _ := p.Status("u1"); st.TokenUsage.RequestCount != 1 {
			t.Errorf("请求未走完: %+v", st.TokenUsage)
		}
	})
}
