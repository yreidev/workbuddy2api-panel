// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// Package upstream 封装对 CodeBuddy 上游（chat / billing / auth）的全部 HTTP 调用，
// 以及错误分类（驱动 pool 冷却状态机）。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
)

// ErrKind 错误分类，pool 据此决定冷却时长。
type ErrKind int

const (
	ErrNone           ErrKind = iota // 成功
	ErrHardCredit                    // 余额不足（402 或 body 关键词）→ 长冷却
	ErrSoftRate                      // 429 软限流 → 短冷却
	ErrSessionDead                   // 401 + 12153 offline session 失效 → 禁用
	ErrNotFound                      // 404 上游偶发 → 短冷却，不累计错误计数（防雪崩）
	ErrServer                        // 5xx 上游故障
	ErrContentBlocked                // 内容策略拦截（400 + 审核文案）→ 不罚账号，走降级重试
	ErrBadParams                     // 请求体解析失败（400 + Unmarshal chat params failed / 11101）→ 请求级错误：不罚号、不轮转，末端 400 透传原文
	ErrAccountFault                  // 账号级授权/配额故障（11140 request illegal / 14017 trial not activated）→ 冷却轮换，不无限重试
	ErrModelBlocked                  // 11102「该后端无此模型」→ (账号,模型) 负缓存避让，切模型/切账号
	ErrWafBlock                      // 403 + 非业务信封体（APISIX WAF 拦截页/空体）→ 账号软冷却 + 抖动退避
	ErrPromptTooLong                 // 11115「prompt is too long」→ 请求级错误（上下文超限是请求的问题非账号的问题）：不罚号、不轮转，末端透传原文
	ErrImageInvalid                  // 图片请求格式/数据无效 → 请求级错误：不罚号、不轮转，末端透传原文
	ErrUpstreamBusy                  // 6021 容量排队满（渠道级，不是账号问题）→ 不罚号、不轮转，末端 503 + upstream_busy
	ErrQueueWaiting                  // 6020 排进容量队列了 → 不罚号、不换号，在这个号上排队到号后重发；不等了才回 503
	ErrQueueUserLimit                // 6022 这个号排队的数量到上限 → 不罚号，换号
	ErrClient                        // 其他 4xx / 业务错误
)

func (k ErrKind) String() string {
	switch k {
	case ErrHardCredit:
		return "hard_credit"
	case ErrSoftRate:
		return "soft_rate"
	case ErrSessionDead:
		return "session_dead"
	case ErrNotFound:
		return "not_found"
	case ErrServer:
		return "server"
	case ErrContentBlocked:
		return "content_blocked"
	case ErrBadParams:
		return "bad_params"
	case ErrModelBlocked:
		return "model_blocked"
	case ErrWafBlock:
		return "waf_block"
	case ErrPromptTooLong:
		return "prompt_too_long"
	case ErrImageInvalid:
		return "image_invalid"
	case ErrUpstreamBusy:
		return "upstream_busy"
	case ErrQueueWaiting:
		return "queue_waiting"
	case ErrQueueUserLimit:
		return "queue_user_limit"
	case ErrAccountFault:
		return "account_fault"
	case ErrClient:
		return "client"
	default:
		return "none"
	}
}

// Error 带分类的上游错误。
type Error struct {
	Kind   ErrKind
	Status int
	Msg    string
	// RetryAfter 上游明示的等待时长（Retry-After 秒 / retry-after-ms /
	// x-ratelimit-reset 头解析，见 ParseRetryAfter）。零值 = 上游未明示，
	// 冷却时长回落调用方计算值。挂载点选在 Error 信封：Kind 决定「罚不罚」，
	// RetryAfter 决定「罚多久」，同为上游响应的一等公民。
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	return fmt.Sprintf("upstream %s (http %d): %s", e.Kind, e.Status, e.Msg)
}

// hardMarkers 余额不足关键词（小写比较 + 中文原文比较双通道）。
var hardMarkers = []string{
	"insufficient credit", "no credit", "credit exhausted", "credits exhausted", "out of credit",
	"quota exceeded", "quota exhaust", "payment required", "credit not enough",
	"not enough credit",
	"积分不足", "额度不足", "余额不足", "积分用完", "额度用尽", "没有积分",
}

// softRateMarkers 限流/节流关键词（小写比较 + 中文原文比较双通道）。
// 上游在状态码非 429 时也会返回限流语义（如 200 + code 11140
// "The model provider is rate-limiting requests."、400 + "rate limit"），
// 此类响应若不识别，账号既不被冷却也不喂熔断，下次请求仍会被选中（issue #28）。
//
// 词表按子串匹配，宁缺毋滥：只收录明确指向「请求速率/模型用量被节流」的措辞。
// 连字符形式（rate-limiting / rate-limited）需单列——Contains 不跨 '-'。
// "too many" 会命中 "too many tokens" 这类客户端参数错误，代价是该号被软冷却
// 一个 SoftCooldown（默认 60s）后自愈，远小于漏判限流导致反复选中同一号的代价。
var softRateMarkers = []string{
	"rate limit", // rate limit / rate limits / rate limiting
	"rate-limiting",
	"rate-limited",
	"too many requests",
	"too many",
	"usage limit", // usage limit reached / model usage limit exceeded（用量节流，非计费余额）
	"请求过于频繁", "限流",
}

var sessionDeadMarkers = []string{"Offline user session not found", "12153"}

// accountFaultMarkers 账号级授权/配额故障关键词（大小写不敏感子串匹配）。
//
// 定位：这类错误是**账号本身状态**决定的本机故障，不是请求格式、不是临时限流、
// 也不是内容误报——继续重试只会反复刷上游风控/配额检查，必须把该账号冷却轮换。
//   - "request illegal"（code 11140）→ 上游 auth/auth_forbidden，账号级授权风控，
//     需重新 OAuth 登录才能恢复，短冷却只能阻止继续送死。
//   - code 14017（"trial not activated" / "The trial version is not yet activated"）→
//     上游 quota/quota_not_activated，register 未完成的试用未激活账号，同样账号级。
//
// 注意 11140 **不能**按 code 判定：该 code 也承载模型级限流文案（"The model provider
// is rate-limiting requests."），那种场景必须保持 ErrSoftRate（下方 softRateMarkers
// 后判定）。故此处只收 msg 关键词 "request illegal"（auth_forbidden 的真实文案）。
// 14017 文案唯一（无软限流歧义），可安全收录。
var accountFaultMarkers = []string{
	"request illegal",
	"trial not activated",
	"trial version is not yet activated",
}

// contentBlockedMarkers 内容策略拦截关键词（大小写不敏感子串匹配）。
//
// 定位：上游按逐字精确指纹审核，system 来源的模板句（如 Claude Code/Codex
// 注入指令）触发 HTTP 400 + 以下文案。这是「误报」（合法流量被审核误杀），
// 非账号问题——该账号余额健康、未限流、session 未死，故 ErrContentBlocked
// 在 applyErrorPolicy 中不罚账号（无冷却/熔断/NoteError），改由网关降级重试。
var contentBlockedMarkers = []string{
	"blocked by security policy",
	"unapproved channel",
	"illegal api invocation",
}

// badParamsMarkers 请求体解析失败关键词（issue #41 连带）：HTTP 400 + 上游
// "Unmarshal chat params failed..."（code 11101）。这是"发给上游的 body 有问题"，
// 与账号健康无关——不罚号，但仍轮转（commit B）。
var badParamsMarkerMsg = "Unmarshal chat params failed"

// invalidImageMarkers 图片请求格式/数据无效（HTTP 400）的**文案**形态。这类错误由
// 请求内容决定，不是账号问题：换账号不会改变同一 body 的解析结果。上游常见形态包括
// `Parse message failed: invalid image_url content`、invalid_image_data、
// `replace the image`。
//
// 业务码 11135 不放在这里：code 判定必须容忍 JSON 空白（`"code": 11135`），
// 字面量 marker 只能覆盖紧凑形态，故统一走 codeMarker（见 Classify 的 400 分支，
// 与 hint.go 的 isInvalidImageData 同口径；上游 5d5223d 的 Copilot review 修复）。
var invalidImageMarkers = []string{
	"invalid image_url content",
	"invalid_image_data",
	"replace the image",
}

// 定位：上下文超限是**请求的问题不是账号的问题**——同一个 body 换任何账号发都会
// 超限，与 WAF fail-fast 同哲学（确定与账号无关的错误不罚号不轮转，白白浪费健康号
// 的请求配额）。判定双通道：
//   - 业务码 11115（官方 ContextTooLong；JSON 空格容差，字符串形态也命中）；
//   - 文案：照官方客户端 isInputLengthError 的词表与正则（小写比较），覆盖各家模型
//     供应商的超限说法。
//
// 只在 400/404/413 请求级状态码上判（429+11115 概率极低且属限流语义优先，
// 5xx 属服务端故障优先）。误判代价（好 body 被归 prompt_too_long）：不罚号 +
// 不轮转 + 透传原文，客户端看到上游原文可自行排查，代价可控。
var promptTooLongMarkers = []string{
	"input length too long",
	"input is too long for requested model",
	"exceeds the maximum length",
	"input tokens exceed",
	"prompt is too long",
	"input is too long",
	"exceeds the maximum number of tokens allowed",
	"too many total text bytes",
	"requested token count exceeds the model's maximum context length",
	"'max_tokens' or 'max_completion_tokens' is too large",
	"user prompt is too large",
	"range of input length should be",
	"request too large",
	"please reduce the length of the input prompt",
	"context window exceeds limit",
	"exceeds model limit",
	"request body too large",
	"your request exceeded model token limit",
	"please reduce the length of the messages",
	"model_context_window_exceeded",
	"this endpoint's maximum context length",
	"token limit exceeded",
	"the request input size exceeds the model's maximum input token limit",
	"request entity too large",
	"prompt exceeds max length",
	"the request exceeds the model limits",
	"exceeds 30.000mb limit",
	"maximum context length",
}

// rePromptTooLong 官方词表之外的那条正则（"input length 12345 exceeds ..." 一类）。
var rePromptTooLong = regexp.MustCompile(`input\s*(?:length|tokens?)[^0-9]*\d+[^0-9]+exceeds`)

// isPromptTooLong 小写 body 是否是上下文超限（见 promptTooLongMarkers 注释）。
func isPromptTooLong(lower string) bool {
	if codeMarker(lower, "11115") {
		return true
	}
	for _, m := range promptTooLongMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return rePromptTooLong.MatchString(lower)
}

// isPromptTooLongStatus 11115 只在请求级 4xx 上判（见 promptTooLongMarkers 注释）。
func isPromptTooLongStatus(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusNotFound ||
		status == http.StatusRequestEntityTooLarge
}

// alreadyCheckinMarkers "今天已签到"关键词（上游对重复签到返回 code!=0，
// 实测 code=10001/14001 "今天已签到"/"今日已签到"）。只对 *Error.Msg 做包含匹配，
// 网络层/解析层错误不在此识别（见 IsAlreadyCheckin）。
var alreadyCheckinMarkers = []string{"已签到", "already"}
var badParamsMarkerCode = `"code":11101`

// softRateResetLoc 上游 429 6004 文案中的重置时间固定按 UTC+8 解释（上游文案如此，
// 与容器时区无关）。
var softRateResetLoc = time.FixedZone("UTC+8", 8*60*60)

// SoftRateResetLoc 暴露重置时间的固定时区（供测试构造/断言同一时区口径）。
func SoftRateResetLoc() *time.Location { return softRateResetLoc }

// softRateResetPatternCN/EN 匹配重置文案（CN「将在 … 重置」/ global 域英文
// "reset at <固定格式时间>"），捕获中间的时间串。
const softRateResetPatternCN = `将在 (.+?) 重置`
const softRateResetPatternEN = `(?i)reset at (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})`

// 限流判定正则预编译为包级 var：IsModelRateLimit / ParseRateReset 在每次错误
// 分类、每个限流 body 上调用，函数体内 MustCompile 是纯浪费；错误风暴（429
// 轰炸）时尤甚。模式串均为纯常量。regexp 并发安全（匹配只读），无需额外锁。
var (
	reSoftRateResetCN = regexp.MustCompile(softRateResetPatternCN)
	reSoftRateResetEN = regexp.MustCompile(softRateResetPatternEN)
)

// softRateTimeLayout 上游重置时间的格式（无时区后缀；时区固定 UTC+8）。
const softRateTimeLayout = "2006-01-02 15:04:05"

// modelBlockCode 明确指向「该后端无此模型」的业务 code。
const modelBlockCode = "11102"

// modelForbiddenCode 明确指向「该账号类型无权使用该模型」的业务 code。
// 同一模型在不同账号类型上权限不同，因此与 11102 同属 (账号, 模型) 级
// 负缓存：换有权限的账号仍可能成功，全部账号都拒绝时再向客户端报模型不可用。
const modelForbiddenCode = "11158"

// modelBlockMsgMarker 11102 答复的确定性文案（官方 error message 固定短语）。
// 只收这个窄短语，不收 "model ... not found" 宽正则——后者会误伤其他业务的
// not found 措辞。
const modelBlockMsgMarker = "service info not found"

const modelForbiddenMsgMarker = "is not allowed for user type"

// ModelBlockReason 11102 负缓存条目在 pool.modelCooldowns 里的 reason 前缀。
// handler 写 BlockModelBackoff；pool.BlockModelClear 按 "11102" 前缀识别条目
// （与 6004 条目的 "6004 model rate limit" reason 互不干扰）。
const ModelBlockReason = "11102 model not available"

// ModelForbiddenReason 11158 负缓存条目的 reason 前缀。
// pool.BlockModelClear 同时识别 11102/11158 两种确定性的模型级拒绝。
const ModelForbiddenReason = "11158 model not allowed for account type"

// IsModelBlocked 报告 body 是否是「该后端无此模型」(11102) 或「该账号类型无权
// 使用该模型」(11158) 的确定性答复。
//
// 只比对 code/msg 等独立字段，绝不做整段文本子串匹配：错误体还带 requestId 等字段，
// 拿整段文本匹配会把 code 撞在 ID 上、误避让一个本来能用的模型。判定 =
// code 字段精确等于 "11102"/"11158"，或 msg/message 字段命中对应窄短语。
// 只看 400/404：429 带这些 code 仍属限流语义。
// 字段遍历覆盖顶层与 error 子对象两层。
func IsModelBlocked(status int, body string) bool {
	if (status != http.StatusBadRequest && status != http.StatusNotFound) || body == "" {
		return false
	}
	lower := strings.ToLower(body)
	// 轻量预检：body 无任何已知 code/marker 时直接短路（大多数 4xx 零分配返回）。
	if !strings.Contains(body, modelBlockCode) && !strings.Contains(body, modelForbiddenCode) &&
		!strings.Contains(lower, modelBlockMsgMarker) && !strings.Contains(lower, modelForbiddenMsgMarker) {
		return false
	}
	code, msg := modelBlockFields(body)
	if code == modelBlockCode || code == modelForbiddenCode {
		return true
	}
	lowerMsg := strings.ToLower(msg)
	return strings.Contains(lowerMsg, modelBlockMsgMarker) || strings.Contains(lowerMsg, modelForbiddenMsgMarker)
}

// ModelBlockReasonFor 返回确定性模型拒绝对应的负缓存 reason。
// 11158 是账号类型权限问题；其余已由 IsModelBlocked 确认的形态沿用 11102。
func ModelBlockReasonFor(body string) string {
	code, msg := modelBlockFields(body)
	if code == modelForbiddenCode || strings.Contains(strings.ToLower(msg), modelForbiddenMsgMarker) {
		return ModelForbiddenReason
	}
	return ModelBlockReason
}

// modelBlockFields 提取顶层或 error 子对象中的业务 code 与消息。
func modelBlockFields(body string) (code, msg string) {
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return "", ""
	}
	nodes := []map[string]any{root}
	if inner, ok := root["error"].(map[string]any); ok {
		nodes = append(nodes, inner)
	}
	for _, node := range nodes {
		for _, key := range []string{"code", "errCode", "error_code"} {
			if v, ok := node[key]; ok && v != nil && code == "" {
				code = strings.TrimSpace(fmt.Sprint(v))
			}
		}
		for _, key := range []string{"msg", "message"} {
			if v, ok := node[key].(string); ok && v != "" && msg == "" {
				msg = strings.TrimSpace(v)
			}
		}
	}
	return code, msg
}

