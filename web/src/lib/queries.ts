// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 所有读接口的查询（TanStack Query）。打上游的接口（模型、积分包、任务、券码、补偿）不随窗口聚焦自动重查，
// 只读网关内存的接口（总览、日志、队列、版本）按页面需要轮询；标签页切到后台时轮询自动暂停。
import { useMemo } from 'react'
import { QueryClient, keepPreviousData, useQuery } from '@tanstack/react-query'
import { accountName, namesOf, relabel } from './accounts'
import { ApiError, acct, api, errorMessage } from './api'
import { creditLogQuery } from './credits'
import { logArchiveQuery, type LevelFilter, type LogChannel } from './logs'
import { rangeKey, rangeQuery, type TimeRange } from './timerange'
import { SEEN_VERSION_KEY, readStored, updateBusy } from './update'
import type {
  CompensationResp, ConfigResp, CreditLogResp, GroupsResp, KeysResp, LogArchiveResp, LogEntry, LogStorage, Model, Overview, PackageAccount, ProbesResp, ProxiesResp, QueueState,
  RequestEvent, RequestMetrics, Task, UsageResp, VersionResp, VoucherAccount,
} from './types'

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // 401 由密钥框处理，重试没有意义；其余错误重试一次
      retry: (count, err) => !(err instanceof ApiError && err.status === 401) && count < 1,
      refetchOnWindowFocus: false,
    },
  },
})

export const qk = {
  overview: ['overview'] as const,
  logs: ['logs'] as const,
  /** 按时间范围查的历史运行日志；清空留存后按前缀 ['logs_archive'] 一起作废 */
  logArchive: (range: TimeRange, ch: string, level: string, q: string, limit: number) => ['logs_archive', rangeKey(range), ch, level, q, limit] as const,
  logStorage: ['log_storage'] as const,
  /** 积分获取流水；清空留存后按前缀 ['credit_log'] 一起作废 */
  creditLog: (range: TimeRange, uid: string, source: string, limit: number) => ['credit_log', rangeKey(range), uid, source, limit] as const,
  requests: (range: TimeRange, limit: number) => ['request_metrics', rangeKey(range), limit] as const,
  recentRequests: ['request_metrics', 'recent'] as const,
  models: ['models'] as const,
  probes: ['model_probes'] as const,
  usage: (range: TimeRange, key: string) => ['usage', rangeKey(range), key] as const,
  keys: ['keys'] as const,
  groups: ['groups'] as const,
  proxies: ['proxies'] as const,
  packages: ['packages'] as const,
  config: ['config'] as const,
  tasks: (uid: string) => ['tasks', uid] as const,
  queue: ['tasks-queue'] as const,
  vouchers: ['vouchers'] as const,
  compensation: ['compensation'] as const,
  version: ['version'] as const,
}

/** 总览（账号池）：账号池页 5 秒一刷，其余页面只给侧栏状态用，30 秒一刷 */
export function useOverview(intervalMs = 30_000) {
  return useQuery({
    queryKey: qk.overview,
    queryFn: () => api<Overview>('overview'),
    refetchInterval: intervalMs,
  })
}

const NO_NAMES: ReturnType<typeof namesOf> = { notes: new Map(), nicks: new Map() }

/**
 * 账号显示名（备注 > 昵称 > UID 前 8 位），给只带 uid 和昵称的接口数据（用量、积分、任务、补偿、请求记录…）
 * 补上备注；没给昵称的（代理绑了哪些号只有 uid）用总览里的昵称。备注、昵称取自外壳一直在轮询的总览缓存，
 * 这个钩子自己不发请求。
 */
export function useAccountNames() {
  const { data: names = NO_NAMES } = useQuery({ queryKey: qk.overview, queryFn: () => api<Overview>('overview'), enabled: false, select: namesOf })
  return useMemo(() => ({
    note: (uid: string) => names.notes.get(uid),
    name: (uid: string, nickname?: string) => accountName({ uid, nickname: nickname || names.nicks.get(uid), note: names.notes.get(uid) }),
    /** 请求记录里的「昵称(uid8)」标签 */
    label: (label: string) => relabel(label, names.notes),
  }), [names])
}

export function useLogs(live: boolean) {
  return useQuery({
    queryKey: qk.logs,
    queryFn: () => api<{ entries: LogEntry[] | null }>('logs'),
    refetchInterval: live ? 5000 : false,
  })
}

