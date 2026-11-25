// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 外壳：左侧可折叠分组导航（桌面端 ⌘B/Ctrl+B 切换精简图标栏、窄屏收进抽屉）+ 毛玻璃顶栏（标题、运行时长、
// ⌘K/Ctrl+K 命令面板、明暗切换、刷新余额、添加账号）+ 路由切换轻过渡。交互组件全部使用 HeroUI v3。
import { useCallback, useEffect, useState } from 'react'
import {
  ArrowsRotateRight, Bars, LayoutSideContentLeft, LogoGithub, Magnifier, Moon, Plus, Sun,
} from '@gravity-ui/icons'
import {
  Button, Drawer, Link, RouterProvider, ScrollShadow, Separator, Toast, Tooltip, toast, useMediaQuery, useTheme,
} from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { Outlet, useLocation, useMatches, useRouter } from '@tanstack/react-router'
import * as m from 'motion/react-m'
import { post } from '../lib/api'
import { uptime } from '../lib/format'
import { toastError, useNow, useStoredChoice } from '../lib/hooks'
import { isExternalHref } from '../lib/links'
import { hasOpenDialog, isEditableTarget, matchesModShortcut, PAGE_TRANSITION, SPRING_SNAPPY } from '../lib/motion'
import { NAV_GROUPS } from '../lib/nav'
import { qk, useHasProviderData, useOverview, useProviders } from '../lib/queries'
import { AddAccountModal } from './AddAccountModal'
import { CommandPalette } from './CommandPalette'
import { BusyButton } from './Feedback'
import { KeyGate } from './KeyGate'
import { ModKbd } from './ModKbd'
import { NAV_ICONS } from './navIcons'
import { UpdateBadge, UpdateNotice, type ManualPanel } from './UpdateNotice'

/** 本面板的源码仓库（侧栏底部的链接） */
const REPO = { name: 'yreidev/workbuddy2api-panel', url: 'https://github.com/yreidev/workbuddy2api-panel' }
const SIDEBAR_MODES = ['expanded', 'collapsed'] as const

