// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号的各个展示单元（名称、状态、积分、用量），账号表、账号卡片、详情抽屉共用。
import { Chip, ProgressBar } from '@heroui/react'
import { activeRateLimits, creditPercent, groupLine, isUnlimited, livenessSummary, rateLimitInfo, type Health } from '../../lib/accounts'
import { ago, dateTime, dur, formatLatency, formatRate, formatTokenCount, fmtTok, timeMs, until } from '../../lib/format'
import { useGroups } from '../../lib/queries'
import type { Account, ProxyAccount } from '../../lib/types'
import { Hint } from '../../components/Hint'

/** 账号名：有备注时备注当主名、昵称浅色跟在后面；下一行是 UID，所以没名字时写「未命名」 */
export function AccountName({ a }: { a: Account }) {
  const name = a.note || a.nickname
  return (
    <div className="min-w-0">
      <div className="flex items-center gap-1.5">
        <span className={`truncate font-medium ${name ? '' : 'text-muted'}`}>{name || '未命名'}</span>
        {a.note && a.nickname && <span className="truncate text-xs text-muted">{a.nickname}</span>}
        <RealmChips a={a} />
      </div>
      <div className="truncate font-mono text-xs text-muted">{a.uid.length > 16 ? a.uid.slice(0, 16) + '…' : a.uid}</div>
    </div>
  )
}

/** 国际版 / 企业版标签（账号名旁边、详情抽屉标题） */
export function RealmChips({ a }: { a: Account }) {
  return (
    <>
      {a.realm === 'global' && <Chip size="sm" variant="soft" color="accent">国际版</Chip>}
      {a.enterprise && <Chip size="sm" variant="soft" color="default">企业版</Chip>}
    </>
  )
}

export function HealthChip({ health, now }: { health: Health; now: number }) {
  if (health.state === 'disabled') return <Chip size="sm" variant="soft" color="danger">已禁用</Chip>
  const cooling = health.coolEnd != null && (
    <Chip size="sm" variant="soft" color="warning">{health.kind} · {dur((health.coolEnd - now) / 1000)}</Chip>
  )
  // 被代理拦着（等代理、代理不通…）：不接请求，换了代理或代理恢复才好
  const proxy = health.proxy && <Chip size="sm" variant="soft" color="warning">{health.proxy}</Chip>
  if (health.state === 'paused' || health.state === 'blocked') {
    // 暂停选号、被代理拦着的号也可能同时在冷却，都标出来（不然只看到「解冻」按钮不知道为什么）
    return (
      <span className="inline-flex flex-wrap gap-1">
        {health.state === 'paused' && <Chip size="sm" variant="soft" color="default">已暂停选号</Chip>}
        {proxy}
        {cooling}
      </span>
    )
  }
  return cooling || <Chip size="sm" variant="soft" color="success">可用</Chip>
}

/** 号的分组和优先级：「分组 主力、备用 · 优先级 3」；没分组、优先级是 0 时不显示 */
export function GroupLine({ a, className = '' }: { a: Account; className?: string }) {
  const groups = useGroups().data
  const text = groupLine(a, (id) => groups?.find((g) => g.id === id)?.name)
  return text ? <span className={`truncate text-xs text-muted ${className}`}>{text}</span> : null
}

/**
 * 号的代理：「代理 乙 · 203.0.113.7」，悬停看池、是不是单独指定的、出口地区。没分到代理的号不显示
 * （等代理、开着「只许走号的代理」却没绑，状态标签和拦截原因已经写了）。
 */
export function ProxyLine({ v, className = '' }: { v?: ProxyAccount; className?: string }) {
  if (!v?.proxy) return null
  const tip = [
    v.pool_name && '池「' + v.pool_name + '」' + (v.manual ? '（单独指定的代理）' : ''),
    v.exit_ip && '出口 ' + v.exit_ip + (v.exit_geo ? ' · ' + v.exit_geo : ''),
  ].filter(Boolean).join('\n')
  return (
    <Hint tip={tip} className={`truncate text-xs ${v.blocked ? 'text-warning' : 'text-muted'} ${className}`}>
      代理 {v.proxy_name || v.proxy}{v.exit_ip && ' · ' + v.exit_ip}
    </Hint>
  )
}

export function StatusCell({ a, health, now, proxy }: { a: Account; health: Health; now: number; proxy?: ProxyAccount }) {
  return (
    <div className="flex min-w-40 flex-col items-start gap-1">
      <HealthChip health={health} now={now} />
      {a.proxy_blocked && <span className="line-clamp-2 max-w-40 text-xs text-warning">{a.proxy_blocked}</span>}
      {a.reason && <span className="line-clamp-2 max-w-40 text-xs text-muted">{a.reason}</span>}
      <RateLimitList a={a} now={now} />
      <ProxyLine v={proxy} className="max-w-40" />
      <LivenessLine a={a} now={now} className="max-w-40" />
    </div>
  )
}

