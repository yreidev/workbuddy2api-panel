// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 测活：面板单测、全部测活和定时测活给一个号发一条最短的真实对话（见 server.TestAccount）。
// 这里记上次测活的结果、测通后恢复这个号，以及挑出该定时测的号。
// 做法参考 new-api 的渠道测试（被动恢复模式）和 sub2api 的账号测试（auto_recover）。

package pool

import (
	"sort"
	"time"
)

// LivenessResult 一次测活的结果，账号卡片上的「上次测活」。存进 state.json，重启不丢。
type LivenessResult struct {
	At    time.Time `json:"at"`
	OK    bool      `json:"ok"`
	Model string    `json:"model,omitempty"`
	// Status 测活请求最后回的 HTTP 状态码；Error 失败原因（同请求记录，最多 200 字）。
	Status     int     `json:"status,omitempty"`
	Error      string  `json:"error,omitempty"`
	TTFBMs     int64   `json:"ttfb_ms,omitempty"`
	DurationMs int64   `json:"duration_ms,omitempty"`
	Credit     float64 `json:"credit,omitempty"`
	// Scheduled 定时测活发起的；面板上点的为 false。
	Scheduled bool `json:"scheduled,omitempty"`
	// Recovered 测通后解除了冷却、熔断或网关自动加的禁用。
	Recovered bool `json:"recovered,omitempty"`
}

// RecordLiveness 记下这个号最近一次测活的结果；uid 不存在返回 false。
func (p *Pool) RecordLiveness(uid string, r LivenessResult) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return false
	}
	e.liveness = &r
	p.dirty.Store(true)
	return true
}

// RecoverAfterLiveness 测活测通后恢复这个号：清账号级冷却（限流、余额不足）和测的这个模型的
// 模型级冷却，解除网关自动加的禁用（凭证失效、风控限制，测通说明已经好了）。熔断和连败计数
// 在测活请求成功时已由 NoteSuccess 清掉。人工禁用、暂停选号不动；风控波的整域暂停是整个域的
// 保护，也不因一个号测通就解除。返回这里有没有解除什么。
func (p *Pool) RecoverAfterLiveness(uid, model string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return false
	}
	now := time.Now()
	changed := false
	// 禁用时冷却已清空（disableLocked），两者不会同时存在。
	if e.disabled && e.reason != ManualDisableReason {
		e.disabled = false
		e.reason = ""
		e.sessionDeadFails = 0
		changed = true
	}
	if now.Before(e.until) && e.coolKind != CoolPause {
		e.until = time.Time{}
		e.reason = ""
		e.softStreak = 0
		changed = true
	}
	if mc, ok := e.modelCooldowns[model]; ok {
		delete(e.modelCooldowns, model)
		if now.Before(mc.Until) {
			changed = true
		}
	}
	if changed {
		p.dirty.Store(true)
	}
	return changed
}

// LivenessDue 定时测活要测的号：整个号在限流冷却、熔断或连败降权中的。余额不足的冷却有余额刷新管，
// 风控波暂停是整域的保护，只是某个模型限额的号别的模型照常能用，都不测；禁用（人工或网关自动）、
// 暂停选号和被代理池拦着（请求出不去）的也不测。按 UID 排序。
func (p *Pool) LivenessDue(now time.Time) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []string
	for uid, e := range p.byUID {
		if e.excluded() {
			continue
		}
		soft := now.Before(e.until) && e.coolKind == CoolSoft
		if soft || now.Before(e.breakerUntil) || now.Before(e.degradeUntil) {
			out = append(out, uid)
		}
	}
	sort.Strings(out)
	return out
}
