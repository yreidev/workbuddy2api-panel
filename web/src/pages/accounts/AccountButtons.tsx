import { Button } from '@heroui/react'
import { isFrozen, type Health } from '../../lib/accounts'
import type { Account } from '../../lib/types'
import type { AccountAction } from './useAccountActions'

/**
 * 一排操作按钮（口径同上游旧面板）：禁用或冷却中给「解冻」，否则在「暂停选号 / 恢复选号」间切换；
 * 没禁用的号都给「禁用」。
 */
export function AccountButtons({ a, health, busy, run, nowrap }: {
  a: Account
  health: Health
  /** 表格里一行放下，不折行（卡片和抽屉里允许折行） */
  nowrap?: boolean
  busy: string | null
  run: (a: Account, action: AccountAction) => void
}) {
  const btn = (action: AccountAction, label: string, opts: { variant?: 'ghost' | 'primary'; className?: string; ariaLabel?: string } = {}) => (
    <Button
      size="sm"
      variant={opts.variant ?? 'ghost'}
      aria-label={opts.ariaLabel}
      className={`${nowrap ? 'px-2' : ''} ${opts.className ?? ''}`}
      isPending={busy === a.uid + ':' + action}
      onPress={() => run(a, action)}
    >
      {label}
    </Button>
  )
  return (
    <div className={`flex items-center ${nowrap ? 'flex-nowrap gap-0.5' : 'flex-wrap gap-1'}`}>
      {/* 今日已签仍可点：重新签到会顺带刷新余额 */}
      {a.checkin_done ? btn('checkin', '已签', { className: 'text-muted', ariaLabel: '今日已签到，点击重新签到并刷新余额' }) : btn('checkin', '签到')}
      {btn('balance', '余额')}
      {btn('tasks', '任务')}
      {isFrozen(health) ? btn('revive', '解冻', { variant: 'primary' })
        : a.paused ? btn('resume', '恢复选号', { variant: 'primary' })
        // Button 不收 title，悬停说明挂在外层
        : <span className="contents" title="退出选号，但照常签到 / 活跃上报 / 保活 / 刷新余额">{btn('pause', '暂停选号')}</span>}
      {!a.disabled && btn('disable', '禁用')}
      {btn('remove', '移除', { className: 'text-danger' })}
    </div>
  )
}
