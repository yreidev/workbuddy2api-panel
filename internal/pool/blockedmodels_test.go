// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const forbidden = "11158 model not allowed for account type"

// TestBlockModelForever 11158 加进名单：一直不派这个模型（不到期），别的模型照常；重复加不动；不在池里的号忽略。
func TestBlockModelForever(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"})
	p.BlockModelForever("g1", "deepseek-v4.1-flash", forbidden)
	p.BlockModelForever("g1", "DeepSeek-V4.1-Flash", "again") // 已经挡住了
	p.BlockModelForever("g1", "", forbidden)
	p.BlockModelForever("gone", "deepseek-v4.1-flash", forbidden)

	st, _ := p.Status("g1")
	if len(st.BlockedModels) != 1 || st.BlockedModels[0].Model != "deepseek-v4.1-flash" || !st.BlockedModels[0].Auto || st.BlockedModels[0].Reason != forbidden {
		t.Fatalf("名单应只有自动加的一项: %+v", st.BlockedModels)
	}
	p.mu.Lock()
	p.byUID["g1"].blockedModels[0].At = time.Now().Add(-30 * 24 * time.Hour) // 一个月前加的也照样挡
	p.mu.Unlock()
	if a := p.PickExcludingForRealm(nil, "deepseek-v4.1-flash", "global"); a != nil {
		t.Fatalf("名单里的模型不应派给这个号，选中了 %s", a.UID)
	}
	if a := p.PickExcludingForRealm(nil, "glm-5.2", "global"); a == nil || a.UID != "g1" {
		t.Fatalf("别的模型照常派: %v", a)
	}
	if uids := p.AvailableUIDsForModelRealm("deepseek-v4.1-flash", ""); len(uids) != 0 {
		t.Fatalf("粘性候选也不应有这个号: %v", uids)
	}
}

// TestBlockedModelsBlockStatus 能接这个模型的号全被名单挡住：报模型级阻塞，原因取上游拒绝的那项，没有解封时刻；
// 另一域、手动加的照常计数。
func TestBlockedModelsBlockStatus(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "cn1", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"})
	p.Add(&auth.Auth{UID: "g2", Domain: "www.workbuddy.ai"})
	p.SetBlockedModels("g1", []string{"gpt-*"})
	p.BlockModelForever("g2", "gpt-5.6-sol", forbidden)

	st := p.ModelBlockedForRealm("gpt-5.6-sol", "global")
	if !st.Blocked || st.Count != 2 || st.Reason != forbidden || !st.Until.IsZero() {
		t.Fatalf("global 应全被挡住、原因 11158、没有解封时刻: %+v", st)
	}
	if st := p.ModelBlockedForRealm("gpt-5.6-sol", ""); st.Blocked {
		t.Fatalf("cn1 还能接，合池不算全挡住: %+v", st)
	}
	// 自动加的，用这个模型成功了就解除；手动加的不动。
	p.BlockModelClear("g2", "gpt-5.6-sol")
	p.BlockModelClear("g1", "gpt-5.6-sol")
	if st, _ := p.Status("g2"); len(st.BlockedModels) != 0 {
		t.Fatalf("成功后自动加的应解除: %+v", st.BlockedModels)
	}
	if st, _ := p.Status("g1"); len(st.BlockedModels) != 1 {
		t.Fatalf("手动加的不随成功解除: %+v", st.BlockedModels)
	}
	if st := p.ModelBlockedForRealm("gpt-5.6-sol", "global"); st.Blocked {
		t.Fatalf("g2 解除后不应再全挡住: %+v", st)
	}
}

// TestBlockedModelsFallbackSkips 全冷却兜底也不挑名单挡住这个模型的号。
func TestBlockedModelsFallbackSkips(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.Cooldown("u1", CoolSoft, time.Minute, "429")
	p.Cooldown("u2", CoolSoft, time.Hour, "429")
	p.SetBlockedModels("u1", []string{"glm-*"})
	if a := p.PickExcludingForRealm(nil, "glm-5.2", ""); a == nil || a.UID != "u2" {
		t.Fatalf("兜底应跳过名单挡住的 u1、选 u2: %v", a)
	}
}

