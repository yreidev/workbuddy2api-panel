// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/imroc/req/v3"
	"github.com/imroc/req/v3/http2"
	utls "github.com/refraction-networking/utls"
)

// profileHTTP2 把 ALPN、连接池、取消和帧编排交给 req 的 Transport；拨号仍用现有线路。
// Qoder 使用同一 Transport 的 HTTP/1.1 写入器，以保留 Bun 的头序和大小写。
// 标准库 Transport 只识别 crypto/tls.Conn，不能直接承接协商了 h2 的 uTLS 连接。
type profileHTTP2 struct {
	*req.Transport
	profile  ClientProfile
	sessions *profileSessionCache
}

// profileTLSConn 让 req 的初次连接与 HTTP/2 后续拨号都读取标准 TLS 状态。
// uTLS 的同名方法返回自己的类型，直接断言 req 的 TLS 接口会 panic。
type profileTLSConn struct {
	profileTLSHandshakeConn
	state tls.ConnectionState
}

type profileTLSHandshakeConn interface {
	net.Conn
	Handshake() error
	HandshakeContext(context.Context) error
}

func (c *profileTLSConn) ConnectionState() tls.ConnectionState { return c.state }

func (c *profileTLSConn) SetReadStateCallback(callback func(bool)) bool {
	if tlsConn, ok := c.profileTLSHandshakeConn.(interface{ NetConn() net.Conn }); ok {
		conn := tlsConn.NetConn()
		if proxyTLS, ok := conn.(*tls.Conn); ok {
			conn = proxyTLS.NetConn()
		}
		if pending, ok := conn.(interface{ SetReadStateCallback(func(bool)) bool }); ok {
			return pending.SetReadStateCallback(callback)
		}
	}
	return false
}

// 将 TLS 自身的 pending 通知交给 HTTP/2；TCP pending 仍可能被 BIO 缓冲吸收。
func (c *profileTLSConn) SetWritePendingCallback(callback func(bool)) {
	if tlsConn, ok := c.profileTLSHandshakeConn.(interface{ NetConn() net.Conn }); ok {
		conn := tlsConn.NetConn()
		// HTTPS CONNECT 仍通过外层 TLS 读写，只从其下层接收写阻塞通知。
		if proxyTLS, ok := conn.(*tls.Conn); ok {
			conn = proxyTLS.NetConn()
		}
		if pending, ok := conn.(interface{ SetWritePendingCallback(func(bool)) }); ok {
			pending.SetWritePendingCallback(callback)
		}
	}
}

// ttnetUploadBody 对齐官方 bodyProviderPair 的固定正文读法：首次数据和完成通知
// 分开发送；超过首块后，读取到已知正文结尾时可在最后一个 DATA 上结束。
type ttnetUploadBody struct {
	io.ReadCloser
	remaining int64
	readOnce  bool
}

func (b *ttnetUploadBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	if n > 0 && b.remaining == 0 && (err == nil || err == io.EOF) {
		if b.readOnce {
			err = io.EOF
		} else {
			err = nil
		}
	}
	if n > 0 {
		b.readOnce = true
	}
	return n, err
}

