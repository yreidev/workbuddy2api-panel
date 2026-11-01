// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// Package session 会话粘性路由：同一会话（conversationId / metadata 键）尽量绑定同一账号。
//
// 设计参考 antigravityProxyGo internal/session（fast-path RLock / 双段分配 / TTL / 持久化），
// 但改为纯内存 + redisstore 异步镜像：
//   - 命中走 RLock 快查（绝大多数请求已绑定）；
//   - 未命中/失效走写锁 re-check 后分配，避免同 key 并发重复分配（TOCTOU 防护）；
//   - 分配先回到这段对话最近成功用过的号（见 warmEntry），没有再优先"空闲账号"（未绑定任何
//     会话的可用号）哈希，其次全池哈希（双段策略）；
//   - LastActive 滚动续期，TTL 过期由后台 GC 或快路径惰性过期清理；
//   - 每次绑定变更 fire-and-forget 镜像到 redisstore（防重启丢粘性）。
package session

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/redisstore"
)

// entry 单条会话绑定。
type entry struct {
	uid        string
	lastActive time.Time
}

// warmEntry 一段对话最近成功用过的号，最近的在前（issue #2）。有的模型的前缀缓存按号存
// （deepseek，见 docs/official-client.md），绑定过期（闲置超过 TTL）、绑定的号这次用不了、
// 网关重启后，先回到这些号：上面留着这段对话的前缀缓存。思路参考 workbuddy2api-hub 按会话记录
// 用过的号（affinity），见 README 致谢。
type warmEntry struct {
	uids []string
	last time.Time
}

const (
	// warmMax 每段对话记几个用过的号。
	warmMax = 3
	// warmTTL 用过的号在最后一次成功后记多久，同会话 id 记录（convIDTTL）。
	warmTTL = convIDTTL
	// warmKeysMax 最多记多少段对话，超了先清过期的，再清最久没用的，降到九成。
	warmKeysMax = 20000
)

// Config 路由依赖；Available 返回"可用账号"（healthy 且未占满在途）的有序 uid 列表，
// 由 pool.AvailableUIDs 提供。Store 可为 redisstore.Noop（纯内存）。
type Config struct {
	TTL        time.Duration
	GCInterval time.Duration
	Store      redisstore.Store
	Available  func() []string
	// AvailableForModel 按请求的裸模型名与域返回"在该模型上可用"的账号（healthy 且未占满在途，
	// 且未被该模型限流/限额）；realm 为 "" 表示两域合池（跨域负载均衡）。
	// nil 时回落 Available（无模型维度，行为与引入前一致）。
	//
	// 为什么粘性需要模型维度：绑定只记 uid，而同一个会话可能换模型。账号被 6004
	// 模型级限额后对**其他模型**仍可用（issue #31 豁免），此时若只按账号级可用性
	// 校验，会话会被钉在这个号上反复失败——正是"限额后换不动号"的观感来源。
	// 域由调用方（handler）按前缀与跨域开关定好再传入，这里不再自己解析模型名。skip 里的号不算
	// （客户端密钥绑了分组时，组外的号）；只返回优先级最高的那一层（见 pool.servableEntriesLocked）。
	AvailableForModel func(model, realm string, skip map[string]bool) []string
	// AssignableForModel 新会话（或绑定失效后重新分配）挑号的候选，在 AvailableForModel 之上
	// 再按成本分层只留最便宜那一层（pool.CheapestWeightedUIDsForModelRealm）。只用于分配，
	// 已绑定会话的续用仍按 AvailableForModel 校验，免得免费号一出现就把会话全挪走。
	// nil 时分配也用 AvailableForModel。
	AssignableForModel func(model, realm string, skip map[string]bool) []string
}

// Router 会话粘性路由器。
type Router struct {
	mu      sync.RWMutex
	entries map[string]entry
	// warm 会话键的 sha256 → 这段对话最近成功用过的号；warmDirty 有没存盘的改动（见 persist.go）。
	warm      map[[32]byte]warmEntry
	warmDirty bool
	cfg       Config
	stop      chan struct{}
	done      chan struct{} // GC goroutine 退出时关闭，StopGC 等它
}

