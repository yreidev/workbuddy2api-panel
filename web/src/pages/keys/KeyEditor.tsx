// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 新建 / 编辑客户端密钥的弹窗，和建好后的「接入方法」弹窗（密钥原文 + 各客户端的填法）。
import { useMemo, useState } from 'react'
import { Copy } from '@gravity-ui/icons'
import {
  Button, Calendar, ComboBox, DateField, DatePicker, type DateValue, Description, Input, Label, ListBox, Modal, NumberField, Surface, Switch, Tag, TagGroup, TextArea, TextField, toast,
} from '@heroui/react'
import { CalendarDateTime, getLocalTimeZone } from '@internationalized/date'
import { useQueryClient } from '@tanstack/react-query'
import { post } from '../../lib/api'
import { copyText } from '../../lib/clipboard'
import { toastError, useNow } from '../../lib/hooks'
import {
  LIMIT_KINDS, addModelPattern, allowedModels, bareModelIds, emptyKeyForm, keyFormFrom, keyInput, validateKeyForm,
  type KeyForm, type LimitForm,
} from '../../lib/keys'
import { qk, useModels } from '../../lib/queries'
import type { ApiKey } from '../../lib/types'
import { BusyButton } from '../../components/Feedback'

const toValue = (ms: number) => {
  const d = new Date(ms)
  return new CalendarDateTime(d.getFullYear(), d.getMonth() + 1, d.getDate(), d.getHours(), d.getMinutes())
}
const DAY = 86_400_000

/** 弹窗关闭时留住上一次的内容，退场动画里标题不会变空或变成别的 */
function useLast<T>(v: T | null): T | null {
  const [last, setLast] = useState<T | null>(v)
  if (v != null && v !== last) setLast(v)
  return v ?? last
}

