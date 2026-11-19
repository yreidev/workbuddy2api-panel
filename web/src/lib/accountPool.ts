// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { NO_GROUP } from './accounts'
import type { Account, ProviderAccount, ProviderUsage } from './types'

export interface PoolFilter { product: string; region: string; query: string; group: string }

export interface ProviderCardUsage { requests: number; failed: number; tokens: number; tokensKnown: number }

/** 按账号汇总当前筛选后的账本，保留有 token 的请求数，以区分完整与部分用量。 */
export function providerCardUsage(rows: ProviderUsage[]) {
  const byAccount = new Map<string, ProviderCardUsage>()
  for (const row of rows) {
    const sum = byAccount.get(row.account_id) ?? { requests: 0, failed: 0, tokens: 0, tokensKnown: 0 }
    sum.requests += row.requests
    sum.failed += row.failed
    sum.tokens += row.prompt_tokens + row.completion_tokens
    sum.tokensKnown += row.tokens_known
    byAccount.set(row.account_id, sum)
  }
  return byAccount
}

/** 产品和地区必须匹配同一个通道，避免多通道账号把两者交叉拼接。 */
export function matchesPoolTarget(target: string, { product, region }: Pick<PoolFilter, 'product' | 'region'>) {
  const [p, r] = target.split(':')
  return (product === 'all' || product === p) && (region === 'all' || region === r)
}

export function filterAccountPool(workbuddy: Account[], providers: ProviderAccount[], filter: PoolFilter) {
  const q = filter.query.trim().toLocaleLowerCase()
  const matches = (groups: string[], names: string[]) => (
    (!filter.group || (filter.group === NO_GROUP ? !groups.length : groups.includes(filter.group))) &&
    (!q || names.some((name) => name.toLocaleLowerCase().includes(q)))
  )
  return {
    workbuddy: workbuddy.filter((a) => matchesPoolTarget(`workbuddy:${a.realm || 'cn'}`, filter) && matches(a.groups ?? [], [a.uid, a.nickname ?? '', a.note ?? ''])),
    providers: providers.filter((a) => a.targets.some((t) => matchesPoolTarget(t, filter)) && matches(a.groups, [a.account_id, a.name])),
  }
}
