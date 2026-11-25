// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 用量：所选时间范围内的请求数与 token 用量（卡片、时序图、按账号 / 模型 / 域 / 密钥明细同一口径）。
// 选了密钥时整页（含积分扣除）只算这个密钥的请求。
import { useMemo, useState, type ReactNode } from 'react'
import { ChartColumn, Pulse, ShieldExclamation, Thunderbolt } from '@gravity-ui/icons'
import { Chip, Label, ListBox, Select, Table, ToggleButton, ToggleButtonGroup, type SortDescriptor } from '@heroui/react'
import { useReducedMotion } from 'motion/react'
import { Bar, BarChart, CartesianGrid, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { fmtMs, fmtRate, fmtTok } from '../../lib/format'
import type { TipProps } from '../../lib/chart'
import { USAGE_KEY_ADMIN, USAGE_KEY_ALL, USAGE_KEY_NONE } from '../../lib/keys'
import { hasProviderData, useAccountNames, useKeys, useProviders, useUsage } from '../../lib/queries'
import { TARGETS, targetName } from '../../lib/targets'
import { initialRange, rangeLabel } from '../../lib/timerange'
import type { KeyedUsage, UsagePoint, UsageResp } from '../../lib/types'
import { chartPoints, chartStats, dayBoundaries, matchesUsageFilter, minGap, nearestTicks, share, tickLabel, usageTokenText, type ChartPoint } from '../../lib/usage'
import { ChartTooltipCard, ChartTooltipRow } from '../../components/ChartTooltip'
import { BusyButton, Empty, Loaded } from '../../components/Feedback'
import { MixBar, Panel, Stat } from '../../components/Panel'
import { TimeRangePicker } from '../../components/TimeRangePicker'
import { CreditHistory } from './CreditHistory'
import { TargetSelect } from '../../components/TargetSelect'
import { ProviderUsagePanel } from '../accounts/ProviderAccounts'

const pct = (v: number | null) => (v == null ? '—' : v.toFixed(1) + '%')
/** 服务端回显的时刻（本地时间 RFC3339）→ 「2026-09-30 09:00」 */
const echo = (iso: string) => iso.replace('T', ' ').slice(0, 16)

export function UsagePage() {
  const [range, setRange] = useState(() => initialRange('72'))
  const [key, setKey] = useState(USAGE_KEY_ALL)
  const [target, setTarget] = useState('')
  const providers = useProviders()
  const query = useUsage(range, key, target)
  const d = query.data
  const showTarget = !!target || hasProviderData(providers.data) || (d?.by_realm?.length ?? 0) > 1 || !!d?.by_realm?.some((r) => r.key.includes(':') && !r.key.startsWith('workbuddy:'))
  const providerUsage = useMemo(
    () => (providers.data?.usage ?? []).filter((u) => matchesUsageFilter(u, target, key)),
    [providers.data, target, key],
  )
  // 显式区间（今天 / 自定义）以服务端回显的实际区间为准；滚动窗口没有回显，用控件自己的标签
  const windowText = d?.window_from ? echo(d.window_from) + ' → ' + (d.window_to ? echo(d.window_to) : '现在') : rangeLabel(range)

  return (
    <>
      <Panel
        title="用量总览"
        desc={d && [
          windowText,
          d.buckets + ' 个分桶',
          d.since && '数据自 ' + d.since.replace('T', ' '),
          d.file_bytes && '文件 ' + (d.file_bytes / 1024).toFixed(1) + ' KB',
        ].filter(Boolean).join(' · ')}
        actions={
          <>
            {showTarget && <TargetSelect compact label="按上游筛选" value={target || 'all'} onChange={(v) => setTarget(v === 'all' ? '' : v)} targets={[{ id: 'all', name: '全部上游' }, ...TARGETS]} />}
            <KeyFilter value={key} onChange={setKey} showSources={hasProviderData(providers.data) || !!d?.by_key?.some((r) => r.key === 'liveness')} />
            <TimeRangePicker value={range} onChange={setRange} />
            <BusyButton size="sm" variant="secondary" busy={query.isFetching} onPress={() => void query.refetch()}>刷新</BusyButton>
          </>
        }
        contentClassName="flex flex-col gap-4"
      >
        <Loaded query={query} rows={4}>
          {(u) => (
            <>
              <Totals u={u} />
              {!!u.totals.unknown_tokens && <p className="text-sm text-warning">其中 {u.totals.unknown_tokens} 次尝试未返回完整 token；下列 token 合计与图表仅包含已知部分。</p>}
              <UsageChart series={u.series || []} />
            </>
          )}
        </Loaded>
      </Panel>

      {d && (
        <>
          <UsageBreakdown data={d} />
          <CreditHistory data={d} />
        </>
      )}
      {providerUsage.length > 0 && <ProviderUsagePanel usage={providerUsage} />}
    </>
  )
}

/** 按密钥筛：全部 / 管理员 / 各客户端密钥 / 未区分（没开鉴权或加密钥维度之前的数据） */
function KeyFilter({ value, onChange, showSources }: { value: string; onChange: (v: string) => void; showSources: boolean }) {
  const keys = useKeys()
  const list = keys.data?.keys || []
  // 纯 WorkBuddy 没有客户端密钥或测活用量时，沿用原来的精简工具栏。
  if (!list.length && !showSources && value === USAGE_KEY_ALL) return null
  const options = [
    { id: USAGE_KEY_ALL, label: '全部密钥' },
    { id: USAGE_KEY_ADMIN, label: '管理员' },
    { id: 'liveness', label: '测活' },
    ...list.map((k) => ({ id: k.id, label: k.name })),
    { id: USAGE_KEY_NONE, label: '未区分' },
  ]
  return (
    <Select aria-label="按密钥筛选" value={value} onChange={(k) => onChange(String(k ?? USAGE_KEY_ALL))} className="w-36">
      <Label className="sr-only">按密钥筛选</Label>
      <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
      <Select.Popover>
        <ListBox>
          {options.map((o) => <ListBox.Item key={o.id} id={o.id} textValue={o.label}>{o.label}<ListBox.ItemIndicator /></ListBox.Item>)}
        </ListBox>
      </Select.Popover>
    </Select>
  )
}

function Totals({ u }: { u: UsageResp }) {
  const t = u.totals
  const reqs = t.requests || 0, errs = t.errors || 0
  const pp = share(t.prompt_tokens, t.total_tokens), cp = share(t.completion_tokens, t.total_tokens)
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-6">
      <Stat label="请求数" value={fmtTok(reqs)} icon={<Pulse className="size-4" />} sub={errs ? `其中失败 ${errs} 次` : '全部成功'} />
      <Stat label="总 token" value={usageTokenText(t, t.total_tokens)} icon={<ChartColumn className="size-4" />} sub={`prompt ${pct(pp)} · completion ${pct(cp)}`}>
        <MixBar prompt={t.prompt_tokens} completion={t.completion_tokens} total={t.total_tokens} className="mt-1" />
      </Stat>
      <Stat label="prompt" value={usageTokenText(t, t.prompt_tokens)} sub={'占比 ' + pct(pp)} />
      <Stat label="completion" value={usageTokenText(t, t.completion_tokens)} sub={'占比 ' + pct(cp)} />
      <Stat
        label="失败尝试" value={errs} tone={errs ? 'warning' : undefined}
        icon={errs ? <ShieldExclamation className="size-4" /> : undefined}
        sub={reqs ? '成功率 ' + pct((reqs - errs) / reqs * 100) : '—'}
      />
      <Stat label="平均延迟" value={fmtMs(t.avg_latency_ms)} icon={<Thunderbolt className="size-4" />} sub={t.avg_tokens_per_second ? '吐字 ' + fmtRate(t.avg_tokens_per_second) : '无速率样本'} />
    </div>
  )
}

