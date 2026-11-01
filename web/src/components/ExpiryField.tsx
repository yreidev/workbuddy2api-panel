// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 到期时间：开关打开后选一个时刻（本机时区，精确到分），另给几个常用时长。客户端密钥和代理共用。
import type { ReactNode } from 'react'
import { Button, Calendar, DateField, DatePicker, type DateValue, Description, Switch } from '@heroui/react'
import { CalendarDateTime, getLocalTimeZone } from '@internationalized/date'
import { useNow } from '../lib/hooks'

const toValue = (ms: number) => {
  const d = new Date(ms)
  return new CalendarDateTime(d.getFullYear(), d.getMonth() + 1, d.getDate(), d.getHours(), d.getMinutes())
}
const DAY = 86_400_000

export function ExpiryField({ value, onChange, description, pastWarning, presets, initialDays = 30 }: {
  /** 到期时刻（毫秒），null = 不设 */
  value: number | null
  onChange: (ms: number | null) => void
  /** 开关下面的说明：不设会怎样、到期会怎样 */
  description: ReactNode
  /** 选的时刻已经过了时的提醒 */
  pastWarning: string
  /** 常用时长（天） */
  presets: number[]
  /** 打开开关时先填几天后 */
  initialDays?: number
}) {
  const now = useNow(30_000)
  const pick = (v: DateValue | null) => onChange(v ? v.toDate(getLocalTimeZone()).getTime() : null)
  const after = (days: number) => {
    const d = new Date(Date.now() + days * DAY)
    d.setSeconds(0, 0)
    onChange(d.getTime())
  }
  return (
    <div className="flex flex-col gap-2">
      <Switch isSelected={value != null} onChange={(on) => (on ? after(initialDays) : onChange(null))}>
        <Switch.Content>
          <Switch.Control><Switch.Thumb /></Switch.Control>
          设到期时间
        </Switch.Content>
        <Description>{description}</Description>
      </Switch>
      {value != null && (
        <div className="flex flex-wrap items-end gap-2">
          <DatePicker aria-label="到期时间" granularity="minute" hourCycle={24} value={toValue(value)} onChange={pick} className="w-52">
            <DateField.Group>
              <DateField.Input>{(segment) => <DateField.Segment segment={segment} />}</DateField.Input>
              <DateField.Suffix>
                <DatePicker.Trigger><DatePicker.TriggerIndicator /></DatePicker.Trigger>
              </DateField.Suffix>
            </DateField.Group>
            <DatePicker.Popover>
              <Calendar aria-label="选择到期日期">
                <Calendar.Header>
                  <Calendar.Heading />
                  <Calendar.NavButton slot="previous" />
                  <Calendar.NavButton slot="next" />
                </Calendar.Header>
                <Calendar.Grid>
                  <Calendar.GridHeader>{(day) => <Calendar.HeaderCell>{day}</Calendar.HeaderCell>}</Calendar.GridHeader>
                  <Calendar.GridBody>{(date) => <Calendar.Cell date={date} />}</Calendar.GridBody>
                </Calendar.Grid>
              </Calendar>
            </DatePicker.Popover>
          </DatePicker>
          {/* 几个常用时长一起折行，窄的弹窗里不会剩一个掉到下一行 */}
          <div className="flex gap-2">
            {presets.map((n) => <Button key={n} size="sm" variant="ghost" onPress={() => after(n)}>{n} 天后</Button>)}
          </div>
          {value <= now && <span className="w-full text-xs text-warning">{pastWarning}</span>}
        </div>
      )}
    </div>
  )
}
