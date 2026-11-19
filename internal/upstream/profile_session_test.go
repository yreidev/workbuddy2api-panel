// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProfileSessionEarlyFailureRetriesBeforeHTTP(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
			for _, alert := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/TLS%x/alert=%t", httpProfileName(profile, false), version, alert), func(t *testing.T) {
					cert := httptest.NewTLSServer(nil)
					config := &tls.Config{Certificates: cert.TLS.Certificates, MinVersion: version, MaxVersion: version, NextProtos: []string{"http/1.1"}}
					config.SetSessionTicketKeys([][32]byte{{1, 2, 3}})
					cert.Close()
					ln, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					defer ln.Close()
					done := make(chan error, 1)
					go func() {
						for attempt := range 5 {
							raw, err := ln.Accept()
							if err != nil {
								done <- err
								return
							}
							_ = raw.SetDeadline(time.Now().Add(4 * time.Second))
							if attempt == 1 {
								var header [5]byte
								_, err = io.ReadFull(raw, header[:])
								if err == nil {
									_, err = io.CopyN(io.Discard, raw, int64(header[3])<<8|int64(header[4]))
								}
								if err == nil && alert {
									_, err = raw.Write([]byte{21, 3, 3, 0, 2, 2, 40})
								}
								_ = raw.Close()
								if err != nil {
									done <- err
									return
								}
								continue
							}
							conn := tls.Server(raw, config)
							r, err := http.ReadRequest(bufio.NewReader(conn))
							if err == nil {
								_, err = io.Copy(io.Discard, r.Body)
								_ = r.Body.Close()
							}
							if err == nil && attempt == 2 && version == tls.VersionTLS12 && conn.ConnectionState().DidResume != (profile != ProfileTraeCode) {
								err = fmt.Errorf("恢复失败后的缓存策略不符: resumed=%t", conn.ConnectionState().DidResume)
							}
							if err == nil {
								_, err = fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
							}
							_ = conn.Close()
							if err != nil {
								done <- err
								return
							}
						}
						done <- nil
					}()
					base := newTransport(nil)
					base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
					tr := newProfileHTTP2(base, profile)
					defer tr.CloseIdleConnections()
					client := &http.Client{Transport: tr, Timeout: 4 * time.Second}
					for range 4 {
						r, err := client.Post("https://"+ln.Addr().String()+"/fixture", "application/json", strings.NewReader("{}"))
						if err != nil {
							t.Fatal(err)
						}
						_, err = io.Copy(io.Discard, r.Body)
						_ = r.Body.Close()
						if err != nil {
							t.Fatal(err)
						}
					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

type profileHelloRecorder struct {
	net.Conn
	once  sync.Once
	hello chan<- []byte
}

func (c *profileHelloRecorder) Write(b []byte) (int, error) {
	c.once.Do(func() { c.hello <- append([]byte(nil), b...) })
	return c.Conn.Write(b)
}

func TestProfileSessionTicketRotationAndDisabled(t *testing.T) {
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		for _, profile := range []ClientProfile{ProfileTraeCode, ProfileTraeWork, ProfileTraeAuth} {
			for _, disabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/TLS%x/disabled=%t", httpProfileName(profile, false), version, disabled), func(t *testing.T) {
					var requests atomic.Int32
					srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						_, _ = fmt.Fprint(w, r.TLS.DidResume)
					}))
					srv.EnableHTTP2 = true
					srv.TLS = &tls.Config{MinVersion: version, MaxVersion: version, SessionTicketsDisabled: disabled}
					srv.StartTLS()
					defer srv.Close()
					hellos := make(chan []byte, 4)
					base := newTransport(nil)
					base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
					base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
						conn, err := newDialer().DialContext(ctx, network, address)
						if err != nil {
							return nil, err
						}
						return &profileHelloRecorder{Conn: conn, hello: hellos}, nil
					}
					tr := newProfileHTTP2(base, profile)
					defer tr.CloseIdleConnections()
					for i := range 4 {
						if i == 2 {
							// 丢弃旧密钥：恢复被拒后仍须只发送一次业务请求，并保存新票据。
							srv.TLS.SetSessionTicketKeys([][32]byte{{1, 2, 3}})
						}
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						r, err := http.NewRequestWithContext(ctx, "POST", srv.URL, http.NoBody)
						if err != nil {
							cancel()
							t.Fatal(err)
						}
						resp, err := tr.RoundTrip(r)
						if err != nil {
							cancel()
							t.Fatal(err)
						}
						body, err := io.ReadAll(resp.Body)
						_ = resp.Body.Close()
						cancel()
						want := fmt.Sprint(!disabled && i%2 == 1)
						if err != nil || string(body) != want {
							t.Fatalf("第 %d 次握手 resume=%s，期望 %s：%v", i+1, body, want, err)
						}
						var hello []byte
						select {
						case hello = <-hellos:
						case <-time.After(2 * time.Second):
							t.Fatalf("第 %d 次请求未建立新的 TLS 连接", i+1)
						}
						if i == 1 && !disabled {
							name := "trae-code"
							switch profile {
							case ProfileTraeAuth:
								name = "trae-auth"
							case ProfileTraeWork:
								name = "trae-work"
							}
							minor := 12
							if version == tls.VersionTLS13 {
								minor = 13
							}
							sample, err := os.ReadFile(fmt.Sprintf("clienthello/%s-tls%d-resume.bin", name, minor))
							if err != nil {
								t.Fatal(err)
							}
							got, expected := clientHelloFields(hello), clientHelloFields(sample)
							for _, fields := range []map[string]string{got, expected} {
								// padding 是否出现还取决于 SNI 和服务端票据的长度。
								delete(fields, "extension_21")
								// 票据密文、年龄和 binder 由服务端及本次握手决定。
								for _, key := range []string{"extension_35", "extension_41"} {
									if value, ok := fields[key]; ok && value != "" {
										fields[key] = "present"
									}
								}
							}
							if !reflect.DeepEqual(got, expected) {
								for k, v := range expected {
									if got[k] != v {
										t.Errorf("恢复 ClientHello %s=%s，官方样本 %s", k, got[k], v)
									}
								}
								for k := range got {
									if _, ok := expected[k]; !ok {
										t.Errorf("恢复 ClientHello 多出字段 %s", k)
									}
								}
							}
						}
						tr.CloseIdleConnections()
					}
					if n := requests.Load(); n != 4 {
						t.Fatalf("四次连接发送了 %d 次业务请求", n)
					}
				})
			}
		}
	}
}

