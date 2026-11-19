// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// 每次确认只写近期尝试。累计到一批才将汇总及绑定写成快照。
const checkpointAttempts = 128

type attemptRecord struct {
	Sequence   uint64   `json:"sequence"`
	RequestID  string   `json:"request_id"`
	Attempt    int      `json:"attempt"`
	Delta      Usage    `json:"usage"`
	BindingKey string   `json:"binding_key,omitempty"`
	Binding    *Binding `json:"binding,omitempty"`
}

type activitySnapshot struct {
	Version  int                `json:"version"`
	Sequence uint64             `json:"sequence"`
	Usage    []Usage            `json:"usage"`
	Bindings map[string]Binding `json:"bindings"`
}

type activityLedger struct {
	mu       sync.Mutex // 排列记账次序。写磁盘时不持账号管理锁。
	failed   atomic.Bool
	sequence uint64
	batch    []attemptRecord
	index    map[[4]string]int
}

func usageKey(u Usage) [4]string { return [4]string{u.Target, u.AccountID, u.Model, u.KeyID} }

func (m *Manager) openActivity() error {
	a := &m.activity
	a.index = make(map[[4]string]int)
	if m.path == "" {
		return nil
	}
	snapshotPath, batchPath := m.path+".activity.json", m.path+".attempts.json"
	var snap activitySnapshot
	var batch []attemptRecord
	if m.data.Version == 0 {
		// 全新且尚未导入账号的安装可直接启动。只在首次保存账号时创建账本文件。
		for _, path := range []string{snapshotPath, batchPath} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				if err != nil {
					return err
				}
				return fmt.Errorf("账号文件缺失，但账本仍存在：%s", path)
			}
		}
		return nil
	}
	if m.data.Version == 1 {
		// 旧账号文件直到两份新账本都安全写入才切换版本。中途退出可从旧文件重复迁移。
		if m.data.Usage == nil {
			m.data.Usage = []Usage{}
		}
		snap = activitySnapshot{Version: 1, Usage: m.data.Usage, Bindings: m.data.Bindings}

	} else {
		if err := readActivity(snapshotPath, &snap); err != nil {
			return err
		}
		if snap.Version != 1 || snap.Bindings == nil || snap.Usage == nil {
			return fmt.Errorf("无效的平台用量快照 %s", snapshotPath)
		}
		if err := readActivity(batchPath, &batch); err != nil {
			return err
		}
		if batch == nil {
			return fmt.Errorf("无效的平台尝试账本 %s", batchPath)
		}
	}
	m.data.Usage, m.data.Bindings, a.sequence = snap.Usage, maps.Clone(snap.Bindings), snap.Sequence
	for i, u := range m.data.Usage {
		k := usageKey(u)
		if _, ok := a.index[k]; ok {
			return fmt.Errorf("平台快照包含重复用量行 %v", k)
		}
		a.index[k] = i
	}
	for i, e := range batch {
		if e.Sequence == 0 || i > 0 && batch[i-1].Sequence+1 != e.Sequence || e.RequestID == "" || e.Attempt <= 0 || e.Delta.Requests != 1 || e.Delta.AccountID == "" || e.Delta.Model == "" || (e.Binding != nil) != (e.BindingKey != "") {
			return fmt.Errorf("无效的平台尝试记录 sequence=%d", e.Sequence)
		}
		// 已合并快照的记录可能仍在旧批次里，不得重复计数。
		if e.Sequence <= snap.Sequence {
			continue
		}
		if e.Sequence != a.sequence+1 {
			return fmt.Errorf("平台尝试流水号不连续：快照后为 %d，读到 %d", a.sequence, e.Sequence)
		}
		m.applyAttempt(e)
		a.sequence = e.Sequence
		a.batch = append(a.batch, e)
	}
	epochs := make(map[string]string, len(m.data.Accounts))
	for _, account := range m.data.Accounts {
		epochs[account.ID] = account.BindingEpoch
	}
	now := time.Now()
	for key, b := range m.data.Bindings {
		if epoch, ok := epochs[b.AccountID]; !ok || epoch != b.Epoch || !now.Before(b.ExpiresAt) {
			delete(m.data.Bindings, key)
		}
	}
	if m.data.Version == 1 {
		if err := m.writeInitialActivity(snap); err != nil {
			return err
		}
		return m.saveLocked()
	}
	return nil
}

