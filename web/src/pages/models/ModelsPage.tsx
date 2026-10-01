// 模型与档位：实时查询上游模型目录（国内版、国际版两个域），展示积分倍率、思考档位、上下文与输出上限。
// 输出上限列若有 scripts/probe_max_tokens.py 的实测结果，标注钳制情况；没有则显示上游声称值。
import { useMemo, useState, type ReactNode } from 'react'
import {
  Button, Card, Chip, Label, ListBox, SearchField, Select, Table, ToggleButton, ToggleButtonGroup, useMediaQuery,
} from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { fmtK } from '../../lib/format'
import {
  EMPTY_MODEL_FILTER, filterModels, probeFor, type Capability, type EffortFilter, type ModelFilter, type ModelSort, type PromoFilter,
  type RealmFilter,
} from '../../lib/models'
import { qk, useModels, useProbes } from '../../lib/queries'
import type { Model, Probe } from '../../lib/types'
import { BusyButton, Empty, Loaded } from '../../components/Feedback'
import { InfoTip, Panel } from '../../components/Panel'

function probeDays(ts?: string): number | null {
  if (!ts) return null
  const d = (Date.now() - new Date(String(ts).replace(' ', 'T')).getTime()) / 86400000
  return Number.isNaN(d) ? null : Math.floor(d)
}

/** 最大输出：有实测按实测标注（钳制 ⚠ / 达标 ✓ / 至少 ≥ / 未测出 ?），没有就显示上游声称值 */
function OutputCell({ m, pr }: { m: Model; pr?: Probe }) {
  if (!pr) return <span className="tabular-nums">{m.max_output_tokens ? fmtK(m.max_output_tokens) : '—'}</span>
  const tip = [
    '声称 ' + (pr.claimed ? fmtK(pr.claimed) : '?') + ' · 实测 ' + (pr.measured ? fmtK(pr.measured) : '?'),
    pr.note, pr.tested_at && '探测于 ' + pr.tested_at,
  ].filter(Boolean).join('\n')
  const days = probeDays(pr.tested_at)
  const stale = days !== null && days > 30 ? ' · ' + days + ' 天前' : ''
  let body: ReactNode
  if (pr.verdict === 'clamped' && pr.measured) {
    if (pr.claimed && pr.measured < pr.claimed) {
      const x = pr.claimed / pr.measured
      body = (
        <span className="flex flex-col">
          <span className="font-semibold text-warning">{fmtK(pr.measured)} ⚠</span>
          <span className="text-xs text-muted">钳制 {x >= 10 ? Math.round(x) : Math.round(x * 10) / 10}×{stale}</span>
        </span>
      )
    } else {
      body = <span className="text-success">{fmtK(pr.measured)}{pr.claimed && pr.measured > pr.claimed ? ' ↑' : ' ✓'}</span>
    }
  } else if (pr.verdict === 'at_least' && pr.measured) {
    body = <span className="text-muted">≥{fmtK(pr.measured)}</span>
  } else {
    body = <span className="flex flex-col"><span className="text-muted">?</span><span className="text-xs text-muted">未测出{stale}</span></span>
  }
  return <span className="flex items-center gap-1 tabular-nums">{body}<InfoTip label="实测详情">{tip}</InfoTip></span>
}

/** 积分倍率：上游 credits 是牌价，promo_* 是当前生效折扣（WorkBuddy 客户端显示的是生效价） */
function RateCell({ m }: { m: Model }) {
  const note = m.promo_note ? <InfoTip label="优惠说明">{m.promo_note}</InfoTip> : null
  if (m.promo_factor != null && m.promo_credits) {
    return (
      <span className="flex flex-wrap items-center gap-1">
        <b>{m.promo_credits}</b>
        {m.promo_label && <Chip size="sm" variant="soft" color="success">{m.promo_label}</Chip>}
        {m.credits && <s className="text-xs text-muted">{m.credits}</s>}
        {note}
      </span>
    )
  }
  if (m.promo_label) {
    return (
      <span className="flex flex-wrap items-center gap-1">
        {m.credits || '—'}<Chip size="sm" variant="soft" color="warning">{m.promo_label}</Chip>{note}
      </span>
    )
  }
  return <span>{m.credits || '—'}</span>
}

function Efforts({ m }: { m: Model }) {
  const eff = [...(m.supported_efforts || [])]
  if (m.can_disable_thinking && eff.length && !eff.includes('off')) eff.push('off（可关）')
  if (!eff.length) {
    return <span className="text-sm text-muted">{m.supports_reasoning ? '固定档 · 默认 ' + (m.default_effort || '?') : '不支持思考'}</span>
  }
  return <span className="flex flex-wrap gap-1">{eff.map((e) => <Chip key={e} size="sm" variant="soft" color="warning">{e}</Chip>)}</span>
}

