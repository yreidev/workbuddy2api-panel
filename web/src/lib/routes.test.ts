// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it } from 'vitest'
import { cleanRoutes, explainRoute, globMatch, routesFromConfig, shadowedBy, validateRoutes, type ModelRoute } from './routes'

it('完整新平台前缀不改写，裸名不会借同名新平台目录直通', () => {
  const rules = [{ match: '*', target: 'qoder:cn:target' }]
  expect(explainRoute(rules, 'qoder:global:same', [])).toEqual({ kind: 'prefixed' })
  expect(explainRoute(rules, 'same', ['qoder:cn:same'])).toEqual({ kind: 'route', index: 0, target: 'qoder:cn:target' })
})

describe('globMatch', () => {
  it.each([
    ['claude-*haiku*', 'claude-haiku-4-5-20251001', true],
    ['claude-*haiku*', 'Claude-3-5-HAIKU', true],
    ['claude-*', 'claude', false],
    ['claude-*', 'claude-', true],
    ['*', 'anything', true],
    ['gpt-*-codex*', 'gpt-5.5-codex', true],
    ['gpt-*-codex*', 'gpt-5.5', false],
    ['a*a', 'a', false],
    ['a*b*c', 'axxbyyc', true],
    ['a*b*c', 'acb', false],
    ['exact', 'exact', true],
    ['exact', 'exactly', false],
  ])('%s ~ %s → %s', (p, s, want) => {
    expect(globMatch(p, s)).toBe(want)
  })
})

describe('routesFromConfig / cleanRoutes / validateRoutes', () => {
  it('缺失、null、坏项', () => {
    expect(routesFromConfig({})).toEqual([])
    expect(routesFromConfig({ model_routes: null })).toEqual([])
    expect(routesFromConfig({ model_routes: [{ match: 'a', target: 'b' }, null, 'x', { match: 'c' }] }))
      .toEqual([{ match: 'a', target: 'b' }, { match: 'c', target: '' }])
  })
  it('整理与校验', () => {
    const rs: ModelRoute[] = [{ match: ' a* ', target: ' m ' }, { match: '', target: '' }, { match: 'b', target: '' }]
    expect(cleanRoutes(rs)).toEqual([{ match: 'a*', target: 'm' }, { match: 'b', target: '' }])
    expect(validateRoutes(rs)).toBe('第 2 条缺少目标模型')
    expect(validateRoutes([{ match: '', target: 'm' }])).toBe('第 1 条缺少匹配')
    expect(validateRoutes([{ match: 'a', target: 'm*' }])).toBe('第 1 条的目标模型不能带 *')
    expect(validateRoutes([{ match: 'a', target: 'cn:m' }])).toBeNull()
  })
})

describe('explainRoute', () => {
  const rs: ModelRoute[] = [
    { match: 'claude-*haiku*', target: 'cn:deepseek-v4-flash' },
    { match: 'claude-*', target: 'glm-5.2' },
  ]
  const catalog = ['cn:glm-5.2', 'global:gpt-5.5']
  it.each([
    ['', { kind: 'empty' }],
    ['global:whatever', { kind: 'prefixed' }],
    ['gpt-5.5', { kind: 'catalog' }],
    ['claude-haiku-4-5', { kind: 'route', index: 0, target: 'cn:deepseek-v4-flash' }],
    ['claude-sonnet-4-5', { kind: 'route', index: 1, target: 'glm-5.2' }],
    ['qwen-max', { kind: 'none' }],
  ])('%s', (name, want) => {
    expect(explainRoute(rs, name, catalog)).toEqual(want)
  })
})

describe('shadowedBy', () => {
  const r = (...ms: string[]): ModelRoute[] => ms.map((match) => ({ match, target: 'x' }))
  it('前面更宽的规则挡住后面的', () => {
    expect(shadowedBy(r('claude-*', 'claude-*sonnet*', 'claude-*haiku*'))).toEqual([null, 0, 0])
    expect(shadowedBy(r('*', 'gpt-5'))).toEqual([null, 0])
    expect(shadowedBy(r('Claude-Haiku', 'claude-haiku'))).toEqual([null, 0])
  })
  it('窄的在前、互不包含、空匹配都不算', () => {
    expect(shadowedBy(r('claude-*haiku*', 'claude-*'))).toEqual([null, null])
    expect(shadowedBy(r('claude-*opus*', 'claude-*sonnet*'))).toEqual([null, null])
    expect(shadowedBy(r('', 'a', ' '))).toEqual([null, null, null])
  })
})
