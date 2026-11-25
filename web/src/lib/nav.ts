// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 侧栏导航分组与全局命令面板共用的页面路由元数据及搜索过滤纯函数。
export type NavIconId =
  | 'accounts'
  | 'proxies'
  | 'usage'
  | 'requests'
  | 'packages'
  | 'credits'
  | 'taskscenter'
  | 'models'
  | 'keys'
  | 'config'
  | 'logs'

export interface NavItem {
  to: string
  label: string
  icon: NavIconId
  group: string
  keywords: readonly string[]
}

export interface NavGroup {
  id: string
  label: string
  items: readonly NavItem[]
}

export const NAV_GROUPS: readonly NavGroup[] = [
  {
    id: 'core',
    label: '核心资源',
    items: [
      { to: '/accounts', label: '账号池', icon: 'accounts', group: '核心资源', keywords: ['accounts', 'pool', '号池', 'workbuddy', 'trae', 'qoder'] },
      { to: '/proxies', label: '代理', icon: 'proxies', group: '核心资源', keywords: ['proxy', 'proxies', '代理池', 'ip'] },
      { to: '/models', label: '模型与档位', icon: 'models', group: '核心资源', keywords: ['models', 'routes', '路由', '隐藏'] },
      { to: '/keys', label: '密钥', icon: 'keys', group: '核心资源', keywords: ['keys', 'api', 'token', '客户端密钥'] },
    ],
  },
  {
    id: 'observe',
    label: '观测统计',
    items: [
      { to: '/usage', label: '用量', icon: 'usage', group: '观测统计', keywords: ['usage', 'tokens', '统计', '图表'] },
      { to: '/requests', label: '请求记录', icon: 'requests', group: '观测统计', keywords: ['requests', 'history', '缓存', '明细'] },
      { to: '/packages', label: '积分构成', icon: 'packages', group: '观测统计', keywords: ['packages', 'quota', '到期', '额度'] },
      { to: '/credits', label: '积分获取', icon: 'credits', group: '观测统计', keywords: ['credits', 'earning', '签到', '流水'] },
    ],
  },
  {
    id: 'system',
    label: '任务与系统',
    items: [
      { to: '/taskscenter', label: '任务中心', icon: 'taskscenter', group: '任务与系统', keywords: ['tasks', 'growth', '成长任务', '补偿'] },
      { to: '/config', label: '配置', icon: 'config', group: '任务与系统', keywords: ['config', 'settings', '设置', '更新通道'] },
      { to: '/logs', label: '运行日志', icon: 'logs', group: '任务与系统', keywords: ['logs', 'runtime', '排查', '报错'] },
    ],
  },
]

export const ALL_NAV_ITEMS: readonly NavItem[] = NAV_GROUPS.flatMap((g) => g.items)

/** 按中文名、路由路径或关键词模糊筛选命令项（大小写不敏感） */
export function filterCommandItems<T extends { label: string; keywords?: readonly string[]; desc?: string }>(
  items: readonly T[],
  query: string,
): T[] {
  const q = query.trim().toLowerCase()
  if (!q) return [...items]
  return items.filter((item) => {
    if (item.label.toLowerCase().includes(q)) return true
    if (item.desc?.toLowerCase().includes(q)) return true
    return !!item.keywords?.some((k) => k.toLowerCase().includes(q))
  })
}
