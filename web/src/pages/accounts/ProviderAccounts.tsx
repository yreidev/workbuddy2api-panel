// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { useMemo, useState } from 'react'
import { Button, Checkbox, CheckboxGroup, Input, Label, Modal, Table, Tag, TagGroup, TextField } from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { post } from '../../lib/api'
import { toastError, useNow } from '../../lib/hooks'
import { providerCardUsage } from '../../lib/accountPool'
import { providerChargeText } from '../../lib/billing'
import { USAGE_KEY_ADMIN, USAGE_KEY_NONE } from '../../lib/keys'
import { qk, useAccountNames, useGroups, useKeys, useProxies } from '../../lib/queries'
import type { ProviderAccount, ProviderUsage } from '../../lib/types'
import { BusyButton, Confirm } from '../../components/Feedback'
import { Panel } from '../../components/Panel'
import { TargetSelect } from '../../components/TargetSelect'
import { ProviderAccountCard } from './ProviderAccountCard'
import { ProviderLivenessModal } from './ProviderLivenessModal'
import { BatchMenu, PriorityField } from './BatchMenu'
import { download, stamp } from '../../lib/credentials'
import { ModelPatternInput } from '../../components/ModelPatternInput'
import { ModelComboBox } from '../../components/ModelComboBox'

export function ProviderAccounts({ accounts, usage }: { accounts: ProviderAccount[]; usage: ProviderUsage[] }) {
  const qc = useQueryClient()
  const now = useNow()
  const groups = useGroups().data ?? []
  const proxyAccounts = useProxies().data?.accounts ?? {}
  const usageByAccount = useMemo(() => providerCardUsage(usage), [usage])
  const [busy, setBusy] = useState(false)
  const [exporting, setExporting] = useState(false)
  const [editing, setEditing] = useState<ProviderAccount | null>(null)
  const [removing, setRemoving] = useState<ProviderAccount | null>(null)
  const [testing, setTesting] = useState<ProviderAccount | null>(null)
  const [selection, setSelection] = useState<string[]>([])
  const selected = selection.filter((id) => accounts.some((a) => a.account_id === id))
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: qk.providers })
    void qc.invalidateQueries({ queryKey: qk.groups })
    void qc.invalidateQueries({ queryKey: qk.models })
    void qc.invalidateQueries({ queryKey: qk.proxies })
  }
  const action = async (fn: () => Promise<unknown>) => {
    setBusy(true)
    try { await fn(); refresh() } catch (e) { toastError(e) } finally { setBusy(false) }
  }
  const exportSelected = async () => {
    setExporting(true)
    try {
      const out = await post<{ accounts: unknown[] }>('providers/export', { ids: selected })
      download({ name: `platform-accounts-${stamp(new Date())}.json`, text: JSON.stringify(out.accounts, null, 2) + '\n' })
    } catch (e) { toastError(e) } finally { setExporting(false) }
  }
  return <>
    {accounts.length > 0 && <Panel title="Trae / Qoder 账号" actions={<>
      <Button size="sm" variant="ghost" onPress={() => setSelection(selected.length === accounts.length ? [] : accounts.map((a) => a.account_id))}>{selected.length === accounts.length ? '取消全选' : '全选'}</Button>
      {selected.length > 0 && <><span className="text-sm text-muted">已选 {selected.length} 个</span><BatchMenu provider selected={selected} nameOf={(id) => accounts.find((a) => a.account_id === id)?.name ?? id} onRemoved={() => setSelection([])} />
      <BusyButton size="sm" variant="secondary" busy={exporting} onPress={() => void exportSelected()}>导出凭证</BusyButton></>}
    </>} contentClassName="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(100%,340px),1fr))]">
      {accounts.map((a) => <ProviderAccountCard key={a.account_id} account={a} now={now} usage={usageByAccount.get(a.account_id)}
        proxy={proxyAccounts[a.account_id]} groups={groups} busy={busy} selected={selected.includes(a.account_id)}
        onSelect={(v) => setSelection(v ? [...selected, a.account_id] : selected.filter((id) => id !== a.account_id))}
        onEdit={() => setEditing(a)} onTest={() => setTesting(a)} onRemove={() => setRemoving(a)}
        onRefreshModels={(target) => void action(() => post(`providers/accounts/${a.account_id}/models`, { target }))} />)}
    </Panel>}
    <AccountEditor account={editing} onClose={() => setEditing(null)} onSaved={refresh} />
    <ProviderLivenessModal account={testing} onClose={() => setTesting(null)} />
    <Confirm open={removing != null} onClose={() => setRemoving(null)} title="移除平台账号" body={`移除「${removing?.name}」的本地凭证和全部产品通道；历史用量保留。`} onConfirm={() => {
      if (!removing) return
      const id = removing.account_id
      void action(async () => {
        await post(`providers/accounts/${id}/delete`)
        qc.removeQueries({ queryKey: qk.providerQuota(id) })
      })
    }} />
  </>
}