export function KeyEditor({ target, onClose, onCreated }: {
  /** new = 新建；null = 关闭 */
  target: ApiKey | 'new' | null
  onClose: () => void
  onCreated: (k: ApiKey) => void
}) {
  const editing = target && target !== 'new' ? target : null
  const shown = useLast(target)
  const title = shown && shown !== 'new' ? `编辑「${shown.name}」` : '新建客户端密钥'
  return (
    <Modal.Backdrop isOpen={target != null} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="lg" scroll="inside" placement="auto">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>{title}</Modal.Heading>
          </Modal.Header>
          {/* 每次打开都从头填：关闭时卸载内容 */}
          {target != null && <EditorBody key={editing?.id ?? 'new'} editing={editing} onClose={onClose} onCreated={onCreated} />}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

function EditorBody({ editing, onClose, onCreated }: { editing: ApiKey | null; onClose: () => void; onCreated: (k: ApiKey) => void }) {
  const qc = useQueryClient()
  const [form, setForm] = useState<KeyForm>(() => (editing ? keyFormFrom(editing) : emptyKeyForm()))
  const [saving, setSaving] = useState(false)
  const set = (patch: Partial<KeyForm>) => setForm((f) => ({ ...f, ...patch }))
  const error = validateKeyForm(form)

  const save = async () => {
    if (error) {
      toast.danger(error)
      return
    }
    setSaving(true)
    try {
      const path = editing ? 'keys/' + encodeURIComponent(editing.id) : 'keys'
      const r = await post<{ ok: boolean; key: ApiKey }>(path, keyInput(form))
      void qc.invalidateQueries({ queryKey: qk.keys })
      if (editing) {
        toast.success(`已保存「${r.key.name}」`)
        onClose()
      } else {
        onCreated(r.key)
      }
    } catch (e) {
      toastError(e, '保存失败：')
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <Modal.Body className="flex flex-col gap-5">
        <div className="grid gap-3 sm:grid-cols-[1fr_auto] sm:items-end">
          <TextField value={form.name} onChange={(v) => set({ name: v })} isRequired autoComplete="off" autoFocus>
            <Label>名称</Label>
            <Input placeholder="比如使用者的名字" />
          </TextField>
          <Switch isSelected={form.enabled} onChange={(v) => set({ enabled: v })} className="pb-2">
            <Switch.Content>
              <Switch.Control><Switch.Thumb /></Switch.Control>
              启用
            </Switch.Content>
          </Switch>
        </div>
        <TextField value={form.note} onChange={(v) => set({ note: v })}>
          <Label>备注</Label>
          <TextArea rows={2} placeholder="可选：给谁用、什么时候给的" />
        </TextField>

        <ExpiryField value={form.expires} onChange={(expires) => set({ expires })} />

        <fieldset className="flex flex-col gap-3">
          <legend className="mb-1 text-sm font-medium">用量上限</legend>
          <p className="-mt-1 text-xs text-muted">不填或填 0 = 不限。请求次数只算成功的请求。到上限后网关回 429，告诉客户端别自动重试。</p>
          <LimitRow title="每天（北京时间 00:00 重置）" value={form.daily} onChange={(daily) => set({ daily })} />
          <LimitRow title="总量" value={form.total} onChange={(total) => set({ total })} />
        </fieldset>

        <ModelsField value={form.models} onChange={(models) => set({ models })} />
      </Modal.Body>
      <Modal.Footer>
        {error && form.name.trim() !== '' && <span className="mr-auto text-sm text-danger">{error}</span>}
        <Button variant="tertiary" onPress={onClose}>取消</Button>
        <BusyButton busy={saving} isDisabled={!!error} onPress={() => void save()}>{editing ? '保存' : '创建'}</BusyButton>
      </Modal.Footer>
    </>
  )
}

/** 到期时间：不过期 / 选一个时刻（本机时区），另给几个常用时长 */
function ExpiryField({ value, onChange }: { value: number | null; onChange: (ms: number | null) => void }) {
  const now = useNow(30_000)
  const pick = (v: DateValue | null) => onChange(v ? v.toDate(getLocalTimeZone()).getTime() : null)
  const after = (days: number) => {
    const d = new Date(Date.now() + days * DAY)
    d.setSeconds(0, 0)
    onChange(d.getTime())
  }
  return (
    <div className="flex flex-col gap-2">
      <Switch isSelected={value != null} onChange={(on) => (on ? after(30) : onChange(null))}>
        <Switch.Content>
          <Switch.Control><Switch.Thumb /></Switch.Control>
          设到期时间
        </Switch.Content>
        <Description>不设就一直有效；到期后网关回 401 并说明已过期。</Description>
      </Switch>
      {value != null && (
        <div className="flex flex-wrap items-end gap-2">
          <DatePicker aria-label="到期时间" granularity="minute" hourCycle={24} value={toValue(value)} onChange={pick} className="w-52">
            <DateField.Group>
              <DateField.Input>{(segment) => <DateField.Segment segment={segment} />}</DateField.Input>
              <DateField.Suffix>
                <DatePicker.Trigger><DatePicker.TriggerIndicator /></DatePicker.Trigger>
              </DateField.Suffix>
            </DateField.Group>
            <DatePicker.Popover>
              <Calendar aria-label="选择到期日期">
                <Calendar.Header>
                  <Calendar.Heading />
                  <Calendar.NavButton slot="previous" />
                  <Calendar.NavButton slot="next" />
                </Calendar.Header>
                <Calendar.Grid>
                  <Calendar.GridHeader>{(day) => <Calendar.HeaderCell>{day}</Calendar.HeaderCell>}</Calendar.GridHeader>
                  <Calendar.GridBody>{(date) => <Calendar.Cell date={date} />}</Calendar.GridBody>
                </Calendar.Grid>
              </Calendar>
            </DatePicker.Popover>
          </DatePicker>
          {[7, 30, 90].map((n) => <Button key={n} size="sm" variant="ghost" onPress={() => after(n)}>{n} 天后</Button>)}
          {value <= now && <span className="w-full text-xs text-warning">这个时间已经过了，保存后密钥立即不能用。</span>}
        </div>
      )}
    </div>
  )
}

function LimitRow({ title, value, onChange }: { title: string; value: LimitForm; onChange: (v: LimitForm) => void }) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-sm text-muted">{title}</span>
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
        {LIMIT_KINDS.map((k) => (
          <NumberField
            key={k.id}
            value={value[k.id] ?? Number.NaN}
            onChange={(n) => onChange({ ...value, [k.id]: Number.isNaN(n) || n <= 0 ? null : n })}
            minValue={0}
            step={k.id === 'credits' ? 0.5 : 1}
            formatOptions={k.id === 'credits' ? { maximumFractionDigits: 2 } : { maximumFractionDigits: 0 }}
          >
            <Label>{k.label}</Label>
            <NumberField.Group>
              <NumberField.DecrementButton />
              <NumberField.Input placeholder="不限" />
              <NumberField.IncrementButton />
            </NumberField.Group>
          </NumberField>
        ))}
      </div>
    </div>
  )
}

/** 模型白名单：从目录选或手填（支持 * 通配），下面列出放行了目录里的哪些模型 */
function ModelsField({ value, onChange }: { value: string[]; onChange: (v: string[]) => void }) {
  const models = useModels()
  const catalog = useMemo(() => bareModelIds((models.data?.models || []).map((m) => m.id)), [models.data])
  const [text, setText] = useState('')
  const [hint, setHint] = useState('')
  const add = (raw: string) => {
    const next = addModelPattern(value, raw)
    if (typeof next === 'string') {
      setHint(next)
      return
    }
    setHint('')
    setText('')
    onChange(next)
  }
  const allowed = allowedModels(value, catalog)
  return (
    <div className="flex flex-col gap-2">
      <span className="text-sm font-medium">模型白名单</span>
      <p className="-mt-1 text-xs text-muted">
        按经模型路由改写后、实际发给上游的模型名匹配，不带 cn: / global: 前缀，只认 * 通配（如 glm-*）。不加 = 不限。
        客户端发 claude-sonnet 这类名字、被路由改成 glm-5.2 时，白名单里要有 glm-5.2。
      </p>
      {value.length > 0 && (
        <TagGroup aria-label="已加的模型" onRemove={(keys) => onChange(value.filter((m) => !keys.has(m)))}>
          <TagGroup.List items={value.map((m) => ({ id: m }))}>
            {(m) => <Tag id={m.id} textValue={m.id} className="font-mono">{m.id}</Tag>}
          </TagGroup.List>
        </TagGroup>
      )}
      <div className="flex flex-wrap items-end gap-2">
        <ComboBox
          aria-label="添加模型"
          allowsCustomValue
          inputValue={text}
          onInputChange={(v) => {
            setText(v)
            setHint('')
          }}
          value={null}
          onChange={(k) => { if (k != null) add(String(k)) }}
          className="min-w-56 flex-1"
        >
          <ComboBox.InputGroup>
            <Input placeholder="从目录选，或手填 glm-*" />
            <ComboBox.Trigger />
          </ComboBox.InputGroup>
          <ComboBox.Popover>
            <ListBox>
              {catalog.map((id) => <ListBox.Item key={id} id={id} textValue={id}>{id}<ListBox.ItemIndicator /></ListBox.Item>)}
            </ListBox>
          </ComboBox.Popover>
        </ComboBox>
        <Button size="sm" variant="secondary" isDisabled={!text.trim()} onPress={() => add(text)} className="mb-0.5">添加</Button>
      </div>
      {hint && <span className="text-xs text-danger">{hint}</span>}
      {value.length > 0 && catalog.length > 0 && (
        <p className={`text-xs ${allowed.length ? 'text-muted' : 'text-warning'}`}>
          {allowed.length
            ? `放行目录里的 ${allowed.length} 个模型：${allowed.slice(0, 12).join('、')}${allowed.length > 12 ? ' 等' : ''}`
            : '目录里没有模型能匹配这份白名单，用这个密钥的对话请求都会被拒。'}
        </p>
      )}
    </div>
  )
}

/** 建好后（或点「接入方法」）显示：密钥原文与各客户端的填法 */
export function KeyUsageGuide({ k: open, onClose }: { k: ApiKey | null; onClose: () => void }) {
  const k = useLast(open)
  const origin = location.origin
  const copy = (text: string, what: string) =>
    void copyText(text).then(() => toast.success('已复制' + what), (e: unknown) => toastError(e, '复制失败：'))
  const items = k ? [
    { title: '密钥', text: k.secret, what: '密钥' },
    { title: 'OpenAI 兼容客户端、Codex 的 Base URL', text: origin + '/v1', what: 'Base URL' },
    { title: 'Claude Code', text: `export ANTHROPIC_BASE_URL=${origin}\nexport ANTHROPIC_AUTH_TOKEN=${k.secret}`, what: 'Claude Code 配置' },
    { title: 'Codex 联网搜索（MCP，写进 ~/.codex/config.toml）', text: `[mcp_servers.wb2api]\nurl = "${origin}/mcp"\nbearer_token_env_var = "WB2API_KEY"`, what: 'Codex 联网搜索配置' },
  ] : []
  return (
    <Modal.Backdrop isOpen={open != null} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="md" scroll="inside" placement="auto">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>「{k?.name}」的接入方法</Modal.Heading>
            <p className="mt-1.5 text-sm text-muted">把密钥发给使用者。地址按你现在打开面板的地址生成，经反向代理访问时以对外地址为准。</p>
          </Modal.Header>
          <Modal.Body className="flex flex-col gap-3">
            {items.map((it) => (
              <div key={it.title} className="flex flex-col gap-1">
                <span className="text-sm text-muted">{it.title}</span>
                <div className="flex items-start gap-1">
                  <Surface<'pre'> variant="secondary" render={(props) => <pre {...props} />} className="min-w-0 flex-1 rounded-lg px-3 py-2 font-mono text-sm break-all whitespace-pre-wrap select-all">{it.text}</Surface>
                  <Button isIconOnly size="sm" variant="ghost" aria-label={'复制' + it.what} onPress={() => copy(it.text, it.what)}><Copy /></Button>
                </div>
              </div>
            ))}
            <p className="text-xs text-muted">
              Claude Code 的 Base URL 不带 /v1，其余客户端带 /v1。Codex 把 Base URL 写进 config.toml 的 base_url，env_key 指向的环境变量填密钥；联网搜索那段的 bearer_token_env_var 用同一个变量。Claude Code 自带的 WebSearch 不用配，直接可用。
            </p>
          </Modal.Body>
          <Modal.Footer>
            <Button variant="primary" onPress={onClose}>好了</Button>
          </Modal.Footer>
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}
