// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"net"
	"time"
)

// dnsDialTimeout 连单台 DNS 服务器的上限，多台依次试。
const dnsDialTimeout = 5 * time.Second

// newDNSResolver 只问 servers 这几台 DNS 服务器的解析器（纯 Go 实现），按顺序试，前一台连不上换下一台。
// servers 的每项是 IP 或 IP:端口，没写端口用 53（格式在读配置时校验）。
func newDNSResolver(servers []string) *net.Resolver {
	addrs := dnsServerAddrs(servers)
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: dnsDialTimeout}
		var lastErr error
		for _, addr := range addrs {
			c, err := d.DialContext(ctx, network, addr)
			if err == nil {
				return c, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}}
}

// dnsServerAddrs 给没写端口的服务器补上 53。
func dnsServerAddrs(servers []string) []string {
	addrs := make([]string, len(servers))
	for i, s := range servers {
		if _, _, err := net.SplitHostPort(s); err != nil {
			s = net.JoinHostPort(s, "53")
		}
		addrs[i] = s
	}
	return addrs
}

// UseDNSServers 让整个进程的出站解析只问 servers（dns_servers / WB2A_DNS）：换掉 net.DefaultResolver，
// 没指定解析器的拨号都走它，所以对话与积分请求的 uTLS 握手、更新检查、Redis 镜像都跟着换。
// 用在没有 /etc/resolv.conf 的环境（Android、精简容器）：Go 的解析器那时会退回问本机 53 端口，
// 查什么都失败。HTTPDNS 和钉 IP 的连接直接连 IP，不经解析器，不受影响。servers 为空时什么都不改。
// 只能在启动时、发出任何请求之前调用一次。
func UseDNSServers(servers []string) {
	if len(servers) == 0 {
		return
	}
	r := newDNSResolver(servers)
	net.DefaultResolver.PreferGo = true
	net.DefaultResolver.Dial = r.Dial
}
