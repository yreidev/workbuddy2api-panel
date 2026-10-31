// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 条件查询部分从上游 internal/panel/frontend_test.go 的 TestAppJSModelFilter 平移（期望值原样保留；
// 上游能力是单选，这里是多选，单个能力的结果一致）。
import { describe, expect, it } from 'vitest'
import {
  addHiddenModel, EMPTY_MODEL_FILTER, filterModels, hiddenFromConfig, hiddenModelSet, modelAvailability, probeFor, rateValue, type ModelFilter,
  type ModelSort,
} from './models'
import type { Model, ModelLockRow, RequestEvent } from './types'

const list: Model[] = [
  { id: 'cn:glm-5.2', name: 'GLM 5.2', vendor: 'zhipu', supports_tool_call: true, supported_efforts: ['low', 'high'] },
  { id: 'cn:hunyuan', name: '混元', supports_images: true },
  { id: 'global:gpt-5', name: 'GPT-5', vendor: 'openai', supports_tool_call: true, supports_images: true },
]

const models = [
  { id: 'cn:glm-5.2', name: 'GLM-5.2', vendor: 'Zhipu', tags: ['视觉'], supports_tool_call: true, supports_images: true, supports_reasoning: true, can_disable_thinking: true, supported_efforts: ['high', 'xhigh'], default_effort: 'high', is_default: false, credits: '0.79', promo_factor: 0.5, promo_credits: '0.40', promo_label: '夜间折扣', context_length: 1000000, max_output_tokens: 131000 },
  { id: 'cn:hy3', name: 'Hy3', supports_tool_call: true, supports_images: true, supports_reasoning: true, can_disable_thinking: false, supported_efforts: ['low', 'high'], default_effort: 'high', is_default: false, credits: '0', promo_factor: 0, promo_credits: '0', promo_label: '限时免费', context_length: 192000, max_output_tokens: 64000 },
  { id: 'global:hy3', name: 'Hy3 Global', supports_tool_call: false, supports_images: false, supports_reasoning: false, supported_efforts: [], is_default: false, credits: '0.11', context_length: 1000000, max_output_tokens: 393000 },
  { id: 'cn:auto', name: 'Auto', supports_tool_call: true, supports_images: true, supports_reasoning: true, is_default: true, credits: undefined, context_length: 256000, max_output_tokens: 32000 },
] as Model[]

const ids = (f: Partial<ModelFilter>, sort: ModelSort = 'default') =>
  filterModels(models, { ...EMPTY_MODEL_FILTER, ...f }, sort).map((m) => m.id)

describe('models', () => {
  it('按域、能力、关键字筛选', () => {
    const f = (x: Partial<ModelFilter>) => filterModels(list, { ...EMPTY_MODEL_FILTER, ...x }).map((m) => m.id)
    expect(f({ realm: 'cn' })).toEqual(['cn:glm-5.2', 'cn:hunyuan'])
    expect(f({ caps: ['tools', 'vision'] })).toEqual(['global:gpt-5'])
    expect(f({ effort: 'any' })).toEqual(['cn:glm-5.2'])
    expect(f({ q: 'OPENAI' })).toEqual(['global:gpt-5'])
  })

  it('企业专属模型能按「企业专属」搜到', () => {
    const withEnterprise: Model[] = [...list, { id: 'cn:claude-sonnet', name: 'Claude Sonnet', enterprise_only: true }]
    expect(filterModels(withEnterprise, { ...EMPTY_MODEL_FILTER, q: '企业专属' }).map((m) => m.id)).toEqual(['cn:claude-sonnet'])
  })

  it('条件查询：域 / 能力 / 档位 / 价格 / 关键词（空格分词 AND）', () => {
    expect(ids({ realm: 'cn' })).toEqual(['cn:glm-5.2', 'cn:hy3', 'cn:auto'])
    expect(ids({ caps: ['tools'] })).toEqual(['cn:glm-5.2', 'cn:hy3', 'cn:auto'])
    expect(ids({ caps: ['vision'] })).toEqual(['cn:glm-5.2', 'cn:hy3', 'cn:auto'])
    expect(ids({ caps: ['reasoning'] })).toEqual(['cn:glm-5.2', 'cn:hy3', 'cn:auto'])
    expect(ids({ caps: ['default'] })).toEqual(['cn:auto'])
    expect(ids({ effort: 'off' })).toEqual(['cn:glm-5.2'])
    expect(ids({ effort: 'low' })).toEqual(['cn:hy3'])
    expect(ids({ promo: 'free' })).toEqual(['cn:hy3'])
    expect(ids({ promo: 'promo' })).toEqual(['cn:glm-5.2', 'cn:hy3'])
    expect(ids({ promo: 'discount' })).toEqual(['cn:glm-5.2'])
    expect(ids({ q: 'glm zhipu' })).toEqual(['cn:glm-5.2'])
    expect(ids({ q: 'glm nosuch' })).toEqual([])
  })

  it('排序：倍率低→高（没有倍率排最后）、上下文 / 输出大→小、ID A→Z', () => {
    expect(ids({}, 'rate')).toEqual(['cn:hy3', 'global:hy3', 'cn:glm-5.2', 'cn:auto'])
    expect(ids({}, 'context')).toEqual(['cn:glm-5.2', 'global:hy3', 'cn:auto', 'cn:hy3'])
    expect(ids({}, 'output')).toEqual(['global:hy3', 'cn:glm-5.2', 'cn:hy3', 'cn:auto'])
    expect(ids({}, 'name')).toEqual(['cn:auto', 'cn:glm-5.2', 'cn:hy3', 'global:hy3'])
    expect(rateValue(models[1])).toBe(0)
    expect(rateValue(models[3])).toBe(Infinity)
  })

  it('实测上限按精确键或 :后缀 关联', () => {
    const probes = { 'cn:glm-5.2': { measured: 1 }, 'global:gpt-5': { measured: 2 } }
    expect(probeFor(probes, 'cn:glm-5.2')?.measured).toBe(1)
    expect(probeFor(probes, 'gpt-5')?.measured).toBe(2)
    expect(probeFor({ 'global:glm-5.2': { measured: 3 } }, 'cn:glm-5.2')).toBeUndefined()
    expect(probeFor(probes, 'cn:hunyuan')).toBeUndefined()
  })
})

