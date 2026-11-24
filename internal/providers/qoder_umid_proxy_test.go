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

func TestQoderUMIDProxyDialFailureReportsFailoverAndNoGlobalPoison(t *testing.T) {
	// 拒绝 CONNECT 的代理服务端：验证拨号失败会 ReportProxy 标 down、换号容灾、不写全局退避、不向客户端泄露代理地址。
	badProxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "connect refused", http.StatusBadGateway)
	}))
	defer badProxyServer.Close()

	reg, err := proxies.Open(filepath.Join(t.TempDir(), "proxies.json"))
	if err != nil {
		t.Fatal(err)
	}
	poolID, err := reg.CreatePool(proxies.PoolInput{Name: "测试池"})
	if err != nil {
		t.Fatal(err)
	}
	added, err := reg.Import(badProxyServer.URL+" 坏代理\nhttp://good-umid.invalid:8888 好代理", "http", poolID, nil)
	if err != nil || len(added.Added) != 2 {
		t.Fatalf("导入代理失败: %+v %v", added, err)
	}
	badID, goodID := added.Added[0], added.Added[1]

	m, _ := testManager(t, filepath.Join(t.TempDir(), "providers.json"))
	m.SetProxies(reg)
	first, err := m.Import(t.Context(), AccountInput{Target: "qoder:cn", Name: "一号", ProxyID: badID, Priority: 1, Credential: Credential{UID: "first", AccessToken: "t1", UserType: "teams"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Import(t.Context(), AccountInput{Target: "qoder:cn", Name: "二号", ProxyID: goodID, Priority: 2, Credential: Credential{UID: "second", AccessToken: "t2", UserType: "teams"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.EnableQoderUMID(""); err != nil {
		t.Fatal(err)
	}
	u := m.qoderMachine
	now := time.Now()
	u.now = func() time.Time { return now }
	u.prepare = func(ctx context.Context, proxyURL string) (string, error) {
		if proxyURL == badProxyServer.URL {
			if _, dialErr := dialQoderUMIDProxy(ctx, proxyURL, "tcp", "registry.npmjs.org:443"); dialErr != nil {
				return "", dialErr
			}
		}
		return "fixture-helper", nil
	}
	u.run = func(_ context.Context, _ string, _ int, proxyURL string) (qoderMachineInfo, error) {
		if proxyURL != "http://good-umid.invalid:8888" {
			return qoderMachineInfo{}, errors.New("未走好代理")
		}
		return qoderMachineInfo{Token: "good-umid-token", Code: "good-code", Type: "good-type"}, nil
	}

	// 场景一：首号代理在 UMID 阶段失败，自动 ReportProxy 标 down 且不污染全局 retryAt，次号立即成功接管。
	res, err := m.Execute(t.Context(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat), MaxRotate: 2}, discardFrame)
	if err != nil || res.AccountID != second || res.Attempts != 2 || res.ProxyName != "好代理" {
		t.Fatalf("UMID 代理故障未正常换号容灾: res=%+v err=%v", res, err)
	}
	if _, _, usable, code := reg.CheckProxy(badID); usable || code != "proxy_not_available" {
		t.Fatalf("坏代理未被 ReportProxy 标记 down: usable=%v code=%q", usable, code)
	}
	if !u.retryAt[0].IsZero() {
		t.Fatalf("账号线路错误污染了全局 UMID 退避: %v", u.retryAt[0])
	}
	_ = first

	// 场景二：仅有单号且走坏代理（隧道拨号路径），对外错误不得泄露真实代理 host:port，也不得污染全局 retryAt。
	m2, _ := testManager(t, filepath.Join(t.TempDir(), "providers2.json"))
	reg2, err := proxies.Open(filepath.Join(t.TempDir(), "proxies2.json"))
	if err != nil {
		t.Fatal(err)
	}
	pool2, err := reg2.CreatePool(proxies.PoolInput{Name: "单测池"})
	if err != nil {
		t.Fatal(err)
	}
	added2, err := reg2.Import(badProxyServer.URL+" 单坏代理", "http", pool2, nil)
	if err != nil || len(added2.Added) != 1 {
		t.Fatalf("导入代理失败: %+v %v", added2, err)
	}
	m2.SetProxies(reg2)
	if _, err := m2.Import(t.Context(), AccountInput{Target: "qoder:cn", ProxyID: added2.Added[0], Credential: Credential{UID: "only", AccessToken: "t", UserType: "teams"}}); err != nil {
		t.Fatal(err)
	}
	if err := m2.EnableQoderUMID(""); err != nil {
		t.Fatal(err)
	}
	u2 := m2.qoderMachine
	u2.now = func() time.Time { return now }
	u2.prepare = func(context.Context, string) (string, error) { return "fixture-helper", nil }
	u2.run = func(ctx context.Context, _ string, _ int, proxyURL string) (qoderMachineInfo, error) {
		_, dialErr := dialQoderUMIDProxy(ctx, proxyURL, "tcp", "pum.m.alibaba.com:443")
		return qoderMachineInfo{}, dialErr
	}
	_, err = m2.Execute(t.Context(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat), MaxRotate: 1}, discardFrame)
	var pubErr *Error
	if !errors.As(err, &pubErr) || pubErr.Status != 503 || pubErr.Code != "proxy_not_available" {
		t.Fatalf("全部代理失败未返回 503 proxy_not_available: %v", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), badProxyServer.Listener.Addr().String()) || strings.Contains(err.Error(), "单坏代理") {
		t.Fatalf("对外错误泄露了代理地址或名称: %v", err)
	}
	if !u2.retryAt[0].IsZero() {
		t.Fatalf("隧道拨号线路错误写入了全局退避: %v", u2.retryAt[0])
	}
}

func TestQoderUMIDProxyIdempotentCloseAndKeepAlive(t *testing.T) {
	// 模拟远端保持 keep-alive 长连接不主动关：客户端收到响应后关闭连接，p.close() 仍计为成功且重复调用幂等。
	remoteLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer remoteLn.Close()
	remoteDone := make(chan struct{})
	remoteSawEOF := make(chan struct{})
	holdRemote := make(chan struct{})
	defer func() {
		select {
		case <-holdRemote:
		default:
			close(holdRemote)
		}
	}()
	go func() {
		defer close(remoteDone)
		conn, acceptErr := remoteLn.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 16)
		if _, rErr := io.ReadFull(conn, buf[:4]); rErr != nil {
			return
		}
		_, _ = io.WriteString(conn, "OK-KEEPALIVE")
		// 客户端半关闭后，代理应把 FIN 透传给远端（CloseWrite），随后远端保持连接打开模拟 keep-alive。
		_, _ = io.Copy(io.Discard, conn)
		close(remoteSawEOF)
		<-holdRemote
	}()

	p, err := newQoderMachineProxy(t.Context(), t.TempDir(), func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", remoteLn.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.DialTimeout("tcp", p.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = fmt.Fprintf(client, "CONNECT pum.m.alibaba.com:443 HTTP/1.1\r\nHost: pum.m.alibaba.com:443\r\nProxy-Authorization: %s\r\n\r\n", p.authorization)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("未建立隧道: %+v %v", resp, err)
	}
	if _, err := io.WriteString(client, "PING"); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len("OK-KEEPALIVE"))
	if _, err := io.ReadFull(reader, reply); err != nil || string(reply) != "OK-KEEPALIVE" {
		t.Fatalf("未收到远端响应: %q %v", reply, err)
	}
	if cw, ok := client.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	_ = client.Close()
	select {
	case <-remoteSawEOF:
	case <-time.After(2 * time.Second):
		t.Fatal("客户端关闭写端后未收到 CloseWrite 透传")
	}

	// 远端仍保持打开时调用 close()：仍应计入 completed 且重复调用幂等。
	if err := p.close(); err != nil {
		t.Fatalf("keep-alive 场景首次 close 失败: %v", err)
	}
	if err := p.close(); err != nil {
		t.Fatalf("重复 close 不幂等: %v", err)
	}
	if _, err := os.Stat(p.config); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("close 后遗留临时配置: %v", err)
	}
	close(holdRemote)
	<-remoteDone
}