// New 构建路由器。若 cfg.Store 为 nil 则用 Noop（纯内存）；cfg.Available 为 nil 视为空池。
// TTL/GCInterval 非正取默认（30m / 5m）——main 从 config 解析后传入，这里兜底。
func New(cfg Config) *Router {
	if cfg.Store == nil {
		cfg.Store = redisstore.Noop{}
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 30 * time.Minute
	}
	if cfg.GCInterval <= 0 {
		cfg.GCInterval = 5 * time.Minute
	}
	return &Router{entries: map[string]entry{}, warm: map[[32]byte]warmEntry{}, cfg: cfg}
}

// StartGC 启动后台 GC goroutine（幂等）。进程退出时调 StopGC。
//
// stop channel 必须在启 goroutine 前捕获到**局部变量**：goroutine 在 select 里
// 每轮重新求值 r.stop 是无锁读，而 StopGC 持写锁把它置 nil——既是数据竞争
// （-race 可复现），又会在读到 nil 后让该 case 永久阻塞（nil channel 永不就绪），
// 于是关停彻底失效：goroutine 再也不会退出，ticker 无限触发 gcOnce（goroutine
// 泄漏 + 关停后仍持续 GC）。捕获局部变量后，close(stop) 与 select 观测的是同一个
// channel，StopGC 一定能让 goroutine 退出。
func (r *Router) StartGC() {
	r.mu.Lock()
	if r.stop != nil {
		r.mu.Unlock()
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	r.stop, r.done = stop, done
	r.mu.Unlock()

	go func() {
		defer close(done)
		t := time.NewTicker(r.cfg.GCInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				r.gcOnce(time.Now())
			}
		}
	}()
}

