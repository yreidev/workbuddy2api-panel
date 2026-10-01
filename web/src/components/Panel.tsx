import type { ReactNode } from 'react'
import { CircleInfo } from '@gravity-ui/icons'
import { Button, Card, Popover } from '@heroui/react'

/** 页面里的一块：标题 + 说明 + 右侧操作 + 内容 */
export function Panel({ title, desc, actions, children, className = '', contentClassName }: {
  title?: ReactNode
  desc?: ReactNode
  actions?: ReactNode
  children?: ReactNode
  className?: string
  contentClassName?: string
}) {
  return (
    <Card className={className}>
      {(title || actions) && (
        <Card.Header className="flex flex-row flex-wrap items-center gap-x-3 gap-y-2">
          <div className="min-w-0">
            {title && <Card.Title>{title}</Card.Title>}
            {desc && <Card.Description>{desc}</Card.Description>}
          </div>
          {actions && <div className="ml-auto flex flex-wrap items-center gap-2">{actions}</div>}
        </Card.Header>
      )}
      {children !== undefined && <Card.Content className={contentClassName}>{children}</Card.Content>}
    </Card>
  )
}

/** 统计数字。sub 放「占比 / 成功率」这类解释性的小字，children 放卡片内的构成条 */
export function Stat({ label, value, tone, sub, children, className = '' }: {
  label: ReactNode
  value: ReactNode
  tone?: 'success' | 'warning' | 'danger'
  sub?: ReactNode
  children?: ReactNode
  className?: string
}) {
  const color = tone === 'success' ? 'text-success' : tone === 'warning' ? 'text-warning' : tone === 'danger' ? 'text-danger' : ''
  return (
    <Card className={`gap-1 p-4 ${className}`}>
      <span className={`text-2xl font-semibold tabular-nums ${color}`}>{value}</span>
      <span className="text-xs text-muted">{label}</span>
      {children}
      {sub && <span className="text-xs text-muted tabular-nums">{sub}</span>}
    </Card>
  )
}

/** prompt / completion 构成条（宽度按百分比，随列宽伸缩） */
export function MixBar({ prompt, completion, total, className = '' }: { prompt?: number; completion?: number; total?: number; className?: string }) {
  const t = Number(total || 0)
  if (!t) return null
  const w = (v?: number) => Math.max(0, Math.min(100, Number(v || 0) / t * 100))
  return (
    <span className={`flex h-1.5 w-full overflow-hidden rounded-full bg-surface-secondary ${className}`} aria-hidden="true">
      <span className="h-full bg-(--viz-1)" style={{ width: w(prompt) + '%' }} />
      <span className="h-full bg-(--viz-2)" style={{ width: w(completion) + '%' }} />
    </span>
  )
}

/**
 * 点一下看说明（手机上没有悬停，原先藏在 title 悬停提示里的内容都改用它）。
 */
export function InfoTip({ label = '查看说明', children }: { label?: string; children: ReactNode }) {
  return (
    <Popover>
      <Button isIconOnly aria-label={label} size="sm" variant="ghost" className="size-6 min-w-6 shrink-0 text-muted">
        <CircleInfo className="size-3.5" />
      </Button>
      <Popover.Content className="max-w-xs">
        <Popover.Dialog>
          <div className="whitespace-pre-line text-sm leading-6">{children}</div>
        </Popover.Dialog>
      </Popover.Content>
    </Popover>
  )
}
