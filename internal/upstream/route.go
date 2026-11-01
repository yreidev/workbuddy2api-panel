// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// route.go 按号选线路（代理池，见 internal/proxies）：号的请求都经 newAccountRequest 建，ctx 里标上号；
// 出站时 routedTransport 问 Router 这个号走哪个代理，每个代理一个独立的 Transport（连接池不混用，
// 一个号的连接不会被别的代理上的号复用）。没绑代理的号、不带号的请求（官方静态站点、models.dev）走
// 默认线路：环境变量里的代理或直连（proxy.go）。
//
//   - 号现在不能出站（开着「只许走号的代理」却没绑、在等代理、代理不通或到期）：不发请求，返回 Blocked
//     的 *RouteError。
//   - 连号的代理这一步失败（连不上代理、代理认证失败、代理拒绝转发）：返回 *RouteError，并报给 Router
//     （代理标不通，绑在上面的号暂停选号）；连上了也报，代理恢复得快。调用方取消的不报。
//   - RouteError 不是官方入口的问题：不切入口、不换 IP、不记入口熔断，调用方换个号再试。
//   - 地址层的钉 IP 重发（HTTPDNS 候选、备用 IP）也走号的代理：经代理连到那个 IP，握手和 Host 仍是官方
//     域名。官方客户端钉 IP 的连接一律直连（见 proxy.go），这里偏离官方，免得号的请求从服务器 IP 出去
//     （见 docs/official-client.md「出站代理」）。钉 IP 的结果不报给 Router：连不上可能只是那个 IP 的问题。
//   - HTTPDNS 查询本身照旧直连，不带号。
package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
)

// Router 号的线路，由代理池（internal/proxies.Registry）实现。
type Router interface {
	// RouteAccount 号 uid 走哪个代理：url 为空走默认线路；号现在不能出站时返回错误。
	RouteAccount(uid string) (id, name, url string, err error)
	// RouteProxy 代理 id 的名字和地址（登录新号时用，那时还没有号）。
	RouteProxy(id string) (name, url string, err error)
	// ReportProxy 真实请求连代理这一步的结果，err 为 nil 是连上了。
	ReportProxy(id string, err error)
}

// RouteError 号的线路出了问题，见文件头。
type RouteError struct {
	// Proxy 代理的名字；Blocked 为 true 时是号现在不能出站，请求没有发出去。
	Proxy   string
	Blocked bool
	Err     error
}

// Error 里的 IP 已经遮掉（logfmt.MaskIPs）：这个错误会进日志，没起名字的代理名字就是「IP:端口」。
func (e *RouteError) Error() string {
	if e.Blocked {
		return logfmt.MaskIPs(e.Err.Error())
	}
	return logfmt.MaskIPs(fmt.Sprintf("连不上号的代理「%s」：%v", e.Proxy, e.Err))
}

func (e *RouteError) Unwrap() error { return e.Err }

// IsRouteError 报告 err 是不是号的线路问题（见文件头）：调用方别罚号，换个号再试。
func IsRouteError(err error) bool {
	var re *RouteError
	return errors.As(err, &re)
}

// proxyDialError 号的代理在拨号这一步出的错（还没到官方入口）。
type proxyDialError struct{ err error }

func (e *proxyDialError) Error() string { return e.err.Error() }
func (e *proxyDialError) Unwrap() error { return e.err }

type routeCtxKey struct{}

// routeRef 请求标的线路：uid 非空按这个号绑的代理；否则 proxy 非空走这个代理（登录新号时）；都空走
// 默认线路。
type routeRef struct{ uid, proxy string }

// newAccountRequest 建号 a 的请求，ctx 里标上号。发给官方、带号的请求都要经它建。
func newAccountRequest(ctx context.Context, a *auth.Auth, method, url string, body io.Reader) (*http.Request, error) {
	return http.NewRequestWithContext(context.WithValue(ctx, routeCtxKey{}, routeRef{uid: a.UID}), method, url, body)
}

// withLoginRoute 登录请求的 ctx：标上登录用的代理（proxy 为空走默认线路）。
func withLoginRoute(ctx context.Context, proxy string) context.Context {
	return context.WithValue(ctx, routeCtxKey{}, routeRef{proxy: proxy})
}

