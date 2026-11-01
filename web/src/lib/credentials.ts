// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 凭证导出：后端按 uid 返回与数据目录 workbuddy-<uid>.json 同形的凭证文档，这里拼成要下载的文件。
// 单个账号 → workbuddy-<uid>.json（一个对象，和数据目录里的文件一模一样）；多个 → 一个数组文件。
// 两种文件面板「添加账号 → 导入」都认，企业 ID、域、设备 token 不丢。
import { post } from './api'

export interface CredentialFile {
  name: string
  text: string
}

const pad = (n: number) => String(n).padStart(2, '0')

/** 本地时间 20261019-1530，批量文件名用 */
export const stamp = (d: Date) => `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}`

export function credentialFile(docs: unknown[], now: Date): CredentialFile {
  if (docs.length === 1) {
    const uid = (docs[0] as { account?: { uid?: string } }).account?.uid || 'account'
    return { name: `workbuddy-${uid}.json`, text: JSON.stringify(docs[0], null, 2) + '\n' }
  }
  return { name: `workbuddy-accounts-${stamp(now)}-${docs.length}.json`, text: JSON.stringify(docs, null, 2) + '\n' }
}

/** 把一段文本存成文件下载（凭证、代理导出共用） */
export function download({ name, text }: CredentialFile, type = 'application/json') {
  const url = URL.createObjectURL(new Blob([text], { type }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  // 立刻 revoke 个别浏览器会来不及开始下载
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/** 导出这些账号的凭证并触发下载，返回导出的账号数 */
export async function exportCredentials(uids: string[]): Promise<number> {
  const { accounts } = await post<{ accounts: unknown[] }>('accounts/export', { uids })
  download(credentialFile(accounts, new Date()))
  return accounts.length
}