// StopGC 停止后台 GC（幂等），等 GC goroutine 退出才返回，返回后不会再清理。
// select 在 stop 和 ticker 同时就绪时随机选，只关 stop 不等的话还可能再跑一轮 gcOnce。
// 要放开锁再等：gcOnce 自己要拿 r.mu。
func (r *Router) StopGC() {
	r.mu.Lock()
	stop, done := r.stop, r.done
	r.stop, r.done = nil, nil
	r.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

// LoadFromStore 启动时从 redisstore 恢复绑定（内存覆盖本地，读操作仅此处发生）。
// 已有本地绑定被保留——Redis 仅为恢复备份，本地一旦建立即为权威。
func (r *Router) LoadFromStore() {
	binds := r.cfg.Store.LoadBinds()
	if len(binds) == 0 {
		return
	}
	now := time.Now()
	r.mu.Lock()
	loaded := 0
	for key, uid := range binds {
		if _, exists := r.entries[key]; exists {
			continue
		}
		r.entries[key] = entry{uid: uid, lastActive: now}
		loaded++
	}
	r.mu.Unlock()
	if loaded > 0 {
		log.Printf("[session] 从 Redis 恢复 %d 条粘性会话绑定", loaded)
	}
}

// Resolve 返回会话 key 应绑定的账号 uid，ok=false 表示当前无可用账号。
// 无模型、无域维度（等价于 ResolveForModel(key, "", "")），保留给不关心模型的调用方。
func (r *Router) Resolve(key string) (string, bool) {
	return r.ResolveForModel(key, "", "", nil)
}

// ResolveForModel 返回会话 key 在该模型（裸名）与域上应绑定的账号 uid；realm 为 "" 即两域合池，skip 里的号
// 不用（客户端密钥绑了分组时，组外的号）。命中且账号在该模型可用 → 滚动 lastActive 并直接返回；否则（绑定号已冷却/
// 占满/被该模型限流/不在此刻最高的优先级层）走重新分配。
//
// 为什么必须带模型：绑定只记 uid，同一个会话可能换模型；账号被 6004 模型级限额后
// 对其他模型仍可用（见 pool.healthyForModel 的 softRateModel 豁免）。若只按账号级
// 可用性校验，会话会被钉在一个"对当前模型不可用"的号上反复失败。
func (r *Router) ResolveForModel(key, model, realm string, skip map[string]bool) (string, bool) {
	now := time.Now()
	available := r.availableSet(model, realm, skip)

	// ── Fast path: RLock 快查 ──────────────────────────────
	r.mu.RLock()
	e, found := r.entries[key]
	r.mu.RUnlock()
	if found && !expired(e, now, r.cfg.TTL) {
		if available[e.uid] {
			r.touch(key, e.uid, now)
			return e.uid, true
		}
		// 绑定号已冷却/占满 → 失效，落入慢路径重分配。
	}

	// ── Slow path: 写锁 re-check 后分配 ────────────────────
	r.mu.Lock()
	defer r.mu.Unlock()

	// re-check：并发同 key 可能已被其他 goroutine 分配好。
	if e2, found2 := r.entries[key]; found2 && !expired(e2, now, r.cfg.TTL) {
		if available[e2.uid] {
			r.entries[key] = entry{uid: e2.uid, lastActive: now}
			return e2.uid, true
		}
		delete(r.entries, key) // 失效：清掉再分配
	}

	// 先回到这段对话用过的号（最近的优先）：按 AvailableForModel 校验、不按成本分层筛，
	// 同已绑定会话的续用——换到更便宜的号就丢了这段对话的前缀缓存。
	if uid := r.warmPickLocked(key, available, now); uid != "" {
		r.entries[key] = entry{uid: uid, lastActive: now}
		r.cfg.Store.SetBind(key, uid, r.cfg.TTL)
		return uid, true
	}

	uids := r.assignableSlice(model, realm, skip)
	if len(uids) == 0 {
		return "", false
	}

	// 双段策略：优先"空闲账号"（未被任何会话绑定的可用号），其次全池。
	bound := map[string]bool{}
	for _, v := range r.entries {
		bound[v.uid] = true
	}
	var idle []string
	for _, u := range uids {
		if !bound[u] {
			idle = append(idle, u)
		}
	}
	pool2 := idle
	if len(pool2) == 0 {
		pool2 = uids
	}
	uid := pool2[hashIndex(key, len(pool2))]

	prev, existed := r.entries[key]
	r.entries[key] = entry{uid: uid, lastActive: now}
	if existed && prev.uid != uid {
		r.cfg.Store.DelBind(key)
	}
	r.cfg.Store.SetBind(key, uid, r.cfg.TTL)
	return uid, true
}

// touch 滚动 lastActive 并异步镜像（只在快路径命中时写最后一次）。
func (r *Router) touch(key, uid string, now time.Time) {
	r.mu.Lock()
	r.entries[key] = entry{uid: uid, lastActive: now}
	r.mu.Unlock()
	r.cfg.Store.SetBind(key, uid, r.cfg.TTL)
}

// Bind 显式把会话 key 绑定到 uid（幂等覆盖旧值），并异步镜像到 redisstore。
// 供"粘性跟随最终成功号"用：请求成功返回前，把会话重绑到实际成功的账号，让多轮对话下一跳稳定
// 收敛到"对该会话持续成功的号"（对齐 antigravity 语义）。同时把 uid 记成这段对话最近用过的号。
// 空 key 直接返回（无会话则不绑）。
func (r *Router) Bind(key, uid string) {
	if key == "" || uid == "" {
		return
	}
	now := time.Now()
	r.mu.Lock()
	r.entries[key] = entry{uid: uid, lastActive: now}
	r.rememberLocked(key, uid, now)
	r.mu.Unlock()
	r.cfg.Store.SetBind(key, uid, r.cfg.TTL)
}

// rememberLocked 把 uid 排到这段对话用过的号的最前面。只有名单变了或时刻跨了整点才算改动
// （同 convStore.touchLocked）。
func (r *Router) rememberLocked(key, uid string, now time.Time) {
	k := sha256.Sum256([]byte(key))
	e, ok := r.warm[k]
	if ok && now.Sub(e.last) >= warmTTL {
		e = warmEntry{}
	}
	if len(e.uids) > 0 && e.uids[0] == uid {
		if !sameHour(e.last, now) {
			r.warmDirty = true
		}
		e.last = now
		r.warm[k] = e
		return
	}
	uids := make([]string, 0, warmMax)
	uids = append(uids, uid)
	for _, u := range e.uids {
		if u != uid && len(uids) < warmMax {
			uids = append(uids, u)
		}
	}
	if !ok && len(r.warm) >= warmKeysMax {
		evictStale(r.warm, func(e warmEntry) time.Time { return e.last }, now, warmTTL, warmKeysMax)
	}
	r.warm[k] = warmEntry{uids: uids, last: now}
	r.warmDirty = true
}

// warmPickLocked 这段对话用过的号里，按最近用过的先后挑第一个还在 available 里的；没有返回 ""。
func (r *Router) warmPickLocked(key string, available map[string]bool, now time.Time) string {
	e, ok := r.warm[sha256.Sum256([]byte(key))]
	if !ok || now.Sub(e.last) >= warmTTL {
		return ""
	}
	for _, u := range e.uids {
		if available[u] {
			return u
		}
	}
	return ""
}

// WarmUIDs 这段对话最近成功用过的号（最近的在前），供换号重试时先回到它们。
func (r *Router) WarmUIDs(key string) []string {
	if key == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.warm[sha256.Sum256([]byte(key))]
	if !ok || time.Since(e.last) >= warmTTL {
		return nil
	}
	return slices.Clone(e.uids)
}

// Unbind 解除会话绑定（请求失败时调用，让该会话下次重新分配）。返回是否存在。
func (r *Router) Unbind(key string) bool {
	r.mu.Lock()
	_, found := r.entries[key]
	if found {
		delete(r.entries, key)
	}
	r.mu.Unlock()
	if found {
		r.cfg.Store.DelBind(key)
	}
	return found
}

// Count 返回当前绑定数（供 /status 观测）。
func (r *Router) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// gcOnce 清理 TTL 过期的绑定，并镜像删除。
func (r *Router) gcOnce(now time.Time) int {
	r.mu.Lock()
	var expiredKeys []string
	for key, e := range r.entries {
		if now.Sub(e.lastActive) > r.cfg.TTL {
			expiredKeys = append(expiredKeys, key)
		}
	}
	for _, key := range expiredKeys {
		delete(r.entries, key)
	}
	for k, e := range r.warm {
		if now.Sub(e.last) >= warmTTL {
			delete(r.warm, k)
		}
	}
	r.mu.Unlock()
	for _, key := range expiredKeys {
		r.cfg.Store.DelBind(key)
	}
	return len(expiredKeys)
}

// warmSnap 存盘的一段对话用过的号。
type warmSnap struct {
	K string   `json:"k"` // 会话键的 sha256（base64url）
	U []string `json:"u"` // 用过的号，最近的在前
	T int64    `json:"t"` // 最后一次成功（Unix 秒）
}

// takeWarmDirty 返回用过的号有没有没存盘的改动，并清掉标记。nil 路由器（粘性关闭）恒为 false。
func (r *Router) takeWarmDirty() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.warmDirty
	r.warmDirty = false
	return d
}

