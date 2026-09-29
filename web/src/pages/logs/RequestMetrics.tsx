// 请求指标：完成数、成功率、HTTP 成功率、平均耗时、进行中，以及最近的请求（最新的在前）。
// 只展示请求元数据（时间、状态、模型、账号、耗时、token、积分、请求 ID），不含提示词与响应正文。
import { Chip, Table, useMediaQuery } from '@heroui/react'
import { fmtBytes, fmtMs, fmtTok } from '../../lib/format'
import { useRequestMetrics } from '../../lib/queries'
import { OUTCOME_LABEL, outcomeLevel, requestCredit, requestLogText, requestTokens } from '../../lib/requests'
import type { RequestEvent, RequestMetrics as Metrics } from '../../lib/types'
import { Empty, Loaded } from '../../components/Feedback'
import { Panel } from '../../components/Panel'

const LEVEL_COLOR = { ok: 'success', warn: 'warning', error: 'danger' } as const

function summary(m: Metrics | null): string {
  if (!m) return '当前网关没有请求指标接口'
  const pct = (v?: number) => (v == null ? '—' : Number(v).toFixed(1) + '%')
  return `已完成 ${fmtTok(m.completed)} · 成功 ${pct(m.success_rate)} · HTTP ${pct(m.http_success_rate)} · 平均 ${fmtMs(m.avg_duration_ms)} · 进行中 ${m.in_flight || 0}`
}

function archiveNote(m: Metrics | null): string {
  const a = m?.archive
  if (!a?.enabled) return '仅内存指标，JSONL 归档已关闭'
  return 'JSONL 归档 ' + fmtBytes(a.bytes) + (a.dropped_writes ? ' · 丢弃 ' + a.dropped_writes + ' 条' : '') + (a.last_error ? ' · 错误：' + a.last_error : '')
}

const clock = (e: RequestEvent) => (e.time ? new Date(e.time).toLocaleTimeString('zh-CN', { hour12: false }) : '—')

function Outcome({ e }: { e: RequestEvent }) {
  return (
    <Chip size="sm" variant="soft" color={LEVEL_COLOR[outcomeLevel(e.outcome)]}>
      {e.status || '—'} {OUTCOME_LABEL[e.outcome] || e.outcome || '—'}
    </Chip>
  )
}

export function RequestMetrics() {
  const query = useRequestMetrics(true)
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const m = query.data?.metrics ?? null
  return (
    <Panel
      title="请求指标"
      desc={query.data ? <>{summary(m)}<br />{archiveNote(m)}</> : undefined}
    >
      <Loaded query={query} rows={4}>
        {({ recent }) => recent.length === 0 ? <Empty title="暂无请求记录" /> : isDesktop ? (
          <Table variant="secondary">
            <Table.ScrollContainer className="max-h-96 overflow-y-auto">
              <Table.Content aria-label="最近请求" className="min-w-[900px]">
                <Table.Header className="sticky top-0 z-10 bg-surface">
                  <Table.Column isRowHeader>时间</Table.Column>
                  <Table.Column>结果</Table.Column>
                  <Table.Column>模型</Table.Column>
                  <Table.Column>账号</Table.Column>
                  <Table.Column className="text-right">耗时</Table.Column>
                  <Table.Column className="text-right">Token</Table.Column>
                  <Table.Column className="text-right">积分</Table.Column>
                  <Table.Column>请求 ID</Table.Column>
                </Table.Header>
                <Table.Body>
                  {recent.map((e, i) => (
                    <Table.Row key={(e.request_id || '') + i} id={(e.request_id || '') + i}>
                      <Table.Cell className="whitespace-nowrap font-mono tabular-nums">{clock(e)}</Table.Cell>
                      <Table.Cell><Outcome e={e} /></Table.Cell>
                      <Table.Cell className="font-mono">{e.model || '—'}</Table.Cell>
                      <Table.Cell>{e.account || '—'}</Table.Cell>
                      <Table.Cell className="text-right font-mono tabular-nums">{fmtMs(e.duration_ms)}</Table.Cell>
                      <Table.Cell className="text-right font-mono tabular-nums">{fmtTok(requestTokens(e))}</Table.Cell>
                      <Table.Cell className="whitespace-nowrap text-right font-mono tabular-nums">{e.credit_known ? requestCredit(e).replace(' credit', '') : '—'}</Table.Cell>
                      <Table.Cell><span className="select-all font-mono text-xs text-muted">{e.request_id || '—'}</span></Table.Cell>
                    </Table.Row>
                  ))}
                </Table.Body>
              </Table.Content>
            </Table.ScrollContainer>
          </Table>
        ) : (
          <ul className="flex max-h-[60dvh] flex-col divide-y divide-separator overflow-y-auto">
            {recent.map((e, i) => (
              <li key={(e.request_id || '') + i} className="flex flex-col gap-1 py-2.5 text-sm" aria-label={requestLogText(e)}>
                <div className="flex items-center justify-between gap-2">
                  <span className="font-mono text-xs text-muted">{clock(e)}</span>
                  <Outcome e={e} />
                </div>
                <div className="flex flex-wrap gap-x-3 gap-y-0.5">
                  <span className="font-mono">{e.model || '—'}</span>
                  <span className="text-muted">{e.account || '—'}</span>
                </div>
                <div className="flex flex-wrap gap-x-3 text-xs text-muted tabular-nums">
                  <span>{fmtMs(e.duration_ms)}</span>
                  <span>{fmtTok(requestTokens(e))} tok</span>
                  <span>{requestCredit(e)}</span>
                </div>
                {e.request_id && <span className="select-all break-all font-mono text-xs text-muted">{e.request_id}</span>}
              </li>
            ))}
          </ul>
        )}
      </Loaded>
    </Panel>
  )
}
