// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 单号测活：给这个号发一条「hi」，看它现在能不能用。默认挑这个号能接的最便宜的模型，可以临时换；
// 测完显示结果和上游回了什么。关掉弹窗网关也会测完，结果照样记进卡片上的「上次测活」。
import { useState, type ReactNode } from 'react'
import { Alert, Button, Description, Label, ListBox, Modal, Select, Surface } from '@heroui/react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { accountName, livenessModels } from '../../lib/accounts'
import { acct, post } from '../../lib/api'
import { formatLatency } from '../../lib/format'
import { toastError } from '../../lib/hooks'
import { qk, useModels } from '../../lib/queries'
import type { Account, LivenessResp } from '../../lib/types'
import { BusyButton } from '../../components/Feedback'

/** 下拉框里「自动挑」那一项的 id（真实模型名不会是这个） */
const AUTO = '__auto__'

export function LivenessModal({ account, onClose }: { account: Account | null; onClose: () => void }) {
  return (
    <Modal.Backdrop isOpen={!!account} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container scroll="inside">
        <Modal.Dialog className="sm:max-w-lg">
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>测活 <span className="text-sm font-normal text-muted">{account ? accountName(account) : ''}</span></Modal.Heading>
            <p className="mt-1.5 text-sm text-muted">
              给这个号发一条「hi」，走和真实请求同一条路径。测通会解除冷却、熔断和网关自动加的禁用（人工禁用、暂停选号不动）；
              失败照真实请求处理。会记进请求记录（密钥一列写「测活」），可能扣一点积分。
            </p>
          </Modal.Header>
          {account && <Body key={account.uid} a={account} onClose={onClose} />}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

function Body({ a, onClose }: { a: Account; onClose: () => void }) {
  const qc = useQueryClient()
  const models = useModels()
  const [model, setModel] = useState(AUTO)
  const test = useMutation({
    mutationFn: () => post<LivenessResp>(acct(a.uid, 'liveness'), model === AUTO ? {} : { model }),
    onError: (e) => toastError(e),
    onSettled: () => void qc.invalidateQueries({ queryKey: qk.overview }),
  })
  const options = livenessModels(models.data?.models || [], a)
  const hint = models.isPending ? '正在拉模型目录…' : models.isError ? '模型目录拉不到，只能自动挑' : '自动挑：按积分倍率选这个号目录里最便宜的模型'

  return (
    <>
      <Modal.Body className="flex flex-col gap-4">
        <Select value={model} onChange={(k) => k != null && setModel(String(k))} isDisabled={test.isPending} className="w-full">
          <Label>模型</Label>
          <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
          <Description>{hint}</Description>
          <Select.Popover>
            <ListBox>
              <ListBox.Item id={AUTO} textValue="自动（最便宜）">自动（最便宜）<ListBox.ItemIndicator /></ListBox.Item>
              {options.map((m) => <ListBox.Item key={m} id={m} textValue={m}>{m}<ListBox.ItemIndicator /></ListBox.Item>)}
            </ListBox>
          </Select.Popover>
        </Select>
        {test.data && <Result data={test.data} />}
      </Modal.Body>
      <Modal.Footer>
        <Button variant="tertiary" onPress={onClose}>关闭</Button>
        <BusyButton variant="primary" busy={test.isPending} onPress={() => test.mutate()}>
          {test.isPending ? '测活中…' : test.data ? '再测一次' : '开始测活'}
        </BusyButton>
      </Modal.Footer>
    </>
  )
}

function Line({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex gap-3 text-sm">
      <span className="w-14 shrink-0 text-muted">{label}</span>
      <span className="min-w-0 break-all tabular-nums">{children}</span>
    </div>
  )
}

function Result({ data: { result: r, reply } }: { data: LivenessResp }) {
  return (
    <div className="flex flex-col gap-3">
      <Alert status={r.ok ? 'success' : 'danger'}>
        <Alert.Indicator />
        <Alert.Content className="min-w-0">
          <Alert.Title>{r.ok ? (r.recovered ? '通了，已解除冷却 / 熔断 / 自动禁用' : '通了') : '不通'}</Alert.Title>
          {!r.ok && r.error && <Alert.Description className="break-all">{r.error}</Alert.Description>}
        </Alert.Content>
      </Alert>
      <div className="flex flex-col gap-1.5">
        {r.model && <Line label="模型">{r.model}</Line>}
        {!!r.status && <Line label="状态码">{r.status}</Line>}
        <Line label="耗时">首字 {formatLatency(r.ttfb_ms)} · 共 {formatLatency(r.duration_ms)}</Line>
        {!!r.credit && <Line label="扣积分">{r.credit}</Line>}
      </div>
      {reply && (
        <div className="flex flex-col gap-1">
          <span className="text-sm text-muted">上游回复</span>
          <Surface variant="secondary" className="max-h-48 overflow-y-auto whitespace-pre-wrap break-all rounded-xl px-3 py-2 text-sm">{reply}</Surface>
        </div>
      )}
    </div>
  )
}
