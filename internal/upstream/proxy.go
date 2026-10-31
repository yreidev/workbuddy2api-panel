// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// proxy.go 出站代理：照官方客户端读代理环境变量，在代理上建隧道（http、https 代理发 CONNECT，
// socks5 代理走 SOCKS5），隧道里照样用 uTLS 握手（tlsmimic.go），上游看到的 TLS 指纹和直连一样。
//
// 不用 http.Transport.Proxy：设了它，https 请求由标准库自己发 CONNECT、自己做 TLS 握手，
// DialTLSContext 不再被调用，uTLS 复刻的官方 ClientHello 会悄悄换成 Go 的。所以代理藏在拨号里，
// 对 Transport 来说一律是直连。
//
// 依据：官方桌面端 5.9.3 内置 CLI 2.165.0 的 ProxyResolver 与地址层，以及桌面端主进程的地址层
// （读安装包确认，见 docs/official-client.md「出站代理」）：
//   - 代理地址依次取 HTTPS_PROXY、https_proxy、HTTP_PROXY、http_proxy 第一个非空的，没写协议按 http://；
//   - NO_PROXY（没设再看 no_proxy）里的域名直连，localhost、127.0.0.1、::1 总是直连；
//   - 连不上代理报错，不退回直连；
//   - 钉 IP 的请求（地址层的 HTTPDNS 候选、备用 IP）直连，HTTPDNS 查询本身也直连；
//     拉模型目录的「备用 IP」一步只给直连的请求用，走代理时跳过。
//
// 官方只认 http、https 代理；网关多认 ALL_PROXY（all_proxy）和 socks5、socks5h 代理。
package upstream

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// proxyEnvKeys 读代理地址的环境变量，取第一个非空的。前四个的顺序同官方，ALL_PROXY 是网关多认的。
var proxyEnvKeys = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"}

// outboundProxy 出站代理。nil 表示不走代理，方法对 nil 也能调。
type outboundProxy struct {
	url *url.URL
	// env 代理地址取自哪个环境变量，记日志用。
	env string
	// noProxy NO_PROXY 拆开的各项，已去空白、转小写。
	noProxy []string
	// tlsConfig 和 https 代理本身握手用的配置；nil 用系统根证书（测试换成自签根）。
	tlsConfig *tls.Config
}

// envProxy 读代理环境变量（proxyFromEnv），记一行日志：用的是哪个代理，或者设了但用不了（那样直连）。
func envProxy() *outboundProxy {
	p, err := proxyFromEnv()
	if err != nil {
		log.Printf("upstream: WARN 代理环境变量用不了，出站直连：%v", err)
		return nil
	}
	if p != nil {
		log.Printf("upstream: 出站经代理 %s（取自 %s）；本机、NO_PROXY 里的地址和钉 IP 的连接直连", p, p.env)
	}
	return p
}

// proxyFromEnv 照官方读代理环境变量。都没设返回 nil；设了但用不了返回错误，错误里不带原值
// （代理地址里可能有密码）。
func proxyFromEnv() (*outboundProxy, error) {
	for _, key := range proxyEnvKeys {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		u, err := parseProxyURL(raw)
		if err != nil {
			return nil, fmt.Errorf("%s：%w", key, err)
		}
		noProxy := os.Getenv("NO_PROXY")
		if noProxy == "" {
			noProxy = os.Getenv("no_proxy")
		}
		return &outboundProxy{url: u, env: key, noProxy: splitNoProxy(noProxy)}, nil
	}
	return nil, nil
}

// parseProxyURL 解析代理地址：没写协议按 http://（同官方）；只认 http、https、socks5、socks5h。
func parseProxyURL(raw string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("不是合法的代理地址")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return u, nil
	}
	return nil, fmt.Errorf("不支持的协议 %s（只认 http、https、socks5、socks5h）", u.Scheme)
}

