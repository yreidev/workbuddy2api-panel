// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

// proxy_test.go 出站代理：读环境变量的顺序与校验、NO_PROXY 规则、CONNECT 与 SOCKS5 隧道（隧道里仍是
// 官方 TLS 指纹）、钉 IP 直连、拉模型目录走代理时跳过备用 IP，以及连代理的各种失败。代理和上游都是
// 本机回环上起的假服务，不连外网。

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// clearProxyEnv 清空代理相关的环境变量（t.Setenv 结束后自动还原）。
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, k := range append(append([]string(nil), proxyEnvKeys...), "NO_PROXY", "no_proxy") {
		t.Setenv(k, "")
	}
}

func TestProxyFromEnv(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    string // String()；空 = 不走代理
		from    string
		noProxy []string
		err     string // 错误里应有的字样；空 = 不该出错
	}{
		{name: "都没设"},
		{name: "没写协议按 http", env: map[string]string{"HTTPS_PROXY": "127.0.0.1:7890"}, want: "http://127.0.0.1:7890", from: "HTTPS_PROXY"},
		{name: "HTTPS_PROXY 优先于 HTTP_PROXY", env: map[string]string{"HTTPS_PROXY": "http://a:1", "HTTP_PROXY": "http://b:2"}, want: "http://a:1", from: "HTTPS_PROXY"},
		{name: "https_proxy 优先于 HTTP_PROXY", env: map[string]string{"https_proxy": "http://a:1", "HTTP_PROXY": "http://b:2"}, want: "http://a:1", from: "https_proxy"},
		{name: "只有空白算没设", env: map[string]string{"HTTPS_PROXY": "  ", "http_proxy": "http://b:2"}, want: "http://b:2", from: "http_proxy"},
		{name: "HTTP_PROXY 优先于 ALL_PROXY", env: map[string]string{"HTTP_PROXY": "http://b:2", "ALL_PROXY": "socks5://c:3"}, want: "http://b:2", from: "HTTP_PROXY"},
		{name: "all_proxy 接 socks5h，日志不带密码", env: map[string]string{"all_proxy": "socks5h://u:secret@c:1080"}, want: "socks5h://c:1080", from: "all_proxy"},
		{name: "https 代理", env: map[string]string{"HTTPS_PROXY": "https://p.example:8443"}, want: "https://p.example:8443", from: "HTTPS_PROXY"},
		{name: "NO_PROXY 拆项、去空白、转小写", env: map[string]string{"HTTPS_PROXY": "http://a:1", "NO_PROXY": " Example.COM, ,.b.com ", "no_proxy": "ignored"}, want: "http://a:1", from: "HTTPS_PROXY", noProxy: []string{"example.com", ".b.com"}},
		{name: "NO_PROXY 没设看 no_proxy", env: map[string]string{"HTTPS_PROXY": "http://a:1", "no_proxy": "c.com"}, want: "http://a:1", from: "HTTPS_PROXY", noProxy: []string{"c.com"}},
		{name: "不支持的协议", env: map[string]string{"HTTPS_PROXY": "ftp://u:secret@h:21", "HTTP_PROXY": "http://b:2"}, err: "HTTPS_PROXY：不支持的协议 ftp"},
		{name: "解析不了", env: map[string]string{"HTTP_PROXY": "http://u:secret@[::1"}, err: "HTTP_PROXY：不是合法的代理地址"},
		{name: "没有主机", env: map[string]string{"ALL_PROXY": "http://:8080"}, err: "ALL_PROXY：不是合法的代理地址"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearProxyEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			p, err := proxyFromEnv()
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v，应含 %q", err, c.err)
				}
				if strings.Contains(err.Error(), "secret") {
					t.Errorf("错误里漏了密码：%v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该出错：%v", err)
			}
			if c.want == "" {
				if p != nil {
					t.Fatalf("没设代理却得到 %s", p)
				}
				return
			}
			if p == nil {
				t.Fatalf("没读到代理，应为 %s", c.want)
			}
			if p.String() != c.want || p.env != c.from {
				t.Errorf("得到 %s（取自 %s），应为 %s（取自 %s）", p, p.env, c.want, c.from)
			}
			if !reflect.DeepEqual(p.noProxy, c.noProxy) {
				t.Errorf("noProxy = %q，应为 %q", p.noProxy, c.noProxy)
			}
		})
	}
}

