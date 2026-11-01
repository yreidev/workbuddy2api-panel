// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

// wafip_test.go WAF IP 级拦截状态机：不同号计数阈值、窗口剪枝、激活期不续期、
// 到期自然解除。时间通过直接构造 hits/until 的过去时刻验证，不依赖真实等待。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// 单号反复命中永不触发 IP 级拦截（同号覆盖不累计）。
func TestWafIPGateSameAccountNeverTriggers(t *testing.T) {
	var g wafIPGate
	for i := 0; i < 5; i++ {
		if g.noteWaf("", "", "u1") {
			t.Fatalf("第 %d 次单号命中不应激活", i+1)
		}
	}
	if len(g.hits[""]) != 1 {
		t.Fatalf("同号命中应只记一条，实际 %d", len(g.hits[""]))
	}
	if g.active("") {
		t.Fatal("未达阈值不应激活")
	}
}

// 窗口内同一个出口的不同账号数达到阈值（2）即激活这个出口；激活时清空它的判定窗。
func TestWafIPGateThresholdActivates(t *testing.T) {
	var g wafIPGate
	if g.noteWaf("", "", "u1") {
		t.Fatal("第一个号不应激活")
	}
	if !g.noteWaf("", "", "u2") {
		t.Fatal("第二个不同号应激活 IP 级拦截")
	}
	if !g.active("") {
		t.Fatal("激活后 active() 应为 true")
	}
	if _, ok := g.hits[""]; ok {
		t.Fatalf("激活后应清空判定窗，实际 %d 条", len(g.hits[""]))
	}
}

// 激活期内新命中：返回 true 但不续期、不记账（自然解除语义）。
func TestWafIPGateActiveDoesNotExtend(t *testing.T) {
	var g wafIPGate
	g.noteWaf("", "", "u1")
	g.noteWaf("", "", "u2")
	if !g.active("") {
		t.Fatal("前置激活失败")
	}
	until := g.until[""]
	if !g.noteWaf("", "", "u3") {
		t.Fatal("激活期内新命中应直接返回 true")
	}
	if !g.until[""].Equal(until) {
		t.Fatalf("激活期不应续期: until %v -> %v", until, g.until[""])
	}
	if len(g.hits[""]) != 0 {
		t.Fatalf("激活期内不应记账，实际 %d 条", len(g.hits[""]))
	}
}

// 窗口外的旧命中在下次记账时被剪掉，不参与阈值判定。
func TestWafIPGatePrunesOldHits(t *testing.T) {
	var g wafIPGate
	old := time.Now().Add(-2 * wafIPWindow)
	g.hits = map[string]map[string]time.Time{"": {"stale": old}}
	g.until = map[string]time.Time{}
	if g.noteWaf("", "", "u1") {
		t.Fatal("旧命中应被剪掉，不同号数只有 1，不应激活")
	}
	if _, ok := g.hits[""]["stale"]; ok {
		t.Fatal("窗口外命中应被删除")
	}
	if len(g.hits[""]) != 1 {
		t.Fatalf("应只保留本次命中，实际 %d", len(g.hits[""]))
	}
}

// active()：未激活 false；until 已过 false。
func TestWafIPGateActiveExpiry(t *testing.T) {
	var g wafIPGate
	if g.active("") {
		t.Fatal("零值应未激活")
	}
	g.until = map[string]time.Time{"": time.Now().Add(-time.Second)}
	if g.active("") {
		t.Fatal("until 已过应解除激活")
	}
	g.until[""] = time.Now().Add(time.Minute)
	if !g.active("") {
		t.Fatal("until 未到应处于激活")
	}
}

// 按出口分开记：两个号在不同出口上被拦不算 IP 级；同一个出口再有一个号被拦才激活这个出口，别的出口不受影响。
func TestWafIPGateByExit(t *testing.T) {
	var g wafIPGate
	if g.noteWaf("ip:A", "代理「甲」", "u1") || g.noteWaf("ip:B", "代理「乙」", "u2") {
		t.Fatal("不同出口的号各被拦一次不应激活")
	}
	if !g.noteWaf("ip:A", "代理「甲」", "u3") {
		t.Fatal("同出口第二个号被拦应激活这个出口")
	}
	if !g.active("ip:A") || g.active("ip:B") || g.active("") {
		t.Fatalf("只有出口 A 激活: %+v", g.until)
	}
	if len(g.hits["ip:B"]) != 1 {
		t.Fatalf("出口 B 的记录不受影响: %+v", g.hits)
	}
}

// TestChatWafRotatesToOtherExit 一个出口被 WAF 整个拦了：同出口的号这次不再试，换到别的出口的号；不挂代理时
// 照旧止损（TestFull8ChatWafIPFailFast）。
func TestChatWafRotatesToOtherExit(t *testing.T) {
	fastRotate(t)
	calls := map[string]int{}
	var mu sync.Mutex
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		mu.Lock()
		calls[authz]++
		mu.Unlock()
		if authz == "Bearer at3" {
			return 200, sseOK, true
		}
		return 403, "<html>waf blocked</html>", false
	})
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u3", AccessToken: "at3", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u4", AccessToken: "at4", ExpiresAt: 9999999999},
	)
	// 余额定先后：u1、u2 先被选中，没有按出口排除的话接着选 u4（和它们同出口），u3 最后。
	for uid, c := range map[string]int64{"u1": 5000, "u2": 4000, "u4": 3000, "u3": 1000} {
		p.SetCredits(uid, c, 0)
	}
	p.SetExitKey(func(uid string) (string, string) {
		if uid == "u3" {
			return "ip:B", "代理「乙」"
		}
		return "ip:A", "代理「甲」"
	})
	h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 5})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("应换到别的出口的号成功，code=%d body=%s calls=%v", rec.Code, rec.Body, calls)
	}
	if calls["Bearer at4"] != 0 || calls["Bearer at3"] != 1 || calls["Bearer at1"] != 1 || calls["Bearer at2"] != 1 {
		t.Fatalf("出口 A 被拦后不应再试 u4: %v", calls)
	}
}
