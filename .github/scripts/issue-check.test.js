// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// issue-check.js 的测试：node --test .github/scripts/
'use strict'

const test = require('node:test')
const assert = require('node:assert/strict')
const check = require('./issue-check.js')

/** 照 GitHub 表单生成的正文格式拼（### 标题、空行、内容；render 的包代码块；选填没填是 _No response_） */
function formBody({ version = '1.13.0', deploy = 'Docker 镜像', accounts = '国际版企业号', logs = 'WARN: [upstream] global models: boom' } = {}) {
  return [
    '### 提交前确认', '', '- [X] 我搜过已有的 issue（含已关闭的），没有同样的问题', '',
    '### 版本号', '', version, '',
    '### 部署方式', '', deploy, '',
    '### 涉及的账号类型', '', accounts, '',
    '### 问题描述', '', '模型页打不开', '',
    '### 复现步骤', '', '_No response_', '',
    '### 报错原文和运行日志', '', '```text', logs, '```', '',
    '### 截图和其他补充', '', '_No response_', '',
  ].join('\r\n')
}

test('parseForm 按标题切分，去掉代码块，_No response_ 当空', () => {
  const f = check.parseForm(formBody({ logs: 'line1\n### 不是标题吗' }))
  assert.equal(f['版本号'], '1.13.0')
  assert.equal(f['复现步骤'], '')
  assert.equal(f['报错原文和运行日志'], 'line1')
  assert.deepEqual(check.parseForm(''), {})
  assert.deepEqual(check.parseForm('### 只有标题'), { 只有标题: '' })
})

test('missingFields', () => {
  assert.deepEqual(check.missingFields(formBody()), [])
  assert.deepEqual(check.missingFields(formBody({ version: 'v1.13.0' })), [])
  assert.deepEqual(check.missingFields(formBody({ logs: '没有：页面没报错，运行日志里也搜不到' })), [])

  const only = (body) => check.missingFields(body).map((h) => h.match(/^\*\*(.+?)\*\*/)[1])
  assert.deepEqual(only(formBody({ version: 'latest' })), ['版本号'])
  assert.deepEqual(only(formBody({ deploy: '_No response_', accounts: '_No response_' })), ['部署方式', '涉及的账号类型'])
  for (const logs of ['', '无', ' 没有 ', 'N/A', '-', '。']) {
    assert.deepEqual(only(formBody({ logs })), ['报错原文和运行日志'], `日志「${logs}」应算没填`)
  }
  // 不是用表单开的（没有这些标题）：四项都缺
  assert.deepEqual(only('模型页打不开'), ['版本号', '部署方式', '涉及的账号类型', '报错原文和运行日志'])
})

/** 假的 github 客户端：记下每次调用；comments 是 issue 已有的评论 */
function fakeGitHub(comments = []) {
  const calls = []
  const rec = (name) => async (args) => { calls.push([name, args]) }
  const listComments = async () => { throw new Error('应走 paginate') }
  return {
    calls,
    paginate: async (fn, args) => { assert.equal(fn, listComments); calls.push(['listComments', args]); return comments },
    rest: {
      issues: {
        listComments,
        createComment: rec('createComment'),
        updateComment: rec('updateComment'),
        addLabels: rec('addLabels'),
        removeLabel: rec('removeLabel'),
        update: rec('update'),
      },
    },
  }
}

const core = { info() {} }
const repo = { owner: 'yreidev', repo: 'workbuddy2api-panel' }

function issuesEvent({ action = 'opened', body = formBody(), labels = ['bug'], association = 'NONE', changes } = {}) {
  return {
    eventName: 'issues',
    repo,
    payload: {
      action,
      changes,
      issue: { number: 6, body, labels: labels.map((name) => ({ name })), user: { login: 'reporter' }, author_association: association },
    },
  }
}

const names = (calls) => calls.map(([n]) => n)
const BOT = { id: 99, user: { type: 'Bot' }, body: '<!-- issue-check -->\n还差下面几项' }

