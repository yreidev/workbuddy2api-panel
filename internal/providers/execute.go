// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yreidev/workbuddy2api-panel/internal/glob"
	"github.com/yreidev/workbuddy2api-panel/internal/targets"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

type Binding struct {
	Epoch     string    `json:"epoch,omitempty"`
	AccountID string    `json:"account_id"`
	SessionID string    `json:"session_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Request struct {
	Target, Model, KeyID, Session string
	RequestID                     string
	SlotWait                      *time.Duration // 从配置取得本地排队预算。
	// TestAccountID 仅供管理员单号测活：固定账号、只试一次、不读写粘性绑定。
	TestAccountID string
	Body          []byte
	MaxRotate     int
	// Allow 每次选号和发出请求前重新读取密钥与分组权限。
	Allow func(target string, groups []string) bool
}

type Result struct {
	ProxyID                        string
	AccountID                      string
	Attempts                       int
	PromptTokens, CompletionTokens int64
	TokensKnown                    bool
	Charge                         float64
	ChargeKnown                    bool
	Unit                           string
	Started                        bool
	LedgerPending                  bool // 上游完成，账本尚待补写。
}

// Catalog 只拉取调用方可见账号的目录，不把其他分组的模型暴露给客户端。
func (m *Manager) Catalog(ctx context.Context, allow func(string, []string) bool) []map[string]any {
	out, _ := m.CatalogDetails(ctx, allow, false)
	return out
}

// CatalogDetails 面板可强制刷新并显示局部失败；客户端目录沿用缓存和权限过滤。
func (m *Manager) CatalogDetails(ctx context.Context, allow func(string, []string) bool, force bool) ([]map[string]any, []string) {
	out := []map[string]any{}
	var failures []string
	if m == nil {
		return out, failures
	}
	type catalog struct {
		account, target, epoch string
		models                 []Model
		err                    error
	}
	var catalogs []catalog
	for _, a := range m.List() {
		for _, target := range a.Targets {
			current, ok := m.catalogAccount(a.ID, target, allow)
			if !ok {
				continue
			}
			models, err := m.Models(ctx, a.ID, target, force)
			if err != nil {
				log.Printf("[providers] 拉取模型 account=%s target=%s: %v", a.ID, target, err)
			}
			catalogs = append(catalogs, catalog{a.ID, target, current.BindingEpoch, models, err})
		}
	}
	// 全部上游查询结束后才按来源授权并去重，已移出分组的账号也不能影响同名模型。
	seen := map[string]bool{}
	for _, c := range catalogs {
		current, ok := m.catalogAccount(c.account, c.target, allow)
		if !ok || current.BindingEpoch != c.epoch {
			continue
		}
		if c.err != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): %v", c.target, c.account, c.err))
			continue
		}
		for _, md := range c.models {
			id := targets.Public(c.target, md.ID)
			if seen[id] || !force && slices.ContainsFunc(current.BlockedModels, func(pattern string) bool { return glob.Match(pattern, id) }) {
				continue
			}
			seen[id] = true
			entry := map[string]any{"id": id, "object": "model", "owned_by": strings.Split(c.target, ":")[0], "target": c.target, "name": md.Name, "supports_tool_call": md.Tools, "validated": false}
			if md.Context > 0 {
				entry["context_length"] = md.Context
			}
			out = append(out, entry)
		}
	}
	return out, failures
}

func (m *Manager) catalogAccount(id, target string, allow func(string, []string) bool) (Account, bool) {
	m.mu.Lock()
	for _, a := range m.data.Accounts {
		if a.ID == id && slices.Contains(a.Targets, target) && !a.Disabled && !m.running[id].blocked {
			// 分组和限制在编辑时整体替换，回调不持有管理器锁。
			m.mu.Unlock()
			return a, allow == nil || allow(target, a.Groups)
		}
	}
	m.mu.Unlock()
	return Account{}, false
}

func validateChat(target string, raw []byte) error {
	var body struct {
		Messages []struct {
			Role      string `json:"role"`
			Content   any    `json:"content"`
			ToolCalls []any  `json:"tool_calls"`
		} `json:"messages"`
		Tools []any `json:"tools"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("解析对话请求: %w", err)
	}
	if len(body.Messages) == 0 {
		return errors.New("messages 不能为空")
	}
	for _, msg := range body.Messages {
		if target == "traework:cn" && (msg.Role == "tool" || len(msg.ToolCalls) > 0 || len(body.Tools) > 0) {
			return errors.New("TraeWork 当前仅开放文本对话，请使用 Trae Code 或 Qoder 完成客户端工具调用")
		}
		switch content := msg.Content.(type) {
		case nil, string:
		case []any:
			for _, part := range content {
				p := object(part)
				if text(p["type"]) != "text" {
					return errors.New("该平台首期仅支持文本内容，不支持图片或其他多模态内容")
				}
				if _, ok := p["text"].(string); !ok {
					return errors.New("文本内容缺少 text 字符串")
				}
			}
		default:
			return errors.New("message.content 必须是文本或文本段数组")
		}
	}
	return nil
}