func newProfileHTTP2(base *http.Transport, profile ClientProfile) *profileHTTP2 {
	tr := req.NewTransport().DisableAutoDecode()
	// 代理在 DialContext 里建隧道，不能再读取环境代理或让库另走一条线路。
	tr.Proxy = nil
	tr.DialContext = base.DialContext
	if profile == ProfileTraeCode || profile == ProfileTraeWork || profile == ProfileTraeAuth {
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return base.DialContext(context.WithValue(ctx, profileProxyBIOKey{}, true), network, addr)
		}
	}
	tr.TLSClientConfig = base.TLSClientConfig
	tr.TLSHandshakeTimeout = base.TLSHandshakeTimeout
	tr.ResponseHeaderTimeout = base.ResponseHeaderTimeout
	tr.ExpectContinueTimeout = base.ExpectContinueTimeout
	tr.MaxIdleConns = base.MaxIdleConns
	tr.MaxIdleConnsPerHost = base.MaxIdleConnsPerHost
	tr.MaxConnsPerHost = base.MaxConnsPerHost
	tr.IdleConnTimeout = base.IdleConnTimeout
	tr.DisableCompression = base.DisableCompression
	tr.DisableKeepAlives = base.DisableKeepAlives
	var sessions *profileSessionCache
	if profile == ProfileTraeCode || profile == ProfileTraeWork || profile == ProfileTraeAuth {
		// Transport 按平台/代理/钉 IP 隔离，缓存内部按原始主机和端口隔离。
		sessions = newProfileSessionCache()
		if base.TLSClientConfig != nil && base.TLSClientConfig.Time != nil {
			sessions.now = base.TLSClientConfig.Time
		}
	}
	tr.TLSHandshakeContext = func(ctx context.Context, host string, raw net.Conn) (net.Conn, *tls.ConnectionState, error) {
		ctx, cancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
		defer cancel()
		var view *profileSessionCacheView
		var request profileSessionRequest
		if sessions != nil {
			request = ctx.Value(profileSessionRequestKey{}).(profileSessionRequest)
			view = sessions.newView(request.origin, false)
		}
		conn, err := handshakeTLSProfileWithCache(ctx, raw, host, base.TLSClientConfig, profile, view)
		if view != nil {
			retry := err != nil && view.selected.state != nil && ctx.Err() == nil && request.ctx.Err() == nil && retryProfileSessionHandshake(err)
			// 恢复早期失败不清除未用票据；TTNet 仅在紧接的重试跳过缓存。
			view.finishHandshake(retry)
			if retry {
				// Transport 拨号会分离请求的取消信号，恢复失败后的额外拨号仍随请求取消。
				retryCtx, cancelRetry := context.WithCancel(ctx)
				defer cancelRetry()
				stop := context.AfterFunc(request.ctx, cancelRetry)
				defer stop()
				_ = raw.Close()
				if err := request.ctx.Err(); err != nil {
					return nil, nil, err
				}
				raw, err = tr.DialContext(retryCtx, "tcp", view.origin)
				if err != nil {
					return nil, nil, fmt.Errorf("客户端 %d 恢复握手失败后重新拨号: %w", profile, err)
				}
				view = sessions.newView(view.origin, profile == ProfileTraeCode)
				conn, err = handshakeTLSProfileWithCache(retryCtx, raw, host, base.TLSClientConfig, profile, view)
				view.finishHandshake(err != nil && retryProfileSessionHandshake(err))
			}
		}
		if err != nil {
			_ = raw.Close()
			return nil, nil, err
		}
		var state tls.ConnectionState
		switch c := conn.(type) {
		case *utls.UConn:
			s := c.ConnectionState()
			state = tls.ConnectionState{
				Version: s.Version, HandshakeComplete: s.HandshakeComplete, DidResume: s.DidResume,
				CipherSuite: s.CipherSuite, NegotiatedProtocol: s.NegotiatedProtocol,
				NegotiatedProtocolIsMutual: true, //nolint:staticcheck // req/v3 仍读取此字段决定是否启用 h2；Go TLS 固定为 true。
				ServerName:                 s.ServerName, PeerCertificates: s.PeerCertificates, VerifiedChains: s.VerifiedChains,
				SignedCertificateTimestamps: s.SignedCertificateTimestamps, OCSPResponse: s.OCSPResponse, TLSUnique: s.TLSUnique,
			}
		case *tls.Conn:
			state = c.ConnectionState()
		}
		return &profileTLSConn{profileTLSHandshakeConn: conn.(profileTLSHandshakeConn), state: state}, &state, nil
	}
	if profile == ProfileTraeCode || profile == ProfileTraeWork || profile == ProfileTraeAuth {
		settings := []http2.Setting{{ID: 1, Val: 65536}, {ID: 2, Val: 0}}
		tr.SetHTTP2PathWithoutIndex(true)
		tr.SetHTTP2IndexOversized(true)
		tr.SetHTTP2ChromiumFlowControl(true)
		tr.SetHTTP2MaxUploadFrameSize(16375)
		tr.SetHTTP2StrictMaxConcurrentStreams(true)
		tr.SetHTTP2ChromiumUpload(true)
		if profile == ProfileTraeWork || profile == ProfileTraeAuth {
			tr.SetHTTP2RespectHeaderTableSize(true)
			tr.SetHTTP2ChromiumRetry(true)
		}
		if profile == ProfileTraeCode {
			settings = append(settings, http2.Setting{ID: 3, Val: 1000})
		}
		settings = append(settings, http2.Setting{ID: 4, Val: 6291456}, http2.Setting{ID: 6, Val: 262144})
		tr.SetHTTP2SettingsFrame(settings...)
		tr.SetHTTP2ConnectionFlow(15663105)
		weight := uint8(109)
		if profile != ProfileTraeAuth {
			weight = 219
		}
		tr.SetHTTP2HeaderPriority(http2.PriorityParam{Exclusive: true, StreamDep: 0, Weight: weight})
	}
	return &profileHTTP2{Transport: tr, profile: profile, sessions: sessions}
}

