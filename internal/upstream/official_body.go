// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// official_body.go 出站 chat body 补官方 CLI 会补的字段（WorkBuddy 桌面端 5.11.3 / 5.11.4 内置的
// @tencent-ai/codebuddy-code 2.176.0，dist/codebuddy-lite-wb.mjs 的 axiosToFetchAdapter、
// resolveCacheRetention、injectReasoningSummaryIfNeeded、buildHarnessMetadata 与
// MessageInitiatorRequestProcessor）。
package upstream

import (
	"bytes"
	"encoding/json"

	"github.com/yreidev/workbuddy2api-panel/internal/session"
)

// defaultCacheRetention 官方 CLI 2.176.0 的默认缓存保留档位（resolveCacheRetention：
// 未设 CODEBUDDY_DISABLE_PROMPT_CACHING_1H 时固定返回 "long" 并在会话内锁定）。
const defaultCacheRetention = "long"

// OfficialBodyOpts 控制出站请求体按官方 CLI 2.176.0 补齐的字段参数。
type OfficialBodyOpts struct {
	// CliVersion 内置 CLI 版本（写入 extra_vars.harnessMetadata.harnessVersion）；空则取国内版默认。
	CliVersion string
	// CacheRetention 未显式带 cache_retention 时补的值；空则按官方默认 "long"。
	CacheRetention string
	// HarnessStep 当前对话轮内的步骤位置（stepIndex 与上一步 X-Conversation-Message-ID）；
	// nil 时按首步（stepIndex=0，无 previous）处理。
	HarnessStep *session.HarnessStep
}

type harnessPrevious struct {
	ConversationMessageID string `json:"conversationMessageId"`
}

type harnessAgent struct {
	Depth int `json:"depth"`
}

type harnessMetadataPayload struct {
	SchemaVersion  int              `json:"schemaVersion"`
	HarnessVersion string           `json:"harnessVersion"`
	StepIndex      int              `json:"stepIndex"`
	Previous       *harnessPrevious `json:"previous,omitempty"`
	Agent          harnessAgent     `json:"agent"`
}

// InjectOfficialBodyFields 用默认选项（国内版 CLI 2.176.0、cache_retention="long"、首步 stepIndex=0）
// 补齐官方 CLI 请求体字段，客户端自己带了的字段均原样保留；body 不可解析时原样返回。
func InjectOfficialBodyFields(body []byte) []byte {
	return InjectOfficialBodyFieldsWithOpts(body, OfficialBodyOpts{})
}

// InjectOfficialBodyFieldsWithOpts 按官方内置 CLI 2.176.0 的 axiosToFetchAdapter 与
// MessageInitiatorRequestProcessor 补齐出站 chat/completions 请求体字段，客户端自己带了的原样保留：
//   - cache_retention：官方非自定义模型缺席时默认补 "long"（会话内一致）；
//   - reasoning_summary：带了思考档位（reasoning_effort 或 reasoning.effort 为字符串）时补 "auto"；
//   - extra_vars.harnessMetadata：带 messages 数组时合并补 JSON 字符串，含 schemaVersion(1)、
//     harnessVersion(CLI 版本)、stepIndex、stepIndex>0 且有上一步消息 id 时的 previous、agent.depth(0)，
//     保留客户端原有的其他 extra_vars 键；
//   - extra_vars.initiator：带 messages 数组且末尾消息为普通 role=="user" 时补 "user"，末尾为 tool 等角色时不写。
//
// body 为空或不是 JSON 对象时原样返回。
func InjectOfficialBodyFieldsWithOpts(body []byte, opts OfficialBodyOpts) []byte {
	if len(body) == 0 {
		return body
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return body
	}
	changed := false

	if _, exists := obj["cache_retention"]; !exists {
		retention := opts.CacheRetention
		if retention == "" {
			retention = defaultCacheRetention
		}
		obj["cache_retention"] = retention
		changed = true
	}

	if _, exists := obj["reasoning_summary"]; !exists && hasReasoningEffort(obj) {
		obj["reasoning_summary"] = "auto"
		changed = true
	}

	if msgs, ok := obj["messages"].([]any); ok {
		extra, _ := obj["extra_vars"].(map[string]any)
		extraChanged := false

		if extra == nil || !hasKey(extra, "harnessMetadata") {
			cliVer := opts.CliVersion
			if cliVer == "" {
				cliVer = defaultCliVersionCN
			}
			var step session.HarnessStep
			if opts.HarnessStep != nil {
				step = *opts.HarnessStep
			}
			meta := harnessMetadataPayload{
				SchemaVersion:  1,
				HarnessVersion: cliVer,
				StepIndex:      step.Index,
				Agent:          harnessAgent{Depth: 0},
			}
			if step.Index > 0 && step.PreviousMessageID != "" {
				meta.Previous = &harnessPrevious{ConversationMessageID: step.PreviousMessageID}
			}
			if rawMeta, err := json.Marshal(meta); err == nil {
				if extra == nil {
					extra = map[string]any{}
				}
				extra["harnessMetadata"] = string(rawMeta)
				extraChanged = true
			}
		}

		if (extra == nil || !hasKey(extra, "initiator")) && trailingMessageIsUser(msgs) {
			if extra == nil {
				extra = map[string]any{}
			}
			extra["initiator"] = "user"
			extraChanged = true
		}

		if extraChanged {
			obj["extra_vars"] = extra
			changed = true
		}
	}

	if !changed {
		return body
	}
	out, err := marshalJSONNoEscape(obj)
	if err != nil {
		return body
	}
	return out
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

// trailingMessageIsUser 报告 messages 末尾消息是否为普通 user 消息（对应官方
// MessageInitiatorRequestProcessor 的 resolveTrailingMessageInitiator：末尾为 tool 等非 user 角色时不写 initiator）。
func trailingMessageIsUser(msgs []any) bool {
	if len(msgs) == 0 {
		return false
	}
	last, ok := msgs[len(msgs)-1].(map[string]any)
	if !ok {
		return false
	}
	role, _ := last["role"].(string)
	return role == "user"
}

// marshalJSONNoEscape 同 JS JSON.stringify，不把 <、>、& 转义为 \u003c 等形式。
func marshalJSONNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// hasReasoningEffort 报告 body 是否带了思考档位：官方判定是 reasoning_effort 或 reasoning.effort 为字符串。
func hasReasoningEffort(obj map[string]any) bool {
	if _, ok := obj["reasoning_effort"].(string); ok {
		return true
	}
	r, _ := obj["reasoning"].(map[string]any)
	_, ok := r["effort"].(string)
	return ok
}
