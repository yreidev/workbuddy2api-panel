// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 更新提醒：本浏览器的提醒记录（localStorage）、该弹哪个窗，以及更新日志条目里的行内标记。
// 记录读写失败（无痕模式等）按没记过处理：最多是同一条提醒在本次会话之外再弹一次。
import type { SelfUpdateStatus, VersionResp } from './types'

/**
 * 上次看过「本次更新」的版本。没有记录 = 新装，或从还没有更新提醒的旧版（1.13.0-panel 及更早）升上来，
 * 分不清是哪种，就都弹一次本机这一版的条目
 */
export const SEEN_VERSION_KEY = 'wb2api.seenVersion'
/** 已关掉提醒的最新版本：同一版本只弹一次，出了更新的版本才再弹 */
export const DISMISSED_UPDATE_KEY = 'wb2api.dismissedUpdate'

export function readStored(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

export function writeStored(key: string, value: string) {
  try {
    localStorage.setItem(key, value)
  } catch {
    // 拿不到存储：只在本次会话里记住
  }
}

export type NoticeMode = 'whatsnew' | 'update'

/** 该自动弹的窗：先「本次更新」，看完再「有新版本」；都看过返回 null */
export function pickNotice(d: VersionResp, seen: string | null, dismissed: string | null): NoticeMode | null {
  if (d.whats_new?.length && seen !== d.version) return 'whatsnew'
  if (d.update_available && d.latest && dismissed !== d.latest) return 'update'
  return null
}

/** 有本次更新要看时不动记录，等用户关掉弹窗；其余情况（降级、没有新条目）直接记下当前版本 */
export function shouldRecordSeen(d: VersionResp, seen: string | null): boolean {
  return seen !== d.version && !d.whats_new?.length
}

/** 版本号是否带预发布后缀（如 1.14.0-rc.2） */
export function isPrerelease(version?: string): boolean {
  return !!version && version.includes('-')
}

/** 对应目标版本的 GHCR 镜像标签：正式版推 latest，预发布版只推完整版本标签 */
export function dockerImageTag(version?: string): string {
  return version && isPrerelease(version) ? version : 'latest'
}

/** 一键更新正在下载、校验或重启 */
export function updateBusy(s?: SelfUpdateStatus | null): boolean {
  return s?.phase === 'downloading' || s?.phase === 'verifying' || s?.phase === 'restarting'
}

/** 本页发起、正在等结果的一键更新或退回 */
export interface PendingRestart {
  kind: 'update' | 'rollback'
  target: string
  startedAt: number
}

/**
 * 等重启的结果。发起时已把缓存里的阶段改成下载中 / 重启中，所以：版本变成目标版本 → done（刷新页面）；
 * 更新失败 → failed；阶段回到 idle（换了新进程）却不是目标版本 → elsewhere（新版没能启动，退回了自带版本）；
 * 其余（包括重启期间连不上，缓存停在 restarting）→ waiting。
 */
export function restartOutcome(p: PendingRestart, d?: VersionResp): 'waiting' | 'done' | 'failed' | 'elsewhere' {
  if (!d) return 'waiting'
  if (d.version === p.target) return 'done'
  const phase = d.self_update?.phase
  if (phase === 'failed') return 'failed'
  if (phase === 'idle' || phase === undefined) return 'elsewhere'
  return 'waiting'
}

export type UpdateStepKey = 'download' | 'verify' | 'restart'

export interface UpdateStep {
  key: UpdateStepKey
  label: string
  state: 'done' | 'active' | 'todo'
}

/** 各步在总进度里占的份额：下载按字节走；校验与重启量不出进度，做完一步填满一段 */
const STEP_WEIGHT: Record<UpdateStepKey, number> = { download: 70, verify: 15, restart: 15 }
const STEP_LABEL: Record<UpdateStepKey, string> = { download: '下载安装包', verify: '校验并试运行新程序', restart: '重启网关' }

/**
 * 一键更新 / 退回的进度：每一步的状态和总百分比。退回只有「重启网关」一步，量不出进度，percent 为 null。
 * 重启期间连不上网关，缓存停在 restarting，仍按重启这一步算。
 */
export function updateProgress(kind: PendingRestart['kind'], su?: SelfUpdateStatus | null): { steps: UpdateStep[]; percent: number | null } {
  const keys: UpdateStepKey[] = kind === 'rollback' ? ['restart'] : ['download', 'verify', 'restart']
  const phase = su?.phase
  const current: UpdateStepKey = kind === 'update' && phase === 'downloading' ? 'download'
    : kind === 'update' && phase === 'verifying' ? 'verify'
    : 'restart'
  const at = keys.indexOf(current)
  const steps = keys.map((key, i) => ({ key, label: STEP_LABEL[key], state: i < at ? 'done' as const : i === at ? 'active' as const : 'todo' as const }))
  if (kind === 'rollback') return { steps, percent: null }
  const finished = keys.slice(0, at).reduce((n, k) => n + STEP_WEIGHT[k], 0)
  const total = su?.total || 0
  const part = current === 'download' && total > 0 ? Math.min(1, (su?.done || 0) / total) : 0
  return { steps, percent: finished + STEP_WEIGHT[current] * part }
}

export type InlinePart = { kind: 'text' | 'code'; text: string } | { kind: 'link'; text: string; href: string }

/**
 * 更新日志条目只用到行内代码和链接两种标记。相对链接（指向 docs/ 里的其他文档）按 base 补全成 GitHub 上的地址，
 * 补全后不是 https 的链接只留文字。
 */
export function inlineParts(text: string, base: string): InlinePart[] {
  const out: InlinePart[] = []
  const re = /`([^`]+)`|\[([^\]]+)\]\(([^)\s]+)\)/g
  let last = 0
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m.index > last) out.push({ kind: 'text', text: text.slice(last, m.index) })
    if (m[1] !== undefined) out.push({ kind: 'code', text: m[1] })
    else {
      const href = resolveLink(m[3], base)
      out.push(href ? { kind: 'link', text: m[2], href } : { kind: 'text', text: m[2] })
    }
    last = re.lastIndex
  }
  if (last < text.length) out.push({ kind: 'text', text: text.slice(last) })
  return out
}

function resolveLink(href: string, base: string): string | null {
  try {
    const url = new URL(href, base)
    return url.protocol === 'https:' ? url.href : null
  } catch {
    return null
  }
}
