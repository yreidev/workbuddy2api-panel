// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"time"
)

const (
	maxSlotWaiters    = 512
	maxKeySlotWaiters = 64
)

func slotWaitExpired() error {
	return &Error{Status: 503, Code: "account_queue_timeout", Message: "等待平台账号空位超时"}
}

func (m *Manager) joinQueueLocked(key string) error {
	if m.waiters >= maxSlotWaiters || m.waitersByKey[key] >= maxKeySlotWaiters {
		return &Error{Status: 429, Code: "account_queue_full", Message: "平台账号等待队列已满"}
	}
	m.waiters++
	m.waitersByKey[key]++
	return nil
}

func (m *Manager) leaveQueueLocked(key string) {
	m.waiters--
	m.waitersByKey[key]--
	if m.waitersByKey[key] == 0 {
		delete(m.waitersByKey, key)
	}
}

func waitForSlot(ctx context.Context, changed <-chan struct{}, budget time.Duration) error {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return slotWaitExpired()
	case <-changed:
		return nil
	}
}
