// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

'use strict'

const test = require('node:test')
const assert = require('node:assert/strict')
const { execFileSync } = require('node:child_process')
const { mkdtempSync, readFileSync, rmSync, writeFileSync } = require('node:fs')
const { tmpdir } = require('node:os')
const { join } = require('node:path')

test('版本发布、滚动标签、只读事件和重跑使用同一策略', (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'release-policy-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const origin = join(dir, 'origin')
  const clone = join(dir, 'clone')
  const git = (args, input) => execFileSync('git', args, { encoding: 'utf8', input }).trim()
  git(['init', '--bare', '-q', origin])
  const tree = git(['-C', origin, 'hash-object', '-w', '-t', 'tree', '--stdin'], '')
  const identity = 'Fixture <fixture@example.invalid> 1000000000 +0000'
  const commit = (parent, message) => git(['-C', origin, 'hash-object', '-w', '-t', 'commit', '--stdin'],
    'tree ' + tree + '\n' + (parent ? 'parent ' + parent + '\n' : '') +
    'author ' + identity + '\ncommitter ' + identity + '\n\n' + message + '\n')
  const first = commit(null, 'first')
  const second = commit(first, 'second')
  const third = commit(second, 'third')
  const ref = (name, sha) => git(['-C', origin, 'update-ref', name, sha])
  ref('refs/heads/main', third)
  git(['clone', '-q', '-b', 'main', origin, clone])
  function check(version, sha, expected, event = 'push', branch = 'refs/heads/main', attempt = '1') {
    const output = join(dir, 'output')
    writeFileSync(output, '')
    execFileSync('bash', [join(__dirname, 'release-policy.sh'), version], {
      cwd: clone, stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, GITHUB_OUTPUT: output, GITHUB_EVENT_NAME: event, GITHUB_REF: branch, GITHUB_SHA: sha, GITHUB_RUN_ATTEMPT: attempt },
    })
    const result = Object.fromEntries(readFileSync(output, 'utf8').trim().split('\n').map((line) => line.split('=')))
    assert.deepEqual(result, { versioned: String(expected[0]), rolling: String(expected[1]), prerelease: String(version.includes('-')) }, [version, event, branch, attempt].join(' '))
  }
  check('1.14.0', first, [true, true])
  for (const v of ['1.14.0-alpha.1', '1.14.0-beta.2', '1.14.0-rc.1']) check(v, second, [true, false])
  for (const event of ['pull_request', 'workflow_dispatch']) {
    for (const v of ['1.14.0', '1.14.0-rc.1']) check(v, third, [false, false], event)
  }
  check('1.14.0', third, [false, false], 'push', 'refs/heads/topic')
  ref('refs/tags/latest', second)
  ref('refs/tags/v1.13.2', first)
  ref('refs/tags/v1.14.0-rc.1', second)
  check('1.13.2', first, [true, false])
  check('1.13.2', third, [false, true])
  check('1.14.0-rc.1', second, [true, false], 'push', 'refs/heads/main', '2')
  check('1.14.0-rc.1', third, [false, false])
  check('1.14.0', second, [true, false], 'push', 'refs/heads/main', '2')
  check('1.14.0', third, [true, true], 'push', 'refs/heads/main', '2')
  ref('refs/heads/main', first)
  check('1.14.0', third, [false, false])
})

test('模拟 gh 确认首发、重跑、草稿恢复和错误退出', (t) => {
  const { mkdirSync } = require('node:fs')
  const dir = mkdtempSync(join(tmpdir(), 'publish-version-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  mkdirSync(join(dir, 'dist'))
  for (const name of ['checksums.txt', 'package.zip']) writeFileSync(join(dir, 'dist', name), name)
  writeFileSync(join(dir, 'notes.md'), '测试')
  writeFileSync(join(dir, 'gh'), `#!/bin/sh
printf '%s\\n' "$*" >> "$CALLS"
case "$1 $2" in
  'api graphql') test "$STATE" != error || exit 22; echo "$STATE" ;;
  'release view') case "$*" in *targetCommitish*) echo "$TARGET";; *) printf '%s\\n' "$ASSETS";; esac ;;
esac
`, { mode: 0o700 })
  function run(state, { target = 'fixture', assets = 'checksums.txt\npackage.zip', pre = 'false', latest = 'true', ok = true } = {}) {
    const calls = join(dir, 'calls')
    writeFileSync(calls, '')
    const exec = () => execFileSync('bash', [join(__dirname, 'publish-version.sh'), pre === 'true' ? '1.14.0-rc.1' : '1.14.0', pre, latest], {
      cwd: dir, stdio: 'pipe',
      env: { ...process.env, PATH: dir + ':' + process.env.PATH, TARGET: target, STATE: state, ASSETS: assets,
        CALLS: calls, GITHUB_SHA: 'fixture', GITHUB_REPOSITORY: 'fixture/project' },
    })
    if (ok) exec(); else assert.throws(exec)
    return readFileSync(calls, 'utf8').split('\n').filter((line) => /^release (upload|edit|create) /.test(line))
  }
  assert.match(run('missing')[0], /release create v1\.14\.0 .*--prerelease=false --latest=true/)
  assert.match(run('missing', { pre: 'true', latest: 'false' })[0], /--prerelease=true --latest=false/)
  assert.deepEqual(run('published'), [])
  assert.match(run('published', { assets: 'checksums.txt' })[0], /release upload .*--clobber/)
  const draft = run('draft')
  assert.equal(draft.length, 2)
  assert.match(draft[0], /release upload .*--clobber/)
  assert.match(draft[1], /release edit .*--draft=false/)
  assert.deepEqual(run('draft', { target: 'other', ok: false }), [])
  assert.deepEqual(run('error', { ok: false }), [])
})

test('main 发布入口保留排队中的推送', () => {
  const workflow = readFileSync(join(__dirname, '../workflows/main-release.yml'), 'utf8')
  assert.match(workflow, /^  push:\n    branches: \[main\]$/m)
  assert.match(workflow, /^concurrency:\n  group: main-publications\n  queue: max$/m)
  assert.doesNotMatch(workflow, /cancel-in-progress: true/)
})
