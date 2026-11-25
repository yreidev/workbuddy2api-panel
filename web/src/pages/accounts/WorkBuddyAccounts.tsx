// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号池：统计（点一下按状态筛选）+ 积分到期提醒 + 代理提醒 + 搜索、按分组筛选 + 账号卡片或账号表（可排序，可多选导出凭证、
// 绑定代理、批量操作）+ 批量任务（含全部测活）+ 账号分组 + 模型锁池。
// 宽屏默认卡片网格，可切换成表格（记在本浏览器）；窄屏只有卡片。
import { useMemo, useState, type ReactNode } from 'react'
import { CreditCard, Pulse } from '@gravity-ui/icons'
import {
  Button, Checkbox, Label, ListBox, Select, Table, ToggleButton, ToggleButtonGroup, useMediaQuery, type Selection, type SortDescriptor,
} from '@heroui/react'
import {
  accountHealth, accountName, CARD_SORT_OPTIONS, cardSortKey, filterAccounts, parseCardSort, poolCredits, sortAccounts, type AccountSortKey, type StatusFilter,
} from '../../lib/accounts'
import { post } from '../../lib/api'
import { ago } from '../../lib/format'
import { toastError, useNow, useStartedToast, useStoredChoice } from '../../lib/hooks'
import type { Account, Overview, ProxyAccount } from '../../lib/types'
import { Empty } from '../../components/Feedback'
import { FloatingActionBar } from '../../components/FloatingActionBar'
import { Panel, Stat } from '../../components/Panel'
import { AccountButtons } from './AccountButtons'
import { AccountCard } from './AccountCard'
import { AccountName, CreditsCell, GroupLine, StatusCell, UsageChips } from './AccountCells'
import { AccountDrawer } from './AccountDrawer'
import { BindProxyModal } from './BindProxyModal'
import { ExpiryReminder } from './ExpiryReminder'
import { ExportActions, SelectionCheckbox } from './ExportBar'
import { LivenessModal } from './LivenessModal'
import { ModelLocks } from './ModelLocks'
import { ProxyReminder } from './ProxyReminder'
import { TasksModal } from './TasksModal'
import { useAccountActions } from './useAccountActions'

const VIEWS = ['cards', 'table'] as const

const BATCH: { path: string; label: string; done: string }[] = [
  { path: 'checkin_all', label: '全部签到', done: '全部签到已开始' },
  { path: 'travel_all', label: '旅行巡检', done: '旅行巡检已开始（含领养链路）' },
  { path: 'activity_all', label: '活跃上报', done: '活跃上报已开始' },
  { path: 'keepalive_all', label: '全部保活', done: '全部保活已开始' },
  { path: 'liveness_all', label: '全部测活', done: '全部测活已开始，结果会显示在各号卡片上' },
]

