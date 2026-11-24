// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// qoderMachineProxy 只在一次官方组件调用期间接受本机 CONNECT。
// proxychains-ng 负责组件的 DNS／connect 适配；此处转发原字节，不重建官方请求。
type qoderMachineProxy struct {
	server        *http.Server
	listener      net.Listener
	config        string
	authorization string
	dial          func(context.Context, string) (net.Conn, error)
	ctx           context.Context
	mu            sync.Mutex
	closed        bool
	conns         map[net.Conn]struct{}
	wg            sync.WaitGroup
	completed     int
	err           error
	closeOnce     sync.Once
	closeErr      error
}

func runQoderMachineProxy(ctx context.Context, binary string, environment int, dial func(context.Context, string) (net.Conn, error)) (qoderMachineInfo, error) {
	command, err := exec.LookPath("proxychains4")
	if err != nil {
		return qoderMachineInfo{}, errors.New("Qoder UMID 代理需要 proxychains-ng 的 proxychains4；组件未启动，不回落直连")
	}
	return withQoderSDKLocale(binary, environment, func() (qoderMachineInfo, error) {
		ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		proxy, err := newQoderMachineProxy(ctx, filepath.Dir(filepath.Dir(binary)), dial)
		if err != nil {
			return qoderMachineInfo{}, err
		}
		defer func() { _ = proxy.close() }()
		info, runErr := runQoderMachineCommand(ctx, binary, command, []string{"-q", "-f", proxy.config, binary, strconv.Itoa(environment)})
		closeErr := proxy.close()
		if err := errors.Join(runErr, closeErr); err != nil {
			return qoderMachineInfo{}, fmt.Errorf("官方 UMID 组件经代理执行: %w", err)
		}
		return info, nil
	})
}

func newQoderMachineProxy(ctx context.Context, dir string, dial func(context.Context, string) (net.Conn, error)) (*qoderMachineProxy, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("创建 UMID 代理目录: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("监听 UMID 本机隧道: %w", err)
	}
	user, password := rand.Text(), rand.Text()
	p := &qoderMachineProxy{listener: ln, authorization: "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password)), dial: dial, ctx: ctx, conns: map[net.Conn]struct{}{}}
	file, err := os.CreateTemp(dir, ".proxychains-*.conf")
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("创建 UMID 代理配置: %w", err)
	}
	p.config = file.Name()
	config := fmt.Sprintf("strict_chain\nproxy_dns\nquiet_mode\ntcp_read_time_out 10000\ntcp_connect_time_out 10000\n[ProxyList]\nhttp 127.0.0.1 %d %s %s\n", ln.Addr().(*net.TCPAddr).Port, user, password)
	_, err = io.WriteString(file, config)
	err = errors.Join(err, file.Close())
	if err != nil {
		_ = ln.Close()
		_ = os.Remove(p.config)
		return nil, fmt.Errorf("写入 UMID 代理配置: %w", err)
	}
	p.server = &http.Server{Handler: http.HandlerFunc(p.serve), ReadHeaderTimeout: 2 * time.Second, MaxHeaderBytes: 4096}
	go func() {
		if err := p.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.mu.Lock()
			p.err = errors.Join(p.err, fmt.Errorf("UMID 本机隧道服务: %w", err))
			p.mu.Unlock()
		}
	}()
	return p, nil
}

func (p *qoderMachineProxy) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect || (r.Host != "pum.m.alibaba.com:443" && r.Host != "sg-pum.alibabachengdun.com:443") {
		http.Error(w, "UMID tunnel destination rejected", http.StatusForbidden)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Proxy-Authorization")), []byte(p.authorization)) != 1 {
		w.Header().Set("Proxy-Authenticate", `Basic realm="UMID"`)
		http.Error(w, "UMID tunnel authentication required", http.StatusProxyAuthRequired)
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		http.Error(w, "UMID tunnel closed", http.StatusServiceUnavailable)
		return
	}
	p.wg.Add(1)
	p.mu.Unlock()
	defer p.wg.Done()
	remote, err := p.dial(p.ctx, r.Host)
	if err != nil {
		p.mu.Lock()
		p.err = errors.Join(p.err, fmt.Errorf("UMID 代理拨号 %s: %w", r.Host, err))
		p.mu.Unlock()
		http.Error(w, "UMID upstream proxy failed", http.StatusBadGateway)
		return
	}
	defer remote.Close()
	client, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		p.mu.Lock()
		p.err = errors.Join(p.err, fmt.Errorf("接管 UMID 隧道: %w", err))
		p.mu.Unlock()
		return
	}
	defer client.Close()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.conns[client] = struct{}{}
	p.conns[remote] = struct{}{}
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.conns, client); delete(p.conns, remote); p.mu.Unlock() }()
	stop := context.AfterFunc(p.ctx, func() { _ = remote.Close(); _ = client.Close() })
	defer stop()
	if _, err = rw.WriteString("HTTP/1.0 200 Connection established\r\n\r\n"); err == nil {
		err = rw.Flush()
	}
	if err != nil {
		p.mu.Lock()
		p.err = errors.Join(p.err, fmt.Errorf("确认 UMID 本机隧道: %w", err))
		p.mu.Unlock()
		return
	}
	sent := make(chan error, 1)
	go func() {
		_, err := io.Copy(remote, rw)
		if cw, ok := remote.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		sent <- err
	}()
	received, readErr := io.Copy(client, remote)
	_ = client.Close()
	_ = remote.Close()
	writeErr := <-sent
	p.mu.Lock()
	defer p.mu.Unlock()
	if readErr != nil && !errors.Is(readErr, net.ErrClosed) {
		p.err = errors.Join(p.err, fmt.Errorf("读取 UMID 代理响应: %w", readErr))
	}
	if writeErr != nil && !errors.Is(writeErr, net.ErrClosed) {
		p.err = errors.Join(p.err, fmt.Errorf("发送 UMID 代理请求: %w", writeErr))
	}
	if received > 0 && (readErr == nil || (p.closed && p.ctx.Err() == nil && errors.Is(readErr, net.ErrClosed) && (writeErr == nil || errors.Is(writeErr, net.ErrClosed)))) {
		p.completed++
	}
}

func (p *qoderMachineProxy) close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		for conn := range p.conns {
			_ = conn.Close()
		}
		p.mu.Unlock()
		closeErr := p.server.Close()
		p.wg.Wait()
		removeErr := os.Remove(p.config)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.completed == 0 {
			p.err = errors.Join(p.err, errors.New("UMID 未经所选代理完成网络交互，不采用组件的离线回退结果"))
		}
		p.closeErr = errors.Join(p.err, closeErr, removeErr)
	})
	return p.closeErr
}