// 仅重试官方样本确认的恢复阶段 EOF／handshake_failure；尚未发送任何 HTTP 正文。
func retryProfileSessionHandshake(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var remote *net.OpError
	return errors.As(err, &remote) && remote.Op == "remote error" && remote.Err.Error() == "tls: handshake failure"
}

func (p *profileHTTP2) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	port := r.URL.Port()
	if port == "" {
		port = "443"
	}
	sessionRequest := profileSessionRequest{
		origin: net.JoinHostPort(strings.ToLower(r.URL.Hostname()), port),
		ctx:    r.Context(),
	}
	r = r.WithContext(context.WithValue(r.Context(), profileSessionRequestKey{}, sessionRequest))
	if err := prepareProfileUpload(r, p.profile); err != nil {
		return nil, err
	}
	if p.profile == ProfileQoder {
		qoderHeaderOrder(r)
	}
	if p.profile == ProfileTraeCode || p.profile == ProfileTraeWork || p.profile == ProfileTraeAuth {
		r.Header[req.PseudoHeaderOderKey] = []string{":method", ":authority", ":scheme", ":path"}
		if p.profile == ProfileTraeCode {
			// @aha-kit/net 先经 WHATWG Headers 排序，再由 TTNet 追加原生头。
			var order []string
			// 这里保存线上小写拼写，不能用 http.Header.Get 的规范化查询。
			headers := make(map[string][]string, len(r.Header)+3)
			for k := range r.Header {
				key := strings.ToLower(k)
				if key == "connection" || key == "host" || key == "content-length" || key == "transfer-encoding" || key == req.HeaderOderKey {
					continue
				}
				if key == "user-agent" || key == "accept-encoding" || key == req.PseudoHeaderOderKey {
					headers[k] = r.Header[k]
				} else {
					headers[key] = r.Header[k]
				}
				if key != req.PseudoHeaderOderKey && key != "user-agent" && key != "accept-encoding" && key != "x-request-id" && key != "x-tt-trace-id" {
					order = append(order, key)
				}
			}
			slices.Sort(order)
			if len(headers["x-request-id"]) == 0 || headers["x-request-id"][0] == "" {
				const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
				var id [10]byte
				for i := range id {
					id[i] = alphabet[rand.IntN(len(alphabet))]
				}
				headers["x-request-id"] = []string{string(id[:])}
			}
			if len(headers["x-tt-trace-id"]) == 0 || headers["x-tt-trace-id"][0] == "" {
				var deviceID string
				if values := headers["x-device-id"]; len(values) != 0 {
					deviceID = values[0]
				}
				headers["x-tt-trace-id"] = []string{newTTNetTraceID(deviceID)}
			}
			headers["Connection"] = []string{"keep-alive"}
			r.Header = headers
			r.Header[req.HeaderOderKey] = append([]string{"host", "connection", "transfer-encoding"}, append(order, "x-request-id", "x-tt-trace-id", "user-agent", "accept-encoding")...)
			if r.Body != nil && r.Body != http.NoBody {
				// 原生 fetch 不发送 content-length；首块内结束时另发空 END_STREAM。
				if r.GetBody != nil && r.ContentLength > 0 {
					r.Body = &ttnetUploadBody{ReadCloser: r.Body, remaining: r.ContentLength}
				}
				r.ContentLength = -1
				// 官方 bodyProviderPair 把正文交给不可回放的流；HTTP/2 拒绝后不重发 POST。
				r.GetBody = nil
				r.Header.Del("Content-Length")
			}
		} else if p.profile == ProfileTraeWork {
			r.Header[req.HeaderOderKey] = []string{"content-length", "x-trae-user-timezone", "sec-ch-ua-platform", "authorization", "x-trae-client-type", "sec-ch-ua", "x-preferenced-language", "sec-ch-ua-mobile", "user-agent", "accept", "content-type", "origin", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "referer", "accept-encoding", "accept-language", "priority"}
		} else {
			r.Header[req.HeaderOderKey] = []string{"content-length", "content-type", "x-cloudide-token", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "user-agent", "accept-encoding", "accept-language", "priority"}
		}
	}
	resp, err := p.Transport.RoundTrip(r)
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() && !errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("客户端 %d 的出站请求超时: %w", p.profile, errors.Join(err, context.DeadlineExceeded))
	}
	if err == nil && p.profile == ProfileTraeCode && resp.ProtoMajor == 2 && resp.ContentLength == 0 {
		// 官方原生桥在 Content-Length 为零时直接完成，不启动 Cronet 正文读取。
		// Close 仍释放原流；如果对端尚未结束，由既有取消路径清理。
		if closeErr := resp.Body.Close(); closeErr != nil {
			return nil, fmt.Errorf("TTNet 零长度响应关闭正文: %w", closeErr)
		}
		resp.Body = http.NoBody
	}
	return resp, err
}

