// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestWorkBuddyLatestRuntimeFixture(t *testing.T) {
	raw, err := os.ReadFile("clienthello/workbuddy-electron37.bin")
	if err != nil {
		t.Fatal(err)
	}
	if computeJA3(raw) != computeJA3(clientHelloRaw) || computeJA4(raw) != computeJA4(clientHelloRaw) {
		t.Fatal("5.11.3 的 Electron 37.10.3 样本已偏离 WorkBuddy 传输基准")
	}
}

func TestQoderNativeRegionalFixtures(t *testing.T) {
	global, err := os.ReadFile("clienthello/qoder-bun142-global.bin")
	if err != nil {
		t.Fatal(err)
	}
	if ja3MD5(qoderClientHello) != "1523504b38f0fae0d881d4b6554aac1b" || computeJA4(qoderClientHello) != "t13d1713h1_5b57614c22b0_6a3d802a7139" || !reflect.DeepEqual(clientHelloFields(global), clientHelloFields(qoderClientHello)) {
		t.Fatal("Qoder 原生国内/国际的 TLS 稳定字段偏离采样基准")
	}
}

func TestClientProfileHello(t *testing.T) {
	for _, tc := range []struct {
		name        string
		profile     ClientProfile
		sample      []byte
		randomOrder bool
	}{
		{"workbuddy", ProfileWorkBuddy, clientHelloRaw, false},
		{"qoder", ProfileQoder, qoderClientHello, false},
		{"trae-code", ProfileTraeCode, traeCodeClientHello, false},
		{"trae-work", ProfileTraeWork, traeWorkClientHello, true},
		{"trae-auth", ProfileTraeAuth, traeAuthClientHello, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := captureClientHello(t, dialTLSProfile(newDialer().DialContext, nil, tc.profile))
			if want := computeJA4(tc.sample); computeJA4(got) != want {
				t.Fatalf("JA4 = %s，官方样本 %s", computeJA4(got), want)
			}
			if !tc.randomOrder && computeJA3(got) != computeJA3(tc.sample) {
				t.Fatalf("JA3 = %s，官方样本 %s", computeJA3(got), computeJA3(tc.sample))
			}
			if want, fields := clientHelloFields(tc.sample), clientHelloFields(got); !reflect.DeepEqual(want, fields) {
				for key, value := range want {
					if fields[key] != value {
						t.Errorf("ClientHello %s = %s，官方样本 %s", key, fields[key], value)
					}
				}
				for key := range fields {
					if _, exists := want[key]; !exists {
						t.Errorf("ClientHello 出现样本没有的字段 %s", key)
					}
				}
			}
			again := captureClientHello(t, dialTLSProfile(newDialer().DialContext, nil, tc.profile))
			for key, secret := range clientHelloEphemeral(got) {
				if secret == clientHelloEphemeral(again)[key] || secret == clientHelloEphemeral(tc.sample)[key] {
					t.Errorf("%s 复用了抓包或上一条连接的随机值", key)
				}
			}
			t.Logf("JA3=%s JA4=%s", ja3MD5(got), computeJA4(got))
		})
	}
}

func TestChromeSignatureGREASEVariesAcrossConnections(t *testing.T) {
	seen := map[uint16]bool{}
	for range 8 {
		raw := captureClientHello(t, dialTLSProfile(newDialer().DialContext, nil, ProfileTraeWork))
		b := raw[9:]
		i := 35 + int(b[34])
		i += 2 + int(binary.BigEndian.Uint16(b[i:]))
		i += 1 + int(b[i]) + 2
		for i < len(b) {
			kind, size := binary.BigEndian.Uint16(b[i:]), int(binary.BigEndian.Uint16(b[i+2:]))
			if kind == 13 {
				value := binary.BigEndian.Uint16(b[i+6:])
				if !isGREASEValue(value) {
					t.Fatalf("Chrome 签名算法缺少 GREASE：%04x", value)
				}
				seen[value] = true
				break
			}
			i += 4 + size
		}
	}
	if len(seen) < 2 {
		t.Fatal("八次连接的签名算法 GREASE 固定，疑似复用了样本值")
	}
}

