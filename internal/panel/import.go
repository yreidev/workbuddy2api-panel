// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// cockpitAccount 映射 cockpit tools 导出格式的单个账号。
type cockpitAccount struct {
	ID             string `json:"id"`
	Email          string `json:"email"`
	UID            string `json:"uid"`
	Nickname       string `json:"nickname"`
	AccessToken    string `json:"access_token"`
	RefreshToken   string `json:"refresh_token"`
	TokenType      string `json:"token_type"`
	ExpiresAt      int64  `json:"expires_at"`
	Domain         string `json:"domain"`
	DosageNotify   string `json:"dosage_notify_code"`
	PaymentType    string `json:"payment_type"`
	Status         string `json:"status"`
	UsageUpdatedAt int64  `json:"usage_updated_at"`
	LastCheckin    int64  `json:"last_checkin_time"`
	CheckinStreak  int    `json:"checkin_streak"`
	CreatedAt      int64  `json:"created_at"`
	LastUsed       int64  `json:"last_used"`
}

// importAccounts 导入账号凭证，两种格式逐项自动识别：
//   - 网关自己的凭证（auth 文件的嵌套形 / 扁平形，即面板「导出凭证」得到的文件）：
//     企业 ID、域、设备 token 原样保留；
//   - cockpit tools 导出的账号（snake_case 字段）：没有企业 ID / 域，域按 domain 推断。
//
// 文件可以是单个对象（单个导出的 workbuddy-<uid>.json）或数组（批量导出 / cockpit）。
//
//	POST /panel/api/import（旧路径 /panel/api/import/cockpit 保留，同一个处理）
//	Content-Type: multipart/form-data
//	Body: file=<json>
//
// 返回 {ok, total, imported, skipped, errors}。
func (p *Panel) importAccounts(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "parse form: "+err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing file field: "+err.Error())
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read file: "+err.Error())
		return
	}

	items, err := splitImportItems(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if len(items) == 0 {
		writeErr(w, http.StatusBadRequest, "empty accounts array")
		return
	}

	var imported, skipped int
	var errs []string

	for i, item := range items {
		var a *auth.Auth
		var msg string
		if isGatewayAuth(item) {
			a, msg = p.gatewayImportAuth(item)
		} else {
			a, msg = p.cockpitImportAuth(item)
		}
		if a == nil {
			skipped++
			errs = append(errs, fmt.Sprintf("#%d: %s", i+1, msg))
			continue
		}
		uid := a.UID

		if err := a.SaveAtomic(); err != nil {
			skipped++
			errs = append(errs, fmt.Sprintf("uid=%s: save auth failed: %v", uid, err))
			continue
		}

		p.cfg.Pool.Add(a)
		p.cfg.Pool.Revive(uid)

		// 顺带签到/激活（幂等；失败仅记日志，不阻断导入）。
		if a.IsGlobal() {
			if activated, err := p.cfg.Upstream.GlobalCompleteRegistration(a); err != nil {
				log.Printf("panel: import global 注册激活 uid=%s: %v", uid, err)
			} else if activated {
				log.Printf("panel: import global 注册激活 uid=%s 完成", uid)
			}
			if claimed, err := p.cfg.Upstream.ClaimTrial(a); err != nil {
				log.Printf("panel: import global trial uid=%s: %v", uid, err)
			} else if claimed {
				log.Printf("panel: import global trial uid=%s 已领", uid)
			}
		} else if !a.IsEnterprise() {
			// 企业版跳过签到（上游 400 code 10001「企业账号不支持该操作」），同 loginPoll。
			if err := p.cfg.Upstream.DailyCheckin(a); err != nil {
				log.Printf("panel: import checkin uid=%s: %v", uid, err)
			}
		}
		if rm, tt, err := p.cfg.Upstream.UserResource(a); err == nil {
			p.cfg.Pool.ReenableIfCredits(uid, rm, tt)
		}

		imported++
	}

	total := len(items)
	log.Printf("panel: import finished total=%d imported=%d skipped=%d", total, imported, skipped)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"total":    total,
		"imported": imported,
		"skipped":  skipped,
		"errors":   errs,
	})
}

