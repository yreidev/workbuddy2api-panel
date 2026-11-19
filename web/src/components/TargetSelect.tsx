// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { Label, ListBox, Select } from '@heroui/react'
import { TARGETS } from '../lib/targets'

export function TargetSelect({ label, value, onChange, targets, compact = false }: { label: string; value: string; onChange: (value: string) => void; targets: typeof TARGETS; compact?: boolean }) {
  return (
    <Select className={compact ? "w-full sm:w-40" : undefined} value={value || null} onChange={(v) => { if (v != null) onChange(String(v)) }} placeholder="选择上游">
      <Label className={compact ? "sr-only" : undefined}>{label}</Label>
      <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
      <Select.Popover><ListBox>{targets.map((t) => <ListBox.Item key={t.id} id={t.id} textValue={t.name}>{t.name}<ListBox.ItemIndicator /></ListBox.Item>)}</ListBox></Select.Popover>
    </Select>
  )
}
