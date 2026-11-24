// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"golang.org/x/net/idna"
)

type qoderMachineInfo struct {
	Token string `json:"machineToken"`
	Type  string `json:"machineType"`
	Code  string `json:"machineCode"`
}

type qoderMachineCache struct {
	Version     int              `json:"version"`
	Environment int              `json:"environment"`
	Platform    string           `json:"platform"`
	Arch        string           `json:"arch"`
	Info        qoderMachineInfo `json:"info"`
	UpdatedAt   time.Time        `json:"updated_at"`
	RefreshAt   time.Time        `json:"refresh_at"`
}

// qoderMachine 串行调用官方组件，避免它的 SDK 状态文件被不同地区同时改写。
// 不启动定时器或常驻进程；有业务请求时按官方的一小时 ± 五分钟周期刷新。
type qoderMachine struct {
	dir          string
	gate         chan struct{}
	cache        map[int]qoderMachineCache
	retryAt      map[int]time.Time
	defaultProxy string
	prepare      func(context.Context, string) (string, error)
	run          func(context.Context, string, int, string) (qoderMachineInfo, error)
	now          func() time.Time
}

type qoderUMIDRouteKey struct{}

type qoderUMIDRoute struct {
	id     string
	name   string
	report func(string, error)
}

// dialQoderUMIDProxy 统一包装 UMID 下载与隧道拨号：绑定代理拨号结果上报 ReportProxy，
// 失败时包装为 *upstream.RouteError，既支持换号容灾又避免向客户端泄露代理地址。
func dialQoderUMIDProxy(ctx context.Context, proxyURL, network, addr string) (net.Conn, error) {
	conn, err := upstream.DialProxyContext(ctx, proxyURL, network, addr)
	route, hasRoute := ctx.Value(qoderUMIDRouteKey{}).(qoderUMIDRoute)
	if err != nil {
		if hasRoute && route.id != "" && route.report != nil && ctx.Err() == nil {
			route.report(route.id, err)
		}
		name := "默认代理"
		if hasRoute && route.name != "" {
			name = route.name
		}
		return nil, &upstream.RouteError{Proxy: name, Err: err}
	}
	if hasRoute && route.id != "" && route.report != nil {
		route.report(route.id, nil)
	}
	return conn, nil
}

// EnableQoderUMID 由服务入口装配；首次 Qoder 请求才取得并运行官方外部组件。
// 该组件不随本仓库或镜像分发，账号文件与组件缓存都放在持久化数据目录。
func (m *Manager) EnableQoderUMID(defaultProxy string) error {
	if m.path == "" {
		return errors.New("Qoder UMID 需要持久化的平台账号文件路径")
	}
	u := &qoderMachine{dir: filepath.Join(filepath.Dir(m.path), "qoder-umid"), gate: make(chan struct{}, 1), cache: map[int]qoderMachineCache{}, retryAt: map[int]time.Time{}, defaultProxy: defaultProxy, now: time.Now}
	u.prepare = func(ctx context.Context, proxyURL string) (string, error) {
		return prepareQoderHelper(ctx, u.dir, proxyURL)
	}
	u.run = func(ctx context.Context, binary string, environment int, proxyURL string) (qoderMachineInfo, error) {
		if proxyURL == "" {
			return runQoderMachine(ctx, binary, environment)
		}
		return runQoderMachineProxy(ctx, binary, environment, func(ctx context.Context, address string) (net.Conn, error) {
			return dialQoderUMIDProxy(ctx, proxyURL, "tcp", address)
		})
	}
	m.qoderMachine = u
	return nil
}

