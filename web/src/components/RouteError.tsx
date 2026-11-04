// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 页面渲染出错时显示（路由的 defaultErrorComponent）：错误、堆栈和版本、浏览器列成能复制的文字。
// 截图常常传不上来，有的环境一开调试工具就刷新（issue #6），反馈问题时复制这段最省事。
import { useState } from 'react'
import { Alert, Button, Surface, toast } from '@heroui/react'
import type { ErrorComponentProps } from '@tanstack/react-router'
import { copyText } from '../lib/clipboard'
import { toastError } from '../lib/hooks'
import { useOverview } from '../lib/queries'

export function RouteError({ error, info, reset }: ErrorComponentProps) {
  const { data } = useOverview()
  const [time] = useState(() => new Date().toISOString())
  const err = error instanceof Error ? error : new Error(String(error))
  const head = `${err.name}: ${err.message}`
  // Chrome 的 stack 第一行就是 head，Safari、Firefox 的不带
  const stack = (err.stack ?? '').replace(head + '\n', '').trimEnd()
  const report = [
    `版本：${data?.version ?? '未知'}`,
    `页面：${location.hash || '#/'}`,
    `浏览器：${navigator.userAgent}`,
    `时间：${time}`,
    '',
    head,
    stack,
    info?.componentStack ? '\n组件栈：' + info.componentStack : '',
  ].join('\n').trim()
  const copy = () => copyText(report).then(() => toast.success('已复制错误信息'), (e: unknown) => toastError(e, '复制失败：'))

  return (
    <div className="flex flex-col gap-3">
      <Alert status="danger">
        <Alert.Indicator />
        <Alert.Content>
          <Alert.Title>这一页出错了</Alert.Title>
          <Alert.Description>{err.message}</Alert.Description>
        </Alert.Content>
      </Alert>
      <p className="text-sm text-muted">反馈问题时点「复制错误信息」，把复制到的文字贴进 issue，不用截图。</p>
      <Surface<'pre'> variant="secondary" render={(props) => <pre {...props} />} className="max-h-96 overflow-auto rounded-lg px-3 py-2 font-mono text-xs break-all whitespace-pre-wrap">{report}</Surface>
      <div className="flex gap-2">
        <Button size="sm" onPress={() => void copy()}>复制错误信息</Button>
        <Button size="sm" variant="secondary" onPress={reset}>重试</Button>
      </div>
    </div>
  )
}
