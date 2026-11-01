// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"reflect"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestAuthByUIDVariants 存在返回同一凭证指针，不存在返回 nil。
func TestAuthByUIDVariants(t *testing.T) {
	p := New("")
	a := &auth.Auth{UID: "u1"}
	p.Add(a)
	if got := p.AuthByUID("u1"); got != a {
		t.Errorf("AuthByUID 应返回同一指针")
	}
	if got := p.AuthByUID("missing"); got != nil {
		t.Errorf("不存在的 uid 应返回 nil，got %+v", got)
	}
}

// TestAvailableUIDsVariants 只列 healthy 且未占满在途的账号；空池返回空切片。
func TestAvailableUIDsVariants(t *testing.T) {
	p := New("")
	for _, uid := range []string{"u1", "u2", "u3", "u4"} {
		p.Add(&auth.Auth{UID: uid})
	}
	p.Disable("u2", "test")
	p.Pause("u3")
	p.Cooldown("u4", CoolSoft, time.Hour, "429")
	if got := p.AvailableUIDs(); !reflect.DeepEqual(got, []string{"u1"}) {
		t.Errorf("AvailableUIDs = %v，应为 [u1]", got)
	}

	p.SetMaxInFlight(1)
	if !p.Acquire("u1") {
		t.Fatal("名额应可用")
	}
	if got := p.AvailableUIDs(); len(got) != 0 {
		t.Errorf("在途满的账号不应列出，got %v", got)
	}

	empty := New("")
	if got := empty.AvailableUIDs(); got == nil || len(got) != 0 {
		t.Errorf("空池应返回非 nil 空切片，got %#v", got)
	}
}

// TestAvailableUIDsForModel 模型维度：目录回调拒绝的账号不列出；空模型等价全量。
func TestAvailableUIDsForModel(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.SetModelServes(func(uid, realm string, enterprise bool, model string) bool {
		return uid != "u1" || model != "blocked"
	})
	if got := p.AvailableUIDsForModel("blocked"); !reflect.DeepEqual(got, []string{"u2"}) {
		t.Errorf("blocked 模型 = %v，应为 [u2]", got)
	}
	if got := p.AvailableUIDsForModel("ok"); !reflect.DeepEqual(got, []string{"u1", "u2"}) {
		t.Errorf("ok 模型 = %v", got)
	}
	if got := p.AvailableUIDsForModel(""); !reflect.DeepEqual(got, []string{"u1", "u2"}) {
		t.Errorf("空模型 = %v", got)
	}
}

// TestPickByUIDVariants 粘性命中校验：不存在/禁用/暂停/冷却/在途满都返回 nil，
// 健康号返回凭证并推进选号序号。
func TestPickByUIDVariants(t *testing.T) {
	p := New("")
	a := &auth.Auth{UID: "u1"}
	p.Add(a)
	if p.PickByUID("missing") != nil {
		t.Error("不存在应返回 nil")
	}
	if got := p.PickByUID("u1"); got != a {
		t.Fatalf("健康号应返回凭证，got %+v", got)
	}
	e := p.byUID["u1"]
	if e.lastUsed.IsZero() || e.usedSeq == 0 {
		t.Errorf("应记录 lastUsed/usedSeq: %+v", e)
	}

	p.Disable("u1", "test")
	if p.PickByUID("u1") != nil {
		t.Error("禁用应返回 nil")
	}
	p.Revive("u1")
	p.Pause("u1")
	if p.PickByUID("u1") != nil {
		t.Error("暂停应返回 nil")
	}
	p.Resume("u1")
	p.Cooldown("u1", CoolSoft, time.Hour, "429")
	if p.PickByUID("u1") != nil {
		t.Error("冷却中应返回 nil")
	}
	p.Revive("u1")
	p.SetMaxInFlight(1)
	p.Acquire("u1")
	if p.PickByUID("u1") != nil {
		t.Error("在途满应返回 nil")
	}
}

// TestPickByUIDForModel 模型级冷却的号只在触发模型上返回 nil（其他模型照常）。
func TestPickByUIDForModel(t *testing.T) {
	p := New("")
	a := &auth.Auth{UID: "u1"}
	p.Add(a)
	p.CooldownSoftForModel("u1", time.Hour, time.Now(), "m1", "6004 model rate limit")
	if p.PickByUIDForModel("u1", "m1") != nil {
		t.Error("该模型被限流的号应返回 nil")
	}
	if got := p.PickByUIDForModel("u1", "m2"); got != a {
		t.Errorf("其他模型应可用，got %+v", got)
	}
	if p.PickByUIDForModel("missing", "m2") != nil {
		t.Error("不存在应返回 nil")
	}
}