func modelNotAvailable() error {
	return &Error{Status: 404, Code: "model_not_available_for_account", Message: "可选账号的目录不包含这个模型"}
}

func (m *Manager) Execute(ctx context.Context, req Request, emit func(map[string]any) error) (Result, error) {
	var result Result

	if !targets.Valid(req.Target) || targets.WorkBuddy(req.Target) {
		return result, &Error{Status: 400, Code: "invalid_target"}
	}
	if strings.TrimSpace(req.Model) == "" {
		return result, &Error{Status: 400, Code: "invalid_model", Message: "平台模型名缺少原生模型 ID"}
	}
	if m == nil {
		return result, &Error{Status: 503, Code: "provider_not_configured"}
	}
	if err := validateChat(req.Target, req.Body); err != nil {
		return result, &Error{Status: 400, Code: "unsupported_content", Message: err.Error()}
	}
	if req.MaxRotate <= 0 {
		req.MaxRotate = 3
	}
	probing := req.TestAccountID != ""
	if probing {
		req.MaxRotate = 1
		req.Session = ""
	}
	waitBudget := 30 * time.Second
	if req.SlotWait != nil {
		waitBudget = *req.SlotWait
	}
	if req.RequestID == "" {
		req.RequestID = uuid.NewString()
	}
	hash := sha256.Sum256([]byte(req.KeyID + "\x00" + req.Target + "\x00" + req.Session))
	bindingKey := hex.EncodeToString(hash[:])
	tried := map[string]bool{}
	// 状态广播会反复唤醒请求。已入队的请求保留等待名额，直到取得槽位或退出。
	queued := false
	defer func() {
		if queued {
			m.mu.Lock()
			m.leaveQueueLocked(req.KeyID)
			m.mu.Unlock()
		}
	}()
	var held *runtime
	release := func() {
		if held != nil {
			m.mu.Lock()
			held.inFlight--
			m.wakeLocked()
			m.mu.Unlock()
			held = nil
		}
	}
	defer release()
	var lastErr error
	for result.Attempts < req.MaxRotate {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := m.FlushActivity(); err != nil {
			log.Printf("[providers] 重试平台账本 request=%s: %v", req.RequestID, err)
			return result, &Error{Status: 503, Code: "ledger_unavailable", Message: "平台账本暂时无法写入"}
		}
		m.mu.Lock()
		now := time.Now()
		var chosen *Account
		var binding Binding
		if !probing {
			binding = m.data.Bindings[bindingKey]
		}
		if !now.Before(binding.ExpiresAt) {
			binding = Binding{}
		}
		waiting := false
		modelMissing := false
		for _, a := range m.data.Accounts {
			rt := m.running[a.ID]
			if probing && a.ID != req.TestAccountID {
				continue
			}
			blockedModel := slices.ContainsFunc(a.BlockedModels, func(pattern string) bool { return glob.Match(pattern, targets.Public(req.Target, req.Model)) })
			if (a.Disabled || a.Paused) && !probing || blockedModel || rt.blocked || tried[a.ID] || !slices.Contains(a.Targets, req.Target) || req.Allow != nil && !req.Allow(req.Target, a.Groups) {
				continue
			}
			if !probing && now.Before(rt.coolUntil) {
				continue
			}
			catalog := rt.catalogs[req.Target]
			if (!probing || rt.inFlight == 0) && catalog.fresh(now) && catalog.unavailable == nil && !slices.ContainsFunc(catalog.models, func(md Model) bool { return md.ID == req.Model }) {
				modelMissing = true
				continue
			}
			if rt.inFlight > 0 {
				if probing {
					m.mu.Unlock()
					return result, &Error{Status: 409, Code: "account_busy", Message: "账号有在途请求，请稍后测活"}
				}
				waiting = true
				continue
			}
			candidate := a
			if chosen == nil || a.ID == binding.AccountID && now.Before(binding.ExpiresAt) || chosen.ID != binding.AccountID && (a.Priority < chosen.Priority || a.Priority == chosen.Priority && rt.lastUsed.Before(m.running[chosen.ID].lastUsed)) {
				chosen = &candidate
			}
		}
		if chosen == nil {
			if waiting {
				if waitBudget <= 0 {
					m.mu.Unlock()
					return result, slotWaitExpired()
				}
				if !queued {
					if err := m.joinQueueLocked(req.KeyID); err != nil {
						m.mu.Unlock()
						return result, err
					}
					queued = true
				}
				changed := m.changed
				m.mu.Unlock()
				before := time.Now()
				err := waitForSlot(ctx, changed, waitBudget)
				waitBudget -= time.Since(before)
				if err != nil {
					return result, err
				}
				if waitBudget <= 0 {
					return result, slotWaitExpired()
				}
				continue
			}
			m.mu.Unlock()
			if modelMissing && lastErr == nil {
				return result, modelNotAvailable()
			}
			if lastErr != nil {
				return result, lastErr
			}
			return result, &Error{Status: 503, Code: "no_available_account"}
		}
		if queued {
			m.leaveQueueLocked(req.KeyID)
			queued = false
		}
		a := *chosen
		rt := m.running[a.ID]
		rt.inFlight++
		held = rt
		rt.lastUsed = now
		m.mu.Unlock()
		tried[a.ID] = true
		models, err := m.Models(ctx, a.ID, req.Target, false)
		var md Model
		if err == nil {
			index := slices.IndexFunc(models, func(v Model) bool { return v.ID == req.Model })
			if index < 0 {
				release()
				if lastErr == nil {
					lastErr = modelNotAvailable()
				}
				continue
			}
			md = models[index]
		}
		result.AccountID = a.ID
		result.ProxyID = a.ProxyID
		result.Attempts++
		var credential Credential
		routed := ctx
		if err == nil {
			routed, err = m.route(ctx, a.ProxyID)
		}
		if err == nil {
			credential, err = m.credential(routed, a.ID, req.Target)
		}
		checkDispatch := func() error {
			m.mu.Lock()
			defer m.mu.Unlock()
			for _, current := range m.data.Accounts {
				if current.ID == a.ID && ((!current.Disabled && !current.Paused) || probing) && !rt.blocked &&
					slices.Contains(current.Targets, req.Target) && (req.Allow == nil || req.Allow(req.Target, current.Groups)) {
					return nil
				}
			}
			return &Error{Status: 403, Code: "target_not_allowed"}
		}
		if err == nil {
			err = checkDispatch()
			routed = context.WithValue(routed, dispatchCheckKey{}, checkDispatch)
		}
		upstreamSession := uuid.NewString()
		if req.Session != "" && binding.AccountID == a.ID && now.Before(binding.ExpiresAt) {
			upstreamSession = binding.SessionID
		}
		finished := false
		result.Started = false
		result.TokensKnown = false
		result.PromptTokens = 0
		result.CompletionTokens = 0
		result.Charge, result.ChargeKnown, result.Unit = 0, false, ""
		meter := requestMeter{target: req.Target}
		if err == nil {
			err = m.adapters[strings.Split(req.Target, ":")[0]].Chat(routed, req.Target, credential, md, req.Body, upstreamSession, func(frame map[string]any) error {
				if usage := object(frame["usage"]); usage != nil {
					if err := meter.observe(usage); err != nil {
						result.Charge, result.ChargeKnown, result.Unit = 0, false, ""
						return fmt.Errorf("读取平台计费 target=%s account=%s: %w", req.Target, a.ID, err)
					}
					result.Charge, result.ChargeKnown = meter.charge()
					if result.ChargeKnown {
						result.Unit = "credits"
					}
					if usage["prompt_tokens"] != nil && usage["completion_tokens"] != nil {
						for _, field := range []string{"prompt_tokens", "completion_tokens"} {
							value, ok := usage[field].(float64)
							if !ok || value < 0 || value >= float64(math.MaxInt64) || math.Trunc(value) != value {
								return fmt.Errorf("平台 usage.%s 必须为非负整数", field)
							}
						}
						prompt, completion := number(usage["prompt_tokens"]), number(usage["completion_tokens"])
						if prompt > math.MaxInt64-completion {
							return errors.New("平台 usage token 总数溢出")
						}
						result.TokensKnown = true
						result.PromptTokens = prompt
						result.CompletionTokens = completion
					}
				}
				if choices, ok := frame["choices"].([]any); ok {
					for _, v := range choices {
						if value := object(v)["finish_reason"]; value != nil {
							reason, ok := value.(string)
							if !ok {
								return errors.New("平台 finish_reason 必须为字符串")
							}
							finished = finished || strings.TrimSpace(reason) != ""
						}
					}
				}
				frame["model"] = targets.Public(req.Target, req.Model)
				result.Started = true
				return emit(frame)
			})
			if err == nil && !finished {
				err = errors.New("平台对话提前结束，缺少 finish_reason")
			}
		}
		routeFailure := upstream.IsRouteError(err)
		var upstreamErr *Error
		retry := errors.As(err, &upstreamErr) && upstreamErr.Retry
		m.mu.Lock()
		if err != nil {
			rt.lastError = err.Error()
			if retry && !routeFailure {
				rt.coolUntil = time.Now().Add(30 * time.Second)
			}
		} else {
			rt.lastError = ""
			if probing || !rt.coolUntil.After(now) {
				rt.coolUntil = time.Time{}
			}
		}
		m.mu.Unlock()
		delta := Usage{AccountID: a.ID, Target: req.Target, Model: req.Model, KeyID: req.KeyID, Requests: 1}
		if err != nil {
			delta.Failed = 1
		}
		if result.TokensKnown {
			delta.TokensKnown, delta.PromptTokens, delta.CompletionTokens = 1, result.PromptTokens, result.CompletionTokens
		}
		if result.ChargeKnown {
			delta.ChargeKnown, delta.Charge, delta.Unit = 1, result.Charge, result.Unit
		}
		entry := attemptRecord{RequestID: req.RequestID, Attempt: result.Attempts, Delta: delta}
		if err == nil && req.Session != "" {
			entry.BindingKey = bindingKey
			entry.Binding = &Binding{Epoch: a.BindingEpoch, AccountID: a.ID, SessionID: upstreamSession, ExpiresAt: time.Now().Add(24 * time.Hour)}
		}
		saveErr := m.recordAttempt(entry)
		release()
		m.usage.Add(now, req.Target, a.ID, targets.Public(req.Target, req.Model), usage.Delta{
			Key: req.KeyID, PromptTokens: result.PromptTokens, CompletionTokens: result.CompletionTokens,
			HasPromptTokens: result.TokensKnown, HasCompletion: result.TokensKnown,
			TokensUnknown: !result.TokensKnown, HasLatency: true, LatencyMs: time.Since(now).Milliseconds(),
			Credit: result.Charge, HasCredit: result.ChargeKnown,
		}, err == nil)
		if saveErr != nil {
			log.Printf("[providers] 保存平台账本 request=%s attempt=%d account=%s upstream_completed=%t: %v", req.RequestID, result.Attempts, a.ID, err == nil, saveErr)
			result.LedgerPending = err == nil
			message := "平台账本写入失败"
			if result.LedgerPending {
				message = "上游已完成，平台账本待补写"
			}
			return result, &Error{Status: 502, Code: "ledger_write_failed", Message: message}
		}
		if err == nil {
			return result, nil
		}
		if routeFailure {
			// 代理地址、名称和底层错误仅供管理员排查。
			publicErr := upstreamErr
			if publicErr == nil {
				publicErr = &Error{Status: 503, Code: "proxy_not_available", Message: "账号代理线路不可用"}
			}
			if result.Started || ctx.Err() != nil || !canRetryRouteError(err) {
				return result, publicErr
			}
			lastErr = publicErr
			continue
		}
		var dispatched *dispatchedError
		if result.Started || ctx.Err() != nil || errors.As(err, &dispatched) {
			return result, err
		}
		// 目录为空只影响当前账号。继续选号，但沿用原目录错误，且不触发推理冷却。
		if !retry && !errors.Is(err, errQoderCatalogUnavailable) {
			return result, err
		}
		lastErr = err
	}
	return result, lastErr
}
