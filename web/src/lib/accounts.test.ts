// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it } from 'vitest'
import {
  accountHealth, accountName, activeRateLimits, batchParam, batchSummary, blockedLabel, cardSortKey, creditPercent, creditsText, filterAccounts, groupLine, isFrozen,
  livenessModels, livenessSummary, namesOf, NO_GROUP, notesOf, parseCardSort, poolCredits, rateLimitInfo, relabel, sortAccounts,
} from './accounts'
import type { Account, Model } from './types'

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
  it('暂停选号：禁用优先；没冷却时只是暂停，冷却中照样带上冷却截止（按钮据此给「解冻」）', () => {
    expect(accountHealth({ ...base, disabled: true, paused: true }, now).state).toBe('disabled')
    expect(accountHealth({ ...base, paused: true }, now)).toEqual({ state: 'paused', coolEnd: null, kind: '已暂停选号' })
    expect(accountHealth({ ...base, paused: true, cool_remaining_sec: 60 }, now)).toEqual({ state: 'paused', coolEnd: now + 60_000, kind: '限流冷却' })
    expect(isFrozen(accountHealth({ ...base, paused: true }, now))).toBe(false)
    expect(isFrozen(accountHealth({ ...base, paused: true, cool_remaining_sec: 60 }, now))).toBe(true)
    expect(isFrozen(accountHealth({ ...base, disabled: true }, now))).toBe(true)
    expect(isFrozen(accountHealth(base, now))).toBe(false)
  })
  it('被代理拦着：排在禁用、暂停选号之后，冷却之前；冷却中照样带上冷却截止', () => {
    const reason = '等代理：池「国内」里没有可用的空位'
    expect(accountHealth({ ...base, proxy_blocked: reason }, now)).toEqual({ state: 'blocked', coolEnd: null, kind: '等代理', proxy: '等代理' })
    expect(accountHealth({ ...base, proxy_blocked: reason, cool_remaining_sec: 60 }, now))
      .toEqual({ state: 'blocked', coolEnd: now + 60_000, kind: '限流冷却', proxy: '等代理' })
    expect(accountHealth({ ...base, proxy_blocked: reason, paused: true }, now)).toEqual({ state: 'paused', coolEnd: null, kind: '已暂停选号', proxy: '等代理' })
    expect(accountHealth({ ...base, proxy_blocked: reason, disabled: true }, now).state).toBe('disabled')
    expect(isFrozen(accountHealth({ ...base, proxy_blocked: reason }, now))).toBe(false)
  })
  it('代理拦截原因归成短说明（文案同 internal/proxies 的 gateLocked）', () => {
    expect(blockedLabel('没绑代理（开着「只许走号的代理」）')).toBe('没绑代理')
    expect(blockedLabel('等代理：池「国内」里没有可用的空位')).toBe('等代理')
    expect(blockedLabel('代理「甲」已到期')).toBe('代理到期')
    expect(blockedLabel('代理「已到期」连不上（11-01 09:30 起）')).toBe('代理不通')
    expect(blockedLabel('别的原因')).toBe('因代理暂停')
  })
  it('取三种冷却里最晚的截止时刻，并按来源命名', () => {
    expect(accountHealth({ ...base, cool_remaining_sec: 60 }, now)).toEqual({ state: 'cooling', coolEnd: now + 60_000, kind: '限流冷却' })
    expect(accountHealth({ ...base, cool_remaining_sec: 60, cool_kind: 'hard_credit' }, now).kind).toBe('积分冷却')
    expect(accountHealth({ ...base, cool_remaining_sec: 60, cool_kind: 'restriction_wave' }, now).kind).toBe('风控暂停')
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
  it('企业版不限量（credits_total=-1）：显示「不限」、进度满，不计入池内合计和最高余额', () => {
    const unlimited = { ...base, uid: 'ent', enterprise: true, credits: 2 ** 40, credits_total: -1 }
    expect(creditPercent(unlimited, 50)).toBe(100)
    expect(creditsText(unlimited.credits, unlimited.credits_total)).toBe('不限')
    expect(creditsText(120, 500)).toBe('120 / 500')
    expect(creditsText(120, 0)).toBe('120')
    expect(creditsText(null, undefined)).toBe('—')
    const pool = poolCredits([{ ...base, credits: 30, credits_total: 100 }, { ...base, credits: 80 }, unlimited])
    expect(pool).toEqual({ remain: 110, total: 100, unlimited: 1, max: 80 })
  })
  it('按状态和关键字筛选、按列排序', () => {
    const list = [
      { ...base, uid: 'aaa', nickname: '甲', credits: 10, err_total: 3 },
      { ...base, uid: 'bbb', nickname: '乙', credits: 30, disabled: true },
      { ...base, uid: 'ccc', credits: 20, cool_remaining_sec: 30 },
    ]
    expect(filterAccounts(list, now, 'cooling', '').map((a) => a.uid)).toEqual(['ccc'])
    // 与后端总览计数同口径（上游 issue #125）：暂停选号单独一栏（哪怕在冷却），又禁用又暂停的算禁用
    const withPaused = [...list, { ...base, uid: 'ddd', paused: true, cool_remaining_sec: 30 }, { ...base, uid: 'eee', paused: true, disabled: true }]
    expect(filterAccounts(withPaused, now, 'disabled', '').map((a) => a.uid)).toEqual(['bbb', 'eee'])
    expect(filterAccounts(withPaused, now, 'paused', '').map((a) => a.uid)).toEqual(['ddd'])
    expect(filterAccounts(withPaused, now, 'cooling', '').map((a) => a.uid)).toEqual(['ccc'])
    // 被代理拦着的号也单独一栏（哪怕在冷却），不算进冷却
    const withBlocked = [...withPaused, { ...base, uid: 'fff', proxy_blocked: '代理「甲」已到期', cool_remaining_sec: 30 }]
    expect(filterAccounts(withBlocked, now, 'blocked', '').map((a) => a.uid)).toEqual(['fff'])
    expect(filterAccounts(withBlocked, now, 'cooling', '').map((a) => a.uid)).toEqual(['ccc'])
    expect(filterAccounts(list, now, 'all', '乙').map((a) => a.uid)).toEqual(['bbb'])
    expect(sortAccounts(list, 'credits', true).map((a) => a.uid)).toEqual(['bbb', 'ccc', 'aaa'])
    expect(sortAccounts(list, null, false)).toBe(list)
  })
  it('备注能搜到，按名称排序时备注优先于昵称', () => {
    const list = [
      { ...base, uid: 'aaa', nickname: '甲', note: '主力' },
      { ...base, uid: 'bbb', nickname: '乙' },
      { ...base, uid: 'ccc', note: 'Backup' },
    ]
    expect(filterAccounts(list, now, 'all', '主力').map((a) => a.uid)).toEqual(['aaa'])
    expect(filterAccounts(list, now, 'all', 'backup').map((a) => a.uid)).toEqual(['ccc'])
    // 按小写后的码位比：backup < 主力（U+4E3B）< 乙（U+4E59）
    expect(sortAccounts(list, 'name', false).map((a) => a.uid)).toEqual(['ccc', 'aaa', 'bbb'])
  })
  it('按分组筛选：全部、没分组的号、某个分组（一个号可以在好几个组里）', () => {
    const list = [
      { ...base, uid: 'aaa', groups: ['g1', 'g2'] },
      { ...base, uid: 'bbb', groups: ['g2'] },
      { ...base, uid: 'ccc' },
      { ...base, uid: 'ddd', groups: [] },
    ]
    expect(filterAccounts(list, now, 'all', '', '').map((a) => a.uid)).toEqual(['aaa', 'bbb', 'ccc', 'ddd'])
    expect(filterAccounts(list, now, 'all', '', NO_GROUP).map((a) => a.uid)).toEqual(['ccc', 'ddd'])
    expect(filterAccounts(list, now, 'all', '', 'g2').map((a) => a.uid)).toEqual(['aaa', 'bbb'])
    expect(filterAccounts(list, now, 'all', '', 'g1').map((a) => a.uid)).toEqual(['aaa'])
  })
  it('分组和优先级一行：查不到名字的分组不列，优先级 0 不写', () => {
    const names: Record<string, string> = { g1: '主力', g2: '备用' }
    const nameOf = (id: string) => names[id]
    expect(groupLine({ groups: ['g1', 'gone', 'g2'], priority: 3 }, nameOf)).toBe('分组 主力、备用 · 优先级 3')
    expect(groupLine({ groups: ['g1'], priority: 0 }, nameOf)).toBe('分组 主力')
    expect(groupLine({ priority: 5 }, nameOf)).toBe('优先级 5')
    expect(groupLine({ groups: ['gone'] }, nameOf)).toBe('')
  })
})

