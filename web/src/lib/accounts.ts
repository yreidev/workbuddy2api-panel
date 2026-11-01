// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号池的状态推导、筛选与排序（纯函数，见 accounts.test.ts）。
import { dur, fmtLocalDateTime, formatLatency, timeMs } from './format'
import type { Account, LivenessResult, Model, Overview, RateLimitedModel } from './types'

/**
 * paused = 暂停选号：不参与选号，但签到 / 活跃上报 / 保活 / 刷新余额照常；
 * blocked = 被代理池拦着（代理不通或到期、在等代理、开着「只许走号的代理」却没绑）：不接请求、不跑定时任务
 */
export type AccountState = 'ok' | 'cooling' | 'paused' | 'blocked' | 'disabled'
/** 与后端总览计数同口径：禁用优先，其次暂停选号（暂停中的号哪怕在冷却也算暂停），再次被代理拦着，再次冷却 */
export type StatusFilter = 'all' | 'ok' | 'cooling' | 'paused' | 'blocked' | 'disabled'

export interface Health {
  state: AccountState
  /** 冷却截止时刻（毫秒）；未冷却为 null。暂停选号、被代理拦着的账号也可能同时在冷却 */
  coolEnd: number | null
  /** 冷却类型：熔断 / 连败降权 / 积分冷却 / 风控暂停 / 限流冷却 */
  kind: string
  /** 被代理池拦着时的短说明（见 blockedLabel）；没拦时没有 */
  proxy?: string
}

/**
 * 代理池拦着号的原因归成短说明，卡片上的状态标签用。按原因开头的几个字认（文案在
 * internal/proxies/bind.go 的 gateLocked），认不出就笼统说「因代理暂停」。
 */
export function blockedLabel(reason: string): string {
  if (reason.startsWith('没绑代理')) return '没绑代理'
  if (reason.startsWith('等代理')) return '等代理'
  if (reason.startsWith('代理「') && reason.endsWith('」已到期')) return '代理到期'
  if (reason.startsWith('代理「') && reason.includes('」连不上')) return '代理不通'
  return '因代理暂停'
}

/**
 * 账号健康：禁用优先，其次暂停选号，再次被代理拦着；冷却取「账号级冷却、熔断、连败降权」三者最晚的截止时刻。
 * cool_remaining_sec 是接口返回时刻的剩余秒数，用 fetchedAt 换算成绝对时刻，页面才能逐秒倒数。
 */
export function accountHealth(a: Account, fetchedAt: number): Health {
  if (a.disabled) return { state: 'disabled', coolEnd: null, kind: '已禁用' }
  const proxy = a.proxy_blocked ? blockedLabel(a.proxy_blocked) : undefined
  const soft = a.cool_remaining_sec ? fetchedAt + a.cool_remaining_sec * 1000 : 0
  const breaker = timeMs(a.breaker_until) ?? 0
  const degrade = timeMs(a.degrade_until) ?? 0
  const end = Math.max(soft, breaker, degrade)
  if (end <= fetchedAt) {
    if (a.paused) return { state: 'paused', coolEnd: null, kind: '已暂停选号', proxy }
    return proxy ? { state: 'blocked', coolEnd: null, kind: proxy, proxy } : { state: 'ok', coolEnd: null, kind: '可用' }
  }
  const kind = breaker > Math.max(soft, degrade) ? '熔断'
    : degrade > soft ? '连败降权'
    : a.cool_kind === 'hard_credit' ? '积分冷却'
    : a.cool_kind === 'restriction_wave' ? '风控暂停' : '限流冷却'
  return { state: a.paused ? 'paused' : proxy ? 'blocked' : 'cooling', coolEnd: end, kind, proxy }
}

/** 禁用或冷却中：操作按钮给「解冻」（解冻是全清，会一并解除暂停选号） */
export const isFrozen = (h: Health) => h.state === 'disabled' || h.coolEnd != null

const matchStatus = (state: AccountState, f: StatusFilter) => f === 'all' || state === f

/** 暂时不能用的模型（后端只返回仍在冷却中的条目，这里只去掉缺模型名的脏数据） */
export function activeRateLimits(a: Account) {
  return (a.rate_limited_models || []).filter((m) => m && m.model)
}

