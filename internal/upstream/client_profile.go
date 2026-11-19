// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	_ "embed"
	"fmt"
	"net"
	"net/http"

	utls "github.com/refraction-networking/utls"
)

// ClientProfile 按官方请求的运行时隔离握手与连接池，零值保持 WorkBuddy 行为。
type ClientProfile uint8

const (
	ProfileWorkBuddy ClientProfile = iota
	ProfileQoder
	ProfileTraeCode
	ProfileTraeWork
	ProfileTraeAuth
)

// 以下是 2026-11-16 官方运行时样本，来源和边界见 docs/official-client.md。
//
//go:embed clienthello/qoder-bun142.bin
var qoderClientHello []byte

//go:embed clienthello/trae-code-ttnet.bin
var traeCodeClientHello []byte

//go:embed clienthello/trae-work-chrome156.bin
var traeWorkClientHello []byte

//go:embed clienthello/trae-auth-electron39.bin
var traeAuthClientHello []byte

type clientProfileKey struct{}

func WithClientProfile(ctx context.Context, profile ClientProfile) context.Context {
	return context.WithValue(ctx, clientProfileKey{}, profile)
}

// ClientProfileFromContext 读取请求指定的传输配置，没指定时沿用 WorkBuddy。
func ClientProfileFromContext(ctx context.Context) ClientProfile {
	profile, _ := ctx.Value(clientProfileKey{}).(ClientProfile)
	return profile
}

type profileRoundTripper struct {
	base    http.RoundTripper
	profile ClientProfile
}

func (p profileRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, ok := req.Context().Value(clientProfileKey{}).(ClientProfile); !ok {
		req = req.WithContext(WithClientProfile(req.Context(), p.profile))
	}
	return p.base.RoundTrip(req)
}

func (p profileRoundTripper) CloseIdleConnections() { roundTripCloseIdle(p.base) }

// HTTPClientForProfile 复用线路路由与超时配置，平台身份只通过请求上下文传递。
func HTTPClientForProfile(base *http.Client, profile ClientProfile) *http.Client {
	client := *base
	rt := base.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	client.Transport = profileRoundTripper{rt, profile}
	return &client
}

func clientHelloSpec(profile ClientProfile) (*utls.ClientHelloSpec, error) {
	var raw []byte
	switch profile {
	case ProfileWorkBuddy:
		return workbuddyClientHelloSpec()
	case ProfileQoder:
		raw = qoderClientHello
	case ProfileTraeCode:
		raw = traeCodeClientHello
	case ProfileTraeWork:
		raw = traeWorkClientHello
	case ProfileTraeAuth:
		raw = traeAuthClientHello
	default:
		return nil, fmt.Errorf("未知客户端传输配置：%d", profile)
	}
	spec, err := (&utls.Fingerprinter{AllowBluntMimicry: true}).FingerprintClientHello(raw)
	if err != nil {
		return nil, fmt.Errorf("解析客户端 %d 的官方 ClientHello: %w", profile, err)
	}
	if profile == ProfileTraeWork || profile == ProfileTraeAuth {
		// Chrome 每次握手随机排列扩展；TTNet 样本则保持固定顺序。
		spec.Extensions = utls.ShuffleChromeTLSExtensions(spec.Extensions)
	}
	for _, ext := range spec.Extensions {
		if sigs, ok := ext.(*utls.SignatureAlgorithmsExtension); ok {
			for i, sig := range sigs.SupportedSignatureAlgorithms {
				if isGREASEValue(uint16(sig)) {
					// uTLS 不会重写签名算法里的 GREASE，不能沿用抓包时的随机值。
					var random [1]byte
					_, _ = rand.Read(random[:])
					sigs.SupportedSignatureAlgorithms[i] = utls.SignatureScheme(0x0a0a + uint16(random[0]&15)*0x1010)
				}
			}
		}
	}
	return spec, nil
}

func isGREASEValue(v uint16) bool { return v&0x0f0f == 0x0a0a && v>>8 == v&0xff }

func dialTLSProfile(dial func(context.Context, string, string) (net.Conn, error), base *tls.Config, profile ClientProfile) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		raw, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		hctx, cancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
		defer cancel()
		conn, err := handshakeTLSProfile(hctx, raw, host, base, profile)
		if err != nil {
			_ = raw.Close()
		}
		return conn, err
	}
}

func handshakeTLSProfile(ctx context.Context, raw net.Conn, host string, base *tls.Config, profile ClientProfile) (net.Conn, error) {
	return handshakeTLSProfileWithCache(ctx, raw, host, base, profile, nil)
}

func handshakeTLSProfileWithCache(ctx context.Context, raw net.Conn, host string, base *tls.Config, profile ClientProfile, cache *profileSessionCacheView) (net.Conn, error) {
	if profile == ProfileWorkBuddy {
		return handshakeTLS(ctx, raw, host, base)
	}
	cfg := tlsConfigForHost(base, host)
	if !TLSMimicEnabled() {
		cfg.NextProtos = []string{"http/1.1"}
		if profile == ProfileTraeCode || profile == ProfileTraeWork || profile == ProfileTraeAuth {
			cfg.NextProtos = []string{"h2", "http/1.1"}
		}
		conn := tls.Client(raw, cfg)
		return conn, conn.HandshakeContext(ctx)
	}
	spec, err := clientHelloSpec(profile)
	if err != nil {
		return nil, err
	}
	if cache != nil {
		// 官方 TTNet/Chromium 的重连样本会在末尾追加 PSK；首次握手隐藏空扩展。
		spec.Extensions = append(spec.Extensions, &utls.UtlsPreSharedKeyExtension{})
	}
	if profile == ProfileTraeCode || profile == ProfileTraeWork || profile == ProfileTraeAuth {
		raw, err = newProfileSocketBIO(raw)
		if err != nil {
			return nil, fmt.Errorf("初始化客户端 %d 的 TLS 写缓冲: %w", profile, err)
		}
	}
	clientConfig := &utls.Config{
		ServerName: cfg.ServerName, RootCAs: cfg.RootCAs, InsecureSkipVerify: cfg.InsecureSkipVerify,
		Time:         cfg.Time,
		OmitEmptyPsk: true, SessionTicketsDisabled: cfg.SessionTicketsDisabled,
		// 官方 Trae／Qoder 运行时从首次正文起使用 16 KiB 记录上限，
		// 不采用 Go 的递增记录大小，TLS 1.2／1.3 均已采样。
		DynamicRecordSizingDisabled: true,
	}
	if cache != nil {
		clientConfig.ClientSessionCache = cache
		// 证书校验或会话恢复确认后、写入 TLS 1.2 替换票据或 TLS 1.3 新票据前调用。
		clientConfig.VerifyConnection = func(s utls.ConnectionState) error {
			if s.DidResume {
				cache.authenticatedAt = cache.selected.authenticatedAt
			}
			return nil
		}
	}
	conn := utls.UClient(raw, clientConfig, utls.HelloCustom)
	if err := conn.ApplyPreset(spec); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("套用客户端 %d 的官方 ClientHello: %w", profile, err)
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}