// hasBusinessEnvelope 报告错误 body 是否携带上游业务信封形态（JSON 且含
// `"code":` 或 `"msg":` 字段）。WAF 403 判定（IsWafBlocked）用「无业务信封」
// 区分 APISIX WAF 拦截页（HTML/空体/纯文本）与上游业务层 403（带 code/msg
// 信封，正常走既有分类）。JSON 解析不做：信封存在性只需字段名命中——
// 畸形 JSON 但含 `"msg":` 字样仍按业务响应保守处理（宁漏判 WAF 也不误罚
// 业务 403，后者有各自的权威分类）。
func hasBusinessEnvelope(body string) bool {
	return strings.Contains(body, `"code":`) || strings.Contains(body, `"msg":`)
}

// IsWafBlocked 报告 403 响应是否为 WAF 拦截形态：HTTP 403 且 body 无业务信封
// （无 `"code":`/`"msg":` JSON 字段——HTML 拦截页、空体、纯文本均命中）。
// 带业务信封的 403（11140 request illegal / 11128 等）仍走既有分类链。
// 403 含 accountFault 文案的维持现状（ErrAccountFault），由 Classify 的规则序保证。
func IsWafBlocked(status int, body string) bool {
	return status == http.StatusForbidden && !hasBusinessEnvelope(body)
}

// retryAfterHeaderCandidates 冷却时长优先解析的响应头候选序列：
// retry-after（秒，RFC 7231）/ retry-after-ms（毫秒）/ x-ratelimit-reset
// （epoch 秒或毫秒，取 now+ 剩余量）。大小写不敏感（http.Header.Get 已归一）。
var retryAfterHeaderCandidates = []string{"Retry-After", "Retry-After-Ms", "X-Ratelimit-Reset"}

// retryAfterSanity 解析结果的上限（超过视为上游异常值丢弃，回落本地计算），
// 与 pool 的 softRateMax 默认 2h 同量级。
const retryAfterSanity = 2 * time.Hour

// ParseRetryAfter 从限流/拦截响应头解析上游明示的等待时长：
// 依次尝试 Retry-After（整数秒）→ retry-after-ms（整数毫秒）→
// x-ratelimit-reset（纯数字按 epoch 秒/毫秒推断；HTTP-Date 形态不支持——
// 上游族实践发的是数字）。任一头缺失/非法/非正/超上限则尝试下一头；
// 全部不可用返回 false（调用方回落既有计算值，绝不臆造等待时长）。
func ParseRetryAfter(h http.Header) (time.Duration, bool) {
	for _, name := range retryAfterHeaderCandidates {
		v := strings.TrimSpace(h.Get(name))
		if v == "" {
			continue
		}
		if !isAllDigits(v) {
			continue // 非纯数字（如 HTTP-Date）不解析，宁缺毋滥
		}
		n, ok := parseRetryNumber(v, name)
		if !ok {
			continue
		}
		if n <= 0 || n > retryAfterSanity {
			continue // 非正/异常大：丢弃（回落本地计算）
		}
		return n, true
	}
	return 0, false
}

