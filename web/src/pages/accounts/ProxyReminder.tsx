// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 代理提醒（账号池页、代理页共用）：代理连不上、已到期、快到期，号在等代理。没有要提醒的就不显示。
import { Alert, Button } from '@heroui/react'
import { useNavigate } from '@tanstack/react-router'
import { warningLines } from '../../lib/proxies'
import type { ProxyWarnings } from '../../lib/types'

export function ProxyReminder({ warnings, link = true }: { warnings?: ProxyWarnings | null; link?: boolean }) {
  const navigate = useNavigate()
  const lines = warningLines(warnings)
  if (!lines.length) return null
  return (
    <Alert status="warning">
      <Alert.Indicator />
      <Alert.Content>
        <Alert.Title>代理要处理：{lines.join('，')}</Alert.Title>
        <Alert.Description>
          连不上、已到期的代理上绑的号先暂停出站（不罚号），代理恢复或续费改了到期日就放开；连续不可用超过设定的时间（默认 30 分钟），
          号改绑到同池能用的代理。等代理的号在池里加了代理或调高上限后自动补上。
        </Alert.Description>
      </Alert.Content>
      {link && <Button size="sm" variant="secondary" onPress={() => void navigate({ to: '/proxies' })}>去代理页</Button>}
    </Alert>
  )
}
