// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 配置页的字段定义：每个表单字段对应 config.json 里的一个路径（与旧版 CFG_MAP 一致），
// 按标签页分组。保存时只提交表单管理的键，后端深合并，未知键原样保留。
import type { StoreKind } from './storage'

export type FieldKind = 'text' | 'password' | 'number' | 'hours' | 'duration' | 'switch' | 'select'

export interface FieldDef {
  name: string
  path: string[]
  label: string
  kind: FieldKind
  hint?: string
  placeholder?: string
  /** 改动后要重启进程才生效 */
  restart?: boolean
  /**
   * 「覆盖型」文本字段：空串本身有意义（= 回落内置默认），清空也要提交（上游 issue #102）。
   * 其余文本字段保持「空 = 不提交」——没填通常是没改，当成清空会静默抹掉配置。
   * 刻意不给 api_key：清空它等于关掉整个鉴权，误触代价太大。
   */
  clearable?: boolean
  min?: number
  step?: number
  options?: { id: string; label: string }[]
}

export interface Section {
  title?: string
  fields: FieldDef[]
  /** 日志留存的一类：卡片下面显示这类现在的占用，并能清空 */
  store?: StoreKind
}

export interface ConfigTab {
  id: string
  label: string
  sections: Section[]
}

const hours = (name: string, key: string, label: string, placeholder: string, hint?: string): FieldDef =>
  ({ name, path: ['schedule', key], label, kind: 'hours', placeholder, hint })
const toggle = (name: string, path: string[], label: string, hint?: string, restart?: boolean): FieldDef =>
  ({ name, path, label, kind: 'switch', hint, restart })
const duration = (name: string, path: string[], label: string, placeholder: string, hint?: string, restart?: boolean): FieldDef =>
  ({ name, path, label, kind: 'duration', placeholder, hint, restart })
const num = (name: string, path: string[], label: string, placeholder: string, extra: Partial<FieldDef> = {}): FieldDef =>
  ({ name, path, label, kind: 'number', placeholder, ...extra })
/** 一类按天写文件的日志：开关、保留天数（0 = 永久）、容量上限 */
const archive = (prefix: string, label: string, hint: string, days: string, maxMB: string): FieldDef[] => [
  toggle(prefix + '_enabled', ['logging', prefix + '_enabled'], label, hint),
  num(prefix + '_retention_days', ['logging', prefix + '_retention_days'], '保留天数', days, { min: 0, hint: '0 = 永久保留（仍受容量上限）；超期的按天文件自动删除' }),
  num(prefix + '_max_mb', ['logging', prefix + '_max_mb'], '容量上限（MiB）', maxMB, { min: 1, hint: '超过上限先删最旧的文件' }),
]

