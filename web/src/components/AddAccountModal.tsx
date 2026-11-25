// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 添加账号：先选产品与地区，再使用对应的授权或导入表单。
// 建过代理池时两种方式都能选池：登录请求走池里分到的代理，新号绑在它上面；导入的号放进选的池（规则见 docs/proxy.md）。
import { useEffect, useState } from 'react'
import { ArrowUpRightFromSquare, Copy } from '@gravity-ui/icons'
import {
  Alert, Button, Description, Label, ListBox, Modal, Select, Spinner, Tabs, toast, Typography,
} from '@heroui/react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { creditsText } from '../lib/accounts'
import { api, errorMessage, post, upload } from '../lib/api'
import { copyText } from '../lib/clipboard'
import { defaultPoolOf, poolSummary } from '../lib/proxies'
import { qk, useProxies } from '../lib/queries'
import { TARGETS } from '../lib/targets'
import type { ImportResp, LoginPoll, LoginStart } from '../lib/types'
import { BusyButton } from './Feedback'
import { FileDropZone } from './FileDropZone'
import { ProviderAccountForm } from './ProviderAccountForm'
import { TargetSelect } from './TargetSelect'

/** 选池的两个特殊项：默认池（发给网关是空串）、不绑代理（同 internal/proxies 的 NoPool） */
const DEFAULT_POOL = 'default'
const NO_POOL = '-'
const poolParam = (v: string) => (v === DEFAULT_POOL ? '' : v)

/**
 * 登录、导入账号时选代理池：默认池 / 某个池 / 不绑代理。没建过池时不显示。
 * realm 是登录的版本（默认池按它找）；导入时不给，各号按自己的版本放进默认池。
 */
function PoolPicker({ value, onChange, realm, description }: { value: string; onChange: (v: string) => void; realm?: string; description: string }) {
  const query = useProxies()
  const view = query.data?.view
  if (!view || view.pools.length === 0) return null
  const def = realm ? defaultPoolOf(view.pools, realm) : undefined
  const required = view.settings.require_bound
  const defaultLabel = !realm ? '各版本的默认池' : def ? `默认池「${def.name}」` : required ? '默认池（这个版本还没设，要选一个池）' : '默认池（这个版本还没设，不绑代理）'
  return (
    <Select value={value} onChange={(k) => k != null && onChange(String(k))}>
      <Label>代理池</Label>
      <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
      <Select.Popover>
        <ListBox>
          <ListBox.Item id={DEFAULT_POOL} textValue={defaultLabel}>{defaultLabel}<ListBox.ItemIndicator /></ListBox.Item>
          {view.pools.map((p) => (
            <ListBox.Item key={p.id} id={p.id} textValue={p.name}>
              <div className="flex flex-col">
                <Label>{p.name}</Label>
                <Description>{poolSummary(p)}</Description>
              </div>
              <ListBox.ItemIndicator />
            </ListBox.Item>
          ))}
          <ListBox.Item id={NO_POOL} textValue="不绑代理" isDisabled={required}>
            {required ? '不绑代理（开着「只许走号的代理」，不能选）' : '不绑代理'}
            <ListBox.ItemIndicator />
          </ListBox.Item>
        </ListBox>
      </Select.Popover>
      <Description>{description}</Description>
    </Select>
  )
}

/** 登录完成后要告诉用户的代理情况和签到结果；没有返回空数组 */
function loginNotes(d: LoginPoll): string[] {
  const p = d.proxy
  const proxy = d.proxy_kept
    ? (p?.proxy_name ? `原来就在号池里，沿用原来的代理「${p.proxy_name}」` : p?.pool ? `原来就在号池里，沿用原来的池「${p.pool_name || p.pool}」` : '')
    : p?.proxy_name ? `绑在代理「${p.proxy_name}」上（池「${p.pool_name || p.pool}」）`
    : p?.waiting ? `在等代理：池「${p.pool_name || p.pool}」里没有空位，加了代理或调高上限后自动补上`
    : ''
  return [proxy, d.checkin_message].filter((s): s is string => !!s)
}

