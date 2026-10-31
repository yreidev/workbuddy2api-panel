// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 积分获取：签到、成长任务领奖、积分补偿、猫猫旅行、新手礼包、连登兑换领到的积分逐笔记录（日志留存里的「积分获取」）。
// 时间范围、账号、来源在网关侧筛；合计、按来源、按账号算的是区间内符合条件的全部笔数，流水只列最新的若干笔。
// 只记领到的，扣除见用量页的「积分扣除历史」；升级到有这一页的版本之前领的没有记录。
import { useMemo, useState } from 'react'
import { Chip, Label, ListBox, Select, Table, useMediaQuery } from '@heroui/react'
import { accountName } from '../../lib/accounts'
import { EARN_LABEL, earnDetail, earnLabel } from '../../lib/credits'
import { dayClock } from '../../lib/format'
import { useAccountNames, useCreditLog, useLogStorage, useOverview } from '../../lib/queries'
import { retentionText, storageProblem, storageSummary } from '../../lib/storage'
import { initialRange } from '../../lib/timerange'
import type { CreditEarning, CreditLogResp, CreditSum } from '../../lib/types'
import { BusyButton, Empty, Loaded } from '../../components/Feedback'
import { Panel, Stat } from '../../components/Panel'
import { TimeRangePicker } from '../../components/TimeRangePicker'

const LIMITS = [100, 500, 2000]
const SOURCES = [{ id: '', label: '全部来源' }, ...Object.entries(EARN_LABEL).map(([id, label]) => ({ id, label }))]
const plus = (n: number) => '+' + n.toLocaleString('zh-CN')

/** 筛选用的下拉框 */
function Pick<T extends string | number>({ label, value, options, onChange, className }: {
  label: string
  value: T
  options: { id: T; label: string }[]
  onChange: (v: T) => void
  className: string
}) {
  return (
    <Select aria-label={label} value={value} onChange={(k) => k != null && onChange(k as T)} className={className}>
      <Label className="sr-only">{label}</Label>
      <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
      <Select.Popover>
        <ListBox>
          {options.map((o) => <ListBox.Item key={o.id} id={o.id} textValue={o.label}>{o.label}<ListBox.ItemIndicator /></ListBox.Item>)}
        </ListBox>
      </Select.Popover>
    </Select>
  )
}

export function CreditLogPage() {
  const overview = useOverview()
  const storage = useLogStorage(true)
  const [range, setRange] = useState(() => initialRange('720'))
  const [uid, setUid] = useState('')
  const [source, setSource] = useState('')
  const [limit, setLimit] = useState(500)
  const query = useCreditLog(range, uid, source, limit)
  const d = query.data
  const accounts = useMemo(() => [
    { id: '', label: '全部账号' },
    ...(overview.data?.accounts || []).map((a) => ({ id: a.uid, label: accountName(a) })).sort((a, b) => a.label.localeCompare(b.label, 'zh-CN')),
  ], [overview.data])
  const st = storage.data?.credit

  return (
    <>
      <Panel
        title="积分获取"
        desc={['只记领到的积分，扣除见「用量 → 积分扣除历史」', st && retentionText(st.retention_days), st && storageSummary('credit', st), storageProblem(st)]
          .filter(Boolean).join(' · ')}
        actions={
          <>
            <Pick label="按账号筛选" value={uid} options={accounts} onChange={setUid} className="w-40" />
            <Pick label="按来源筛选" value={source} options={SOURCES} onChange={setSource} className="w-32" />
            <TimeRangePicker value={range} onChange={setRange} />
            <Pick label="流水条数" value={limit} options={LIMITS.map((n) => ({ id: n, label: `最近 ${n} 笔` }))} onChange={setLimit} className="w-36" />
            <BusyButton size="sm" variant="secondary" busy={query.isFetching} onPress={() => void query.refetch()}>重新读取</BusyButton>
          </>
        }
        contentClassName="flex flex-col gap-4"
      >
        {st?.enabled === false && (
          <p className="text-sm text-warning">积分获取的留存已关闭，新领到的不再记录，这里只查得到关闭前留下的；在「配置 → 日志留存」里打开。</p>
        )}
        <Loaded query={query} rows={2}>
          {(r) => r.count ? <Totals d={r} /> : (
            <Empty title="这段时间没有领到积分">签到、领奖等领到积分时逐笔记录；升级到有这一页的版本之前领的没有记录。</Empty>
          )}
        </Loaded>
      </Panel>
      {!!d?.count && (
        <>
          <ByAccount rows={d.by_account} />
          <Entries d={d} />
        </>
      )}
    </>
  )
}

