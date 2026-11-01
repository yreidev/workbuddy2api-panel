// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// Package server 暴露 OpenAI 兼容 HTTP 接口，内部驱动 pool 挑号 + upstream 转发。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"sync"
	"sync/atomic"
	"time"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"

	"github.com/yreidev/workbuddy2api-panel/internal/apikeys"
	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/httpauth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/prompt"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

// Config handler 依赖。
type Config struct {
	Pool      *pool.Pool
	Upstream  *upstream.Client
	APIKey    string         // 管理员密钥，空 = 不鉴权（静态值；与 Live 同时给出时 Live 优先）
	Keys      *apikeys.Store // 面板里建的客户端密钥（可选；nil = 只认管理员密钥），见 apikey.go
	MaxRotate int            // 单请求最多换号次数，默认 3
	// Session 会话粘性路由器（可选；nil = 关闭粘性，纯 Pick 轮换）。
	Session *session.Router
	// StickyCount 返回当前粘性会话绑定数（供 /status）；nil 时报告 0。
	StickyCount func() int
	// ProxyName 号走的代理的名字（代理池 Registry.ProxyName），记进请求记录；nil 或返回空 = 没绑代理。
	ProxyName func(uid string) string
	// RedisMode 观测字段（"upstash" / "noop"），供 /status 透出。
	RedisMode    string
	SoftCooldown time.Duration // 429/限流文案软冷却基数，默认 600s（连续触发指数退避，封顶 soft_rate_max）
	RefreshSkew  time.Duration // token 提前刷新窗口，默认 10m

	// Panel 管理面板 handler（可选；nil = 不挂载）。挂载在 /panel/ 前缀下，
	// 面板自带 Bearer 鉴权（同一 api_key）与内嵌静态资源，主路由只做转发。
	Panel http.Handler

	// Live 运行期可变配置（面板在线改 api_key / soft_rate / 脱敏开关时立即生效）。
	// nil 时回退静态字段（测试与裸用场景）。
	Live *livecfg.Holder

	// PromptMode "custom"（网关用自有提示词替换 system）/ "passthrough"（透传）。
	PromptMode string
	// PromptText custom 模式下注入的系统提示词文本（来自 config.PromptText）。
	PromptText string

	// GlobalEnabled global realm 路由开关（config global.enabled，缺省 true）。
	// handler 侧第三道闸（与 main 注入 auth 开关、upstream.GlobalEnabled 呼应）：
	// false（显式逃生门）时即便 auth realm=global 也不提供 global: 模型名
	// （modelList 不列 global 名单）。
	GlobalEnabled bool

	// Usage 逐请求用量记录器（可选；nil = 不记录）。
	// 在 recordAttempt 这一唯一汇聚点调用，因此流式/非流式、成功/失败都会计入，
	// 且与 pool 的每账号累计器同源，两条口径不会漂移。
	Usage *usage.Recorder

	// RequestLog 请求指标与脱敏 JSONL 归档（可选；nil = 不记录）。
	RequestLog *reqlog.Recorder

	// RecordClientInfo 是否在请求日志里记录调用来源（客户端 IP / User-Agent）。
	// 来自 logging.request_client_info（缺省 true）；关闭时 reqlog 事件的来源字段
	// 保持为空，归档与面板都不出现来源信息。
	RecordClientInfo bool

	// Version 网关版本，/mcp 在初始化回应的 serverInfo 里报给客户端。
	Version string
}

// loadLive 返回当前运行期快照；Live 为 nil 时用静态字段合成。
func (h *Handler) loadLive() livecfg.Snapshot {
	if h.cfg.Live != nil {
		return h.cfg.Live.Load()
	}
	return livecfg.Snapshot{
		APIKey:           h.cfg.APIKey,
		SoftCooldown:     h.cfg.SoftCooldown,
		RecordClientInfo: h.cfg.RecordClientInfo,
	}
}

// softCooldown 返回当前生效的软冷却基数（热改优先，<=0 回退默认）。
func (h *Handler) softCooldown() time.Duration {
	if d := h.loadLive().SoftCooldown; d > 0 {
		return d
	}
	if h.cfg.SoftCooldown > 0 {
		return h.cfg.SoftCooldown
	}
	return 600 * time.Second
}

// notFoundCooldown 上游 404 的固定短冷却时长。
// 与 SoftCooldown 分流的原因：404 是上游**偶发**路径缺失，不是"本账号在限流"，
// 若共用 soft_rate（600s 起 + 指数升级），一次偶发 404 会把好账号罚 10 分钟并逐次加倍。
// 故固定 60s 防雪崩即可，不随 soft_rate 配置、也不参与软退避指数。
const notFoundCooldown = 60 * time.Second

// transientRateCooldown 14003「模型繁忙」与每秒/每分钟限额只冷却 (账号, 模型) 这么久（再抖动）。
// 官方客户端遇到它们原样重发，退避从 0.5 秒起、封顶 32 秒；网关当场换号，这个号的这个模型
// 歇半分钟再用。以前落进通用 429，整个号冷却约 10 分钟，号少的域（国际版）会整域停摆。
const transientRateCooldown = 30 * time.Second

// modelBusyRetryWindow 所有号都被同一模型的冷却挡住、但最早解封就在这个时间内时，回 429 +
// Retry-After 让客户端稍等重试，不回 400「换个模型」（见 chat 末端的模型级阻塞分支）。
const modelBusyRetryWindow = 2 * time.Minute

// activeSessionCooldown 10105（这个号同时进行的会话太多）的账号级冷却（再抖动），等它手上的
// 会话结束。
const activeSessionCooldown = 60 * time.Second

// ServiceName 网关身份标识。经 /healthz 响应体 service 字段与 X-Service 头同时透出：
// 宿主（如 workbuddy-switch 托管网关子进程）探测同端口的旧服务/其他服务时，对方即使
// 返回 2xx 也不带本标识，宿主据此可识别"假成功"。
const ServiceName = "workbuddy2api"

// Handler 主路由。
type Handler struct {
	cfg     Config
	mux     *http.ServeMux
	degrade degradeGate
	// wafIP WAF IP 级拦截状态机（fail-fast，wafip.go）：短窗多号 WAF 403 →
	// 激活期轮转遇 WAF 403 直接终止（不放大请求量）。进程内状态、重启清零。
	wafIP wafIPGate
	// turns 客户端没给会话 id 时的对话轮 ID 分配（session/turns.go）。进程内状态、重启清零。
	turns *session.TurnIDs
}

// NewHandler 构建 handler。
func NewHandler(cfg Config) *Handler {
	if cfg.MaxRotate <= 0 {
		cfg.MaxRotate = 3
	}
	if cfg.SoftCooldown <= 0 {
		cfg.SoftCooldown = 600 * time.Second // 软限流基数（连续触发按指数退避放大）
	}
	if cfg.RefreshSkew <= 0 {
		cfg.RefreshSkew = 10 * time.Minute
	}
	if cfg.PromptMode == "" {
		cfg.PromptMode = "custom" // 缺省 custom：网关自有提示词
	}
	h := &Handler{cfg: cfg, mux: http.NewServeMux(), turns: session.NewTurnIDs()}
	h.mux.HandleFunc("POST /v1/chat/completions", h.withKeyAuth(sdktranslator.FormatOpenAI, false, h.chatCompletions))
	h.registerBridgeRoutes()
	h.mux.HandleFunc("GET /v1/models", h.withKeyAuth(sdktranslator.FormatOpenAI, false, h.models))
	h.mux.HandleFunc("GET /status", h.withAuth(h.status))
	h.mux.HandleFunc("GET /healthz", h.healthz)
	h.mux.HandleFunc("GET /readyz", h.readyz)
	if cfg.Panel != nil {
		h.mux.Handle("/panel/", cfg.Panel) // /panel → /panel/ 由 ServeMux 自动重定向
	}
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.cfg.RequestLog != nil && r.Method == http.MethodPost && tracedPaths[r.URL.Path] {
		trace := &requestTrace{id: reqlog.NewRequestID(), start: time.Now(), path: r.URL.Path}
		if h.loadLive().RecordClientInfo {
			trace.captureClientInfo(r)
		}
		r = r.WithContext(context.WithValue(r.Context(), requestTraceKey{}, trace))
		obs := &responseObserver{ResponseWriter: w}
		w.Header().Set("X-Request-Id", trace.id)
		h.cfg.RequestLog.Begin()
		defer func() {
			status := obs.status
			if status == 0 {
				status = http.StatusOK
			}
			h.cfg.RequestLog.Record(trace.event(status, obs.errBody))
		}()
		h.mux.ServeHTTP(obs, r)
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !httpauth.VerifyBearer(r, h.loadLive().APIKey) {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "missing or invalid API key")
			return
		}
		next(w, r)
	}
}

// draining 进程收到停止信号后置位（main 调 SetDraining），此后 /readyz 恒返回 503。
// 全局而非挂在 Handler 上：停机是进程级状态，信号处理处拿不到 Handler 实例也能标记。
var draining atomic.Bool

// SetDraining 标记开始停机：/readyz 立刻转 503，让编排系统（k3s 就绪探针）把本实例摘出流量；
// 已建立的连接与在途请求（含流式对话）照常处理完，由 http.Server.Shutdown 负责等待。
func SetDraining() { draining.Store(true) }

// readyz 就绪探针：平时 200，停机开始后 503。
// 与 /healthz 分开：/healthz 在账号全忙或没账号时本来就返回 503（表示暂不可服务），
// 拿它做就绪判断会把「号忙」误当成「实例要下线」；/readyz 只反映进程是否在停机。恒无鉴权。
func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Service", ServiceName)
	if draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false, "draining": true, "service": ServiceName})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": true, "service": ServiceName})
}

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	total, healthy, _, _, _ := h.cfg.Pool.CountsDetailed()
	// 用 ServableNow 判定：healthy>0 但全占满在途时 chat 会 503，探活必须同口径，
	// 否则负载均衡器会把流量持续打进无法受理的实例。
	status := http.StatusOK
	if !h.cfg.Pool.ServableNow() {
		status = http.StatusServiceUnavailable
	}
	// realm_servable 域可服务维度：不改判活语义（存在性探活保持不变），
	// 只新增 CN/global 各自可达性供双域部署运维观察（任一域不可用单独告警）。
	realmServable := map[string]bool{
		"cn":     h.cfg.Pool.ServableForRealm("cn"),
		"global": h.cfg.Pool.ServableForRealm("global"),
	}
	// 恒无鉴权（负载均衡/编排探活只需 2xx/503 语义），身份靠 service 字段 + X-Service 头双保险。
	w.Header().Set("X-Service", ServiceName)
	writeJSON(w, status, map[string]any{
		"healthy":        healthy,
		"total":          total,
		"service":        ServiceName,
		"realm_servable": realmServable,
	})
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	total, healthy, cooling, disabled, inFlightFull := h.cfg.Pool.CountsDetailed()
	sticky := 0
	if h.cfg.StickyCount != nil {
		sticky = h.cfg.StickyCount()
	}
	redisMode := h.cfg.RedisMode
	if redisMode == "" {
		redisMode = "noop"
	}
	// cost_explore 探索台账（issue #136 §5 可观测性）：累计探索事件数 + 各
	// (域, 模型) 的最近探索时刻（键 "realm|model"）。与 accounts[].model_costs
	// 行对照即可读出「探索→毕业」全链路（单一事实来源，不做双表示）。零回归只增键。
	exploreEvents, exploreLast := h.cfg.Pool.CostExploreStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts":       h.cfg.Pool.List(),
		"total":          total,
		"healthy":        healthy,
		"cooling":        cooling,
		"disabled":       disabled,
		"in_flight_full": inFlightFull,
		// realm_totals 按域分组的计数汇总（双 realm 并存时运维一眼看到各域可用性）：
		// 只新增字段，既有 total/healthy/cooling/disabled/in_flight_full 汇总键不变（零回归）。
		"realm_totals": map[string]map[string]int{
			"cn":     countsMapFrom(h.cfg.Pool.CountsDetailedForRealm("cn")),
			"global": countsMapFrom(h.cfg.Pool.CountsDetailedForRealm("global")),
		},
		"sticky_sessions": sticky,
		"redis_mode":      redisMode,
		// model_locks 当前有未过期模型级限流的 (域, 模型) 全清单：账号池视图回答
		// 「哪些号不能用」，本键回答「哪些模型不能用、锁了几个号、还要锁多久」。
		// 与 ModelBlocked（请求失败时的单模型判定）互补；无锁时为 null。零回归只增键。
		"model_locks": h.cfg.Pool.ModelLockView(),
		// credit_floor 生效的积分保底值（0 = 关闭）。与 accounts[].credits +
		// model_costs 对照即可判定「某号为何对某模型不出票」。零值也显式写出
		// （运维口径：缺失会让人误以为没记录）。
		"credit_floor": h.cfg.Pool.CreditFloor(),
		// cost_explore 事件与 per-model 时间戳（时间值由 encoding/json 写 RFC3339）。
		"cost_explore": map[string]any{
			"events_total": exploreEvents,
			"per_model":    exploreLast,
		},
	})
}

// countsMapFrom 把 CountsDetailed 五元组打包成 /status 的域分组建模。
func countsMapFrom(total, healthy, cooling, disabled, inFlightFull int) map[string]int {
	return map[string]int{
		"total":          total,
		"healthy":        healthy,
		"cooling":        cooling,
		"disabled":       disabled,
		"in_flight_full": inFlightFull,
	}
}

// dynamicModelsCache 动态模型缓存。
var dynamicModelsCache struct {
	sync.RWMutex
	ids      []upstream.ModelInfo
	fetched  time.Time // 最近一次成功拉取时间
	lastFail time.Time // 最近一次拉取失败时间（负缓存）
}

