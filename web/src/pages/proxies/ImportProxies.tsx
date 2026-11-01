// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 批量导入代理：粘贴或上传 txt，一行一个（认常见写法，行尾可以跟名字），选导入到哪个池、没写协议的行按
// http 还是 socks5、这批的到期时间；先预览（认不出的行、和已有的重复的行标出来、不导入），再确认导入。
// 导入后网关马上测连通和出口 IP，测不通的照样导入、标「不可用」，代理表上可以一键删掉。
import { useRef, useState } from 'react'
import { Alert, Button, Description, Label, ListBox, Modal, Radio, RadioGroup, Select, TextArea, TextField, toast } from '@heroui/react'
import { post } from '../../lib/api'
import { toastError } from '../../lib/hooks'
import { defaultForLabels, lineCounts, lineResult } from '../../lib/proxies'
import type { ProxyImportResp, ProxyLine, ProxyPool } from '../../lib/types'
import { ExpiryField } from '../../components/ExpiryField'
import { BusyButton } from '../../components/Feedback'

const PLACEHOLDER = `一行一个，行尾可以空一格写名字，# 开头的行跳过。认这几种写法：
socks5://用户名:密码@203.0.113.10:1080 香港1
203.0.113.11:8080
203.0.113.12:8080:用户名:密码
用户名:密码@203.0.113.13:8080
用户名:密码:203.0.113.14:8080`

