// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 修改一个代理：名字、地址（密码还是 *** 就沿用原密码）、池、到期时间。换了池，绑在上面的号跟着换池；
// 改了地址，检测结果清空重测；续费后改到期时间，到期的代理就恢复。
import { useState } from 'react'
import { Button, Description, Input, Label, ListBox, Modal, Select, TextField, toast } from '@heroui/react'
import { post } from '../../lib/api'
import { timeMs } from '../../lib/format'
import { toastError, useLast } from '../../lib/hooks'
import type { ProxyInput, ProxyItem, ProxyPool } from '../../lib/types'
import { ExpiryField } from '../../components/ExpiryField'
import { BusyButton } from '../../components/Feedback'

export function ProxyEditor({ target, pools, onClose, onSaved }: {
  target: ProxyItem | null
  pools: ProxyPool[]
  onClose: () => void
  onSaved: () => void
}) {
  const shown = useLast(target)
  return (
    <Modal.Backdrop isOpen={target != null} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="md" placement="auto">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>修改代理「{shown?.name}」</Modal.Heading>
          </Modal.Header>
          {target != null && <EditorBody key={target.id} p={target} pools={pools} onClose={onClose} onSaved={onSaved} />}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

function EditorBody({ p, pools, onClose, onSaved }: { p: ProxyItem; pools: ProxyPool[]; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(p.name)
  const [url, setUrl] = useState(p.url)
  const [pool, setPool] = useState(p.pool)
  const [expires, setExpires] = useState<number | null>(timeMs(p.expires_at))
  const [saving, setSaving] = useState(false)
  const moving = pool !== p.pool && p.accounts.length > 0

  const save = async () => {
    setSaving(true)
    const input: ProxyInput = {
      name: name.trim(),
      url: url.trim(),
      pool,
      expires_at: expires == null ? null : new Date(expires).toISOString(),
      clear_expiry: expires == null,
    }
    try {
      await post(`proxies/${encodeURIComponent(p.id)}`, input)
      toast.success(`代理「${input.name || p.name}」已保存`, url.trim() !== p.url ? { description: '地址改了，正在重新测连通和出口 IP。' } : undefined)
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
        <TextField value={name} onChange={setName} autoComplete="off">
          <Label>名字</Label>
          <Input placeholder={p.host} />
          <Description>账号池卡片、请求记录里显示这个名字；空着不改。</Description>
        </TextField>
        <TextField value={url} onChange={setUrl} autoComplete="off">
          <Label>地址</Label>
          <Input className="font-mono" spellCheck={false} />
          <Description>密码显示成 ***：只改主机、端口时保持 *** 就沿用原密码。改了地址会清空检测结果，马上重测。</Description>
        </TextField>
        <Select value={pool} onChange={(k) => k != null && setPool(String(k))}>
          <Label>池</Label>
          <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
          <Select.Popover>
            <ListBox>
              {pools.map((pl) => <ListBox.Item key={pl.id} id={pl.id} textValue={pl.name}>{pl.name}<ListBox.ItemIndicator /></ListBox.Item>)}
            </ListBox>
          </Select.Popover>
          <Description>{moving ? `绑在上面的 ${p.accounts.length} 个号跟着换到新池。` : '换了池，绑在上面的号跟着换到新池。'}</Description>
        </Select>
        <ExpiryField
          value={expires}
          onChange={setExpires}
          description="到期前几天在代理页和账号池页提醒；到期后按不可用处理（绑在上面的号暂停出站），续费后把到期时间往后改就恢复。"
          pastWarning="这个时间已经过了，保存后这个代理马上按到期处理。"
          presets={[30, 90, 365]}
        />
      </Modal.Body>
      <Modal.Footer>
        <Button variant="tertiary" onPress={onClose}>取消</Button>
        <BusyButton busy={saving} isDisabled={!url.trim()} onPress={() => void save()}>保存</BusyButton>
      </Modal.Footer>
    </>
  )
}
