// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// targetJA3MD5 官方桌面端（Electron 37.10.3 / Node v22.21.1 / BoringSSL）对话出站 ClientHello 的
// JA3 MD5，2026-10-28 本机实测（见 tlsmimic.go clientHelloHex 与 docs/official-client.md 传输层）。
// 这个常量锁住指纹：embed 的 ClientHello 或 uTLS 复刻有任何漂移，这里立刻红。
const targetJA3MD5 = "1a3153f314dc13e133dc71d113a81b16"

// targetJA4 同一份官方 ClientHello（clientHelloHex）按 FoxIO spec 算出的 JA4（TLS client）指纹。
// 前缀 t13d1812h1 = TCP、TLS1.3、有 SNI、18 个密码、12 个扩展、ALPN http/1.1；后两段是密码与
// 扩展+签名算法的 sha256 前 12。JA4 比 JA3 多钉签名算法扩展的内容；embed 的 hello 或 uTLS 复刻漂移时这里会红。
const targetJA4 = "t13d1812h1_5d04281c6031_ef7df7f74e48"

// captureClientHello 起一个本地回环监听，拨号方握手时把它的 ClientHello 原始字节抓下来返回。
// 不回 ServerHello（抓到第一段即够），握手随后失败由调用方忽略。
func captureClientHello(t *testing.T, dial func(ctx context.Context, network, addr string) (net.Conn, error)) []byte {
	t.Helper()
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
		buf := make([]byte, 0, 8192)
		tmp := make([]byte, 4096)
		for {
			n, rerr := conn.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if rerr != nil || n == 0 || len(buf) > 4096 {
				break
			}
		}
		got <- buf
	}()
	// 用主机名 localhost（而非监听器的 127.0.0.1）当 dial 目标：ServerName 是 IP 时 TLS 规范下
	// SNI 扩展会被省略，JA3 会少扩展 0；生产里出站 addr 永远是域名，这里照此用主机名。
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", net.JoinHostPort("localhost", port))
	if conn != nil {
		_ = conn.Close()
	}
	_ = err // 握手必失败（没有真服务器），只要 ClientHello 发出即可
	select {
	case b := <-got:
		if len(b) == 0 {
			t.Fatal("没抓到 ClientHello")
		}
		return b
	case <-time.After(3 * time.Second):
		t.Fatal("抓 ClientHello 超时")
		return nil
	}
}

func isGREASEValue(v uint16) bool { return v&0x0f0f == 0x0a0a && v>>8 == v&0xff }

// computeJA3 从 ClientHello 记录算 JA3 字符串（版本,密码,扩展,曲线,点格式；去 GREASE）。
func computeJA3(rec []byte) string {
	b := rec[9:]
	ver := binary.BigEndian.Uint16(b[0:2])
	p := 2 + 32
	p += 1 + int(b[p]) // session id
	csLen := int(binary.BigEndian.Uint16(b[p : p+2]))
	p += 2
	var ciphers []string
	for i := 0; i < csLen; i += 2 {
		if c := binary.BigEndian.Uint16(b[p+i : p+i+2]); !isGREASEValue(c) {
			ciphers = append(ciphers, fmt.Sprintf("%d", c))
		}
	}
	p += csLen
	p += 1 + int(b[p]) // compression methods
	extTotal := int(binary.BigEndian.Uint16(b[p : p+2]))
	p += 2
	end := p + extTotal
	var exts, curves, points []string
	for p < end {
		et := binary.BigEndian.Uint16(b[p : p+2])
		el := int(binary.BigEndian.Uint16(b[p+2 : p+4]))
		body := b[p+4 : p+4+el]
		if !isGREASEValue(et) {
			exts = append(exts, fmt.Sprintf("%d", et))
		}
		switch et {
		case 10: // supported_groups
			n := int(binary.BigEndian.Uint16(body[0:2]))
			for i := 0; i < n; i += 2 {
				if g := binary.BigEndian.Uint16(body[2+i : 2+i+2]); !isGREASEValue(g) {
					curves = append(curves, fmt.Sprintf("%d", g))
				}
			}
		case 11: // ec_point_formats
			for i := 0; i < int(body[0]); i++ {
				points = append(points, fmt.Sprintf("%d", body[1+i]))
			}
		}
		p += 4 + el
	}
	return fmt.Sprintf("%d,%s,%s,%s,%s", ver, strings.Join(ciphers, "-"),
		strings.Join(exts, "-"), strings.Join(curves, "-"), strings.Join(points, "-"))
}

func ja3MD5(rec []byte) string {
	sum := md5.Sum([]byte(computeJA3(rec)))
	return hex.EncodeToString(sum[:])
}

