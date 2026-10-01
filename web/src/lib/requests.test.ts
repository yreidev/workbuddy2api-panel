// 从上游 internal/panel/frontend_test.go 的 TestAppJSRequestLogFormatting / TestAppJSRequestMatch 平移（期望值原样保留）。
import { describe, expect, it } from 'vitest'
import { cacheLevel, cacheRateText, matchRequest, outcomeLevel, requestCredit, requestLogText, type RequestFilter } from './requests'
import type { RequestEvent } from './types'

describe('请求指标', () => {
  it('摘要行紧凑可读，带调用来源与缓存命中；来源缺失时以 — 兜底', () => {
    const time = new Date(2026, 8, 28, 14, 5, 6).toISOString()
    const good: RequestEvent = {
      time, status: 200, ok: true, outcome: 'success', path: '/v1/chat/completions', model: 'glm-5.3', account: '账号(uid8)',
      duration_ms: 1250, total_tokens: 2300, credit_known: true, credit: 0.12, request_id: 'req-1',
      client_ip: '203.0.113.7', user_agent: 'python-requests/2.31.0',
    }
    const noSource: RequestEvent = { ...good, request_id: 'req-3', client_ip: '', user_agent: '' }
    const cached: RequestEvent = { ...good, request_id: 'req-2', cache_hit_tokens: 2257, cache_miss_tokens: 43 }
    expect(requestLogText(good)).toBe('14:05:06 | 200 成功 | glm-5.3 | 账号(uid8) | 203.0.113.7 | python-requests/2.31.0 | 1.25s | 2.3k tok | 0.12 credit | req-1')
    expect(requestLogText(noSource)).toBe('14:05:06 | 200 成功 | glm-5.3 | 账号(uid8) | — | — | 1.25s | 2.3k tok | 0.12 credit | req-3')
    expect(requestLogText(cached)).toBe('14:05:06 | 200 成功 | glm-5.3 | 账号(uid8) | 203.0.113.7 | python-requests/2.31.0 | 1.25s | 2.3k tok | 0.12 credit | 命中 98.1% | req-2')
    expect(outcomeLevel('success')).toBe('ok')
    expect(outcomeLevel('http_error')).toBe('error')
    expect(outcomeLevel('interrupted')).toBe('warn')
  })

  it('没返回积分时不显示成 0', () => {
    const e: RequestEvent = { time: '', request_id: '', path: '', status: 200, ok: true, outcome: 'success', duration_ms: 0, prompt_tokens: 3, completion_tokens: 4, credit_known: false }
    expect(requestLogText(e)).toBe('— | 200 成功 | — | — | — | — | — | 7 tok | credit — | —')
    // 扣了 0 积分：后端 omitempty 省掉 credit 字段，但 credit_known 为真
    expect(requestCredit({ ...e, credit_known: true })).toBe('0 credit')
  })

  it('缓存命中率：没有样本显示 —，按 90% / 80% 分级', () => {
    expect(cacheRateText(0, 0)).toBe('—')
    expect(cacheRateText(9, 1)).toBe('90%')
    expect([cacheLevel(95), cacheLevel(85), cacheLevel(50)]).toEqual(['ok', 'warn', 'error'])
  })

  it('筛选：IP / UA / 模型 / 账号 / 请求 ID 包含匹配（空格分词 AND），结果精确匹配', () => {
    const base = { outcome: 'success', client_ip: '203.0.113.7', user_agent: 'python-requests/2.31.0', model: 'cn:glm-5.3', account: '示例(uid8)', request_id: 'req-1' } as RequestEvent
    const other = { outcome: 'http_error', client_ip: '198.51.100.4', user_agent: 'Mozilla/5.0 Chrome/120', model: 'global:hy3', account: '甲(uid9)', request_id: 'req-2' } as RequestEvent
    const pick = (f: RequestFilter) => [base, other].filter((e) => matchRequest(e, f)).map((e) => e.request_id)
    expect(pick({ q: '', outcome: '' })).toEqual(['req-1', 'req-2'])
    expect(pick({ q: '203.0.113', outcome: '' })).toEqual(['req-1'])
    expect(pick({ q: 'chrome/120', outcome: '' })).toEqual(['req-2'])
    expect(pick({ q: 'glm', outcome: '' })).toEqual(['req-1'])
    // q 不匹配结果（结果有独立的下拉）
    expect(pick({ q: 'glm success', outcome: '' })).toEqual([])
    expect(pick({ q: 'glm chrome', outcome: '' })).toEqual([])
    expect(pick({ q: '', outcome: 'http_error' })).toEqual(['req-2'])
    expect(pick({ q: '198.51', outcome: 'http_error' })).toEqual(['req-2'])
  })
})
