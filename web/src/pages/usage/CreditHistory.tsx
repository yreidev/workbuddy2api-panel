// 积分扣除历史：按所选时间窗口统计上游返回的 usage.credit，按账号、按模型（带请求时的积分倍率）两个维度。
// 只统计明确返回了积分的请求；升级前只有 token 的历史不会伪造积分，所以「积分 / 1M Token」只用同时
// 有积分和 token 的样本算。
import { useMemo, useState, type ReactNode } from 'react'
import { Table, type SortDescriptor } from '@heroui/react'
import { fmtCredit, fmtCreditRatio, fmtModelRate, fmtTok } from '../../lib/format'
import type { CreditUsage, UsageResp } from '../../lib/types'
import { Empty } from '../../components/Feedback'
import { Panel, Stat } from '../../components/Panel'

type NumKey = 'requests' | 'credits' | 'credit_tokens' | 'credits_per_1m_tokens'

const ratio = (r: CreditUsage) => fmtCreditRatio(r.credits_per_1m_tokens, r.credit_samples, r.credit_tokens)

const NUM_COLS: { id: NumKey; label: string; fmt: (r: CreditUsage) => ReactNode }[] = [
  { id: 'requests', label: '请求', fmt: (r) => fmtTok(r.requests) },
  { id: 'credits', label: '扣除积分', fmt: (r) => fmtCredit(r.credits) },
  { id: 'credit_tokens', label: '有效样本 Token', fmt: (r) => fmtTok(r.credit_tokens) },
  { id: 'credits_per_1m_tokens', label: '积分 / 1M Token', fmt: ratio },
]

const EMPTY_LINE = '暂无积分扣除记录；升级前只有 Token 的历史不会伪造积分。'

export function CreditHistory({ data }: { data: UsageResp }) {
  const t = data.totals
  const accounts = data.credit_by_account || []
  const models = data.credit_by_model || []
  return (
    <Panel
      title="积分扣除历史"
      desc={`${accounts.length} 个账号 · ${models.length} 个模型倍率分组 · 只统计与积分同时观测到的 Token`}
      contentClassName="flex flex-col gap-4"
    >
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="扣除积分" value={fmtCredit(t.credits)} />
        <Stat label="匹配 Token" value={fmtTok(t.credit_tokens)} />
        <Stat label="平均积分 / 1M Token" value={fmtCreditRatio(t.credits_per_1m_tokens, t.credit_samples, t.credit_tokens)} />
        <Stat label="有效积分样本" value={t.credit_samples || 0} />
      </div>
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">按账号</h3>
        <CreditTable kind="account" rows={accounts} />
      </section>
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">按模型</h3>
        <CreditTable kind="model" rows={models} />
      </section>
    </Panel>
  )
}

function CreditTable({ kind, rows }: { kind: 'account' | 'model'; rows: CreditUsage[] }) {
  const [sort, setSort] = useState<SortDescriptor | undefined>()
  const sorted = useMemo(() => {
    if (!sort) return rows
    const k = sort.column as NumKey
    const out = [...rows].sort((a, b) => (a[k] || 0) - (b[k] || 0))
    return sort.direction === 'descending' ? out.reverse() : out
  }, [rows, sort])
  if (!rows.length) return <Empty title="暂无数据">{EMPTY_LINE}</Empty>
  return (
    <Table variant="secondary">
      <Table.ScrollContainer>
        <Table.Content aria-label={kind === 'account' ? '积分扣除按账号' : '积分扣除按模型'} className="min-w-[640px]" sortDescriptor={sort} onSortChange={setSort}>
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
