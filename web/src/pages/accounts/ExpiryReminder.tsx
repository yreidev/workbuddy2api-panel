// 积分到期提醒：每个账号最近一批要过期的积分、到期前日均至少要耗多少。数据与积分构成页共用一份缓存
// （逐账号实时查上游，两分钟内不重复查），走缓存时标出数据是几分钟前的，免得把旧数据当实时。
import { useMemo } from 'react'
import { fmtTok } from '../../lib/format'
import { useNow } from '../../lib/hooks'
import { expiryReminder, expiryTone, type ExpiryReminder as Reminder } from '../../lib/packages'
import { usePackages } from '../../lib/queries'
import type { PackageAccount } from '../../lib/types'
import { BusyButton, Empty, Loaded } from '../../components/Feedback'
import { InfoTip, Panel } from '../../components/Panel'

export function ExpiryReminder() {
  const query = usePackages()
  const now = useNow(60_000)
  const n = query.data?.accounts?.length ?? 0
  const age = query.dataUpdatedAt ? Math.floor((now - query.dataUpdatedAt) / 60_000) : 0
  return (
    <Panel
      title={
        <span className="flex items-center gap-1">
          积分到期提醒
          <InfoTip label="到期提醒说明">
            签到、任务发的积分包约一个月失效，用不完的部分到期直接作废。这里按每个账号最近要到期的一批估算：
            日均需耗 = 该批剩余 ÷ 距到期天数。上游按失效时刻先后优先扣减（FEFO），快过期的批次本来就最先被用掉。
          </InfoTip>
        </span>
      }
      desc={!query.data ? '逐账号向上游实时查询' : n + ' 个账号 · ' + (age > 0 ? age + ' 分钟前的数据，可点「检查」刷新' : '实时查询上游')}
      actions={<BusyButton size="sm" variant="secondary" busy={query.isFetching} onPress={() => void query.refetch()}>检查</BusyButton>}
    >
      <Loaded query={query} rows={2}>{(d) => <Rows list={d.accounts || []} now={now} />}</Loaded>
    </Panel>
  )
}

const DOT = { danger: 'bg-danger', warning: 'bg-warning', success: 'bg-success' } as const

const dayWord = (days: number) => (days === 0 ? '今天到期' : days === 1 ? '明天到期' : days + ' 天后到期')

// 最急的排前面：有到期批次的按剩余天数，其次没有会到期积分的，查询失败的垫底
function Rows({ list, now }: { list: PackageAccount[]; now: number }) {
  const rows = useMemo(() => {
    const rank = (a: PackageAccount, r: Reminder | null) => (a.error ? 2 : r ? 0 : 1)
    return list
      .map((a) => ({ a, r: a.error ? null : expiryReminder(a.packages, now) }))
      .sort((x, y) => rank(x.a, x.r) - rank(y.a, y.r) || (x.r && y.r ? x.r.first.days - y.r.first.days || y.r.first.remain - x.r.first.remain : 0))
  }, [list, now])
  if (!rows.length) return <Empty title="没有账号" />
  return (
    <div className="max-h-80 divide-y divide-separator overflow-y-auto overscroll-contain">
      {rows.map(({ a, r }) => (
        <div key={a.uid} className="flex items-start gap-3 py-2 text-sm">
          <span className={`mt-1.5 size-2 shrink-0 rounded-full ${a.error ? 'bg-muted' : DOT[r ? expiryTone(r.first.days) : 'success']}`} />
          <div className="flex min-w-0 flex-1 flex-col gap-0.5 sm:flex-row sm:gap-3">
            <span className="shrink-0 truncate font-medium sm:w-36">{a.nickname || a.uid.slice(0, 8)}</span>
            {a.error ? (
              <span className="text-danger">查询失败：{a.error}</span>
            ) : !r ? (
              <span className="text-muted">没有会到期的积分</span>
            ) : (
              <div className="min-w-0">
                <span>
                  最近到期 <b className="font-mono tabular-nums">{r.first.date}</b>（{dayWord(r.first.days)}）· 该批{' '}
                  <b className="font-mono tabular-nums">{fmtTok(r.first.remain)}</b> 积分 · 到期前日均需耗 ≥
                  <b className="font-mono tabular-nums">{fmtTok(r.daily)}</b>
                  {r.week > r.first.remain && <> · 7 天内合计 {fmtTok(r.week)}</>}
                </span>
                {r.next.length > 0 && (
                  <div className="text-xs text-muted">
                    {r.next.map((b) => '随后 ' + b.date.slice(5) + ' · ' + fmtTok(b.remain)).join('　')}
                    {r.count > 4 && '　等 ' + r.count + ' 批'}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      ))}
    </div>
  )
}