export function AddAccountModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  return (
    <Modal.Backdrop isOpen={open} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="md" placement="auto">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>添加账号</Modal.Heading>
          </Modal.Header>
          {/* 每次打开都是新的一轮：关闭时卸载内容，登录轮询随之停止 */}
          {open && <AddAccountBody onClose={onClose} />}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

function AddAccountBody({ onClose }: { onClose: () => void }) {
  const [target, setTarget] = useState('workbuddy:cn')
  return (
    <Modal.Body className="flex flex-col gap-4">
      <TargetSelect label="产品与地区" value={target} onChange={setTarget} targets={TARGETS} />
      {target.startsWith('workbuddy:') ? <Tabs key={target}>
        <Tabs.ListContainer>
          <Tabs.List aria-label="添加方式">
            <Tabs.Tab id="login">浏览器登录<Tabs.Indicator /></Tabs.Tab>
            <Tabs.Tab id="import">导入 JSON<Tabs.Indicator /></Tabs.Tab>
          </Tabs.List>
        </Tabs.ListContainer>
        <Tabs.Panel id="login" className="pt-4"><LoginPanel realm={target === 'workbuddy:global' ? 'global' : 'cn'} onClose={onClose} /></Tabs.Panel>
        <Tabs.Panel id="import" className="pt-4"><ImportPanel /></Tabs.Panel>
      </Tabs> : <>
        <p className="text-sm text-muted">当前为实验接入，各通道与模型的实测进度见多平台说明文档（docs/multi-platform.md）。添加后统一在「账号池」管理。</p>
        <ProviderAccountForm key={target} target={target} />
      </>}
    </Modal.Body>
  )
}

function LoginPanel({ realm, onClose }: { realm: string; onClose: () => void }) {
  const qc = useQueryClient()
  const [pool, setPool] = useState(DEFAULT_POOL)
  const [session, setSession] = useState<LoginStart | null>(null)
  const [starting, setStarting] = useState(false)
  const [startError, setStartError] = useState('')

  // 授权完成前每 3 秒查一次；完成或出错后停
  const poll = useQuery({
    queryKey: ['login-poll', session?.state],
    queryFn: () => api<LoginPoll>('login/poll?state=' + encodeURIComponent(session!.state)),
    enabled: !!session,
    retry: false,
    refetchInterval: (q) => (q.state.data?.done || q.state.error ? false : 3000),
  })
  const done = poll.data?.done ? poll.data : null
  const notes = done ? loginNotes(done) : []

  // 登录完成后自动关；有代理、签到的说明要看时不自动关
  const autoClose = !!done && notes.length === 0
  useEffect(() => {
    if (!done) return
    void qc.invalidateQueries({ queryKey: qk.overview })
    void qc.invalidateQueries({ queryKey: qk.proxies })
    if (!autoClose) return
    const t = setTimeout(onClose, 1600)
    return () => clearTimeout(t)
  }, [done, autoClose, qc, onClose])

  const start = async () => {
    setStarting(true)
    setStartError('')
    try {
      setSession(await post<LoginStart>('login/start', { realm, pool: poolParam(pool) }))
    } catch (e) {
      setStartError(errorMessage(e))
    } finally {
      setStarting(false)
    }
  }

  if (done) {
    const credits = done.credits != null && done.credits >= 0 ? ' · 积分 ' + creditsText(done.credits, done.credits_total) : ''
    return (
      <Alert status="success">
        <Alert.Indicator />
        <Alert.Content>
          <Alert.Title>已添加 {done.nickname || done.uid}{done.realm === 'global' ? '（国际版）' : ''}</Alert.Title>
          <Alert.Description>
            账号已载入池中{credits}
            {notes.map((n) => <span key={n} className="block break-all">{n}</span>)}
          </Alert.Description>
        </Alert.Content>
      </Alert>
    )
  }

  if (session) {
    return (
      <div className="flex flex-col gap-3">
        <p className="text-sm">在浏览器打开以下链接并登录：</p>
        <Typography.Code className="select-all break-all rounded-xl bg-surface-secondary px-3 py-2.5 text-xs text-accent">{session.url}</Typography.Code>
        <div className="flex flex-wrap gap-2">
          <Button variant="primary" onPress={() => window.open(session.url, '_blank', 'noopener')}>
            <ArrowUpRightFromSquare />在浏览器打开
          </Button>
          <Button variant="secondary" onPress={() => copyText(session.url).then(() => toast.success('链接已复制'), () => toast.danger('复制失败，请手动选择复制'))}>
            <Copy />复制链接
          </Button>
        </div>
        {poll.error ? (
          <Alert status="danger">
            <Alert.Indicator />
            <Alert.Content>
              <Alert.Title>授权失败</Alert.Title>
              <Alert.Description>{errorMessage(poll.error)}（关闭后重新添加）</Alert.Description>
            </Alert.Content>
          </Alert>
        ) : (
          <p className="flex items-center gap-2 text-sm text-muted"><Spinner size="sm" />等待授权完成，自动检测中…</p>
        )}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      {realm === 'global' && <p className="text-sm text-muted">国际版登录后，网关自动完成注册地区、激活与试用额度领取，全程无需手动操作。</p>}
      <PoolPicker
        value={pool}
        onChange={setPool}
        realm={realm}
        description="登录请求就走池里分到的代理，登录成功后新号绑在它上面；原来就在号池里的号沿用原来的绑定。"
      />
      {startError && <p className="text-sm text-danger">{startError}</p>}
      <div>
        <BusyButton variant="primary" busy={starting} onPress={() => void start()}>获取授权链接</BusyButton>
      </div>
    </div>
  )
}

function ImportPanel() {
  const qc = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [pool, setPool] = useState(DEFAULT_POOL)
  const [result, setResult] = useState<ImportResp | null>(null)
  const [error, setError] = useState('')

  const onFile = async (file: File) => {
    setBusy(true)
    setResult(null)
    setError('')
    const fd = new FormData()
    fd.append('file', file)
    fd.append('proxy_pool', poolParam(pool))
    try {
      setResult(await upload<ImportResp>('import', fd))
      void qc.invalidateQueries({ queryKey: qk.overview })
      void qc.invalidateQueries({ queryKey: qk.proxies })
    } catch (e) {
      setError('导入失败：' + errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  const errors = result?.errors ?? []
  const notes = result?.notes ?? []
  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm text-muted">选择账号池「导出凭证」得到的 JSON 文件（单个或批量都行），或 cockpit tools 导出的账号数组。网关导出的文件会原样保留企业版、国际版等信息；cockpit tools 的文件没有这些，按域名判断国内版 / 国际版。</p>
      <PoolPicker
        value={pool}
        onChange={setPool}
        description={pool === DEFAULT_POOL
          ? '凭证里带了代理的照凭证（网关导出的文件带 proxy_url），原来就在号池里的号沿用原来的绑定，其余放进各版本的默认池。'
          : pool === NO_POOL ? '不放进池；凭证里带了代理的照样认出来，原来就在号池里的号沿用原来的绑定。' : '号都放进这个池；凭证里带了代理的认出那个代理单独指定（新建的代理也建在这个池里）。'}
      />
      <FileDropZone
        accept=".json,application/json"
        title="拖拽 JSON 凭证文件到这里"
        hint="支持网关导出的单个／批量文件或 cockpit tools 账号数组"
        buttonLabel="选择 JSON 文件"
        busy={busy}
        onFile={onFile}
      />
      {error && <p className="text-sm text-danger">{error}</p>}
      {result && (
        <Alert status={errors.length || notes.length ? 'warning' : 'success'}>
          <Alert.Indicator />
          <Alert.Content>
            <Alert.Title>
              导入完成：成功 {result.imported} 个{result.skipped ? '，跳过 ' + result.skipped + ' 个' : ''}
              {errors.length ? '，' + errors.length + ' 个出错' : ''}
            </Alert.Title>
            {(errors.length > 0 || notes.length > 0) && (
              <Alert.Description>
                <ul className="mt-1 list-disc space-y-0.5 pl-4 text-xs">
                  {errors.map((e, i) => <li key={'e' + i} className="break-all">{e}</li>)}
                  {notes.map((n, i) => <li key={'n' + i} className="break-all">{n}</li>)}
                </ul>
              </Alert.Description>
            )}
          </Alert.Content>
        </Alert>
      )}
    </div>
  )
}