const (
	// dynamicModelsTTL 模型目录缓存时长。曾是 1h；缩到 10min 对齐「面板实时、
	// API 缓存」的漂移痛点（PR #38 报告）：目录新增模型时面板立即可见，公开
	// /v1/models 最多滞后一个 TTL。再短就不值得——每次失效都是 2 次上游探测。
	dynamicModelsTTL        = 10 * time.Minute
	modelsFetchFailCooldown = 5 * time.Minute
)

// models 返回模型列表：纯动态（缓存 10min），失败/无号返回空列表（无静态兜底——
// 拉不出目录即意味着上游不可用，假名单只会让客户端选到 11102 的模型）。
func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	data := h.modelList()
	// 客户端密钥只列白名单里的模型。id 带 cn: / global: 前缀，按裸名判。
	if p := principalFrom(r.Context()); p.key != nil {
		allowed := make([]map[string]any, 0, len(data))
		for _, m := range data {
			id, _ := m["id"].(string)
			if _, bare := resolveModel(id); p.key.AllowsModel(bare) {
				allowed = append(allowed, m)
			}
		}
		data = allowed
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

// fmtCreditsPrefix 从上游 credits 原文提取倍率并格式化为 "[x0.05 credit]"。
// 上游格式不统一："x0.05 credits" / "x0.29" / "x0.00 credits" 等，
// 统一提取 x数字 部分，去 "credits" 后缀。
func fmtCreditsPrefix(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "credits")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return "[" + s + " credit]"
}

// applyModelInfoFields 把上游模型对象全字段（ModelInfo）按「空值省略」写出规则
// 合入 /v1/models 条目：name/description/credits/tags/vendor/能力旗标/
// max_allowed_size/reasoning_effort/reasoning_summary。CN 动态分支与 global
// 探测命中分支共用（两域模型对象同构），保证输出字段集一致。
// 不覆盖 id/object/created/owned_by 及调用方先前写好的基础字段；上游未下发的
// 字段（零值）整体省略——不编造。
func applyModelInfoFields(entry map[string]any, mi upstream.ModelInfo) map[string]any {
	if mi.Name != "" {
		entry["name"] = mi.Name
	}
	if mi.Description != "" {
		// 积分倍率前缀：从 "x0.05 credits" / "x0.29" 等格式提取纯数字，
		// 统一为 "[x0.05 credit]" 前缀拼入 description，方便下游面板直接展示。
		if mi.Credits != "" {
			entry["description"] = fmtCreditsPrefix(mi.Credits) + " " + mi.Description
		} else {
			entry["description"] = mi.Description // descriptionZh 中文描述
		}
	}
	if mi.Credits != "" {
		entry["credits"] = mi.Credits // 积分倍率原文（如 "x0.05"），仅展示
	}
	if len(mi.Tags) > 0 {
		entry["tags"] = mi.Tags
	}
	if mi.Vendor != "" {
		entry["vendor"] = mi.Vendor
	}
	if mi.IsDefault {
		entry["is_default"] = true
	}
	if mi.SupportsImages {
		entry["supports_images"] = true // 多模态能力透出
	}
	if mi.SupportsReasoning {
		entry["supports_reasoning"] = true
		if mi.CanDisableThinking {
			entry["can_disable_thinking"] = true
		}
	}
	if mi.SupportsToolCall {
		entry["supports_tool_call"] = true
	}
	if mi.OnlyReasoning {
		entry["only_reasoning"] = true
	}
	if mi.MaxAllowedSize > 0 {
		entry["max_allowed_size"] = mi.MaxAllowedSize
	}
	if mi.ReasoningEffort != "" {
		entry["reasoning_effort"] = mi.ReasoningEffort
	}
	if mi.ReasoningSummary != "" {
		entry["reasoning_summary"] = mi.ReasoningSummary
	}
	return entry
}

// modelList 模型列表：CN 模型输出统一加 "cn:" 前缀（gateway 路由协议，与 resolveModel
// 对称）；global.enabled=true 时追加 global: 前缀的国际版名单。
// 纯动态：动态拉取失败/无号 → 该域空列表，无静态兜底。
func (h *Handler) modelList() []map[string]any {
	out := make([]map[string]any, 0)
	hidden := h.loadLive().HiddenModels // models.hidden：被藏起来的不列
	cnEntry := func(mi upstream.ModelInfo) map[string]any {
		entry := map[string]any{
			"id":       "cn:" + mi.ID,
			"object":   "model",
			"created":  1753600000,
			"owned_by": "workbuddy",
		}
		// context_length / max_output_tokens 四级查找（upstream.model_catalog）：
		// 上游动态值（maxInputTokens/maxOutputTokens）权威 → 静态种子表 →
		// model.json 本地缓存 → models.dev 按需拉取（异步不阻塞本次响应，拉到后
		// 写 model.json 供下次命中）→ 1M 兜底 / max_output_tokens 省略。
		// 上游零值不再透出假 131072（误导 Codex/ZCode 等按 context_length 提前
		// 截断、白白丢上下文）。
		entry["context_length"] = upstream.ContextWindowListingV4(mi.ID, mi.ContextWindow, h.cfg.Upstream.HTTP)
		if mo, ok := upstream.MaxOutputTokensListingV4(mi.ID, mi.MaxTokens, h.cfg.Upstream.HTTP); ok {
			entry["max_output_tokens"] = mo
		}
		// 上游模型对象全字段透出（name/描述/标签/倍率/能力旗标等，空值省略）。
		entry = applyModelInfoFields(entry, mi)
		// effort 能力透出——远端 supportedEfforts 权威，缺失落到 CN 静态兜底表
		// （客户端可发现档位，不再盲传）。无档位 → 省略字段。
		if efforts, def := upstream.EffortListing("cn", mi.ID, mi.Efforts, mi.DefaultEffort); efforts != nil {
			entry["reasoning_supported_efforts"] = efforts
			if def != "" {
				entry["reasoning_default_effort"] = def
			}
		}
		return entry
	}
	for _, mi := range h.fetchDynamicModels() {
		if _, hide := hidden["cn:"+mi.ID]; !hide {
			out = append(out, cnEntry(mi))
		}
	}
	// 企业专属模型：域目录（个人号视角）里没有、池里某个企业号自己的目录里有，
	// 请求只会派给目录里有它的企业号（见 upstream/account_catalog.go）。
	if h.cfg.Upstream != nil {
		for _, mi := range h.cfg.Upstream.EnterpriseOnlyModels("cn") {
			if _, hide := hidden["cn:"+mi.ID]; hide {
				continue
			}
			entry := cnEntry(mi)
			entry["enterprise_only"] = true
			delete(entry, "is_default")
			out = append(out, entry)
		}
	}
	// global 模型名单：仅 GlobalEnabled=true 时列出（逃生门）。
	// 名单 = 纯动态探测结果（fetchGlobalModels，失败/无号 → 空）。
	if h.cfg.GlobalEnabled {
		// global 域 effort 能力三级查找：探测下发桶（权威）→ 静态兜底表 → 省略。
		// 先 fetchGlobalModels（内部探测并落 effort 桶），再按 id 取快照。
		globalIDs, globalAccount := h.fetchGlobalModels()
		// 探测对象形态的全字段条目（与 fetchGlobalModels 共享同一次探测缓存）：
		// 命中 id 才透出富字段；窄表/失败 → nil，按裸 ID 条目输出（不编造字段）。
		// globalAccount 为 nil（无 global 号）时返回 nil，跳过富字段映射。
		globalInfos := map[string]upstream.ModelInfo{}
		for _, mi := range h.cfg.Upstream.FetchGlobalModelInfos(globalAccount) {
			globalInfos[mi.ID] = mi
		}
		globalEfforts, globalDefaults := h.cfg.Upstream.GlobalEffortSnapshot()
		for _, id := range globalIDs {
			if _, hide := hidden["global:"+id]; hide {
				continue
			}
			entry := map[string]any{
				"id":       "global:" + id,
				"object":   "model",
				"created":  1753600000,
				"owned_by": "workbuddy",
			}
			// context_length / max_output_tokens 四级查找（与 CN 动态分支同口径）。
			var remoteCtx, remoteOut int64
			if mi, ok := globalInfos[id]; ok {
				entry = applyModelInfoFields(entry, mi)
				remoteCtx, remoteOut = mi.ContextWindow, mi.MaxTokens
			}
			entry["context_length"] = upstream.ContextWindowListingV4(id, remoteCtx, h.cfg.Upstream.HTTP)
			if mo, ok := upstream.MaxOutputTokensListingV4(id, remoteOut, h.cfg.Upstream.HTTP); ok {
				entry["max_output_tokens"] = mo
			}
			if efforts, def := upstream.EffortListing("global", id, globalEfforts[id], globalDefaults[id]); efforts != nil {
				entry["reasoning_supported_efforts"] = efforts
				if def != "" {
					entry["reasoning_default_effort"] = def
				}
			}
			out = append(out, entry)
		}
		// 跨域负载均衡打开时，再列一份不带前缀的名字放在最前：客户端从列表里选到的
		// 就是会分到两域的名字；带前缀的条目照旧保留，用来钉死某一域。
		if crossRealmOn(h.loadLive().CrossRealm) {
			out = append(bareModelEntries(out), out...)
		}
	}
	return out
}

// bareModelEntries 由带前缀的条目生成不带前缀的条目（两域并集，按名字去重）。
// 元数据取先出现的那条（国内版排在前面）；realms 标出这个名字会分到哪些域；
// is_default 不带过来，免得列表里出现两个默认模型。
func bareModelEntries(prefixed []map[string]any) []map[string]any {
	idx := map[string]int{}
	out := make([]map[string]any, 0, len(prefixed))
	for _, e := range prefixed {
		id, _ := e["id"].(string)
		realm, bare := resolveModel(id)
		if bare == id {
			continue
		}
		if i, ok := idx[bare]; ok {
			out[i]["realms"] = append(out[i]["realms"].([]string), realm)
			continue
		}
		c := make(map[string]any, len(e)+1)
		for k, v := range e {
			c[k] = v
		}
		c["id"] = bare
		c["realms"] = []string{realm}
		delete(c, "is_default")
		idx[bare] = len(out)
		out = append(out, c)
	}
	return out
}

// fetchGlobalModels 返回 global 模型名单（纯动态探测结果）及被探测账号。
// 缓存/失败回落封在 upstream.FetchGlobalModels（内部 1h + 5min 负缓存）。
// 本方法只负责"何时探测"：池中无 global 账号 → 空名单 + nil 账号（零上游调用）。
// 返回的 acct 供调用方在同一账号上取富 ModelInfo（FetchGlobalModelInfos 与
// FetchGlobalModels 共享缓存，不会触发第二次上游探测）。
// GlobalEnabled=false 时 modelList 已不进入本分支（逃生门在调用方 gate）。
func (h *Handler) fetchGlobalModels() ([]string, *auth.Auth) {
	acct := h.cfg.Pool.PickExcludingForRealm(nil, "", "global")
	if acct == nil {
		return nil, nil
	}
	return h.cfg.Upstream.FetchGlobalModels(acct), acct
}

// fetchDynamicModels 从第一个可用 CN 个人号（没有个人号才用企业号）拉模型列表
// （含 contextWindow/maxTokens），缓存 10min。
// 选号与 /panel/api/models 完全同口径（CatalogUIDForRealm("cn") + AuthByUID），
// 而非 Pool.Pick()：Pick 无 realm 过滤，混合池里可能选中 global 号去打 CN 端点，
// 表现为偶发失败/面板与 /v1/models 两套目录（PR #38 报告并给出的选号修复）。
// 缓存 + 5min 负缓存按既有语义**保留**（#38 原案整体删除缓存被拒）：公开端点逐请求
// 实时拉取 = 每次 2 个上游探测，客户端周期性刷新模型列表会持续打上游；上游故障时
// 无冷却窗口，客户端重试即放大请求量——负缓存正是为此设计（见 handler_test 吸收
// 上游 9832283 的注释）；且 cachedModelsSnapshot（gateway_hint 判定）依赖缓存写入。
func (h *Handler) fetchDynamicModels() []upstream.ModelInfo {
	dynamicModelsCache.RLock()
	if len(dynamicModelsCache.ids) > 0 && time.Since(dynamicModelsCache.fetched) < dynamicModelsTTL {
		out := dynamicModelsCache.ids
		dynamicModelsCache.RUnlock()
		return out
	}
	// 失败负缓存：冷却期内不再请求上游。
	if !dynamicModelsCache.lastFail.IsZero() && time.Since(dynamicModelsCache.lastFail) < modelsFetchFailCooldown {
		dynamicModelsCache.RUnlock()
		return nil
	}
	dynamicModelsCache.RUnlock()

	// 优先用个人号拉：域目录要个人号视角，企业号的名单各自单独记。
	uid := h.cfg.Pool.CatalogUIDForRealm("cn")
	if uid == "" {
		return nil
	}
	acct := h.cfg.Pool.AuthByUID(uid)
	if acct == nil {
		return nil
	}
	infos, err := h.cfg.Upstream.FetchModels(acct)
	if err != nil || len(infos) == 0 {
		// 拉取失败只进负缓存（5min lastFail），不 NoteError：NoteError 喂的是 chat
		// 熔断器，models 端点偶发 5xx 跨界惩罚 chat 通道健康的账号；
		// models 拉取失败 ≠ 账号 chat 不可用。
		// 只在真正打上游失败时记一行（负缓存期内前面已直接 return，不会刷屏）：
		// 此前这里静默，CN 域不可达时 /v1/models 只剩 global 模型却毫无线索。
		if err == nil {
			err = fmt.Errorf("models api returned empty list")
		}
		log.Printf("WARN: [server] /v1/models: CN 模型目录拉取失败（%s 内不再重试，cn: 模型暂不列出）: %v",
			modelsFetchFailCooldown, err)
		dynamicModelsCache.Lock()
		dynamicModelsCache.lastFail = time.Now()
		dynamicModelsCache.Unlock()
		return nil
	}
	dynamicModelsCache.Lock()
	dynamicModelsCache.ids = infos
	dynamicModelsCache.fetched = time.Now()
	dynamicModelsCache.lastFail = time.Time{} // 成功则清空负缓存
	dynamicModelsCache.Unlock()
	return infos
}

