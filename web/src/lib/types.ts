// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 面板接口（/panel/api/*）的返回类型，手写对齐 internal/panel 与 internal/pool 的 JSON 字段。
// Go 端 time.Time 的零值序列化为 "0001-01-01T00:00:00Z"，展示前要当作「无」处理（见 format.ts 的 isZeroTime）。

export interface TokenUsage {
  request_count?: number
  usage_count?: number
  prompt_tokens?: number
  completion_tokens?: number
  total_tokens?: number
  last_latency_ms?: number
  last_tokens_per_second?: number
  last_used_at?: string
  last_model?: string
}

export interface ModelCost {
  model: string
  /** 实测每千 token 单价；≤0 = 实测免费 */
  cost_per_1k: number
  last_seen: string
  samples?: number
}

/**
 * 模型级限额台账（issue #36）：该账号的某个模型暂时不能用。
 * kind：rate_limit（6000–6008 按模型限额、14003 模型繁忙）/ model_unavailable（11102 模型不可用）。
 * until 是网关最早重试时刻，reset_at 是上游「将在 … 重置」的原始时刻（可能没有）。
 */
export interface RateLimitedModel {
  model: string
  /** rate_limit / model_unavailable */
  kind?: string
  until?: string
  reset_at?: string
  reason?: string
}

export interface Account {
  uid: string
  nickname?: string
  /** 面板里写的备注（只在面板上显示），各页的账号名优先用它，见 accounts.ts 的 accountName */
  note?: string
  credits: number | null
  credits_total?: number
  credits_expiring?: number
  credits_earliest_expiry?: string
  credits_earliest_remaining?: number
  cooling: boolean
  cool_kind?: string
  cool_remaining_sec?: number
  until?: string
  reason?: string
  soft_streak?: number
  rate_limited_models?: RateLimitedModel[]
  realm?: string
  /** 企业版（auth 带 enterpriseId）：没有签到 / 成长任务；credits_total 是分配额度，-1 = 不限量 */
  enterprise?: boolean
  /** 个人号的凭证签发自企业切换会话（令牌带 token_source=enterprise_switch）：成长任务、连登、旅行、夜猫子不计分，网关都跳过，要重新登录 */
  enterprise_switch?: boolean
  /** 本地今日已签到（签到成功或上游「今天已签到」都算；国际版没有签到，恒为 false） */
  checkin_done?: boolean
  disabled: boolean
  disabled_reason?: string
  /** 暂停选号：不参与选号，保号任务照常（与 disabled 正交；总览里单独计 paused，禁用优先） */
  paused?: boolean
  success_count?: number
  err_total?: number
  last_success?: string
  last_err?: string
  token_usage?: TokenUsage
  model_costs?: ModelCost[]
  consecutive_fails: number
  degrade_until?: string
  in_flight: number
  breaker_fails: number
  breaker_until?: string
}

export interface Overview {
  version: string
  uptime_sec: number
  auth_required: boolean
  redis_mode: string
  sticky_sessions: number
  total: number
  healthy: number
  cooling: number
  disabled: number
  /** 暂停选号（不含已禁用的） */
  paused: number
  in_flight_full: number
  accounts: Account[] | null
  /** 模型锁池：有未过期模型级限流的「域 + 模型」，已按 locked → starved → partial 排好序；无锁时为 null */
  model_locks?: ModelLockRow[] | null
}

/**
 * 模型锁池的一行（internal/pool/modelview.go 的 ModelLockRow）。
 * 只统计参与选号的账号（禁用、暂停选号的不算），只看真正拦路由的限流。
 */
export interface ModelLockRow {
  model: string
  /** cn / global */
  realm: string
  /** 该域参与选号的账号数 */
  total: number
  /** 此刻能服务该模型的账号数 */
  servable: number
  /** 被该模型限流挡住的账号数 */
  locked: number
  /** locked 整池不可用 / starved 没号可用（不是模型限流造成的）/ partial 部分限流 */
  state: string
  /** 第一个被锁账号恢复的时刻 */
  unlock_at: string
  /** 最后一个被锁账号恢复的时刻 */
  fully_unlock_at: string
  reason?: string
}