export interface RateLimitInfo {
  model: string
  unavailable: boolean
  /** 完整说明：「预计 2026-09-28 16:00 解封（剩余 2时00分）」「网关 2026-09-28 15:00 起重试（剩余 1时00分） · 上游称 2026-09-28 16:00 恢复」 */
  detail: string
  /** 表格里的短说明：「2时00分后解封」「1时00分后重试」「不可用 · 1时00分后重试」 */
  short: string
  /** 上游重置时刻优先，没有就用网关最早重试时刻 */
  deadline: number | null
}

/**
 * 限流 / 模型不可用的展示文案（口径与上游旧面板 rateLimitMeta 一致，见 accounts.test.ts）：
 * 上游给了重置时间（reset_at）就按它说「几点解封」；网关更早重试（until 早于 reset_at，网关把重试
 * 封顶在软冷却上限内）时两个时刻都写出来，倒计时跟着 until——这一行在 until 到了就消失，按 reset_at
 * 倒数会和实际恢复对不上（上游 20c3997）。只有 until 就按它说「几点恢复」；都没有就是「时间未知」。
 * 模型不可用只说多久后重试。
 */
export function rateLimitInfo(row: RateLimitedModel, now: number): RateLimitInfo {
  const model = row.model || '未知模型'
  const resetAt = timeMs(row.reset_at)
  const until = timeMs(row.until)
  const deadline = resetAt ?? until
  const remaining = deadline != null && deadline > now ? Math.round((deadline - now) / 1000) : 0
  if (row.kind === 'model_unavailable') {
    return {
      model, unavailable: true, deadline,
      detail: remaining ? '预计 ' + dur(remaining) + ' 后重试' : '等待重新探测',
      short: remaining ? '不可用 · ' + dur(remaining) + '后重试' : '不可用 · 等待重新探测',
    }
  }
  if (until != null && resetAt != null && until < resetAt) {
    const retryIn = until > now ? Math.round((until - now) / 1000) : 0
    return {
      model, unavailable: false, deadline,
      detail: '网关 ' + fmtLocalDateTime(until) + ' 起重试' + (retryIn ? '（剩余 ' + dur(retryIn) + '）' : '') +
        ' · 上游称 ' + fmtLocalDateTime(resetAt) + ' 恢复',
      short: retryIn ? dur(retryIn) + '后重试' : '等待重试',
    }
  }
  const detail = resetAt != null
    ? '预计 ' + fmtLocalDateTime(resetAt) + ' 解封' + (remaining ? '（剩余 ' + dur(remaining) + '）' : '')
    : until != null ? '预计 ' + fmtLocalDateTime(until) + ' 恢复（剩余 ' + dur(remaining) + '）' : '预计解封时间未知'
  return { model, unavailable: false, deadline, detail, short: remaining ? dur(remaining) + '后解封' : '解封时间未知' }
}

/**
 * 企业版不限量：上游 limitNum = -1，网关透出 credits_total = -1、credits 填一个极大值（让选号权重和
 * credit_floor 都不受限），所以这个 credits 不能拿来展示或求和。
 */
export const isUnlimited = (a: Pick<Account, 'credits_total'>) => a.credits_total === -1

/** 积分的文字口径（toast、详情）：「120 / 500」「120」「不限」 */
export function creditsText(credits: number | null | undefined, total: number | null | undefined): string {
  if (total === -1) return '不限'
  if (credits == null) return '—'
  return total && total > 0 ? credits + ' / ' + total : String(credits)
}

/** 池内合计（统计卡）与最高余额（没有总额的号按它算进度）：不限量的号不计入，单独计数 */
export function poolCredits(list: Account[]) {
  let remain = 0, total = 0, unlimited = 0, max = 1
  for (const a of list) {
    if (isUnlimited(a)) { unlimited++; continue }
    remain += a.credits || 0
    total += a.credits_total || 0
    max = Math.max(max, a.credits || 0)
  }
  return { remain, total, unlimited, max }
}

/** 积分进度百分比：不限量为满；有总额按 剩余/总额；旧数据没有总额时按池内最高 = 100% */
export function creditPercent(a: Account, poolMax: number): number {
  if (isUnlimited(a)) return 100
  const c = a.credits || 0
  if (a.credits_total && a.credits_total > 0) return Math.min(100, Math.round((c / a.credits_total) * 100))
  return Math.round((c / Math.max(1, poolMax)) * 100)
}

export type AccountSortKey = 'name' | 'credits' | 'errors' | 'last_success'

