// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号详情抽屉：展示后端已有、账号表放不下的运维信息——模型级限额台账、成本台账、
// 最早到期批次、连败 / 熔断计数、最近一次请求、上次测活、代理。数据取自总览（随 5 秒轮询实时更新）。
// 账号备注、分组、优先级、不接的模型也在这里改（存在网关的号池状态里，备注各页的账号名都优先用它）。
import { useState, type ReactNode } from 'react'
import {
  Button, Checkbox, CheckboxGroup, Chip, Description, Drawer, FieldError, Input, Label, Separator, Surface, Tag, TagGroup, TextField, toast, useMediaQuery,
} from '@heroui/react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { activeRateLimits, creditsText, livenessSummary, NOTE_MAX, rateLimitInfo, type Health } from '../../lib/accounts'
import { acct, post } from '../../lib/api'
import { ago, dateTime, formatLatency, formatRate, formatTokenCount, fmtTok, timeMs, until } from '../../lib/format'
import { toastError } from '../../lib/hooks'
import { qk, useGroups } from '../../lib/queries'
import type { Account, BlockedModel, Overview, ProxyAccount } from '../../lib/types'
import { ModelPatternInput } from '../../components/ModelPatternInput'
import { AccountButtons } from './AccountButtons'
import { HealthChip, RealmChips } from './AccountCells'
import { PriorityField } from './BatchMenu'
import { ExportOneButton } from './ExportBar'
import type { AccountAction } from './useAccountActions'

function Row({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 py-1.5 text-sm">
      <span className="shrink-0 text-muted">{label}</span>
      <span className="min-w-0 text-right tabular-nums">{children}</span>
    </div>
  )
}

function Section({ title, children }: { title: ReactNode; children: ReactNode }) {
  return (
    <section className="flex flex-col">
      <h3 className="mb-1 text-sm font-semibold">{title}</h3>
      {children}
    </section>
  )
}

const when = (iso?: string) => {
  const t = timeMs(iso)
  return t == null ? '—' : dateTime(t)
}

export function AccountDrawer({ account: a, health, proxy, now, busy, run, onBind, onClose }: {
  account: Account | null
  health: Health | null
  /** 号的代理情况；没绑也没被拦时没有 */
  proxy?: ProxyAccount
  now: number
  busy: string | null
  run: (a: Account, action: AccountAction) => void
  /** 打开绑定代理的弹窗；没用代理池时不给（详情里也不显示代理一节） */
  onBind?: (uid: string) => void
  onClose: () => void
}) {
  const isWide = useMediaQuery('(min-width: 640px)')
  return (
    <Drawer.Backdrop isOpen={!!a} onOpenChange={(v) => { if (!v) onClose() }}>
      <Drawer.Content placement={isWide ? 'right' : 'bottom'}>
        <Drawer.Dialog className={isWide ? 'w-[440px] max-w-full' : 'max-h-[88dvh]'}>
          {isWide ? <Drawer.CloseTrigger /> : <Drawer.Handle />}
          {a && health && <Body a={a} health={health} proxy={proxy} now={now} busy={busy} run={run} onBind={onBind} />}
        </Drawer.Dialog>
      </Drawer.Content>
    </Drawer.Backdrop>
  )
}

/** 改总览缓存里这个号的几个字段，不等下一次轮询 */
function usePatchAccount(uid: string) {
  const qc = useQueryClient()
  return (patch: Partial<Account>) =>
    qc.setQueryData<Overview>(qk.overview, (o) => o && { ...o, accounts: (o.accounts || []).map((x) => x.uid === uid ? { ...x, ...patch } : x) })
}

/** 改备注：改完点保存，清空再保存就是去掉备注。存好后直接改总览缓存，不等下一次轮询 */
function NoteEditor({ a }: { a: Account }) {
  const patch = usePatchAccount(a.uid)
  const [value, setValue] = useState(a.note || '')
  const note = value.trim()
  const tooLong = [...note].length > NOTE_MAX
  const save = useMutation({
    mutationFn: () => post<{ note: string }>(acct(a.uid, 'note'), { note }),
    onSuccess: (d) => {
      patch({ note: d.note })
      setValue(d.note)
      toast.success(d.note ? '备注已保存' : '备注已清除')
    },
    onError: toastError,
  })
  return (
    <div className="flex items-start gap-2">
      <TextField value={value} onChange={setValue} isInvalid={tooLong} autoComplete="off" className="min-w-0 flex-1">
        <Label>备注</Label>
        <Input placeholder="比如这个号是谁的、拿来做什么" />
        {tooLong ? <FieldError>最多 {NOTE_MAX} 个字</FieldError> : <Description>只在面板上显示，各页的账号名优先用它</Description>}
      </TextField>
      <Button size="sm" variant="secondary" className="mt-6" isDisabled={tooLong || note === (a.note || '')} isPending={save.isPending} onPress={() => save.mutate()}>
        保存
      </Button>
    </div>
  )
}

