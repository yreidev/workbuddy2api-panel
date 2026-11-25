// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 统一的语义化图表提示气泡：与 HeroUI 浮层配色、圆角和深浅色主题保持一致。
import type { ReactNode } from 'react'

export function ChartTooltipCard({ title, footer, className = '', children }: {
  title: ReactNode
  footer?: ReactNode
  className?: string
  children: ReactNode
}) {
  return (
    <div className={`min-w-44 rounded-xl border border-separator/80 bg-overlay/95 px-3 py-2.5 text-xs shadow-lg backdrop-blur-md ${className}`}>
      <div className="mb-1.5 font-medium text-foreground">{title}</div>
      <div className="flex flex-col gap-1">{children}</div>
      {footer && <div className="mt-2 border-t border-separator/70 pt-1.5 text-muted">{footer}</div>}
    </div>
  )
}

export function ChartTooltipRow({ color, label, value, sub }: {
  color?: string
  label: ReactNode
  value: ReactNode
  sub?: ReactNode
}) {
  return (
    <div className="flex flex-col gap-0.5">
      <div className="flex items-center gap-2">
        {color && <span className="size-2 shrink-0 rounded-full" style={{ background: color }} />}
        <span className="min-w-0 truncate text-muted">{label}</span>
        <span className="ml-auto pl-3 font-medium text-foreground tabular-nums">{value}</span>
      </div>
      {sub && <div className="ps-4 text-[11px] text-muted">{sub}</div>}
    </div>
  )
}
