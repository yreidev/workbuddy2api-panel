// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 全局命令面板（⌘K / Ctrl+K）：纯 HeroUI v3 组合实现，支持中英文模糊检索页面与快捷操作，
// 键盘方向键切换高亮项并自动滚动入视口、回车直达（兼容中文输入法组字）、Esc 关闭。
import { useEffect, useMemo, useRef, useState, type ComponentType, type KeyboardEvent as ReactKeyboardEvent, type SVGProps } from 'react'
import { ArrowsRotateRight, CircleInfo, LayoutSideContentLeft, Moon, Plus, Sun } from '@gravity-ui/icons'
import {
  Chip, Description, EmptyState, Header, Kbd, Label, ListBox, Modal, ScrollShadow, SearchField,
} from '@heroui/react'
import { hasOpenDialog, matchesModShortcut } from '../lib/motion'
import { ALL_NAV_ITEMS, filterCommandItems } from '../lib/nav'
import { ModKbd } from './ModKbd'
import { NAV_ICONS } from './navIcons'

type IconComp = ComponentType<SVGProps<SVGSVGElement>>

interface CommandEntry {
  id: string
  label: string
  desc?: string
  groupLabel?: string
  icon: IconComp
  keywords: readonly string[]
  isCurrent?: boolean
  shortcutKey?: string
  run: () => void
}

export interface CommandPaletteProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  currentPath: string
  onNavigate: (to: string) => void
  onAddAccount: () => void
  onRefreshBalance?: () => void
  onToggleTheme: () => void
  onOpenAbout: () => void
  onToggleSidebar: () => void
  isDark: boolean
  sidebarCollapsed: boolean
  canToggleSidebar: boolean
}

