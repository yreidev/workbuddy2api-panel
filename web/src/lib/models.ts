// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 模型列表的筛选、排序与实测上限关联（纯函数，见 models.test.ts）。
// 模型目录一次拉全（几十条），筛选排序都在前端做：改条件零延迟，也不会每调一次筛选就打一次上游。
import type { Model, ModelLockRow, Probe, RequestEvent } from './types'

export type RealmFilter = 'all' | 'cn' | 'global'
export type Capability = 'tools' | 'vision' | 'reasoning' | 'default'
/** any = 有可调档位（任意一档）；off = 可以关闭思考 */
export type EffortFilter = '' | 'any' | 'off' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'
/** promo = 有折扣或限时免费；free = 限时免费；discount = 打折但不免费 */
export type PromoFilter = '' | 'promo' | 'free' | 'discount'
export type ModelSort = 'default' | 'rate' | 'context' | 'output' | 'name'

export interface ModelFilter {
  /** 搜 ID / 名称 / 厂商 / 描述 / 标签，空格分词后逐个 AND（如「cn 视觉」） */
  q: string
  realm: RealmFilter
  /** 多选，全部满足才算 */
  caps: Capability[]
  effort: EffortFilter
  promo: PromoFilter
}

export const EMPTY_MODEL_FILTER: ModelFilter = { q: '', realm: 'all', caps: [], effort: '', promo: '' }

/** 模型 id 带域前缀（cn:glm-5.2 / global:gpt-5），就是调用时要填的完整 model 值 */
export const modelRealm = (id: string) => (id.startsWith('global:') ? 'global' : id.startsWith('cn:') ? 'cn' : '')

export function hasCapability(m: Model, c: Capability): boolean {
  if (c === 'tools') return !!m.supports_tool_call
  if (c === 'vision') return !!m.supports_images
  if (c === 'reasoning') return !!m.supports_reasoning
  return !!m.is_default
}

function searchText(m: Model): string {
  return [m.id, m.name, m.vendor, m.description, (m.tags || []).join(' '), m.enterprise_only ? '企业专属' : ''].filter(Boolean).join(' ').toLowerCase()
}

export function matchModel(m: Model, f: ModelFilter): boolean {
  if (f.realm !== 'all' && modelRealm(m.id) !== f.realm) return false
  if (f.caps.some((c) => !hasCapability(m, c))) return false
  const efforts = m.supported_efforts || []
  if (f.effort === 'any' && !efforts.length) return false
  if (f.effort === 'off' && !m.can_disable_thinking) return false
  if (f.effort && f.effort !== 'any' && f.effort !== 'off' && !efforts.includes(f.effort)) return false
  const factor = m.promo_factor == null ? null : Number(m.promo_factor)
  if (f.promo === 'promo' && factor == null && !m.promo_label) return false
  if (f.promo === 'free' && factor !== 0) return false
  if (f.promo === 'discount' && !(factor != null && factor > 0)) return false
  const text = searchText(m)
  return f.q.toLowerCase().split(/\s+/).filter(Boolean).every((kw) => text.includes(kw))
}

/** 当前生效的积分倍率：优先促销价（限时免费 = 0）；没有倍率记为 Infinity 排到最后，不冒充最便宜 */
export function rateValue(m: Model): number {
  const raw = m.promo_credits != null && m.promo_credits !== '' ? m.promo_credits : m.credits
  const n = parseFloat(String(raw ?? '').replace(/[^\d.]/g, ''))
  return Number.isFinite(n) ? n : Infinity
}

const num = (v?: number) => (Number.isFinite(Number(v)) ? Number(v || 0) : 0)

/** 排序返回新数组；default 保持上游原始顺序 */
export function sortModels(list: Model[], sort: ModelSort): Model[] {
  const out = [...list]
  if (sort === 'rate') out.sort((a, b) => rateValue(a) - rateValue(b))
  else if (sort === 'context') out.sort((a, b) => num(b.context_length) - num(a.context_length))
  else if (sort === 'output') out.sort((a, b) => num(b.max_output_tokens) - num(a.max_output_tokens))
  else if (sort === 'name') out.sort((a, b) => a.id.localeCompare(b.id))
  return out
}

export const filterModels = (list: Model[], f: ModelFilter, sort: ModelSort = 'default') =>
  sortModels(list.filter((m) => matchModel(m, f)), sort)

/** 探测键带域前缀（cn:glm-5.2），按「精确命中或 :后缀」关联到模型（与旧版口径一致） */
export function probeFor(probes: Record<string, Probe>, id: string): Probe | undefined {
  if (probes[id]) return probes[id]
  const key = Object.keys(probes).find((k) => k.endsWith(':' + id))
  return key ? probes[key] : undefined
}