describe('models.hidden', () => {
  it('读配置：缺失、形状不对都当空表，非字符串的项丢掉', () => {
    expect(hiddenFromConfig({})).toEqual([])
    expect(hiddenFromConfig({ models: null })).toEqual([])
    expect(hiddenFromConfig({ models: { hidden: 'x' } })).toEqual([])
    expect(hiddenFromConfig({ models: { hidden: ['a', 1, null, 'cn:b'] } })).toEqual(['a', 'cn:b'])
  })
  it('归一：带前缀只藏那个域，裸名两个域都藏，别的冒号前缀算名字的一部分', () => {
    expect([...hiddenModelSet([' glm-5 ', 'cn:deepseek-v4', 'global:gpt-5', 'vendor:x', ' '])].sort()).toEqual([
      'cn:deepseek-v4', 'cn:glm-5', 'cn:vendor:x', 'global:glm-5', 'global:gpt-5', 'global:vendor:x',
    ])
  })
  it('添加：去空白、不重复', () => {
    expect(addHiddenModel(['a'], ' b ')).toEqual(['a', 'b'])
    expect(addHiddenModel(['a'], 'a')).toEqual(['a'])
    expect(addHiddenModel(['a'], '  ')).toEqual(['a'])
  })
})

describe('modelAvailability', () => {
  const lock = (o: Partial<ModelLockRow>): ModelLockRow => ({
    model: 'm', realm: 'global', total: 2, servable: 0, locked: 2, state: 'locked', unlock_at: '', fully_unlock_at: '', ...o,
  })
  const ev = (o: Partial<RequestEvent>): RequestEvent => ({
    time: '2026-10-31T09:00:00Z', request_id: 'r', path: '/v1/chat/completions', model: 'global:m', status: 200, ok: true,
    outcome: 'success', duration_ms: 1, credit_known: false, attempts: 1, ...o,
  })
  const at = (iso: string) => 'T' + iso.slice(11, 16)
  const label = (id: string, locks: ModelLockRow[], recent: RequestEvent[]) => modelAvailability(id, locks, recent, at)?.label

  it('不带域前缀的不判', () => {
    expect(modelAvailability('m', [], [], at)).toBeNull()
  })
  it('模型锁优先，按状态和原因分', () => {
    const ok = [ev({})]
    expect(label('global:m', [lock({ state: 'partial', servable: 1, locked: 1 })], ok)).toBe('部分可用')
    expect(label('global:m', [lock({ state: 'starved' })], ok)).toBe('暂无可用号')
    expect(label('global:m', [lock({ reason: '11158 model not allowed for account type' })], ok)).toBe('无权限')
    expect(label('global:m', [lock({ reason: '11102 model not available' })], ok)).toBe('上游无此模型')
    expect(label('global:m', [lock({ reason: '6004 model rate limit' })], ok)).toBe('限流中')
    expect(label('cn:m', [lock({ realm: 'cn', reason: '11158 x' })], [])).toBe('无权限')
  })
  it('锁只认同一个域', () => {
    expect(label('cn:m', [lock({ reason: '11158 x' })], [])).toBe('待验证')
  })
  it('没有锁看最近一次到过上游的请求', () => {
    const av = modelAvailability('global:m', [], [ev({})], at)
    expect(av).toEqual({ label: '可用', color: 'success', tip: '最近一次真实请求成功 · T09:00' })
    expect(label('global:m', [], [ev({ status: 429, ok: false, outcome: 'http_error' })])).toBe('限流')
    expect(label('global:m', [], [ev({ status: 502, ok: false, outcome: 'http_error' })])).toBe('上游错误')
    expect(modelAvailability('global:m', [], [ev({ status: 400, ok: false, outcome: 'http_error', error: 'bad' })], at)?.tip)
      .toBe('最近一次请求失败 HTTP 400 · T09:00：bad')
  })
  it('只看最新的一条，没发到上游的不算', () => {
    expect(label('global:m', [], [ev({ status: 502, ok: false, outcome: 'http_error' }), ev({})])).toBe('上游错误')
    expect(label('global:m', [], [ev({ status: 403, ok: false, outcome: 'http_error', attempts: undefined }), ev({})])).toBe('可用')
    expect(label('global:m', [], [ev({ model: 'cn:m' })])).toBe('待验证')
  })
})