// sha12 算 SHA-256 取前 12 位小写 hex（JA4 的 b/c 段用）。
func sha12(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// ja4ALPNCode 照 FoxIO spec 取第一个 ALPN 值的两字符编码：首尾字节都是 ASCII 字母数字就用
// 那两个字符，否则用整值小写 hex 的首尾 hex 字符；无 ALPN 为 "00"。
func ja4ALPNCode(v string) string {
	if v == "" {
		return "00"
	}
	alnum := func(c byte) bool {
		return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
	}
	if f, l := v[0], v[len(v)-1]; alnum(f) && alnum(l) {
		return string([]byte{f, l})
	}
	h := hex.EncodeToString([]byte(v))
	return string([]byte{h[0], h[len(h)-1]})
}

// computeJA4 从 ClientHello 记录算 JA4（TLS client）指纹，照 FoxIO 官方 spec：
// a 段 t + TLS 版本 + 有无 SNI + 密码数 + 扩展数（含 SNI/ALPN）+ ALPN 首尾；
// b 段密码套件去 GREASE、升序排序后 sha256 取前 12；
// c 段扩展去 GREASE 且去 SNI(0)/ALPN(16)、升序排序，拼签名算法（原序、不排序）后 sha256 取前 12。
// 比 JA3 多钉签名算法扩展的内容。
func computeJA4(rec []byte) string {
	b := rec[9:]
	p := 2 + 32
	p += 1 + int(b[p]) // session id
	csLen := int(binary.BigEndian.Uint16(b[p : p+2]))
	p += 2
	var ciphers []uint16
	for i := 0; i < csLen; i += 2 {
		if c := binary.BigEndian.Uint16(b[p+i : p+i+2]); !isGREASEValue(c) {
			ciphers = append(ciphers, c)
		}
	}
	p += csLen
	p += 1 + int(b[p]) // compression methods
	extTotal := int(binary.BigEndian.Uint16(b[p : p+2]))
	p += 2
	end := p + extTotal
	var exts, sigAlgs []uint16
	hasSNI := false
	var alpn string
	tlsVer := binary.BigEndian.Uint16(rec[1:3]) // record 版本，supported_versions 缺省时回落
	for p < end {
		et := binary.BigEndian.Uint16(b[p : p+2])
		el := int(binary.BigEndian.Uint16(b[p+2 : p+4]))
		body := b[p+4 : p+4+el]
		if !isGREASEValue(et) {
			exts = append(exts, et)
		}
		switch et {
		case 0: // server_name
			hasSNI = true
		case 16: // ALPN：list len(2) + proto len(1) + proto...
			if len(body) >= 3 {
				if n := int(body[2]); n > 0 && 3+n <= len(body) {
					alpn = string(body[3 : 3+n])
				}
			}
		case 13: // signature_algorithms：list len(2) + 一串 2 字节
			if len(body) >= 2 {
				n := int(binary.BigEndian.Uint16(body[0:2]))
				for i := 0; i+2 <= n && 2+i+2 <= len(body); i += 2 {
					sigAlgs = append(sigAlgs, binary.BigEndian.Uint16(body[2+i:2+i+2]))
				}
			}
		case 43: // supported_versions：list len(1) + 一串 2 字节，取最高（去 GREASE）
			if len(body) >= 1 {
				n := int(body[0])
				var best uint16
				for i := 0; i+2 <= n && 1+i+2 <= len(body); i += 2 {
					if v := binary.BigEndian.Uint16(body[1+i : 1+i+2]); !isGREASEValue(v) && v > best {
						best = v
					}
				}
				if best != 0 {
					tlsVer = best
				}
			}
		}
		p += 4 + el
	}

	verCode := map[uint16]string{
		0x0304: "13", 0x0303: "12", 0x0302: "11", 0x0301: "10",
		0x0300: "s3", 0x0002: "s2", 0xfeff: "d1", 0xfefd: "d2", 0xfefc: "d3",
	}
	vc := verCode[tlsVer]
	if vc == "" {
		vc = "00"
	}
	sni := "i"
	if hasSNI {
		sni = "d"
	}
	cc, ec := len(ciphers), len(exts)
	if cc > 99 {
		cc = 99
	}
	if ec > 99 {
		ec = 99
	}
	a := fmt.Sprintf("t%s%s%02d%02d%s", vc, sni, cc, ec, ja4ALPNCode(alpn))

	hexSorted := func(vs []uint16) []string {
		s := append([]uint16(nil), vs...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		out := make([]string, len(s))
		for i, v := range s {
			out[i] = fmt.Sprintf("%04x", v)
		}
		return out
	}

	bPart := "000000000000"
	if len(ciphers) > 0 {
		bPart = sha12(strings.Join(hexSorted(ciphers), ","))
	}

	var ce []uint16
	for _, e := range exts {
		if e != 0x0000 && e != 0x0010 {
			ce = append(ce, e)
		}
	}
	cPart := "000000000000"
	if len(ce) > 0 {
		input := strings.Join(hexSorted(ce), ",")
		if len(sigAlgs) > 0 {
			sparts := make([]string, len(sigAlgs))
			for i, s := range sigAlgs {
				sparts[i] = fmt.Sprintf("%04x", s) // 签名算法保持原序
			}
			input += "_" + strings.Join(sparts, ",")
		}
		cPart = sha12(input)
	}

	return a + "_" + bPart + "_" + cPart
}

// sniOf 从 ClientHello 取 server_name（扩展 0）的主机名，验证钉 IP 时 SNI 仍是域名。
func sniOf(rec []byte) string {
	b := rec[9:]
	p := 2 + 32
	p += 1 + int(b[p])
	p += 2 + int(binary.BigEndian.Uint16(b[p:p+2])) // cipher suites
	p += 1 + int(b[p])                              // compression
	end := p + 2 + int(binary.BigEndian.Uint16(b[p:p+2]))
	p += 2
	for p < end {
		et := binary.BigEndian.Uint16(b[p : p+2])
		el := int(binary.BigEndian.Uint16(b[p+2 : p+4]))
		if et == 0 { // server_name
			body := b[p+4 : p+4+el]
			// server_name_list(2) + type(1) + len(2) + host
			nameLen := int(binary.BigEndian.Uint16(body[3:5]))
			return string(body[5 : 5+nameLen])
		}
		p += 4 + el
	}
	return ""
}

// TestTLSMimicJA3 默认开启时，newTransport 出站的 ClientHello JA3 必须等于官方桌面端的目标指纹。
func TestTLSMimicJA3(t *testing.T) {
	t.Cleanup(func() { SetTLSMimic(true) })
	SetTLSMimic(true)
	tr := newTransport(nil)
	rec := captureClientHello(t, tr.DialTLSContext)
	if got := ja3MD5(rec); got != targetJA3MD5 {
		t.Errorf("出站 JA3 MD5 = %s, want %s\nJA3=%s", got, targetJA3MD5, computeJA3(rec))
	}
}

// TestTLSMimicJA4 默认开启时出站 ClientHello 的 JA4 必须等于官方桌面端目标指纹（多钉签名算法）。
func TestTLSMimicJA4(t *testing.T) {
	t.Cleanup(func() { SetTLSMimic(true) })
	SetTLSMimic(true)
	tr := newTransport(nil)
	rec := captureClientHello(t, tr.DialTLSContext)
	if got := computeJA4(rec); got != targetJA4 {
		t.Errorf("出站 JA4 = %s, want %s", got, targetJA4)
	}
}

// TestTLSMimicDisabled 关掉开关回落标准库 TLS：JA3 必须变（不再是官方指纹）。
func TestTLSMimicDisabled(t *testing.T) {
	t.Cleanup(func() { SetTLSMimic(true) })
	SetTLSMimic(false)
	if TLSMimicEnabled() {
		t.Fatal("SetTLSMimic(false) 后 TLSMimicEnabled 仍为真")
	}
	tr := newTransport(nil)
	rec := captureClientHello(t, tr.DialTLSContext)
	if got := ja3MD5(rec); got == targetJA3MD5 {
		t.Error("关掉伪装后 JA3 仍等于官方指纹，开关没生效")
	}
}

// TestTLSMimicPinnedIP 钉 IP 的 Transport：TCP 连到钉的 IP，SNI 仍是请求域名，JA3 仍是官方指纹。
func TestTLSMimicPinnedIP(t *testing.T) {
	t.Cleanup(func() { SetTLSMimic(true) })
	SetTLSMimic(true)
	tr := pinTransport(newTransport(nil), "127.0.0.1")
	if tr.DialTLSContext == nil {
		t.Fatal("钉 IP Transport 应设 DialTLSContext")
	}
	// addr 用一个不可解析的域名+端口；能连上说明 TCP 确实钉到了 127.0.0.1。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	got := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 0, 8192)
		tmp := make([]byte, 4096)
		for {
			n, rerr := conn.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if rerr != nil || n == 0 || len(buf) > 4096 {
				break
			}
		}
		got <- buf
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _ := tr.DialTLSContext(ctx, "tcp", net.JoinHostPort("www.workbuddy.cn", port))
	if conn != nil {
		_ = conn.Close()
	}
	rec := <-got
	if len(rec) == 0 {
		t.Fatal("钉 IP 后没连到 127.0.0.1（没抓到 ClientHello）")
	}
	if got := ja3MD5(rec); got != targetJA3MD5 {
		t.Errorf("钉 IP 出站 JA3 MD5 = %s, want %s", got, targetJA3MD5)
	}
	if sni := sniOf(rec); sni != "www.workbuddy.cn" {
		t.Errorf("钉 IP 后 SNI = %q, 应为请求域名 www.workbuddy.cn", sni)
	}
}

// TestTLSMimicRealHandshake 用真 TLS 服务器验证：伪装开/关两种都能真正完成握手——不止发对 ClientHello，
// uTLS 照 spec 复刻出的握手要能走通并通过证书校验。覆盖 handshakeTLS 两条成功返回与 dialTLSMimic 的成功路径。
// 自签根经 TLSClientConfig.RootCAs 传入（生产为 nil 走系统根），ServerName=127.0.0.1 按 IP SAN 校验。
func TestTLSMimicRealHandshake(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	cfg := &tls.Config{RootCAs: pool}
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	for _, mimic := range []bool{true, false} {
		name := "伪装关走标准库"
		if mimic {
			name = "伪装开走uTLS"
		}
		t.Run(name, func(t *testing.T) {
			t.Cleanup(func() { SetTLSMimic(true) })
			SetTLSMimic(mimic)
			dial := dialTLSMimic(newDialer().DialContext, cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := dial(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
			if err != nil {
				t.Fatalf("握手应成功: %v", err)
			}
			_ = conn.Close()
		})
	}
}

// TestMustDecodeHexPanic 坏 hex 必须 panic——这是 clientHelloHex 写错的守卫，正常编译期常量走不到这条。
func TestMustDecodeHexPanic(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("坏 hex 应当 panic")
		}
	}()
	_ = mustDecodeHex("zz 不是 hex")
}

// TestTLSMimicBadClientHello 把 clientHelloRaw 换成解析不了的字节（模拟将来重抓时写坏），
// spec 解析与握手都要显式报错——绝不能静默发出畸形握手。覆盖 workbuddyClientHelloSpec 与
// handshakeTLS 的解析失败分支。测试串行跑，临时改包级 var 后即刻还原。
func TestTLSMimicBadClientHello(t *testing.T) {
	t.Cleanup(func() { SetTLSMimic(true) })
	SetTLSMimic(true)
	old := clientHelloRaw
	clientHelloRaw = []byte{0x01, 0x02, 0x03} // 不是合法 ClientHello
	defer func() { clientHelloRaw = old }()

	if _, err := workbuddyClientHelloSpec(); err == nil {
		t.Error("坏 ClientHello 的 spec 解析应报错")
	}
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	if _, err := handshakeTLS(context.Background(), c1, "example.com", nil); err == nil {
		t.Error("坏 ClientHello 的握手应报错")
	}
}

// TestDialTLSMimicErrors 覆盖拨号的错误分支：addr 没端口（SplitHostPort 失败）、连不上（dial 失败，
// 走 ctx 无 deadline 补握手超时那条）。
func TestDialTLSMimicErrors(t *testing.T) {
	dial := dialTLSMimic(newDialer().DialContext, nil)
	if _, err := dial(context.Background(), "tcp", "没有端口的地址"); err == nil {
		t.Error("addr 缺端口应报错")
	}
	// 连一个没人监听的回环端口：ctx 用 Background（无 deadline，覆盖补握手超时分支），拨号必失败。
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close() // 立刻关掉，确保连不上
	if _, err := dial(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port)); err == nil {
		t.Error("连不上的端口应报错")
	}
}

// TestWorkbuddyClientHelloSpec 每次解析都得到独立的 spec（非 nil，扩展数一致），不跨连接共享。
func TestWorkbuddyClientHelloSpec(t *testing.T) {
	s1, err := workbuddyClientHelloSpec()
	if err != nil {
		t.Fatalf("解析 spec: %v", err)
	}
	s2, err := workbuddyClientHelloSpec()
	if err != nil {
		t.Fatalf("解析 spec: %v", err)
	}
	if s1 == s2 {
		t.Error("两次解析返回了同一个 spec 指针，应各自独立")
	}
	if len(s1.CipherSuites) != 18 || len(s1.Extensions) != 12 {
		t.Errorf("spec 结构漂移：cipher %d（期望 18）、ext %d（期望 12）", len(s1.CipherSuites), len(s1.Extensions))
	}
}