func TestProfileSessionTicketLifetimes(t *testing.T) {
	const (
		day      = 24 * time.Hour
		full     = "完整握手"
		resumed  = "恢复成功"
		rejected = "提交票据后重新认证"
	)
	type step struct {
		offset time.Duration
		result string
	}
	for _, tc := range []struct {
		name    string
		version uint16
		steps   []step
	}{
		{"tls12", tls.VersionTLS12, []step{
			{0, full}, {time.Hour - time.Second, resumed}, {time.Hour, full}, {time.Hour + time.Second, resumed},
		}},
		{"tls13", tls.VersionTLS13, []step{
			{0, full}, {2*day - time.Second, resumed}, {4*day - time.Second, full}, {4 * day, resumed},
		}},
		{"auth", tls.VersionTLS13, []step{
			{0, full}, {day, resumed}, {2 * day, resumed}, {3 * day, resumed}, {4 * day, resumed}, {5 * day, resumed}, {6 * day, resumed},
			{7*day - time.Second, resumed}, {7 * day, full},
			{8 * day, rejected}, {9 * day, resumed}, {10 * day, resumed}, {11 * day, resumed}, {12 * day, resumed}, {13 * day, resumed},
			{14 * day, resumed}, {15*day - time.Second, resumed}, {15 * day, full}, {23 * day, full}, {23*day + time.Second, resumed},
		}},
	} {
		for _, profile := range []ClientProfile{ProfileTraeCode, ProfileTraeWork, ProfileTraeAuth} {
			t.Run(httpProfileName(profile, false)+"/"+tc.name, func(t *testing.T) {
				srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = fmt.Fprint(w, r.TLS.DidResume)
				}))
				srv.EnableHTTP2 = true
				srv.TLS = &tls.Config{MinVersion: tc.version, MaxVersion: tc.version}
				srv.StartTLS()
				defer srv.Close()
				var clock atomic.Int64
				start := time.Now().Truncate(time.Second)
				roots := x509.NewCertPool()
				roots.AddCert(srv.Certificate())
				base := newTransport(nil)
				base.TLSClientConfig = &tls.Config{RootCAs: roots, Time: func() time.Time { return time.Unix(0, clock.Load()) }}
				hellos := make(chan []byte, 4)
				base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
					conn, err := newDialer().DialContext(ctx, network, address)
					if err != nil {
						return nil, err
					}
					return &profileHelloRecorder{Conn: conn, hello: hellos}, nil
				}
				tr := newProfileHTTP2(base, profile)
				defer tr.CloseIdleConnections()
				for _, step := range tc.steps {
					clock.Store(start.Add(step.offset).UnixNano())
					if step.result == rejected {
						srv.TLS.SetSessionTicketKeys([][32]byte{{4, 5, 6}})
					}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					r, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
					resp, err := tr.RoundTrip(r)
					if err != nil {
						cancel()
						t.Fatal(err)
					}
					body, err := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					cancel()
					select {
					case hello := <-hellos:
						fields := clientHelloFields(hello)
						_, offer := fields["extension_41"]
						if tc.version == tls.VersionTLS12 {
							offer = fields["extension_35"] != ""
						}
						if err != nil || string(body) != fmt.Sprint(step.result == resumed) || offer != (step.result != full) {
							t.Fatalf("经过 %v：携带票据=%t 恢复=%s，预期%s，读取错误 %v", step.offset, offer, body, step.result, err)
						}
						t.Logf("elapsed=%d offer=%t resume=%s", step.offset/time.Second, offer, body)
					case <-time.After(2 * time.Second):
						t.Fatalf("首次认证后 %v 未捕获新的 ClientHello", step.offset)
					}
					tr.CloseIdleConnections()
				}
			})
		}
	}
}

