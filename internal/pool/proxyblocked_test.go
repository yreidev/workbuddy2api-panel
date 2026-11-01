// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestSetProxyBlocked 代理池拦住的号：不参与选号（含全冷却兜底）、不测活、不算「能服务」，状态里带原因，
// 计数在 /status 口径算进冷却、面板口径单独计；放开后恢复，并叫醒在等空位的请求。
func TestSetProxyBlocked(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	for _, uid := range []string{"u1", "u2"} {
		p.Add(&auth.Auth{UID: uid, Domain: "www.codebuddy.cn"})
		p.SetCredits(uid, 1000, 0)
	}
	const reason = "代理「甲」连不上（11-01 09:30 起）"
	p.SetProxyBlocked("u1", reason)
	p.SetProxyBlocked("ghost", reason) // 不在池里：忽略
	if st, _ := p.Status("u1"); st.ProxyBlocked != reason {
		t.Fatalf("状态应带拦截原因: %+v", st)
	}
	for range 5 {
		if a := p.Pick(); a == nil || a.UID != "u2" {
			t.Fatalf("被拦的号不应被选中，实际 %+v", a)
		}
	}
	if _, healthy, cooling, _, _ := p.CountsDetailed(); healthy != 1 || cooling != 1 {
		t.Fatalf("/status 口径被拦的号算进冷却: healthy=%d cooling=%d", healthy, cooling)
	}
	if _, healthy, cooling, _, _, blocked, _ := p.CountsDetailedWithPaused(); healthy != 1 || cooling != 0 || blocked != 1 {
		t.Fatalf("面板口径被拦的号单独计: healthy=%d cooling=%d blocked=%d", healthy, cooling, blocked)
	}

	// u1 同时在软冷却：被拦时不进兜底、不测活；u2 被 tried 排除后没有号可选。
	p.Cooldown("u1", CoolSoft, time.Minute, "429")
	if a := p.PickExcludingForRealm(map[string]bool{"u2": true}, "", "cn"); a != nil {
		t.Fatalf("被拦的号不应进全冷却兜底，实际 %s", a.UID)
	}
	if due := p.LivenessDue(time.Now()); len(due) != 0 {
		t.Fatalf("被拦的号不测活: %v", due)
	}

	p.CooldownModel("u2", "m", time.Minute, "6004")
	if st := p.ModelBlockedForRealm("m", "cn"); !st.Blocked || st.Count != 1 {
		t.Fatalf("被拦的号不算「能服务」，只剩 u2 被模型冷却挡住: %+v", st)
	}
	if rows := p.ModelLockView(); len(rows) != 1 || rows[0].Total != 1 {
		t.Fatalf("被拦的号不算参与选号: %+v", rows)
	}

	ch := p.SlotFreed()
	p.SetProxyBlocked("u2", "等代理")
	if slotClosed(ch) {
		t.Fatal("拦住号不应叫醒等空位的请求")
	}
	p.SetProxyBlocked("u1", "")
	if !slotClosed(ch) {
		t.Fatal("放开号应叫醒等空位的请求")
	}
	if st, _ := p.Status("u1"); st.ProxyBlocked != "" {
		t.Fatalf("放开后不再带原因: %+v", st)
	}
	if a := p.PickExcludingForRealm(map[string]bool{"u2": true}, "", "cn"); a == nil || a.UID != "u1" {
		t.Fatalf("放开后冷却中的号回到兜底，实际 %+v", a)
	}
	if due := p.LivenessDue(time.Now()); len(due) != 1 || due[0] != "u1" {
		t.Fatalf("放开后冷却中的号要测活: %v", due)
	}
}
