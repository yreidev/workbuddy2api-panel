import { describe, expect, it } from 'vitest'
import { inlineParts, pickNotice, restartOutcome, shouldRecordSeen, updateBusy } from './update'
import type { Release, SelfUpdateStatus, VersionResp } from './types'

const rel = (version: string): Release => ({ version, groups: [{ title: '新增', items: ['x'] }] })
const resp = (over: Partial<VersionResp> = {}): VersionResp => ({
  version: '1.1.0', update_check: true, update_available: false, updates: null, whats_new: null, ...over,
})

describe('pickNotice', () => {
  it('升级后先弹本次更新', () => {
    const d = resp({ whats_new: [rel('1.1.0')], update_available: true, latest: '1.2.0', updates: [rel('1.2.0')] })
    expect(pickNotice(d, '1.0.0', null)).toBe('whatsnew')
    // 看完本次更新（记下当前版本）后轮到新版本提醒
    expect(pickNotice(d, '1.1.0', null)).toBe('update')
  })
  it('新版本提醒每个版本只弹一次，出了更新的版本再弹', () => {
    const d = resp({ update_available: true, latest: '1.2.0', updates: [rel('1.2.0')] })
    expect(pickNotice(d, '1.1.0', null)).toBe('update')
    expect(pickNotice(d, '1.1.0', '1.2.0')).toBeNull()
    expect(pickNotice({ ...d, latest: '1.3.0' }, '1.1.0', '1.2.0')).toBe('update')
  })
  it('没有记录时也弹一次本次更新（老用户分不出是新装还是升级）', () => {
    expect(pickNotice(resp({ whats_new: [rel('1.1.0')] }), null, null)).toBe('whatsnew')
  })
  it('没有新版本、也没有本次更新时不弹', () => {
    expect(pickNotice(resp(), '1.1.0', null)).toBeNull()
    expect(pickNotice(resp(), null, null)).toBeNull()
  })
})

describe('shouldRecordSeen', () => {
  it('降级或没有新条目时直接记下当前版本', () => {
    expect(shouldRecordSeen(resp(), null)).toBe(true)
    expect(shouldRecordSeen(resp(), '1.2.0')).toBe(true)
    expect(shouldRecordSeen(resp(), '1.1.0')).toBe(false)
  })
  it('有本次更新要看时等用户关掉弹窗再记', () => {
    expect(shouldRecordSeen(resp({ whats_new: [rel('1.1.0')] }), '1.0.0')).toBe(false)
    expect(shouldRecordSeen(resp({ whats_new: [rel('1.1.0')] }), null)).toBe(false)
  })
})

describe('inlineParts', () => {
  const base = 'https://github.com/o/r/blob/main/docs/'
  it('拆出行内代码与链接，相对链接按 docs/ 补全', () => {
    expect(inlineParts('改 `panel.update_check` 见[文档](https://example.com/a)和[本地](upstream.md#同步记录)。', base)).toEqual([
      { kind: 'text', text: '改 ' },
      { kind: 'code', text: 'panel.update_check' },
      { kind: 'text', text: ' 见' },
      { kind: 'link', text: '文档', href: 'https://example.com/a' },
      { kind: 'text', text: '和' },
      { kind: 'link', text: '本地', href: base + 'upstream.md#%E5%90%8C%E6%AD%A5%E8%AE%B0%E5%BD%95' },
      { kind: 'text', text: '。' },
    ])
  })
  it('不是 https 的链接只留文字', () => {
    expect(inlineParts('[脚本](javascript:void)', base)).toEqual([{ kind: 'text', text: '脚本' }])
    expect(inlineParts('[明文](http://example.com)', base)).toEqual([{ kind: 'text', text: '明文' }])
  })
  it('没有标记时原样返回', () => {
    expect(inlineParts('纯文字', base)).toEqual([{ kind: 'text', text: '纯文字' }])
    expect(inlineParts('', base)).toEqual([])
  })
})

const su = (over: Partial<SelfUpdateStatus> = {}): SelfUpdateStatus => ({
  supported: true, base: '1.1.0', override: false, phase: 'idle', grace_sec: 5, ...over,
})

describe('updateBusy', () => {
  it('下载、校验、重启时算进行中', () => {
    expect(['downloading', 'verifying', 'restarting'].map((phase) => updateBusy(su({ phase } as Partial<SelfUpdateStatus>)))).toEqual([true, true, true])
    expect(updateBusy(su())).toBe(false)
    expect(updateBusy(su({ phase: 'failed' }))).toBe(false)
    expect(updateBusy(undefined)).toBe(false)
  })
})

describe('restartOutcome', () => {
  const pending = { kind: 'update' as const, target: '1.2.0', startedAt: 0 }
  it('网关回来且是目标版本：完成', () => {
    expect(restartOutcome(pending, resp({ version: '1.2.0', self_update: su({ override: true }) }))).toBe('done')
  })
  it('还在下载、重启中或连不上：继续等', () => {
    expect(restartOutcome(pending, undefined)).toBe('waiting')
    expect(restartOutcome(pending, resp({ self_update: su({ phase: 'downloading', target: '1.2.0' }) }))).toBe('waiting')
    expect(restartOutcome(pending, resp({ self_update: su({ phase: 'restarting', target: '1.2.0' }) }))).toBe('waiting')
  })
  it('下载或校验失败', () => {
    expect(restartOutcome(pending, resp({ self_update: su({ phase: 'failed', error: 'x' }) }))).toBe('failed')
  })
  it('换了新进程却不是目标版本：新版没能启动', () => {
    expect(restartOutcome(pending, resp({ self_update: su({ rolled_back: 'v1.2.0 启动失败' }) }))).toBe('elsewhere')
  })
  it('退回自带版本', () => {
    const back = { kind: 'rollback' as const, target: '1.1.0', startedAt: 0 }
    expect(restartOutcome(back, resp({ version: '1.2.0', self_update: su({ override: true, phase: 'restarting', target: '1.1.0' }) }))).toBe('waiting')
    expect(restartOutcome(back, resp({ version: '1.1.0', self_update: su() }))).toBe('done')
  })
})
