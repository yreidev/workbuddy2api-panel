// 模型锁池：被上游按模型限流的模型（哪些模型不能用、锁了几个号、还要锁多久）。
// 账号表回答「哪个账号不能用」，这里回答「哪个模型不能用」。窄屏换成卡片。
import { Chip, Table, useMediaQuery } from '@heroui/react'
import { earliestUnlock, lockLeft, lockState, lockSummary } from '../../lib/modellocks'
import type { ModelLockRow } from '../../lib/types'
import { Panel } from '../../components/Panel'

const realmLabel = (r: string) => (r === 'global' ? '国际版' : '国内版')

export function ModelLocks({ rows, now }: { rows: ModelLockRow[]; now: number }) {
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  if (!rows.length) return <Panel title="模型锁池" desc="当前没有模型级限流，所有模型均可选" />
  const summary = lockSummary(rows)
  return (
    <Panel
      title="模型锁池"
      desc="哪些模型不能用、还要锁多久（不含已禁用和暂停选号的账号）"
      actions={<span className={`text-sm ${summary.bad ? 'text-danger' : 'text-warning'}`}>{summary.text}</span>}
    >
      {isDesktop ? (
        <Table variant="secondary">
          <Table.ScrollContainer>
            <Table.Content aria-label="模型锁池" className="min-w-[900px]">
              <Table.Header>
                <Table.Column isRowHeader className="whitespace-nowrap">模型</Table.Column>
                <Table.Column className="whitespace-nowrap">域</Table.Column>
                <Table.Column className="whitespace-nowrap">状态</Table.Column>
                <Table.Column className="whitespace-nowrap text-right">可选 / 总数</Table.Column>
                <Table.Column className="whitespace-nowrap text-right">锁定账号</Table.Column>
                <Table.Column className="whitespace-nowrap text-right">最早解锁</Table.Column>
                <Table.Column className="whitespace-nowrap text-right">全池解锁</Table.Column>
                <Table.Column>原因</Table.Column>
              </Table.Header>
              <Table.Body>
                {rows.map((r) => {
                  const st = lockState(r.state)
                  return (
                    <Table.Row key={r.realm + ':' + r.model} id={r.realm + ':' + r.model}>
                      <Table.Cell><span className="break-all font-mono">{r.model}</span></Table.Cell>
                      <Table.Cell className="whitespace-nowrap">{realmLabel(r.realm)}</Table.Cell>
                      <Table.Cell><Chip size="sm" variant="soft" color={st.color}>{st.label}</Chip></Table.Cell>
                      <Table.Cell className="whitespace-nowrap text-right font-mono tabular-nums">{r.servable || 0} / {r.total || 0}</Table.Cell>
                      <Table.Cell className="text-right font-mono tabular-nums">{r.locked || 0}</Table.Cell>
                      <Table.Cell className="whitespace-nowrap text-right font-mono tabular-nums">{lockLeft(earliestUnlock(r), now)}</Table.Cell>
                      <Table.Cell className="whitespace-nowrap text-right font-mono tabular-nums">{lockLeft(r.fully_unlock_at, now)}</Table.Cell>
                      <Table.Cell>
                        {r.reason ? <span title={r.reason} className="line-clamp-2 max-w-72 break-all text-xs text-muted">{r.reason}</span> : <span className="text-muted">—</span>}
                      </Table.Cell>
                    </Table.Row>
                  )
                })}
              </Table.Body>
            </Table.Content>
          </Table.ScrollContainer>
        </Table>
      ) : (
        <ul className="flex flex-col gap-2">
          {rows.map((r) => {
            const st = lockState(r.state)
            return (
              <li key={r.realm + ':' + r.model} className="flex flex-col gap-1 rounded-xl bg-surface-secondary px-3 py-2 text-sm">
                <div className="flex items-start justify-between gap-2">
                  <span className="break-all font-mono">{r.model}</span>
                  <Chip size="sm" variant="soft" color={st.color} className="shrink-0">{st.label}</Chip>
                </div>
                <div className="text-xs text-muted tabular-nums">
                  {realmLabel(r.realm)} · 可选 {r.servable || 0} / {r.total || 0} · 锁定 {r.locked || 0} 个号
                </div>
                <div className="text-xs tabular-nums">
                  最早 {lockLeft(earliestUnlock(r), now)} 后解锁 · 全池 {lockLeft(r.fully_unlock_at, now)} 后解锁
                </div>
                {r.reason && <div className="break-all text-xs text-muted">{r.reason}</div>}
              </li>
            )
          })}
        </ul>
      )}
    </Panel>
  )
}
