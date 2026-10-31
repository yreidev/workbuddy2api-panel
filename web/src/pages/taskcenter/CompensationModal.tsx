// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 积分补偿（官方不定期发放的补偿活动）：打开弹窗逐账号查状态，可单号领取或领取全部；额度与领取期限均采用官方返回值。
// 签到排程会自动检查并领取，这里是手动入口，所以和券码一样收在任务中心的按钮里，不常驻页面。
// 还没领的离截止不到 48 小时就标红（签到排程停了、号被禁用时，补偿会悄悄过期）。
import { Button, Chip, Modal, toast } from '@heroui/react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { acct, post } from '../../lib/api'
import { until } from '../../lib/format'
import { toastError, useNow } from '../../lib/hooks'
import { pkExpiryMs } from '../../lib/packages'
import { qk, useAccountNames, useCompensation } from '../../lib/queries'
import type { CompensationAccount, CompensationResp, CompensationStatus } from '../../lib/types'
import { BusyButton, Empty, Loaded } from '../../components/Feedback'

const URGENT_MS = 48 * 3600_000

export function CompensationModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const query = useCompensation(open)
  const names = useAccountNames()
  const now = useNow(60_000)
  const mutation = useMutation({
    mutationFn: async (uid: string) => {
      if (uid === '*') return post<CompensationResp>('compensation/claim_all')
      const item = await post<CompensationAccount>(acct(uid, 'compensation/claim'))
      const accounts = query.data?.accounts || []
      return { accounts: accounts.map((a) => a.uid === uid ? { ...item, disabled: a.disabled } : a) }
    },
    onSuccess: (data, uid) => {
      qc.setQueryData(qk.compensation, data)
      // 领取全部不替已禁用账号领，它们只是顺带刷新状态，不计入结果
      const acted = uid === '*' ? data.accounts.filter((a) => !a.disabled) : data.accounts.filter((a) => a.uid === uid)
      const claimed = acted.filter((a) => a.newly_claimed)
      const failed = acted.filter((a) => a.error)
      if (claimed.length) toast.success(`已为 ${claimed.length} 个账号领取 ${claimed.reduce((n, a) => n + a.credit, 0)} 积分`)
      else if (!failed.length) toast.success('没有可领取的补偿，已更新状态')
      if (failed.length) toast.warning(`${failed.length} 个账号领取失败，请查看原因后重试`)
      void qc.invalidateQueries({ queryKey: qk.overview })
      void qc.invalidateQueries({ queryKey: qk.packages, refetchType: 'none' })
    },
    onError: toastError,
  })
  const accounts = query.data?.accounts
  const bulk = accounts?.filter((a) => !a.disabled) || []
  const available = bulk.filter((a) => !a.error && a.status?.active && !a.status.claimed).length
  const retryable = bulk.some((a) => !!a.error)
  return (
    <Modal.Backdrop isOpen={open} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="md" scroll="inside" placement="auto">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>积分补偿</Modal.Heading>
            <p className="mt-1.5 text-sm text-muted">
              国内版个人账号的官方补偿活动，额度和领取期限以官方返回为准。签到排程也会自动检查并领取；已禁用账号可单独领取，不参与「领取全部」。
            </p>
          </Modal.Header>
          <Modal.Body>
            <Loaded query={query} rows={3}>
              {(d) => !d.accounts?.length ? <Empty title="没有可查询的国内版个人账号" /> : (
                <ul className="divide-y divide-separator">
                  {d.accounts.map((a) => (
                    <li key={a.uid} className="flex flex-wrap items-center gap-3 py-3 text-sm">
                      <div className="min-w-0 flex-1 basis-48">
                        <p className="flex items-center gap-1.5 break-all font-medium text-foreground">
                          {names.name(a.uid, a.nickname)}
                          {a.disabled && <Chip size="sm" variant="soft">已禁用</Chip>}
                        </p>
                        {a.error ? <p className="mt-1 break-all text-xs text-danger">{a.error}</p> : a.status && <p className="mt-1 text-xs text-muted">
                          {a.status.credit_num > 0 && `${a.status.credit_num} 积分`}
                          {a.status.end_time && <Deadline status={a.status} now={now} />}
                        </p>}
                        {a.balance_error && <p className="mt-1 break-all text-xs text-warning">补偿已领取，余额刷新失败：{a.balance_error}</p>}
                      </div>
                      <CompensationBadge account={a} />
                      <Button size="sm" variant="secondary" isPending={mutation.isPending && mutation.variables === a.uid} isDisabled={mutation.isPending || query.isFetching || (!a.error && (!a.status?.active || a.status.claimed))} onPress={() => mutation.mutate(a.uid)}>{a.error ? '重试领取' : '领取补偿'}</Button>
                    </li>
                  ))}
                </ul>
              )}
            </Loaded>
          </Modal.Body>
          <Modal.Footer>
            <BusyButton variant="secondary" busy={query.isFetching} isDisabled={mutation.isPending} onPress={() => void query.refetch()}>刷新</BusyButton>
            <BusyButton variant="primary" busy={mutation.isPending && mutation.variables === '*'} isDisabled={(!available && !retryable) || mutation.isPending || query.isFetching} onPress={() => mutation.mutate('*')}>
              {available > 0 ? `领取全部（${available}）` : retryable ? '重试领取' : '领取全部'}
            </BusyButton>
          </Modal.Footer>
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

/** 领取截止时间；上游给的是不带时区的北京时间。还没领、离截止不到 48 小时的标红，并写明还剩多久
 *  （按小时写：until 的天数向上取整，剩 30 小时会写成「2 天后」） */
function Deadline({ status: s, now }: { status: CompensationStatus; now: number }) {
  const at = pkExpiryMs({ end_time: s.end_time })
  if (at == null || !s.active || s.claimed || at - now > URGENT_MS) return <> · 领取截止 {s.end_time}</>
  const left = at <= now ? '已过截止时间' : (at - now < 3600_000 ? until(at, now) : Math.ceil((at - now) / 3600_000) + ' 小时后') + '截止'
  return <span className="text-danger"> · 领取截止 {s.end_time}（{left}）</span>
}

function CompensationBadge({ account: a }: { account: CompensationAccount }) {
  if (a.error) return <Chip size="sm" variant="soft" color="danger">失败</Chip>
  if (a.status?.claimed) return <Chip size="sm" variant="soft" color="success">已领取</Chip>
  if (a.status?.active) return <Chip size="sm" variant="soft" color="warning">可领取</Chip>
  return <Chip size="sm" variant="soft">暂无可领补偿</Chip>
}
