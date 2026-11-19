// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { useState } from 'react'
import { Button, Label, ListBox, Modal, Select } from '@heroui/react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { post } from '../../lib/api'
import { toastError } from '../../lib/hooks'
import { qk, useProviderModels } from '../../lib/queries'
import { TARGETS } from '../../lib/targets'
import type { LivenessResp, ProviderAccount } from '../../lib/types'
import { BusyButton, Empty, Loaded } from '../../components/Feedback'
import { TargetSelect } from '../../components/TargetSelect'
import { LivenessResultView } from './LivenessModal'

export function ProviderLivenessModal({ account, onClose }: { account: ProviderAccount | null; onClose: () => void }) {
  return <Modal.Backdrop isOpen={account != null} onOpenChange={(v) => { if (!v) onClose() }}>
    <Modal.Container scroll="inside"><Modal.Dialog className="sm:max-w-lg">
      <Modal.CloseTrigger />
      <Modal.Header>
        <Modal.Heading>测活 <span className="text-sm font-normal text-muted">{account?.name}</span></Modal.Heading>
        <p className="mt-1.5 text-sm text-muted">给这个账号发一条「hi」，固定产品与地区，只试一次。会记录结果及用量，可能消耗平台额度；测通解除临时冷却，保留人工停用。关闭弹窗后本次测试仍会完成。</p>
      </Modal.Header>
      {account && <Body key={account.account_id} account={account} onClose={onClose} />}
    </Modal.Dialog></Modal.Container>
  </Modal.Backdrop>
}

function Body({ account, onClose }: { account: ProviderAccount; onClose: () => void }) {
  const qc = useQueryClient()
  const [target, setTarget] = useState(account.targets[0])
  const [model, setModel] = useState('')
  const models = useProviderModels(account.account_id, target)
  // 官方目录可能按多个能力分组重复返回同名模型，选择器只保留一个完整名称。
  const options = [...new Map((models.data?.models ?? []).map((m) => [`${target}:${m.id}`, m])).entries()]
  const selected = options.some(([id]) => id === model) ? model : options[0]?.[0] ?? ''
  const test = useMutation({
    mutationFn: () => post<LivenessResp>(`providers/accounts/${account.account_id}/liveness`, { model: selected }),
    onError: (e) => toastError(e),
    onSettled: () => { void qc.invalidateQueries({ queryKey: qk.providers }) },
  })
  return <>
    <Modal.Body className="flex flex-col gap-4">
      {account.targets.length > 1 && <div inert={test.isPending}><TargetSelect label="产品与地区" value={target} onChange={(v) => { setTarget(v); setModel(''); test.reset() }} targets={TARGETS.filter((t) => account.targets.includes(t.id))} /></div>}
      <Loaded query={models}>{() => options.length ? <Select value={selected} onChange={(v) => { if (v != null) { setModel(String(v)); test.reset() } }} isDisabled={test.isPending}>
        <Label>模型</Label>
        <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
        <Select.Popover><ListBox>{options.map(([id, m]) => <ListBox.Item key={id} id={id} textValue={id}>{m.name || m.id}<span className="block text-xs text-muted">{id}</span><ListBox.ItemIndicator /></ListBox.Item>)}</ListBox></Select.Popover>
      </Select> : <Empty title="这个产品暂未返回模型目录" action={<BusyButton size="sm" variant="secondary" busy={models.isFetching} onPress={() => void models.refetch()}>刷新目录</BusyButton>} />}</Loaded>
      {test.data && <LivenessResultView data={test.data} />}
    </Modal.Body>
    <Modal.Footer>
      <Button variant="tertiary" onPress={onClose}>关闭</Button>
      <BusyButton busy={test.isPending} isDisabled={!selected || models.isFetching || models.isError} onPress={() => test.mutate()}>{test.isPending ? '测活中…' : test.data ? '再测一次' : '开始测活'}</BusyButton>
    </Modal.Footer>
  </>
}
