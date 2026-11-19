// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type accountCatalogAdapter struct{ *fakeAdapter }

func (a accountCatalogAdapter) Models(_ context.Context, _ string, c Credential) ([]Model, error) {
	return []Model{{ID: c.UID}}, nil
}

func TestMissingModelDoesNotSpendRotationBudget(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(fmt.Sprint("已缓存_", warm), func(t *testing.T) {
			m, f := testManager(t, "")
			m.adapters["qoder"] = accountCatalogAdapter{f}
			first := importFake(t, m, "qoder:cn", "first")
			second := importFake(t, m, "qoder:cn", "second")
			if warm {
				if _, err := m.Models(t.Context(), first, "qoder:cn", false); err != nil {
					t.Fatal(err)
				}
			}
			req := Request{Target: "qoder:cn", Model: "second", Body: []byte(plainChat), MaxRotate: 1}
			result, err := m.Execute(t.Context(), req, discardFrame)
			if err != nil || result.Attempts != 1 || result.AccountID != second || len(m.Usage()) != 1 || !m.running[first].coolUntil.IsZero() {
				t.Fatal("目录不匹配占用轮换或记账", result, err, m.Usage())
			}
			req.Model = "missing"
			result, err = m.Execute(t.Context(), req, discardFrame)
			var e *Error
			if !errors.As(err, &e) || e.Status != 404 || result.Attempts != 0 || len(m.Usage()) != 1 {
				t.Fatal("所有目录不匹配返回错误或计量错误", result, err)
			}
			m.running[first].catalogs[req.Target] = modelCatalog{models: []Model{{ID: "outdated"}}, fetchedAt: time.Now().Add(-time.Hour)}
			req.Model = "first"
			result, err = m.Execute(t.Context(), req, discardFrame)
			if err != nil || result.AccountID != first {
				t.Fatal("使用过期目录跳过了合格账号", result, err)
			}
		})
	}
}

func TestProviderSlotQueueBoundsAndRelease(t *testing.T) {
	m, _ := testManager(t, "")
	id := importFake(t, m, "qoder:cn", "one")
	m.running[id].inFlight++
	wait := 15 * time.Millisecond
	req := Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat), KeyID: "key", SlotWait: &wait}
	for _, budget := range []time.Duration{wait, 0} {
		wait = budget
		result, err := m.Execute(t.Context(), req, discardFrame)
		var pe *Error
		if !errors.As(err, &pe) || pe.Code != "account_queue_timeout" || result.Attempts != 0 || m.waiters != 0 || len(m.waitersByKey) != 0 {
			t.Fatal("等待超时未释放名额", result, err)
		}
	}
	wait = time.Second
	// 用真实的登记路径占满每密钥限额，再由 Execute 检查拒绝是否影响其他密钥。
	m.mu.Lock()
	for range maxKeySlotWaiters {
		if err := m.joinQueueLocked("key"); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Unlock()
	_, err := m.Execute(t.Context(), req, discardFrame)
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != "account_queue_full" || pe.Status != 429 {
		t.Fatal("密钥满队列未拒绝", err)
	}
	req.KeyID = "other"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.Execute(ctx, req, discardFrame); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		m.mu.Lock()
		registered := m.waitersByKey["other"] == 1
		m.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("另一密钥无法排队")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	m.mu.Lock()
	if m.waiters != maxKeySlotWaiters || m.waitersByKey["other"] != 0 {
		t.Fatal("取消泄漏等待名额")
	}
	for i := m.waiters; i < maxSlotWaiters; i++ {
		if err := m.joinQueueLocked(fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Unlock()
	_, err = m.Execute(t.Context(), req, discardFrame)
	if !errors.As(err, &pe) || pe.Code != "account_queue_full" || len(m.Usage()) != 0 {
		t.Fatal("全局队满或入队计量错误", err)
	}
}
