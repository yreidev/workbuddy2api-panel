// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// config.go 加载 JSON 配置 + 环境变量覆盖。
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/prompt"
)

// Config 顶层配置。
type Config struct {
	Listen    string `json:"listen"`     // ":7863"
	APIKey    string `json:"api_key"`    // 空 = 不鉴权
	AuthDir   string `json:"auth_dir"`   // ./auths
	StateFile string `json:"state_file"` // ./data/state.json

	// DNSServers 出站解析只问这几台 DNS 服务器（IP 或 IP:端口，没写端口用 53），按顺序试；空 = 系统解析器。
	// 用在没有 /etc/resolv.conf 的环境（Android、精简容器）。WB2A_DNS（逗号分隔）覆盖。需重启。
	DNSServers []string `json:"dns_servers,omitempty"`

	// ModelRoutes 模型路由：客户端发来的模型名不在上游目录里时，从上往下找第一条 match
	// 匹配的规则（只认 * 通配，不区分大小写），改用它的 target。三个对话入口都生效，
	// 带 cn: / global: 前缀的名字不改；target 可以带前缀。缺省为空（不路由）。热生效。
	ModelRoutes []livecfg.ModelRoute `json:"model_routes"`

	Models struct {
		// Hidden 不对外的模型：/v1/models 不列，点名调用回 400 model_unavailable。带 cn: / global:
		// 前缀的只藏那个域，裸名两个域都藏；精确匹配、区分大小写。按经模型路由改写后实际发往
		// 上游的模型判，和客户端密钥的模型白名单叠加。缺省为空。热生效。
		Hidden []string `json:"hidden"`
	} `json:"models"`

	Panel struct {
		// PackageDetailLimit 积分构成页单账号默认展示的最近到期包数；<=0 回落 5。
		PackageDetailLimit int `json:"package_detail_limit"`
		// UpdateCheck 每 6 小时读取一次 GitHub 上的更新日志，有新版本时面板提醒；缺省 true。
		// 热生效（经 livecfg 快照）。
		UpdateCheck bool `json:"update_check"`
	} `json:"panel"`

	Logging struct {
		// 日志留存：请求记录、任务日志、系统日志、积分获取各自一个开关、保留天数、容量上限，用量统计只设
		// 保留天数。保留天数 0 = 永久保留（容量上限照样生效），负值回落默认；容量 <=0 回落默认。
		// 全部热生效（面板保存配置即生效）。

		// RequestArchiveEnabled 请求元数据 JSONL 归档开关，缺省 true。
		RequestArchiveEnabled bool `json:"request_archive_enabled"`
		// RequestRetentionDays 请求归档保留天数，缺省 7。
		RequestRetentionDays int `json:"request_retention_days"`
		// RequestArchiveMaxMB 请求归档总上限（MiB），缺省 100。
		RequestArchiveMaxMB int `json:"request_archive_max_mb"`
		// RequestClientInfo 是否在请求日志（归档事件 + stdout 流水行 + 面板运行
		// 日志）里记录调用来源：客户端 IP 与 User-Agent。缺省 true。
		//
		// 为什么做成开关而不是恒开：来源信息是排查"谁在打网关"的第一手线索，
		// 但它比 token 计数敏感（IP 属个人信息），共享部署/多租户场景可能需要
		// 关掉。关闭后 Event.ClientIP/UserAgent 保持为空，归档里不出现该字段。
		// 热生效（经 livecfg 快照），无需重启。
		RequestClientInfo bool `json:"request_client_info"`

		// TaskLogEnabled 任务日志（运行日志的任务频道：签到、领奖、任务中心等）落盘留存，缺省 true。
		TaskLogEnabled bool `json:"task_log_enabled"`
		// TaskLogRetentionDays 任务日志保留天数，缺省 30。
		TaskLogRetentionDays int `json:"task_log_retention_days"`
		// TaskLogMaxMB 任务日志总上限（MiB），缺省 20。和系统日志分开设，免得系统日志把任务日志挤掉。
		TaskLogMaxMB int `json:"task_log_max_mb"`
		// SystemLogEnabled 系统日志（运行日志的系统频道：启动、热更新、冷却风控、告警等）落盘留存，缺省 true。
		SystemLogEnabled bool `json:"system_log_enabled"`
		// SystemLogRetentionDays 系统日志保留天数，缺省 30。
		SystemLogRetentionDays int `json:"system_log_retention_days"`
		// SystemLogMaxMB 系统日志总上限（MiB），缺省 50。
		SystemLogMaxMB int `json:"system_log_max_mb"`
		// UsageRetentionDays 用量统计保留天数，缺省 0 = 永久保留。
		UsageRetentionDays int `json:"usage_retention_days"`
		// CreditLogEnabled 积分获取流水（签到、领奖、补偿、旅行、新手礼包、连登兑换领到的积分）落盘留存，缺省 true。
		CreditLogEnabled bool `json:"credit_log_enabled"`
		// CreditLogRetentionDays 积分获取流水保留天数，缺省 0 = 永久保留（一天也就几十条）。
		CreditLogRetentionDays int `json:"credit_log_retention_days"`
		// CreditLogMaxMB 积分获取流水总上限（MiB），缺省 20。
		CreditLogMaxMB int `json:"credit_log_max_mb"`
	} `json:"logging"`

	Server struct {
		// ReadTimeout 入站请求读取（含 body 上传）总时长上限（issue #100）。
		// http.Server 的 ReadTimeout 覆盖整个请求读取：大上下文/文件块请求经
		// 反代链转发时上传可超过旧固定值 60s，被掐后客户端拿到
		// 400 "read body: ... i/o timeout"。缺省 "300s"；"0" = 不限制
		//（慢速 body 可无限占用连接，自担风险）；改动需重启进程。
		ReadTimeout string `json:"read_timeout"` // "300s"；"0" = 不限制
	} `json:"server"`

	Cooldown struct {
		// hard_credit / err_threshold / err_cooldown 三个历史键已退役：
		// 硬冷却固定为次日 04:00（CooldownUntilTomorrow4AM），连续错误语义并入熔断器。
		// 旧 config 中的这些键因 JSON 未知字段而自然忽略，不报错。
		SoftRate string `json:"soft_rate"` // "600s"，软限流冷却基数
		// SoftRateMax 软冷却指数退避的封顶，默认 "2h"。
		// 空值回落默认，非法值报错（处理风格同 soft_rate）。
		SoftRateMax string `json:"soft_rate_max"` // "2h"
	} `json:"cooldown"`

	Schedule struct {
		CheckinHours   []int `json:"checkin_hours"`   // [9,21]
		TravelHours    []int `json:"travel_hours"`    // [9,21]
		ActivityHours  []int `json:"activity_hours"`  // [10]
		KeepaliveHours []int `json:"keepalive_hours"` // [22]
		BlackcatHours  []int `json:"blackcat_hours"`  // [23] 夜猫子窗口（23:00–08:00 计数）
		GrowthHours    []int `json:"growth_hours"`    // [1] 成长任务队列（Sequential 族每日零点解锁，01:00 自动扫描执行）
		// CheckinEnabled/TravelEnabled/ActivityEnabled/KeepaliveEnabled/BlackcatEnabled 显式禁用开关（缺省 true）。
		//
		// 为什么用独立 bool 而不是空数组/哨兵值表意"禁用"：
		//   - 空数组与 null 在老语义里已被"未配置 → 回落默认"占用，改判会静默翻转
		//     所有老 config 的行为（用户只想删掉一行，结果关掉了签到）；bool 缺省 true
		//     则对老配置零影响，向后完全兼容。
		//   - 开关与取值解耦：禁用时仍保留用户显式配的小时，重新启用无需补配。
		//   - 无需猜测哨兵（[-1] 之类），非法小时一律报错并提示改用本开关。
		// 旧 config 里的该键因 JSON 未知字段而自然忽略，不报错。
		CheckinEnabled   bool `json:"checkin_enabled"`   // 缺省 true；false = 关签到
		TravelEnabled    bool `json:"travel_enabled"`    // 缺省 true；false = 完全停猫猫旅行
		ActivityEnabled  bool `json:"activity_enabled"`  // 缺省 true；false = 停活跃上报
		KeepaliveEnabled bool `json:"keepalive_enabled"` // 缺省 true；false = 关 token 保活
		BlackcatEnabled  bool `json:"blackcat_enabled"`  // 缺省 true；false = 关夜猫子
		GrowthEnabled    bool `json:"growth_enabled"`    // 缺省 true；false = 关成长任务自动排程

		// IncludeDisabledInTasks 让「保号类」定时任务（签到 / 活跃上报 / token 保活 /
		// 余额刷新）对**已禁用（disabled）**的账号也执行。
		//
		// 为什么需要它：面板「禁用」的语义是「不再参与选号」（见面板确认文案），但这四类
		// 任务此前一律 `if st.Disabled { continue }`，等于把「停用流量」放大成「停止一切
		// 上游保号行为」——被禁用的号拿不到签到积分、不续 token、余额也不再刷新；而
		// ReenableIfCredits 明确不复活 disabled 账号（见 pool.state.go），于是签到这条唯一
		// 的自动回血路径也断了，账号只能靠人工「解冻」回来。
		//
		// 对「一次只放开一个号、用禁用做流量开关」的轮换用法（同 IP 多号防风控），闲置
		// 待命的号恰恰是最需要签到的那批——本开关即为该用法提供出口。
		//
		// 缺省 false = 保持既有行为，对老配置零影响。打开后禁用号仍会签到 / 保活，但
		// **依旧不参与选号**：pool 选号侧的 disabled 过滤不受本开关影响。
		IncludeDisabledInTasks bool `json:"include_disabled_in_tasks"`

		// 余额后台周期刷新：两次签到时点之间 credits 也能保持新鲜（面板/状态观测用）。
		// 解冻语义同签到（余额 > 0 的冷却账号自动解冻），但不做签到不刷 token。
		BalanceRefreshEnabled bool `json:"balance_refresh_enabled"` // 缺省 true；false = 关闭
		BalanceRefreshMinutes int  `json:"balance_refresh_minutes"` // 缺省 5；<=0 回落 5

		// 定时测活：每隔一段时间给整个号在限流冷却、熔断中的号发一条最短的真实对话，测通就提前恢复
		// （见 scheduler/liveness.go）。余额不足、风控、禁用、暂停选号的号不测。
		LivenessEnabled bool `json:"liveness_enabled"` // 缺省 true；false = 关闭
		LivenessMinutes int  `json:"liveness_minutes"` // 缺省 30；<=0 回落 30
	} `json:"schedule"`

	Global struct {
		// Enabled global realm 路由开关。缺省 true：Realm() 正常把 realm=global/
		// domain=workbuddy.ai 的账号判为 global 并路由 global base/路径。
		// 显式 "enabled": false 关闭（逃生门，纯 CN 锁定：即便 auth 写了 realm=global
		// 也不路由，auth.Realm() 双保险的第一道闸）。纯 CN 部署行为不变：CN 账号
		// 恒判 cn，global base 只在 realm=global 的账号上被使用。
		Enabled bool `json:"enabled"`
		// ChatBase / BillingBase 国际版上游 base 覆盖；空 = 回落内置默认
		// https://www.workbuddy.ai（internal/upstream.defaultGlobalBase）。
		ChatBase    string `json:"chat_base"`
		BillingBase string `json:"billing_base"`
	} `json:"global"`

	Upstream struct {
		// TimeoutSeconds 短 RPC（refresh/checkin/balance/FetchModels）总时长上限，默认 120。
		TimeoutSeconds int `json:"timeout_seconds"`
		// HeaderTimeoutSeconds 聊天 SSE 首字节前（响应头）上限；<=0 回落 TimeoutSeconds。
		HeaderTimeoutSeconds int `json:"header_timeout_seconds"`
		// IdleTimeoutSeconds 聊天 SSE 流中空闲上限（活跃吐数据续命不掐）；<=0 回落默认 300。
		IdleTimeoutSeconds int `json:"idle_timeout_seconds"`
		// UserAgent 出站 User-Agent 显式覆盖：非空时对话、刷新令牌、模型目录、积分、签到、
		// 成长任务、上报都用它（任务中心的桌面端上报除外）。空 = 按账号域取官方桌面端的 UA：
		// 内置 CLI 发的请求 `WorkBuddy/<ver> <applicationName>/<ver> CLI/<cli>`，后台服务发的
		// 请求前两段对调（applicationName 国内版 WorkBuddy、国际版 workbuddy-ai）。
		UserAgent string `json:"user_agent"`
		// ClientVersion 桌面端版本（UA 的版本段与 X-IDE-Version），两域共用一个值。
		// 空 = 内置默认：国内版 5.9.3、国际版 5.9.4（官方安装包）。
		ClientVersion string `json:"client_version"`
		// CliVersion 出站 UA 中 `CLI/<ver>` 段的版本。空 = 内置默认 2.165.0（两域桌面端内置的 CLI）。
		CliVersion string `json:"cli_version"`
		// ClientName 对话请求的归属头 X-IDE-Name / X-IDE-Type 取值。空 = "WorkBuddy"，同官方
		// 桌面端（带 X-Agent-Purpose 与 X-IDE-*）；配 "SaaS" 还原旧行为：对话不带这几个头，
		// 后台服务的请求不设 UA。X-Product 不受影响，固定为官方的 "SaaS"。
		ClientName string `json:"client_name"`
		// DeviceToken 设备风控 Token（X-Device-Token 头）全局兜底；空 = 不注入。官方只在签到请求上
		// 带它，网关同样只在签到（和小程序接口）上带。每号 auth 文件的 device_token 键优先于本项。
		DeviceToken string `json:"device_token"`
		// DeviceTokenFile device token 文件路径兜底（宿主落盘的桌面端 token，5 分钟读取缓存）。
		DeviceTokenFile string `json:"device_token_file"`
		// PassthroughIP 是否透传客户端 IP 给上游（默认 false，反代安全边界）。
		PassthroughIP bool `json:"passthrough_ip"`
		// ModelOptimization 允许上游用对话做模型优化（默认 false）：false 时对话一律带
		// X-Private-Data: true；true 时照官方客户端默认，个人号 false、企业号 true。面板保存即生效。
		ModelOptimization bool `json:"model_optimization"`
		// TLSFingerprint 出站是否把 TLS 握手伪装成官方桌面端客户端的 ClientHello（默认 true）。
		// 键缺席保留 true，显式 false 才关（关掉回落 Go 标准库 TLS，旧行为）。改这项要重启生效。
		TLSFingerprint bool `json:"tls_fingerprint"`
	} `json:"upstream"`

	Features struct {
		// SanitizeBlacklistFingerprints 出站请求体黑名单指纹脱敏（默认 true；false 完全还原）。
		SanitizeBlacklistFingerprints bool `json:"sanitize_blacklist_fingerprints"`
	} `json:"features"`

	Prompt struct {
		// Mode passthrough（默认）= 透传客户端原始 system（降级重试仍会切到 Degraded）；
		// custom = 网关用自有系统提示词替换客户端 system/developer；
		// append = 两者并用：开头连续 system/developer 块后插网关 system，既有消息逐字不动（issue #129）。
		Mode string `json:"mode"` // "passthrough" / "custom" / "append"
		// File 提示词文件路径；空 = 内置默认 defaultprompt.md；
		// 路径非空但不可读 → 启动报错（fail fast，避免静默回落到内置默认）。
		File string `json:"file"`
	} `json:"prompt"`

	// PromptText 解析后的系统提示词文本（custom/append 模式使用）。
	PromptText string `json:"-"`

	Upstash struct {
		URL   string `json:"url"`   // 空 = 纯内存模式；支持完整 rediss:// URL 或 https://xxx.upstash.io host
		Token string `json:"token"` // url 非完整连接串时用于组装 rediss://default:<token>@<host>:6379
	} `json:"upstash"`

	Pool struct {
		MaxInFlight        int    `json:"max_in_flight"`        // 单账号最大在途请求数，0 = 不限
		MaxInFlightGlobal  int    `json:"max_in_flight_global"` // global 域单账号在途上限（WAF 风控紧域压低并发），0 = 回落默认 2
		InFlightWait       string `json:"in_flight_wait"`       // 能接的号在途都满时请求最多等多久空位，默认 "30s"，"0" = 不等直接 503
		MaxRPM             int    `json:"max_rpm"`              // 国内版单号每分钟请求数上限（最近 60 秒），0 = 不限
		MaxRPMGlobal       int    `json:"max_rpm_global"`       // 国际版单号每分钟请求数上限，0 = 不限；两档各管各的域
		BreakerThreshold   int    `json:"breaker_threshold"`    // 连续失败次数触发熔断，默认 3
		BreakerCooldown    string `json:"breaker_cooldown"`     // 基础熔断时长，默认 "30m"
		BreakerCooldownMax string `json:"breaker_cooldown_max"` // 指数退避封顶，默认 "6h"
		// 连败降权（issue #114）：ErrClient/传输层这类「不罚号」失败连续计数，达阈
		// 临时出池。与冷却/熔断并存取更长者不叠加。默认 5 次 / 10m。
		DegradeThreshold   int     `json:"degrade_threshold"`    // 连败次数触发降权，默认 5
		DegradeCooldown    string  `json:"degrade_cooldown"`     // 降权时长（固定，非指数退避），默认 "10m"
		DegradeCooldownMax string  `json:"degrade_cooldown_max"` // 降权时长的上限钳制，默认 "2h"（仅当 cooldown 超该值才钳制）
		IdleWeightPerHour  float64 `json:"idle_weight_per_hour"` // 闲置补偿：每小时未用 +0.5 权重
		IdleWeightMax      float64 `json:"idle_weight_max"`      // 闲置补偿封顶，默认 5.0
		// PreferExpiring 快过期积分加权开关，默认 true。开启且 expiring_soon 窗口内
		// 存在有效批次时，该账号选号权重 ×3（虚拟实例，见 pool 路由加权）；
		// 不按到期时间排序、与批次金额无关（issue #101 对齐实现口径）。
		// 关闭后完全不使用到期信息选号。
		PreferExpiring bool `json:"prefer_expiring"`
		// ExpiringSoon 快过期积分窗口（如 "168h"=7天）：签到/余额刷新时，到期时间在
		// 此窗口内的批次令账号命中上述 ×3 加权；窗口开大 → 命中账号变多、
		// 偏好被稀释。空/0 = 禁用该加权门槛。
		ExpiringSoon string `json:"expiring_soon"`
		// CostExploreInterval costTier 条件探索窗口（issue #136 方案 a′）：tier 0
		// 垄断层存在且 tier 1 有成员时，距上次探索 ≥ 窗口则本次 pick 生效层切
		// tier 1-only（探索=搭车改道，零新增上游请求；成功即毕业，失败走既有
		// 错误策略）。默认 "30m"（≤48 次/天/模型）；"0" 关停（完全回到现状行为）；
		// 空值回落默认。
		CostExploreInterval string `json:"cost_explore_interval"`
		// CreditFloor 积分保底：账号余额低于该值时，对实测收费模型（tier 2）不再
		// 参与选号——防止收费请求把余额打穿、连免费模型都 402 冷却到次日签到。
		// tier 0（免费）/ tier 1（无观测）不受限；签到回血越过 floor 自动恢复。
		// 默认 0 = 关闭；负值钳 0。
		CreditFloor int64 `json:"credit_floor"`
		// CrossRealm 跨域负载均衡，三档（见 livecfg.CrossRealm*），默认 "off"：不带前缀的模型名只走国内版。
		// "merge"：不带 cn: / global: 前缀的模型请求在「模型目录里有这个模型」的各域账号合成的一个池里
		// 选号，规则不变（实测免费优先，再按积分 / 闲置等权重加权随机）。"cn_first"：两个域都有这个模型时
		// 国内版优先，国内版此刻没有能接的号才在国际版选。带前缀的照旧只走该域；打开时 /v1/models 同时
		// 列出不带前缀的模型名。热生效。1.8.0 及以前是布尔，true 读成 "merge"、false 读成 "off"。
		CrossRealm crossRealmMode `json:"cross_realm"`
		// 风控波熔断：restriction_wave_window 内同一个域有 restriction_wave_threshold 个号
		// 被上游 11140「账号存在异常行为」限制，就把该域其余的号暂停 restriction_wave_pause。
		// 默认 2 个 / "15m" / "30m"（照 workbuddy2api-hub #629 的做法）；阈值 0 = 关闭，
		// 负值钳 0；两个时长空值或非正值回落默认。热生效。
		RestrictionWaveThreshold int    `json:"restriction_wave_threshold"`
		RestrictionWaveWindow    string `json:"restriction_wave_window"`
		RestrictionWavePause     string `json:"restriction_wave_pause"`
	} `json:"pool"`

	SessionSticky struct {
		Enabled    bool   `json:"enabled"`     // 默认 true
		TTL        string `json:"ttl"`         // 会话绑定 TTL，默认 "30m"
		GCInterval string `json:"gc_interval"` // 会话 GC 周期，默认 "5m"
	} `json:"session_sticky"`

	// 解析后
	SoftRateDur            time.Duration `json:"-"`
	SoftRateMaxDur         time.Duration `json:"-"`
	BreakerCooldownDur     time.Duration `json:"-"`
	BreakerCooldownMaxD    time.Duration `json:"-"`
	DegradeCooldownDur     time.Duration `json:"-"`
	DegradeCooldownMaxD    time.Duration `json:"-"`
	SessionTTL             time.Duration `json:"-"`
	SessionGCInterval      time.Duration `json:"-"`
	BalanceRefreshInterval time.Duration `json:"-"` // 0 = 不启动（enabled=false）
	LivenessInterval       time.Duration `json:"-"` // 定时测活间隔；0 = 关闭（liveness_enabled=false）
	ExpiringSoonDur        time.Duration `json:"-"`
	// CostExploreIntervalDur 解析后的 costTier 探索窗口（issue #136）；0 = 关停。
	CostExploreIntervalDur time.Duration `json:"-"`
	// InFlightWaitDur 解析后的排队等空位上限；0 = 不等。
	InFlightWaitDur time.Duration `json:"-"`
	// RestrictionWaveWindowDur / RestrictionWavePauseDur 解析后的风控波窗口与暂停时长。
	RestrictionWaveWindowDur time.Duration `json:"-"`
	RestrictionWavePauseDur  time.Duration `json:"-"`
	// ServerReadTimeoutDur 解析后的入站请求读取上限（issue #100）；0 = 不限制。
	ServerReadTimeoutDur time.Duration `json:"-"`
}

