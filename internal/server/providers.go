// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yreidev/workbuddy2api-panel/internal/providers"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
	"github.com/yreidev/workbuddy2api-panel/internal/targets"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

func (h *Handler) targetModel(ctx context.Context, model string) (string, string, string) {
	if target, native, explicit := targets.Parse(model); explicit {
		return targets.Public(target, native), target, native
	}
	p := principalFrom(ctx)
	if p.key != nil {
		if key, ok := h.cfg.Keys.Get(p.key.ID, time.Now()); ok {
			if key.DefaultTarget != "" {
				return targets.Public(key.DefaultTarget, model), key.DefaultTarget, model
			}
			// 只有一个 WorkBuddy 地区的密钥，裸名不能借跨域策略访问另一个地区。
			if key.AllowsTarget("workbuddy:cn") != key.AllowsTarget("workbuddy:global") {
				target := "workbuddy:cn"
				if key.AllowsTarget("workbuddy:global") {
					target = "workbuddy:global"
				}
				return targets.Public(target, model), target, model
			}
		}
	}
	return model, "", model
}

func (h *Handler) targetAllowed(ctx context.Context, target string, groups []string, checkGroups bool) bool {
	return h.targetModelAllowed(ctx, target, "", groups, checkGroups)
}

func (h *Handler) targetModelAllowed(ctx context.Context, target, model string, groups []string, checkGroups bool) bool {
	p := principalFrom(ctx)
	if p.key == nil {
		return true
	}
	key, ok := h.cfg.Keys.Get(p.key.ID, time.Now())
	if !ok || !key.Enabled || key.Expired(time.Now()) || !key.AllowsTarget(target) || model != "" && !key.AllowsTargetModel(target, model) {
		return false
	}
	if !checkGroups || len(key.Groups) == 0 {
		return true
	}
	return slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(key.Groups, g) })
}