export type AccountSortDirection = 'ascending' | 'descending'

/** 卡片视图的排序选项：卡片没有表头可点，用下拉选；值是「排序键:方向」，default = 接口返回的原顺序 */
export const CARD_SORT_OPTIONS: { id: string; label: string }[] = [
  { id: 'default', label: '默认顺序' },
  { id: 'credits:descending', label: '积分从多到少' },
  { id: 'credits:ascending', label: '积分从少到多' },
  { id: 'errors:descending', label: '失败从多到少' },
  { id: 'errors:ascending', label: '失败从少到多' },
  { id: 'last_success:descending', label: '最近成功在前' },
  { id: 'last_success:ascending', label: '最久没成功在前' },
  { id: 'name:ascending', label: '名称 A→Z' },
  { id: 'name:descending', label: '名称 Z→A' },
]

/** 表格的排序状态 → 卡片下拉的选项 id（表格、卡片共用一份排序状态，切换视图时排序不丢） */
export function cardSortKey(sort: { column?: string | number; direction?: string } | undefined): string {
  const id = sort?.column != null ? `${sort.column}:${sort.direction ?? 'ascending'}` : 'default'
  return CARD_SORT_OPTIONS.some((o) => o.id === id) ? id : 'default'
}

/** 卡片下拉的选项 id → 排序状态；default 或不认识的 id 返回 undefined（不排序） */
export function parseCardSort(id: string): { column: AccountSortKey; direction: AccountSortDirection } | undefined {
  if (!CARD_SORT_OPTIONS.some((o) => o.id === id) || id === 'default') return undefined
  const [column, direction] = id.split(':') as [AccountSortKey, AccountSortDirection]
  return { column, direction }
}

/** 账号备注最长几个字（同后端 noteMaxRunes） */
export const NOTE_MAX = 40

/** 账号显示名，各页统一：备注 > 昵称 > UID 前 8 位 */
export function accountName(a: { uid: string; nickname?: string; note?: string }): string {
  return a.note || a.nickname || a.uid.slice(0, 8)
}

/** 总览里各账号的备注（uid → 备注）；别的接口只给 uid 和昵称，显示名靠它补上备注 */
export function notesOf(o: Pick<Overview, 'accounts'>): Map<string, string> {
  const out = new Map<string, string>()
  for (const a of o.accounts || []) if (a.note) out.set(a.uid, a.note)
  return out
}

/** 总览里各账号的备注和昵称：只给 uid 的接口（代理绑了哪些号）显示名靠昵称补上 */
export function namesOf(o: Pick<Overview, 'accounts'>) {
  const nicks = new Map<string, string>()
  for (const a of o.accounts || []) if (a.nickname) nicks.set(a.uid, a.nickname)
  return { notes: notesOf(o), nicks }
}

/**
 * 请求记录里存的账号标签是记录时的「昵称(uid8)」或「uid8」（后端 logfmt.Label）。这个号现在有备注，
 * 就换成「备注(uid8)」；认不出是哪个号（没对上，或 uid8 对上了不止一个号）照原样。
 */
export function relabel(label: string, notes: Map<string, string>): string {
  const open = label.lastIndexOf('(')
  const uid8 = label.endsWith(')') && open >= 0 ? label.slice(open + 1, -1) : label
  const hits = [...notes].filter(([uid]) => uid.slice(0, 8) === uid8)
  return hits.length === 1 ? hits[0][1] + '(' + uid8 + ')' : label
}

/** 按分组筛选时「没分组的号」这一项；'' 是全部，其余是分组 id */
export const NO_GROUP = '-'

export function filterAccounts(list: Account[], fetchedAt: number, status: StatusFilter, q: string, group = ''): Account[] {
  const kw = q.trim().toLowerCase()
  return list.filter((a) => {
    if (!matchStatus(accountHealth(a, fetchedAt).state, status)) return false
    if (group && !(group === NO_GROUP ? !a.groups?.length : a.groups?.includes(group))) return false
    return !kw || [a.uid, a.nickname, a.note].some((s) => (s || '').toLowerCase().includes(kw))
  })
}

