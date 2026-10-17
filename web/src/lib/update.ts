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