// cachedModelsSnapshot 只读模型目录缓存（TTL 内快照）；缓存冷/空 → nil。
// 不发起任何上游调用（hint 判定用：错误路径加一次 FetchModels 网络调用既拖慢
// 错误响应、又污染上游调用语义）。
func cachedModelsSnapshot() []upstream.ModelInfo {
	dynamicModelsCache.RLock()
	defer dynamicModelsCache.RUnlock()
	if len(dynamicModelsCache.ids) == 0 || time.Since(dynamicModelsCache.fetched) >= dynamicModelsTTL {
		return nil
	}
	return dynamicModelsCache.ids
}

// acquireInFlight 占一个在途名额。包级变量（默认行为与 (*pool.Pool).Acquire
// 完全一致）供测试注入「名额被并发抢走」的确定路径。
var acquireInFlight = (*pool.Pool).Acquire

func (h *Handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	// 客户端 IP 提取（按请求传递到 ChatStream，不透传时 upstream 侧忽略）；
	// 消除早年共享字段方案的并发交叉污染（issue：ClientIP 竞态）。
	clientIP := upstream.ExtractClientIP(r)
	// 请求体无大小上限（max_body_mb 已移除，对齐上游）：完整读入，超限类问题交由
	// 上游自然返回错误（其响应经既有错误分类链路透出，信息量更大）。#41 的截断
	// 防御语义保留在读错误路径——移除预拦截后，截断只可能来自客户端自己断流，
	// 读 body 出错就地 400，不把半截 JSON 喂上游 unmarshal 报 unexpected EOF 冤枉罚号。
	body, err := readRequestBody(r)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}
	// 不是合法 JSON 就地 400：转给上游只会换来 11101，白白占一次上游调用。字段类型不对
	// （比如 stream 写成字符串）仍照转，由上游判定。
	if !json.Valid(body) {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "request body is not valid JSON")
		return
	}
	var peek struct {
		Stream bool   `json:"stream"`
		Model  string `json:"model"`
	}
	_ = json.Unmarshal(body, &peek)
	// 模型路由：目录里没有的名字按规则改写成上游模型，之后整条链路都按改写后的名字走。
	if routed, ok := h.routeModel(r.Context(), peek.Model); ok {
		body = rewriteModel(body, routed)
		peek.Model = routed
	}

	// 工具名名单：上游在没有 tools 的请求里会把工具调用吐成原生标记文本
	// （见 upstream/dsml.go），修复层用它做严格判定。名单来自「本请求声明的 tools」
	// 与「会话历史里出现过的工具名」两个来源——实测 tools 声明会在中转环节丢失，
	// 只认前者会让修复层在最需要它的场景下失效。
	// 在提示词/模型名改写之前取（那些改写不动 tools / messages[].tool_calls 子树）。
	declaredTools := upstream.ToolNameAllowlist(body)

	// realm 前缀解析（D6）：model 名可能带 "[realm:]" 前缀。剥出 realm + bareModel，
	// bareModel 用于选号/粘性/出站 body 重写（前缀是网关侧路由协议，上游只认裸名）。
	// 裸名 → ("cn", 原串)，CN 现状零回归。
	realm, bareModel := resolveModel(peek.Model)
	hidden := h.loadLive().HiddenModels
	// 测活（见 liveness.go）：钉死一个号只试一次，域跟着那个号走（模型名带它的域前缀），不跨域、不看 models.hidden。
	pinned := livenessFrom(r.Context())
	// 跨域负载均衡（pool.cross_realm，热生效）：只管不带前缀的模型名，带前缀的照旧钉死该域。
	// 改写 realm 为目录里有这个模型的域；两域都有时合池为 ""，下方选号即在两域合池里挑；国内版优先时
	// 先定成此刻该用的域，选号循环每次再按国内版有没有能接的号重定（cnFirstRealm）。
	// models.hidden 里只藏了一个域的，当那个域没有这个模型。
	// tried 这次请求不再选的号：试过的号；客户端密钥绑了分组时，组外的号从一开始就算在里面（选号、排队、
	// 国内版优先选域、会话粘性都跳过它们）。
	tried, keyGroups := h.keyScope(r.Context())
	crossRouted, cnFirst := false, false
	if mode := h.loadLive().CrossRealm; pinned == "" && bareModel == peek.Model && bareModel != "" && h.cfg.GlobalEnabled && h.cfg.Upstream != nil && crossRealmOn(mode) {
		realm = crossRealmFor(bareModel, func(r, m string) bool {
			_, hide := hidden[r+":"+m]
			return !hide && h.cfg.Upstream.RealmHasModel(r, m)
		})
		crossRouted = true
		if realm == "" && mode == livecfg.CrossRealmCNFirst {
			realm, cnFirst = h.cnFirstRealm(tried, bareModel), true
		}
	}
	modelRate := ""
	if h.cfg.Upstream != nil {
		modelRate = h.cfg.Upstream.ModelRate(realm, bareModel)
	}

	// 请求级统计：出口即打一行表格日志（任何路径都会走到）。
	st := newChatStat(time.Now(), body, peek.Stream)
	if tr := requestTraceFrom(r); tr != nil {
		tr.stat = st
		// 来源在 ServeHTTP 入口采集（此时才知道开关与请求头），此处转交给统计对象，
		// 让 stdout 流水行与归档事件共用同一份来源值，两处不会漂移。
		st.clientIP, st.userAgent = tr.clientIP, tr.userAgent
	}
	defer st.done()

	// models.hidden：点名调用被藏起来的模型直接拒，不选号、不发上游（只藏列表不够，知道名字照样能调）。
	// 按改写后实际发上游的模型判；跨域合池时上面选域已经避开被藏的域（realm 为 "" 时不会命中）。
	if _, hide := hidden[realm+":"+bareModel]; hide && pinned == "" {
		writeOpenAIError(w, http.StatusBadRequest, "model_unavailable",
			fmt.Sprintf("model %s is hidden by gateway configuration (models.hidden), try another model", peek.Model))
		st.status, st.outcome = http.StatusBadRequest, reqlog.OutcomeHTTPError
		return
	}

	// 客户端密钥：白名单按改写后实际发上游的模型判，过了再按上限放行并计一次请求。被拦下的也记一行
	// 请求日志（带模型名），看得出是哪个模型、哪项上限不让用。
	keyOwner := principalFrom(r.Context())
	usageKey := keyOwner.usageKey()
	if pinned != "" {
		usageKey = livenessUsageKey
	}
	admitted := time.Now()
	if status := h.admitKey(w, keyOwner, bareModel, admitted); status != 0 {
		st.status, st.outcome = status, reqlog.OutcomeHTTPError
		return
	}
	if keyOwner.key != nil {
		// 失败的请求不算次数：最后没成功就退还放行时计的那一次。客户端中途断开时上游已经在回了，照算。
		defer func() {
			if st.outcome != reqlog.OutcomeSuccess && st.outcome != reqlog.OutcomeInterrupted {
				h.cfg.Keys.Refund(keyOwner.key.ID, admitted)
			}
		}()
	}

	var lastErr error
	// wafExit 最近一次回 WAF 403 的号所在的出口：末端文案看这个出口是不是整个被拦了。
	wafExit := ""
	// modelBlock 记录本次是否因「模型级冷却」而选不到号（下方 acct==nil 分支填充）。
	// 默认零值 Blocked=false = 按既有的"没有可用账号"口径报错。
	var modelBlock pool.ModelBlockStatus

	// 会话粘性：从请求体提取会话键并解析绑定号（找不到/无效则 stickyUID 为空，走普通轮换）。
	// ExtractKey 与粘性开关解耦（issue #35 侧）：关闭粘性时会话头族的聚合主键仍按
	// 会话级（RequestIDForKey(sessKey)），不悄悄退化成轮级——提取本身与粘性无关。
	sessKey := session.ExtractKey(body)
	// 认对话用的摘要在提示词改写之前取：降级开关切换会换系统提示词，不能让出站会话 id 跟着变。
	conv := session.ParseConversation(body)
	if pinned != "" {
		// 测活不碰会话粘性和出站会话 id 的记录：一句「hi」不能和以「hi」开头的真实对话串到一起。
		sessKey, conv = "", session.Conversation{}
	}
	stickyUID := ""
	if h.cfg.Session != nil && sessKey != "" {
		// 按模型解析：绑定号在**当前模型**被 6004 限额时视为不可用 → 重新分配，
		// 而不是钉在限额号上反复失败（"限额后换不动号"的正解）。
		// 域用上面已定好的 realm（跨域合池时为 ""，国内版优先时为此刻该用的域）：粘性候选若自己按模型名再判一次，
		// 不带前缀的名字一律算国内版，合池时会话永远只绑国内版号，国际版号轮不到。
		if uid, ok := h.cfg.Session.ResolveForModel(sessKey, bareModel, realm, tried); ok {
			stickyUID = uid
		}
	}

	// 轮级聚合键：按 body 里最后一条 user 消息派生（同轮内所有上游调用同键，
	// 换 user 消息换键）。#170 起带会话键的客户端也统一走轮级（对齐官方桌面 CLI
	// 的 X-Conversation-Request-ID 轮级语义——TraceStartHook 每次 USER_PROMPT_SUBMIT
	// 清空重生成），故不再限 sessKey=="" 才计算；sessKey 由下方派生处以复合键方式
	// 入键（防不同会话同轮文本互撞）。
	// 必须在下方 prompt.Rewrite 之前取——改写会动 messages 内容，之后取会让键漂移。
	turn := session.ParseTurn(body)
	if pinned != "" {
		turn = session.Turn{} // 同上，也不记轮次
	}
	turnKey := turn.Key

	// gateway_hint 判定所需的请求形态（image_url part）：在改写前取（与 turnKey
	// 同理）。11133「模型不支持图片」指向的前提。
	reqHasImage := hasImagePart(body)

	// 在途租约：成功选中即占名额；函数出口（含成功 return 与 panic）统一释放。
	var heldUID string
	defer func() {
		if heldUID != "" {
			h.cfg.Pool.Release(heldUID)
		}
	}()
	releaseHeld := func() {
		if heldUID != "" {
			h.cfg.Pool.Release(heldUID)
			heldUID = ""
		}
	}
	// unbindSticky 解绑当前会话粘性号（stickyUID 非空时）。供「粘性号不可用/被抢」与 fail 共用。
	// 幂等：stickyUID 已空则空操作；不会误解绑其他轮的绑定。仅当 Session != nil 时 stickyUID 才会非空。
	unbindSticky := func() {
		if stickyUID != "" {
			h.cfg.Session.Unbind(sessKey)
			stickyUID = ""
		}
	}
	// fail 在轮转失败分支统一：释放租约 + 若失败号正是粘性号则解绑（下次请求重新分配）。
	fail := func(uid string) {
		releaseHeld()
		if stickyUID != "" && uid == stickyUID {
			unbindSticky()
		}
	}
	// ttfb 首 token 等待（仅流式有观测；非流式传 0 = 无观测，速率不扣减）。
	// 显式入参而不是读 st.ttfb：后者在流式分支里是**调用之后**才赋值的，
	// 靠顺序传递会让将来重排代码时静默把速率算回旧的错口径。
	recordAttempt := func(uid string, delta pool.TokenUsageDelta, credit float64, hasCredit bool, started time.Time, ttfb time.Duration) {
		st.attempts++
		if delta.HasPromptTokens {
			st.promptTokens = delta.PromptTokens
		}
		if delta.HasCompletionTokens {
			st.completionTokens = delta.CompletionTokens
		}
		if delta.HasTotalTokens {
			st.totalTokens = delta.TotalTokens
		} else if delta.HasPromptTokens || delta.HasCompletionTokens {
			st.totalTokens = st.promptTokens + st.completionTokens
		}
		delta.Model = bareModel
		if delta.Model == "" {
			delta.Model = peek.Model
		}
		latency := time.Since(started)
		latencyMs := latency.Milliseconds()
		if latencyMs < 1 {
			latencyMs = 1
		}
		delta.HasLatencyMs = true
		delta.LatencyMs = latencyMs

		// HasCompletionTokens 是「本次有没有 token 观测」的独立判断，与速率怎么算
		// 无关，所以留在调用点；速率本身的边界（TTFB 缺失/超界）收在 tokensPerSecond。
		if delta.HasCompletionTokens {
			if tps, ok := tokensPerSecond(delta.CompletionTokens,
				time.Duration(latencyMs)*time.Millisecond, ttfb); ok {
				delta.HasTokensPerSecond = true
				delta.TokensPerSecond = tps
			}
		}
		h.cfg.Pool.RecordTokenUsage(uid, delta)

		// 用量时序记录。ok 以「上游是否给了 usage」判定：空 delta 意味着这次尝试
		// 没拿到任何 token 统计（传输错误 / >=400 / 解析失败），计为失败尝试。
		// 失败也计入请求数——否则重试放大在「用量」视图里看不见。
		if h.cfg.Usage != nil {
			realm := "cn"
			if a, ok := h.cfg.Pool.Status(uid); ok && a.Realm != "" {
				realm = a.Realm
			}
			// 合池选号时同一模型名两域倍率不同，按实际服务的号所在域取
			rate := modelRate
			if crossRouted && h.cfg.Upstream != nil {
				rate = h.cfg.Upstream.ModelRate(realm, bareModel)
			}
			h.cfg.Usage.Add(time.Now(), realm, uid, delta.Model, usage.Delta{
				PromptTokens:     delta.PromptTokens,
				HasPromptTokens:  delta.HasPromptTokens,
				CompletionTokens: delta.CompletionTokens,
				HasCompletion:    delta.HasCompletionTokens,
				TotalTokens:      delta.TotalTokens,
				HasTotal:         delta.HasTotalTokens,
				Credit:           credit,
				HasCredit:        hasCredit,
				HasCacheTokens:   st.hasCache,
				CacheHitTokens:   st.cacheHit,
				CacheMissTokens:  st.cacheMiss,
				ModelRate:        rate,
				LatencyMs:        delta.LatencyMs,
				HasLatency:       delta.HasLatencyMs,
				TokensPerSecond:  delta.TokensPerSecond,
				HasTPS:           delta.HasTokensPerSecond,
				Key:              usageKey,
			}, delta.HasTotalTokens || delta.HasCompletionTokens || delta.HasPromptTokens)
		}
		if keyOwner.key != nil {
			tokens := delta.TotalTokens
			if !delta.HasTotalTokens {
				tokens = delta.PromptTokens + delta.CompletionTokens
			}
			h.cfg.Keys.Record(keyOwner.key.ID, time.Now(), tokens, credit, hasCredit)
		}
	}

	// 系统提示词改写（出站前、轮转前；每个请求一次）。
	//   - custom：用自有提示词替换客户端 system/developer（从源头消灭 system 指纹误报）。
	//   - append：开头连续 system/developer 块后插自有提示词，既有消息逐字不动
	//     （客户端项目规范/工具约定与网关提示词并用，issue #129）。
	//   - passthrough + 降级期：换 Degraded 中性提示词直达，不再先撞 400。
	//   - passthrough / append 非降级期：透传客户端原始 system（append 则再插一条网关 system）。
	// 降级裁决：append 在降级期退化为 replace（Rewrite(Degraded)）——append 带
	// 指纹原文重试是确定性再撞墙，replace 是一次性最小抢救（issue #129 设计 §4）。
	degradedApplied := false
	if h.cfg.PromptMode == "custom" && h.cfg.PromptText != "" {
		body = prompt.Rewrite(body, h.cfg.PromptText)
	} else if h.cfg.PromptMode == "append" && h.cfg.PromptText != "" && !h.degrade.Active() {
		body = prompt.Append(body, h.cfg.PromptText)
	} else if (h.cfg.PromptMode == "passthrough" || h.cfg.PromptMode == "append") && h.degrade.Active() {
		body = prompt.Rewrite(body, prompt.Degraded)
		degradedApplied = true
	}

	// outbound model 名重写为 bareModel（D6）：realm 前缀是网关侧路由协议，
	// 上游不认前缀（global 账号也请求裸模型名）。裸名时 bareModel==peek.Model 恒等。
	if bareModel != peek.Model {
		body = rewriteModel(body, bareModel)
	}

	// 会话头族（issue #35）：后台按 X-Conversation-Request-ID（对话轮级）聚合请求，
	// 官方客户端一次 user send 内所有 tool call/重试/换号复用同一个 ID。此处**轮转
	// 循环外**生成一次，循环内每次出站原样复用 → 换号/重试/降级全部同 ID，后台不再
	// 碎片化（此前网关一个都不发，上游按 HTTP 请求逐条记账，同一对话几十上百个
	// RequestID）。
	//   - conversationID：客户端给了就原样透传；没给时选好号以后定（循环内，
	//     session.OutboundConversationID：第一轮记下的 id 整段对话沿用，换号也不变，见 session/convids.go）；
	//   - conversationRequestID：入站 X-Conversation-Request-ID 透传优先，否则按对话轮：
	//     客户端给了会话 id 时由会话 id + 轮级键派生，没给时开轮新生成、轮内凭工具调用 id
	//     找回（session/turns.go）；建不出轮级键时退化成会话级或本请求级随机——
	//     轮转内捕获一次即共享；
	//   - messageID 在 ChatHeaders 内每条消息生成（消息级独立，无需外部可见）。
	convID := session.ResolveConversationID(body)
	chatMeta := upstream.ChatMeta{ConversationID: convID}
	// rememberTurn 开轮请求成功转出回复后，记下回复里的工具调用 id，轮内后续请求凭它找回本轮 ID。
	rememberTurn := false
	if v := r.Header.Get("X-Conversation-Request-ID"); v != "" {
		chatMeta.ConversationRequestID = v
	} else if turnKey != "" && convID == "" {
		// 客户端没给会话 id：会话键是派生的（或为空），到某一轮为止逐字相同的两段对话
		// 轮级键也相同，不能再从内容派生。开轮请求每次新生成，轮内后续请求凭工具调用 id
		// 找回（见 session/turns.go）。
		chatMeta.ConversationRequestID = h.turns.ForRequest(sessKey, turn)
		rememberTurn = turn.Opening
	} else if turnKey != "" {
		// 客户端给了会话 id（此时 sessKey 就是它）：轮级复合键，sessKey 入键防跨会话
		// 同轮文本互撞（#170 统一轮级）。
		chatMeta.ConversationRequestID = session.TurnRequestID(sessKey + ":" + turnKey)
	} else if sessKey != "" {
		// 残留空态兜底（无 user 消息/无可签名内容）：会话级聚合，好于请求级随机。
		chatMeta.ConversationRequestID = session.RequestIDForKey(sessKey)
	} else {
		// 无会话键也无轮级键：请求级随机（轮转内捕获一次即共享）。
		chatMeta.ConversationRequestID = session.TurnRequestID("")
	}
	chatMeta.TraceID = r.Header.Get("X-Trace-ID")
	// noteCache 把这一跳的前缀缓存观测交给未命中告警，按对话对照上一轮（见 cacheMissSignal）。
	noteCache := func(acct *auth.Auth) {
		if !st.hasCache {
			return
		}
		cacheMissWarn.note(cacheObservation{
			key: sessKey, model: bareModel, uid: acct.UID, acct: logfmt.Label(acct.UID, acct.Nickname),
			convID: chatMeta.ConversationID, msgs: conv.Messages,
			prompt: st.promptTokens, hit: st.cacheHit, miss: st.cacheMiss,
		})
	}

	// slot 这个请求排队等在途名额的状态（见 waitSlot）。
	var slot slotWait
	for i := 0; i < h.cfg.MaxRotate; i++ {
		// 在选号之前取：选号和开始等之间放出的名额也能叫醒这个请求。
		slotFreed := h.cfg.Pool.SlotFreed()
		if cnFirst {
			// 国内版优先：每次选号前重定域。换号后国内版可能没有能接的号了，排队醒来后国内版可能又有了；
			// 绑在国际版号上的会话等国内版有号了也回国内版（下面按域不符解绑）。
			realm = h.cnFirstRealm(tried, bareModel)
		}
		// 选号：粘性号优先（PickByUIDForModel 已校验该模型可用性 + 在途未满），否则普通轮换。
		var acct *auth.Auth
		if pinned != "" {
			// 测活：不管冷却、禁用，就用这个号，失败了也不换号。
			if acct = h.cfg.Pool.AuthByUID(pinned); acct == nil || i > 0 {
				break
			}
		}
		if acct == nil && stickyUID != "" {
			acct = h.cfg.Pool.PickByUIDForModel(stickyUID, bareModel)
			if acct == nil || (realm != "" && acct.Realm() != realm) {
				// 粘性号在当前模型不可用（冷却/占满/该模型被 6004 限额）或 realm 不符 → 解绑，
				// 本次回落普通轮换。
				unbindSticky()
				acct = nil
			}
		}
		if acct == nil {
			// 粘性号用不了时先回到这段对话用过的其他号：上面留着这段对话的前缀缓存。
			acct = h.pickWarm(sessKey, bareModel, realm, tried)
		}
		if acct == nil {
			// 模型感知 + realm 感知选号：模型非空时启用 6004 模型级冷却豁免
			// （healthyForModel），realm 谓词过滤跨域账号。
			acct = h.cfg.Pool.PickExcludingForRealm(tried, bareModel, realm)
		}
		if acct == nil && h.waitSlot(r.Context(), slotFreed, &slot, tried, bareModel, realm) {
			// 能接这个请求的号在途都满了，等到有号放出名额（或到了重看的时刻）：再选一次，不算换号。
			i--
			continue
		}
		if acct == nil {
			st.status = http.StatusServiceUnavailable
			if slot.timedOut {
				why := "在途都满"
				if slot.rpm {
					why = "在途满或每分钟请求数到上限"
				}
				log.Printf("WARN: [server] in-flight full: model=%s 能接的号%s，等了 %.1fs 仍没有空位，回 503",
					st.model, why, time.Since(slot.since).Seconds())
			}
			// 记下"是不是模型级阻塞"。选号返回 nil 有两种完全不同的成因：
			//   (a) 池子真的没有可用号（账号级冷却/在途占满/积分保底）；
			//   (b) 号都在，但每个号都对这个模型处于模型级冷却（11102/11158/6004）。
			// 两者此前都报 no_healthy_account，导致「模型不可用」被读成「号全挂了」。
			// 在这里取一次快照，供下方错误构造区分（issue #102 附带发现 1）。
			modelBlock = h.cfg.Pool.ModelBlockedForRealm(bareModel, realm)
			break
		}
		st.uid = acct.UID
		// 同步昵称：请求流水行只写 uid8 时无法直观看是哪一号，昵称随本次选号带入日志行。
		st.nick = acct.Nickname
		st.proxy = h.proxyName(acct.UID)
		if crossRouted {
			// 跨域选号：日志与请求记录写实际服务的域，看得出这次落在国内版还是国际版
			st.model = acct.Realm() + ":" + bareModel
		}
		tried[acct.UID] = true
		woke := slot.woke
		slot.woke = false

		// 占用在途名额：Pick 已跳过满额账号，此处 CAS 兜底并发抢名额的竞态。
		if !acquireInFlight(h.cfg.Pool, acct.UID) {
			if pinned != "" {
				// 测活不等名额：这个号在途已满或每分钟请求数到上限，说明正在接请求，回 503 让人稍后再测。
				st.status, st.outcome = http.StatusServiceUnavailable, reqlog.OutcomeHTTPError
				writeOpenAIError(w, http.StatusServiceUnavailable, "account_busy", "account is at its in-flight or requests-per-minute limit, test again later")
				return
			}
			// 若被抢的正是粘性号，立即解绑并回落普通轮换，避免下一轮仍撞同一个
			// 满载粘性号再浪费一次 PickByUID 往返（语义与 fail()/PickByUID-nil 的解绑一致）。
			if stickyUID != "" && acct.UID == stickyUID {
				unbindSticky()
			}
			if woke {
				// 排队醒来的几个请求抢同一个名额、没抢到：不算这个号失败，也不算换号，接着排队。
				delete(tried, acct.UID)
				i--
				continue
			}
			if !rotateBackoff(i, r.Context()) {
				// 客户端已断连：换号重试无意义，终止轮转走末端错误透传。
				break
			}
			continue // 最后一个名额被并发抢走 → 换号
		}
		heldUID = acct.UID
		if !slot.since.IsZero() && !slot.logged {
			slot.logged = true
			log.Printf("INFO: [server] in-flight full: model=%s 等了 %.1fs 等到空位 acct=%s",
				st.model, time.Since(slot.since).Seconds(), logfmt.Label(acct.UID, acct.Nickname))
		}

		// token 临近过期 → 先 refresh（失败冷却换号）
		if acct.NeedsRefresh(h.cfg.RefreshSkew) {
			if err := h.cfg.Upstream.RefreshToken(acct); err != nil {
				lastErr = err
				var ue *upstream.Error
				if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
					h.cfg.Pool.Disable(acct.UID, "refresh session dead")
				} else if !upstream.IsRouteError(err) {
					h.cfg.Pool.NoteError(acct.UID) // 号的代理连不上不罚号（同下方传输层分支）
				}
				fail(acct.UID)
				if !rotateBackoff(i, r.Context()) {
					break // ctx 取消：终止轮转（refresh 失败换号退避）
				}
				continue
			}
			if err := acct.SaveAtomic(); err != nil {
				// 刷新成功但落盘失败：下次启动会用旧 token，必须暴露
				log.Printf("ERR: [server] chat refresh acct=%s: save auth failed: %v", logfmt.Label(acct.UID, acct.Nickname), err)
			}
		}

		// 出站会话 id：客户端没给时取这段对话的 id，换号重试也是同一个（选中的号只用来认 1.6.0 的旧记录）。
		if convID == "" {
			chatMeta.ConversationID = session.OutboundConversationID(acct.UID, conv)
		}

		// 客户端 IP 按请求传递（PassthroughIP 开启时注入；消除共享字段竞态）。
		attemptStarted := time.Now()
		rc, status, respBody, terr := h.cfg.Upstream.ChatStreamContext(r.Context(), acct, body, clientIP, chatMeta)
		if isQueueWaiting(terr) {
			// 6020 排进容量队列了：在这个号上照官方排队，到号了原样重发；不等了才拿着最后那次 6020
			// 往下走，由下面回 503（见 capqueue.go）。
			a := h.capacityQueue(r.Context(), acct, body, clientIP, chatMeta, bareModel,
				chatAttempt{rc: rc, status: status, body: respBody, err: terr, started: attemptStarted})
			rc, status, respBody, terr, attemptStarted = a.rc, a.status, a.body, a.err, a.started
		}
		// 分类信封一次成型：upstream 已在错误路径返回 *upstream.Error（Kind +
		// Retry-After 头解析）。传输层错误（非 *Error）走抖动换号分支；防御分支
		// （terr 为 nil 但 status>=400，如 ErrNone 兜底）回落本地 Classify，双保险。
		var uerr *upstream.Error
		if errors.As(terr, &uerr) {
			status = uerr.Status
		}
		if uerr == nil && terr != nil {
			// 号的线路出了问题（号的代理连不上，或号此刻被代理池拦着，见 upstream/route.go）：不是这个号的
			// 问题，也不是上游的问题——不罚号、不喂连败，换个号再试。连不上的代理已经标成不通，绑在上面的号
			// 不会再被选中。放在超时判定之前：连代理超时也是 net.Error 超时，不能当成上游慢而止损。
			if upstream.IsRouteError(terr) {
				var rerr *upstream.RouteError
				if errors.As(terr, &rerr); !rerr.Blocked {
					recordAttempt(acct.UID, pool.TokenUsageDelta{}, 0, false, attemptStarted, 0) // 被拦的根本没发出去
				}
				st.status = http.StatusServiceUnavailable
				lastErr = terr
				fail(acct.UID)
				if !rotateBackoff(i, r.Context()) {
					break // ctx 取消：终止轮转
				}
				continue
			}
			// 上游超时 / 停滞：**不换号、不罚号**。
			//
			// 超时不是账号的问题：同一份请求换到别的号，撞上的是同一个慢上游，
			// 只会把客户端拖到 MaxRotate × header_timeout（部署值 600s 时最坏
			// 约半小时），期间还给一串健康号喂连败计数。此前全仓没有任何超时
			// 识别，超时和"网络抖动"共用同一条换号路径。
			//
			// 判定三态：net.Error.Timeout()（ResponseHeaderTimeout / Client.Timeout）、
			// 显式 deadline（DeadlineExceeded / os.ErrDeadlineExceeded）、以及
			// **客户端仍在但 ctx 被取消**——那只能是我们自己的空闲看门狗掐的流，
			// 也就是上游停滞。客户端主动断连时 r.Context() 已取消，走下面的抖动分支。
			if isUpstreamTimeout(terr, r.Context().Err() != nil) {
				recordAttempt(acct.UID, pool.TokenUsageDelta{}, 0, false, attemptStarted, 0)
				st.status = http.StatusServiceUnavailable
				lastErr = fmt.Errorf("%w: %v", errUpstreamTimeout, terr)
				log.Printf("WARN: [server] upstream timeout acct=%s: %v (rotation stopped, account not penalized)",
					logfmt.Label(acct.UID, acct.Nickname), terr)
				break
			}
			// 网络层抖动：只换号，不喂熔断计数（传输层错误对连续失败连坐熔断过于严苛）。
			// 连败兜底（issue #114）：喂连败计数——连不上上游是「不知道原因的失败」，
			// 连败 N 次临时出池，单次/偶发不罚（NoteFailures 内部达阈才动作）。
			// 上游 client 已打 transport error 日志。
			recordAttempt(acct.UID, pool.TokenUsageDelta{}, 0, false, attemptStarted, 0)
			st.status = http.StatusServiceUnavailable
			lastErr = terr
			h.cfg.Pool.NoteFailures(acct.UID)
			fail(acct.UID)
			if !rotateBackoff(i, r.Context()) {
				break // ctx 取消：终止轮转（传输层错误换号退避）
			}
			continue
		}
		if status >= 400 {
			recordAttempt(acct.UID, pool.TokenUsageDelta{}, 0, false, attemptStarted, 0)
			st.status = status
			var kind upstream.ErrKind
			if uerr != nil {
				kind = uerr.Kind
			} else {
				kind = upstream.Classify(status, string(respBody))
				uerr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody)}
			}
			// 内容拦截误报（passthrough/append 模式首遇）：判定为 system 指纹误报，
			// 触发降级到次日 00:00 CST，换 Degraded 中性提示词同请求内重试（append
			// 降级重试同样退化为 replace——原文在场只会确定性再撞 400）。
			// 第二次仍被拦（用户内容本身触发审核）→ 回内容防火墙错误（见下分支）。
			// 内容问题非账号问题：applyErrorPolicy 不罚账号（见 ErrContentBlocked 分支）。
			if kind == upstream.ErrContentBlocked && (h.cfg.PromptMode == "passthrough" || h.cfg.PromptMode == "append") && !degradedApplied {
				h.degrade.Trigger()
				body = prompt.Rewrite(body, prompt.Degraded)
				degradedApplied = true
				delete(tried, acct.UID) // 单账号池也能拿到重试机会（降级重试占一次名额）
				releaseHeld()
				log.Printf("content-blocked (likely fingerprint false positive) -> degraded prompt retry")
				continue
			}
			if kind == upstream.ErrContentBlocked {
				// 内容命中网关内容防火墙：立即回客户端，**不轮转**——换任何账号都会撞同一
				// 审核，轮转纯属浪费时间。不罚账号（ErrContentBlocked 分支无冷却/熔断/NoteError）。
				// error-passthrough：message 装上游 body 原文（code/msg/requestId 原样），
				// 不再改写成网关固定文案——客户端必须看到真实错误才能排查。
				h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
				fail(acct.UID)
				msg := string(respBody)
				if strings.TrimSpace(msg) == "" {
					// 空 body 兜底：无上游原文可透传，保留可读分类文案（不编造原文）。
					msg = "content blocked by upstream content firewall"
				}
				writeOpenAIErrorHint(w, http.StatusBadRequest, "content_blocked", msg,
					h.hintOf(upstream.ErrContentBlocked, string(respBody), bareModel, reqHasImage, uerr))
				st.status = http.StatusBadRequest
				st.outcome = reqlog.OutcomeHTTPError
				return
			}
			// 11115「prompt is too long」：立即透传上游原文回客户端，**不罚号不轮转**
			// ——上下文超限是请求的问题（同一 body 换任何号都超限，白扔健康号配额；
			// 与 WAF IP fail-fast 同哲学：确定与账号无关的错误直接终止轮转）。
			// applyErrorPolicy ErrPromptTooLong 分支零动作，fail 只释放租约。
			// message 装上游 body 原文（含真实 token 数与上限值——上游原文是最有价值
			// 的错误信息，客户端必须看到，禁止固定词覆盖）。
			if kind == upstream.ErrPromptTooLong {
				h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
				fail(acct.UID)
				writeOpenAIErrorHint(w, http.StatusBadRequest, "prompt_too_long", promptTooLongMessage(string(respBody)),
					h.hintOf(upstream.ErrPromptTooLong, string(respBody), bareModel, reqHasImage, uerr))
				st.status = http.StatusBadRequest
				st.outcome = reqlog.OutcomeHTTPError
				return
			}
			// 图片格式/数据无效：立即透传上游原文回客户端，不罚号不轮转。
			// 同一 body 换账号仍是同样的解析结果，轮转只会放大无效请求。
			if kind == upstream.ErrImageInvalid {
				h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
				fail(acct.UID)
				msg := string(respBody)
				if strings.TrimSpace(msg) == "" {
					msg = "image request was rejected by upstream"
				}
				writeOpenAIErrorHint(w, http.StatusBadRequest, "image_invalid", msg,
					h.hintOf(upstream.ErrImageInvalid, string(respBody), bareModel, reqHasImage, uerr))
				st.status = http.StatusBadRequest
				st.outcome = reqlog.OutcomeHTTPError
				return
			}
			// 请求体解析失败（11101）：与 11115 / 图片无效同一哲学——同一 body 换任何
			// 账号都是同样的解析结果，轮转只会放大无效请求（每号一次上游调用 +
			// rotateBackoff 占用在途名额）。更要紧的是：继续轮转后末端会落到
			// 「其余保持 503」，把确定失败的请求伪装成"账号不可用、稍后再试"，
			// 客户端于是对必然失败的请求无限重试。立即透传上游原文回 400。
			if kind == upstream.ErrBadParams {
				h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
				fail(acct.UID)
				msg := string(respBody)
				if strings.TrimSpace(msg) == "" {
					msg = "chat request body was rejected by upstream"
				}
				writeOpenAIErrorHint(w, http.StatusBadRequest, "bad_params", msg,
					h.hintOf(upstream.ErrBadParams, string(respBody), bareModel, reqHasImage, uerr))
				st.status = http.StatusBadRequest
				st.outcome = reqlog.OutcomeHTTPError
				return
			}
			// 6021 容量排队满：**不罚号不轮转**，立即透传上游原文回客户端（503 +
			// upstream_busy）。整条渠道排满了，换任何账号都撞同一堵墙——按限流处置会
			// 逐号软冷却（上游 linguo2625469 2026-10-15 实测 2 秒内整池健康号全被冷却），
			// 换号还把无效请求放大 MaxRotate 倍。官方遇到 6021 也只提示、不自动重发；
			// 客户端按 SDK 退避重试即可。
			if kind == upstream.ErrUpstreamBusy {
				h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
				fail(acct.UID)
				msg := string(respBody)
				if strings.TrimSpace(msg) == "" {
					msg = "upstream capacity is full (queue.full); retry after a short wait"
				}
				writeOpenAIErrorHint(w, http.StatusServiceUnavailable, "upstream_busy", msg,
					h.hintOf(upstream.ErrUpstreamBusy, string(respBody), bareModel, reqHasImage, uerr))
				st.status = http.StatusServiceUnavailable
				st.outcome = reqlog.OutcomeHTTPError
				return
			}
			// 6020 排队没等到号（等满上限、上游说过期，或客户端断开）：同 6021，不罚号不换号，回 503 带上游原文。
			// 换号也是排进同一条队列，从头再排。
			if kind == upstream.ErrQueueWaiting {
				h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
				fail(acct.UID)
				// 6020 是从这份回应体里认出来的，原文不会是空的。
				writeOpenAIErrorHint(w, http.StatusServiceUnavailable, "upstream_busy", string(respBody),
					h.hintOf(upstream.ErrQueueWaiting, string(respBody), bareModel, reqHasImage, uerr))
				st.status = http.StatusServiceUnavailable
				st.outcome = reqlog.OutcomeHTTPError
				if r.Context().Err() != nil {
					st.outcome = reqlog.OutcomeInterrupted
				}
				return
			}
			// lastErr 携带完整 body（uerr.Msg 在 upstream 侧截断 200 字符，透传语义
			// 要求原文全量）+ Kind/RetryAfter（末端映射与冷却时长共用）。
			lastErr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody), RetryAfter: uerr.RetryAfter}
			h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
			fail(acct.UID)
			// WAF IP 级 fail-fast（优先于 rotateBackoff 退避——IP 级拦截时退避无意义）：
			// 该次 WAF 403 喂入这个号所在出口的 IP 级状态机，若激活（短窗内同出口多号命中，IP 被拦而非
			// 账号）则同出口的号这次都不再试——继续换同出口的号只会把请求放大 MaxRotate 倍打同一出口 IP，
			// 加重风控；别的出口（号挂了别的代理）还有号能接就接着换，没有就立即终止轮转（见 wafip.go）。
			// 账号级软冷却已在上方 applyErrorPolicy 照常记账。
			if kind == upstream.ErrWafBlock {
				var stop bool
				if wafExit, stop = h.noteWafBlock(acct.UID, tried, bareModel, realm); stop {
					break
				}
			}
			if !rotateBackoff(i, r.Context()) {
				break // ctx 取消：终止轮转（分类错误换号退避）
			}
			continue
		}
		// 成功判定与粘性绑定一律**延后到这一跳真正成功之后**（见下方流式/非流式分支）：
		// 上游「200 已开流 + 一帧 error」是真实形态（6004 限流、内容拦截、审核），
		// 此前在读第一帧之前就 NoteSuccess + 清 11102 负缓存 + 绑粘性 → 被限流的号
		// 记成健康、粘性把会话钉死在它身上，后续每一轮都打同一个限流号。
		if peek.Stream {
			// 流式：透传结束后立即关闭上游 body，避免 defer 在轮转场景下堆积 fd。
			st.status = http.StatusOK
			stats := newChatStatsReaderSince(rc, st.start)
			// gateway_hint（SSE）：成功状态 200 已开流，中途 error 帧透传时附加
			// hint 字段（hintFn 惰性求值——正常流零开销，只有真撞到 error 帧才
			// 组装请求上下文做判定）。
			// errFrame：上游 error 帧原文（观察者旁路采集），用于流尾的账号处置。
			var errFrame string
			// 标记修复：上游在没有 tools 的请求里会把工具调用吐成原生标记文本，
			// 这里在透传前还原成 delta.tool_calls（见 upstream/dsml.go）。
			// 第二个参数为 true = 名单为空时启用弱判定——tools 声明在链路上丢失是
			// 实测最常见的泄漏成因，只做严格判定等于对主场景不设防。
			repair := upstream.NewMarkupRepair(declaredTools, true)
			var replyCallIDs []string
			sErr := upstream.StreamHint(w, stats, upstream.FrameHintFunc(func() upstream.HintContext {
				return h.hintContext(bareModel, reqHasImage)
			}), upstream.WithErrorFrameObserver(func(payload string) { errFrame = payload }),
				upstream.WithMarkupRepair(repair),
				upstream.WithToolCallIDObserver(func(id string) { replyCallIDs = append(replyCallIDs, id) }))
			if rememberTurn {
				h.turns.Remember(sessKey, turn, chatMeta.ConversationRequestID, replyCallIDs)
			}
			switch {
			case upstream.IsEmptyStreamError(sErr):
				// 上游 200 但空流（0 有效帧）：StreamHint 已写 error 帧 + [DONE]
				// 兜底（HTTP 头已发出只能 200），但这是上游缺陷不是成功——日志/
				// 状态收敛到 502 观测，与非流式 Aggregate 空流→502 upstream_parse
				// 同语义（此前 `_ =` 吞错把失败流记成 200，运维看到假成功）。
				// 只认 IsEmptyStreamError：客户端断连的写失败不误标（人已走，
				// 502 观测没有意义）。
				st.status = http.StatusBadGateway
				st.outcome = reqlog.OutcomeStreamError
				st.errMsg = "upstream returned an empty stream (200 with no frames)"
				log.Printf("WARN: [server] stream acct=%s model=%s: empty upstream stream (200+0 frames)", logfmt.Label(acct.UID, acct.Nickname), bareModel)
			case errFrame != "":
				// 上游以 error 帧报错（6004 限流 / 内容拦截 / 审核）：按帧内容分类并
				// 处置账号——**不记成功、不清 11102 负缓存、不绑粘性**。此前这些动作
				// 在流开始前就做了，于是一个正在限流的号被当成健康号，粘性还会把
				// 整个会话钉在它身上，后续每轮都失败。
				kind := upstream.FrameKind(errFrame)
				h.applyErrorPolicy(acct.UID, kind, errFrame, bareModel, nil)
				st.status = http.StatusServiceUnavailable
				st.outcome = reqlog.OutcomeStreamError
				st.errMsg = errFrame
				log.Printf("WARN: [server] stream acct=%s model=%s: upstream error frame kind=%s payload=%s",
					logfmt.Label(acct.UID, acct.Nickname), bareModel, kind, logfmt.Truncate(errFrame, 200))
			case sErr != nil:
				// 客户端写失败（断连）：上游帧无恙，账号健康——账号侧照常记成功
				// （与 default 同语义），请求日志归为 Interrupted（人已走，未完成）。
				st.outcome = reqlog.OutcomeInterrupted
				h.cfg.Pool.NoteSuccess(acct.UID)
				h.cfg.Pool.BlockModelClear(acct.UID, bareModel)
				if sessKey != "" && h.cfg.Session != nil {
					h.cfg.Session.Bind(sessKey, acct.UID)
				}
			default:
				// 真成功：这一跳读完且上游没有报错，才记成功并让粘性跟上。
				st.outcome = reqlog.OutcomeSuccess
				h.cfg.Pool.NoteSuccess(acct.UID)
				// 11102/11158 负缓存清命：该账号该模型实测成功，立即解除避让（不必等 TTL 到期）。
				// BlockModelClear 按 reason 前缀识别，只清这两类条目、不碰 6004 独立冷却。
				h.cfg.Pool.BlockModelClear(acct.UID, bareModel)
				// 粘性跟随最终成功号：本轮成功的账号成为该会话的粘性绑定（覆盖旧绑定）。
				// 若 sticky 号失败、轮换到别的号成功，这里把会话重绑到新号，多轮对话下一跳不再随机抽。
				if sessKey != "" && h.cfg.Session != nil {
					h.cfg.Session.Bind(sessKey, acct.UID)
				}
			}
			credit, hasCredit := stats.Credit()
			if hit, miss, ok := stats.CacheTokens(); ok {
				st.cacheHit, st.cacheMiss, st.hasCache = hit, miss, true
			}
			recordAttempt(acct.UID, stats.Usage(), credit, hasCredit, attemptStarted, stats.TTFB())
			// 未命中告警放 recordAttempt 之后：st.promptTokens 此时才是本次的观测值。
			noteCache(acct)
			if n := repair.Converted(); n > 0 {
				// 修复命中：上游本来会把工具调用吐成正文标记，这里已还原成 tool_calls。
				// 打一行便于运维确认「这次不再空转」以及统计发生率。
				log.Printf("INFO: [server] stream acct=%s model=%s: repaired %d native tool call(s) from assistant text (tool names known=%d)",
					logfmt.Label(acct.UID, acct.Nickname), bareModel, n, len(declaredTools))
			} else if m := repair.Seen(); m > 0 {
				// 识别到标记却没还原：说明判定没过（工具名不合规 / 块内夹带正文 /
				// 参数畸形）。单独记一行，排障时能一眼区分「没识别到」与「识别到但拒绝」。
				log.Printf("WARN: [server] stream acct=%s model=%s: saw %d native tool call block(s) but repaired none (tool names known=%d)",
					logfmt.Label(acct.UID, acct.Nickname), bareModel, m, len(declaredTools))
			}
			st.ttfb = stats.TTFB()
			// usage 缺失时保留 chatStat.toks 的 -1 哨兵（观测缺失 → 显示 "-"），
			// 不写入零值——否则「没观测到 usage」被伪造成「测得 0 token」，
			// 与非流式走 completionTokens 返回 -1 的口径不一致。
			if toks, hasUsage := stats.Tokens(); hasUsage {
				st.toks = toks
			}
			// 成本账本：末帧 usage 带 credit 与 token 总数时记录实测单价，
			// 供下次选号把免费/便宜的号排在前面。
			if hasCredit {
				st.credit = credit
				st.hasCredit = true
				if total, tok := stats.TotalTokens(); tok && total > 0 {
					h.cfg.Pool.NoteModelCost(acct.UID, bareModel, credit, total)
				}
			}
			rc.Close()
			return
		}
		resp, err := upstream.Aggregate(rc)
		rc.Close()
		if err != nil {
			recordAttempt(acct.UID, pool.TokenUsageDelta{}, 0, false, attemptStarted, 0)
			// 上游流解析失败：客户端还没看到任何输出，回 502 并告知原因。
			writeOpenAIError(w, http.StatusBadGateway, "upstream_parse", err.Error())
			st.status = http.StatusBadGateway
			st.outcome = reqlog.OutcomeHTTPError
			return
		}
		// 标记修复（非流式）：把正文里的原生工具调用标记还原成 message.tool_calls。
		// 只在「没有结构化 tool_calls」且「finish_reason 为 stop」时生效（见 dsml.go）；
		// 名单为空时走弱判定（tools 声明在中转环节丢失是常态）。
		mrepair := upstream.RepairAggregatedResponse(resp, declaredTools, true)
		if n := mrepair.Converted(); n > 0 {
			log.Printf("INFO: [server] non-stream model=%s: repaired %d native tool call(s) from assistant text (tool names known=%d)",
				bareModel, n, len(declaredTools))
		} else if m := mrepair.Seen(); m > 0 {
			log.Printf("WARN: [server] non-stream model=%s: saw %d native tool call block(s) but repaired none (tool names known=%d)",
				bareModel, m, len(declaredTools))
		}
		if rememberTurn {
			h.turns.Remember(sessKey, turn, chatMeta.ConversationRequestID, upstream.ResponseToolCallIDs(resp))
		}
		credit, total, hasCredit := usageCreditTotal(resp)
		if usage, ok := resp["usage"].(map[string]any); ok {
			if hit, miss, okC := upstream.UsageCacheTokens(usage); okC {
				st.cacheHit, st.cacheMiss, st.hasCache = int64(hit), int64(miss), true
			}
		}
		recordAttempt(acct.UID, usageDeltaFromResponse(resp), credit, hasCredit, attemptStarted, 0)
		noteCache(acct)
		writeJSON(w, http.StatusOK, resp)
		st.status = http.StatusOK
		st.outcome = reqlog.OutcomeSuccess
		st.toks = completionTokens(resp)
		// 非流式同理：聚合成功（无 error 帧、非空流）才算这一跳成功，事后才记成功/绑粘性。
		h.cfg.Pool.NoteSuccess(acct.UID)
		h.cfg.Pool.BlockModelClear(acct.UID, bareModel)
		if sessKey != "" && h.cfg.Session != nil {
			h.cfg.Session.Bind(sessKey, acct.UID)
		}
		// 成本账本（非流式）：从聚合响应的 usage 取 credit 与 token 总数。
		if hasCredit {
			st.credit = credit
			st.hasCredit = true
			h.cfg.Pool.NoteModelCost(acct.UID, bareModel, credit, total)
		}
		return
	}
	// 末端错误透传（error-passthrough）：上游返回的错误原样透传，不再规范化成固定文案。
	// 上游返回（*upstream.Error）→ error.message 装**上游 body 原文**（code/msg/
	// requestId 原样保留）。HTTP 状态码按 OpenAI 兼容口径映射类别：ErrSoftRate → 429
	// （限流语义、客户端应等待重试），其余保持 503。本地调度类错误（无可用账号/
	// 传输层抖动/非上游返回的 lastErr）→ 保留自有文案 no_healthy_account（本地错误
	// 没有上游原文可透传，不编造）。
	status := http.StatusServiceUnavailable
	code := "no_healthy_account"
	msg := "all accounts are temporarily unavailable, please retry later"
	// 上游超时：轮转已在传输层分支止损（见 isUpstreamTimeout），这里给一条**能区分**
	// 的文案，别混进"没有可用账号"——两者的排查方向完全不同。
	if errors.Is(lastErr, errUpstreamTimeout) {
		code = "upstream_timeout"
		msg = "upstream timed out: rotation stopped (another account would hit the same slow upstream), please retry later"
	}
	// 最后一个号的代理连不上（或号被代理池拦着）：客户端只知道是代理的问题，请求记录（含测活结果）写上
	// 是哪个代理、为什么。
	if upstream.IsRouteError(lastErr) {
		msg = "all accounts are temporarily unavailable: the proxy of the account could not be reached, please retry later"
		st.errMsg = lastErr.Error()
	}
	// gateway_hint（末端透传）：上游错误按 Kind + 原文 + 请求形态判定；本地调度类
	// 错误（无上游原文）固定 no_healthy_account hint。
	hint := upstream.NoHealthyAccountHint()
	if slot.timedOut && lastErr == nil {
		// 号都在、只是都忙（在途满或每分钟请求数到上限）：说清楚是忙，不是号坏了（见 waitSlot）。
		msg, hint = h.busyError(&slot)
	} else if keyGroups != nil && lastErr == nil {
		// 客户端密钥绑了分组：说清楚是这个密钥能用的号里没有能接的，别让人以为整个池子都挂了。
		msg = "no account in this API key's groups can take this request right now"
		hint = "key_groups: this API key only uses accounts in its groups; add accounts to them, or check the accounts there in the panel"
	}
	// upstreamMsgPassed 记录 error.message 是否已被上游原文占据：模型级阻塞分支
	// 据此决定要不要覆盖 msg（上游原文优先，含 requestId）。
	upstreamMsgPassed := false
	var ue *upstream.Error
	if errors.As(lastErr, &ue) {
		hint = h.hintOf(ue.Kind, ue.Msg, bareModel, reqHasImage, ue)
		switch ue.Kind {
		case upstream.ErrSoftRate:
			status = http.StatusTooManyRequests
			code = "rate_limit_exceeded"
			msg = "rate limited: all accounts are cooling down, please wait a moment and try again"
		case upstream.ErrWafBlock:
			if h.wafIP.active(wafExit) {
				// IP 级拦截措辞（fail-fast 终止路径）：网关出口 IP 被 WAF 拦截、
				// 轮转已止损、窗口过后自动解除。客户端提前重试无意义（换号不换 IP）；
				// 有上游原文时原文优先（下方统一）。
				code = "waf_ip_blocked"
				msg = "waf ip-level block: upstream firewall is blocking the gateway IP, rotation stopped; retry after the block window expires"
			}
		case upstream.ErrUpstreamBusy:
			// 6021 容量排队满（防御路径：正常已在轮转循环内 fail-fast 拦截）。503 +
			// 可重试语义——上游原文（下方统一透传）含 requestId，客户端按 SDK 退避重试即可。
			code = "upstream_busy"
			msg = "upstream capacity is full (queue.full); retry after a short wait"
		case upstream.ErrQueueUserLimit:
			// 6022 每个号排着队的请求都到上限了：同样是上游忙，不是网关没号。上游原文下方统一透传。
			code = "upstream_busy"
		case upstream.ErrModelBlocked:
			// 11102「该后端无此模型」：上游原文（下方统一透传）已经写清了原因，但
			// code 此前停在默认的 no_healthy_account —— 那是"服务端过载"的语义，
			// 客户端据此会不断重试（#81 里就是 503 → 客户端自动重试 5 次），而真正
			// 该做的是换个模型。用 400 + 明确的 code 把不可重试的性质讲清楚。
			code = "model_unavailable"
			status = http.StatusBadRequest
		}
		if s := strings.TrimSpace(ue.Msg); s != "" {
			// 上游原文优先：透传 code/msg/requestId，不拼接本地前缀。
			msg = s
			upstreamMsgPassed = true
		}
	}
	// 模型级阻塞覆盖上面的通用文案（放在最后 = 优先级最高）。
	//
	// 为什么能覆盖 code 而不丢信息：modelBlock.Reason 就是 BlockModelBackoff 存下的
	// 上游原因，已在 hint 里原样带出，所以覆盖 message 不会丢掉上游信息，反而补上
	// 了上游不会告诉客户端的两件事——「有几个号被挡」和「最早什么时候解封」。
	//
	// 用 400 而非 503：这是"你选的这个模型当前不可用"，不是"服务端暂时过载"。
	// 换号重试必然同样失败，客户端不该按可重试错误处理。
	// 例外：最早解封就在 modelBusyRetryWindow 内（14003「模型繁忙」这类半分钟的短冷却），
	// 回 429 + Retry-After 让客户端稍等重试，不叫它换模型。
	if modelBlock.Blocked {
		hint = fmt.Sprintf("model_blocked: %d account(s) cooling down or not taking this model", modelBlock.Count)
		if !modelBlock.Until.IsZero() {
			hint += "; earliest unblock at " + modelBlock.Until.Format(time.RFC3339)
		}
		if s := strings.TrimSpace(modelBlock.Reason); s != "" {
			hint += "; upstream: " + s
		}
		if wait := time.Until(modelBlock.Until); !modelBlock.Until.IsZero() && wait <= modelBusyRetryWindow {
			code = "rate_limit_exceeded"
			if !upstreamMsgPassed {
				msg = "model is busy on every account (short per-model cooldown), retry in a few seconds"
			}
			w.Header().Set("Retry-After", strconv.Itoa(int(max(wait, 0).Seconds())+1))
			status = http.StatusTooManyRequests
		} else {
			code = "model_unavailable"
			// 有上游原文时保留它（含 requestId，用户要拿去向上游反馈），只把"有几个号
			// 被挡、最早何时解封"这类本地调度信息放进 gateway_hint。
			if !upstreamMsgPassed {
				msg = "model is unavailable on every account (per-model cooldown), try another model"
			}
			status = http.StatusBadRequest
		}
	}
	writeOpenAIErrorHint(w, status, code, msg, hint)
	st.status = status
	st.outcome = reqlog.OutcomeHTTPError
}