// Default 默认配置。
func Default() *Config {
	c := &Config{
		Listen:    ":7863",
		APIKey:    "",
		AuthDir:   "./auths",
		StateFile: "./data/state.json",
	}
	c.Cooldown.SoftRate = "600s"
	c.Cooldown.SoftRateMax = "2h"
	c.Server.ReadTimeout = "300s"
	c.Panel.PackageDetailLimit = 5
	c.Panel.UpdateCheck = true
	c.Logging.RequestArchiveEnabled = true
	c.Logging.RequestRetentionDays = 7
	c.Logging.RequestArchiveMaxMB = 100
	c.Logging.TaskLogEnabled = true
	c.Logging.TaskLogRetentionDays = 30
	c.Logging.TaskLogMaxMB = 20
	c.Logging.SystemLogEnabled = true
	c.Logging.SystemLogRetentionDays = 30
	c.Logging.SystemLogMaxMB = 50
	c.Logging.CreditLogEnabled = true
	c.Logging.CreditLogMaxMB = 20
	// 缺省 true 靠显式赋值实现（同 Schedule 开关）：JSON 里键缺席时字段保留此值，
	// 只有显式 false 才关闭来源记录。
	c.Logging.RequestClientInfo = true
	c.Schedule.CheckinHours = []int{9, 21}
	c.Schedule.TravelHours = []int{9, 21}
	c.Schedule.ActivityHours = []int{10}
	c.Schedule.KeepaliveHours = []int{22}
	c.Schedule.BlackcatHours = []int{23}
	c.Schedule.GrowthHours = []int{1}
	// 开关「缺省 true」靠这几行实现：Load 先取 Default() 再 json.Unmarshal 覆盖，
	// 键缺席（或为 null）时字段原样保留 true，只有显式 false 才关。
	c.Schedule.CheckinEnabled = true
	c.Schedule.GrowthEnabled = true
	c.Schedule.TravelEnabled = true
	c.Schedule.ActivityEnabled = true
	c.Schedule.KeepaliveEnabled = true
	c.Schedule.BlackcatEnabled = true
	c.Schedule.BalanceRefreshEnabled = true
	c.Schedule.BalanceRefreshMinutes = 5
	c.Schedule.LivenessEnabled = true
	c.Schedule.LivenessMinutes = 30
	c.Upstream.TimeoutSeconds = 120
	// TLSFingerprint 缺省 true（伪装官方客户端的 TLS 指纹）；键缺席保留 true，显式 false 才关。
	c.Upstream.TLSFingerprint = true
	// HeaderTimeoutSeconds/IdleTimeoutSeconds 默认 0（未设置态），回落见 normalize()。
	c.Upstream.HeaderTimeoutSeconds = 0
	c.Upstream.IdleTimeoutSeconds = 0
	// Global.Enabled 缺省 true（纯 CN 行为不变：CN 账号恒判 cn，global base 不被使用）；
	// ChatBase/BillingBase 缺省空（回落内置默认）。
	c.Global.Enabled = true
	c.Features.SanitizeBlacklistFingerprints = true
	c.Prompt.Mode = "passthrough" // 缺省 passthrough：透传客户端原始 system（对齐上游；custom 由用户显式选择）
	c.Pool.MaxInFlight = 3
	// MaxInFlightGlobal 缺省 2：global 域 WAF 风控更紧，压低单号并发（WAF 403 修复
	// P1-1）；0/负数 normalize 回落默认（与 max_in_flight 的 0=不限语义不同，分档键
	// 的 0 没有合理语义，回退分档默认最稳）。
	c.Pool.MaxInFlightGlobal = 2
	// 号都在途占满时排队等空位，默认 30s（参考 sub2api 的 fallback_wait_timeout）；"0" 不等。
	c.Pool.InFlightWait = "30s"
	c.Pool.BreakerThreshold = 3
	c.Pool.BreakerCooldown = "30m"
	c.Pool.BreakerCooldownMax = "6h"
	c.Pool.DegradeThreshold = 5
	c.Pool.DegradeCooldown = "10m"
	c.Pool.DegradeCooldownMax = "2h"
	c.Pool.IdleWeightPerHour = 0.5
	c.Pool.IdleWeightMax = 5.0
	c.Pool.PreferExpiring = true
	c.Pool.ExpiringSoon = "168h" // 快过期窗口默认 7 天：官方活动奖励积分多在两周内过期
	// costTier 探索默认 30m（issue #136：垄断破除 + 搭车改道零新增请求）；"0" 关停。
	c.Pool.CostExploreInterval = "30m"
	c.Pool.RestrictionWaveThreshold = 2
	c.Pool.RestrictionWaveWindow = "15m"
	c.Pool.RestrictionWavePause = "30m"
	c.SessionSticky.Enabled = true
	c.SessionSticky.TTL = "30m"
	c.SessionSticky.GCInterval = "5m"
	return c
}