/** 不接的模型：加一项、点 × 删一项都马上保存（整份发给网关）。上游拒绝后自动加的标出来，删掉就是解除 */
function BlockedModelsEditor({ a }: { a: Account }) {
  const patch = usePatchAccount(a.uid)
  const list = a.blocked_models || []
  const names = list.map((b) => b.model)
  const save = useMutation({
    mutationFn: (models: string[]) => post<{ blocked_models?: BlockedModel[] }>(acct(a.uid, 'blocked_models'), { models }),
    onSuccess: (d) => {
      patch({ blocked_models: d.blocked_models })
      toast.success('不接的模型已保存')
    },
    onError: (e) => toastError(e),
  })
  return (
    <div className="flex flex-col gap-2">
      <p className="text-xs text-muted">
        选号时不把这些模型派给这个号。上游回 11158（这类账号不能用这个模型）时自动加上，标「上游拒绝」，一直屏蔽；点 × 解除。
        也可以手动加，按不带 cn: / global: 前缀的模型名匹配，支持 * 通配。
      </p>
      {list.length > 0 && (
        <TagGroup aria-label="不接的模型" onRemove={(keys) => save.mutate(names.filter((m) => !keys.has(m)))}>
          <TagGroup.List items={list.map((b) => ({ ...b, id: b.model }))}>
            {(b) => (
              <Tag id={b.id} textValue={b.model} className="font-mono">
                {b.model}
                {b.auto && <span className="font-sans text-muted" title={b.reason}>· 上游拒绝</span>}
              </Tag>
            )}
          </TagGroup.List>
        </TagGroup>
      )}
      <ModelPatternInput value={names} onChange={(next) => save.mutate(next)} isDisabled={save.isPending} />
    </div>
  )
}

/** 号在哪些分组里：勾上、取消都马上保存（整份发给网关）。还没建分组时提示去账号池页下方建 */
function GroupsEditor({ a }: { a: Account }) {
  const qc = useQueryClient()
  const patch = usePatchAccount(a.uid)
  const groups = useGroups().data ?? []
  const save = useMutation({
    mutationFn: (ids: string[]) => post<{ groups: string[] }>(acct(a.uid, 'groups'), { groups: ids }),
    onSuccess: (d) => {
      patch({ groups: d.groups })
      void qc.invalidateQueries({ queryKey: qk.groups })
      toast.success('分组已保存')
    },
    onError: (e) => toastError(e),
  })
  if (groups.length === 0) return <p className="text-sm text-muted">还没有分组，在账号池页下方的「账号分组」里建。</p>
  return (
    <CheckboxGroup value={a.groups || []} onChange={(ids) => save.mutate(ids)} isDisabled={save.isPending}>
      <Label>分组</Label>
      <div className="flex flex-wrap gap-x-4 gap-y-2">
        {groups.map((g) => (
          <Checkbox key={g.id} value={g.id}>
            <Checkbox.Content>
              <Checkbox.Control><Checkbox.Indicator /></Checkbox.Control>
              {g.name}
            </Checkbox.Content>
          </Checkbox>
        ))}
      </div>
      <Description>限定了分组的客户端密钥只用组里的号；分组本身不影响选号。</Description>
    </CheckboxGroup>
  )
}

/** 改优先级：改完点保存 */
function PriorityEditor({ a }: { a: Account }) {
  const patch = usePatchAccount(a.uid)
  const [value, setValue] = useState(a.priority ?? 0)
  const save = useMutation({
    mutationFn: () => post<{ priority: number }>(acct(a.uid, 'priority'), { priority: value }),
    onSuccess: (d) => {
      patch({ priority: d.priority })
      toast.success('优先级已保存')
    },
    onError: (e) => toastError(e),
  })
  return (
    <div className="flex items-start gap-2">
      <PriorityField value={value} onChange={setValue} className="min-w-0 flex-1" />
      <Button size="sm" variant="secondary" className="mt-6" isDisabled={value === (a.priority ?? 0)} isPending={save.isPending} onPress={() => save.mutate()}>
        保存
      </Button>
    </div>
  )
}

