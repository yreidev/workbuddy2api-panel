// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package session

import (
	"reflect"
	"testing"
	"time"
)

// waitCount 轮询直到 Count 满足条件（最长 2s，每 5ms）。
func waitCount(t *testing.T, r *Router, want func(int) bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if want(r.Count()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s（count=%d）", what, r.Count())
}

// TestNewDefaults 零值/负值配置按源码补默认（Store=Noop、TTL 30m、GC 5m）。
func TestNewDefaults(t *testing.T) {
	r := New(Config{})
	if r.cfg.Store == nil {
		t.Error("Store nil 应替换为 Noop")
	}
	if r.cfg.TTL != 30*time.Minute {
		t.Errorf("TTL = %v，应为 30m", r.cfg.TTL)
	}
	if r.cfg.GCInterval != 5*time.Minute {
		t.Errorf("GCInterval = %v，应为 5m", r.cfg.GCInterval)
	}

	neg := New(Config{TTL: -time.Second, GCInterval: -time.Second})
	if neg.cfg.TTL != 30*time.Minute || neg.cfg.GCInterval != 5*time.Minute {
		t.Errorf("负值应回落默认: %v/%v", neg.cfg.TTL, neg.cfg.GCInterval)
	}

	kept := New(Config{TTL: time.Minute, GCInterval: time.Second})
	if kept.cfg.TTL != time.Minute || kept.cfg.GCInterval != time.Second {
		t.Errorf("显式配置不应被覆盖: %v/%v", kept.cfg.TTL, kept.cfg.GCInterval)
	}
}

// TestStartStopGC 后台 GC：到期绑定被清、重复 StartGC 只起一个、StopGC 幂等且停后不再清理。
func TestStartStopGC(t *testing.T) {
	st := newCountingStore()
	r := New(Config{
		TTL:        20 * time.Millisecond,
		GCInterval: 5 * time.Millisecond,
		Store:      st,
		Available:  func() []string { return []string{"a1"} },
	})
	t.Cleanup(r.StopGC)

	r.Bind("c1", "a1")
	r.Bind("c2", "a1")
	if r.Count() != 2 {
		t.Fatalf("初始绑定 = %d", r.Count())
	}

	r.StartGC()
	r.StartGC() // 幂等：不应起第二个 goroutine
	waitCount(t, r, func(n int) bool { return n == 0 }, "GC 清掉到期绑定")

	// 停后不再清理：手工塞一条已过期绑定，等几个 GC 周期仍应在。
	r.StopGC()
	r.StopGC() // 幂等
	r.mu.Lock()
	r.entries["stale"] = entry{uid: "a1", lastActive: time.Now().Add(-time.Hour)}
	r.mu.Unlock()
	time.Sleep(40 * time.Millisecond)
	if r.Count() != 1 {
		t.Errorf("StopGC 后不应再清理，count = %d", r.Count())
	}

	// StopGC 后可重新启动（stop 已置 nil）。
	r.StartGC()
	waitCount(t, r, func(n int) bool { return n == 0 }, "重新启动的 GC 清掉过期绑定")
}

// TestLoadFromStoreEdges 无绑定 no-op；本地已有绑定优先保留。
func TestLoadFromStoreEdges(t *testing.T) {
	st := newCountingStore()
	r := routerWith(st, []string{"local", "a2"}, time.Minute)
	r.LoadFromStore() // 无绑定
	if r.Count() != 0 {
		t.Errorf("无绑定应保持 0，got %d", r.Count())
	}

	r.Bind("c1", "local")
	st.binds["c1"] = "remote"
	st.binds["c2"] = "a2"
	r.LoadFromStore()
	if got, _ := r.Resolve("c1"); got != "local" {
		t.Errorf("本地已有绑定不应被覆盖，got %q", got)
	}
	if got, _ := r.Resolve("c2"); got != "a2" {
		t.Errorf("新绑定应恢复，got %q", got)
	}
}

// TestAvailableSliceFallbacks AvailableForModel 优先、回落 Available、全空返回 nil。
func TestAvailableSliceFallbacks(t *testing.T) {
	var gotModel, gotRealm string
	r := New(Config{
		AvailableForModel: func(model, realm string, _ map[string]bool) []string {
			gotModel, gotRealm = model, realm
			return []string{"m1"}
		},
		Available: func() []string { return []string{"a1"} },
	})
	if got := r.availableSlice("model-x", "global", nil); !reflect.DeepEqual(got, []string{"m1"}) {
		t.Errorf("应优先 AvailableForModel，got %v", got)
	}
	if gotModel != "model-x" || gotRealm != "global" {
		t.Errorf("回调参数 = %q/%q", gotModel, gotRealm)
	}

	r2 := New(Config{Available: func() []string { return []string{"a1"} }})
	if got := r2.availableSlice("m", "", nil); !reflect.DeepEqual(got, []string{"a1"}) {
		t.Errorf("应回落 Available，got %v", got)
	}

	r3 := New(Config{})
	if got := r3.availableSlice("m", "", nil); got != nil {
		t.Errorf("全空应返回 nil，got %v", got)
	}
}

// TestResolveNoAvailable 无可用账号：返回 false；失效绑定被清掉。
func TestResolveNoAvailable(t *testing.T) {
	r := New(Config{TTL: time.Minute, Available: func() []string { return nil }})
	if uid, ok := r.ResolveForModel("c1", "", "", nil); ok || uid != "" {
		t.Fatalf("无可用账号应返回 false，got %q/%v", uid, ok)
	}

	// 先绑定，再让账号不可用：慢路径应删除失效绑定并返回 false。
	available := true
	r2 := New(Config{TTL: time.Minute, Available: func() []string {
		if available {
			return []string{"a1"}
		}
		return nil
	}})
	if uid, ok := r2.Resolve("c1"); !ok || uid != "a1" {
		t.Fatalf("首次分配 = %q/%v", uid, ok)
	}
	available = false
	if _, ok := r2.Resolve("c1"); ok {
		t.Fatal("绑定号不可用且无替代时应返回 false")
	}
	if r2.Count() != 0 {
		t.Errorf("失效绑定应被删除，count=%d", r2.Count())
	}
}

// TestUserContentSignature 会话签名：字符串、null、标量、文本数组、含图片数组、
// 坏 part 的逐个形态。
func TestUserContentSignature(t *testing.T) {
	cases := []struct {
		name    string
		content any
		want    string
	}{
		{"字符串", "hi", "hi"},
		{"null", nil, ""},
		{"数字标量", 42, ""},
		{"文本数组", []any{map[string]any{"type": "text", "text": "abc"}, map[string]any{"type": "text", "text": "def"}}, "abcdef"},
		{"空 type 当文本", []any{map[string]any{"text": "x"}}, "x"},
		{"含图片", []any{map[string]any{"type": "text", "text": "看图"}, map[string]any{"type": "image_url"}}, "看图\n[image_url]"},
		{"纯图片", []any{map[string]any{"type": "image_url"}}, "[image_url]"},
		{"坏 part", []any{42}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := userContentSignature(tc.content); got != tc.want {
				t.Errorf("userContentSignature = %q，应为 %q", got, tc.want)
			}
		})
	}
}

