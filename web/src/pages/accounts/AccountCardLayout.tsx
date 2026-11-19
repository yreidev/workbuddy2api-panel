// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import type { ReactNode } from 'react'
import { ChevronRight } from '@gravity-ui/icons'
import { Button, Card, Separator } from '@heroui/react'
import { SelectionCheckbox } from './ExportBar'

export function AccountCardLayout({ name, heading, status, ring, selected, onSelect, onOpen, children, actions }: {
  name: string
  heading: ReactNode
  status: ReactNode
  ring: string
  selected: boolean
  onSelect: (value: boolean) => void
  onOpen: () => void
  children: ReactNode
  actions: ReactNode
}) {
  return (
    <Card variant="secondary" className={`gap-3 p-4 ${ring}`}>
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-start gap-2">
          <SelectionCheckbox label={'选择 ' + name} isSelected={selected} onChange={onSelect} />
          {heading}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {status}
          <Button isIconOnly size="sm" variant="ghost" aria-label="详情" onPress={onOpen}><ChevronRight /></Button>
        </div>
      </div>
      {children}
      <div className="mt-auto flex flex-col gap-2">
        <Separator />
        <div className="-mx-1">{actions}</div>
      </div>
    </Card>
  )
}
