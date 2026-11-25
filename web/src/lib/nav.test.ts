// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it } from 'vitest'
import { ALL_NAV_ITEMS, filterCommandItems, NAV_GROUPS } from './nav'

describe('NAV_GROUPS / ALL_NAV_ITEMS', () => {
  it('覆盖面板全部 11 个页面且路由与图标唯一', () => {
    expect(NAV_GROUPS.length).toBe(3)
    expect(ALL_NAV_ITEMS.length).toBe(11)
    expect(new Set(ALL_NAV_ITEMS.map((i) => i.to)).size).toBe(11)
    expect(new Set(ALL_NAV_ITEMS.map((i) => i.icon)).size).toBe(11)
  })
})

describe('filterCommandItems', () => {
  it('空查询返回全部项', () => {
    expect(filterCommandItems(ALL_NAV_ITEMS, '   ')).toHaveLength(ALL_NAV_ITEMS.length)
  })

  it('支持中文名称、英文关键词和描述模糊匹配（大小写不敏感）', () => {
    expect(filterCommandItems(ALL_NAV_ITEMS, '账号').map((i) => i.to)).toEqual(['/accounts'])
    expect(filterCommandItems(ALL_NAV_ITEMS, 'PROXY').map((i) => i.to)).toEqual(['/proxies'])
    expect(filterCommandItems(ALL_NAV_ITEMS, '更新通道').map((i) => i.to)).toEqual(['/config'])
    const withDesc = [
      { label: '切换主题', desc: '深色与浅色外观', keywords: ['theme'] },
      { label: '添加账号', desc: '导入凭证', keywords: ['add'] },
    ]
    expect(filterCommandItems(withDesc, '深色').map((i) => i.label)).toEqual(['切换主题'])
  })

  it('无匹配项返回空数组', () => {
    expect(filterCommandItems(ALL_NAV_ITEMS, '不存在的路由xyz')).toEqual([])
  })
})
