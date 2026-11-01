// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 绑定代理：账号池多选的号（或详情里的一个号）绑到池（池内按绑号最少分）、单独指定一个代理，或者解绑。
// 绑池时已经在这个池里有代理的号不动；单独指定也守池的上限，绑满的代理不让选。规则见 docs/proxy.md。
import { useState } from 'react'
import {
  Alert, Button, Description, Header, Label, ListBox, Modal, Radio, RadioGroup, Select, Separator, toast,
} from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { post } from '../../lib/api'
import { toastError, useLast } from '../../lib/hooks'
import { bindSummary, defaultForLabels, loadText, poolSummary, proxyStatus } from '../../lib/proxies'
import { qk, useAccountNames, useProxies } from '../../lib/queries'
import type { ProxiesView, ProxyBindResult } from '../../lib/types'
import { BusyButton, Empty, Loaded } from '../../components/Feedback'

type Mode = 'pool' | 'proxy' | 'unbind'

export function BindProxyModal({ uids, onClose }: { uids: string[] | null; onClose: () => void }) {
  const names = useAccountNames()
  const shown = useLast(uids) ?? []
  const title = shown.length === 1 ? `给「${names.name(shown[0])}」绑定代理` : `给 ${shown.length} 个号绑定代理`
  return (
    <Modal.Backdrop isOpen={uids != null} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="md" placement="auto">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>{title}</Modal.Heading>
          </Modal.Header>
          {/* 每次打开都从头选：关闭时卸载内容 */}
          {uids != null && <BindBody uids={uids} onClose={onClose} />}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

function BindBody({ uids, onClose }: { uids: string[]; onClose: () => void }) {
  const query = useProxies()
  const view = query.data?.view
  if (view && view.pools.length > 0) return <BindForm view={view} uids={uids} onClose={onClose} />
  return (
    <Modal.Body>
      <Loaded query={query} rows={3}>
        {() => <Empty title="还没有代理池">先到「代理」页建一个池、导入代理，再回来绑定。</Empty>}
      </Loaded>
    </Modal.Body>
  )
}

function BindForm({ view, uids, onClose }: { view: ProxiesView; uids: string[]; onClose: () => void }) {
  const qc = useQueryClient()
  const names = useAccountNames()
  const [mode, setMode] = useState<Mode>('pool')
  const [pool, setPool] = useState(view.pools[0].id)
  const [proxy, setProxy] = useState('')
  const [busy, setBusy] = useState(false)
  const [errors, setErrors] = useState<string[]>([])
  const poolOf = (id: string) => view.pools.find((p) => p.id === id)

  const submit = async () => {
    setBusy(true)
    setErrors([])
    try {
      const body = mode === 'pool' ? { uids, pool } : mode === 'proxy' ? { uids, proxy } : { uids, unbind: true }
      const { results } = await post<{ results: ProxyBindResult[] }>('proxies/bind', body)
      void qc.invalidateQueries({ queryKey: qk.overview })
      void qc.invalidateQueries({ queryKey: qk.proxies })
      const s = bindSummary(results, (uid) => names.name(uid))
      const parts = [s.bound && `${s.bound} 个绑上了代理`, s.waiting && `${s.waiting} 个在等代理（池里没有空位）`, s.unbound && `${s.unbound} 个已解绑`].filter(Boolean)
      if (parts.length) toast.success(parts.join('，'))
      if (s.errors.length) setErrors(s.errors)
      else onClose()
    } catch (e) {
      toastError(e, '绑定失败：')
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Modal.Body className="flex flex-col gap-4">
      <RadioGroup value={mode} onChange={(v) => setMode(v as Mode)}>
        <Label>怎么绑</Label>
        <Radio value="pool">
          <Radio.Content><Radio.Control><Radio.Indicator /></Radio.Control>绑到池</Radio.Content>
          <Description>池里按绑号最少分代理，分到后固定；已经在这个池里有代理的号不动。池里没有空位的号先等着，加了代理自动补上。</Description>
        </Radio>
        <Radio value="proxy">
          <Radio.Content><Radio.Control><Radio.Indicator /></Radio.Control>单独指定一个代理</Radio.Content>
          <Description>盖过池的分配，池内重新均分时也不动它；同样守池的上限。</Description>
        </Radio>
        <Radio value="unbind">
          <Radio.Content><Radio.Control><Radio.Indicator /></Radio.Control>解绑</Radio.Content>
          <Description>
            {view.settings.require_bound ? '开着「只许走号的代理」：解绑后这些号不接请求、不跑定时任务。' : '解绑后这些号走服务器自己的网络（或环境变量里的全局代理）。'}
          </Description>
        </Radio>
      </RadioGroup>

      {mode === 'pool' && (
        <Select aria-label="代理池" value={pool} onChange={(k) => k != null && setPool(String(k))}>
          <Label>代理池</Label>
          <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
          <Select.Popover>
            <ListBox>
              {view.pools.map((p) => (
                <ListBox.Item key={p.id} id={p.id} textValue={p.name}>
                  <div className="flex flex-col">
                    <Label>{[p.name, ...defaultForLabels(p)].join(' · ')}</Label>
                    <Description>{poolSummary(p)}</Description>
                  </div>
                  <ListBox.ItemIndicator />
                </ListBox.Item>
              ))}
            </ListBox>
          </Select.Popover>
        </Select>
      )}

      {mode === 'proxy' && (
        view.proxies.length === 0 ? <p className="text-sm text-muted">池里还没有代理。</p> : (
          <Select aria-label="代理" value={proxy || null} onChange={(k) => k != null && setProxy(String(k))} placeholder="选一个代理">
            <Label>代理</Label>
            <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
            <Select.Popover>
              <ListBox>
                {view.pools.filter((pl) => view.proxies.some((p) => p.pool === pl.id)).map((pl, i) => (
                  <ListBox.Section key={pl.id}>
                    {i > 0 && <Separator />}
                    <Header>{pl.name}</Header>
                    {view.proxies.filter((p) => p.pool === pl.id).map((p) => {
                      // 绑满的代理选不了（这些号里已经绑在它上面的不受影响，但多选时说不清，干脆不让选）
                      const full = !!p.full && !uids.every((uid) => p.accounts.includes(uid))
                      return (
                        <ListBox.Item key={p.id} id={p.id} textValue={p.name} isDisabled={full}>
                          <div className="flex flex-col">
                            <Label>{p.name}</Label>
                            <Description>{[proxyStatus(p).label, loadText(p, poolOf(p.pool)), full && '绑满了', p.health.exit?.ip].filter(Boolean).join(' · ')}</Description>
                          </div>
                          <ListBox.ItemIndicator />
                        </ListBox.Item>
                      )
                    })}
                  </ListBox.Section>
                ))}
              </ListBox>
            </Select.Popover>
          </Select>
        )
      )}

      {errors.length > 0 && (
        <Alert status="warning">
          <Alert.Indicator />
          <Alert.Content>
            <Alert.Title>{errors.length} 个号没绑成</Alert.Title>
            <Alert.Description>
              <ul className="mt-1 list-disc space-y-0.5 pl-4 text-xs">
                {errors.map((e, i) => <li key={i} className="break-all">{e}</li>)}
              </ul>
            </Alert.Description>
          </Alert.Content>
        </Alert>
      )}

      </Modal.Body>
      <Modal.Footer>
        <Button variant="tertiary" onPress={onClose}>{errors.length ? '关闭' : '取消'}</Button>
        <BusyButton busy={busy} isDisabled={mode === 'proxy' && !proxy} onPress={() => void submit()}>
          {mode === 'unbind' ? '解绑' : '绑定'}
        </BusyButton>
      </Modal.Footer>
    </>
  )
}