/** 上次测活：「测活 3 分钟前 · 通 · 首字 1.2s · 共 2.3s」，不通标红；悬停看模型、时间和完整原因。没测过不显示 */
export function LivenessLine({ a, now, className = '' }: { a: Account; now: number; className?: string }) {
  const r = a.last_liveness
  const at = timeMs(r?.at)
  if (!r || at == null) return null
  const tip = [
    (r.scheduled ? '定时测活' : '手动测活') + ' · ' + dateTime(at),
    r.model && '模型 ' + r.model,
    !!r.credit && '扣了 ' + r.credit + ' 积分',
    r.recovered && '测通后解除了冷却、熔断或网关自动加的禁用',
    !r.ok && r.error,
  ].filter(Boolean).join('\n')
  return (
    <Hint tip={tip} className={`line-clamp-2 wrap-anywhere text-xs ${r.ok ? 'text-muted' : 'text-danger'} ${className}`}>
      测活 {ago(r.at, now)} · {livenessSummary(r)}
    </Hint>
  )
}

/**
 * 暂时不能用的模型，每个一行：模型名 + 多久后解封（限流）或「不可用」。
 * 完整的解封时间、网关最早重试时间写在悬停提示和详情抽屉里。
 */
export function RateLimitList({ a, now }: { a: Account; now: number }) {
  const rows = activeRateLimits(a)
  if (!rows.length) return null
  return (
    <ul className="flex max-w-48 flex-col gap-0.5 text-xs" aria-label="暂时不能用的模型">
      {rows.map((row) => {
        const info = rateLimitInfo(row, now)
        return (
          // 模型名不截断：放不下时说明文字换到下一行
          <li key={row.model}>
            <Hint tip={info.model + '\n' + info.detail} className="flex flex-wrap items-baseline gap-x-1.5">
              <span className={`size-1.5 shrink-0 translate-y-[-1px] rounded-full ${info.unavailable ? 'bg-muted' : 'bg-warning'}`} />
              <span className="break-all font-mono">{info.model}</span>
              <span className={`whitespace-nowrap ${info.unavailable ? 'text-muted' : 'text-warning'}`}>{info.short}</span>
            </Hint>
          </li>
        )
      })}
    </ul>
  )
}

/** 最早到期的一批积分：「3 天后到期 120 分」（路由按最早到期优先选号，这里能看出为什么选它） */
export function EarliestExpiry({ a, now }: { a: Account; now: number }) {
  const at = timeMs(a.credits_earliest_expiry)
  if (at == null || at <= now || !a.credits_earliest_remaining) return null
  return <span className="text-xs text-muted">{until(at, now)}到期 {fmtTok(a.credits_earliest_remaining)} 分</span>
}

export function CreditsCell({ a, poolMax, now }: { a: Account; poolMax: number; now: number }) {
  const unlimited = isUnlimited(a)
  if (a.credits == null && !unlimited) return <span className="text-muted">—</span>
  const pct = creditPercent(a, poolMax)
  // 企业版的总额是分配给这个号的额度（企业池总额成员号查不到），悬停说明一下
  const tip = unlimited ? '企业版不限量' : a.enterprise ? '企业版：剩余额度 / 分配额度（按周期重置）' : undefined
  return (
    <Hint tip={tip} className="flex min-w-28 flex-col gap-1">
      <span className="font-mono text-sm tabular-nums">
        {unlimited ? '不限' : a.credits}
        {!unlimited && !!a.credits_total && <span className="text-xs text-muted">/{a.credits_total}</span>}
      </span>
      <ProgressBar aria-label={unlimited ? '不限量' : a.credits_total ? `剩余 ${pct}%` : '积分（相对池内最高）'} value={pct} size="sm" className="max-w-28">
        <ProgressBar.Track><ProgressBar.Fill /></ProgressBar.Track>
      </ProgressBar>
      <EarliestExpiry a={a} now={now} />
    </Hint>
  )
}

/** 最近一次请求的用量：次数 / token / 延迟 / 速率 */
export function UsageChips({ a }: { a: Account }) {
  const tu = a.token_usage || {}
  const total = formatTokenCount(tu.total_tokens)
  return (
    <div className="flex flex-wrap gap-1">
      <Chip size="sm" variant="soft" color="accent">{tu.request_count || 0} 次</Chip>
      <Chip size="sm" variant="soft" color="accent">{total}{total !== '—' && ' tok'}</Chip>
      <Chip size="sm" variant="soft" color="warning">{formatLatency(tu.last_latency_ms)}</Chip>
      <Chip size="sm" variant="soft" color="success">{formatRate(tu.last_tokens_per_second)}</Chip>
    </div>
  )
}
