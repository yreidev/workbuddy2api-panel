// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package session

import (
	"crypto/sha256"
	"slices"
	"testing"
	"time"
)

// warmRouter 可用号由 avail 决定，新分配只能挑 cheapest（模拟成本分层只留最便宜那一层）。
func warmRouter(avail *[]string, cheapest ...string) *Router {
	return New(Config{
		TTL:                time.Minute,
		AvailableForModel:  func(string, string, map[string]bool) []string { return *avail },
		AssignableForModel: func(string, string, map[string]bool) []string { return cheapest },
	})
}

// 绑定没了（请求失败解绑、闲置过期）时先回到这段对话用过的号，最近的优先；不受成本分层限制；
// 用过的号都用不了才按分配规则挑。
func TestRouterReturnsToWarmAccount(t *testing.T) {
	avail := []string{"u1", "u2", "u3"}
	r := warmRouter(&avail, "u1")
	r.Bind("k", "u3")
	r.Bind("k", "u2")
	r.Unbind("k")
	if uid, ok := r.ResolveForModel("k", "m", "", nil); !ok || uid != "u2" {
		t.Fatalf("应回到最近用过的 u2，got %q", uid)
	}
	r.Unbind("k")
	avail = []string{"u1", "u3"}
	if uid, _ := r.ResolveForModel("k", "m", "", nil); uid != "u3" {
		t.Fatalf("u2 用不了时回到更早用过的 u3，got %q", uid)
	}
	r.Unbind("k")
	avail = []string{"u1"}
	if uid, _ := r.ResolveForModel("k", "m", "", nil); uid != "u1" {
		t.Fatalf("用过的号都用不了时按分配规则挑，got %q", uid)
	}
	if uid, _ := r.ResolveForModel("new", "m", "", nil); uid != "u1" {
		t.Fatalf("新对话照旧按成本分层分配，got %q", uid)
	}

	// 绑定闲置过期：回到原来的号，不重新哈希分配。
	avail = []string{"u1", "u2", "u3"}
	r.Bind("idle", "u3")
	r.mu.Lock()
	r.entries["idle"] = entry{uid: "u3", lastActive: time.Now().Add(-2 * time.Minute)}
	r.mu.Unlock()
	if uid, _ := r.ResolveForModel("idle", "m", "", nil); uid != "u3" {
		t.Fatalf("绑定过期后应回到用过的 u3，got %q", uid)
	}
}

// 用过的号最近的在前、最多记 warmMax 个；返回的是副本；空键、没记过、过期的都返回 nil。
func TestRouterWarmUIDs(t *testing.T) {
	avail := []string{"u1"}
	r := warmRouter(&avail, "u1")
	for _, u := range []string{"u1", "u2", "u3", "u4"} {
		r.Bind("k", u)
	}
	if got := r.WarmUIDs("k"); !slices.Equal(got, []string{"u4", "u3", "u2"}) {
		t.Fatalf("WarmUIDs = %v", got)
	}
	r.Bind("k", "u3")
	got := r.WarmUIDs("k")
	if !slices.Equal(got, []string{"u3", "u4", "u2"}) {
		t.Fatalf("再用一次排到最前: %v", got)
	}
	got[0] = "changed"
	if r.WarmUIDs("k")[0] != "u3" {
		t.Fatal("应返回副本")
	}
	if r.WarmUIDs("") != nil || r.WarmUIDs("none") != nil {
		t.Fatal("空键、没记过的返回 nil")
	}
	k := sha256.Sum256([]byte("k"))
	r.mu.Lock()
	e := r.warm[k]
	e.last = time.Now().Add(-warmTTL)
	r.warm[k] = e
	r.mu.Unlock()
	if r.WarmUIDs("k") != nil {
		t.Fatal("过期的返回 nil")
	}
	r.Bind("k", "u5")
	if got := r.WarmUIDs("k"); !slices.Equal(got, []string{"u5"}) {
		t.Fatalf("过期后重新记，不带旧的: %v", got)
	}
	r.Bind("", "u1")
	r.Bind("k", "")
	if got := r.WarmUIDs("k"); !slices.Equal(got, []string{"u5"}) {
		t.Fatalf("空键或空号不记: %v", got)
	}
}

