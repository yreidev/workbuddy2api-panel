// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import type { ReactNode } from 'react'
import { Button, Tooltip } from '@heroui/react'
import { isFrozen, type Health } from '../../lib/accounts'
import type { Account } from '../../lib/types'
import type { AccountAction } from './useAccountActions'

/**
 * 一排操作按钮（口径同上游旧面板）：禁用或冷却中给「解冻」，否则在「暂停选号 / 恢复选号」间切换；
 * 没禁用的号都给「禁用」。企业版没有个人成长体系（签到上游 400、成长任务 403），
 * 不给「签到」「任务」，「余额」改叫「额度」（刷新分配给这个号的企业额度）。
 * 国际版没有签到活动（上游 400「签到活动未开启或已过期」），也不给「签到」。
 */
export function AccountButtons({ a, health, busy, run, nowrap, compact, extra }: {
  a: Account
  health: Health
  /** 表格里一行放下，不折行（卡片和抽屉里允许折行） */
  nowrap?: boolean
  /** 按钮收紧间距（卡片用，按钮之间不留空隙，靠按钮自身的内边距隔开），放不下照样折行 */
  compact?: boolean
  /** 额外按钮（如抽屉里的「导出凭证」），放在「移除」之前，让危险操作留在最后 */
  extra?: ReactNode
  busy: string | null
  run: (a: Account, action: AccountAction) => void
}) {
  const btn = (action: AccountAction, label: string, opts: { variant?: 'ghost' | 'primary'; className?: string; ariaLabel?: string } = {}) => (
    <Button
      size="sm"
      variant={opts.variant ?? 'ghost'}
      aria-label={opts.ariaLabel}
      className={`${nowrap || compact ? 'px-2' : ''} ${opts.className ?? ''}`}
      isPending={busy === a.uid + ':' + action}
      onPress={() => run(a, action)}
    >
      {label}
    </Button>
  )
  return (
    <div className={`flex items-center ${nowrap ? 'flex-nowrap gap-0.5' : compact ? 'flex-wrap' : 'flex-wrap gap-1'}`}>
      {/* 今日已签仍可点：重新签到会顺带刷新余额 */}
      {!a.enterprise && a.realm !== 'global' && (a.checkin_done ? btn('checkin', '已签', { className: 'text-muted', ariaLabel: '今日已签到，点击重新签到并刷新余额' }) : btn('checkin', '签到'))}
      {a.enterprise ? btn('balance', '额度', { ariaLabel: '刷新企业版分配额度' }) : btn('balance', '余额')}
      {!a.enterprise && btn('tasks', '任务')}
      {btn('liveness', '测活', { ariaLabel: '测活：给这个号发一条最短的对话，看它现在能不能用' })}
      {isFrozen(health) ? btn('revive', '解冻', { variant: 'primary' })
        : a.paused ? btn('resume', '恢复选号', { variant: 'primary' })
        : (
          <Tooltip delay={300}>
            {btn('pause', '暂停选号')}
            <Tooltip.Content>{a.enterprise ? '退出选号，但照常保活 / 刷新额度' : '退出选号，但照常签到 / 活跃上报 / 保活 / 刷新余额'}</Tooltip.Content>
          </Tooltip>
        )}
      {!a.disabled && btn('disable', '禁用')}
      {extra}
      {btn('remove', '移除', { className: 'text-danger' })}
    </div>
  )
}
