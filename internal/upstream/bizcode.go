// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// 上游业务码，名字和含义照官方客户端（WorkBuddy 桌面端内置 CLI codebuddy-lite-wb.mjs
// 的错误码枚举、isTransientRateLimitBusinessCode、isQuotaExhaustedError、classifyErrorDetail）。
const (
	codeCraftRateLimit = 6000 // CraftRateLimit：6000–6008 是按模型的限额，上游文案带「您也可以切换其他模型继续使用」
	codeCraftRateTPS   = 6001 // 每秒 token
	codeCraftRateTPM   = 6002 // 每分钟 token
	codeCraftRateRPS   = 6005 // 每秒请求
	codeCraftRateRPM   = 6006 // 每分钟请求
	codeCraftRateRPD   = 6008 // CraftRateRPDLimit，限额段的最后一个

	// 容量排队（桌面端 5.9.3 渲染层的 QueuePollingManager 与主进程 conversations.js 的 CapacityQueueManager）：
	// 6020 WAITING 排进队了，查 /v2/chat/queue/status 到号后原样重发（capqueue.go）；6021 FULL 队列满，
	// 只提示不重试；6022 USER_LIMIT 这个用户排队的数量到上限，只提示不重试。
	codeCapacityQueueWaiting   = 6020
	codeCapacityQueueFull      = 6021
	codeCapacityQueueUserLimit = 6022

	codeUsageLimitExceeded      = 14001 // UsageLimitExceeded
	codeRateLimitError          = 14003 // RateLimitError：上游文案「模型繁忙，请换模型或稍后重试」
	codeUsageLimitEnterprise    = 14012 // UsageLimitExceededEnterprise
	codeUsageLimitTencent       = 14013 // UsageLimitExceededTencent
	codeEnterpriseExhausted     = 14014 // UsageLimitEnterpriseExhausted
	codeLicenseExpired          = 14015 // UsageLimitLicenseExpired（官方归 auth_expired）
	codeEnterpriseNotActivated  = 14016 // UsageLimitEnterpriseNotActivated
	codeUserNotActivated        = 14017 // UsageLimitUserNotActivated（trial not activated）
	codeUserExhausted           = 14018 // UsageLimitUserExhausted
	codeNoTokenBudget           = 14019 // UsageLimitNoTokenBudget
	codeUsageLimitDepartment    = 14022 // UsageLimitExceededDepartment（部门额度用完；桌面端 5.9.3 内置的 CLI 还没有，npm 上的官方 CLI 2.179.0 有）
	codeEnterprisePackageEmpty  = 14023 // UsageLimitEnterprisePackageExhausted（企业资源包用完；同上）
	codeConversationLimitExceed = 10105 // ConversationLimitExceeded（官方归 quota_active_session：同时进行的会话太多）
)

// BizCode 取错误体里的业务码，取不到返回 0。口径同官方 extractStructuredBizCode：
// 依次看顶层、data、error、response.data 里的 code，只认 ≥1000 的数字或纯数字字符串
// （HTTP 状态码一类的小数字不算业务码）。
func BizCode(body string) int {
	if !strings.Contains(body, `"code"`) {
		return 0
	}
	var root any
	if json.Unmarshal([]byte(body), &root) != nil {
		return 0
	}
	return bizCodeOf(root, 6)
}

func bizCodeOf(v any, depth int) int {
	node, ok := v.(map[string]any)
	if !ok || depth <= 0 {
		return 0
	}
	switch c := node["code"].(type) {
	case float64:
		if c >= 1000 && c == float64(int(c)) {
			return int(c)
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(c)); err == nil && n >= 1000 {
			return n
		}
	}
	if c := bizCodeOf(node["data"], depth-1); c != 0 {
		return c
	}
	if c := bizCodeOf(node["error"], depth-1); c != 0 {
		return c
	}
	if resp, ok := node["response"].(map[string]any); ok {
		return bizCodeOf(resp["data"], depth-1)
	}
	return 0
}