// Load 从文件读，再用 WB2A_* env 覆盖。
func Load(path string) (*Config, error) {
	c := Default()
	if path != "" {
		// 目录检查：Docker bind mount 在宿主机文件缺失时会静默创建同名目录，
		// 直接 ReadFile 会报 "Incorrect function" 之类晦涩错误，这里给出可操作提示。
		if st, statErr := os.Stat(path); statErr == nil && st.IsDir() {
			return nil, fmt.Errorf("config %s 是目录而非文件——"+
				"Docker 部署时若宿主机缺少 config.json，bind mount 会创建同名目录。"+
				"请先 `cp config.example.json config.json` 或删除该目录（程序会自动生成配置）", path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if _, err := ParseConfigInto(raw, c); err != nil {
			return nil, err
		}
	}
	applyEnv(c)
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseConfigInto 把 JSON 覆盖到 c 上并 normalize（不做 env、不读文件）。
// 面板保存配置走这条路径：与 Load 完全同一套解析/校验逻辑，避免两处漂移。
func ParseConfigInto(raw []byte, c *Config) (*Config, error) {
	if err := json.Unmarshal(raw, c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseConfig 基于默认值解析一段配置 JSON（等价于 Load 的文件分支，但不读环境变量）。
func ParseConfig(raw []byte) (*Config, error) {
	return ParseConfigInto(raw, Default())
}

// 测试可替换：随机数失败回退、写配置失败。默认值就是原来的直接调用，行为不变。
var (
	randRead        = rand.Read
	configFileWrite = (*os.File).Write
	configFileClose = (*os.File).Close
)

// WriteDefault 在 path 落一份推荐配置（首次运行自动生成，双击即开免手工复制样例）。
// 值取自 Default()（含超时/熔断/签到排程等推荐值），api_key 用 crypto/rand 随机生成：
// 安全默认优于示例占位符（listen 绑定 0.0.0.0，空 key 会把网关裸暴露给局域网）。
// 返回生成的 key 供启动日志透出。已存在时经 O_EXCL 原子拒绝，绝不改写用户配置。
func WriteDefault(path string) (string, error) {
	raw := make([]byte, 18)
	if _, err := randRead(raw); err != nil {
		return "", fmt.Errorf("gen api_key: %w", err)
	}
	key := "sk-" + base64.RawURLEncoding.EncodeToString(raw)
	c := Default()
	c.APIKey = key
	_ = c.normalize() // Default() 全合法，normalize 仅补齐 header/idle 超时的展示值
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir config dir: %w", err)
		}
	}
	// O_EXCL 原子拒绝覆盖：即使调用方漏判"不存在"，也绝不悄悄改写用户已有配置。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	defer f.Close()
	if _, err := configFileWrite(f, out); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	return key, nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("WB2A_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("WB2A_API_KEY"); v != "" {
		c.APIKey = v
	}
	if v := os.Getenv("WB2A_AUTH_DIR"); v != "" {
		c.AuthDir = v
	}
	if v := os.Getenv("WB2A_STATE_FILE"); v != "" {
		c.StateFile = v
	}
	if v := os.Getenv("WB2A_DNS"); v != "" {
		c.DNSServers = strings.Split(v, ",")
	}
	if v := os.Getenv("WB2A_SOFT_RATE"); v != "" {
		c.Cooldown.SoftRate = v
	}
	if v := os.Getenv("WB2A_SOFT_RATE_MAX"); v != "" {
		c.Cooldown.SoftRateMax = v
	}
	if v := os.Getenv("WB2A_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Upstream.TimeoutSeconds = n
		}
	}
	if v := os.Getenv("WB2A_HEADER_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Upstream.HeaderTimeoutSeconds = n
		}
	}
	if v := os.Getenv("WB2A_IDLE_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Upstream.IdleTimeoutSeconds = n
		}
	}
	if v := os.Getenv("WB2A_USER_AGENT"); v != "" {
		c.Upstream.UserAgent = v
	}
	if v := os.Getenv("WB2A_CLIENT_VERSION"); v != "" {
		c.Upstream.ClientVersion = v
	}
	if v := os.Getenv("WB2A_CLI_VERSION"); v != "" {
		c.Upstream.CliVersion = v
	}
	if v := os.Getenv("WB2A_CLIENT_NAME"); v != "" {
		c.Upstream.ClientName = v
	}
	if v := os.Getenv("WB2A_DEVICE_TOKEN"); v != "" {
		c.Upstream.DeviceToken = v
	}
	if v := os.Getenv("WB2A_DEVICE_TOKEN_FILE"); v != "" {
		c.Upstream.DeviceTokenFile = v
	}
	if v := os.Getenv("WB2A_PASSTHROUGH_IP"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Upstream.PassthroughIP = b
		}
	}
	if v := os.Getenv("WB2A_MODEL_OPTIMIZATION"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Upstream.ModelOptimization = b
		}
	}
	if v := os.Getenv("WB2A_TLS_FINGERPRINT"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Upstream.TLSFingerprint = b
		}
	}
	if v := os.Getenv("WB2A_SANITIZE_FINGERPRINTS"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Features.SanitizeBlacklistFingerprints = b
		}
	}
	if v := os.Getenv("WB2A_PROMPT_MODE"); v != "" {
		c.Prompt.Mode = v
	}
	if v := os.Getenv("WB2A_PROMPT_FILE"); v != "" {
		c.Prompt.File = v
	}
	if v := os.Getenv("WB2A_EXPIRING_SOON"); v != "" {
		c.Pool.ExpiringSoon = v
	}
	if v := os.Getenv("WB2A_PREFER_EXPIRING"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Pool.PreferExpiring = b
		}
	}
}

