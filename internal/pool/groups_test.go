// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestGroupCRUD 建、改、删分组：同名（不区分大小写）不让建也不让改成；删组后号退出这个组；列表带号数。
func TestGroupCRUD(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	a, err := p.CreateGroup("主力", "")
	if err != nil || a.ID == "" || a.CreatedAt.IsZero() {
		t.Fatalf("建分组: %+v %v", a, err)
	}
	b, _ := p.CreateGroup("Backup", "备用号")
	if _, err := p.CreateGroup("backup", ""); !errors.Is(err, ErrGroupNameTaken) {
		t.Fatalf("同名应报 ErrGroupNameTaken: %v", err)
	}
	if _, err := p.UpdateGroup(a.ID, "BACKUP", ""); !errors.Is(err, ErrGroupNameTaken) {
		t.Fatalf("改成别的组的名字应报 ErrGroupNameTaken: %v", err)
	}
	if g, err := p.UpdateGroup(b.ID, "backup", "改了"); err != nil || g.Name != "backup" || g.Note != "改了" {
		t.Fatalf("改自己的大小写和备注应成功: %+v %v", g, err)
	}
	if _, err := p.UpdateGroup("nope", "x", ""); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("改不存在的分组: %v", err)
	}
	if found, err := p.SetAccountGroups("u1", []string{a.ID, b.ID}); !found || err != nil {
		t.Fatalf("SetAccountGroups: %v %v", found, err)
	}
	if gs := p.Groups(); len(gs) != 2 || gs[0].ID != a.ID || gs[0].Accounts != 1 || gs[1].Accounts != 1 {
		t.Fatalf("列表 = %+v", gs)
	}
	if err := p.DeleteGroup(a.ID); err != nil || p.HasGroup(a.ID) || !p.HasGroup(b.ID) {
		t.Fatalf("删分组: %v", err)
	}
	if st, _ := p.Status("u1"); !slices.Equal(st.Groups, []string{b.ID}) {
		t.Fatalf("删组后 u1 应只剩 %s: %v", b.ID, st.Groups)
	}
	if err := p.DeleteGroup(a.ID); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("删两次: %v", err)
	}
}

// TestAccountGroupMembership 号进出分组：整份替换、单个加进退出（重复不动），号或分组不存在时报出来。
func TestAccountGroupMembership(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	g, _ := p.CreateGroup("A", "")
	if found, err := p.SetAccountGroups("u1", []string{g.ID, "nope"}); !found || !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("有不存在的分组: %v %v", found, err)
	}
	if found, _ := p.SetAccountGroups("gone", nil); found {
		t.Fatal("号不存在应返回 false")
	}
	for _, leave := range []bool{false, false, true, true} {
		if found, err := p.JoinGroup("u1", g.ID, leave); !found || err != nil {
			t.Fatalf("JoinGroup leave=%v: %v %v", leave, found, err)
		}
		if st, _ := p.Status("u1"); len(st.Groups) != map[bool]int{false: 1, true: 0}[leave] {
			t.Fatalf("leave=%v 后分组 = %v", leave, st.Groups)
		}
	}
	if _, err := p.JoinGroup("u1", "nope", false); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("分组不存在: %v", err)
	}
	if found, err := p.JoinGroup("gone", g.ID, false); found || err != nil {
		t.Fatalf("号不存在: %v %v", found, err)
	}
}

// TestUIDsOutsideGroups 密钥限定分组时要跳过的号：不在任何一个限定分组里的（没分组的也算）。
func TestUIDsOutsideGroups(t *testing.T) {
	p := New("")
	for _, uid := range []string{"a", "b", "ab", "none"} {
		p.Add(&auth.Auth{UID: uid})
	}
	ga, _ := p.CreateGroup("A", "")
	gb, _ := p.CreateGroup("B", "")
	p.SetAccountGroups("a", []string{ga.ID})
	p.SetAccountGroups("b", []string{gb.ID})
	p.SetAccountGroups("ab", []string{ga.ID, gb.ID})
	if got := p.UIDsOutsideGroups([]string{ga.ID}); len(got) != 2 || !got["b"] || !got["none"] {
		t.Fatalf("只限 A 时跳过 = %v", got)
	}
	if got := p.UIDsOutsideGroups([]string{ga.ID, gb.ID}); len(got) != 1 || !got["none"] {
		t.Fatalf("限 A、B 时跳过 = %v", got)
	}
}