// isCraftRateCode 6000–6008：按模型的限额，换别的模型照样能用。
func isCraftRateCode(code int) bool {
	return code >= codeCraftRateLimit && code <= codeCraftRateRPD
}

// isTransientRateCode 官方 isTransientRateLimitBusinessCode：14003 与每秒、每分钟的限额。
// 官方遇到它们原样重发，退避从 0.5 秒起、封顶 32 秒。
func isTransientRateCode(code int) bool {
	switch code {
	case codeRateLimitError, codeCraftRateTPS, codeCraftRateTPM, codeCraftRateRPS, codeCraftRateRPM:
		return true
	}
	return false
}

// isQuotaExhaustedCode 官方 isQuotaExhaustedError 的码集合：额度用完，官方不重试。
func isQuotaExhaustedCode(code int) bool {
	switch code {
	case codeUsageLimitExceeded, codeUsageLimitEnterprise, codeUsageLimitTencent,
		codeEnterpriseExhausted, codeUserExhausted, codeNoTokenBudget,
		codeUsageLimitDepartment, codeEnterprisePackageEmpty:
		return true
	}
	return false
}

// isAccountStateCode 账号本身的状态问题（授权过期、未激活），重试等不来自愈。
func isAccountStateCode(code int) bool {
	switch code {
	case codeLicenseExpired, codeEnterpriseNotActivated, codeUserNotActivated:
		return true
	}
	return false
}

// codeKind 业务码直接决定的分类（Classify 与 SSE error 帧共用），码表里没有的返回 ErrNone。
func codeKind(code int) ErrKind {
	switch {
	case isQuotaExhaustedCode(code):
		return ErrHardCredit
	case isAccountStateCode(code):
		return ErrAccountFault
	case isCraftRateCode(code), isTransientRateCode(code), code == codeConversationLimitExceed:
		return ErrSoftRate
	case code == codeCapacityQueueFull:
		return ErrUpstreamBusy
	case code == codeCapacityQueueWaiting:
		return ErrQueueWaiting
	case code == codeCapacityQueueUserLimit:
		return ErrQueueUserLimit
	}
	return ErrNone
}

// IsModelRateLimit 报告错误体是否是按模型的限额（6000–6008）：账号整体健康，只是这个模型
// 此刻用不了（issue #31）。用于区分「账号级软限流」和「切模型即可用」。
func IsModelRateLimit(body string) bool {
	return isCraftRateCode(BizCode(body))
}

// IsTransientRateLimit 报告错误体是否是几秒到几十秒就会过去的限流（14003「模型繁忙」与
// 每秒、每分钟限额）。网关只冷却这个号的这个模型一小会儿，不冷却整个号。
func IsTransientRateLimit(body string) bool {
	return isTransientRateCode(BizCode(body))
}

// IsActiveSessionLimit 报告错误体是否是 10105：这个号同时进行的会话太多，换号或稍等即可。
func IsActiveSessionLimit(body string) bool {
	return BizCode(body) == codeConversationLimitExceed
}

// RateLimitReason 限流类冷却在台账里的原因文案：码表里的限流带上业务码，其余用通用文案。
func RateLimitReason(body string) string {
	switch code := BizCode(body); {
	case code == codeRateLimitError:
		return "14003 model busy"
	case isCraftRateCode(code):
		return fmt.Sprintf("%d model rate limit", code)
	case code == codeConversationLimitExceed:
		return "10105 too many active sessions"
	}
	return "429 rate limit"
}

// AccountFaultReason 账号级故障的冷却原因（11140 走禁用，不经过这里）。
func AccountFaultReason(body string) string {
	switch BizCode(body) {
	case codeLicenseExpired:
		return "account fault (14015 license expired)"
	case codeEnterpriseNotActivated:
		return "account fault (14016 enterprise not activated)"
	}
	return "account fault (14017)"
}
