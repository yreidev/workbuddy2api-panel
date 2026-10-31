// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

func slotClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// TestSlotFreedOnRelease 放出在途名额时叫醒在等的人，之后再等拿到新的 channel；名额本来就是 0 的
// 重复释放不算放出名额。
func TestSlotFreedOnRelease(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetMaxInFlight(1)
	ch := p.SlotFreed()
	if p.SlotFreed() != ch {
		t.Fatal("没人放名额之前，等的人应拿到同一个 channel")
	}
	p.Release("u1")
	if slotClosed(ch) {
		t.Fatal("在途本来就是 0，重复释放不应叫醒等的人")
	}
	if !p.Acquire("u1") {
		t.Fatal("Acquire 应成功")
	}
	p.Release("u1")
	if !slotClosed(ch) {
		t.Fatal("放出名额后应叫醒等的人")
	}
	if next := p.SlotFreed(); slotClosed(next) {
		t.Fatal("叫醒之后再等，应拿到新的、还没关的 channel")
	}
	p.Release("nobody") // 不存在的号：不 panic
}

// TestServableIgnoringInFlight 只是在途满的号算「等得到」；tried、别的域、禁用暂停、冷却、模型级冷却、
// 模型目录不接、积分保底拦下的号都不算。
func TestServableIgnoringInFlight(t *testing.T) {
	withNoPickGap(t)
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	newPool := func() *Pool {
		p := New("")
		p.Add(&auth.Auth{UID: "u1"})
		p.SetMaxInFlight(1)
		if !p.Acquire("u1") {
			t.Fatal("Acquire 应成功")
		}
		return p
	}
	if p := newPool(); !p.ServableIgnoringInFlight(nil, "m", "") || p.PickExcludingForRealm(nil, "m", "") != nil {
		t.Fatal("号只是在途满：选不到号，但应算等得到")
	}
	cases := []struct {
		name  string
		set   func(*Pool)
		tried map[string]bool
		realm string
	}{
		{"这次已经试过", func(*Pool) {}, map[string]bool{"u1": true}, ""},
		{"别的域", func(*Pool) {}, nil, "global"},
		{"禁用", func(p *Pool) { p.Disable("u1", "test") }, nil, ""},
		{"暂停选号", func(p *Pool) { p.Pause("u1") }, nil, ""},
		{"冷却", func(p *Pool) { p.Cooldown("u1", CoolHard, time.Hour, "test") }, nil, ""},
		{"模型级冷却", func(p *Pool) { p.CooldownModel("u1", "m", time.Hour, "test") }, nil, ""},
		{"模型目录不接", func(p *Pool) {
			p.SetModelServes(func(string, string, bool, string) bool { return false })
		}, nil, ""},
		{"积分保底", func(p *Pool) {
			p.SetCreditFloor(100)
			p.SetCredits("u1", 10, 0)
			p.NoteModelCost("u1", "m", 2, 1000)
		}, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPool()
			tc.set(p)
			if p.ServableIgnoringInFlight(tc.tried, "m", tc.realm) {
				t.Fatal("这个号等也等不来，不应算等得到")
			}
		})
	}
	// 不带模型时按账号级健康判断。
	if p := newPool(); !p.ServableIgnoringInFlight(nil, "", "cn") {
		t.Fatal("不带模型、号只是在途满：应算等得到")
	}
}

// TestServableForModel 「此刻就能接」比「等得到」更严：在途满的号不算；软冷却的号也不算，
// 尽管 pick 的全冷却兜底照样会把它选出来。
func TestServableForModel(t *testing.T) {
	withNoPickGap(t)
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetMaxInFlight(1)
	if !p.ServableForModel(nil, "m", "cn") || !p.ServableForModel(nil, "", "") {
		t.Fatal("空闲的健康号应算此刻能接")
	}
	if p.ServableForModel(map[string]bool{"u1": true}, "m", "") || p.ServableForModel(nil, "m", "global") {
		t.Fatal("这次试过的号、别的域的号都不算")
	}
	if !p.Acquire("u1") {
		t.Fatal("Acquire 应成功")
	}
	if p.ServableForModel(nil, "m", "") || !p.ServableIgnoringInFlight(nil, "m", "") {
		t.Fatal("在途满：此刻接不了，但等得到")
	}
	p.Release("u1")
	p.Cooldown("u1", CoolSoft, time.Hour, "test")
	if p.ServableForModel(nil, "m", "") {
		t.Fatal("软冷却中的号不算此刻能接")
	}
	if p.PickExcludingForRealm(nil, "m", "") == nil {
		t.Fatal("对照：pick 的全冷却兜底应选出软冷却的号")
	}
}

// TestSetInFlightWait 默认 30 秒；0 = 不等；负值保留原值。
func TestSetInFlightWait(t *testing.T) {
	p := New("")
	if got := p.InFlightWait(); got != 30*time.Second {
		t.Fatalf("默认应是 30s，got %v", got)
	}
	p.SetInFlightWait(0)
	if got := p.InFlightWait(); got != 0 {
		t.Fatalf("设成 0 应不等，got %v", got)
	}
	p.SetInFlightWait(5 * time.Second)
	p.SetInFlightWait(-time.Second)
	if got := p.InFlightWait(); got != 5*time.Second {
		t.Fatalf("负值应保留原值，got %v", got)
	}
}
