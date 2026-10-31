// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// official_body.go 出站 chat body 补官方 CLI 会补的字段（WorkBuddy 桌面端安装包内置的
// @tencent-ai/codebuddy-code，dist/codebuddy-lite-wb.mjs 的 injectReasoningSummaryIfNeeded；10-20 读
// 5.8.3 / 5.8.4 内置的 2.161.0，两域现在的 2.165.0 也一样）。
package upstream

import "encoding/json"

// InjectOfficialBodyFields 补官方 CLI 发请求前会补的字段，客户端自己带了就原样保留：
//   - reasoning_summary：带了思考档位（reasoning_effort 或 reasoning.effort 是字符串）时补 "auto"。
//
// 不补 cache_retention：两域桌面端现在内置的 2.165.0（和之前的 2.161.0）都不发这个字段（npm 上更新的
// 2.169.0 才对非自定义模型补 "long"），网关的 UA 报的也是 2.165.0；而且 1 小时缓存在部分模型上写入更贵，
// 实测只覆盖过 deepseek。
// body 不可解析时原样返回。
func InjectOfficialBodyFields(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	if _, ok := obj["reasoning_summary"]; ok || !hasReasoningEffort(obj) {
		return body
	}
	obj["reasoning_summary"] = "auto"
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
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