func (h *Handler) providerChat(w http.ResponseWriter, r *http.Request, body []byte, target, model string, stream bool) {
	st := newChatStat(time.Now(), rewriteModel(body, targets.Public(target, model)), stream)
	st.target = target
	if tr := requestTraceFrom(r); tr != nil {
		tr.stat = st
		st.clientIP, st.userAgent = tr.clientIP, tr.userAgent
	}
	defer st.done()
	if _, hidden := h.loadLive().HiddenModels[targets.Public(target, model)]; hidden {
		writeOpenAIError(w, 400, "model_unavailable", "模型已被网关隐藏")
		st.status = 400
		st.outcome = reqlog.OutcomeHTTPError
		return
	}
	p := principalFrom(r.Context())
	usageKey := p.usageKey()
	if livenessFrom(r.Context()) != "" {
		usageKey = livenessUsageKey
	}
	admitted := time.Now()
	var result providers.Result
	if p.key != nil {
		if denied := h.cfg.Keys.AdmitTarget(p.key.ID, target, model, admitted); denied != nil {
			denialHeaders(w, denied)
			writeOpenAIError(w, denied.Status, denied.Code, denied.Message)
			st.status = denied.Status
			st.outcome = reqlog.OutcomeHTTPError
			return
		}
		defer func() {
			// 未选到账号便取消的排队请求退次数；已进入账号处理的中断仍计一次。
			if st.outcome != reqlog.OutcomeSuccess && (st.outcome != reqlog.OutcomeInterrupted || result.Attempts == 0) {
				h.cfg.Keys.Refund(p.key.ID, admitted)
			}
		}()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	started := false
	writeFailed := false
	var buffer bytes.Buffer
	id := "chatcmpl-" + uuid.NewString()
	created := time.Now().Unix()
	writeFrame := func(frame map[string]any) error {
		frame["id"] = id
		frame["created"] = created
		raw, marshalErr := json.Marshal(frame)
		if marshalErr != nil {
			return marshalErr
		}
		if !stream {
			_, _ = fmt.Fprintf(&buffer, "data: %s\n\n", raw)
			return nil
		}
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(200)
			started = true
			st.ttfb = time.Since(st.start)
		}
		if _, writeErr := fmt.Fprintf(w, "data: %s\n\n", raw); writeErr != nil {
			writeFailed = true
			return writeErr
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return nil
	}
	// 结束原因和用量可能早于完成标记到达；等完整性检查及落盘成功后再让翻译器收尾。
	terminal := map[string]any{"object": "chat.completion.chunk", "model": targets.Public(target, model), "choices": []any{}}
	var slotWait *time.Duration
	if h.cfg.Pool != nil {
		wait := h.cfg.Pool.InFlightWait()
		slotWait = &wait
	}
	requestID := id
	if tr := requestTraceFrom(r); tr != nil {
		requestID = tr.id
	}
	result, err := h.cfg.Providers.Execute(ctx, providers.Request{RequestID: requestID, SlotWait: slotWait, Target: target, Model: model, KeyID: usageKey, Session: session.ExtractKey(body), TestAccountID: livenessFrom(r.Context()), Body: body, MaxRotate: h.cfg.MaxRotate, Allow: func(t string, groups []string) bool {
		return h.targetModelAllowed(ctx, t, model, groups, true)
	}}, func(frame map[string]any) error {
		if usage, ok := frame["usage"].(map[string]any); ok {
			merged, _ := terminal["usage"].(map[string]any)
			// 仅向客户端透出标准 OpenAI token 字段，过滤网关内部计费键（credit/credits/billable 等）。
			for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "prompt_tokens_details", "completion_tokens_details"} {
				if value, exists := usage[key]; exists {
					if merged == nil {
						merged = map[string]any{}
					}
					merged[key] = value
				}
			}
			if merged != nil {
				terminal["usage"] = merged
			}
			delete(frame, "usage")
		}
		if stream {
			if choices, ok := frame["choices"].([]any); ok {
				for _, value := range choices {
					if choice, ok := value.(map[string]any); ok && choice["finish_reason"] != nil {
						if reason, ok := choice["finish_reason"].(string); ok && strings.TrimSpace(reason) != "" {
							termChoices, _ := terminal["choices"].([]any)
							entry := map[string]any{"index": choice["index"], "delta": map[string]any{}, "finish_reason": reason}
							replaced := false
							for i, existing := range termChoices {
								if ec, ok := existing.(map[string]any); ok && fmt.Sprint(ec["index"]) == fmt.Sprint(choice["index"]) {
									termChoices[i] = entry
									replaced = true
									break
								}
							}
							if !replaced {
								termChoices = append(termChoices, entry)
							}
							terminal["choices"] = termChoices
						}
						choice["finish_reason"] = nil
					}
				}
			}
		}
		if !providerFrameHasPayload(frame) {
			return nil
		}
		return writeFrame(frame)
	})
	st.uid = result.AccountID
	st.nick = result.AccountName
	st.proxy = result.ProxyName
	st.attempts = result.Attempts
	st.promptTokens = result.PromptTokens
	st.completionTokens = result.CompletionTokens
	st.totalTokens = result.PromptTokens + result.CompletionTokens
	st.tokensKnown = result.TokensKnown
	st.credit, st.hasCredit, st.creditUnit = result.Charge, result.ChargeKnown, result.Unit
	if result.TokensKnown {
		st.toks = int(result.CompletionTokens)
		if p.key != nil {
			h.cfg.Keys.Record(p.key.ID, admitted, st.totalTokens, 0, false)
		}
	}
	interrupted := func() bool {
		if cancelErr := r.Context().Err(); cancelErr != nil {
			st.status, st.outcome, st.errMsg = 499, reqlog.OutcomeInterrupted, cancelErr.Error()
			if started {
				st.status = 200
			}
			return true
		}
		return false
	}
	if err == nil && interrupted() {
		return
	}
	if err != nil {
		st.errMsg = err.Error()
		st.status = 502
		st.outcome = reqlog.OutcomeHTTPError
		var providerErr *providers.Error
		code := "provider_error"
		if errors.As(err, &providerErr) {
			st.status = providerErr.Status
			code = providerErr.Code
		}
		if result.LedgerPending {
			st.outcome = reqlog.OutcomeLedgerPending
		} else if r.Context().Err() != nil || writeFailed {
			st.outcome = reqlog.OutcomeInterrupted
		}
		if started {
			st.status = 200
			if st.outcome == reqlog.OutcomeHTTPError {
				st.outcome = reqlog.OutcomeStreamError
			}
			raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": err.Error(), "type": "upstream_error", "code": code}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		writeOpenAIError(w, st.status, code, err.Error())
		return
	}
	if stream {
		if writeErr := writeFrame(terminal); writeErr != nil {
			st.status = 200
			st.outcome = reqlog.OutcomeInterrupted
			st.errMsg = writeErr.Error()
			return
		}
		if interrupted() {
			return
		}
		if _, writeErr := fmt.Fprint(w, "data: [DONE]\n\n"); writeErr != nil {
			st.status = 200
			st.outcome = reqlog.OutcomeInterrupted
			st.errMsg = writeErr.Error()
			return
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	} else {
		if writeErr := writeFrame(terminal); writeErr != nil {
			st.status, st.outcome, st.errMsg = 502, reqlog.OutcomeHTTPError, writeErr.Error()
			writeOpenAIError(w, 502, "provider_parse", writeErr.Error())
			return
		}
		buffer.WriteString("data: [DONE]\n\n")
		response, parseErr := upstream.Aggregate(&buffer)
		if parseErr != nil {
			st.status = 502
			st.outcome = reqlog.OutcomeHTTPError
			writeOpenAIError(w, 502, "provider_parse", parseErr.Error())
			return
		}
		if interrupted() {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if writeErr := json.NewEncoder(w).Encode(response); writeErr != nil {
			st.status, st.outcome, st.errMsg = 200, reqlog.OutcomeInterrupted, writeErr.Error()
			return
		}
	}
	st.status = 200
	st.outcome = reqlog.OutcomeSuccess
}

// providerFrameHasPayload 判断剥离 usage 和（流式）finish_reason 后，帧是否仍含可交付内容，
// 避免向客户端输出空 delta 帧或提前开启 HTTP 200 SSE 响应头。
func providerFrameHasPayload(frame map[string]any) bool {
	choices, ok := frame["choices"].([]any)
	if !ok || len(choices) == 0 {
		return false
	}
	for _, value := range choices {
		choice, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if reason, ok := choice["finish_reason"].(string); ok && strings.TrimSpace(reason) != "" {
			return true
		}
		if delta, ok := choice["delta"].(map[string]any); ok {
			for _, v := range delta {
				if v != nil {
					return true
				}
			}
		}
	}
	return false
}

func (h *Handler) workbuddyAccountAllowed(ctx context.Context, uid, model string) bool {
	if principalFrom(ctx).key == nil {
		return true
	}
	account, ok := h.cfg.Pool.Status(uid)
	return ok && h.targetModelAllowed(ctx, "workbuddy:"+account.Realm, model, account.Groups, true)
}