// TestServableForRealm 各 realm 的可服务判定：realm 过滤、冷却、在途满、模型豁免。
func TestServableForRealm(t *testing.T) {
	empty := New("")
	for _, realm := range []string{"", "cn", "global"} {
		if empty.ServableForRealm(realm) {
			t.Errorf("空池 realm=%q 应为 false", realm)
		}
	}

	p := New("")
	p.Add(&auth.Auth{UID: "cn1", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai", AccessToken: "at"})
	if !p.ServableForRealm("") || !p.ServableForRealm("cn") || !p.ServableForRealm("global") {
		t.Error("两域健康号应都可服务")
	}

	// 只有国内版号：问国际版时全被跳过，不可服务（不依赖 map 遍历顺序）。
	cnOnly := New("")
	cnOnly.Add(&auth.Auth{UID: "cn2", Domain: "www.codebuddy.cn"})
	if cnOnly.ServableForRealm("global") {
		t.Error("只有国内版号时国际版应不可服务")
	}

	// 仅冷却号：不可服务。
	p2 := New("")
	p2.Add(&auth.Auth{UID: "c1"})
	p2.Cooldown("c1", CoolSoft, time.Hour, "429")
	if p2.ServableForRealm("") {
		t.Error("冷却号不可服务")
	}

	// 在途满：不可服务（即使 healthy）。
	p3 := New("")
	p3.Add(&auth.Auth{UID: "f1"})
	p3.SetMaxInFlight(1)
	p3.Acquire("f1")
	if p3.ServableForRealm("") {
		t.Error("在途满不可服务")
	}

	// 模型级豁免形态：账号级健康但存在未过期 6004 冷却 → 仍可服务。
	p4 := New("")
	p4.Add(&auth.Auth{UID: "m1"})
	e := p4.byUID["m1"]
	e.modelCooldowns = map[string]modelCooldown{"x": {Until: time.Now().Add(time.Hour), Reason: "6004"}}
	if !p4.ServableForRealm("") {
		t.Error("模型级豁免形态应可服务")
	}
}

// TestCountsDetailedForRealm 分类计数：realm 过滤、paused 单列、在途满子集。
func TestCountsDetailedForRealm(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "cn-ok", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "cn-cool", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "cn-dis", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "cn-pause", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "g-ok", Domain: "www.workbuddy.ai", AccessToken: "at"})
	p.Add(&auth.Auth{UID: "g-full", Domain: "www.workbuddy.ai", AccessToken: "at"})
	p.Cooldown("cn-cool", CoolSoft, time.Hour, "429")
	p.Disable("cn-dis", "test")
	p.Pause("cn-pause")
	p.SetMaxInFlight(1)
	p.Acquire("g-full")

	total, healthy, cooling, disabled, inFlightFull := p.CountsDetailed()
	if total != 6 || healthy != 3 || cooling != 1 || disabled != 2 || inFlightFull != 1 {
		t.Errorf("CountsDetailed = %d/%d/%d/%d/%d，应为 6/3/1/2/1",
			total, healthy, cooling, disabled, inFlightFull)
	}
	total, healthy, cooling, disabled, paused, blocked, inFlightFull := p.CountsDetailedWithPaused()
	if total != 6 || healthy != 3 || cooling != 1 || disabled != 1 || paused != 1 || blocked != 0 || inFlightFull != 1 {
		t.Errorf("CountsDetailedWithPaused = %d/%d/%d/%d/%d/%d/%d，应为 6/3/1/1/1/0/1",
			total, healthy, cooling, disabled, paused, blocked, inFlightFull)
	}

	total, healthy, cooling, disabled, inFlightFull = p.CountsDetailedForRealm("cn")
	if total != 4 || healthy != 1 || cooling != 1 || disabled != 2 || inFlightFull != 0 {
		t.Errorf("cn 计数 = %d/%d/%d/%d/%d", total, healthy, cooling, disabled, inFlightFull)
	}
	total, healthy, _, _, inFlightFull = p.CountsDetailedForRealm("global")
	if total != 2 || healthy != 2 || inFlightFull != 1 {
		t.Errorf("global 计数 = %d/%d/%d，应为 2/2/1", total, healthy, inFlightFull)
	}
}

