// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it } from 'vitest'
import { formatAcceptHint, matchesAccept } from './files'

describe('matchesAccept', () => {
  it('按扩展名或 MIME 类型匹配文件（兼容 Windows/Linux 空 MIME）', () => {
    expect(matchesAccept({ name: 'accounts.json', type: '' }, '.json,application/json')).toBe(true)
    expect(matchesAccept({ name: 'EXPORT.JSON', type: 'application/json' }, '.json,application/json')).toBe(true)
    expect(matchesAccept({ name: 'creds', type: 'application/json' }, '.json,application/json')).toBe(true)
    expect(matchesAccept({ name: 'proxies.txt', type: '' }, '.txt,text/plain')).toBe(true)
    expect(matchesAccept({ name: 'list.log', type: 'text/plain' }, 'text/*')).toBe(true)
  })

  it('拒绝不符合 accept 的文件类型', () => {
    expect(matchesAccept({ name: 'photo.png', type: 'image/png' }, '.json,application/json')).toBe(false)
    expect(matchesAccept({ name: 'doc.pdf', type: 'application/pdf' }, '.txt,text/plain')).toBe(false)
    expect(matchesAccept({ name: 'unknown', type: '' }, '.json,application/json')).toBe(false)
  })

  it('空 accept 或 */* 允许任意文件', () => {
    expect(matchesAccept({ name: 'any.bin', type: 'application/octet-stream' }, '')).toBe(true)
    expect(matchesAccept({ name: 'any.bin', type: 'application/octet-stream' }, '*/*')).toBe(true)
  })
})

describe('formatAcceptHint', () => {
  it('优先展示扩展名，无扩展名时展示原规则', () => {
    expect(formatAcceptHint('.json,application/json')).toBe('.json')
    expect(formatAcceptHint('.txt,text/plain')).toBe('.txt')
    expect(formatAcceptHint('text/plain')).toBe('text/plain')
  })
})