// TestProxyApplies NO_PROXY 规则同官方 ProxyResolver.shouldBypassProxy，本机总是直连。
func TestProxyApplies(t *testing.T) {
	var none *outboundProxy
	if none.applies("www.workbuddy.cn") {
		t.Error("nil 代理不该走代理")
	}
	p := &outboundProxy{url: &url.URL{Scheme: "http", Host: "p:1"}, noProxy: splitNoProxy("*.a.com,.b.com,c.com,10.0.0.5")}
	for host, want := range map[string]bool{
		"localhost": false, "LOCALHOST": false, "127.0.0.1": false, "::1": false,
		"a.com": false, "x.a.com": false, "xa.com": true,
		"b.com": true, "x.b.com": false,
		"c.com": false, "X.C.COM": false, "xc.com": true,
		"10.0.0.5": false, "10.0.0.6": true,
		"www.workbuddy.cn": true,
	} {
		if got := p.applies(host); got != want {
			t.Errorf("applies(%q) = %v，应为 %v", host, got, want)
		}
	}
	all := &outboundProxy{url: p.url, noProxy: []string{"*"}}
	if all.applies("www.workbuddy.cn") {
		t.Error("NO_PROXY=* 应全部直连")
	}
}

func TestProxyHostPort(t *testing.T) {
	for raw, want := range map[string]string{
		"http://p.example":      "p.example:80",
		"https://p.example":     "p.example:443",
		"http://p.example:3128": "p.example:3128",
		"https://[::1]":         "[::1]:443",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("解析 %s: %v", raw, err)
		}
		if got := proxyHostPort(u); got != want {
			t.Errorf("proxyHostPort(%s) = %s，应为 %s", raw, got, want)
		}
	}
}

// TestEnvProxyLogs 启动时记一行：用上的代理（不带密码），或者设了但用不了（WARN，直连）。
func TestEnvProxyLogs(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	clearProxyEnv(t)
	if p := envProxy(); p != nil || buf.Len() != 0 {
		t.Fatalf("没设代理：p=%v 日志=%q", p, buf.String())
	}
	t.Setenv("HTTPS_PROXY", "http://u:secret@127.0.0.1:7890")
	if p := envProxy(); p == nil || !strings.Contains(buf.String(), "出站经代理 http://127.0.0.1:7890（取自 HTTPS_PROXY）") || strings.Contains(buf.String(), "secret") {
		t.Fatalf("用上代理：p=%v 日志=%q", p, buf.String())
	}
	buf.Reset()
	t.Setenv("HTTPS_PROXY", "ftp://h:21")
	if p := envProxy(); p != nil || !strings.Contains(buf.String(), "WARN 代理环境变量用不了，出站直连：HTTPS_PROXY：不支持的协议 ftp") {
		t.Fatalf("用不了：p=%v 日志=%q", p, buf.String())
	}
}

// fakeConnectProxy 假 HTTP CONNECT 代理：不管 CONNECT 到哪儿，一律转到 backend，记下收到的请求。
// status 非 0 时回这个状态码就断开；hangup 读完请求直接断开；silent 读完请求不回话。
type fakeConnectProxy struct {
	backend string
	status  int
	hangup  bool
	silent  bool

	mu   sync.Mutex
	reqs []*http.Request
}

// start 在本机回环上起代理，tlsCfg 非空时代理本身走 TLS（https 代理）。返回代理地址。
func (fp *fakeConnectProxy) start(t *testing.T, tlsCfg *tls.Config) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().String()
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go fp.serve(c)
		}
	}()
	return addr
}

func (fp *fakeConnectProxy) serve(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	fp.mu.Lock()
	fp.reqs = append(fp.reqs, req)
	fp.mu.Unlock()
	switch {
	case fp.hangup:
		return
	case fp.silent:
		_, _ = io.Copy(io.Discard, br)
		return
	case fp.status != 0:
		_, _ = fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", fp.status, http.StatusText(fp.status))
		return
	}
	relayTo(c, br, fp.backend, []byte("HTTP/1.1 200 Connection established\r\n\r\n"))
}

