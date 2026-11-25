// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it } from 'vitest'
import { hasOpenDialog, isApplePlatform, isEditableTarget, matchesModShortcut } from './motion'

describe('matchesModShortcut', () => {
  it('匹配 Cmd/Ctrl + 字母（不区分大小写）', () => {
    expect(matchesModShortcut({ key: 'k', metaKey: true, ctrlKey: false, altKey: false, shiftKey: false }, 'k')).toBe(true)
    expect(matchesModShortcut({ key: 'K', metaKey: false, ctrlKey: true, altKey: false, shiftKey: false }, 'k')).toBe(true)
    expect(matchesModShortcut({ key: 'b', metaKey: true, ctrlKey: false, altKey: false, shiftKey: false }, 'b')).toBe(true)
  })

  it('未按修饰键或同时按下 Alt / Shift 时不匹配', () => {
    expect(matchesModShortcut({ key: 'k', metaKey: false, ctrlKey: false, altKey: false, shiftKey: false }, 'k')).toBe(false)
    expect(matchesModShortcut({ key: 'k', metaKey: true, ctrlKey: false, altKey: true, shiftKey: false }, 'k')).toBe(false)
    expect(matchesModShortcut({ key: 'k', metaKey: false, ctrlKey: true, altKey: false, shiftKey: true }, 'k')).toBe(false)
    expect(matchesModShortcut({ key: 'j', metaKey: true, ctrlKey: false, altKey: false, shiftKey: false }, 'k')).toBe(false)
  })
})

describe('isEditableTarget', () => {
  it('识别输入框、文本域、下拉框和 contenteditable 节点', () => {
    expect(isEditableTarget({ tagName: 'INPUT' } as unknown as EventTarget)).toBe(true)
    expect(isEditableTarget({ tagName: 'textarea' } as unknown as EventTarget)).toBe(true)
    expect(isEditableTarget({ tagName: 'SELECT' } as unknown as EventTarget)).toBe(true)
    expect(isEditableTarget({ tagName: 'DIV', isContentEditable: true } as unknown as EventTarget)).toBe(true)
  })

  it('普通展示或按钮节点返回 false', () => {
    expect(isEditableTarget(null)).toBe(false)
    expect(isEditableTarget({ tagName: 'BUTTON' } as unknown as EventTarget)).toBe(false)
    expect(isEditableTarget({ tagName: 'DIV', isContentEditable: false } as unknown as EventTarget)).toBe(false)
  })
})

describe('isApplePlatform / hasOpenDialog', () => {
  it('按 platform / userAgent 区分 Apple 与 Windows / Linux 平台', () => {
    expect(isApplePlatform('MacIntel', '')).toBe(true)
    expect(isApplePlatform('', 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)')).toBe(true)
    expect(isApplePlatform('Linux x86_64', 'Mozilla/5.0 (X11; Linux x86_64)')).toBe(false)
    expect(isApplePlatform('Win32', 'Mozilla/5.0 (Windows NT 10.0; Win64; x64)')).toBe(false)
  })

  it('无 DOM 环境下 hasOpenDialog 安全返回 false', () => {
    expect(hasOpenDialog()).toBe(false)
  })
})