// slotPoll 等空位时多久重看一次：冷却到期、恢复选号、调大在途上限都不会叫醒在等的请求，靠它兜底。
// 变量供测试缩短。
var slotPoll = time.Second

// slotWaitStarted 开始等空位时调用；供测试确认请求已经在等，生产为空操作。
var slotWaitStarted = func() {}

// slotWait 一个请求排队等在途名额的状态（见 waitSlot）。
type slotWait struct {
	since    time.Time // 第一次开始等的时刻，一个请求合计最多等 pool.in_flight_wait；零值 = 没等过
	woke     bool      // 这次选号是刚等完醒来的
	timedOut bool      // 等到上限仍没有空位，末端错误据此说明是号都在忙
	rpm      bool      // 等满时有号是因为每分钟请求数到上限才忙（pool.max_rpm），末端错误据此说明
	logged   bool      // 「等到空位」的日志已经记过
}

// waitSlot 选不到号时，看是不是只因为能接这个请求的号在途都满了（号都在、只是忙）：是就等到
// 有号放出名额（freed 要在选号前取），或者到了 slotPoll 重看一次，返回 true 让调用方再选一次号。
// 不等（设成 0）、等满上限、客户端断开，或者号用不了不是因为在途满（冷却、禁用、模型级冷却、
// 积分保底）时返回 false。粘性号满了照旧换号，只有所有能接的号都满了才等。
func (h *Handler) waitSlot(ctx context.Context, freed <-chan struct{}, sw *slotWait, tried map[string]bool, model, realm string) bool {
	wait := h.cfg.Pool.InFlightWait()
	if wait <= 0 || !h.cfg.Pool.ServableIgnoringInFlight(tried, model, realm) {
		return false
	}
	if sw.since.IsZero() {
		sw.since = time.Now()
	}
	left := wait - time.Since(sw.since)
	if left <= 0 {
		sw.giveUp(h.cfg.Pool, tried, model, realm)
		return false
	}
	// 每分钟请求数到上限的号不会 Release 叫醒人，靠 slotPoll 定时重看。
	t := time.NewTimer(min(left, slotPoll))
	defer t.Stop()
	slotWaitStarted()
	select {
	case <-freed:
	case <-t.C:
		if time.Since(sw.since) >= wait {
			sw.giveUp(h.cfg.Pool, tried, model, realm)
			return false
		}
	case <-ctx.Done():
		return false
	}
	sw.woke = true
	return true
}

