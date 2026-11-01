// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// wavePool 三个国内版号 + 一个国际版号，余额都充足。
func wavePool(t *testing.T) *Pool {
	t.Helper()
	p := New("")
	for _, uid := range []string{"c1", "c2", "c3"} {
		p.Add(&auth.Auth{UID: uid, Domain: "www.codebuddy.cn"})
		p.SetCredits(uid, 1000, 0)
	}
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"})
	p.SetCredits("g1", 1000, 0)
	return p
}

// restrict 模拟 handler 的 11140 处理：先禁用再记风控波。
func restrict(p *Pool, uid string) int {
	p.Disable(uid, "11140")
	return p.NoteRestriction(uid)
}

func TestRestrictionWavePausesRealm(t *testing.T) {
	p := wavePool(t)
	if n := restrict(p, "c1"); n != 0 {
		t.Fatalf("第一个号被限不应触发暂停，暂停了 %d 个", n)
	}
	if st, _ := p.Status("c2"); st.Cooling {
		t.Fatalf("未达阈值时同域的号不应冷却: %+v", st)
	}
	if n := restrict(p, "c2"); n != 1 {
		t.Fatalf("第二个号被限应暂停同域剩下的 1 个号，实际 %d", n)
	}
	st, _ := p.Status("c3")
	if !st.Cooling || st.CoolKind != "restriction_wave" || !strings.Contains(st.Reason, "restriction wave") {
		t.Fatalf("c3 应处风控波暂停: %+v", st)
	}
	if st.CoolRemaining < int64((29 * time.Minute).Seconds()) {
		t.Fatalf("默认暂停 30 分钟，剩余 %d 秒", st.CoolRemaining)
	}
	if st, _ := p.Status("g1"); st.Cooling {
		t.Fatalf("别的域不受影响: %+v", st)
	}
	if st, _ := p.Status("c1"); !st.Disabled || st.CoolKind != "" {
		t.Fatalf("已禁用的号保持禁用，不再叠暂停: %+v", st)
	}
	if acct := p.PickExcludingForRealm(nil, "", "cn"); acct != nil {
		t.Fatalf("暂停期的号不参与兜底，实际选中 %s", acct.UID)
	}
	if acct := p.Pick(); acct == nil || acct.UID != "g1" {
		t.Fatalf("应只选到国际版号，实际 %+v", acct)
	}
	if len(p.waveHits["cn"]) != 0 {
		t.Fatalf("触发后该域记录应清空: %+v", p.waveHits["cn"])
	}
}

func TestRestrictionWaveKeepsLongerCooldown(t *testing.T) {
	p := wavePool(t)
	p.Cooldown("c3", CoolHard, 10*time.Hour, "credit exhausted")
	restrict(p, "c1")
	if n := restrict(p, "c2"); n != 0 {
		t.Fatalf("已有更晚截止的号不应改成暂停，实际暂停 %d 个", n)
	}
	if st, _ := p.Status("c3"); st.CoolKind != "hard_credit" || st.CoolRemaining < int64((9*time.Hour).Seconds()) {
		t.Fatalf("积分冷却不应被缩短: %+v", st)
	}
}

func TestRestrictionWaveWindowAndDedupe(t *testing.T) {
	p := wavePool(t)
	restrict(p, "c1")
	p.ReviveDisabled("c1")
	if n := restrict(p, "c1"); n != 0 {
		t.Fatalf("同一个号重复被限只算一次，实际暂停 %d 个", n)
	}
	// 把 c1 的记录挪到窗口外：再有一个号被限也不够阈值。
	p.mu.Lock()
	p.waveHits["cn"][0].at = time.Now().Add(-16 * time.Minute)
	p.mu.Unlock()
	if n := restrict(p, "c2"); n != 0 {
		t.Fatalf("窗口外的记录不计数，实际暂停 %d 个", n)
	}
	if got := len(p.waveHits["cn"]); got != 1 {
		t.Fatalf("过期记录应被剔除，剩 %d 条", got)
	}
	if n := p.NoteRestriction("ghost"); n != 0 {
		t.Fatalf("不存在的账号不记录，实际暂停 %d 个", n)
	}
}

func TestRestrictionWaveConfig(t *testing.T) {
	p := wavePool(t)
	restrict(p, "c1")
	p.SetRestrictionWave(0, 0, 0)
	if p.waveHits != nil {
		t.Fatalf("关闭时应清空记录: %+v", p.waveHits)
	}
	if n := restrict(p, "c2"); n != 0 {
		t.Fatalf("关闭后不应暂停，实际 %d 个", n)
	}

	p2 := wavePool(t)
	p2.SetRestrictionWave(-1, -time.Minute, 0)
	if p2.waveThreshold != defaultRestrictionWaveThreshold || p2.waveWindow != defaultRestrictionWaveWindow || p2.wavePause != defaultRestrictionWavePause {
		t.Fatalf("非法值应保留默认: %d %s %s", p2.waveThreshold, p2.waveWindow, p2.wavePause)
	}
	p2.SetRestrictionWave(3, time.Minute, time.Hour)
	restrict(p2, "c1")
	if n := restrict(p2, "c2"); n != 0 {
		t.Fatalf("阈值 3 时两个号不够，实际暂停 %d 个", n)
	}
	if n := restrict(p2, "c3"); n != 0 {
		t.Fatalf("国内版号都被禁用了，没有可暂停的号，实际 %d 个", n)
	}
	if st, _ := p2.Status("g1"); st.Cooling {
		t.Fatalf("别的域不受影响: %+v", st)
	}
}

