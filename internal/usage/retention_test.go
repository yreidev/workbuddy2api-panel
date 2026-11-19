// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tokens(n int64) Delta { return Delta{TotalTokens: n, HasTotal: true} }

// 保留天数：整段早于 N 天的桶删掉，跨过分界的留着；0 = 永久；负数当 0。
func TestRetentionPrunesWholeBuckets(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 30, 0, 0, time.Local)
	r := New("")
	r.Add(now, "cn", "u1", "m", tokens(1), true)                                        // 今天
	r.Add(now.AddDate(0, 0, -7).Add(-30*time.Minute), "cn", "u1", "m", tokens(2), true) // 7 天前 12:00 那个小时：结束在分界之后
	r.Add(now.AddDate(0, 0, -7).Add(-2*time.Hour), "cn", "u1", "m", tokens(4), true)    // 7 天前 10:00 那个小时：整段在分界之前
	// 分界是 10-21 12:30：10-20 的日桶到 10-21 零点结束，整段在分界之前；10-21 的到 10-22 零点，跨过分界。
	r.buckets[bucketKey("d:2026-10-20", "cn", "u1", "m", "", "")] = &bucket{Scope: "d:2026-10-20", Realm: "cn", UID: "u1", Model: "m", TT: 8}
	r.buckets[bucketKey("d:2026-10-21", "cn", "u1", "m", "", "")] = &bucket{Scope: "d:2026-10-21", Realm: "cn", UID: "u1", Model: "m", TT: 16}
	r.buckets[bucketKey("x:bad", "cn", "u1", "m", "", "")] = &bucket{Scope: "x:bad"} // 时间解析不了的不动

	r.SetRetentionDays(0)
	if n := len(r.buckets); n != 6 {
		t.Fatalf("0 = 永久，不删: %d", n)
	}
	r.mu.Lock()
	r.retentionDays = 7
	r.mu.Unlock()
	r.pruneExpired(now)
	var left int64
	for _, b := range r.buckets {
		left += b.TT
	}
	if len(r.buckets) != 4 || left != 1+2+16 || r.revision == r.savedRevision {
		t.Fatalf("应删掉整段在 7 天前的两个桶: 剩 %d 个、合计 %d", len(r.buckets), left)
	}
	if st := r.StoreStats(); st.RetentionDays != 7 || st.Buckets != 4 || st.OldestDay != "2026-10-21" {
		t.Fatalf("状态不对: %+v", st)
	}

	r.SetRetentionDays(-5)
	if st := r.StoreStats(); st.RetentionDays != 0 {
		t.Fatalf("负数当 0: %+v", st)
	}
}

// 后台循环每次刷盘前按保留天数清一次。
func TestRetentionInBackgroundLoop(t *testing.T) {
	old := flushInterval
	flushInterval = time.Millisecond
	t.Cleanup(func() { flushInterval = old })
	r := New("")
	r.Add(time.Now().AddDate(0, 0, -3), "cn", "u1", "m", tokens(1), true)
	r.Add(time.Now(), "cn", "u1", "m", tokens(1), true)
	r.mu.Lock()
	r.retentionDays = 1
	r.mu.Unlock()
	r.Start()
	defer r.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for r.StoreStats().Buckets != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("后台没按天数清: %+v", r.StoreStats())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// 清空：桶全删并马上落盘；状态里的文件大小是 usage.json 的大小。nil 记录器的方法是空操作。
func TestClearAndStoreStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	r := New(path)
	r.Add(time.Now(), "cn", "u1", "m", tokens(5), true)
	r.Save()
	if st := r.StoreStats(); st.Buckets != 1 || st.Bytes == 0 || st.OldestDay != time.Now().Format(dayLayout) {
		t.Fatalf("清空前: %+v", st)
	}
	if err := r.Clear(); err != nil {
		t.Fatal(err)
	}
	if st := r.StoreStats(); st.Buckets != 0 || st.OldestDay != "" {
		t.Fatalf("清空后: %+v", st)
	}
	if again := New(path); len(again.buckets) != 0 {
		t.Fatalf("清空应马上落盘，重新载入还有 %d 个桶", len(again.buckets))
	}

	// 落盘失败时清空回报错误：usage.json 的位置是个目录。
	dir := filepath.Join(t.TempDir(), "usage.json")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := &Recorder{path: dir, buckets: map[string]*bucket{}}
	if err := bad.Clear(); err == nil {
		t.Fatal("落盘失败应回报错误")
	}

	var nilRec *Recorder
	nilRec.SetRetentionDays(3)
	if nilRec.Clear() != nil || nilRec.StoreStats() != (StoreStats{}) {
		t.Fatal("nil 记录器的方法应是空操作")
	}
	if st := New("").StoreStats(); st.Bytes != 0 {
		t.Fatalf("不落盘时文件大小为 0: %+v", st)
	}
}
