// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 对外隐藏的模型（config.json 的 models.hidden）：/v1/models 不列，点名调用回 400 model_unavailable。
// 加一项、删一项都立即保存并热生效；归一口径与网关一致（lib/models.ts 的 hiddenModelSet）。
import { useMemo, useState } from 'react'
import { Button, Tag, TagGroup, toast } from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { post } from '../../lib/api'
import { toastError } from '../../lib/hooks'
import { addHiddenModel, hiddenFromConfig } from '../../lib/models'
import { qk, useConfig, useModels } from '../../lib/queries'
import type { SaveConfigResp } from '../../lib/types'
import { Loaded } from '../../components/Feedback'
import { ModelComboBox } from '../../components/ModelComboBox'
import { InfoTip, Panel } from '../../components/Panel'

export function HiddenModels() {
  const query = useConfig()
  return (
    <Panel
      title="对外隐藏的模型"
      desc="这里的模型不出现在 /v1/models 里，点名调用回 400 model_unavailable。"
      actions={
        <InfoTip label="匹配规则">
          {'完整产品／地区前缀只隐藏对应上游，例如 qoder:cn:模型名。cn: / global: 和不带前缀的名字继续只作用于 WorkBuddy；裸名同时隐藏其两个地区。\n精确匹配、区分大小写，不认通配。\n按模型路由改写后的完整模型名判断，和客户端密钥白名单叠加。\n加一项、删一项都立即保存生效。'}
        </InfoTip>
      }
      contentClassName="flex flex-col gap-3"
    >
      <Loaded query={query} rows={1}>
        {(d) => <HiddenForm saved={hiddenFromConfig(d.config)} />}
      </Loaded>
    </Panel>
  )
}

function HiddenForm({ saved }: { saved: string[] }) {
  const qc = useQueryClient()
  const models = useModels()
  const ids = useMemo(() => (models.data?.models || []).map((m) => m.id), [models.data])
  const [list, setList] = useState(saved)
  const [text, setText] = useState('')
  const [saving, setSaving] = useState(false)

  const save = async (next: string[]) => {
    setSaving(true)
    try {
      await post<SaveConfigResp>('config', { models: { hidden: next } })
      toast.success(next.length ? `已保存，${next.length} 项对外隐藏` : '已清空隐藏名单')
      setList(next)
      setText('')
      void qc.invalidateQueries({ queryKey: qk.config })
    } catch (e) {
      toastError(e, '保存失败：')
    } finally {
      setSaving(false)
    }
  }
  const add = (raw: string) => {
    const next = addHiddenModel(list, raw)
    if (next !== list) void save(next)
  }

  return (
    <>
      {list.length === 0 ? (
        <p className="text-sm text-muted">没有隐藏的模型：目录里的模型都对外列出、都能调用。</p>
      ) : (
        <TagGroup aria-label="对外隐藏的模型" disabledKeys={saving ? list : []} onRemove={(keys) => void save(list.filter((m) => !keys.has(m)))}>
          <TagGroup.List items={list.map((m) => ({ id: m }))}>
            {(m) => <Tag id={m.id} textValue={m.id} className="font-mono">{m.id}</Tag>}
          </TagGroup.List>
        </TagGroup>
      )}
      <div className="flex flex-wrap items-end gap-2">
        <ModelComboBox
          aria-label="添加要隐藏的模型"
          ids={ids}
          inputValue={text}
          onInputChange={setText}
          placeholder="从目录选完整模型名，或手填 WorkBuddy 裸名"
          value={null}
          onChange={(k) => { if (k != null) add(String(k)) }}
          isDisabled={saving}
          className="min-w-56 flex-1 sm:max-w-96"
        />
        <Button size="sm" variant="secondary" isDisabled={!text.trim() || saving} onPress={() => add(text)} className="mb-0.5">隐藏</Button>
      </div>
    </>
  )
}