export function WorkBuddyAccounts({ data, fetchedAt, workBuddyOnly }: {
  data: Overview; fetchedAt: number
  workBuddyOnly?: { filters: ReactNode; accounts: Account[]; clear: () => void; groupPanel: ReactNode }
}) {
  const now = useNow()
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const [status, setStatus] = useState<StatusFilter>('all')
  const [sort, setSort] = useState<SortDescriptor | undefined>()
  const [view, setView] = useStoredChoice('wb2api.accountsView', VIEWS, 'cards')
  // 表格最小宽 1140px，窄屏放不下，只给卡片
  const cards = !isDesktop || view === 'cards'
  const [detail, setDetail] = useState<string | null>(null)
  const [tasksOf, setTasksOf] = useState<string | null>(null)
  const [livenessOf, setLivenessOf] = useState<string | null>(null)
  const [bindUids, setBindUids] = useState<string[] | null>(null)
  const { busy, run, confirmEl } = useAccountActions(setTasksOf, setLivenessOf)
  // 用了代理池（建过池、导过代理或开着「只许走号的代理」）才给绑定入口和「因代理暂停」一栏
  const px = data.proxies
  const proxiesInUse = !!px && (px.pools > 0 || px.proxies > 0 || px.require_bound)
  const proxyOf = (uid: string): ProxyAccount | undefined => px?.accounts?.[uid]
  const onBind = proxiesInUse ? (uids: string[]) => setBindUids(uids) : undefined

  const accounts = useMemo(() => data.accounts || [], [data])
  const pool = useMemo(() => poolCredits(accounts), [accounts])
  const poolMax = pool.max
  const visibleAccounts = workBuddyOnly?.accounts ?? accounts
  const rows = useMemo(
    () => sortAccounts(filterAccounts(visibleAccounts, fetchedAt, status, ''), (sort?.column as AccountSortKey) ?? null, sort?.direction === 'descending'),
    [visibleAccounts, fetchedAt, status, sort],
  )
  // 多选只算当前筛选结果里的号：表头全选（'all'）= 当前筛选和搜索出来的全部；筛掉的号即使勾过也不导出
  const [selection, setSelection] = useState<Selection>(new Set())
  const selected = useMemo(
    () => (selection === 'all' ? rows : rows.filter((a) => selection.has(a.uid))).map((a) => a.uid),
    [selection, rows],
  )
  const toggle = (uid: string, on: boolean) => {
    const next = new Set(selection === 'all' ? rows.map((a) => a.uid) : selection)
    if (on) next.add(uid)
    else next.delete(uid)
    setSelection(next)
  }
  const clearSelection = () => setSelection(new Set())
  const byUid = (uid: string | null) => accounts.find((a) => a.uid === uid) ?? null
  const nameOf = (uid: string) => {
    const a = byUid(uid)
    return a ? accountName(a) : uid.slice(0, 8)
  }
  const detailAcct = byUid(detail)
  const tasksAcct = byUid(tasksOf)
  const livenessAcct = byUid(livenessOf)

  // 暂停选号单独一栏（上游 issue #125）：它只关选号、照常签到保活，和禁用是两种状态
  const tiles: { id: StatusFilter; label: string; value: number; color?: string }[] = [
    { id: 'all', label: '账号总数', value: data.total },
    { id: 'ok', label: '可用', value: data.healthy, color: 'text-success' },
    { id: 'cooling', label: '冷却中', value: data.cooling, color: 'text-warning' },
    { id: 'disabled', label: '已禁用', value: data.disabled, color: 'text-danger' },
    { id: 'paused', label: '已暂停选号', value: data.paused },
  ]
  // 被代理池拦着的号（等代理、代理不通或到期、没绑代理）：不算冷却，单独一栏
  if (proxiesInUse || data.proxy_blocked) tiles.push({ id: 'blocked', label: '因代理暂停', value: data.proxy_blocked ?? 0, color: 'text-warning' })
  // 企业版不限量的号没有可加的数，合计里不算，另外注明
  const creditSum = pool.total > 0 ? `${pool.remain} / ${pool.total}` : pool.remain

  const loadNotice = data.in_flight_full ? `${data.in_flight_full} 个账号满载（在途、每分钟请求数或每分钟 Token 数到上限）` : ''
  const batchNotice = '全部签到、旅行巡检、活跃上报、全部保活与全部测活作用于全部 WorkBuddy 账号。'
  const description = workBuddyOnly && visibleAccounts.length === accounts.length
    ? loadNotice || undefined
    : (loadNotice ? `全部 WorkBuddy 账号中有 ${loadNotice}。` : '') + batchNotice

  return (
    <>
      {!workBuddyOnly && <h2 className="font-semibold">WorkBuddy 状态与积分</h2>}
      {/* 宽屏一行放下：状态卡均分，积分合计、粘性会话按内容定宽（积分合计数字长，均分会折行） */}
      <div className={`grid grid-cols-2 gap-3 sm:grid-cols-4 ${tiles.length > 5 ? 'xl:grid-cols-[repeat(6,minmax(0,1fr))_auto_auto]' : 'xl:grid-cols-[repeat(5,minmax(0,1fr))_auto_auto]'}`}>
        <ToggleButtonGroup
          aria-label="按状态筛选"
          isDetached
          selectionMode="single"
          disallowEmptySelection
          selectedKeys={[status]}
          onSelectionChange={(keys) => setStatus(([...keys][0] as StatusFilter) ?? 'all')}
          className="contents"
        >
          {tiles.map((t) => (
            <ToggleButton
              key={t.id}
              id={t.id}
              className="h-auto w-full flex-col items-start justify-start gap-1 rounded-3xl p-4 text-left transition-[transform,background-color,box-shadow] duration-150 hover:shadow-xs active:scale-[0.98]"
            >
              <span className={`text-2xl font-semibold tabular-nums ${t.color ?? ''}`}>{t.value}</span>
              <span className="text-xs font-normal text-muted">{t.label}</span>
            </ToggleButton>
          ))}
        </ToggleButtonGroup>
        <Stat
          label="积分剩余 / 总额"
          value={creditSum}
          icon={<CreditCard className="size-4" />}
          sub={pool.unlimited ? `另有 ${pool.unlimited} 个企业版不限量` : undefined}
        />
        <Stat label="粘性会话" value={data.sticky_sessions} icon={<Pulse className="size-4" />} />
      </div>

      <ExpiryReminder accounts={accounts} />
      <ProxyReminder warnings={px?.warnings} />

      <Panel
        title={workBuddyOnly ? "账号池" : "WorkBuddy 账号"}
        desc={description}
        actions={<BatchButtons />}
        contentClassName="flex flex-col gap-3"
      >
        <div className="flex flex-wrap items-center justify-between gap-3">
          {workBuddyOnly && <div className="flex w-full flex-wrap items-center gap-3 sm:w-auto">{workBuddyOnly.filters}</div>}
          <div className="flex flex-wrap items-center gap-3">
            {/* 卡片没有表头复选框和可点的表头，在这里给「全选」和排序 */}
            {cards && rows.length > 0 && (
              <SelectionCheckbox
                label="全选"
                isSelected={selected.length === rows.length}
                isIndeterminate={selected.length > 0 && selected.length < rows.length}
                onChange={(v) => setSelection(v ? new Set(rows.map((a) => a.uid)) : new Set())}
              >
                <span className="text-sm">全选</span>
              </SelectionCheckbox>
            )}
            {cards && (
              <Select aria-label="排序" value={cardSortKey(sort)} onChange={(k) => k != null && setSort(parseCardSort(String(k)))} className="w-36">
                <Label className="sr-only">排序</Label>
                <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
                <Select.Popover>
                  <ListBox>
                    {CARD_SORT_OPTIONS.map((o) => <ListBox.Item key={o.id} id={o.id} textValue={o.label}>{o.label}<ListBox.ItemIndicator /></ListBox.Item>)}
                  </ListBox>
                </Select.Popover>
              </Select>
            )}
            {isDesktop && (
              <ToggleButtonGroup
                aria-label="显示方式"
                size="sm"
                selectionMode="single"
                disallowEmptySelection
                selectedKeys={[view]}
                onSelectionChange={(keys) => { const k = [...keys][0]; if (k === 'cards' || k === 'table') setView(k) }}
              >
                <ToggleButton id="cards">卡片</ToggleButton>
                <ToggleButton id="table">表格</ToggleButton>
              </ToggleButtonGroup>
            )}
          </div>
        </div>
        {accounts.length === 0 ? (
          <Empty title="账号池是空的">点击右上角「添加账号」，选择产品与地区后完成授权或导入。</Empty>
        ) : rows.length === 0 ? (
          <Empty title="没有符合条件的账号" action={<Button size="sm" variant="secondary" onPress={() => { setStatus('all'); workBuddyOnly?.clear() }}>{workBuddyOnly ? '清除筛选' : '清除状态筛选'}</Button>} />
        ) : !cards ? (
          <AccountTable
            rows={rows} fetchedAt={fetchedAt} now={now} poolMax={poolMax} sort={sort} onSort={setSort} selection={selection} onSelect={setSelection}
            busy={busy} run={run} onOpen={setDetail} proxyOf={proxyOf}
          />
        ) : (
          // 卡片至少 340px 宽，放得下几列就排几列
          <div className="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(100%,340px),1fr))]">
            {rows.map((a) => (
              <AccountCard
                key={a.uid}
                a={a}
                health={accountHealth(a, fetchedAt)}
                proxy={proxyOf(a.uid)}
                now={now}
                poolMax={poolMax}
                selected={selected.includes(a.uid)}
                onSelect={(v) => toggle(a.uid, v)}
                onOpen={() => setDetail(a.uid)}
                busy={busy}
                run={run}
              />
            ))}
          </div>
        )}
        <FloatingActionBar open={selected.length > 0} ariaLabel="账号批量操作">
          <ExportActions selected={selected} onClear={clearSelection} onBind={onBind && (() => onBind(selected))} nameOf={nameOf} />
        </FloatingActionBar>
      </Panel>

      {workBuddyOnly?.groupPanel}
      <ModelLocks rows={data.model_locks || []} now={now} />

      <AccountDrawer
        account={detailAcct}
        health={detailAcct ? accountHealth(detailAcct, fetchedAt) : null}
        proxy={detailAcct ? proxyOf(detailAcct.uid) : undefined}
        now={now}
        busy={busy}
        run={run}
        onBind={onBind && ((uid) => onBind([uid]))}
        onClose={() => setDetail(null)}
      />
      <TasksModal uid={tasksOf} name={tasksAcct ? accountName(tasksAcct) : tasksOf?.slice(0, 16) || ''} onClose={() => setTasksOf(null)} />
      <LivenessModal account={livenessAcct} onClose={() => setLivenessOf(null)} />
      <BindProxyModal uids={bindUids} onClose={() => setBindUids(null)} />
      {confirmEl}
    </>
  )
}

