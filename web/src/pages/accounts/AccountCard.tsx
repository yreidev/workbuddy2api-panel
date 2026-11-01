// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号卡片：宽屏的卡片网格和窄屏列表共用。边框按状态着色（冷却、被代理拦着橙，禁用红），右上角箭头看详情。
import { ChevronRight } from '@gravity-ui/icons'
import { Button, Card, Separator, Surface } from '@heroui/react'
import { accountName, activeRateLimits, type Health } from '../../lib/accounts'
import { ago } from '../../lib/format'
import type { Account, ProxyAccount } from '../../lib/types'
import { AccountButtons } from './AccountButtons'
import { AccountName, CreditsCell, GroupLine, HealthChip, LivenessLine, ProxyLine, RateLimitList, UsageChips } from './AccountCells'
import { SelectionCheckbox } from './ExportBar'
import type { AccountAction } from './useAccountActions'

const RING: Record<Health['state'], string> = {
  ok: '',
  cooling: 'ring-1 ring-warning/40',
  paused: 'ring-1 ring-border',
  blocked: 'ring-1 ring-warning/40',
  disabled: 'ring-1 ring-danger/40',
}

/** 成功率（一位小数）；还没有请求时返回 null */
const successRate = (ok: number, fail: number) => (ok + fail > 0 ? Math.round((ok / (ok + fail)) * 1000) / 10 : null)

export function AccountCard({ a, health, proxy, now, poolMax, selected, onSelect, onOpen, busy, run }: {
  a: Account
  health: Health
  /** 号的代理情况（总览里的 proxies.accounts）；没绑也没被拦时没有 */
  proxy?: ProxyAccount
  now: number
  poolMax: number
  selected: boolean
  onSelect: (on: boolean) => void
  onOpen: () => void
  busy: string | null
  run: (a: Account, action: AccountAction) => void
}) {
  const ok = a.success_count || 0
  const fail = a.err_total || 0
  const rate = successRate(ok, fail)
  return (
    <Card variant="secondary" className={`gap-3 p-4 ${RING[health.state]}`}>
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-start gap-2">
          <SelectionCheckbox label={'选择 ' + accountName(a)} isSelected={selected} onChange={onSelect} />
          <AccountName a={a} />
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <HealthChip health={health} now={now} />
          <Button isIconOnly size="sm" variant="ghost" aria-label="详情" onPress={onOpen}><ChevronRight /></Button>
        </div>
      </div>

      {(a.proxy_blocked || a.reason || activeRateLimits(a).length > 0) && (
        <Surface className="flex flex-col gap-1 rounded-xl px-3 py-2">
          {a.proxy_blocked && <span className="line-clamp-2 break-all text-xs text-warning">{a.proxy_blocked}</span>}
          {a.reason && <span className="line-clamp-2 break-all text-xs text-muted">{a.reason}</span>}
          <RateLimitList a={a} now={now} />
        </Surface>
      )}

      <div className="grid grid-cols-2 gap-3 text-sm">
        <CreditsCell a={a} poolMax={poolMax} now={now} />
        <div className="flex flex-col gap-0.5 text-xs text-muted">
          <span>
            成功 <span className="tabular-nums text-foreground">{ok}</span> · 失败 <span className="tabular-nums text-danger">{fail}</span>
            {rate != null && <>（{rate}%）</>}
          </span>
          <span>在途 {a.in_flight || 0}</span>
          <span>最近成功 {ago(a.last_success, now)}</span>
        </div>
      </div>

      <UsageChips a={a} />
      <GroupLine a={a} />
      <ProxyLine v={proxy} />
      <LivenessLine a={a} now={now} />

      <div className="mt-auto flex flex-col gap-2">
        <Separator />
        <div className="-mx-1">
          <AccountButtons a={a} health={health} busy={busy} run={run} compact />
        </div>
      </div>
    </Card>
  )
}
