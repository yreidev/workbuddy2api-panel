// 用量：所选时间范围内的请求数与 token 用量（卡片、时序图、按账号 / 模型 / 域明细同一口径）。
import { useMemo, useState, type ReactNode } from 'react'
import { Chip, Table, ToggleButton, ToggleButtonGroup, type SortDescriptor } from '@heroui/react'
import { Bar, BarChart, CartesianGrid, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { fmtMs, fmtRate, fmtTok } from '../../lib/format'
import type { TipProps } from '../../lib/chart'
import { useUsage } from '../../lib/queries'
import { initialRange, rangeLabel } from '../../lib/timerange'
import type { KeyedUsage, UsagePoint, UsageResp } from '../../lib/types'
import { chartPoints, chartStats, dayBoundaries, minGap, nearestTicks, share, tickLabel, type ChartPoint } from '../../lib/usage'
import { BusyButton, Empty, Loaded } from '../../components/Feedback'
import { MixBar, Panel, Stat } from '../../components/Panel'
import { TimeRangePicker } from '../../components/TimeRangePicker'
import { CreditHistory } from './CreditHistory'

const pct = (v: number | null) => (v == null ? '—' : v.toFixed(1) + '%')
/** 服务端回显的时刻（本地时间 RFC3339）→ 「2026-09-30 09:00」 */
const echo = (iso: string) => iso.replace('T', ' ').slice(0, 16)

export function UsagePage() {
  const [range, setRange] = useState(() => initialRange('72'))
  const query = useUsage(range)
  const d = query.data
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
    </>
  )
}

function Totals({ u }: { u: UsageResp }) {
  const t = u.totals
  const reqs = t.requests || 0, errs = t.errors || 0
  const pp = share(t.prompt_tokens, t.total_tokens), cp = share(t.completion_tokens, t.total_tokens)
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-6">
      <Stat label="请求数" value={fmtTok(reqs)} sub={errs ? `其中失败 ${errs} 次` : '全部成功'} />
      <Stat label="总 token" value={fmtTok(t.total_tokens)} sub={`prompt ${pct(pp)} · completion ${pct(cp)}`}>
        <MixBar prompt={t.prompt_tokens} completion={t.completion_tokens} total={t.total_tokens} className="mt-1" />
      </Stat>
      <Stat label="prompt" value={fmtTok(t.prompt_tokens)} sub={'占比 ' + pct(pp)} />
      <Stat label="completion" value={fmtTok(t.completion_tokens)} sub={'占比 ' + pct(cp)} />
      <Stat
        label="失败尝试" value={errs} tone={errs ? 'warning' : undefined}
        sub={reqs ? '成功率 ' + pct((reqs - errs) / reqs * 100) : '—'}
      />
      <Stat label="平均延迟" value={fmtMs(t.avg_latency_ms)} sub={t.avg_tokens_per_second ? '吐字 ' + fmtRate(t.avg_tokens_per_second) : '无速率样本'} />
    </div>
  )
}

function ChartTip({ active, payload }: TipProps) {
  const p = payload?.[0]?.payload as ChartPoint | undefined
  if (!active || !p) return null
  return (
    <div className="rounded-xl border border-separator bg-overlay px-3 py-2 text-xs shadow-lg">
      <div className="mb-1 font-medium">{p.raw.replace('T', ' ') + (p.scope === 'hour' ? ':00' : '')}</div>
      <div className="flex items-center gap-2"><span className="size-2 rounded-sm bg-(--viz-1)" />prompt <b className="ml-auto pl-3 tabular-nums">{fmtTok(p.prompt)}</b></div>
      <div className="flex items-center gap-2"><span className="size-2 rounded-sm bg-(--viz-2)" />completion <b className="ml-auto pl-3 tabular-nums">{fmtTok(p.completion)}</b></div>
      <div className="mt-1 text-muted">{p.requests} 次请求</div>
    </div>
  )
}