// giveUp 等满上限：记下等满了，以及是不是有号因为每分钟请求数到上限而忙。
func (sw *slotWait) giveUp(p *pool.Pool, tried map[string]bool, model, realm string) {
	sw.timedOut = true
	sw.rpm = p.RPMBusy(tried, model, realm)
}

// busyError 号都在忙、等满仍没空位时回给客户端的说明与 gateway_hint（对话与搜索共用）。
func (h *Handler) busyError(sw *slotWait) (msg, hint string) {
	wait := h.cfg.Pool.InFlightWait()
	if sw.rpm {
		return fmt.Sprintf("all accounts are busy: every account that can serve this request is at its requests-per-minute or in-flight limit, no free slot after waiting %s, please retry later", wait),
			"rpm_full: raise pool.max_rpm / pool.max_rpm_global, add accounts, or retry later"
	}
	return fmt.Sprintf("all accounts are busy: every account that can serve this request is at its in-flight limit, no free slot after waiting %s, please retry later", wait),
		"in_flight_full: raise pool.max_in_flight / pool.max_in_flight_global, add accounts, or retry later"
}

// keyScope 这次请求一开始就不选的号和客户端密钥限定的分组：密钥绑了分组时是组外的号和那些分组，
// 否则是空 map 和 nil。
func (h *Handler) keyScope(ctx context.Context) (map[string]bool, []string) {
	if k := principalFrom(ctx).key; k != nil && len(k.Groups) > 0 {
		return h.cfg.Pool.UIDsOutsideGroups(k.Groups), k.Groups
	}
	return map[string]bool{}, nil
}

