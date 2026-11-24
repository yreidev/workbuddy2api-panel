// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it } from 'vitest'
import { filterAccountPool, matchesPoolTarget, providerCardUsage, type PoolFilter } from './accountPool'
import { NO_GROUP } from './accounts'
import { hasProviderData } from './queries'
import type { Account, ProviderAccount, ProviderUsage } from './types'

const wb: Account[] = [
  { uid: 'wb-cn', realm: 'cn', nickname: '国内测试', credits: 20, cooling: false, disabled: false, consecutive_fails: 0, in_flight: 0, breaker_fails: 0, groups: ['team'], note: '备用账号' },
  { uid: 'wb-global', realm: 'global', nickname: '国际测试', credits: 30, cooling: false, disabled: false, consecutive_fails: 0, in_flight: 0, breaker_fails: 0 },
]
const providers: ProviderAccount[] = [
  { account_id: 'trae', name: 'Trae 测试', targets: ['traework:cn', 'traecode:cn'], groups: ['team'], priority: 0, disabled: false, in_flight: 0, models: [] },
  { account_id: 'qoder-cn', name: 'Qoder 国内', targets: ['qoder:cn'], groups: [], priority: 0, disabled: false, in_flight: 0, models: [] },
  { account_id: 'qoder-global', name: 'Qoder 国际', targets: ['qoder:global'], groups: ['team'], priority: 0, disabled: true, in_flight: 0, models: [] },
]
const all: PoolFilter = { product: 'all', region: 'all', query: '', group: '' }

describe('统一账号池筛选', () => {
  it('默认展示所有平台，共享 Work / Code 凭证只显示一次，禁用账号仍可管理', () => {
    const out = filterAccountPool(wb, providers, all)
    expect(out.workbuddy.map((a) => a.uid)).toEqual(['wb-cn', 'wb-global'])
    expect(out.providers.map((a) => a.account_id)).toEqual(['trae', 'qoder-cn', 'qoder-global'])
  })
  it('产品和地区交叉筛选，不掺入其他产品的同地区账号', () => {
    const out = filterAccountPool(wb, providers, { ...all, product: 'qoder', region: 'global' })
    expect(out.workbuddy).toEqual([])
    expect(out.providers.map((a) => a.account_id)).toEqual(['qoder-global'])
    expect(filterAccountPool(wb, providers, { ...all, product: 'workbuddy', region: 'cn' }).workbuddy.map((a) => a.uid)).toEqual(['wb-cn'])
    expect(filterAccountPool(wb, providers, { ...all, product: 'traecode' }).providers.map((a) => a.account_id)).toEqual(['trae'])
  })
  it('地区筛选涵盖所有产品，产品和地区必须匹配同一个通道', () => {
    const out = filterAccountPool(wb, providers, { ...all, region: 'global' })
    expect(out.workbuddy.map((a) => a.uid)).toEqual(['wb-global'])
    expect(out.providers.map((a) => a.account_id)).toEqual(['qoder-global'])
    expect(matchesPoolTarget('traecode:cn', { product: 'traecode', region: 'global' })).toBe(false)
    expect(filterAccountPool(wb, providers, { ...all, product: 'traecode', region: 'global' }).providers).toEqual([])
  })
  it('跨平台按分组筛选，没分组同时涵盖原账号与新平台账号', () => {
    const team = filterAccountPool(wb, providers, { ...all, group: 'team' })
    expect(team.workbuddy.map((a) => a.uid)).toEqual(['wb-cn'])
    expect(team.providers.map((a) => a.account_id)).toEqual(['trae', 'qoder-global'])
    const ungrouped = filterAccountPool(wb, providers, { ...all, group: NO_GROUP })
    expect(ungrouped.workbuddy.map((a) => a.uid)).toEqual(['wb-global'])
    expect(ungrouped.providers.map((a) => a.account_id)).toEqual(['qoder-cn'])
  })
  it('搜索忽略大小写，支持备注和账号 ID，可与分组组合', () => {
    expect(filterAccountPool(wb, providers, { ...all, query: '  备用  ' }).workbuddy.map((a) => a.uid)).toEqual(['wb-cn'])
    expect(filterAccountPool(wb, providers, { ...all, query: 'QODER-GLOBAL', group: 'team' }).providers.map((a) => a.account_id)).toEqual(['qoder-global'])
    expect(filterAccountPool(wb, providers, { ...all, query: 'Qoder', group: 'missing' }).providers).toEqual([])
  })
  it('只有新平台账号时不依赖 WorkBuddy 池，空池返回明确空集合', () => {
    expect(filterAccountPool([], providers, all).providers).toHaveLength(3)
    expect(filterAccountPool([], [], all)).toEqual({ workbuddy: [], providers: [] })
  })
})

it('卡片按账号累计不同通道的请求与已知 token，保留未知和已知零的区别', () => {
  const base: ProviderUsage = {
    account_id: 'trae', target: 'traework:cn', model: 'test', requests: 1, failed: 0,
    prompt_tokens: 7, completion_tokens: 3, tokens_known: 1, charge_known: 0, charge: 0,
  }
  const rows = [
    base,
    { ...base, target: 'traecode:cn', failed: 1, prompt_tokens: 0, completion_tokens: 0, tokens_known: 0 },
    { ...base, account_id: 'qoder', target: 'qoder:cn', prompt_tokens: 0, completion_tokens: 0 },
  ]
  const sums = providerCardUsage(rows)
  expect(sums.get('trae')).toEqual({ requests: 2, failed: 1, tokens: 10, tokensKnown: 1 })
  expect(sums.get('qoder')).toEqual({ requests: 1, failed: 0, tokens: 0, tokensKnown: 1 })
  expect(providerCardUsage([rows[1]]).get('trae')?.tokensKnown).toBe(0)
})

it('hasProviderData 在仅有账号或仅有历史用量时均返回 true', () => {
  const usage: ProviderUsage = {
    account_id: 'old-qoder', target: 'qoder:cn', model: 'qwen3', requests: 1, failed: 0,
    prompt_tokens: 10, completion_tokens: 5, tokens_known: 1, charge_known: 0, charge: 0,
  }
  expect(hasProviderData(undefined)).toBe(false)
  expect(hasProviderData({ accounts: [], usage: [] })).toBe(false)
  expect(hasProviderData({ accounts: [providers[0]], usage: [] })).toBe(true)
  expect(hasProviderData({ accounts: [], usage: [usage] })).toBe(true)
})
