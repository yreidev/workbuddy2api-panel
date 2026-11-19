// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"fmt"
	"math"
	"strings"
)

// requestMeter 合并同一次上游调用的累计计费帧；不把原价、余额或会话累计当作单次扣费。
type requestMeter struct {
	target   string
	credits  *float64
	billable *bool
}

func (m *requestMeter) observe(u map[string]any) error {
	if !strings.HasPrefix(m.target, "qoder:") && m.target != "traework:cn" {
		return nil // Trae Code 的 llm_utils_chat 单次扣费字段尚未确认。
	}
	if m.target == "traework:cn" {
		// 仅由 Work 适配器按本条用户消息的 ID 查询并确认。
		if value, exists := u["credit"]; exists {
			v, ok := value.(float64)
			if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return fmt.Errorf("usage.credit 必须为有限非负数")
			}
			m.credits = &v
		}
		return nil
	}
	if value, exists := u["billable"]; exists {
		v, ok := value.(bool)
		if !ok {
			return fmt.Errorf("usage.billable 必须为布尔值")
		}
		m.billable = &v
	}
	if m.billable != nil && !*m.billable {
		// 官方 session 计费器以 billable 为准；不计费帧里的 credits 不参与扣费。
		m.credits = nil
		return nil
	}
	if value, exists := u["credits"]; exists {
		v, ok := value.(float64)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return fmt.Errorf("usage.credits 必须为有限非负数")
		}
		m.credits = &v
	}
	return nil
}

func (m *requestMeter) charge() (float64, bool) {
	if strings.HasPrefix(m.target, "qoder:") {
		if m.billable == nil {
			return 0, false
		}
		if !*m.billable {
			return 0, true
		}
	}
	if m.credits == nil {
		return 0, false
	}
	return *m.credits, true
}
