// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/providers"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/targets"
)

// TestProviderAccount 沿用真实对话、错误处理和记账链路，固定账号及产品，只发一次请求。
func (h *Handler) TestProviderAccount(ctx context.Context, id, model string) (providers.LivenessResponse, error) {
	return h.testProviderAccount(ctx, id, model, false)
}

// RunProviderLivenessNow 只探测已配置模型、仍在临时冷却的号，人工停用与暂停、刷新阻断的号均跳过。
func (h *Handler) RunProviderLivenessNow() {
	for _, a := range h.cfg.Providers.List() {
		if a.Disabled || a.Paused || a.LivenessModel == "" || a.CoolUntil.IsZero() || a.InFlight > 0 {
			continue
		}
		if _, err := h.testProviderAccount(context.Background(), a.ID, a.LivenessModel, true); err != nil {
			log.Printf("liveness scheduled account=%s: %v", a.ID, err)
		}
	}
}

func (h *Handler) testProviderAccount(ctx context.Context, id, model string, scheduled bool) (providers.LivenessResponse, error) {
	var out providers.LivenessResponse
	if h.cfg.Providers == nil {
		return out, fmt.Errorf("平台账号管理未启用")
	}
	target, native, explicit := targets.Parse(strings.TrimSpace(model))
	if !explicit || targets.WorkBuddy(target) || native == "" {
		return out, fmt.Errorf("请选择带产品与地区的完整平台模型名")
	}
	accounts := h.cfg.Providers.List()
	index := slices.IndexFunc(accounts, func(a providers.AccountView) bool { return a.ID == id })
	if index < 0 {
		return out, os.ErrNotExist
	}
	if !slices.Contains(accounts[index].Targets, target) {
		return out, fmt.Errorf("账号没有开通 %s 通道", target)
	}
	if _, hidden := h.loadLive().HiddenModels[targets.Public(target, native)]; hidden {
		return out, fmt.Errorf("模型已被网关隐藏")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]any{
		"model": targets.Public(target, native), "stream": true, "max_tokens": livenessMaxTokens,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	tr := &requestTrace{id: reqlog.NewRequestID(), start: time.Now(), path: livenessPath, apiKey: livenessLabel}
	ctx = context.WithValue(context.WithValue(ctx, requestTraceKey{}, tr), livenessKey{}, id)
	// 面板鉴权由入口完成；测试不继承客户端密钥，不消耗客户端限额。
	ctx = context.WithValue(ctx, principalKey{}, principal{})
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	w := &livenessWriter{header: http.Header{}}
	obs := &responseObserver{ResponseWriter: w}
	if h.cfg.RequestLog != nil {
		h.cfg.RequestLog.Begin()
	}
	h.providerChat(obs, r, body, target, native, true)
	ev := tr.event(obs.status, obs.errBody)
	if h.cfg.RequestLog != nil {
		h.cfg.RequestLog.Record(ev)
	}
	out.Result = providers.LivenessResult{At: tr.start, OK: ev.OK, Model: targets.Public(target, native), Status: ev.Status, Error: ev.Error, TTFBMs: ev.TTFBMs, DurationMs: ev.DurationMs}
	out.Result.Scheduled = scheduled
	out.Result.Credit, out.Result.HasCredit, out.Result.CreditUnit = ev.Credit, ev.HasCredit, ev.CreditUnit
	if tr.stat != nil {
		out.Result.TokensKnown = tr.stat.tokensKnown
		out.Result.PromptTokens = tr.stat.promptTokens
		out.Result.CompletionTokens = tr.stat.completionTokens
	}
	out.Reply = livenessReply(w.body.Bytes())
	if err := h.cfg.Providers.RecordLiveness(id, out.Result); err != nil {
		return out, err
	}
	log.Printf("liveness account=%s target=%s: ok=%v model=%s status=%d ttfb=%dms total=%dms error=%s", id, target, out.Result.OK, native, out.Result.Status, out.Result.TTFBMs, out.Result.DurationMs, out.Result.Error)
	return out, nil
}