describe('账号显示名', () => {
  it('备注 > 昵称 > UID 前 8 位', () => {
    expect(accountName({ uid: 'u1234567890', nickname: '一号', note: '主力' })).toBe('主力')
    expect(accountName({ uid: 'u1234567890', nickname: '一号', note: '' })).toBe('一号')
    expect(accountName({ uid: 'u1234567890' })).toBe('u1234567')
  })
  it('总览里只收有备注的号', () => {
    const notes = notesOf({ accounts: [{ ...base, uid: 'a', note: '甲' }, { ...base, uid: 'b' }] })
    expect([...notes]).toEqual([['a', '甲']])
    expect(notesOf({ accounts: null }).size).toBe(0)
  })
  it('总览里的备注和昵称（只给 uid 的接口用昵称补上显示名）', () => {
    const n = namesOf({ accounts: [{ ...base, uid: 'a', note: '甲', nickname: '一号' }, { ...base, uid: 'b' }] })
    expect([...n.notes]).toEqual([['a', '甲']])
    expect([...n.nicks]).toEqual([['a', '一号']])
  })
  it('请求记录的标签按 uid8 换成备注，认不出的照原样', () => {
    const notes = new Map([['abcdefgh-1111', '主力'], ['zzzzzzzz-1', '甲'], ['zzzzzzzz-2', '乙']])
    expect(relabel('一号(abcdefgh)', notes)).toBe('主力(abcdefgh)')
    expect(relabel('abcdefgh', notes)).toBe('主力(abcdefgh)')
    // 昵称里自己带括号
    expect(relabel('小(号)(abcdefgh)', notes)).toBe('主力(abcdefgh)')
    expect(relabel('二号(12345678)', notes)).toBe('二号(12345678)')
    // uid8 对上了两个号：不猜
    expect(relabel('某号(zzzzzzzz)', notes)).toBe('某号(zzzzzzzz)')
  })
})

