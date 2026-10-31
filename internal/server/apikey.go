// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

// apikey.go 接口鉴权：管理员密钥（config.json 的 api_key）和面板里建的客户端密钥（internal/apikeys）。
//
//   - 带的是管理员密钥：放行，不受白名单和上限约束；
//   - 带的是客户端密钥：删了、停用、过期的回 401；对话请求再按白名单和上限放行（admitKey，要等模型
//     路由改写完才知道实际发上游的模型）；
//   - 都不是：管理员密钥为空、也没建过客户端密钥时放行（没开鉴权，和以前一样），否则 401。
//     只要建过客户端密钥，管理员密钥为空也必须带密钥，不然白名单和上限一绕就过。

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"

	"github.com/yreidev/workbuddy2api-panel/internal/apikeys"
	"github.com/yreidev/workbuddy2api-panel/internal/httpauth"
)

// adminUsageKey 用量统计里管理员密钥的密钥维度。客户端密钥的 id 以 k 开头，不会撞上。
const adminUsageKey = "admin"

type principalKey struct{}

// principal 请求是谁发来的。零值 = 没开鉴权。
type principal struct {
	admin bool
	key   *apikeys.Key // 客户端密钥（鉴权时的快照）
}

func principalFrom(ctx context.Context) principal {
	p, _ := ctx.Value(principalKey{}).(principal)
	return p
}

// usageKey 用量统计里的密钥维度：管理员密钥记 admin，客户端密钥记它的 id，没开鉴权记空串。
func (p principal) usageKey() string {
	switch {
	case p.key != nil:
		return p.key.ID
	case p.admin:
		return adminUsageKey
	}
	return ""
}

// label 请求日志里的密钥：客户端密钥记名字，管理员密钥记「管理员」，没开鉴权为空。
func (p principal) label() string {
	switch {
	case p.key != nil:
		return p.key.Name
	case p.admin:
		return "管理员"
	}
	return ""
}

// authenticate 认出请求带的密钥。xAPIKey 为 true 时另认 x-api-key 头（Claude Code / Codex 入口）。
func (h *Handler) authenticate(r *http.Request, xAPIKey bool) (principal, *apikeys.Denied) {
	admin := h.loadLive().APIKey
	tok := httpauth.Token(r, xAPIKey)
	if admin != "" && tok != "" && httpauth.Equal(tok, admin) {
		return principal{admin: true}, nil
	}
	now := time.Now()
	if k, ok := h.cfg.Keys.Lookup(tok); ok {
		if d := h.cfg.Keys.Check(k.ID, now); d != nil {
			return principal{}, d
		}
		return principal{key: &k}, nil
	}
	if admin == "" && !h.cfg.Keys.Any() {
		return principal{}, nil
	}
	return principal{}, &apikeys.Denied{Status: http.StatusUnauthorized, Code: "invalid_api_key", Message: "missing or invalid API key"}
}

// withKeyAuth 对话和模型列表接口的鉴权，管理员密钥和客户端密钥都认；报错按入口协议的格式写。
// 认出的密钥放进请求上下文，请求日志记下它的名字。
func (h *Handler) withKeyAuth(format sdktranslator.Format, xAPIKey bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, d := h.authenticate(r, xAPIKey)
		if d != nil {
			writeBridgeError(w, format, d.Status, d.Code, d.Message)
			return
		}
		if tr := requestTraceFrom(r); tr != nil {
			tr.apiKey = p.label()
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}
}

// admitKey 客户端密钥的对话请求：先按白名单判实际发上游的模型（裸模型名），再按上限放行并计一次
// 请求（请求最后失败时由调用方 Refund 退还）。不放行时写好错误并返回状态码，放行返回 0。管理员密钥
// 和没开鉴权的请求直接放行。
func (h *Handler) admitKey(w http.ResponseWriter, p principal, bareModel string, now time.Time) int {
	d := h.keyDenial(p, bareModel, now)
	if d == nil {
		return 0
	}
	denialHeaders(w, d)
	writeOpenAIError(w, d.Status, d.Code, d.Message)
	return d.Status
}

// keyDenial 客户端密钥这次放不放行（规则见 admitKey）：放行返回 nil 并计一次请求，不放行返回原因，
// 由调用方按入口协议写出。bareModel 为空时不看白名单（联网搜索不是模型调用）。
func (h *Handler) keyDenial(p principal, bareModel string, now time.Time) *apikeys.Denied {
	if p.key == nil {
		return nil
	}
	if bareModel != "" && !p.key.AllowsModel(bareModel) {
		return &apikeys.Denied{Status: http.StatusForbidden, Code: "model_not_allowed",
			Message: fmt.Sprintf("密钥「%s」不能用模型 %s，允许的模型：%s", p.key.Name, bareModel, strings.Join(p.key.Models, "、"))}
	}
	return h.cfg.Keys.Admit(p.key.ID, now)
}

// denialHeaders 上限用完重试没用：告诉 SDK 别自动重试，每天的上限另给出到日切的秒数。
func denialHeaders(w http.ResponseWriter, d *apikeys.Denied) {
	if d.Status != http.StatusTooManyRequests {
		return
	}
	w.Header().Set("X-Should-Retry", "false")
	if d.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(d.RetryAfter.Round(time.Second).Seconds())))
	}
}
