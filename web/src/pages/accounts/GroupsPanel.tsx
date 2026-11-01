// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号分组（账号池页下方）：建、改名、写备注、删；每组看有几个号、哪些客户端密钥限定了它，点「筛选」只看组里的号。
// 号进出分组在账号详情里改，或多选后批量加进、退出。分组本身不影响选号，客户端密钥限定了分组才只用组里的号。
import { useState } from 'react'
import { Button, Card, Description, FieldError, Input, Label, Modal, TextField, toast } from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { post } from '../../lib/api'
import { toastError, useLast } from '../../lib/hooks'
import { qk } from '../../lib/queries'
import type { Group } from '../../lib/types'
import { BusyButton } from '../../components/Feedback'
import { InfoTip, Panel } from '../../components/Panel'
import { useConfirm } from '../../components/useConfirm'

/** 分组名字、备注最长几个字（同 internal/panel/groups.go） */
const NAME_MAX = 20
const NOTE_MAX = 100

export function GroupsPanel({ groups, onFilter }: { groups: Group[]; onFilter: (id: string) => void }) {
  const qc = useQueryClient()
  const [edit, setEdit] = useState<Group | 'new' | null>(null)
  const [confirmEl, ask] = useConfirm()
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: qk.groups })
    void qc.invalidateQueries({ queryKey: qk.overview })
  }
  const remove = (g: Group) => {
    if (g.keys.length > 0) {
      toast.danger(`这些密钥限定了分组「${g.name}」，先在密钥里去掉它：${g.keys.join('、')}`)
      return
    }
    ask({
      title: `删掉分组「${g.name}」？`,
      body: g.accounts ? `组里的 ${g.accounts} 个号退出这个组，号本身不动。` : '组里没有号。',
      confirmLabel: '删掉',
      onConfirm: () => void post(`groups/${encodeURIComponent(g.id)}/delete`).then(() => {
        toast.success(`分组「${g.name}」已删掉`)
        refresh()
      }, (e: unknown) => toastError(e, '删除失败：')),
    })
  }

  return (
    <Panel
      title={
        <span className="flex items-center gap-1">
          账号分组
          <InfoTip label="账号分组说明">
            分组用来给客户端密钥划号：密钥限定了分组，用它的请求就只用组里的号，组里的号都接不了时回 503，不借别的号；没限定的密钥照常用全部的号。{'\n'}
            一个号可以在好几个组里。号进出分组在账号详情里改，或在账号表多选后「批量操作」。{'\n'}
            先用哪个号看号的优先级（账号详情里设），和分组无关。
          </InfoTip>
        </span>
      }
      desc={groups.length ? undefined : '还没有分组。要让某个客户端密钥只用一部分号时，先建分组、把号放进去，再在密钥里勾上它。'}
      actions={<Button size="sm" variant="secondary" onPress={() => setEdit('new')}>新建分组</Button>}
    >
      {groups.length > 0 && (
        <div className="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(100%,260px),1fr))]">
          {groups.map((g) => (
            <Card key={g.id} variant="secondary" className="gap-2 p-4">
              <span className="truncate font-medium">{g.name}</span>
              {g.note && <span className="line-clamp-2 text-sm text-muted">{g.note}</span>}
              <span className="text-sm">{g.accounts} 个号</span>
              <span className="text-xs text-muted">{g.keys.length ? '限定它的密钥：' + g.keys.join('、') : '没有密钥限定它'}</span>
              <div className="-mx-1 mt-auto flex flex-wrap gap-1">
                <Button size="sm" variant="ghost" isDisabled={g.accounts === 0} onPress={() => onFilter(g.id)}>筛选</Button>
                <Button size="sm" variant="ghost" onPress={() => setEdit(g)}>改</Button>
                <Button size="sm" variant="ghost" className="text-danger" onPress={() => remove(g)}>删</Button>
              </div>
            </Card>
          ))}
        </div>
      )}
      <GroupEditor target={edit} onClose={() => setEdit(null)} onSaved={refresh} />
      {confirmEl}
    </Panel>
  )
}

function GroupEditor({ target, onClose, onSaved }: { target: Group | 'new' | null; onClose: () => void; onSaved: () => void }) {
  const shown = useLast(target)
  const editing = target && target !== 'new' ? target : null
  return (
    <Modal.Backdrop isOpen={target != null} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="md" placement="auto">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>{shown && shown !== 'new' ? `修改分组「${shown.name}」` : '新建分组'}</Modal.Heading>
          </Modal.Header>
          {target != null && <EditorBody key={editing?.id ?? 'new'} editing={editing} onClose={onClose} onSaved={onSaved} />}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

function EditorBody({ editing, onClose, onSaved }: { editing: Group | null; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(editing?.name ?? '')
  const [note, setNote] = useState(editing?.note ?? '')
  const [saving, setSaving] = useState(false)
  const trimmed = name.trim()
  const nameErr = name !== '' && !trimmed ? '名字不能为空' : [...trimmed].length > NAME_MAX ? `最多 ${NAME_MAX} 个字` : ''
  const noteErr = [...note.trim()].length > NOTE_MAX ? `最多 ${NOTE_MAX} 个字` : ''

  const save = async () => {
    setSaving(true)
    try {
      await post(editing ? `groups/${encodeURIComponent(editing.id)}` : 'groups', { name: trimmed, note: note.trim() })
      toast.success(editing ? `分组「${trimmed}」已保存` : `分组「${trimmed}」已建好`)
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
        <TextField value={name} onChange={setName} isInvalid={!!nameErr} autoComplete="off" autoFocus>
          <Label>名字</Label>
          <Input placeholder="比如「给小王」「国际版」「备用」" />
          {nameErr ? <FieldError>{nameErr}</FieldError> : <Description>不能和别的分组重名。改名不影响已经放进来的号和限定了它的密钥。</Description>}
        </TextField>
        <TextField value={note} onChange={setNote} isInvalid={!!noteErr}>
          <Label>备注</Label>
          <Input placeholder="可选：这组号拿来做什么" />
          {noteErr && <FieldError>{noteErr}</FieldError>}
        </TextField>
      </Modal.Body>
      <Modal.Footer>
        <Button variant="tertiary" onPress={onClose}>取消</Button>
        <BusyButton busy={saving} isDisabled={!trimmed || !!nameErr || !!noteErr} onPress={() => void save()}>{editing ? '保存' : '建好'}</BusyButton>
      </Modal.Footer>
    </>
  )
}
