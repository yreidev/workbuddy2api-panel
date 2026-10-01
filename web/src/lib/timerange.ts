// 时间范围（用量页、请求记录共用）：预设 → 查询参数（纯函数，见 timerange.test.ts；口径与上游旧面板 trange* 一致）。
//
// 区间一律由浏览器算好再发：
//   - 「今天」必须是浏览器本地时区的 00:00 起。服务端时区未必与浏览器一致（容器常挂 TZ=Asia/Shanghai），
//     让服务端算「今天」跨时区时会切错日子；
//   - 「自定义」本来就是用户挑的具体时刻。
// 滚动预设（近 N 小时 / 天）在用量页仍发 hours：服务端按整点对齐的滚动窗口与旧行为逐位一致，
// 前端自己减 N 小时会多算或少算一个边界桶。请求记录是线性日志，一律折算成 from。

export type RangePreset = 'today' | '24' | '72' | '168' | '720' | '0' | 'custom'

export const RANGE_PRESETS: { id: RangePreset; label: string }[] = [
  { id: 'today', label: '今天' },
  { id: '24', label: '近 24 小时' },
  { id: '72', label: '近 3 天' },
  { id: '168', label: '近 7 天' },
  { id: '720', label: '近 30 天' },
  { id: '0', label: '全部历史' },
  { id: 'custom', label: '自定义' },
]

export interface TimeRange {
  preset: RangePreset
  /** 仅「自定义」用；本地时间 */
  from: Date | null
  to: Date | null
}

/** 今天 00:00（本地时区） */
export function midnight(now = new Date()): Date {
  const d = new Date(now)
  d.setHours(0, 0, 0, 0)
  return d
}

/** 「自定义」的起止给一段有意义的初值：今天 00:00 → 现在（精确到分钟） */
export function initialRange(preset: RangePreset): TimeRange {
  const to = new Date()
  to.setSeconds(0, 0)
  return { preset, from: midnight(), to }
}

const sec = (d: Date) => String(Math.floor(d.getTime() / 1000))

/**
 * 当前选择 → 查询参数。rolling=true：滚动预设发 hours，今天 / 自定义发 from/to；
 * rolling=false：一律发 from/to。「全部历史」都不发。
 */
export function rangeQuery(r: TimeRange, rolling: boolean, now = Date.now()): URLSearchParams {
  const q = new URLSearchParams()
  if (r.preset === 'custom') {
    if (r.from) q.set('from', sec(r.from))
    if (r.to) q.set('to', sec(r.to))
  } else if (r.preset === 'today') {
    q.set('from', sec(midnight(new Date(now))))
  } else if (r.preset !== '0') {
    if (rolling) q.set('hours', r.preset)
    else q.set('from', sec(new Date(now - Number(r.preset) * 3600_000)))
  }
  return q
}

const short = (d: Date | null) => d
  ? d.getMonth() + 1 + '-' + String(d.getDate()).padStart(2, '0') + ' ' +
    String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0')
  : '…'

/** 人读口径（用量总览的说明行回显当前区间） */
export function rangeLabel(r: TimeRange): string {
  if (r.preset !== 'custom') return RANGE_PRESETS.find((p) => p.id === r.preset)?.label ?? ''
  if (!r.from && !r.to) return '自定义'
  return short(r.from) + ' → ' + short(r.to)
}

/** 查询缓存键：只含用户的选择，不含「现在」——近 N 小时的起点在发请求时才算，否则每次渲染都是新键 */
export function rangeKey(r: TimeRange): string {
  return r.preset === 'custom' ? 'custom:' + (r.from?.getTime() ?? '') + '-' + (r.to?.getTime() ?? '') : r.preset
}
