// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 积分获取页（/panel/api/credit_log）的展示口径：来源的中文名、任务码与档位的说法、查询参数（纯函数，见 credits.test.ts）。
import { growthTitles } from './tasks'
import { rangeQuery, type TimeRange } from './timerange'
import type { CreditEarning, EarnSource } from './types'

export const EARN_LABEL: Record<EarnSource, string> = {
  checkin: '每日签到',
  task: '成长任务',
  compensation: '积分补偿',
  travel: '猫猫旅行',
  gift: '新手礼包',
  redeem: '连登兑换',
}

/** 来源的中文名；不认识的（更新的网关多了来源）原样显示 */
export const earnLabel = (source: string) => EARN_LABEL[source as EarnSource] ?? source

/** 这一笔的说明：成长任务写任务名（本次打开面板后在任务中心扫描过才认得，否则写任务码），连登兑换写「连登 7 天」 */
export function earnDetail(e: Pick<CreditEarning, 'source' | 'detail'>): string {
  if (!e.detail) return ''
  if (e.source === 'task') return growthTitles.get(e.detail) || e.detail
  if (e.source === 'redeem' && /^\d+d$/.test(e.detail)) return '连登 ' + e.detail.slice(0, -1) + ' 天'
  return e.detail
}

/** 筛选条件 → 查询参数。流水是线性日志，时间范围和请求记录一样一律折算成 from/to */
export function creditLogQuery(range: TimeRange, uid: string, source: string, limit: number, now = Date.now()): URLSearchParams {
  const q = rangeQuery(range, false, now)
  if (uid) q.set('uid', uid)
  if (source) q.set('source', source)
  q.set('limit', String(limit))
  return q
}
