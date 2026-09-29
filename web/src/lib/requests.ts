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

/** 积分：上游明确返回了才显示（没返回不等于扣了 0） */
export function requestCredit(e: RequestEvent): string {
  if (!e.credit_known) return 'credit —'
  const v = Number(e.credit)
  return Number.isFinite(v) ? String(Number(v.toFixed(2))) + ' credit' : 'credit —'
}

/** 一行文字摘要（屏幕阅读器与悬停提示用；表格按列展示同样的内容） */
export function requestLogText(e: RequestEvent): string {
  const when = e.time ? new Date(e.time).toLocaleTimeString('zh-CN', { hour12: false }) : '—'
  return [
    when,
    String(e.status || '—') + ' ' + (OUTCOME_LABEL[e.outcome] || e.outcome || '—'),
    e.model || '—',
    e.account || '—',
    fmtMs(e.duration_ms),
    fmtTok(requestTokens(e)) + ' tok',
    requestCredit(e),
    e.request_id || '—',
  ].join(' | ')
}
