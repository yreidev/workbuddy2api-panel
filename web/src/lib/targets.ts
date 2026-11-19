// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

export const TARGETS = [
  { id: 'workbuddy:cn', name: 'WorkBuddy 国内' },
  { id: 'workbuddy:global', name: 'WorkBuddy 国际' },
  { id: 'traework:cn', name: 'TraeWork 国内' },
  { id: 'traecode:cn', name: 'Trae Code 国内' },
  { id: 'qoder:cn', name: 'Qoder 国内' },
  { id: 'qoder:global', name: 'Qoder 国际' },
]
export const WORKBUDDY_TARGETS = ['workbuddy:cn', 'workbuddy:global']

/** 与后端 targets.Parse 一致，旧 cn/global 前缀始终属于 WorkBuddy。 */
export function parseModelTarget(id: string) {
  const target = TARGETS.find((t) => id.startsWith(t.id + ':'))?.id
    ?? (id.startsWith('cn:') ? 'workbuddy:cn' : id.startsWith('global:') ? 'workbuddy:global' : '')
  const prefix = target.startsWith('workbuddy:') && !id.startsWith('workbuddy:') ? target.slice(10) : target
  return { target, product: target.split(':')[0], region: target.split(':')[1] ?? '', native: target ? id.slice(prefix.length + 1) : id }
}

export const targetName = (target: string) => TARGETS.find((t) => t.id === target)?.name ?? target
