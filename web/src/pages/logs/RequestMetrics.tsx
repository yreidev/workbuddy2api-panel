// 请求记录：完成数、成功率、HTTP 成功率、平均耗时、进行中，以及请求明细（最新的在前）。
// 只展示请求元数据（时间、状态、模型、账号、调用来源、耗时、token、积分、请求 ID），不含提示词与响应正文。
// 归档开着时按时间范围与条数从 JSONL 归档读；关键字与结果筛选只在已读到的条目上做，不发新请求。
import { useMemo, useState } from 'react'
import { Chip, Label, ListBox, SearchField, Select, Table, useMediaQuery } from '@heroui/react'
import { fmtBytes, fmtMs, fmtTok } from '../../lib/format'
import { useRequestMetrics } from '../../lib/queries'
import {
  OUTCOME_LABEL, cacheRateText, matchRequest, outcomeLevel, requestCredit, requestLogText, requestTokens,
} from '../../lib/requests'
import { initialRange } from '../../lib/timerange'
import type { RequestEvent, RequestMetrics as Metrics } from '../../lib/types'
import { BusyButton, Empty, ErrorBox, Loaded } from '../../components/Feedback'
import { InfoTip, Panel } from '../../components/Panel'
import { TimeRangePicker } from '../../components/TimeRangePicker'

const LEVEL_COLOR = { ok: 'success', warn: 'warning', error: 'danger' } as const
const OUTCOMES = [{ id: '', label: '全部结果' }, ...Object.entries(OUTCOME_LABEL).map(([id, label]) => ({ id, label }))]
const LIMITS = [100, 300, 1000]

function summary(m: Metrics | null): string {
  if (!m) return '当前网关没有请求指标接口'
  const pct = (v?: number) => (v == null ? '—' : Number(v).toFixed(1) + '%')
  return `已完成 ${fmtTok(m.completed)} · 成功 ${pct(m.success_rate)} · HTTP ${pct(m.http_success_rate)} · 平均 ${fmtMs(m.avg_duration_ms)} · 进行中 ${m.in_flight || 0}`
}

function archiveNote(m: Metrics | null): string {
  const a = m?.archive
  if (!a?.enabled) return '仅内存指标（最近 100 条），JSONL 归档已关闭'
  return 'JSONL 归档 ' + fmtBytes(a.bytes) + (a.dropped_writes ? ' · 丢弃 ' + a.dropped_writes + ' 条' : '') + (a.last_error ? ' · 错误：' + a.last_error : '')
}

/** 今天的只显示时刻，更早的带上日期（时间范围可以跨好几天） */
function clock(e: RequestEvent): string {
  if (!e.time) return '—'
  const d = new Date(e.time)
  const t = d.toLocaleTimeString('zh-CN', { hour12: false })
  return d.toDateString() === new Date().toDateString() ? t : d.getMonth() + 1 + '-' + String(d.getDate()).padStart(2, '0') + ' ' + t
}

function Outcome({ e }: { e: RequestEvent }) {
  return (
    <Chip size="sm" variant="soft" color={LEVEL_COLOR[outcomeLevel(e.outcome)]}>
      {e.status || '—'} {OUTCOME_LABEL[e.outcome] || e.outcome || '—'}
    </Chip>
  )
}

/** Token 数；上游返回了前缀缓存时下面带一行命中率 */
function Tokens({ e }: { e: RequestEvent }) {
  const cache = cacheRateText(e.cache_hit_tokens, e.cache_miss_tokens)
  return (
    <span className="inline-flex flex-col items-end">
      {fmtTok(requestTokens(e))}
      {cache !== '—' && <span className="whitespace-nowrap text-xs text-muted">命中 {cache}</span>}
    </span>
  )
}

const dash = <span className="text-muted">—</span>

