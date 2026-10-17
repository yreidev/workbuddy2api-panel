// 更新提醒与一键更新：升级后第一次打开弹「本次更新」，有新版本时弹「有新版本」（列出新版本的变更，
// 能一键更新就给「更新到 vX」按钮，否则给手动升级方法）。每条提醒在本浏览器只自动弹一次（记在 localStorage），
// 没升级前侧栏一直有「有新版本」入口可以再打开；点侧栏版本号打开「版本信息」，可立即检查、退回自带版本。
// 条目来自网关解析的 docs/CHANGELOG.md：「本次更新」取二进制内嵌的那份，「有新版本」取网关定时从 GitHub 读到的那份。
// 一键更新由网关下载、校验后原地重启（internal/selfupdate），本页轮询到新版本上线后自动刷新。
import { useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Alert, buttonVariants, Button, Chip, Label, Link, Modal, ProgressBar, toast } from '@heroui/react'
import { post } from '../lib/api'
import { fmtBytes, dateTime } from '../lib/format'
import { toastError, useNow } from '../lib/hooks'
import { qk, useVersion } from '../lib/queries'
import {
  DISMISSED_UPDATE_KEY, SEEN_VERSION_KEY, inlineParts, pickNotice, readStored, restartOutcome, shouldRecordSeen, writeStored,
  type NoticeMode, type PendingRestart,
} from '../lib/update'
import type { Release, SelfUpdatePhase, SelfUpdateStatus, VersionResp } from '../lib/types'
import { BusyButton } from './Feedback'
import { useConfirm } from './useConfirm'

const DOCS_URL = 'https://github.com/yreidev/workbuddy2api-panel/blob/main/docs/'
const CHANGELOG_URL = DOCS_URL + 'CHANGELOG.md'
const UPGRADE_DOC_URL = DOCS_URL + 'deployment.md#升级备份与停机'
/** 正文里的链接：带常驻下划线，和普通文字区分开 */
const TEXT_LINK = 'underline decoration-muted/50 underline-offset-2'
/** 重启等了这么久还没恢复，提示去看日志 */
const SLOW_RESTART_SEC = 120

/** 手动打开的窗：侧栏「有新版本」→ update，侧栏版本号 → about */
export type ManualPanel = 'update' | 'about' | null

/** 侧栏的「有新版本」入口：没有新版本或关了更新检查时不显示 */
export function UpdateBadge({ onPress }: { onPress: () => void }) {
  const { data } = useVersion()
  if (!data?.update_available) return null
  return (
    <button
      type="button"
      onClick={onPress}
      className="rounded-full bg-accent-soft px-1.5 py-px text-[11px] font-medium text-accent-soft-foreground hover:opacity-80"
    >
      有新版本 v{data.latest}
    </button>
  )
}

