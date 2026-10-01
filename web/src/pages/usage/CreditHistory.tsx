// 积分扣除历史：按所选时间范围统计上游返回的 usage.credit，按账号、按模型（带请求时的积分倍率）两个维度页内切换。
// 只统计明确返回了积分的请求；升级前只有 token 的历史不会伪造积分，所以「积分 / 1M Token」只用同时
// 有积分和 token 的样本算。缓存命中率 = 上游前缀缓存命中 / (命中 + 未命中)，低命中意味着费用成倍放大。
import { useMemo, useState, type ReactNode } from 'react'
import { Chip, Table, ToggleButton, ToggleButtonGroup, type SortDescriptor } from '@heroui/react'
import { fmtCredit, fmtCreditRatio, fmtModelRate, fmtTok } from '../../lib/format'
import { cacheLevel, cacheRate, cacheRateText } from '../../lib/requests'
import type { CreditUsage, UsageResp } from '../../lib/types'
import { Empty } from '../../components/Feedback'
import { Panel, Stat } from '../../components/Panel'

type Kind = 'account' | 'model'

const LEVEL_TONE = { ok: 'success', warn: 'warning', error: 'danger' } as const
const LEVEL_TEXT = { ok: 'text-success', warn: 'text-warning', error: 'text-danger' } as const

const ratio = (r: CreditUsage) => fmtCreditRatio(r.credits_per_1m_tokens, r.credit_samples, r.credit_tokens)

function CacheCell({ r }: { r: CreditUsage }) {
  const rate = cacheRate(r.cache_hit_tokens, r.cache_miss_tokens)
  if (rate == null) return <span className="text-muted">—</span>
  return (
    <span className="inline-flex flex-col items-end">
      <span className={LEVEL_TEXT[cacheLevel(rate)]}>{cacheRateText(r.cache_hit_tokens, r.cache_miss_tokens)}</span>
      <span className="whitespace-nowrap text-xs text-muted">命中 {fmtTok(r.cache_hit_tokens)} / 未命中 {fmtTok(r.cache_miss_tokens)}</span>
    </span>
  )
}

const NUM_COLS: { id: string; label: string; val: (r: CreditUsage) => number; fmt: (r: CreditUsage) => ReactNode }[] = [
  { id: 'requests', label: '请求', val: (r) => r.requests, fmt: (r) => fmtTok(r.requests) },
  { id: 'credits', label: '扣除积分', val: (r) => r.credits, fmt: (r) => fmtCredit(r.credits) },
  { id: 'credit_tokens', label: '有效样本 Token', val: (r) => r.credit_tokens, fmt: (r) => fmtTok(r.credit_tokens) },
  { id: 'credits_per_1m_tokens', label: '积分 / 1M Token', val: (r) => r.credits_per_1m_tokens, fmt: ratio },
  // 没有样本的行排在有样本的后面（-1），不和「命中 0%」混在一起
  { id: 'cache', label: '缓存命中率', val: (r) => cacheRate(r.cache_hit_tokens, r.cache_miss_tokens) ?? -1, fmt: (r) => <CacheCell r={r} /> },
]

const EMPTY_LINE = '暂无积分扣除记录；升级前只有 Token 的历史不会伪造积分。'

