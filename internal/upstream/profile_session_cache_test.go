// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"fmt"
	"sync"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

func profileSessionFixture(id byte, version uint16) *utls.ClientSessionState {
	ticket := utls.MakeClientSessionState([]byte{id}, version, 0, nil, nil, nil)
	now := uint64(time.Now().Unix())
	ticket.SetCreatedAt(now)
	ticket.SetUseBy(now + 3600)
	return ticket
}

func TestProfileSessionCacheTicketConsumption(t *testing.T) {
	for _, version := range []uint16{utls.VersionTLS12, utls.VersionTLS13} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			cache := newProfileSessionCache()
			var tickets []*utls.ClientSessionState
			for i := range 5 {
				ticket := profileSessionFixture(byte(i), version)
				tickets = append(tickets, ticket)
				cache.newView("fixture.invalid:443", false).Put("", ticket)
			}
			for i := range 5 {
				ticket, _ := cache.newView("fixture.invalid:443", false).Get("")
				if version == utls.VersionTLS12 {
					if ticket != tickets[4] {
						t.Fatal("TLS 1.2 没有持续复用最新票据")
					}
				} else if i < 2 {
					if ticket != tickets[4-i] {
						t.Fatal("TLS 1.3 没有只保留并依次消费最新两张票据")
					}
				} else if ticket != nil {
					t.Fatal("TLS 1.3 重复使用票据，或保存了两张以上的票据")
				}
			}
			_, revision := cache.take("fixture.invalid:443")
			cache.invalidate("fixture.invalid:443", revision)
			if ticket, _ := cache.newView("fixture.invalid:443", false).Get(""); ticket != nil {
				t.Fatal("失效票据仍留在缓存")
			}
		})
	}
}

func TestProfileSessionCacheLRUEviction(t *testing.T) {
	cache := newProfileSessionCache()
	for i := range 1024 {
		cache.newView(fmt.Sprint(i), false).Put("", profileSessionFixture(1, utls.VersionTLS12))
	}
	if ticket, _ := cache.newView("0", false).Get(""); ticket == nil {
		t.Fatal("1024 个端点未填满前已淘汰票据")
	}
	cache.newView("1024", false).Put("", profileSessionFixture(2, utls.VersionTLS12))
	if ticket, _ := cache.newView("1", false).Get(""); ticket != nil {
		t.Fatal("插入第 1025 个端点后未淘汰最久未用条目")
	}
	if ticket, _ := cache.newView("0", false).Get(""); ticket == nil {
		t.Fatal("最近访问的条目被淘汰")
	}
}