func (r *Router) markWarmDirty() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.warmDirty = true
	r.mu.Unlock()
}

// warmSnapshot 没过期的用过的号。nil 路由器返回 nil。
func (r *Router) warmSnapshot() []warmSnap {
	if r == nil {
		return nil
	}
	now := time.Now()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]warmSnap, 0, len(r.warm))
	for k, e := range r.warm {
		if now.Sub(e.last) >= warmTTL {
			continue
		}
		out = append(out, warmSnap{K: base64.RawURLEncoding.EncodeToString(k[:]), U: slices.Clone(e.uids), T: e.last.Unix()})
	}
	return out
}

// restoreWarm 读回存盘的用过的号：跳过过期和格式不对的，内存里已有的以内存为准。返回读回的条数。
func (r *Router) restoreWarm(recs []warmSnap) int {
	if r == nil {
		return 0
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rec := range recs {
		k, ok := decodeKey(rec.K)
		last := time.Unix(rec.T, 0)
		if !ok || len(rec.U) == 0 || now.Sub(last) >= warmTTL {
			continue
		}
		if _, exists := r.warm[k]; exists {
			continue
		}
		if len(r.warm) >= warmKeysMax {
			break
		}
		uids := rec.U
		if len(uids) > warmMax {
			uids = uids[:warmMax]
		}
		r.warm[k] = warmEntry{uids: slices.Clone(uids), last: last}
		n++
	}
	return n
}

// availableSet 把 AvailableForModel(model, realm, skip) 的有序列表转集合（快路径命中校验用）。
func (r *Router) availableSet(model, realm string, skip map[string]bool) map[string]bool {
	uids := r.availableSlice(model, realm, skip)
	set := make(map[string]bool, len(uids))
	for _, u := range uids {
		set[u] = true
	}
	return set
}

// assignableSlice 分配新绑定用的候选：注入了 AssignableForModel 就用它，否则同 availableSlice。
func (r *Router) assignableSlice(model, realm string, skip map[string]bool) []string {
	if r.cfg.AssignableForModel != nil {
		return r.cfg.AssignableForModel(model, realm, skip)
	}
	return r.availableSlice(model, realm, skip)
}

// availableSlice 安全调用 AvailableForModel；未注入时回落 Available（nil 视空池，不看 skip：只有测试不注入）。
func (r *Router) availableSlice(model, realm string, skip map[string]bool) []string {
	if r.cfg.AvailableForModel != nil {
		return r.cfg.AvailableForModel(model, realm, skip)
	}
	if r.cfg.Available == nil {
		return nil
	}
	return r.cfg.Available()
}

func expired(e entry, now time.Time, ttl time.Duration) bool {
	return now.Sub(e.lastActive) > ttl
}

// hashIndex FNV-1a 哈希取模（antigravity 双段分配的稳定散列）。
func hashIndex(key string, n int) int {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return int(h % uint32(n))
}

// ExtractKey 从请求体提取会话键；按下列顺序依次尝试，找不到时回退到
// 内容派生的稳定键（见 deriveKey），仍为空则返回空串（绝不失败）。
//  1. metadata.conversation_id
//  2. metadata.conversationId
//  3. conversation_id
//  4. conversationId
//  5. prompt_cache_key（OpenAI 前缀缓存键，见下）
//  6. 派生键：system 提示词 + 首条用户消息的哈希（客户端不发会话 id 时的回退，
//     带 user_id 的请求不派生——见下方契约说明）
//
// 前 5 项均为 conversation 维度（对话级）。metadata.user_id 不再作为粘性键
// （P1-anti-monopoly 剔除）：user 维度粒度过粗——一个 user 的全部并行对话
// 会钉同一账号（粘性范围远大于上游 prompt cache 的对话级边界），且曾抢占顶层
// conversation_id 的优先级。剔除后发 user_id 的客户端回落加权轮换（与无标识
// 客户端同路径），旧 user_id 绑定靠 TTL 自然过期，键消失不产生脏绑定。
//
// issue #35：客户端实际发 camelCase 的 conversationId，此前只识别 snake_case，
// 导致粘性路由不命中、同对话轮转不同账号、上游上下文缓存 miss。现两种命名均识别，
// snake_case 优先级高于 camelCase（同值不同名命中同一对话时返回相同值，天然不混用）。
//
// 第 5 项 prompt_cache_key：pi-ai 驱动的客户端（dsh 等）把会话 ID 放在这个 OpenAI
// 前缀缓存字段里（而非 conversation_id），网关在 upstream 侧本就认它（见
// InjectPromptCacheKey 优先级 1：客户端自带则原值保留）。纳入识别后，这类客户端
// 无需改配置即可命中粘性。置于显式会话键之后、派生键之前，绝不抢占 conversation
// 维度的优先级。
//
// 派生键的 user_id 抑制（P1-anti-monopoly 契约在 fallback 路径的延伸）：body 携带
// metadata.user_id 或顶层 user_id 时**不派生**。ExtractKey 有意剔除 user_id 作粘性键
// （user 维度粒度过粗——一个 user 的全部并行对话会被钉到同一账号），若派生路径不设
// 此闸，只发 user_id 的请求会借内容哈希重新获得粘性，使该契约在 fallback 路径失效
// （上游 #169 同款回归修正）。这类客户端回落加权轮换。
func ExtractKey(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return ""
	}
	if meta, ok := obj["metadata"].(map[string]any); ok {
		if v := strOrEmpty(meta["conversation_id"]); v != "" {
			return v
		}
		if v := strOrEmpty(meta["conversationId"]); v != "" {
			return v
		}
	}
	if v := strOrEmpty(obj["conversation_id"]); v != "" {
		return v
	}
	if v := strOrEmpty(obj["conversationId"]); v != "" {
		return v
	}
	// 5. prompt_cache_key：OpenAI 系的会话级前缀缓存键，语义就是"同一会话复用同一
	//    前缀"，与粘性诉求同源。放显式会话键之后、派生键之前，不抢占优先级。
	if v := strOrEmpty(obj["prompt_cache_key"]); v != "" {
		return v
	}
	// 派生回退闸：带 user 维度标识的请求不派生（契约见上）。
	if hasUserIDKey(obj) {
		return ""
	}
	return deriveKey(obj)
}

