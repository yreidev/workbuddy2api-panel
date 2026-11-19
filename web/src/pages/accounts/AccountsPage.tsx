// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { useState } from 'react'
import { Button, Label, SearchField } from '@heroui/react'
import { accountHealth, NO_GROUP } from '../../lib/accounts'
import { filterAccountPool, matchesPoolTarget } from '../../lib/accountPool'
import { hasProviderData, useGroups, useOverview, useProviders } from '../../lib/queries'
import type { Group, Overview, ProvidersResp } from '../../lib/types'
import { Empty, Loaded } from '../../components/Feedback'
import { Panel, Stat } from '../../components/Panel'
import { TargetSelect } from '../../components/TargetSelect'
import { GroupsPanel } from './GroupsPanel'
import { ProviderAccounts, ProviderUsagePanel } from './ProviderAccounts'
import { WorkBuddyAccounts } from './WorkBuddyAccounts'

const PRODUCTS = [
  { id: 'all', name: '全部产品' }, { id: 'workbuddy', name: 'WorkBuddy' },
  { id: 'traework', name: 'TraeWork' }, { id: 'traecode', name: 'Trae Code' }, { id: 'qoder', name: 'Qoder' },
]
const REGIONS = [{ id: 'all', name: '全部地区' }, { id: 'cn', name: '国内' }, { id: 'global', name: '国际' }]

export function AccountsPage() {
  const overview = useOverview(5000)
  const providers = useProviders(5000)
  const groups = useGroups()
  return <Loaded query={overview} rows={6}>{(data) => (
    <Loaded query={providers}>{(platforms) => (
      <Loaded query={groups}>{(g) => <AccountPool data={data} platforms={platforms} groups={g} fetchedAt={overview.dataUpdatedAt} />}</Loaded>
    )}</Loaded>
  )}</Loaded>
}

function AccountPool({ data, platforms, groups, fetchedAt }: { data: Overview; platforms: ProvidersResp; groups: Group[]; fetchedAt: number }) {
  const [product, setProduct] = useState('all')
  const [region, setRegion] = useState('all')
  const [query, setQuery] = useState('')
  const [groupPick, setGroup] = useState('')
  const group = groupPick === NO_GROUP || groups.some((g) => g.id === groupPick) ? groupPick : ''
  const workBuddyOnly = !hasProviderData(platforms)
  const filter = { product: workBuddyOnly ? 'all' : product, region, query, group }
  const allWorkBuddy = data.accounts ?? []
  const shown = filterAccountPool(allWorkBuddy, platforms.accounts, filter)
  const clear = () => { setProduct('all'); setRegion('all'); setQuery(''); setGroup('') }
  const showRegion = !workBuddyOnly || region !== 'all' || new Set(allWorkBuddy.map((a) => a.realm || 'cn')).size > 1
  const showGroup = !workBuddyOnly || groups.length > 0 || group !== ''
  const filters = <>
    <SearchField className={workBuddyOnly ? 'w-full sm:w-72' : undefined} value={query} onChange={setQuery}>
      <Label className={workBuddyOnly ? 'sr-only' : undefined}>搜索账号</Label>
      <SearchField.Group><SearchField.SearchIcon /><SearchField.Input placeholder={workBuddyOnly ? '搜索昵称或 UID' : '名称、备注或账号 ID'} /><SearchField.ClearButton /></SearchField.Group>
    </SearchField>
    {!workBuddyOnly && <TargetSelect label="产品" value={product} onChange={setProduct} targets={PRODUCTS} />}
    {showRegion && <TargetSelect compact={workBuddyOnly} label="地区" value={region} onChange={setRegion} targets={REGIONS} />}
    {showGroup && <TargetSelect compact={workBuddyOnly} label="账号分组" value={group || 'all'} onChange={(v) => setGroup(v === 'all' ? '' : v)} targets={[
      { id: 'all', name: '全部分组' }, { id: NO_GROUP, name: '没分组的号' }, ...groups.map((g) => ({ id: g.id, name: g.name })),
    ]} />}
  </>
  const groupPanel = <GroupsPanel groups={groups} onFilter={(id) => { setGroup(id); setProduct('all'); setRegion('all'); setQuery('') }} />
  if (workBuddyOnly) {
    return <WorkBuddyAccounts data={data} fetchedAt={fetchedAt} workBuddyOnly={{ filters, accounts: shown.workbuddy, clear, groupPanel }} />
  }
  const total = allWorkBuddy.length + platforms.accounts.length
  const visible = shown.workbuddy.length + shown.providers.length
  const health = shown.workbuddy.map((a) => accountHealth(a, fetchedAt).state)
  const wb: Overview = {
    ...data, accounts: shown.workbuddy, total: shown.workbuddy.length,
    healthy: health.filter((s) => s === 'ok').length, cooling: health.filter((s) => s === 'cooling').length,
    disabled: health.filter((s) => s === 'disabled').length, paused: health.filter((s) => s === 'paused').length,
    proxy_blocked: health.filter((s) => s === 'blocked').length,
  }
  const providerIDs = new Set(shown.providers.map((a) => a.account_id))
  // 未按名称或分组筛号时保留已移除账号的历史用量。
  const byAccount = !!(query.trim() || group)
  const usage = platforms.usage.filter((u) => (!byAccount || providerIDs.has(u.account_id)) && matchesPoolTarget(u.target, filter))
  return <>
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
      <Stat label="账号总数" value={total} />
      <Stat label="WorkBuddy 账号" value={allWorkBuddy.length} />
      <Stat label="Trae 账号" value={platforms.accounts.filter((a) => a.targets.some((t) => t.startsWith('trae'))).length} />
      <Stat label="Qoder 账号" value={platforms.accounts.filter((a) => a.targets.some((t) => t.startsWith('qoder:'))).length} />
    </div>
    <Panel title="全部平台账号" desc={`符合筛选 ${visible} / ${total} 个账号；同一账号的多个产品通道只计一个账号。`} contentClassName="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      {filters}
    </Panel>
    {!total ? <Empty title="账号池是空的">点击右上角「添加账号」，选择产品与地区后完成授权或导入。</Empty>
      : !visible && <Empty title="没有符合条件的账号" action={<Button size="sm" variant="secondary" onPress={clear}>清除筛选</Button>} />}
    {shown.workbuddy.length > 0 && <WorkBuddyAccounts data={wb} fetchedAt={fetchedAt} />}
    <ProviderAccounts accounts={shown.providers} usage={usage} />
    {usage.length > 0 && <ProviderUsagePanel usage={usage} />}
    {groupPanel}
  </>
}