// isAllDigits 报告 s 是否为纯数字（前置快筛，免 strconv 之后再判语义）。
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseRetryNumber 按头名口径把纯数字串折算成时长。x-ratelimit-reset 是
// epoch 时刻而非时长：秒口径（10 位）与毫秒口径（13 位）都按「now+ 该时刻
// 的剩余量」折算，已在过去则不可用。位数不足（8 位以下）无法判定 epoch
// 语义的丢弃（宁缺毋滥：短串多半是序号之类的误用头）。
func parseRetryNumber(v, headerName string) (time.Duration, bool) {
	// 上限 16 位防 int64 溢出（超过 epoch 毫秒的现实量级必非法）。
	if len(v) > 16 {
		return 0, false
	}
	var n int64
	for _, r := range v {
		n = n*10 + int64(r-'0')
	}
	switch headerName {
	case "Retry-After":
		// 先做上限校验再乘 time.Second：16 位数字乘 1e9 会溢出 int64 回绕成
		// 小正数，进而通过调用方的 retryAfterSanity 校验被当作合法等待时长。
		if n > int64(retryAfterSanity/time.Second) {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	case "Retry-After-Ms":
		if n > int64(retryAfterSanity/time.Millisecond) {
			return 0, false
		}
		return time.Duration(n) * time.Millisecond, true
	default: // X-Ratelimit-Reset：epoch → 剩余量
		sec := n
		if len(v) >= 12 { // 毫秒口径（13 位）；11 位边界按秒（误判代价是多算 1000 倍）
			sec = n / 1000
		}
		remain := time.Until(time.Unix(sec, 0))
		return remain, true
	}
}

// ParseRateReset 从任何限流响应 body 里统一解析「将在 … 重置」时间（上游 UTC+8 文案）。
// 成功返回解析出的**墙钟时刻**（按 UTC+8 解释），失败返回零值 + false。
//
// 是否走模型级豁免、时日对齐到 until 还是 modelCooldowns，由冷却决策侧（pool）按
// IsModelRateLimit 判定，本函数只负责「把上游明说的恢复时刻抽出来」。没有时间文案
// 的限流也照常由调用方退回有界退避（绝不臆造时间）。
func ParseRateReset(body string) (time.Time, bool) {
	// CN 文案优先；global 域 429 body 是英文形态（"will reset at YYYY-MM-DD HH:MM:SS
	// UTC+8"），此前只认中文 → global 限流解析不到恢复时刻，退回有界退避基数反复
	// 翻倍（修「global 域冷却指数翻倍」）。英文正则锚定固定格式时间，自然语言
	// （"reset at the end of the day"）不匹配。
	m := reSoftRateResetCN.FindStringSubmatch(body)
	if len(m) < 2 {
		m = reSoftRateResetEN.FindStringSubmatch(body)
	}
	if len(m) < 2 {
		return time.Time{}, false
	}
	ts := strings.TrimSpace(m[1])
	ts = strings.TrimSuffix(ts, " UTC+8") // 去掉后缀，固定按 softRateResetLoc 解释
	t, err := time.ParseInLocation(softRateTimeLayout, ts, softRateResetLoc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Classify 按 HTTP 状态码 + body 判定错误类别。
//
// 判定顺序自「严」到「宽」，每层的先后都有语义依据：
//  0. 11102（IsModelBlocked）——「该后端无此模型」确定性答复，语义最具体，最先判
//     （只认 400/404，429+11102 属限流语义走第 4 层）。
//  1. 402 —— 真正的计费余额耗尽状态码，最严、最不可自愈，最先判。
//  2. sessionDeadMarkers —— 需要人工重登的终态。若 401 body 同时含 "12153" 与
//     "rate limit"（如网关错误页混排），归 session_dead：短冷却救不活失效 session，
//     误判为限流会让该死号留在池中反复被选中；且此层 marker 是精确词（12153 等），
//     比限流层的大范围子串更具体，具体优先于宽泛。
//  3. accountFaultMarkers —— 账号级授权/配额故障（11140 request illegal auth 风控、
//     14017 trial not activated register 未完成）。11128 文案同为 request illegal，
//     但它是请求拦截、与账号无关，在本层之前先判为 ErrContentBlocked。必须先于 status==429 判定：
//     14017 常带 429 状态码，若落到 status==429 会误归 soft_rate（"限流"语义不符：
//     限流可指数退避等自愈，账号级故障等不来）。11140 的 model 级限流变体
//     （rate-limiting 文案）因 marker 不含该文案而天然落到 softRateMarkers 层，
//     不受影响。
//  4. 业务码（bizcode.go，照官方客户端）—— 额度用完（14001/14012/14013/14014/14018/
//     14019/14022/14023）归 ErrHardCredit（issue #175 起），授权过期/未激活（14015/14016/14017）归
//     ErrAccountFault，按模型限额（6000–6008）、模型繁忙（14003）、会话太多（10105）归
//     ErrSoftRate，容量排队满（6021）归 ErrUpstreamBusy，排进队了（6020）归 ErrQueueWaiting，
//     排队数到上限（6022）归 ErrQueueUserLimit。只认结构化业务码，不靠可能跨计费/限流两界的文案猜测。
//  5. status==429 —— 限流状态码兜底（先于 hardMarkers）：429 body 高频携带
//     "quota exceeded"/"额度不足" 等跨计费/限流两界的措辞，hardMarkers 先判会把
//     限流误归 ErrHardCredit 硬冷却到次日 04:00，白扔号约 12h。状态码是比关键词
//     更权威的信号；真正的余额耗尽由 402（第 1 层）或额度用完业务码（第 4 层）捕获，
//     非 429 状态码的 quota 措辞仍走下方 hardMarkers（第 7 层）。
//  6. 11115 与上下文超限文案（400/404/413）—— 请求级语义：判在关键词层、404/5xx 与通用
//     4xx 兜底之前（"too many total text bytes" 会被限流词 "too many" 抢走；404 上打
//     11115 若落 ErrNotFound 会误冷却账号——上下文超限与账号无关）。
//  7. hardMarkers —— 非 429 响应携带计费关键词（200 业务信封 / 403 信封等）。
//  8. softRateMarkers —— 非 429 状态码携带限流文案（issue #28 修复点）。
//     位于此处可覆盖 200/400/403/5xx 各状态码。
//  9. 404 / 5xx —— 与限流无关的常规分类。
//  10. IsWafBlocked —— 403 且无业务信封（HTML 拦截页/空体/纯文本）：APISIX WAF
//     拦截形态。判在通用 4xx 兜底**之前**：此前该形态落 ErrClient → 只换号不罚 →
//     连环 403。带业务信封的 403 已被上方各层捕获，走不到本层。
//  12. 内容策略/参数错误/其他 4xx —— 通用兜底。
func Classify(status int, body string) ErrKind {
	// 11102「该后端无此模型」须最先判：它是「模型在后端不存在」的确定性答复，语义比
	// 计费/限流都更具体——若不先判，msg 里的 "service info not found" 会被更宽的
	// 4xx 兜底归为 ErrClient（只换号不避让），该坏号会留在池内反复被选中。
	// 只认 400/404（见 IsModelBlocked），429+11102 落下方 status==429 层走限流语义。
	if IsModelBlocked(status, body) {
		return ErrModelBlocked
	}
	// 402：真正的计费余额耗尽状态码，最严、最不可自愈，最先判。
	if status == http.StatusPaymentRequired {
		return ErrHardCredit
	}
	lower := strings.ToLower(body)
	// sessionDead / accountFault 先于 status==429：账号级终态等不来自愈，限流状态码
	// 不得掩盖它们（429+14017 必须 accountFault，401+12153 混排 "rate limit" 必须
	// sessionDead——此层 marker 是精确词，比限流层的大范围子串更具体，具体优先于宽泛）。
	for _, m := range sessionDeadMarkers {
		if strings.Contains(body, m) {
			return ErrSessionDead
		}
	}
	// 11128 是上游的请求拦截（内容指纹、角色白名单、缺会话头等），文案也是 "request illegal"，
	// 但和账号状态无关，必须先于 accountFaultMarkers 判：以前落到账号故障，被当成 11140
	// 封号直接禁用（10-19 生产：国际版缺 X-Conversation-ID 被拦，三个好号全被禁用）。
	if codeMarker(lower, "11128") {
		return ErrContentBlocked
	}
	for _, m := range accountFaultMarkers {
		if strings.Contains(lower, strings.ToLower(m)) || strings.Contains(body, m) {
			return ErrAccountFault
		}
	}
	// 业务码先于状态码和关键词判（码表与口径见 bizcode.go，照官方客户端）：
	//   - 额度用完（14001/14012/14013/14014/14018/14019/14022/14023，官方不重试）→ 余额不足。以前只认
	//     429+14018（issue #175），其余落进通用 429 当软限流，冷却完又被选中。
	//   - 授权过期、未激活（14015/14016/14017）→ 账号故障。
	//   - 按模型限额（6000–6008）、模型繁忙（14003）、会话太多（10105）→ 限流，不论状态码；
	//     冷却只落在这个模型还是整个号，由 handler.applyErrorPolicy 按业务码细分。
	//   - 容量排队满（6021）→ 上游忙：整条渠道满了，换哪个号都一样，不罚号不换号。以前落进
	//     通用 429 逐号软冷却，上游 linguo2625469 2026-10-15 实测 2 秒内整池健康号全被冷却。
	//   - 排进队了（6020）、排队数到上限（6022）→ 各自一类，不落进通用 429 的软冷却。
	if k := codeKind(BizCode(body)); k != ErrNone {
		return k
	}
	// status==429 先于 hardMarkers：限流响应 body 高频携带 "quota exceeded"/
	// "额度不足" 等跨计费/限流两界的措辞，hardMarkers 先判会把限流误归
	// ErrHardCredit 硬冷却到次日 04:00，白扔号约 12h。状态码是比关键词更权威的
	// 信号：上游既然给了 429，就按限流语义处理（宁可短冷却自愈，不可长冷却弃号）；
	// 真正的余额耗尽由 402（上层）或额度用完业务码（上层）捕获，非 429 状态码的 quota
	// 措辞仍走下方 hardMarkers（历史语义不变）。
	if status == http.StatusTooManyRequests {
		return ErrSoftRate
	}
	// 11115 与上下文超限文案：判在余额/限流关键词、404/5xx/WAF/内容策略/参数错误/通用
	// 4xx 之前——请求级语义最具体，官方词表里的 "too many total text bytes" 之类会被
	// 限流词 "too many" 抢走；404 兜底会误归 ErrNotFound 只冷却不透传；ErrClient 只换号，
	// 浪费健康号配额。
	if isPromptTooLongStatus(status) && isPromptTooLong(lower) {
		return ErrPromptTooLong
	}
	for _, m := range hardMarkers {
		if strings.Contains(lower, strings.ToLower(m)) || strings.Contains(body, m) {
			return ErrHardCredit
		}
	}
	for _, m := range softRateMarkers {
		if strings.Contains(lower, strings.ToLower(m)) || strings.Contains(body, m) {
			return ErrSoftRate
		}
	}
	if status == http.StatusNotFound {
		return ErrNotFound
	}
	if status >= 500 {
		return ErrServer
	}
	// WAF 403（无业务信封的拦截形态）：判在内容策略/参数错误/通用 4xx 之前——
	// 这些层只认带文案的 body，WAF 空体/HTML 永远不会命中它们的 marker，
	// 但落 ErrClient 兜底的代价是「只换号不罚」（连环 403 根因），必须在兜底前分流。
	// 带信封的 403 在上方各层已有权威分类，不受影响。
	if IsWafBlocked(status, body) {
		return ErrWafBlock
	}
	// 图片格式/数据错误是确定性的请求级错误：同 body 换账号结果不变，直接
	// fail-fast，避免把健康账号轮转一遍后仍把最终 503 返回给客户端。
	// 11135 业务码走 codeMarker（JSON 空白容差），文案走 invalidImageMarkers。
	if status == http.StatusBadRequest && codeMarker(lower, "11135") {
		return ErrImageInvalid
	}
	if status == http.StatusBadRequest {
		for _, m := range invalidImageMarkers {
			if strings.Contains(lower, m) {
				return ErrImageInvalid
			}
		}
	}
	// 内容策略拦截（HTTP 400 + 审核文案）：判在通用 ErrClient 之前。
	// 这是误报信号，不罚账号，由网关降级重试处理（见 handler.applyErrorPolicy）。
	if status >= 400 {
		for _, m := range contentBlockedMarkers {
			if strings.Contains(lower, m) {
				return ErrContentBlocked
			}
		}
		// 请求体解析失败（HTTP 400 + Unmarshal chat params failed / code 11101）：
		// 这是"发给上游的 body 有问题"。网关侧截断已由 413 消灭（issue #41 commit A），
		// 剩余来源是客户端 JSON 本身畸形——换了账号照样 400，不该罚号（白白冷却好号）。
		// 归 ErrBadParams：不冷却/不熔断/不计错，且**不轮转**——11101 发生在上游解析
		// 请求体阶段，还没走到模型路由，所以"不同账号可能有不同模型权限"其实是
		// 11102（ErrModelBlocked）的理由，那里已有 (账号,模型) 负缓存避让。
		if strings.Contains(body, badParamsMarkerMsg) || strings.Contains(body, badParamsMarkerCode) {
			return ErrBadParams
		}
		return ErrClient
	}
	// HTTP 200 但业务 code 非 0 且含余额关键词的情况已被上面 hardMarkers 捕获。
	return ErrNone
}

// apiEnvelope 上游统一信封。
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// Client 上游 HTTP 客户端。Base 字段可覆盖便于测试。
type Client struct {
	HTTP *http.Client

	// ChatHTTP 聊天 SSE 专用 client：无总时长上限（Timeout=0），首字节由
	// Transport.ResponseHeaderTimeout 约束，流中空闲由 IdleTimeout 约束。
	// 与 HTTP 共享同一个 *http.Transport 实例，连接池不重复。
	ChatHTTP *http.Client

	// HeaderTimeout 聊天 SSE 首字节前（响应头）超时；<=0 表示未设置（回落 HTTP.Timeout）。
	HeaderTimeout time.Duration
	// IdleTimeout 聊天 SSE 流中空闲超时；<=0 表示禁用空闲监控。
	IdleTimeout time.Duration

	// effortsMu/efforts 缓存各模型 supportedEfforts（FetchModels 刷新），供请求体 effort 降级。
	// 按 realm 分层桶（cn/global）：同模型名跨域探测的 effort 集合可能不同，
	// 混桶会互相污染（C-2）。
	effortsMu sync.RWMutex
	efforts   map[string]map[string][]string
	// defaultEfforts 缓存各模型 reasoning.defaultEffort（FetchModels 刷新），供
	// thinking.go 补档：缺显式 effort 时优先用模型声明默认档，空串回退硬编码 high。
	// 与 efforts 同 realm 分层桶（同 C-2 隔离原则），共用 effortsMu。
	defaultEfforts map[string]map[string]string
	// modelRates 缓存各模型当前生效积分倍率（规范化数值，如 "0.5"）。
	// 与 efforts 共用 realm 分层和锁；每次成功刷新模型目录时整体替换对应域。
	modelRates map[string]map[string]string
	// realmModels 各域模型目录里出现过的模型名（跨域选号判断「这个域有没有这个模型」）。
	// 与 modelRates 同口径整体替换，但不要求有可解析倍率；某域从未成功拉到目录时没有该键。
	realmModels map[string]map[string]bool
	// accountCatalogs 企业号各自的模型目录（按 uid），见 account_catalog.go。独立的锁，
	// 不与 effortsMu 嵌套持有。
	accountMu       sync.RWMutex
	accountCatalogs map[string]accountCatalog

	// globalModels 缓存 global 模型名目录探测结果（成功 ∩ 静态 overlay；
	// 1h TTL + 5min 负缓存），见 global_models.go。按实例持有，测试新建 Client 即隔离。
	globalModels fetchGlobalModelsCache

	// static 官方静态站点内容的缓存（灵感案例、首页模板），见 playbook.go。
	static staticCache

	// SanitizeFingerprints 出站请求体黑名单指纹脱敏开关（默认 true；false 完全还原）。
	// 面板保存配置热改 + chat 热路径并发读写，用 atomic.Bool 消除数据竞争。
	SanitizeFingerprints atomic.Bool

	// ModelOptimization 允许上游用对话做模型优化（config upstream.model_optimization，默认 false）：
	// false 时 chat 一律带 X-Private-Data: true；true 时照官方默认，个人号 false、企业号 true。
	// 面板保存配置热改，与 SanitizeFingerprints 同样用 atomic.Bool。
	ModelOptimization atomic.Bool

	// OnEarning 领到积分时调用（签到、领奖、补偿、旅行、新手礼包、连登兑换，见 earning.go）；nil = 不记。
	// 在发出第一个请求之前设好，之后不再改。
	OnEarning func(Earning)

	// UserAgent 出站 User-Agent 显式覆盖（非空时除任务中心桌面端上报外都用它）。
	// 空 = 按账号域取官方桌面端的 UA：对话 / 模型目录用 userAgent（内置 CLI），登录 / 刷新令牌 /
	// 上报用 daemonUserAgent（后台服务），积分 / 签到用 axios 的默认 UA（后台服务的 AuthService），
	// 成长中心这类官网网页的请求用浏览器的 UA，见 headers.go、web.go。
	UserAgent string

	// ClientVersion 桌面端版本（UA 版本段 + X-IDE-Version），两域共用。
	// 空 = 内置默认：国内版 5.9.3、国际版 5.9.4。
	ClientVersion string

	// CliVersion 出站 UA 中 `CLI/<ver>` 段版本。空 = 内置默认 2.165.0（两域相同）。
	CliVersion string

	// ClientName 对话请求的归属头 X-IDE-Name / X-IDE-Type 取值。空 = "WorkBuddy"（同官方桌面端）；
	// "SaaS" = 旧行为，对话不带 X-Agent-Purpose 与 X-IDE-*，后台服务请求不设 UA。X-Product 固定 "SaaS"。
	ClientName string

	// PassthroughIP 是否透传客户端 IP 给上游（X-Forwarded-For/X-Real-IP 首段）。
	// 缺省 false（反代安全边界）；handler 在 chat 路径按请求把 clientIP 传入 ChatStream。
	PassthroughIP bool

	// DeviceToken 设备风控 Token（X-Device-Token 头）全局兜底来源：config upstream.device_token。
	// 解析优先级：auth.Auth.DeviceToken > DeviceToken（config）> DeviceTokenFile（文件）。
	DeviceToken string

	// DeviceTokenFile 设备 token 文件路径兜底（宿主落盘的桌面端 token，5 分钟读取缓存）。
	DeviceTokenFile string

	// ChatBaseCN / BillingBaseCN 国内版上游 base，默认都是官方产品入口 https://www.workbuddy.cn
	// （桌面端 product.json 的 endpoint；copilot.tencent.com 是官方的备用入口）。1.1.2 以前分别是
	// copilot.tencent.com 与 www.codebuddy.cn，两者与主入口的只读接口、对话、登录 10-20 生产实测一致。
	ChatBaseCN    string
	BillingBaseCN string
	// WebBaseCN 官网（workbuddy.cn）域：部分「任务领奖」类接口只在此域提供
	// （Web 成长中心用；copilot.tencent.com 的同名路径返回 400）。
	WebBaseCN string
	// PlaybookBase 灵感案例的静态站点，空为官方地址（见 playbook.go；测试用）。
	PlaybookBase string
	// ScenesBase 首页模板列表所在的目录，空为官方地址（见 scenes.go；测试用）。
	ScenesBase string

	// ChatBaseGlobal / BillingBaseGlobal 国际版（global realm）上游 base。
	// 空 = 缺省默认 https://www.workbuddy.ai（D5）。
	ChatBaseGlobal    string
	BillingBaseGlobal string

	// GlobalEnabled 是否启用 global realm 路由（config global.enabled，缺省 true）。
	// false 时即便用户 auth 写了 realm=global 也**不**路由到 global base——
	// chatBase/billingBase 返回 CN base，路径也走 CN（双保险，与 auth.Realm() 的开关闸呼应）。
	GlobalEnabled bool

	// failover 国内版对话请求的入口熔断状态（前门 → 备用入口），按入口分别记，
	// 零值可用；见 failover.go 的 NetworkFailover。
	failover failoverState

	// webSessions 官网网页登录态（每个「域 + uid」一个 Cookie 罐），见 websession.go。
	// 零值可用，不需要构造函数；并发安全。
	webSessions webSessionStore

	// HTTPDNSQuery 地址层的查询函数：查 host 的 HTTPDNS 候选 IP（带 600 毫秒预算）。
	// 测试可替换；nil 时地址层整体不生效（候选为空，现有测试的零值 Client 行为不变）。
	HTTPDNSQuery func(ctx context.Context, host string) []string
	// PinIP 地址层的「钉 IP」函数：返回一个 URL/Host/SNI 仍是原域名、TCP 连到 ip 的
	// RoundTripper。测试可替换；nil 时地址层整体不生效。
	PinIP func(ip string) http.RoundTripper
	// addr 地址层的成功/失败记忆、在途换入口表与钉 IP 备忘，零值可用、并发安全。
	addr addressState
	// proxy 出站代理（proxy.go），New() 按环境变量装上，nil 直连。连接本身由 Transport 的拨号经代理建，
	// 这里只用来判断拉模型目录时跳不跳过备用 IP（viaEnvProxy），以及告诉代理池默认线路的代理（EnvProxyURL）。
	proxy *outboundProxy
	// routes 各条线路的 Transport（route.go），New() 装上；HTTP、ChatHTTP 的 Transport 和 PinIP 都经它
	// 按号选线路。零值 Client（测试）没有，全走 HTTP 自己的 Transport。
	routes *routeTable

	// refreshMu/refreshInFlight 同账号刷新单飞（按 UID 分桶）：同一时刻只放一个刷新在途，
	// 其余调用等它的结果，见 RefreshToken。
	refreshMu       sync.Mutex
	refreshInFlight map[string]*refreshFlight
}

// New 生产默认值。Transport 由 newTransport() 集中构造（连接层加固：真正禁 h2 /
// TLS 握手超时 / 短 keepalive 探测 / 失败清池，参数见 transport.go——吸收上游
// kongjianguan 4 连击实测经验）。
func New() *Client {
	px := envProxy()
	routes := newRouteTable(newTransport(px))
	rt := routedTransport{routes}
	c := &Client{
		HTTP:          &http.Client{Timeout: 120 * time.Second, Transport: rt},
		ChatHTTP:      &http.Client{Timeout: 0, Transport: rt}, // 无总时长；首字节由 ResponseHeaderTimeout 管
		ChatBaseCN:    cnProductEndpoint,
		BillingBaseCN: cnProductEndpoint,
		WebBaseCN:     cnProductEndpoint,
		// GlobalEnabled 缺省 true（与 config global.enabled 缺省 true 一致；纯 CN 部署行为不变：
		// CN 账号恒判 cn，global base 只在 realm=global 的账号上被使用）。
		GlobalEnabled: true,
	}
	// 地址层只在 New() 里装上真实实现：HTTPDNS 查询与按线路钉 IP（route.go）。零值 Client（测试都
	// 这样建）两个函数都是 nil，地址层不生效。
	dns := newHTTPDNSClient()
	c.HTTPDNSQuery = dns.query
	c.PinIP = routes.pin
	c.proxy = px
	c.routes = routes
	c.SanitizeFingerprints.Store(true)
	return c
}

// chatHTTP 返回聊天专用 client；未设置（如测试只注入 HTTP）时回落 HTTP。
func (c *Client) chatHTTP() *http.Client {
	if c.ChatHTTP != nil {
		return c.ChatHTTP
	}
	return c.HTTP
}

// defaultGlobalBase 缺省 global base（D5：config 未覆盖时默认 workbuddy.ai）。
const defaultGlobalBase = "https://www.workbuddy.ai"

// cnProductEndpoint 国内版官方产品入口（桌面端 5.8.3 product.json 的 endpoint），国内版所有请求的默认 base。
const cnProductEndpoint = "https://www.workbuddy.cn"

// globalChatBase 生效的 global chat base：Client.ChatBaseGlobal 非空取之，否则默认。
func (c *Client) globalChatBase() string {
	if c.ChatBaseGlobal != "" {
		return c.ChatBaseGlobal
	}
	return defaultGlobalBase
}

// globalBillingBase 生效的 global billing base：Client.BillingBaseGlobal 非空取之，否则默认。
func (c *Client) globalBillingBase() string {
	if c.BillingBaseGlobal != "" {
		return c.BillingBaseGlobal
	}
	return defaultGlobalBase
}

// globalOn 报告账号是否路由到 global 上游：GlobalEnabled 开且账号 Realm()==global。
// 双保险：config 开关是第一道闸（上游侧），auth.Realm() 的开关闸是第二道（账号侧）。
func (c *Client) globalOn(a *auth.Auth) bool {
	return c.GlobalEnabled && a != nil && a.Realm() == "global"
}

// chatCompletionsPath 是 CN 与 global 共用的 chat 出站路径（/v2 单路径）。
// global 自 #119 固定走 /v2：/console 挂腾讯云 WAF body 内容规则，反引号
// printf/whoami 等命令执行特征确定性 403；/v2 同 base 不挂该规则，实测等价端点。
// 已知取舍：若上游未来关闭 /v2，global chat 整体不可用——届时应重新启用 /console
// 路径，此注释即"坏了再说"的锚点。
const chatCompletionsPath = "/v2/chat/completions"

// 签到：官方桌面端后台服务 AuthService 的 getCheckinStatus / claimDailyCheckin（两个域都带 /v2）。
// 先查签到状态再签到；两条都带设备令牌（buildHeadersWithTuringToken）。
const (
	checkinStatusPath = "/v2/billing/meter/checkin-activity-status"
	dailyCheckinPath  = "/v2/billing/meter/daily-checkin"
)

func (c *Client) chatBase(a *auth.Auth) string {
	if c.globalOn(a) {
		return c.globalChatBase()
	}
	return c.ChatBaseCN
}

// prepareBody 组装出站请求体（脱敏开关由 Client.SanitizeFingerprints 控制）。
// realm 为账号 Realm()（cn/global），供 efforts 缓存分桶（跨域 effort 集合不互相污染）。
func (c *Client) prepareBody(body []byte, realm string) []byte {
	efforts, defs := c.effortsSnapshot(realm), c.defaultEffortsSnapshot(realm)
	if realmKey(realm) == "global" {
		// global 域降级源 = 远端探测桶（权威）∪ 产品静态兜底表（全局 21 名内档位如
		// deepseek-v4.1-flash ['high']）。当前探测桶为空时也按静态表降级，不全程透传
		//（issue #84：往 WorkBuddy 上游发 low/max 非法，须降级到 high）。
		efforts, defs = globalEffortMap(efforts, defs)
	}
	body = PrepareBodyOptWithEffortsAndDefault(body, c.SanitizeFingerprints.Load(), efforts, defs)
	// 不再自动补 prompt_cache_key（客户端自己带的照转）：官方客户端不发它；10-19 生产实测
	// deepseek、glm、kimi 两域带不带都不多命中，glm 会话 id 每轮换新时带它反而更少命中。
	// 前缀缓存靠稳定的 X-Conversation-ID，见 ChatHeaders。
	// reasoning_summary 对齐官方 CLI，见 InjectOfficialBodyFields。
	body = InjectOfficialBodyFields(body)
	return body
}

// effortsSnapshot 返回 effort 能力缓存副本；nil 表示未知（透传不降级）。
func (c *Client) effortsSnapshot(realm string) map[string][]string {
	c.effortsMu.RLock()
	defer c.effortsMu.RUnlock()
	bucket, ok := c.efforts[realmKey(realm)]
	if !ok || len(bucket) == 0 {
		return nil
	}
	cp := make(map[string][]string, len(bucket))
	for k, v := range bucket {
		cp[k] = v
	}
	return cp
}

// defaultEffortsSnapshot 返回指定 realm 的模型 defaultEffort 缓存副本；
// 该域无探测或无声明默认档 → nil（thinking.go 回退硬编码 high）。
func (c *Client) defaultEffortsSnapshot(realm string) map[string]string {
	c.effortsMu.RLock()
	defer c.effortsMu.RUnlock()
	bucket, ok := c.defaultEfforts[realmKey(realm)]
	if !ok || len(bucket) == 0 {
		return nil
	}
	cp := make(map[string]string, len(bucket))
	for k, v := range bucket {
		cp[k] = v
	}
	return cp
}

// realmKey 归一化 efforts 缓存键：cn/global。空 realm 视为 cn（老调用/无前缀模型名）。
func realmKey(realm string) string {
	if realm == "" {
		return "cn"
	}
	return realm
}

func (c *Client) billingBase(a *auth.Auth) string {
	if c.globalOn(a) {
		return c.globalBillingBase()
	}
	return c.BillingBaseCN
}

// webBase 返回官网域（任务领奖类接口；未注入时回落默认）。
// realm 感知：global 账号切国际站 workbuddy.ai，CN 用 workbuddy.cn。
func (c *Client) webBase(a *auth.Auth) string {
	if c.globalOn(a) {
		return defaultGlobalBase
	}
	if c.WebBaseCN != "" {
		return c.WebBaseCN
	}
	return cnProductEndpoint
}

// jsonAttempt 一次 JSON 请求的结果。status 为 0 表示传输错误（没拿到 HTTP 状态）；
// parseFailed 表示 HTTP 2xx 但响应体不是合法信封（刷新令牌按「200 但响应体不合格」
// 换 IP 时要区分）。
type jsonAttempt struct {
	data        json.RawMessage
	raw         []byte
	status      int
	err         error
	parseFailed bool
}

// doJSON 发请求并解信封；HTTP 非 2xx 或业务 code != 0 时返回带 body 片段的 *Error。
// body 读失败（连接中断/空闲掐流/截断）返回普通错误（非 *Error）——半截 body 不进
// Classify，不参与账号惩罚（传输层故障不该喂熔断误罚号）。
func (c *Client) doJSON(req *http.Request) (json.RawMessage, error) {
	res := c.doJSONAttempt(req, nil)
	return res.data, res.err
}

// doJSONAttempt 同 doJSON，但返回完整结果；rt 非 nil 时用地址层的钉 IP Transport。
func (c *Client) doJSONAttempt(req *http.Request, rt http.RoundTripper) jsonAttempt {
	hc := c.HTTP
	if rt != nil {
		cp := *hc
		cp.Transport = rt
		hc = &cp
	}
	resp, err := doDecoded(hc, req)
	if err != nil {
		return jsonAttempt{err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return jsonAttempt{status: resp.StatusCode, err: fmt.Errorf("read body: %w", err)}
	}
	if resp.StatusCode >= 400 {
		kind := Classify(resp.StatusCode, string(raw))
		return jsonAttempt{status: resp.StatusCode, raw: raw,
			err: &Error{Kind: kind, Status: resp.StatusCode, Msg: truncate(string(raw), 200)}}
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return jsonAttempt{status: resp.StatusCode, raw: raw, parseFailed: true,
			err: fmt.Errorf("parse failed: %w (body: %s)", err, truncate(string(raw), 120))}
	}
	if env.Code != 0 {
		kind := Classify(resp.StatusCode, env.Msg)
		if kind == ErrNone {
			kind = ErrClient
		}
		return jsonAttempt{status: resp.StatusCode, raw: raw,
			err: &Error{Kind: kind, Status: resp.StatusCode, Msg: fmt.Sprintf("code=%d msg=%s", env.Code, truncate(env.Msg, 160))}}
	}
	return jsonAttempt{data: env.Data, raw: raw, status: resp.StatusCode}
}

// RefreshToken 刷新 access token；成功时更新 a 的字段（缺省值保留旧值），
// 调用方负责 SaveAtomic。全程持 a 锁，防止并发 SaveAtomic 读半更新 token。
// refreshIOTimeout 刷新端点网络 I/O 上限（两段式锁外执行，防上游 hang 长占锁）。
const refreshIOTimeout = 30 * time.Second

// refreshTimeoutCN 国内版刷新令牌的超时（官方桌面端 8 秒；只在原超时更长时下调）；测试可调。
var refreshTimeoutCN = 8 * time.Second

// refreshAccountTimeout 刷新后取账号的超时（官方桌面端 4 秒）；测试可调。
var refreshAccountTimeout = 4 * time.Second

// refreshTokenExpiresInMax refresh 响应 expiresIn 的量级上限（10 年，纯防御值：
// 早先实测 R-D 响应恒 5184000=60d，2026-10-20 生产实测是 2592000=30d）。超限视为上游脏数据，不写 ExpiresAt（保留旧值），
// 防止 NeedsRefresh 永假导致 token 永不刷新反而真过期失效。
const refreshTokenExpiresInMax = 10 * 365 * 24 * time.Hour

// RefreshToken 刷新 access token；成功时更新 a 的字段（缺省值保留旧值），
// 调用方负责 SaveAtomic。
//
// 并发安全模型（单飞 + 两段式）：
//   - 单飞：同一账号（UID）同一时刻只放一个刷新在途，其余调用等它结束、原样复用结果，
//     不再带着同一个刷新令牌重复打上游。官方 AuthenticationManager.scheduleRefreshSession
//     也是这样（refreshInFlight 在途时直接复用，日志 `joined: reason=in-flight`）。上游
//     linguo2625469 #215 报告刷新令牌用过一次再用会回 12153、整条令牌链作废，账号只能
//     重新登录；对话前临近过期的刷新、保活、签到工具会同时刷同一个号（本仓库早先实测
//     R-E 并发两次都成功，没复现）。
//   - 首个调用内部仍是两段式：锁内读快照 → 锁外 I/O → 锁内校验快照一致后写回；写回前
//     发现另一刷新已完成（两个令牌都变了）就直接采用。
//   - 12153 兜底：凭证文件已被别的进程（网关和签到工具同时跑）刷新过时，采用文件里的
//     令牌重试一次——进程内单飞管不到跨进程。
//
// 请求形状照官方 refreshSession（两个安装包，10-20 读包确认）：POST
// /v2/plugin/auth/token/refresh，请求体 {}，头见 RefreshHeaders，另带这次刷新的根 span
// 链路头（trace.go）。拿到新令牌后官方用新令牌取一次账号——国内版 fetchCurrentAccount
// （GET /v2/plugin/account），国际版 getAccountSnapshot（GET /v2/plugin/accounts）——把对得上
// 的账号合并进本地（mergeCurrentAccountDetails，见 refreshAccountCheck）。取不到账号、合并后
// 部门全名变了（normalizeAndAssertRefreshedAuthority），或者新访问令牌和旧的一样，这次刷新都
// 算失败，不保存新令牌。网关照做：不写回，返回普通错误（不是 12153 会话失效，调用方只记一次
// 失败，下轮再刷）。10-20 生产实测刷新后旧访问令牌仍然可用，所以丢掉这次的新令牌不会让账号
// 失效。官方还核对账号类型、登录域等，网关不存类型、登录域照旧换成新值（见
// docs/official-client.md 的刷新令牌一节）。
func (c *Client) RefreshToken(a *auth.Auth) error {
	key := a.UID
	if key == "" {
		// 无 UID 的临时 auth：退化为按对象指针去重，仍保证同一对象串行。
		key = fmt.Sprintf("%p", a)
	}
	c.refreshMu.Lock()
	if c.refreshInFlight == nil {
		c.refreshInFlight = make(map[string]*refreshFlight)
	}
	if f, ok := c.refreshInFlight[key]; ok {
		c.refreshMu.Unlock()
		<-f.done
		return f.err
	}
	f := &refreshFlight{done: make(chan struct{})}
	c.refreshInFlight[key] = f
	c.refreshMu.Unlock()

	err := c.refreshTokenOnce(a)
	if err != nil && isSessionDead(err) && adoptNewerTokensFromDisk(a) {
		// 跨进程竞态：磁盘 RT 已被别的进程 rotate 过，用新 RT 再试一次。
		err = c.refreshTokenOnce(a)
	}

	// 先写结果再关 channel：等待者 <-f.done 之后读到的一定是最终 err。
	f.err = err
	close(f.done)
	c.refreshMu.Lock()
	delete(c.refreshInFlight, key)
	c.refreshMu.Unlock()
	return err
}

// refreshFlight 是同一账号一次 refresh 的单飞票据：done 关闭即结果就绪。
type refreshFlight struct {
	done chan struct{}
	err  error
}

// isSessionDead 判定错误是否为上游 12153（会话 / RT 失效）。
func isSessionDead(err error) bool {
	var ue *Error
	return errors.As(err, &ue) && ue.Kind == ErrSessionDead
}

// adoptNewerTokensFromDisk 在 refresh 失败后读回 auth 文件：若磁盘 RT 与内存不同
// （另一进程已刷新并写回），采纳磁盘 token 并返回 true，供调用方重试一次。
// 读文件失败、解析失败、RT 一致均返回 false（不改内存状态），保持失败语义。
func adoptNewerTokensFromDisk(a *auth.Auth) bool {
	if a.FilePath == "" {
		return false
	}
	raw, err := os.ReadFile(a.FilePath)
	if err != nil {
		return false
	}
	disk, err := auth.Parse(raw)
	if err != nil || strings.TrimSpace(disk.RefreshToken) == "" {
		return false
	}
	a.Lock()
	defer a.Unlock()
	if disk.RefreshToken == a.RefreshToken {
		return false
	}
	a.AccessToken = disk.AccessToken
	a.RefreshToken = disk.RefreshToken
	if disk.ExpiresAt > 0 {
		a.ExpiresAt = disk.ExpiresAt
	}
	return true
}

// refreshTokenOnce 是单次刷新本体（调用方 RefreshToken 已保证同账号串行）。
func (c *Client) refreshTokenOnce(a *auth.Auth) error {
	// 第 1 段（锁内）：读快照。
	a.Lock()
	rtSnapshot := a.RefreshToken
	atBefore := a.AccessToken
	a.Unlock()
	if strings.TrimSpace(rtSnapshot) == "" {
		return fmt.Errorf("no refreshToken")
	}

	endpoint := c.chatBase(a) + "/v2/plugin/auth/token/refresh"
	// 超时：国内版照官方每次尝试各自限时（刷新 8 秒、刷新后取账号 4 秒，换 IP 重发时重新
	// 计时，见 refreshTokenCall 与 refreshAccountCheck）；国际版官方没有设，整个刷新（含取
	// 账号）仍以 refreshIOTimeout 兜底。
	cn := !c.globalOn(a)
	ctx := context.Background()
	if !cn {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, refreshIOTimeout)
		defer cancel()
	}
	req, err := newAccountRequest(ctx, a, http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	// RefreshHeaders 读取 a 的字段（domain/uid 等）注入请求头——需在锁内取快照值，
	// 用一个显式逐字段拷贝的临时 auth 构造头（不拷贝 sync.Mutex，避免 vet copies-lock）。
	a.Lock()
	hdrSnapshot := auth.Auth{
		AccessToken:        a.AccessToken,
		RefreshToken:       rtSnapshot,
		ExpiresAt:          a.ExpiresAt,
		Domain:             a.Domain,
		UID:                a.UID,
		EnterpriseID:       a.EnterpriseID,
		Nickname:           a.Nickname,
		DeviceToken:        a.DeviceToken,
		DepartmentFullName: a.DepartmentFullName,
		DepartmentSet:      a.DepartmentSet,
		AccountType:        a.AccountType,
		OneIDAccountID:     a.OneIDAccountID,
		IDP:                a.IDP,
		TokenType:          a.TokenType,
	}
	a.Unlock()
	c.RefreshHeaders(req, &hdrSnapshot)
	tr := newDaemonTrace()
	tr.setRoot(req)

	// 网络 I/O（锁外，国内版 8s / 国际版 30s 上限）。国内版失败时按地址层换 IP 重发。
	res := c.refreshTokenCall(req, cn)
	data := res.data
	if res.err != nil {
		return res.err
	}
	var tok struct {
		AccessToken  string  `json:"accessToken"`
		RefreshToken string  `json:"refreshToken"`
		ExpiresIn    int64   `json:"expiresIn"`
		Domain       *string `json:"domain"`
		TokenType    *string `json:"tokenType"`
	}
	jsonData := data
	// data 缺失（或为 null）时看顶层（官方 refresh 响应的兼容口径）。
	if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		jsonData = res.raw
	}
	if err := json.Unmarshal(jsonData, &tok); err != nil || tok.AccessToken == "" {
		return fmt.Errorf("refresh_failed: no accessToken in response — re-login required")
	}
	if tok.AccessToken == atBefore {
		return fmt.Errorf("refresh_failed: refresh did not produce a new access token")
	}
	// 用新令牌取账号（官方 refreshSession 的第二步），通过了才写回。
	checkAuth := auth.Auth{
		AccessToken:        tok.AccessToken,
		RefreshToken:       rtSnapshot,
		Domain:             hdrSnapshot.Domain,
		UID:                hdrSnapshot.UID,
		EnterpriseID:       hdrSnapshot.EnterpriseID,
		DepartmentFullName: hdrSnapshot.DepartmentFullName,
	}
	if tok.Domain != nil && *tok.Domain != "" {
		checkAuth.Domain = *tok.Domain
	}
	accounts, err := c.refreshAccountCheck(ctx, c.chatBase(a), c.globalOn(a), &checkAuth, tr)
	if err != nil {
		return err
	}
	// 官方 mergeCurrentAccountDetails 合并账号，normalizeAndAssertRefreshedAuthority 核对身份。
	merged := mergeRefreshedAccount(&hdrSnapshot, matchRefreshedAccount(accounts, &hdrSnapshot))
	if !refreshIdentityKept(&hdrSnapshot, merged, tok.TokenType, tok.Domain) {
		return fmt.Errorf("refresh_failed: refresh changed authority context")
	}

	// 第 2 段（锁内）：校验快照一致后写回。
	a.Lock()
	defer a.Unlock()
	// 写回守卫是 AND 语义：锁外期间另一刷新已完成 → 两 token 必同时变化（实测 R-D：
	// refresh 响应 accessToken/refreshToken 总是一起 rotate，写回也同时写两个），AND
	// 即「并发刷新已完成」判据；AND 与 OR 在真实形态下等价。唯 OR 会额外放弃的
	// 「只有单 token 变化」（如手工只改 auth 文件一个字段）不构成放弃条件——本次
	// 结果覆盖手工编辑。
	if a.AccessToken != atBefore && a.RefreshToken != rtSnapshot {
		// 锁外期间另一 goroutine 已完成刷新：新 token 已生效，本次结果不必再写
		// （实测 R-E：服务端无 rotation 撤销，并发双刷新拿到的两个新 token 都有效，
		// 后写覆盖先写二者等价可用；提前返回避免无意义覆盖与 ExpiresAt 抖动）。
		return nil
	}
	a.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		a.RefreshToken = tok.RefreshToken
	}
	if tok.Domain != nil && *tok.Domain != "" {
		a.Domain = *tok.Domain
	}
	if tok.TokenType != nil {
		a.TokenType = tok.TokenType
	}
	merged.applyTo(a)
	// preserveExpiry：响应缺 expiresIn 时保留旧过期时间，避免刷新风暴。
	// 实测 R-D 响应恒带 expiresIn=5184000（60d）——缺省分支仅为防御，保留旧值
	// 避免过期判定漂移。同理，超过 10 年的 expiresIn 按脏值处理保留旧值：
	// 实测 JWT exp-iat 与 expiresIn 严格自洽（R-F），超量级值只会是上游脏数据，
	// 照写会把 ExpiresAt 推到荒谬未来 → NeedsRefresh 永假 → token 永不刷新
	// 反而真过期失效。
	if tok.ExpiresIn > 0 && time.Duration(tok.ExpiresIn)*time.Second < refreshTokenExpiresInMax {
		a.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix()
	}
	return nil
}

// refreshTokenCall 发刷新请求；国内版失败时按地址层换 IP 重发（规格 §5）。换 IP 只认
// HTTP 408/502/503、没有 HTTP 状态的传输错误（含 DNS 失败），以及 200 但 accessToken
// 不是非空字符串（data 缺失时看顶层）；请求头、请求体原样重发。不换入口。
func (c *Client) refreshTokenCall(req *http.Request, allowAddr bool) jsonAttempt {
	// 国内版每次尝试各自限时 refreshTimeoutCN（官方每次重发都是新请求，超时重新算）。
	attempt := func(r *http.Request, rt http.RoundTripper) jsonAttempt {
		if allowAddr {
			ctx, cancel := context.WithTimeout(r.Context(), refreshTimeoutCN)
			defer cancel() // doJSONAttempt 读完响应体才返回
			r = r.WithContext(ctx)
		}
		return c.doJSONAttempt(r, rt)
	}
	res := attempt(req, nil)
	if !allowAddr {
		return res
	}
	host := req.URL.Hostname()
	if !c.addrLayerOn() || !failoverTrustedHost(host) {
		return res
	}
	var slot *addrSlot
	pinned := ""
	for {
		if !refreshAttemptRetryable(res) {
			if pinned != "" && res.err == nil {
				c.noteAddrSuccess(host, pinned)
			}
			return res
		}
		next := c.nextAddrSlot(req.Context(), slot, host)
		if next == nil {
			return res
		}
		slot = next
		pinned = slot.current()
		retryReq, err := retryRequest(req)
		if err != nil {
			return res
		}
		res = attempt(retryReq, c.PinIP(pinned))
	}
}

// refreshAttemptRetryable 报告这次刷新失败是否换 IP。
func refreshAttemptRetryable(res jsonAttempt) bool {
	if res.err == nil {
		return refreshAccessToken(res.data, res.raw) == ""
	}
	if res.parseFailed {
		// 200 但不是合法信封：data 不是非空字符串，按「可重试的无效响应」处理。
		return true
	}
	var ue *Error
	if errors.As(res.err, &ue) {
		switch ue.Status {
		case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable:
			return true
		}
		return false
	}
	// 没有 HTTP 状态的传输错误（含 DNS 失败与超时）；调用方取消、号的线路问题（route.go）不算。
	return res.status == 0 && !errors.Is(res.err, context.Canceled) && !IsRouteError(res.err)
}

// refreshAccessToken 取响应里的 accessToken：data.accessToken；data 缺失（或为 null）
// 时看顶层。
func refreshAccessToken(data, raw []byte) string {
	src := data
	if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		src = raw
	}
	var tok struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(src, &tok); err != nil {
		return ""
	}
	return tok.AccessToken
}

// retryRequest 复制一个请求用于重发：请求头原样（含同一个 X-Request-ID），请求体
// 用 GetBody 重新造一份。
func retryRequest(req *http.Request) (*http.Request, error) {
	next := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		next.Body = body
	}
	return next, nil
}