func (m *Manager) writeInitialActivity(snap activitySnapshot) error {
	if err := migrateActivityFile(m.path+".activity.json", snap); err != nil {
		return err
	}
	return migrateActivityFile(m.path+".attempts.json", []attemptRecord{})
}

// 迁移中断只允许续用从同一份旧数据生成的文件，避免还原旧账号文件时覆盖新账本。
func migrateActivityFile(path string, value any) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return writePlatformFile(path, value)
	}
	if err != nil {
		return err
	}
	want, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, want) {
		return fmt.Errorf("旧账号数据与已有平台账本不一致：%s", path)
	}
	// 可能上次 rename 已成功但目录同步失败，再确认一次写入。
	return writePlatformFile(path, value)
}

func readActivity(path string, dst any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("解析平台账本 %s: %w", path, err)
	}
	return nil
}

func (m *Manager) applyAttempt(e attemptRecord) {
	d := e.Delta
	key := usageKey(d)
	if i, ok := m.activity.index[key]; ok {
		u := &m.data.Usage[i]
		u.Requests += d.Requests
		u.Failed += d.Failed
		u.PromptTokens += d.PromptTokens
		u.CompletionTokens += d.CompletionTokens
		u.TokensKnown += d.TokensKnown
		u.Charge += d.Charge
		u.ChargeKnown += d.ChargeKnown
		if d.ChargeKnown != 0 {
			u.Unit = d.Unit
		}
	} else {
		m.activity.index[key] = len(m.data.Usage)
		m.data.Usage = append(m.data.Usage, d)
	}
	if e.Binding != nil {
		m.data.Bindings[e.BindingKey] = *e.Binding
	}
}

func (m *Manager) recordAttempt(e attemptRecord) error {
	a := &m.activity
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sequence++
	e.Sequence = a.sequence
	m.mu.Lock()
	m.applyAttempt(e)
	if m.path == "" && a.sequence%checkpointAttempts == 0 {
		m.pruneBindingsLocked(time.Now())
	}
	m.mu.Unlock()
	if m.path == "" {
		return nil
	}
	a.batch = append(a.batch, e)
	return m.persistActivity()
}

func (m *Manager) pruneBindingsLocked(now time.Time) {
	for k, b := range m.data.Bindings {
		if !now.Before(b.ExpiresAt) {
			delete(m.data.Bindings, k)
		}
	}
}

func (m *Manager) persistActivity() error {
	a := &m.activity
	var err error
	if len(a.batch) >= checkpointAttempts {
		m.mu.Lock()
		m.pruneBindingsLocked(time.Now())
		snap := activitySnapshot{Version: 1, Sequence: a.sequence, Usage: slices.Clone(m.data.Usage), Bindings: maps.Clone(m.data.Bindings)}
		m.mu.Unlock()
		err = writePlatformFile(m.path+".activity.json", snap)
		if err == nil {
			err = writePlatformFile(m.path+".attempts.json", []attemptRecord{})
		}
		if err == nil {
			a.batch = nil
		}
	} else {
		err = writePlatformFile(m.path+".attempts.json", a.batch)
	}
	a.failed.Store(err != nil)
	return err
}

// FlushActivity 重试待写的尝试，沿用原流水号，磁盘操作不触发上游调用。
func (m *Manager) FlushActivity() error {
	a := &m.activity
	if !a.failed.Load() {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.failed.Load() {
		return nil
	}
	return m.persistActivity()
}
