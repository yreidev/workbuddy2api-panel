// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// groups_test.go 客户端密钥限定分组、号的优先级分层（见 pool/groups.go）。
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/apikeys"
	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
)

// groupRig 两个号 in（在分组 A 里）和 out（没分组）、一个限定 A 的密钥和一个不限的密钥；记下上游收到的令牌。
func groupRig(t *testing.T) (h *Handler, p *pool.Pool, scoped, open apikeys.Key, tokens func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		mu.Lock()
		seen = append(seen, authz)
		mu.Unlock()
		return 200, sseOK, true
	})
	p = testPoolWith(
		&auth.Auth{UID: "in", AccessToken: "at-in", ExpiresAt: 9999999999},
		&auth.Auth{UID: "out", AccessToken: "at-out", ExpiresAt: 9999999999},
	)
	g, _ := p.CreateGroup("A", "")
	p.SetAccountGroups("in", []string{g.ID})
	store, _ := apikeys.Open("")
	scoped = createKey(t, store, apikeys.Input{Name: "只用 A", Enabled: true, Groups: []string{g.ID}})
	open = createKey(t, store, apikeys.Input{Name: "不限", Enabled: true})
	sess := session.New(session.Config{TTL: time.Minute, Store: newBindStore(),
		AvailableForModel: p.WeightedAvailableUIDsForModelRealm, AssignableForModel: p.CheapestWeightedUIDsForModelRealm})
	h = NewHandler(Config{Pool: p, Upstream: up, Session: sess, Keys: store, Live: livecfg.New(livecfg.Snapshot{})})
	return h, p, scoped, open, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := append([]string(nil), seen...)
		seen = nil
		return out
	}
}

func chatWith(h *Handler, k apikeys.Key, conv string) *httptest.ResponseRecorder {
	body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true,"metadata":{"conversation_id":"` + conv + `"}}`
	return post(h, "/v1/chat/completions", body, "Authorization", "Bearer "+k.Secret)
}

// TestKeyGroupsOnlyOwnAccounts 限定分组的密钥只用组里的号：组外的号优先级再高、会话已经绑在它上面也不用；
// 组里的号都接不了时回 503 并说明是这个密钥的分组里没号，不借组外的号。不限的密钥照常用全部的号。
func TestKeyGroupsOnlyOwnAccounts(t *testing.T) {
	h, p, scoped, open, tokens := groupRig(t)
	p.SetPriority("in", 5) // 组外的 out 优先级更高
	// 不限的密钥先把会话 c1 绑到 out 上
	if rec := chatWith(h, open, "c1"); rec.Code != 200 {
		t.Fatalf("不限的密钥: code=%d body=%s", rec.Code, rec.Body)
	}
	if got := tokens(); len(got) != 1 || got[0] != "Bearer at-out" {
		t.Fatalf("不限的密钥应先用优先级高的 out: %v", got)
	}
	for _, conv := range []string{"c1", "c2", "c3"} {
		if rec := chatWith(h, scoped, conv); rec.Code != 200 {
			t.Fatalf("限定 A 的密钥 %s: code=%d body=%s", conv, rec.Code, rec.Body)
		}
	}
	if got := tokens(); strings.Join(got, ",") != "Bearer at-in,Bearer at-in,Bearer at-in" {
		t.Fatalf("限定 A 的密钥只能用 in: %v", got)
	}

	p.Pause("in")
	rec := chatWith(h, scoped, "c4")
	if code, msg := openAIErr(t, rec); rec.Code != http.StatusServiceUnavailable || code != "no_healthy_account" || !strings.Contains(msg, "API key's groups") ||
		!strings.Contains(rec.Body.String(), "key_groups") {
		t.Fatalf("组里的号都接不了应回 503 说明分组: code=%d body=%s", rec.Code, rec.Body)
	}
	if got := tokens(); len(got) != 0 {
		t.Fatalf("不应借组外的号: %v", got)
	}
}

// TestKeyGroupsSlotWait 组里的号在途满了：排队等组里的号，组外有空闲的号也不用。
func TestKeyGroupsSlotWait(t *testing.T) {
	shortSlotPoll(t, 20*time.Millisecond)
	h, p, scoped, _, tokens := groupRig(t)
	p.SetMaxInFlight(1)
	p.SetInFlightWait(150 * time.Millisecond)
	p.Acquire("in")
	rec := chatWith(h, scoped, "c1")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "all accounts are busy") {
		t.Fatalf("应排队等满回 503: code=%d body=%s", rec.Code, rec.Body)
	}
	if got := tokens(); len(got) != 0 {
		t.Fatalf("排队时不应用组外的号: %v", got)
	}
}

// TestPriorityStickyReturns 高优先级的号满了，会话落到低优先级的号上；高优先级的号空出来后，下一次请求回到高优先级。
// 换号时先回这段对话用过的号，也只回最高那层的。
func TestPriorityStickyReturns(t *testing.T) {
	h, p, _, open, tokens := groupRig(t)
	p.SetPriority("out", 5) // in 是主力，out 是备用
	p.SetMaxInFlight(1)
	p.SetInFlightWait(0)
	p.Acquire("in")
	if rec := chatWith(h, open, "c1"); rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	p.Release("in")
	if rec := chatWith(h, open, "c1"); rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if got := tokens(); strings.Join(got, ",") != "Bearer at-out,Bearer at-in" {
		t.Fatalf("主力号满时用备用号，空出来后回主力号: %v", got)
	}
	// 用过的号 in、out 都记着；in 被选中时冷却了，换号回用过的号只回最高那层（此刻是 out 所在的层）。
	if a := h.pickWarm(session.ExtractKey([]byte(`{"metadata":{"conversation_id":"c1"}}`)), "glm-5.2", "", map[string]bool{"in": true}); a == nil || a.UID != "out" {
		t.Fatalf("in 试过以后回用过的 out: %v", a)
	}
	if a := h.pickWarm(session.ExtractKey([]byte(`{"metadata":{"conversation_id":"c1"}}`)), "glm-5.2", "", map[string]bool{}); a == nil || a.UID != "in" {
		t.Fatalf("应先回最高层的 in: %v", a)
	}
	// 只用过备用号的会话：主力号能接时不回备用号。
	p.Acquire("in")
	chatWith(h, open, "c2")
	p.Release("in")
	if a := h.pickWarm(session.ExtractKey([]byte(`{"metadata":{"conversation_id":"c2"}}`)), "glm-5.2", "", map[string]bool{}); a != nil {
		t.Fatalf("备用号不在最高层，不应回它: %v", a.UID)
	}
}