// refreshAccountCheck 刷新令牌后的取账号：国内版 GET /v2/plugin/account（data 是账号），国际版
// GET /v2/plugin/accounts（data.accounts[]）。base、global 取自刷新的账号；a 是换上新令牌
// 的快照。头是请求框架的头加 X-Domain（同 RefreshHeaders：国际版固定 www.workbuddy.ai，国内版
// 令牌的 domain，没有就用产品入口域）和这次刷新的链路头。
//
// 取不到账号（请求失败、国内版 data 为空、国际版列表为空）返回错误；取到的账号交给
// matchRefreshedAccount 挑（见 refresh_identity.go）。
func (c *Client) refreshAccountCheck(ctx context.Context, base string, global bool, a *auth.Auth, tr daemonTrace) ([]refreshAccountJSON, error) {
	if !global {
		// 国内版刷新后取账号按官方 4 秒（只在原 ctx 期限更长时下调）。
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, refreshAccountTimeout)
		defer cancel()
	}
	path := "/v2/plugin/account"
	if global {
		path = "/v2/plugin/accounts"
	}
	req, err := newAccountRequest(ctx, a, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	c.frameworkHeaders(req, a, c.refreshUserAgent(a))
	d := a.Domain
	if global {
		d = "www.workbuddy.ai"
	} else if d == "" {
		d = "www.workbuddy.cn" // 国内版产品入口 https://www.workbuddy.cn 的 authority
	}
	req.Header.Set("X-Domain", d)
	tr.setRoot(req)
	data, err := c.doJSON(req)
	if err != nil {
		return nil, fmt.Errorf("refresh_failed: fetch account after refresh: %w", err)
	}
	var accounts []refreshAccountJSON
	if global {
		var list struct {
			Accounts []refreshAccountJSON `json:"accounts"`
		}
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("refresh_failed: parse account after refresh: %w", err)
		}
		if len(list.Accounts) == 0 {
			return nil, fmt.Errorf("refresh_failed: account list is empty after refresh")
		}
		accounts = list.Accounts
	} else {
		if t := bytes.TrimSpace(data); len(t) == 0 || bytes.Equal(t, []byte("null")) {
			return nil, fmt.Errorf("refresh_failed: no account after refresh")
		}
		var one refreshAccountJSON
		if err := json.Unmarshal(data, &one); err != nil {
			return nil, fmt.Errorf("refresh_failed: parse account after refresh: %w", err)
		}
		accounts = []refreshAccountJSON{one}
	}
	return accounts, nil
}