function ChartTip({ active, payload }: TipProps) {
  const p = payload?.[0]?.payload as ChartPoint | undefined
  if (!active || !p) return null
  return (
    <ChartTooltipCard
      title={p.raw.replace('T', ' ') + (p.scope === 'hour' ? ':00' : '')}
      footer={`${p.requests} 次请求`}
    >
      <ChartTooltipRow color="var(--viz-1)" label="prompt" value={fmtTok(p.prompt)} />
      <ChartTooltipRow color="var(--viz-2)" label="completion" value={fmtTok(p.completion)} />
    </ChartTooltipCard>
  )
}

function UsageChart({ series }: { series: UsagePoint[] }) {
  const reducedMotion = useReducedMotion()
  const pts = useMemo(() => chartPoints(series), [series])
  const byT = useMemo(() => new Map(pts.map((p) => [p.t, p])), [pts])
  const stats = chartStats(pts)
  if (!stats) return <Empty title="暂无用量数据">发起一次对话后再刷新。</Empty>
  const pad = minGap(pts) / 2
  const tickStyle = { fill: 'var(--muted)', fontSize: 11 }
  // 堆叠段之间描一道底色细缝；柱子多到只有一两像素宽（30 天小时桶）时不描，否则会把柱子吃掉
  const seam = pts.length <= 120 ? 1 : 0
  return (
    <div className="rounded-2xl border border-separator p-3">
      <div className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        <span className="font-medium">已知 Token 时序</span>
        <span className="text-muted tabular-nums">
          {pts.length} 个点 · 峰值 {fmtTok(stats.peak.total)} @ {tickLabel(stats.peak)} · 均值 {fmtTok(stats.avg)}
        </span>
        <span className="ml-auto flex items-center gap-1.5 text-muted"><span className="size-2.5 rounded-sm bg-(--viz-1)" />prompt</span>
        <span className="flex items-center gap-1.5 text-muted"><span className="size-2.5 rounded-sm bg-(--viz-2)" />completion</span>
      </div>
      <ResponsiveContainer width="100%" height={220}>
        <BarChart data={pts} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
          <CartesianGrid vertical={false} stroke="var(--separator)" strokeDasharray="2 3" />
          <XAxis
            dataKey="t" type="number" scale="time" domain={[pts[0].t - pad, pts[pts.length - 1].t + pad]}
            ticks={nearestTicks(pts)} tickFormatter={(t: number) => tickLabel(byT.get(t) ?? { t, scope: 'hour' })}
            tick={tickStyle} tickLine={false} axisLine={{ stroke: 'var(--border)' }}
          />
          <YAxis tickFormatter={(v: number) => fmtTok(v)} width={48} tick={tickStyle} tickLine={false} axisLine={false} />
          <Tooltip content={ChartTip} cursor={{ fill: 'var(--surface-secondary)' }} />
          {dayBoundaries(pts).map((x) => <ReferenceLine key={x} x={x - pad} stroke="var(--separator)" />)}
          <Bar dataKey="prompt" stackId="tok" fill="var(--viz-1)" stroke="var(--surface)" strokeWidth={seam} maxBarSize={30} isAnimationActive={!reducedMotion} animationDuration={420} />
          <Bar dataKey="completion" stackId="tok" fill="var(--viz-2)" stroke="var(--surface)" strokeWidth={seam} maxBarSize={30} radius={[4, 4, 0, 0]} isAnimationActive={!reducedMotion} animationDuration={420} />
          {/* 均值参考线：一眼看出哪根异常高；只有一个点（均值 = 峰值）时不画 */}
          {pts.length > 1 && (
            <ReferenceLine
              y={stats.avg} stroke="var(--muted)" strokeDasharray="4 4"
              label={{ value: '均值 ' + fmtTok(stats.avg), position: 'insideTopLeft', fill: 'var(--muted)', fontSize: 11 }}
            />
          )}
        </BarChart>
      </ResponsiveContainer>
    </div>
  )
}

