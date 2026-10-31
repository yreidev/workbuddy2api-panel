// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号池的状态推导、筛选与排序（纯函数，见 accounts.test.ts）。
import { dur, fmtLocalDateTime, timeMs } from './format'
import type { Account, RateLimitedModel } from './types'

/** paused = 暂停选号：不参与选号，但签到 / 活跃上报 / 保活 / 刷新余额照常 */
export type AccountState = 'ok' | 'cooling' | 'paused' | 'disabled'
/** 与后端总览计数同口径：禁用优先，其次暂停选号（暂停中的号哪怕在冷却也算暂停），再次冷却 */
export type StatusFilter = 'all' | 'ok' | 'cooling' | 'paused' | 'disabled'

export interface Health {
  state: AccountState
  /** 冷却截止时刻（毫秒）；未冷却为 null。暂停选号的账号也可能同时在冷却 */
  coolEnd: number | null
  /** 冷却类型：熔断 / 连败降权 / 积分冷却 / 风控暂停 / 限流冷却 */
  kind: string
}

/**
 * 账号健康：禁用优先，其次暂停选号；冷却取「账号级冷却、熔断、连败降权」三者最晚的截止时刻。
 * cool_remaining_sec 是接口返回时刻的剩余秒数，用 fetchedAt 换算成绝对时刻，页面才能逐秒倒数。
 */
export function accountHealth(a: Account, fetchedAt: number): Health {
  if (a.disabled) return { state: 'disabled', coolEnd: null, kind: '已禁用' }
  const soft = a.cool_remaining_sec ? fetchedAt + a.cool_remaining_sec * 1000 : 0
  const breaker = timeMs(a.breaker_until) ?? 0
  const degrade = timeMs(a.degrade_until) ?? 0
  const end = Math.max(soft, breaker, degrade)
  if (end <= fetchedAt) return a.paused ? { state: 'paused', coolEnd: null, kind: '已暂停选号' } : { state: 'ok', coolEnd: null, kind: '可用' }
  const kind = breaker > Math.max(soft, degrade) ? '熔断'
    : degrade > soft ? '连败降权'
    : a.cool_kind === 'hard_credit' ? '积分冷却'
    : a.cool_kind === 'restriction_wave' ? '风控暂停' : '限流冷却'
  return { state: a.paused ? 'paused' : 'cooling', coolEnd: end, kind }
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

export function filterAccounts(list: Account[], fetchedAt: number, status: StatusFilter, q: string): Account[] {
  const kw = q.trim().toLowerCase()
  return list.filter((a) => {
    if (!matchStatus(accountHealth(a, fetchedAt).state, status)) return false
    return !kw || a.uid.toLowerCase().includes(kw) || (a.nickname || '').toLowerCase().includes(kw)
  })
}

export function sortAccounts(list: Account[], key: AccountSortKey | null, desc: boolean): Account[] {
  if (!key) return list
  const val = (a: Account): number | string => {
    switch (key) {
      case 'name': return (a.nickname || a.uid).toLowerCase()
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

/** 企业切换会话的说明（账号名旁的标签悬停、详情抽屉） */
export const ENTERPRISE_SWITCH_TIP = '登录时浏览器停在企业空间，凭证是企业切换会话：成长任务、连登、猫猫旅行、夜猫子做了也不计分，网关都跳过（签到、对话照常）。请在浏览器里切回个人身份，移除这个号后重新登录。'
