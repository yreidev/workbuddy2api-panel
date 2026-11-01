// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 每号「不接的模型」名单：上游回 11158（这类账号不能用这个模型）时自动加，面板也能手动加（* 通配，
// 按不带 cn: / global: 前缀的模型名匹配，不区分大小写）。名单里的模型选号时不派给这个号，重启也记着。
// 11158 按账号类型定死，等多久都一样，所以不像 11102 那样到期再试：自动加的要手动删，或者指定这个
// 模型测活通过才解除。
package pool

import (
	"log"
	"slices"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/glob"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
)

// BlockedModel 号不接的一个模型。
type BlockedModel struct {
	// Model 模型名；手动加的可以带 * 通配。
	Model string `json:"model"`
	// Auto 上游拒绝后自动加的，Reason 是拒绝的原因（如 11158）；手动加的两项都为空。
	Auto   bool      `json:"auto,omitempty"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// refusedBy 号的名单里挡住这个模型的那一项；没有返回 nil。
func (e *entry) refusedBy(model string) *BlockedModel {
	if model == "" {
		return nil
	}
	for i := range e.blockedModels {
		if glob.Match(e.blockedModels[i].Model, model) {
			return &e.blockedModels[i]
		}
	}
	return nil
}

// BlockModelForever 上游回 11158：把这个模型加进号的名单，以后不再派给它。名单里已经挡住这个模型时不动。
func (p *Pool) BlockModelForever(uid, model, reason string) {
	if model == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok || e.refusedBy(model) != nil {
		return
	}
	e.blockedModels = append(e.blockedModels, BlockedModel{Model: model, Auto: true, Reason: reason, At: time.Now()})
	p.dirty.Store(true)
	log.Printf("[pool] blocked model: acct=%s model=%s（%s），以后不再派给这个号，可在面板账号详情里解除",
		logfmt.Label(e.a.UID, e.a.Nickname), model, reason)
}

// SetBlockedModels 用 models 替换号的名单（面板账号详情保存）：还留着的项原样保留（自动加的保留原因和时间），
// 去掉的就解除，新加的算手动加的。models 由调用方校验、去重过。uid 不存在返回 false。
func (p *Pool) SetBlockedModels(uid string, models []string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return false
	}
	now := time.Now()
	var out []BlockedModel
	for _, m := range models {
		if i := blockedIndex(e.blockedModels, m); i >= 0 {
			out = append(out, e.blockedModels[i])
		} else {
			out = append(out, BlockedModel{Model: m, At: now})
		}
	}
	e.blockedModels = out
	p.dirty.Store(true)
	return true
}

// AddBlockedModels 批量加进名单（手动加的），名单里已有的同名项不动。uid 不存在返回 false。
func (p *Pool) AddBlockedModels(uid string, models []string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return false
	}
	now := time.Now()
	for _, m := range models {
		if blockedIndex(e.blockedModels, m) < 0 {
			e.blockedModels = append(e.blockedModels, BlockedModel{Model: m, At: now})
			p.dirty.Store(true)
		}
	}
	return true
}

// RemoveBlockedModels 批量从名单删掉同名的项，自动加的也删。uid 不存在返回 false。
func (p *Pool) RemoveBlockedModels(uid string, models []string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return false
	}
	n := len(e.blockedModels)
	e.blockedModels = slices.DeleteFunc(e.blockedModels, func(b BlockedModel) bool {
		return slices.ContainsFunc(models, func(m string) bool { return strings.EqualFold(m, b.Model) })
	})
	if len(e.blockedModels) != n {
		p.dirty.Store(true)
	}
	return true
}

// ModelRefused 号是不是确定不能用这个模型：名单挡住了，或者上游回过 11102（后端没有这个模型）还在避让期。
// 测活自动挑模型时跳过这些。
func (p *Pool) ModelRefused(uid, model string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.byUID[uid]
	if !ok {
		return false
	}
	if e.refusedBy(model) != nil {
		return true
	}
	mc, ok := e.modelCooldowns[model]
	return ok && modelDenied(mc.Reason) && time.Now().Before(mc.Until)
}

// clearAutoBlockLocked 这个号用这个模型成功了：删掉名单里自动加的同名项（手动加的不动）。调用方持 p.mu。
func (p *Pool) clearAutoBlockLocked(e *entry, model string) {
	n := len(e.blockedModels)
	e.blockedModels = slices.DeleteFunc(e.blockedModels, func(b BlockedModel) bool {
		return b.Auto && strings.EqualFold(b.Model, model)
	})
	if len(e.blockedModels) != n {
		p.dirty.Store(true)
		log.Printf("[pool] blocked model cleared: acct=%s model=%s 用这个模型成功了，解除自动屏蔽",
			logfmt.Label(e.a.UID, e.a.Nickname), model)
	}
}

// blockedIndex 名单里和 m 同名（不区分大小写）的项的下标，没有返回 -1。
func blockedIndex(list []BlockedModel, m string) int {
	return slices.IndexFunc(list, func(b BlockedModel) bool { return strings.EqualFold(b.Model, m) })
}