func (c *Config) normalize() error {
	var err error
	if c.Panel.PackageDetailLimit <= 0 {
		c.Panel.PackageDetailLimit = 5
	}
	// 日志留存：保留天数 0 = 永久，负值回落默认；容量 <=0 回落默认。
	for _, d := range []struct {
		days *int
		def  int
	}{
		{&c.Logging.RequestRetentionDays, 7},
		{&c.Logging.TaskLogRetentionDays, 30},
		{&c.Logging.SystemLogRetentionDays, 30},
		{&c.Logging.UsageRetentionDays, 0},
		{&c.Logging.CreditLogRetentionDays, 0},
	} {
		if *d.days < 0 {
			*d.days = d.def
		}
	}
	for _, m := range []struct {
		mb  *int
		def int
	}{
		{&c.Logging.RequestArchiveMaxMB, 100},
		{&c.Logging.TaskLogMaxMB, 20},
		{&c.Logging.SystemLogMaxMB, 50},
		{&c.Logging.CreditLogMaxMB, 20},
	} {
		if *m.mb <= 0 {
			*m.mb = m.def
		}
	}
	// 入站读取上限（issue #100）：空值回落默认 300s；"0" 合法（不限制）；
	// 负值无语义，fail fast（静默钳 0 会把保护悄悄关掉）。
	if c.Server.ReadTimeout == "" {
		c.Server.ReadTimeout = "300s"
	}
	if c.ServerReadTimeoutDur, err = time.ParseDuration(c.Server.ReadTimeout); err != nil {
		return fmt.Errorf("server.read_timeout: %w", err)
	}
	if c.ServerReadTimeoutDur < 0 {
		return fmt.Errorf("server.read_timeout: 负时长 %q 无意义", c.Server.ReadTimeout)
	}
	if c.SoftRateDur, err = time.ParseDuration(c.Cooldown.SoftRate); err != nil {
		return fmt.Errorf("cooldown.soft_rate: %w", err)
	}
	// 空值回落默认 2h（Default() 已置值；此兜底覆盖显式 "" 与 Default() 被绕过的场景）。
	if c.Cooldown.SoftRateMax == "" {
		c.Cooldown.SoftRateMax = "2h"
	}
	if c.SoftRateMaxDur, err = time.ParseDuration(c.Cooldown.SoftRateMax); err != nil {
		return fmt.Errorf("cooldown.soft_rate_max: %w", err)
	}
	if c.BreakerCooldownDur, err = time.ParseDuration(c.Pool.BreakerCooldown); err != nil {
		return fmt.Errorf("pool.breaker_cooldown: %w", err)
	}
	if c.BreakerCooldownMaxD, err = time.ParseDuration(c.Pool.BreakerCooldownMax); err != nil {
		return fmt.Errorf("pool.breaker_cooldown_max: %w", err)
	}
	if c.DegradeCooldownDur, err = time.ParseDuration(c.Pool.DegradeCooldown); err != nil {
		return fmt.Errorf("pool.degrade_cooldown: %w", err)
	}
	if c.DegradeCooldownMaxD, err = time.ParseDuration(c.Pool.DegradeCooldownMax); err != nil {
		return fmt.Errorf("pool.degrade_cooldown_max: %w", err)
	}
	if c.SessionTTL, err = time.ParseDuration(c.SessionSticky.TTL); err != nil {
		return fmt.Errorf("session_sticky.ttl: %w", err)
	}
	if c.SessionGCInterval, err = time.ParseDuration(c.SessionSticky.GCInterval); err != nil {
		return fmt.Errorf("session_sticky.gc_interval: %w", err)
	}
	// 快过期窗口：空 = 禁用（ExpiringSoonDur 0）；非空必须可解析（拼写错误 fail fast）。
	if c.Pool.ExpiringSoon != "" {
		if c.ExpiringSoonDur, err = time.ParseDuration(c.Pool.ExpiringSoon); err != nil {
			return fmt.Errorf("pool.expiring_soon: %w", err)
		}
	}
	if c.ExpiringSoonDur < 0 {
		c.ExpiringSoonDur = 0
		c.Pool.ExpiringSoon = "0"
	}
	// costTier 探索窗口（issue #136）：空值回落默认 30m（Default 已置；此兜底覆盖
	// 显式 ""）；"0" 是合法值（关停，完全回到现状行为），不回落；负值钳 0 同关停
	//（"−5m" 无合理语义）。
	if c.Pool.CostExploreInterval == "" {
		c.Pool.CostExploreInterval = "30m"
	}
	if c.CostExploreIntervalDur, err = time.ParseDuration(c.Pool.CostExploreInterval); err != nil {
		return fmt.Errorf("pool.cost_explore_interval: %w", err)
	}
	if c.CostExploreIntervalDur < 0 {
		c.CostExploreIntervalDur = 0
	}
	// 排队等空位：空值回落默认 30s，"0" 是合法值（不等，选不到号直接 503），负值钳 0。
	if c.Pool.InFlightWait == "" {
		c.Pool.InFlightWait = "30s"
	}
	if c.InFlightWaitDur, err = time.ParseDuration(c.Pool.InFlightWait); err != nil {
		return fmt.Errorf("pool.in_flight_wait: %w", err)
	}
	if c.InFlightWaitDur < 0 {
		c.InFlightWaitDur = 0
	}
	// 风控波：阈值 0 是合法值（关闭），负值钳 0；时长 0 没有合理语义，空值或非正值回落默认。
	if c.Pool.RestrictionWaveThreshold < 0 {
		c.Pool.RestrictionWaveThreshold = 0
	}
	if c.RestrictionWaveWindowDur, err = parseDurationOr(c.Pool.RestrictionWaveWindow, "15m"); err != nil {
		return fmt.Errorf("pool.restriction_wave_window: %w", err)
	}
	if c.RestrictionWavePauseDur, err = parseDurationOr(c.Pool.RestrictionWavePause, "30m"); err != nil {
		return fmt.Errorf("pool.restriction_wave_pause: %w", err)
	}
	// 每号每分钟请求数上限：0 是合法默认（不限），负值钳 0。
	c.Pool.MaxRPM, c.Pool.MaxRPMGlobal = max(c.Pool.MaxRPM, 0), max(c.Pool.MaxRPMGlobal, 0)
	// 积分保底：负值钳 0（= 关闭）。0 是合法默认（关闭），无需空值回落。
	if c.Pool.CreditFloor < 0 {
		c.Pool.CreditFloor = 0
	}
	if c.Pool.BreakerThreshold <= 0 {
		c.Pool.BreakerThreshold = 3
	}
	// 连败降权参数缺省归一（非法/未设置回落默认，与 breaker_threshold 同风格）。
	// degrade_cooldown / degrade_cooldown_max 的默认值由 Default() 给出，显式写 "" 在上面
	// 解析时报错（与 breaker_cooldown 一致），这里不再回落。
	if c.Pool.DegradeThreshold <= 0 {
		c.Pool.DegradeThreshold = 5
	}
	// global 在途分档：0/负数视为未设置回落默认 2（WAF 403 修复 P1-1）。
	if c.Pool.MaxInFlightGlobal <= 0 {
		c.Pool.MaxInFlightGlobal = 2
	}
	if c.Pool.IdleWeightPerHour <= 0 {
		c.Pool.IdleWeightPerHour = 0.5
	}
	if c.Pool.IdleWeightMax <= 0 {
		c.Pool.IdleWeightMax = 5.0
	}
	if c.Upstream.TimeoutSeconds <= 0 {
		c.Upstream.TimeoutSeconds = 120
	}
	// header 缺省回落 timeout（保"首字节前换号"既有语义）；idle 缺省走内置大值。
	// 任务书约定：0 一律视为"未设置"走默认，真正的"禁用"留待后续（避免歧义）。
	if c.Upstream.HeaderTimeoutSeconds <= 0 {
		c.Upstream.HeaderTimeoutSeconds = c.Upstream.TimeoutSeconds
	}
	if c.Upstream.IdleTimeoutSeconds <= 0 {
		c.Upstream.IdleTimeoutSeconds = 300
	}
	if !strings.HasPrefix(c.Listen, ":") && !strings.Contains(c.Listen, ":") {
		c.Listen = ":" + c.Listen
	}
	// 空数组与 null 反序列化后覆盖掉 Default() 的排程值（键缺席才保留），在此补齐。
	// 空 = 未配置 → 回落默认；「禁用」一律走 *_enabled=false，两者互不混淆。
	if len(c.Schedule.CheckinHours) == 0 {
		c.Schedule.CheckinHours = []int{9, 21}
	}
	if len(c.Schedule.TravelHours) == 0 {
		c.Schedule.TravelHours = []int{9, 21}
	}
	if len(c.Schedule.ActivityHours) == 0 {
		c.Schedule.ActivityHours = []int{10}
	}
	if len(c.Schedule.KeepaliveHours) == 0 {
		c.Schedule.KeepaliveHours = []int{22}
	}
	if len(c.Schedule.BlackcatHours) == 0 {
		c.Schedule.BlackcatHours = []int{23}
	}
	if len(c.Schedule.GrowthHours) == 0 {
		c.Schedule.GrowthHours = []int{1}
	}
	// 余额后台刷新：启用时 minutes<=0 回落默认 5；关闭时 interval 保持 0（不启动）。
	if c.Schedule.BalanceRefreshEnabled {
		if c.Schedule.BalanceRefreshMinutes <= 0 {
			c.Schedule.BalanceRefreshMinutes = 5
		}
		c.BalanceRefreshInterval = time.Duration(c.Schedule.BalanceRefreshMinutes) * time.Minute
	}
	// 定时测活同理：启用时 minutes<=0 回落默认 30；关闭时 interval 保持 0。
	if c.Schedule.LivenessEnabled {
		if c.Schedule.LivenessMinutes <= 0 {
			c.Schedule.LivenessMinutes = 30
		}
		c.LivenessInterval = time.Duration(c.Schedule.LivenessMinutes) * time.Minute
	}
	if err := c.validateScheduleHours(); err != nil {
		return err
	}
	if err := c.normalizeModelRoutes(); err != nil {
		return err
	}
	if err := c.normalizeDNSServers(); err != nil {
		return err
	}
	switch c.Pool.CrossRealm {
	case "":
		c.Pool.CrossRealm = livecfg.CrossRealmOff
	case livecfg.CrossRealmOff, livecfg.CrossRealmMerge, livecfg.CrossRealmCNFirst:
	default:
		return fmt.Errorf("pool.cross_realm 只能是 off、merge、cn_first，不认 %q", string(c.Pool.CrossRealm))
	}
	return c.normalizePrompt()
}