// routeIdle 多久没用的线路在建新线路时清掉（代理删了、改了地址以后留下的）。
const routeIdle = time.Hour

// routeTable 各条线路的 Transport。由 newRouteTable 建。
type routeTable struct {
	// base 默认线路（环境变量里的代理或直连）；别的线路从它克隆，超时、连接池参数一样。
	base *http.Transport

	mu     sync.Mutex
	router Router
	cache  map[routeKey]*routeEntry
}

// routeKey 一条线路：代理地址（空为直连）和钉的 IP（空为不钉）。
type routeKey struct{ proxy, ip string }

type routeEntry struct {
	tr   *http.Transport
	used time.Time
}

func newRouteTable(base *http.Transport) *routeTable {
	return &routeTable{base: base, cache: map[routeKey]*routeEntry{}}
}

// SetRouter 装上号的线路（代理池）。在发出第一个号的请求之前调用；不装时都走默认线路。没经 New 建的
// Client（测试里直接拼的）没有线路表，装了也不起作用。
func (c *Client) SetRouter(r Router) {
	if c.routes == nil {
		return
	}
	c.routes.mu.Lock()
	defer c.routes.mu.Unlock()
	c.routes.router = r
}

// SetHeaderTimeout 设聊天 SSE 首字节前的超时：HeaderTimeout 和各条线路 Transport 的
// ResponseHeaderTimeout。在发出第一个请求之前调用。
func (c *Client) SetHeaderTimeout(d time.Duration) {
	c.HeaderTimeout = d
	if c.routes != nil {
		c.routes.base.ResponseHeaderTimeout = d
	}
}

// EnvProxyURL 默认线路用的代理地址（环境变量里的，含用户名密码），没有为空。代理池查服务器出口时用。
func (c *Client) EnvProxyURL() string {
	if c.proxy == nil {
		return ""
	}
	return c.proxy.url.String()
}

// ProbeProxy 经代理 rawURL 连 host:443 并做完 TLS 握手（和真实请求一样的 TLS 指纹，证书按 host 校验），
// 返回用时。代理池的健康检查用（proxies.Tester）。
func (c *Client) ProbeProxy(ctx context.Context, rawURL, host string) (time.Duration, error) {
	u, err := parseProxyURL(rawURL)
	if err != nil {
		return 0, err
	}
	px := &outboundProxy{url: u}
	d := newDialer()
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return px.dial(ctx, d, network, addr)
	}
	var base *tls.Config
	if c.routes != nil {
		base = c.routes.base.TLSClientConfig
	}
	start := time.Now()
	conn, err := dialTLSMimic(dial, base)(ctx, "tcp", net.JoinHostPort(host, "443"))
	if err != nil {
		return 0, err
	}
	_ = conn.Close()
	return time.Since(start), nil
}

// EntryHosts 两个域对话主入口的主机名（cn、global）：代理池的健康检查经代理测它们通不通。
func (c *Client) EntryHosts() map[string]string {
	host := func(base string) string {
		u, err := url.Parse(base)
		if err != nil {
			return ""
		}
		return u.Hostname()
	}
	return map[string]string{"cn": host(c.ChatBaseCN), "global": host(c.globalChatBase())}
}

// viaEnvProxy 报告号 a 到 host 的请求走不走环境变量里的代理：号没绑代理、环境变量的代理管这个域名。
func (c *Client) viaEnvProxy(a *auth.Auth, host string) bool {
	if !c.proxy.applies(host) {
		return false
	}
	r := c.routes.getRouter()
	if r == nil {
		return true
	}
	_, _, url, _ := r.RouteAccount(a.UID)
	return url == ""
}

func (t *routeTable) getRouter() Router {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.router
}

// routedTransport HTTP、ChatHTTP 共用的 RoundTripper：按请求标的线路选 Transport。
type routedTransport struct{ t *routeTable }

func (rt routedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return rt.t.roundTrip(req, "")
}