export function ImportProxies({ open, pools, onClose, onDone }: { open: boolean; pools: ProxyPool[]; onClose: () => void; onDone: () => void }) {
  return (
    <Modal.Backdrop isOpen={open} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="lg" scroll="inside" placement="auto">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>导入代理</Modal.Heading>
          </Modal.Header>
          {/* 每次打开都从头填：关闭时卸载内容 */}
          {open && <ImportBody pools={pools} onClose={onClose} onDone={onDone} />}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

function ImportBody({ pools, onClose, onDone }: { pools: ProxyPool[]; onClose: () => void; onDone: () => void }) {
  const file = useRef<HTMLInputElement>(null)
  const [text, setText] = useState('')
  const [scheme, setScheme] = useState('http')
  const [pool, setPool] = useState(pools[0]?.id ?? '')
  const [expires, setExpires] = useState<number | null>(null)
  const [lines, setLines] = useState<ProxyLine[] | null>(null)
  const [busy, setBusy] = useState(false)
  const counts = lines ? lineCounts(lines) : null

  // 改了文本或协议，之前的预览就不作数了
  const edit = (t: string) => {
    setText(t)
    setLines(null)
  }
  const onFile = async (f: File | undefined) => {
    if (!f) return
    try {
      const t = await f.text()
      edit(text.trim() ? text.replace(/\n*$/, '\n') + t : t)
    } catch (e) {
      toastError(e, '读不了文件：')
    } finally {
      if (file.current) file.current.value = ''
    }
  }
  const preview = async () => {
    setBusy(true)
    try {
      const r = await post<{ lines: ProxyLine[] | null }>('proxies/preview', { text, scheme })
      setLines(r.lines || [])
    } catch (e) {
      toastError(e, '预览失败：')
    } finally {
      setBusy(false)
    }
  }
  const doImport = async () => {
    setBusy(true)
    try {
      const r = await post<ProxyImportResp>('proxies/import', { text, scheme, pool, expires_at: expires == null ? null : new Date(expires).toISOString() })
      const n = r.added?.length ?? 0
      toast.success(`导入了 ${n} 个代理`, { description: '正在测连通和出口 IP；测不通的照样导入、标「不可用」，可以在代理表上一键删掉。', timeout: 6000 })
      onDone()
      onClose()
    } catch (e) {
      toastError(e, '导入失败：')
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Modal.Body className="flex flex-col gap-4">
        <TextField value={text} onChange={edit}>
          <Label>代理</Label>
          <TextArea rows={7} placeholder={PLACEHOLDER} className="font-mono text-xs" spellCheck={false} />
          <Description>也可以上传 txt 文件（追加到上面的内容后面）。网关导出的文件能原样导回来。</Description>
        </TextField>
        <input ref={file} type="file" accept=".txt,text/plain" hidden onChange={(e) => void onFile(e.target.files?.[0])} />
        <div className="-mt-2">
          <Button size="sm" variant="secondary" onPress={() => file.current?.click()}>上传 txt</Button>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Select value={pool} onChange={(k) => k != null && setPool(String(k))}>
            <Label>导入到哪个池</Label>
            <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
            <Select.Popover>
              <ListBox>
                {pools.map((p) => (
                  <ListBox.Item key={p.id} id={p.id} textValue={p.name}>
                    {[p.name, ...defaultForLabels(p)].join(' · ')}
                    <ListBox.ItemIndicator />
                  </ListBox.Item>
                ))}
              </ListBox>
            </Select.Popover>
            <Description>池里有号在等代理的，导入后马上补上。</Description>
          </Select>
          {/* HeroUI 的横排把标题、选项、说明排在同一行里折行，半栏宽时选项会错开；改成标题单独一行，选项那行和左边的下拉框一样高 */}
          <RadioGroup value={scheme} onChange={(v) => { setScheme(v); setLines(null) }} orientation="horizontal" className="flex-col flex-nowrap gap-1">
            <Label>没写协议的行按</Label>
            <div className="flex min-h-9 flex-wrap items-center gap-x-6 gap-y-2">
              <Radio value="http"><Radio.Content><Radio.Control><Radio.Indicator /></Radio.Control>HTTP</Radio.Content></Radio>
              <Radio value="socks5"><Radio.Content><Radio.Control><Radio.Indicator /></Radio.Control>SOCKS5</Radio.Content></Radio>
            </div>
            <Description>写了 http://、socks5:// 的行照写的来。</Description>
          </RadioGroup>
        </div>

        <ExpiryField
          value={expires}
          onChange={setExpires}
          description="这一批一起设，之后可以逐个改。到期前几天提醒，到期后按不可用处理；不设就不管到期。"
          pastWarning="这个时间已经过了，导入后这批代理马上按到期处理。"
          presets={[30, 90, 365]}
        />

        {lines && counts && (
          <div className="flex flex-col gap-2">
            <Alert status={counts.ok ? (counts.error || counts.duplicate ? 'warning' : 'success') : 'danger'}>
              <Alert.Indicator />
              <Alert.Content>
                <Alert.Title>
                  {lines.length === 0 ? '没有认出任何一行' : `能导入 ${counts.ok} 个`}
                  {counts.error > 0 && `，${counts.error} 行认不出`}
                  {counts.duplicate > 0 && `，${counts.duplicate} 行重复`}
                </Alert.Title>
                {(counts.error > 0 || counts.duplicate > 0) && <Alert.Description>认不出和重复的行不导入。</Alert.Description>}
              </Alert.Content>
            </Alert>
            {lines.length > 0 && (
              <div className="max-h-64 divide-y divide-separator overflow-y-auto overscroll-contain rounded-xl bg-surface-secondary px-3">
                {lines.map((l) => {
                  const r = lineResult(l)
                  return (
                    <div key={l.no} className="flex flex-col gap-0.5 py-1.5 text-xs sm:flex-row sm:items-baseline sm:gap-3">
                      <span className="w-12 shrink-0 text-muted tabular-nums">第 {l.no} 行</span>
                      <span className="min-w-0 flex-1 break-all font-mono">{l.url || l.text}</span>
                      {r.ok && l.name && <span className="shrink-0 text-muted">{l.name}</span>}
                      <span className={`shrink-0 ${r.ok ? 'text-success' : l.error ? 'text-danger' : 'text-warning'}`}>{r.text}</span>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        )}
      </Modal.Body>
      <Modal.Footer>
        <Button variant="tertiary" onPress={onClose}>取消</Button>
        {counts && counts.ok > 0 ? (
          <BusyButton busy={busy} isDisabled={!pool} onPress={() => void doImport()}>导入 {counts.ok} 个</BusyButton>
        ) : (
          <BusyButton busy={busy} variant={lines ? 'secondary' : 'primary'} isDisabled={!text.trim()} onPress={() => void preview()}>{lines ? '重新预览' : '预览'}</BusyButton>
        )}
      </Modal.Footer>
    </>
  )
}