// ChatStream 发 chat 请求并返回原始 SSE body 流（调用方负责 Close）。
// 等价于 ChatStreamContext(context.Background(), ...)：不带调用方取消语义。
// 需要客户端断开联动的调用方用 ChatStreamContext 传入请求 ctx。
//
// global realm：先打 /console/chat/completions，404/405 时同一 base 二次换 /v2/chat/completions
// （上游新旧路径分叉，PLAN R9 fallback 顺序）。cn：/v2/chat/completions 现状不变。
func (c *Client) ChatStream(a *auth.Auth, body []byte, clientIP string, meta ChatMeta) (rc io.ReadCloser, status int, respBody []byte, err error) {
	return c.ChatStreamContext(context.Background(), a, body, clientIP, meta)
}

// ChatStreamContext 同 ChatStream，但从 ctx 派生请求 context：调用方（handler）传入
// r.Context() 后，客户端断连/请求取消会立即中断在途上游调用、释放连接与账号在途名额，
// 不再空转到 IdleTimeout。ctx 为 nil 时回落 Background。成功流的 cancel 仍由
// monitorBody 的 Close 接管（reqCtx 取消与显式 Close 任一触发即断）。
//
// 错误路径（≥400 且非 fallback 状态码）除 (status, respBody) 外还返回**已分类的**
// *Error（Kind 信封 + Retry-After 头解析）：客户端错误分类在此一次完成，handler
// 不再对 body 二次 Classify（消除「上游分类一次、网关再分类一次」的双路径漂移面），
// Retry-After 也随信封流动。respBody 仍原样返回（错误透传语义：message 透传上游
// 原文）。判定为 ErrNone 的响应（理论上不存在，防御）err 为 nil，handler 按
// respBody 自行兜底。
//
// 国内版对话请求按官方桌面端 5.8.3 内置 CLI 的失败切换规则（见 failover.go）：
// chatBase 是国内版主入口或备选入口时，失败后按 DNS / 状态码在「换 IP（HTTPDNS
// 候选与备用 IP）」与「换入口（备用入口）」之间切换，每个请求最多换一次入口；
// 熔断器打开时直接发备用入口。国际版（globalOn）与 chatBase 不是可信前门的部署
// （如用户配了自己的中转）不走这套，行为与改造前一致。
//
// global chat 自 #119 实测后固定走 /v2（chat 层无 fallback 链；billing 层的 404
// fallback 独立存在，语义不受影响）。ensureConsoleSystem 在 prepareBody 后统一套用
// 全局脚本：首条消息非 system 时前置兜底 system（防 console 域上游 code 11-128；
// #119 后 global 出站固定 /v2，该兜底保留——上游对 /v2 是否需要 system 无实测
// 反证，删了无回滚路径）。
func (c *Client) ChatStreamContext(ctx context.Context, a *auth.Auth, body []byte, clientIP string, meta ChatMeta) (rc io.ReadCloser, status int, respBody []byte, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	prepared := c.prepareBody(body, a.Realm())
	if c.globalOn(a) {
		prepared = ensureConsoleSystem(prepared)
	}
	base := c.chatBase(a)
	// 请求头只生成一次：ChatHeaders 里的 X-Request-ID、会话 id 每次调用都会新生成，
	// 切入口时请求头、请求体要原样复用（见 failover.go）。这里建的请求只用来出头。
	hreq, err := http.NewRequest(http.MethodPost, base+chatCompletionsPath, bytes.NewReader(prepared))
	if err != nil {
		return nil, 0, nil, err
	}
	c.ChatHeaders(hreq, a, clientIP, meta)
	header := hreq.Header

	// 选入口：前门熔断器关着发 chatBase，打开时可能直接发备用入口（见 failover.go）。
	plan := c.chatFailoverPlan(a, base)
	if !plan.active {
		first := c.chatStreamOnce(ctx, a, plan.firstBase, header, prepared, nil)
		return first.rc, first.status, first.body, first.err
	}
	// 国内版可信前门：在入口与地址之间按官方规则切换（见 failover.go）。
	return c.chatStreamWithFailover(ctx, a, plan, header, prepared)
}

// chatOnceResult 一次对话请求的结果（chatStreamOnce 的返回值）。cancel 供外层
// 丢弃响应时调用；成功流交给调用方后由流的 Close（monitorBody）接管，与改造前一致。
type chatOnceResult struct {
	rc     io.ReadCloser
	status int
	body   []byte
	err    error
	cancel context.CancelFunc
}

// chatStreamOnce 向 origin 发一次对话请求——改造前 ChatStreamContext 里的「发一次」
// 逻辑原样抽出，供入口切换与地址层复用。header 由外层 ChatHeaders 只生成一次后传入，
// 保证切入口时请求头原样复用、请求体每次重新造 bytes.NewReader，只换 scheme+host。
// rt 非 nil 时用地址层的「钉 IP」Transport 发这次请求（URL、Host、SNI 都不变）。
func (c *Client) chatStreamOnce(ctx context.Context, a *auth.Auth, origin string, header http.Header, prepared []byte, rt http.RoundTripper) chatOnceResult {
	// reqCtx 的 cancel 在每个出口显式调用（Do 失败 / ≥400 / 成功分支移交 monitorBody）。
	// 从调用方 ctx 派生：保留取消传播（父 ctx 取消 → 本 ctx 取消），
	// 同时 monitorBody.Close 仍能独立 cancel 本分支（空闲掐流）。
	reqCtx, cancel := context.WithCancel(ctx)
	req, err := newAccountRequest(reqCtx, a, http.MethodPost, origin+chatCompletionsPath, bytes.NewReader(prepared))
	if err != nil {
		cancel()
		return chatOnceResult{err: err}
	}
	req.Header = header.Clone()
	hc := c.chatHTTP()
	if rt != nil {
		// 钉 IP 的 Transport 独立（连接池里的连接连的是别的 IP）：复制 client 只换
		// Transport，超时等其余参数不变。
		cp := *hc
		cp.Transport = rt
		hc = &cp
	}
	resp, err := doDecoded(hc, req)
	if err != nil {
		cancel()
		log.Printf("ERR: [upstream] chat_stream acct=%s: transport error: %v", logfmt.Label(a.UID, a.Nickname), err)
		// 传输层失败 → 清空所用 Transport 的空闲连接（连接层加固）：失败连接可能仍
		// 留在空闲池里，下一个请求会继续捡到它——仅靠 IdleConnTimeout 等过期
		// 不够，主动清池才断根。
		roundTripCloseIdle(hc.Transport)
		return chatOnceResult{err: err}
	}
	if resp.StatusCode >= 400 {
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		cancel()
		// body 读失败（掐流/截断）→ 传输层错误：半截 raw 不交回调用方进 Classify，
		// 否则 handler 侧 applyErrorPolicy 会按误判分类罚号。
		if rerr != nil {
			log.Printf("ERR: [upstream] chat_stream acct=%s: read body: %v", logfmt.Label(a.UID, a.Nickname), rerr)
			return chatOnceResult{err: fmt.Errorf("read body: %w", rerr)}
		}
		kind := Classify(resp.StatusCode, string(raw))
		log.Printf("WARN: [upstream] chat_stream acct=%s: upstream %d %s body=%s",
			logfmt.Label(a.UID, a.Nickname), resp.StatusCode, kind, truncate(string(raw), 200))
		// ≥400 直接返回（#119 后 global 单路径 /v2，chat 层无 fallback 链）。
		// 分类一次、随 Kind 信封返回（含 Retry-After 头解析）：
		// ErrNone 是防御分支（≥400 不应产生 None），返回原文让 handler 兜底。
		if kind == ErrNone {
			return chatOnceResult{status: resp.StatusCode, body: raw}
		}
		ue := &Error{Kind: kind, Status: resp.StatusCode, Msg: truncate(string(raw), 200)}
		if d, ok := ParseRetryAfter(resp.Header); ok {
			ue.RetryAfter = d
		}
		return chatOnceResult{status: resp.StatusCode, body: raw, err: ue}
	}
	// 成功分支：cancel 所有权交给 monitorBody（其 Close 会 cancel）；
	// IdleTimeout<=0 时 monitorBody 原样返回底流、无人调 cancel——可接受：
	// 取消传播由 http.Transport 在 body Close / 父 ctx 取消时处理，连接正常清理。
	return chatOnceResult{rc: monitorBody(resp.Body, c.IdleTimeout, cancel), status: resp.StatusCode, cancel: cancel}
}

// ModelInfo 动态模型信息（含 maxInputTokens/maxOutputTokens + 上游模型对象全字段）。
// CN /console 与 global /v2 的模型对象同构，故共用此结构；上游省略的字段保持零值，
// /v1/models 侧按「空值省略」透出（不编造）。
type ModelInfo struct {
	ID            string
	Name          string
	ContextWindow int64    // = maxInputTokens
	MaxTokens     int64    // = maxOutputTokens（思考与最终回答共享此预算，上游无独立思考上限字段）
	Efforts       []string // reasoning.supportedEfforts（空=未知/固定档）
	DefaultEffort string   // reasoning.defaultEffort（新模型键）或 reasoning.effort（老模型键）；空=未返回

	// 模型目录全字段（models-full-fields）：
	Description        string   // descriptionZh 中文描述
	Credits            string   // credits 积分倍率原文（如 "x0.05"），仅展示不参与选号
	Tags               []string // tags 模型标签（含 badge:限时免费 等）
	Vendor             string   // vendor 厂商标识
	IsDefault          bool     // isDefault 是否默认模型
	SupportsReasoning  bool     // supportsReasoning 是否支持推理
	SupportsToolCall   bool     // supportsToolCall 是否支持工具调用
	OnlyReasoning      bool     // onlyReasoning 是否纯推理模型
	SupportsImages     bool     // 顶层 supportsImages（多模态能力，透出到 /v1/models）
	MaxAllowedSize     int64    // maxAllowedSize 最大允许上下文（与 maxInputTokens 口径并列，上游各自下发）
	CanDisableThinking bool     // reasoning.canDisableThinking：思考可关（off 档可用）
	ReasoningEffort    string   // reasoning.effort 推理模式（与 supportedEfforts 数组不同源）
	ReasoningSummary   string   // reasoning.summary 推理摘要模式（如 "auto"）

	// 优惠（modelPromotions，/v3/config data.modelPromotions）：Credits 是**牌价**
	//（转正后基准倍率），Promo* 是当前生效的限时优惠——面板据此显示「生效价 +
	// 标签 + 牌价」。PromoFactor 为 nil 表示无 machine-readable 折扣（如「错峰
	// 使用」只有时段文案无 factor），仅挂标签/提示。
	PromoFactor  *float64 // 折扣系数（0=限时免费，0.5=五折）；nil=无
	PromoCredits string   // 折扣后倍率原文（如 "0x" / "0.50x"），仅展示
	PromoLabel   string   // 徽章文案（限时免费 / 夜间折扣 / 错峰使用）
	PromoNote    string   // hover 说明原文（含时段/日期描述）
}

// dynModelEntry 上游模型目录（CN /console 与 global /v2 同构）的单条模型解析形态，
// FetchModels 与 global_models.go 的探测共用。iconUrl/descriptionEn/生成参数等
// 按「不透出」原则不解析。modelInfo() 是 dynEntry→ModelInfo 映射的单一事实来源，
// 杜绝两域映射漂移。
type dynModelEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// ModelID / Model id 的宽松回退键（仅 global 目录的多信封兜底用，CN 目录
	// 不下发这两个键；字段加在这里只是让 typed 解析能"看见"它们）。
	ModelID         string   `json:"modelId"`
	Model           string   `json:"model"`
	Description     string   `json:"descriptionZh"`
	Credits         string   `json:"credits"`
	Tags            []string `json:"tags"`
	Vendor          string   `json:"vendor"`
	IsDefault       bool     `json:"isDefault"`
	MaxInputTokens  int64    `json:"maxInputTokens"`
	MaxOutputTokens int64    `json:"maxOutputTokens"`
	MaxAllowedSize  int64    `json:"maxAllowedSize"`
	Disabled        bool     `json:"disabled"`
	SupportsImages  bool     `json:"supportsImages"`
	SupportsReason  bool     `json:"supportsReasoning"`
	SupportsTool    bool     `json:"supportsToolCall"`
	OnlyReasoning   bool     `json:"onlyReasoning"`
	Reasoning       struct {
		Effort             string   `json:"effort"`
		Summary            string   `json:"summary"`
		DefaultEffort      string   `json:"defaultEffort"`
		CanDisableThinking bool     `json:"canDisableThinking"`
		SupportedEfforts   []string `json:"supportedEfforts"`
	} `json:"reasoning"`
}