func TestProfileSessionCachePortIsolation(t *testing.T) {
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		for _, profile := range []ClientProfile{ProfileTraeCode, ProfileTraeWork, ProfileTraeAuth} {
			t.Run(fmt.Sprintf("%s/TLS%x", httpProfileName(profile, false), version), func(t *testing.T) {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Connection", "close")
					_, _ = fmt.Fprint(w, r.TLS.DidResume)
				})
				first := httptest.NewUnstartedServer(handler)
				first.TLS = &tls.Config{MinVersion: version, MaxVersion: version}
				first.StartTLS()
				defer first.Close()
				second := httptest.NewUnstartedServer(handler)
				second.TLS = &tls.Config{Certificates: first.TLS.Certificates, MinVersion: version, MaxVersion: version}
				second.StartTLS()
				defer second.Close()
				// 共享服务端密钥，使错误地跨端口携带的票据也能被接受。
				keys := [][32]byte{{1, 2, 3}}
				first.TLS.SetSessionTicketKeys(keys)
				second.TLS.SetSessionTicketKeys(keys)
				base := newTransport(nil)
				base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				tr := newProfileHTTP2(base, profile)
				defer tr.CloseIdleConnections()
				for i, url := range []string{first.URL, first.URL, second.URL, first.URL, second.URL} {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					r, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
					response, err := tr.RoundTrip(r)
					if err != nil {
						cancel()
						t.Fatal(err)
					}
					body, err := io.ReadAll(response.Body)
					_ = response.Body.Close()
					cancel()
					if want := fmt.Sprint(i != 0 && i != 2); err != nil || string(body) != want {
						t.Fatalf("第 %d 次连接恢复=%s，期望 %s：%v", i+1, body, want, err)
					}
				}
			})
		}
	}
}

