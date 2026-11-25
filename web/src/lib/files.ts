// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 文件拖拽与选择时的 accept 规则校验（浏览器原生 onDrop 不会自动按 input accept 过滤）。

/** 判断文件是否符合 HTML input accept 规则（支持扩展名 .json、通配 MIME text/* 与精确 MIME） */
export function matchesAccept(file: { name: string; type: string }, accept: string): boolean {
  const rules = accept.split(',').map((s) => s.trim().toLowerCase()).filter(Boolean)
  if (rules.length === 0) return true
  const name = file.name.toLowerCase()
  const type = file.type.toLowerCase()
  return rules.some((rule) => {
    if (rule === '*/*') return true
    if (rule.startsWith('.')) return name.endsWith(rule)
    if (rule.endsWith('/*')) return type.startsWith(rule.slice(0, -1))
    return type !== '' && type === rule
  })
}

/** 把 accept 规则提取成简短可读提示（优先展示扩展名，如 ".json"） */
export function formatAcceptHint(accept: string): string {
  const rules = accept.split(',').map((s) => s.trim()).filter(Boolean)
  const exts = rules.filter((r) => r.startsWith('.'))
  return (exts.length > 0 ? exts : rules).join(' / ')
}