// crossRealmMode pool.cross_realm 的取值。老配置是布尔：true 读成 "merge"、false 读成 "off"。
type crossRealmMode string

func (m *crossRealmMode) UnmarshalJSON(b []byte) error {
	var on bool
	if json.Unmarshal(b, &on) == nil {
		*m = livecfg.CrossRealmOff
		if on {
			*m = livecfg.CrossRealmMerge
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("pool.cross_realm 只能是 \"off\"、\"merge\"、\"cn_first\" 或老写法 true、false，不认 %s", b)
	}
	*m = crossRealmMode(s)
	return nil
}

// normalizeModelRoutes 去掉两端空白并校验：match、target 都不能为空，target 不能带通配。
// parseDurationOr 解析时长；空值或非正值用 def（def 须是合法时长）。
func parseDurationOr(s, def string) (time.Duration, error) {
	if s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, err
		}
		if d > 0 {
			return d, nil
		}
	}
	return time.ParseDuration(def)
}

// normalizeDNSServers 去掉两端空白和空项，每项必须是 IP 或 IP:端口：写域名的话解析它又得先有 DNS。
func (c *Config) normalizeDNSServers() error {
	out := c.DNSServers[:0]
	for _, s := range c.DNSServers {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		host := s
		if h, port, err := net.SplitHostPort(s); err == nil {
			if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("dns_servers: %q 的端口不对", s)
			}
			host = h
		}
		if net.ParseIP(host) == nil {
			return fmt.Errorf("dns_servers: %q 不是 IP 或 IP:端口", s)
		}
		out = append(out, s)
	}
	c.DNSServers = out
	return nil
}