export function ProviderUsagePanel({ usage }: { usage: ProviderUsage[] }) {
  const names = useAccountNames()
  const keys = useKeys().data?.keys
  const keyMap = useMemo(() => new Map((keys ?? []).map((k) => [k.id, k.name])), [keys])
  const showKey = usage.some((u) => !!u.key_id)
  const keyLabel = (id?: string) => {
    if (!id || id === USAGE_KEY_NONE) return '未区分'
    if (id === USAGE_KEY_ADMIN) return '管理员'
    if (id === 'liveness') return '测活'
    return keyMap.get(id) || id
  }
  return <Panel title="Trae / Qoder 历史累计" desc="全部历史累计，包含接入时序账本之前的记录，不受时间范围筛选影响。按当前上游、密钥或账号筛选；未知的 token 和费用不补零，也不折算 WorkBuddy 积分。" contentClassName="overflow-x-auto">
    <Table variant="secondary">
      <Table.ScrollContainer>
        <Table.Content aria-label="Trae / Qoder 历史累计" className="min-w-[760px]">
          <Table.Header>
            <Table.Column isRowHeader>上游 / 模型</Table.Column>
            <Table.Column>账号</Table.Column>
            {showKey ? <Table.Column>密钥</Table.Column> : null}
            <Table.Column className="text-right">请求 / 失败</Table.Column>
            <Table.Column className="text-right">输入 / 输出 token</Table.Column>
            <Table.Column className="text-right">费用</Table.Column>
          </Table.Header>
          <Table.Body>
            {usage.map((u) => {
              const id = JSON.stringify([u.target, u.account_id, u.model, u.key_id ?? '', u.unit ?? ''])
              const accName = names.name(u.account_id)
              return <Table.Row key={id} id={id}>
                <Table.Cell className="break-all font-mono text-xs">{u.target}:{u.model}</Table.Cell>
                <Table.Cell>
                  <div className="flex flex-col">
                    <span className="font-medium">{accName}</span>
                    {accName !== u.account_id && <span className="font-mono text-xs text-muted">{u.account_id}</span>}
                  </div>
                </Table.Cell>
                {showKey ? <Table.Cell className="whitespace-nowrap">{keyLabel(u.key_id)}</Table.Cell> : null}
                <Table.Cell className="text-right tabular-nums">{u.requests} / {u.failed}</Table.Cell>
                <Table.Cell className="text-right tabular-nums">{u.tokens_known ? `${u.prompt_tokens} / ${u.completion_tokens}（${u.tokens_known}/${u.requests} 次已知）` : '未知'}</Table.Cell>
                <Table.Cell className="text-right tabular-nums">{providerChargeText(u)}</Table.Cell>
              </Table.Row>
            })}
          </Table.Body>
        </Table.Content>
      </Table.ScrollContainer>
    </Table>
  </Panel>
}

