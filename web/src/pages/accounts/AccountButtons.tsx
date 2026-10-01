import { Button } from '@heroui/react'
import type { Account } from '../../lib/types'
import type { AccountAction } from './useAccountActions'

/** 一排操作按钮。frozen（禁用或冷却中）时显示「解冻」，否则显示「禁用」 */
export function AccountButtons({ a, frozen, busy, run, nowrap }: {
  a: Account
  /** 表格里一行放下，不折行（卡片和抽屉里允许折行） */
  nowrap?: boolean
  frozen: boolean
  busy: string | null
  run: (a: Account, action: AccountAction) => void
}) {
  const btn = (action: AccountAction, label: string, variant: 'ghost' | 'primary' = 'ghost', className?: string, ariaLabel?: string) => (
    <Button
      size="sm"
      variant={variant}
      aria-label={ariaLabel}
      className={`${nowrap ? 'px-2' : ''} ${className ?? ''}`}
      isPending={busy === a.uid + ':' + action}
      onPress={() => run(a, action)}
    >
      {label}
    </Button>
  )
  return (
    <div className={`flex items-center ${nowrap ? 'flex-nowrap gap-0.5' : 'flex-wrap gap-1'}`}>
      {/* 今日已签仍可点：重新签到会顺带刷新余额 */}
      {a.checkin_done ? btn('checkin', '已签', 'ghost', 'text-muted', '今日已签到，点击重新签到并刷新余额') : btn('checkin', '签到')}
      {btn('balance', '余额')}
      {btn('tasks', '任务')}
      {frozen ? btn('revive', '解冻', 'primary') : btn('disable', '禁用')}
      {btn('remove', '移除', 'ghost', 'text-danger')}
    </div>
  )
}