// 改动标记：名单变了算改动，同一个号同一小时内再成功不算；GC 清掉过期的。
func TestRouterWarmDirtyAndGC(t *testing.T) {
	avail := []string{"u1"}
	r := warmRouter(&avail, "u1")
	if r.takeWarmDirty() {
		t.Fatal("空的不应有改动")
	}
	r.Bind("k", "u1")
	if !r.takeWarmDirty() {
		t.Fatal("新记的算改动")
	}
	r.Bind("k", "u1")
	if r.takeWarmDirty() {
		t.Fatal("同一个号同一小时内再成功不算改动")
	}
	k := sha256.Sum256([]byte("k"))
	r.mu.Lock()
	e := r.warm[k]
	e.last = e.last.Add(-time.Hour)
	r.warm[k] = e
	r.mu.Unlock()
	r.Bind("k", "u1")
	if !r.takeWarmDirty() {
		t.Fatal("跨了整点算改动")
	}
	r.Bind("k", "u2")
	if !r.takeWarmDirty() {
		t.Fatal("换了号算改动")
	}
	r.markWarmDirty()
	if !r.takeWarmDirty() {
		t.Fatal("markWarmDirty 后应有改动")
	}
	r.gcOnce(time.Now().Add(warmTTL))
	if r.WarmUIDs("k") != nil || len(r.warm) != 0 {
		t.Fatal("GC 应清掉过期的用过的号")
	}
}

// 超过上限：先清过期的，再清最久没用的，降到九成。
func TestRouterWarmEvict(t *testing.T) {
	avail := []string{"u1"}
	r := warmRouter(&avail, "u1")
	now := time.Now()
	for i := 0; i < warmKeysMax; i++ {
		var k [32]byte
		k[0], k[1], k[2] = byte(i), byte(i>>8), byte(i>>16)
		r.warm[k] = warmEntry{uids: []string{"u1"}, last: now.Add(-time.Duration(i) * time.Second)}
	}
	r.Bind("new", "u2")
	if n := len(r.warm); n != warmKeysMax*9/10+1 {
		t.Fatalf("应降到九成再加上新的一条，剩 %d", n)
	}
	if got := r.WarmUIDs("new"); !slices.Equal(got, []string{"u2"}) {
		t.Fatalf("新记的应留下: %v", got)
	}
}

// 存盘再读回：名单和先后都在；过期的不存，读回时跳过过期、格式不对、空名单，超过 warmMax 的截断，
// 内存里已有的以内存为准，满了就停。nil 路由器（粘性关闭）一律空操作。
func TestRouterWarmSnapshotRestore(t *testing.T) {
	avail := []string{"u1"}
	r := warmRouter(&avail, "u1")
	r.Bind("a", "u1")
	r.Bind("a", "u2")
	r.Bind("b", "u3")
	r.Bind("old", "u4")
	k := sha256.Sum256([]byte("old"))
	r.mu.Lock()
	e := r.warm[k]
	e.last = time.Now().Add(-warmTTL)
	r.warm[k] = e
	r.mu.Unlock()
	snap := r.warmSnapshot()
	if len(snap) != 2 {
		t.Fatalf("过期的不存: %d", len(snap))
	}

	r2 := warmRouter(&avail, "u1")
	r2.Bind("b", "u9")
	long := warmSnap{K: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", U: []string{"x1", "x2", "x3", "x4"}, T: time.Now().Unix()}
	snap = append(snap,
		warmSnap{K: "!!", U: []string{"u1"}, T: time.Now().Unix()},
		warmSnap{K: "BAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", U: nil, T: time.Now().Unix()},
		warmSnap{K: "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", U: []string{"u1"}, T: time.Now().Add(-warmTTL).Unix()},
		long,
	)
	if n := r2.restoreWarm(snap); n != 2 {
		t.Fatalf("应读回 a 和截断后的长名单，got %d", n)
	}
	if got := r2.WarmUIDs("a"); !slices.Equal(got, []string{"u2", "u1"}) {
		t.Fatalf("读回的名单: %v", got)
	}
	if got := r2.WarmUIDs("b"); !slices.Equal(got, []string{"u9"}) {
		t.Fatalf("内存里已有的以内存为准: %v", got)
	}
	lk, _ := decodeKey(long.K)
	if got := r2.warm[lk].uids; !slices.Equal(got, []string{"x1", "x2", "x3"}) {
		t.Fatalf("超过 warmMax 应截断: %v", got)
	}

	full := warmRouter(&avail, "u1")
	for i := 0; i < warmKeysMax; i++ {
		var fk [32]byte
		fk[0], fk[1] = byte(i), byte(i>>8)
		fk[31] = 0xff
		full.warm[fk] = warmEntry{uids: []string{"u1"}, last: time.Now()}
	}
	if n := full.restoreWarm(snap[:1]); n != 0 {
		t.Fatalf("满了就不再读回，got %d", n)
	}

	var nilRouter *Router
	nilRouter.markWarmDirty()
	if nilRouter.takeWarmDirty() || nilRouter.warmSnapshot() != nil || nilRouter.restoreWarm(snap) != 0 {
		t.Fatal("nil 路由器应全是空操作")
	}
}