// TestPriorityTiers 严格分层：只在优先级最高、此刻能接的那层挑；那层冷却、在途满、积分保底拦着时才轮到下一层；
// 优先级先于成本分层。
func TestPriorityTiers(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "main"})
	p.Add(&auth.Auth{UID: "backup"})
	p.SetCredits("main", 1000, 0)
	p.SetCredits("backup", 1000, 0)
	if p.SetPriority("gone", 1) {
		t.Fatal("号不存在应返回 false")
	}
	p.SetPriority("backup", 5)
	if st, _ := p.Status("backup"); st.Priority != 5 {
		t.Fatalf("Status.Priority = %d", st.Priority)
	}
	pickN := func(model string) map[string]int {
		got := map[string]int{}
		for range 20 {
			a := p.PickExcludingForRealm(nil, model, "")
			if a == nil {
				got["nil"]++
				continue
			}
			got[a.UID]++
			p.mu.Lock()
			p.byUID[a.UID].lastUsed = time.Time{} // 不让防撞号把下一次挤到别的号
			p.mu.Unlock()
		}
		return got
	}
	// 主力号实测收费、备用号实测免费：优先级先于成本，照样只用主力号。
	p.NoteModelCost("main", "glm-5.2", 2, 1000)
	p.NoteModelCost("backup", "glm-5.2", 0, 1000)
	if got := pickN("glm-5.2"); got["main"] != 20 {
		t.Fatalf("应只用主力号: %v", got)
	}
	// 主力号在途满：轮到备用号。
	p.SetMaxInFlight(1)
	p.Acquire("main")
	if got := pickN("glm-5.2"); got["backup"] != 20 {
		t.Fatalf("主力号满了应用备用号: %v", got)
	}
	p.Release("main")
	// 主力号冷却：轮到备用号。
	p.Cooldown("main", CoolSoft, time.Hour, "429")
	if got := pickN("glm-5.2"); got["backup"] != 20 {
		t.Fatalf("主力号冷却应用备用号: %v", got)
	}
	p.Revive("main")
	// 主力号触底、模型收费：积分保底拦着，轮到备用号；两层都触底时原样不分层（再由保底拦掉）。
	p.SetCreditFloor(100)
	p.SetCredits("main", 10, 0)
	if got := pickN("glm-5.2"); got["backup"] != 20 {
		t.Fatalf("主力号触底应用备用号: %v", got)
	}
	if uids := p.WeightedAvailableUIDsForModelRealm("glm-5.2", "", nil); !slices.Equal(uids, []string{"backup"}) {
		t.Fatalf("会话候选也应只有备用号: %v", uids)
	}
	p.SetCredits("backup", 10, 0)
	p.NoteModelCost("main", "kimi-k3", 2, 1000)
	p.NoteModelCost("backup", "kimi-k3", 2, 1000)
	if uids := p.WeightedAvailableUIDsForModelRealm("kimi-k3", "", nil); len(uids) != 2 {
		t.Fatalf("都触底时会话候选不分层: %v", uids)
	}
}

// TestSessionCandidatesTierAndSkip 会话候选（续用、分配、换号回用过的号）只有最高那层，skip 里的号不算。
func TestSessionCandidatesTierAndSkip(t *testing.T) {
	p := New("")
	for _, uid := range []string{"a1", "a2", "b1"} {
		p.Add(&auth.Auth{UID: uid})
	}
	p.SetPriority("b1", 1)
	if uids := p.WeightedAvailableUIDsForModelRealm("m", "", nil); !slices.Equal(uids, []string{"a1", "a2"}) {
		t.Fatalf("续用候选 = %v", uids)
	}
	if uids := p.CheapestWeightedUIDsForModelRealm("m", "", map[string]bool{"a1": true}); !slices.Equal(uids, []string{"a2"}) {
		t.Fatalf("跳过 a1 后分配候选 = %v", uids)
	}
	skip := map[string]bool{"a1": true, "a2": true}
	if uids := p.CheapestWeightedUIDsForModelRealm("m", "", skip); !slices.Equal(uids, []string{"b1"}) {
		t.Fatalf("最高层都跳过时轮到下一层: %v", uids)
	}
	if !p.InTopTier("a1", "m", "", nil) || p.InTopTier("b1", "m", "", nil) || !p.InTopTier("b1", "m", "", skip) || p.InTopTier("a1", "m", "", skip) {
		t.Fatal("InTopTier 不对")
	}
}

// TestGroupsPersist 分组、号的分组和优先级随状态文件落盘，重启恢复。
func TestGroupsPersist(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "state.json")
	p := New(fp)
	p.Add(&auth.Auth{UID: "u1"})
	g, _ := p.CreateGroup("主力", "常用")
	p.SetAccountGroups("u1", []string{g.ID})
	p.SetPriority("u1", 3)
	p.mu.Lock()
	p.saveLocked()
	p.mu.Unlock()
	p2 := New(fp)
	gs := p2.Groups()
	st, _ := p2.Status("u1")
	if len(gs) != 1 || gs[0].ID != g.ID || gs[0].Name != "主力" || gs[0].Note != "常用" || !gs[0].CreatedAt.Equal(g.CreatedAt) || gs[0].Accounts != 1 || !slices.Equal(st.Groups, []string{g.ID}) || st.Priority != 3 {
		t.Fatalf("重启后 groups=%+v status=%+v", gs, st)
	}
}
