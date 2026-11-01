// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 凭证导出：账号表多选后的操作（已选 N 个 / 导出凭证 / 绑定代理 / 批量操作 / 取消选择）、勾选框，以及详情抽屉里的单个导出按钮。
// 宽屏操作放在搜索框旁；窄屏卡片列表很长，操作改放底部吸附条（同配置页的保存条），滚到哪都看得到。
import type { ReactNode } from 'react'
import { Button, Checkbox } from '@heroui/react'
import { BatchMenu } from './BatchMenu'
import { useExportCredentials } from './useExportCredentials'

/** 详情抽屉里导出单个账号 */
export function ExportOneButton({ uid }: { uid: string }) {
  const { busy, run } = useExportCredentials()
  return <Button size="sm" variant="ghost" isPending={busy} onPress={() => void run([uid])}>导出凭证</Button>
}

export function SelectionCheckbox({ label, isSelected, isIndeterminate, onChange, children }: {
  label: string
  isSelected: boolean
  isIndeterminate?: boolean
  onChange: (v: boolean) => void
  children?: ReactNode
}) {
  return (
    <Checkbox aria-label={children ? undefined : label} isSelected={isSelected} isIndeterminate={isIndeterminate} onChange={onChange}>
      <Checkbox.Content>
        <Checkbox.Control>
          <Checkbox.Indicator />
        </Checkbox.Control>
        {children}
      </Checkbox.Content>
    </Checkbox>
  )
}

/** 已选并且在当前筛选结果里的 uid 的操作；onBind 打开绑定代理的弹窗（没用代理池时不给）；nameOf 给批量操作的结果提示用 */
export function ExportActions({ selected, onClear, onBind, nameOf }: { selected: string[]; onClear: () => void; onBind?: () => void; nameOf: (uid: string) => string }) {
  const { busy, run } = useExportCredentials()
  return (
    <>
      <span className="text-sm text-muted">已选 {selected.length} 个</span>
      <Button size="sm" variant="secondary" isPending={busy} onPress={() => void run(selected)}>导出凭证</Button>
      {onBind && <Button size="sm" variant="secondary" onPress={onBind}>绑定代理</Button>}
      <BatchMenu selected={selected} nameOf={nameOf} onRemoved={onClear} />
      <Button size="sm" variant="tertiary" onPress={onClear}>取消选择</Button>
    </>
  )
}
