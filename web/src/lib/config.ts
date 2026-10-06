// 配置页的字段定义：每个表单字段对应 config.json 里的一个路径（与旧版 CFG_MAP 一致），
// 按标签页分组。保存时只提交表单管理的键，后端深合并，未知键原样保留。

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

export const CONFIG_TABS: ConfigTab[] = [
  {
    id: 'service',
    label: '服务',
    sections: [{
      fields: [
        { name: 'listen', path: ['listen'], label: '监听地址', kind: 'text', placeholder: ':7863', restart: true },
        { name: 'api_key', path: ['api_key'], label: 'API 密钥', kind: 'password', placeholder: '留空 = 不鉴权', hint: '立即生效（含面板自身）' },
        num('package_detail_limit', ['panel', 'package_detail_limit'], '单账号明细默认展示条数', '5', { min: 1, hint: '积分构成页按最早到期展示，其余未用完包聚合' }),
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
        ],
      },
      {
        title: '选号权重',
        fields: [
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
          { name: 'user_agent', path: ['upstream', 'user_agent'], label: '出站 User-Agent', kind: 'text', placeholder: '留空 = CLI/2.63.2 CodeBuddy/2.63.2', hint: '影响官网积分记录「使用端」显示', restart: true, clearable: true },
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
          toggle('session_sticky_enabled', ['session_sticky', 'enabled'], '会话粘性路由'),
        ],
      },
      {
        title: '请求日志',
        fields: [
          toggle('request_client_info', ['logging', 'request_client_info'], '记录调用来源（客户端 IP / User-Agent）',
            '开启后运行日志页的请求记录显示每次调用来自哪个 IP、用什么客户端；关闭则归档与面板都不再出现来源信息'),
          toggle('request_archive_enabled', ['logging', 'request_archive_enabled'], '请求元数据 JSONL 归档',
            '只记录时间、状态、模型、账号、耗时、token、积分，不记录提示词、响应正文或密钥', true),
          num('request_retention_days', ['logging', 'request_retention_days'], '归档保留天数', '7', { min: 1, restart: true, hint: '超期文件在启动和定期清理时删除' }),
          num('request_archive_max_mb', ['logging', 'request_archive_max_mb'], '归档容量上限（MiB）', '100', { min: 1, restart: true, hint: '超过上限先删最旧的文件' }),
        ],
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