export function RequestMetrics() {
  const [range, setRange] = useState(() => initialRange('0')) // 默认全部历史：打开页面就是「最近 N 条」
  const [limit, setLimit] = useState(100)
  const [q, setQ] = useState('')
  const [outcome, setOutcome] = useState('')
  const query = useRequestMetrics(true, range, limit)
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const d = query.data
  const m = d?.metrics ?? null
  const all = useMemo(() => d?.recent ?? [], [d])
  const shown = useMemo(() => all.filter((e) => matchRequest(e, { q, outcome })), [all, q, outcome])
  // 归档里早于「调用来源」功能的条目没有来源字段：提示一句，免得以为筛选坏了
  const hasSource = all.some((e) => e.client_ip || e.user_agent)
  const count = !all.length ? '' : (shown.length !== all.length ? `命中 ${shown.length} / ${all.length} 条` : `${all.length} 条`) + (hasSource ? '' : ' · 来源未记录')

  return (
    <Panel
      title={
        <span className="flex items-center gap-1">
          请求记录
          <InfoTip label="调用来源说明">
            来源 IP 取 X-Forwarded-For 首段，其次 X-Real-IP，没有代理头时取 TCP 对端；User-Agent 最多保留 200 字节。
            配置项 logging.request_client_info 关闭后不再记录来源。
          </InfoTip>
        </span>
      }
      desc={d ? <>{summary(m)}<br />{archiveNote(m)}</> : undefined}
      actions={<BusyButton size="sm" variant="secondary" busy={query.isFetching} onPress={() => void query.refetch()}>重新读取</BusyButton>}
      contentClassName="flex flex-col gap-3"
    >
      <div className="flex flex-wrap items-center gap-3">
        <SearchField aria-label="搜索请求记录" value={q} onChange={setQ} className="w-full sm:w-72">
          <SearchField.Group>
            <SearchField.SearchIcon />
            <SearchField.Input placeholder="搜索 IP / UA / 模型 / 账号 / 请求 ID" />
            <SearchField.ClearButton />
          </SearchField.Group>
        </SearchField>
        <Select aria-label="按结果筛选" value={outcome} onChange={(k) => setOutcome(String(k ?? ''))} className="w-32">
          <Label className="sr-only">按结果筛选</Label>
          <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
          <Select.Popover>
            <ListBox>
              {OUTCOMES.map((o) => <ListBox.Item key={o.id} id={o.id} textValue={o.label}>{o.label}<ListBox.ItemIndicator /></ListBox.Item>)}
            </ListBox>
          </Select.Popover>
        </Select>
        {/* 时间范围与条数是在归档里取数的条件；归档关闭时只有内存里的最近 100 条，这两项没有意义 */}
        {d?.archiveOn && (
          <>
            <TimeRangePicker value={range} onChange={setRange} />
            <Select aria-label="读取条数" value={limit} onChange={(k) => k != null && setLimit(Number(k))} className="w-36">
              <Label className="sr-only">读取条数</Label>
              <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
              <Select.Popover>
                <ListBox>
                  {LIMITS.map((n) => <ListBox.Item key={n} id={n} textValue={`最近 ${n} 条`}>最近 {n} 条<ListBox.ItemIndicator /></ListBox.Item>)}
                </ListBox>
              </Select.Popover>
            </Select>
          </>
        )}
        {count && <span className={`text-sm sm:ml-auto ${shown.length !== all.length || !hasSource ? 'text-warning' : 'text-muted'}`}>{count}</span>}
      </div>
      {d?.archiveError && <ErrorBox title="读取请求归档失败" error={d.archiveError} onRetry={() => void query.refetch()} />}
      <Loaded query={query} rows={4}>
        {() => shown.length === 0 ? <Empty title={all.length ? '没有符合当前筛选条件的请求记录' : '暂无请求记录'} /> : isDesktop ? (
          <Table variant="secondary">
            <Table.ScrollContainer className="max-h-[32rem] overflow-y-auto">
              <Table.Content aria-label="请求记录" className="min-w-[1120px]">
                <Table.Header className="sticky top-0 z-10 bg-surface">
                  <Table.Column isRowHeader>时间</Table.Column>
                  <Table.Column>结果</Table.Column>
                  <Table.Column>模型</Table.Column>
                  <Table.Column>账号</Table.Column>
                  <Table.Column>来源 IP</Table.Column>
                  <Table.Column>User-Agent</Table.Column>
                  <Table.Column className="text-right">耗时</Table.Column>
                  <Table.Column className="text-right">Token</Table.Column>
                  <Table.Column className="text-right">积分</Table.Column>
                  <Table.Column>请求 ID</Table.Column>
                </Table.Header>
                <Table.Body>
                  {shown.map((e, i) => (
                    <Table.Row key={(e.request_id || '') + i} id={(e.request_id || '') + i} aria-label={requestLogText(e)}>
                      <Table.Cell className="whitespace-nowrap font-mono tabular-nums">{clock(e)}</Table.Cell>
                      <Table.Cell><Outcome e={e} /></Table.Cell>
                      <Table.Cell className="whitespace-nowrap font-mono">{e.model || '—'}</Table.Cell>
                      <Table.Cell className="whitespace-nowrap">{e.account || '—'}</Table.Cell>
                      <Table.Cell className="whitespace-nowrap font-mono text-xs">{e.client_ip || dash}</Table.Cell>
                      <Table.Cell>
                        {e.user_agent ? <span className="block max-w-56 truncate text-xs" title={e.user_agent}>{e.user_agent}</span> : dash}
                      </Table.Cell>
                      <Table.Cell className="text-right font-mono tabular-nums">{fmtMs(e.duration_ms)}</Table.Cell>
                      <Table.Cell className="text-right font-mono tabular-nums"><Tokens e={e} /></Table.Cell>
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
            {shown.map((e, i) => (
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
                  {cacheRateText(e.cache_hit_tokens, e.cache_miss_tokens) !== '—' && <span>命中 {cacheRateText(e.cache_hit_tokens, e.cache_miss_tokens)}</span>}
                  <span>{requestCredit(e)}</span>
                </div>
                {(e.client_ip || e.user_agent) && (
                  <div className="break-all font-mono text-xs text-muted">{[e.client_ip, e.user_agent].filter(Boolean).join(' · ')}</div>
                )}
                {e.request_id && <span className="select-all break-all font-mono text-xs text-muted">{e.request_id}</span>}
              </li>
            ))}
          </ul>
        )}
      </Loaded>
    </Panel>
  )
}
