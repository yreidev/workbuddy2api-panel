import { describe, expect, it } from 'vitest'
import { chartPoints, chartStats, dayBoundaries, minGap, nearestTicks, parsePointTime, share } from './usage'
import type { UsagePoint } from './types'

const pt = (t: string, scope: 'hour' | 'day', p: number, c: number): UsagePoint => ({
  t, scope, prompt_tokens: p, completion_tokens: c, total_tokens: 0, requests: 1, errors: 0, avg_latency_ms: 0, avg_tokens_per_second: 0,
})

describe('usage chart', () => {
  it('小时桶与日桶解析到同一条连续时间轴，坏点丢弃', () => {
    expect(parsePointTime('2026-09-16T13')).toBe(new Date('2026-09-16T13:00:00').getTime())
    expect(parsePointTime('2026-09-16')).toBe(new Date('2026-09-16T00:00:00').getTime())
    const pts = chartPoints([pt('2026-09-16T13', 'hour', 1, 2), pt('bad', 'hour', 1, 1), pt('2026-09-15', 'day', 5, 5)])
    expect(pts.map((p) => p.raw)).toEqual(['2026-09-15', '2026-09-16T13'])
    expect(pts[1].total).toBe(3)
  })

  it('刻度落在实际柱子上且不重复；跨天画分隔线', () => {
    const pts = chartPoints([
      pt('2026-09-16T01', 'hour', 1, 1), pt('2026-09-16T02', 'hour', 1, 1),
      pt('2026-09-16T10', 'hour', 1, 1), pt('2026-09-17T03', 'hour', 1, 1),
    ])
    const ticks = nearestTicks(pts)
    expect(ticks.every((t) => pts.some((p) => p.t === t))).toBe(true)
    expect(new Set(ticks).size).toBe(ticks.length)
    expect(minGap(pts)).toBe(3600_000)
    expect(dayBoundaries(pts)).toEqual([pts[3].t])
  })

  it('峰值与均值；占比无分母时不伪造 0%', () => {
    const pts = chartPoints([pt('2026-09-30T09', 'hour', 35, 16), pt('2026-09-30T11', 'hour', 900, 100), pt('2026-09-30T13', 'hour', 10, 2)])
    const st = chartStats(pts)!
    expect(st.peak.raw).toBe('2026-09-30T11')
    expect(st.avg).toBeCloseTo((51 + 1000 + 12) / 3)
    expect(chartStats([])).toBeNull()
    expect(share(1, 4)).toBe(25)
    expect(share(1, 0)).toBeNull()
  })
})
