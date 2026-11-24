// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestTPMCounterWindow 最近 60 秒内累计 token 到上限就满；最早样本滑出窗口后扣减并不漂移；不限时不记。
func TestTPMCounterWindow(t *testing.T) {
	var c tpmCounter
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	// 不限时不算满、也不分配样本。
	c.add(0, 5000, t0)
	if c.full(0, t0) || len(c.samples) != 0 || c.sum != 0 {
		t.Fatalf("不限时不应记样本: samples=%d sum=%d", len(c.samples), c.sum)
	}
	// 非正 token 不记。
	c.add(1000, 0, t0)
	c.add(1000, -100, t0)
	if len(c.samples) != 0 || c.sum != 0 {
		t.Fatalf("非正 token 不应写入: samples=%d sum=%d", len(c.samples), c.sum)
	}

	c.add(1000, 600, t0)
	c.add(1000, 400, t0.Add(10*time.Second))
	if !c.full(1000, t0.Add(59*time.Second)) || c.sum != 1000 {
		t.Fatalf("60 秒内累计 1000 应满: sum=%d", c.sum)
	}
	// 第一条在 t0+60s 刚好滑出窗口，剩 400 未满。
	if c.full(1000, t0.Add(60*time.Second)) {
		t.Fatal("第一条滑出窗口后剩余 400，不应满")
	}
	if len(c.samples) != 1 || c.sum != 400 {
		t.Fatalf("滑出后应剩 1 条 400，实际 samples=%d sum=%d", len(c.samples), c.sum)
	}
	// 全部滑出窗口后 sum 精确归零。
	if c.full(1000, t0.Add(70*time.Second)) || len(c.samples) != 0 || c.sum != 0 {
		t.Fatalf("全部滑出后应归零: samples=%d sum=%d", len(c.samples), c.sum)
	}
}

// TestAcquireTPMAndRPMOrder TPM 到上限时 Acquire 失败并退回在途名额，且不白耗 RPM 名额；
// 国内版与国际版各管各的域不互相回落；负值保留原值；失败请求（无 token）不虚扣。
func TestAcquireTPMAndRPMOrder(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "c1", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"})
	p.SetMaxInFlight(5)
	p.SetMaxRPM(1)
	p.SetMaxTPM(1000)
	p.SetMaxTPM(-500) // 负值保留 1000

	// 失败尝试（无有效 token）不扣减 TPM 窗。
	p.RecordTokenUsage("c1", TokenUsageDelta{Model: "glm-5.2"})
	p.RecordTokenUsage("c1", TokenUsageDelta{HasTotalTokens: true, TotalTokens: -10})
	if p.byUID["c1"].tpm.sum != 0 {
		t.Fatalf("无有效 token 不应写入 TPM 窗: sum=%d", p.byUID["c1"].tpm.sum)
	}

	// 写入 1000 token（优先 TotalTokens），使 c1 的 TPM 满载（此时 RPM 仍为 0/1）。
	p.RecordTokenUsage("c1", TokenUsageDelta{
		Model:               "glm-5.2",
		HasPromptTokens:     true,
		PromptTokens:        300,
		HasCompletionTokens: true,
		CompletionTokens:    200,
		HasTotalTokens:      true,
		TotalTokens:         1000,
	})
	ch := p.SlotFreed()
	if p.Acquire("c1") {
		t.Fatal("c1 已达 TPM 上限，Acquire 应失败")
	}
	if n := p.byUID["c1"].inFlight.Load(); n != 0 {
		t.Fatalf("TPM 拒绝后应退回在途名额，实际 %d", n)
	}
	if slotClosed(ch) {
		t.Fatal("退回未用上的在途名额不应叫醒排队的请求")
	}
	// 关键校验：TPM 先检拒绝时，不得白白消耗 RPM 名额。
	if got := len(p.byUID["c1"].rpm.times); got != 0 {
		t.Fatalf("TPM 满载拒绝 Acquire 时不应消耗 RPM 名额，实际 rpm.times=%d", got)
	}
	// 热更新调大 TPM 上限后，原本未被消耗的 1 个 RPM 名额仍可立即使用。
	p.SetMaxTPM(2000)
	if !p.Acquire("c1") {
		t.Fatal("调大 TPM 后未被消耗的 RPM 名额应立即可用")
	}
	p.Release("c1")

	// 国际版档未设（0 = 不限），不回落国内版档，也不记录样本。
	p.RecordTokenUsage("g1", TokenUsageDelta{HasTotalTokens: true, TotalTokens: 99999})
	if len(p.byUID["g1"].tpm.samples) != 0 {
		t.Fatal("国际版不限 TPM 时不应记录样本")
	}
	if !p.Acquire("g1") {
		t.Fatal("国际版不限 TPM，应一直能占")
	}
	p.Release("g1")

	// 国际版打开上限后才开始记；TotalTokens 缺失时回落 Prompt+Completion。
	p.SetMaxTPMGlobal(500)
	p.SetMaxTPMGlobal(-1) // 负值保留 500
	if !p.Acquire("g1") {
		t.Fatal("国际版刚打开上限，之前不限时未记，应可占")
	}
	p.Release("g1")
	p.RecordTokenUsage("g1", TokenUsageDelta{
		HasPromptTokens:     true,
		PromptTokens:        300,
		HasCompletionTokens: true,
		CompletionTokens:    250,
	})
	if p.Acquire("g1") {
		t.Fatal("国际版回落 Prompt+Completion=550 >= 500，应因 TPM 满拒绝")
	}
}

