// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"container/list"
	"context"
	"reflect"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
)

const (
	profileSessionCacheCapacity     = 1024
	profileSessionSweepInterval     = 256
	profileTLS12LifetimeSeconds     = 60 * 60
	profileTLS13LifetimeSeconds     = 2 * 24 * 60 * 60
	profileTLS13AuthLifetimeSeconds = 7 * 24 * 60 * 60
)

type profileSessionRequestKey struct{}

type profileSessionRequest struct {
	origin string
	ctx    context.Context
}

// profileSessionCache 保留实测 Chromium 的 1024 端点 LRU 与两张单次 TLS 1.3 票据。
// uTLS 的默认缓存只有一张可重复使用票据，不能直接表达该策略。
type profileSessionCache struct {
	mu           sync.Mutex
	entries      map[string]*list.Element
	order        list.List
	capacity     int
	generation   uint64
	lastRevision uint64
	lookups      int
	now          func() time.Time
}

type profileSessionEntry struct {
	origin   string
	tickets  [2]profileSessionTicket
	revision uint64
}

type profileSessionTicket struct {
	state                *utls.ClientSessionState
	createdAt, expiresAt int64
	authenticatedAt      int64
}

func (t profileSessionTicket) expired(now int64) bool {
	return t.state == nil || now < 0 || now < t.createdAt-1 || now >= t.expiresAt
}

func (c *profileSessionCacheView) ticketWithLifetime(ticket *utls.ClientSessionState) profileSessionTicket {
	_, state, err := ticket.ResumptionState()
	if err != nil || state == nil {
		return profileSessionTicket{}
	}
	// uTLS v1.8.2 未公开这两个时间。只读整数，避免序列化票据的密钥与证书；
	// 依赖升级若不再提供这些字段，回到完整握手，相关测试会检查字段与期限。
	fields := reflect.ValueOf(state).Elem()
	created := fields.FieldByName("createdAt")
	deadline := fields.FieldByName("useBy")
	if created.Kind() != reflect.Uint64 || deadline.Kind() != reflect.Uint64 {
		return profileSessionTicket{}
	}
	issued := int64(created.Uint())
	localDeadline := c.authenticatedAt + profileTLS12LifetimeSeconds
	expires := localDeadline
	if ticket.Vers() == utls.VersionTLS13 {
		// 服务端期限从出票起算，本地可续期窗口从本次握手起算；连续恢复沿用首次认证期限。
		localDeadline = min(c.handshakeAt+profileTLS13LifetimeSeconds, c.authenticatedAt+profileTLS13AuthLifetimeSeconds)
		expires = min(int64(deadline.Uint()), localDeadline)
	}
	if issued >= localDeadline {
		return profileSessionTicket{}
	}
	return profileSessionTicket{state: ticket, createdAt: issued, expiresAt: expires, authenticatedAt: c.authenticatedAt}
}

func newProfileSessionCache() *profileSessionCache {
	return &profileSessionCache{
		entries: make(map[string]*list.Element), capacity: profileSessionCacheCapacity, now: time.Now,
	}
}

func (c *profileSessionCache) applyPressure(profile ClientProfile, level sessionMemoryPressure) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if profile == ProfileTraeWork {
		switch level {
		case sessionPressureNormal:
			c.capacity = profileSessionCacheCapacity
		case sessionPressureModerate:
			c.capacity = profileSessionCacheCapacity / 2
		case sessionPressureCritical:
			c.entries = make(map[string]*list.Element)
			c.order.Init()
			c.capacity = 0
		}
		for c.order.Len() > c.capacity {
			c.removeLocked(c.order.Back())
		}
		return
	}
	switch level {
	case sessionPressureModerate:
		c.sweepExpiredLocked(c.now().Unix())
	case sessionPressureCritical:
		if profile == ProfileTraeCode {
			// Linux TTNet 在 cgroup 硬限制回收后仍用原票据恢复两个端点。
			c.sweepExpiredLocked(c.now().Unix())
			return
		}
		c.entries = make(map[string]*list.Element)
		c.order.Init()
		// Auth 的 Chromium 142 在清空后拒收此前握手产生的迟到票据。
		c.generation++
	}
}

