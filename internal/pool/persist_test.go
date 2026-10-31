// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestRestoreNoStoreOrNoPath 无 Store 或无 stateFp 时 RestoreFromSnapshot 静默返回。
func TestRestoreNoStoreOrNoPath(t *testing.T) {
	p := New("")
	p.RestoreFromSnapshot() // store nil + stateFp 空
	p.SetStore(&memStore{})
	p.RestoreFromSnapshot() // store 有但 stateFp 空

	dir := t.TempDir()
	p2 := New(filepath.Join(dir, "state.json"))
	defer p2.Close()
	p2.RestoreFromSnapshot() // stateFp 有但 store nil
}

// TestRestoreSnapshotWithoutSavedAt 快照无 saved_at（或坏 JSON）→ 本地优先，不采用快照。
func TestRestoreSnapshotWithoutSavedAt(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")
	if err := os.WriteFile(fp, []byte(`{"accounts":{"u1":{"credits":1}}}`), 0o600); err != nil {
		t.Fatalf("写本地状态: %v", err)
	}
	p := New(fp)
	defer p.Close()
	for _, snap := range [][]byte{
		[]byte(`{"accounts":{"u1":{"credits":9}}}`),
		[]byte(`not-json`),
	} {
		p.SetStore(&memStore{loadData: snap, loadOK: true})
		p.RestoreFromSnapshot()
		if e := p.byUID["u1"]; e == nil || e.credits != 1 {
			t.Errorf("快照无 saved_at 应采用本地，got %+v", e)
		}
	}
}

// TestRestoreLocalUnavailableAdoptsSnapshot 本地 state.json 不可用 → 采用快照
// （快照是唯一运行态来源），并置 dirty 以便物化回本地。
func TestRestoreLocalUnavailableAdoptsSnapshot(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")
	p := New(fp)
	defer p.Close()
	snap := []byte(`{"accounts":{"u1":{"credits":9}},"saved_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}`)
	p.SetStore(&memStore{loadData: snap, loadOK: true})
	p.RestoreFromSnapshot()

	e := p.byUID["u1"]
	if e == nil || e.credits != 9 {
		t.Fatalf("本地不可用时应采用快照，byUID=%v", p.byUID)
	}
	if !p.dirty.Load() {
		t.Error("采用快照后应置 dirty（物化回本地）")
	}
}