const SelectBox = ({ label }: { label: string }) => (
  <Checkbox aria-label={label} slot="selection" variant="secondary">
    <Checkbox.Content>
      <Checkbox.Control>
        <Checkbox.Indicator />
      </Checkbox.Control>
    </Checkbox.Content>
  </Checkbox>
)

function AccountTable({ rows, fetchedAt, now, poolMax, sort, onSort, selection, onSelect, busy, run, onOpen, proxyOf }: {
  rows: Account[]
  fetchedAt: number
  now: number
  poolMax: number
  sort: SortDescriptor | undefined
  onSort: (s: SortDescriptor) => void
  selection: Selection
  onSelect: (s: Selection) => void
  busy: string | null
  run: ReturnType<typeof useAccountActions>['run']
  onOpen: (uid: string) => void
  proxyOf: (uid: string) => ProxyAccount | undefined
}) {
  const sortable = (id: AccountSortKey, label: string) => (
    <Table.Column id={id} allowsSorting className="whitespace-nowrap">
      {({ sortDirection }) => <Table.SortableColumnHeader sortDirection={sortDirection}>{label}</Table.SortableColumnHeader>}
    </Table.Column>
  )
  return (
    <Table variant="secondary">
      <Table.ScrollContainer>
        <Table.Content
          aria-label="账号池"
          className="min-w-[1140px]"
          sortDescriptor={sort}
          onSortChange={onSort}
          selectionMode="multiple"
          selectedKeys={selection}
          onSelectionChange={onSelect}
          onRowAction={(key) => onOpen(String(key))}
        >
          <Table.Header>
            <Table.Column id="select" className="pe-0"><SelectBox label="全选" /></Table.Column>
            <Table.Column id="name" isRowHeader allowsSorting className="whitespace-nowrap">
              {({ sortDirection }) => <Table.SortableColumnHeader sortDirection={sortDirection}>账号</Table.SortableColumnHeader>}
            </Table.Column>
            <Table.Column id="status" className="whitespace-nowrap">状态</Table.Column>
            {sortable('credits', '积分')}
            {sortable('errors', '成功 / 失败')}
            <Table.Column id="usage" className="whitespace-nowrap">用量</Table.Column>
            {sortable('last_success', '最近成功')}
            <Table.Column id="actions"> </Table.Column>
          </Table.Header>
          <Table.Body>
            {rows.map((a) => {
              const health = accountHealth(a, fetchedAt)
              return (
                <Table.Row key={a.uid} id={a.uid} className="cursor-pointer">
                  <Table.Cell className="pe-0"><SelectBox label={'选择 ' + accountName(a)} /></Table.Cell>
                  <Table.Cell><div className="flex max-w-40 flex-col"><AccountName a={a} /><GroupLine a={a} /></div></Table.Cell>
                  <Table.Cell><StatusCell a={a} health={health} now={now} proxy={proxyOf(a.uid)} /></Table.Cell>
                  <Table.Cell><CreditsCell a={a} poolMax={poolMax} now={now} /></Table.Cell>
                  <Table.Cell className="whitespace-nowrap">
                    <div className="font-mono tabular-nums">
                      {a.success_count || 0} <span className="text-muted">/</span> <span className="text-danger">{a.err_total || 0}</span>
                    </div>
                    <div className="text-xs text-muted">在途 {a.in_flight || 0}</div>
                  </Table.Cell>
                  <Table.Cell><div className="w-36"><UsageChips a={a} /></div></Table.Cell>
                  <Table.Cell className="whitespace-nowrap text-muted">{ago(a.last_success, now)}</Table.Cell>
                  <Table.Cell><div className="flex justify-end"><AccountButtons a={a} health={health} busy={busy} run={run} nowrap /></div></Table.Cell>
                </Table.Row>
              )
            })}
          </Table.Body>
        </Table.Content>
      </Table.ScrollContainer>
    </Table>
  )
}

function BatchButtons() {
  const started = useStartedToast()
  const [busy, setBusy] = useState<string | null>(null)
  const fire = async (b: (typeof BATCH)[number]) => {
    setBusy(b.path)
    try {
      await post(b.path)
      started(b.done)
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(null)
    }
  }
  return BATCH.map((b) => (
    <Button key={b.path} size="sm" variant="secondary" isPending={busy === b.path} onPress={() => void fire(b)}>{b.label}</Button>
  ))
}