// pickWarm 换号时先回到这段对话最近成功用过、这次还没试过的号（见 session.Router.WarmUIDs），
// 校验同粘性号（PickByUIDForModel：该模型可用、在途未满、积分保底），也只回此刻最高的优先级层里的号。
// 粘性关闭、没有会话键或这些号都用不了时返回 nil。
func (h *Handler) pickWarm(sessKey, model, realm string, tried map[string]bool) *auth.Auth {
	if h.cfg.Session == nil || sessKey == "" {
		return nil
	}
	for _, uid := range h.cfg.Session.WarmUIDs(sessKey) {
		if tried[uid] || !h.cfg.Pool.InTopTier(uid, model, realm, tried) {
			continue
		}
		if a := h.cfg.Pool.PickByUIDForModel(uid, model); a != nil && (realm == "" || a.Realm() == realm) {
			return a
		}
	}
	return nil
}

// tokensPerSecond 计算吐字速率（token/s），返回 (速率, 是否有意义)。
//
// 分母用「生成耗时」= 端到端耗时 - 首 token 等待（TTFB），不是端到端耗时。
//
// 为什么要减：不减的话，首 token 等待越长、报告速率被压得越低。同一模型换个
// 上游或网络，TTFB 从 0.3s 涨到 2s，速率能凭空掉一半——读起来像"模型变慢了"，
// 其实只是排队久了。issue #34 报的正是这个，维护者也确认「速率计算时并没有减去
// 首token到达时间」。
//
// ttfb<=0 表示没有观测：非流式回复天然没有「首个 data 帧」（日志里那一列记的是
// "-"）。此时不猜、不扣——凭空假定一个 TTFB 会把分母推向零、把速率抬成虚高，
// 比不扣更糟。只有真测到才扣。
//
// 同理，ttfb 不小于总耗时时（时钟粒度、或 TTFB 落在计时终点之后）退回端到端耗时，
// 避免零/负分母。token 数为负哨兵值（-1 = 观测缺失）时返回 false。
//
// 扣除后不足 minGenWindow 也退回端到端（issue #127）：ttfb 量的是「首个 SSE 帧
// 到达」，当上游把整个响应攒到最后一次性下发（假流式/中间层攒批刷新——首帧与
// 末帧几乎同时到）时，total−ttfb 只剩几毫秒，拿它当分母会把几百 token 除成
// 上万 tok/s 的幻数。这种形态下「生成时长」根本不可测，诚实的分母只有端到端。
//
// 用量账本（handler）与控制台流水行（logging.go）都走这一个函数：两处各算一遍时
// 口径漂移过一次（流水行漏扣 TTFB、与面板数字对不上），共用是防再次分叉的唯一办法。
func tokensPerSecond(completionTokens int64, total, ttfb time.Duration) (float64, bool) {
	if completionTokens < 0 || total <= 0 {
		return 0, false
	}
	gen := total
	if ttfb > 0 {
		if g := total - ttfb; g >= minGenWindow {
			gen = g
		}
	}
	return float64(completionTokens) / gen.Seconds(), true
}

