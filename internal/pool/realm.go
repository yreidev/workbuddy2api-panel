// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 分池选号域：按 realm（cn/global）过滤选号与可用集合。realm=="" 退化为现状。
package pool

import (
	"log"
	"slices"
	"sort"
	"time"
)

// expiringVirtualSlots 快过期账号在新会话候选集中的虚拟实例权重。
// 3:1 是温和偏好，不是固定比例：账号组成变化会自然改变最终占比。
const expiringVirtualSlots = 3

// CatalogUIDForRealm 拉该域目录用的账号：可用账号里按 UID 排序第一个个人号，没有个人号时
// 才用企业号。域目录要的是个人号视角——企业号的名单因企业而异，由它们各自的目录单独记
// （upstream.FetchAccountCatalog）。该域没有可用账号返回 ""。
func (p *Pool) CatalogUIDForRealm(realm string) string {
	uids := p.AvailableUIDsForRealm(realm)
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, uid := range uids {
		if e := p.byUID[uid]; e != nil && !e.a.IsEnterprise() {
			return uid
		}
	}
	if len(uids) > 0 {
		return uids[0]
	}
	return ""
}

// EnterpriseUIDs 该域没禁用的企业号（按 UID 排序），供定时刷新企业号各自的目录。
func (p *Pool) EnterpriseUIDs(realm string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var uids []string
	for uid, e := range p.byUID {
		if e.disabled || !e.a.IsEnterprise() || (realm != "" && e.a.Realm() != realm) {
			continue
		}
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	return uids
}

// AvailableUIDsForRealm 同 AvailableUIDs，但仅返回 Realm()==realm 的账号。
// realm=="" 退化为 AvailableUIDs（现状语义）。
func (p *Pool) AvailableUIDsForRealm(realm string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	uids := make([]string, 0, len(p.byUID))
	for uid, e := range p.byUID {
		if realm != "" && e.a.Realm() != realm {
			continue
		}
		if !e.healthy(now) {
			continue
		}
		if p.busy(e, now) {
			continue
		}
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	return uids
}

// WeightedAvailableUIDsForModelRealm 返回带虚拟实例权重的可用账号列表。
//
// 普通账号出现 1 次；有效快过期账号出现 expiringVirtualSlots 次。调用方继续按
// 原有序列表哈希，即可让新会话对快过期账号形成温和偏好。重复项按 UID 排序后
// 展开，保证同一账号拓扑下不同进程得到一致列表。
//
// 该方法是现有 AvailableUIDsForModelRealm 的增量入口，不改变旧方法语义，也不
// 修改配置、状态或 Redis schema。prefer_expiring=false 时退化为逐账号一次。
func (p *Pool) WeightedAvailableUIDsForModelRealm(model, realm string, skip map[string]bool) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	return p.weightedUIDsLocked(p.servableEntriesLocked(model, realm, skip, now), now)
}

// CheapestWeightedUIDsForModelRealm 新会话分配用：同 WeightedAvailableUIDsForModelRealm，
// 但先按成本分层只留生效层（实测免费 > 无观测 > 实测收费，含条件探索，见
// effectiveCostTierLocked），与 pick 普通轮换同一口径。model 为空不分层。
// 已绑定会话的续用校验仍走 WeightedAvailableUIDsForModelRealm（不分层），免得免费号
// 一出现就把绑在收费号上的会话全部挪走、丢掉上游前缀缓存。
// 取写锁：条件探索会写探索计时器。
func (p *Pool) CheapestWeightedUIDsForModelRealm(model, realm string, skip map[string]bool) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	cands := p.servableEntriesLocked(model, realm, skip, now)
	if model != "" && len(cands) > 0 {
		best, explored := p.effectiveCostTierLocked(cands, model, realm, now)
		kept := cands[:0]
		for _, e := range cands {
			if ti, _ := e.costTierOf(model, now); ti == best {
				kept = append(kept, e)
			}
		}
		cands = kept
		if explored {
			log.Printf("[pool] cost explore model=%s realm=%q (新会话) window=%s", model, realm, p.costExploreInterval)
		}
	}
	return p.weightedUIDsLocked(cands, now)
}

// servableEntriesLocked 在该模型与域上可接请求（servableFor 且在途未满）的账号里优先级最高的那一层，按 UID 排序；
// skip 里的号不算（客户端密钥绑了分组时，组外的号）。会话粘性按它续用和分配，所以绑在低优先级号上的会话，
// 等高优先级的号又能接了，下一次请求就回到高优先级那层。调用方持 p.mu（读锁即可）。
func (p *Pool) servableEntriesLocked(model, realm string, skip map[string]bool, now time.Time) []*entry {
	out := make([]*entry, 0, len(p.byUID))
	for uid, e := range p.byUID {
		if skip[uid] || (realm != "" && e.a.Realm() != realm) {
			continue
		}
		if !p.servableFor(e, now, model) {
			continue
		}
		if p.busy(e, now) {
			continue
		}
		out = append(out, e)
	}
	out = p.topPriorityLocked(out, model, realm, now)
	sort.Slice(out, func(i, j int) bool { return out[i].a.UID < out[j].a.UID })
	return out
}

// InTopTier 号此刻在不在这个请求能用的最高优先级那一层里（同会话粘性的候选，skip 里的号不算）。换号时先回
// 这段对话用过的号，也只回这一层的。
func (p *Pool) InTopTier(uid, model, realm string, skip map[string]bool) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return slices.ContainsFunc(p.servableEntriesLocked(model, realm, skip, time.Now()), func(e *entry) bool { return e.a.UID == uid })
}

// weightedUIDsLocked 把账号展开成带虚拟实例权重的 UID 列表（快过期账号出现
// expiringVirtualSlots 次，见 WeightedAvailableUIDsForModelRealm）。调用方持 p.mu。
func (p *Pool) weightedUIDsLocked(es []*entry, now time.Time) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		slots := 1
		if p.preferExpiring && expiringNow(e, now) {
			slots = expiringVirtualSlots
		}
		for i := 0; i < slots; i++ {
			out = append(out, e.a.UID)
		}
	}
	return out
}

// AvailableUIDsForModelRealm 同 AvailableUIDsForModel，但仅返回 Realm()==realm 的账号
// （6004 模型豁免照常生效）。realm=="" 退化为 AvailableUIDsForModel。
func (p *Pool) AvailableUIDsForModelRealm(model, realm string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	uids := make([]string, 0, len(p.byUID))
	for uid, e := range p.byUID {
		if realm != "" && e.a.Realm() != realm {
			continue
		}
		if !p.servableFor(e, now, model) {
			continue
		}
		if p.busy(e, now) {
			continue
		}
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	return uids
}
