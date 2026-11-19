// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { targetName } from './targets'
import type { ProviderUsage } from './types'

/** 保留上游数值精度，小额扣费不能四舍五入成免费。 */
export const chargeText = (value: number) => Number.isFinite(value) && value >= 0 ? String(value) : '未知'

export function creditUnitName(unit?: string) {
  if (!unit || unit === 'workbuddy:credits') return 'WorkBuddy credit'
  const target = unit.replace(/:credits$/, '')
  return `${targetName(target)} credits`
}

export function providerChargeText(u: ProviderUsage) {
  if (!u.charge_known) return '未知'
  return `${chargeText(u.charge)} ${u.unit || 'credits'}（${u.charge_known}/${u.requests} 次已知）`
}
