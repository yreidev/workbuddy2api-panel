// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 可视化文件拖拽上传区：基于 HeroUI Surface + HTML5 Drag & Drop + motion 拖拽反馈，
// 替代裸文件选择按钮；仅响应文件拖拽并按 accept 规则校验扩展名/MIME。
import { useRef, useState, type DragEvent } from 'react'
import { CloudArrowUpIn, FileCheck } from '@gravity-ui/icons'
import * as m from 'motion/react-m'
import { Chip, Surface } from '@heroui/react'
import { formatAcceptHint, matchesAccept } from '../lib/files'
import { SPRING_SNAPPY } from '../lib/motion'
import { BusyButton } from './Feedback'

function hasDraggedFiles(e: DragEvent): boolean {
  return Array.from(e.dataTransfer?.types ?? []).includes('Files')
}

export function FileDropZone({ accept, title, hint, buttonLabel, busy = false, onFile }: {
  accept: string
  title: string
  hint: string
  buttonLabel: string
  busy?: boolean
  onFile: (file: File) => void | Promise<void>
}) {
  const inputRef = useRef<HTMLInputElement>(null)
  const dragDepth = useRef(0)
  const [dragging, setDragging] = useState(false)
  const [pickedName, setPickedName] = useState('')
  const [rejectMsg, setRejectMsg] = useState('')

  const handleFile = async (file: File | undefined) => {
    if (!file || busy) return
    if (!matchesAccept(file, accept)) {
      setRejectMsg(`仅支持 ${formatAcceptHint(accept)} 文件，已忽略「${file.name}」`)
      if (inputRef.current) inputRef.current.value = ''
      return
    }
    setRejectMsg('')
    setPickedName(file.name)
    try {
      await onFile(file)
    } finally {
      if (inputRef.current) inputRef.current.value = ''
    }
  }

  const onDragEnter = (e: DragEvent<HTMLDivElement>) => {
    if (!hasDraggedFiles(e)) return
    e.preventDefault()
    e.stopPropagation()
    dragDepth.current += 1
    if (!busy) setDragging(true)
  }

  const onDragOver = (e: DragEvent<HTMLDivElement>) => {
    if (!hasDraggedFiles(e)) return
    e.preventDefault()
    e.stopPropagation()
  }

  const onDragLeave = (e: DragEvent<HTMLDivElement>) => {
    if (!hasDraggedFiles(e)) return
    e.preventDefault()
    e.stopPropagation()
    dragDepth.current = Math.max(0, dragDepth.current - 1)
    if (dragDepth.current === 0) setDragging(false)
  }

  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    if (!hasDraggedFiles(e)) return
    e.preventDefault()
    e.stopPropagation()
    dragDepth.current = 0
    setDragging(false)
    void handleFile(e.dataTransfer.files?.[0])
  }

  return (
    <Surface
      variant="secondary"
      onDragEnter={onDragEnter}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
      className={`flex flex-col items-center gap-2.5 rounded-2xl border border-dashed px-4 py-5 text-center transition-[border-color,background-color,box-shadow] duration-200 ${
        dragging
          ? 'border-accent bg-accent-soft/45 shadow-sm'
          : rejectMsg
            ? 'border-danger/60 hover:border-danger'
            : 'border-separator hover:border-foreground/25'
      }`}
    >
      <input
        ref={inputRef}
        type="file"
        accept={accept}
        hidden
        onChange={(e) => void handleFile(e.target.files?.[0])}
      />
      <m.div
        animate={{ scale: dragging ? 1.08 : 1, y: dragging ? -2 : 0 }}
        transition={SPRING_SNAPPY}
        className={`flex size-10 items-center justify-center rounded-xl transition-colors duration-200 ${
          dragging ? 'bg-accent text-accent-foreground' : 'bg-default text-muted'
        }`}
      >
        <CloudArrowUpIn className="size-5" />
      </m.div>
      <div className="flex flex-col gap-0.5">
        <p className="text-sm font-medium text-foreground">{dragging ? '松开即可读取文件' : title}</p>
        <p className="text-xs text-muted">{hint}</p>
      </div>
      <div className="flex flex-wrap items-center justify-center gap-2">
        <BusyButton size="sm" variant="secondary" busy={busy} onPress={() => inputRef.current?.click()}>
          {buttonLabel}
        </BusyButton>
        {pickedName && (
          <Chip size="sm" variant="soft" color="accent">
            <FileCheck className="mr-1 size-3.5" />
            {pickedName}
          </Chip>
        )}
      </div>
      {rejectMsg && <p role="alert" className="text-xs text-danger">{rejectMsg}</p>}
    </Surface>
  )
}