// TestLoadAppliesAccountFilters 反序列化恢复时的字段映射与惰性清洗：
// 旧 err_count 迁移、到期快照钳制、熔断/降权/模型冷却/成本账本过滤。
func TestLoadAppliesAccountFilters(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")
	now := time.Now()
	future := now.Add(time.Hour).UTC().Format(time.RFC3339)
	past := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	twoDaysAgo := now.Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	state := `{"accounts":{
		"u1":{
			"credits":10,"credits_total":20,"disabled":true,"paused":true,"reason":"x",
			"cool_kind":1,"until":"` + future + `","success_count":3,
			"err_total":2,"err_count":5,"soft_streak":2,"session_dead_fails":1,"consecutive_fails":4,
			"credits_expiring":15,"credits_earliest_expiry":"` + future + `","credits_earliest_remaining":99,
			"breaker_until":"` + future + `","retry_count":2,"degrade_until":"` + future + `",
			"model_cooldowns":{"keep":{"until":"` + future + `","reason":"6004"},
				"past":{"until":"` + past + `"},"zero":{}},
			"model_costs":{"keep":{"cost_per_1k":1.5,"last_seen":"` + future + `","samples":2},
				"old":{"cost_per_1k":1,"last_seen":"` + twoDaysAgo + `"},
				"zero":{"cost_per_1k":1},
				"neg":{"cost_per_1k":-1,"last_seen":"` + future + `"}}
		},
		"u2":{"credits":5,"credits_expiring":-3,
			"credits_earliest_expiry":"` + past + `","credits_earliest_remaining":2},
		"u3":{"credits":5,"breaker_until":"` + past + `","retry_count":9,"degrade_until":"` + past + `"}
	}}`
	if err := os.WriteFile(fp, []byte(state), 0o600); err != nil {
		t.Fatalf("写状态: %v", err)
	}
	p := New(fp)
	defer p.Close()

	e := p.byUID["u1"]
	if e == nil {
		t.Fatal("u1 未恢复")
	}
	if e.credits != 10 || e.creditsTotal != 20 || !e.disabled || !e.paused || e.reason != "x" ||
		e.coolKind != CoolSoft || e.until.IsZero() || e.successCount != 3 {
		t.Errorf("基础字段 = %+v", e)
	}
	if e.errTotal != 5 {
		t.Errorf("err_count 应迁移为 err_total 的较大者，got %d", e.errTotal)
	}
	if e.softStreak != 2 || e.sessionDeadFails != 1 || e.consecutiveFails != 4 {
		t.Errorf("运行态计数 = %+v", e)
	}
	if e.creditsExpiring != 10 {
		t.Errorf("creditsExpiring 应钳到 credits，got %d", e.creditsExpiring)
	}
	if e.creditsEarliestRemaining != 0 || !e.creditsEarliestExpiry.IsZero() {
		t.Errorf("超出余额的最早到期批次应清洗: %+v", e)
	}
	if e.breakerUntil.IsZero() || e.retryCount != 2 {
		t.Errorf("未过期熔断应恢复: %+v", e)
	}
	if e.degradeUntil.IsZero() {
		t.Error("未过期降权应恢复")
	}
	if len(e.modelCooldowns) != 1 || e.modelCooldowns["keep"].Reason != "6004" {
		t.Errorf("modelCooldowns = %+v", e.modelCooldowns)
	}
	if len(e.modelCost) != 1 {
		t.Errorf("modelCost 应只留有效观测: %+v", e.modelCost)
	}
	if mc := e.modelCost["keep"]; mc.CostPer1k != 1.5 || mc.Samples != 2 {
		t.Errorf("modelCost keep = %+v", mc)
	}

	u2 := p.byUID["u2"]
	if u2.creditsExpiring != 0 {
		t.Errorf("负 creditsExpiring 应归零，got %d", u2.creditsExpiring)
	}
	if !u2.creditsEarliestExpiry.IsZero() || u2.creditsEarliestRemaining != 0 {
		t.Errorf("过期的最早到期批次应清洗: %+v", u2)
	}

	u3 := p.byUID["u3"]
	if !u3.breakerUntil.IsZero() || u3.retryCount != 0 {
		t.Errorf("过期熔断不应恢复: %+v", u3)
	}
	if !u3.degradeUntil.IsZero() {
		t.Error("过期降权不应恢复")
	}
}

// TestLoadCorruptStateFile 损坏的 state.json：load 静默跳过，不 panic。
func TestLoadCorruptStateFile(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(fp, []byte(`{not-json`), 0o600); err != nil {
		t.Fatalf("写坏文件: %v", err)
	}
	p := New(fp)
	defer p.Close()
	if len(p.byUID) != 0 {
		t.Errorf("损坏文件不应恢复账号: %v", p.byUID)
	}
	p.Add(&auth.Auth{UID: "u1"})
	p.Flush()
	if _, err := os.Stat(fp); err != nil {
		t.Errorf("后续落盘应覆盖坏文件: %v", err)
	}
}

