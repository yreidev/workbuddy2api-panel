// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

// keys.go 客户端密钥的管理接口（internal/apikeys）：列出、新建、修改、重新生成、删除。
// 面板只认管理员密钥，客户端密钥登不了面板，所以这里返回密钥原文没有问题。

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/apikeys"
)

// adminUsageKey / livenessUsageKey 用量统计里管理员密钥、测活的密钥维度，和 server 包一致。
const (
	adminUsageKey    = "admin"
	livenessUsageKey = "liveness"
)

// keyView 列表里的一个密钥，另带算好的过期状态（按网关的时钟算，不看浏览器）。
type keyView struct {
	apikeys.Key
	Expired bool `json:"expired"`
}

func viewKey(k apikeys.Key, now time.Time) keyView {
	return keyView{Key: k, Expired: k.Expired(now)}
}

// keysList GET /panel/api/keys。required：建过客户端密钥后 /v1/* 必须带密钥（管理员密钥为空也一样）。
func (p *Panel) keysList(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Keys == nil {
		writeErr(w, http.StatusNotImplemented, "api key store not available")
		return
	}
	now := time.Now()
	keys := p.cfg.Keys.List(now)
	out := make([]keyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, viewKey(k, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"keys":          out,
		"admin_key_set": p.apiKey() != "",
		"required":      len(keys) > 0 || p.apiKey() != "",
	})
}

// keyCreate POST /panel/api/keys：新建，返回带原文的密钥。
func (p *Panel) keyCreate(w http.ResponseWriter, r *http.Request) {
	in, ok := p.keyInput(w, r)
	if !ok {
		return
	}
	k, err := p.cfg.Keys.Create(in, time.Now())
	p.writeKeyResult(w, k, err)
}

// keyUpdate POST /panel/api/keys/{id}：改设置，原文和用量不变。
func (p *Panel) keyUpdate(w http.ResponseWriter, r *http.Request) {
	in, ok := p.keyInput(w, r)
	if !ok {
		return
	}
	k, err := p.cfg.Keys.Update(r.PathValue("id"), in)
	p.writeKeyResult(w, k, err)
}

// keyRegenerate POST /panel/api/keys/{id}/regenerate：换新原文，旧的立即失效。
func (p *Panel) keyRegenerate(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Keys == nil {
		writeErr(w, http.StatusNotImplemented, "api key store not available")
		return
	}
	k, err := p.cfg.Keys.Regenerate(r.PathValue("id"))
	p.writeKeyResult(w, k, err)
}

// keyDelete POST /panel/api/keys/{id}/delete。用量统计里的历史数据保留。
func (p *Panel) keyDelete(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Keys == nil {
		writeErr(w, http.StatusNotImplemented, "api key store not available")
		return
	}
	if err := p.cfg.Keys.Delete(r.PathValue("id")); err != nil {
		p.writeKeyResult(w, apikeys.Key{}, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (p *Panel) keyInput(w http.ResponseWriter, r *http.Request) (apikeys.Input, bool) {
	var in apikeys.Input
	if p.cfg.Keys == nil {
		writeErr(w, http.StatusNotImplemented, "api key store not available")
		return in, false
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对："+err.Error())
		return in, false
	}
	if err := p.checkGroups(in.Groups); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return in, false
	}
	return in, true
}

// writeKeyResult 没有这个密钥回 404，写盘失败回 500，输入不合法回 400（文案都是给人看的说明）。
func (p *Panel) writeKeyResult(w http.ResponseWriter, k apikeys.Key, err error) {
	switch {
	case errors.Is(err, apikeys.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, apikeys.ErrSave):
		writeErr(w, http.StatusInternalServerError, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": viewKey(k, time.Now())})
	}
}

// keyLabel 用量统计里密钥维度的显示名：管理员、测活、已删除的密钥，空串是没开鉴权或加密钥维度之前的数据。
func keyLabel(id string, names map[string]string) string {
	switch id {
	case "":
		return "未区分"
	case adminUsageKey:
		return "管理员"
	case livenessUsageKey:
		return "测活"
	}
	if n, ok := names[id]; ok {
		return n
	}
	return "已删除的密钥"
}