func (c *Config) normalizeModelRoutes() error {
	for i := range c.ModelRoutes {
		rt := &c.ModelRoutes[i]
		rt.Match, rt.Target = strings.TrimSpace(rt.Match), strings.TrimSpace(rt.Target)
		if rt.Match == "" || rt.Target == "" {
			return fmt.Errorf("model_routes[%d]: match 和 target 都不能为空", i)
		}
		if strings.Contains(rt.Target, "*") {
			return fmt.Errorf("model_routes[%d]: target %q 不能带 *", i, rt.Target)
		}
	}
	return nil
}

// normalizePrompt 校验 prompt.mode 并按 file 加载提示词文本（custom/append 模式）。
//
// mode 非法（非 passthrough/custom/append）启动报错，避免静默回落到某一分支；
// custom/append 模式下 file 非空但不可读 → 报错（fail fast），file 空 → 用内置默认
// （两模式共用同一加载路径，PromptText 均非空）。
// passthrough 模式不加载文本（透传客户端原始 system，文本在降级时用 prompt.Degraded）。
func (c *Config) normalizePrompt() error {
	switch m := strings.ToLower(strings.TrimSpace(c.Prompt.Mode)); m {
	case "", "passthrough":
		c.Prompt.Mode = "passthrough"
	case "custom":
		c.Prompt.Mode = "custom"
	case "append":
		c.Prompt.Mode = "append"
	default:
		return fmt.Errorf("prompt.mode: %q 不是合法值（passthrough / custom / append）", c.Prompt.Mode)
	}
	if c.Prompt.Mode == "custom" || c.Prompt.Mode == "append" {
		text, err := prompt.Load(c.Prompt.Mode, c.Prompt.File)
		if err != nil {
			return err
		}
		c.PromptText = text
	}
	return nil
}