func (c *profileSessionCache) newView(origin string, skipLookup bool) *profileSessionCacheView {
	c.mu.Lock()
	defer c.mu.Unlock()
	start := c.now().Unix()
	return &profileSessionCacheView{
		cache: c, origin: origin, handshaking: true, skipLookup: skipLookup, generation: c.generation,
		handshakeAt: start, authenticatedAt: start,
	}
}

func (c *profileSessionCache) take(origin string) (profileSessionTicket, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().Unix()
	c.lookups++
	if c.lookups == profileSessionSweepInterval {
		c.lookups = 0
		c.sweepExpiredLocked(now)
	}
	e, ok := c.entries[origin]
	if !ok {
		return profileSessionTicket{}, 0
	}
	entry := e.Value.(*profileSessionEntry)
	c.order.MoveToFront(e)
	ticket := entry.tickets[0]
	if ticket.state.Vers() == utls.VersionTLS13 {
		entry.tickets = [2]profileSessionTicket{entry.tickets[1], {}}
	}
	c.expireEntryLocked(e, now)
	if ticket.expired(now) {
		return profileSessionTicket{}, 0
	}
	return ticket, entry.revision
}

func (c *profileSessionCache) store(origin string, cached profileSessionTicket, generation uint64) {
	if cached.state == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.capacity == 0 || generation != c.generation {
		return
	}
	e, ok := c.entries[origin]
	if !ok {
		if c.order.Len() == c.capacity {
			c.removeLocked(c.order.Back())
		}
		e = c.order.PushFront(&profileSessionEntry{origin: origin})
		c.entries[origin] = e
	}
	entry := e.Value.(*profileSessionEntry)
	if primary := entry.tickets[0]; primary.state != nil && primary.state.Vers() == utls.VersionTLS13 {
		entry.tickets[1] = primary
	}
	entry.tickets[0] = cached
	c.lastRevision++
	entry.revision = c.lastRevision
	c.order.MoveToFront(e)
}

func (c *profileSessionCache) invalidate(origin string, revision uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 握手完成的顺序可能与读取票据的顺序不同。旧连接只能清理它读过的版本。
	if e := c.entries[origin]; e != nil && e.Value.(*profileSessionEntry).revision == revision {
		c.removeLocked(e)
	}
}

func (c *profileSessionCache) expireEntryLocked(e *list.Element, now int64) {
	entry := e.Value.(*profileSessionEntry)
	if entry.tickets[0].expired(now) {
		c.removeLocked(e)
		return
	}
	if entry.tickets[1].expired(now) {
		entry.tickets[1] = profileSessionTicket{}
	}
}

func (c *profileSessionCache) sweepExpiredLocked(now int64) {
	for e := c.order.Front(); e != nil; {
		next := e.Next()
		c.expireEntryLocked(e, now)
		e = next
	}
}

func (c *profileSessionCache) removeLocked(e *list.Element) {
	delete(c.entries, e.Value.(*profileSessionEntry).origin)
	c.order.Remove(e)
}

// profileSessionCacheView 固定当前连接的原始主机和端口，避免 uTLS 只按 SNI 查找。
type profileSessionCacheView struct {
	cache           *profileSessionCache
	origin          string
	selected        profileSessionTicket
	handshakeAt     int64
	authenticatedAt int64
	revision        uint64
	generation      uint64
	handshaking     bool
	invalidated     bool
	skipLookup      bool
}

func (c *profileSessionCacheView) Get(_ string) (*utls.ClientSessionState, bool) {
	if c.skipLookup {
		return nil, false
	}
	c.selected, c.revision = c.cache.take(c.origin)
	return c.selected.state, c.selected.state != nil
}

func (c *profileSessionCacheView) Put(_ string, ticket *utls.ClientSessionState) {
	if ticket == nil {
		if c.handshaking {
			// uTLS 将过期与握手失败都表示为删除；结束后按该 profile 的实测策略处理。
			c.invalidated = true
		} else {
			c.cache.invalidate(c.origin, c.revision)
		}
		return
	}
	c.invalidated = false
	c.cache.store(c.origin, c.ticketWithLifetime(ticket), c.generation)
}

func (c *profileSessionCacheView) finishHandshake(preserve bool) {
	c.handshaking = false
	if c.invalidated && !preserve {
		c.cache.invalidate(c.origin, c.revision)
	}
}
