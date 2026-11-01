// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// accountsExport 导出账号凭证，供备份或迁到另一台网关。每项与数据目录里的
// workbuddy-<uid>.json 同形（auth.Document），面板「导入」原样认回，企业 ID / 域 /
// 设备 token 不丢；绑了代理的号还带 proxy_url（代理的完整地址，含密码），导进别的网关时认出同一个代理。
//
//	POST /panel/api/accounts/export
//	Body: {"uids":["..."]}
//
// 返回 {"accounts":[凭证文档…]}，顺序同请求（重复 uid 只出一次）。有 uid 不在池里时整单
// 404，不做部分导出——少导一个号而用户以为导全了，比报错更糟。内容是可以直接登录的凭证，
// 响应带 Cache-Control: no-store。
func (p *Panel) accountsExport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UIDs []string `json:"uids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.UIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "请求体需要非空的 uids 列表")
		return
	}
	docs := make([]map[string]any, 0, len(body.UIDs))
	seen := make(map[string]bool, len(body.UIDs))
	var missing []string
	for _, uid := range body.UIDs {
		if seen[uid] {
			continue
		}
		seen[uid] = true
		a := p.cfg.Pool.AuthByUID(uid)
		if a == nil {
			missing = append(missing, uid)
			continue
		}
		doc := a.Document()
		if p.cfg.Proxies != nil {
			// 带上代理的完整地址：导进别的网关时按它认出（或新建）同一个代理，号的出口 IP 不变。
			if u := p.cfg.Proxies.ProxyURL(uid); u != "" {
				doc["proxy_url"] = u
			}
		}
		docs = append(docs, doc)
	}
	if len(missing) > 0 {
		writeErr(w, http.StatusNotFound, "账号不在池里："+strings.Join(missing, ", "))
		return
	}
	log.Printf("panel: 导出凭证 %d 个账号", len(docs))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"accounts": docs})
}