// validateScheduleHours 校验排程小时落在 0-23。
//
// 为什么不用 `[-1]` 之类的哨兵值表意"禁用"：非法小时被静默吞掉时，用户以为关掉了签到，
// 实际可能被当成另一个整点照常执行；这里直接快速失败，并在错误信息里指向正确的开关
// （checkin_enabled / keepalive_enabled），避免用户靠猜哨兵值来配。
func (c *Config) validateScheduleHours() error {
	if err := checkHourRange("schedule.checkin_hours", "checkin_enabled", c.Schedule.CheckinHours); err != nil {
		return err
	}
	if err := checkHourRange("schedule.travel_hours", "travel_enabled", c.Schedule.TravelHours); err != nil {
		return err
	}
	if err := checkHourRange("schedule.activity_hours", "activity_enabled", c.Schedule.ActivityHours); err != nil {
		return err
	}
	if err := checkHourRange("schedule.keepalive_hours", "keepalive_enabled", c.Schedule.KeepaliveHours); err != nil {
		return err
	}
	if err := checkHourRange("schedule.blackcat_hours", "blackcat_enabled", c.Schedule.BlackcatHours); err != nil {
		return err
	}
	return checkHourRange("schedule.growth_hours", "growth_enabled", c.Schedule.GrowthHours)
}

func checkHourRange(field, switchKey string, hours []int) error {
	for _, h := range hours {
		if h < 0 || h > 23 {
			return fmt.Errorf("%s: %d 不是合法小时（0-23）；如要关闭该任务请设 schedule.%s=false", field, h, switchKey)
		}
	}
	return nil
}