function AccountEditor({ account, onClose, onSaved }: { account: ProviderAccount | null; onClose: () => void; onSaved: () => void }) {
  return <Modal.Backdrop isOpen={account != null} onOpenChange={(v) => { if (!v) onClose() }}><Modal.Container><Modal.Dialog><Modal.CloseTrigger /><Modal.Header><Modal.Heading>平台账号设置</Modal.Heading></Modal.Header>{account && <AccountForm key={account.account_id} account={account} onClose={onClose} onSaved={onSaved} />}</Modal.Dialog></Modal.Container></Modal.Backdrop>
}
function AccountForm({ account, onClose, onSaved }: { account: ProviderAccount; onClose: () => void; onSaved: () => void }) {
  const groups = useGroups().data ?? []
  const proxies = useProxies().data?.view.proxies ?? []
  const modelIds = useMemo(() => [...new Set(account.models.map((m) => m.id))], [account.models])
  const [proxyID, setProxyID] = useState(account.proxy_id || '')
  const [name, setName] = useState(account.name)
  const [selected, setSelected] = useState(account.groups)
  const [priority, setPriority] = useState(account.priority)
  const [disabled, setDisabled] = useState(account.manual_disabled ?? account.disabled)
  const [paused, setPaused] = useState(account.paused ?? false)
  const [blockedModels, setBlockedModels] = useState(account.blocked_models ?? [])
  const [livenessModel, setLivenessModel] = useState(account.liveness_model ?? '')
  const [busy, setBusy] = useState(false)
  const save = async () => { setBusy(true); try { await post(`providers/accounts/${account.account_id}`, { name, groups: selected, priority, disabled, paused, blocked_models: blockedModels, liveness_model: livenessModel.trim(), proxy_id: proxyID }); onSaved(); onClose() } catch (e) { toastError(e) } finally { setBusy(false) } }
  return <><Modal.Body className="flex flex-col gap-4">
    <TextField value={name} onChange={setName}><Label>名称</Label><Input /></TextField>
    <PriorityField value={priority} onChange={setPriority} />
    <TargetSelect label="出站代理" value={proxyID || 'default'} onChange={(v) => setProxyID(v === 'default' ? '' : v)} targets={[{ id: 'default', name: '默认线路' }, ...proxies.map((p) => ({ id: p.id, name: p.name }))]} />
    <Checkbox isSelected={disabled} onChange={setDisabled}><Checkbox.Content><Checkbox.Control><Checkbox.Indicator /></Checkbox.Control>停止派发</Checkbox.Content></Checkbox>
    <Checkbox isSelected={paused} onChange={setPaused}><Checkbox.Content><Checkbox.Control><Checkbox.Indicator /></Checkbox.Control>暂停选号</Checkbox.Content></Checkbox>
    <ModelComboBox label="批量与定时测活模型" ids={modelIds} inputValue={livenessModel} onInputChange={setLivenessModel} placeholder="选择完整模型名；留空不参与批量和定时测活" />
    <p className="text-xs text-muted">保存明确模型后，批量测活使用它；启用全局定时测活时，仅探测临时冷却过的账号。测试可能消耗额度。</p>
    <div className="flex flex-col gap-2"><Label>不接的模型</Label>
      <TagGroup aria-label="账号不接的模型" onRemove={(keys) => setBlockedModels(blockedModels.filter((m) => !keys.has(m)))}><TagGroup.List items={blockedModels.map((id) => ({ id }))}>{(m) => <Tag id={m.id} textValue={m.id}>{m.id}</Tag>}</TagGroup.List></TagGroup>
      <ModelPatternInput value={blockedModels} onChange={setBlockedModels} providerOnly allowedTargets={account.targets} extraIds={modelIds} />
      <p className="text-xs text-muted">按完整产品／地区模型名匹配，支持 * 通配。</p>
    </div>
    <CheckboxGroup value={selected} onChange={setSelected}><Label>账号分组（空 = 不分组）</Label>{groups.map((g) => <Checkbox key={g.id} value={g.id}><Checkbox.Content><Checkbox.Control><Checkbox.Indicator /></Checkbox.Control>{g.name}</Checkbox.Content></Checkbox>)}</CheckboxGroup>
  </Modal.Body><Modal.Footer><Button variant="tertiary" onPress={onClose}>取消</Button><BusyButton busy={busy} onPress={() => void save()}>保存</BusyButton></Modal.Footer></>
}