function ModelName({ m }: { m: Model }) {
  const caps: [string, 'success' | 'warning'][] = []
  if (m.is_default) caps.push(['默认', 'success'])
  if (m.supports_tool_call) caps.push(['工具', 'warning'])
  if (m.supports_images) caps.push(['视觉', 'warning'])
  if (m.supports_reasoning && !m.can_disable_thinking) caps.push(['思考常开', 'warning'])
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <span className="flex items-center gap-1">
        <span className="break-all font-medium">{m.id}</span>
        {m.description && <InfoTip label="模型说明">{m.description}</InfoTip>}
      </span>
      {(m.name || m.vendor) && <span className="text-xs text-muted">{[m.name, m.vendor].filter(Boolean).join(' · ')}</span>}
      {caps.length > 0 && (
        <span className="flex flex-wrap gap-1">{caps.map(([l, c]) => <Chip key={l} size="sm" variant="soft" color={c}>{l}</Chip>)}</span>
      )}
    </div>
  )
}

const EFFORTS: { id: EffortFilter; label: string }[] = [
  { id: '', label: '全部档位' },
  { id: 'any', label: '可调档位' },
  { id: 'off', label: '可关闭思考' },
  ...(['low', 'medium', 'high', 'xhigh', 'max'] as const).map((id) => ({ id, label: id })),
]
const PROMOS: { id: PromoFilter; label: string }[] = [
  { id: '', label: '全部价格' },
  { id: 'promo', label: '有折扣 / 限时免费' },
  { id: 'free', label: '限时免费' },
  { id: 'discount', label: '打折（非免费）' },
]
const SORTS: { id: ModelSort; label: string }[] = [
  { id: 'default', label: '上游默认顺序' },
  { id: 'rate', label: '积分倍率 低 → 高' },
  { id: 'context', label: '上下文 大 → 小' },
  { id: 'output', label: '最大输出 大 → 小' },
  { id: 'name', label: '模型 ID A → Z' },
]