export const CONFIG_TABS: ConfigTab[] = [
  {
    id: 'service',
    label: '服务',
    sections: [{
      fields: [
        { name: 'listen', path: ['listen'], label: '监听地址', kind: 'text', placeholder: ':7863', restart: true },
        { name: 'api_key', path: ['api_key'], label: 'API 密钥', kind: 'password', placeholder: '留空 = 不鉴权', hint: '立即生效（含面板自身）' },
        num('package_detail_limit', ['panel', 'package_detail_limit'], '单账号明细默认展示条数', '5', { min: 1, hint: '积分构成页按最早到期展示，其余未用完包聚合' }),
        toggle('update_check', ['panel', 'update_check'], '检查更新', '每 6 小时从 GitHub 读取一次更新日志，有新版本时在面板提醒；关闭后不再联网检查'),
      ],
    }],
  },
  {
    id: 'schedule',
    label: '定时任务',
    sections: [
      { title: '自动签到', fields: [toggle('checkin_enabled', ['schedule', 'checkin_enabled'], '自动签到'), hours('checkin_hours', 'checkin_hours', '签到时点', '9, 21')] },
      { title: 'Token 保活', fields: [toggle('keepalive_enabled', ['schedule', 'keepalive_enabled'], 'Token 保活'), hours('keepalive_hours', 'keepalive_hours', '保活时点', '22')] },
      { title: '猫猫旅行', fields: [toggle('travel_enabled', ['schedule', 'travel_enabled'], '猫猫旅行'), hours('travel_hours', 'travel_hours', '旅行时点', '9, 21', '一趟派出 + 一趟领奖闭环')] },
      { title: '活跃上报', fields: [toggle('activity_enabled', ['schedule', 'activity_enabled'], '活跃上报'), hours('activity_hours', 'activity_hours', '上报时点', '10', '点亮连登 + 解锁领养前置')] },
      {
        title: '后台刷新余额',
        fields: [
          toggle('balance_refresh_enabled', ['schedule', 'balance_refresh_enabled'], '后台刷新余额'),
          num('balance_refresh_minutes', ['schedule', 'balance_refresh_minutes'], '刷新间隔（分钟）', '5', { min: 1 }),
        ],
      },
      {
        title: '定时测活',
        fields: [
          toggle('liveness_enabled', ['schedule', 'liveness_enabled'], '定时测活',
            '每隔一段时间给整个号在限流冷却、熔断中的号发一条最短的真实对话，测通就提前恢复；余额不足、风控、已禁用、暂停选号的号不测，健康的号也不测'),
          num('liveness_minutes', ['schedule', 'liveness_minutes'], '测活间隔（分钟）', '30', { min: 1 }),
        ],
      },
      {
        title: '成长任务自动执行',
        fields: [
          toggle('growth_enabled', ['schedule', 'growth_enabled'], '成长任务自动执行'),
          hours('growth_hours', 'growth_hours', '执行时点', '1', '每日到点自动「扫描 + 执行全部待办」（Sequential 任务链零点解锁后自动推进，建议零点后）'),
        ],
      },
      {
        title: '已禁用的账号',
        fields: [
          toggle('include_disabled_in_tasks', ['schedule', 'include_disabled_in_tasks'], '保号任务覆盖已禁用账号',
            '禁用只关选号：打开后已禁用的账号仍会签到 / 活跃上报 / 保活 / 刷新余额（依旧不参与选号）。适合「一次只放开一个账号、用禁用做流量开关」的轮换养号用法。猫猫旅行、夜猫子、连登管家与成长任务仍跳过禁用账号。若只想让单个账号临时退出选号但保留保号，直接用账号行的「暂停选号」按钮（账号级，无需打开本开关）'),
        ],
      },
    ],
  },
  {
    id: 'pool',
    label: '账号池与流量治理',
    sections: [
      {
        title: '在途与熔断',
        fields: [
          num('max_in_flight', ['pool', 'max_in_flight'], '单账号最大在途', '3', { min: 0, hint: '0 = 不限制' }),
          num('max_in_flight_global', ['pool', 'max_in_flight_global'], '国际版在途上限', '2', { min: 1, hint: 'global 域风控更紧，默认 2' }),
          num('max_rpm', ['pool', 'max_rpm'], '国内版每号每分钟请求数', '0',
            { min: 0, hint: '最近 60 秒内一个号最多接几个请求，到了就换别的号或排队，同在途满；0 = 不限制' }),
          num('max_rpm_global', ['pool', 'max_rpm_global'], '国际版每号每分钟请求数', '0', { min: 0, hint: '同上，只管国际版的号；0 = 不限制' }),
          duration('in_flight_wait', ['pool', 'in_flight_wait'], '号都满时排队上限', '30s', '能接的号在途都满或每分钟请求数到上限时，请求最多等多久空位；0 = 不等，直接 503'),
          num('breaker_threshold', ['pool', 'breaker_threshold'], '连续失败熔断阈值', '3', { min: 1 }),
          duration('breaker_cooldown', ['pool', 'breaker_cooldown'], '熔断基础时长', '30m'),
          duration('breaker_cooldown_max', ['pool', 'breaker_cooldown_max'], '熔断退避上限', '6h'),
        ],
      },
      {
        title: '冷却与降权',
        fields: [
          duration('soft_rate', ['cooldown', 'soft_rate'], '软限流冷却基数', '600s'),
          duration('soft_rate_max', ['cooldown', 'soft_rate_max'], '软冷却退避上限', '2h'),
          num('degrade_threshold', ['pool', 'degrade_threshold'], '连败降权阈值', '5', { min: 1, hint: '未知错误连败 N 次临时出池' }),
          duration('degrade_cooldown', ['pool', 'degrade_cooldown'], '连败降权时长', '10m'),
          duration('degrade_cooldown_max', ['pool', 'degrade_cooldown_max'], '连败降权上限', '2h'),
          num('restriction_wave_threshold', ['pool', 'restriction_wave_threshold'], '风控波阈值', '2',
            { min: 0, hint: '窗口内同一个域有这么多个号被上游 11140 风控限制，就暂停该域其余的号；0 关闭' }),
          duration('restriction_wave_window', ['pool', 'restriction_wave_window'], '风控波统计窗口', '15m'),
          duration('restriction_wave_pause', ['pool', 'restriction_wave_pause'], '风控波暂停时长', '30m', '到期自动恢复；账号池里点「解冻」可提前恢复单个号'),
        ],
      },
      {
        title: '选号权重',
        fields: [
          {
            name: 'cross_realm', path: ['pool', 'cross_realm'], label: '跨域负载均衡', kind: 'select',
            hint: '打开后 /v1/models 会多列出不带 cn: / global: 前缀的模型名，用这种名字请求时，网关自动在国内版、国际版账号里选号。带前缀的名字照旧只走那一域。需启用国际版',
            options: [
              { id: 'off', label: 'off — 关闭：不带前缀的模型名只用国内版账号' },
              { id: 'merge', label: 'merge — 合池：国内版、国际版账号一起选' },
              { id: 'cn_first', label: 'cn_first — 国内版优先：国内版没号可用时才用国际版账号' },
            ],
          },
          num('idle_weight_per_hour', ['pool', 'idle_weight_per_hour'], '闲置补偿 / 小时', '0.5', { step: 0.1 }),
          num('idle_weight_max', ['pool', 'idle_weight_max'], '闲置补偿上限', '5', { step: 0.1 }),
          duration('cost_explore_interval', ['pool', 'cost_explore_interval'], '成本探索窗口', '30m', '垄断破除：免费层垄断时定期搭车探索未知号；0 关停'),
          num('credit_floor', ['pool', 'credit_floor'], '积分保底', '100', { min: 0, hint: '余额低于此值不再接实测收费模型（保住免费模型可用）；0 关闭' }),
          toggle('prefer_expiring', ['pool', 'prefer_expiring'], '快过期积分优先', '软偏好：弱于会话粘性与模型成本分层'),
          duration('expiring_soon', ['pool', 'expiring_soon'], '快过期路由窗口', '168h', '窗口内有快过期批次的账号选号权重 ×3（不排序、与金额无关）；0 关闭'),
          duration('ttl', ['session_sticky', 'ttl'], '会话粘性 TTL', '30m', undefined, true),
        ],
      },
    ],
  },
  {
    id: 'upstream',
    label: '上游与高级',
    sections: [
      {
        title: '超时',
        fields: [
          duration('read_timeout', ['server', 'read_timeout'], '入站请求读取上限', '300s',
            '含请求体上传；大上下文经反代转发超时会报 400 read body: i/o timeout，调大即可；0 = 不限制', true),
          num('timeout_seconds', ['upstream', 'timeout_seconds'], '短请求超时（秒）', '120', { min: 1, restart: true }),
          num('header_timeout_seconds', ['upstream', 'header_timeout_seconds'], '聊天首字节超时（秒）', '120', { min: 1, restart: true }),
          num('idle_timeout_seconds', ['upstream', 'idle_timeout_seconds'], '流空闲超时（秒）', '300', { min: 1, restart: true }),
        ],
      },
      {
        title: '出站请求',
        fields: [
          { name: 'user_agent', path: ['upstream', 'user_agent'], label: '出站 User-Agent', kind: 'text', placeholder: '留空 = 按账号域对齐官方桌面端（国内版 WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0）', hint: '影响官网积分记录「使用端」显示', restart: true, clearable: true },
          {
            name: 'prompt_mode', path: ['prompt', 'mode'], label: '系统提示词模式', kind: 'select', restart: true,
            options: [
              { id: 'custom', label: 'custom — 网关自有提示词（避免指纹误报）' },
              { id: 'append', label: 'append — 客户端 system 后插网关提示词（并用）' },
              { id: 'passthrough', label: 'passthrough — 透传客户端原始 system' },
            ],
          },
          { name: 'prompt_file', path: ['prompt', 'file'], label: '提示词文件路径', kind: 'text', placeholder: '留空 = 内置默认提示词', restart: true, clearable: true },
          toggle('sanitize_blacklist_fingerprints', ['features', 'sanitize_blacklist_fingerprints'], '出站请求指纹脱敏'),
          toggle('model_optimization', ['upstream', 'model_optimization'], '允许上游用对话做模型优化',
            '关闭时每条对话都声明不允许（X-Private-Data: true），和官方桌面端默认一样；打开等于在桌面端打开「模型优化」：个人号允许，企业号仍不允许'),
          toggle('session_sticky_enabled', ['session_sticky', 'enabled'], '会话粘性路由'),
        ],
      },
    ],
  },
  {
    id: 'logs',
    label: '日志留存',
    sections: [
      {
        title: '请求记录',
        store: 'requests',
        fields: [
          toggle('request_archive_enabled', ['logging', 'request_archive_enabled'], '留存请求记录（JSONL 归档）',
            '只记时间、状态、模型、账号、耗时、token、积分和失败原因，不记提示词、响应正文或密钥；关掉后请求记录页只剩内存里的最近 100 条'),
          num('request_retention_days', ['logging', 'request_retention_days'], '保留天数', '7', { min: 0, hint: '0 = 永久保留（仍受容量上限）；超期的按天文件自动删除' }),
          num('request_archive_max_mb', ['logging', 'request_archive_max_mb'], '容量上限（MiB）', '100', { min: 1, hint: '超过上限先删最旧的文件' }),
          toggle('request_client_info', ['logging', 'request_client_info'], '记录调用来源（客户端 IP / User-Agent）',
            '开启后请求记录页显示每次调用来自哪个 IP、用什么客户端；关闭则归档与面板都不再出现来源信息'),
        ],
      },
      {
        title: '任务日志',
        store: 'task',
        fields: archive('task_log', '留存任务日志', '运行日志「任务」频道（签到、保活、旅行、成长任务等）按天落盘，运行日志页可按时间范围查历史', '30', '20'),
      },
      {
        title: '系统日志',
        store: 'system',
        fields: archive('system_log', '留存系统日志', '运行日志「系统」频道（选号、冷却、熔断、配置改动等）按天落盘，运行日志页可按时间范围查历史', '30', '50'),
      },
      {
        title: '用量统计',
        store: 'usage',
        fields: [
          num('usage_retention_days', ['logging', 'usage_retention_days'], '保留天数', '0',
            { min: 0, hint: '0 = 永久保留；超期的整点、整天用量明细自动删除，用量页能查到的最早时间随之后移' }),
        ],
      },
      {
        title: '积分获取',
        store: 'credit',
        fields: archive('credit_log', '留存积分获取',
          '签到、领奖、积分补偿、猫猫旅行、新手礼包、连登兑换领到的积分逐笔落盘，积分获取页可按时间范围查', '0', '20'),
      },
    ],
  },
]