// 从上游 internal/panel/frontend_test.go 的 TestAppJSRateLimitMeta 平移（期望值原样保留）：
// 同时支持上游 reset_at、网关 until 和两者都没有三种形态，以及模型不可用。
describe('卡片排序', () => {
  it('表格排序状态与卡片下拉互转', () => {
    expect(cardSortKey(undefined)).toBe('default')
    expect(cardSortKey({ column: 'credits', direction: 'descending' })).toBe('credits:descending')
    expect(cardSortKey({ column: 'nope', direction: 'descending' })).toBe('default')
    expect(parseCardSort('credits:descending')).toEqual({ column: 'credits', direction: 'descending' })
    expect(parseCardSort('default')).toBeUndefined()
    expect(parseCardSort('credits:sideways')).toBeUndefined()
  })
})

describe('rateLimitInfo', () => {
  const t = (h: number) => new Date(2026, 8, 28, h, 0, 0).getTime()
  const at = t(14)
  it('上游重置时间 + 网关更早重试：两个时刻都写，倒计时跟着网关', () => {
    const r = rateLimitInfo({ model: 'glm-5.3', kind: 'rate_limit', reset_at: new Date(t(16)).toISOString(), until: new Date(t(15)).toISOString() }, at)
    expect(r.detail).toBe('网关 2026-09-28 15:00 起重试（剩余 1时00分） · 上游称 2026-09-28 16:00 恢复')
    expect(r.short).toBe('1时00分后重试')
    expect(r.unavailable).toBe(false)
  })
  it('网关重试时刻已过、上游还没恢复', () => {
    const r = rateLimitInfo({ model: 'glm-5.3', kind: 'rate_limit', reset_at: new Date(t(16)).toISOString(), until: new Date(t(13)).toISOString() }, at)
    expect(r.detail).toBe('网关 2026-09-28 13:00 起重试 · 上游称 2026-09-28 16:00 恢复')
    expect(r.short).toBe('等待重试')
  })
  it('只有上游重置时间', () => {
    const r = rateLimitInfo({ model: 'glm-5.3', kind: 'rate_limit', reset_at: new Date(t(16)).toISOString() }, at)
    expect(r.detail).toBe('预计 2026-09-28 16:00 解封（剩余 2时00分）')
    expect(r.short).toBe('2时00分后解封')
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

describe('livenessSummary', () => {
  it('通：带上首字和总耗时，没有的省掉', () => {
    expect(livenessSummary({ at: '', ok: true, ttfb_ms: 1200, duration_ms: 2300 })).toBe('通 · 首字 1.2s · 共 2.3s')
    expect(livenessSummary({ at: '', ok: true, duration_ms: 800 })).toBe('通 · 共 800ms')
    expect(livenessSummary({ at: '', ok: true })).toBe('通')
  })
  it('不通：带上状态码和原因', () => {
    expect(livenessSummary({ at: '', ok: false, status: 429, error: 'rate limited' })).toBe('不通（429）：rate limited')
    expect(livenessSummary({ at: '', ok: false })).toBe('不通')
  })
})

describe('livenessModels', () => {
  const models: Model[] = [
    { id: 'cn:glm-5.2' }, { id: 'cn:claude-x', enterprise_only: true }, { id: 'global:gpt-x' }, { id: 'cn:deepseek-v4' },
  ]
  it('按号的域挑目录、去掉域前缀，保持目录顺序', () => {
    expect(livenessModels(models, {})).toEqual(['glm-5.2', 'deepseek-v4'])
    expect(livenessModels(models, { realm: 'cn' })).toEqual(['glm-5.2', 'deepseek-v4'])
    expect(livenessModels(models, { realm: 'global' })).toEqual(['gpt-x'])
  })
  it('企业专属模型只给企业号', () => {
    expect(livenessModels(models, { realm: 'cn', enterprise: true })).toEqual(['glm-5.2', 'claude-x', 'deepseek-v4'])
  })
})

describe('batchParam', () => {
  it('要先填参数的批量操作', () => {
    expect(batchParam('block_models')).toBe('models')
    expect(batchParam('unblock_models')).toBe('models')
    expect(batchParam('join_group')).toBe('group')
    expect(batchParam('leave_group')).toBe('group')
    expect(batchParam('priority')).toBe('priority')
    expect(batchParam('pause')).toBeNull()
  })
})

describe('batchSummary', () => {
  const nameOf = (uid: string) => ({ u1: '一号', u2: '二号' })[uid] ?? uid
  it('全部成功：一句话，不列号', () => {
    expect(batchSummary('禁用', { ok: 3, failed: [], file_errors: [] }, nameOf)).toEqual({ title: '禁用：3 个已完成', lines: [] })
  })
  it('有失败或凭证文件没删掉：逐个列出是哪个号', () => {
    const r = batchSummary('移除', {
      ok: 1,
      failed: [{ uid: 'gone', error: 'account not found' }],
      file_errors: [{ uid: 'u1', error: 'permission denied' }],
    }, nameOf)
    expect(r).toEqual({
      title: '移除：1 个成功，1 个失败',
      lines: ['gone：account not found', '一号 已移除，但凭证文件没删掉：permission denied'],
    })
  })
})