export function sortAccounts(list: Account[], key: AccountSortKey | null, desc: boolean): Account[] {
  if (!key) return list
  const val = (a: Account): number | string => {
    switch (key) {
      case 'name': return accountName(a).toLowerCase()
      case 'credits': return a.credits ?? -1
      case 'errors': return a.err_total || 0
      case 'last_success': return timeMs(a.last_success) ?? 0
    }
  }
  const out = [...list].sort((x, y) => {
    const a = val(x), b = val(y)
    return a < b ? -1 : a > b ? 1 : 0
  })
  return desc ? out.reverse() : out
}

/** 上次测活的一句话：「通 · 首字 1.2s · 共 2.3s」「不通（429）：原因」 */
export function livenessSummary(r: LivenessResult): string {
  if (r.ok) {
    return ['通', r.ttfb_ms ? '首字 ' + formatLatency(r.ttfb_ms) : '', r.duration_ms ? '共 ' + formatLatency(r.duration_ms) : ''].filter(Boolean).join(' · ')
  }
  return '不通' + (r.status ? '（' + r.status + '）' : '') + (r.error ? '：' + r.error : '')
}

/** 单测弹窗里能换的模型：这个号所在域的目录（企业专属的只给企业号），去掉「cn:」「global:」前缀 */
export function livenessModels(models: Model[], a: Pick<Account, 'realm' | 'enterprise'>): string[] {
  const prefix = (a.realm === 'global' ? 'global' : 'cn') + ':'
  return models.filter((m) => m.id.startsWith(prefix) && (!m.enterprise_only || a.enterprise)).map((m) => m.id.slice(prefix.length))
}

/** 号的分组和优先级一行（卡片、账号表）：「分组 主力、备用 · 优先级 3」；没分组、优先级是 0 时为空。nameOf 查不到的分组不列 */
export function groupLine(a: Pick<Account, 'groups' | 'priority'>, nameOf: (id: string) => string | undefined): string {
  const names = (a.groups || []).map(nameOf).filter(Boolean)
  return [names.length ? '分组 ' + names.join('、') : '', a.priority ? '优先级 ' + a.priority : ''].filter(Boolean).join(' · ')
}

/** 优先级的上限（pool.MaxPriority），下限 0 */
export const MAX_PRIORITY = 99

/** 账号表多选后的批量操作（POST /panel/api/accounts/batch），菜单按这个顺序列 */
export type BatchAction =
  | 'liveness' | 'pause' | 'resume' | 'join_group' | 'leave_group' | 'priority' | 'block_models' | 'unblock_models' | 'revive' | 'disable' | 'remove'

/** 要先填参数的批量操作：模型名（加、删不接的模型）、分组（加进、退出）、优先级 */
export function batchParam(action: BatchAction): 'models' | 'group' | 'priority' | null {
  if (action === 'block_models' || action === 'unblock_models') return 'models'
  if (action === 'join_group' || action === 'leave_group') return 'group'
  return action === 'priority' ? 'priority' : null
}

export const BATCH_ACTIONS: { id: BatchAction; label: string }[] = [
  { id: 'liveness', label: '测活' },
  { id: 'pause', label: '暂停选号' },
  { id: 'resume', label: '恢复选号' },
  { id: 'join_group', label: '加进分组' },
  { id: 'leave_group', label: '退出分组' },
  { id: 'priority', label: '设优先级' },
  { id: 'block_models', label: '加不接的模型' },
  { id: 'unblock_models', label: '删不接的模型' },
  { id: 'revive', label: '解冻' },
  { id: 'disable', label: '禁用' },
  { id: 'remove', label: '移除' },
]

export interface BatchFailure { uid: string; error: string }
export interface BatchResp { ok: number; failed?: BatchFailure[]; file_errors?: BatchFailure[] }

/** 批量操作结果的提示：标题说几个成功几个失败；lines 逐个列出失败的号、移除了但凭证文件没删掉的号（nameOf 把 uid 换成显示名） */
export function batchSummary(label: string, r: BatchResp, nameOf: (uid: string) => string): { title: string; lines: string[] } {
  const failed = r.failed ?? []
  const fileErrs = r.file_errors ?? []
  const title = failed.length ? `${label}：${r.ok} 个成功，${failed.length} 个失败` : `${label}：${r.ok} 个已完成`
  const lines = [
    ...failed.map((f) => `${nameOf(f.uid)}：${f.error}`),
    ...fileErrs.map((f) => `${nameOf(f.uid)} 已移除，但凭证文件没删掉：${f.error}`),
  ]
  return { title, lines }
}
