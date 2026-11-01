// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 代理页（规则见 docs/proxy.md）：统计和提醒 + 代理池（增删改、池内重新均分）+ 代理表（批量导入先预览、
// 导出、全部重测、删掉不可用；每个代理看状态、出口 IP、到期、绑了哪些号，能测、改、删）+ 代理池设置。
// 数据 5 秒一刷：健康检查、查出口 IP 都在后台跑。号绑哪个代理在账号池页多选绑定，或在账号详情里改。
import { useMemo, useState } from 'react'
import {
  Button, Card, Checkbox, Chip, Label, ListBox, SearchField, Select, Table, ToggleButton, ToggleButtonGroup, toast, useMediaQuery, type Selection,
} from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { apiText, post } from '../../lib/api'
import { download, stamp } from '../../lib/credentials'
import { ago, dateTime, fmtLocalDateTime, timeMs, until } from '../../lib/format'
import { toastError, useNow } from '../../lib/hooks'
import { defaultForLabels, exitGeo, filterProxies, loadText, PROXY_FILTERS, proxyStatus, type ProxyFilter } from '../../lib/proxies'
import { qk, useAccountNames, useProxies } from '../../lib/queries'
import type { ProxiesResp, ProxyItem, ProxyPool } from '../../lib/types'
import { Empty, ErrorBox, Loaded } from '../../components/Feedback'
import { Hint } from '../../components/Hint'
import { InfoTip, Panel, Stat } from '../../components/Panel'
import { useConfirm } from '../../components/useConfirm'
import { SelectionCheckbox } from '../accounts/ExportBar'
import { ProxyReminder } from '../accounts/ProxyReminder'
import { ImportProxies } from './ImportProxies'
import { PoolEditor } from './PoolEditor'
import { ProxyEditor } from './ProxyEditor'
import { ProxySettingsPanel } from './ProxySettingsPanel'

export function ProxiesPage() {
  const query = useProxies(5000)
  return (
    <Loaded query={query} rows={6}>
      {(d) => (d ? <Proxies data={d} /> : <ErrorBox title="用不了代理池" error="这个网关没有装代理池。" />)}
    </Loaded>
  )
}