// CloseIdleConnections 清掉所有线路的空闲连接（传输层失败后，见 roundTripCloseIdle）。
func (rt routedTransport) CloseIdleConnections() {
	rt.t.base.CloseIdleConnections()
	rt.t.mu.Lock()
	defer rt.t.mu.Unlock()
	for _, e := range rt.t.cache {
		e.tr.CloseIdleConnections()
	}
}

// pin 地址层的钉 IP（Client.PinIP）：返回的 RoundTripper 按请求标的线路钉到 ip。
func (t *routeTable) pin(ip string) http.RoundTripper { return pinnedTransport{t, ip} }

type pinnedTransport struct {
	t  *routeTable
	ip string
}

func (pt pinnedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return pt.t.roundTrip(req, pt.ip)
}

// roundTrip 按请求标的线路发出去，ip 非空时钉到这个 IP。号的代理连不上的错误包成 RouteError，见文件头。
func (t *routeTable) roundTrip(req *http.Request, ip string) (*http.Response, error) {
	id, name, proxyURL, err := t.resolve(req.Context())
	if err != nil {
		return nil, &RouteError{Proxy: name, Blocked: true, Err: err}
	}
	tr, err := t.transport(proxyURL, ip)
	if err != nil {
		return nil, &RouteError{Proxy: name, Blocked: true, Err: err}
	}
	resp, err := tr.RoundTrip(req)
	if id == "" {
		return resp, err
	}
	var de *proxyDialError
	switch {
	case errors.As(err, &de) && req.Context().Err() == nil:
		if ip == "" {
			t.getRouter().ReportProxy(id, de.err)
		}
		return nil, &RouteError{Proxy: name, Err: de.err}
	case err == nil && ip == "":
		t.getRouter().ReportProxy(id, nil)
	}
	return resp, err
}

// resolve 请求走哪个代理：id 为空是默认线路。
func (t *routeTable) resolve(ctx context.Context) (id, name, url string, err error) {
	r := t.getRouter()
	ref, _ := ctx.Value(routeCtxKey{}).(routeRef)
	switch {
	case r == nil:
		return "", "", "", nil
	case ref.uid != "":
		return r.RouteAccount(ref.uid)
	case ref.proxy != "":
		name, url, err := r.RouteProxy(ref.proxy)
		return ref.proxy, name, url, err
	}
	return "", "", "", nil
}

// transport 一条线路的 Transport，建过的复用；建新的时顺手清掉一小时没用过的。
func (t *routeTable) transport(proxyURL, ip string) (*http.Transport, error) {
	if proxyURL == "" && ip == "" {
		return t.base, nil
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	key := routeKey{proxyURL, ip}
	if e := t.cache[key]; e != nil {
		e.used = now
		return e.tr, nil
	}
	var tr *http.Transport
	if proxyURL == "" {
		tr = pinTransport(t.base, ip)
	} else {
		u, err := parseProxyURL(proxyURL)
		if err != nil {
			return nil, err
		}
		tr = proxyTransport(t.base, &outboundProxy{url: u}, ip)
	}
	for k, e := range t.cache {
		if now.Sub(e.used) > routeIdle {
			e.tr.CloseIdleConnections()
			delete(t.cache, k)
		}
	}
	t.cache[key] = &routeEntry{tr: tr, used: now}
	return tr, nil
}

// proxyTransport 号的代理的 Transport：从 base 克隆，拨号经代理 px（本机地址照 applies 直连）；ip 非空时
// 经代理连到 ip（端口不变），SNI、证书校验和 Host 仍按请求的域名。连代理这一步的错误包成 proxyDialError。
func proxyTransport(base *http.Transport, px *outboundProxy, ip string) *http.Transport {
	tr := base.Clone()
	d := newDialer()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if ip != "" {
			host, addr = ip, net.JoinHostPort(ip, port)
		}
		if !px.applies(host) {
			return d.DialContext(ctx, network, addr)
		}
		conn, err := px.dial(ctx, d, network, addr)
		if err != nil {
			return nil, &proxyDialError{err}
		}
		return conn, nil
	}
	tr.DialTLSContext = dialTLSMimic(tr.DialContext, tr.TLSClientConfig)
	return tr
}