// TestNoteModelCostBranches 成本账本与余额扣减的边界。
func TestNoteModelCostBranches(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCredits("u1", 10, 0)
	p.NoteModelCost("", "m", 1, 10)
	p.NoteModelCost("u1", "", 1, 10)
	p.NoteModelCost("u1", "m", 1, 0)
	p.NoteModelCost("missing", "m", 1, 10)
	if p.byUID["u1"].modelCost != nil {
		t.Error("无效入参不应写账本")
	}

	// 免费请求（credit=0）只记账本，不动余额。
	p.NoteModelCost("u1", "free", 0, 1000)
	e := p.byUID["u1"]
	if e.credits != 10 {
		t.Errorf("免费请求不应扣余额，got %d", e.credits)
	}
	if got := e.modelCost["free"]; got.Samples != 1 || got.CostPer1k != 0 {
		t.Errorf("首条观测 = %+v", got)
	}

	// 扣费超过余额：钳到 0；到期批次同步清零。
	e.creditsExpiring = 5
	e.creditsEarliestRemaining = 5
	e.creditsEarliestExpiry = time.Now().Add(time.Hour)
	p.NoteModelCost("u1", "paid", 100, 100)
	if e.credits != 0 {
		t.Errorf("扣穿应钳 0，got %d", e.credits)
	}
	if e.creditsExpiring != 0 || e.creditsEarliestRemaining != 0 || !e.creditsEarliestExpiry.IsZero() {
		t.Errorf("到期批次应清零: %+v/%v/%v", e.creditsExpiring, e.creditsEarliestRemaining, e.creditsEarliestExpiry)
	}

	// 负 credit：单价钳 0，余额不动。
	p.SetCredits("u1", 10, 0)
	p.NoteModelCost("u1", "neg", -5, 100)
	if e.modelCost["neg"].CostPer1k != 0 || e.credits != 10 {
		t.Errorf("负 credit 应钳 0 且不扣余额: %+v/%d", e.modelCost["neg"], e.credits)
	}

	// 第二次观测走 EMA：样本 +1、单价平滑；从 0 变正触发“限免结束”。
	e.modelCost["tier"] = modelCostEntry{CostPer1k: 0, LastSeen: time.Now(), Samples: 1}
	p.NoteModelCost("u1", "tier", 1, 1000) // per1k = 1
	if got := e.modelCost["tier"]; got.Samples != 2 || got.CostPer1k <= 0 {
		t.Errorf("EMA 更新 = %+v", got)
	}
}

// TestRecordTokenUsageBranches 用量累计：未知 uid、各字段有无、负值忽略、吞吐回空。
func TestRecordTokenUsageBranches(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.RecordTokenUsage("missing", TokenUsageDelta{HasPromptTokens: true, PromptTokens: 1})

	p.RecordTokenUsage("u1", TokenUsageDelta{
		Model: "m1", HasPromptTokens: true, PromptTokens: 10,
		HasCompletionTokens: true, CompletionTokens: 5,
		HasTotalTokens: true, TotalTokens: 15,
		HasLatencyMs: true, LatencyMs: 200,
		HasTokensPerSecond: true, TokensPerSecond: 12.5,
	})
	e := p.byUID["u1"]
	u := e.tokenUsage
	if u.RequestCount != 1 || u.UsageCount != 1 || u.PromptTokens != 10 || u.CompletionTokens != 5 ||
		u.TotalTokens != 15 || u.LastLatencyMs != 200 || u.LastModel != "m1" {
		t.Errorf("首次累计 = %+v", u)
	}
	if u.LastTokensPerSecond == nil || *u.LastTokensPerSecond != 12.5 {
		t.Errorf("吞吐 = %v", u.LastTokensPerSecond)
	}

	// 负值不计入 known；不带吞吐时清空上一次。
	p.RecordTokenUsage("u1", TokenUsageDelta{
		HasPromptTokens: true, PromptTokens: -1,
		HasTotalTokens: true, TotalTokens: -1,
		HasLatencyMs: true, LatencyMs: -1,
	})
	if u := e.tokenUsage; u.RequestCount != 2 || u.UsageCount != 1 || u.LastLatencyMs != 200 {
		t.Errorf("负值应忽略: %+v", u)
	}
	if e.tokenUsage.LastTokensPerSecond != nil {
		t.Error("缺少吞吐时上一次速度应清空")
	}
}