function Totals({ d }: { d: CreditLogResp }) {
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-4 xl:grid-cols-7">
      <Stat label="领到积分" value={plus(d.total)} tone="success" sub={`${d.count} 笔 · ${d.by_account.length} 个账号`} />
      {d.by_source.map((s) => <Stat key={s.key} label={earnLabel(s.key)} value={plus(s.credit)} sub={`${s.count} 笔`} />)}
    </div>
  )
}

function AccountCell({ uid, nickname, realm }: { uid: string; nickname?: string; realm?: string }) {
  const names = useAccountNames()
  return (
    <div className="flex flex-col">
      <span className="font-medium">{names.name(uid, nickname)}</span>
      <span className="font-mono text-xs text-muted">{[realm, uid.slice(0, 8)].filter(Boolean).join(' · ')}</span>
    </div>
  )
}

function ByAccount({ rows }: { rows: CreditSum[] }) {
  return (
    <Panel title="按账号" desc={`${rows.length} 个账号，按领到的积分从多到少`}>
      <Table variant="secondary">
        <Table.ScrollContainer className="max-h-[50dvh] overflow-y-auto">
          <Table.Content aria-label="积分获取按账号" className="min-w-[480px]">
            <Table.Header className="sticky top-0 z-10 bg-surface">
              <Table.Column isRowHeader>账号</Table.Column>
              <Table.Column className="text-right">领到积分</Table.Column>
              <Table.Column className="text-right">笔数</Table.Column>
            </Table.Header>
            <Table.Body>
              {rows.map((r) => (
                <Table.Row key={r.key} id={r.key}>
                  <Table.Cell><AccountCell uid={r.key} nickname={r.nickname} realm={r.realm} /></Table.Cell>
                  <Table.Cell className="text-right font-mono tabular-nums text-success">{plus(r.credit)}</Table.Cell>
                  <Table.Cell className="text-right font-mono tabular-nums">{r.count}</Table.Cell>
                </Table.Row>
              ))}
            </Table.Body>
          </Table.Content>
        </Table.ScrollContainer>
      </Table>
    </Panel>
  )
}

const clock = (e: CreditEarning) => dayClock(e.t) || '—'
const rowKey = (e: CreditEarning, i: number) => e.t + e.uid + e.source + i

function Entries({ d }: { d: CreditLogResp }) {
  const names = useAccountNames()
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  return (
    <Panel title="流水" desc={d.more ? `新的在前，只列最新 ${d.entries.length} 笔；合计按区间内全部 ${d.count} 笔算` : `新的在前，共 ${d.count} 笔`}>
      {isDesktop ? (
        <Table variant="secondary">
          <Table.ScrollContainer className="max-h-[70dvh] overflow-y-auto">
            <Table.Content aria-label="积分获取流水" className="min-w-[720px]">
              <Table.Header className="sticky top-0 z-10 bg-surface">
                <Table.Column isRowHeader>时间</Table.Column>
                <Table.Column>账号</Table.Column>
                <Table.Column>来源</Table.Column>
                <Table.Column>说明</Table.Column>
                <Table.Column className="text-right">积分</Table.Column>
              </Table.Header>
              <Table.Body>
                {d.entries.map((e, i) => (
                  <Table.Row key={rowKey(e, i)} id={rowKey(e, i)}>
                    <Table.Cell className="whitespace-nowrap font-mono tabular-nums">{clock(e)}</Table.Cell>
                    <Table.Cell><AccountCell uid={e.uid} nickname={e.nickname} realm={e.realm} /></Table.Cell>
                    <Table.Cell><Chip size="sm" variant="soft">{earnLabel(e.source)}</Chip></Table.Cell>
                    <Table.Cell className="break-all">{earnDetail(e) || <span className="text-muted">—</span>}</Table.Cell>
                    <Table.Cell className="text-right font-mono tabular-nums text-success">{plus(e.credit)}</Table.Cell>
                  </Table.Row>
                ))}
              </Table.Body>
            </Table.Content>
          </Table.ScrollContainer>
        </Table>
      ) : (
        <ul className="flex max-h-[70dvh] flex-col divide-y divide-separator overflow-y-auto">
          {d.entries.map((e, i) => (
            <li key={rowKey(e, i)} className="flex flex-col gap-1 py-2.5 text-sm">
              <div className="flex items-center justify-between gap-2">
                <span className="font-mono text-xs text-muted">{clock(e)}</span>
                <span className="font-mono tabular-nums text-success">{plus(e.credit)}</span>
              </div>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                <Chip size="sm" variant="soft">{earnLabel(e.source)}</Chip>
                <span>{names.name(e.uid, e.nickname)}</span>
                {earnDetail(e) && <span className="break-all text-muted">{earnDetail(e)}</span>}
              </div>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}
