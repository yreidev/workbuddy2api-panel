import { describe, expect, it } from 'vitest'
import { ALL_FIELDS, changedFields, fromForm, toForm, validateField } from './config'

const field = (name: string) => ALL_FIELDS.find((f) => f.name === name)!

describe('配置表单', () => {
  it('回填与提交互为逆运算：数组变「9, 21」，空输入不提交，开关总是提交', () => {
    const cfg = {
      listen: ':7863',
      schedule: { checkin_hours: [9, 21], checkin_enabled: true },
      pool: { max_in_flight: 3 },
      prompt: { mode: 'custom' },
    }
    const form = toForm(cfg)
    expect(form.checkin_hours).toBe('9, 21')
    expect(form.max_in_flight).toBe('3')
    expect(form.api_key).toBe('')
    expect(form.prefer_expiring).toBe(false)
    const body = fromForm(form) as Record<string, Record<string, unknown>>
    expect(body.listen).toBe(':7863')
    expect(body.schedule.checkin_hours).toEqual([9, 21])
    expect(body.pool.max_in_flight).toBe(3)
    expect('api_key' in body).toBe(false)
    expect(body.pool.prefer_expiring).toBe(false)
    expect(body.schedule.include_disabled_in_tasks).toBe(false)
    expect('server' in body).toBe(false)
  })

  // 从上游 internal/panel/frontend_test.go 的 TestAppJSCollectConfigClearable 平移（期望值原样保留）：
  // 覆盖型字段（user_agent / prompt_file）空串必须照发，其余文本字段空串仍不下发。
  it('覆盖型字段清空也提交，其余空输入不提交', () => {
    const form = { ...toForm({}), listen: '', api_key: 'secret', user_agent: '', prompt_file: '', checkin_hours: '' }
    const out = fromForm(form) as Record<string, Record<string, unknown> | string>
    const has = (o: unknown, k: string) => Object.prototype.hasOwnProperty.call(o || {}, k)
    const up = out.upstream as Record<string, unknown>
    const prompt = out.prompt as Record<string, unknown>
    expect([
      has(up, 'user_agent'), up.user_agent,
      has(prompt, 'file'), prompt.file,
      has(out, 'listen'),
      has(out.schedule, 'checkin_hours'),
      out.api_key,
    ]).toEqual([true, '', true, '', false, false, 'secret'])
  })

  // 上游 TestConfigFormMatchesCFGMap 钉的是「表单控件 ↔ CFG_MAP」一一对应；这里两者是同一份
  // 字段定义，对应关系天然成立，剩下要钉的是：名字和路径不重复，且关键热改键确实在表单里。
  it('字段名与配置路径不重复，request_client_info 在表单里', () => {
    const names = ALL_FIELDS.map((f) => f.name)
    const paths = ALL_FIELDS.map((f) => f.path.join('.'))
    expect(new Set(names).size).toBe(names.length)
    expect(new Set(paths).size).toBe(paths.length)
    expect(field('request_client_info').path).toEqual(['logging', 'request_client_info'])
    expect(field('request_client_info').kind).toBe('switch')
  })

  it('时长、时点、数字校验', () => {
    expect(validateField(field('soft_rate'), '1h30m')).toBeNull()
    expect(validateField(field('soft_rate'), '30 分钟')).not.toBeNull()
    expect(validateField(field('soft_rate'), '')).toBeNull()
    // 单写 0 是 Go 合法时长（「0 关闭 / 不限制」），其余数字必须带单位
    expect(validateField(field('read_timeout'), '0')).toBeNull()
    expect(validateField(field('expiring_soon'), '0')).toBeNull()
    expect(validateField(field('read_timeout'), '300')).not.toBeNull()
    expect(validateField(field('read_timeout'), '00')).not.toBeNull()
    expect(validateField(field('checkin_hours'), '9，21')).toBeNull()
    expect(validateField(field('checkin_hours'), '9, 24')).not.toBeNull()
    expect(validateField(field('max_in_flight_global'), '0')).not.toBeNull()
  })

  it('统计改动项（忽略首尾空格）', () => {
    const a = toForm({ listen: ':7863' })
    expect(changedFields(a, { ...a, listen: ' :7863 ' })).toEqual([])
    expect(changedFields(a, { ...a, listen: ':8000', prefer_expiring: true })).toEqual(['listen', 'prefer_expiring'])
  })
})