func clientHelloEphemeral(record []byte) map[string]string {
	b := record[9:]
	fields := map[string]string{"random": hex.EncodeToString(b[2:34])}
	i := 35 + int(b[34])
	i += 2 + int(binary.BigEndian.Uint16(b[i:]))
	i += 1 + int(b[i]) + 2
	for i < len(b) {
		kind, size := binary.BigEndian.Uint16(b[i:]), int(binary.BigEndian.Uint16(b[i+2:]))
		body := b[i+4 : i+4+size]
		if kind == 51 || kind == 65037 {
			fields[fmt.Sprint(kind)] = hex.EncodeToString(body)
		}
		i += 4 + size
	}
	return fields
}

// clientHelloFields 比较 JA3/JA4 未覆盖的扩展内容；连接随机数和实际密钥不作固定值比较。
func clientHelloFields(record []byte) map[string]string {
	b := record[9:]
	fields := map[string]string{"record_version": hex.EncodeToString(record[1:3]), "version": hex.EncodeToString(b[:2]), "session_id_length": fmt.Sprint(b[34])}
	i := 35 + int(b[34])
	n := int(binary.BigEndian.Uint16(b[i : i+2]))
	i += 2
	ciphers := append([]byte(nil), b[i:i+n]...)
	for j := 0; j < len(ciphers); j += 2 {
		if isGREASEValue(binary.BigEndian.Uint16(ciphers[j:])) {
			ciphers[j], ciphers[j+1] = 0x0a, 0x0a
		}
	}
	fields["ciphers"] = hex.EncodeToString(ciphers)
	i += n
	n = int(b[i])
	fields["compression"] = hex.EncodeToString(b[i+1 : i+1+n])
	i += 1 + n + 2
	grease := 0
	for i < len(b) {
		kind, size := binary.BigEndian.Uint16(b[i:]), int(binary.BigEndian.Uint16(b[i+2:]))
		body := append([]byte(nil), b[i+4:i+4+size]...)
		i += 4 + size
		key := fmt.Sprintf("extension_%d", kind)
		switch {
		case isGREASEValue(kind):
			key = fmt.Sprintf("grease_%d", grease)
			grease++
		case kind == 0 || kind == 21:
			// SNI 随请求变化；padding 随 SNI 长度调整。
			body = nil
		case kind == 51:
			var shares []string
			for j := 2; j < len(body); {
				group, length := binary.BigEndian.Uint16(body[j:]), int(binary.BigEndian.Uint16(body[j+2:]))
				if isGREASEValue(group) {
					group = 0x0a0a
				}
				shares = append(shares, fmt.Sprintf("%d:%d", group, length))
				j += 4 + length
			}
			fields[key] = strings.Join(shares, ",")
			continue
		case kind == 65037:
			// GREASE ECH 的密文每连接随机，长度由客户端随机选择。
			fields[key] = "grease-ech"
			continue
		case kind == 10 || kind == 13 || kind == 43:
			start := 2
			if kind == 43 {
				start = 1
			}
			for j := start; j+1 < len(body); j += 2 {
				if isGREASEValue(binary.BigEndian.Uint16(body[j:])) {
					body[j], body[j+1] = 0x0a, 0x0a
				}
			}
		}
		fields[key] = hex.EncodeToString(body)
	}
	return fields
}

func TestClientProfilePoolsStaySeparate(t *testing.T) {
	routes := newRouteTable(newTransport(nil))
	defer (routedTransport{routes}).CloseIdleConnections()
	seen := map[http.RoundTripper]bool{}
	for _, proxy := range []string{"", "http://fixture.invalid:8080"} {
		for profile := ProfileWorkBuddy; profile <= ProfileTraeAuth; profile++ {
			tr, err := routes.transportForProfile(proxy, "", profile)
			if err != nil {
				t.Fatal(err)
			}
			again, err := routes.transportForProfile(proxy, "", profile)
			if err != nil || again != tr || seen[tr] {
				t.Fatal("平台身份或代理连接池发生混用")
			}
			seen[tr] = true
		}
	}
}

