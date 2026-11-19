// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { useState } from 'react'
import { Button, Chip, Disclosure, Drawer, ProgressBar, Surface, useMediaQuery } from '@heroui/react'
import { BusyButton } from '../../components/Feedback'
import { ProviderQuotaDetails } from '../../components/ProviderQuota'
import type { ProviderCardUsage } from '../../lib/accountPool'
import { fmtLocalDateTime, formatTokenCount } from '../../lib/format'
import { quotaNumber } from '../../lib/providerQuota'
import { useProviderQuota } from '../../lib/queries'
import { targetName } from '../../lib/targets'
import type { Group, ProviderAccount, ProxyAccount } from '../../lib/types'
import { AccountCardLayout } from './AccountCardLayout'
import { LivenessLine } from './AccountCells'

export function ProviderAccountCard({ account: a, usage, now, groups, proxy, selected, busy, onSelect, onEdit, onTest, onRemove, onRefreshModels }: {
  account: ProviderAccount
  usage?: ProviderCardUsage
  now: number
  groups: Group[]
  proxy?: ProxyAccount
  selected: boolean
  busy: boolean
  onSelect: (value: boolean) => void
  onEdit: () => void
  onTest: () => void
  onRemove: () => void
  onRefreshModels: (target: string) => void
}) {
  const [open, setOpen] = useState(false)
  const wide = useMediaQuery('(min-width: 640px)')
  const quota = useProviderQuota(a)
  const snapshot = quota.query.data?.snapshot
  const quotaError = quota.refresh.isError || quota.query.isError || !!quota.query.data?.error
  const balance = snapshot?.balance
  const total = snapshot?.total
  const cooling = !!a.cool_until && Date.parse(a.cool_until) > now
  const targets = a.targets.map(targetName).join(' / ')
  const status = a.disabled ? '已停止派发' : a.paused ? '暂停选号' : a.error ? '需要处理' : cooling ? '冷却中' : '已启用'
  const color = a.disabled ? 'danger' : a.paused ? 'default' : a.error ? 'danger' : cooling ? 'warning' : 'success'
  const ring = color === 'danger' ? 'ring-1 ring-danger/40' : color === 'warning' || proxy?.blocked ? 'ring-1 ring-warning/40' : a.paused ? 'ring-1 ring-border' : ''
  const requests = usage?.requests ?? 0
  const failed = usage?.failed ?? 0
  const tokens = usage?.tokensKnown ? formatTokenCount(usage.tokens) + ' tok' : 'Token 未知'
  const groupNames = a.groups.map((id) => groups.find((g) => g.id === id)?.name ?? id).join('、')

  return <>
    <AccountCardLayout name={a.name} ring={ring} selected={selected} onSelect={onSelect} onOpen={() => setOpen(true)}
      heading={<div className="min-w-0">
        <div className="truncate font-medium">{a.name}</div>
        <div className="truncate text-xs text-muted">{targets}</div>
      </div>}
      status={<Chip size="sm" variant="soft" color={color}>{status}</Chip>}
      actions={<div className="flex flex-wrap items-center">
        <Button size="sm" variant="ghost" className="px-2" aria-label="刷新额度" isPending={quota.query.isFetching || quota.refresh.isPending} onPress={() => quota.refresh.mutate()}>额度</Button>
        <Button size="sm" variant="ghost" className="px-2" onPress={onEdit}>设置</Button>
        <Button size="sm" variant="ghost" className="px-2" onPress={onTest}>测活</Button>
        <Button size="sm" variant="ghost" className="px-2 text-danger" onPress={onRemove}>移除</Button>
      </div>}>
      {(a.error || proxy?.blocked) && <Surface className="flex flex-col gap-1 rounded-xl px-3 py-2">
        {a.error && <span className="line-clamp-2 break-all text-xs text-danger">{a.error}</span>}
        {proxy?.blocked && <span className="line-clamp-2 break-all text-xs text-warning">{proxy.blocked}</span>}
      </Surface>}
      <div className="grid grid-cols-2 gap-3 text-sm">
        <div className="flex min-w-0 flex-col gap-1">
          <span className="break-words font-mono text-sm tabular-nums" aria-label="官方剩余额度与总额">
            {quotaNumber(balance)}{total != null && <span className="text-xs text-muted">/{quotaNumber(total)}</span>}
          </span>
          {balance != null && total != null && total > 0 && <ProgressBar aria-label="剩余额度占总额" value={Math.max(0, Math.min(100, balance / total * 100))} size="sm" className="max-w-28">
            <ProgressBar.Track><ProgressBar.Fill /></ProgressBar.Track>
          </ProgressBar>}
          <span className="text-xs text-muted">{snapshot ? snapshot.unit || '单位未确认' : quota.query.isFetching ? '正在查询额度' : '额度未获取'}</span>
          {snapshot?.exceeded && <span className="text-xs text-warning">已超额度</span>}
          {quotaError && <span className="text-xs text-warning">{snapshot ? '刷新失败 · 显示上次额度' : '额度查询失败'}</span>}
        </div>
        <div className="flex flex-col gap-0.5 text-xs text-muted">
          <span>成功 <span className="tabular-nums text-foreground">{requests - failed}</span> · 失败 <span className="tabular-nums text-danger">{failed}</span></span>
          <span>在途 {a.in_flight} · 优先级 {a.priority}</span>
          <span>模型 {a.models.length} 个</span>
        </div>
      </div>
      <div className="flex flex-wrap gap-1">
        <Chip size="sm" variant="soft" color="accent">{requests} 次</Chip>
        <Chip size="sm" variant="soft" color="accent">{!!usage?.tokensKnown && usage.tokensKnown < requests && '已知 '}{tokens}</Chip>
      </div>
      {groupNames && <p className="text-xs text-muted">分组：{groupNames}</p>}
      {a.proxy_id && <p className="break-all text-xs text-muted">代理：{proxy?.proxy_name || a.proxy_id}</p>}
      <LivenessLine a={a} now={now} />
    </AccountCardLayout>

    <Drawer.Backdrop isOpen={open} onOpenChange={setOpen}>
      <Drawer.Content placement={wide ? 'right' : 'bottom'}>
        <Drawer.Dialog className={wide ? 'w-[440px] max-w-full' : 'max-h-[88dvh]'}>
          {wide ? <Drawer.CloseTrigger /> : <Drawer.Handle />}
          <Drawer.Header>
            <Drawer.Heading>{a.name}</Drawer.Heading>
            <p className="text-xs text-muted">{targets}</p>
            <p className="select-all break-all font-mono text-xs text-muted">{a.account_id}</p>
          </Drawer.Header>
          <Drawer.Body className="flex flex-col gap-5">
            {a.error && <p className="break-all text-sm text-danger">{a.error}</p>}
            {proxy?.blocked && <p className="break-all text-sm text-warning">{proxy.blocked}</p>}
            {cooling && <p className="text-sm text-warning">冷却至 {fmtLocalDateTime(Date.parse(a.cool_until!))}</p>}
            <ProviderQuotaDetails account={a} quota={quota} />
            <section className="flex flex-col gap-2">
              <Disclosure>
                <Disclosure.Heading><Disclosure.Trigger className="inline-flex items-center gap-1 text-sm font-medium">模型目录（{a.models.length}）<Disclosure.Indicator /></Disclosure.Trigger></Disclosure.Heading>
                <Disclosure.Content>
                  {a.models.length ? <ul className="space-y-1 text-xs">{a.models.map((m) => <li key={m.id} className="break-all font-mono">{m.id} {m.tools ? '· 支持工具协议' : '· 文本对话'}</li>)}</ul> : <p className="text-sm text-muted">暂无可用模型</p>}
                </Disclosure.Content>
              </Disclosure>
              {!!a.blocked_models?.length && <p className="break-all text-xs text-muted">不接的模型：{a.blocked_models.join('、')}</p>}
              <div className="flex flex-wrap gap-1">
                {a.targets.map((t) => <BusyButton key={t} size="sm" variant="secondary" busy={busy} onPress={() => onRefreshModels(t)}>刷新 {targetName(t)} 模型</BusyButton>)}
              </div>
            </section>
          </Drawer.Body>
          <Drawer.Footer><Button size="sm" variant="secondary" onPress={() => setOpen(false)}>关闭</Button></Drawer.Footer>
        </Drawer.Dialog>
      </Drawer.Content>
    </Drawer.Backdrop>
  </>
}