// hasUserIDKey 报告已解析 body 是否携带 user 维度标识（metadata.user_id 或顶层
// user_id，非空字符串才算）。只用于派生回退闸（ExtractKey）；解析失败按无处理。
func hasUserIDKey(obj map[string]any) bool {
	if meta, ok := obj["metadata"].(map[string]any); ok {
		if strOrEmpty(meta["user_id"]) != "" {
			return true
		}
	}
	return strOrEmpty(obj["user_id"]) != ""
}

// derivedKeyPrefix 派生键前缀，与显式会话 id 的命名空间隔离：
// 即便客户端恰好传了形如 "d-<hex>" 的显式 id 也不至于与派生键混淆（显式 id 优先返回）。
const derivedKeyPrefix = "d-"

// deriveKey 从消息内容派生稳定会话键：SHA-256(system 文本 + 首条 user 文本) 前 16 字节。
//
// 为什么用「system + 首条 user」而不是全部消息：
//   - 多轮对话里历史消息每轮追加，全量哈希会每轮变化 → 粘性完全失效；
//   - system 与首条 user 在一次对话中恒定，足以区分不同对话；
//   - 同一会话多轮请求 → 同一键 → 稳定粘住同一账号（上游 prompt 缓存命中）。
//
// 取不到用户文本（纯图片等）时返回空串：不粘性，退回普通轮换（安全降级）。
func deriveKey(obj map[string]any) string {
	msgs, ok := obj["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return ""
	}
	systemText, firstUserText := "", ""
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		text := messageText(msg["content"])
		switch strOrEmpty(msg["role"]) {
		case "system", "developer":
			if systemText == "" {
				systemText = text
			}
		case "user":
			if firstUserText == "" {
				// 首条 user 用内容签名（ids.go contentSignature）而非纯文本提取：
				// 纯图片轮（无 text part）在 messageText 下恒空串 → 派生键失效
				//（首图会话的粘性盲区）；签名口径下图片 part 以 [type:摘要] 入键。
				firstUserText = userContentSignature(msg["content"])
			}
		}
		if firstUserText != "" && systemText != "" {
			break // 都已拿到：停止遍历长历史
		}
	}
	if firstUserText == "" {
		return "" // 无用户消息：无从归属会话
	}
	sum := sha256.Sum256([]byte(systemText + "\x00" + firstUserText))
	return derivedKeyPrefix + hex.EncodeToString(sum[:16])
}

