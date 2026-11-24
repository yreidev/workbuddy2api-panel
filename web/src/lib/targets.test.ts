// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it } from 'vitest'
import { parseModelTarget, targetName } from './targets'

describe('上游目标解析与名称', () => {
  it('解析 workbuddy / cn / global 前缀以及 Trae / Qoder 完整前缀', () => {
    expect(parseModelTarget('workbuddy:cn:glm-5.3')).toEqual({
      target: 'workbuddy:cn', product: 'workbuddy', region: 'cn', native: 'glm-5.3',
    })
    expect(parseModelTarget('workbuddy:global:gpt-5.5')).toEqual({
      target: 'workbuddy:global', product: 'workbuddy', region: 'global', native: 'gpt-5.5',
    })
    expect(parseModelTarget('cn:glm-5.3')).toEqual({
      target: 'workbuddy:cn', product: 'workbuddy', region: 'cn', native: 'glm-5.3',
    })
    expect(parseModelTarget('global:gpt-5.5')).toEqual({
      target: 'workbuddy:global', product: 'workbuddy', region: 'global', native: 'gpt-5.5',
    })
    expect(parseModelTarget('traework:cn:doubao-seed')).toEqual({
      target: 'traework:cn', product: 'traework', region: 'cn', native: 'doubao-seed',
    })
    expect(parseModelTarget('traecode:cn:claude-3-7')).toEqual({
      target: 'traecode:cn', product: 'traecode', region: 'cn', native: 'claude-3-7',
    })
    expect(parseModelTarget('qoder:cn:qwen3-coder')).toEqual({
      target: 'qoder:cn', product: 'qoder', region: 'cn', native: 'qwen3-coder',
    })
  })

  it('保留原生模型 ID 中自带的冒号，未知前缀和裸模型名原样回落', () => {
    expect(parseModelTarget('qoder:global:vendor:model-v2')).toEqual({
      target: 'qoder:global', product: 'qoder', region: 'global', native: 'vendor:model-v2',
    })
    expect(parseModelTarget('glm-5.3')).toEqual({
      target: '', product: '', region: '', native: 'glm-5.3',
    })
    expect(parseModelTarget('unknown:cn:foo')).toEqual({
      target: '', product: '', region: '', native: 'unknown:cn:foo',
    })
  })

  it('展示名映射已知目标，未知目标原样返回', () => {
    expect(targetName('workbuddy:cn')).toBe('WorkBuddy 国内')
    expect(targetName('traework:cn')).toBe('TraeWork 国内')
    expect(targetName('traecode:cn')).toBe('Trae Code 国内')
    expect(targetName('qoder:global')).toBe('Qoder 国际')
    expect(targetName('custom:x')).toBe('custom:x')
  })
})