export function CommandPalette(props: CommandPaletteProps) {
  const { open, onOpenChange } = props

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (!matchesModShortcut(e, 'k')) return
      // 未鉴权锁屏（KeyGate）或其他模态框打开时禁止穿透弹出；命令面板自身打开时允许再次按 ⌘K/Ctrl+K 关闭
      if (!open && hasOpenDialog()) return
      e.preventDefault()
      onOpenChange(!open)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [open, onOpenChange])

  return (
    <Modal.Backdrop variant="blur" isOpen={open} onOpenChange={onOpenChange}>
      <Modal.Container size="md" placement="top" className="pt-[8vh] sm:pt-[12vh]">
        <Modal.Dialog aria-label="命令面板" className="gap-0 overflow-hidden p-0">
          {/* ModalOverlay 在退场动画结束后自动卸载子树，直接渲染可保住完整退场动画且下次打开状态自动重置 */}
          <CommandPaletteBody {...props} />
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

function CommandPaletteBody({
  onOpenChange, currentPath, onNavigate, onAddAccount, onRefreshBalance,
  onToggleTheme, onOpenAbout, onToggleSidebar, isDark, sidebarCollapsed, canToggleSidebar,
}: CommandPaletteProps) {
  const [query, setQuery] = useState('')
  const [activeIndex, setActiveIndex] = useState(0)
  const listRef = useRef<HTMLDivElement>(null)
  const navSourceRef = useRef<'keyboard' | 'mouse'>('keyboard')

  const close = () => onOpenChange(false)

  const pageEntries = useMemo<CommandEntry[]>(() => {
    const all: CommandEntry[] = ALL_NAV_ITEMS.map((item) => ({
      id: 'nav:' + item.to,
      label: item.label,
      groupLabel: item.group,
      icon: NAV_ICONS[item.icon],
      keywords: [item.to, item.group, ...item.keywords],
      isCurrent: item.to === currentPath,
      run: () => onNavigate(item.to),
    }))
    return filterCommandItems(all, query)
  }, [currentPath, onNavigate, query])

  const actionEntries = useMemo<CommandEntry[]>(() => {
    const all: CommandEntry[] = [
      {
        id: 'act:add',
        label: '添加账号',
        desc: '浏览器登录或导入 JSON 凭证',
        icon: Plus,
        keywords: ['add', 'account', 'login', 'import', '新增', '登录', '导入'],
        run: onAddAccount,
      },
      ...(onRefreshBalance
        ? [{
            id: 'act:refresh',
            label: '刷新全部余额',
            desc: '从上游同步 WorkBuddy 账号余额与昵称',
            icon: ArrowsRotateRight,
            keywords: ['refresh', 'balance', 'sync', 'credits', '余额', '刷新'],
            run: onRefreshBalance,
          }]
        : []),
      {
        id: 'act:theme',
        label: isDark ? '切换到浅色模式' : '切换到深色模式',
        desc: '立即切换控制台明暗外观',
        icon: isDark ? Sun : Moon,
        keywords: ['theme', 'dark', 'light', 'mode', '主题', '深色', '浅色'],
        run: onToggleTheme,
      },
      ...(canToggleSidebar
        ? [{
            id: 'act:sidebar',
            label: sidebarCollapsed ? '展开侧边栏' : '收起侧边栏',
            desc: '在完整导航与精简图标栏之间切换',
            icon: LayoutSideContentLeft,
            keywords: ['sidebar', 'collapse', 'expand', 'menu', '侧栏', '折叠', '展开'],
            shortcutKey: 'B',
            run: onToggleSidebar,
          }]
        : []),
      {
        id: 'act:about',
        label: '版本信息与更新',
        desc: '查看当前版本、更新通道与开源许可',
        icon: CircleInfo,
        keywords: ['version', 'about', 'update', 'channel', '版本', '更新', '关于'],
        run: onOpenAbout,
      },
    ]
    return filterCommandItems(all, query)
  }, [canToggleSidebar, isDark, onAddAccount, onOpenAbout, onRefreshBalance, onToggleSidebar, onToggleTheme, query, sidebarCollapsed])

  const flatEntries = useMemo(() => [...pageEntries, ...actionEntries], [pageEntries, actionEntries])
  const safeIndex = flatEntries.length > 0 ? Math.min(activeIndex, flatEntries.length - 1) : -1
  const activeId = safeIndex >= 0 ? flatEntries[safeIndex].id : null

  useEffect(() => {
    if (navSourceRef.current !== 'keyboard' || !activeId) return
    listRef.current?.querySelector<HTMLElement>('[data-command-active="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [activeId])

  const execute = (entry: CommandEntry) => {
    close()
    entry.run()
  }

  const onInputKeyDown = (e: ReactKeyboardEvent<HTMLInputElement>) => {
    // 中文/日文等输入法组字期间的回车用于上屏，不触发命令执行
    if (e.nativeEvent.isComposing || e.key === 'Process') return
    if (flatEntries.length === 0) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      navSourceRef.current = 'keyboard'
      setActiveIndex((i) => (i + 1) % flatEntries.length)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      navSourceRef.current = 'keyboard'
      setActiveIndex((i) => (i - 1 + flatEntries.length) % flatEntries.length)
    } else if (e.key === 'Enter' && safeIndex >= 0) {
      e.preventDefault()
      execute(flatEntries[safeIndex])
    }
  }

  const renderEntry = (entry: CommandEntry) => {
    const idx = flatEntries.findIndex((x) => x.id === entry.id)
    const isActive = entry.id === activeId
    const Icon = entry.icon
    return (
      <ListBox.Item
        key={entry.id}
        id={entry.id}
        textValue={entry.label}
        onHoverStart={() => {
          if (idx >= 0) {
            navSourceRef.current = 'mouse'
            setActiveIndex(idx)
          }
        }}
        className={`rounded-xl px-2.5 py-2 transition-colors ${
          isActive ? 'bg-default text-foreground' : ''
        }`}
      >
        <div
          data-command-active={isActive ? 'true' : undefined}
          className="flex w-full min-w-0 items-center gap-2.5"
        >
          <Icon className={`size-4 shrink-0 ${isActive ? 'text-foreground' : 'text-muted'}`} />
          <div className="flex min-w-0 flex-1 flex-col">
            <div className="flex items-center gap-2">
              <Label className="truncate font-normal">{entry.label}</Label>
              {entry.groupLabel && <span className="truncate text-xs text-muted">{entry.groupLabel}</span>}
            </div>
            {entry.desc && <Description className="truncate text-xs">{entry.desc}</Description>}
          </div>
          {entry.isCurrent && <Chip size="sm" variant="soft" color="accent">当前</Chip>}
          {entry.shortcutKey && <ModKbd keyChar={entry.shortcutKey} variant="light" className="shrink-0" />}
        </div>
      </ListBox.Item>
    )
  }

  return (
    <>
      <div className="border-b border-separator p-2.5">
        <SearchField
          aria-label="搜索页面或快捷操作"
          variant="secondary"
          fullWidth
          autoFocus
          value={query}
          onChange={(v) => {
            navSourceRef.current = 'keyboard'
            setQuery(v)
            setActiveIndex(0)
          }}
        >
          <SearchField.Group className="rounded-xl border-0 bg-transparent shadow-none">
            <SearchField.SearchIcon />
            <SearchField.Input
              placeholder="搜索页面名称、关键词或快捷操作…"
              onKeyDown={onInputKeyDown}
            />
            <SearchField.ClearButton />
          </SearchField.Group>
        </SearchField>
      </div>

      <ScrollShadow ref={listRef} hideScrollBar className="max-h-80 px-2 py-2">
        {flatEntries.length === 0 ? (
          <EmptyState className="py-10 text-center text-sm text-muted">
            没有找到与「{query.trim()}」匹配的页面或操作
          </EmptyState>
        ) : (
          <ListBox
            aria-label="命令结果"
            selectionMode="none"
            onAction={(key) => {
              const found = flatEntries.find((x) => x.id === String(key))
              if (found) execute(found)
            }}
          >
            {pageEntries.length > 0 ? (
              <ListBox.Section>
                <Header className="px-2.5 pb-1 pt-1 text-[11px] font-medium text-muted">页面跳转</Header>
                {pageEntries.map(renderEntry)}
              </ListBox.Section>
            ) : null}
            {actionEntries.length > 0 ? (
              <ListBox.Section className={pageEntries.length > 0 ? 'mt-2 border-t border-separator pt-2' : undefined}>
                <Header className="px-2.5 pb-1 pt-1 text-[11px] font-medium text-muted">快捷操作</Header>
                {actionEntries.map(renderEntry)}
              </ListBox.Section>
            ) : null}
          </ListBox>
        )}
      </ScrollShadow>

      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-separator bg-surface-secondary/50 px-3.5 py-2 text-[11px] text-muted">
        <span className="flex items-center gap-1.5">
          <Kbd variant="light"><Kbd.Abbr keyValue="up" /><Kbd.Abbr keyValue="down" /></Kbd>
          <span>选择</span>
          <Kbd variant="light"><Kbd.Abbr keyValue="enter" /></Kbd>
          <span>执行</span>
        </span>
        <span className="flex items-center gap-1.5">
          <Kbd variant="light"><Kbd.Abbr keyValue="escape" /></Kbd>
          <span>关闭</span>
        </span>
      </div>
    </>
  )
}
