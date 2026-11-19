// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type sessionMemoryPressure uint8

const (
	sessionPressureNormal sessionMemoryPressure = iota
	sessionPressureModerate
	sessionPressureCritical
)

// MonitorSessionMemoryPressure 按当前 Linux cgroup v2 的内存回收事件调整 Trae 会话缓存。
// 网关进程启动一次，ctx 取消后退出。没有可读 cgroup v2 事件的部署沿用正常容量。
func (c *Client) MonitorSessionMemoryPressure(ctx context.Context) {
	if c.routes == nil || runtime.GOOS != "linux" {
		return
	}
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("读取 TLS 会话缓存压力来源: %v", err)
		}
		return
	}
	path := sessionMemoryEventsPath(string(membership))
	if path == "" {
		return
	}
	if err := c.routes.watchSessionMemoryEvents(ctx, path, time.Second, 20*time.Second); err != nil {
		log.Printf("监控 TLS 会话缓存压力: %v", err)
	}
}

func sessionMemoryEventsPath(membership string) string {
	for line := range strings.Lines(membership) {
		if group, ok := strings.CutPrefix(strings.TrimSpace(line), "0::/"); ok {
			if group == "" || filepath.IsLocal(group) {
				return path.Join("/sys/fs/cgroup", group, "memory.events")
			}
		}
	}
	return ""
}

type sessionMemoryEvents struct {
	high, max uint64
}

func readSessionMemoryEvents(path string) (sessionMemoryEvents, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return sessionMemoryEvents{}, err
	}
	var counters sessionMemoryEvents
	var hasHigh, hasMax bool
	for line := range strings.Lines(string(raw)) {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "high":
			counters.high, err = strconv.ParseUint(fields[1], 10, 64)
			hasHigh = true
		case "max":
			counters.max, err = strconv.ParseUint(fields[1], 10, 64)
			hasMax = true
		}
		if err != nil {
			return sessionMemoryEvents{}, fmt.Errorf("解析 %s 的 %s 计数: %w", path, fields[0], err)
		}
	}
	if !hasHigh || !hasMax {
		return sessionMemoryEvents{}, fmt.Errorf("%s 缺少 high 或 max 计数", path)
	}
	return counters, nil
}

func (t *routeTable) watchSessionMemoryEvents(ctx context.Context, path string, interval, quietPeriod time.Duration) error {
	last, err := readSessionMemoryEvents(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer t.applySessionMemoryPressure(sessionPressureNormal)
	var lastPressure time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			current, err := readSessionMemoryEvents(path)
			if err != nil {
				return err
			}
			switch {
			// 计数来自内核的直接回收事件，硬限制优先于软限制。
			case current.max > last.max:
				t.applySessionMemoryPressure(sessionPressureCritical)
				lastPressure = now
			case current.high > last.high:
				t.applySessionMemoryPressure(sessionPressureModerate)
				lastPressure = now
			case !lastPressure.IsZero() && now.Sub(lastPressure) >= quietPeriod:
				t.applySessionMemoryPressure(sessionPressureNormal)
				lastPressure = time.Time{}
			}
			last = current
		}
	}
}

func (t *routeTable) applySessionMemoryPressure(level sessionMemoryPressure) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessionPressure = level
	for _, entry := range t.cache {
		if transport, ok := entry.tr.(*profileHTTP2); ok && transport.sessions != nil {
			transport.sessions.applyPressure(transport.profile, level)
		}
	}
}