func qoderHeaderOrder(r *http.Request) {
	if r.Header.Get("Accept-Encoding") == "" {
		r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	}
	// 原生 CLI 的 WHATWG Headers 使用以下拼写；Go 默认会把 Id/OS 写成 Id/Os。
	spellings := map[string]string{
		"cosy-machineid": "Cosy-MachineId", "cosy-machineos": "Cosy-MachineOS",
		"cosy-machinecode": "Cosy-MachineCode", "cosy-machinetoken": "Cosy-MachineToken",
		"cosy-machinetype": "Cosy-MachineType", "cosy-machinehostname": "Cosy-MachineHostname",
		"cosy-clienttype": "Cosy-ClientType", "cosy-clientip": "Cosy-ClientIp",
		"cosy-data-policy": "Cosy-Data-Policy", "cosy-business-product": "Cosy-Business-Product",
		"cosy-business-type": "Cosy-Business-Type", "x-request-id": "X-Request-ID",
		"x-model-key": "X-Model-Key", "x-model-source": "X-Model-Source", "traceparent": "traceparent",
	}
	var order []string
	headers := make(http.Header, len(r.Header)+2)
	for k, v := range r.Header {
		key := strings.ToLower(k)
		if spelling := spellings[key]; spelling != "" && spelling != k {
			k = spelling
		}
		headers[k] = v
		if key == "host" || key == "content-length" || key == "connection" || key == "user-agent" || key == "accept-encoding" {
			continue
		}
		order = append(order, key)
	}
	r.Header = headers
	if r.Header.Get("Accept-Encoding") == "identity" {
		order = append(order, "accept-encoding")
	}
	if r.Header.Get("Cache-Control") != "" {
		order = append(order, "connection")
	}
	if strings.HasPrefix(r.URL.Path, "/api/") && r.UserAgent() == "qoder/1.1.89" {
		order = append(order, "user-agent")
	}
	slices.Sort(order)
	if r.Header.Get("Connection") == "" {
		r.Header.Set("Connection", "keep-alive")
	}
	for _, key := range []string{"connection", "user-agent", "host", "accept-encoding", "content-length"} {
		if !slices.Contains(order, key) {
			order = append(order, key)
		}
	}
	r.Header[req.HeaderOderKey] = order
}
