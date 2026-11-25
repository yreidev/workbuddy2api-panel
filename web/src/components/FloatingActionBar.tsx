// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 底部悬浮操作栏：基于 HeroUI 原生 Toolbar 与 motion 弹簧进出场动画，
// 使用 fixed 定位避免触发时引起下方卡片跳动，桌面端经 .wb-floating-bar 自动按侧栏宽度在主内容区居中。
import type { ReactNode } from 'react'
import { AnimatePresence } from 'motion/react'
import * as m from 'motion/react-m'
import { Toolbar } from '@heroui/react'
import { SPRING_BAR } from '../lib/motion'

export function FloatingActionBar({ open, ariaLabel, className = '', children }: {
  open: boolean
  ariaLabel: string
  className?: string
  children: ReactNode
}) {
  return (
    <AnimatePresence>
      {open && (
        <m.div
          initial={{ opacity: 0, y: 14, scale: 0.98 }}
          animate={{ opacity: 1, y: 0, scale: 1 }}
          exit={{ opacity: 0, y: 12, scale: 0.98 }}
          transition={SPRING_BAR}
          className={`wb-floating-bar pointer-events-none fixed inset-x-0 bottom-4 z-30 flex justify-center px-3 ${className}`}
        >
          <Toolbar
            isAttached
            aria-label={ariaLabel}
            className="pointer-events-auto flex max-w-full flex-wrap items-center justify-center gap-2 rounded-2xl border border-separator/80 bg-overlay/92 px-3.5 py-2 shadow-xl backdrop-blur-md"
          >
            {children}
          </Toolbar>
        </m.div>
      )}
    </AnimatePresence>
  )
}
