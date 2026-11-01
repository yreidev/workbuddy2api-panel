// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 客户端密钥的展示与表单整理（纯函数，见 keys.test.ts）。口径与网关 internal/apikeys 一致：
// 上限按「已用 ≥ 上限」拦，0 或不填 = 不限；白名单只认 * 通配，按实际发上游的模型名（不带域前缀）匹配。
import { fmtCredit, fmtTok, timeMs } from './format'
import { globMatch } from './routes'
import type { ApiKey, KeyInput, KeyLimits, KeyUsage } from './types'

export type KeyStatus = 'active' | 'disabled' | 'expired'

/** 和网关鉴权的判断顺序一致：先看停用，再看到期 */
export function keyStatus(k: Pick<ApiKey, 'enabled' | 'expired'>): KeyStatus {
  if (!k.enabled) return 'disabled'
  return k.expired ? 'expired' : 'active'
}

export const KEY_STATUS: Record<KeyStatus, { label: string; color: 'success' | 'default' | 'warning' }> = {
  active: { label: '启用中', color: 'success' },
  disabled: { label: '已停用', color: 'default' },
  expired: { label: '已过期', color: 'warning' },
}

/** 列表里遮住中间：sk-AbCd••••wxyz */
export function maskSecret(s: string): string {
  return s.length <= 12 ? s.slice(0, 3) + '••••' : s.slice(0, 7) + '••••' + s.slice(-4)
}

export type LimitKind = keyof KeyLimits

export const LIMIT_KINDS: { id: LimitKind; label: string }[] = [
  { id: 'requests', label: '请求次数' },
  { id: 'tokens', label: 'token' },
  { id: 'credits', label: '积分' },
]

export const fmtLimit = (kind: LimitKind, v: number) => (kind === 'credits' ? fmtCredit(v) : fmtTok(v))

export interface LimitRow {
  kind: LimitKind
  label: string
  used: number
  limit: number
  /** 0–100 */
  pct: number
  /** 已到上限：网关会拦下一次请求 */
  full: boolean
}

/** 设了上限的几项：已用 / 上限 / 占比 */
export function limitRows(limit: KeyLimits, used: KeyUsage): LimitRow[] {
  const out: LimitRow[] = []
  for (const k of LIMIT_KINDS) {
    const max = Number(limit[k.id] || 0)
    if (max <= 0) continue
    const u = Number(used[k.id] || 0)
    out.push({ kind: k.id, label: k.label, used: u, limit: max, pct: Math.min(100, u / max * 100), full: u >= max })
  }
  return out
}

/** 表单里的上限：null = 不限 */
export type LimitForm = Record<LimitKind, number | null>

export interface KeyForm {
  name: string
  note: string
  enabled: boolean
  /** 毫秒时间戳，null = 不过期 */
  expires: number | null
  daily: LimitForm
  total: LimitForm
  models: string[]
  /** 限定的账号分组 id，空 = 不限 */
  groups: string[]
}

const noLimits = (): LimitForm => ({ requests: null, tokens: null, credits: null })

export const emptyKeyForm = (): KeyForm => ({ name: '', note: '', enabled: true, expires: null, daily: noLimits(), total: noLimits(), models: [], groups: [] })

function limitForm(l: KeyLimits): LimitForm {
  const f = noLimits()
  for (const k of LIMIT_KINDS) f[k.id] = l[k.id] ? Number(l[k.id]) : null
  return f
}

export function keyFormFrom(k: ApiKey): KeyForm {
  return {
    name: k.name,
    note: k.note || '',
    enabled: k.enabled,
    expires: timeMs(k.expires_at),
    daily: limitForm(k.daily_limit),
    total: limitForm(k.total_limit),
    models: [...(k.models || [])],
    groups: [...(k.groups || [])],
  }
}

function limitsOut(f: LimitForm): KeyLimits {
  const out: KeyLimits = {}
  for (const k of LIMIT_KINDS) {
    const v = f[k.id]
    if (v != null && Number.isFinite(v) && v > 0) out[k.id] = k.id === 'credits' ? v : Math.floor(v)
  }
  return out
}

/** 提交给网关的整份设置（修改也是整份提交） */
export function keyInput(f: KeyForm): KeyInput {
  return {
    name: f.name.trim(),
    note: f.note.trim(),
    enabled: f.enabled,
    expires_at: f.expires == null ? null : new Date(f.expires).toISOString(),
    daily_limit: limitsOut(f.daily),
    total_limit: limitsOut(f.total),
    models: f.models,
    groups: f.groups,
  }
}

/** 提交前先在本地挡住网关一定会拒的输入（文案和网关一致） */
export function validateKeyForm(f: KeyForm): string | null {
  const name = f.name.trim()
  if (!name) return '密钥名称不能为空'
  if ([...name].length > 64) return '密钥名称最多 64 个字'
  if ([...f.note.trim()].length > 500) return '备注最多 500 个字'
  for (const l of [f.daily, f.total]) {
    if (Object.values(l).some((v) => v != null && v < 0)) return '用量上限不能是负数'
  }
  if (f.models.length > 100) return '模型白名单最多 100 项'
  return null
}

/** 往模型名单（密钥的白名单、账号不接的模型）里加一项：去首尾空白，带域前缀、中间有空格的拒绝，不区分大小写去重。
 * 返回新列表或错误文案 */
export function addModelPattern(list: string[], raw: string): string[] | string {
  const p = raw.trim()
  if (!p) return list
  if (p.includes(':')) return '按模型名填，不带 cn: / global: 前缀'
  if (/\s/.test(p)) return '模型名里不能有空格'
  if (p.length > 128) return '一项最长 128 个字符'
  if (list.some((x) => x.toLowerCase() === p.toLowerCase())) return list
  return [...list, p]
}

/** 目录里的模型名去掉域前缀、去重、排序（白名单的候选） */
export function bareModelIds(ids: string[]): string[] {
  return [...new Set(ids.map((id) => id.replace(/^(cn|global):/, '')))].sort()
}

/** 白名单放行目录里的哪些模型（空白名单 = 全部） */
export function allowedModels(patterns: string[], catalog: string[]): string[] {
  if (!patterns.length) return catalog
  return catalog.filter((m) => patterns.some((p) => globMatch(p, m)))
}

/** 用量页的密钥筛选：空串 = 全部，admin = 管理员密钥，- = 没开鉴权或加密钥维度之前的数据 */
export const USAGE_KEY_ALL = ''
export const USAGE_KEY_ADMIN = 'admin'
export const USAGE_KEY_NONE = '-'