/** 下拉筛选（选项 id 可能是空串：「全部」） */
function Pick<T extends string>({ label, value, options, onChange, className }: {
  label: string
  value: T
  options: { id: T; label: string }[]
  onChange: (v: T) => void
  className?: string
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

const ctx = (m: Model) => (m.context_length ? Math.round(m.context_length / 1000) + 'K' : '—')

export function ModelsPage() {
  const qc = useQueryClient()
  const models = useModels()
  const probes = useProbes()
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const [filter, setFilter] = useState<ModelFilter>(EMPTY_MODEL_FILTER)
  const [sort, setSort] = useState<ModelSort>('default')
  const set = <K extends keyof ModelFilter>(k: K, v: ModelFilter[K]) => setFilter((f) => ({ ...f, [k]: v }))
  const filtered = JSON.stringify(filter) !== JSON.stringify(EMPTY_MODEL_FILTER)

  const list = useMemo(() => models.data?.models || [], [models.data])
  const shown = useMemo(() => filterModels(list, filter, sort), [list, filter, sort])
  const pr = probes.data?.probes || {}
  const hit = list.filter((m) => probeFor(pr, m.id)).length

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: qk.models })
    void qc.invalidateQueries({ queryKey: qk.probes })
  }

  return (
    <Panel
      title="模型能力"
      desc={
        <>
          max_tokens 已可正常透传最大输出；官方返回的最大输出仅供参考，不保证完全正确。
          {list.length > 0 && <> {list.length} 个模型 · 已刷新降级缓存{hit ? ' · ' + hit + ' 个有实测上限' : ''}</>}
        </>
      }
      actions={<BusyButton size="sm" variant="secondary" busy={models.isFetching} onPress={refresh}>重新获取</BusyButton>}
      contentClassName="flex flex-col gap-3"
    >
      <div className="flex flex-wrap items-center gap-3">
        <SearchField aria-label="搜索模型" value={filter.q} onChange={(v) => set('q', v)} className="w-full sm:w-72">
          <SearchField.Group>
            <SearchField.SearchIcon />
            <SearchField.Input placeholder="搜索 ID / 名称 / 厂商 / 描述" />
            <SearchField.ClearButton />
          </SearchField.Group>
        </SearchField>
        <ToggleButtonGroup aria-label="按域筛选" size="sm" selectionMode="single" disallowEmptySelection
          selectedKeys={[filter.realm]} onSelectionChange={(k) => set('realm', ([...k][0] as RealmFilter) ?? 'all')}>
          <ToggleButton id="all">全部</ToggleButton>
          <ToggleButton id="cn"><ToggleButtonGroup.Separator />国内版</ToggleButton>
          <ToggleButton id="global"><ToggleButtonGroup.Separator />国际版</ToggleButton>
        </ToggleButtonGroup>
        <ToggleButtonGroup aria-label="按能力筛选" size="sm" selectionMode="multiple"
          selectedKeys={filter.caps} onSelectionChange={(k) => set('caps', [...k] as Capability[])}>
          <ToggleButton id="tools">工具调用</ToggleButton>
          <ToggleButton id="vision"><ToggleButtonGroup.Separator />视觉</ToggleButton>
          <ToggleButton id="reasoning"><ToggleButtonGroup.Separator />思考</ToggleButton>
          <ToggleButton id="default"><ToggleButtonGroup.Separator />默认模型</ToggleButton>
        </ToggleButtonGroup>
        <Pick label="按思考档位筛选" value={filter.effort} options={EFFORTS} onChange={(v) => set('effort', v)} className="w-32" />
        <Pick label="按价格筛选" value={filter.promo} options={PROMOS} onChange={(v) => set('promo', v)} className="w-40" />
        <Pick label="排序" value={sort} options={SORTS} onChange={setSort} className="w-40" />
        {list.length > 0 && (
          <span className={`text-sm sm:ml-auto ${shown.length !== list.length ? 'text-warning' : 'text-muted'}`}>
            {shown.length !== list.length ? `命中 ${shown.length} / ${list.length} 个模型` : `${list.length} 个模型`}
          </span>
        )}
        {(filtered || sort !== 'default') && (
          <Button size="sm" variant="ghost" onPress={() => { setFilter(EMPTY_MODEL_FILTER); setSort('default') }}>重置</Button>
        )}
      </div>
      <Loaded query={models} rows={8}>
        {() => shown.length === 0 ? (
          <Empty title={list.length ? '没有符合条件的模型' : '上游未返回模型'} />
        ) : isDesktop ? (
          <Table variant="secondary">
            <Table.ScrollContainer>
              <Table.Content aria-label="模型能力" className="min-w-[860px]">
                <Table.Header>
                  <Table.Column isRowHeader>模型</Table.Column>
                  <Table.Column>积分倍率</Table.Column>
                  <Table.Column>默认档</Table.Column>
                  <Table.Column>支持的思考档位</Table.Column>
                  <Table.Column>上下文长度</Table.Column>
                  <Table.Column>最大输出</Table.Column>
                </Table.Header>
                <Table.Body>
                  {shown.map((m) => (
                    <Table.Row key={m.id} id={m.id}>
                      <Table.Cell><div className="max-w-72"><ModelName m={m} /></div></Table.Cell>
                      <Table.Cell><RateCell m={m} /></Table.Cell>
                      <Table.Cell>{m.default_effort ? <Chip size="sm" variant="soft" color="success">{m.default_effort}</Chip> : <span className="text-muted">—</span>}</Table.Cell>
                      <Table.Cell><div className="max-w-56"><Efforts m={m} /></div></Table.Cell>
                      <Table.Cell className="tabular-nums">{ctx(m)}</Table.Cell>
                      <Table.Cell><OutputCell m={m} pr={probeFor(pr, m.id)} /></Table.Cell>
                    </Table.Row>
                  ))}
                </Table.Body>
              </Table.Content>
            </Table.ScrollContainer>
          </Table>
        ) : (
          <div className="flex flex-col gap-3">
            {shown.map((m) => (
              <Card key={m.id} variant="secondary" className="gap-3 p-4">
                <ModelName m={m} />
                <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
                  <dt className="text-muted">积分倍率</dt><dd><RateCell m={m} /></dd>
                  <dt className="text-muted">默认档</dt><dd>{m.default_effort || '—'}</dd>
                  <dt className="text-muted">思考档位</dt><dd><Efforts m={m} /></dd>
                  <dt className="text-muted">上下文</dt><dd className="tabular-nums">{ctx(m)}</dd>
                  <dt className="text-muted">最大输出</dt><dd><OutputCell m={m} pr={probeFor(pr, m.id)} /></dd>
                </dl>
              </Card>
            ))}
          </div>
        )}
      </Loaded>
    </Panel>
  )
}
