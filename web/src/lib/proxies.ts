// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 代理池的展示、筛选和表单换算（纯函数，见 proxies.test.ts）。规则见 docs/proxy.md。
import { formatLatency } from './format'
import type { ProxyBindResult, ProxyExit, ProxyItem, ProxyLine, ProxyPool, ProxySettings, ProxyWarnings } from './types'

/** 地区和运营商连成一句：「中国 香港 · 某运营商」（同 internal/proxies 的 Exit.Geo） */
export function exitGeo(e?: ProxyExit | null): string {
  if (!e) return ''
  const parts: string[] = []
  for (const s of [e.country, e.region, e.city]) if (s && !parts.includes(s)) parts.push(s)
  const geo = parts.join(' ')
  return e.isp ? (geo ? geo + ' · ' : '') + e.isp : geo
}

export interface ProxyStatus {
  label: string
  color: 'success' | 'danger' | 'default'
  /** 不通的原因 */
  detail?: string
}

/** 代理状态：已到期优先，其次测出来的通不通，没测过是「还没测」 */
export function proxyStatus(p: ProxyItem): ProxyStatus {
  if (p.expired) return { label: '已到期', color: 'danger' }
  if (p.health.state === 'down') return { label: '不通', color: 'danger', detail: p.health.error }
  if (p.health.state === 'ok') return { label: '通' + (p.health.latency_ms ? ' · ' + formatLatency(p.health.latency_ms) : ''), color: 'success' }
  return { label: '还没测', color: 'default' }
}

export type ProxyFilter = 'all' | 'usable' | 'down' | 'expired' | 'expiring' | 'untested'

export const PROXY_FILTERS: { id: ProxyFilter; label: string }[] = [
  { id: 'all', label: '全部' },
  { id: 'usable', label: '能用' },
  { id: 'down', label: '不通' },
  { id: 'expired', label: '已到期' },
  { id: 'expiring', label: '快到期' },
  { id: 'untested', label: '还没测' },
]

const matchFilter = (p: ProxyItem, f: ProxyFilter) => {
  switch (f) {
    case 'all': return true
    case 'usable': return p.usable
    case 'down': return !p.expired && p.health.state === 'down'
    case 'expired': return !!p.expired
    case 'expiring': return !!p.expiring
    case 'untested': return !p.expired && !p.health.state
  }
}

/** 按池（空 = 全部池）、状态、关键字（名字、地址、出口 IP、地区）筛选 */
export function filterProxies(list: ProxyItem[], pool: string, f: ProxyFilter, q: string): ProxyItem[] {
  const kw = q.trim().toLowerCase()
  return list.filter((p) => {
    if (pool && p.pool !== pool) return false
    if (!matchFilter(p, f)) return false
    if (!kw) return true
    return [p.name, p.url, p.health.exit?.ip, exitGeo(p.health.exit)].some((s) => (s || '').toLowerCase().includes(kw))
  })
}

/** 这个域新号的默认池；没有返回 undefined */
export const defaultPoolOf = (pools: ProxyPool[], realm: string) => pools.find((p) => p.default_for?.includes(realm))

/** 默认池的标签：「国内版默认」「国际版默认」 */
export const defaultForLabels = (p: Pick<ProxyPool, 'default_for'>) =>
  (p.default_for || []).map((r) => (r === 'global' ? '国际版默认' : '国内版默认'))

/** 绑号数：「3 / 5 个号」（池有上限）或「3 个号」 */
export function loadText(p: ProxyItem, pool?: ProxyPool): string {
  const n = p.accounts.length
  return pool?.max_per_proxy ? `${n} / ${pool.max_per_proxy} 个号` : `${n} 个号`
}

/** 选池的下拉里一项的说明：「2 / 3 个代理能用 · 每个最多 5 个号」 */
export function poolSummary(p: ProxyPool): string {
  return `${p.usable} / ${p.proxies} 个代理能用 · ` + (p.max_per_proxy ? `每个最多 ${p.max_per_proxy} 个号` : '每个不限号数')
}

/** 导入预览一行的结果说明 */
export function lineResult(l: ProxyLine): { ok: boolean; text: string } {
  if (l.error) return { ok: false, text: l.error }
  if (l.duplicate === 'existing') return { ok: false, text: `和已有的代理「${l.duplicate_of}」重复` }
  if (l.duplicate === 'batch') return { ok: false, text: `和第 ${l.duplicate_of} 行重复` }
  return { ok: true, text: '可以导入' }
}

/** 导入预览的计数：能导入几行、认不出几行、重复几行 */
export function lineCounts(lines: ProxyLine[]) {
  const c = { ok: 0, error: 0, duplicate: 0 }
  for (const l of lines) {
    if (l.error) c.error++
    else if (l.duplicate) c.duplicate++
    else c.ok++
  }
  return c
}

/** 要提醒的事，一条一句（代理页和账号池页的提醒共用）；没有要提醒的返回空数组 */
export function warningLines(w?: ProxyWarnings | null): string[] {
  if (!w) return []
  return [
    w.down && `${w.down} 个代理连不上`,
    w.expired && `${w.expired} 个代理已到期`,
    w.expiring && `${w.expiring} 个代理快到期`,
    w.waiting && `${w.waiting} 个号在等代理（池里没有空位）`,
  ].filter((s): s is string => !!s)
}

/** 批量绑定的结果归总：绑上几个、在等代理几个、没绑成的原因 */
export function bindSummary(results: ProxyBindResult[], name: (uid: string) => string) {
  let bound = 0, waiting = 0, unbound = 0
  const errors: string[] = []
  for (const r of results) {
    if (r.error) errors.push(name(r.uid) + '：' + r.error)
    else if (r.waiting) waiting++
    else if (r.proxy) bound++
    else unbound++
  }
  return { bound, waiting, unbound, errors }
}

/** 设置表单：查出口 IP 的地址在文本框里一行一个 */
export interface SettingsForm extends Omit<ProxySettings, 'exit_ip_urls'> {
  exit_ip_urls: string
}

export const settingsForm = (s: ProxySettings): SettingsForm => ({ ...s, exit_ip_urls: (s.exit_ip_urls || []).join('\n') })

export const settingsInput = (f: SettingsForm): ProxySettings => ({
  ...f,
  exit_ip_urls: f.exit_ip_urls.split('\n').map((s) => s.trim()).filter(Boolean),
})

/** 设置的地址是不是 http:// 或 https:// 开头的完整地址（同网关的校验）；返回第一个不对的，都对返回空串 */
export function badExitURL(text: string): string {
  for (const s of text.split('\n').map((x) => x.trim()).filter(Boolean)) {
    let u: URL
    try {
      u = new URL(s)
    } catch {
      return s
    }
    if ((u.protocol !== 'http:' && u.protocol !== 'https:') || !u.host) return s
  }
  return ''
}
