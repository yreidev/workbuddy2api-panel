import { describe, expect, it } from 'vitest'
import { earliestUnlock, lockLeft, lockState, lockSummary } from './modellocks'
import type { ModelLockRow } from './types'

const now = Date.parse('2026-10-06T12:00:00Z')
const iso = (ms: number) => new Date(ms).toISOString()
const row = (state: string, extra: Partial<ModelLockRow> = {}): ModelLockRow => ({
  model: 'm', realm: 'cn', total: 3, servable: 0, locked: 3, state,
  unlock_at: iso(now + 60_000), fully_unlock_at: iso(now + 3_600_000), ...extra,
})

describe('模型锁池', () => {
  it('状态文案，未知状态原样显示', () => {
    expect(lockState('locked')).toEqual({ label: '整池不可用', color: 'danger' })
    expect(lockState('starved').label).toBe('没号可用')
    expect(lockState('partial').label).toBe('部分限流')
    expect(lockState('weird')).toEqual({ label: 'weird', color: 'default' })
  })
  it('倒计时：Go 零值时间显示「—」，已过去的按 0 算', () => {
    expect(lockLeft(iso(now + 90_000), now)).toBe('1分30秒')
    expect(lockLeft('0001-01-01T00:00:00Z', now)).toBe('—')
    expect(lockLeft(iso(now - 5000), now)).toBe('0秒')
  })
  it('最早解锁缺失时退回全池解锁', () => {
    expect(earliestUnlock(row('locked', { unlock_at: '0001-01-01T00:00:00Z' }))).toBe(iso(now + 3_600_000))
    expect(earliestUnlock(row('locked'))).toBe(iso(now + 60_000))
  })
  // 口径与上游旧面板 renderModelLocks 的 mlNote 一致：没号可用也算「整池不可用」
  it('标题摘要', () => {
    expect(lockSummary([])).toEqual({ text: '', bad: false })
    expect(lockSummary([row('locked'), row('starved'), row('partial')])).toEqual({ text: '2 个模型整池不可用', bad: true })
    expect(lockSummary([row('partial'), row('partial')])).toEqual({ text: '2 个模型部分限流', bad: false })
  })
})
