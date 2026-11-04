// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 模型路由：客户端发来的模型名不在上游目录里时（Claude Code、Codex 的默认模型名），按规则改用目标模型。
// 规则存 config.json 的 model_routes，保存即热生效；匹配口径与网关一致（lib/routes.ts）。
import { useMemo, useState } from 'react'
import { ArrowDown, ArrowUp, Plus, TrashBin } from '@gravity-ui/icons'
import { Button, Input, Label, Surface, TextField, toast } from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { useBlocker } from '@tanstack/react-router'
import { post } from '../../lib/api'
import { toastError } from '../../lib/hooks'
import { qk, useConfig, useModels } from '../../lib/queries'
import {
  CLAUDE_TIERS, cleanRoutes, explainRoute, routesFromConfig, shadowedBy, validateRoutes, type ModelRoute, type RouteVerdict,
} from '../../lib/routes'
import type { SaveConfigResp } from '../../lib/types'
import { BusyButton, Confirm, Loaded } from '../../components/Feedback'
import { ModelComboBox } from '../../components/ModelComboBox'
import { InfoTip, Panel } from '../../components/Panel'

export function ModelRoutes() {
  const query = useConfig()
  return (
    <Panel
      title="模型路由"
      desc="客户端发来的模型名不在上游目录里时（比如 Claude Code、Codex 的默认模型名），从上往下找第一条匹配的规则，改用它的目标模型。"
      actions={
        <InfoTip label="匹配规则">
          {'只认 * 通配（任意长度），不区分大小写。\n目录里有的名字直通，不会被规则改动；带 cn: / global: 前缀的名字也不改。\n目标模型可以带前缀指定域。保存后立即生效，三个入口（chat/completions、messages、responses）都按这张表改写。'}
        </InfoTip>
      }
      contentClassName="flex flex-col gap-3"
    >
      <Loaded query={query} rows={3}>
        {(d) => <RoutesForm saved={routesFromConfig(d.config)} />}
      </Loaded>
    </Panel>
  )
}

