// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import "encoding/json"

// normalizeUsageCacheAliases keeps cache-hit aliases consistent before the
// response leaves the gateway. Some WorkBuddy responses carry the real hit in
// prompt_tokens_details.cached_tokens while also emitting
// cache_read_input_tokens: 0 and cached_tokens: 0 compatibility aliases.
// Strict downstream parsers may prefer those zero aliases and lose the hit.
func normalizeUsageCacheAliases(usage map[string]any) map[string]any {
	best, ok := bestUsageCacheHitTokens(usage)
	if !ok || best <= 0 {
		return usage
	}

	out := cloneUsageMap(usage)
	out["cache_read_input_tokens"] = best
	out["cached_tokens"] = best
	out["prompt_cache_hit_tokens"] = best

	promptDetails := cloneUsageDetails(out, "prompt_tokens_details")
	promptDetails["cached_tokens"] = best
	out["prompt_tokens_details"] = promptDetails

	// Responses API consumers use this nested form. Preserve it when the
	// upstream already supplies it, but do not invent it for Chat-only clients.
	if _, exists := out["input_tokens_details"]; exists {
		inputDetails := cloneUsageDetails(out, "input_tokens_details")
		inputDetails["cached_tokens"] = best
		out["input_tokens_details"] = inputDetails
	}

	return out
}

func bestUsageCacheHitTokens(usage map[string]any) (float64, bool) {
	paths := []struct {
		section string
		key     string
	}{
		{"prompt_tokens_details", "cached_tokens"},
		{"", "prompt_cache_hit_tokens"},
		{"", "cache_read_input_tokens"},
		{"", "cached_tokens"},
		{"input_tokens_details", "cached_tokens"},
	}

	for _, path := range paths {
		var value any
		if path.section == "" {
			value = usage[path.key]
		} else if details, ok := usage[path.section].(map[string]any); ok {
			value = details[path.key]
		}
		if tokens, ok := positiveUsageNumber(value); ok {
			return tokens, true
		}
	}
	return 0, false
}

func positiveUsageNumber(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, n > 0
	case float32:
		value := float64(n)
		return value, value > 0
	case int:
		return float64(n), n > 0
	case int64:
		return float64(n), n > 0
	case int32:
		return float64(n), n > 0
	case uint:
		return float64(n), n > 0
	case uint64:
		return float64(n), n > 0
	case uint32:
		return float64(n), n > 0
	default:
		return 0, false
	}
}

func cloneUsageMap(usage map[string]any) map[string]any {
	out := make(map[string]any, len(usage))
	for key, value := range usage {
		out[key] = value
	}
	return out
}

func cloneUsageDetails(usage map[string]any, key string) map[string]any {
	out := make(map[string]any)
	details, _ := usage[key].(map[string]any)
	for detailKey, value := range details {
		out[detailKey] = value
	}
	return out
}

// UsageCacheHitTokens 返回 usage 里的缓存命中 token 数（多别名取最优，口径与
// 回写给客户端的 normalizeUsageCacheAliases 一致）。供网关统计层（usage 桶 /
// reqlog）观测命中率使用；usage 缺失该维度时 ok=false。
func UsageCacheHitTokens(usage map[string]any) (float64, bool) {
	if usage == nil {
		return 0, false
	}
	return bestUsageCacheHitTokens(usage)
}

// UsageCacheMissTokens 返回 usage 里的缓存未命中 token 数：优先读上游显式的
// prompt_cache_miss_tokens，缺失时按 prompt_tokens - 命中 推导（推导值为负时
// 视为不可信，返回 ok=false）。
func UsageCacheMissTokens(usage map[string]any) (float64, bool) {
	if usage == nil {
		return 0, false
	}
	if miss, ok := usageNumber(usage, "prompt_cache_miss_tokens"); ok {
		return miss, true
	}
	prompt, okP := usageNumber(usage, "prompt_tokens")
	hit, okH := UsageCacheHitTokens(usage)
	if okP && okH && prompt-hit >= 0 {
		return prompt - hit, true
	}
	return 0, false
}

// UsageCacheTokens 统计层用的前缀缓存观测：命中同 UsageCacheHitTokens（多别名取最优），未命中同
// UsageCacheMissTokens。和 UsageCacheHitTokens 不同，命中为 0 也算有观测：usage 里只要出现任意一个
// 缓存字段（含 prompt_cache_miss_tokens），整段未命中的请求也要计入命中率、交给未命中告警判断。
// 一个缓存字段都没有时 ok=false。
//
// 命中为 0 而未命中是显式给的、又比 prompt_tokens 小时，按 prompt_tokens - 未命中 反推命中：上游在
// 接近全命中时会回 hit=0 + miss=3、prompt 几万这种对不上的组合，不反推会被当成整段未命中、打出
// 误报的 WARN（照上游 linguo2625469 329c09e，issue #170）。未命中不小于 prompt_tokens 时是真的整段
// 未命中，命中保持 0。
func UsageCacheTokens(usage map[string]any) (hit, miss float64, ok bool) {
	if !hasUsageCacheField(usage) {
		return 0, 0, false
	}
	hit, _ = UsageCacheHitTokens(usage)
	miss, _ = UsageCacheMissTokens(usage)
	_, explicitMiss := usageNumber(usage, "prompt_cache_miss_tokens")
	prompt, hasPrompt := usageNumber(usage, "prompt_tokens")
	switch {
	case hit == 0 && !explicitMiss:
		// 命中为 0 时 UsageCacheMissTokens 推导不出（它只认正的命中），按 prompt_tokens 全量算未命中。
		miss = prompt
	case hit == 0 && hasPrompt && prompt > miss:
		hit = prompt - miss
	}
	return hit, miss, true
}

// hasUsageCacheField usage 里有没有任意一个缓存命中 / 未命中字段（值为 0 也算）。
func hasUsageCacheField(usage map[string]any) bool {
	if usage == nil {
		return false
	}
	for _, key := range []string{"prompt_cache_hit_tokens", "prompt_cache_miss_tokens", "cache_read_input_tokens", "cached_tokens"} {
		if _, ok := usageNumber(usage, key); ok {
			return true
		}
	}
	for _, section := range []string{"prompt_tokens_details", "input_tokens_details"} {
		if details, ok := usage[section].(map[string]any); ok {
			if _, ok := usageNumber(details, "cached_tokens"); ok {
				return true
			}
		}
	}
	return false
}

// usageNumber 从 usage 顶层取数值字段（JSON 数字可能是 float64 / json.Number 形态）。
func usageNumber(usage map[string]any, key string) (float64, bool) {
	v, ok := usage[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
