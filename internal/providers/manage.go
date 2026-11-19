// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Change 按字段修改，批量操作不会把列表里的运行时停用状态写成人工禁用。
func (m *Manager) Change(id, action, group string, priority int, models []string, proxyID string) error {
	if action == "remove" {
		return m.Update(id, AccountInput{}, true)
	}
	return m.update(id, func(a *Account) error {
		switch action {
		case "pause":
			a.Paused = true
		case "resume":
			a.Paused = false
		case "disable":
			a.Disabled = true
		case "revive":
			a.Disabled, a.Paused = false, false
		case "priority":
			if priority < 0 || priority > 99 {
				return errors.New("优先级必须在 0～99 之间")
			}
			a.Priority = priority
		case "join_group":
			if group == "" {
				return errors.New("请选择分组")
			}
			if !slices.Contains(a.Groups, group) {
				a.Groups = append(slices.Clone(a.Groups), group)
			}
		case "leave_group":
			a.Groups = slices.DeleteFunc(slices.Clone(a.Groups), func(g string) bool { return g == group })
		case "proxy":
			a.ProxyID = proxyID
		case "block_models":
			for _, model := range models {
				model = strings.TrimSpace(model)
				if model != "" && !slices.Contains(a.BlockedModels, model) {
					a.BlockedModels = append(slices.Clone(a.BlockedModels), model)
				}
			}
		case "unblock_models":
			a.BlockedModels = slices.DeleteFunc(slices.Clone(a.BlockedModels), func(model string) bool { return slices.Contains(models, model) })
		default:
			return fmt.Errorf("不支持的平台账号操作：%s", action)
		}
		return nil
	}, false, action == "revive")
}

// Export 只由管理员显式下载；普通列表始终不含凭证。按通道导出，可重新导入并合并同身份账号。
func (m *Manager) Export(ids []string) ([]AccountInput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []AccountInput{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		index := slices.IndexFunc(m.data.Accounts, func(a Account) bool { return a.ID == id })
		if index < 0 {
			return nil, fmt.Errorf("导出账号不存在：%s", id)
		}
		a := m.data.Accounts[index]
		if rt := m.running[id]; rt.blocked || rt.importing || rt.inFlight > 0 {
			return nil, fmt.Errorf("账号 %s 正在使用或需要重新授权，暂不能导出", id)
		}
		for _, target := range a.Targets {
			credential := a.Credential
			// 导出再导入时视为迁移的设备身份，不能在新机器上自动覆盖它。
			credential.MachineLocal = false
			out = append(out, AccountInput{Target: target, Name: a.Name, Groups: slices.Clone(a.Groups), Priority: a.Priority, Disabled: a.Disabled, Paused: a.Paused, BlockedModels: slices.Clone(a.BlockedModels), LivenessModel: a.LivenessModel, ProxyID: a.ProxyID, Credential: credential})
		}
	}
	return out, nil
}
