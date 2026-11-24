// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

'use strict'

const test = require('node:test')
const assert = require('node:assert/strict')
const { execFileSync } = require('node:child_process')
const { createHash } = require('node:crypto')
const { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readlinkSync, rmSync, writeFileSync } = require('node:fs')
const { tmpdir } = require('node:os')
const { join } = require('node:path')

const REPO_ROOT = join(__dirname, '..', '..')
const SCRIPT = join(REPO_ROOT, 'scripts', 'install-linux.sh')
const SERVICE_TEMPLATE = join(REPO_ROOT, 'deploy', 'systemd', 'workbuddy2api.service')

function runScript(args, env = {}) {
  return execFileSync('bash', [SCRIPT, ...args], {
    cwd: REPO_ROOT,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, ...env },
  })
}

test('install-linux.sh 语法合法且包含 AGPL-3.0 SPDX 声明头', () => {
  execFileSync('bash', ['-n', SCRIPT], { stdio: 'pipe' })
  const scriptHead = readFileSync(SCRIPT, 'utf8').split('\n').slice(0, 6).join('\n')
  assert.match(scriptHead, /^#!\/usr\/bin\/env bash\n/)
  assert.match(scriptHead, /# SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）/)
  assert.match(scriptHead, /# SPDX-License-Identifier: AGPL-3\.0-only/)
  assert.match(scriptHead, /https:\/\/github\.com\/yreidev\/workbuddy2api-panel/)

  const unit = readFileSync(SERVICE_TEMPLATE, 'utf8')
  assert.match(unit, /# SPDX-License-Identifier: AGPL-3\.0-only/)
  assert.match(unit, /^ExecStart=\/opt\/workbuddy2api\/wb2api -config \/opt\/workbuddy2api\/data\/config\.json$/m)
  assert.match(unit, /^Environment=HOME=\/opt\/workbuddy2api$/m)
  assert.match(unit, /^ProtectSystem=strict$/m)
  assert.doesNotMatch(unit, /^MemoryDenyWriteExecute=/m)
  assert.doesNotMatch(unit, /^NoExecPaths=/m)
})

test('dry-run 默认 latest 与指定版本/架构/参数渲染正确', () => {
  const outLatest = runScript(['--dry-run'], { WB2A_ARCH: 'x86_64' })
  assert.match(outLatest, /releases\/download\/latest\/wb2api-panel-linux-amd64\.tar\.gz/)
  assert.match(outLatest, /releases\/download\/latest\/checksums\.txt/)
  assert.match(outLatest, /\/opt\/workbuddy2api\/\.config -> \/opt\/workbuddy2api\/data\/qoder-umid\/sdk/)
  assert.match(outLatest, /^ProtectHome=true$/m)
  assert.match(outLatest, /^ReadWritePaths=\/opt\/workbuddy2api\/auths \/opt\/workbuddy2api\/data$/m)
  assert.match(outLatest, /grep api_key \/opt\/workbuddy2api\/data\/config\.json/)
  assert.doesNotMatch(outLatest, /MemoryDenyWriteExecute|NoExecPaths/)

  const outVer = runScript(
    ['--dry-run', '--version', '1.14.0-rc.1', '--dir', '/home/ops/wb2api', '--user', 'ops', '--service', 'wb2a.service', '--listen', '127.0.0.1:8080', '--proxy', 'http://127.0.0.1:7890'],
    { WB2A_ARCH: 'aarch64' },
  )
  assert.match(outVer, /releases\/download\/v1\.14\.0-rc\.1\/wb2api-panel-v1\.14\.0-rc\.1-linux-arm64\.tar\.gz/)
  assert.match(outVer, /releases\/download\/v1\.14\.0-rc\.1\/checksums\.txt/)
  assert.match(outVer, /^User=ops$/m)
  assert.match(outVer, /^ProtectHome=false$/m)
  assert.match(outVer, /^Environment=WB2A_LISTEN=127\.0\.0\.1:8080$/m)
  assert.match(outVer, /^Environment=HTTPS_PROXY=http:\/\/127\.0\.0\.1:7890$/m)
  assert.match(outVer, /^Environment=HTTP_PROXY=http:\/\/127\.0\.0\.1:7890$/m)
  assert.match(outVer, /^Environment=ALL_PROXY=http:\/\/127\.0\.0\.1:7890$/m)
  assert.match(outVer, /\/etc\/systemd\/system\/wb2a\.service/)

  const outRoot = runScript(['--dry-run', '--dir', '/root/wb2api'], { WB2A_ARCH: 'x86_64' })
  assert.match(outRoot, /^ProtectHome=false$/m)

  const outNoSystemd = runScript(['--dry-run', '--no-systemd'], { WB2A_ARCH: 'x86_64' })
  assert.match(outNoSystemd, /跳过（--no-systemd）/)
  assert.doesNotMatch(outNoSystemd, /\[Unit\]/)

  // systemd % specifier 转义：代理密码含 %40 与目录含 % 须转义为 %%
  const outPct = runScript(
    ['--dry-run', '--dir', '//home//ops/wb%20api///', '--proxy', 'http://user:p%40ss@127.0.0.1:7890'],
    { WB2A_ARCH: 'x86_64' },
  )
  assert.match(outPct, /^ProtectHome=false$/m)
  assert.match(outPct, /^WorkingDirectory=\/home\/ops\/wb%%20api$/m)
  assert.match(outPct, /^Environment=HOME=\/home\/ops\/wb%%20api$/m)
  assert.match(outPct, /^ExecStart=\/home\/ops\/wb%%20api\/wb2api -config \/home\/ops\/wb%%20api\/data\/config\.json$/m)
  assert.match(outPct, /^ReadWritePaths=\/home\/ops\/wb%%20api\/auths \/home\/ops\/wb%%20api\/data$/m)
  assert.match(outPct, /^Environment=HTTPS_PROXY=http:\/\/user:p%%40ss@127\.0\.0\.1:7890$/m)
  assert.match(outPct, /^Environment=HTTP_PROXY=http:\/\/user:p%%40ss@127\.0\.0\.1:7890$/m)
  assert.match(outPct, /^Environment=ALL_PROXY=http:\/\/user:p%%40ss@127\.0\.0\.1:7890$/m)
})

test('已有用户主组解析（User 与 Group 不同名时按 id -gn 解析）', (t) => {
  const work = mkdtempSync(join(tmpdir(), 'wb2a-group-test-'))
  t.after(() => rmSync(work, { recursive: true, force: true }))
  const binDir = join(work, 'bin')
  mkdirSync(binDir, { recursive: true })
  const realId = execFileSync('sh', ['-c', 'command -v id'], { encoding: 'utf8' }).trim()
  writeFileSync(
    join(binDir, 'id'),
    `#!/usr/bin/env bash
if [[ "$1" == "-gn" && "$2" == "nobody" ]]; then
  echo "nogroup"
  exit 0
fi
exec "${realId}" "$@"
`,
    { mode: 0o755 },
  )
  const out = runScript(['--dry-run', '--user', 'nobody'], { PATH: `${binDir}:${process.env.PATH}`, WB2A_ARCH: 'x86_64' })
  assert.match(out, /^User=nobody$/m)
  assert.match(out, /^Group=nogroup$/m)
  assert.match(out, /运行用户：nobody:nogroup/)

  const src = readFileSync(SCRIPT, 'utf8')
  assert.match(src, /useradd --system --user-group/)
  assert.match(src, /chown -R "\$\{RUN_USER\}:\$\{RUN_GROUP\}"/)
})

test('卸载 dry-run 与非法参数边界校验', () => {
  const keep = runScript(['--dry-run', '--uninstall', '--dir', '/opt/wb'])
  assert.match(keep, /保留 \/opt\/wb\/auths 与 \/opt\/wb\/data/)

  const purge = runScript(['--dry-run', '--uninstall', '--purge', '--dir', '/opt/wb'])
  assert.match(purge, /彻底删除 \/opt\/wb（含 auths\/ 与 data\/）/)

  const badCases = [
    ['--purge'],
    ['--version', '../evil'],
    ['--dir', 'relative/path'],
    ['--dir', '/'],
    ['--dir', '///'],
    ['--dir', '/opt/foo/../..'],
    ['--dir', '/opt/..'],
    ['--dir', '/opt/./wb'],
    ['--dir', '/opt/bad dir'],
    ['--user', 'BadUser!'],
    ['--service', '../evil'],
    ['--listen', '127.0.0.1:7863\nExecStart=/bin/sh'],
    ['--proxy', 'http://127.0.0.1:7890\nBad=1'],
    ['--unknown-flag'],
  ]
  for (const args of badCases) {
    assert.throws(() => runScript(['--dry-run', ...args], { WB2A_ARCH: 'x86_64' }), `应拒绝非法参数: ${args.join(' ')}`)
  }
  assert.throws(() => runScript(['--dry-run'], { WB2A_ARCH: 'mips64' }), /暂不支持的 Linux 架构/)
})

test('离线 --no-systemd 完整安装、SHA-256 校验、原地升级保数据与卸载', (t) => {
  const work = mkdtempSync(join(tmpdir(), 'wb2a-install-test-'))
  t.after(() => rmSync(work, { recursive: true, force: true }))

  const binDir = join(work, 'bin')
  const serveDir = join(work, 'serve')
  const pkgSrc = join(work, 'pkg')
  const installDir = join(work, 'opt', 'wb2api')
  mkdirSync(binDir, { recursive: true })
  mkdirSync(serveDir, { recursive: true })
  mkdirSync(join(pkgSrc, 'internal', 'providers', 'cosy'), { recursive: true })

  writeFileSync(join(pkgSrc, 'wb2api'), '#!/bin/sh\necho v1\n', { mode: 0o755 })
  writeFileSync(join(pkgSrc, 'LICENSE'), 'AGPL-3.0\n')
  writeFileSync(join(pkgSrc, 'NOTICE'), 'NOTICE\n')
  writeFileSync(join(pkgSrc, 'README.md'), 'README\n')
  writeFileSync(join(pkgSrc, 'config.example.json'), '{}\n')
  writeFileSync(join(pkgSrc, 'internal', 'providers', 'NOTICE'), 'providers notice\n')
  writeFileSync(join(pkgSrc, 'internal', 'providers', 'cosy', 'NOTICE'), 'cosy notice\n')

  const archiveName = 'wb2api-panel-v1.14.0-linux-amd64.tar.gz'
  const archivePath = join(serveDir, archiveName)
  execFileSync('tar', ['-czf', archivePath, '-C', pkgSrc, 'wb2api', 'LICENSE', 'NOTICE', 'README.md', 'config.example.json', 'internal/providers/NOTICE', 'internal/providers/cosy/NOTICE'])
  const sha = createHash('sha256').update(readFileSync(archivePath)).digest('hex')
  writeFileSync(join(serveDir, 'checksums.txt'), `${sha}  ${archiveName}\n`)

  // 伪造离线 curl：按 URL 末尾文件名从 serveDir 复制
  const fakeCurl = join(binDir, 'curl')
  writeFileSync(
    fakeCurl,
    `#!/usr/bin/env bash
set -euo pipefail
out=""
url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -x|--retry|--connect-timeout) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
name="\${url##*/}"
cp "${serveDir}/\${name}" "$out"
`,
    { mode: 0o755 },
  )
  const env = { PATH: `${binDir}:${process.env.PATH}`, WB2A_ARCH: 'x86_64' }

  // 1. 首次安装
  const installOut = runScript(['--version', '1.14.0', '--dir', installDir, '--no-systemd'], env)
  assert.match(installOut, /SHA-256 校验通过/)
  assert.match(installOut, /grep api_key/)
  assert.equal(existsSync(join(installDir, 'wb2api')), true)
  assert.equal(existsSync(join(installDir, 'LICENSE')), true)
  assert.equal(existsSync(join(installDir, 'NOTICE')), true)
  assert.equal(existsSync(join(installDir, 'internal', 'providers', 'NOTICE')), true)
  assert.equal(existsSync(join(installDir, 'internal', 'providers', 'cosy', 'NOTICE')), true)
  assert.equal(existsSync(join(installDir, 'data', 'qoder-umid', 'sdk')), true)
  assert.equal(lstatSync(join(installDir, '.config')).isSymbolicLink(), true)
  assert.equal(readlinkSync(join(installDir, '.config')), join(installDir, 'data', 'qoder-umid', 'sdk'))

  // 写入模拟用户数据，验证原地升级不触碰数据
  writeFileSync(join(installDir, 'data', 'config.json'), '{"api_key":"sk-secret-do-not-print"}\n')
  writeFileSync(join(installDir, 'auths', 'workbuddy-1.json'), '{"uid":"1"}\n')

  // 2. 篡改 checksums.txt 必须拒绝安装
  writeFileSync(join(serveDir, 'checksums.txt'), `${'0'.repeat(64)}  ${archiveName}\n`)
  assert.throws(() => runScript(['--version', '1.14.0', '--dir', installDir, '--no-systemd'], env), /SHA-256 校验失败/)

  // 3. 恢复正确校验并升级，确认输出不泄露明文密钥、数据保留
  writeFileSync(join(pkgSrc, 'wb2api'), '#!/bin/sh\necho v2\n', { mode: 0o755 })
  execFileSync('tar', ['-czf', archivePath, '-C', pkgSrc, 'wb2api', 'LICENSE', 'NOTICE', 'README.md', 'config.example.json', 'internal/providers/NOTICE', 'internal/providers/cosy/NOTICE'])
  const sha2 = createHash('sha256').update(readFileSync(archivePath)).digest('hex')
  writeFileSync(join(serveDir, 'checksums.txt'), `${sha2}  ${archiveName}\n`)

  const upgradeOut = runScript(['--version', '1.14.0', '--dir', installDir, '--no-systemd'], env)
  assert.doesNotMatch(upgradeOut, /sk-secret-do-not-print/)
  assert.match(readFileSync(join(installDir, 'wb2api'), 'utf8'), /echo v2/)
  assert.match(readFileSync(join(installDir, 'data', 'config.json'), 'utf8'), /sk-secret-do-not-print/)
  assert.equal(existsSync(join(installDir, 'auths', 'workbuddy-1.json')), true)

  // 4. 默认卸载保留 auths/ 和 data/
  runScript(['--uninstall', '--dir', installDir, '--no-systemd'], env)
  assert.equal(existsSync(join(installDir, 'wb2api')), false)
  assert.equal(existsSync(join(installDir, 'LICENSE')), false)
  assert.equal(existsSync(join(installDir, 'data', 'config.json')), true)
  assert.equal(existsSync(join(installDir, 'auths', 'workbuddy-1.json')), true)

  // 5. --uninstall --purge 彻底删除
  runScript(['--uninstall', '--purge', '--dir', installDir, '--no-systemd'], env)
  assert.equal(existsSync(installDir), false)
})