export function Shell() {
  const router = useRouter()
  const { pathname } = useLocation()
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const { resolvedTheme, setTheme } = useTheme('system')
  const isDark = resolvedTheme === 'dark'
  const [sidebarMode, setSidebarMode] = useStoredChoice('wb2api.sidebar', SIDEBAR_MODES, 'expanded')
  const collapsed = isDesktop && sidebarMode === 'collapsed'
  const [drawer, setDrawer] = useState(false)
  const [adding, setAdding] = useState(false)
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [manualPanel, setManualPanel] = useState<ManualPanel>(null)
  const overview = useOverview()
  const matches = useMatches()
  const title = matches.map((x) => x.staticData.title).filter(Boolean).pop() ?? ''
  const balance = useRefreshBalance()
  const canRefreshBalance = overview.data != null && overview.data.total > 0

  const toggleTheme = useCallback(() => setTheme(isDark ? 'light' : 'dark'), [isDark, setTheme])
  const toggleSidebar = useCallback(
    () => setSidebarMode((m) => (m === 'collapsed' ? 'expanded' : 'collapsed')),
    [setSidebarMode],
  )

  useEffect(() => {
    document.title = (title ? title + ' · ' : '') + 'WorkBuddy2API'
  }, [title])

  useEffect(() => {
    if (!isDesktop) return
    const onKeyDown = (e: KeyboardEvent) => {
      if (isEditableTarget(e.target) || hasOpenDialog() || !matchesModShortcut(e, 'b')) return
      e.preventDefault()
      toggleSidebar()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [isDesktop, toggleSidebar])

  return (
    <RouterProvider
      navigate={(to) => void router.navigate({ to })}
      // 面板内部路径交给路由生成 #/xxx 地址；https:// 这类完整地址（外链）原样保留
      useHref={(to) => (isExternalHref(to) ? to : router.history.createHref(router.buildLocation({ to }).href))}
    >
      <div
        data-sidebar-collapsed={collapsed ? 'true' : 'false'}
        className="wb-layout flex min-h-dvh"
      >
        <aside
          data-collapsed={collapsed ? 'true' : 'false'}
          className="wb-sidebar sticky top-0 hidden h-dvh shrink-0 flex-col border-r border-separator bg-background lg:flex"
        >
          <Sidebar collapsed={collapsed} layoutPrefix="desktop" onShow={setManualPanel} />
        </aside>
        <Drawer.Backdrop isOpen={drawer && !isDesktop} onOpenChange={setDrawer}>
          <Drawer.Content placement="left">
            <Drawer.Dialog aria-label="导航菜单" className="w-64 p-0">
              <Sidebar
                collapsed={false}
                layoutPrefix="drawer"
                onNavigate={() => setDrawer(false)}
                onShow={(panel) => { setDrawer(false); setManualPanel(panel) }}
              />
            </Drawer.Dialog>
          </Drawer.Content>
        </Drawer.Backdrop>

        <div className="flex min-w-0 flex-1 flex-col">
          <header className="sticky top-0 z-40 flex h-14 shrink-0 items-center gap-2 border-b border-separator/80 bg-background/80 px-3 backdrop-blur-md sm:gap-3 sm:px-6">
            <Button isIconOnly aria-label="打开菜单" variant="ghost" className="lg:hidden" onPress={() => setDrawer(true)}>
              <Bars />
            </Button>
            <Tooltip delay={200}>
              <Button
                isIconOnly
                size="sm"
                variant="ghost"
                aria-label={collapsed ? '展开侧边栏' : '收起侧边栏'}
                className="hidden lg:inline-flex"
                onPress={toggleSidebar}
              >
                <LayoutSideContentLeft />
              </Button>
              <Tooltip.Content>
                <span className="flex items-center gap-1.5">
                  {collapsed ? '展开侧边栏' : '收起侧边栏'}
                  <ModKbd keyChar="B" />
                </span>
              </Tooltip.Content>
            </Tooltip>
            <h1 className="truncate text-base font-semibold sm:text-lg">{title}</h1>
            {overview.data && <span className="hidden truncate text-sm text-muted md:inline">{uptime(overview.data.uptime_sec)}</span>}
            <div className="ml-auto flex items-center gap-1.5 sm:gap-2">
              {isDesktop ? (
                <Button
                  size="sm"
                  variant="secondary"
                  aria-label="打开命令面板"
                  className="gap-2 px-2.5 text-muted hover:text-foreground"
                  onPress={() => setPaletteOpen(true)}
                >
                  <Magnifier className="size-3.5" />
                  <span className="text-xs font-normal">快速跳转</span>
                  <ModKbd keyChar="K" className="text-[11px]" />
                </Button>
              ) : (
                <Button isIconOnly aria-label="打开命令面板" variant="ghost" onPress={() => setPaletteOpen(true)}>
                  <Magnifier />
                </Button>
              )}
              <ThemeSwitch isDark={isDark} onToggle={toggleTheme} />
              {canRefreshBalance && (
                <BusyButton
                  variant="secondary"
                  busy={balance.busy}
                  isIconOnly={!isDesktop}
                  aria-label={balance.ariaLabel}
                  onPress={() => void balance.run()}
                >
                  {!balance.busy && <ArrowsRotateRight />}{isDesktop && balance.label}
                </BusyButton>
              )}
              <Button variant="primary" isIconOnly={!isDesktop} aria-label="添加账号" onPress={() => setAdding(true)}>
                <Plus />{isDesktop && '添加账号'}
              </Button>
            </div>
          </header>
          <main className="mx-auto flex w-full max-w-7xl flex-1 flex-col px-3 pb-24 pt-4 sm:px-6 sm:pt-6">
            <m.div
              key={pathname}
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              transition={PAGE_TRANSITION}
              className="flex flex-col gap-4"
            >
              <Outlet />
            </m.div>
          </main>
        </div>
      </div>
      <CommandPalette
        open={paletteOpen}
        onOpenChange={setPaletteOpen}
        currentPath={pathname}
        onNavigate={(to) => void router.navigate({ to })}
        onAddAccount={() => setAdding(true)}
        onRefreshBalance={canRefreshBalance ? () => void balance.run() : undefined}
        onToggleTheme={toggleTheme}
        onOpenAbout={() => setManualPanel('about')}
        onToggleSidebar={toggleSidebar}
        isDark={isDark}
        sidebarCollapsed={collapsed}
        canToggleSidebar={isDesktop}
      />
      <AddAccountModal open={adding} onClose={() => setAdding(false)} />
      <KeyGate />
      <UpdateNotice manual={manualPanel} onManualChange={setManualPanel} />
      <Toast.Provider placement={isDesktop ? 'bottom end' : 'top'} />
    </RouterProvider>
  )
}

function Sidebar({ collapsed, layoutPrefix, onNavigate, onShow }: {
  collapsed: boolean
  layoutPrefix: 'desktop' | 'drawer'
  onNavigate?: () => void
  onShow: (panel: ManualPanel) => void
}) {
  const { pathname } = useLocation()
  const { data } = useOverview()
  const providers = useProviders()
  const now = useNow(30_000)
  const newAccounts = providers.data?.accounts ?? []
  const state = !data ? { text: '连接中', dot: 'bg-default' }
    : data.healthy > 0 ? { text: '服务正常', dot: 'bg-success' }
    : newAccounts.some((a) => !a.disabled && !a.paused && !a.error && !(a.cool_until && Date.parse(a.cool_until) > now)) ? { text: '账号已接入', dot: 'bg-success' }
    : providers.isError ? { text: '平台账号读取失败', dot: 'bg-warning' }
    : !providers.data ? { text: '读取账号中', dot: 'bg-default' }
    : data.total || newAccounts.length ? { text: '无可用账号', dot: 'bg-warning' }
    : { text: '待添加账号', dot: 'bg-danger' }

  return (
    <>
      {collapsed ? (
        <div className="flex flex-col items-center gap-1.5 px-2 pb-3 pt-4">
          <Tooltip delay={120}>
            <Button
              isIconOnly
              size="sm"
              variant="secondary"
              aria-label={data ? `WorkBuddy2API v${data.version}（查看版本信息）` : 'WorkBuddy2API'}
              onPress={() => onShow('about')}
              className="size-9 rounded-xl bg-accent-soft text-xs font-bold text-accent-soft-foreground"
            >
              WB
            </Button>
            <Tooltip.Content placement="right">
              {data ? `WorkBuddy2API · v${data.version}` : 'WorkBuddy2API'}
            </Tooltip.Content>
          </Tooltip>
          <UpdateBadge compact onPress={() => onShow('update')} />
        </div>
      ) : (
        <div className="px-5 pb-3 pt-5">
          <div className="truncate font-semibold">WorkBuddy2API</div>
          <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-muted">
            {data
              ? (
                <Tooltip delay={300}>
                  <Button size="sm" variant="ghost" onPress={() => onShow('about')} className="-ms-1 h-auto min-w-0 px-1 py-0 text-xs font-normal text-muted hover:text-foreground">
                    v{data.version}
                  </Button>
                  <Tooltip.Content>版本信息</Tooltip.Content>
                </Tooltip>
              )
              : '控制台'}
            <UpdateBadge onPress={() => onShow('update')} />
          </div>
        </div>
      )}

      {/* 固定 px-2.5(10px) + Link px-[13px] + 18px 图标：折叠至 4rem(64px) 时图标水平位置 0px 跳动且精确居中 */}
      <ScrollShadow hideScrollBar className="flex-1 px-2.5">
        <nav aria-label="导航" className="flex flex-col gap-2.5 py-1">
          {NAV_GROUPS.map((group, groupIdx) => (
            <div key={group.id} className="flex flex-col gap-0.5">
              {groupIdx > 0 && collapsed && <Separator className="mx-auto my-1 w-6" />}
              <div
                aria-hidden={collapsed || undefined}
                className="wb-sidebar-group-title px-[13px] pb-1 text-[11px] font-medium text-muted"
              >
                {group.label}
              </div>
              <ul className="flex flex-col gap-0.5">
                {group.items.map((item) => {
                  const active = pathname === item.to
                  const Icon = NAV_ICONS[item.icon]
                  return (
                    <li key={item.to}>
                      <Tooltip delay={120} isDisabled={!collapsed}>
                        <Link
                          href={item.to}
                          aria-label={item.label}
                          aria-current={active ? 'page' : undefined}
                          onPress={onNavigate}
                          className={`relative flex h-9 w-full items-center rounded-xl px-[13px] no-underline transition-colors duration-150 ${
                            active ? 'font-medium text-foreground' : 'text-muted hover:bg-default/60 hover:text-foreground'
                          }`}
                        >
                          {active && (
                            <m.span
                              layoutId={`${layoutPrefix}-sidebar-active-pill`}
                              transition={SPRING_SNAPPY}
                              className="pointer-events-none absolute inset-0 rounded-xl bg-default shadow-xs"
                            />
                          )}
                          <Icon className={`relative z-10 size-4.5 shrink-0 transition-colors ${active ? 'text-foreground' : 'text-muted'}`} />
                          <span aria-hidden={collapsed || undefined} className="wb-sidebar-label relative z-10 text-sm">
                            {item.label}
                          </span>
                        </Link>
                        <Tooltip.Content placement="right" showArrow>
                          <Tooltip.Arrow />
                          {item.label}
                        </Tooltip.Content>
                      </Tooltip>
                    </li>
                  )
                })}
              </ul>
            </div>
          ))}
        </nav>
      </ScrollShadow>

      {collapsed ? (
        <div className="flex flex-col items-center gap-2 border-t border-separator px-2 py-3 text-xs text-muted">
          <Tooltip delay={120}>
            <Tooltip.Trigger
              aria-label={state.text}
              className="flex size-7 items-center justify-center rounded-lg hover:bg-default/60"
            >
              <span className={`size-2.5 rounded-full ${state.dot}`} />
            </Tooltip.Trigger>
            <Tooltip.Content placement="right">
              {state.text}{data ? ` · v${data.version} · ${data.redis_mode === 'upstash' ? 'Redis 镜像' : '本地内存'}` : ''}
            </Tooltip.Content>
          </Tooltip>
          <Tooltip delay={120}>
            <Link
              href={REPO.url}
              target="_blank"
              rel="noopener noreferrer"
              aria-label={'GitHub 仓库 ' + REPO.name + '（在新标签页打开）'}
              className="flex size-7 items-center justify-center rounded-lg text-muted no-underline hover:bg-default/60 hover:text-foreground"
            >
              <LogoGithub className="size-4 shrink-0" />
            </Link>
            <Tooltip.Content placement="right">{REPO.name}</Tooltip.Content>
          </Tooltip>
        </div>
      ) : (
        <div className="flex flex-col gap-1 border-t border-separator px-5 py-3 text-xs text-muted">
          <span className="flex items-center gap-2"><span className={`size-2 rounded-full ${state.dot}`} />{state.text}</span>
          {data && <span>v{data.version} · {data.redis_mode === 'upstash' ? 'Redis 镜像' : '本地内存'}</span>}
          <Link
            href={REPO.url}
            target="_blank"
            rel="noopener noreferrer"
            aria-label={'GitHub 仓库 ' + REPO.name + '（在新标签页打开）'}
            className="mt-0.5 flex w-fit items-center gap-1.5 text-xs text-muted no-underline hover:text-foreground"
          >
            <LogoGithub className="size-3.5 shrink-0" />
            <span className="truncate">{REPO.name}</span>
          </Link>
        </div>
      )}
    </>
  )
}

function ThemeSwitch({ isDark, onToggle }: { isDark: boolean; onToggle: () => void }) {
  const label = isDark ? '切换到浅色' : '切换到深色'
  return (
    <Tooltip delay={0}>
      <Button isIconOnly aria-label={label} variant="ghost" onPress={onToggle}>
        {isDark ? <Sun /> : <Moon />}
      </Button>
      <Tooltip.Content><p>{label}</p></Tooltip.Content>
    </Tooltip>
  )
}

/** 从上游刷新全部 WorkBuddy 账号余额，顺带同步昵称。 */
function useRefreshBalance() {
  const multiPlatform = useHasProviderData()
  const qc = useQueryClient()
  const [busy, setBusy] = useState(false)
  const run = async () => {
    if (busy) return
    setBusy(true)
    try {
      await post('balance_all')
      toast.success('余额与昵称已从上游刷新')
    } catch (e) {
      toastError(e, '刷新失败：')
    } finally {
      setBusy(false)
      void qc.invalidateQueries({ queryKey: qk.overview })
      void qc.invalidateQueries({ queryKey: qk.logs })
    }
  }
  return {
    busy,
    run,
    label: multiPlatform ? '刷新 WorkBuddy 余额' : '刷新余额',
    ariaLabel: multiPlatform ? '刷新 WorkBuddy 余额与昵称' : '刷新余额与昵称',
  }
}
