// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 代理池设置：只许走号的代理、健康检查和查出口 IP 的间隔、连续不可用多久后改绑、到期前几天提醒、查出口 IP 的地址。
// 存在 proxies.json 里（不在 config.json），保存后马上生效，命令行工具也读。打开「只许走号的代理」前确认一次。
import { useState } from 'react'
import { Button, Description, FieldError, Label, NumberField, Switch, TextArea, TextField, toast } from '@heroui/react'
import { useQueryClient } from '@tanstack/react-query'
import { post } from '../../lib/api'
import { toastError } from '../../lib/hooks'
import { badExitURL, settingsForm, settingsInput, type SettingsForm } from '../../lib/proxies'
import { qk, useOverview } from '../../lib/queries'
import type { ProxyAccount, ProxySettings } from '../../lib/types'
import { BusyButton } from '../../components/Feedback'
import { Panel } from '../../components/Panel'
import { useConfirm } from '../../components/useConfirm'

type MinuteKey = 'health_minutes' | 'exit_minutes' | 'rebind_minutes' | 'expiry_warn_days'

const NUMBERS: { id: MinuteKey; label: string; desc: string }[] = [
  { id: 'health_minutes', label: '健康检查间隔（分钟）', desc: '经代理连一次官方入口，只测通不通和延迟。' },
  { id: 'exit_minutes', label: '查出口 IP 的间隔（分钟）', desc: '手动测、导入后也会马上查。' },
  { id: 'rebind_minutes', label: '连续不可用多久后改绑（分钟）', desc: '同池有能用的代理时，号改绑到绑号最少的那个。' },
  { id: 'expiry_warn_days', label: '到期前几天提醒', desc: '在代理页和账号池页提醒。' },
]

export function ProxySettingsPanel({ settings, accounts }: { settings: ProxySettings; accounts: Record<string, ProxyAccount> }) {
  // 别处改了设置（另一个标签页、命令行）时表单跟着换成新的
  return <SettingsBody key={JSON.stringify(settings)} settings={settings} accounts={accounts} />
}

function SettingsBody({ settings, accounts }: { settings: ProxySettings; accounts: Record<string, ProxyAccount> }) {
  const qc = useQueryClient()
  const overview = useOverview()
  const [confirmEl, ask] = useConfirm()
  const [form, setForm] = useState<SettingsForm>(() => settingsForm(settings))
  const [saving, setSaving] = useState(false)
  const set = (patch: Partial<SettingsForm>) => setForm((f) => ({ ...f, ...patch }))
  const dirty = JSON.stringify(settingsInput(form)) !== JSON.stringify(settingsInput(settingsForm(settings)))
  const bad = badExitURL(form.exit_ip_urls)
  const unbound = (overview.data?.accounts || []).filter((a) => !accounts[a.uid]?.pool).length

  const save = async () => {
    setSaving(true)
    try {
      await post('proxies/settings', settingsInput(form))
      toast.success('代理池设置已保存')
      void qc.invalidateQueries({ queryKey: qk.proxies })
      void qc.invalidateQueries({ queryKey: qk.overview })
    } catch (e) {
      toastError(e, '保存失败：')
    } finally {
      setSaving(false)
    }
  }
  const onSave = () => {
    if (form.require_bound && !settings.require_bound && unbound > 0) {
      ask({
        title: '打开「只许走号的代理」？',
        body: `现在有 ${unbound} 个号没绑代理，保存后它们马上不接请求、不跑定时任务（签到、保活等），命令行工具也跳过，直到绑上代理。`,
        confirmLabel: '打开并保存',
        onConfirm: () => void save(),
      })
      return
    }
    void save()
  }

  return (
    <Panel
      title="代理池设置"
      desc="存在数据目录的 proxies.json 里，保存后马上生效，命令行工具（签到、查积分、登录、领试用）也照这里来。"
      actions={dirty && <Button size="sm" variant="tertiary" onPress={() => setForm(settingsForm(settings))}>撤销修改</Button>}
      contentClassName="flex flex-col gap-5"
    >
      <Switch isSelected={form.require_bound} onChange={(v) => set({ require_bound: v })}>
        <Switch.Content>
          <Switch.Control><Switch.Thumb /></Switch.Control>
          只许走号的代理
        </Switch.Content>
        <Description>
          开了以后没绑代理的号不接请求、不跑定时任务，命令行工具也跳过，卡片上标「没绑代理」；关着时没绑的号走服务器自己的网络（或环境变量里的全局代理）。
          {unbound > 0 && ` 现在没绑代理的号有 ${unbound} 个。`}
        </Description>
      </Switch>

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {NUMBERS.map((n) => (
          <NumberField
            key={n.id}
            value={form[n.id]}
            onChange={(v) => set({ [n.id]: Number.isNaN(v) || v < 1 ? 1 : v })}
            minValue={1}
            formatOptions={{ maximumFractionDigits: 0 }}
          >
            <Label>{n.label}</Label>
            <NumberField.Group>
              <NumberField.DecrementButton />
              <NumberField.Input />
              <NumberField.IncrementButton />
            </NumberField.Group>
            <Description>{n.desc}</Description>
          </NumberField>
        ))}
      </div>

      <TextField value={form.exit_ip_urls} onChange={(v) => set({ exit_ip_urls: v })} isInvalid={!!bad}>
        <Label>查出口 IP 的地址</Label>
        <TextArea rows={3} placeholder={'http://ip-api.com/json/?lang=zh-CN\nhttp://api64.ipify.org?format=json'} className="font-mono text-xs" spellCheck={false} />
        {bad ? (
          <FieldError>「{bad}」不对：要写 http:// 或 https:// 开头的完整地址</FieldError>
        ) : (
          <Description>一行一个，按顺序试，第一个查到的为准；空着用内置的（先 ip-api.com，带地区和运营商；再 ipify，只有 IP）。经代理去查，对方看到的是代理的出口。</Description>
        )}
      </TextField>

      <div className="flex justify-end">
        <BusyButton busy={saving} isDisabled={!dirty || !!bad} onPress={onSave}>保存设置</BusyButton>
      </div>
      {confirmEl}
    </Panel>
  )
}
