// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 密钥：给别人用的客户端密钥，只能调 /v1/*，登不了面板。每个密钥可以设到期时间、每天和总量的用量上限、
// 模型白名单、只用哪些账号分组的号，列表里看今天和累计用量。管理员密钥是配置页的 api_key，不受这些限制。
// 网关把密钥存在 data/api_keys.json（0600，含原文），面板只认管理员密钥，所以这里能随时复制原文。
import { useState } from 'react'
import { ArrowsRotateRight, Copy, Ellipsis, Eye, EyeSlash, Pencil, Plus, TrashBin } from '@gravity-ui/icons'
import { Alert, Button, Chip, Dropdown, Label, Meter, Surface, toast, Typography } from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { post } from '../../lib/api'
import { copyText } from '../../lib/clipboard'
import { ago, fmtCredit, fmtLocalDateTime, fmtTok, timeMs, until } from '../../lib/format'
import { toastError } from '../../lib/hooks'
import { KEY_STATUS, fmtLimit, keyFormFrom, keyInput, keyStatus, limitRows, maskSecret } from '../../lib/keys'
import { qk, useGroups, useKeys } from '../../lib/queries'
import type { ApiKey, KeyLimits, KeysResp, KeyUsage } from '../../lib/types'
import { Empty, Loaded } from '../../components/Feedback'
import { InfoTip, Panel } from '../../components/Panel'
import { useConfirm } from '../../components/useConfirm'
import { KeyEditor, KeyUsageGuide } from './KeyEditor'

export function KeysPage() {
  const query = useKeys(10_000)
  const [editing, setEditing] = useState<ApiKey | 'new' | null>(null)
  const [shown, setShown] = useState<ApiKey | null>(null)
  const count = query.data?.keys?.length ?? 0
  return (
    <>
      <Panel
        title={
          <span className="flex items-center gap-1">
            客户端密钥
            <InfoTip label="密钥说明">
              {'客户端密钥只能调 /v1/*（OpenAI、Claude Code、Codex 三个入口和模型列表），登不了面板。\n' +
                '建了客户端密钥以后，/v1/* 必须带有效密钥，管理员密钥为空也一样。\n' +
                '用量上限按北京时间每天 00:00 重置。请求次数只算成功的请求（没号可用、上游报错的不算）；token 和积分在上游回复后计，并发请求可能略超上限。\n' +
                '模型白名单按经模型路由改写后、实际发给上游的模型名匹配，只认 * 通配。\n' +
                '限定了账号分组的密钥只用组里的号，组里的号都接不了时回 503，不借别的号。'}
            </InfoTip>
          </span>
        }
        desc={count ? `${count} 个密钥 · 用量 10 秒刷新一次` : undefined}
        actions={<Button size="sm" onPress={() => setEditing('new')}><Plus />新建密钥</Button>}
        contentClassName="flex flex-col gap-3"
      >
        <Loaded query={query} rows={3}>
          {(d) => (
            <>
              <AuthNotice d={d} />
              {(d.keys || []).length === 0 ? (
                <Empty title="还没有客户端密钥" action={<Button size="sm" onPress={() => setEditing('new')}><Plus />新建密钥</Button>}>
                  给别人用时建一个密钥：可以限到期时间、每天和总量的用量、能用哪些模型，用量单独统计。
                </Empty>
              ) : (
                <ul className="flex flex-col gap-3">
                  {(d.keys || []).map((k) => <KeyRow key={k.id} k={k} onEdit={() => setEditing(k)} onShowGuide={() => setShown(k)} />)}
                </ul>
              )}
            </>
          )}
        </Loaded>
      </Panel>
      <KeyEditor
        target={editing}
        onClose={() => setEditing(null)}
        onCreated={(k) => {
          setEditing(null)
          setShown(k)
        }}
      />
      <KeyUsageGuide k={shown} onClose={() => setShown(null)} />
    </>
  )
}

