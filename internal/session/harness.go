// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// harness.go 按对话轮（X-Conversation-Request-ID）跟踪内置 CLI 2.176.0 的
// extra_vars.harnessMetadata 步骤状态（stepIndex 与上一步的 X-Conversation-Message-ID）。
//
// 依据：WorkBuddy 桌面端 5.11.3 / 5.11.4 内置 CLI 2.176.0（codebuddy-lite-wb.mjs 的
// allocateHarnessStep / buildHarnessMetadata / withModelMessageCorrelation /
// startRetriedModelAttempt）：
//   - 同一对话轮（同 X-Conversation-Request-ID）内第 1 次模型调用 stepIndex=0、不带 previous；
//   - 同轮后续步（如工具调用回传继续调模型）stepIndex 依次递增，previous.conversationMessageId
//     指向该轮上一步的 X-Conversation-Message-ID；
//   - 单次模型调用失败后的重试（startRetriedModelAttempt）复用同一 messageId 与 stepIndex，
//     不推进步数；因此这里采用 Peek（发前读）+ Commit（成功后推进）两段式语义。
package session

import (
	"sync"
	"time"
)

const (
	// defaultHarnessTTL 与 defaultTurnLinkTTL 一致，覆盖一轮内最长工具调用间隔。
	defaultHarnessTTL = 2 * time.Hour
	// defaultHarnessMax 记录上限，超限时先清过期条目，再随机裁至九成。
	defaultHarnessMax = 50000
)

// HarnessStep 一次模型调用在所属对话轮内的步骤元数据。
type HarnessStep struct {
	Index             int    // 从 0 起算的步骤号
	PreviousMessageID string // 同轮上一步的 X-Conversation-Message-ID；Index == 0 时为空
}

type harnessEntry struct {
	nextIndex     int
	lastMessageID string
	exp           time.Time
}

// HarnessTracker 并发安全地按 X-Conversation-Request-ID 记录每轮下一步的 stepIndex 与 previous。
// 零值或 nil 接收者安全可用（退化为每条都返回 Index=0）。
type HarnessTracker struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]harnessEntry
	now     func() time.Time
}

// NewHarnessTracker 用默认 TTL 与容量上限创建步骤跟踪表。
func NewHarnessTracker() *HarnessTracker {
	return &HarnessTracker{
		ttl:     defaultHarnessTTL,
		max:     defaultHarnessMax,
		entries: map[string]harnessEntry{},
		now:     time.Now,
	}
}

func (t *HarnessTracker) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// Peek 读取 convReqID 当前应发的步骤信息，不推进状态。
// 重试、换号、入口切换或排队重发期间重复调用得到一致的 stepIndex 与 previous。
func (t *HarnessTracker) Peek(convReqID string) HarnessStep {
	if t == nil || convReqID == "" {
		return HarnessStep{}
	}
	now := t.clock()
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.entries[convReqID]; ok && now.Before(e.exp) {
		return HarnessStep{Index: e.nextIndex, PreviousMessageID: e.lastMessageID}
	}
	return HarnessStep{}
}

// Commit 在本轮一次模型调用成功后推进 stepIndex，并记下本次实际发出的 messageID
// 供同轮下一步作为 previous.conversationMessageId。重复提交同一 messageID 幂等。
func (t *HarnessTracker) Commit(convReqID, messageID string) {
	if t == nil || convReqID == "" || messageID == "" {
		return
	}
	now := t.clock()
	ttl := t.ttl
	if ttl <= 0 {
		ttl = defaultHarnessTTL
	}
	maxEntries := t.max
	if maxEntries <= 0 {
		maxEntries = defaultHarnessMax
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = map[string]harnessEntry{}
	}
	if e, ok := t.entries[convReqID]; ok && now.Before(e.exp) {
		if e.lastMessageID == messageID {
			e.exp = now.Add(ttl)
			t.entries[convReqID] = e
			return
		}
		e.nextIndex++
		e.lastMessageID = messageID
		e.exp = now.Add(ttl)
		t.entries[convReqID] = e
		return
	}
	if len(t.entries) >= maxEntries {
		t.evictLocked(now, maxEntries)
	}
	t.entries[convReqID] = harnessEntry{
		nextIndex:     1,
		lastMessageID: messageID,
		exp:           now.Add(ttl),
	}
}

func (t *HarnessTracker) evictLocked(now time.Time, maxEntries int) {
	for k, e := range t.entries {
		if !now.Before(e.exp) {
			delete(t.entries, k)
		}
	}
	target := maxEntries * 9 / 10
	for k := range t.entries {
		if len(t.entries) <= target {
			break
		}
		delete(t.entries, k)
	}
}
