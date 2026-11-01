// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号表多选后的「批量操作」菜单：测活（后台跑）、暂停与恢复选号、加或删不接的模型（先填模型名）、解冻、禁用、移除；
// 禁用和移除先确认。逐号执行，结果汇总成一条提示，有号失败时列出是哪个号、为什么。
import { useState } from 'react'
import { Button, Dropdown, Label, Modal, Tag, TagGroup, toast } from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { BATCH_ACTIONS, MODEL_BATCH_ACTIONS, batchSummary, type BatchAction, type BatchResp } from '../../lib/accounts'
import { post } from '../../lib/api'
import { toastError, useLast, useStartedToast } from '../../lib/hooks'
import { qk } from '../../lib/queries'
import { ModelPatternInput } from '../../components/ModelPatternInput'
import { useConfirm } from '../../components/useConfirm'

/** selected 已选并且在当前筛选结果里的 uid；onRemoved 移除完成后调用（清掉选择） */
export function BatchMenu({ selected, nameOf, onRemoved }: { selected: string[]; nameOf: (uid: string) => string; onRemoved: () => void }) {
  const qc = useQueryClient()
  const started = useStartedToast()
  const [busy, setBusy] = useState(false)
  const [modelAction, setModelAction] = useState<BatchAction | null>(null)
  const [confirmEl, ask] = useConfirm()

  const exec = async (action: BatchAction, models?: string[]) => {
    const label = BATCH_ACTIONS.find((a) => a.id === action)!.label
    setBusy(true)
    try {
      if (action === 'liveness') {
        const r = await post<{ total: number }>('accounts/batch', { action, uids: selected })
        started(`已开始测活 ${r.total} 个号，结果会显示在各号卡片上`)
        return
      }
      const r = await post<BatchResp>('accounts/batch', { action, uids: selected, models })
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
    if (MODEL_BATCH_ACTIONS.includes(action)) return setModelAction(action)
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
      <ModelsModal
        action={modelAction}
        n={selected.length}
        onClose={() => setModelAction(null)}
        onSubmit={(action, models) => {
          setModelAction(null)
          void exec(action, models)
        }}
      />
    </>
  )
}

/** 批量加、删不接的模型前先填模型名 */
function ModelsModal({ action, n, onClose, onSubmit }: {
  action: BatchAction | null
  n: number
  onClose: () => void
  onSubmit: (action: BatchAction, models: string[]) => void
}) {
  const shown = useLast(action)
  const adding = shown === 'block_models'
  return (
    <Modal.Backdrop isOpen={action != null} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="md" placement="auto">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>{adding ? `给 ${n} 个号加不接的模型` : `从 ${n} 个号删不接的模型`}</Modal.Heading>
          </Modal.Header>
          {/* 每次打开都从空的开始填：关闭时卸载内容 */}
          {action != null && <ModelsForm adding={adding} onClose={onClose} onSubmit={(models) => onSubmit(action, models)} />}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

function ModelsForm({ adding, onClose, onSubmit }: { adding: boolean; onClose: () => void; onSubmit: (models: string[]) => void }) {
  const [models, setModels] = useState<string[]>([])
  return (
    <>
      <Modal.Body className="flex flex-col gap-2">
        <p className="text-sm text-muted">
          {adding
            ? '选号时不再把这些模型派给选中的号。按不带 cn: / global: 前缀的模型名匹配，支持 * 通配。'
            : '从选中的号的名单里删掉同名的项，上游拒绝后自动加的也一样删（以后会再试这个模型）。要和名单里写的一字不差，比如删 gpt-* 不会删掉 gpt-5。'}
        </p>
        {models.length > 0 && (
          <TagGroup aria-label="要处理的模型" onRemove={(keys) => setModels(models.filter((m) => !keys.has(m)))}>
            <TagGroup.List items={models.map((m) => ({ id: m }))}>
              {(m) => <Tag id={m.id} textValue={m.id} className="font-mono">{m.id}</Tag>}
            </TagGroup.List>
          </TagGroup>
        )}
        <ModelPatternInput value={models} onChange={setModels} />
      </Modal.Body>
      <Modal.Footer>
        <Button variant="tertiary" onPress={onClose}>取消</Button>
        <Button isDisabled={models.length === 0} onPress={() => onSubmit(models)}>{adding ? '加上' : '删掉'}</Button>
      </Modal.Footer>
    </>
  )
}