// TestSaveRenameFailureIsSilent stateFp 指向目录：tmp 写成功但 rename 失败，静默记失败。
func TestSaveRenameFailureIsSilent(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")
	if err := os.Mkdir(fp, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	p := New(fp)
	defer p.Close()
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCredits("u1", 1, 0) // 置 dirty，Flush 才会走 saveLocked
	p.Flush()
	if p.persistFails == 0 {
		t.Error("rename 失败应计数 persistFails")
	}
}

// TestNotePersistFailThrottle 落盘失败计数与成功后归零。
func TestNotePersistFailThrottle(t *testing.T) {
	p := New("")
	p.notePersistFail(errors.New("first"))
	if p.persistFails != 1 {
		t.Errorf("首败计数 = %d", p.persistFails)
	}
	p.persistFails = persistLogEvery - 1
	p.notePersistFail(errors.New("retry log"))
	if p.persistFails != persistLogEvery {
		t.Errorf("计数 = %d，应为 %d", p.persistFails, persistLogEvery)
	}
	p.notePersistFail(errors.New("silent"))
	if p.persistFails != persistLogEvery+1 {
		t.Errorf("计数 = %d", p.persistFails)
	}

	// 成功后归零（走 saveLocked 的恢复分支）。
	p.stateFp = filepath.Join(t.TempDir(), "state.json")
	p.persistFails = 3
	p.saveLocked()
	if p.persistFails != 0 {
		t.Errorf("成功落盘后 persistFails 应归零，got %d", p.persistFails)
	}
}

// TestStateOverviewFilters 落盘视图：未过期的熔断/降权/模型冷却/成本才写。
func TestStateOverviewFilters(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	now := time.Now()
	e := p.byUID["u1"]
	e.credits = 5
	e.creditsExpiring = 6 // 脏值：应原样写入视图（清洗在 load 侧）
	e.breakerUntil = now.Add(time.Hour)
	e.retryCount = 2
	e.degradeUntil = now.Add(-time.Hour) // 过期：不写
	e.modelCooldowns = map[string]modelCooldown{
		"keep": {Until: now.Add(time.Hour), Reason: "6004"},
		"past": {Until: now.Add(-time.Hour)},
		"zero": {},
	}
	e.modelCost = map[string]modelCostEntry{
		"keep": {CostPer1k: 1.5, LastSeen: now, Samples: 2},
		"old":  {CostPer1k: 1, LastSeen: now.Add(-modelCostTTL - time.Hour)},
		"zero": {},
	}
	sf := p.stateOverviewLocked()
	s := sf.Accounts["u1"]
	if s.Credits != 5 || s.CreditsExpiring != 6 {
		t.Errorf("基础字段 = %+v", s)
	}
	if s.BreakerUntil == nil || s.RetryCount != 2 {
		t.Errorf("未过期熔断应写入: %+v", s)
	}
	if s.DegradeUntil != nil {
		t.Error("过期降权不应写入")
	}
	e.degradeUntil = now.Add(time.Hour)
	sf = p.stateOverviewLocked()
	if sf.Accounts["u1"].DegradeUntil == nil {
		t.Error("未过期降权应写入")
	}
	if len(s.ModelCooldowns) != 1 {
		t.Errorf("modelCooldowns = %+v", s.ModelCooldowns)
	}
	if len(s.ModelCosts) != 1 {
		t.Errorf("modelCosts = %+v", s.ModelCosts)
	}

	// 视图可被 JSON 序列化（omitempty 指针语义正确）。
	if _, err := json.Marshal(sf); err != nil {
		t.Errorf("序列化: %v", err)
	}
}

// TestSetNotePersists 面板备注存进 state.json：重新导入同一个号（Add 换凭证）不丢、重启后还在；
// 空串清除后不再落盘；未知 uid 返回 false。
func TestSetNotePersists(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "state.json")
	p := New(fp)
	p.Add(&auth.Auth{UID: "u1", Nickname: "一号"})
	if !p.SetNote("u1", "主力号") || p.SetNote("no-such", "x") {
		t.Fatal("已有账号应改成功，未知 uid 应返回 false")
	}
	p.Add(&auth.Auth{UID: "u1", Nickname: "一号", AccessToken: "new"})
	p.Flush()
	p2 := New(fp)
	p2.Add(&auth.Auth{UID: "u1"})
	if st, _ := p2.Status("u1"); st.Note != "主力号" {
		t.Fatalf("重启后备注 = %q，应为 主力号", st.Note)
	}
	p2.SetNote("u1", "")
	p2.Flush()
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	var sf stateFile
	if err := json.Unmarshal(raw, &sf); err != nil || sf.Accounts["u1"].Note != "" {
		t.Fatalf("清除后 state.json 里的备注 = %q，err=%v", sf.Accounts["u1"].Note, err)
	}
	if p2.dirty.Load() {
		t.Fatal("Flush 后不应还有没落盘的改动")
	}
	p2.SetNote("u1", "")
	if p2.dirty.Load() {
		t.Fatal("备注没变不应标记要落盘")
	}
}
