// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 导航项图标映射：供侧边栏与全局命令面板复用，保持 lib/nav.ts 纯净无 React 依赖。
import type { ComponentType, SVGProps } from 'react'
import {
  ArrowRightArrowLeft, Boxes3, ChartColumn, Cpu, Gear, Gift, Globe, Key, ListCheck, ListTimeline, Persons,
} from '@gravity-ui/icons'
import type { NavIconId } from '../lib/nav'

export type NavIconComponent = ComponentType<SVGProps<SVGSVGElement>>

export const NAV_ICONS: Record<NavIconId, NavIconComponent> = {
  accounts: Persons,
  proxies: Globe,
  usage: ChartColumn,
  requests: ArrowRightArrowLeft,
  packages: Boxes3,
  credits: Gift,
  taskscenter: ListCheck,
  models: Cpu,
  keys: Key,
  config: Gear,
  logs: ListTimeline,
}
