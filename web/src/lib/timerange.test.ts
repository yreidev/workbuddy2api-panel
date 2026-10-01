// 从上游 internal/panel/frontend_test.go 的 TestAppJSTimeRangeQuery 平移（期望值原样保留）。
import { describe, expect, it } from 'vitest'
import { midnight, rangeLabel, rangeQuery, type RangePreset, type TimeRange } from './timerange'

const r = (preset: RangePreset): TimeRange => ({ preset, from: null, to: null })
const fromOf = (q: URLSearchParams) => Number(q.get('from'))

describe('时间范围', () => {
  it('今天发浏览器本地 00:00，不带 to', () => {
    const q = rangeQuery(r('today'), true)
    expect(q.toString()).toBe('from=' + Math.floor(midnight().getTime() / 1000))
    expect(q.has('to')).toBe(false)
  })

  it('滚动预设：用量发 hours，请求记录折算成 from；全部历史都不发', () => {
    expect(rangeQuery(r('24'), true).toString()).toBe('hours=24')
    expect(rangeQuery(r('72'), true).toString()).toBe('hours=72')
    expect(rangeQuery(r('0'), true).toString()).toBe('')
    expect(rangeQuery(r('0'), false).toString()).toBe('')
    const now = Date.now()
    const log24 = rangeQuery(r('24'), false, now)
    expect(log24.has('hours')).toBe(false)
    expect(Math.abs(fromOf(log24) - Math.floor((now - 24 * 3600e3) / 1000))).toBeLessThan(120)
    expect(Math.abs(fromOf(rangeQuery(r('168'), false, now)) - Math.floor((now - 168 * 3600e3) / 1000))).toBeLessThan(120)
  })

  it('自定义发用户挑的起止，标签精确到分钟', () => {
    const custom: TimeRange = { preset: 'custom', from: new Date(2026, 8, 30, 9, 0, 0), to: new Date(2026, 8, 30, 18, 30, 0) }
    const sec = (d: Date) => Math.floor(d.getTime() / 1000)
    expect(rangeQuery(custom, true).toString()).toBe('from=' + sec(custom.from!) + '&to=' + sec(custom.to!))
    expect(rangeLabel(custom)).toBe('9-30 09:00 → 9-30 18:30')
    expect(rangeLabel(r('today'))).toBe('今天')
  })
})
