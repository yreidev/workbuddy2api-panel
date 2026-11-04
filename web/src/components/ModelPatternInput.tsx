// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 往模型名单里加一项：从模型目录选，或手填（支持 * 通配）。密钥的模型白名单、账号不接的模型共用。
import { useMemo, useState } from 'react'
import { Button } from '@heroui/react'
import { addModelPattern, bareModelIds } from '../lib/keys'
import { useModels } from '../lib/queries'
import { ModelComboBox } from './ModelComboBox'

/** value 现有的名单；onChange 收加了一项以后的新名单（重复的不加）；isDisabled 保存中等情况不让加 */
export function ModelPatternInput({ value, onChange, isDisabled }: { value: string[]; onChange: (v: string[]) => void; isDisabled?: boolean }) {
  const models = useModels()
  const catalog = useMemo(() => bareModelIds((models.data?.models || []).map((m) => m.id)), [models.data])
  const [text, setText] = useState('')
  const [hint, setHint] = useState('')
  const add = (raw: string) => {
    const next = addModelPattern(value, raw)
    if (typeof next === 'string') {
      setHint(next)
      return
    }
    setHint('')
    setText('')
    if (next !== value) onChange(next)
  }
  return (
    <>
      <div className="flex flex-wrap items-end gap-2">
        <ModelComboBox
          aria-label="添加模型"
          ids={catalog}
          inputValue={text}
          onInputChange={(v) => {
            setText(v)
            setHint('')
          }}
          placeholder="从目录选，或手填 glm-*"
          value={null}
          onChange={(k) => { if (k != null) add(String(k)) }}
          isDisabled={isDisabled}
          className="min-w-56 flex-1"
        />
        <Button size="sm" variant="secondary" isDisabled={isDisabled || !text.trim()} onPress={() => add(text)} className="mb-0.5">添加</Button>
      </div>
      {hint && <span className="text-xs text-danger">{hint}</span>}
    </>
  )
}
