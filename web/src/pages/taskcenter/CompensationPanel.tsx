// 补偿活动独立于成长任务：手动查状态，金额与领取期限均采用官方返回值。
import { Button, Chip, toast } from '@heroui/react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { acct, api, post } from '../../lib/api'
import { toastError } from '../../lib/hooks'
import { qk } from '../../lib/queries'
import type { CompensationAccount, CompensationResp } from '../../lib/types'
import { BusyButton, Empty, ErrorBox } from '../../components/Feedback'
import { Panel } from '../../components/Panel'

export function CompensationPanel() {
  const qc = useQueryClient()
  const query = useQuery({
    queryKey: qk.compensation,
    queryFn: () => api<CompensationResp>('compensation'),
    enabled: false,
    staleTime: Infinity,
  })
  const mutation = useMutation({
    mutationFn: async (uid: string) => {
      if (uid === '*') return post<CompensationResp>('compensation/claim_all')
      const item = await post<CompensationAccount>(acct(uid, 'compensation/claim'))
      const accounts = query.data?.accounts || []
      return { accounts: accounts.map((a) => a.uid === uid ? item : a) }
    },
    onSuccess: (data, uid) => {
      qc.setQueryData(qk.compensation, data)
      const acted = uid === '*' ? data.accounts : data.accounts.filter((a) => a.uid === uid)
      const claimed = acted.filter((a) => a.newly_claimed)
      const failed = acted.filter((a) => a.error)
      if (claimed.length) toast.success(`已为 ${claimed.length} 个账号领取 ${claimed.reduce((n, a) => n + a.credit, 0)} 积分`)
      else if (!failed.length) toast.success('没有可领取的补偿，已更新状态')
      if (failed.length) toast.warning(`${failed.length} 个账号领取失败，请查看下方原因后重试`)
      void qc.invalidateQueries({ queryKey: qk.overview })
      void qc.invalidateQueries({ queryKey: qk.packages, refetchType: 'none' })
    },
    onError: toastError,
  })
  const accounts = query.data?.accounts
  const available = accounts?.filter((a) => !a.error && a.status?.active && !a.status.claimed).length || 0
  const retryable = accounts?.some((a) => !!a.error)
  const refresh = async () => {
    const result = await query.refetch()
    if (result.error) toastError(result.error)
  }
  return (
    <Panel
      title="积分补偿"
      desc="查询国内版个人账号的官方补偿活动。签到排程也会自动检查并领取；已禁用账号不参与批量操作。DeepSeek 消耗积分返还由官方另行自动到账。"
      actions={<>
        <BusyButton size="sm" variant="secondary" busy={query.isFetching} isDisabled={mutation.isPending} onPress={() => void refresh()}>查询补偿</BusyButton>
        <BusyButton size="sm" variant="primary" busy={mutation.isPending && mutation.variables === '*'} isDisabled={(!available && !retryable) || mutation.isPending || query.isFetching} onPress={() => mutation.mutate('*')}>
          {available > 0 ? `领取全部（${available}）` : retryable ? '重试领取' : '领取全部'}
        </BusyButton>
      </>}
    >
      {query.error && <ErrorBox error={query.error} />}
      {!accounts ? <p className="text-sm text-muted">点击「查询补偿」查看各账号是否可领、奖励积分和领取期限。</p>
        : accounts.length === 0 ? <Empty title="没有可查询的国内版个人账号" />
          : <ul className="divide-y divide-separator">
            {accounts.map((a) => (
              <li key={a.uid} className="flex flex-wrap items-center gap-3 py-3 text-sm">
                <div className="min-w-0 flex-1 basis-48">
                  <p className="break-all font-medium">{a.nickname || a.uid}</p>
                  {a.error ? <p className="mt-1 break-all text-xs text-danger">{a.error}</p> : a.status && <p className="mt-1 text-xs text-muted">
                    {a.status.credit_num > 0 && `${a.status.credit_num} 积分`}
                    {a.status.end_time && ` · 领取截止 ${a.status.end_time}`}
                  </p>}
                  {a.balance_error && <p className="mt-1 break-all text-xs text-warning">补偿已领取，余额刷新失败：{a.balance_error}</p>}
                </div>
                <CompensationBadge account={a} />
                <Button size="sm" variant="secondary" isPending={mutation.isPending && mutation.variables === a.uid} isDisabled={mutation.isPending || query.isFetching || (!a.error && (!a.status?.active || a.status.claimed))} onPress={() => mutation.mutate(a.uid)}>{a.error ? '重试领取' : '领取补偿'}</Button>
              </li>
            ))}
          </ul>}
    </Panel>
  )
}

function CompensationBadge({ account: a }: { account: CompensationAccount }) {
  if (a.error) return <Chip size="sm" variant="soft" color="danger">失败</Chip>
  if (a.status?.claimed) return <Chip size="sm" variant="soft" color="success">已领取</Chip>
  if (a.status?.active) return <Chip size="sm" variant="soft" color="warning">可领取</Chip>
  return <Chip size="sm" variant="soft">暂无可领补偿</Chip>
}