export const ALL_FIELDS: FieldDef[] = CONFIG_TABS.flatMap((t) => t.sections.flatMap((s) => s.fields))
export const tabOfField = (name: string) => CONFIG_TABS.find((t) => t.sections.some((s) => s.fields.some((f) => f.name === name)))?.id

/** 表单值：开关是 boolean，其余一律是输入框里的原文（数字、时点、时长都在提交时再解析） */
export type FormValues = Record<string, string | boolean>

function dig(obj: unknown, path: string[]): unknown {
  let o = obj
  for (const k of path) {
    if (o == null || typeof o !== 'object') return undefined
    o = (o as Record<string, unknown>)[k]
  }
  return o
}

function put(obj: Record<string, unknown>, path: string[], val: unknown) {
  let o = obj
  for (const k of path.slice(0, -1)) {
    if (typeof o[k] !== 'object' || o[k] === null) o[k] = {}
    o = o[k] as Record<string, unknown>
  }
  o[path[path.length - 1]] = val
}

export function toForm(cfg: Record<string, unknown>): FormValues {
  const out: FormValues = {}
  for (const f of ALL_FIELDS) {
    const v = dig(cfg, f.path)
    if (f.kind === 'switch') out[f.name] = !!v
    else if (Array.isArray(v)) out[f.name] = v.join(', ')
    else out[f.name] = typeof v === 'string' || typeof v === 'number' ? String(v) : ''
  }
  return out
}