func (fp *fakeConnectProxy) seen() []*http.Request {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return append([]*http.Request(nil), fp.reqs...)
}

// fakeSOCKS5 假 SOCKS5 代理（RFC 1928，user 非空时要求 RFC 1929 用户名密码）：不管请求连哪儿，一律转到
// backend，记下收到的目标。fail 时对 CONNECT 回「连接被拒」。
type fakeSOCKS5 struct {
	backend    string
	user, pass string
	fail       bool

	mu      sync.Mutex
	targets []string
}

func (sp *fakeSOCKS5) start(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go sp.serve(c)
		}
	}()
	return ln.Addr().String()
}

func (sp *fakeSOCKS5) serve(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	readN := func(n int) []byte {
		b := make([]byte, n)
		if _, err := io.ReadFull(br, b); err != nil {
			return nil
		}
		return b
	}
	// 问候：VER NMETHODS METHODS...
	hello := readN(2)
	if hello == nil || readN(int(hello[1])) == nil {
		return
	}
	if sp.user != "" {
		_, _ = c.Write([]byte{5, 2})
		// RFC 1929：VER ULEN UNAME PLEN PASSWD
		v := readN(2)
		if v == nil {
			return
		}
		user := readN(int(v[1]))
		plen := readN(1)
		if user == nil || plen == nil {
			return
		}
		if pass := readN(int(plen[0])); string(user) != sp.user || string(pass) != sp.pass {
			_, _ = c.Write([]byte{1, 1})
			return
		}
		_, _ = c.Write([]byte{1, 0})
	} else {
		_, _ = c.Write([]byte{5, 0})
	}
	// 请求：VER CMD RSV ATYP DST.ADDR DST.PORT；x/net 对域名一律发 ATYP=3（交给代理解析）。
	head := readN(4)
	if head == nil || head[3] != 3 {
		return
	}
	n := readN(1)
	if n == nil {
		return
	}
	name, port := readN(int(n[0])), readN(2)
	if name == nil || port == nil {
		return
	}
	sp.mu.Lock()
	sp.targets = append(sp.targets, net.JoinHostPort(string(name), strconv.Itoa(int(binary.BigEndian.Uint16(port)))))
	sp.mu.Unlock()
	if sp.fail {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	relayTo(c, br, sp.backend, []byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
}

func (sp *fakeSOCKS5) seen() []string {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return append([]string(nil), sp.targets...)
}

// relayTo 连上 backend，先给客户端回 reply，再双向转发到任一方断开。
func relayTo(c net.Conn, br *bufio.Reader, backend string, reply []byte) {
	up, err := net.Dial("tcp", backend)
	if err != nil {
		return
	}
	defer up.Close()
	if _, err := c.Write(reply); err != nil {
		return
	}
	go func() {
		_, _ = io.Copy(up, br)
		_ = up.Close()
	}()
	_, _ = io.Copy(c, up)
}

// hostSNIServer 起一个 TLS 假上游（证书认 example.com 和 127.0.0.1），响应体回「Host SNI」。
func hostSNIServer(t *testing.T) (srv *httptest.Server, pool *x509.CertPool, port string) {
	t.Helper()
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Host+" "+r.TLS.ServerName)
	}))
	t.Cleanup(srv.Close)
	pool = x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	_, port, _ = net.SplitHostPort(srv.Listener.Addr().String())
	return srv, pool, port
}

// proxiedTransport newTransport(p) 的拨号接法，证书校验认 pool（生产用系统根）。
func proxiedTransport(t *testing.T, p *outboundProxy, pool *x509.CertPool) *http.Transport {
	t.Helper()
	tr := newTransport(p)
	tr.DialTLSContext = dialTLSMimic(tr.DialContext, &tls.Config{RootCAs: pool})
	t.Cleanup(tr.CloseIdleConnections)
	return tr
}

// proxyGet 用 rt 发一次 GET，返回 200 的响应体。
func proxyGet(t *testing.T, rt http.RoundTripper, rawURL string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", rawURL, resp.StatusCode, b)
	}
	return string(b)
}

