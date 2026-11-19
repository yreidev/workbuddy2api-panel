// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"fmt"
	"os"
	"time"
)

// LivenessResult 保存新平台最近一次单号测活，不包含回复正文或凭证。
type LivenessResult struct {
	Scheduled        bool      `json:"scheduled,omitempty"`
	At               time.Time `json:"at"`
	OK               bool      `json:"ok"`
	Model            string    `json:"model"`
	Status           int       `json:"status"`
	Error            string    `json:"error,omitempty"`
	TTFBMs           int64     `json:"ttfb_ms"`
	DurationMs       int64     `json:"duration_ms"`
	TokensKnown      bool      `json:"tokens_known"`
	PromptTokens     int64     `json:"prompt_tokens,omitempty"`
	CompletionTokens int64     `json:"completion_tokens,omitempty"`
	Credit           float64   `json:"credit,omitempty"`
	HasCredit        bool      `json:"credit_known"`
	CreditUnit       string    `json:"credit_unit,omitempty"`
}

type LivenessResponse struct {
	Result LivenessResult `json:"result"`
	Reply  string         `json:"reply"`
}

func (m *Manager) RecordLiveness(id string, result LivenessResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, a := range m.data.Accounts {
		if a.ID != id {
			continue
		}
		m.data.Accounts[i].LastLiveness = &result
		if err := m.saveLocked(); err != nil {
			m.data.Accounts[i].LastLiveness = a.LastLiveness
			return fmt.Errorf("保存平台账号 %s 的测活结果: %w", id, err)
		}
		return nil
	}
	return os.ErrNotExist
}
