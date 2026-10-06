// 模型锁池的展示口径（纯函数，见 modellocks.test.ts）。后端已排好序，这里只做文案。
import { dur, timeMs } from './format'
import type { ModelLockRow } from './types'

export const LOCK_STATES: Record<string, { label: string; color: 'danger' | 'warning' }> = {
  locked: { label: '整池不可用', color: 'danger' },
  starved: { label: '没号可用', color: 'warning' },
  partial: { label: '部分限流', color: 'warning' },
}

export function lockState(state: string): { label: string; color: 'danger' | 'warning' | 'default' } {
  return LOCK_STATES[state] ?? { label: state || '—', color: 'default' }
}

/** 距某时刻还有多久；没有时间（含 Go 零值）显示「—」 */
export function lockLeft(iso: string | undefined, now: number): string {
  const t = timeMs(iso)
  return t == null ? '—' : dur(Math.max(0, (t - now) / 1000))
}

/** 最早解锁：第一个被锁账号恢复的时刻，没有就用全池解锁时刻 */
export const earliestUnlock = (r: ModelLockRow) => (timeMs(r.unlock_at) != null ? r.unlock_at : r.fully_unlock_at)

/** 标题旁的一句话：整池不可用（含没号可用）优先，否则说几个模型部分限流 */
export function lockSummary(rows: ModelLockRow[]): { text: string; bad: boolean } {
  if (!rows.length) return { text: '', bad: false }
  const bad = rows.filter((r) => r.state === 'locked' || r.state === 'starved').length
  return bad ? { text: bad + ' 个模型整池不可用', bad: true } : { text: rows.length + ' 个模型部分限流', bad: false }
}
