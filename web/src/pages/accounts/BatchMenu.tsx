// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号表多选后的「批量操作」菜单：测活（后台跑）、暂停与恢复选号、加进或退出分组（先选分组）、设优先级（先填数）、
// 加或删不接的模型（先填模型名）、解冻、禁用、移除；禁用和移除先确认。逐号执行，结果汇总成一条提示，有号失败时
// 列出是哪个号、为什么。还没建分组时不给加进、退出分组。
import { useState } from 'react'
import { Button, Description, Dropdown, Label, ListBox, Modal, NumberField, Select, Tag, TagGroup, toast } from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { BATCH_ACTIONS, MAX_PRIORITY, batchParam, batchSummary, type BatchAction, type BatchResp } from '../../lib/accounts'
import { post } from '../../lib/api'
import { toastError, useLast, useStartedToast } from '../../lib/hooks'
import { qk, useGroups } from '../../lib/queries'
import type { Group } from '../../lib/types'
import { ModelPatternInput } from '../../components/ModelPatternInput'
import { useConfirm } from '../../components/useConfirm'

/** 要先填参数的批量操作填好的参数，原样放进请求体 */
interface BatchParams { models?: string[]; group?: string; priority?: number }

/** selected 已选并且在当前筛选结果里的 uid；onRemoved 移除完成后调用（清掉选择） */
export function BatchMenu({ selected, nameOf, onRemoved }: { selected: string[]; nameOf: (uid: string) => string; onRemoved: () => void }) {
  const qc = useQueryClient()
  const started = useStartedToast()
  const groups = useGroups().data ?? []
  const [busy, setBusy] = useState(false)
  const [paramAction, setParamAction] = useState<BatchAction | null>(null)
  const [confirmEl, ask] = useConfirm()
  const actions = groups.length ? BATCH_ACTIONS : BATCH_ACTIONS.filter((a) => batchParam(a.id) !== 'group')

  const exec = async (action: BatchAction, params?: BatchParams) => {
    let label = BATCH_ACTIONS.find((a) => a.id === action)!.label
    if (params?.group) label += '「' + (groups.find((g) => g.id === params.group)?.name ?? params.group) + '」'
    setBusy(true)
    try {
      if (action === 'liveness') {
        const r = await post<{ total: number }>('accounts/batch', { action, uids: selected })
        started(`已开始测活 ${r.total} 个号，结果会显示在各号卡片上`)
        return
      }
      const r = await post<BatchResp>('accounts/batch', { action, uids: selected, ...params })
      const { title, lines } = batchSummary(label, r, nameOf)
      if (lines.length === 0) toast.success(title)
      else toast.warning(title, { description: lines.map((l) => <div key={l}>{l}</div>), timeout: 8000 })
      if (action === 'remove') onRemoved()
    } catch (e) {
      toastError(e, label + '失败：')
    } finally {
      setBusy(false)
      void qc.invalidateQueries({ queryKey: qk.overview })
      if (batchParam(action) === 'group' || action === 'remove') void qc.invalidateQueries({ queryKey: qk.groups })
    }
  }

  const onAction = (action: BatchAction) => {
    const n = selected.length
    if (batchParam(action)) return setParamAction(action)
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
            {actions.map((a) => (
              <Dropdown.Item key={a.id} id={a.id} textValue={a.label} variant={a.id === 'remove' || a.id === 'disable' ? 'danger' : undefined}>
                <Label>{a.label}</Label>
              </Dropdown.Item>
            ))}
          </Dropdown.Menu>
        </Dropdown.Popover>
      </Dropdown>
      {confirmEl}
      <ParamModal
        action={paramAction}
        n={selected.length}
        groups={groups}
        onClose={() => setParamAction(null)}
        onSubmit={(action, params) => {
          setParamAction(null)
          void exec(action, params)
        }}
      />
    </>
  )
}

const PARAM_TITLE: Partial<Record<BatchAction, (n: number) => string>> = {
  join_group: (n) => `把 ${n} 个号加进分组`,
  leave_group: (n) => `让 ${n} 个号退出分组`,
  priority: (n) => `设 ${n} 个号的优先级`,
  block_models: (n) => `给 ${n} 个号加不接的模型`,
  unblock_models: (n) => `从 ${n} 个号删不接的模型`,
}

