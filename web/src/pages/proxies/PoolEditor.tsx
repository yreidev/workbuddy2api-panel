// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 新建、修改代理池：名字、每个代理最多几个号（0 = 不限）、设为国内版 / 国际版新号的默认池。
// 新设成默认池时问一句要不要把这个域现有没绑代理的号也放进来（默认不放：放进来就换了这些号的出口 IP）。
import { useState } from 'react'
import { Button, Checkbox, Description, FieldError, Input, Label, Modal, NumberField, TextField, toast } from '@heroui/react'
import { post } from '../../lib/api'
import { toastError, useLast } from '../../lib/hooks'
import { useOverview } from '../../lib/queries'
import type { ProxyAccount, ProxyPool, ProxyPoolInput } from '../../lib/types'
import { BusyButton } from '../../components/Feedback'

const REALM_LABEL: Record<string, string> = { cn: '国内版', global: '国际版' }

export function PoolEditor({ target, accounts, onClose, onSaved }: {
  /** new = 新建；null = 关闭 */
  target: ProxyPool | 'new' | null
  /** 每个号的代理情况（算现有没绑代理的号有几个） */
  accounts: Record<string, ProxyAccount>
  onClose: () => void
  onSaved: () => void
}) {
  const shown = useLast(target)
  const title = shown && shown !== 'new' ? `修改池「${shown.name}」` : '新建代理池'
  const editing = target && target !== 'new' ? target : null
  return (
    <Modal.Backdrop isOpen={target != null} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="md" placement="auto">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>{title}</Modal.Heading>
          </Modal.Header>
          {target != null && <EditorBody key={editing?.id ?? 'new'} editing={editing} accounts={accounts} onClose={onClose} onSaved={onSaved} />}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

function EditorBody({ editing, accounts, onClose, onSaved }: {
  editing: ProxyPool | null
  accounts: Record<string, ProxyAccount>
  onClose: () => void
  onSaved: () => void
}) {
  const overview = useOverview()
  const was = editing?.default_for ?? []
  const [name, setName] = useState(editing?.name ?? '')
  const [max, setMax] = useState(editing?.max_per_proxy ?? 0)
  const [cn, setCn] = useState(was.includes('cn'))
  const [global, setGlobal] = useState(was.includes('global'))
  const [adopt, setAdopt] = useState(false)
  const [saving, setSaving] = useState(false)

  // 新设成哪些域的默认池；这些域里现有没绑代理的号（放进来的就是它们，同网关 adoptLocked）
  const realms = [cn && !was.includes('cn') && 'cn', global && !was.includes('global') && 'global'].filter((r): r is string => !!r)
  const defaults = [cn && 'cn', global && 'global'].filter((r): r is string => !!r)
  const unbound = (overview.data?.accounts || []).filter((a) => !accounts[a.uid]?.pool && defaults.includes(a.realm === 'global' ? 'global' : 'cn')).length
  const askAdopt = realms.length > 0 && unbound > 0
  const trimmed = name.trim()

  const save = async () => {
    setSaving(true)
    const input: ProxyPoolInput = { name: trimmed, max_per_proxy: max, default_cn: cn, default_global: global, adopt_unbound: askAdopt && adopt }
    try {
      await post(editing ? `proxy_pools/${encodeURIComponent(editing.id)}` : 'proxy_pools', input)
      toast.success(editing ? `池「${trimmed}」已保存` : `池「${trimmed}」已建好`, input.adopt_unbound ? { description: `现有没绑代理的 ${unbound} 个号放进来了` } : undefined)
      onSaved()
      onClose()
    } catch (e) {
      toastError(e, '保存失败：')
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <Modal.Body className="flex flex-col gap-4">
        <TextField value={name} onChange={setName} isInvalid={!trimmed && name !== ''} autoComplete="off" autoFocus>
          <Label>名字</Label>
          <Input placeholder="比如「国内」「国际版」「香港住宅」" />
          {!trimmed && name !== '' ? <FieldError>名字不能为空</FieldError> : <Description>号绑在池上，账号池页和登录、导入账号时按名字选。</Description>}
        </TextField>

        <NumberField value={max} onChange={(n) => setMax(Number.isNaN(n) || n < 0 ? 0 : n)} minValue={0} formatOptions={{ maximumFractionDigits: 0 }}>
          <Label>每个代理最多几个号</Label>
          <NumberField.Group>
            <NumberField.DecrementButton />
            <NumberField.Input placeholder="0 = 不限" />
            <NumberField.IncrementButton />
          </NumberField.Group>
          <Description>0 = 不限。满了新号等着（不接请求），加代理或调高上限后自动补上；调低上限不会挪走已经绑着的号（标「超上限」）。</Description>
        </NumberField>

        <div className="flex flex-col gap-2">
          <span className="text-sm font-medium">默认池</span>
          <Checkbox isSelected={cn} onChange={setCn}>
            <Checkbox.Content>
              <Checkbox.Control><Checkbox.Indicator /></Checkbox.Control>
              国内版新号的默认池
            </Checkbox.Content>
          </Checkbox>
          <Checkbox isSelected={global} onChange={setGlobal}>
            <Checkbox.Content>
              <Checkbox.Control><Checkbox.Indicator /></Checkbox.Control>
              国际版新号的默认池
            </Checkbox.Content>
          </Checkbox>
          <p className="text-xs text-muted">新登录、导入的号没选池时放进默认池。一个域只能有一个默认池，设在这里，别的池的默认就取消。</p>
        </div>

        {askAdopt && (
          <Checkbox isSelected={adopt} onChange={setAdopt} className="rounded-xl bg-surface-secondary p-3">
            <Checkbox.Content>
              <Checkbox.Control><Checkbox.Indicator /></Checkbox.Control>
              把现有没绑代理的{defaults.map((r) => REALM_LABEL[r]).join('、')}号（{unbound} 个）也放进这个池
            </Checkbox.Content>
            <Description>放进来的号按绑号最少分到池里的代理，出口 IP 从服务器换成代理的；池里没有空位的先等着。</Description>
          </Checkbox>
        )}
      </Modal.Body>
      <Modal.Footer>
        <Button variant="tertiary" onPress={onClose}>取消</Button>
        <BusyButton busy={saving} isDisabled={!trimmed} onPress={() => void save()}>{editing ? '保存' : '建池'}</BusyButton>
      </Modal.Footer>
    </>
  )
}