func TestProfileHTTP2TimeoutAndActiveStream(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(map[bool]string{false: "流不受响应头超时或清池影响", true: "响应头超时可识别"}[stalled], func(t *testing.T) {
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stalled {
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, "first\n")
				w.(http.Flusher).Flush()
				select {
				case <-time.After(100 * time.Millisecond):
					_, _ = io.WriteString(w, "last\n")
				case <-r.Context().Done():
				}
			}))
			srv.EnableHTTP2 = true
			srv.StartTLS()
			defer srv.Close()
			base := newTransport(nil)
			base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			base.ResponseHeaderTimeout = 40 * time.Millisecond
			routes := newRouteTable(base)
			rt := routedTransport{routes}
			defer rt.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(WithClientProfile(ctx, ProfileTraeWork), "GET", srv.URL, nil)
			resp, err := rt.RoundTrip(req)
			if stalled {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("响应头超时没有保留原因：%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			rt.CloseIdleConnections()
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || string(body) != "first\nlast\n" {
				t.Fatalf("流被提前终止：%q %v", body, err)
			}
		})
	}
}

func TestProfileHTTP2WireSettings(t *testing.T) {
	certServer := httptest.NewTLSServer(nil)
	certs := certServer.TLS.Certificates
	certServer.Close()
	for _, profile := range []ClientProfile{ProfileTraeCode, ProfileTraeWork, ProfileTraeAuth} {
		t.Run(httpProfileName(profile, false), func(t *testing.T) {
			ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: certs, NextProtos: []string{"h2"}})
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			done := make(chan error, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				preface := make([]byte, len(http2.ClientPreface))
				if _, err = io.ReadFull(conn, preface); err != nil {
					done <- err
					return
				}
				if string(preface) != http2.ClientPreface {
					done <- errors.New("未发送 h2 preface")
					return
				}
				fr := http2.NewFramer(conn, conn)
				if err = fr.WriteSettings(); err != nil {
					done <- err
					return
				}
				var settings []http2.Setting
				var flow uint32
				for {
					frame, err := fr.ReadFrame()
					if err != nil {
						done <- err
						return
					}
					switch f := frame.(type) {
					case *http2.SettingsFrame:
						if !f.IsAck() {
							err = f.ForeachSetting(func(s http2.Setting) error { settings = append(settings, s); return nil })
							if err != nil {
								done <- err
								return
							}
							_ = fr.WriteSettingsAck()
						}
					case *http2.WindowUpdateFrame:
						if f.StreamID == 0 {
							flow = f.Increment
						}
					case *http2.HeadersFrame:
						want := []http2.Setting{{ID: 1, Val: 65536}, {ID: 2, Val: 0}, {ID: 4, Val: 6291456}, {ID: 6, Val: 262144}}
						if profile == ProfileTraeCode {
							want = []http2.Setting{{ID: 1, Val: 65536}, {ID: 2, Val: 0}, {ID: 3, Val: 1000}, {ID: 4, Val: 6291456}, {ID: 6, Val: 262144}}
						}
						if !reflect.DeepEqual(settings, want) || flow != 15663105 {
							done <- errors.New("线上帧的 SETTINGS 顺序或窗口偏离官方 Chromium 样本")
							return
						}
						weight := uint8(109)
						if profile != ProfileTraeAuth {
							weight = 219
						}
						if !f.HasPriority() || f.Priority.Weight != weight || !f.Priority.Exclusive || f.Priority.StreamDep != 0 {
							done <- errors.New("HEADERS 优先级偏离官方样本")
							return
						}
						err = fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndHeaders: true, EndStream: true, BlockFragment: []byte{0x88}})
						done <- err
						return
					}
				}
			}()
			base := newTransport(nil)
			base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			routes := newRouteTable(base)
			rt := routedTransport{routes}
			defer rt.CloseIdleConnections()
			addr := ln.Addr().(*net.TCPAddr)
			req, _ := http.NewRequestWithContext(WithClientProfile(context.Background(), profile), "GET", "https://"+addr.String(), nil)
			resp, err := rt.RoundTrip(req)
			if frameErr := <-done; frameErr != nil {
				t.Fatal(frameErr)
			}
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
		})
	}
}

