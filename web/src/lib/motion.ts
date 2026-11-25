// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 面板共用的动效曲线与快捷键判定工具，配合根节点的 MotionConfig(reducedMotion="user") 与 LazyMotion 使用。
export const EASE_OUT = [0.22, 1, 0.36, 1] as const

/** 页面切换与面板内容进场的轻盈减速过渡 */
export const PAGE_TRANSITION = {
  duration: 0.22,
  ease: EASE_OUT,
} as const

/** 导航激活指示条等高频微交互用的紧凑弹簧 */
export const SPRING_SNAPPY = {
  type: 'spring' as const,
  stiffness: 420,
  damping: 34,
  mass: 0.9,
} as const

/** 底部悬浮操作栏进出场用的无回弹弹簧 */
export const SPRING_BAR = {
  type: 'spring' as const,
  duration: 0.3,
  bounce: 0,
} as const

/** 焦点是否位于可编辑输入元素内，用于避免侧栏等快捷键误触发 */
export function isEditableTarget(target: EventTarget | null): boolean {
  if (!target || typeof target !== 'object') return false
  const el = target as { tagName?: string; isContentEditable?: boolean }
  if (el.isContentEditable) return true
  const tag = el.tagName?.toUpperCase()
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT'
}

/** 判定 Cmd/Ctrl + key 组合键（不接受同时按下 Alt 或 Shift） */
export function matchesModShortcut(
  e: Pick<KeyboardEvent, 'key' | 'metaKey' | 'ctrlKey' | 'altKey' | 'shiftKey'>,
  key: string,
): boolean {
  return (e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === key.toLowerCase()
}

/** 判断当前是否为 Apple 平台（macOS / iOS / iPadOS），用于快捷键徽章显示 ⌘ 或 Ctrl */
export function isApplePlatform(platform?: string, userAgent?: string): boolean {
  const p = platform ?? (typeof navigator !== 'undefined' ? navigator.platform : '')
  const ua = userAgent ?? (typeof navigator !== 'undefined' ? navigator.userAgent : '')
  return /(mac|iphone|ipad|ipod)/i.test(p) || /(macintosh|iphone|ipad|ipod)/i.test(ua)
}

/** 页面当前是否有打开的模态对话框（如 KeyGate 锁屏、确认框、添加账号弹窗），避免全局快捷键穿透触发 */
export function hasOpenDialog(): boolean {
  if (typeof document === 'undefined') return false
  return !!document.querySelector('[role="dialog"], [role="alertdialog"]')
}