// TestProxyConnectTunnel 经 HTTP CONNECT 代理发 https 和 http 请求：代理收到的是目标域名与端口、带上
// 用户名密码；上游看到的 Host 与 SNI 仍是原域名。
func TestProxyConnectTunnel(t *testing.T) {
	srv, pool, port := hostSNIServer(t)
	fp := &fakeConnectProxy{backend: srv.Listener.Addr().String()}
	p := &outboundProxy{url: &url.URL{Scheme: "http", Host: fp.start(t, nil), User: url.UserPassword("u", "p w")}}

	if got, want := proxyGet(t, proxiedTransport(t, p, pool), "https://example.com:"+port+"/"), "example.com:"+port+" example.com"; got != want {
		t.Errorf("上游看到 %q，应为 %q", got, want)
	}
	reqs := fp.seen()
	if len(reqs) != 1 || reqs[0].Method != http.MethodConnect || reqs[0].Host != "example.com:"+port {
		t.Fatalf("代理收到 %d 个请求：%+v", len(reqs), reqs)
	}
	if got, want := reqs[0].Header.Get("Proxy-Authorization"), "Basic "+base64.StdEncoding.EncodeToString([]byte("u:p w")); got != want {
		t.Errorf("Proxy-Authorization = %q，应为 %q", got, want)
	}

	t.Run("http 目标也走隧道", func(t *testing.T) {
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, r.Host)
		}))
		defer plain.Close()
		_, plainPort, _ := net.SplitHostPort(plain.Listener.Addr().String())
		fp := &fakeConnectProxy{backend: plain.Listener.Addr().String()}
		p := &outboundProxy{url: &url.URL{Scheme: "http", Host: fp.start(t, nil)}}
		if got := proxyGet(t, proxiedTransport(t, p, pool), "http://example.com:"+plainPort+"/"); got != "example.com:"+plainPort {
			t.Errorf("上游看到 Host %q", got)
		}
		if reqs := fp.seen(); len(reqs) != 1 || reqs[0].Host != "example.com:"+plainPort || reqs[0].Header.Get("Proxy-Authorization") != "" {
			t.Errorf("代理收到 %+v", reqs)
		}
	})

	t.Run("本机目标不走代理", func(t *testing.T) {
		fp := &fakeConnectProxy{status: http.StatusForbidden}
		p := &outboundProxy{url: &url.URL{Scheme: "http", Host: fp.start(t, nil)}}
		if got := proxyGet(t, proxiedTransport(t, p, pool), "https://127.0.0.1:"+port+"/"); got != "127.0.0.1:"+port+" " {
			t.Errorf("上游看到 %q", got)
		}
		if n := len(fp.seen()); n != 0 {
			t.Errorf("本机目标不该经代理，代理收到 %d 个请求", n)
		}
	})
}

// TestProxyTunnelKeepsFingerprint 走代理时隧道里的 ClientHello 仍是官方指纹，SNI 仍是请求域名。
func TestProxyTunnelKeepsFingerprint(t *testing.T) {
	t.Cleanup(func() { SetTLSMimic(true) })
	SetTLSMimic(true)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		// 读满一条 TLS 记录（5 字节头 + 头里写的长度）就够了。
		var buf []byte
		tmp := make([]byte, 4096)
		for len(buf) < 5 || len(buf) < 5+int(binary.BigEndian.Uint16(buf[3:5])) {
			n, rerr := conn.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if rerr != nil {
				break
			}
		}
		got <- buf
	}()
	fp := &fakeConnectProxy{backend: ln.Addr().String()}
	p := &outboundProxy{url: &url.URL{Scheme: "http", Host: fp.start(t, nil)}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if conn, err := newTransport(p).DialTLSContext(ctx, "tcp", "www.workbuddy.cn:443"); err == nil {
		_ = conn.Close()
	}
	rec := <-got
	if len(rec) == 0 {
		t.Fatal("没经代理连到抓包端（没抓到 ClientHello）")
	}
	if got := ja3MD5(rec); got != targetJA3MD5 {
		t.Errorf("隧道里的 JA3 MD5 = %s，应为 %s", got, targetJA3MD5)
	}
	if sni := sniOf(rec); sni != "www.workbuddy.cn" {
		t.Errorf("隧道里的 SNI = %q，应为 www.workbuddy.cn", sni)
	}
	if reqs := fp.seen(); len(reqs) != 1 || reqs[0].Host != "www.workbuddy.cn:443" {
		t.Errorf("代理收到 %+v，应为 CONNECT www.workbuddy.cn:443", reqs)
	}
}

