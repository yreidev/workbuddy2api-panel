// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号表多选后的「批量操作」菜单：测活（后台跑）、暂停与恢复选号、解冻、禁用、移除；禁用和移除先确认。
// 逐号执行，结果汇总成一条提示，有号失败时列出是哪个号、为什么。
import { useState } from 'react'
import { Button, Dropdown, Label, toast } from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { BATCH_ACTIONS, batchSummary, type BatchAction, type BatchResp } from '../../lib/accounts'
import { post } from '../../lib/api'
import { toastError, useStartedToast } from '../../lib/hooks'
import { qk } from '../../lib/queries'
import { useConfirm } from '../../components/useConfirm'

/** selected 已选并且在当前筛选结果里的 uid；onRemoved 移除完成后调用（清掉选择） */
export function BatchMenu({ selected, nameOf, onRemoved }: { selected: string[]; nameOf: (uid: string) => string; onRemoved: () => void }) {
  const qc = useQueryClient()
  const started = useStartedToast()
  const [busy, setBusy] = useState(false)
  const [confirmEl, ask] = useConfirm()

  const exec = async (action: BatchAction) => {
    const label = BATCH_ACTIONS.find((a) => a.id === action)!.label
    setBusy(true)
    try {
      if (action === 'liveness') {
        const r = await post<{ total: number }>('accounts/batch', { action, uids: selected })
        started(`已开始测活 ${r.total} 个号，结果会显示在各号卡片上`)
        return
      }
      const r = await post<BatchResp>('accounts/batch', { action, uids: selected })
      const { title, lines } = batchSummary(label, r, nameOf)
      if (lines.length === 0) toast.success(title)
      else toast.warning(title, { description: lines.map((l) => <div key={l}>{l}</div>), timeout: 8000 })
      if (action === 'remove') onRemoved()
    } catch (e) {
      toastError(e, label + '失败：')
    } finally {
      setBusy(false)
      void qc.invalidateQueries({ queryKey: qk.overview })
    }
  }

  const onAction = (action: BatchAction) => {
    const n = selected.length
    if (action === 'remove') {
      return ask({
        title: `移除选中的 ${n} 个账号？`,
        body: '移除账号将删除池状态与 auths/ 下的凭证文件，且不可恢复。',
        confirmLabel: '移除',
        onConfirm: () => void exec(action),
      })
    }
    if (action === 'disable') {
      return ask({
        title: `禁用选中的 ${n} 个账号？`,
        body: '禁用后这些账号不再参与选号（保号任务默认也跳过），需手动解冻才能恢复。若只是想临时让位、仍要保号，请改用「暂停选号」。',
        confirmLabel: '禁用',
        onConfirm: () => void exec(action),
      })
    }
    void exec(action)
  }

  return (
    <>
      <Dropdown>
        <Button size="sm" variant="secondary" isPending={busy}>批量操作</Button>
        <Dropdown.Popover placement="bottom end">
          <Dropdown.Menu onAction={(id) => onAction(id as BatchAction)}>
            {BATCH_ACTIONS.map((a) => (
              <Dropdown.Item key={a.id} id={a.id} textValue={a.label} variant={a.id === 'remove' || a.id === 'disable' ? 'danger' : undefined}>
                <Label>{a.label}</Label>
              </Dropdown.Item>
            ))}
          </Dropdown.Menu>
        </Dropdown.Popover>
      </Dropdown>
      {confirmEl}
    </>
  )
}