/** 表单 → 提交体：空输入框 = 沿用现值（不提交该键）；覆盖型字段（clearable）空串照发 */
export function fromForm(values: FormValues): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const f of ALL_FIELDS) {
    const v = values[f.name]
    if (f.kind === 'switch') {
      put(out, f.path, !!v)
      continue
    }
    const raw = String(v ?? '').trim()
    if (raw === '') {
      if (f.clearable) put(out, f.path, '')
      continue
    }
    if (f.kind === 'number') put(out, f.path, Number(raw))
    else if (f.kind === 'hours') put(out, f.path, parseHours(raw))
    else put(out, f.path, raw)
  }
  return out
}

export const parseHours = (raw: string) => raw.split(/[,，\s]+/).filter(Boolean).map(Number)

/** Go 时长语法（30m / 2h / 600s / 1h30m，可组合可带小数；单写 0 也合法），与后端 time.ParseDuration 同口径 */
export const DURATION_RE = /^(0|(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+)$/

/** 字段校验：返回错误文案，合法返回 null。空值合法（= 沿用现值） */
export function validateField(f: FieldDef, value: string | boolean): string | null {
  if (typeof value === 'boolean') return null
  const v = value.trim()
  if (!v) return null
  if (f.kind === 'duration' && !DURATION_RE.test(v)) return '格式应为 Go 时长：30m / 2h / 600s / 1h30m'
  if (f.kind === 'hours' && parseHours(v).some((h) => !Number.isInteger(h) || h < 0 || h > 23)) return '填 0–23 的整点小时，逗号分隔，如 9, 21'
  if (f.kind === 'number') {
    const n = Number(v)
    if (!Number.isFinite(n)) return '请填数字'
    if (f.min != null && n < f.min) return '不能小于 ' + f.min
  }
  return null
}

/** 与初始值不同的字段名 */
export function changedFields(initial: FormValues, current: FormValues): string[] {
  return ALL_FIELDS.filter((f) => String(initial[f.name] ?? '').trim() !== String(current[f.name] ?? '').trim()).map((f) => f.name)
}
