import { describe, expect, it } from 'vitest'
import { isExternalHref } from './links'

describe('isExternalHref', () => {
  it('完整地址是外链，面板内部路径不是', () => {
    expect(isExternalHref('https://github.com/yreidev/workbuddy2api-panel')).toBe(true)
    expect(isExternalHref('mailto:a@b.c')).toBe(true)
    expect(isExternalHref('//example.com/x')).toBe(true)
    expect(isExternalHref('/accounts')).toBe(false)
    expect(isExternalHref('/logs?ch=task')).toBe(false)
  })
})