func TestRestrictionWavePauseNotShortened(t *testing.T) {
	p := wavePool(t)
	restrict(p, "c1")
	restrict(p, "c2")
	p.Cooldown("c3", CoolSoft, time.Minute, "upstream 404")
	p.CooldownSoftRate("c3", time.Minute, time.Time{}, "429")
	p.CooldownSoftRate("c3", time.Minute, time.Now().Add(time.Minute), "429 reset")
	p.CooldownSoftForModel("c3", time.Minute, time.Time{}, "m", "429")
	p.ReenableIfCredits("c3", 500, 1000)
	st, _ := p.Status("c3")
	if st.CoolKind != "restriction_wave" || st.CoolRemaining < int64((29*time.Minute).Seconds()) {
		t.Fatalf("暂停期内更短的冷却和余额刷新都不应解除暂停: %+v", st)
	}
	p.Cooldown("c3", CoolHard, 10*time.Hour, "credit exhausted")
	if st, _ := p.Status("c3"); st.CoolKind != "hard_credit" {
		t.Fatalf("更晚截止的冷却照常记: %+v", st)
	}

	p2 := wavePool(t)
	restrict(p2, "c1")
	restrict(p2, "c2")
	if !p2.Revive("c3") {
		t.Fatal("Revive 应找到 c3")
	}
	if st, _ := p2.Status("c3"); st.Cooling {
		t.Fatalf("面板解冻应提前解除暂停: %+v", st)
	}
}

// exitPool wavePool 再加一个国内版号 c4，并装上出口函数：c1、c2、c4 和 g1 在出口 A（代理「甲」），c3 在出口 B。
func exitPool(t *testing.T, exits map[string]string) *Pool {
	t.Helper()
	p := wavePool(t)
	p.Add(&auth.Auth{UID: "c4", Domain: "www.codebuddy.cn"})
	p.SetCredits("c4", 1000, 0)
	p.SetExitKey(func(uid string) (string, string) {
		k := exits[uid]
		return k, "代理「" + map[string]string{"ip:A": "甲", "ip:B": "乙"}[k] + "」"
	})
	return p
}

// TestRestrictionWaveSameExit 被限的号都在同一个出口：只暂停这个域里这个出口下的号，原因写出口；别的出口、
// 别的域照常。
func TestRestrictionWaveSameExit(t *testing.T) {
	p := exitPool(t, map[string]string{"c1": "ip:A", "c2": "ip:A", "c3": "ip:B", "c4": "ip:A", "g1": "ip:A"})
	restrict(p, "c1")
	if n := restrict(p, "c2"); n != 1 {
		t.Fatalf("同出口应只暂停 c4，实际暂停 %d 个", n)
	}
	st, _ := p.Status("c4")
	if st.CoolKind != "restriction_wave" || !strings.Contains(st.Reason, "风控波：15 分钟内 2 个号在代理「甲」上") {
		t.Fatalf("c4 应按出口暂停，原因写出口: %+v", st)
	}
	if st, _ := p.Status("c3"); st.Cooling {
		t.Fatalf("别的出口不受影响: %+v", st)
	}
	if st, _ := p.Status("g1"); st.Cooling {
		t.Fatalf("同出口但别的域不受影响: %+v", st)
	}
	if acct := p.PickExcludingForRealm(nil, "", "cn"); acct == nil || acct.UID != "c3" {
		t.Fatalf("国内版应只剩 c3 可选，实际 %+v", acct)
	}
}

// TestRestrictionWaveMixedExits 被限的号分布在几个出口，或有一条记录没有出口：照旧暂停整个域。
func TestRestrictionWaveMixedExits(t *testing.T) {
	p := exitPool(t, map[string]string{"c1": "ip:A", "c2": "ip:B", "c3": "ip:B", "c4": "ip:A"})
	restrict(p, "c1")
	if n := restrict(p, "c2"); n != 2 {
		t.Fatalf("不同出口应暂停整个域剩下的 2 个号，实际 %d", n)
	}
	for _, uid := range []string{"c3", "c4"} {
		if st, _ := p.Status(uid); st.CoolKind != "restriction_wave" || !strings.Contains(st.Reason, "restriction wave") {
			t.Fatalf("%s 应按整域暂停: %+v", uid, st)
		}
	}

	// c1 被限时还没装出口函数（记录里没有出口），c2 被限时装上了：不能断定同出口，整域暂停。
	p2 := wavePool(t)
	restrict(p2, "c1")
	p2.SetExitKey(func(string) (string, string) { return "ip:A", "代理「甲」" })
	if n := restrict(p2, "c2"); n != 1 {
		t.Fatalf("有一条记录没有出口时应整域暂停，实际 %d", n)
	}
	if st, _ := p2.Status("c3"); !strings.Contains(st.Reason, "restriction wave") {
		t.Fatalf("应按整域暂停: %+v", st)
	}
}

// TestExitOf 出口查询：没装出口函数时所有号算一个出口；装了按出口分。
func TestExitOf(t *testing.T) {
	p := wavePool(t)
	if k, l := p.ExitOf("c1"); k != "" || l != "" {
		t.Fatalf("没装出口函数应为空: %q %q", k, l)
	}
	if got := strings.Join(p.SameExit("c1"), ","); got != "c1,c2,c3,g1" {
		t.Fatalf("没装出口函数时所有号同一个出口: %s", got)
	}
	p = exitPool(t, map[string]string{"c1": "ip:A", "c2": "ip:B", "c3": "ip:B", "c4": "ip:A", "g1": "ip:A"})
	if k, l := p.ExitOf("c2"); k != "ip:B" || l != "代理「乙」" {
		t.Fatalf("出口不对: %q %q", k, l)
	}
	if got := strings.Join(p.SameExit("c1"), ","); got != "c1,c4,g1" {
		t.Fatalf("同出口的号不对: %s", got)
	}
}