func TestProfileSessionMemoryPressure(t *testing.T) {
	now := time.Unix(1800000000, 0)
	ticket := profileSessionFixture(1, utls.VersionTLS13)
	ticket.SetCreatedAt(uint64(now.Unix()))
	ticket.SetUseBy(uint64(now.Add(time.Hour).Unix()))
	expired := profileSessionFixture(2, utls.VersionTLS13)
	expired.SetCreatedAt(uint64(now.Add(-time.Hour).Unix()))
	expired.SetUseBy(uint64(now.Unix()))
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		t.Run(httpProfileName(profile, false), func(t *testing.T) {
			cache := newProfileSessionCache()
			cache.now = func() time.Time { return now }
			for i := range 513 {
				cache.newView(fmt.Sprint(i), false).Put("", ticket)
			}
			cache.newView("expired", false).Put("", expired)
			late := cache.newView("late", false)
			late.finishHandshake(false)
			cache.applyPressure(profile, sessionPressureModerate)
			if profile == ProfileTraeWork {
				if len(cache.entries) != 512 || cache.entries["1"] != nil || cache.entries["2"] == nil {
					t.Fatal("Work 中度压力没有按 LRU 缩至 512 项")
				}
			} else if len(cache.entries) != 513 || cache.entries["expired"] != nil {
				t.Fatal("Auth／Code 中度压力应清扫过期票据并保留新鲜端点")
			}
			cache.newView("expired-again", false).Put("", expired)
			cache.applyPressure(profile, sessionPressureCritical)
			got, _ := cache.newView("512", false).Get("")
			if profile == ProfileTraeCode {
				if got != ticket || cache.entries["expired-again"] != nil || cache.capacity != profileSessionCacheCapacity {
					t.Fatal("Code 在 cgroup 硬限制回收后应保留有效票据、清理过期项并保持容量上限")
				}
			} else if got != nil || len(cache.entries) != 0 {
				t.Fatal("严重压力后仍能取到旧票据")
			}
			cache.newView("under-pressure", false).Put("", ticket)
			if got, _ := cache.newView("under-pressure", false).Get(""); (got == nil) != (profile == ProfileTraeWork) {
				t.Fatal("严重压力期间的新连接缓存行为错误")
			}
			cache.applyPressure(profile, sessionPressureModerate)
			late.Put("", ticket)
			if got, _ := cache.newView("late", false).Get(""); (got == nil) != (profile == ProfileTraeAuth) {
				t.Fatal("旧连接在压力下降后的迟到票据行为错误")
			}
			cache.newView("recovered", false).Put("", ticket)
			if got, _ := cache.newView("recovered", false).Get(""); got != ticket {
				t.Fatal("压力下降后未缓存新连接的票据")
			}
			cache.applyPressure(profile, sessionPressureNormal)
			if cache.capacity != 1024 {
				t.Fatal("压力解除后未恢复正常容量")
			}
		})
	}
}

func TestProfileSessionCacheSweepsExpiredOriginsBeforeEviction(t *testing.T) {
	cache := newProfileSessionCache()
	now := uint64(time.Now().Unix())
	valid := profileSessionFixture(1, utls.VersionTLS13)
	valid.SetCreatedAt(now - 60)
	valid.SetUseBy(now + 3600)
	short := profileSessionFixture(2, utls.VersionTLS13)
	short.SetCreatedAt(now - 60)
	short.SetUseBy(now - 30)
	cache.newView("old-live", false).Put("", valid)
	for i := range 16 {
		cache.newView(fmt.Sprintf("short-%d", i), false).Put("", short)
	}
	for range 255 {
		cache.take("missing")
	}
	if got := len(cache.entries); got != 17 {
		t.Fatalf("第 256 次查找之前清扫了其他端点：还剩 %d 项", got)
	}
	cache.take("missing")
	if got := len(cache.entries); got != 1 {
		t.Errorf("第 256 次查找后仍有过期端点：缓存有 %d 项", got)
	}
	for i := range 1023 {
		cache.newView(fmt.Sprintf("new-%d", i), false).Put("", valid)
	}
	if ticket, _ := cache.newView("old-live", false).Get(""); ticket != valid {
		t.Fatal("过期端点占满容量，挤掉了最早但仍有效的票据")
	}
}

func TestProfileSessionCacheExpiresTicketsInLookupOrder(t *testing.T) {
	cache := newProfileSessionCache()
	now := uint64(time.Now().Unix())
	valid := profileSessionFixture(1, utls.VersionTLS13)
	valid.SetCreatedAt(now - 60)
	valid.SetUseBy(now + 3600)
	expired := profileSessionFixture(2, utls.VersionTLS13)
	expired.SetCreatedAt(now - 60)
	expired.SetUseBy(now - 30)
	cache.newView("old-primary", false).Put("", valid)
	cache.newView("old-primary", false).Put("", expired)
	if ticket, _ := cache.newView("old-primary", false).Get(""); ticket != nil {
		t.Fatal("第一次查询应消费过期票据，不能直接复用较旧的票据")
	}
	if ticket, _ := cache.newView("old-primary", false).Get(""); ticket != valid {
		t.Fatal("下一次查询应返回保留的有效票据")
	}
	cache.newView("old-backup", false).Put("", expired)
	cache.newView("old-backup", false).Put("", valid)
	if ticket, _ := cache.newView("old-backup", false).Get(""); ticket != valid {
		t.Fatal("备用票据过期不应影响仍有效的首张票据")
	}
	if _, ok := cache.entries["old-backup"]; ok {
		t.Fatal("已用掉有效票据的端点仍保留过期备用票据")
	}
	cache.newView("swept-primary", false).Put("", valid)
	cache.newView("swept-primary", false).Put("", expired)
	for range 252 {
		cache.take("missing")
	}
	if ticket, _ := cache.newView("swept-primary", false).Get(""); ticket != nil {
		t.Fatal("全局清扫应移除首张票据过期的整个端点")
	}
}