function Proxies({ data }: { data: ProxiesResp }) {
  const v = data.view
  const qc = useQueryClient()
  const [confirmEl, ask] = useConfirm()
  const [poolEdit, setPoolEdit] = useState<ProxyPool | 'new' | null>(null)
  const [proxyEdit, setProxyEdit] = useState<ProxyItem | null>(null)
  const [importing, setImporting] = useState(false)
  const [exporting, setExporting] = useState(false)
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: qk.proxies })
    void qc.invalidateQueries({ queryKey: qk.overview })
  }
  const w = v.warnings
  const usable = v.proxies.filter((p) => p.usable).length
  const bad = v.proxies.filter((p) => p.expired || p.health.state === 'down')

  const deletePool = (p: ProxyPool) => {
    if (p.proxies > 0) {
      toast.danger(`池「${p.name}」里还有 ${p.proxies} 个代理，先删掉或移到别的池`)
      return
    }
    ask({
      title: `删掉池「${p.name}」？`,
      body: p.accounts ? `绑着这个池、在等代理的 ${p.accounts} 个号会解绑。` : '池里没有代理，也没有号。',
      confirmLabel: '删掉',
      onConfirm: () => void post(`proxy_pools/${encodeURIComponent(p.id)}/delete`).then(() => {
        toast.success(`池「${p.name}」已删掉`)
        refresh()
      }, (e: unknown) => toastError(e, '删除失败：')),
    })
  }
  const rebalance = (p: ProxyPool) => ask({
    title: `池「${p.name}」重新均分？`,
    body: '池里不是单独指定代理的号，全部按绑号最少重新分一遍。换了代理的号出口 IP 会变，官方那边相当于换了网络，没必要时别常做。',
    confirmLabel: '重新均分',
    onConfirm: () => void post<{ moved: number }>(`proxy_pools/${encodeURIComponent(p.id)}/rebalance`).then((r) => {
      toast.success(r.moved ? `${r.moved} 个号换了代理` : '已经分得很匀，没有号要换')
      refresh()
    }, (e: unknown) => toastError(e, '重新均分失败：')),
  })
  const deleteProxies = (list: ProxyItem[], onDone?: () => void) => {
    const bound = list.reduce((n, p) => n + p.accounts.length, 0)
    ask({
      title: list.length === 1 ? `删掉代理「${list[0].name}」？` : `删掉 ${list.length} 个代理？`,
      body: bound ? `绑在上面的 ${bound} 个号改绑到同池别的代理，没有空位就等代理（等的时候不接请求）。` : '没有号绑在上面。',
      confirmLabel: '删掉',
      onConfirm: () => void post<{ deleted: number }>('proxies/delete', { ids: list.map((p) => p.id) }).then((r) => {
        toast.success(`删掉了 ${r.deleted} 个代理`)
        onDone?.()
        refresh()
      }, (e: unknown) => toastError(e, '删除失败：')),
    })
  }
  const deleteBad = () => {
    const down = bad.filter((p) => !p.expired).length
    const bound = bad.reduce((n, p) => n + p.accounts.length, 0)
    ask({
      title: `删掉 ${bad.length} 个不可用的代理？`,
      body: `连不上的 ${down} 个、已到期的 ${bad.length - down} 个（还没测过的不删）。` +
        (bound ? `绑在上面的 ${bound} 个号改绑到同池能用的代理，没有空位就等代理。` : '没有号绑在上面。'),
      confirmLabel: '删掉',
      onConfirm: () => void post<{ deleted: number }>('proxies/delete_unavailable').then((r) => {
        toast.success(`删掉了 ${r.deleted} 个不可用的代理`)
        refresh()
      }, (e: unknown) => toastError(e, '删除失败：')),
    })
  }
  const testAll = () => void post<{ total: number }>('proxies/test_all').then((r) => {
    toast.success(`开始重测 ${r.total} 个代理`, { description: '连通和出口 IP 都测，结果陆续出现在列表里。' })
  }, (e: unknown) => toastError(e))
  const exportAll = async () => {
    setExporting(true)
    try {
      download({ name: `proxies-${stamp(new Date())}.txt`, text: await apiText('proxies/export') }, 'text/plain')
      toast.success(`已导出 ${v.proxies.length} 个代理`, {
        description: '文件里是带密码的完整地址，请妥善保管。按池分组（# 开头的行是池名），导回来时每个池的那几行各选各的池导入。',
        timeout: 8000,
      })
    } catch (e) {
      toastError(e, '导出失败：')
    } finally {
      setExporting(false)
    }
  }

  const exit = v.default_exit
  const exitDesc = exit?.ip
    ? `没绑代理的号从 ${exit.ip}${exitGeo(exit) ? '（' + exitGeo(exit) + '）' : ''} 出去。`
    : '没绑代理的号走服务器自己的网络（出口 IP 还没查到）。'

  return (
    <>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-6">
        <Stat label="代理" value={v.proxies.length} sub={`${v.pools.length} 个池`} />
        <Stat label="能用" value={usable} tone={usable ? 'success' : undefined} />
        <Stat label="连不上" value={w.down} tone={w.down ? 'danger' : undefined} />
        <Stat label="已到期" value={w.expired} tone={w.expired ? 'danger' : undefined} />
        <Stat label={`快到期（${v.settings.expiry_warn_days} 天内）`} value={w.expiring} tone={w.expiring ? 'warning' : undefined} />
        <Stat label="等代理的号" value={w.waiting} tone={w.waiting ? 'warning' : undefined} />
      </div>

      <ProxyReminder warnings={w} link={false} />

      <Panel
        title={
          <span className="flex items-center gap-1">
            代理池
            <InfoTip label="代理池说明">
              号绑在池上，池里按绑号最少分代理，分到后固定不变；号也可以单独指定代理，盖过池的分配。{'\n'}
              国内版、国际版各能设一个默认池：新登录、导入的号没选池时放进默认池。{'\n'}
              每个代理最多几个号按池设，满了新号等着（不接请求），加了代理或调高上限后自动补上。{'\n'}
              代理连不上时号先暂停出站、不罚号；连续不可用超过设定的时间、同池有能用的代理，就改绑过去，从不退回直连。
            </InfoTip>
          </span>
        }
        desc="号绑池，池里分代理。号绑哪个池在账号池页多选「绑定代理」，或在账号详情里改。"
        actions={<Button size="sm" variant="secondary" onPress={() => setPoolEdit('new')}>新建池</Button>}
      >
        {v.pools.length === 0 ? (
          <Empty title="还没有代理池">先建一个池（比如「国内」「国际版」），再往池里导入代理。</Empty>
        ) : (
          <div className="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(100%,300px),1fr))]">
            {v.pools.map((p) => (
              <PoolCard key={p.id} p={p} onEdit={() => setPoolEdit(p)} onRebalance={() => rebalance(p)} onDelete={() => deletePool(p)} />
            ))}
          </div>
        )}
      </Panel>

      <Panel
        title="代理"
        desc={exitDesc}
        actions={
          <>
            <Button size="sm" variant="primary" isDisabled={v.pools.length === 0} onPress={() => setImporting(true)}>导入代理</Button>
            <Button size="sm" variant="secondary" isDisabled={v.proxies.length === 0} isPending={exporting} onPress={() => void exportAll()}>导出</Button>
            <Button size="sm" variant="secondary" isDisabled={v.proxies.length === 0} onPress={testAll}>全部重测</Button>
            <Button size="sm" variant="secondary" isDisabled={bad.length === 0} onPress={deleteBad}>删掉不可用{bad.length ? `（${bad.length}）` : ''}</Button>
          </>
        }
        contentClassName="flex flex-col gap-3"
      >
        {v.proxies.length === 0 ? (
          <Empty
            title="还没有代理"
            action={v.pools.length > 0 && <Button size="sm" variant="secondary" onPress={() => setImporting(true)}>导入代理</Button>}
          >
            {v.pools.length === 0 ? '先在上面建一个池，再导入代理。' : '把代理一行一个贴进来或上传 txt，先预览再导入，导入后自动测连通和出口 IP。'}
          </Empty>
        ) : (
          <ProxyTable data={data} onEdit={setProxyEdit} onDelete={deleteProxies} />
        )}
      </Panel>

      <ProxySettingsPanel settings={v.settings} accounts={data.accounts} />

      <PoolEditor target={poolEdit} accounts={data.accounts} onClose={() => setPoolEdit(null)} onSaved={refresh} />
      <ProxyEditor target={proxyEdit} pools={v.pools} onClose={() => setProxyEdit(null)} onSaved={refresh} />
      <ImportProxies open={importing} pools={v.pools} onClose={() => setImporting(false)} onDone={refresh} />
      {confirmEl}
    </>
  )
}