// splitNoProxy 拆 NO_PROXY：逗号分隔，去空白、转小写，丢掉空项。
func splitNoProxy(v string) []string {
	var out []string
	for _, e := range strings.Split(v, ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// String 代理的协议、主机和端口，不带用户名密码，记日志用。
func (p *outboundProxy) String() string {
	return p.url.Scheme + "://" + p.url.Host
}

// applies 报告到 host 的连接走不走代理：p 为 nil、host 是本机或在 NO_PROXY 里时不走。
// NO_PROXY 的规则同官方 ProxyResolver.shouldBypassProxy：* 全部直连；example.com 匹配它自己和子域；
// .example.com 只匹配子域；*.example.com 匹配它自己和子域。不认端口和网段。
func (p *outboundProxy) applies(host string) bool {
	if p == nil {
		return false
	}
	host = strings.ToLower(host)
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return false
	}
	for _, e := range p.noProxy {
		switch {
		case e == "*" || e == host:
			return false
		case strings.HasPrefix(e, "*."):
			if strings.HasSuffix(host, e[1:]) || host == e[2:] {
				return false
			}
		case strings.HasPrefix(e, "."):
			if strings.HasSuffix(host, e) {
				return false
			}
		case strings.HasSuffix(host, "."+e):
			return false
		}
	}
	return true
}

// proxyDial 返回 Transport 用的拨号：p 为 nil 时就是 d 直连；否则到不走代理的目标（见 applies）直连，
// 其余经代理建隧道。返回的连接已经通到 addr，上面直接做 TLS 或发 HTTP。
func proxyDial(p *outboundProxy, d *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	if p == nil {
		return d.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if !p.applies(host) {
			return d.DialContext(ctx, network, addr)
		}
		return p.dial(ctx, d, network, addr)
	}
}

// dial 经代理连到 addr。d 是连代理本身用的拨号器。
func (p *outboundProxy) dial(ctx context.Context, d *net.Dialer, network, addr string) (net.Conn, error) {
	if p.url.Scheme == "socks5" || p.url.Scheme == "socks5h" {
		// 协议已在 parseProxyURL 里限定，FromURL 不会出错。域名交给代理解析（x/net 对两种写法都这样，同 Go 标准库）。
		sd, _ := proxy.FromURL(p.url, d)
		conn, err := sd.(proxy.ContextDialer).DialContext(ctx, network, addr)
		if err != nil {
			return nil, fmt.Errorf("经代理 %s 连 %s：%w", p, addr, err)
		}
		return conn, nil
	}
	conn, err := d.DialContext(ctx, network, proxyHostPort(p.url))
	if err != nil {
		return nil, fmt.Errorf("连代理 %s：%w", p, err)
	}
	tunnel, err := p.connect(ctx, conn, addr)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tunnel, nil
}

// proxyHostPort 代理本身的 host:port，没写端口时 http 用 80、https 用 443（socks 的 1080 由 x/net 补）。
func proxyHostPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	port := "80"
	if u.Scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// connect 在连到 http、https 代理的 conn 上发 CONNECT addr，代理回 200 即隧道建好。https 代理先和代理
// 本身做一次标准库 TLS 握手（到代理这一段上游看不到）。CONNECT 只带 Host 和（地址里有用户名时）
// Proxy-Authorization。整个过程最多等 dialTimeout（ctx 先到期以 ctx 为准），ctx 取消时立刻打断：
// 代理连上了却不回话时不能一直挂着。
func (p *outboundProxy) connect(ctx context.Context, conn net.Conn, addr string) (net.Conn, error) {
	deadline := time.Now().Add(dialTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	if p.url.Scheme == "https" {
		tc := tls.Client(conn, tlsConfigForHost(p.tlsConfig, p.url.Hostname()))
		if err := tc.Handshake(); err != nil {
			return nil, fmt.Errorf("和代理 %s 握手：%w", p, err)
		}
		conn = tc
	}
	head := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
	if u := p.url.User; u != nil {
		pass, _ := u.Password()
		head += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pass)) + "\r\n"
	}
	if _, err := conn.Write([]byte(head + "\r\n")); err != nil {
		return nil, fmt.Errorf("向代理 %s 发 CONNECT %s：%w", p, addr, err)
	}
	// 读完回应就丢掉 bufio 不会吞掉隧道里的数据：TLS 和 HTTP 都是客户端先说话，代理回完 200 不会再发东西
	// （标准库的 CONNECT 也这样读）。回应体同样不读不关：200 没有长度时它会一直读到连接关闭。
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, fmt.Errorf("读代理 %s 对 CONNECT %s 的回应：%w", p, addr, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("代理 %s 拒绝 CONNECT %s：%s", p, addr, resp.Status)
	}
	// ctx 恰在这时取消的话 stop 返回 false，截止时间已被设成过去，接下来的握手或请求会立刻失败，照样报错。
	if stop() {
		_ = conn.SetDeadline(time.Time{})
	}
	return conn, nil
}
