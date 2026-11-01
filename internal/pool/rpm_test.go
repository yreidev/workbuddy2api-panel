// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestRPMCounterWindow 最近 60 秒内到上限就满；最早那条滑出窗口后又能接；不限时不记。
func TestRPMCounterWindow(t *testing.T) {
	var c rpmCounter
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if !c.take(2, t0) || !c.take(2, t0.Add(10*time.Second)) {
		t.Fatal("没到上限应能记")
	}
	if c.take(2, t0.Add(20*time.Second)) || !c.full(2, t0.Add(59*time.Second)) {
		t.Fatal("60 秒内已有 2 条，应满")
	}
	if c.full(2, t0.Add(60*time.Second)) {
		t.Fatal("第一条刚好滑出窗口，应不满")
	}
	if !c.take(2, t0.Add(61*time.Second)) || len(c.times) != 2 {
		t.Fatalf("滑出的记录应被去掉，窗口内剩 2 条，实际 %d", len(c.times))
	}
	if c.full(0, t0) || !c.take(0, t0) || len(c.times) != 2 {
		t.Fatal("不限时不算满，也不记")
	}
}

// TestAcquireRPM 每分钟请求数到上限时 Acquire 失败并退回在途名额；国内版、国际版各用各的档，不互相回落；
// 负值保留原值。
func TestAcquireRPM(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "c1", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"})
	p.SetMaxInFlight(5)
	p.SetMaxRPM(2)
	p.SetMaxRPM(-1)
	for i := 0; i < 2; i++ {
		if !p.Acquire("c1") {
			t.Fatalf("第 %d 次 Acquire 应成功", i+1)
		}
		p.Release("c1")
	}
	ch := p.SlotFreed()
	if p.Acquire("c1") {
		t.Fatal("一分钟内第 3 次应因 RPM 到上限失败")
	}
	if n := p.byUID["c1"].inFlight.Load(); n != 0 {
		t.Fatalf("RPM 拒绝后应退回在途名额，实际在途 %d", n)
	}
	if slotClosed(ch) {
		t.Fatal("退回没真正用上的名额不应叫醒排队的请求")
	}
	// 国际版档没设（0 = 不限），不回落到国内版档。
	for i := 0; i < 3; i++ {
		if !p.Acquire("g1") {
			t.Fatal("国际版不限 RPM，应一直能占")
		}
		p.Release("g1")
	}
	// 打开上限后才开始记：之前不限时的请求不算。
	p.SetMaxRPMGlobal(1)
	p.SetMaxRPMGlobal(-1)
	if !p.Acquire("g1") {
		t.Fatal("国际版刚打开上限，之前的请求没记，第一次应成功")
	}
	p.Release("g1")
	if p.Acquire("g1") {
		t.Fatal("国际版上限 1，一分钟内第 2 次应失败")
	}
}

// TestRPMBusySelection RPM 到上限的号同在途满：选号跳过、算「等得到」但不算「此刻能接」，
// RPMBusy 报告是 RPM 的原因；只是在途满时 RPMBusy 为 false。
func TestRPMBusySelection(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.SetMaxInFlight(1)
	p.SetMaxRPM(2)
	for i := 0; i < 2; i++ {
		if !p.Acquire("u1") {
			t.Fatal("Acquire 应成功")
		}
		p.Release("u1")
	}
	for i := 0; i < 5; i++ {
		if a := p.PickExcludingForRealm(nil, "m", ""); a == nil || a.UID != "u2" {
			t.Fatalf("u1 RPM 到上限，应只选 u2，实际 %v", a)
		}
	}
	if p.PickByUIDForModel("u1", "m") != nil || p.PickByUID("u1") != nil {
		t.Fatal("粘性命中 RPM 到上限的号应返回 nil，让调用方换号")
	}
	if uids := p.AvailableUIDsForModelRealm("m", ""); len(uids) != 1 || uids[0] != "u2" {
		t.Fatalf("可分配的号应只剩 u2，实际 %v", uids)
	}
	if !p.Acquire("u2") {
		t.Fatal("Acquire u2 应成功")
	}
	// u1 RPM 满、u2 在途满（RPM 才 1/2）：此刻都接不了，但等得到，且有号是 RPM 的原因。
	if p.ServableForModel(nil, "m", "") || !p.ServableIgnoringInFlight(nil, "m", "") || !p.RPMBusy(nil, "m", "") {
		t.Fatal("两个号都忙：此刻接不了、等得到、有 RPM 原因")
	}
	if p.RPMBusy(map[string]bool{"u1": true}, "m", "") {
		t.Fatal("RPM 满的号试过了，剩下的只是在途满，不算 RPM 原因")
	}
	if _, _, _, _, _, _, full := p.CountsDetailedWithPaused(); full != 2 {
		t.Fatalf("满载计数应含 RPM 到上限的号，实际 %d", full)
	}
	// u1 最早那条滑出窗口：又能接。
	p.byUID["u1"].rpm.times[0] = time.Now().Add(-rpmWindow - time.Second)
	if a := p.PickExcludingForRealm(nil, "m", ""); a == nil || a.UID != "u1" || p.RPMBusy(nil, "m", "") {
		t.Fatalf("u1 窗口过了应能再选，实际 %v", a)
	}
}
