// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 跨平台快捷键徽章：Apple 设备显示 ⌘ + 键位，Windows / Linux 显示 Ctrl+键位。
import { Kbd } from '@heroui/react'
import { isApplePlatform } from '../lib/motion'

const APPLE = isApplePlatform()

export function ModKbd({ keyChar, variant = 'light', className }: {
  keyChar: string
  variant?: 'default' | 'light'
  className?: string
}) {
  return (
    <Kbd variant={variant} className={className}>
      {APPLE ? (
        <>
          <Kbd.Abbr keyValue="command" />
          <Kbd.Content>{keyChar}</Kbd.Content>
        </>
      ) : (
        <Kbd.Content>Ctrl+{keyChar}</Kbd.Content>
      )}
    </Kbd>
  )
}