function Body({ a, health, proxy, now, busy, run, onBind }: {
  a: Account
  health: Health
  proxy?: ProxyAccount
  now: number
  busy: string | null
  run: (a: Account, action: AccountAction) => void
  onBind?: (uid: string) => void
}) {
  const limited = activeRateLimits(a)
  const costs = (a.model_costs || []).filter((c) => c.model)
  const tu = a.token_usage || {}
  const earliest = timeMs(a.credits_earliest_expiry)
  const breaker = timeMs(a.breaker_until)
  const degrade = timeMs(a.degrade_until)
  const lv = a.last_liveness

  return (
    <>
      <Drawer.Header>
        <Drawer.Heading className="flex flex-wrap items-center gap-2">
          {a.note || a.nickname || '未命名'}
          {a.note && a.nickname && <span className="text-sm font-normal text-muted">{a.nickname}</span>}
          <RealmChips a={a} />
        </Drawer.Heading>
        <p className="select-all break-all font-mono text-xs text-muted">{a.uid}</p>
      </Drawer.Header>
      <Drawer.Body className="flex flex-col gap-5">
        <NoteEditor key={a.uid} a={a} />

        <Section title="状态">
          <Row label="当前"><HealthChip health={health} now={now} /></Row>
          {a.paused && <Row label="暂停选号">{a.enterprise ? '不参与选号，保活 / 刷新额度照常' : '不参与选号，签到 / 活跃上报 / 保活 / 刷新余额照常'}</Row>}
          {health.coolEnd != null && <Row label="恢复时间">{dateTime(health.coolEnd)}</Row>}
          {a.reason && <Row label={a.disabled ? '禁用原因' : '原因'}><span className="break-all">{a.reason}</span></Row>}
          <Row label="连续失败">{a.consecutive_fails}{degrade != null && degrade > now && <span className="text-warning">（降权至 {dateTime(degrade)}）</span>}</Row>
          <Row label="熔断计数">{a.breaker_fails}{breaker != null && breaker > now && <span className="text-warning">（熔断至 {dateTime(breaker)}）</span>}</Row>
          {!!a.soft_streak && <Row label="连续软冷却">{a.soft_streak} 次（退避指数）</Row>}
          <Row label="在途请求">{a.in_flight}</Row>
        </Section>

        {(onBind || proxy) && (
          <>
            <Separator />
            <Section title="代理">
              {proxy?.proxy ? (
                <>
                  <Row label="代理">{proxy.proxy_name || proxy.proxy}{proxy.manual && <span className="text-muted">（单独指定）</span>}</Row>
                  <Row label="池">{proxy.pool_name || '—'}</Row>
                  <Row label="出口"><span className="break-all">{proxy.exit_ip ? proxy.exit_ip + (proxy.exit_geo ? ' · ' + proxy.exit_geo : '') : '还没查到'}</span></Row>
                </>
              ) : proxy?.waiting ? (
                <Row label="池">{proxy.pool_name || proxy.pool}（池里没有空位，在等代理）</Row>
              ) : (
                <p className="text-sm text-muted">没绑代理，走服务器自己的网络（或环境变量里的全局代理）。</p>
              )}
              {a.proxy_blocked && <Row label="现在"><span className="break-all text-warning">{a.proxy_blocked}</span></Row>}
              {onBind && (
                <div className="pt-1">
                  <Button size="sm" variant="secondary" onPress={() => onBind(a.uid)}>{proxy?.proxy || proxy?.waiting ? '改绑代理' : '绑定代理'}</Button>
                </div>
              )}
            </Section>
          </>
        )}

        <Separator />
        <Section title="分组与优先级">
          <div className="flex flex-col gap-4">
            <GroupsEditor a={a} />
            <PriorityEditor key={a.uid} a={a} />
          </div>
        </Section>

        <Separator />
        <Section title={`模型限流与不可用${limited.length ? `（${limited.length}）` : ''}`}>
          {limited.length === 0 ? (
            <p className="text-sm text-muted">所有模型都可以用。</p>
          ) : (
            <ul className="flex flex-col gap-2">
              {limited.map((m) => {
                const info = rateLimitInfo(m, now)
                return (
                  <Surface<'li'> key={m.model} variant="secondary" render={(props) => <li {...props} />} className="rounded-xl px-3 py-2 text-sm">
                    <div className="flex items-center justify-between gap-2">
                      <span className="truncate font-medium">{info.model}</span>
                      <Chip size="sm" variant="soft" color={info.unavailable ? 'default' : 'warning'}>
                        {info.unavailable ? '模型不可用' : '限流'}
                      </Chip>
                    </div>
                    <div className="text-xs text-muted">{info.detail}</div>
                    {m.reason && <div className="mt-0.5 break-all text-xs text-muted">{m.reason}</div>}
                  </Surface>
                )
              })}
            </ul>
          )}
        </Section>

        <Separator />
        <Section title={`不接的模型${a.blocked_models?.length ? `（${a.blocked_models.length}）` : ''}`}>
          <BlockedModelsEditor a={a} />
        </Section>

        <Separator />
        <Section title="积分">
          <Row label={a.enterprise ? '剩余 / 分配额度' : '剩余 / 总额'}>{creditsText(a.credits, a.credits_total)}</Row>
          {a.enterprise && <p className="text-xs text-muted">企业版额度按周期重置，未用完即作废；这里只能看到分配给这个号的额度，企业池总额只有管理员可见。</p>}
          {earliest != null && earliest > now && (
            <Row label="最早到期">{fmtTok(a.credits_earliest_remaining)} 分 · {until(earliest, now)}（{dateTime(earliest)}）</Row>
          )}
          {!!a.credits_expiring && <Row label="快过期窗口内">{fmtTok(a.credits_expiring)} 分</Row>}
        </Section>

        <Separator />
        <Section title="成本台账">
          {costs.length === 0 ? (
            <p className="text-sm text-muted">暂无实测单价（有效期内没有观测）。</p>
          ) : (
            costs.map((c) => (
              <Row key={c.model} label={<span className="break-all">{c.model}</span>}>
                {c.cost_per_1k <= 0 ? <span className="text-success">免费</span> : c.cost_per_1k + ' credits/1K'}
                <span className="block text-xs text-muted">{c.samples ? c.samples + ' 次样本 · ' : ''}{ago(c.last_seen, now)}</span>
              </Row>
            ))
          )}
        </Section>

        <Separator />
        <Section title="请求">
          <Row label="成功 / 失败">{a.success_count || 0} / <span className="text-danger">{a.err_total || 0}</span></Row>
          <Row label="最近成功">{ago(a.last_success, now)}</Row>
          <Row label="最近失败">{ago(a.last_err, now)}</Row>
          <Row label="累计请求">{tu.request_count || 0} 次</Row>
          <Row label="累计 token">{formatTokenCount(tu.total_tokens)}（输入 {formatTokenCount(tu.prompt_tokens)} · 输出 {formatTokenCount(tu.completion_tokens)}）</Row>
          <Row label="最近一次">{tu.last_model || '—'} · {formatLatency(tu.last_latency_ms)} · {formatRate(tu.last_tokens_per_second)}</Row>
          <Row label="最近使用">{when(tu.last_used_at)}</Row>
        </Section>

        <Separator />
        <Section title="上次测活">
          {lv ? (
            <>
              <Row label="结果"><span className={`break-all ${lv.ok ? 'text-success' : 'text-danger'}`}>{livenessSummary(lv)}</span></Row>
              <Row label="时间">{when(lv.at)}（{lv.scheduled ? '定时测活' : '手动测活'}）</Row>
              {lv.model && <Row label="模型"><span className="break-all">{lv.model}</span></Row>}
              {!!lv.credit && <Row label="扣积分">{lv.credit}</Row>}
              {lv.recovered && <Row label="恢复">测通后解除了冷却、熔断或网关自动加的禁用</Row>}
            </>
          ) : (
            <p className="text-sm text-muted">还没测过。点下面的「测活」给这个号发一条最短的对话。</p>
          )}
        </Section>
      </Drawer.Body>
      <Drawer.Footer>
        <AccountButtons a={a} health={health} busy={busy} run={run} extra={<ExportOneButton uid={a.uid} />} />
      </Drawer.Footer>
    </>
  )
}
