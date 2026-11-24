// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { useState } from 'react'
import { ArrowUpRightFromSquare, Copy } from '@gravity-ui/icons'
import { Button, Description, Input, Label, ListBox, Select, Tabs, TextArea, TextField, toast, Typography } from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { post } from '../lib/api'
import { copyText } from '../lib/clipboard'
import { toastError } from '../lib/hooks'
import { qk, useProxies } from '../lib/queries'
import { targetName } from '../lib/targets'
import { BusyButton } from './Feedback'

interface Login { state: string; url: string; target: string; pending: boolean; account_id?: string; local_callback?: boolean }

/** 顶栏添加账号弹窗中的授权、凭证导入表单；切换目标时由父组件重置。 */
export function ProviderAccountForm({ target }: { target: string }) {
  const qc = useQueryClient()
  const proxyView = useProxies().data?.view
  const proxies = proxyView?.proxies ?? []
  const requireBound = !!proxyView?.settings.require_bound
  const [credential, setCredential] = useState('')
  const [name, setName] = useState('')
  const [proxyID, setProxyID] = useState('')
  const [login, setLogin] = useState<Login | null>(null)
  const [callback, setCallback] = useState('')
  const [busy, setBusy] = useState(false)
  const invalidateMutated = () => {
    void qc.invalidateQueries({ queryKey: qk.providers })
    void qc.invalidateQueries({ queryKey: qk.models })
    void qc.invalidateQueries({ queryKey: qk.groups })
    void qc.invalidateQueries({ queryKey: qk.proxies })
  }
  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true)
    try {
      await fn()
    } catch (e) { toastError(e) } finally { setBusy(false) }
  }
  const importAccount = () => run(async () => {
    const value: unknown = target.startsWith('qoder:') && !credential.trim().startsWith('[') && !credential.trim().startsWith('{') ? { pat: credential.trim() } : JSON.parse(credential)
    const items = Array.isArray(value) ? value : [value]
    if (!items.length) throw new Error('导入文件没有账号')
    let imported = 0
    const errors: string[] = []
    for (const [index, item] of items.entries()) {
      try {
        if (item == null || typeof item !== 'object' || Array.isArray(item)) throw new Error('必须是凭证或导出记录对象')
        const record = item as Record<string, unknown>
        await post('providers/import', record.credential ? record : { target, name, proxy_id: proxyID, credential: record })
        imported++
      } catch (e) { errors.push(`第 ${index + 1} 项：${e instanceof Error ? e.message : String(e)}`) }
    }
    if (imported > 0) invalidateMutated()
    if (errors.length) toast.warning(`导入 ${imported} 个通道成功，${errors.length} 项失败`, { description: errors.join('；'), timeout: 8000 })
    else { setCredential(''); toast.success(`已导入 ${imported} 个通道，可在「账号池」查看`) }
  })
  const complete = () => run(async () => {
    if (!login) return
    const result = await post<Login>('providers/login/complete', { state: login.state, callback })
    if (result.pending) {
      toast.info('授权尚未完成，请在浏览器完成后再检查')
    } else {
      invalidateMutated()
      setLogin(null)
      setCallback('')
      toast.success('授权成功，账号已保存')
    }
  })
  const defaultProxyLabel = requireBound ? '默认线路（开着「只许走号的代理」，须选一个代理）' : '默认线路'
  const proxyPicker = (
    <Select value={proxyID || 'default'} onChange={(v) => { if (v != null) setProxyID(v === 'default' ? '' : String(v)) }}>
      <Label>账号出站代理（登录后保留）</Label>
      <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
      <Select.Popover>
        <ListBox>
          <ListBox.Item id="default" textValue={defaultProxyLabel} isDisabled={requireBound}>
            {defaultProxyLabel}<ListBox.ItemIndicator />
          </ListBox.Item>
          {proxies.map((p) => (
            <ListBox.Item key={p.id} id={p.id} textValue={p.name}>{p.name}<ListBox.ItemIndicator /></ListBox.Item>
          ))}
        </ListBox>
      </Select.Popover>
      {requireBound && !proxyID && (
        <Description className="text-warning">
          {proxies.length ? '当前开启了「只许走号的代理」，请选择一个出站代理。' : '当前开启了「只许走号的代理」，且代理池暂无代理，请先到代理页添加。'}
        </Description>
      )}
    </Select>
  )
  return (
    <Tabs>
      <Tabs.ListContainer><Tabs.List aria-label="添加方式">
        <Tabs.Tab id="login">浏览器登录<Tabs.Indicator /></Tabs.Tab>
        <Tabs.Tab id="import">导入凭证<Tabs.Indicator /></Tabs.Tab>
      </Tabs.List></Tabs.ListContainer>
      <Tabs.Panel id="login" className="pt-4">
        <div className="flex flex-col gap-4">
          {proxyPicker}
          <div className="flex flex-wrap gap-2">
            <BusyButton busy={busy} isDisabled={requireBound && !proxyID} onPress={() => void run(async () => { setLogin(await post<Login>('providers/login', { target, proxy_id: proxyID })); setCallback('') })}>网页授权登录</BusyButton>
          </div>
          {login && <div className="flex flex-col gap-3 rounded-lg border border-separator p-3">
            <p className="text-sm">在浏览器打开 {targetName(login.target)} 官方授权页并完成登录：</p>
            <Typography.Code className="select-all break-all rounded-xl bg-surface-secondary px-3 py-2.5 text-xs text-accent">{login.url}</Typography.Code>
            <div className="flex flex-wrap gap-2">
              <Button variant="primary" onPress={() => window.open(login.url, '_blank', 'noopener')}>
                <ArrowUpRightFromSquare />打开官方授权页
              </Button>
              <Button variant="secondary" onPress={() => void copyText(login.url).then(() => toast.success('链接已复制'), () => toast.danger('复制失败，请手动选择复制'))}>
                <Copy />复制链接
              </Button>
            </div>
            {!login.target.startsWith('qoder:') && <TextField value={callback} onChange={setCallback}><Label>完成授权后的回调地址</Label><TextArea rows={3} placeholder="复制浏览器地址栏中的完整 127.0.0.1:1024/authorize 地址" /><p className="text-xs text-muted">直接在本机运行可自动接收回调。容器或远程部署时，若回调页面无法打开，复制地址栏的完整地址粘贴到这里，再点击检查。</p></TextField>}
            <BusyButton busy={busy} isDisabled={!login.target.startsWith('qoder:') && !callback.trim() && !login.local_callback} onPress={() => void complete()}>检查并保存授权</BusyButton>
          </div>}
        </div>
      </Tabs.Panel>
      <Tabs.Panel id="import" className="pt-4">
        <div className="flex flex-col gap-4">
          <TextField value={name} onChange={setName}><Label>备注名称（导入时使用）</Label><Input /></TextField>
          {proxyPicker}
          <TextField value={credential} onChange={setCredential}><Label>{target.startsWith('qoder:') ? 'Qoder PAT 或完整凭证 JSON' : 'Trae 完整凭证 JSON'}</Label><TextArea rows={4} placeholder={target.startsWith('qoder:') ? '粘贴 PAT' : '{"access_token":"…","refresh_token":"…","uid":"…","expires_at":…,"device_id":"…","machine_id":"…","device_key":"…"}'} /></TextField>
          <BusyButton busy={busy} isDisabled={!credential.trim()} onPress={() => void importAccount()}>导入账号</BusyButton>
          <p className="text-xs text-muted">支持粘贴本面板导出的 JSON 数组，按文件中的产品与地区逐项导入；失败项会单独报告。相同地区和 UID 合并为同一账号，已有设置保留。Trae 两个通道分别添加，TraeWork 首期仅提供文本对话。</p>
        </div>
      </Tabs.Panel>
    </Tabs>
  )
}
