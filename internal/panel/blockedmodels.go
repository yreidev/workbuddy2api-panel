// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 每号不接的模型（见 pool/blockedmodels.go）：账号详情里整份保存，多选后批量加、删（accounts_batch.go）。

package panel

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"unicode"
)

const (
	// blockedModelMax 不接的模型名单一项最长多少字符（同密钥模型白名单）。
	blockedModelMax = 128
	// blockedModelsMax 一个号的名单最多几项。
	blockedModelsMax = 100
)

// normalizeBlockedModels 去掉首尾空白和空项、按不区分大小写去重；带域前缀、有空白字符、太长、太多的报错。
func normalizeBlockedModels(models []string) ([]string, error) {
	var out []string
	for _, m := range models {
		m = strings.TrimSpace(m)
		switch {
		case m == "":
			continue
		case strings.Contains(m, ":"):
			return nil, fmt.Errorf("按模型名填，不带 cn: / global: 前缀：%s", m)
		case strings.IndexFunc(m, unicode.IsSpace) >= 0:
			return nil, fmt.Errorf("模型名里不能有空格：%s", m)
		case len(m) > blockedModelMax:
			return nil, fmt.Errorf("模型名最长 %d 个字符：%s", blockedModelMax, m)
		}
		if !slices.ContainsFunc(out, func(x string) bool { return strings.EqualFold(x, m) }) {
			out = append(out, m)
		}
	}
	if len(out) > blockedModelsMax {
		return nil, fmt.Errorf("一个号最多填 %d 个模型", blockedModelsMax)
	}
	return out, nil
}

// accountBlockedModels 整份保存一个号不接的模型：去掉的解除（上游拒绝后自动加的也一样），新加的算手动加的。
//
//	POST /panel/api/accounts/{uid}/blocked_models
//	Body: {"models":["gpt-*","deepseek-v4.1-flash"]}
//
// 返回 {"ok":true,"blocked_models":[...]}（保存后的名单，同账号状态里的 blocked_models）。
func (p *Panel) accountBlockedModels(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	var body struct {
		Models []string `json:"models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体要有 models 列表")
		return
	}
	models, err := normalizeBlockedModels(body.Models)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !p.cfg.Pool.SetBlockedModels(uid, models) {
		writeErr(w, http.StatusNotFound, errAccountNotFound.Error())
		return
	}
	st, _ := p.cfg.Pool.Status(uid)
	log.Printf("panel: blocked models uid=%s 现在 %d 项：%s", uid, len(st.BlockedModels), strings.Join(models, ", "))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "blocked_models": st.BlockedModels})
}