export interface LogEntry {
  ts: string
  /** task / sys */
  ch: string
  text: string
}

export interface Model {
  id: string
  name?: string
  default_effort?: string
  supported_efforts?: string[] | null
  can_disable_thinking?: boolean
  supports_reasoning?: boolean
  supports_images?: boolean
  supports_tool_call?: boolean
  only_reasoning?: boolean
  credits?: string | number
  description?: string
  vendor?: string
  tags?: string[] | null
  is_default?: boolean
  /** 只有池里企业号自己的目录里有，请求只派给这些企业号 */
  enterprise_only?: boolean
  promo_factor?: number
  promo_credits?: string | number
  promo_label?: string
  promo_note?: string
  context_length?: number
  max_output_tokens?: number
}

export interface Probe {
  claimed?: number
  measured?: number
  /** clamped（被钳制）/ at_least（至少）/ 其他 = 未测出 */
  verdict?: string
  note?: string
  tested_at?: string
}

export interface ProbesResp {
  probes: Record<string, Probe>
  exists: boolean
  updated_at?: string
}

export interface UsageAgg {
  requests: number
  errors: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  /** 上游 usage.credit 累计（只统计明确返回了积分的请求） */
  credits?: number
  /** 带积分的请求数（区分「没返回积分」和「真的扣了 0」） */
  credit_samples?: number
  /** 同时有积分和 token 的请求的 token 合计（积分 / 1M Token 的分母） */
  credit_tokens?: number
  credits_per_1m_tokens?: number
  /** 上游前缀缓存命中 / 未命中 token（上游返回了这一项才累计） */
  cache_hit_tokens?: number
  cache_miss_tokens?: number
  avg_latency_ms: number
  avg_tokens_per_second: number
}

/** 积分扣除按账号 / 按模型（模型行带请求时生效的积分倍率）聚合的一行 */
export interface CreditUsage {
  key: string
  realm?: string
  nickname?: string
  rate?: string
  requests: number
  credits: number
  credit_samples: number
  credit_tokens: number
  credits_per_1m_tokens: number
  cache_hit_tokens?: number
  cache_miss_tokens?: number
}

export interface KeyedUsage extends UsageAgg {
  key: string
  realm?: string
  /** 按账号的行里放昵称 */
  extra?: string
}

export interface UsagePoint extends UsageAgg {
  /** hour: "2026-09-16T13"，day: "2026-09-16" */
  t: string
  scope: 'hour' | 'day'
}

export interface UsageResp {
  totals: UsageAgg
  by_realm: KeyedUsage[] | null
  by_account: KeyedUsage[] | null
  by_model: KeyedUsage[] | null
  /** 按密钥：key 是密钥 id（管理员是 admin，空串是没开鉴权或加密钥维度之前的数据），extra 是显示名 */
  by_key?: KeyedUsage[] | null
  series: UsagePoint[] | null
  credit_by_account?: CreditUsage[] | null
  credit_by_model?: CreditUsage[] | null
  buckets: number
  file_bytes: number
  since?: string
  /** 显式区间（今天 / 自定义）时服务端实际生效的起止（本地时间 RFC3339）；滚动窗口和全部历史没有 */
  window_from?: string
  window_to?: string
  generated: string
}

export interface CreditPackage {
  name: string
  remain: number
  used: number
  size: number
  end_time?: string
  /** 毫秒时间戳 */
  expires_at?: number
  created_at?: string
  package_code?: string
  sub_product_code?: string
  sub_product_name?: string
  cycle?: boolean
  /** 按周期刷新的包（订阅、体验类）：end_time 是刷新时刻 */
  refresh?: boolean
}

export interface PackageAccount {
  uid: string
  nickname: string
  /** 面板从总览补上的备注（接口不带），显示名用 accountName */
  note?: string
  realm: string
  remain: number
  size: number
  packages: CreditPackage[] | null
  error?: string
}