/**
 * 历史运行日志：按时间范围从任务日志、系统日志的留存里查，频道 / 级别 / 关键字在网关侧筛。
 * 留存是磁盘上的文件，不轮询，换条件或点「重新读取」才查；换条件时先留着上一次的结果。
 */
export function useLogArchive(range: TimeRange, ch: LogChannel, level: LevelFilter, q: string, limit: number) {
  return useQuery({
    queryKey: qk.logArchive(range, ch, level, q, limit),
    queryFn: () => api<LogArchiveResp>('logs/archive?' + logArchiveQuery(range, ch, level, q, limit).toString()),
    placeholderData: keepPreviousData,
  })
}

/** 积分获取流水：留存是磁盘上的文件，不轮询，换条件或点「重新读取」才查；换条件时先留着上一次的结果 */
export function useCreditLog(range: TimeRange, uid: string, source: string, limit: number) {
  return useQuery({
    queryKey: qk.creditLog(range, uid, source, limit),
    queryFn: () => api<CreditLogResp>('credit_log?' + creditLogQuery(range, uid, source, limit).toString()),
    placeholderData: keepPreviousData,
  })
}

/** 日志留存各类的占用（文件数、大小、最早日期）：配置页「日志留存」打开时查，保存配置或清空后重查 */
export function useLogStorage(enabled: boolean) {
  return useQuery({
    queryKey: qk.logStorage,
    queryFn: () => api<LogStorage>('log_storage'),
    enabled,
  })
}

/**
 * 请求指标：进程内统计 + 请求记录。JSONL 归档开着时请求记录以归档为准（按时间范围在归档侧取，
 * 「区间内没有记录」是真实结果，不能回落成内存里的最近请求）；归档关闭时用内存里的最近 100 条。
 * 两个接口都是可选的（旧网关没有），读失败不影响运行日志。
 */
export function useRequestMetrics(live: boolean, range: TimeRange, limit: number) {
  return useQuery({
    queryKey: qk.requests(range, limit),
    queryFn: async () => {
      const q = rangeQuery(range, false)
      q.set('limit', String(limit))
      const [metrics, archived] = await Promise.all([
        api<RequestMetrics>('request_metrics').catch((): RequestMetrics | null => null),
        api<{ entries: RequestEvent[] | null }>('request_logs?' + q.toString())
          .then((r) => ({ entries: r.entries || [], error: '' }))
          .catch((e: unknown) => ({ entries: [] as RequestEvent[], error: errorMessage(e) })),
      ])
      const archiveOn = !!metrics?.archive?.enabled
      return {
        metrics,
        archiveOn,
        recent: archiveOn ? archived.entries : metrics?.recent || [],
        archiveError: archiveOn ? archived.error : '',
      }
    },
    refetchInterval: live ? 5000 : false,
    placeholderData: keepPreviousData,
  })
}

/** 网关内存里的最近请求（最近 100 条，不查归档）：模型页「状态」列用，30 秒一刷 */
export function useRecentRequests() {
  return useQuery({
    queryKey: qk.recentRequests,
    queryFn: () => api<RequestMetrics>('request_metrics').then((m) => m.recent || []),
    refetchInterval: 30_000,
  })
}

/** 模型目录（实时查上游）：用量页借它回填积分倍率，与模型页共用同一份缓存 */
const fetchModels = () => api<{ models: Model[] | null }>('models')

/** 模型列表会实时查上游并刷新降级缓存：只在首次进入和手动「重新获取」时查 */
export function useModels() {
  return useQuery({
    queryKey: qk.models,
    queryFn: fetchModels,
    staleTime: Infinity,
  })
}

/** 实测上限是可选增强：读失败按「没有探测数据」处理，不影响模型列表 */
export function useProbes() {
  return useQuery({
    queryKey: qk.probes,
    queryFn: () => api<ProbesResp>('model_probes').catch((): ProbesResp => ({ probes: {}, exists: false })),
    staleTime: Infinity,
  })
}

/**
 * 用量。积分扣除按模型的「积分倍率」来自网关缓存的模型目录：拉用量前先确保目录在 10 分钟内查过
 * （和旧版面板同口径；模型页刚查过就直接复用，不重复打上游）。目录查询失败不影响用量本身。
 */
