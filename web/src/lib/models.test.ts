// 条件查询部分从上游 internal/panel/frontend_test.go 的 TestAppJSModelFilter 平移（期望值原样保留；
// 上游能力是单选，这里是多选，单个能力的结果一致）。
import { describe, expect, it } from 'vitest'
import { EMPTY_MODEL_FILTER, filterModels, probeFor, rateValue, type ModelFilter, type ModelSort } from './models'
import type { Model } from './types'

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
