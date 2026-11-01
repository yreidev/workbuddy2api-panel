// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// logging.go 请求级表格日志：每个 /v1/chat/completions 请求结束后打印一行到 stdout。
package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// maxUserAgentLen 归档与面板展示保留的 UA 字节上限。UA 是客户端完全可控的
// 自由文本（浏览器动辄 150+ 字符，恶意客户端可以塞几 KB），落盘前必须截断，
// 否则一条请求就能把归档行撑大。截断只影响展示，不影响请求处理。
const maxUserAgentLen = 200

// maxFailureReasonRunes 请求记录里失败原因保留的字数；maxErrorBodyCapture 为提取原因
// 最多留下的失败响应正文字节数（只看开头，错误 JSON 的 message 一般就在前面）。
const (
	maxFailureReasonRunes = 200
	maxErrorBodyCapture   = 4 << 10
)

// chatSeq 进程级请求序号。
var chatSeq atomic.Int64

// chatLogEnabled 聊天表格日志总开关。生产恒 true；
// 测试包经 TestMain 置 false 关闭 stdout 噪音，需要断言行输出的测试用 withChatLog 临时开启（R5）。
var chatLogEnabled = true

// chatLogOut 聊天表格日志的输出目标，生产就是 os.Stdout。面板的运行日志不收这些行：
// 同样的信息在「请求记录」里，混进日志环还会把任务和系统日志挤掉。
// 测试经 SetChatLogOutput 换成缓冲区，需在开始服务前调用（无并发竞争窗口）。
var chatLogOut io.Writer = os.Stdout

// SetChatLogOutput 替换聊天表格日志输出目标（测试用）。
func SetChatLogOutput(w io.Writer) { chatLogOut = w }

// chatStat 单个 chat 请求的日志统计；handler 挂 defer，请求出口后落一行。
type chatStat struct {
	start            time.Time
	model            string
	mode             string // "stream" | "sync"
	uid              string // 完整 uid，展示时只取前 8 位
	nick             string // 账号昵称（随选号同步），流水行经 logfmt.Label 拼成 "昵称(uid8)"
	proxy            string // 号走的代理名（只进请求记录，不进流水行：没起名的代理名字就是 IP）
	ttfb             time.Duration
	toks             int // <0 表示 usage 缺失 → 显示 "-"
	status           int
	requestID        string
	outcome          string
	attempts         int
	credit           float64
	hasCredit        bool
	promptTokens     int64
	completionTokens int64
	totalTokens      int64
	// 缓存命中观测（usage.prompt_cache_hit_tokens / miss）：上游给到才有效。
	// 供 usage 桶命中率维度与 reqlog 逐次记录；缺失时 hasCache=false 不参与统计。
	cacheHit  int64
	cacheMiss int64
	hasCache  bool

	// 调用来源（客户端 IP / User-Agent）。空 = 未采集（logging.request_client_info
	// 关闭，或非 chat 路径），展示层一律以 "-" 兜底。
	clientIP  string
	userAgent string

	// errMsg 流已开始后才出错时的原因（HTTP 头已发出、状态码是 200，响应观察器截不到
	// 失败正文），或请求记录里要写得比回给客户端的更具体的原因（号的代理连不上：客户端不该看到代理名）；
	// 其余失败的原因从回给客户端的错误响应里取，见 failureReason。
	errMsg string

	logged bool
}

// newChatStat 以请求进入 handler 的时刻为起点构造统计对象；toks 默认 -1（usage 缺失）。
func newChatStat(now time.Time, body []byte, stream bool) *chatStat {
	mode := "sync"
	if stream {
		mode = "stream"
	}
	return &chatStat{start: now, model: parseModelFromBody(body), mode: mode, toks: -1}
}

// done 幂等落一行表格日志。
func (s *chatStat) done() {
	if s.logged {
		return
	}
	s.logged = true
	logChatRowEx(s.ttfb, time.Since(s.start), s.model, s.mode, s.uid, s.nick, s.status, s.toks,
		s.requestID, s.outcome, s.attempts, s.credit, s.hasCredit, s.clientIP, s.userAgent)
}

