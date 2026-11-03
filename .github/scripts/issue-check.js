// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 问题反馈的信息检查，由 .github/workflows/issue-check.yml 调用（actions/github-script）。
//
// - 带 bug 标签（「问题反馈」表单自动打上）的 issue 新建或改了正文时，查版本号、部署方式、账号类型、
//   报错原文和运行日志；缺了就留一条机器人评论点名要补什么（只留一条，再查时原地更新），打「待补充」。
//   补齐后把那条评论改成「信息齐了」，摘掉「待补充」和「即将关闭」。维护者自己开的不查。
// - 作者在打了「待补充」的 issue 下回复：摘掉这两个标签，不再计时，等维护者看（补充的内容可能在回复里）；
//   已经因为没补充被关掉的，顺带重新打开。
// 「待补充」14 天没动静自动关闭由 .github/workflows/issue-stale.yml 负责。
// 维护者用法和改规则要一起动的地方见 docs/issues.md；CHECKS 的标题要和 1-bug.yml 的 label 一字不差。
'use strict'

const NEED_INFO = '待补充'
const CLOSING = '即将关闭'
const MARKER = '<!-- issue-check -->'
const MAINTAINERS = new Set(['OWNER', 'MEMBER', 'COLLABORATOR'])

/** 表单生成的正文按「### 标题」切成 {标题: 内容}：去掉 render 加的代码块，选填没填的 _No response_ 当空 */
function parseForm(body) {
  const out = {}
  const parts = (body || '').replace(/\r\n/g, '\n').split(/^### /m).slice(1)
  for (const p of parts) {
    const i = p.indexOf('\n')
    const title = (i < 0 ? p : p.slice(0, i)).trim()
    let value = (i < 0 ? '' : p.slice(i + 1)).trim()
    value = value.replace(/^```[^\n]*\n?/, '').replace(/\n?```$/, '').trim()
    out[title] = value === '_No response_' ? '' : value
  }
  return out
}

/** 只写了「无」「没有」「N/A」这类、没说原因的，等于没填 */
const bare = (v) => /^(无|没有|暂无|略|none|no|n\/?a|null|nil|-+|\.+|。+)?$/i.test(v.replace(/\s+/g, ''))

const CHECKS = [
  {
    title: '版本号',
    ok: (v) => /\d+\.\d+\.\d+/.test(v),
    hint: '**版本号**：面板左上角标题旁显示的版本（比如 1.13.0），或者 `wb2api -version` 的输出；填 latest 查不了',
  },
  {
    title: '部署方式',
    ok: (v) => v !== '',
    hint: '**部署方式**：Docker 镜像、Docker Compose、单文件二进制还是从源码构建',
  },
  {
    title: '涉及的账号类型',
    ok: (v) => v !== '',
    hint: '**涉及的账号类型**：出问题的号是国内版还是国际版、个人号还是企业号',
  },
  {
    title: '报错原文和运行日志',
    ok: (v) => !bare(v),
    hint: '**报错原文和运行日志**：页面报错的整段文字，面板「运行日志」页出问题那段时间的相关几行；截图常常传不上来，请贴文字。确实没有就写「没有」并说一下原因',
  },
]

/** 缺的项的提示，按表单顺序；都齐了返回空数组 */
function missingFields(body) {
  const form = parseForm(body)
  return CHECKS.filter((c) => !c.ok(form[c.title] ?? '')).map((c) => c.hint)
}

const askBody = (missing) => [
  MARKER,
  '还差下面几项，补上才好查：',
  '',
  ...missing.map((h) => '- ' + h),
  '',
  '直接编辑上面的正文补上，或者在下面回复都行。14 天没动静会自动关闭。',
].join('\n')

const DONE_BODY = MARKER + '\n信息齐了，谢谢补充，等维护者来看。'

module.exports = async ({ github, context, core }) => {
  const { issue, comment } = context.payload
  const ref = { owner: context.repo.owner, repo: context.repo.repo, issue_number: issue.number }
  const labels = issue.labels.map((l) => l.name)
  const removeLabels = async (names) => {
    for (const name of names.filter((n) => labels.includes(n))) {
      await github.rest.issues.removeLabel({ ...ref, name })
    }
  }

  if (context.eventName === 'issue_comment') {
    if (comment.user.login !== issue.user.login || !labels.includes(NEED_INFO)) return
    if (issue.state === 'closed') await github.rest.issues.update({ ...ref, state: 'open' })
    await removeLabels([NEED_INFO, CLOSING])
    core.info(`#${issue.number} 作者回复了，摘掉「${NEED_INFO}」${issue.state === 'closed' ? '并重新打开' : ''}`)
    return
  }

  if (context.payload.action === 'edited' && !context.payload.changes?.body) return
  if (!labels.includes('bug') || MAINTAINERS.has(issue.author_association)) return

  const missing = missingFields(issue.body)
  const comments = await github.paginate(github.rest.issues.listComments, { ...ref, per_page: 100 })
  const mine = comments.find((c) => c.user.type === 'Bot' && c.body.startsWith(MARKER))

  if (missing.length) {
    const body = askBody(missing)
    if (mine) await github.rest.issues.updateComment({ ...ref, comment_id: mine.id, body })
    else await github.rest.issues.createComment({ ...ref, body })
    if (!labels.includes(NEED_INFO)) await github.rest.issues.addLabels({ ...ref, labels: [NEED_INFO] })
    core.info(`#${issue.number} 缺 ${missing.length} 项，打「${NEED_INFO}」`)
    return
  }
  if (mine) {
    await github.rest.issues.updateComment({ ...ref, comment_id: mine.id, body: DONE_BODY })
    await removeLabels([NEED_INFO, CLOSING])
    core.info(`#${issue.number} 信息补齐了`)
  }
}

module.exports.parseForm = parseForm
module.exports.missingFields = missingFields