/** 要先填参数的批量操作：选分组、填优先级或模型名，再执行 */
function ParamModal({ action, n, groups, onClose, onSubmit }: {
  action: BatchAction | null
  n: number
  groups: Group[]
  onClose: () => void
  onSubmit: (action: BatchAction, params: BatchParams) => void
}) {
  const shown = useLast(action)
  const kind = shown && batchParam(shown)
  return (
    <Modal.Backdrop isOpen={action != null} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="md" placement="auto">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>{shown && PARAM_TITLE[shown]?.(n)}</Modal.Heading>
          </Modal.Header>
          {/* 每次打开都从空的开始填：关闭时卸载内容 */}
          {action != null && kind === 'models' && (
            <ModelsForm adding={action === 'block_models'} onClose={onClose} onSubmit={(models) => onSubmit(action, { models })} />
          )}
          {action != null && kind === 'group' && (
            <GroupForm leaving={action === 'leave_group'} groups={groups} onClose={onClose} onSubmit={(group) => onSubmit(action, { group })} />
          )}
          {action != null && kind === 'priority' && <PriorityForm onClose={onClose} onSubmit={(priority) => onSubmit(action, { priority })} />}
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

function GroupForm({ leaving, groups, onClose, onSubmit }: { leaving: boolean; groups: Group[]; onClose: () => void; onSubmit: (group: string) => void }) {
  const [group, setGroup] = useState<string | null>(null)
  return (
    <>
      <Modal.Body>
        <Select value={group} onChange={(k) => setGroup(k == null ? null : String(k))} placeholder="选一个分组">
          <Label>分组</Label>
          <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
          <Select.Popover>
            <ListBox>
              {groups.map((g) => <ListBox.Item key={g.id} id={g.id} textValue={g.name}>{g.name}<ListBox.ItemIndicator /></ListBox.Item>)}
            </ListBox>
          </Select.Popover>
          <Description>
            {leaving ? '不在这个组里的号不动，其余分组也不动。' : '已经在这个组里的号不动；一个号可以同时在好几个组里，原来的分组照旧。'}
          </Description>
        </Select>
      </Modal.Body>
      <Modal.Footer>
        <Button variant="tertiary" onPress={onClose}>取消</Button>
        <Button isDisabled={group == null} onPress={() => group != null && onSubmit(group)}>{leaving ? '退出' : '加进去'}</Button>
      </Modal.Footer>
    </>
  )
}

function PriorityForm({ onClose, onSubmit }: { onClose: () => void; onSubmit: (priority: number) => void }) {
  const [n, setN] = useState(0)
  return (
    <>
      <Modal.Body>
        <PriorityField value={n} onChange={setN} />
      </Modal.Body>
      <Modal.Footer>
        <Button variant="tertiary" onPress={onClose}>取消</Button>
        <Button onPress={() => onSubmit(n)}>设好</Button>
      </Modal.Footer>
    </>
  )
}

/** 优先级输入（0～99 的整数，清空当 0）：批量设和账号详情共用 */
export function PriorityField({ value, onChange, className }: { value: number; onChange: (n: number) => void; className?: string }) {
  return (
    <NumberField
      value={value}
      onChange={(v) => onChange(Number.isNaN(v) ? 0 : Math.min(MAX_PRIORITY, Math.max(0, Math.round(v))))}
      minValue={0}
      maxValue={MAX_PRIORITY}
      formatOptions={{ maximumFractionDigits: 0 }}
      className={className}
    >
      <Label>优先级</Label>
      <NumberField.Group>
        <NumberField.DecrementButton />
        <NumberField.Input />
        <NumberField.IncrementButton />
      </NumberField.Group>
      <Description>0～{MAX_PRIORITY}，越小越先用，默认 0。选号只在最小的那一层里挑，那层的号都接不了（冷却、满载、积分保底等）才轮到下一层。</Description>
    </NumberField>
  )
}
