// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it } from 'vitest'
import {
  badExitURL, bindSummary, defaultForLabels, defaultPoolOf, exitGeo, filterProxies, lineCounts, lineResult, loadText, poolSummary, proxyStatus, settingsForm,
  settingsInput, warningLines,
} from './proxies'
import type { ProxyItem, ProxyPool } from './types'

const px = (over: Partial<ProxyItem>): ProxyItem => ({
  id: 'x1', name: '甲', url: 'http://u:***@203.0.113.1:8080', scheme: 'http', host: '203.0.113.1:8080', pool: 'p1',
  created_at: '2026-11-01T00:00:00Z', health: {}, accounts: [], usable: true, ...over,
})
const pool: ProxyPool = { id: 'p1', name: '国内', max_per_proxy: 5, default_for: ['cn'], proxies: 3, usable: 2, accounts: 4, waiting: 1 }

describe('代理的展示', () => {
  it('地区去重后和运营商连成一句', () => {
    expect(exitGeo({ ip: '203.0.113.1', country: '中国', region: '香港', city: '香港', isp: '某运营商', checked_at: '' })).toBe('中国 香港 · 某运营商')
    expect(exitGeo({ isp: '某运营商', checked_at: '' })).toBe('某运营商')
    expect(exitGeo(undefined)).toBe('')
  })
  it('状态：已到期优先，其次通不通，没测过是还没测', () => {
    expect(proxyStatus(px({ expired: true, health: { state: 'ok' } }))).toEqual({ label: '已到期', color: 'danger' })
    expect(proxyStatus(px({ health: { state: 'down', error: '连不上' } }))).toEqual({ label: '不通', color: 'danger', detail: '连不上' })
    expect(proxyStatus(px({ health: { state: 'ok', latency_ms: 320 } }))).toEqual({ label: '通 · 320ms', color: 'success' })
    expect(proxyStatus(px({ health: { state: 'ok' } })).label).toBe('通')
    expect(proxyStatus(px({})).label).toBe('还没测')
  })
  it('绑号数和池的说明', () => {
    expect(loadText(px({ accounts: ['a', 'b'] }), pool)).toBe('2 / 5 个号')
    expect(loadText(px({ accounts: ['a'] }), { ...pool, max_per_proxy: 0 })).toBe('1 个号')
    expect(loadText(px({}))).toBe('0 个号')
    expect(poolSummary(pool)).toBe('2 / 3 个代理能用 · 每个最多 5 个号')
    expect(poolSummary({ ...pool, max_per_proxy: 0 })).toBe('2 / 3 个代理能用 · 每个不限号数')
    expect(defaultForLabels({ default_for: ['cn', 'global'] })).toEqual(['国内版默认', '国际版默认'])
    expect(defaultForLabels({})).toEqual([])
    expect(defaultPoolOf([pool], 'cn')).toBe(pool)
    expect(defaultPoolOf([pool], 'global')).toBeUndefined()
  })
})

describe('筛选代理', () => {
  const list = [
    px({ id: 'a', name: '甲', health: { state: 'ok', exit: { ip: '198.51.100.7', country: '日本', checked_at: '' } } }),
    px({ id: 'b', name: '乙', pool: 'p2', usable: false, health: { state: 'down' } }),
    px({ id: 'c', name: '丙', usable: false, expired: true, health: { state: 'down' } }),
    px({ id: 'd', name: '丁', expiring: true }),
  ]
  const ids = (l: ProxyItem[]) => l.map((p) => p.id)
  it('按池、状态筛', () => {
    expect(ids(filterProxies(list, 'p2', 'all', ''))).toEqual(['b'])
    expect(ids(filterProxies(list, '', 'usable', ''))).toEqual(['a', 'd'])
    // 到期的代理哪怕测出来不通也只算「已到期」
    expect(ids(filterProxies(list, '', 'down', ''))).toEqual(['b'])
    expect(ids(filterProxies(list, '', 'expired', ''))).toEqual(['c'])
    expect(ids(filterProxies(list, '', 'expiring', ''))).toEqual(['d'])
    expect(ids(filterProxies(list, '', 'untested', ''))).toEqual(['d'])
  })
  it('关键字搜名字、地址、出口 IP 和地区', () => {
    expect(ids(filterProxies(list, '', 'all', '乙'))).toEqual(['b'])
    expect(ids(filterProxies(list, '', 'all', '198.51'))).toEqual(['a'])
    expect(ids(filterProxies(list, '', 'all', '日本'))).toEqual(['a'])
    expect(ids(filterProxies(list, '', 'all', '203.0.113'))).toEqual(['a', 'b', 'c', 'd'])
  })
})

describe('导入预览', () => {
  it('每行的结果和计数', () => {
    const lines = [
      { no: 1, text: 'a', url: 'http://a' },
      { no: 2, text: 'b', error: '端口不对' },
      { no: 3, text: 'c', duplicate: 'existing' as const, duplicate_of: '甲' },
      { no: 4, text: 'a', duplicate: 'batch' as const, duplicate_of: '1' },
    ]
    expect(lines.map((l) => lineResult(l).text)).toEqual(['可以导入', '端口不对', '和已有的代理「甲」重复', '和第 1 行重复'])
    expect(lineResult(lines[0]).ok).toBe(true)
    expect(lineCounts(lines)).toEqual({ ok: 1, error: 1, duplicate: 2 })
  })
})

describe('提醒和绑定结果', () => {
  it('只列有数的提醒', () => {
    expect(warningLines({ down: 2, expired: 0, expiring: 1, waiting: 3, blocked: 9 }))
      .toEqual(['2 个代理连不上', '1 个代理快到期', '3 个号在等代理（池里没有空位）'])
    expect(warningLines({ down: 0, expired: 1, expiring: 0, waiting: 0, blocked: 0 })).toEqual(['1 个代理已到期'])
    expect(warningLines(null)).toEqual([])
  })
  it('批量绑定的结果归总', () => {
    const s = bindSummary([
      { uid: 'u1', proxy: 'x1', proxy_name: '甲' },
      { uid: 'u2', waiting: true },
      { uid: 'u3' },
      { uid: 'u4', error: '代理「甲」已经绑满了' },
    ], (uid) => '号' + uid)
    expect(s).toEqual({ bound: 1, waiting: 1, unbound: 1, errors: ['号u4：代理「甲」已经绑满了'] })
  })
})

describe('设置表单', () => {
  it('查出口 IP 的地址一行一个，空行去掉', () => {
    const s = { require_bound: true, health_minutes: 5, exit_minutes: 60, rebind_minutes: 30, expiry_warn_days: 3, exit_ip_urls: ['https://a.example/json', 'https://b.example'] }
    const f = settingsForm(s)
    expect(f.exit_ip_urls).toBe('https://a.example/json\nhttps://b.example')
    expect(settingsInput({ ...f, exit_ip_urls: ' https://a.example/json \n\n' })).toEqual({ ...s, exit_ip_urls: ['https://a.example/json'] })
    expect(settingsForm({ ...s, exit_ip_urls: undefined }).exit_ip_urls).toBe('')
  })
  it('地址要 http:// 或 https:// 开头', () => {
    expect(badExitURL('https://a.example/json\n\nhttp://b.example')).toBe('')
    expect(badExitURL('https://a.example\nftp://b.example')).toBe('ftp://b.example')
    expect(badExitURL('a.example')).toBe('a.example')
  })
})
