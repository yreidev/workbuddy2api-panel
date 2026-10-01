// 时间范围选择：预设下拉 +「自定义」时的起止（精确到分钟，本地时区）。用量页与请求记录共用，口径见 lib/timerange.ts。
import {
  DateField, DateRangePicker, Label, ListBox, RangeCalendar, Select, TimeField, type DateValue, type RangeValue, type TimeValue,
} from '@heroui/react'
import { CalendarDateTime, getLocalTimeZone } from '@internationalized/date'
import { RANGE_PRESETS, initialRange, type RangePreset, type TimeRange } from '../lib/timerange'

const toValue = (d: Date) => new CalendarDateTime(d.getFullYear(), d.getMonth() + 1, d.getDate(), d.getHours(), d.getMinutes())
const toDate = (v: DateValue) => v.toDate(getLocalTimeZone())

export function TimeRangePicker({ value, onChange }: { value: TimeRange; onChange: (r: TimeRange) => void }) {
  const pickPreset = (preset: RangePreset) => {
    // 切到「自定义」时起止重置为「今天 00:00 → 现在」，免得上次留下的半年区间被无声沿用
    onChange(preset === 'custom' ? initialRange('custom') : { ...value, preset })
  }
  const range = value.from && value.to ? { start: toValue(value.from), end: toValue(value.to) } : null
  const pickRange = (r: RangeValue<DateValue> | null) => {
    if (!r) return
    const from = toDate(r.start), to = toDate(r.end)
    if (from > to) return // 起止颠倒时控件自己标红，不发请求
    onChange({ preset: 'custom', from, to })
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      <Select aria-label="时间范围" value={value.preset} onChange={(k) => k != null && pickPreset(k as RangePreset)} className="w-32">
        <Label className="sr-only">时间范围</Label>
        <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
        <Select.Popover>
          <ListBox>
            {RANGE_PRESETS.map((p) => <ListBox.Item key={p.id} id={p.id} textValue={p.label}>{p.label}<ListBox.ItemIndicator /></ListBox.Item>)}
          </ListBox>
        </Select.Popover>
      </Select>
      {value.preset === 'custom' && (
        <DateRangePicker aria-label="自定义时间区间" granularity="minute" hourCycle={24} value={range} onChange={pickRange}>
          {({ state }) => (
            <>
              <DateField.Group>
                <DateField.InputContainer>
                  <DateField.Input slot="start">{(segment) => <DateField.Segment segment={segment} />}</DateField.Input>
                  <DateRangePicker.RangeSeparator />
                  <DateField.Input slot="end">{(segment) => <DateField.Segment segment={segment} />}</DateField.Input>
                </DateField.InputContainer>
                <DateField.Suffix>
                  <DateRangePicker.Trigger><DateRangePicker.TriggerIndicator /></DateRangePicker.Trigger>
                </DateField.Suffix>
              </DateField.Group>
              <DateRangePicker.Popover className="flex flex-col gap-3">
                <RangeCalendar aria-label="选择日期">
                  <RangeCalendar.Header>
                    <RangeCalendar.Heading />
                    <RangeCalendar.NavButton slot="previous" />
                    <RangeCalendar.NavButton slot="next" />
                  </RangeCalendar.Header>
                  <RangeCalendar.Grid>
                    <RangeCalendar.GridHeader>{(day) => <RangeCalendar.HeaderCell>{day}</RangeCalendar.HeaderCell>}</RangeCalendar.GridHeader>
                    <RangeCalendar.GridBody>{(date) => <RangeCalendar.Cell date={date} />}</RangeCalendar.GridBody>
                  </RangeCalendar.Grid>
                </RangeCalendar>
                {(['start', 'end'] as const).map((side) => (
                  <div key={side} className="flex items-center justify-between gap-3 px-1 pb-1">
                    <Label>{side === 'start' ? '开始时间' : '结束时间'}</Label>
                    <TimeField
                      aria-label={side === 'start' ? '开始时间' : '结束时间'}
                      granularity="minute"
                      hourCycle={24}
                      value={state.timeRange?.[side] ?? null}
                      onChange={(v) => state.setTimeRange({ ...state.timeRange, [side]: v } as RangeValue<TimeValue>)}
                    >
                      <TimeField.Group variant="secondary">
                        <TimeField.Input>{(segment) => <TimeField.Segment segment={segment} />}</TimeField.Input>
                      </TimeField.Group>
                    </TimeField>
                  </div>
                ))}
              </DateRangePicker.Popover>
            </>
          )}
        </DateRangePicker>
      )}
    </div>
  )
}
