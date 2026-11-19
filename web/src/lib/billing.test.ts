// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { expect, it } from 'vitest'
import { chargeText, creditUnitName, providerChargeText } from './billing'
import { requestCredit } from './requests'
import type { ProviderUsage, RequestEvent } from './types'

it('单次费用区分未知、真实零和小额扣费，保留单位及精度', () => {
  const event = { target: 'qoder:cn', credit_known: true, credit_unit: 'credits', credit: 0.000012345 } as RequestEvent
  expect(requestCredit(event)).toBe('0.000012345 credits')
  expect(requestCredit({ ...event, credit_known: false })).toBe('credit —')
  expect(requestCredit({ ...event, credit: undefined })).toBe('0 credits')
  expect(requestCredit({ ...event, credit: Number.NaN })).toBe('credits —')
  expect(chargeText(-1)).toBe('未知')
  expect(chargeText(Infinity)).toBe('未知')
})

it('累计费用保留已知样本覆盖率，平台单位不能合并', () => {
  const usage = { charge: 0.000012345, charge_known: 2, requests: 3, unit: 'credits' } as ProviderUsage
  expect(providerChargeText(usage)).toBe('0.000012345 credits（2/3 次已知）')
  expect(providerChargeText({ ...usage, charge_known: 0 })).toBe('未知')
  expect(providerChargeText({ ...usage, charge: 0 })).toBe('0 credits（2/3 次已知）')
  expect(creditUnitName('qoder:cn:credits')).toBe('Qoder 国内 credits')
  expect(creditUnitName('qoder:global:credits')).toBe('Qoder 国际 credits')
  expect(creditUnitName('traework:cn:credits')).toBe('TraeWork 国内 credits')
  expect(creditUnitName('workbuddy:credits')).toBe('WorkBuddy credit')
})