function UsageChart({ series }: { series: UsagePoint[] }) {
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
        <span className="font-medium">Token 时序</span>
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
          <Bar dataKey="prompt" stackId="tok" fill="var(--viz-1)" stroke="var(--surface)" strokeWidth={seam} maxBarSize={30} isAnimationActive={false} />
          <Bar dataKey="completion" stackId="tok" fill="var(--viz-2)" stroke="var(--surface)" strokeWidth={seam} maxBarSize={30} radius={[4, 4, 0, 0]} isAnimationActive={false} />
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

type UsageKind = 'account' | 'model' | 'realm'
type NumKey = 'requests' | 'errors' | 'prompt_tokens' | 'completion_tokens' | 'total_tokens' | 'avg_latency_ms' | 'avg_tokens_per_second'

const KINDS: { id: UsageKind; label: string; key: 'by_account' | 'by_model' | 'by_realm' }[] = [
  { id: 'account', label: '按账号', key: 'by_account' },
  { id: 'model', label: '按模型', key: 'by_model' },
  { id: 'realm', label: '按域', key: 'by_realm' },
]

/** 按账号 / 模型 / 域三个维度共用一块，页内切换（零请求） */
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
            <ToggleButton key={k.id} id={k.id}>
              {i > 0 && <ToggleButtonGroup.Separator />}{k.label}
              <Chip size="sm" variant="soft" className="ml-1">{(data[k.key] || []).length}</Chip>
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

const NUM_COLS: { id: NumKey; label: string; fmt: (r: KeyedUsage) => ReactNode; perf?: boolean }[] = [
  { id: 'requests', label: '请求', fmt: (r) => fmtTok(r.requests) },
  { id: 'errors', label: '失败', fmt: (r) => (r.errors ? <span className="text-warning">{fmtTok(r.errors)}</span> : '—') },
  { id: 'prompt_tokens', label: 'Prompt', fmt: (r) => fmtTok(r.prompt_tokens) },
  { id: 'completion_tokens', label: 'Completion', fmt: (r) => fmtTok(r.completion_tokens) },
  {
    id: 'total_tokens', label: '合计',
    fmt: (r) => (
      <span className="inline-flex min-w-16 flex-col items-end gap-1">
        {fmtTok(r.total_tokens)}
        <MixBar prompt={r.prompt_tokens} completion={r.completion_tokens} total={r.total_tokens} />
      </span>
    ),
  },
  { id: 'avg_latency_ms', label: '均延迟', fmt: (r) => fmtMs(r.avg_latency_ms), perf: true },
  { id: 'avg_tokens_per_second', label: '均速率', fmt: (r) => fmtRate(r.avg_tokens_per_second), perf: true },
]

/** 明细表；数字列点表头排序，默认按合计 token 从大到小。账号行显示昵称（后端放在 extra 里），UID 做副标题 */
function UsageTable({ kind, rows }: { kind: UsageKind; rows: KeyedUsage[] }) {
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
            <Table.Column id="key" isRowHeader>{kind === 'account' ? '账号' : kind === 'model' ? '模型' : 'realm'}</Table.Column>
            {kind === 'account' ? <Table.Column id="realm">域</Table.Column> : null}
            {cols.map((c) => (
              <Table.Column key={c.id} id={c.id} allowsSorting className="text-right">
                {({ sortDirection }) => <Table.SortableColumnHeader sortDirection={sortDirection} className="justify-end gap-1">{c.label}</Table.SortableColumnHeader>}
              </Table.Column>
            ))}
          </Table.Header>
          <Table.Body>
            {sorted.map((r) => (
              <Table.Row key={r.key} id={r.key}>
                <Table.Cell>
                  {kind === 'account' ? (
                    <div className="flex flex-col">
                      <span className="font-medium">{r.extra || r.key.slice(0, 8)}</span>
                      {r.extra && <span className="font-mono text-xs text-muted">{r.key.slice(0, 8)}</span>}
                    </div>
                  ) : <span className="break-all">{r.key}</span>}
                </Table.Cell>
                {kind === 'account' ? <Table.Cell className="text-muted">{r.realm || '—'}</Table.Cell> : null}
                {cols.map((c) => <Table.Cell key={c.id} className="text-right font-mono tabular-nums">{c.fmt(r)}</Table.Cell>)}
              </Table.Row>
            ))}
          </Table.Body>
        </Table.Content>
      </Table.ScrollContainer>
    </Table>
  )
}
