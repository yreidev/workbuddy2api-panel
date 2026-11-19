// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

const amount = new Intl.NumberFormat('zh-TW', { maximumSignificantDigits: 15 })

export function quotaNumber(value: number | null | undefined): string {
  return value == null || !Number.isFinite(value) ? '未知' : amount.format(value)
}

/** 余额按原单位显示；缺失不能记作零，也不按其他字段倒推。 */
export function quotaAmount(value: number | null | undefined, unit?: string): string {
  if (value == null || !Number.isFinite(value)) return '未知'
  return `${quotaNumber(value)} ${unit || '（单位未确认）'}`
}