// modelInfo 按解析条目构造 ModelInfo（dynEntry→ModelInfo 映射的单一事实来源）。
// defaultEffort 新老双键兼容：defaultEffort 优先，缺省回落 effort。
func (m dynModelEntry) modelInfo() ModelInfo {
	def := m.Reasoning.DefaultEffort
	if def == "" {
		def = m.Reasoning.Effort
	}
	return ModelInfo{
		ID:                 m.ID,
		Name:               m.Name,
		ContextWindow:      m.MaxInputTokens,
		MaxTokens:          m.MaxOutputTokens,
		Efforts:            m.Reasoning.SupportedEfforts,
		DefaultEffort:      def,
		SupportsImages:     m.SupportsImages,
		Description:        m.Description,
		Credits:            m.Credits,
		Tags:               m.Tags,
		Vendor:             m.Vendor,
		IsDefault:          m.IsDefault,
		SupportsReasoning:  m.SupportsReason,
		SupportsToolCall:   m.SupportsTool,
		OnlyReasoning:      m.OnlyReasoning,
		MaxAllowedSize:     m.MaxAllowedSize,
		CanDisableThinking: m.Reasoning.CanDisableThinking,
		ReasoningEffort:    m.Reasoning.Effort,
		ReasoningSummary:   m.Reasoning.Summary,
	}
}

// nonChatModel 判定是否非对话模型（应从模型列表过滤掉）。
// 来源：harness buddy.ts:547-555。三类规则：
//   - id 前缀 nes-/completion-/codewise-：嵌入/补全/代码专用模型，选了报 code=11102。
//   - maxOutputTokens ≤ 256：tiny 输出非对话模型。
//   - tags 含生成类标签（图片/视频）：生成模型走各自专用端点，作为对话模型
//     选上去只会报 11102，非本网关用途。
//
// 生成类标签随上游扩充：早期只有 text-to-image，桌面端目录（2026-10-02 实测）
// 另有 text-to-video / image-to-video（seedance 系列）与 image-to-image
// （gpt-image 系列）——后者已由 text-to-image 覆盖，此处补齐视频两类。
// 注意本函数 CN 与 global 共用，新增标签对两域同时生效。
func nonChatModel(id string, maxOutputTokens int64, tags []string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range [...]string{"nes-", "completion-", "codewise-"} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	if maxOutputTokens > 0 && maxOutputTokens <= 256 {
		return true
	}
	for _, t := range tags {
		switch t {
		case "text-to-image", "image-to-image", "text-to-video", "image-to-video":
			return true
		}
	}
	return false
}

// FetchModels 调上游动态模型接口（CN 侧；global 账号见 global_models.go 家族）。
//
// 目录 = /v3/config（桌面端内置 CLI 的 UA，与官方桌面端拉到的同一份）；企业号再并上企业模型
// 目录（官方内置 CLI 的 ModelsProductProvider，只要 tags 含 chat 的对话模型，补缺），
// 两路**并发**探测。合并去重 key = 模型 id，v3 条目优先
// （credits 等字段以 v3 为准），企业端点只补 v3 缺失的模型。企业号任一路失败不拖累另一路。
func (c *Client) FetchModels(a *auth.Auth) ([]ModelInfo, error) {
	out, err := c.probeCatalog(a)
	if err != nil {
		return nil, err
	}
	// 企业号的名单因企业而异，顺带记一份它自己的目录（个人号不记）。
	c.storeAccountCatalog(a, out)
	c.storeModelRates(a.Realm(), out)
	ids := make([]string, 0, len(out))
	for _, mi := range out {
		ids = append(ids, mi.ID)
	}
	c.storeRealmModels(a.Realm(), ids)
	// 刷新 effort 能力缓存（供请求体降级；无 supportedEfforts 的模型不入桶）。
	// 空桶时跳过写：避免「某探测无档位数据」清掉既有桶。
	cache := make(map[string][]string, len(out))
	defCache := make(map[string]string, len(out))
	for _, mi := range out {
		if len(mi.Efforts) > 0 {
			cache[mi.ID] = mi.Efforts
		}
		if mi.DefaultEffort != "" {
			defCache[mi.ID] = mi.DefaultEffort
		}
	}
	if len(cache) == 0 && len(defCache) == 0 {
		return out, nil
	}
	// 按探测账号的 realm 写入对应桶：CN 探测只进 cn 桶，global 同模型名不被污染（C-2）。
	c.storeEfforts(a.Realm(), cache, defCache)
	return out, nil
}

// probeCatalog 探测 /v3/config（企业号并发再探企业模型目录）并合并成一份目录，不写任何缓存
// （FetchModels 写域缓存，FetchAccountCatalog 只写企业号自己的目录）。
//
// 个人号只拉 /v3/config：官方桌面端个人号只调这一个目录接口。以前还会换 IDE UA、
// 再探个人号的企业端点，同一个号用几种客户端身份拉目录是官方用户不会有的特征；10-19 生产
// 实测个人号企业端点给的模型全在桌面端目录里。
func (c *Client) probeCatalog(a *auth.Auth) ([]ModelInfo, error) {
	if !a.IsEnterprise() {
		out, err := c.fetchV3Models(a)
		if err != nil {
			return nil, err
		}
		return out, nil
	}
	type probeResult struct {
		infos []ModelInfo
		err   error
	}
	enterpriseCh := make(chan probeResult, 1)
	v3Ch := make(chan probeResult, 1)
	go func() {
		infos, err := c.fetchEnterpriseModels(a)
		enterpriseCh <- probeResult{infos, err}
	}()
	go func() {
		infos, err := c.fetchV3Models(a)
		v3Ch <- probeResult{infos, err}
	}()
	enterprise := <-enterpriseCh
	v3 := <-v3Ch
	if enterprise.err != nil && v3.err != nil {
		return nil, enterprise.err // 两路全失败：返回企业端点错误（既有调用方语义零漂移）
	}
	if v3.err != nil {
		// /v3 失败降级：不拖累企业端点结果（降级仅企业端点 + warn）。
		log.Printf("WARN: [upstream] fetch models: v3/config probe failed (degraded to enterprise endpoint): %v", v3.err)
	}
	if enterprise.err != nil {
		log.Printf("WARN: [upstream] fetch models: enterprise endpoint failed (v3/config only): %v", enterprise.err)
	}
	out := mergeModelInfos(v3.infos, enterprise.infos)
	if len(out) == 0 {
		return nil, fmt.Errorf("models api returned empty list")
	}
	return out, nil
}

// mergeModelInfos 合并两路模型目录：primary 为主（同 id 以 primary 条目为准——
// credits 等字段以主端点为权威），secondary 只补 primary 缺失的 id。
// 去重 key = 模型 id；输出顺序 = primary 原序在前、secondary 补充项（secondary 原序）
// 在后——稳定输出，不依赖 map 迭代序。
func mergeModelInfos(primary, secondary []ModelInfo) []ModelInfo {
	if len(secondary) == 0 {
		return primary
	}
	seen := make(map[string]bool, len(primary)+len(secondary))
	out := make([]ModelInfo, 0, len(primary)+len(secondary))
	for _, mi := range primary {
		if mi.ID == "" || seen[mi.ID] {
			continue
		}
		seen[mi.ID] = true
		out = append(out, mi)
	}
	for _, mi := range secondary {
		if mi.ID == "" || seen[mi.ID] {
			continue
		}
		seen[mi.ID] = true
		out = append(out, mi)
	}
	return out
}

// enterpriseModelsPath 企业模型目录端点路径模板。
// 依据：官方桌面端内置 CLI 的 ModelsProductProvider（2.161.0 起，两域现在的 2.165.0 相同）——只有账号有
// enterpriseId 才调，GET <endpoint>/console/enterprises/{enterpriseId}/config/models，
// 走请求框架并带内置 CLI 的 UA，超时 5 秒。响应 {"code":0,"data":[模型对象, ...]}，
// data 直接是数组（10-20 生产用个人号请求 /console/enterprises/personal/config/models
// 返回 {"code":0,"data":[]}，结构一致）。
const enterpriseModelsPath = "/console/enterprises/%s/config/models"

// enterpriseModelsTimeout 企业模型目录请求的超时（官方 ModelsProductProvider 5 秒）。
// 变量而非常量：测试里临时改短，避免为验证超时真等 5 秒。
var enterpriseModelsTimeout = 5 * time.Second