// minGenWindow 可信生成窗口的下限：扣除 TTFB 后剩余窗口不足该值即视为「生成
// 时长不可测」，退回端到端耗时（见 tokensPerSecond 注释，issue #127）。
// 真流式下首帧到末帧通常铺满剩余窗口，200ms 远低于正常生成时长，不影响真实
// 快速输出（如缓存命中后的爆发）被如实报告。
const minGenWindow = 200 * time.Millisecond

// promptTooLongMessage 11115 透传 message：上游 body 原文（含真实 token 数/
// 上限值/requestId，客户端自行排查）；空 body 兜底为可读分类短文案（不编造原文）。
func promptTooLongMessage(body string) string {
	if strings.TrimSpace(body) == "" {
		return "prompt is too long"
	}
	return body
}

// usageCreditTotal 从聚合响应取 usage.credit 与 total_tokens（成本台账非流式入口）。
// 任一字段缺失/非法 → ok=false（不记录）。
func usageCreditTotal(resp map[string]any) (credit float64, total int, ok bool) {
	usage, _ := resp["usage"].(map[string]any)
	if usage == nil {
		return 0, 0, false
	}
	c, _ := usage["credit"].(float64)
	t, _ := usage["total_tokens"].(float64)
	if t <= 0 {
		return 0, 0, false
	}
	return c, int(t), true
}

// rotateBackoff 轮转间指数退避 + 抖动（WAF 403 修复 P0-2）：第 i 次轮转失败
// （continue 换号前）等待 backoffAfter(i)（500ms·2^i 封顶 8s，±25% 抖动），
// ctx 取消（客户端断连/优雅停机）返回 false——调用方立即终止轮转（客户端已走，
// 换号重试无意义）。退避是「换号前歇一下」让上游频控窗口滑过；正常单号请求
// （首次成功）不经过本函数，零开销。
func rotateBackoff(i int, ctx context.Context) bool {
	d := backoffAfter(i)
	if d <= 0 {
		return ctx.Err() == nil
	}
	if !sleepCtx(ctx, d) {
		log.Printf("WARN: [server] rotate backoff aborted: ctx cancelled")
		return false
	}
	return true
}

// errUpstreamTimeout 上游超时的哨兵：末端出口据此给出与「没号可用」可区分的文案。
var errUpstreamTimeout = errors.New("upstream timeout")

// isUpstreamTimeout 判断这一跳的失败是否属于「上游超时 / 停滞」。超时不是账号的
// 问题，换号注定白换（同一份请求撞同一个慢上游），必须止损：不轮转、不罚号。
// 三态判定见传输层错误分支的注释。
func isUpstreamTimeout(err error, clientGone bool) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	return !clientGone && errors.Is(err, context.Canceled)
}