func TestProfileSessionCacheRespectsClientLifetime(t *testing.T) {
	now := time.Unix(1800000000, 0)
	nowSeconds := uint64(now.Unix())
	for _, tc := range []struct {
		name      string
		version   uint16
		age       int64
		remaining int64
		live      bool
	}{
		{"TLS 1.2 忽略四秒服务端提示", utls.VersionTLS12, 10, -6, true},
		{"TLS 1.2 一小时前一秒", utls.VersionTLS12, 3599, 3600, true},
		{"TLS 1.2 一小时到期", utls.VersionTLS12, 3600, 3600, false},
		{"TLS 1.3 服务端期限前一秒", utls.VersionTLS13, 3, 1, true},
		{"TLS 1.3 到达服务端期限", utls.VersionTLS13, 4, 0, false},
		{"TLS 1.3 两天前一秒", utls.VersionTLS13, 2*24*3600 - 1, 3600, true},
		{"TLS 1.3 两天到期", utls.VersionTLS13, 2 * 24 * 3600, 3600, false},
		{"时钟回退一秒", utls.VersionTLS13, -1, 3600, true},
		{"时钟回退两秒", utls.VersionTLS13, -2, 3600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := newProfileSessionCache()
			clock := time.Unix(now.Unix()-tc.age, 0)
			cache.now = func() time.Time { return clock }
			ticket := profileSessionFixture(1, tc.version)
			ticket.SetCreatedAt(uint64(int64(nowSeconds) - tc.age))
			ticket.SetUseBy(uint64(int64(nowSeconds) + tc.remaining))
			cache.newView("fixture", false).Put("", ticket)
			clock = now
			got, _ := cache.newView("fixture", false).Get("")
			if (got != nil) != tc.live {
				t.Fatalf("票据可用=%t，预期 %t", got != nil, tc.live)
			}
		})
	}
}

func TestProfileSessionCacheDelayedTicketKeepsHandshakeDeadline(t *testing.T) {
	const origin = "fixture.invalid:443"
	now := time.Unix(1800000000, 0)
	cache := newProfileSessionCache()
	cache.now = func() time.Time { return now }
	old := cache.newView(origin, false)
	now = now.Add(24 * time.Hour)
	ticket := profileSessionFixture(1, utls.VersionTLS13)
	ticket.SetCreatedAt(uint64(now.Unix()))
	ticket.SetUseBy(uint64(now.Add(7 * 24 * time.Hour).Unix()))
	old.Put("", ticket)
	now = now.Add(24 * time.Hour)
	if _, ok := cache.newView(origin, false).Get(""); ok {
		t.Fatal("已在首次握手后经过两天，晚到票据仍可恢复")
	}

	fresh := profileSessionFixture(2, utls.VersionTLS13)
	fresh.SetCreatedAt(uint64(now.Unix()))
	fresh.SetUseBy(uint64(now.Add(time.Hour).Unix()))
	cache.newView(origin, false).Put("", fresh)
	ticket.SetCreatedAt(uint64(now.Unix()))
	old.Put("", ticket)
	if got, _ := cache.newView(origin, false).Get(""); got != fresh {
		t.Fatal("已到期连接的迟到票据覆盖了刚完成认证的连接")
	}
}

