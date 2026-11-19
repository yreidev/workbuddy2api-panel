// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { Disclosure } from '@heroui/react'
import { errorMessage } from '../lib/api'
import { fmtLocalDateTime } from '../lib/format'
import { quotaAmount } from '../lib/providerQuota'
import { useProviderQuota } from '../lib/queries'
import type { ProviderAccount } from '../lib/types'
import { BusyButton } from './Feedback'

export function ProviderQuota({ account }: { account: ProviderAccount }) {
  const quota = useProviderQuota(account)
  return <ProviderQuotaDetails account={account} quota={quota} />
}

export function ProviderQuotaDetails({ account, quota }: { account: ProviderAccount; quota: ReturnType<typeof useProviderQuota> }) {
  const { query, refresh } = quota
  const status = query.data
  const snapshot = status?.snapshot
  const error = refresh.error ? errorMessage(refresh.error) : query.error ? errorMessage(query.error) : status?.error
  const busy = query.isFetching || refresh.isPending
  const trae = account.targets.some((t) => t.startsWith('trae'))
  return <div className="flex flex-col gap-2 border-t border-separator pt-2 text-sm">
    <div className="flex items-center justify-between gap-2">
      <span className="font-medium">{trae ? 'Trae 账号额度' : 'Qoder 账号额度'}</span>
      <BusyButton size="sm" variant="ghost" busy={busy} onPress={() => refresh.mutate()}>刷新额度</BusyButton>
    </div>
    {snapshot ? <>
      <p className="font-mono text-xl font-semibold tabular-nums">剩余 {quotaAmount(snapshot.balance, snapshot.unit)}</p>
      <p className="text-muted">已用 {quotaAmount(snapshot.used, snapshot.unit)} · 总额 {quotaAmount(snapshot.total, snapshot.unit)}</p>
      {snapshot.exceeded && <p className="text-warning">上游标记已超额度</p>}
      {snapshot.expires_at && <p className="text-xs text-muted">到期：{fmtLocalDateTime(Date.parse(snapshot.expires_at))}</p>}
      {snapshot.refresh_at && <p className="text-xs text-muted">重置：{fmtLocalDateTime(Date.parse(snapshot.refresh_at))}</p>}
      <p className="text-xs text-muted">{error ? '上次成功：' : '更新：'}{fmtLocalDateTime(Date.parse(snapshot.fetched_at))}</p>
      {!!snapshot.packages?.length && <Disclosure>
        <Disclosure.Heading><Disclosure.Trigger className="inline-flex items-center gap-1 text-sm">官方额度包（{snapshot.packages.length}）<Disclosure.Indicator /></Disclosure.Trigger></Disclosure.Heading>
        <Disclosure.Content>
          <ul className="mt-2 space-y-3">{snapshot.packages.map((p, i) => <li key={i} className="border-t border-separator pt-2 text-xs">
            <p className="font-medium">{p.name || `额度包 ${i + 1}`}{p.hidden ? '（上游隐藏包）' : ''}</p>
            <p>总额 {p.unlimited ? '不限量' : quotaAmount(p.total, p.unit)} · 已用 {quotaAmount(p.used, p.unit)}</p>
            {p.starts_at && <p>开始：{fmtLocalDateTime(Date.parse(p.starts_at))}</p>}
            {p.ends_at && <p>权益结束：{fmtLocalDateTime(Date.parse(p.ends_at))}</p>}
            {p.expires_at && <p>包到期：{fmtLocalDateTime(Date.parse(p.expires_at))}</p>}
            {p.refresh_at && <p>下次计费：{fmtLocalDateTime(Date.parse(p.refresh_at))}</p>}
          </li>)}</ul>
          <p className="mt-2 text-xs text-muted">包用量与汇总的口径尚未完全对齐，按官方原值展示。</p>
        </Disclosure.Content>
      </Disclosure>}
    </> : <p className="text-muted">{busy ? '正在查询官方额度…' : '额度未获取'}</p>}
    {error && <p role="alert" className="break-words text-xs text-danger">{error}{snapshot ? '；当前显示上次成功结果。' : ''}{status?.checked_at ? `（查询于 ${fmtLocalDateTime(Date.parse(status.checked_at))}）` : ''}</p>}
    {trae && <p className="text-xs text-muted">账号汇总，Work／Code 专用额度池的归属未确认。</p>}
  </div>
}
