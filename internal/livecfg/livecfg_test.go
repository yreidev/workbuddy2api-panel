// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package livecfg

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// TestNewAndLoad New 后 Load 拿到同一快照。
func TestNewAndLoad(t *testing.T) {
	want := Snapshot{
		APIKey:               "k1",
		SoftCooldown:         90 * time.Second,
		SanitizeFingerprints: true,
		RecordClientInfo:     true,
		CrossRealm:           CrossRealmMerge,
		UpdateCheck:          true,
		ModelRoutes:          []ModelRoute{{Match: "claude-*", Target: "glm-5.2"}},
	}
	h := New(want)
	if got := h.Load(); !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v，应为 %+v", got, want)
	}
}

// TestStoreReplacesSnapshot Store 后 Load 拿到新值，旧快照整体被替换。
func TestStoreReplacesSnapshot(t *testing.T) {
	h := New(Snapshot{APIKey: "old", SoftCooldown: time.Minute})
	h.Store(Snapshot{APIKey: "new", CrossRealm: CrossRealmCNFirst})
	got := h.Load()
	if got.APIKey != "new" || got.CrossRealm != CrossRealmCNFirst || got.SoftCooldown != 0 {
		t.Errorf("Store 后 = %+v", got)
	}
}

// TestNilAndZeroHolderLoad nil Holder 与未 Store 的零值 Holder 都返回零值快照，不 panic。
func TestNilAndZeroHolderLoad(t *testing.T) {
	var nilHolder *Holder
	if got := nilHolder.Load(); !reflect.DeepEqual(got, Snapshot{}) {
		t.Errorf("nil Holder = %+v，应为零值", got)
	}
	var zero Holder
	if got := zero.Load(); !reflect.DeepEqual(got, Snapshot{}) {
		t.Errorf("零值 Holder = %+v，应为零值", got)
	}
}

// TestConcurrentLoadStore 并发读写在 -race 下无数据竞争，读到的永远是完整快照。
func TestConcurrentLoadStore(t *testing.T) {
	h := New(Snapshot{APIKey: "v0"})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.Store(Snapshot{APIKey: "v", SoftCooldown: time.Duration(i*100 + j)})
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if s := h.Load(); s.APIKey != "v0" && s.APIKey != "v" {
					t.Errorf("读到不完整快照: %+v", s)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := h.Load(); got.APIKey != "v" {
		t.Errorf("最终快照 = %+v", got)
	}
}

// TestNormalizeHiddenModels 带 cn: / global: 前缀的只藏那个域，裸名两个域都藏；空白条目去掉；
// 别的冒号前缀当成名字的一部分。
func TestNormalizeHiddenModels(t *testing.T) {
	if got := NormalizeHiddenModels(nil); got != nil {
		t.Errorf("空列表应为 nil，got %v", got)
	}
	if got := NormalizeHiddenModels([]string{" ", ""}); got != nil {
		t.Errorf("只有空白条目应为 nil，got %v", got)
	}
	got := NormalizeHiddenModels([]string{" glm-5 ", "cn:deepseek-v4", "global:gpt-5", "vendor:x"})
	want := []string{"cn:glm-5", "global:glm-5", "cn:deepseek-v4", "global:gpt-5", "cn:vendor:x", "global:vendor:x"}
	if len(got) != len(want) {
		t.Fatalf("got %v，应为 %v", got, want)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("缺 %s：%v", k, got)
		}
	}
	if _, ok := got["global:deepseek-v4"]; ok {
		t.Error("cn: 前缀的条目不应藏国际版")
	}
}
