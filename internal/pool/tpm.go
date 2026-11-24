// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"sync"
	"time"
)

// 每号每分钟 Token 数上限（pool.max_tpm 管国内版、pool.max_tpm_global 管国际版，0 = 不限）：
// 按最近 60 秒滑动窗口事后记账（在 RecordTokenUsage 写入上游返回的真实 token 数，不依赖本地
// 分词估算，失败请求也不会虚扣）。窗口内累计达到上限时和在途占满、RPM 到上限一样当作号忙：
// 选号跳过它，能接的号都忙时排队等（见 slotwait.go），靠 slotPoll 随窗口自然滑出后唤醒。

// tpmWindow 滑动窗口的长度。
const tpmWindow = time.Minute

// tpmSample 单次成功返回记入滑动窗口的 token 样本。
type tpmSample struct {
	at     time.Time
	tokens int64
}

// tpmCounter 一个号最近 60 秒内的 token 消耗样本（按时间先后排）及窗口累加和。
// 只在开着上限时记录（不限时零分配）；运行态，不持久化：重启后从零记起。
type tpmCounter struct {
	mu      sync.Mutex
	samples []tpmSample
	sum     int64
}

// pruneLocked 淘汰窗口外样本并同步扣减 sum（清空时归零防漂移）。调用方持 c.mu。
func (c *tpmCounter) pruneLocked(now time.Time) {
	cut := now.Add(-tpmWindow)
	i := 0
	for i < len(c.samples) && !c.samples[i].at.After(cut) {
		c.sum -= c.samples[i].tokens
		i++
	}
	if i > 0 {
		c.samples = append(c.samples[:0], c.samples[i:]...)
	}
	if len(c.samples) == 0 {
		c.sum = 0
		if cap(c.samples) > 128 {
			c.samples = nil
		}
	} else if c.sum < 0 {
		c.sum = 0
	}
}

// full 窗口内累计 token 已达到 limit；limit <= 0 不限。
func (c *tpmCounter) full(limit int64, now time.Time) bool {
	if limit <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
	return c.sum >= limit
}

// add 在开着上限且 tokens > 0 时写入一条样本；limit <= 0 不限、也不记。
func (c *tpmCounter) add(limit, tokens int64, now time.Time) {
	if limit <= 0 || tokens <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
	c.samples = append(c.samples, tpmSample{at: now, tokens: tokens})
	c.sum += tokens
}

// deltaTokens 提取本次尝试计入 TPM 滑动窗的真实 token 数：优先 TotalTokens，
// 缺失或非正时回落 PromptTokens + CompletionTokens；无有效观测（如失败请求）返回 0。
func deltaTokens(delta TokenUsageDelta) int64 {
	if delta.HasTotalTokens && delta.TotalTokens > 0 {
		return delta.TotalTokens
	}
	var sum int64
	if delta.HasPromptTokens && delta.PromptTokens > 0 {
		sum += delta.PromptTokens
	}
	if delta.HasCompletionTokens && delta.CompletionTokens > 0 {
		sum += delta.CompletionTokens
	}
	return sum
}

// SetMaxTPM 注入国内版单号每分钟 Token 数上限；0 = 不限。负值保留原值。
func (p *Pool) SetMaxTPM(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n >= 0 {
		p.maxTPM = n
	}
}

// SetMaxTPMGlobal 注入国际版单号每分钟 Token 数上限；0 = 不限。负值保留原值。两档各管各的域，不互相回落。
func (p *Pool) SetMaxTPMGlobal(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n >= 0 {
		p.maxTPMGlobal = n
	}
}

// tpmLimit 账号所在域的每分钟 Token 上限；0 = 不限。调用方持 p.mu。
func (p *Pool) tpmLimit(e *entry) int64 {
	if e.a.Realm() == "global" {
		return p.maxTPMGlobal
	}
	return p.maxTPM
}

// TPMBusy 报告 tried 以外、除了忙以外都能接这个请求的号里（口径同 ServableIgnoringInFlight），
// 有没有号是因为每分钟 Token 数到了上限才接不了。排队等满时据此说明是 TPM 满了，不只是在途或 RPM 满了。
func (p *Pool) TPMBusy(tried map[string]bool, model, realm string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	for uid, e := range p.byUID {
		if tried[uid] || (realm != "" && e.a.Realm() != realm) {
			continue
		}
		if p.servableIgnoringBusyLocked(e, now, model, realm) && e.tpm.full(p.tpmLimit(e), now) {
			return true
		}
	}
	return false
}