func TestProfileSessionFailureRetryBoundaries(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		for _, tc := range []struct {
			name   string
			seed   bool
			alert  byte
			faults int
			cancel bool
		}{
			{name: "首次握手EOF", faults: 1},
			{name: "恢复时其他TLS告警", seed: true, alert: 42, faults: 1},
			{name: "恢复时连续EOF只重连一次", seed: true, faults: 2},
			{name: "取消后不重连", seed: true, faults: 1, cancel: true},
		} {
			t.Run(httpProfileName(profile, false)+"/"+tc.name, func(t *testing.T) {
				cert := httptest.NewTLSServer(nil)
				config := &tls.Config{Certificates: cert.TLS.Certificates, NextProtos: []string{"http/1.1"}}
				cert.Close()
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer ln.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					done <- func() error {
						if tc.seed {
							raw, err := ln.Accept()
							if err != nil {
								return err
							}
							_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
							conn := tls.Server(raw, config)
							request, err := http.ReadRequest(bufio.NewReader(conn))
							if err == nil {
								_, err = io.Copy(io.Discard, request.Body)
								_ = request.Body.Close()
							}
							if err == nil {
								_, err = fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
							}
							_ = conn.Close()
							if err != nil {
								return err
							}
						}
						for range tc.faults {
							raw, err := ln.Accept()
							if err != nil {
								return err
							}
							_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
							var header [5]byte
							_, err = io.ReadFull(raw, header[:])
							if err == nil {
								_, err = io.CopyN(io.Discard, raw, int64(header[3])<<8|int64(header[4]))
							}
							if tc.cancel {
								cancel()
							}
							if err == nil && tc.alert != 0 {
								_, err = raw.Write([]byte{21, 3, 3, 0, 2, 2, tc.alert})
							}
							_ = raw.Close()
							if err != nil {
								return err
							}
						}
						return nil
					}()
				}()
				base := newTransport(nil)
				base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				dial := base.DialContext
				var dials atomic.Int32
				base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
					if n := dials.Add(1); tc.cancel && n > 2 {
						return nil, errors.New("取消后仍尝试拨号")
					}
					return dial(ctx, network, address)
				}
				tr := newProfileHTTP2(base, profile)
				defer tr.CloseIdleConnections()
				client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
				url := "https://" + ln.Addr().String() + "/fixture"
				if tc.seed {
					response, err := client.Post(url, "text/plain", strings.NewReader("seed"))
					if err != nil {
						t.Fatal(err)
					}
					_, err = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				request, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("must-not-be-sent"))
				handshakeDone := make(chan struct{})
				if tc.cancel {
					request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
						TLSHandshakeDone: func(tls.ConnectionState, error) { close(handshakeDone) },
					}))
				}
				response, err := client.Do(request)
				if response != nil {
					_ = response.Body.Close()
				}
				if err == nil {
					t.Fatal("故障握手没有返回错误")
				}
				if tc.cancel && !errors.Is(err, context.Canceled) {
					t.Fatalf("取消应返回 context.Canceled，实际为 %v", err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if tc.cancel {
					select {
					case <-handshakeDone:
					case <-time.After(3 * time.Second):
						t.Fatal("取消后 TLS 握手未结束")
					}
				}
				want := tc.faults
				if tc.seed {
					want++
				}
				if dials.Load() != int32(want) {
					t.Fatalf("重连次数越界：拨号=%d，期望=%d", dials.Load(), want)
				}
			})
		}
	}
}