// applyErrorPolicy 按错误分类对账号施加冷却/禁用/熔断策略（最终版状态机）。
// kind 是唯一权威分类（来自 upstream.Classify / ChatStreamContext 的 *Error 信封），
// 此处不再按原始 status 二次判断。仅在 chatCompletions 轮转循环内调用：内容拦截
// 会立即 400 返回，其余种类 continue 换号（continue 前由 rotateBackoff 退避）。
//
// 各条路径，各司其职：
//   - ErrHardCredit → CooldownUntilTomorrow4AM：即时硬冷却到次日 04:00（等签到恢复）。
//   - ErrSoftRate → 优先对齐上游重置墙钟（带「将在 … 重置」时 6004 走模型级豁免、
//     非 6004 走账号级，均不指数堆加）；无重置时间才走有界退避。冷却时长优先采信
//     Retry-After 头（uerr.RetryAfter，body 文案墙钟之外的头形态来源）。
//   - ErrWafBlock → 账号级软冷却：**不 Disable**——WAF 403 是 IP/指纹维频控信号，
//     罚过即走、到期自愈。时长优先 Retry-After 头；缺失按 wafCooldownBase(60s)
//     起 · softStreak 指数、封顶 soft_rate_max 的既有 CooldownSoftRate 有界退避。
//     基数经 jitterDur 抖动（防多账号同相位冷却到期再聚团）。
//   - ErrNotFound → Cooldown(CoolSoft, notFoundCooldown 固定 60s)：短冷却防雪崩。
//   - ErrSessionDead → Disable：session 死亡，永久禁用（需人工重登）。
//   - ErrContentBlocked → 不罚账号；passthrough 首遇触发降级重试，最终仍拦则回 400。
//   - ErrBadParams → 不罚账号，且与 ErrPromptTooLong/ErrImageInvalid 同待遇：
//     调用方在轮转循环内即刻 400 透传原文终止（换号必然同样失败）。
//   - ErrPromptTooLong → 11115：请求的问题不是账号的问题。零动作（不冷却/不熔断/
//     不 NoteError、不喂连败），chatCompletions 已直接透传原文返回不轮转。
//   - ErrImageInvalid → 图片格式/数据无效：请求的问题不是账号的问题（同一 body
//     换任何号都会得到相同的解析错误）。零动作（不冷却/不熔断/不 NoteError、
//     不喂连败），chatCompletions 已直接透传原文返回不轮转。
//   - ErrModelBlocked → BlockModelBackoff：(账号, 模型) 11102/11158 负缓存避让。
//   - ErrUpstreamBusy（6021）、ErrQueueWaiting（6020）、ErrQueueUserLimit（6022）→ 上游这一路忙，
//     零动作；6021 和没排到号的 6020 回 503 不轮转，6022 换号。
//   - ErrServer → NoteError：喂单一连续失败计数器 fails + 累计错误 errTotal，
//     达到 breakerThreshold 触发熔断（指数退避）。
//   - 其他（default：ErrClient/ErrNone）→ 只换号不罚（防雪崩），不喂熔断；ErrClient
//     额外喂连败计数（NoteFailures，issue #114）：未知 4xx 连败 N 次临时出池。
//
// body 仅在 ErrSoftRate/ErrAccountFault 分支用于解析重置时间/分野；model 为请求
// 携带的模型名。uerr 是 ChatStreamContext 返回的分类信封（可携带 RetryAfter）；
// 零值/防御路径下为 nil，冷却时长回落既有计算。
func (h *Handler) applyErrorPolicy(uid string, kind upstream.ErrKind, body, model string, uerr *upstream.Error) {
	switch kind {
	case upstream.ErrHardCredit:
		// 402 + 余额关键词即积分耗尽：同步冷却到次日 04:00（签到任务 09/21 点恢复），
		// 不需要异步核查（冗余）。立即换号。
		h.cfg.Pool.CooldownUntilTomorrow4AM(uid, "余额不足")
	case upstream.ErrSoftRate:
		// 统一对齐上游重置时间：只要 body 带「将在 … 重置」，无论业务 code 是
		// 6004 还是 11140 rate-limiting 等形态，都精确冷却到该墙钟、绝不指数堆加。
		//   - 模型级（6000–6008 按模型限额、14003 模型繁忙）→ CooldownSoftForModel：
		//     写 modelCooldowns[model]，切模型豁免。
		//   - 账号级（其余）→ CooldownSoftRate：写账号级 until，不产生模型豁免。
		modelRateLimited := upstream.IsModelRateLimit(body)
		transient := upstream.IsTransientRateLimit(body)
		reason := upstream.RateLimitReason(body)
		if resetAt, ok := upstream.ParseRateReset(body); ok {
			if modelRateLimited || transient {
				h.cfg.Pool.CooldownSoftForModel(uid, h.softCooldown(), resetAt, model, reason)
				return
			}
			h.cfg.Pool.CooldownSoftRate(uid, h.softCooldown(), resetAt, "429 rate limit")
			return
		}
		// 短时限流（14003、每秒/每分钟限额）没有重置时间：只冷却这个号的这个模型半分钟
		// （上游给了 Retry-After 就用它）。不知道模型名时退回同样时长的账号级冷却。
		if transient {
			d := jitterDur(transientRateCooldown)
			if uerr != nil && uerr.RetryAfter > 0 {
				d = uerr.RetryAfter
			}
			if model == "" {
				h.cfg.Pool.Cooldown(uid, pool.CoolSoft, d, reason)
				return
			}
			h.cfg.Pool.CooldownModel(uid, model, d, reason)
			return
		}
		// 10105 会话太多：账号级短冷却，不走指数退避。
		if upstream.IsActiveSessionLimit(body) {
			d := jitterDur(activeSessionCooldown)
			if uerr != nil && uerr.RetryAfter > 0 {
				d = uerr.RetryAfter
			}
			h.cfg.Pool.Cooldown(uid, pool.CoolSoft, d, reason)
			return
		}
		// body 无重置文案但带 Retry-After 头 → 冷却到该时刻（不做指数堆加）。
		// 头优先于「有界退避」，但低于 body 重置文案（文案是上游更权威的口径）。
		if uerr != nil && uerr.RetryAfter > 0 {
			h.cfg.Pool.CooldownSoftRate(uid, h.softCooldown(), time.Now().Add(uerr.RetryAfter), "429 rate limit (retry-after)")
			if modelRateLimited {
				h.cfg.Pool.RecordModelRateLimitAudit(uid, model, reason+" (reset unknown)")
			}
			return
		}
		// 无重置时间 → 账号级有界退避（soft_rate 基数起、softStreak 翻倍、封顶
		// soft_rate_max；已在冷却中的兜底探测不翻倍）。基数取 h.softCooldown()
		// （热改优先），管理面板改 soft_rate 后立即生效。
		h.cfg.Pool.CooldownSoftRate(uid, h.softCooldown(), time.Time{}, "429 rate limit")
		if modelRateLimited {
			h.cfg.Pool.RecordModelRateLimitAudit(uid, model, reason+" (reset unknown)")
		}
	case upstream.ErrWafBlock:
		// WAF 403（无业务信封拦截形态）。软冷却复用 CooldownSoftRate 家族：基数
		// wafCooldownBase（60s，抖动后落 [45s,75s]）、softStreak 指数升级、封顶
		// soft_rate_max、冷却中兜底探测不翻倍——全部继承既有语义。
		// Retry-After 头优先（WAF 拦截页可能带该头）。不 Disable。
		if uerr != nil && uerr.RetryAfter > 0 {
			h.cfg.Pool.CooldownSoftRate(uid, jitterDur(wafCooldownBase), time.Now().Add(uerr.RetryAfter), "waf 403 block (retry-after)")
			return
		}
		h.cfg.Pool.CooldownSoftRate(uid, jitterDur(wafCooldownBase), time.Time{}, "waf 403 block")
	case upstream.ErrSessionDead:
		h.cfg.Pool.Disable(uid, "12153 session dead")
	case upstream.ErrNotFound:
		// 404 短冷却（软冷却），防雪崩。固定 notFoundCooldown，不随 soft_rate 退避：
		// 偶发路径缺失不是限流信号，不该按限流惩罚升级。
		h.cfg.Pool.Cooldown(uid, pool.CoolSoft, notFoundCooldown, "upstream 404")
	case upstream.ErrAccountFault:
		// 账号级授权/配额故障按 msg 分野（口径与 Classify 的 accountFaultMarkers 一致）：
		//   - "request illegal"（code 11140）→ 账号被上游风控限制（「账号存在异常行为」）：
		//     硬禁用（Disable）。只封对话、token 仍有效，重新登录一般解不开，要去官方申诉；
		//     同时记入风控波（NoteRestriction），同域短时间内多个号被限就整域暂停。
		//   - 14017（trial not activated）→ register 未完成，补完 register 后可能自愈，
		//     **保持软冷却**（禁用会让用户补完 register 后仍无法用）。14015 授权过期、
		//     14016 企业未激活同样软冷却，原因文案带上业务码。
		// 大小写不敏感（与 Classify 的 marker 匹配同口径）。
		if strings.Contains(strings.ToLower(body), "request illegal") {
			// 已经禁用的号（测活时）再回 11140 不是新的风控，不记风控波：不然复测两个早就被限制的号
			// 就会把整个域其余的号暂停。
			before, _ := h.cfg.Pool.Status(uid)
			h.cfg.Pool.Disable(uid, "account restricted by upstream (11140 request illegal); "+
				"appeal via official WorkBuddy support, re-login usually does not help")
			if !before.Disabled {
				h.cfg.Pool.NoteRestriction(uid)
			}
			return
		}
		h.cfg.Pool.Cooldown(uid, pool.CoolSoft, h.softCooldown(), upstream.AccountFaultReason(body))
	case upstream.ErrServer:
		// 5xx 上游故障：Classify 已把 ≥500 判为 ErrServer，在此喂熔断计数（不再手写 status>=500）。
		h.cfg.Pool.NoteError(uid)
	case upstream.ErrContentBlocked:
		// 内容策略拦截（误报）：内容问题非账号问题，不罚账号（无冷却/熔断/NoteError）。
		// passthrough 模式由 chatCompletions 内降级重试处理；custom 模式本不会到此分支。
	case upstream.ErrPromptTooLong:
		// 11115「prompt is too long」：请求的问题不是账号的问题（同一 body 换任何
		// 号都超限）。零动作（不冷却/不熔断/不 NoteError，同 ErrContentBlocked 待遇），
		// chatCompletions 已直接透传原文返回不轮转——该分支只为文档完备。
	case upstream.ErrImageInvalid:
		// 图片格式/数据无效：请求的问题不是账号的问题（同一 body 换任何号都会
		// 得到相同解析错误）。零动作，chatCompletions 已 fail-fast 透传。
	case upstream.ErrBadParams:
		// 请求体解析失败（400 + Unmarshal chat params failed / 11101）：发给上游的 body
		// 有问题（网关侧不再截断，均为客户端畸形 JSON）。换了账号照样 400，
		// 不罚账号（无冷却/熔断/NoteError，同 ErrContentBlocked 待遇）；chatCompletions
		// 已 fail-fast 400 透传原文、终止轮转——「换号可能有不同模型权限」属 11102
		// （ErrModelBlocked）的分类域，与本类无关。
	case upstream.ErrUpstreamBusy:
		// 6021 容量排队满：账号余额健康、未限流、session 未死，换号也撞同一堵墙。
		// 零动作（不冷却/不熔断/不 NoteError/不喂连败，同 ErrContentBlocked 待遇）；
		// 非流式已 fail-fast 503 透传原文、终止轮转，流式 error 帧走到这里也不罚号。
	case upstream.ErrQueueWaiting, upstream.ErrQueueUserLimit:
		// 6020 排进容量队列（在号上排过队、没等到才会走到这里）、6022 这个号排队的数量到上限：都是上游
		// 这一路忙，账号本身健康。零动作（不冷却/不熔断/不喂连败）；6022 换号由轮转做。
	case upstream.ErrModelBlocked:
		// 11102「该后端无此模型」：(账号, 模型) 负缓存避让。复用 modelCooldowns 机制（与 6004 同域），
		// 选号侧 healthyForModel 对该账号自动避开该模型。
		// 11158「该账号类型无此模型权限」按账号类型定死，等多久都一样：加进号不接的模型名单，一直屏蔽。
		// 两种都立即换号（本轮 continue），下次选号避开。
		if reason := upstream.ModelBlockReasonFor(body); reason == upstream.ModelForbiddenReason {
			h.cfg.Pool.BlockModelForever(uid, model, reason)
		} else {
			h.cfg.Pool.BlockModelBackoff(uid, model, reason)
		}
	default:
		// 其余（ErrClient/ErrNone）：只换号不罚（防雪崩），不喂熔断。
		// ErrClient（未知 4xx）喂连败计数（issue #114）：连续 N 次该形态失败 →
		// 账号临时出池（NoteFailures 达阈降权），单次/偶发不罚（不误伤）。ErrNone
		// 到这里属防御路径（status>=400 但分类成功），语义不明不喂。
		if kind == upstream.ErrClient {
			h.cfg.Pool.NoteFailures(uid)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func writeOpenAIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "api_error",
			"code":    code,
		},
	})
}

// wafCooldownBase WAF 403 软冷却基数（建议 60s 起；抖动 ±25% 后落 [45s,75s]，
// 实际进入 CooldownSoftRate 后再按 softStreak 指数、封顶 soft_rate_max）。
// 与 SoftCooldown 分流的原因：WAF 403 是 IP/指纹维频控，信号比 429「账号级限流」轻
// （账号本身健康），但比 404 重（带粘性会连环）；60s 级的快速避让已足够让频控窗口
// 滑过。抖动复用 backoff.go jitterDur（单一来源）。
const wafCooldownBase = 60 * time.Second

// writeOpenAIErrorHint 同 writeOpenAIError，另在 error 对象上附加
// error.gateway_hint（hint 为空串时不带字段——未覆盖形态不编造）。
// message 仍是上游原文透传（hint 只做并列补充，绝不替换/包装 message）。
func writeOpenAIErrorHint(w http.ResponseWriter, status int, code, msg, hint string) {
	if hint == "" {
		writeOpenAIError(w, status, code, msg)
		return
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message":      msg,
			"type":         "api_error",
			"code":         code,
			"gateway_hint": hint,
		},
	})
}

// hasImagePart 报告聊天请求体是否携带多模态 image_url part（OpenAI 兼容形态
// messages[].content[] {type:"image_url"}）。畸形/其他形态一律 false（hint 侧
// 宁缺勿滥：判不出带图就不给「模型不支持图片」指向）。
func hasImagePart(body []byte) bool {
	var peek struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &peek) != nil {
		return false
	}
	for _, m := range peek.Messages {
		for _, p := range m.Content {
			if p.Type == "image_url" {
				return true
			}
		}
	}
	return false
}

// hintContext 组装 chatCompletions 的 gateway_hint 判定上下文：请求裸模型名 +
// 是否带图 + 模型目录 supports_images 声明（目录未收录 → ModelInCatalog=false，
// 不做「不支持」判定，防查不到误判）。仅错误路径调用（成功请求零开销）。
//
// 目录查询只读既有缓存快照（cachedModelsSnapshot），**不触发上游拉取**：错误路径
// 加一次 FetchModels 网络调用既拖慢错误响应、又污染上游调用语义（错误风暴时放大
// 请求量——与 WAF IP fail-fast 的「不放大请求量」哲学相悖）。缓存冷（最近 10min 未
// 拉过）→ ModelInCatalog=false，11133 退中性 hint（宁缺勿滥，不编造能力事实）。
func (h *Handler) hintContext(bareModel string, hasImage bool) upstream.HintContext {
	ctx := upstream.HintContext{Model: bareModel, HasImage: hasImage}
	if bareModel == "" {
		return ctx
	}
	for _, mi := range cachedModelsSnapshot() {
		if mi.ID == bareModel {
			ctx.ModelInCatalog = true
			ctx.ModelSupportsImages = mi.SupportsImages
			return ctx
		}
	}
	return ctx
}

// hintOf 末端错误透传的统一 hint 入口：kind + 上游原文 + 请求上下文 →
// gateway_hint 文案（upstream.GatewayHint 单一事实来源）。uerr 为 nil 时回落
// body 原文判定（防御路径）。transport 层错误（lastErr 非 *upstream.Error 且
// 上游没回 body）→ 无 hint（不编造）。
func (h *Handler) hintOf(kind upstream.ErrKind, body, bareModel string, hasImage bool, uerr *upstream.Error) string {
	msg := body
	if uerr != nil && uerr.Msg != "" {
		msg = uerr.Msg
	}
	return upstream.GatewayHint(kind, msg, h.hintContext(bareModel, hasImage))
}