/** 读配置里的 models.hidden：缺失或形状不对 → 空表，非字符串的项丢掉 */
export function hiddenFromConfig(cfg: Record<string, unknown>): string[] {
  const models = cfg.models
  if (!models || typeof models !== 'object') return []
  const raw = (models as Record<string, unknown>).hidden
  return Array.isArray(raw) ? raw.filter((v): v is string => typeof v === 'string') : []
}

/** models.hidden 归一成带域前缀的全名集合，口径同网关 livecfg.NormalizeHiddenModels：
 *  带 cn: / global: 前缀的只藏那个域，裸名两个域都藏；精确匹配、区分大小写 */
export function hiddenModelSet(hidden: string[]): Set<string> {
  const out = new Set<string>()
  for (const raw of hidden) {
    const m = raw.trim()
    if (!m) continue
    const i = m.indexOf(':')
    const prefix = i < 0 ? '' : m.slice(0, i)
    if (prefix === 'cn' || prefix === 'global') {
      out.add(m)
    } else {
      out.add('cn:' + m)
      out.add('global:' + m)
    }
  }
  return out
}

/** 往隐藏名单里加一项：去两端空白，空的不加，已有的（原样相同）不重复加 */
export function addHiddenModel(list: string[], raw: string): string[] {
  const m = raw.trim()
  if (!m || list.includes(m)) return list
  return [...list, m]
}

/** 模型当前可用性（模型页的「状态」列） */
export interface ModelAvailability {
  label: string
  color: 'success' | 'warning' | 'danger' | 'default'
  /** 悬停说明：依据和时间 */
  tip: string
}

/** 按模型锁和最近一次到过上游的真实请求判模型能不能用，两个域都判；不另发推理请求（照上游 74f8583）。
 *  模型锁优先；没有锁就看最近的请求（recent 最新的在前；没发到上游的，比如密钥白名单、隐藏名单拦下的，
 *  不算数）；都没有是「待验证」：目录里有不代表号能用。id 不带域前缀时返回 null。 */
export function modelAvailability(
  id: string, locks: ModelLockRow[], recent: RequestEvent[], fmtTime: (iso: string) => string,
): ModelAvailability | null {
  const i = id.indexOf(':')
  const realm = i < 0 ? '' : id.slice(0, i)
  if (realm !== 'cn' && realm !== 'global') return null
  const bare = id.slice(i + 1)
  const lock = locks.find((r) => r.realm === realm && r.model === bare)
  if (lock) {
    const reason = (lock.reason || '').trim()
    const why = reason ? '；上游：' + reason : ''
    if (lock.state === 'partial') {
      return { label: '部分可用', color: 'warning', tip: `还有 ${lock.servable} 个号能用，${lock.locked} 个号被这个模型的限流挡住${why}` }
    }
    if (lock.state === 'starved') {
      return { label: '暂无可用号', color: 'default', tip: '没有号能用这个模型，但不是这个模型被限流：看看账号冷却和在途占用' }
    }
    const lower = reason.toLowerCase()
    if (lower.startsWith('11158') || lower.includes('not allowed for user type')) {
      return { label: '无权限', color: 'danger', tip: '这一类账号不能用这个模型' + why }
    }
    if (lower.startsWith('11102') || lower.includes('service info not found')) {
      return { label: '上游无此模型', color: 'danger', tip: '目录里还列着，实际请求回模型不存在' + why }
    }
    return { label: '限流中', color: 'warning', tip: '所有号都被这个模型的限流挡住' + why }
  }
  const ev = recent.find((e) => e.model === id && (e.attempts ?? 0) > 0)
  if (!ev) return { label: '待验证', color: 'default', tip: '没有模型锁，最近的请求里也没用过它；目录里有不代表号能用' }
  const when = ev.time ? ' · ' + fmtTime(ev.time) : ''
  const err = ev.error ? '：' + ev.error : ''
  if (ev.outcome === 'success' || (ev.status >= 200 && ev.status < 300)) {
    return { label: '可用', color: 'success', tip: '最近一次真实请求成功' + when }
  }
  if (ev.status === 429) return { label: '限流', color: 'warning', tip: '最近一次请求被限流' + when + err }
  if (ev.status >= 500) return { label: '上游错误', color: 'danger', tip: `最近一次请求上游出错 HTTP ${ev.status}${when}${err}` }
  return { label: '最近失败', color: 'danger', tip: `最近一次请求失败 HTTP ${ev.status || '—'}${when}${err}` }
}