export interface Task {
  task_code: string
  title?: string
  description?: string
  task_desc?: string
  credit?: number
  energy?: number
  reward_buddy?: boolean
  tag?: string
  jump_url?: string
  locked?: boolean
  target: number
  current: number
  accept_status?: string
  claimable?: boolean
  claimed?: boolean
}

/** 单个任务「一键完成」的结果 */
export interface AutoResult {
  ok?: boolean
  skipped?: boolean
  message?: string
  progress_before?: string
  progress_after?: string
  claimable?: boolean
  claimed?: boolean
  claim_error?: string
  attempt?: boolean
}

/** 「一键完成可自动任务」逐项结果 */
export interface AutoAllItem {
  task_code: string
  desc?: string
  /** done / skipped / error */
  status: string
  message?: string
  progress_after?: string
  claimed?: boolean
  claim_error?: string
}

export interface AcceptAllResp {
  accepted?: number
  failed?: string[] | null
  message?: string
}

export interface ScanAccount {
  uid: string
  nickname: string
  growth?: Task[]
  growth_error?: string
}

export interface QueueItem {
  uid: string
  nickname: string
  kind: string
  code: string
  /** pending / running / done / skipped / error */
  status: string
  message?: string
}

export interface QueueState {
  running: boolean
  total: number
  conc: number
  started: boolean
  started_at: string
  seq: number
  items: QueueItem[] | null
}

export interface RunQueueResp {
  started: boolean
  message?: string
  total?: number
  seq?: number
}

export interface Voucher {
  prize_name?: string
  sku_code?: string
  code: string
  valid_to?: string
  granted_at?: string
}

export interface VoucherAccount {
  uid: string
  nickname: string
  vouchers: Voucher[] | null
  error?: string
}

export interface ConfigResp {
  path?: string
  config: Record<string, unknown>
}

export interface SaveConfigResp {
  restart_required?: string[] | null
}

export interface LoginStart {
  url: string
  state: string
  realm: string
}

export interface LoginPoll {
  done: boolean
  message?: string
  uid?: string
  nickname?: string
  realm?: string
  credits?: number
  credits_total?: number
}

export interface ImportResp {
  total: number
  imported: number
  skipped: number
  errors: string[] | null
}

/** 一次请求的元数据（不含提示词、响应正文与凭证） */
export interface RequestEvent {
  time: string
  request_id: string
  path: string
  /** 账号展示名（昵称(uid 前 8 位)） */
  account?: string
  model?: string
  status: number
  ok: boolean
  /** success / http_error / stream_error / interrupted */
  outcome: string
  duration_ms: number
  ttfb_ms?: number
  attempts?: number
  prompt_tokens?: number
  completion_tokens?: number
  total_tokens?: number
  credit?: number
  credit_known: boolean
  cache_hit_tokens?: number
  cache_miss_tokens?: number
  /** 调用来源（logging.request_client_info 关闭时为空） */
  client_ip?: string
  user_agent?: string
  /** 用的密钥：客户端密钥记名字，管理员密钥记「管理员」，没开鉴权为空 */
  api_key?: string
  /** 失败原因：回给客户端的错误消息（流中途出错时是上游的错误帧），最多 200 字；成功的请求没有 */
  error?: string
}

export interface RequestMetrics {
  started_at?: string
  completed?: number
  in_flight?: number
  succeeded?: number
  failed?: number
  success_rate?: number
  http_success_rate?: number
  avg_duration_ms?: number
  /** 最近的请求，最新的在前 */
  recent?: RequestEvent[] | null
  archive?: ArchiveStats
}

/** 一类日志归档的存储状态（请求记录、任务日志、系统日志） */
export interface ArchiveStats {
  enabled: boolean
  dir?: string
  /** 保留天数，0 = 永久保留 */
  retention_days?: number
  max_bytes?: number
  files?: number
  bytes?: number
  /** 最早那份文件的日期（YYYY-MM-DD），没有文件时没有这一项 */
  oldest_day?: string
  dropped_writes?: number
  last_error?: string
}