type UsageKind = 'account' | 'model' | 'realm' | 'key'
type NumKey = 'requests' | 'errors' | 'prompt_tokens' | 'completion_tokens' | 'total_tokens' | 'avg_latency_ms' | 'avg_tokens_per_second'

const KINDS: { id: UsageKind; label: string; key: 'by_account' | 'by_model' | 'by_realm' | 'by_key' }[] = [
  { id: 'account', label: '按账号', key: 'by_account' },
  { id: 'model', label: '按模型', key: 'by_model' },
  { id: 'realm', label: '按上游', key: 'by_realm' },
  { id: 'key', label: '按密钥', key: 'by_key' },
]

/** 按账号 / 模型 / 域 / 密钥四个维度共用一块，页内切换（零请求） */
function UsageBreakdown({ data }: { data: UsageResp }) {
  const [kind, setKind] = useState<UsageKind>('account')
  const rows = data[KINDS.find((k) => k.id === kind)!.key] || []
  return (
    <Panel
      title="用量明细"
      desc={`${rows.length} 行 · 请求数含失败尝试`}
      actions={
        <ToggleButtonGroup aria-label="明细维度" size="sm" selectionMode="single" disallowEmptySelection
          selectedKeys={[kind]} onSelectionChange={(k) => setKind([...k][0] as UsageKind)}>
          {KINDS.map((k, i) => (
            <ToggleButton key={k.id} id={k.id} className="max-sm:h-auto max-sm:flex-col max-sm:gap-0.5 max-sm:px-2 max-sm:py-1">
              {i > 0 && <ToggleButtonGroup.Separator />}{k.label}
              <Chip size="sm" variant="soft" className="sm:ml-1">{(data[k.key] || []).length}</Chip>
            </ToggleButton>
          ))}
        </ToggleButtonGroup>
      }
    >
      {/* 换维度时换一张表：各自的排序状态互不串 */}
      <UsageTable key={kind} kind={kind} rows={rows} />
    </Panel>
  )
}