// TestProxySOCKS5Tunnel 经 SOCKS5 代理发 https 请求：用户名密码认证，目标按域名交给代理（socks5 与
// socks5h 一样），上游看到的 Host 与 SNI 仍是原域名；代理拒绝连接时报错。
func TestProxySOCKS5Tunnel(t *testing.T) {
	srv, pool, port := hostSNIServer(t)
	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			sp := &fakeSOCKS5{backend: srv.Listener.Addr().String(), user: "u", pass: "pw"}
			p := &outboundProxy{url: &url.URL{Scheme: scheme, Host: sp.start(t), User: url.UserPassword("u", "pw")}}
			if got, want := proxyGet(t, proxiedTransport(t, p, pool), "https://example.com:"+port+"/"), "example.com:"+port+" example.com"; got != want {
				t.Errorf("上游看到 %q，应为 %q", got, want)
			}
			if got := sp.seen(); !reflect.DeepEqual(got, []string{"example.com:" + port}) {
				t.Errorf("代理收到的目标 = %v", got)
			}
		})
	}
	t.Run("代理拒绝连接", func(t *testing.T) {
		sp := &fakeSOCKS5{fail: true}
		p := &outboundProxy{url: &url.URL{Scheme: "socks5", Host: sp.start(t)}}
		_, err := proxyDial(p, newDialer())(context.Background(), "tcp", "example.com:443")
		if err == nil || !strings.Contains(err.Error(), "经代理 "+p.String()+" 连 example.com:443") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestProxyHTTPSProxy https 代理：先和代理本身做 TLS 握手再发 CONNECT；代理不说 TLS 时报握手错误。
func TestProxyHTTPSProxy(t *testing.T) {
	srv, pool, port := hostSNIServer(t)
	fp := &fakeConnectProxy{backend: srv.Listener.Addr().String()}
	p := &outboundProxy{
		url:       &url.URL{Scheme: "https", Host: fp.start(t, &tls.Config{Certificates: srv.TLS.Certificates})},
		tlsConfig: &tls.Config{RootCAs: pool},
	}
	if got, want := proxyGet(t, proxiedTransport(t, p, pool), "https://example.com:"+port+"/"), "example.com:"+port+" example.com"; got != want {
		t.Errorf("上游看到 %q，应为 %q", got, want)
	}
	if reqs := fp.seen(); len(reqs) != 1 || reqs[0].Host != "example.com:"+port {
		t.Errorf("代理收到 %+v", reqs)
	}

	t.Run("代理不说 TLS", func(t *testing.T) {
		plain := &fakeConnectProxy{backend: srv.Listener.Addr().String()}
		p := &outboundProxy{url: &url.URL{Scheme: "https", Host: plain.start(t, nil)}, tlsConfig: &tls.Config{RootCAs: pool}}
		_, err := proxyDial(p, newDialer())(context.Background(), "tcp", "example.com:443")
		if err == nil || !strings.Contains(err.Error(), "和代理 "+p.String()+" 握手") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestProxyConnectErrors 连代理的各种失败都报错、带上代理地址，不退回直连。
func TestProxyConnectErrors(t *testing.T) {
	via := func(t *testing.T, fp *fakeConnectProxy) func(context.Context, string, string) (net.Conn, error) {
		return proxyDial(&outboundProxy{url: &url.URL{Scheme: "http", Host: fp.start(t, nil)}}, newDialer())
	}
	wantErr := func(t *testing.T, err error, sub string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), sub) {
			t.Fatalf("err = %v，应含 %q", err, sub)
		}
	}
	t.Run("连不上代理", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		_, err = proxyDial(&outboundProxy{url: &url.URL{Scheme: "http", Host: addr}}, newDialer())(context.Background(), "tcp", "example.com:443")
		wantErr(t, err, "连代理 http://"+addr)
	})
	t.Run("拒绝 CONNECT", func(t *testing.T) {
		_, err := via(t, &fakeConnectProxy{status: http.StatusProxyAuthRequired})(context.Background(), "tcp", "example.com:443")
		wantErr(t, err, "拒绝 CONNECT example.com:443：407 Proxy Authentication Required")
	})
	t.Run("不回话就断开", func(t *testing.T) {
		_, err := via(t, &fakeConnectProxy{hangup: true})(context.Background(), "tcp", "example.com:443")
		wantErr(t, err, "读代理")
	})
	t.Run("不回话等到 ctx 到期", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := via(t, &fakeConnectProxy{silent: true})(ctx, "tcp", "example.com:443")
		wantErr(t, err, "读代理")
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("等了 %v，应在 ctx 到期时就返回", d)
		}
	})
	t.Run("不回话时 ctx 取消", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.AfterFunc(200*time.Millisecond, cancel)
		start := time.Now()
		_, err := via(t, &fakeConnectProxy{silent: true})(ctx, "tcp", "example.com:443")
		wantErr(t, err, "读代理")
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("等了 %v，应在 ctx 取消时就返回", d)
		}
	})
	t.Run("发 CONNECT 前已到期", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		p := &outboundProxy{url: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}}
		_, err := p.connect(ctx, c1, "example.com:443")
		wantErr(t, err, "向代理 http://127.0.0.1:1 发 CONNECT example.com:443")
	})
	t.Run("目标没端口", func(t *testing.T) {
		_, err := proxyDial(&outboundProxy{url: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}}, newDialer())(context.Background(), "tcp", "example.com")
		wantErr(t, err, "missing port")
	})
}

