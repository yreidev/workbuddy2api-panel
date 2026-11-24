// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package session

import (
	"fmt"
	"testing"
	"time"
)

func TestHarnessTrackerLifecycle(t *testing.T) {
	cur := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	tr := NewHarnessTracker()
	tr.now = func() time.Time { return cur }

	// 新轮首步：Index=0、无 previous；多次 Peek（重试/换号未成功）不推进步数。
	if got := tr.Peek("turn-1"); got != (HarnessStep{Index: 0, PreviousMessageID: ""}) {
		t.Fatalf("首步 Peek = %+v", got)
	}
	if got := tr.Peek("turn-1"); got != (HarnessStep{Index: 0, PreviousMessageID: ""}) {
		t.Fatalf("未 Commit 时重复 Peek 不应推进 = %+v", got)
	}

	// 第 0 步成功后 Commit，第 1 步拿到 Index=1 与 previous=msg-0。
	tr.Commit("turn-1", "msg-0")
	if got := tr.Peek("turn-1"); got != (HarnessStep{Index: 1, PreviousMessageID: "msg-0"}) {
		t.Fatalf("第 1 步 Peek = %+v", got)
	}

	// 同一个 messageID 重复 Commit 幂等，不重复递增。
	tr.Commit("turn-1", "msg-0")
	if got := tr.Peek("turn-1"); got != (HarnessStep{Index: 1, PreviousMessageID: "msg-0"}) {
		t.Fatalf("重复 Commit 同一 messageID 不应递增 = %+v", got)
	}

	// 第 1 步成功后 Commit，第 2 步拿到 Index=2 与 previous=msg-1。
	tr.Commit("turn-1", "msg-1")
	if got := tr.Peek("turn-1"); got != (HarnessStep{Index: 2, PreviousMessageID: "msg-1"}) {
		t.Fatalf("第 2 步 Peek = %+v", got)
	}

	// 另一轮独立计数。
	if got := tr.Peek("turn-2"); got != (HarnessStep{Index: 0, PreviousMessageID: ""}) {
		t.Fatalf("新轮应从 0 起算 = %+v", got)
	}

	// 超过 TTL 后过期重置回 0。
	cur = cur.Add(defaultHarnessTTL + time.Second)
	if got := tr.Peek("turn-1"); got != (HarnessStep{Index: 0, PreviousMessageID: ""}) {
		t.Fatalf("过期后应重置回 0 = %+v", got)
	}
}

func TestHarnessTrackerEvictionAndNilSafe(t *testing.T) {
	// nil 与空键安全。
	var nilTracker *HarnessTracker
	if got := nilTracker.Peek("turn-1"); got != (HarnessStep{}) {
		t.Fatalf("nil Peek = %+v", got)
	}
	nilTracker.Commit("turn-1", "msg-0")

	tr := &HarnessTracker{max: 10, ttl: time.Hour, now: time.Now}
	if got := tr.Peek(""); got != (HarnessStep{}) {
		t.Fatalf("空键 Peek = %+v", got)
	}
	tr.Commit("", "msg-0")
	tr.Commit("turn-x", "")
	if len(tr.entries) != 0 {
		t.Fatalf("空参数不应写入: %v", tr.entries)
	}
	for i := 0; i < 15; i++ {
		tr.Commit(fmt.Sprintf("t-%d", i), fmt.Sprintf("m-%d", i))
	}
	if len(tr.entries) > 10 {
		t.Fatalf("超过容量上限未淘汰: len=%d", len(tr.entries))
	}
}