// TestMessageTextFallback 非字符串/数组的 content 返回空串；数组里非对象 part 忽略。
func TestMessageTextFallback(t *testing.T) {
	if got := messageText(42); got != "" {
		t.Errorf("标量 content = %q", got)
	}
	if got := messageText([]any{42, map[string]any{"text": "a"}}); got != "a" {
		t.Errorf("数组 content = %q", got)
	}
	if got := messageText("raw"); got != "raw" {
		t.Errorf("字符串 content = %q", got)
	}
}

// TestExpiredHelper expired 的边界。
func TestExpiredHelper(t *testing.T) {
	now := time.Now()
	if expired(entry{lastActive: now.Add(-2 * time.Minute)}, now, time.Minute) != true {
		t.Error("超过 TTL 应过期")
	}
	if expired(entry{lastActive: now}, now, time.Minute) {
		t.Error("刚活动不应过期")
	}
}

// TestAssignableForModel 新分配用 AssignableForModel，已绑定会话的续用只按 AvailableForModel 校验：
// 绑在不在最便宜层的号上的会话不被挪走；没注入时分配也用 AvailableForModel。
func TestAssignableForModel(t *testing.T) {
	r := New(Config{
		TTL:                time.Minute,
		AvailableForModel:  func(model, realm string, _ map[string]bool) []string { return []string{"paid", "free"} },
		AssignableForModel: func(model, realm string, _ map[string]bool) []string { return []string{"free"} },
	})
	if uid, ok := r.ResolveForModel("c1", "m", "", nil); !ok || uid != "free" {
		t.Fatalf("新会话应从 AssignableForModel 里挑，got %q/%v", uid, ok)
	}
	r.Bind("c2", "paid")
	if uid, ok := r.ResolveForModel("c2", "m", "", nil); !ok || uid != "paid" {
		t.Fatalf("已绑定且仍可用的会话不该换号，got %q/%v", uid, ok)
	}
	r2 := New(Config{TTL: time.Minute, AvailableForModel: func(model, realm string, _ map[string]bool) []string { return []string{"a1"} }})
	if got := r2.assignableSlice("m", "", nil); !reflect.DeepEqual(got, []string{"a1"}) {
		t.Fatalf("没注入 AssignableForModel 时应回落 AvailableForModel，got %v", got)
	}
}