func TestClientProfileProtocolAndProxy(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileWorkBuddy, ProfileQoder, ProfileTraeCode, ProfileTraeWork, ProfileTraeAuth} {
		for _, proxy := range []bool{false, true} {
			t.Run(httpProfileName(profile, proxy), func(t *testing.T) {
				srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Proto) }))
				srv.EnableHTTP2 = true
				srv.StartTLS()
				defer srv.Close()
				base := newTransport(nil)
				base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				base.DialTLSContext = dialTLSMimic(base.DialContext, base.TLSClientConfig)
				routes := newRouteTable(base)
				rt := routedTransport{routes}
				defer rt.CloseIdleConnections()
				endpoint := srv.URL
				ctx := WithClientProfile(context.Background(), profile)
				var px *fakeConnectProxy
				if proxy {
					px = &fakeConnectProxy{backend: srv.Listener.Addr().String()}
					routes.router = &fakeRouter{proxies: map[string]string{"test": "http://" + px.start(t, nil)}}
					ctx = WithProxy(ctx, "test")
					endpoint = "https://upstream.test/"
				}
				req, _ := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
				resp, err := rt.RoundTrip(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				want := "HTTP/1.1"
				if profile == ProfileTraeCode || profile == ProfileTraeWork || profile == ProfileTraeAuth {
					want = "HTTP/2.0"
				}
				if string(body) != want {
					t.Fatalf("协议 %s，期望 %s", body, want)
				}
				if px != nil && len(px.seen()) != 1 {
					t.Fatalf("代理隧道数 = %d", len(px.seen()))
				}
			})
		}
	}
}

func httpProfileName(profile ClientProfile, proxy bool) string {
	names := []string{"workbuddy", "qoder", "trae-code", "trae-work", "trae-auth"}
	suffix := "/direct"
	if proxy {
		suffix = "/proxy"
	}
	return names[profile] + suffix
}

func TestClientProfileHTTP1FallbackAndTLSOff(t *testing.T) {
	defer SetTLSMimic(true)
	for _, mimic := range []bool{true, false} {
		SetTLSMimic(mimic)
		for _, profile := range []ClientProfile{ProfileTraeCode, ProfileTraeWork, ProfileTraeAuth} {
			t.Run(fmt.Sprintf("%s/mimic=%t", httpProfileName(profile, false), mimic), func(t *testing.T) {
				var requests atomic.Int32
				srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					body, _ := io.ReadAll(r.Body)
					_, _ = w.Write(body)
				}))
				defer srv.Close()
				base := newTransport(nil)
				base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				rt := routedTransport{newRouteTable(base)}
				defer rt.CloseIdleConnections()
				req, _ := http.NewRequestWithContext(WithClientProfile(context.Background(), profile), "POST", srv.URL, strings.NewReader("fixture"))
				resp, err := rt.RoundTrip(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err != nil || string(body) != "fixture" || resp.ProtoMajor != 1 || requests.Load() != 1 {
					t.Fatalf("HTTP/1 回退丢失或重放请求：%q %v，次数 %d", body, err, requests.Load())
				}
			})
		}
	}
}

func TestClientProfilePinnedAndBlocked(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileQoder, ProfileTraeCode, ProfileTraeWork, ProfileTraeAuth} {
		t.Run(httpProfileName(profile, false), func(t *testing.T) {
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, r.TLS.ServerName+"/"+r.Host)
			}))
			srv.EnableHTTP2 = true
			srv.StartTLS()
			defer srv.Close()
			base := newTransport(nil)
			base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			routes := newRouteTable(base)
			defer (routedTransport{routes}).CloseIdleConnections()
			tr, err := routes.transportForProfile("", "127.0.0.1", profile)
			if err != nil {
				t.Fatal(err)
			}
			_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
			host := net.JoinHostPort("upstream.test", port)
			ctx := WithClientProfile(context.Background(), profile)
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+host+"/", nil)
			resp, err := tr.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || string(body) != "upstream.test/"+host {
				t.Fatalf("钉 IP 改变了 SNI 或 Host：%q %v", body, err)
			}
			routes.router = &fakeRouter{}
			var dials atomic.Int32
			base.DialContext = func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("不应拨号")
			}
			req = req.WithContext(WithProxy(ctx, "missing"))
			_, err = (routedTransport{routes}).RoundTrip(req)
			var blocked *RouteError
			if !errors.As(err, &blocked) || !blocked.Blocked || dials.Load() != 0 {
				t.Fatalf("被阻断的线路仍然尝试出站：%v，拨号 %d 次", err, dials.Load())
			}
		})
	}
}
