// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// tlsmimic.go 把出站 TLS 握手伪装成官方桌面端客户端的 ClientHello（指纹对齐，降低被按
// 握手特征识别风控的概率）。官方对话/API 由内置 CLI 经 Node 的 https 发出，跑在 Electron 里用
// BoringSSL——它的 ClientHello（cipher 顺序、扩展顺序、无 GREASE、ALPN 只 http/1.1）既不同于
// Go 标准库，也不同于任何浏览器预设，所以只能照抓到的真实握手复刻。
//
// 做法：embed 一次本机实测抓到的官方 ClientHello 原始字节（clientHelloHex，见 docs/official-client.md
// 传输层一节），每次拨号用 uTLS 的 Fingerprinter 解析出一份全新的 ClientHelloSpec（避免扩展对象
// 跨连接共享），再用 uTLS 按这个 spec 握手。SNI、随机数、key_share 等每连接的值由 uTLS 现生成，
// embed 的那串里的具体值（含抓包时的 SNI）不会泄漏到实际连接。官方桌面端升级后重抓替换 hex 即可。
package upstream

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"sync/atomic"

	utls "github.com/refraction-networking/utls"
)

// clientHelloHex 官方桌面端（Electron 37.10.3 / Node v22.21.1 / BoringSSL）对话出站的真实
// ClientHello 原始字节，2026-10-28 本机用 Electron 的 node 打本地回环抓取。
// JA3 = 771,4865-4866-4867-49199-49195-49200-49196-49191-52393-52392-49161-49171-49162-49172-156-157-47-53,0-23-65281-10-11-35-16-13-51-45-43-21,29-23-24,0
// JA3 MD5 = 1a3153f314dc13e133dc71d113a81b16。
// JA4 = t13d1812h1_5d04281c6031_ef7df7f74e48。重抓办法见 docs/official-client.md 传输层一节。
const clientHelloHex = "1603010200010001fc0303c854f6906553b059bd822f4b371c12f4cdae1e87222d6dbfca48fb42ff2f209320fcb44c58dab9ddf31b16103315da81e9464bab6d0dbd0399a3bd1c994e88c0120024130113021303c02fc02bc030c02cc027cca9cca8c009c013c00ac014009c009d002f00350100018f0000001500130000107777772e776f726b62756464792e636e00170000ff01000100000a00080006001d00170018000b00020100002300000010000b000908687474702f312e31000d00140012040308040401050308050501080606010201003300260024001d002044239314445f0af4f95ee20e11f84987c1aa212756080c7366b8f08943492356002d00020101002b00050403040303001500f3000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

// clientHelloRaw 是 clientHelloHex 解出的字节，包初始化时解一次（格式固定，出错即编码问题）。
var clientHelloRaw = mustDecodeHex(clientHelloHex)

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("upstream: clientHelloHex 解码失败: " + err.Error())
	}
	return b
}

// tlsMimicEnabled 出站是否伪装官方客户端的 TLS 指纹（默认开）。main.go 按 config
// upstream.tls_fingerprint 设置；关掉则回落 Go 标准库 TLS 握手（旧行为）。原子变量，热更新安全。
var tlsMimicEnabled atomic.Bool

func init() { tlsMimicEnabled.Store(true) }

// SetTLSMimic 设置是否伪装 TLS 指纹（main.go 启动与配置热更新时调用）。
func SetTLSMimic(on bool) { tlsMimicEnabled.Store(on) }

// TLSMimicEnabled 当前是否在伪装 TLS 指纹（测试与面板状态可回读）。
func TLSMimicEnabled() bool { return tlsMimicEnabled.Load() }

// workbuddyClientHelloSpec 每次拨号解析出一份全新的 ClientHelloSpec。解析成本极低（几百字节），
// 每连接独立以免 uTLS 扩展对象跨连接共享出错。
func workbuddyClientHelloSpec() (*utls.ClientHelloSpec, error) {
	f := &utls.Fingerprinter{AllowBluntMimicry: true}
	spec, err := f.FingerprintClientHello(clientHelloRaw)
	if err != nil {
		return nil, fmt.Errorf("解析官方 ClientHello: %w", err)
	}
	return spec, nil
}

// dialTLSMimic 返回 http.Transport.DialTLSContext 用的拨号器：先用 dial 建 TCP 连接（直连、钉 IP 或
// 经代理的隧道，由调用方定），再在上面握手，SNI 与证书校验按 addr 里的域名。
// 开关开（默认）用 uTLS 照官方 ClientHello 握手，关则回落标准库 TLS。
//
// base 是所属 Transport 的 TLSClientConfig（可能 nil）。设了 DialTLSContext，Transport 就不再
// 套用自己的 TLSClientConfig，所以这里得接过来：RootCAs、InsecureSkipVerify、ServerName 等都从
// base 取，ServerName 缺省回落 addr 里的域名（钉 IP 时仍是原域名）。生产 base 为 nil（走系统根）。
//
// 握手超时由本函数自管：http.Transport.TLSHandshakeTimeout 只在 Transport 自己做 TLS 时生效，
// 用了 DialTLSContext 就得自己卡（否则握手挂起只能等 Client.Timeout）。
func dialTLSMimic(dial func(context.Context, string, string) (net.Conn, error), base *tls.Config) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		raw, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		// 握手超时：ctx 没自带 deadline 时补 tlsHandshakeTimeout（与旧 Transport 行为一致）。
		hctx := ctx
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			hctx, cancel = context.WithTimeout(ctx, tlsHandshakeTimeout)
			defer cancel()
		}
		conn, err := handshakeTLS(hctx, raw, host, base)
		if err != nil {
			_ = raw.Close()
			return nil, err
		}
		return conn, nil
	}
}

// tlsConfigForHost 从 base（可能 nil）克隆一份标准库 TLS 配置，ServerName 缺省用 host。
func tlsConfigForHost(base *tls.Config, host string) *tls.Config {
	cfg := &tls.Config{}
	if base != nil {
		cfg = base.Clone()
	}
	if cfg.ServerName == "" {
		cfg.ServerName = host
	}
	return cfg
}

// handshakeTLS 按开关做 uTLS 伪装握手或标准库握手。host 为 SNI/证书校验用的域名，base 为
// 所属 Transport 的 TLSClientConfig（RootCAs/校验开关等从中继承，见 dialTLSMimic）。
func handshakeTLS(ctx context.Context, raw net.Conn, host string, base *tls.Config) (net.Conn, error) {
	cfg := tlsConfigForHost(base, host)
	if !tlsMimicEnabled.Load() {
		conn := tls.Client(raw, cfg)
		if err := conn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		return conn, nil
	}
	spec, err := workbuddyClientHelloSpec()
	if err != nil {
		return nil, err
	}
	// 伪装路径用 uTLS 自己的 Config；版本由 spec 的 supported_versions 定，这里只继承
	// 证书校验相关字段（RootCAs/InsecureSkipVerify 用的都是标准 crypto/x509 类型）。
	uconn := utls.UClient(raw, &utls.Config{
		ServerName:         cfg.ServerName,
		RootCAs:            cfg.RootCAs,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}, utls.HelloCustom)
	if err := uconn.ApplyPreset(spec); err != nil {
		return nil, fmt.Errorf("套用官方 ClientHello: %w", err)
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return uconn, nil
}
