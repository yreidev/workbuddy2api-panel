// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it } from 'vitest'
import { quotaAmount } from './providerQuota'

describe('平台额度显示', () => {
  it('区分明确的零、未知与无效数字', () => {
    expect(quotaAmount(0, 'credits')).toBe('0 credits')
    for (const value of [null, undefined, NaN, Infinity]) expect(quotaAmount(value, 'credits')).toBe('未知')
  })
  it('保留原单位和小数，不把小额余额舍成零', () => {
    expect(quotaAmount(12.125, 'USD')).toBe('12.125 USD')
    expect(quotaAmount(0.00000001, 'credits')).toBe('0.00000001 credits')
    expect(quotaAmount(7)).toBe('7 （单位未确认）')
  })
})