export function useUsage(range: TimeRange, key = '') {
  return useQuery({
    queryKey: qk.usage(range, key),
    queryFn: async () => {
      await queryClient.fetchQuery({ queryKey: qk.models, queryFn: fetchModels, staleTime: 10 * 60_000 }).catch(() => undefined)
      const q = rangeQuery(range, true)
      if (key) q.set('key', key)
      return api<UsageResp>('usage?' + q.toString())
    },
    placeholderData: keepPreviousData,
  })
}

/** 客户端密钥：列表里的今天 / 累计用量随请求变，密钥页 10 秒一刷 */
export function useKeys(intervalMs: number | false = false) {
  return useQuery({
    queryKey: qk.keys,
    queryFn: () => api<KeysResp>('keys'),
    refetchInterval: intervalMs,
  })
}

/** 账号分组（带号数和限定了它的密钥）：账号池、密钥编辑器用；增删改、号进出分组后作废重查 */
export function useGroups() {
  return useQuery({
    queryKey: qk.groups,
    queryFn: () => api<GroupsResp>('groups'),
    select: (d) => d.groups,
  })
}

/**
 * 代理池（只读网关内存）：代理页 5 秒一刷（健康检查、出口 IP 在后台变）；绑定、登录选池的弹窗打开时查一次。
 * 没装代理池（501）时返回 null，弹窗里就不显示选池。
 */
export function useProxies(intervalMs: number | false = false, enabled = true) {
  return useQuery({
    queryKey: qk.proxies,
    queryFn: () => api<ProxiesResp>('proxies').catch((e: unknown) => {
      if (e instanceof ApiError && e.status === 501) return null
      throw e
    }),
    refetchInterval: intervalMs,
    enabled,
  })
}

/**
 * 积分包逐账号查上游（慢，账号一多打开页面就卡）：积分构成页与账号池页的到期提醒共用一份缓存，
 * 只在显式要求时查（同上游 8391a05）。积分构成页没有缓存时自动查一次，有缓存就一直用、标出数据年龄；
 * 到期提醒（auto=false）从不自动查，只由「检查」按钮触发。两处的「刷新 / 检查」都是实时查。
 */
export function usePackages(auto = true) {
  return useQuery({
    queryKey: qk.packages,
    queryFn: () => api<{ accounts: PackageAccount[] | null }>('packages'),
    enabled: auto,
    staleTime: Infinity,
    gcTime: Infinity,
  })
}

export function useConfig() {
  return useQuery({
    queryKey: qk.config,
    queryFn: () => api<ConfigResp>('config'),
  })
}

export function useTasks(uid: string | null) {
  return useQuery({
    queryKey: qk.tasks(uid ?? ''),
    queryFn: () => api<{ tasks: Task[] | null }>(acct(uid!, 'tasks')),
    enabled: !!uid,
  })
}

/** 任务队列：执行中每 3 秒轮询一次，结束后停 */
export function useQueue() {
  return useQuery({
    queryKey: qk.queue,
    queryFn: () => api<QueueState>('tasks/queue'),
    refetchInterval: (q) => (q.state.data?.running ? 3000 : false),
  })
}

export function useVouchers(enabled: boolean) {
  return useQuery({
    queryKey: qk.vouchers,
    queryFn: () => api<{ accounts: VoucherAccount[] | null }>('school/vouchers'),
    enabled,
  })
}

/** 积分补偿：逐账号查官方活动状态（只查不领），打开弹窗时才查 */
export function useCompensation(enabled: boolean) {
  return useQuery({
    queryKey: qk.compensation,
    queryFn: () => api<CompensationResp>('compensation'),
    enabled,
  })
}

/**
 * 版本与更新信息：30 分钟取一次网关的检查结果（网关自己 6 小时才读一次 GitHub）。
 * 带上本浏览器上次看过的版本（没有记录时带 none），网关据此返回升级后的「本次更新」；在发请求时才读，
 * 弹窗关掉后下一次就不再带旧版本。
 */
export function useVersion() {
  return useQuery({
    queryKey: qk.version,
    queryFn: () => {
      const since = readStored(SEEN_VERSION_KEY) || 'none'
      return api<VersionResp>('version?since=' + encodeURIComponent(since))
    },
    // 一键更新进行中（含重启时连不上）每 0.8 秒看一次进度（服务器上几 MB 的安装包一两秒就下完），平时 30 分钟一次
    refetchInterval: (q) => (updateBusy(q.state.data?.self_update) ? 800 : 30 * 60_000),
  })
}