export function CreditHistory({ data }: { data: UsageResp }) {
  const [kind, setKind] = useState<Kind>('account')
  const t = data.totals
  const accounts = data.credit_by_account || []
  const models = data.credit_by_model || []
  const rate = cacheRate(t.cache_hit_tokens, t.cache_miss_tokens)
  return (
    <Panel
      title="积分扣除历史"
      desc={`${accounts.length} 个账号 · ${models.length} 个模型倍率分组 · 只统计与积分同时观测到的 Token`}
      actions={
        <ToggleButtonGroup aria-label="积分扣除维度" size="sm" selectionMode="single" disallowEmptySelection
          selectedKeys={[kind]} onSelectionChange={(k) => setKind([...k][0] as Kind)}>
          <ToggleButton id="account">按账号<Chip size="sm" variant="soft" className="ml-1">{accounts.length}</Chip></ToggleButton>
          <ToggleButton id="model"><ToggleButtonGroup.Separator />按模型<Chip size="sm" variant="soft" className="ml-1">{models.length}</Chip></ToggleButton>
        </ToggleButtonGroup>
      }
      contentClassName="flex flex-col gap-4"
    >
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-5">
        <Stat label="扣除积分" value={fmtCredit(t.credits)} sub="按上游 usage.credit 累计" />
        <Stat label="匹配 Token" value={fmtTok(t.credit_tokens)} sub="与积分同时观测到的 Token" />
        <Stat label="平均积分 / 1M Token" value={fmtCreditRatio(t.credits_per_1m_tokens, t.credit_samples, t.credit_tokens)} sub="越低越划算" />
        <Stat label="有效积分样本" value={t.credit_samples || 0} sub="缺字段的历史不参与折算" />
        <Stat
          label="缓存命中率" value={cacheRateText(t.cache_hit_tokens, t.cache_miss_tokens)}
          tone={rate == null ? undefined : LEVEL_TONE[cacheLevel(rate)]}
          className="col-span-2 lg:col-span-1"
          sub="上游前缀缓存命中 / (命中 + 未命中)；低命中意味着费用成倍放大"
        />
      </div>
      <CreditTable key={kind} kind={kind} rows={kind === 'account' ? accounts : models} />
    </Panel>
  )
}

function CreditTable({ kind, rows }: { kind: Kind; rows: CreditUsage[] }) {
  const [sort, setSort] = useState<SortDescriptor | undefined>()
  const sorted = useMemo(() => {
    const col = NUM_COLS.find((c) => c.id === sort?.column)
    if (!sort || !col) return rows
    const out = [...rows].sort((a, b) => (col.val(a) || 0) - (col.val(b) || 0))
    return sort.direction === 'descending' ? out.reverse() : out
  }, [rows, sort])
  if (!rows.length) return <Empty title="暂无数据">{EMPTY_LINE}</Empty>
  return (
    <Table variant="secondary">
      <Table.ScrollContainer>
        <Table.Content aria-label={kind === 'account' ? '积分扣除按账号' : '积分扣除按模型'} className="min-w-[720px]" sortDescriptor={sort} onSortChange={setSort}>
          <Table.Header>
            <Table.Column id="key" isRowHeader>{kind === 'account' ? '账号' : '模型'}</Table.Column>
            {kind === 'model' ? <Table.Column id="rate">积分倍率</Table.Column> : null}
            {NUM_COLS.map((c) => (
              <Table.Column key={c.id} id={c.id} allowsSorting className="text-right">
                {({ sortDirection }) => <Table.SortableColumnHeader sortDirection={sortDirection} className="justify-end gap-1">{c.label}</Table.SortableColumnHeader>}
              </Table.Column>
            ))}
          </Table.Header>
          <Table.Body>
            {sorted.map((r) => (
              // 按模型的行：同一模型不同倍率各占一行，键要带上倍率
              <Table.Row key={r.key + '|' + (r.rate || '')} id={r.key + '|' + (r.rate || '')}>
                <Table.Cell>
                  {kind === 'account' ? (
                    <div className="flex flex-col">
                      <span className="font-medium">{r.nickname || r.key.slice(0, 8) || '—'}</span>
                      <span className="font-mono text-xs text-muted">{[r.realm, r.key.slice(0, 8)].filter(Boolean).join(' · ')}</span>
                    </div>
                  ) : <span className="break-all">{r.key || '—'}</span>}
                </Table.Cell>
                {kind === 'model' ? <Table.Cell className="font-mono tabular-nums">{fmtModelRate(r.rate)}</Table.Cell> : null}
                {NUM_COLS.map((c) => <Table.Cell key={c.id} className="text-right font-mono tabular-nums">{c.fmt(r)}</Table.Cell>)}
              </Table.Row>
            ))}
          </Table.Body>
        </Table.Content>
      </Table.ScrollContainer>
    </Table>
  )
}
