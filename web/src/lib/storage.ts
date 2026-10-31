// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 日志留存（/panel/api/log_storage）的几类与展示口径：配置页「日志留存」每类一张卡片，下面显示占用并能清空。
import { fmtBytes } from './format'
import type { ArchiveStats, LogStorage, UsageStoreStats } from './types'

export type StoreKind = keyof LogStorage

export const STORE_LABEL: Record<StoreKind, string> = {
  requests: '请求记录',
  task: '任务日志',
  system: '系统日志',
  usage: '用量统计',
  credit: '积分获取',
}

/** 清空前的确认文案：说清删掉的是什么、哪些不受影响 */
export const CLEAR_NOTE: Record<StoreKind, string> = {
  requests: '删掉全部请求归档文件，含今天正在写的。请求记录页之后只剩新的请求；内存里的指标不受影响。',
  task: '删掉全部任务日志文件，含今天正在写的。运行日志页「最近 500 行」不受影响。',
  system: '删掉全部系统日志文件，含今天正在写的。运行日志页「最近 500 行」不受影响。',
  usage: '清空全部用量明细，用量页从零开始。客户端密钥的每天、总量限额另有计数，不受影响。',
  credit: '删掉全部积分获取流水，含今天正在写的。积分获取页之后只剩新领到的；账号余额和积分构成不受影响。',
}

/** 保留天数的说法：0 = 永久保留 */
export const retentionText = (days?: number) => (days ? '保留 ' + days + ' 天' : '永久保留')

/** 「10-21」：最早一条的日期，省掉年份 */
const shortDay = (day?: string) => (day ? day.slice(5) : '')

/** 一类日志现在的占用：「3 个文件 · 1.2 MiB · 最早 10-21」；还没有文件时「还没有记录」 */
export function storageSummary(kind: StoreKind, st: ArchiveStats | UsageStoreStats | null | undefined): string {
  if (!st) return '当前网关不支持'
  if (kind === 'usage') {
    const u = st as UsageStoreStats
    if (!u.buckets) return '还没有用量'
    return [u.buckets.toLocaleString('zh-CN') + ' 条明细', fmtBytes(u.bytes), u.oldest_day && '最早 ' + shortDay(u.oldest_day)].filter(Boolean).join(' · ')
  }
  const a = st as ArchiveStats
  if (!a.files) return '还没有记录'
  return [a.files + ' 个文件', fmtBytes(a.bytes), a.oldest_day && '最早 ' + shortDay(a.oldest_day)].filter(Boolean).join(' · ')
}

/** 归档的异常：写不进磁盘的错误、队列满了丢掉的条数；没有异常返回空串 */
export function storageProblem(st: ArchiveStats | UsageStoreStats | null | undefined): string {
  if (!st || !('enabled' in st)) return ''
  return [st.last_error && '错误：' + st.last_error, st.dropped_writes ? '丢弃 ' + st.dropped_writes + ' 条' : ''].filter(Boolean).join(' · ')
}