// chatStatsReader 在流式透传时抓取 SSE 末帧的 usage.completion_tokens 精确值，
// 并记录首个 data 帧的 TTFB；原始字节原样返回给下游透传。
// 注意：不做 rune 估算，token 数一律采信上游 usage。
type chatStatsReader struct {
	br                  *bufio.Reader
	start               time.Time
	ttfb                time.Duration
	seen                bool // 已见过首个 data 帧（TTFB 只记一次）
	promptTokens        int
	completionTokens    int
	totalTokens         int
	hasPromptTokens     bool
	hasCompletionTokens bool
	hasTotalTokens      bool
	// credit 上游末帧 usage.credit（本次真实扣费积分），供成本台账（NoteModelCost）。
	hasCredit  bool
	credit     float64
	errorFrame bool
	// cacheHit/cacheMiss 上游末帧 usage 里的前缀缓存命中 / 未命中（口径同非流式，见
	// upstream.UsageCacheTokens），供用量桶的命中率维度与 reqlog 逐次记录（issue #92）。
	hasCache  bool
	cacheHit  int64
	cacheMiss int64
	pend      []byte // 已读未返回的行缓存
}

// newChatStatsReaderSince 以 since 为 TTFB 计时起点（通常是请求进入 handler 的时刻）。
func newChatStatsReaderSince(r io.Reader, since time.Time) *chatStatsReader {
	return &chatStatsReader{br: bufio.NewReaderSize(r, 64*1024), start: since}
}

// TTFB 返回首个 data 帧到达耗时；无帧时为 0。
func (s *chatStatsReader) TTFB() time.Duration { return s.ttfb }

// Tokens 返回末帧 usage.completion_tokens 与是否缺失；无 usage 时 ok=false。
func (s *chatStatsReader) Tokens() (int, bool) { return s.completionTokens, s.hasCompletionTokens }

// Credit 返回末帧 usage.credit（本次真实扣费积分）与是否缺失。
func (s *chatStatsReader) Credit() (float64, bool) { return s.credit, s.hasCredit }

// TotalTokens 返回末帧 usage.total_tokens 与是否缺失。
func (s *chatStatsReader) TotalTokens() (int, bool) { return s.totalTokens, s.hasTotalTokens }

// Usage 返回流式响应中已收到的 token usage 字段。
func (s *chatStatsReader) Usage() pool.TokenUsageDelta {
	return pool.TokenUsageDelta{
		HasPromptTokens:     s.hasPromptTokens,
		PromptTokens:        int64(s.promptTokens),
		HasCompletionTokens: s.hasCompletionTokens,
		CompletionTokens:    int64(s.completionTokens),
		HasTotalTokens:      s.hasTotalTokens,
		TotalTokens:         int64(s.totalTokens),
	}
}

