// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { afterEach, describe, expect, it } from 'vitest'
import { creditLogQuery, earnDetail, earnLabel } from './credits'
import { growthTitles } from './tasks'
import type { RangePreset, TimeRange } from './timerange'

const r = (preset: RangePreset): TimeRange => ({ preset, from: null, to: null })

describe('积分获取', () => {
  afterEach(() => growthTitles.clear())

  it('来源的中文名，不认识的原样显示', () => {
    expect(earnLabel('checkin')).toBe('每日签到')
    expect(earnLabel('redeem')).toBe('连登兑换')
    expect(earnLabel('lottery')).toBe('lottery')
  })

  it('说明：任务名认得就写任务名，连登档位写天数', () => {
    expect(earnDetail({ source: 'task', detail: 'chat_5' })).toBe('chat_5')
    growthTitles.set('chat_5', '对话 5 次')
    expect(earnDetail({ source: 'task', detail: 'chat_5' })).toBe('对话 5 次')
    expect(earnDetail({ source: 'redeem', detail: '14d' })).toBe('连登 14 天')
    expect(earnDetail({ source: 'redeem', detail: 'vip' })).toBe('vip')
    expect(earnDetail({ source: 'checkin' })).toBe('')
  })

  it('查询参数：时间范围折算成 from，账号、来源没选就不发', () => {
    const now = Date.now()
    const q = creditLogQuery(r('24'), 'u1', 'task', 500, now)
    expect(Number(q.get('from'))).toBe(Math.floor((now - 24 * 3600_000) / 1000))
    expect(q.get('uid')).toBe('u1')
    expect(q.get('source')).toBe('task')
    expect(q.get('limit')).toBe('500')
    expect(creditLogQuery(r('0'), '', '', 100).toString()).toBe('limit=100')
  })
})
