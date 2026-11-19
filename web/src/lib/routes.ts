// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 模型路由规则（config.json 的 model_routes）：匹配口径与网关 internal/server/model_route.go、internal/glob 一致。

import { parseModelTarget } from './targets'

export interface ModelRoute {
  match: string
  target: string
}

/** 只认 * 通配（任意长度，含空串），不区分大小写；其余字符按字面比较 */
export function globMatch(pattern: string, s: string): boolean {
  const p = pattern.toLowerCase()
  let rest = s.toLowerCase()
  const parts = p.split('*')
  if (parts.length === 1) return p === rest
  if (!rest.startsWith(parts[0])) return false
  rest = rest.slice(parts[0].length)
  for (const part of parts.slice(1, -1)) {
    const i = rest.indexOf(part)
    if (i < 0) return false
    rest = rest.slice(i + part.length)
  }
  return rest.endsWith(parts[parts.length - 1])
}

/** 读配置里的规则表：缺失 / null → 空表，形状不对的项丢掉 */
export function routesFromConfig(cfg: Record<string, unknown>): ModelRoute[] {
  const raw = cfg.model_routes
  if (!Array.isArray(raw)) return []
  const str = (v: unknown) => (typeof v === 'string' ? v : '')
  return raw
    .filter((r): r is Record<string, unknown> => !!r && typeof r === 'object')
    .map((r) => ({ match: str(r.match), target: str(r.target) }))
}

/** 提交前整理：去掉两端空白，整行空着的丢掉 */
export function cleanRoutes(rs: ModelRoute[]): ModelRoute[] {
  return rs.map((r) => ({ match: r.match.trim(), target: r.target.trim() })).filter((r) => r.match || r.target)
}

/** 校验（与网关同口径）：返回第一处问题，没问题返回 null。行号从 1 起 */
export function validateRoutes(rs: ModelRoute[]): string | null {
  for (const [i, r] of cleanRoutes(rs).entries()) {
    if (!r.match) return `第 ${i + 1} 条缺少匹配`
    if (!r.target) return `第 ${i + 1} 条缺少目标模型`
    if (r.target.includes('*')) return `第 ${i + 1} 条的目标模型不能带 *`
  }
  return null
}

export type RouteVerdict =
  | { kind: 'empty' }
  | { kind: 'prefixed' }
  | { kind: 'catalog' }
  | { kind: 'route'; index: number; target: string }
  | { kind: 'none' }

/**
 * 「试一下」：这个模型名会被怎么处理。catalog 是面板模型列表的 id（带 cn: / global: 前缀），
 * 不带前缀的名字和其中任一去掉前缀后相同就算在目录里。
 */
export function explainRoute(rs: ModelRoute[], name: string, catalog: string[]): RouteVerdict {
  const n = name.trim()
  if (!n) return { kind: 'empty' }
  if (parseModelTarget(n).target) return { kind: 'prefixed' }
  if (catalog.some((id) => id.replace(/^(cn|global):/, '') === n)) return { kind: 'catalog' }
  const rules = cleanRoutes(rs)
  const index = rules.findIndex((r) => r.match && globMatch(r.match, n))
  return index < 0 ? { kind: 'none' } : { kind: 'route', index, target: rules[index].target }
}

/**
 * 每条规则被前面哪条挡住（下标），没被挡住为 null。前一条的匹配能匹配上后一条匹配的原文时，
 * 后一条能匹配的名字前一条都能匹配，它永远轮不到；比如 `claude-*` 挡住后面的 `claude-*sonnet*`。
 */
export function shadowedBy(rs: ModelRoute[]): (number | null)[] {
  return rs.map((r, j) => {
    const m = r.match.trim()
    if (!m) return null
    const i = rs.slice(0, j).findIndex((e) => e.match.trim() !== '' && globMatch(e.match.trim(), m))
    return i < 0 ? null : i
  })
}

/** Claude Code 三档的常用匹配，填进表里后只需选目标 */
export const CLAUDE_TIERS: ModelRoute[] = [
  { match: 'claude-*opus*', target: '' },
  { match: 'claude-*sonnet*', target: '' },
  { match: 'claude-*haiku*', target: '' },
]