func TestProfileSessionCacheConcurrentConsumption(t *testing.T) {
	cache := newProfileSessionCache()
	for i := range 2 {
		cache.newView("fixture.invalid:443", false).Put("", profileSessionFixture(byte(i), utls.VersionTLS13))
	}
	var wg sync.WaitGroup
	used := make(chan *utls.ClientSessionState, 16)
	for range 16 {
		wg.Go(func() {
			if ticket, _ := cache.newView("fixture.invalid:443", false).Get(""); ticket != nil {
				used <- ticket
			}
		})
	}
	wg.Wait()
	close(used)
	seen := map[*utls.ClientSessionState]bool{}
	for ticket := range used {
		if seen[ticket] {
			t.Fatal("并发连接重复使用同一 TLS 1.3 票据")
		}
		seen[ticket] = true
	}
	if len(seen) != 2 {
		t.Fatalf("实际消费 %d 张票据，期望两张", len(seen))
	}
}

func TestProfileSessionCacheHandshakeInvalidation(t *testing.T) {
	for _, version := range []uint16{utls.VersionTLS12, utls.VersionTLS13} {
		for _, preserve := range []bool{false, true} {
			t.Run(fmt.Sprintf("TLS%x/preserve=%t", version, preserve), func(t *testing.T) {
				cache := newProfileSessionCache()
				for i := range 2 {
					cache.newView("fixture:443", false).Put("", profileSessionFixture(byte(i), version))
				}
				view := cache.newView("fixture:443", false)
				if _, ok := view.Get("ignored-sni"); !ok {
					t.Fatal("恢复前没有缓存")
				}
				view.Put("ignored-sni", nil)
				view.finishHandshake(preserve)
				if ticket, _ := cache.newView("fixture:443", false).Get(""); (ticket != nil) != preserve {
					t.Fatal("失败后的端点清理策略不符")
				}
			})
		}
	}
}

func TestProfileSessionCacheNewTicketSurvivesInvalidation(t *testing.T) {
	for _, version := range []uint16{utls.VersionTLS12, utls.VersionTLS13} {
		t.Run(fmt.Sprintf("TLS%x", version), func(t *testing.T) {
			cache := newProfileSessionCache()
			cache.newView("fixture:443", false).Put("", profileSessionFixture(1, version))
			view := cache.newView("fixture:443", false)
			if _, ok := view.Get("ignored-sni"); !ok {
				t.Fatal("没有取得旧票据")
			}
			view.Put("ignored-sni", nil)
			fresh := profileSessionFixture(2, version)
			view.Put("ignored-sni", fresh)
			view.finishHandshake(false)
			if ticket, _ := cache.newView("fixture:443", false).Get(""); ticket != fresh {
				t.Fatal("旧票据失效通知误删了握手中新收到的票据")
			}
		})
	}
}

func TestProfileSessionCacheConcurrentHandshakeKeepsNewTicket(t *testing.T) {
	for _, version := range []uint16{utls.VersionTLS12, utls.VersionTLS13} {
		t.Run(fmt.Sprintf("TLS%x", version), func(t *testing.T) {
			cache := newProfileSessionCache()
			const origin = "fixture:443"
			cache.newView(origin, false).Put("", profileSessionFixture(1, version))
			cache.newView(origin, false).Put("", profileSessionFixture(2, version))
			older := cache.newView(origin, false)
			if _, ok := older.Get(""); !ok {
				t.Fatal("旧握手未取得缓存票据")
			}
			newer := cache.newView(origin, false)
			if _, ok := newer.Get(""); !ok {
				t.Fatal("新握手未取得缓存票据")
			}
			fresh := profileSessionFixture(3, version)
			newer.Put("", fresh)
			newer.finishHandshake(false)
			older.Put("", nil)
			older.finishHandshake(false)
			if ticket, _ := cache.newView(origin, false).Get(""); ticket != fresh {
				t.Fatal("旧连接报错后删除了另一条连接的新票据")
			}
		})
	}
}