// fetchEnterpriseModels 单路探测企业模型目录（官方内置 CLI 的 ModelsProductProvider 接口）。
// 解析口径：data 数组里 tags 含 "chat" 的才并入，再剔 nonChatModel 与 disabled，顺序按响应原序。
func (c *Client) fetchEnterpriseModels(a *auth.Auth) ([]ModelInfo, error) {
	// 局部变量名避开 url（本包已 import net/url，同名会造成阅读混淆）。
	endpoint := c.chatBase(a) + fmt.Sprintf(enterpriseModelsPath, url.PathEscape(a.EnterpriseID))
	ctx, cancel := context.WithTimeout(context.Background(), enterpriseModelsTimeout)
	defer cancel()
	req, err := newAccountRequest(ctx, a, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	// 请求框架的头带上企业身份（X-User-Id / X-Enterprise-Id / X-Tenant-Id），否则上游给的是个人号的名单。
	c.frameworkHeaders(req, a, c.userAgent(a))
	resp, err := doDecoded(c.HTTP, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		// 读失败 → 传输层错误（handler 侧该路径不 NoteError）。
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models api status %d: %s", resp.StatusCode, truncate(string(raw), 120))
	}
	var env struct {
		Code int             `json:"code"`
		Data []dynModelEntry `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("models parse: %w", err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("models api code=%d", env.Code)
	}
	// 只有 tags 含 "chat" 的才并进 agent 的模型名单（官方 processModelAgentRelations），
	// 再按既有规则剔除非对话条目（nes-/completion-/codewise- 前缀、maxOutputTokens≤256、
	// 生成类 tags；来源：harness buddy.ts:547-555）与 disabled 的。
	out := make([]ModelInfo, 0, len(env.Data))
	for _, m := range env.Data {
		if !hasChatTag(m.Tags) || m.Disabled || nonChatModel(m.ID, m.MaxOutputTokens, m.Tags) {
			continue
		}
		out = append(out, m.modelInfo())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("models api returned empty list")
	}
	return out, nil
}

// hasChatTag 报告 tags 里是否有 "chat"（企业模型目录只收对话模型）。
func hasChatTag(tags []string) bool {
	for _, t := range tags {
		if t == "chat" {
			return true
		}
	}
	return false
}

// fetchV3Models 单路探测 /v3/config（桌面端内置 CLI 的请求形状，见 fetchV3ConfigModelMap）。
// v3 面取全量 models（不按 agents[cli] 过滤，与 global 探测口径一致），按同一
// nonChatModel 规则剔除非对话条目（selected 会选模型报 code=11102）。
// 失败返回错误（调用方降级为仅企业端点）。
func (c *Client) fetchV3Models(a *auth.Auth) ([]ModelInfo, error) {
	byID, err := c.fetchV3ConfigModelMap(a)
	if err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(byID))
	for _, mi := range byID {
		if nonChatModel(mi.ID, mi.MaxTokens, mi.Tags) {
			continue
		}
		out = append(out, mi)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("v3/config returned empty models")
	}
	return out, nil
}

// v3ModelPromotion /v3/config data.modelPromotions 单条优惠定义（2026-09-23 实测
// 7 条：deepseek 系错峰五折、glm-5.2 夜间五折、hy3 与 hy4-preview-f 限时免费）。
// discount 只在部分条目上存在：有 factor 的可算生效价；「错峰使用」类只有时段
// 文案（factor 藏在 hover 文本里，无机器可读值），仅透出标签与说明。
type v3ModelPromotion struct {
	Enabled  bool     `json:"enabled"`
	Priority int      `json:"priority"`
	ModelIDs []string `json:"modelIds"`
	Badge    *struct {
		Label string `json:"label"`
	} `json:"badge"`
	Discount *struct {
		DiscountedCredits string  `json:"discountedCredits"`
		Factor            float64 `json:"factor"`
	} `json:"discount"`
	Hover *struct {
		TextZh string `json:"textZh"`
	} `json:"hover"`
	Schedule *struct {
		Daily []struct {
			Start string `json:"start"` // "23:00"
			End   string `json:"end"`   // "7:50"（可跨午夜）
		} `json:"daily"`
		Timezone   string `json:"timezone"`  // 实测恒 Asia/Shanghai
		ValidFrom  string `json:"validFrom"` // RFC3339，可缺省
		ValidUntil string `json:"validUntil"`
	} `json:"schedule"`
}

// promoZone 优惠时区：上游恒 Asia/Shanghai（UTC+8 无夏令时），用 FixedZone 免依赖
// 系统 tzdata（Windows 无 IANA 库时 LoadLocation 会失败）。
var promoZone = time.FixedZone("CST", 8*3600)

// promoClock 解析 "HH:MM" 为当日分钟数；坏值返回 (-1, false)。
func promoClock(hhmm string) (int, bool) {
	parts := strings.Split(hhmm, ":")
	if len(parts) != 2 {
		return -1, false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 24 || m < 0 || m > 59 {
		return -1, false
	}
	return h*60 + m, true
}

// promoActive 评估优惠在 now 是否生效：enabled + validFrom/validUntil 内 + 落在
// 任一 daily 窗口（支持跨午夜，如 23:00→7:50）。schedule 为 nil 视为全天生效。
func promoActive(p *v3ModelPromotion, now time.Time) bool {
	if !p.Enabled {
		return false
	}
	if sc := p.Schedule; sc != nil {
		if sc.ValidFrom != "" {
			from, err := time.Parse(time.RFC3339, sc.ValidFrom)
			if err == nil && now.Before(from) {
				return false
			}
		}
		if sc.ValidUntil != "" {
			until, err := time.Parse(time.RFC3339, sc.ValidUntil)
			if err == nil && !now.Before(until) {
				return false
			}
		}
		if len(sc.Daily) > 0 {
			cur := now.Hour()*60 + now.Minute()
			inWindow := false
			for _, w := range sc.Daily {
				st, ok1 := promoClock(w.Start)
				ed, ok2 := promoClock(w.End)
				if !ok1 || !ok2 {
					continue
				}
				if st <= ed {
					if cur >= st && cur < ed {
						inWindow = true
						break
					}
				} else if cur >= st || cur < ed { // 跨午夜（23:00→7:50）
					inWindow = true
					break
				}
			}
			if !inWindow {
				return false
			}
		}
	}
	return true
}

// applyModelPromotions 把当前生效的优惠挂到目录条目：同模型多条命中取 priority
// 最高（实测 glm-5.2 白天 badge-only(50) 与夜间五折(100) 靠 priority+daily 双轨
// 切换）。无 discount 对象的条目也挂标签/说明（错峰类），PromoFactor 留 nil。
func applyModelPromotions(out map[string]ModelInfo, promos []v3ModelPromotion) {
	if len(promos) == 0 || len(out) == 0 {
		return
	}
	now := time.Now().In(promoZone)
	type cand struct {
		prio int
		p    *v3ModelPromotion
	}
	best := map[string]cand{}
	for i := range promos {
		p := &promos[i]
		if !promoActive(p, now) {
			continue
		}
		for _, id := range p.ModelIDs {
			if _, ok := out[id]; !ok {
				continue // 目录外模型（如同名 global 变体）不挂
			}
			if b, seen := best[id]; !seen || p.Priority > b.prio {
				best[id] = cand{prio: p.Priority, p: p}
			}
		}
	}
	for id, c := range best {
		mi := out[id]
		if c.p.Badge != nil {
			mi.PromoLabel = c.p.Badge.Label
		}
		if c.p.Hover != nil {
			mi.PromoNote = c.p.Hover.TextZh
		}
		if c.p.Discount != nil {
			f := c.p.Discount.Factor
			mi.PromoFactor = &f
			mi.PromoCredits = c.p.Discount.DiscountedCredits
		}
		out[id] = mi
	}
}

// storeEfforts 按 realm 写入 effort 能力缓存桶（efforts + defaultEfforts），并发安全。
// 供 CN FetchModels 与 global 探测共用：拉取到的模型档位落桶后，出站请求体
// normalizeReasoningEffort 才能按域降级。efforts 与 defs 均空时删除该 realm 桶
// （等价「该域无可降级档位」）。调用方负责在「无新数据」时跳过写。
func (c *Client) storeEfforts(realm string, efforts map[string][]string, defs map[string]string) {
	c.effortsMu.Lock()
	defer c.effortsMu.Unlock()
	if c.efforts == nil {
		c.efforts = make(map[string]map[string][]string)
	}
	if c.defaultEfforts == nil {
		c.defaultEfforts = make(map[string]map[string]string)
	}
	k := realmKey(realm)
	if len(efforts) == 0 && len(defs) == 0 {
		delete(c.efforts, k)
		delete(c.defaultEfforts, k)
		return
	}
	c.efforts[k] = efforts
	c.defaultEfforts[k] = defs
}

// normalizeModelRate 把上游倍率原文规范化为可比较的数值键。
// 兼容 "x0.05" / "x0.05 credits" / "0.50x" 等形态；无法数值化时保留去除
// credits 后缀与空白后的原文，避免编造倍率。
func normalizeModelRate(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(s), "credits") {
		s = strings.TrimSpace(s[:len(s)-len("credits")])
	}
	if strings.HasPrefix(strings.ToLower(s), "x") {
		s = strings.TrimSpace(s[1:])
	} else if strings.HasSuffix(strings.ToLower(s), "x") {
		s = strings.TrimSpace(s[:len(s)-1])
	}
	if s == "" {
		return ""
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// effectiveModelRate 返回模型当前生效倍率：有机器可读优惠时取折扣价，
// 否则取牌价；两者均缺省时为空。
func effectiveModelRate(mi ModelInfo) string {
	if mi.PromoFactor != nil && strings.TrimSpace(mi.PromoCredits) != "" {
		return normalizeModelRate(mi.PromoCredits)
	}
	return normalizeModelRate(mi.Credits)
}

// storeModelRates 按 realm 整体替换模型倍率快照。目录成功刷新但没有可解析
// 倍率时写入空桶，使旧倍率不会继续冒充当前价。
func (c *Client) storeModelRates(realm string, infos []ModelInfo) {
	rates := make(map[string]string, len(infos))
	for _, mi := range infos {
		if mi.ID == "" {
			continue
		}
		if rate := effectiveModelRate(mi); rate != "" {
			rates[mi.ID] = rate
		}
	}
	c.effortsMu.Lock()
	defer c.effortsMu.Unlock()
	if c.modelRates == nil {
		c.modelRates = make(map[string]map[string]string)
	}
	c.modelRates[realmKey(realm)] = rates
}

// storeRealmModels 按 realm 整体替换该域的模型名集合（空名跳过）。只在目录成功刷新时调用，
// 拉取失败不清空：沿用上次成功的目录，免得一次偶发失败把跨域选号退回单域。
func (c *Client) storeRealmModels(realm string, ids []string) {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			set[id] = true
		}
	}
	c.effortsMu.Lock()
	defer c.effortsMu.Unlock()
	if c.realmModels == nil {
		c.realmModels = make(map[string]map[string]bool)
	}
	c.realmModels[realmKey(realm)] = set
}

// RealmHasModel 报告该域最近一次成功拉到的模型目录里有没有这个模型；该域企业号
// 自己目录里的模型（企业专属）也算。
// 目录从未拉到（启动预热失败、该域没有账号）时返回 false——宁可不往那个域分流。
func (c *Client) RealmHasModel(realm, model string) bool {
	if c == nil || model == "" {
		return false
	}
	if has, _ := c.realmCatalogHas(realm, model); has {
		return true
	}
	return c.accountCatalogsHave(realm, model)
}

// realmCatalogHas 域目录里有没有这个模型；known 报告该域目录是否拉到过。
func (c *Client) realmCatalogHas(realm, model string) (has, known bool) {
	c.effortsMu.RLock()
	defer c.effortsMu.RUnlock()
	set, known := c.realmModels[realmKey(realm)]
	return set[model], known
}

// ModelRate 返回最近成功刷新的指定域模型生效倍率；未知返回空串。
func (c *Client) ModelRate(realm, model string) string {
	if c == nil || model == "" {
		return ""
	}
	c.effortsMu.RLock()
	defer c.effortsMu.RUnlock()
	return c.modelRates[realmKey(realm)][model]
}

// GlobalEffortSnapshot 导出 global 域 effort 能力缓存（探测下发 ∪ 静态兜底合并后的桶），
// 供 /v1/models 输出 reasoning_supported_efforts / reasoning_default_effort。
// 返回副本；桶未填充（无 global 账号或从未探测）→ nil（调用方回落静态兜底表）。
func (c *Client) GlobalEffortSnapshot() (efforts map[string][]string, defaults map[string]string) {
	return c.effortsSnapshot("global"), c.defaultEffortsSnapshot("global")
}

// 拉配置的预算（官方桌面端 fetchFromRemote 的 5000ms / 250ms / 3000ms）；测试可调。
var (
	// configFetchBudget 整个拉配置的总预算。
	configFetchBudget = 5 * time.Second
	// configFetchMinBudget 剩余预算低于它就不再试下一步。
	configFetchMinBudget = 250 * time.Millisecond
	// configFetchTimeoutBudget 剩余预算不够、失败又是「data 无效」或超时类时给的独立预算。
	configFetchTimeoutBudget = 3 * time.Second
)

// configAttempt 一次 /v3/config 请求的结果。
type configAttempt struct {
	status      int    // HTTP 状态；0 = 没有状态的传输错误
	raw         []byte // 响应体原文（成功时再解析）
	err         error  // 传输/HTTP/解析/业务码错误；data 无效时是解析出的原错误
	dataInvalid bool   // HTTP 200 但 data 没有可持久化的内容（官方 isPersistableProductConfiguration）
	timeout     bool   // 超时类失败（net.Error.Timeout 或 408）
	retryable   bool   // 按规格「可以换」
}

// configDataPersistable 官方 isPersistableProductConfiguration：data 存在、不是 null，
// 且里面至少有一个非空的值（数组非空、对象有键，其他类型都算非空）。
func configDataPersistable(data json.RawMessage) bool {
	if len(bytes.TrimSpace(data)) == 0 {
		return false
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return false
	}
	switch x := v.(type) {
	case nil:
		return false
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	default:
		return true
	}
}

// configIsTimeout 报告错误是不是超时类（net.Error.Timeout）。
func configIsTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// configFetchOnce 发一次 /v3/config。budget 是这次尝试的 ctx 预算；rt 非 nil 时用
// 地址层的钉 IP Transport。
func (c *Client) configFetchOnce(a *auth.Auth, base string, rt http.RoundTripper, budget time.Duration) configAttempt {
	if budget <= 0 {
		budget = time.Nanosecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	req, err := newAccountRequest(ctx, a, http.MethodGet, base+"/v3/config", nil)
	if err != nil {
		return configAttempt{err: err}
	}
	req.Close = true
	c.frameworkHeaders(req, a, c.userAgent(a))
	hc := c.HTTP
	if rt != nil {
		cp := *hc
		cp.Transport = rt
		hc = &cp
	}
	resp, err := doDecoded(hc, req)
	if err != nil {
		return configAttempt{err: err, timeout: configIsTimeout(err),
			retryable: !errors.Is(err, context.Canceled) && !IsRouteError(err)}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		// 读失败 → 传输层错误：半截 body 不进解析（不罚号）。
		return configAttempt{status: resp.StatusCode, err: fmt.Errorf("read body: %w", err)}
	}
	if resp.StatusCode != http.StatusOK {
		at := configAttempt{status: resp.StatusCode, raw: raw,
			err: fmt.Errorf("v3/config status %d: %s", resp.StatusCode, truncate(string(raw), 120))}
		// 只有 408/502/503 可以换（504 不换）。
		at.retryable = failoverRetryIPStatus(resp.StatusCode)
		at.timeout = resp.StatusCode == http.StatusRequestTimeout
		return at
	}
	var env struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return configAttempt{status: resp.StatusCode, raw: raw,
			err: fmt.Errorf("v3/config parse: %w", err)}
	}
	at := configAttempt{status: resp.StatusCode, raw: raw}
	// 官方只看 data 有没有可持久化的内容，不看业务码：data 无效（业务码非 0 时多半如此）
	// 一律按「可重试的无效响应」处理；data 有内容但业务码非 0 时按原语义报错、不换。
	if configDataPersistable(env.Data) && env.Code != 0 {
		at.err = fmt.Errorf("v3/config code=%d", env.Code)
		return at
	}
	if !configDataPersistable(env.Data) {
		// 200 但 data 无效：按官方口径当「可重试的无效响应」。
		at.dataInvalid = true
		at.retryable = true
		if _, perr := parseV3Config(raw); perr != nil {
			at.err = perr
		} else {
			at.err = fmt.Errorf("v3/config returned empty models")
		}
	}
	return at
}

// configSuccess 报告这次尝试是否拿到了可用目录；成功时返回解析结果。
func configSuccess(at configAttempt) (map[string]ModelInfo, bool) {
	if at.err != nil || at.dataInvalid {
		return nil, false
	}
	out, err := parseV3Config(at.raw)
	if err != nil {
		return nil, false
	}
	return out, true
}

// configAttemptError 这次尝试要返回的错误（data 可持久化但解析失败时用解析错误）。
func configAttemptError(at configAttempt) error {
	if at.err != nil {
		return at.err
	}
	_, err := parseV3Config(at.raw)
	if err != nil {
		return err
	}
	return fmt.Errorf("v3/config returned empty models")
}

// noteConfigAttempt 把一次拉配置的尝试按对话的同一口径记进入口熔断器（共用 c.failover）。
func (c *Client) noteConfigAttempt(origin string, at configAttempt) {
	if at.err == nil && !at.dataInvalid {
		c.failover.noteSuccess(origin)
		return
	}
	if at.status != 0 {
		if failoverIsInfraStatus(at.status) {
			c.failover.noteInfraFailure(origin)
		}
		return
	}
	if failoverIsInfraError(at.err) {
		c.failover.noteInfraFailure(origin)
	}
}

// fetchV3ConfigModelMap 拉 /v3/config 配置目录，按模型 id 建能力表。请求形状同桌面端内置 CLI：
// GET、请求框架的头、`Connection: close`。该端点按 UA 下发不同的模型集合（IDE、CLI、桌面端
// 三种 UA 各不相同），这里只用桌面端的，拿到的就是官方桌面端用户看到的那一份。
//
// 超时与失败切换照官方桌面端 5.8.3 内置 CLI 的 fetchFromRemote（规格 §4）：整个拉配置最多
// configFetchBudget（5 秒）；国内版 chatBase 是前门时按熔断器选首选入口，失败后
// 「主入口 → 备用入口 → 备用 IP」。国际版与不可信中转只发一次。只有「可以换」的失败才换：
// 200 但 data 没有可持久化内容、HTTP 408/502/503、没有 HTTP 状态的传输错误（含 DNS 失败）。
func (c *Client) fetchV3ConfigModelMap(a *auth.Auth) (map[string]ModelInfo, error) {
	base := c.chatBase(a)
	if !c.configFailoverActive(a, base) {
		// 国际版 / 不可信 base：只发一次，5 秒总预算。
		at := c.configFetchOnce(a, base, nil, configFetchBudget)
		if out, ok := configSuccess(at); ok {
			return out, nil
		}
		return nil, configAttemptError(at)
	}

	plan := c.chatFailoverPlan(a, base)
	start := time.Now()
	remaining := func() time.Duration { return configFetchBudget - time.Since(start) }
	// 下一步的预算：剩余 <250ms 且失败可以换、且是「data 无效」或超时类时，给 3 秒独立预算。
	nextBudget := func(prev configAttempt) time.Duration {
		left := remaining()
		if left < configFetchMinBudget && prev.retryable && (prev.dataInvalid || prev.timeout) {
			return configFetchTimeoutBudget
		}
		return left
	}

	// 首选入口按熔断器：前门打开且备用入口没打开时直接发备用入口。
	firstBase := plan.firstBase
	at := c.configFetchOnce(a, firstBase, nil, configFetchBudget)
	c.noteConfigAttempt(plan.firstOrigin, at)
	if out, ok := configSuccess(at); ok {
		return out, nil
	}
	if !at.retryable {
		return nil, configAttemptError(at)
	}
	lastErr := configAttemptError(at)

	// 首选是备用入口 → 直接试备用 IP；否则先发备用入口，失败再试备用 IP。
	if plan.firstOrigin != failoverBackupOrigin {
		budget := nextBudget(at)
		if budget < configFetchMinBudget {
			return nil, lastErr
		}
		backupBase := failoverSwapOrigin(firstBase, failoverBackupOrigin)
		next := c.configFetchOnce(a, backupBase, nil, budget)
		c.noteConfigAttempt(failoverOrigin(backupBase), next)
		if out, ok := configSuccess(next); ok {
			return out, nil
		}
		lastErr = configAttemptError(next)
		if !next.retryable {
			return nil, lastErr
		}
		at = next
	}

	// 试备用 IP：只在备用入口上；地址层不生效（钉 IP 函数为 nil）时跳过；备用入口走环境变量里的代理时
	// 也跳过（照官方：这一步只给直连的请求用，见 proxy.go）。走号的代理时经代理连备用 IP（route.go）。
	if c.PinIP == nil || len(failoverFallbackIPs) == 0 || c.viaEnvProxy(a, failoverHost(failoverBackupOrigin)) {
		return nil, lastErr
	}
	budget := nextBudget(at)
	if budget < configFetchMinBudget {
		return nil, lastErr
	}
	backupBase := failoverSwapOrigin(firstBase, failoverBackupOrigin)
	rt := c.PinIP(failoverFallbackIPs[0])
	final := c.configFetchOnce(a, backupBase, rt, budget)
	c.noteConfigAttempt(failoverOrigin(backupBase), final)
	if out, ok := configSuccess(final); ok {
		return out, nil
	}
	return nil, configAttemptError(final)
}

// configFailoverActive 报告拉配置是否走「主入口 → 备用入口 → 备用 IP」：国内版且 chatBase
// 是可信入口（直接配成备用入口时跳过换入口，只试备用 IP，同官方）；国际版、不可信中转只发一次。
func (c *Client) configFailoverActive(a *auth.Auth, base string) bool {
	if c.globalOn(a) {
		return false
	}
	o := failoverOrigin(base)
	return o == cnProductEndpoint || o == failoverAltOrigin || o == failoverBackupOrigin
}

// parseV3Config 解析 /v3/config 响应体为模型目录。错误语义与改造前一致。
func parseV3Config(raw []byte) (map[string]ModelInfo, error) {
	var env struct {
		Code int `json:"code"`
		Data struct {
			Models []dynModelEntry `json:"models"`
			// 试用模型横幅：上游把「N 天免费试用」的模型放在这里，**不在 data.models 里**。
			// 实测 global 侧 hy4-preview-f 只出现在此（modelId=hy4-preview-f、
			// targetModelId=hy4-preview、trialDays=14），纯 data.models 解析会漏掉它。
			ProductFeaturesConfig struct {
				ModelTrialBanner struct {
					Banners []struct {
						ModelID       string `json:"modelId"`
						TargetModelID string `json:"targetModelId"`
					} `json:"banners"`
				} `json:"ModelTrialBanner"`
			} `json:"productFeaturesConfig"`
			ModelPromotions []v3ModelPromotion `json:"modelPromotions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("v3/config parse: %w", err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("v3/config code=%d", env.Code)
	}
	out := make(map[string]ModelInfo, len(env.Data.Models))
	for _, m := range env.Data.Models {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		out[m.ID] = m.modelInfo()
	}
	// 补入试用横幅模型（ModelTrialBanner）：上游把「N 天免费试用」的模型只放在这里，
	// data.models 里没有，故纯目录解析会漏（实测 global 侧 hy4-preview-f 即如此，
	// 但该模型**实际可调用**）。
	//
	// 元数据口径：能力字段（context/maxTokens/efforts/reasoning 等）从 targetModelId
	// 的既有条目继承——试用版与其转正目标是同族模型，能力应当一致；
	// 但 **Credits 与 Tags 显式清空**——它们描述的是"转正后"的计费与营销信息
	// （如 hy4-preview 的 x0.29 与 badge），用在免费试用版上会误导下游展示。
	//
	// firstUseTimeKey / trialDays 属**账号级**试用状态，不透出给下游。
	for _, b := range env.Data.ProductFeaturesConfig.ModelTrialBanner.Banners {
		id := strings.TrimSpace(b.ModelID)
		if id == "" {
			continue
		}
		if _, exists := out[id]; exists {
			continue
		}
		mi := ModelInfo{ID: id}
		if tgt := strings.TrimSpace(b.TargetModelID); tgt != "" {
			if base, ok := out[tgt]; ok {
				mi = base
				mi.ID = id
			}
		}
		mi.Credits = ""
		mi.Tags = nil
		out[id] = mi
	}
	// 挂当前生效的限时优惠（modelPromotions）：Credits 字段是**牌价**（转正后基准
	// 倍率，如 hy4-preview-f 的 x0.29），而 WorkBuddy 客户端显示的是生效价（试用/
	// 折扣窗口内 factor 打折）——面板据此展示「生效价 + 标签 + 牌价」。
	applyModelPromotions(out, env.Data.ModelPromotions)

	if len(out) == 0 {
		return nil, fmt.Errorf("v3/config returned empty models")
	}
	return out, nil
}

// UserResource 查询账号积分余额与总额度（所有套餐聚合）。remain 负值钳 0；
// total 取与 remain 同源的额度字段（CycleCapacitySize 优先，无周期额度退
// CapacitySize），上游缺 size 的套餐按 remain 兜底，保证百分比不超 100%。
// CreditPackage 单个积分包的构成明细（面板「积分构成」用）。
//
// 两个账号即使任务完成度完全一致，余额也可能相差上千——差别藏在包的**面额与
// 来源**里（「国内运营裂变包」「拉新权益包」按次发放，面额 6~1500 不等）。
// 只看聚合值看不出这件事，所以把逐包明细暴露出来。
type CreditPackage struct {
	Name   string `json:"name"`
	Remain int64  `json:"remain"`
	Used   int64  `json:"used"`
	Size   int64  `json:"size"`
	// EndTime 包里剩下的积分作废的时刻，按包类型取（见 resourceAccount.expiry）。
	// RFC3339 或上游墙钟字符串，前端取日期部分展示。
	EndTime string `json:"end_time,omitempty"`
	// ExpiresAt 与 EndTime 同源的 Unix 毫秒时间戳，供面板按精确剩余天数聚合。
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// Refresh 为 true 表示按周期刷新的包（订阅、体验类），EndTime 是刷新时刻。
	Refresh bool `json:"refresh,omitempty"`
	// CreatedAt 发放时刻，RFC3339。**这是区分「首登赠送」与「活动奖励」的唯一依据**：
	// 两类包的 PackageName 与 PackageCode 完全相同（例如都是「国内运营裂变包」+
	// TCACA_code_007_*），只看名字无法区分，只有时间能说明它是不是账号首次授权那刻发的。
	CreatedAt string `json:"created_at,omitempty"`
	// PackageCode / SubProductCode 上游的包类型标识。同 Name 不同 Code 的包可能
	// 是不同来源；同 Code 不同面额则是同来源分批发放（首登 1500 与活动 300 即如此）。
	PackageCode    string `json:"package_code,omitempty"`
	SubProductCode string `json:"sub_product_code,omitempty"`
	SubProductName string `json:"sub_product_name,omitempty"`
	// Cycle 为 true 表示按周期发放的包（读 Cycle* 字段），否则读 Capacity*。
	Cycle bool `json:"cycle,omitempty"`
}

// CreditPackages 返回账号当前的逐包构成。remain/size 为各包求和。
//
// 包来自官方的付费包、免费包两个逐包接口，单包额度按官方算法（见 resources.go）。
func (c *Client) CreditPackages(a *auth.Auth) ([]CreditPackage, int64, int64, error) {
	packs, _, err := c.userResources(a)
	if err != nil {
		return nil, 0, 0, err
	}
	out := make([]CreditPackage, 0, len(packs))
	var sumRemain, sumSize float64
	for _, p := range packs {
		cp := CreditPackage{
			Name:           p.PackageName,
			PackageCode:    p.PackageCode,
			SubProductCode: p.SubProductCode,
			SubProductName: p.SubProductName,
		}
		var end time.Time
		var ok bool
		cp.EndTime, end, ok = p.expiry()
		if ok {
			cp.ExpiresAt = end.UnixMilli()
		}
		cp.Refresh = p.refreshes()
		// CreateTime 是 epoch 毫秒；0 表示上游没给，留空而不是伪造 1970。
		if p.CreateTime > 0 {
			cp.CreatedAt = time.UnixMilli(p.CreateTime).Format(time.RFC3339)
		}
		size, left := p.capacity()
		cp.Cycle = p.CapacityType != sliceCapacityType || len(p.SlicePeriodUsageDetails) == 0
		cp.Size, cp.Remain = credits(size), credits(left)
		cp.Used = cp.Size - cp.Remain
		sumRemain += left
		sumSize += size
		out = append(out, cp)
	}
	// 面额降序：大包一眼可见，正是差异最可能出现的地方。
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out, credits(sumRemain), credits(sumSize), nil
}

func (c *Client) UserResource(a *auth.Auth) (remain, total int64, err error) {
	remain, total, _, err = c.UserResourceDetailed(a, 0)
	return remain, total, err
}

// packageEndLayout 上游套餐到期时间的墙钟格式（UTC+8，与 softRateResetLoc 同口径）。
const packageEndLayout = "2006-01-02 15:04:05"

// parsePackageEndTime 统一解析上游套餐到期时间。空值、格式异常返回 false，
// 调用方据此保守地不把该包计入最早到期路由。
func parsePackageEndTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(packageEndLayout, raw, softRateResetLoc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// UserResourceDetailed 在 UserResource 基础上额外返回「快过期」积分子集：
// soon > 0 且套餐到期时刻 ≤ now+soon 的余额计入 expiring（pool 据此优先消耗，避免官方活动
// 赠送的奖励积分到期作废）；soon ≤ 0 时 expiring 恒 0（禁用分桶，行为与引入前一致）。
// expiring 是 remain 的一部分。
//
// 到期时刻按包类型取（resourceAccount.expiry）：一次性发放的包看 DeductionEndTime，
// 每日免费包和按周期刷新的包（含国际版 14 天的 Bonus Pack）看 CycleEndTime。解析失败/缺失
// 的套餐保守不计入 expiring（不误标为快过期而插队）。
// 单套餐取数统一调 packageRemainUsed（与 CreditPackages 同一事实来源，含 remain
// 钳 [0,size] 与 used 修正；消除双份逻辑漂移——旧中间 switch 只钳负值，上游脏数据
// CycleRemain>Size 时会高估）。
func (c *Client) UserResourceDetailed(a *auth.Auth, soon time.Duration) (remain, total, expiring int64, err error) {
	remain, total, expiring, _, _, err = c.UserResourceDetailedWithExpiry(a, soon)
	return remain, total, expiring, err
}

// enterpriseUnlimitedRemain / enterpriseUnlimitedTotal 企业版「不限量」在池内的等价表示
// （上游 limitNum == -1；桌面端同判为 unlimited）。
//
// 池内没有"不限"状态位，而 credits 同时参与两处判定——路由权重（credits/maxCredits）与
// credit_floor（余额低于阈值则不接收费模型）——不限量必须两处都"不构成限制"，故剩余量
// 取一个足够大的值；total 记 -1 供面板显示「不限」。total 仅用于展示与快照，
// 不参与任何判定（已核对全仓库引用面）。
const (
	enterpriseUnlimitedRemain int64 = 1 << 40
	enterpriseUnlimitedTotal  int64 = -1
)

// enterpriseResource 查询**企业版账号**的分配额度，并归一到个人口径的
// (remain, total, expiring, earliestAt, earliestRemaining)。
//
// 为什么必须分流：企业额度不在「个人资源包」体系内——/billing/meter/get-user-resource*
// 对 enterpriseId 非空的账号恒返回空 Accounts（实测 code 0 且 Accounts null），
// 面板因此长期显示 0/未知（额度其实是有的）。
//
// 上游口径（实测 2026-10-07，企业号 a6239ec8）：
//
//	POST /v2/billing/meter/get-enterprise-user-usage   （X-Enterprise-Id 由 authServiceHeaders 注入）
//	{"credit":812.45,"limitNum":2000,
//	 "cycleStartTime":"2026-09-27 00:00:00","cycleEndTime":"2026-10-26 23:59:59",
//	 "cycleResetTime":"2026-10-27 00:00:00"}
//
// credit = **本周期已用**（与个人口径 CapacityRemain「剩余」语义相反），
// limitNum = **分配给本账号的额度**。故 remain = limitNum - credit、total = limitNum。
// 注意：企业**池**总额度是另一回事，成员账号无权查询（实测所有池端点 403 not_authorized），
// 本函数只反映该账号被分配的额度。
//
// 分桶：企业配额按周期重置、未用完即作废，与个人「奖励积分到期作废」同性质，故把
// cycleEndTime 作为唯一到期批次——周期末企业号会被 prefer_expiring 优先选中（期望行为）。
// limitNum < 0（不限量）无作废语义，不参与分桶。
func (c *Client) enterpriseResource(a *auth.Auth, soon time.Duration) (remain, total, expiring int64, earliestAt time.Time, earliestRemaining int64, err error) {
	// 与个人口径一致：余额是签到后的紧邻调用，偶发 500 值得有界重试。
	var data json.RawMessage
	err = c.retryBillingTransient(func() error {
		var e error
		data, e = c.authServiceJSON(a, http.MethodPost, "/v2/billing/meter/get-enterprise-user-usage", map[string]any{})
		return e
	})
	if err != nil {
		return 0, 0, 0, time.Time{}, 0, err
	}
	// 兼容上游两套字段命名（桌面端两条解析路径分别读 camelCase 与 snake_case）。
	var resp struct {
		Credit         float64 `json:"credit"`
		LimitNum       int64   `json:"limitNum"`
		LimitNumSnake  int64   `json:"limit_num"`
		UsedNum        float64 `json:"used_num"`
		CycleEndTime   string  `json:"cycleEndTime"`
		CycleResetTime string  `json:"cycleResetTime"`
	}
	if uerr := json.Unmarshal(data, &resp); uerr != nil {
		return 0, 0, 0, time.Time{}, 0, fmt.Errorf("enterprise resource parse: %w", uerr)
	}
	limit := resp.LimitNum
	if limit == 0 && resp.LimitNumSnake != 0 {
		limit = resp.LimitNumSnake
	}
	used := resp.Credit
	if used == 0 && resp.UsedNum != 0 {
		used = resp.UsedNum
	}
	// 不限量：池内无对应状态位，用大剩余量让路由权重与 credit_floor 都不构成限制。
	if limit < 0 {
		return enterpriseUnlimitedRemain, enterpriseUnlimitedTotal, 0, time.Time{}, 0, nil
	}
	// credit 是浮点（如 812.45），池内 credits 是整数：四舍五入到最近整数
	// （向下取整会低报剩余额度，与"额度还有多少"的展示意图相悖）。
	usedInt := int64(used)
	if used-float64(usedInt) >= 0.5 {
		usedInt++
	}
	remain = limit - usedInt
	if remain < 0 {
		remain = 0
	}
	total = limit
	// 周期到期批次：cycleEndTime 优先，缺失回落 cycleResetTime。
	now := time.Now()
	end, ok := parsePackageEndTime(resp.CycleEndTime)
	if !ok {
		end, ok = parsePackageEndTime(resp.CycleResetTime)
	}
	if ok && remain > 0 && end.After(now) {
		earliestAt, earliestRemaining = end, remain
		if soon > 0 && !end.After(now.Add(soon)) {
			expiring = remain
		}
	}
	return remain, total, expiring, earliestAt, earliestRemaining, nil
}

// UserResourceDetailedWithExpiry 在 UserResourceDetailed 基础上返回最早未来到期批次：
// earliestAt 是最早的可用到期时刻，earliestRemaining 是和它同一天（北京时间）到期的所有正余额包
// 的剩余量之和。到期时刻按包类型取（resourceAccount.expiry），和面板「检查」同一个口径。
// 已过期、剩余为 0、缺少或无法解析到期时间的包都不会成为最早批次；无有效批次时返回零值。
func (c *Client) UserResourceDetailedWithExpiry(a *auth.Auth, soon time.Duration) (remain, total, expiring int64, earliestAt time.Time, earliestRemaining int64, err error) {
	// 企业版额度不在「个人资源包」体系内：get-user-resource* 对 enterpriseId 非空账号
	// 恒返回空 Accounts（实测 code 0 且 Accounts null），故分流到企业口径端点。
	if a.IsEnterprise() {
		return c.enterpriseResource(a, soon)
	}
	packs, summary, err := c.userResources(a)
	if err != nil {
		return 0, 0, 0, time.Time{}, 0, err
	}
	now := time.Now()
	var earliestDay string // 最早一批的北京时间日期：同一天到期的合成一批，和面板「检查」一致
	for _, p := range packs {
		_, left := p.capacity()
		r := credits(left)
		if r <= 0 {
			continue
		}
		_, end, ok := p.expiry()
		if !ok || !end.After(now) {
			continue
		}
		switch day := end.In(softRateResetLoc).Format(time.DateOnly); {
		case earliestAt.IsZero() || day < earliestDay:
			earliestAt, earliestDay, earliestRemaining = end, day, r
		case day == earliestDay:
			earliestRemaining += r
			if end.Before(earliestAt) {
				earliestAt = end
			}
		}
		// 分桶：仅 soon>0 且确实在窗口内 → expiring。
		if soon > 0 && !end.After(now.Add(soon)) {
			expiring += r
		}
	}
	// 余额与总额取汇总（官方桌面端显示的剩余积分就是汇总相加）；汇总没查到时用逐包合计。
	sumSize, sumLeft := packageTotals(packs)
	if summary != nil {
		sumLeft, sumSize = summary.left, summary.total
	}
	remain, total = credits(sumLeft), credits(sumSize)
	expiring = min(expiring, remain)
	earliestRemaining = min(earliestRemaining, remain)
	return remain, total, expiring, earliestAt, earliestRemaining, nil
}

// DailyCheckin 执行每日签到。已签到（业务 code 非 0）也返回错误，调用方按 msg 区分。
// 照官方先查签到状态：活动没开就不签，今天已签过就直接返回「今天已签到」，不再多发一次签到。
// 状态查不到时照常签到，免得一次抖动让账号整天漏签。偶发上游 5xx（code 10000）做有界重试
// （见 retryBillingTransient）；「已签到」等业务错误不重试。
func (c *Client) DailyCheckin(a *auth.Auth) error {
	var data json.RawMessage
	err := c.retryBillingTransient(func() error {
		var e error
		data, e = c.authServiceJSON(a, http.MethodPost, checkinStatusPath, map[string]any{}, c.withDeviceToken(a))
		return e
	})
	var st struct {
		Active         bool `json:"active"`
		TodayCheckedIn bool `json:"today_checked_in"`
	}
	if err == nil && json.Unmarshal(data, &st) == nil {
		if !st.Active {
			return &Error{Kind: ErrClient, Status: http.StatusOK, Msg: "签到活动未开启"}
		}
		if st.TodayCheckedIn {
			return &Error{Kind: ErrClient, Status: http.StatusOK, Msg: "今天已签到（签到状态）"}
		}
	}
	err = c.retryBillingTransient(func() error {
		var e error
		data, e = c.authServiceJSON(a, http.MethodPost, dailyCheckinPath, map[string]any{}, c.withDeviceToken(a))
		return e
	})
	if err != nil {
		return err
	}
	// 这次签到得的积分：官方 checkin() 取 data.credit，没有按 0。签到本身已经成功，读不出来只记一行。
	var res struct {
		Credit int64 `json:"credit"`
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &res); err != nil {
			log.Printf("WARN: [upstream] checkin acct=%s: 签到成功，读不出得了多少积分：%v", logfmt.Label(a.UID, a.Nickname), err)
		}
	}
	c.noteEarning(a, EarnCheckin, "", res.Credit)
	return nil
}

// IsAlreadyCheckin 报告 err 是否表示"今天已签到"（上游幂等拒绝重复签到）。
// 只认带分类的 *Error（业务 code 或 HTTP 错误）：网络层/解析层错误不得当作幂等成功，
// 否则停机补签遇到抖动会误记为 already，账号当天实际未签到却被判定正常。
func IsAlreadyCheckin(err error) bool {
	var ue *Error
	if !errors.As(err, &ue) {
		return false
	}
	for _, m := range alreadyCheckinMarkers {
		if strings.Contains(ue.Msg, m) || strings.Contains(strings.ToLower(ue.Msg), strings.ToLower(m)) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	return logfmt.Truncate(s, n)
}