/** 鉴权状态提示：管理员密钥没设时面板谁都能打开，能看到密钥原文 */
function AuthNotice({ d }: { d: KeysResp }) {
  if (d.admin_key_set) return null
  const has = (d.keys || []).length > 0
  return (
    <Alert status={has ? 'danger' : 'warning'}>
      <Alert.Indicator />
      <Alert.Content>
        <Alert.Title>{has ? '管理员密钥还没设：面板不用登录就能打开，谁都能看到这里的密钥原文' : '现在没开鉴权：谁都能调 /v1/*'}</Alert.Title>
        <Alert.Description>
          {has
            ? '/v1/* 已经要求带密钥，但面板仍然是开放的。'
            : '建第一个客户端密钥后，/v1/* 必须带有效密钥；面板要等设了管理员密钥才需要登录。'}
          到<Link to="/config" className="mx-0.5 underline">配置页</Link>设置 api_key（管理员密钥）。
        </Alert.Description>
      </Alert.Content>
    </Alert>
  )
}

function KeyRow({ k, onEdit, onShowGuide }: { k: ApiKey; onEdit: () => void; onShowGuide: () => void }) {
  const qc = useQueryClient()
  const [confirmEl, ask] = useConfirm()
  const [reveal, setReveal] = useState(false)
  const status = KEY_STATUS[keyStatus(k)]
  const groups = useGroups().data
  const groupName = (id: string) => groups?.find((g) => g.id === id)?.name ?? id

  const run = async (path: string, body: unknown, done: string) => {
    try {
      await post(path, body)
      toast.success(done)
    } catch (e) {
      toastError(e)
    } finally {
      void qc.invalidateQueries({ queryKey: qk.keys })
      if (k.groups?.length) void qc.invalidateQueries({ queryKey: qk.groups })
    }
  }
  const base = 'keys/' + encodeURIComponent(k.id)
  const toggle = () => run(base, { ...keyInput(keyFormFrom(k)), enabled: !k.enabled }, k.enabled ? `已停用「${k.name}」` : `已启用「${k.name}」`)
  const regenerate = () => ask({
    title: `重新生成「${k.name}」的密钥？`,
    body: '旧密钥立即失效，用它的客户端要换成新密钥。用量、上限和白名单不变。',
    confirmLabel: '重新生成',
    onConfirm: () => void run(base + '/regenerate', undefined, '已重新生成，记得把新密钥发给使用者'),
  })
  const remove = () => ask({
    title: `删除「${k.name}」？`,
    body: '用这个密钥的请求立即被拒。用量页里它的历史用量保留，显示为「已删除的密钥」。',
    confirmLabel: '删除',
    onConfirm: () => void run(base + '/delete', undefined, `已删除「${k.name}」`),
  })
  const copy = () => copyText(k.secret).then(() => toast.success('已复制密钥'), (e: unknown) => toastError(e, '复制失败：'))

  const expMs = timeMs(k.expires_at)
  return (
    <Surface<'li'> variant="secondary" render={(props) => <li {...props} />} className="flex flex-col gap-3 rounded-2xl p-3">
      <div className="flex flex-wrap items-start gap-2">
        <div className="flex min-w-0 flex-col gap-0.5">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium break-all">{k.name}</span>
            <Chip size="sm" variant="soft" color={status.color}>{status.label}</Chip>
          </div>
          {k.note && <p className="text-sm whitespace-pre-line text-muted">{k.note}</p>}
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-1">
          <Button size="sm" variant="ghost" onPress={onShowGuide}>接入方法</Button>
          <Button size="sm" variant="ghost" onPress={onEdit}><Pencil />编辑</Button>
          <Dropdown>
            <Button isIconOnly size="sm" variant="ghost" aria-label={`「${k.name}」的更多操作`}><Ellipsis /></Button>
            <Dropdown.Popover placement="bottom end">
              <Dropdown.Menu
                onAction={(id) => {
                  if (id === 'toggle') void toggle()
                  else if (id === 'regenerate') regenerate()
                  else if (id === 'delete') remove()
                }}
              >
                <Dropdown.Item id="toggle" textValue={k.enabled ? '停用' : '启用'}>
                  <Label>{k.enabled ? '停用' : '启用'}</Label>
                </Dropdown.Item>
                <Dropdown.Item id="regenerate" textValue="重新生成密钥">
                  <ArrowsRotateRight className="size-4 shrink-0 text-muted" />
                  <Label>重新生成密钥</Label>
                </Dropdown.Item>
                <Dropdown.Item id="delete" textValue="删除" variant="danger">
                  <TrashBin className="size-4 shrink-0 text-danger" />
                  <Label>删除</Label>
                </Dropdown.Item>
              </Dropdown.Menu>
            </Dropdown.Popover>
          </Dropdown>
        </div>
      </div>

      <div className="flex min-w-0 items-center gap-1">
        <Typography.Code className="min-w-0 truncate rounded-lg bg-surface px-2 py-1 select-all">{reveal ? k.secret : maskSecret(k.secret)}</Typography.Code>
        <Button isIconOnly size="sm" variant="ghost" aria-label={reveal ? '隐藏密钥' : '显示密钥'} onPress={() => setReveal((v) => !v)}>
          {reveal ? <EyeSlash /> : <Eye />}
        </Button>
        <Button isIconOnly size="sm" variant="ghost" aria-label="复制密钥" onPress={() => void copy()}><Copy /></Button>
      </div>

      <div className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-2 xl:grid-cols-4">
        <UsageBlock title="今天" usage={k.today} limit={k.daily_limit} />
        <UsageBlock title="累计" usage={k.used} limit={k.total_limit} />
        <div className="flex flex-col gap-1">
          <span className="text-xs text-muted">时间</span>
          <span>到期：{expMs == null ? '不过期' : <>{fmtLocalDateTime(expMs)} <span className={k.expired ? 'text-warning' : 'text-muted'}>（{until(expMs)}）</span></>}</span>
          <span className="text-muted">最近使用：{ago(k.last_used_at) === '—' ? '还没用过' : ago(k.last_used_at)}</span>
          <span className="text-muted">创建于 {fmtLocalDateTime(timeMs(k.created_at) ?? 0)}</span>
        </div>
        <div className="flex flex-col gap-1">
          <span className="text-xs text-muted">模型白名单</span>
          {(k.models || []).length ? (
            <div className="flex flex-wrap gap-1">
              {k.models!.map((m) => <Chip key={m} size="sm" variant="secondary" className="font-mono">{m}</Chip>)}
            </div>
          ) : <span className="text-muted">不限</span>}
          {(k.groups || []).length > 0 && (
            <>
              <span className="mt-1 text-xs text-muted">只用这些分组的号</span>
              <div className="flex flex-wrap gap-1">
                {k.groups!.map((id) => <Chip key={id} size="sm" variant="secondary">{groupName(id)}</Chip>)}
              </div>
            </>
          )}
        </div>
      </div>
      {confirmEl}
    </Surface>
  )
}

/** 一段时间的用量：三项数字，设了上限的项另画一条占用条 */
function UsageBlock({ title, usage, limit }: { title: string; usage: KeyUsage; limit: KeyLimits }) {
  const rows = limitRows(limit, usage)
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs text-muted">{title}{rows.length ? '' : ' · 不限'}</span>
      <span className="tabular-nums">
        {fmtTok(usage.requests)} 次 · {fmtTok(usage.tokens)} token · {fmtCredit(usage.credits)} 积分
      </span>
      {rows.map((r) => (
        <Meter
          key={r.kind}
          size="sm"
          value={r.pct}
          color={r.full ? 'danger' : r.pct >= 80 ? 'warning' : 'accent'}
          valueLabel={`${fmtLimit(r.kind, r.used)} / ${fmtLimit(r.kind, r.limit)}`}
          aria-label={`${title}${r.label}上限`}
        >
          <Label className="text-xs text-muted">{r.label}{r.full ? '（已到上限）' : ''}</Label>
          <Meter.Output className="text-xs tabular-nums" />
          <Meter.Track><Meter.Fill /></Meter.Track>
        </Meter>
      ))}
    </div>
  )
}