func (m *Manager) qoderIdentity(ctx context.Context, target, proxyID string, c Credential) (Credential, error) {
	if m.qoderMachine == nil || !strings.HasPrefix(target, "qoder:") {
		return c, nil
	}
	// 导入的设备身份继续透传；只有新设备及本实例生成的身份参与本机生命周期。
	if !c.MachineLocal && c.MachineToken != "" && c.MachineToken != c.MachineID {
		return c, nil
	}
	proxyURL := m.qoderMachine.defaultProxy
	if proxyID != "" {
		if m.proxies == nil {
			return c, &upstream.RouteError{Blocked: true, Err: errors.New("Qoder UMID 代理管理未启用")}
		}
		name, route, usable, code := m.proxies.CheckProxy(proxyID)
		if !usable {
			return c, proxyUnavailable(code)
		}
		proxyURL = route
		ctx = context.WithValue(ctx, qoderUMIDRouteKey{}, qoderUMIDRoute{id: proxyID, name: name, report: m.proxies.ReportProxy})
	} else if m.proxies != nil && m.proxies.Settings().RequireBound {
		return c, proxyUnavailable("proxy_required")
	}
	info, err := m.qoderMachine.get(ctx, target, proxyURL)
	if err != nil {
		return c, fmt.Errorf("Qoder 设备身份初始化: %w", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		return c, fmt.Errorf("Qoder 读取设备 hostname: %w", err)
	}
	c.MachineToken, c.MachineType, c.MachineCode = info.Token, info.Type, info.Code
	c.MachineOS = qoderMachineArch() + "_" + goruntime.GOOS
	switch goruntime.GOARCH {
	case "amd64":
		c.MachineOS = "x86_64_" + goruntime.GOOS
	case "arm64":
		c.MachineOS = "aarch64_" + goruntime.GOOS
	}
	c.MachineHostname = qoderHostname(hostname)
	c.MachineLocal = true
	return c, nil
}

func qoderMachineArch() string {
	if goruntime.GOARCH == "amd64" {
		return "x64"
	}
	return goruntime.GOARCH
}

func (u *qoderMachine) get(ctx context.Context, target, proxyURL string) (qoderMachineInfo, error) {
	select {
	case u.gate <- struct{}{}:
		defer func() { <-u.gate }()
	case <-ctx.Done():
		return qoderMachineInfo{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return qoderMachineInfo{}, err
	}
	environment := 0
	if target == "qoder:global" {
		environment = 4
	}
	path := filepath.Join(u.dir, fmt.Sprintf("environment-%d.json", environment))
	cache, loaded := u.cache[environment]
	if !loaded {
		raw, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return qoderMachineInfo{}, fmt.Errorf("读取 UMID 缓存: %w", err)
		}
		if err == nil {
			if len(raw) > 1<<20 || json.Unmarshal(raw, &cache) != nil || cache.Version != 1 || cache.Environment != environment || cache.Platform != goruntime.GOOS || cache.Arch != qoderMachineArch() || !validQoderMachineInfo(cache.Info) || cache.UpdatedAt.IsZero() || cache.RefreshAt.Before(cache.UpdatedAt) || cache.RefreshAt.Sub(cache.UpdatedAt) > 65*time.Minute {
				return qoderMachineInfo{}, fmt.Errorf("UMID 缓存 environment=%d 格式或运行环境不匹配", environment)
			}
		}
		u.cache[environment] = cache
	}
	now := u.now()
	if validQoderMachineInfo(cache.Info) && now.Before(cache.RefreshAt) {
		return cache.Info, nil
	}
	if now.Before(u.retryAt[environment]) {
		if validQoderMachineInfo(cache.Info) {
			return cache.Info, nil
		}
		return qoderMachineInfo{}, errors.New("UMID 初始化上次失败，一分钟后重试；原因见服务日志")
	}
	var info qoderMachineInfo
	binary, err := u.prepare(ctx, proxyURL)
	if err == nil {
		info, err = u.run(ctx, binary, environment, proxyURL)
	}
	if err == nil && !validQoderMachineInfo(info) {
		err = errors.New("官方 UMID 组件返回无效设备字段")
	}
	if err == nil {
		cache = qoderMachineCache{Version: 1, Environment: environment, Platform: goruntime.GOOS, Arch: qoderMachineArch(), Info: info, UpdatedAt: now, RefreshAt: now.Add(time.Hour + time.Duration(rand.Int64N(int64(10*time.Minute)+1)) - 5*time.Minute)}
		raw, marshalErr := json.MarshalIndent(cache, "", "  ")
		if marshalErr != nil {
			return qoderMachineInfo{}, marshalErr
		}
		err = writeProviderFile(path, raw, 0600)
		if err == nil {
			u.cache[environment] = cache
			delete(u.retryAt, environment)
			return info, nil
		}
	}
	if ctx.Err() != nil {
		return qoderMachineInfo{}, ctx.Err()
	}
	// 账号代理线路故障只影响该线路，不写入全局环境退避，避免连坐同地区其他健康代理账号。
	if !upstream.IsRouteError(err) {
		u.retryAt[environment] = now.Add(time.Minute)
	}
	log.Printf("[providers] Qoder UMID environment=%d 更新失败：%v", environment, err)
	// 只保留已经安全落盘的旧快照，不把本次写盘失败的新身份当作已保存。
	old := u.cache[environment]
	if validQoderMachineInfo(old.Info) {
		return old.Info, nil
	}
	return qoderMachineInfo{}, err
}

func validQoderMachineInfo(info qoderMachineInfo) bool {
	for _, field := range []string{info.Token, info.Type, info.Code} {
		if strings.TrimSpace(field) == "" || len(field) > 16<<10 {
			return false
		}
		for _, ch := range field {
			if ch < 0x20 || ch > 0x7e {
				return false
			}
		}
	}
	return true
}

// qoderHostname 对照官方 1.1.89／1.1.96 规范化实现：安全 ASCII、IDNA、替换并加摘要。
func qoderHostname(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	safe := func(s string) bool {
		for _, ch := range s {
			if ch < 0x20 || ch > 0x7e {
				return false
			}
		}
		return true
	}
	digest := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))[:8] }
	shorten := func(s string) string {
		if len(s) > 96 {
			s = strings.TrimRight(s[:87], "- \t\r\n") + "-" + digest(s)
		}
		return s
	}
	if safe(host) {
		return shorten(host)
	}
	if ascii, err := idna.Lookup.ToASCII(host); err == nil && ascii != "" && safe(ascii) {
		return shorten(ascii)
	}
	var out strings.Builder
	lastHyphen := false
	for _, ch := range host {
		if ch < 0x21 || ch > 0x7e {
			ch = '-'
		}
		if ch == '-' && (out.Len() == 0 || lastHyphen) {
			continue
		}
		out.WriteRune(ch)
		lastHyphen = ch == '-'
	}
	base := strings.Trim(out.String(), "-")
	if base == "" {
		base = "unknown"
	}
	return shorten(base + "-" + digest(host))
}
