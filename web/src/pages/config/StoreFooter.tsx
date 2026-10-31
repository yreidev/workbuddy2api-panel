// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 配置页「日志留存」每张卡片的页脚：这类日志现在占多少、最早到哪天，写盘出过什么错，能清空（二次确认）。
import { useState } from 'react'
import { Button, Card, Skeleton, toast } from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { post } from '../../lib/api'
import { toastError } from '../../lib/hooks'
import { qk } from '../../lib/queries'
import { CLEAR_NOTE, STORE_LABEL, storageProblem, storageSummary, type StoreKind } from '../../lib/storage'
import type { LogStorage } from '../../lib/types'
import { useConfirm } from '../../components/useConfirm'

/** 清空后要重查的：占用本身，以及读这类数据的页面 */
const AFFECTED: Record<StoreKind, readonly (readonly string[])[]> = {
  requests: [['request_metrics']],
  task: [['logs_archive']],
  system: [['logs_archive']],
  usage: [['usage'], qk.keys],
  credit: [['credit_log']],
}

export function StoreFooter({ kind, storage, loading }: { kind: StoreKind; storage?: LogStorage; loading: boolean }) {
  const qc = useQueryClient()
  const [confirmEl, ask] = useConfirm()
  const [busy, setBusy] = useState(false)
  const st = storage?.[kind]
  const problem = storageProblem(st)
  const empty = !st || !('buckets' in st ? st.buckets : st.files)

  const clear = async () => {
    setBusy(true)
    try {
      await post('log_storage/' + kind + '/clear')
      toast.success('已清空' + STORE_LABEL[kind])
    } catch (e) {
      toastError(e, '清空失败：')
    } finally {
      setBusy(false)
      void qc.invalidateQueries({ queryKey: qk.logStorage })
      for (const key of AFFECTED[kind]) void qc.invalidateQueries({ queryKey: key })
    }
  }

  return (
    <Card.Footer className="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-separator pt-3 text-sm">
      {loading && !storage ? <Skeleton className="h-4 w-48 rounded" /> : (
        <span className="text-muted">现有：{storageSummary(kind, st)}</span>
      )}
      {problem && <span className="text-warning">{problem}</span>}
      <Button
        size="sm"
        variant="danger-soft"
        className="ml-auto"
        isDisabled={empty || busy}
        onPress={() => ask({
          title: `清空${STORE_LABEL[kind]}？`,
          body: CLEAR_NOTE[kind] + '删掉的找不回来。',
          confirmLabel: '清空',
          onConfirm: () => void clear(),
        })}
      >
        {busy ? '清空中…' : '清空'}
      </Button>
      {confirmEl}
    </Card.Footer>
  )
}
