// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 排队等在途名额：能接这个请求的号在途都满了时，请求等其中一个放出名额再选号，不立刻回 503。
// 最多等多久由 pool.in_flight_wait 定，默认 30 秒，参考 sub2api 的 fallback_wait_timeout；0 = 不等。
package pool

import "time"

// defaultInFlightWait 号都在途占满时，请求最多等多久空位。
const defaultInFlightWait = 30 * time.Second

// SetInFlightWait 注入号都在途占满时请求最多等多久空位；0 = 不等，直接回 503。负值保留原值。
func (p *Pool) SetInFlightWait(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d >= 0 {
		p.inFlightWait = d
	}
}

// InFlightWait 当前生效的等待上限。
func (p *Pool) InFlightWait() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.inFlightWait
}

// SlotFreed 返回一个在下一次放出在途名额（Release）时关闭的 channel。要在选号之前取：
// 选号和开始等之间放出的名额也能把人叫醒。
func (p *Pool) SlotFreed() <-chan struct{} {
	p.slotMu.Lock()
	defer p.slotMu.Unlock()
	if p.slotFreed == nil {
		p.slotFreed = make(chan struct{})
	}
	return p.slotFreed
}

// notifySlotFreed 叫醒所有在等空位的请求；下一个来等的再新建 channel。
func (p *Pool) notifySlotFreed() {
	p.slotMu.Lock()
	if p.slotFreed != nil {
		close(p.slotFreed)
		p.slotFreed = nil
	}
	p.slotMu.Unlock()
}

// ServableIgnoringInFlight 报告 tried 以外有没有号除了在途名额以外都能接这个请求：过滤同 pick
// （域、禁用暂停、冷却熔断、模型级冷却、模型目录、积分保底），只是不看在途满没满。没有这样的号，
// 等空位也等不来能用的号，选不到号就该直接回错。
func (p *Pool) ServableIgnoringInFlight(tried map[string]bool, model, realm string) bool {
	return p.servableExcept(tried, model, realm, false)
}

// ServableForModel 报告 tried 以外有没有号此刻就能接这个请求：过滤同 pick 的候选，在途满的也不算，
// 但不含 pick 的全冷却兜底。国内版优先（pool.cross_realm 为 cn_first）据此判断国内版此刻能不能用：
// 兜底会把冷却中的号捞回来，掩盖「这个域现在没有能用的号」。
func (p *Pool) ServableForModel(tried map[string]bool, model, realm string) bool {
	return p.servableExcept(tried, model, realm, true)
}

// servableExcept 上面两个的共用遍历；inFlight 为 true 时忙的号（在途满或每分钟请求数到上限）不算。
func (p *Pool) servableExcept(tried map[string]bool, model, realm string, inFlight bool) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	for uid, e := range p.byUID {
		if tried[uid] || (realm != "" && e.a.Realm() != realm) {
			continue
		}
		if p.servableIgnoringBusyLocked(e, now, model, realm) && (!inFlight || !p.busy(e, now)) {
			return true
		}
	}
	return false
}

// servableIgnoringBusyLocked 号除了忙不忙以外能不能接这个请求（禁用暂停、冷却熔断、模型级冷却、
// 模型目录、积分保底）。调用方持 p.mu。
func (p *Pool) servableIgnoringBusyLocked(e *entry, now time.Time, model, realm string) bool {
	ok := e.healthy(now)
	if model != "" {
		ok = p.servableFor(e, now, model)
	}
	return ok && !p.floorBlockedForRealmModel(e, model, realm, now)
}