// TestTPMBusySelection TPM 到上限的号同在途满：选号跳过、粘性解绑、算「等得到」但不算「此刻能接」，
// 计入满载数，TPMBusy 报告是 TPM 原因，窗口滑出后恢复可选。
func TestTPMBusySelection(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.SetMaxInFlight(1)
	p.SetMaxTPM(1000)

	p.RecordTokenUsage("u1", TokenUsageDelta{HasTotalTokens: true, TotalTokens: 1000})
	for i := 0; i < 5; i++ {
		if a := p.PickExcludingForRealm(nil, "m", ""); a == nil || a.UID != "u2" {
			t.Fatalf("u1 TPM 到上限，应只选 u2，实际 %v", a)
		}
	}
	if p.PickByUIDForModel("u1", "m") != nil || p.PickByUID("u1") != nil {
		t.Fatal("粘性命中 TPM 到上限的号应返回 nil，让调用方换号")
	}
	if uids := p.AvailableUIDsForModelRealm("m", ""); len(uids) != 1 || uids[0] != "u2" {
		t.Fatalf("可分配的号应只剩 u2，实际 %v", uids)
	}
	if !p.Acquire("u2") {
		t.Fatal("Acquire u2 应成功")
	}
	// u1 TPM 满、u2 在途满：此刻接不了、等得到、TPMBusy=true、RPMBusy=false。
	if p.ServableForModel(nil, "m", "") || !p.ServableIgnoringInFlight(nil, "m", "") ||
		!p.TPMBusy(nil, "m", "") || p.RPMBusy(nil, "m", "") {
		t.Fatal("两个号都忙：此刻接不了、等得到、有 TPM 原因且无 RPM 原因")
	}
	if p.TPMBusy(map[string]bool{"u1": true}, "m", "") {
		t.Fatal("TPM 满的号试过了，剩下的只是在途满，不算 TPM 原因")
	}
	if _, _, _, _, _, _, full := p.CountsDetailedWithPaused(); full != 2 {
		t.Fatalf("满载计数应含 TPM 到上限的号，实际 %d", full)
	}
	// u1 样本滑出窗口：又能接，TPMBusy 归 false。
	p.byUID["u1"].tpm.samples[0].at = time.Now().Add(-tpmWindow - time.Second)
	if a := p.PickExcludingForRealm(nil, "m", ""); a == nil || a.UID != "u1" || p.TPMBusy(nil, "m", "") {
		t.Fatalf("u1 窗口过了应能再选，实际 %v", a)
	}
}