// userContentSignature 把已解析的 content（any）转回 RawMessage 后取**会话级稳定**
// 签名，供 deriveKey（粘性键）专用。与 TurnKey 的 contentSignature（全文摘）不同：
// 文本 part 照常拼接；非文本 part 只入 "[type]" 占位**不入内容摘要**——同一逻辑
// 图片的 URL 逐轮变化（签名 URL/重编码 base64）是真实场景，内容摘要入键会让
// 派生键随轮漂移、粘性名存实亡（fork 既有契约：image url changes must not
// break derived key stability）。占位仍能区分"有无图片/图片数量"，纯图片首条
// 消息（无 text part）由此可派生非空键（首图会话粘性盲区修复，对齐上游 G1
// 的目标语义而保留 fork 的稳定性口径）。marshal 失败按无内容处理（不伪造）。
func userContentSignature(content any) string {
	raw, err := json.Marshal(content)
	if err != nil {
		return ""
	}
	st := strings.TrimSpace(string(raw))
	if st == "" || st == "null" {
		return ""
	}
	if st[0] == '"' {
		var str string
		if json.Unmarshal(raw, &str) != nil {
			return ""
		}
		return str
	}
	if st[0] != '[' {
		return ""
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	hasNonText := false
	for _, pr := range parts {
		var p struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(pr, &p) != nil {
			return ""
		}
		if p.Type == "" || p.Type == "text" {
			b.WriteString(p.Text)
			continue
		}
		hasNonText = true
		b.WriteString("\n[" + p.Type + "]\n")
	}
	out := b.String()
	if !hasNonText {
		return out
	}
	return strings.TrimSpace(out)
}

// messageText 提取消息 content 的文本表示。
// 兼容：字符串 / [{type:"text",text:"..."}] 数组（OpenAI 多模态）；其他类型取空。
func messageText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, part := range v {
			if p, ok := part.(map[string]any); ok {
				sb.WriteString(strOrEmpty(p["text"]))
			}
		}
		return sb.String()
	}
	return ""
}

// strOrEmpty 把 JSON 字符串字段安全转 string（非字符串类型返回空）。
func strOrEmpty(v any) string {
	s, _ := v.(string)
	return s
}
