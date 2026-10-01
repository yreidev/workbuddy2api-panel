// 请求指标（/panel/api/request_metrics、request_logs）的展示口径，与上游旧面板一致（见 requests.test.ts）。
import { fmtMs, fmtTok } from './format'
import type { RequestEvent } from './types'

export const OUTCOME_LABEL: Record<string, string> = {
  success: '成功', http_error: 'HTTP 错误', stream_error: '流错误', interrupted: '中断',
}

/** 结果的级别：HTTP 错误、流错误算错误，中断算警告 */
export function outcomeLevel(outcome: string): 'error' | 'warn' | 'ok' {
  if (outcome === 'http_error' || outcome === 'stream_error') return 'error'
  if (outcome === 'interrupted') return 'warn'
  return 'ok'
}

export function requestTokens(e: RequestEvent): number {
  return Number(e.total_tokens || 0) || Number(e.prompt_tokens || 0) + Number(e.completion_tokens || 0)
}

/** 积分：上游明确返回了才显示（没返回不等于扣了 0）。返回了 0 时后端省略 credit 字段（omitempty），按 0 显示 */
export function requestCredit(e: RequestEvent): string {
  if (!e.credit_known) return 'credit —'
  const v = Number(e.credit ?? 0)
  return Number.isFinite(v) ? String(Number(v.toFixed(2))) + ' credit' : 'credit —'
}

/** 缓存命中率 = 命中 / (命中 + 未命中)，百分数；上游没返回这一项时为 null */
export function cacheRate(hit?: number | null, miss?: number | null): number | null {
  const h = Number(hit || 0), total = h + Number(miss || 0)
  return total ? h / total * 100 : null
}

/** 缓存命中率文案：98.1%；没有样本时 — */
export function cacheRateText(hit?: number | null, miss?: number | null): string {
  const r = cacheRate(hit, miss)
  return r == null ? '—' : String(Math.round(r * 10) / 10) + '%'
}

/** 命中率的健康度：≥90% 好 / 80–90% 一般 / <80% 差（低命中意味着费用成倍放大） */
export function cacheLevel(rate: number): 'ok' | 'warn' | 'error' {
  return rate >= 90 ? 'ok' : rate >= 80 ? 'warn' : 'error'
}

/** 一行文字摘要（屏幕阅读器与悬停提示用；表格按列展示同样的内容） */
export function requestLogText(e: RequestEvent): string {
  const when = e.time ? new Date(e.time).toLocaleTimeString('zh-CN', { hour12: false }) : '—'
  const cache = cacheRateText(e.cache_hit_tokens, e.cache_miss_tokens)
  return [
    when,
    String(e.status || '—') + ' ' + (OUTCOME_LABEL[e.outcome] || e.outcome || '—'),
    e.model || '—',
    e.account || '—',
    e.client_ip || '—',
    e.user_agent || '—',
    fmtMs(e.duration_ms),
    fmtTok(requestTokens(e)) + ' tok',
    requestCredit(e),
    cache === '—' ? '' : '命中 ' + cache,
    e.request_id || '—',
  ].filter(Boolean).join(' | ')
}

export interface RequestFilter {
  /** 对 IP / UA / 模型 / 账号 / 请求 ID 做包含匹配，空格分词后逐个 AND */
  q: string
  /** 结果精确匹配，空 = 不筛 */
  outcome: string
}

/** 请求记录筛选：只在已拉取的条目上做，不发新请求 */
export function matchRequest(e: RequestEvent, f: RequestFilter): boolean {
  if (f.outcome && e.outcome !== f.outcome) return false
  const kws = f.q.toLowerCase().split(/\s+/).filter(Boolean)
  if (!kws.length) return true
  const text = [e.client_ip, e.user_agent, e.model, e.account, e.request_id].filter(Boolean).join(' ').toLowerCase()
  return kws.every((kw) => text.includes(kw))
}