function PoolCard({ p, onEdit, onRebalance, onDelete }: { p: ProxyPool; onEdit: () => void; onRebalance: () => void; onDelete: () => void }) {
  return (
    <Card variant="secondary" className="gap-2 p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{p.name}</span>
        {defaultForLabels(p).map((l) => <Chip key={l} size="sm" variant="soft" color="accent">{l}</Chip>)}
      </div>
      <div className="text-sm text-muted">
        代理 {p.proxies} 个{p.proxies > 0 && `，${p.usable} 个能用`} · {p.max_per_proxy ? `每个最多 ${p.max_per_proxy} 个号` : '每个不限号数'}
      </div>
      <div className="text-sm">
        绑了 {p.accounts} 个号{p.waiting > 0 && <span className="text-warning">，{p.waiting} 个在等代理</span>}
      </div>
      <div className="-mx-1 mt-auto flex flex-wrap gap-1">
        <Button size="sm" variant="ghost" onPress={onEdit}>改</Button>
        <Button size="sm" variant="ghost" isDisabled={p.accounts === 0 || p.usable === 0} onPress={onRebalance}>重新均分</Button>
        <Button size="sm" variant="ghost" className="text-danger" onPress={onDelete}>删</Button>
      </div>
    </Card>
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

function ProxyTable({ data, onEdit, onDelete }: {
  data: ProxiesResp
  onEdit: (p: ProxyItem) => void
  onDelete: (list: ProxyItem[], onDone?: () => void) => void
}) {
  const v = data.view
  const qc = useQueryClient()
  const now = useNow(30_000)
  const names = useAccountNames()
  // 窄屏（同请求记录页）：表格换成列表，状态筛选换成下拉框，多选后的操作贴在底部
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const [pool, setPool] = useState('')
  const [filter, setFilter] = useState<ProxyFilter>('all')
  const [q, setQ] = useState('')
  const [testing, setTesting] = useState<string | null>(null)
  const rows = useMemo(() => filterProxies(v.proxies, pool, filter, q), [v.proxies, pool, filter, q])
  const poolOf = (id: string) => v.pools.find((p) => p.id === id)
  // 多选只算当前筛选结果里的代理（同账号池）
  const [selection, setSelection] = useState<Selection>(new Set())
  const selected = selection === 'all' ? rows : rows.filter((p) => selection.has(p.id))
  const toggle = (id: string, on: boolean) => {
    const next = new Set(selection === 'all' ? rows.map((p) => p.id) : selection)
    if (on) next.add(id)
    else next.delete(id)
    setSelection(next)
  }
  const clearSelection = () => setSelection(new Set())
  const selectedActions = (
    <>
      <span className="text-sm text-muted">已选 {selected.length} 个</span>
      <Button size="sm" variant="secondary" className="text-danger" onPress={() => onDelete(selected, clearSelection)}>删掉</Button>
      <Button size="sm" variant="tertiary" onPress={clearSelection}>取消选择</Button>
    </>
  )

  const test = async (p: ProxyItem) => {
    setTesting(p.id)
    try {
      await post(`proxies/${encodeURIComponent(p.id)}/test`)
      await qc.refetchQueries({ queryKey: qk.proxies })
      const np = qc.getQueryData<ProxiesResp | null>(qk.proxies)?.view.proxies.find((x) => x.id === p.id)
      if (np) {
        const st = proxyStatus(np)
        const title = `「${np.name}」${st.label}` + (np.health.exit?.ip ? ' · 出口 ' + np.health.exit.ip : '')
        if (st.color === 'success') toast.success(title)
        else toast.danger(title, st.detail ? { description: st.detail } : undefined)
      }
    } catch (e) {
      toastError(e, '测试失败：')
    } finally {
      setTesting(null)
    }
  }
  const actions = (p: ProxyItem) => (
    <>
      <Button size="sm" variant="ghost" isPending={testing === p.id} isDisabled={testing != null && testing !== p.id} onPress={() => void test(p)}>测试</Button>
      <Button size="sm" variant="ghost" onPress={() => onEdit(p)}>改</Button>
      <Button size="sm" variant="ghost" className="text-danger" onPress={() => onDelete([p])}>删</Button>
    </>
  )

  return (
    <>
      <div className="flex flex-wrap items-center gap-3">
        <SearchField aria-label="搜索代理" value={q} onChange={setQ} className="w-full sm:w-64">
          <SearchField.Group>
            <SearchField.SearchIcon />
            <SearchField.Input placeholder="搜索名字、地址、出口 IP" />
            <SearchField.ClearButton />
          </SearchField.Group>
        </SearchField>
        {v.pools.length > 1 && (
          <Select aria-label="按池筛选" value={pool || 'all'} onChange={(k) => k != null && setPool(k === 'all' ? '' : String(k))} className="w-40">
            <Label className="sr-only">池</Label>
            <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
            <Select.Popover>
              <ListBox>
                <ListBox.Item id="all" textValue="全部池">全部池<ListBox.ItemIndicator /></ListBox.Item>
                {v.pools.map((p) => <ListBox.Item key={p.id} id={p.id} textValue={p.name}>{p.name}<ListBox.ItemIndicator /></ListBox.Item>)}
              </ListBox>
            </Select.Popover>
          </Select>
        )}
        {isDesktop ? (
          <ToggleButtonGroup
            aria-label="按状态筛选"
            size="sm"
            selectionMode="single"
            disallowEmptySelection
            selectedKeys={[filter]}
            onSelectionChange={(keys) => setFilter(([...keys][0] as ProxyFilter) ?? 'all')}
          >
            {PROXY_FILTERS.map((f) => <ToggleButton key={f.id} id={f.id}>{f.label}</ToggleButton>)}
          </ToggleButtonGroup>
        ) : (
          <Select aria-label="按状态筛选" value={filter} onChange={(k) => k != null && setFilter(k as ProxyFilter)} className="w-32">
            <Label className="sr-only">状态</Label>
            <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
            <Select.Popover>
              <ListBox>
                {PROXY_FILTERS.map((f) => <ListBox.Item key={f.id} id={f.id} textValue={f.label}>{f.label}<ListBox.ItemIndicator /></ListBox.Item>)}
              </ListBox>
            </Select.Popover>
          </Select>
        )}
        {/* 列表没有表头复选框，在这里给「全选」 */}
        {!isDesktop && rows.length > 0 && (
          <SelectionCheckbox
            label="全选"
            isSelected={selected.length === rows.length}
            isIndeterminate={selected.length > 0 && selected.length < rows.length}
            onChange={(on) => setSelection(on ? new Set(rows.map((p) => p.id)) : new Set())}
          >
            <span className="text-sm">全选</span>
          </SelectionCheckbox>
        )}
        {isDesktop && selected.length > 0 && <div className="ml-auto flex items-center gap-2">{selectedActions}</div>}
      </div>

      {rows.length === 0 ? (
        <Empty
          title="没有符合条件的代理"
          action={<Button size="sm" variant="secondary" onPress={() => { setQ(''); setFilter('all'); setPool('') }}>清除筛选</Button>}
        />
      ) : isDesktop ? (
        <Table variant="secondary">
          <Table.ScrollContainer>
            <Table.Content
              aria-label="代理"
              className="min-w-[1100px]"
              selectionMode="multiple"
              selectedKeys={selection}
              onSelectionChange={setSelection}
              onRowAction={(key) => {
                const p = rows.find((x) => x.id === key)
                if (p) onEdit(p)
              }}
            >
              <Table.Header>
                <Table.Column id="select" className="pe-0"><SelectBox label="全选" /></Table.Column>
                <Table.Column id="name" isRowHeader className="whitespace-nowrap">代理</Table.Column>
                <Table.Column id="pool" className="whitespace-nowrap">池</Table.Column>
                <Table.Column id="status" className="whitespace-nowrap">状态</Table.Column>
                <Table.Column id="exit" className="whitespace-nowrap">出口</Table.Column>
                <Table.Column id="expiry" className="whitespace-nowrap">到期</Table.Column>
                <Table.Column id="accounts" className="whitespace-nowrap">绑的号</Table.Column>
                <Table.Column id="actions"> </Table.Column>
              </Table.Header>
              <Table.Body>
                {rows.map((p) => (
                  <Table.Row key={p.id} id={p.id} className="cursor-pointer">
                    <Table.Cell className="pe-0"><SelectBox label={'选择 ' + p.name} /></Table.Cell>
                    <Table.Cell>
                      <div className="max-w-56 min-w-0">
                        <div className="truncate font-medium">{p.name}</div>
                        <Hint tip={p.url} className="block truncate font-mono text-xs text-muted">{p.url}</Hint>
                      </div>
                    </Table.Cell>
                    <Table.Cell className="whitespace-nowrap">{poolOf(p.pool)?.name ?? '—'}</Table.Cell>
                    <Table.Cell><StatusCell p={p} now={now} /></Table.Cell>
                    <Table.Cell><ExitCell p={p} now={now} /></Table.Cell>
                    <Table.Cell><ExpiryCell p={p} now={now} /></Table.Cell>
                    <Table.Cell><AccountsCell p={p} pool={poolOf(p.pool)} names={names.name} /></Table.Cell>
                    <Table.Cell><div className="flex justify-end gap-1">{actions(p)}</div></Table.Cell>
                  </Table.Row>
                ))}
              </Table.Body>
            </Table.Content>
          </Table.ScrollContainer>
        </Table>
      ) : (
        <>
          <ul className="flex flex-col divide-y divide-separator">
            {rows.map((p) => (
              <li key={p.id} className="flex flex-col gap-2 py-3 text-sm">
                <div className="flex items-start gap-2">
                  <SelectionCheckbox label={'选择 ' + p.name} isSelected={selection === 'all' || selection.has(p.id)} onChange={(on) => toggle(p.id, on)} />
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-baseline gap-x-2">
                      <span className="font-medium">{p.name}</span>
                      <span className="text-xs text-muted">{poolOf(p.pool)?.name ?? '—'}</span>
                    </div>
                    <div className="break-all font-mono text-xs text-muted">{p.url}</div>
                  </div>
                </div>
                <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-2 ps-7">
                  <dt className="text-xs leading-6 text-muted">状态</dt>
                  <dd><StatusCell p={p} now={now} /></dd>
                  <dt className="text-xs leading-6 text-muted">出口</dt>
                  <dd><ExitCell p={p} now={now} /></dd>
                  {p.expires_at && (
                    <>
                      <dt className="text-xs leading-6 text-muted">到期</dt>
                      <dd><ExpiryCell p={p} now={now} /></dd>
                    </>
                  )}
                  <dt className="text-xs leading-6 text-muted">绑的号</dt>
                  <dd><AccountsCell p={p} pool={poolOf(p.pool)} names={names.name} /></dd>
                </dl>
                <div className="flex gap-1 ps-6">{actions(p)}</div>
              </li>
            ))}
          </ul>
          {/* 列表很长，滚到哪都看得到（同账号池页） */}
          {selected.length > 0 && (
            <Card className="sticky bottom-3 z-30 flex-row flex-wrap items-center gap-3 p-3 shadow-lg">{selectedActions}</Card>
          )}
        </>
      )}
    </>
  )
}

function AccountsCell({ p, pool, names }: { p: ProxyItem; pool?: ProxyPool; names: (uid: string) => string }) {
  return (
    <div className="flex max-w-48 flex-col items-start gap-1">
      <span className="inline-flex items-center gap-1 whitespace-nowrap tabular-nums">
        {loadText(p, pool)}
        {p.over ? <Chip size="sm" variant="soft" color="warning">超上限</Chip> : p.full && <Chip size="sm" variant="soft">满</Chip>}
      </span>
      {p.accounts.length > 0 && (
        <Hint tip={p.accounts.map((uid) => names(uid)).join('\n')} className="line-clamp-2 text-xs text-muted">
          {p.accounts.map((uid) => names(uid)).join('、')}
        </Hint>
      )}
    </div>
  )
}

function StatusCell({ p, now }: { p: ProxyItem; now: number }) {
  const st = proxyStatus(p)
  const down = timeMs(p.health.down_since)
  return (
    <div className="flex max-w-48 flex-col items-start gap-1">
      <Chip size="sm" variant="soft" color={st.color}>{st.label}</Chip>
      {!p.expired && p.health.state === 'down' && down != null && <span className="text-xs text-muted">{fmtLocalDateTime(down)} 起</span>}
      {st.detail && <Hint tip={st.detail} className="line-clamp-2 wrap-anywhere text-xs text-danger">{st.detail}</Hint>}
      {p.health.checked_at && timeMs(p.health.checked_at) != null && <span className="text-xs text-muted">{ago(p.health.checked_at, now)}测</span>}
    </div>
  )
}

function ExitCell({ p, now }: { p: ProxyItem; now: number }) {
  const e = p.health.exit
  const geo = exitGeo(e)
  const changed = timeMs(p.health.exit_changed_at)
  return (
    <div className="flex max-w-56 flex-col items-start gap-1">
      {e?.ip ? <span className="font-mono text-sm">{e.ip}</span> : <span className="text-muted">还没查到</span>}
      {geo && <span className="text-xs text-muted">{geo}</span>}
      {e?.error && <Hint tip={e.error} className="line-clamp-1 wrap-anywhere text-xs text-danger">这次没查到：{e.error}</Hint>}
      {(!!p.dup_exit?.length || p.same_as_default || p.exit_changed) && (
        <div className="flex flex-wrap gap-1">
          {!!p.dup_exit?.length && (
            <Hint tip={`出口 IP 和「${p.dup_exit.join('」「')}」一样：这几个代理其实是同一个出口，绑在上面的号在官方看来是同一个 IP 下的号。`}>
              <Chip size="sm" variant="soft" color="warning">出口重复</Chip>
            </Hint>
          )}
          {p.same_as_default && (
            <Hint tip="出口 IP 和没绑代理的号一样（服务器自己的 IP），这个代理可能没起作用。">
              <Chip size="sm" variant="soft" color="danger">和服务器同 IP</Chip>
            </Hint>
          )}
          {p.exit_changed && (
            <Hint tip={`出口 IP 最近 24 小时变过${p.health.prev_exit_ip ? '（上一个 ' + p.health.prev_exit_ip + '）' : ''}${changed != null ? '，' + ago(p.health.exit_changed_at, now) + '变的' : ''}：动态 IP 的代理，绑在上面的号出口会跟着变。`}>
              <Chip size="sm" variant="soft">IP 变过</Chip>
            </Hint>
          )}
        </div>
      )}
    </div>
  )
}

function ExpiryCell({ p, now }: { p: ProxyItem; now: number }) {
  const t = timeMs(p.expires_at)
  if (t == null) return <span className="text-muted">—</span>
  return (
    <Hint tip={dateTime(t)} className="flex flex-col items-start gap-1">
      <span className="whitespace-nowrap tabular-nums">{fmtLocalDateTime(t)}</span>
      {p.expired ? <Chip size="sm" variant="soft" color="danger">已到期</Chip>
        : p.expiring ? <Chip size="sm" variant="soft" color="warning">{until(t, now)}到期</Chip>
        : <span className="text-xs text-muted">{until(t, now)}到期</span>}
    </Hint>
  )
}