// TestReviveBranches Revive 不存在返回 false；存在清空全部惩罚状态。
func TestReviveBranches(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	if p.Revive("missing") {
		t.Error("不存在应返回 false")
	}
	e := p.byUID["u1"]
	e.disabled, e.paused = true, true
	e.until = time.Now().Add(time.Hour)
	e.coolKind = CoolSoft
	e.reason = "x"
	e.softStreak = 3
	e.modelCooldowns = map[string]modelCooldown{"m": {Until: time.Now().Add(time.Hour)}}
	e.sessionDeadFails = 2
	e.fails = 4
	e.retryCount = 2
	e.breakerUntil = time.Now().Add(time.Hour)
	if !p.Revive("u1") {
		t.Fatal("存在应返回 true")
	}
	if e.disabled || e.paused || !e.until.IsZero() || e.coolKind != 0 || e.reason != "" ||
		e.softStreak != 0 || e.modelCooldowns != nil || e.sessionDeadFails != 0 ||
		e.fails != 0 || e.retryCount != 0 || !e.breakerUntil.IsZero() {
		t.Errorf("Revive 后仍有惩罚状态: %+v", e)
	}
}

// TestStatusCoolingBreakerOnly 只有熔断期时 Cooling 为 true 且 CoolKind=breaker。
func TestStatusCoolingBreakerOnly(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	e := p.byUID["u1"]
	e.breakerUntil = time.Now().Add(time.Hour)
	st, ok := p.Status("u1")
	if !ok || !st.Cooling || st.CoolKind != "breaker" || st.CoolRemaining <= 0 {
		t.Errorf("熔断期状态 = %+v", st)
	}

	e.breakerUntil = time.Time{}
	e.until = time.Now().Add(time.Hour)
	e.coolKind = CoolSoft
	st, _ = p.Status("u1")
	if !st.Cooling || st.CoolKind != "soft_rate" {
		t.Errorf("软冷却状态 = %+v", st)
	}
}

// TestRateLimitedModelsStatus 限额台账：未过期条目按 kind 分类；过期/空表不输出。
func TestRateLimitedModelsStatus(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	e := p.byUID["u1"]
	until := time.Now().Add(time.Hour)
	e.modelCooldowns = map[string]modelCooldown{
		"a": {Until: until, Reason: "6004 model rate limit", ResetAt: until.Add(time.Hour)},
		"b": {Until: until, Reason: "11102 model not available"},
		"c": {Until: time.Now().Add(-time.Hour), Reason: "6004"},
	}
	st, _ := p.Status("u1")
	rows := st.RateLimitedModels
	if len(rows) != 2 || rows[0].Model != "a" || rows[0].Kind != "rate_limit" ||
		rows[0].ResetAt.IsZero() || rows[1].Model != "b" || rows[1].Kind != "model_unavailable" {
		t.Fatalf("台账 = %+v", rows)
	}

	e.modelCooldowns = nil
	st, _ = p.Status("u1")
	if st.RateLimitedModels != nil {
		t.Errorf("空表应为 nil，got %+v", st.RateLimitedModels)
	}
}

// TestCoolKindString 冷却类型字符串。
func TestCoolKindString(t *testing.T) {
	cases := map[CoolKind]string{CoolHard: "hard_credit", CoolSoft: "soft_rate", CoolKind(99): "unknown"}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("CoolKind(%d) = %q，应为 %q", int(k), got, want)
		}
	}
}

// TestModelCostOf modelCostOf 的缺失/过期/有效分支。
func TestModelCostOf(t *testing.T) {
	e := &entry{}
	now := time.Now()
	if _, ok := e.modelCostOf("m", now); ok {
		t.Error("空账本应 false")
	}
	e.modelCost = map[string]modelCostEntry{
		"fresh": {CostPer1k: 1, LastSeen: now.Add(-time.Minute)},
		"old":   {CostPer1k: 1, LastSeen: now.Add(-modelCostTTL - time.Minute)},
		"zero":  {CostPer1k: 1},
	}
	if _, ok := e.modelCostOf("", now); ok {
		t.Error("空模型名应 false")
	}
	if _, ok := e.modelCostOf("missing", now); ok {
		t.Error("未记录应 false")
	}
	if _, ok := e.modelCostOf("old", now); ok {
		t.Error("过期应 false")
	}
	if _, ok := e.modelCostOf("zero", now); ok {
		t.Error("零时间应 false")
	}
	if mc, ok := e.modelCostOf("fresh", now); !ok || mc.CostPer1k != 1 {
		t.Errorf("有效观测 = %+v/%v", mc, ok)
	}
}