// TestSetAddRemoveBlockedModels 整份保存保留还在的项（自动加的保留原因），去掉的解除；批量加不重复、删的时候自动加的也删；
// 不在池里的号返回 false。
func TestSetAddRemoveBlockedModels(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.BlockModelForever("u1", "gpt-5.6-sol", forbidden)
	p.BlockModelForever("u1", "gemini-3-pro", forbidden)
	if !p.SetBlockedModels("u1", []string{"GPT-5.6-SOL", "kimi-*"}) {
		t.Fatal("SetBlockedModels 应成功")
	}
	st, _ := p.Status("u1")
	if len(st.BlockedModels) != 2 || !st.BlockedModels[0].Auto || st.BlockedModels[0].Model != "gpt-5.6-sol" ||
		st.BlockedModels[1].Auto || st.BlockedModels[1].Model != "kimi-*" || st.BlockedModels[1].At.IsZero() {
		t.Fatalf("应保留自动加的 gpt-5.6-sol、去掉 gemini、新加手动的 kimi-*: %+v", st.BlockedModels)
	}
	p.AddBlockedModels("u1", []string{"KIMI-*", "grok-*"})
	p.RemoveBlockedModels("u1", []string{"gpt-5.6-sol", "nope"})
	st, _ = p.Status("u1")
	if got := blockedNames(st.BlockedModels); got != "kimi-*,grok-*" {
		t.Fatalf("名单 = %s", got)
	}
	p.SetBlockedModels("u1", nil)
	if st, _ := p.Status("u1"); st.BlockedModels != nil {
		t.Fatalf("清空后应为 nil: %+v", st.BlockedModels)
	}
	if p.SetBlockedModels("gone", nil) || p.AddBlockedModels("gone", []string{"x"}) || p.RemoveBlockedModels("gone", []string{"x"}) {
		t.Fatal("不在池里的号应返回 false")
	}
}

// TestModelRefused 测活跳过的：名单挡住的、11102 还在避让的；6004 限流和过了避让期的不算。
func TestModelRefused(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetBlockedModels("u1", []string{"gpt-*"})
	p.BlockModelBackoff("u1", "glm-9", "11102 model not available")
	p.mu.Lock()
	p.byUID["u1"].modelCooldowns["glm-6004"] = modelCooldown{Until: time.Now().Add(time.Hour), Reason: "6004 limit"}
	p.byUID["u1"].modelCooldowns["glm-old"] = modelCooldown{Until: time.Now().Add(-time.Hour), Reason: "11102 model not available"}
	p.mu.Unlock()
	for model, want := range map[string]bool{"gpt-5.6-sol": true, "glm-9": true, "glm-6004": false, "glm-old": false, "glm-5.2": false} {
		if got := p.ModelRefused("u1", model); got != want {
			t.Errorf("ModelRefused(%s) = %v, want %v", model, got, want)
		}
	}
	if p.ModelRefused("gone", "gpt-5.6-sol") {
		t.Error("不在池里的号应返回 false")
	}
}

// TestBlockedModelsPersist 名单随状态文件落盘、重启恢复；旧版按到期避让记的 11158（过期的不会落盘）迁进名单，
// 11102 照旧留在避让表。
func TestBlockedModelsPersist(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "state.json")
	p := New(fp)
	p.Add(&auth.Auth{UID: "u1"})
	p.BlockModelForever("u1", "gpt-5.6-sol", forbidden)
	p.AddBlockedModels("u1", []string{"kimi-*"})
	p.mu.Lock()
	p.saveLocked()
	p.mu.Unlock()
	p2 := New(fp)
	st, _ := p2.Status("u1")
	if got := blockedNames(st.BlockedModels); got != "gpt-5.6-sol,kimi-*" || !st.BlockedModels[0].Auto || st.BlockedModels[0].Reason != forbidden {
		t.Fatalf("重启后名单 = %+v", st.BlockedModels)
	}

	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	old := `{"accounts":{"u1":{"credits":1,"model_cooldowns":{
		"gpt-5.6-sol":{"until":"` + future + `","reason":"11158 model not allowed for account type"},
		"glm-9":{"until":"` + future + `","reason":"11102 model not available"}},
		"blocked_models":[{"model":"GPT-*","at":"` + future + `"}]},
		"u2":{"credits":1,"model_cooldowns":{"gemini-3-pro":{"until":"` + future + `","reason":"11158 model not allowed for account type"}}}}}`
	if err := os.WriteFile(fp, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	p3 := New(fp)
	st1, _ := p3.Status("u1")
	st2, _ := p3.Status("u2")
	if got := blockedNames(st1.BlockedModels); got != "GPT-*" {
		t.Fatalf("u1 的 11158 已被手动的 GPT-* 挡住，不再重复加: %s", got)
	}
	if len(st2.BlockedModels) != 1 || st2.BlockedModels[0].Model != "gemini-3-pro" || !st2.BlockedModels[0].Auto || st2.BlockedModels[0].Reason != forbidden {
		t.Fatalf("u2 的 11158 应迁进名单: %+v", st2.BlockedModels)
	}
	p3.mu.RLock()
	_, kept11102 := p3.byUID["u1"].modelCooldowns["glm-9"]
	_, kept11158 := p3.byUID["u2"].modelCooldowns["gemini-3-pro"]
	p3.mu.RUnlock()
	if !kept11102 || kept11158 {
		t.Fatalf("11102 应留在避让表、11158 不应留: 11102=%v 11158=%v", kept11102, kept11158)
	}
}

func blockedNames(list []BlockedModel) string {
	names := make([]string, 0, len(list))
	for _, b := range list {
		names = append(names, b.Model)
	}
	return strings.Join(names, ",")
}