/** 用量统计的存储状态 */
export interface UsageStoreStats {
  /** 保留天数，0 = 永久保留 */
  retention_days: number
  buckets: number
  /** usage.json 的大小 */
  bytes: number
  oldest_day?: string
}

/** /panel/api/log_storage：日志留存各类的状态，没接上的为 null */
export interface LogStorage {
  requests: ArchiveStats | null
  task: ArchiveStats | null
  system: ArchiveStats | null
  usage: UsageStoreStats | null
}

/** /panel/api/logs/archive：按时间范围查到的运行日志，按时间先后排；more = 更早的还有没返回的 */
export interface LogArchiveResp {
  entries: LogEntry[] | null
  limit: number
  more: boolean
}

export interface CompensationStatus {
  active: boolean
  claimed: boolean
  credit_num: number
  end_time?: string
}

export interface CompensationAccount {
  uid: string
  nickname: string
  /** 已禁用：照常列出、可单号领取，但「领取全部」只查不领 */
  disabled?: boolean
  status?: CompensationStatus
  newly_claimed: boolean
  credit: number
  error?: string
  balance_error?: string
}

export interface CompensationResp {
  accounts: CompensationAccount[]
}

/** 更新日志里的一个版本（网关解析 docs/CHANGELOG.md 得到） */
export interface ReleaseGroup {
  title: string
  items: string[] | null
}

export interface Release {
  version: string
  date?: string
  summary?: string
  groups: ReleaseGroup[] | null
}

/** GET /panel/api/version：本机版本、更新检查结果与「本次更新」 */
export interface VersionResp {
  version: string
  update_check: boolean
  latest?: string
  update_available: boolean
  updates: Release[] | null
  release_url?: string
  checked_at?: string
  check_error?: string
  whats_new: Release[] | null
  /** 一键更新的状态；网关没有接入时没有这一项 */
  self_update?: SelfUpdateStatus
}

/** 一键更新进行到哪一步 */
export type SelfUpdatePhase = 'idle' | 'downloading' | 'verifying' | 'restarting' | 'failed'

export interface SelfUpdateStatus {
  supported: boolean
  /** 不支持一键更新的原因 */
  reason?: string
  /** 程序自带的版本 */
  base: string
  /** 正在跑面板装到数据目录的版本 */
  override: boolean
  phase: SelfUpdatePhase
  /** 正在更新到 / 退回到的版本 */
  target?: string
  done?: number
  total?: number
  /** 上一次更新失败的原因 */
  error?: string
  /** 面板装的版本没能稳定运行、已自动退回的说明 */
  rolled_back?: string
  /** 优雅停机最长等待（秒） */
  grace_sec: number
}

/** 客户端密钥的用量上限，没设的项 = 不限 */
export interface KeyLimits {
  requests?: number
  tokens?: number
  credits?: number
}

export interface KeyUsage {
  requests: number
  tokens: number
  credits: number
}

/** 面板里建的客户端密钥（面板只认管理员密钥，所以列表里带原文） */
export interface ApiKey {
  id: string
  name: string
  note?: string
  secret: string
  enabled: boolean
  created_at: string
  /** 没有 = 不过期 */
  expires_at?: string
  daily_limit: KeyLimits
  total_limit: KeyLimits
  /** 模型白名单（* 通配，按实际发上游的模型名匹配），空 = 不限 */
  models?: string[] | null
  last_used_at?: string
  used: KeyUsage
  /** 北京时间今天的用量 */
  today: KeyUsage
  day?: string
  /** 按网关的时钟算的是否已过期 */
  expired: boolean
}

export interface KeysResp {
  keys: ApiKey[] | null
  admin_key_set: boolean
  /** /v1/* 是否必须带密钥（设了管理员密钥或建过客户端密钥） */
  required: boolean
}

/** 新建、修改密钥时提交的字段（修改也要整份提交） */
export interface KeyInput {
  name: string
  note: string
  enabled: boolean
  expires_at: string | null
  daily_limit: KeyLimits
  total_limit: KeyLimits
  models: string[]
}
