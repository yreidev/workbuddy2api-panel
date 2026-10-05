// 所有读接口的查询（TanStack Query）。打上游的接口（模型、积分包、任务、券码）不随窗口聚焦自动重查，
// 只读网关内存的接口（总览、日志、队列）按页面需要轮询；标签页切到后台时轮询自动暂停。
import { QueryClient, keepPreviousData, useQuery } from '@tanstack/react-query'
import { ApiError, acct, api, errorMessage } from './api'
import { rangeKey, rangeQuery, type TimeRange } from './timerange'
import type {
  ConfigResp, LogEntry, Model, Overview, PackageAccount, ProbesResp, QueueState, RequestEvent, RequestMetrics, Task, UsageResp,
  VoucherAccount,
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
  requests: (range: TimeRange, limit: number) => ['request_metrics', rangeKey(range), limit] as const,
  models: ['models'] as const,
  probes: ['model_probes'] as const,
  usage: (range: TimeRange) => ['usage', rangeKey(range)] as const,
  packages: ['packages'] as const,
  config: ['config'] as const,
  tasks: (uid: string) => ['tasks', uid] as const,
  queue: ['tasks-queue'] as const,
  vouchers: ['vouchers'] as const,
}

/** 总览（账号池）：账号池页 5 秒一刷，其余页面只给侧栏状态用，30 秒一刷 */
export function useOverview(intervalMs = 30_000) {
  return useQuery({
    queryKey: qk.overview,
    queryFn: () => api<Overview>('overview'),
    refetchInterval: intervalMs,
  })
}

export function useLogs(live: boolean) {
  return useQuery({
    queryKey: qk.logs,
    queryFn: () => api<{ entries: LogEntry[] | null }>('logs'),
    refetchInterval: live ? 5000 : false,
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
export function useUsage(range: TimeRange) {
  return useQuery({
    queryKey: qk.usage(range),
    queryFn: async () => {
      await queryClient.fetchQuery({ queryKey: qk.models, queryFn: fetchModels, staleTime: 10 * 60_000 }).catch(() => undefined)
      return api<UsageResp>('usage?' + rangeQuery(range, true).toString())
    },
    placeholderData: keepPreviousData,
  })
}

/** 积分包逐账号查上游（慢）：积分构成页与账号池页的到期提醒共用，两分钟内直接用缓存，页面上有「刷新」 */
export function usePackages() {
  return useQuery({
    queryKey: qk.packages,
    queryFn: () => api<{ accounts: PackageAccount[] | null }>('packages'),
    staleTime: 2 * 60_000,
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