export function UpdateNotice({ manual, onManualChange }: { manual: ManualPanel; onManualChange: (m: ManualPanel) => void }) {
  const qc = useQueryClient()
  const { data } = useVersion()
  const [storedSeen, setStoredSeen] = useState(() => readStored(SEEN_VERSION_KEY))
  const [dismissed, setDismissed] = useState(() => readStored(DISMISSED_UPDATE_KEY))
  // pending 在进度窗关掉后仍保留：等太久关掉了窗，网关之后恢复到目标版本照样自动刷新
  const [pending, setPending] = useState<PendingRestart | null>(null)
  const [progressOpen, setProgressOpen] = useState(false)
  const [confirmEl, ask] = useConfirm()

  // 第一次打开、降级或没有新条目：不弹窗，直接把当前版本记为看过
  const autoSeen = data && shouldRecordSeen(data, storedSeen) ? data.version : null
  useEffect(() => {
    if (autoSeen) writeStored(SEEN_VERSION_KEY, autoSeen)
  }, [autoSeen])
  const seen = autoSeen ?? storedSeen

  // 新版上线：刷新页面拿新前端，刷新后按版本变化弹「本次更新」
  const outcome = pending ? restartOutcome(pending, data) : null
  useEffect(() => {
    if (outcome === 'done') window.location.reload()
  }, [outcome])

  const auto = data ? pickNotice(data, seen, dismissed) : null
  const mode: NoticeMode | 'about' | 'progress' | null = pending && progressOpen ? 'progress'
    : manual === 'about' ? 'about'
    : manual === 'update' && data?.update_available ? 'update'
    : auto

  const closeWhatsNew = () => {
    if (!data) return
    writeStored(SEEN_VERSION_KEY, data.version)
    setStoredSeen(data.version)
  }
  const closeUpdate = () => {
    if (data?.latest) {
      writeStored(DISMISSED_UPDATE_KEY, data.latest)
      setDismissed(data.latest)
    }
    onManualChange(null)
  }

  /** 已经开始：缓存里的阶段先改成和网关一致（免得用发起前的 idle 误判），再盯着进度 */
  const begin = (kind: PendingRestart['kind'], target: string, phase: SelfUpdatePhase) => {
    qc.setQueryData<VersionResp>(qk.version, (old) => old?.self_update
      ? { ...old, self_update: { ...old.self_update, phase, target, error: undefined, done: 0, total: 0 } }
      : old)
    setPending({ kind, target, startedAt: Date.now() })
    setProgressOpen(true)
    onManualChange(null)
    void qc.invalidateQueries({ queryKey: qk.version })
  }

  const askUpdate = (su: SelfUpdateStatus, version: string) => ask({
    title: `更新到 v${version}？`,
    danger: false,
    confirmLabel: '更新并重启',
    body: (
      <>
        网关会从 GitHub 下载 v{version}，核对 sha256 并试运行通过后重启：停止接新请求，在途请求最多等 {su.grace_sec} 秒，
        一般一分钟内恢复，页面会自动刷新。新程序放在数据目录的 <Code>bin/</Code> 里，镜像不变；之后可以在「版本信息」里退回
        v{su.base}。
      </>
    ),
    onConfirm: () => {
      post('update', { version }).then(() => begin('update', version, 'downloading'), (e) => toastError(e, '没能开始更新：'))
    },
  })

  const askRollback = (su: SelfUpdateStatus) => ask({
    title: `退回 v${su.base}？`,
    confirmLabel: '退回并重启',
    body: <>删掉面板安装的 v{data?.version}，重启后运行程序自带的 v{su.base}。在途请求最多等 {su.grace_sec} 秒。</>,
    onConfirm: () => {
      post<{ restarting: boolean }>('update/rollback').then(
        (r) => r.restarting ? begin('rollback', su.base, 'restarting') : void qc.invalidateQueries({ queryKey: qk.version }),
        (e) => toastError(e, '退回失败：'),
      )
    },
  })

  const whatsNew = data?.whats_new || []
  const updates = data?.updates || []
  const su = data?.self_update
  const canSelfUpdate = !!(su?.supported && data?.latest)
  return (
    <>
      <NoticeModal
        open={mode === 'whatsnew'}
        onClose={closeWhatsNew}
        title={'已更新到 v' + (data?.version ?? '')}
        desc={
          seen === null ? `v${data?.version} 包含以下变更。`
            : whatsNew.length > 1 ? `从 v${seen} 升级到 v${data?.version}，包含以下 ${whatsNew.length} 个版本的变更。`
            : '这次升级包含以下变更。'
        }
        footer={
          <>
            <ExternalButton href={CHANGELOG_URL} variant="ghost">完整更新日志</ExternalButton>
            <Button variant="primary" slot="close">知道了</Button>
          </>
        }
      >
        <ReleaseList releases={whatsNew} />
      </NoticeModal>
      <NoticeModal
        open={mode === 'update'}
        onClose={closeUpdate}
        title={'有新版本 v' + (data?.latest ?? '')}
        desc={`当前 v${data?.version ?? ''}。以下是新版本的变更，${canSelfUpdate ? '可以一键更新，也可以按最后的方法手动升级' : '升级方法在最后'}。`}
        footer={
          <>
            {data?.release_url && <ExternalButton href={data.release_url} variant="ghost">查看 Release</ExternalButton>}
            <Button variant={canSelfUpdate ? 'secondary' : 'primary'} slot="close">知道了</Button>
            {canSelfUpdate && su && data?.latest && (
              <Button variant="primary" onPress={() => askUpdate(su, data.latest!)}>更新到 v{data.latest}</Button>
            )}
          </>
        }
      >
        {su?.phase === 'failed' && su.error && (
          <Alert status="danger" className="mb-4">
            <Alert.Indicator />
            <Alert.Content>
              <Alert.Title>上次一键更新没有成功</Alert.Title>
              <Alert.Description>{su.error}</Alert.Description>
            </Alert.Content>
          </Alert>
        )}
        <ReleaseList releases={updates} />
        <UpgradeSteps su={su} />
      </NoticeModal>
      <AboutModal
        open={mode === 'about'}
        onClose={() => onManualChange(null)}
        data={data}
        onShowUpdate={() => onManualChange('update')}
        onRollback={askRollback}
      />
      <ProgressModal
        open={mode === 'progress'}
        pending={pending}
        outcome={outcome}
        data={data}
        onClose={() => setProgressOpen(false)}
      />
      {confirmEl}
    </>
  )
}