// TestPinTransportSkipsProxy 钉 IP 的连接照官方直连，不经代理；同一个 base 不钉 IP 时走代理。
func TestPinTransportSkipsProxy(t *testing.T) {
	srv, pool, port := hostSNIServer(t)
	fp := &fakeConnectProxy{status: http.StatusForbidden} // 经了代理就会被拒
	base := newTransport(&outboundProxy{url: &url.URL{Scheme: "http", Host: fp.start(t, nil)}})
	base.TLSClientConfig = &tls.Config{RootCAs: pool}
	target := "https://example.com:" + port + "/"

	pinned := pinTransport(base, srv.Listener.Addr().(*net.TCPAddr).IP.String())
	t.Cleanup(pinned.CloseIdleConnections)
	if got := proxyGet(t, pinned, target); got != "example.com:"+port+" example.com" {
		t.Errorf("上游看到 %q", got)
	}
	if n := len(fp.seen()); n != 0 {
		t.Errorf("钉 IP 的请求不该经代理，代理收到 %d 个请求", n)
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	if _, err := base.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("不钉 IP 应经代理（代理回 403），err = %v", err)
	}
}

// TestConfigFetchProxiedSkipsFallbackIP 备用入口走代理时拉模型目录不试备用 IP（照官方：这一步只给直连的
// 请求用）；备用入口在 NO_PROXY 里时照常试。
func TestConfigFetchProxiedSkipsFallbackIP(t *testing.T) {
	for _, c := range []struct {
		name    string
		noProxy []string
		pinned  int
	}{
		{"走代理跳过备用 IP", nil, 0},
		{"备用入口在 NO_PROXY 里照常试", []string{"tencentbuddy.com"}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &cfgFake{
				main:      cfgResp{status: http.StatusServiceUnavailable, body: `{"code":503}`},
				backup:    cfgResp{status: http.StatusServiceUnavailable, body: `{"code":503}`},
				backupPin: cfgResp{status: http.StatusOK, body: cfgValidBody},
			}
			cl := cfgClient(f)
			cl.proxy = &outboundProxy{url: &url.URL{Scheme: "http", Host: "proxy.test:8080"}, noProxy: c.noProxy}
			_, err := cl.fetchV3ConfigModelMap(upAuth())
			if got := len(f.pinnedIPs); got != c.pinned {
				t.Fatalf("钉 IP %d 次，应为 %d（err=%v）", got, c.pinned, err)
			}
			if (err == nil) != (c.pinned == 1) {
				t.Errorf("err = %v", err)
			}
			if n := len(f.seen()); n != 2+c.pinned {
				t.Errorf("请求数 = %d，应为 %d", n, 2+c.pinned)
			}
		})
	}
}
