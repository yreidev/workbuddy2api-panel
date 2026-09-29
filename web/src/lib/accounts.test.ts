import { describe, expect, it } from 'vitest'
import { accountHealth, activeRateLimits, creditPercent, filterAccounts, rateLimitInfo, sortAccounts } from './accounts'
import type { Account } from './types'

const base: Account = {
  uid: 'u1', credits: 50, cooling: false, disabled: false, consecutive_fails: 0, in_flight: 0, breaker_fails: 0,
}
const now = Date.parse('2026-09-28T12:00:00Z')
const iso = (ms: number) => new Date(ms).toISOString()

describe('accountHealth', () => {
  it('禁用优先于冷却', () => {
    expect(accountHealth({ ...base, disabled: true, cool_remaining_sec: 60 }, now).state).toBe('disabled')
  })
  it('零值时间不算冷却', () => {
    expect(accountHealth({ ...base, breaker_until: '0001-01-01T00:00:00Z' }, now)).toEqual({ state: 'ok', coolEnd: null, kind: '可用' })
  })
  it('取三种冷却里最晚的截止时刻，并按来源命名', () => {
    expect(accountHealth({ ...base, cool_remaining_sec: 60 }, now)).toEqual({ state: 'cooling', coolEnd: now + 60_000, kind: '限流冷却' })
    expect(accountHealth({ ...base, cool_remaining_sec: 60, cool_kind: 'hard_credit' }, now).kind).toBe('积分冷却')
    expect(accountHealth({ ...base, cool_remaining_sec: 60, breaker_until: iso(now + 120_000) }, now).kind).toBe('熔断')
    expect(accountHealth({ ...base, cool_remaining_sec: 60, degrade_until: iso(now + 90_000) }, now).kind).toBe('连败降权')
  })
})

describe('账号表辅助', () => {
  it('限额台账原样展示后端返回的条目，只去掉缺模型名的', () => {
    const a = { ...base, rate_limited_models: [{ model: '' }, { model: 'y', until: iso(now + 1000) }, { model: 'z', kind: 'model_unavailable' }] }
    expect(activeRateLimits(a).map((m) => m.model)).toEqual(['y', 'z'])
  })
  it('积分百分比：有总额用总额，没有按池内最高', () => {
    expect(creditPercent({ ...base, credits: 25, credits_total: 100 }, 999)).toBe(25)
    expect(creditPercent({ ...base, credits: 25 }, 50)).toBe(50)
  })
  it('按状态和关键字筛选、按列排序', () => {
    const list = [
      { ...base, uid: 'aaa', nickname: '甲', credits: 10, err_total: 3 },
      { ...base, uid: 'bbb', nickname: '乙', credits: 30, disabled: true },
      { ...base, uid: 'ccc', credits: 20, cool_remaining_sec: 30 },
    ]
    expect(filterAccounts(list, now, 'cooling', '').map((a) => a.uid)).toEqual(['ccc'])
    expect(filterAccounts(list, now, 'all', '乙').map((a) => a.uid)).toEqual(['bbb'])
    expect(sortAccounts(list, 'credits', true).map((a) => a.uid)).toEqual(['bbb', 'ccc', 'aaa'])
    expect(sortAccounts(list, null, false)).toBe(list)
  })
})

// 从上游 internal/panel/frontend_test.go 的 TestAppJSRateLimitMeta 平移（期望值原样保留）：
// 同时支持上游 reset_at、网关 until 和两者都没有三种形态，以及模型不可用。
describe('rateLimitInfo', () => {
  const t = (h: number) => new Date(2026, 8, 28, h, 0, 0).getTime()
  const at = t(14)
  it('上游重置时间 + 网关更早重试', () => {
    const r = rateLimitInfo({ model: 'glm-5.3', kind: 'rate_limit', reset_at: new Date(t(16)).toISOString(), until: new Date(t(15)).toISOString() }, at)
    expect(r.detail).toBe('预计 2026-09-28 16:00 解封（剩余 2时00分） · 网关最快 1时00分 后重试')
    expect(r.short).toBe('2时00分后解封')
    expect(r.unavailable).toBe(false)
  })
  it('模型不可用', () => {
    const r = rateLimitInfo({ model: 'missing', kind: 'model_unavailable', until: new Date(t(15)).toISOString() }, at)
    expect(r.detail).toBe('预计 1时00分 后重试')
    expect(r.short).toBe('不可用 · 1时00分后重试')
    expect(r.unavailable).toBe(true)
    expect(rateLimitInfo({ model: 'missing', kind: 'model_unavailable' }, at).detail).toBe('等待重新探测')
  })
  it('没有任何时间', () => {
    expect(rateLimitInfo({ model: 'glm-5.3', kind: 'rate_limit' }, at).detail).toBe('预计解封时间未知')
  })
  it('只有网关重试时间；Go 零值时间当作没有', () => {
    const r = rateLimitInfo({ model: 'glm-5.3', until: new Date(t(15)).toISOString(), reset_at: '0001-01-01T00:00:00Z' }, at)
    expect(r.detail).toBe('预计 2026-09-28 15:00 恢复（剩余 1时00分）')
  })
})