function NoticeModal({ open, onClose, title, desc, footer, children }: {
  open: boolean
  onClose: () => void
  title: string
  desc?: string
  footer: ReactNode
  children: ReactNode
}) {
  return (
    <Modal.Backdrop isOpen={open} onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="md" scroll="inside" placement="auto">
        <Modal.Dialog>
          <Modal.CloseTrigger />
          <Modal.Header>
            <Modal.Heading>{title}</Modal.Heading>
            {desc && <p className="mt-1.5 text-sm text-muted">{desc}</p>}
          </Modal.Header>
          <Modal.Body>{children}</Modal.Body>
          <Modal.Footer className="flex-wrap">{footer}</Modal.Footer>
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

/** 版本信息：当前跑的是哪一份、最新版本、立即检查、退回自带版本 */
function AboutModal({ open, onClose, data, onShowUpdate, onRollback }: {
  open: boolean
  onClose: () => void
  data?: VersionResp
  onShowUpdate: () => void
  onRollback: (su: SelfUpdateStatus) => void
}) {
  const qc = useQueryClient()
  const [checking, setChecking] = useState(false)
  const [removing, setRemoving] = useState(false)
  const su = data?.self_update

  const checkNow = async () => {
    setChecking(true)
    try {
      const r = await post<VersionResp>('version/check')
      if (r.check_error) toast.danger('检查更新失败：' + r.check_error)
      else toast.success(r.update_available ? `有新版本 v${r.latest}` : '已是最新版本')
      await qc.invalidateQueries({ queryKey: qk.version })
    } catch (e) {
      toastError(e, '检查更新失败：')
    } finally {
      setChecking(false)
    }
  }
  // 自动退回后留下的那份：删掉，提示也随之消失
  const removeFailed = async () => {
    setRemoving(true)
    try {
      await post('update/rollback')
      toast.success('已删掉面板安装的那份')
      await qc.invalidateQueries({ queryKey: qk.version })
    } catch (e) {
      toastError(e, '删除失败：')
    } finally {
      setRemoving(false)
    }
  }

  const latest = !data?.update_check ? '更新检查已关闭（配置 → 服务 → 检查更新）'
    : data.latest ? (data.update_available ? `v${data.latest}（有新版本）` : `v${data.latest}（已是最新）`)
    : data.check_error ? '还没读到' : '启动后约一分钟读取'
  return (
    <NoticeModal
      open={open}
      onClose={onClose}
      title="版本信息"
      footer={
        <>
          {su?.override && <Button variant="danger-soft" onPress={() => onRollback(su)}>退回 v{su.base}</Button>}
          {data?.update_check && <BusyButton variant="secondary" busy={checking} onPress={() => void checkNow()}>立即检查</BusyButton>}
          {data?.update_available
            ? <Button variant="primary" onPress={onShowUpdate}>查看新版本</Button>
            : <Button variant="primary" slot="close">关闭</Button>}
        </>
      }
    >
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2.5 text-sm text-foreground">
        <dt className="text-muted">当前版本</dt>
        <dd className="flex flex-wrap items-center gap-2">
          v{data?.version}
          {su && <Chip size="sm" variant="soft" color={su.override ? 'accent' : 'default'}>{su.override ? '面板安装' : '程序自带'}</Chip>}
        </dd>
        {su?.override && (
          <>
            <dt className="text-muted">程序自带</dt>
            <dd>v{su.base}</dd>
          </>
        )}
        <dt className="text-muted">最新版本</dt>
        <dd>{latest}</dd>
        {data?.update_check && (
          <>
            <dt className="text-muted">上次检查</dt>
            <dd>
              {data.checked_at ? dateTime(Date.parse(data.checked_at)) : '—'}
              {data.check_error && <span className="block text-danger">最近一次失败：{data.check_error}</span>}
            </dd>
          </>
        )}
        <dt className="text-muted">一键更新</dt>
        <dd>{!su ? '未接入' : su.supported ? '可用' : su.reason}</dd>
      </dl>
      {su?.rolled_back && (
        <Alert status="warning" className="mt-4">
          <Alert.Indicator />
          <Alert.Content>
            <Alert.Title>面板安装的版本没能正常运行</Alert.Title>
            <Alert.Description>{su.rolled_back}。可以删掉那份，等下一个版本再更新。</Alert.Description>
            <BusyButton className="mt-2" size="sm" variant="secondary" busy={removing} onPress={() => void removeFailed()}>删掉那份</BusyButton>
          </Alert.Content>
        </Alert>
      )}
      {su?.phase === 'failed' && su.error && (
        <Alert status="danger" className="mt-4">
          <Alert.Indicator />
          <Alert.Content>
            <Alert.Title>上次一键更新没有成功</Alert.Title>
            <Alert.Description>{su.error}</Alert.Description>
          </Alert.Content>
        </Alert>
      )}
    </NoticeModal>
  )
}

/** 一键更新 / 退回的进度：进行中不能关；失败或等太久才给关闭按钮 */
function ProgressModal({ open, pending, outcome, data, onClose }: {
  open: boolean
  pending: PendingRestart | null
  outcome: ReturnType<typeof restartOutcome> | null
  data?: VersionResp
  onClose: () => void
}) {
  const title = pending?.kind === 'rollback' ? `正在退回 v${pending.target}` : `正在更新到 v${pending?.target ?? ''}`
  const ended = outcome === 'failed' || outcome === 'elsewhere'
  return (
    <Modal.Backdrop isOpen={open} isDismissable={false} isKeyboardDismissDisabled onOpenChange={(v) => { if (!v) onClose() }}>
      <Modal.Container size="sm" placement="auto">
        <Modal.Dialog>
          <Modal.Header>
            <Modal.Heading>{title}</Modal.Heading>
          </Modal.Header>
          <Modal.Body>{pending && <ProgressBody pending={pending} outcome={outcome} data={data} onClose={onClose} />}</Modal.Body>
          {ended && (
            <Modal.Footer>
              <Button variant="primary" slot="close">关闭</Button>
            </Modal.Footer>
          )}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  )
}

function ProgressBody({ pending, outcome, data, onClose }: {
  pending: PendingRestart
  outcome: ReturnType<typeof restartOutcome> | null
  data?: VersionResp
  onClose: () => void
}) {
  const now = useNow(1000)
  const su = data?.self_update
  if (outcome === 'failed') {
    return (
      <Alert status="danger">
        <Alert.Indicator />
        <Alert.Content>
          <Alert.Title>更新没有成功</Alert.Title>
          <Alert.Description>{su?.error}。正在运行的网关没有受影响，可以稍后再试。</Alert.Description>
        </Alert.Content>
      </Alert>
    )
  }
  if (outcome === 'elsewhere') {
    return (
      <Alert status="warning">
        <Alert.Indicator />
        <Alert.Content>
          <Alert.Title>网关已重启，但运行的是 v{data?.version}</Alert.Title>
          <Alert.Description>{su?.rolled_back || `没能切换到 v${pending.target}，详情见运行日志。`}</Alert.Description>
        </Alert.Content>
      </Alert>
    )
  }

  const waited = Math.max(0, Math.round((now - pending.startedAt) / 1000))
  const phase = su?.phase
  const total = su?.total || 0
  const step = pending.kind === 'update' && phase === 'downloading' ? '下载安装包'
    : pending.kind === 'update' && phase === 'verifying' ? '校验并试运行新程序'
    : '重启网关'
  const percent = step === '下载安装包' && total > 0 ? Math.min(100, ((su?.done || 0) / total) * 100) : null
  const hint = step === '重启网关'
    ? `网关正在优雅停机（在途请求最多等 ${su?.grace_sec ?? 5} 秒）并切换版本，恢复后页面会自动刷新。`
    : '核对 sha256 并试运行通过才会重启；中途失败不影响正在运行的网关。'
  return (
    <div className="flex flex-col gap-3">
      <ProgressBar
        aria-label={step}
        className="w-full"
        isIndeterminate={percent === null}
        value={percent ?? undefined}
        valueLabel={percent !== null ? `${fmtBytes(su?.done)} / ${fmtBytes(total)}` : undefined}
      >
        <Label>{step}</Label>
        {percent !== null && <ProgressBar.Output />}
        <ProgressBar.Track>
          <ProgressBar.Fill />
        </ProgressBar.Track>
      </ProgressBar>
      <p className="text-sm text-muted">{hint}</p>
      {step === '重启网关' && waited >= SLOW_RESTART_SEC && (
        <Alert status="warning">
          <Alert.Indicator />
          <Alert.Content>
            <Alert.Title>已经等了 {waited} 秒还没恢复</Alert.Title>
            <Alert.Description>
              看一下网关日志（Docker 用 <Code>docker logs</Code>）。新版连续 3 次没能稳定运行会自动退回自带版本；恢复后页面仍会自动刷新。
            </Alert.Description>
            <Button className="mt-2" size="sm" variant="secondary" onPress={onClose}>先关掉</Button>
          </Alert.Content>
        </Alert>
      )}
    </div>
  )
}

function ReleaseList({ releases }: { releases: Release[] }) {
  return (
    <div className="flex flex-col gap-5">
      {releases.map((r) => (
        <section key={r.version} className="flex flex-col gap-2">
          <h3 className="flex items-baseline gap-2 text-sm font-semibold text-foreground">
            v{r.version}
            {r.date && <span className="text-xs font-normal text-muted">{r.date}</span>}
          </h3>
          {r.summary && <p className="text-sm text-muted"><Inline text={r.summary} /></p>}
          {(r.groups || []).map((g, i) => (
            <div key={i}>
              {g.title && <p className="text-xs font-medium text-muted">{g.title}</p>}
              <ul className="mt-1 flex list-disc flex-col gap-1 pl-5 text-sm text-foreground">
                {(g.items || []).map((it, j) => <li key={j}><Inline text={it} /></li>)}
              </ul>
            </div>
          ))}
        </section>
      ))}
    </div>
  )
}

function UpgradeSteps({ su }: { su?: SelfUpdateStatus }) {
  return (
    <section className="mt-5 flex flex-col gap-2 rounded-xl bg-default/60 p-3 text-sm text-foreground">
      <p className="font-medium">{su?.supported ? '手动升级' : '升级方法'}</p>
      {su && !su.supported && <p className="text-muted">一键更新不可用：{su.reason}</p>}
      <p>
        <span className="text-muted">docker run：</span>
        先 <Code>docker pull ghcr.io/yreidev/workbuddy2api-panel:latest</Code>，删掉旧容器后按原来的参数重新 <Code>docker run</Code>
      </p>
      <p>
        <span className="text-muted">Docker Compose：</span>
        在仓库目录执行 <Code>git pull && docker compose up -d --build</Code>
      </p>
      <p>
        <span className="text-muted">二进制：</span>
        到 Release 页面下载对应平台的包，替换 <Code>wb2api</Code> 后重启
      </p>
      <p className="text-xs text-muted">
        配置、账号凭证和 <Code>data/</Code> 原样保留。详见
        <Link href={UPGRADE_DOC_URL} target="_blank" rel="noopener noreferrer" className={'text-xs ' + TEXT_LINK}>部署文档</Link>。
      </p>
    </section>
  )
}

function ExternalButton({ href, variant, children }: { href: string; variant: 'secondary' | 'ghost'; children: ReactNode }) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" className={buttonVariants({ variant })}>
      {children}
    </a>
  )
}

function Code({ children }: { children: ReactNode }) {
  return <code className="wrap-break-word rounded bg-default px-1 py-0.5 font-mono text-[0.85em]">{children}</code>
}

function Inline({ text }: { text: string }) {
  return inlineParts(text, DOCS_URL).map((p, i) =>
    p.kind === 'code' ? <Code key={i}>{p.text}</Code>
      : p.kind === 'link' ? <Link key={i} href={p.href} target="_blank" rel="noopener noreferrer" className={'text-sm ' + TEXT_LINK}>{p.text}</Link>
      : <span key={i}>{p.text}</span>,
  )
}