function RoutesForm({ saved }: { saved: ModelRoute[] }) {
  const qc = useQueryClient()
  const models = useModels()
  const ids = useMemo(() => (models.data?.models || []).map((m) => m.id), [models.data])
  const [rows, setRows] = useState<ModelRoute[]>(saved)
  const [probe, setProbe] = useState('')
  const [saving, setSaving] = useState(false)
  const dirty = JSON.stringify(cleanRoutes(rows)) !== JSON.stringify(cleanRoutes(saved))
  const error = validateRoutes(rows)
  const shadow = shadowedBy(rows)

  const blocker = useBlocker({ shouldBlockFn: () => dirty, withResolver: true, enableBeforeUnload: () => dirty })

  const setRow = (i: number, patch: Partial<ModelRoute>) => setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const move = (i: number, d: -1 | 1) => setRows((rs) => {
    const next = [...rs]
    ;[next[i], next[i + d]] = [next[i + d], next[i]]
    return next
  })
  const remove = (i: number) => setRows((rs) => rs.filter((_, j) => j !== i))
  // 三档插到最前面（放在后面会被 claude-* 这类宽规则挡住），已经有的匹配不重复加
  const addTiers = () => setRows((rs) => [...CLAUDE_TIERS.filter((t) => !rs.some((r) => r.match.trim() === t.match)), ...rs])

  const save = async () => {
    if (error) {
      toast.danger(error)
      return
    }
    setSaving(true)
    try {
      const routes = cleanRoutes(rows)
      await post<SaveConfigResp>('config', { model_routes: routes })
      toast.success(routes.length ? '模型路由已保存并立即生效' : '已清空模型路由')
      setRows(routes)
      void qc.invalidateQueries({ queryKey: qk.config })
    } catch (e) {
      toastError(e, '保存失败：')
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      {rows.length === 0 ? (
        <p className="text-sm text-muted">
          还没有规则：目录里没有的模型名会直接报「模型不存在」。可以先加 Claude Code 三档，再给每档选一个上游模型。
        </p>
      ) : (
        <ol className="flex flex-col gap-2">
          {rows.map((r, i) => (
            <Surface<'li'> key={i} variant="secondary" render={(props) => <li {...props} />} className="flex flex-col gap-1 rounded-xl p-2">
              <div className="flex flex-wrap items-end gap-2 sm:flex-nowrap">
                <span className="w-6 shrink-0 pb-2 text-center text-sm text-muted tabular-nums">{i + 1}</span>
                <TextField value={r.match} onChange={(v) => setRow(i, { match: v })} autoComplete="off" className="min-w-40 flex-1">
                  <Label>匹配</Label>
                  <Input placeholder="claude-*haiku*" />
                </TextField>
                <span className="hidden pb-2 text-muted sm:inline" aria-hidden="true">→</span>
                <ModelComboBox
                  label="目标模型"
                  ids={ids}
                  inputValue={r.target}
                  onInputChange={(v) => setRow(i, { target: v })}
                  placeholder="从目录选，或手填"
                  className="min-w-48 flex-1"
                />
                <div className="ml-auto flex shrink-0 gap-1 pb-0.5">
                  <Button isIconOnly size="sm" variant="ghost" aria-label="上移" isDisabled={i === 0} onPress={() => move(i, -1)}><ArrowUp /></Button>
                  <Button isIconOnly size="sm" variant="ghost" aria-label="下移" isDisabled={i === rows.length - 1} onPress={() => move(i, 1)}><ArrowDown /></Button>
                  <Button isIconOnly size="sm" variant="ghost" aria-label="删除" onPress={() => remove(i)}><TrashBin /></Button>
                </div>
              </div>
              <ShadowNote by={shadow[i]} />
            </Surface>
          ))}
        </ol>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="secondary" onPress={() => setRows((rs) => [...rs, { match: '', target: '' }])}><Plus />添加规则</Button>
        <Button size="sm" variant="ghost" onPress={addTiers}>添加 Claude Code 三档</Button>
        {dirty && error && <span className="text-sm text-danger">{error}</span>}
        <div className="ml-auto flex gap-2">
          {dirty && <Button size="sm" variant="ghost" onPress={() => setRows(saved)}>放弃修改</Button>}
          <BusyButton size="sm" busy={saving} isDisabled={!dirty} onPress={save}>保存规则</BusyButton>
        </div>
      </div>

      <TextField value={probe} onChange={setProbe} autoComplete="off" className="w-full sm:w-96">
        <Label>试一下</Label>
        <Input placeholder="输入客户端会发的模型名，如 claude-sonnet-4-5" />
      </TextField>
      <Verdict v={explainRoute(rows, probe, ids)} catalogReady={ids.length > 0} />

      <Confirm
        open={blocker.status === 'blocked'}
        onClose={() => blocker.reset?.()}
        title="模型路由还没保存"
        body="离开后这次的修改会丢掉。"
        confirmLabel="不保存，离开"
        onConfirm={() => blocker.proceed?.()}
      />
    </>
  )
}

/** 规则被前面更宽的规则挡住时的提示（by 是挡住它的那条的下标） */
function ShadowNote({ by }: { by: number | null }) {
  if (by === null) return null
  return <p className="pl-8 text-xs text-danger">被第 {by + 1} 条挡住，永远轮不到它：把它移到第 {by + 1} 条前面</p>
}

/** 「试一下」的结论：按当前编辑中的规则算，不必先保存 */
function Verdict({ v, catalogReady }: { v: RouteVerdict; catalogReady: boolean }) {
  if (v.kind === 'empty') return null
  const text = (() => {
    switch (v.kind) {
      case 'prefixed': return '带域前缀，原样发给指定的域，不走路由。'
      case 'catalog': return '上游目录里有这个模型，直通，不走路由。'
      case 'route': return `命中第 ${v.index + 1} 条，改用 ${v.target || '（目标还没填）'}。`
      case 'none': return catalogReady ? '目录里没有，也没有规则命中：会报「模型不存在」。' : '模型目录还没取到，判断不了是否在目录里；没有规则命中。'
    }
  })()
  const tone = v.kind === 'none' ? 'text-warning' : v.kind === 'route' ? 'text-success' : 'text-muted'
  return <p className={`-mt-1 text-sm ${tone}`}>{text}</p>
}
