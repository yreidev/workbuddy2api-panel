// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 从模型目录选、也能手填的输入框：模型路由的目标模型、隐藏模型、模型名单共用。
// 按输入过滤由这里自己做、用 items 传给 ComboBox：React Aria 内置的过滤会把集合转成旧版 ListCollection，
// 那条路上游多次报过「childNodes is not supported」整页崩掉（issue #6，react-spectrum #6600、#9405）。
// 聚焦、点右侧按钮或按 ↓ 打开时列出全部，边打字边打开时才过滤，和内置行为一致（HeroUI 默认 menuTrigger="focus"）。
import { useMemo, useState, type ComponentProps } from 'react'
import { ComboBox, Input, Label, ListBox } from '@heroui/react'

type Props = Omit<ComponentProps<typeof ComboBox>, 'children' | 'items' | 'allowsCustomValue' | 'inputValue' | 'onInputChange' | 'onOpenChange'> & {
  ids: string[]
  inputValue: string
  onInputChange: (v: string) => void
  placeholder: string
  /** 显示在输入框上方；不传就只靠 aria-label */
  label?: string
}

export function ModelComboBox({ ids, inputValue, onInputChange, placeholder, label, ...rest }: Props) {
  const [showAll, setShowAll] = useState(false)
  const items = useMemo(() => {
    const q = inputValue.toLowerCase()
    return (showAll ? ids : ids.filter((id) => id.toLowerCase().includes(q))).map((id) => ({ id }))
  }, [ids, inputValue, showAll])
  return (
    <ComboBox
      {...rest}
      allowsCustomValue
      items={items}
      inputValue={inputValue}
      onInputChange={(v) => {
        setShowAll(false)
        onInputChange(v)
      }}
      onOpenChange={(open, trigger) => { if (open) setShowAll(trigger !== 'input') }}
    >
      {label && <Label>{label}</Label>}
      <ComboBox.InputGroup>
        <Input placeholder={placeholder} />
        <ComboBox.Trigger />
      </ComboBox.InputGroup>
      <ComboBox.Popover>
        <ListBox>
          {(m: { id: string }) => <ListBox.Item id={m.id} textValue={m.id}>{m.id}<ListBox.ItemIndicator /></ListBox.Item>}
        </ListBox>
      </ComboBox.Popover>
    </ComboBox>
  )
}