// splitImportItems 把导入文件拆成逐项的 JSON：单个对象视为一项，数组逐元素。
func splitImportItems(raw []byte) ([]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '{' {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, err
		}
		return []json.RawMessage{raw}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// isGatewayAuth 报告一项是不是网关自己的凭证格式：嵌套形带 "auth"、扁平形带 "accessToken"
// （与 auth.Parse 的两种形态一致）。cockpit tools 用 snake_case 的 access_token，不会误判。
func isGatewayAuth(item json.RawMessage) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal(item, &probe) != nil {
		return false
	}
	_, nested := probe["auth"]
	_, flat := probe["accessToken"]
	return nested || flat
}

// gatewayImportAuth 解析网关格式的一项，返回待落盘的凭证；失败时 Auth 为 nil、附原因。
// 企业 ID / 域 / 设备 token 由 auth.Parse 原样带回；旧文件没存域时按 domain 补上。
func (p *Panel) gatewayImportAuth(item json.RawMessage) (*auth.Auth, string) {
	a, err := auth.Parse(item)
	if err != nil {
		return nil, err.Error()
	}
	a.UID = strings.TrimSpace(a.UID)
	if a.UID == "" || strings.TrimSpace(a.RefreshToken) == "" {
		return nil, fmt.Sprintf("missing required fields (uid=%s)", a.UID)
	}
	if !validImportUID(a.UID) {
		return nil, fmt.Sprintf("invalid uid (uid=%s)", a.UID)
	}
	a.FilePath = filepath.Join(p.cfg.AuthDir, fmt.Sprintf("workbuddy-%s.json", a.UID))
	_, _ = a.BackfillRealm()
	return a, ""
}

// cockpitImportAuth 把 cockpit tools 格式的一项转成待落盘的凭证；失败时 Auth 为 nil、附原因。
func (p *Panel) cockpitImportAuth(item json.RawMessage) (*auth.Auth, string) {
	var acc cockpitAccount
	if err := json.Unmarshal(item, &acc); err != nil {
		return nil, "invalid json: " + err.Error()
	}
	uid := strings.TrimSpace(acc.UID)
	at := strings.TrimSpace(acc.AccessToken)
	rt := strings.TrimSpace(acc.RefreshToken)
	if uid == "" || at == "" || rt == "" {
		return nil, fmt.Sprintf("missing required fields (id=%s)", acc.ID)
	}
	if !validImportUID(uid) {
		return nil, fmt.Sprintf("invalid uid (id=%s)", acc.ID)
	}

	// cockpit tools 的 expires_at 为毫秒时间戳，转为秒。
	expiresAt := acc.ExpiresAt / 1000
	if expiresAt <= 0 {
		expiresAt = time.Now().Add(365 * 24 * time.Hour).Unix()
	}

	nickname := acc.Nickname
	if strings.TrimSpace(nickname) == "" {
		nickname = acc.Email
	}

	a := &auth.Auth{
		AccessToken:  at,
		RefreshToken: rt,
		ExpiresAt:    expiresAt,
		Domain:       acc.Domain,
		UID:          uid,
		Nickname:     nickname,
		FilePath:     filepath.Join(p.cfg.AuthDir, fmt.Sprintf("workbuddy-%s.json", uid)),
	}

	// 按 domain 推断 realm：workbuddy.ai 家族 → global，否则 cn。
	if auth.ResolveRealm("", acc.Domain) == "global" {
		if _, err := auth.BackfillRealmFor(a, "global"); err != nil {
			return nil, fmt.Sprintf("uid=%s: set realm failed: %v", uid, err)
		}
	} else {
		_, _ = a.BackfillRealm()
	}
	return a, ""
}

// validImportUID 校验导入 uid 是否可用于拼文件名（同 login.go validUID 口径）。
func validImportUID(uid string) bool {
	if uid == "" || len(uid) > 64 {
		return false
	}
	for _, c := range uid {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
