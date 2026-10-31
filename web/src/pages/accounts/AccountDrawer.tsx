// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号详情抽屉：展示后端已有、账号表放不下的运维信息——模型级限额台账、成本台账、
// 最早到期批次、连败 / 熔断计数、最近一次请求。数据取自总览（随 5 秒轮询实时更新）。
// 账号备注也在这里改（存在网关的号池状态里，各页的账号名都优先用它）。
import { useState, type ReactNode } from 'react'
import { Button, Chip, Description, Drawer, FieldError, Input, Label, Separator, Surface, TextField, toast, useMediaQuery } from '@heroui/react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { activeRateLimits, creditsText, NOTE_MAX, rateLimitInfo, type Health } from '../../lib/accounts'
import { acct, post } from '../../lib/api'
import { ago, dateTime, formatLatency, formatRate, formatTokenCount, fmtTok, timeMs, until } from '../../lib/format'
import { toastError } from '../../lib/hooks'
import { qk } from '../../lib/queries'
import type { Account, Overview } from '../../lib/types'
import { AccountButtons } from './AccountButtons'
import { HealthChip, RealmChips } from './AccountCells'
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

export function AccountDrawer({ account: a, health, now, busy, run, onClose }: {
  account: Account | null
  health: Health | null
  now: number
  busy: string | null
  run: (a: Account, action: AccountAction) => void
  onClose: () => void
}) {
  const isWide = useMediaQuery('(min-width: 640px)')
  return (
    <Drawer.Backdrop isOpen={!!a} onOpenChange={(v) => { if (!v) onClose() }}>
      <Drawer.Content placement={isWide ? 'right' : 'bottom'}>
        <Drawer.Dialog className={isWide ? 'w-[440px] max-w-full' : 'max-h-[88dvh]'}>
          {isWide ? <Drawer.CloseTrigger /> : <Drawer.Handle />}
          {a && health && <Body a={a} health={health} now={now} busy={busy} run={run} />}
        </Drawer.Dialog>
      </Drawer.Content>
    </Drawer.Backdrop>
  )
}

/** 改备注：改完点保存，清空再保存就是去掉备注。存好后直接改总览缓存，不等下一次轮询 */
function NoteEditor({ a }: { a: Account }) {
  const qc = useQueryClient()
  const [value, setValue] = useState(a.note || '')
  const note = value.trim()
  const tooLong = [...note].length > NOTE_MAX
  const save = useMutation({
    mutationFn: () => post<{ note: string }>(acct(a.uid, 'note'), { note }),
    onSuccess: (d) => {
      qc.setQueryData<Overview>(qk.overview, (o) => o && { ...o, accounts: (o.accounts || []).map((x) => x.uid === a.uid ? { ...x, note: d.note } : x) })
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

function Body({ a, health, now, busy, run }: {
  a: Account
  health: Health
  now: number
  busy: string | null
  run: (a: Account, action: AccountAction) => void
}) {
  const limited = activeRateLimits(a)
  const costs = (a.model_costs || []).filter((c) => c.model)
  const tu = a.token_usage || {}
  const earliest = timeMs(a.credits_earliest_expiry)
  const breaker = timeMs(a.breaker_until)
  const degrade = timeMs(a.degrade_until)

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
      </Drawer.Body>
      <Drawer.Footer>
        <AccountButtons a={a} health={health} busy={busy} run={run} extra={<ExportOneButton uid={a.uid} />} />
      </Drawer.Footer>
    </>
  )
}
