// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"sync"
	"time"
)

// 每号每分钟请求数上限（pool.max_rpm 管国内版、pool.max_rpm_global 管国际版，0 = 不限）：按最近 60 秒
// 滑动窗口计，比自然分钟平滑，不会在分钟交界处突发两倍。超了和在途占满一样当作号忙：选号跳过它，
// 能接的号都忙时排队等（见 slotwait.go）。参考 sub2api 的账号 base_rpm，只是不给已绑定的会话留缓冲。

// rpmWindow 滑动窗口的长度。
const rpmWindow = time.Minute

// rpmCounter 一个号最近 60 秒内占到名额的时刻（按先后排）。只在开着上限时记，所以最多记上限那么多条。
// 运行态，不持久化：重启后从零记起。
type rpmCounter struct {
	mu    sync.Mutex
	times []time.Time
}

// countLocked 去掉窗口外的记录，返回窗口内的条数。调用方持 c.mu。
func (c *rpmCounter) countLocked(now time.Time) int {
	cut := now.Add(-rpmWindow)
	i := 0
	for i < len(c.times) && !c.times[i].After(cut) {
		i++
	}
	if i > 0 {
		c.times = append(c.times[:0], c.times[i:]...)
	}
	return len(c.times)
}

// full 窗口内已经到了 limit 条；limit <= 0 不限。
func (c *rpmCounter) full(limit int, now time.Time) bool {
	if limit <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.countLocked(now) >= limit
}

// take 没到 limit 就记一条并返回 true；limit <= 0 不限、也不记。
func (c *rpmCounter) take(limit int, now time.Time) bool {
	if limit <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.countLocked(now) >= limit {
		return false
	}
	c.times = append(c.times, now)
	return true
}

// SetMaxRPM 注入国内版单号每分钟请求数上限；0 = 不限。负值保留原值。
func (p *Pool) SetMaxRPM(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n >= 0 {
		p.maxRPM = n
	}
}

// SetMaxRPMGlobal 注入国际版单号每分钟请求数上限；0 = 不限。负值保留原值。两档各管各的域，不互相回落。
func (p *Pool) SetMaxRPMGlobal(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n >= 0 {
		p.maxRPMGlobal = n
	}
}

// rpmLimit 账号所在域的每分钟上限；0 = 不限。调用方持 p.mu。
func (p *Pool) rpmLimit(e *entry) int {
	if e.a.Realm() == "global" {
		return p.maxRPMGlobal
	}
	return p.maxRPM
}

// busy 账号此刻接不了新请求：在途占满，或者最近 60 秒的请求数到了上限。调用方持 p.mu。
func (p *Pool) busy(e *entry, now time.Time) bool {
	return p.inFlightFull(e) || e.rpm.full(p.rpmLimit(e), now)
}

// RPMBusy 报告 tried 以外、除了忙以外都能接这个请求的号里（口径同 ServableIgnoringInFlight），
// 有没有号是因为每分钟请求数到了上限才接不了。排队等满时据此说明是 RPM 满了，不只是在途满了。
func (p *Pool) RPMBusy(tried map[string]bool, model, realm string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	for uid, e := range p.byUID {
		if tried[uid] || (realm != "" && e.a.Realm() != realm) {
			continue
		}
		if p.servableIgnoringBusyLocked(e, now, model, realm) && e.rpm.full(p.rpmLimit(e), now) {
			return true
		}
	}
	return false
}