const KEY_COL: Record<UsageKind, string> = { account: '账号', model: '模型', realm: '上游', key: '密钥' }

const NUM_COLS: { id: NumKey; label: string; fmt: (r: KeyedUsage) => ReactNode; perf?: boolean }[] = [
  { id: 'requests', label: '请求', fmt: (r) => fmtTok(r.requests) },
  { id: 'errors', label: '失败', fmt: (r) => (r.errors ? <span className="text-warning">{fmtTok(r.errors)}</span> : '—') },
  { id: 'prompt_tokens', label: 'Prompt', fmt: (r) => usageTokenText(r, r.prompt_tokens) },
  { id: 'completion_tokens', label: 'Completion', fmt: (r) => usageTokenText(r, r.completion_tokens) },
  {
    id: 'total_tokens', label: '合计',
    fmt: (r) => (
      <span className="inline-flex min-w-16 flex-col items-end gap-1">
        {usageTokenText(r, r.total_tokens)}
        <MixBar prompt={r.prompt_tokens} completion={r.completion_tokens} total={r.total_tokens} />
      </span>
    ),
  },
  { id: 'avg_latency_ms', label: '均延迟', fmt: (r) => fmtMs(r.avg_latency_ms), perf: true },
  { id: 'avg_tokens_per_second', label: '均速率', fmt: (r) => fmtRate(r.avg_tokens_per_second), perf: true },
]

/**
 * 明细表；数字列点表头排序，默认按合计 token 从大到小。账号行显示昵称、密钥行显示密钥名（后端都放在 extra 里），
 * UID / 密钥 id 做副标题
 */
function UsageTable({ kind, rows }: { kind: UsageKind; rows: KeyedUsage[] }) {
  const names = useAccountNames()
  const [sort, setSort] = useState<SortDescriptor>({ column: 'total_tokens', direction: 'descending' })
  const cols = NUM_COLS.filter((c) => !c.perf || kind === 'account')
  const sorted = useMemo(() => {
    const k = sort.column as NumKey
    const out = [...rows].sort((a, b) => (a[k] || 0) - (b[k] || 0))
    return sort.direction === 'descending' ? out.reverse() : out
  }, [rows, sort])
  if (!rows.length) return <Empty title="暂无数据" />
  const label = KINDS.find((k) => k.id === kind)!.label
  return (
    <Table variant="secondary">
      <Table.ScrollContainer>
        <Table.Content aria-label={label} className="min-w-[640px]" sortDescriptor={sort} onSortChange={setSort}>
          <Table.Header>
            <Table.Column id="key" isRowHeader>{KEY_COL[kind]}</Table.Column>
            {kind === 'account' ? <Table.Column id="realm">上游</Table.Column> : null}
            {cols.map((c) => (
              <Table.Column key={c.id} id={c.id} allowsSorting className="text-right">
                {({ sortDirection }) => <Table.SortableColumnHeader sortDirection={sortDirection} className="justify-end gap-1">{c.label}</Table.SortableColumnHeader>}
              </Table.Column>
            ))}
          </Table.Header>
          <Table.Body>
            {sorted.map((r) => (
              <Table.Row key={r.key || '-'} id={r.key || '-'}>
                <Table.Cell>
                  {kind === 'account' ? (
                    <div className="flex flex-col">
                      <span className="font-medium">{names.name(r.key, r.extra)}</span>
                      {names.name(r.key, r.extra) !== r.key.slice(0, 8) && <span className="font-mono text-xs text-muted">{r.key.slice(0, 8)}</span>}
                    </div>
                  ) : kind === 'key' ? (
                    <div className="flex flex-col">
                      <span className="font-medium">{r.extra || r.key || '未区分'}</span>
                      {/^k[0-9a-f]+$/.test(r.key) && <span className="font-mono text-xs text-muted">{r.key}</span>}
                    </div>
                  ) : <span className="break-all">{kind === 'realm' ? targetName(r.key.includes(':') ? r.key : 'workbuddy:' + r.key) : r.key}</span>}
                </Table.Cell>
                {kind === 'account' ? <Table.Cell className="text-muted">{(r.realms ?? (r.realm ? [r.realm] : [])).map((v) => targetName(v.includes(':') ? v : 'workbuddy:' + v)).join('、') || '—'}</Table.Cell> : null}
                {cols.map((c) => <Table.Cell key={c.id} className="text-right font-mono tabular-nums">{c.fmt(r)}</Table.Cell>)}
              </Table.Row>
            ))}
          </Table.Body>
        </Table.Content>
      </Table.ScrollContainer>
    </Table>
  )
}