// TestHealthyBranches healthy 的熔断/降权分支。
func TestHealthyBranches(t *testing.T) {
	now := time.Now()
	e := &entry{}
	if !e.healthy(now) {
		t.Error("干净账号应 healthy")
	}
	e.until = now.Add(time.Hour)
	if e.healthy(now) {
		t.Error("冷却中不应 healthy")
	}
	e.until = time.Time{}
	e.breakerUntil = now.Add(time.Hour)
	if e.healthy(now) {
		t.Error("熔断中不应 healthy")
	}
	e.breakerUntil = time.Time{}
	e.degradeUntil = now.Add(time.Hour)
	if e.healthy(now) {
		t.Error("降权中不应 healthy")
	}
}

// TestModelExemptBranches 模型豁免形态判定。
func TestModelExemptBranches(t *testing.T) {
	now := time.Now()
	e := &entry{}
	if e.modelExempt() {
		t.Error("空表不应豁免")
	}
	e.modelCooldowns = map[string]modelCooldown{
		"audit": {Until: now.Add(time.Hour), AuditOnly: true},
		"zero":  {},
	}
	if e.modelExempt() {
		t.Error("审计/零截止条目不应豁免")
	}
	e.modelCooldowns["real"] = modelCooldown{Until: now.Add(time.Hour)}
	if !e.modelExempt() {
		t.Error("有效 6004 条目应豁免")
	}
	for _, mut := range []func(){
		func() { e.disabled = true },
		func() { e.disabled = false; e.paused = true },
		func() { e.paused = false; e.until = now.Add(time.Hour) },
		func() { e.until = time.Time{}; e.degradeUntil = now.Add(time.Hour) },
		func() { e.degradeUntil = time.Time{}; e.breakerUntil = now.Add(time.Hour) },
	} {
		mut()
		if e.modelExempt() {
			t.Errorf("惩罚态不应豁免: %+v", e)
		}
	}
}

// TestPruneExpiredModelCosts 惰性清理过期成本观测。
func TestPruneExpiredModelCosts(t *testing.T) {
	e := &entry{}
	e.pruneExpiredModelCosts(time.Now()) // 空表 no-op

	now := time.Now()
	e.modelCost = map[string]modelCostEntry{
		"fresh": {LastSeen: now},
		"old":   {LastSeen: now.Add(-modelCostTTL - time.Minute)},
		"zero":  {},
	}
	e.pruneExpiredModelCosts(now)
	if len(e.modelCost) != 1 {
		t.Errorf("应只留 fresh，got %+v", e.modelCost)
	}
	if _, ok := e.modelCost["fresh"]; !ok {
		t.Error("fresh 不应被删")
	}
}

// TestExpiryBranches expiry 取仍在生效的最早截止。
func TestExpiryBranches(t *testing.T) {
	now := time.Now()
	e := &entry{}
	if !e.expiry(now).IsZero() {
		t.Error("无冷却应为零值")
	}
	e.until = now.Add(3 * time.Hour)
	e.breakerUntil = now.Add(time.Hour)
	e.degradeUntil = now.Add(2 * time.Hour)
	if got := e.expiry(now); !got.Equal(e.breakerUntil) {
		t.Errorf("应取最早截止 %v，got %v", e.breakerUntil, got)
	}
	e.breakerUntil = time.Time{}
	if got := e.expiry(now); !got.Equal(e.degradeUntil) {
		t.Errorf("应取降权截止 %v，got %v", e.degradeUntil, got)
	}
	e.degradeUntil = time.Time{}
	if got := e.expiry(now); !got.Equal(e.until) {
		t.Errorf("应取冷却截止 %v，got %v", e.until, got)
	}
	e.until = now.Add(-time.Hour) // 已过期不计
	e.breakerUntil = now.Add(time.Hour)
	if got := e.expiry(now); !got.Equal(e.breakerUntil) {
		t.Errorf("过期项应跳过，got %v", got)
	}
}

// TestSessionDeadThresholdBehavior 用导出的阈值驱动连续 12153 行为。
func TestSessionDeadThresholdBehavior(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	n := SessionDeadThreshold()
	if n < 1 {
		t.Fatalf("阈值 = %d，应 >= 1", n)
	}
	for i := 1; i < n; i++ {
		if p.NoteSessionDead("u1") {
			t.Fatalf("第 %d 次（阈值 %d）不应禁用", i, n)
		}
	}
	if !p.NoteSessionDead("u1") {
		t.Fatalf("第 %d 次应禁用", n)
	}
}
