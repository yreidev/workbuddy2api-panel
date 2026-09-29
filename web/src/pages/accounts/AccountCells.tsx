// 账号的各个展示单元（名称、状态、积分、用量），账号表、手机卡片、详情抽屉共用。
import { Chip, ProgressBar } from '@heroui/react'
import { activeRateLimits, creditPercent, rateLimitInfo, type Health } from '../../lib/accounts'
import { dur, formatLatency, formatRate, formatTokenCount, fmtTok, timeMs, until } from '../../lib/format'
import type { Account } from '../../lib/types'

export function AccountName({ a }: { a: Account }) {
  return (
    <div className="min-w-0">
      <div className="flex items-center gap-1.5">
        <span className={`truncate font-medium ${a.nickname ? '' : 'text-muted'}`}>{a.nickname || '未命名'}</span>
        {a.realm === 'global' && <Chip size="sm" variant="soft" color="accent">国际版</Chip>}
      </div>
      <div className="truncate font-mono text-xs text-muted">{a.uid.length > 16 ? a.uid.slice(0, 16) + '…' : a.uid}</div>
    </div>
  )
}

export function HealthChip({ health, now }: { health: Health; now: number }) {
  if (health.state === 'disabled') return <Chip size="sm" variant="soft" color="danger">已禁用</Chip>
  if (health.state === 'cooling') {
    return <Chip size="sm" variant="soft" color="warning">{health.kind} · {dur(((health.coolEnd ?? now) - now) / 1000)}</Chip>
  }
  return <Chip size="sm" variant="soft" color="success">可用</Chip>
}

export function StatusCell({ a, health, now }: { a: Account; health: Health; now: number }) {
  return (
    <div className="flex min-w-40 flex-col items-start gap-1">
      <HealthChip health={health} now={now} />
      {a.reason && <span className="line-clamp-2 max-w-40 text-xs text-muted">{a.reason}</span>}
      <RateLimitList a={a} now={now} />
    </div>
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
          <li key={row.model} title={info.model + '\n' + info.detail} className="flex flex-wrap items-baseline gap-x-1.5">
            <span className={`size-1.5 shrink-0 translate-y-[-1px] rounded-full ${info.unavailable ? 'bg-muted' : 'bg-warning'}`} />
            <span className="break-all font-mono">{info.model}</span>
            <span className={`whitespace-nowrap ${info.unavailable ? 'text-muted' : 'text-warning'}`}>{info.short}</span>
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
  if (a.credits == null) return <span className="text-muted">—</span>
  const pct = creditPercent(a, poolMax)
  return (
    <div className="flex min-w-28 flex-col gap-1">
      <span className="font-mono text-sm tabular-nums">
        {a.credits}
        {!!a.credits_total && <span className="text-xs text-muted">/{a.credits_total}</span>}
      </span>
      <ProgressBar aria-label={a.credits_total ? `剩余 ${pct}%` : '积分（相对池内最高）'} value={pct} size="sm" className="max-w-28">
        <ProgressBar.Track><ProgressBar.Fill /></ProgressBar.Track>
      </ProgressBar>
      <EarliestExpiry a={a} now={now} />
    </div>
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
