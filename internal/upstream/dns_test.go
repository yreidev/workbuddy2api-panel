// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeDNS 本机起一个只回 A=127.0.0.9 的 DNS 服务器（UDP），返回地址和收到的查询数。
func fakeDNS(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("UDP 不可用：%v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	var n atomic.Int32
	go func() {
		buf := make([]byte, 512)
		for {
			size, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			n.Add(1)
			var m dnsmessage.Message
			if m.Unpack(buf[:size]) != nil || len(m.Questions) == 0 {
				continue
			}
			q := m.Questions[0]
			m.Response, m.Authoritative = true, true
			if q.Type == dnsmessage.TypeA {
				m.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AResource{A: [4]byte{127, 0, 0, 9}},
				}}
			}
			if out, err := m.Pack(); err == nil {
				_, _ = pc.WriteTo(out, addr)
			}
		}
	}()
	return pc.LocalAddr().String(), &n
}

// TestDNSResolverUsesServers 解析只问配置的服务器，不碰系统解析器。
func TestDNSResolverUsesServers(t *testing.T) {
	addr, n := fakeDNS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := newDNSResolver([]string{addr}).LookupHost(ctx, "chat.example.test")
	if err != nil {
		t.Fatalf("LookupHost: %v", err)
	}
	if !reflect.DeepEqual(ips, []string{"127.0.0.9"}) {
		t.Errorf("ips=%v，应为假 DNS 回的 127.0.0.9", ips)
	}
	if n.Load() == 0 {
		t.Error("假 DNS 没收到查询")
	}
}

// TestDNSServerAddrs 没写端口的补 53，IPv6 加方括号。
func TestDNSServerAddrs(t *testing.T) {
	got := dnsServerAddrs([]string{"223.5.5.5", "119.29.29.29:5353", "2400:3200::1", "[::1]:53"})
	want := []string{"223.5.5.5:53", "119.29.29.29:5353", "[2400:3200::1]:53", "[::1]:53"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// TestUseDNSServersEmptyKeepsSystem 没配服务器时不动系统解析器。
func TestUseDNSServersEmptyKeepsSystem(t *testing.T) {
	before := net.DefaultResolver.Dial
	UseDNSServers(nil)
	if net.DefaultResolver.Dial != nil || before != nil {
		t.Error("没配服务器时不应改 net.DefaultResolver")
	}
}
