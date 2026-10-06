// 账号池的状态推导、筛选与排序（纯函数，见 accounts.test.ts）。
import { dur, fmtLocalDateTime, timeMs } from './format'
import type { Account, RateLimitedModel } from './types'

/** paused = 暂停选号：不参与选号，但签到 / 活跃上报 / 保活 / 刷新余额照常 */
export type AccountState = 'ok' | 'cooling' | 'paused' | 'disabled'
/** 「已禁用」一栏同时包含暂停选号的账号，与后端 disabled 计数同口径 */
export type StatusFilter = 'all' | 'ok' | 'cooling' | 'disabled'

export interface Health {
  state: AccountState
  /** 冷却截止时刻（毫秒）；未冷却为 null。暂停选号的账号也可能同时在冷却 */
  coolEnd: number | null
  /** 冷却类型：熔断 / 连败降权 / 积分冷却 / 限流冷却 */
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
    : a.cool_kind === 'hard_credit' ? '积分冷却' : '限流冷却'
  return { state: a.paused ? 'paused' : 'cooling', coolEnd: end, kind }
}

/** 禁用或冷却中：操作按钮给「解冻」（解冻是全清，会一并解除暂停选号） */
export const isFrozen = (h: Health) => h.state === 'disabled' || h.coolEnd != null

const matchStatus = (state: AccountState, f: StatusFilter) =>
  f === 'all' || state === f || (f === 'disabled' && state === 'paused')

/** 暂时不能用的模型（后端只返回仍在冷却中的条目，这里只去掉缺模型名的脏数据） */
export function activeRateLimits(a: Account) {
  return (a.rate_limited_models || []).filter((m) => m && m.model)
}

export interface RateLimitInfo {
  model: string
  unavailable: boolean
  /** 完整说明：「预计 2026-09-28 16:00 解封（剩余 2时00分） · 网关最快 1时00分 后重试」 */
  detail: string
  /** 表格里的短说明：「2时00分后解封」「不可用 · 1时00分后重试」 */
  short: string
  /** 上游重置时刻优先，没有就用网关最早重试时刻 */
  deadline: number | null
}

/**
 * 限流 / 模型不可用的展示文案（口径与上游旧面板 rateLimitMeta 一致，见 accounts.test.ts）：
 * 上游给了重置时间（reset_at）就按它说「几点解封」，网关更早重试（until 早于 reset_at）时补一句；
 * 只有 until 就按它说「几点恢复」；都没有就是「时间未知」。模型不可用只说多久后重试。
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
  let detail = resetAt != null
    ? '预计 ' + fmtLocalDateTime(resetAt) + ' 解封' + (remaining ? '（剩余 ' + dur(remaining) + '）' : '')
    : until != null ? '预计 ' + fmtLocalDateTime(until) + ' 恢复（剩余 ' + dur(remaining) + '）' : '预计解封时间未知'
  if (until != null && resetAt != null && until < resetAt) {
    detail += ' · 网关最快 ' + dur(Math.max(0, Math.round((until - now) / 1000))) + ' 后重试'
  }
  return { model, unavailable: false, deadline, detail, short: remaining ? dur(remaining) + '后解封' : '解封时间未知' }
}

/** 积分进度百分比：有总额按 剩余/总额；旧数据没有总额时按池内最高 = 100% */
export function creditPercent(a: Account, poolMax: number): number {
  const c = a.credits || 0
  if (a.credits_total && a.credits_total > 0) return Math.min(100, Math.round((c / a.credits_total) * 100))
  return Math.round((c / Math.max(1, poolMax)) * 100)
}

export type AccountSortKey = 'name' | 'credits' | 'errors' | 'last_success'

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