test('新开的缺信息：留评论、打待补充', async () => {
  const github = fakeGitHub([{ id: 1, user: { type: 'User' }, body: '<!-- issue-check --> 用户抄的' }])
  await check({ github, context: issuesEvent({ body: formBody({ version: 'latest' }) }), core })
  assert.deepEqual(names(github.calls), ['listComments', 'createComment', 'addLabels'])
  const body = github.calls[1][1].body
  assert.ok(body.startsWith('<!-- issue-check -->') && body.includes('**版本号**') && !body.includes('**部署方式**'))
  assert.deepEqual(github.calls[2][1], { ...repo, issue_number: 6, labels: ['待补充'] })
})

test('已经留过评论、已有待补充：原地更新，不重复打标签', async () => {
  const github = fakeGitHub([BOT])
  const context = issuesEvent({ action: 'edited', changes: { body: {} }, body: formBody({ logs: '无' }), labels: ['bug', '待补充'] })
  await check({ github, context, core })
  assert.deepEqual(names(github.calls), ['listComments', 'updateComment'])
  assert.equal(github.calls[1][1].comment_id, 99)
})

test('补齐了：评论改成信息齐了，摘掉待补充和即将关闭', async () => {
  const github = fakeGitHub([BOT])
  const context = issuesEvent({ action: 'edited', changes: { body: {} }, labels: ['bug', '待补充', '即将关闭'] })
  await check({ github, context, core })
  assert.deepEqual(names(github.calls), ['listComments', 'updateComment', 'removeLabel', 'removeLabel'])
  assert.match(github.calls[1][1].body, /信息齐了/)
  assert.deepEqual(github.calls.slice(2).map(([, a]) => a.name), ['待补充', '即将关闭'])
})

test('一开始就齐了：什么都不做', async () => {
  const github = fakeGitHub()
  await check({ github, context: issuesEvent(), core })
  assert.deepEqual(names(github.calls), ['listComments'])
})

test('不查的：只改了标题、不是问题反馈、维护者自己开的', async () => {
  for (const context of [
    issuesEvent({ action: 'edited', changes: { title: {} }, body: '' }),
    issuesEvent({ labels: ['enhancement'], body: '' }),
    issuesEvent({ association: 'OWNER', body: '' }),
  ]) {
    const github = fakeGitHub()
    await check({ github, context, core })
    assert.deepEqual(github.calls, [])
  }
})

function commentEvent({ by = 'reporter', labels = ['bug', '待补充'], state = 'open' } = {}) {
  return {
    eventName: 'issue_comment',
    repo,
    payload: {
      action: 'created',
      comment: { user: { login: by } },
      issue: { number: 6, state, labels: labels.map((name) => ({ name })), user: { login: 'reporter' } },
    },
  }
}

test('作者回复：摘掉待补充（有即将关闭也摘）', async () => {
  const github = fakeGitHub()
  await check({ github, context: commentEvent({ labels: ['bug', '待补充', '即将关闭'] }), core })
  assert.deepEqual(github.calls.map(([n, a]) => n + ':' + a.name), ['removeLabel:待补充', 'removeLabel:即将关闭'])
})

test('因为没补充被关掉后作者回复：重新打开再摘标签', async () => {
  const github = fakeGitHub()
  await check({ github, context: commentEvent({ state: 'closed', labels: ['bug', '待补充', '即将关闭'] }), core })
  assert.deepEqual(names(github.calls), ['update', 'removeLabel', 'removeLabel'])
  assert.deepEqual(github.calls[0][1], { ...repo, issue_number: 6, state: 'open' })
})

test('别人回复、或者没打待补充：不动', async () => {
  for (const context of [commentEvent({ by: 'someone', state: 'closed' }), commentEvent({ labels: ['bug'], state: 'closed' })]) {
    const github = fakeGitHub()
    await check({ github, context, core })
    assert.deepEqual(github.calls, [])
  }
})
