// 从上游 internal/panel/frontend_test.go 的 TestAppJSRequestLogFormatting 平移（期望值原样保留）。
import { describe, expect, it } from 'vitest'
import { outcomeLevel, requestLogText } from './requests'
import type { RequestEvent } from './types'

describe('请求指标', () => {
  it('滚动行紧凑可读，失败结果标成错误', () => {
    const time = new Date(2026, 8, 28, 14, 5, 6).toISOString()
    const good: RequestEvent = {
      time, status: 200, ok: true, outcome: 'success', path: '/v1/chat/completions', model: 'glm-5.3', account: '账号(uid8)',
      duration_ms: 1250, total_tokens: 2300, credit_known: true, credit: 0.12, request_id: 'req-1',
    }
    const bad: RequestEvent = { ...good, status: 500, ok: false, outcome: 'http_error', request_id: 'req-2' }
    expect(requestLogText(good)).toBe('14:05:06 | 200 成功 | glm-5.3 | 账号(uid8) | 1.25s | 2.3k tok | 0.12 credit | req-1')
    expect(requestLogText(bad)).toBe('14:05:06 | 500 HTTP 错误 | glm-5.3 | 账号(uid8) | 1.25s | 2.3k tok | 0.12 credit | req-2')
    expect(outcomeLevel(good.outcome)).toBe('ok')
    expect(outcomeLevel(bad.outcome)).toBe('error')
    expect(outcomeLevel('interrupted')).toBe('warn')
  })

  it('没返回积分时不显示成 0', () => {
    const e: RequestEvent = { time: '', request_id: '', path: '', status: 200, ok: true, outcome: 'success', duration_ms: 0, prompt_tokens: 3, completion_tokens: 4, credit_known: false }
    expect(requestLogText(e)).toBe('— | 200 成功 | — | — | — | 7 tok | credit — | —')
  })
})
