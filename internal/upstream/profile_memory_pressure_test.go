// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestSessionMemoryEventsPath(t *testing.T) {
	for _, tc := range []struct{ membership, want string }{
		{"0::/\n", "/sys/fs/cgroup/memory.events"},
		{"0::/system.slice/wb2api.service\n", "/sys/fs/cgroup/system.slice/wb2api.service/memory.events"},
		{"9:memory:/a\n", ""},
		{"0::/../outside\n", ""},
	} {
		if got := sessionMemoryEventsPath(tc.membership); got != tc.want {
			t.Errorf("cgroup %q 的事件文件为 %q，期望 %q", tc.membership, got, tc.want)
		}
	}
}

func TestSessionMemoryPressureWatchUpdatesExistingAndNewRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.events")
	write := func(high, max int) {
		t.Helper()
		if err := os.WriteFile(path, fmt.Appendf(nil, "low 0\nhigh %d\nmax %d\noom 0\n", high, max), 0600); err != nil {
			t.Fatal(err)
		}
	}
	synctest.Test(t, func(t *testing.T) {
		routes := newRouteTable(newTransport(nil))
		open := func(ip string) *profileSessionCache {
			t.Helper()
			tr, err := routes.transportForProfile("", ip, ProfileTraeWork)
			if err != nil {
				t.Fatal(err)
			}
			return tr.(*profileHTTP2).sessions
		}
		existing := open("")
		capacity := func(cache *profileSessionCache) int {
			cache.mu.Lock()
			defer cache.mu.Unlock()
			return cache.capacity
		}
		write(10, 20)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- routes.watchSessionMemoryEvents(ctx, path, time.Second, 20*time.Second) }()
		synctest.Wait()
		time.Sleep(time.Second + time.Millisecond)
		synctest.Wait()
		if capacity(existing) != 1024 {
			t.Fatal("启动前的历史压力被重复处理")
		}
		write(11, 20)
		time.Sleep(time.Second)
		synctest.Wait()
		if capacity(existing) != 512 {
			t.Fatal("high 增长后容量没有缩至 512")
		}
		write(12, 21)
		time.Sleep(time.Second)
		synctest.Wait()
		newRoute := open("192.0.2.1")
		if capacity(existing) != 0 || capacity(newRoute) != 0 {
			t.Fatal("high 与 max 同时增长时既有和新线路应处于严重压力")
		}
		write(13, 21)
		time.Sleep(time.Second)
		synctest.Wait()
		if capacity(existing) != 512 || capacity(newRoute) != 512 {
			t.Fatal("中度压力没有恢复一半容量")
		}
		time.Sleep(19 * time.Second)
		synctest.Wait()
		if capacity(existing) != 512 {
			t.Fatal("静默期结束前已恢复容量")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if capacity(existing) != 1024 || capacity(newRoute) != 1024 {
			t.Fatal("静默期结束后未恢复正常容量")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
