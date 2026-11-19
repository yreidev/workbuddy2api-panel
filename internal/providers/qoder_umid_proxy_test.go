// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
)

func TestQoderUMIDProxyTunnelPreservesBytes(t *testing.T) {
	for _, host := range []string{"pum.m.alibaba.com:443", "sg-pum.alibabachengdun.com:443"} {
		t.Run(host, func(t *testing.T) {
			done := make(chan error, 1)
			var dials atomic.Int32
			request := "POST /repPc.json?fixture=1 HTTP/1.0\r\nHost: fixture.invalid\r\nContent-Length: 7\r\n\r\npayload"
			reply := "HTTP/1.0 200 OK\r\nContent-Length: 10\r\n\r\n{\"ec\":200}"
			p, err := newQoderMachineProxy(t.Context(), t.TempDir(), func(_ context.Context, address string) (net.Conn, error) {
				dials.Add(1)
				if address != host {
					return nil, fmt.Errorf("错误目标 %s", address)
				}
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					_ = server.SetDeadline(time.Now().Add(3 * time.Second))
					raw := make([]byte, len(request))
					_, err := io.ReadFull(server, raw)
					if err == nil && string(raw) != request {
						err = errors.New("代理改写了官方请求")
					}
					if err == nil {
						_, err = io.WriteString(server, reply)
					}
					done <- err
				}()
				return client, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(p.config)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("代理配置权限不正确", err)
			}
			client, err := net.DialTimeout("tcp", p.listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_ = client.SetDeadline(time.Now().Add(3 * time.Second))
			_, err = fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", host, host, p.authorization)
			if err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(client)
			response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatal("未建立受控隧道", err)
			}
			if _, err = io.WriteString(client, request); err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(reader)
			_ = client.Close()
			if err != nil || string(raw) != reply {
				t.Fatal("代理改写或截断了官方响应", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := p.close(); err != nil {
				t.Fatal(err)
			}
			if dials.Load() != 1 {
				t.Fatal("重复拨号")
			}
			if _, err := os.Stat(p.config); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("运行后遗留了临时代理配置")
			}
		})
	}
}

func TestQoderUMIDProxyRejectsUntrustedRequests(t *testing.T) {
	var dials atomic.Int32
	p, err := newQoderMachineProxy(t.Context(), t.TempDir(), func(context.Context, string) (net.Conn, error) { dials.Add(1); return nil, errors.New("不得拨号") })
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, target, auth string
		status               int
	}{
		{"CONNECT", "pum.m.alibaba.com:443", "", 407},
		{"CONNECT", "pum.m.alibaba.com:443", "Basic invalid", 407},
		{"CONNECT", "fixture.invalid:443", p.authorization, 403},
		{"CONNECT", "pum.m.alibaba.com:80", p.authorization, 403},
		{"GET", "pum.m.alibaba.com:443", p.authorization, 403},
	} {
		client, err := net.DialTimeout("tcp", p.listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = client.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = fmt.Fprintf(client, "%s %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\nConnection: close\r\n\r\n", tc.method, tc.target, tc.target, tc.auth)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: tc.method})
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		_ = client.Close()
		if response.StatusCode != tc.status {
			t.Fatalf("不可信请求状态=%d，期望%d", response.StatusCode, tc.status)
		}
	}
	if dials.Load() != 0 {
		t.Fatal("不可信请求触发了出站拨号")
	}
	if err := p.close(); err == nil {
		t.Fatal("没有网络交互却被当成成功")
	}
}

func TestQoderUMIDProxyFailureAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered := make(chan struct{})
			p, err := newQoderMachineProxy(ctx, t.TempDir(), func(ctx context.Context, _ string) (net.Conn, error) {
				close(entered)
				if cancelled {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return nil, errors.New("fixture proxy offline")
			})
			if err != nil {
				t.Fatal(err)
			}
			client, err := net.DialTimeout("tcp", p.listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_ = client.SetDeadline(time.Now().Add(2 * time.Second))
			_, err = fmt.Fprintf(client, "CONNECT pum.m.alibaba.com:443 HTTP/1.1\r\nHost: pum.m.alibaba.com:443\r\nProxy-Authorization: %s\r\n\r\n", p.authorization)
			if err != nil {
				t.Fatal(err)
			}
			<-entered
			if cancelled {
				cancel()
			}
			response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != 502 {
				t.Fatal("代理失败后未拒绝隧道")
			}
			err = p.close()
			if err == nil || (!cancelled && !strings.Contains(err.Error(), "fixture proxy offline")) || (cancelled && !errors.Is(err, context.Canceled)) {
				t.Fatal("丢失代理失败原因", err)
			}
		})
	}
}

func TestQoderUMIDProxyDoesNotAcceptOfflineResult(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, ".bin", "helper")
	if err := os.MkdirAll(filepath.Dir(helper), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 99\n"), 0500); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	dial := func(context.Context, string) (net.Conn, error) {
		t.Error("不应拨号")
		return nil, errors.New("unexpected")
	}
	if _, err := runQoderMachineProxy(t.Context(), helper, 0, dial); err == nil || !strings.Contains(err.Error(), "proxychains4") {
		t.Fatal("缺少适配组件时启动了 helper", err)
	}
	// 模拟外部适配器没有进行隧道却直接产出设备字段，必须拒绝而非保存离线回退。
	fake := []byte("#!/bin/sh\nprintf '%s\\n' '{\"machineToken\":\"fixture-token\",\"machineType\":\"type\",\"machineCode\":\"code\"}'\n")
	if err := os.WriteFile(filepath.Join(dir, "proxychains4"), fake, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := runQoderMachineProxy(t.Context(), helper, 0, dial); err == nil || !strings.Contains(err.Error(), "离线回退") {
		t.Fatal("接受了未经过所选代理的设备结果", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, ".proxychains-*.conf"))
	if err != nil || len(files) != 0 {
		t.Fatal("失败后遗留代理配置", err)
	}
}

func TestQoderUMIDProxyRouteSelection(t *testing.T) {
	for _, tc := range []struct {
		name, proxyID, want string
		valid               bool
	}{
		{"默认线路", "", "http://default.invalid:8080", true},
		{"绑定线路", "bound", "socks5h://bound.invalid:1080", true},
		{"绑定不存在不回默认", "missing", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, u, _ := testQoderMachine(t)
			u.defaultProxy = "http://default.invalid:8080"
			path := filepath.Join(t.TempDir(), "proxies.json")
			raw := []byte(`{"pools":[{"id":"fixture","name":"fixture"}],"proxies":[{"id":"bound","name":"fixture","pool":"fixture","url":"socks5h://bound.invalid:1080"}]}`)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			registry, err := proxies.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			m.SetProxies(registry)
			var prepared, run string
			u.prepare = func(_ context.Context, route string) (string, error) { prepared = route; return "fixture-helper", nil }
			u.run = func(_ context.Context, _ string, _ int, route string) (qoderMachineInfo, error) {
				run = route
				return qoderMachineInfo{Token: "fixture-token", Code: "code", Type: "type"}, nil
			}
			_, err = m.qoderIdentity(t.Context(), "qoder:cn", tc.proxyID, Credential{})
			if (err == nil) != tc.valid {
				t.Fatal("代理线路解析结果错误", err)
			}
			if prepared != tc.want || run != tc.want {
				t.Fatalf("下载或组件未使用同一线路：prepare=%q run=%q", prepared, run)
			}
		})
	}
}

func TestQoderUMIDHelperDownloadUsesSelectedProxy(t *testing.T) {
	var connect atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "registry.npmjs.org:443" {
			t.Errorf("组件下载绕过所选代理：%s %s", r.Method, r.Host)
		}
		connect.Add(1)
		http.Error(w, "fixture denied", http.StatusForbidden)
	}))
	defer server.Close()
	_, err := prepareQoderHelper(t.Context(), t.TempDir(), server.URL)
	if err == nil || connect.Load() != 1 {
		t.Fatal("所选代理拒绝后下载仍继续", err)
	}
}