// parseSSELine 解析一行 "data: {...}"：首帧记 TTFB，含 usage 时采信精确 completion_tokens。
func (s *chatStatsReader) parseSSELine(line string) {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "data: ") {
		return
	}
	payload := strings.TrimPrefix(line, "data: ")
	if payload == "[DONE]" {
		return
	}
	if !s.seen {
		s.seen = true
		s.ttfb = time.Since(s.start)
	}
	var chunk struct {
		Error json.RawMessage `json:"error"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal([]byte(payload), &chunk) != nil {
		return
	}
	if len(chunk.Error) > 0 {
		s.errorFrame = true
	}
	if len(chunk.Usage) == 0 || string(chunk.Usage) == "null" {
		return
	}
	var usage struct {
		PromptTokens     *int     `json:"prompt_tokens"`
		CompletionTokens *int     `json:"completion_tokens"`
		TotalTokens      *int     `json:"total_tokens"`
		Credit           *float64 `json:"credit"`
	}
	if json.Unmarshal(chunk.Usage, &usage) != nil {
		return
	}
	if usage.PromptTokens != nil {
		s.hasPromptTokens = true
		s.promptTokens = *usage.PromptTokens
	}
	if usage.CompletionTokens != nil {
		s.hasCompletionTokens = true
		s.completionTokens = *usage.CompletionTokens
	}
	if usage.TotalTokens != nil {
		s.hasTotalTokens = true
		s.totalTokens = *usage.TotalTokens
	}
	if usage.Credit != nil {
		s.hasCredit = true
		s.credit = *usage.Credit
	}
	// 缓存字段各家写法不一（prompt_cache_hit_tokens、prompt_tokens_details.cached_tokens 等），
	// 和非流式共用一套别名口径。
	var m map[string]any
	if json.Unmarshal(chunk.Usage, &m) == nil {
		if hit, miss, ok := upstream.UsageCacheTokens(m); ok {
			s.hasCache, s.cacheHit, s.cacheMiss = true, int64(hit), int64(miss)
		}
	}
}

// CacheTokens 返回末帧 usage 的缓存命中 / 未命中 token 数；usage 里一个缓存字段都没有时
// ok=false（不参与命中率统计）。
func (s *chatStatsReader) CacheTokens() (hit, miss int64, ok bool) {
	return s.cacheHit, s.cacheMiss, s.hasCache
}

// SawErrorFrame 报告流中是否透传过 SSE error 帧。
func (s *chatStatsReader) SawErrorFrame() bool { return s.errorFrame }

// Read 返回原始数据，同时解析统计 TTFB/token。
func (s *chatStatsReader) Read(p []byte) (int, error) {
	if len(s.pend) > 0 {
		n := copy(p, s.pend)
		s.pend = s.pend[n:]
		return n, nil
	}
	line, err := s.br.ReadString('\n')
	if line != "" {
		s.parseSSELine(line)
		s.pend = []byte(line)
		n := copy(p, s.pend)
		s.pend = s.pend[n:]
		return n, nil
	}
	return 0, err
}

// rewriteModel 把 outbound chat body 的 model 字段替换为 bare（保留其余字段原样）。
// 仅当 bare != 原 model 时由 chatCompletions 调用；body 不可解析时原样返回（不二次错误化）。
func rewriteModel(body []byte, bare string) []byte {
	if len(body) == 0 || bare == "" {
		return body
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	if cur, ok := obj["model"].(string); !ok || cur == bare {
		return body
	}
	obj["model"] = bare
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// parseModelFromBody 从请求 JSON 取 model 字段，缺省标 "-"。
func parseModelFromBody(body []byte) string {
	var obj struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &obj); err != nil || obj.Model == "" {
		return "-"
	}
	return obj.Model
}

// usageDeltaFromResponse 从非流式聚合响应中提取明确存在的 token 字段。
func usageDeltaFromResponse(resp map[string]any) pool.TokenUsageDelta {
	delta := pool.TokenUsageDelta{}
	u, ok := resp["usage"].(map[string]any)
	if !ok {
		return delta
	}
	read := func(key string) (int64, bool) {
		v, ok := u[key]
		if !ok {
			return 0, false
		}
		switch n := v.(type) {
		case float64:
			return int64(n), true
		case float32:
			return int64(n), true
		case int:
			return int64(n), true
		case int64:
			return n, true
		case json.Number:
			i, err := n.Int64()
			return i, err == nil
		default:
			return 0, false
		}
	}
	if n, ok := read("prompt_tokens"); ok {
		delta.HasPromptTokens, delta.PromptTokens = true, n
	}
	if n, ok := read("completion_tokens"); ok {
		delta.HasCompletionTokens, delta.CompletionTokens = true, n
	}
	if n, ok := read("total_tokens"); ok {
		delta.HasTotalTokens, delta.TotalTokens = true, n
	}
	return delta
}

// completionTokens 从 Aggregate 返回的响应中提取 usage.completion_tokens；缺失返回 -1。
func completionTokens(resp map[string]any) int {
	u, ok := resp["usage"].(map[string]any)
	if !ok {
		return -1
	}
	v, ok := u["completion_tokens"].(float64)
	if !ok {
		return -1
	}
	return int(v)
}

// uidPrefix 只显示 uid 前 8 位；空 uid 显示 "-"。
//
// 实现委托 logfmt.UID8，避免 "截 8 位" 的规则在 server 与 logfmt 两处各写一份而走样。
func uidPrefix(uid string) string {
	return logfmt.UID8(uid)
}

type requestTraceKey struct{}

// requestTrace 在一次 chat 请求内共享标识与最终统计，ServeHTTP 出口统一记账。
type requestTrace struct {
	id    string
	start time.Time
	path  string // 入口路径（聊天、Claude Code、Codex 三个入口和联网搜索共用这套记账）
	stat  *chatStat
	// 调用来源，进入 handler 时一次性采集（见 ServeHTTP / captureClientInfo）。
	clientIP  string
	userAgent string
	apiKey    string // 认出的密钥名（见 principal.label）
}

// captureClientInfo 采集调用来源（客户端 IP + 截断后的 UA）。开关关闭时保持空串：
// 来源信息比 token 计数敏感，是否落盘由 logging.request_client_info 决定。
func (t *requestTrace) captureClientInfo(r *http.Request) {
	if t == nil || r == nil {
		return
	}
	t.clientIP = clientIPForLog(r)
	t.userAgent = logfmt.Truncate(r.UserAgent(), maxUserAgentLen)
}

// clientIPForLog 提取用于日志展示的客户端 IP。
//
// 与 upstream.ExtractClientIP 的差别：后者只认代理头（X-Forwarded-For 首段 →
// X-Real-IP），因为它的用途是把客户端 IP **透传给上游**，回落到网关自身地址会
// 污染上游风控判据；日志场景相反——直连（无反代）时 RemoteAddr 就是唯一线索，
// 必须回落，否则面板里所有来源都显示 "-"。代理头优先保证反代后拿到真实客户端。
func clientIPForLog(r *http.Request) string {
	if r == nil {
		return ""
	}
	if ip := upstream.ExtractClientIP(r); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

// dashIfEmpty 空串统一显示 "-"（来源字段未采集时不留空白列）。
func dashIfEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func requestTraceFrom(r *http.Request) *requestTrace {
	if r == nil {
		return nil
	}
	tr, _ := r.Context().Value(requestTraceKey{}).(*requestTrace)
	return tr
}

// proxyName 号走的代理名（请求记录用），没装代理池为空。
func (h *Handler) proxyName(uid string) string {
	if h.cfg.ProxyName == nil {
		return ""
	}
	return h.cfg.ProxyName(uid)
}

// event 组装一条请求记录。errBody 是失败响应正文的开头（见 responseObserver），用来提取失败原因。
func (t *requestTrace) event(status int, errBody []byte) reqlog.Event {
	e := reqlog.Event{
		Time:      t.start,
		RequestID: t.id,
		Path:      t.path,
		Status:    status,
	}
	if e.Path == "" {
		e.Path = "/v1/chat/completions"
	}
	duration := time.Since(t.start)
	e.DurationMs = duration.Milliseconds()
	if e.DurationMs < 1 {
		e.DurationMs = 1
	}
	if t.stat != nil {
		s := t.stat
		e.Account = logfmt.Label(s.uid, s.nick)
		e.Proxy = s.proxy
		e.Model = s.model
		e.Outcome = s.outcome
		e.TTFBMs = s.ttfb.Milliseconds()
		e.Attempts = s.attempts
		e.PromptTokens = s.promptTokens
		e.CompletionTokens = s.completionTokens
		e.TotalTokens = s.totalTokens
		e.Credit = s.credit
		e.HasCredit = s.hasCredit
		e.CacheHitTokens = s.cacheHit
		e.CacheMissTokens = s.cacheMiss
	}
	e.ClientIP = t.clientIP
	e.UserAgent = t.userAgent
	e.APIKey = t.apiKey
	if e.Outcome == "" {
		if status >= 200 && status < 300 {
			e.Outcome = reqlog.OutcomeSuccess
		} else {
			e.Outcome = reqlog.OutcomeHTTPError
		}
	}
	e.OK = status >= 200 && status < 300 && e.Outcome == reqlog.OutcomeSuccess
	if !e.OK {
		e.Error = failureReason(t.stat, errBody)
	}
	return e
}

// failureReason 失败原因：流中途出错用 chatStat 记下的原因，其余取回给客户端的错误消息。
// 合并空白后截到 maxFailureReasonRunes 字；客户端中途断开这类没有错误响应的返回空串。
func failureReason(st *chatStat, errBody []byte) string {
	reason := ""
	if st != nil && st.errMsg != "" {
		reason = errorMessage(st.errMsg)
	} else if len(errBody) > 0 {
		reason = errorMessage(string(errBody))
	}
	reason = strings.Join(strings.Fields(reason), " ")
	if r := []rune(reason); len(r) > maxFailureReasonRunes {
		reason = string(r[:maxFailureReasonRunes]) + "…"
	}
	return reason
}

// errorMessage 从错误 JSON 里取消息：聊天、Codex 与 Claude Code 入口是 {"error":{"message"}}，
// Responses 流式失败是 {"response":{"error":{"message"}}}，上游原文常见 {"msg"} / {"message"}。
// 不是 JSON 或没有这些字段时原样返回。
func errorMessage(body string) string {
	var v map[string]any
	if json.Unmarshal([]byte(body), &v) != nil {
		return body
	}
	if r, ok := v["response"].(map[string]any); ok {
		v = r
	}
	switch e := v["error"].(type) {
	case map[string]any:
		if m, ok := e["message"].(string); ok && m != "" {
			return m
		}
	case string:
		if e != "" {
			return e
		}
	}
	for _, k := range []string{"message", "msg"} {
		if m, ok := v[k].(string); ok && m != "" {
			return m
		}
	}
	return body
}

// responseObserver 捕获 handler 实际写出的 HTTP 状态，同时保留 Flusher/Unwrap，
// 避免破坏 SSE 逐帧刷新。失败响应（状态 ≥ 400）顺带留下正文开头，供请求记录提取失败原因。
type responseObserver struct {
	http.ResponseWriter
	status  int
	errBody []byte
}

func (o *responseObserver) WriteHeader(code int) {
	if o.status == 0 {
		o.status = code
	}
	o.ResponseWriter.WriteHeader(code)
}

func (o *responseObserver) Write(p []byte) (int, error) {
	if o.status == 0 {
		o.status = http.StatusOK
	}
	if o.status >= http.StatusBadRequest && len(o.errBody) < maxErrorBodyCapture {
		o.errBody = append(o.errBody, p[:min(len(p), maxErrorBodyCapture-len(o.errBody))]...)
	}
	return o.ResponseWriter.Write(p)
}

func (o *responseObserver) Flush() {
	if f, ok := o.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (o *responseObserver) Unwrap() http.ResponseWriter { return o.ResponseWriter }

// 请求流水行的固定列宽（显示列宽，非字节）。取固定宽度而不是让内容自然长度撑开，
// 是为了让 stdout 里成百上千行能竖着扫——否则模型名长短不一、中文昵称按字节补空格
// 错位，根本没法用肉眼对齐着一列列看（这正是上一版 11 字节硬截断要解决的问题）。
const (
	// chatModelWidth 覆盖 realm 前缀 + 最长模型名："global:" (7) + "deepseek-v4.1-flash" (19) = 26。
	// 旧的 11 字节截断会把 "cn:deepseek-v4-flash" 切成 "cn:deepseek"，让人误以为是另一个模型。
	chatModelWidth = 26
	// chatAcctWidth 容纳 "昵称(uid8)"：中文昵称按 2 列/字算，5 字中文 + "(xxxxxxxx)" = 20 列。
	chatAcctWidth = 22
	chatTTFBWidth = 8
	chatTokWidth  = 6
	chatRateWidth = 11 // 形如 "183.6tok/s"
)

// logChatRow 打印一行请求级表格日志（输出 chatLogOut，无 log 时间戳前缀）。
//
// 参数：
//   - model：模型名（含 realm 前缀），超 chatModelWidth 截断（模型名是 ASCII，字节截即列宽）；
//   - uid/nick：完整 uid 与账号昵称，经 logfmt.Label 拼成 "昵称(uid8)" 展示——只有
//     uid8 时人眼无法判断是哪个号，要辨认必须再查 auths/，排障多一跳；
//   - toks<0 表示 usage 缺失，显示 "-"。
func logChatRow(ttfb, total time.Duration, model, mode, uid, nick string, status int, toks int) {
	logChatRowEx(ttfb, total, model, mode, uid, nick, status, toks, "", "", 0, 0, false, "", "")
}

// logChatRowEx 是带请求 ID、结果、重试、积分与调用来源字段的扩展流水行。旧调用保持
// 原格式；requestID 非空时才追加扩展字段；来源两参均为空时不追加来源段。
func logChatRowEx(ttfb, total time.Duration, model, mode, uid, nick string, status int, toks int,
	requestID, outcome string, attempts int, credit float64, hasCredit bool,
	clientIP, userAgent string) {
	if !chatLogEnabled {
		return
	}
	seq := chatSeq.Add(1)
	model = logfmt.Pad(logfmt.Truncate(model, chatModelWidth), chatModelWidth)
	// 账号标签只补不截：超宽时宁可让该行变宽，也不丢昵称信息（昵称是排查的主线索）。
	acct := logfmt.Pad(logfmt.Label(uid, nick), chatAcctWidth)
	tokField := "-"
	tokpsField := "-"
	if toks >= 0 {
		tokField = fmt.Sprintf("%d", toks)
		// 速率与用量账本走同一个函数（扣掉 TTFB）。此前这里自己除 total，漏扣首
		// token 等待，于是控制台流水行的 tok/s 与面板数字对不上（issue #34）——
		// 本函数本来就收到了 ttfb，只是没拿它算速率。
		if tps, ok := tokensPerSecond(int64(toks), total, ttfb); ok {
			tokpsField = fmt.Sprintf("%.1ftok/s", tps)
		} else {
			tokpsField = "0.0tok/s"
		}
	}
	ttfbMS := "-"
	if ttfb > 0 {
		ttfbMS = fmt.Sprintf("%dms", ttfb.Milliseconds())
	}
	extra := ""
	if requestID != "" {
		if outcome == "" {
			outcome = reqlog.OutcomeHTTPError
			if status >= 200 && status < 300 {
				outcome = reqlog.OutcomeSuccess
			}
		}
		creditField := "-"
		if hasCredit {
			creditField = fmt.Sprintf("%.4f", credit)
		}
		extra = fmt.Sprintf(" rid=%s | out=%s | try=%d | credit=%s |", requestID, outcome, attempts, creditField)
	}
	// 调用来源：IP 用可解析的裸值（便于 grep），UA 用 ShortUA 压缩后的客户端标签
	// 并加引号（标签内可能含空格，如 `OpenAI/Python 1.30.0` 只会取到 OpenAI/Python）。
	// 两者都未采集时不追加，旧行格式保持不变。
	src := ""
	if clientIP != "" || userAgent != "" {
		ua := "-"
		if s := logfmt.ShortUA(userAgent); s != "" {
			ua = `"` + s + `"`
		}
		src = fmt.Sprintf(" src=%s ua=%s |", dashIfEmpty(clientIP), ua)
	}
	fmt.Fprintf(chatLogOut, "| #%03d | %s | %s | %s | %d | %s | TTFB=%s | tok=%s | %s | total=%.1fs |%s%s\n",
		seq,
		time.Now().Format("15:04:05"),
		model,
		mode,
		status,
		acct,
		logfmt.Pad(ttfbMS, chatTTFBWidth),
		logfmt.Pad(tokField, chatTokWidth),
		logfmt.Pad(tokpsField, chatRateWidth),
		total.Seconds(),
		extra,
		src,
	)
}

// cacheMissSignal 前缀缓存整段未命中的提示（issue #92 起；issue #2 改成按对话判断）。
//
// 上游的前缀缓存有的模型按号存（deepseek），有的按会话 id 存、换号也接得上（glm-5.3-flash），
// 见 docs/official-client.md：新对话的头一轮、换了号、换了会话 id 都可能整段未命中，这些是预期内的。以前按模型笼统判断
// 「此前命中过」，把它们都报成了异常（issue #2）。现在按对话（会话键）记下上一轮的模型、号、
// 会话 id 和消息条数；这段对话命中过、这一轮大前缀（≥1000 token）却一点没命中时，对照上一轮说原因：
//   - 换了模型 / 换了号 / 会话 id 变了 / 消息比上一轮少（客户端压缩或改写了历史）：预期内，记 INFO；
//   - 都没变：同一个号、同一个会话 id，上游没留住缓存，记 WARN，带上距上一轮多久。
//
// 每段对话 10 分钟内最多记一行。没有会话键的请求认不出是哪段对话，不判断。
type cacheMissSignal struct {
	mu    sync.Mutex
	convs map[string]convCacheState
}

// convCacheState 一段对话上一轮的缓存观测。
type convCacheState struct {
	model, uid, acct, convID string
	msgs                     int
	at                       time.Time
	hit                      bool      // 这段对话命中过
	logged                   time.Time // 上次记日志的时刻
}

// cacheObservation 一跳成功请求的前缀缓存观测。key 是会话键，acct 是日志里的号（uid + 昵称）。
type cacheObservation struct {
	key, model, uid, acct, convID string
	msgs                          int
	prompt, hit, miss             int64
}

func newCacheMissSignal() *cacheMissSignal {
	return &cacheMissSignal{convs: map[string]convCacheState{}}
}

var cacheMissWarn = newCacheMissSignal()

var (
	// cacheMissWarnEvery 每段对话的冷却间隔；变量供测试缩短。
	cacheMissWarnEvery = 10 * time.Minute
	// cacheMissNow 时钟，供测试替换。
	cacheMissNow = time.Now
)

// cacheMissTrackMax 最多记多少段对话，超了先清一天没动的，再随便清掉一成。
const cacheMissTrackMax = 10000

// note 记下这一跳的观测，整段未命中时按上面的规则记一行日志。
func (c *cacheMissSignal) note(o cacheObservation) {
	if o.key == "" || o.model == "" {
		return
	}
	now := cacheMissNow()
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, seen := c.convs[o.key]
	if !seen && len(c.convs) >= cacheMissTrackMax {
		c.evictLocked(now)
	}
	cur := convCacheState{model: o.model, uid: o.uid, acct: o.acct, convID: o.convID, msgs: o.msgs, at: now,
		hit: prev.hit || o.hit > 0, logged: prev.logged}
	c.convs[o.key] = cur
	// 命中了、小前缀、头一次见到的对话、这段对话从没命中过（模型不缓存或还没缓存上）都不提示。
	if o.hit > 0 || o.prompt < 1000 || !seen || !prev.hit || now.Sub(prev.logged) < cacheMissWarnEvery {
		return
	}
	cur.logged = now
	c.convs[o.key] = cur
	miss := o.miss
	if miss <= 0 {
		miss = o.prompt
	}
	if reason := cacheMissReason(prev, o); reason != "" {
		log.Printf("INFO: [server] cache miss: model=%s acct=%s 这一轮大前缀未命中（prompt=%d miss=%d）：%s，属预期", o.model, o.acct, o.prompt, miss, reason)
		return
	}
	log.Printf("WARN: [server] cache miss: model=%s acct=%s 同一个号、同一个会话 id 这一轮大前缀仍未命中（prompt=%d miss=%d，距上一轮 %s）；上游没留住这段对话的前缀缓存，这一轮按未命中计费",
		o.model, o.acct, o.prompt, miss, gapText(now.Sub(prev.at)))
}

// cacheMissReason 对照上一轮说出预期内的未命中原因；都对得上返回 ""。
func cacheMissReason(prev convCacheState, o cacheObservation) string {
	switch {
	case prev.model != o.model:
		return "换了模型（上一轮是 " + prev.model + "）"
	case prev.uid != o.uid:
		return "换了号（上一轮在 " + prev.acct + "），部分模型按号缓存"
	case prev.convID != o.convID:
		return "会话 id 变了，部分模型按会话 id 缓存"
	case o.msgs < prev.msgs:
		return fmt.Sprintf("消息比上一轮少（%d → %d 条），客户端压缩或改写了历史", prev.msgs, o.msgs)
	}
	return ""
}

func (c *cacheMissSignal) evictLocked(now time.Time) {
	for k, s := range c.convs {
		if now.Sub(s.at) > 24*time.Hour {
			delete(c.convs, k)
		}
	}
	for k := range c.convs {
		if len(c.convs) < cacheMissTrackMax*9/10 {
			break
		}
		delete(c.convs, k)
	}
}

// gapText 两轮之间隔了多久，日志里看的粗略写法。
func gapText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	default:
		return fmt.Sprintf("%.1f 小时", d.Hours())
	}
}
