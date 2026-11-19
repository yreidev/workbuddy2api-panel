// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

//go:build unix

package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

type profileBIOObservedTLS struct {
	*profileTLSConn
	pending     chan struct{}
	active      *atomic.Bool
	updated     chan struct{}
	target      *atomic.Uint32
	frames      []byte
	sawUpdate   bool
	updatedOnce sync.Once
}

// 控制队列测试在一次真实的阻塞通知处暂停写协程，等请求正文关闭后再恢复。
type profileDrainTestTLS struct {
	*profileTLSConn
	onPending func()
}

func (c *profileDrainTestTLS) SetWritePendingCallback(callback func(bool)) {
	c.profileTLSConn.SetWritePendingCallback(func(pending bool) {
		callback(pending)
		if pending {
			c.onPending()
		}
	})
}

type profileDrainTestBody struct {
	io.ReadCloser
	closed chan struct{}
	once   sync.Once
}

func (b *profileDrainTestBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { close(b.closed) })
	return err
}

func TestProfileHTTP2SocketControlLimitDrain(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		for _, cancelRequest := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel=%t", httpProfileName(profile, false), cancelRequest), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
				defer cancel()
				ln := profileDynamicListener(t)
				pending, bodyClosed := make(chan struct{}, 1), make(chan struct{})
				callReturned := make(chan struct{})
				var holdWrite atomic.Bool
				serverDone := make(chan error, 1)
				go func() {
					serverDone <- func() error {
						conn, err := ln.Accept()
						if err != nil {
							return err
						}
						defer conn.Close()
						_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
						tcp := conn.(*tls.Conn).NetConn().(*net.TCPConn)
						if err := tcp.SetReadBuffer(32768); err != nil {
							return err
						}
						if _, err := io.CopyN(io.Discard, conn, int64(len(http2.ClientPreface))); err != nil {
							return err
						}
						fr := http2.NewFramer(conn, conn)
						if err := fr.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 32768}); err != nil {
							return err
						}
						if err := fr.WriteWindowUpdate(0, 16*1024*1024); err != nil {
							return err
						}
						acks, received := 0, 0
						for {
							f, err := fr.ReadFrame()
							if err != nil {
								if errors.Is(err, io.EOF) && acks == 10001 {
									return nil
								}
								return fmt.Errorf("超限关闭前 ACK=%d want=10001：%w", acks, err)
							}
							switch f := f.(type) {
							case *http2.HeadersFrame:
								if f.StreamID == 1 && f.StreamEnded() {
									if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndHeaders: true, EndStream: true, BlockFragment: []byte{0x88}}); err != nil {
										return err
									}
								}
							case *http2.SettingsFrame:
								if !f.IsAck() {
									if err := fr.WriteSettingsAck(); err != nil {
										return err
									}
								}
							case *http2.DataFrame:
								if acks > 0 || f.StreamEnded() || !bytes.Equal(f.Data(), bytes.Repeat([]byte{'a'}, len(f.Data()))) {
									return fmt.Errorf("超限后仍发送未开始的正文，或在途 DATA 损坏：%v", f)
								}
								received += len(f.Data())
								if received == 32768 {
									holdWrite.Store(true)
									if err := fr.WriteWindowUpdate(f.StreamID, 16*1024*1024); err != nil {
										return err
									}
									select {
									case <-pending:
									case <-ctx.Done():
										return fmt.Errorf("未进入 TLS pending：%w", ctx.Err())
									}
									var pressure bytes.Buffer
									out := http2.NewFramer(&pressure, nil)
									for i := range 10002 {
										var data [8]byte
										binary.BigEndian.PutUint64(data[:], uint64(i))
										_ = out.WritePing(false, data)
									}
									if _, err := conn.Write(pressure.Bytes()); err != nil {
										return err
									}
									select {
									case <-bodyClosed:
									case <-ctx.Done():
										return fmt.Errorf("超限后未终止请求正文：%w", ctx.Err())
									}
									if cancelRequest {
										cancel()
										select {
										case <-callReturned:
										case <-time.After(2 * time.Second):
											return fmt.Errorf("取消请求仍等待 socket 排空")
										}
									}
									if err := tcp.SetReadBuffer(262144); err != nil {
										return err
									}
								}
							case *http2.PingFrame:
								if !f.IsAck() || binary.BigEndian.Uint64(f.Data[:]) != uint64(acks) {
									return fmt.Errorf("排空 ACK 数量或顺序错误：index=%d frame=%v", acks, f)
								}
								acks++
							case *http2.RSTStreamFrame, *http2.GoAwayFrame:
								return fmt.Errorf("超限排空多发了控制帧：%v", f)
							}
						}
					}()
				}()
				base := newTransport(nil)
				base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				tr := newProfileHTTP2(base, profile)
				defer tr.CloseIdleConnections()
				handshake := tr.TLSHandshakeContext
				tr.TLSHandshakeContext = func(handshakeCtx context.Context, host string, raw net.Conn) (net.Conn, *tls.ConnectionState, error) {
					conn, state, err := handshake(handshakeCtx, host, raw)
					if err != nil {
						return nil, nil, err
					}
					return &profileDrainTestTLS{profileTLSConn: conn.(*profileTLSConn), onPending: func() {
						if !holdWrite.CompareAndSwap(true, false) {
							return
						}
						pending <- struct{}{}
						select {
						case <-bodyClosed:
						case <-ctx.Done():
						}
					}}, state, nil
				}
				client := &http.Client{Transport: tr}
				warm, err := http.NewRequestWithContext(ctx, "GET", "https://"+ln.Addr().String()+"/probe", nil)
				if err != nil {
					t.Fatal(err)
				}
				warmResp, err := client.Do(warm)
				if err != nil {
					t.Fatal(err)
				}
				warmResp.Body.Close()
				r, err := NewBufferedRequest(ctx, "POST", "https://"+ln.Addr().String()+"/pressure", bytes.Repeat([]byte{'a'}, 8*1024*1024))
				if err != nil {
					t.Fatal(err)
				}
				r.Body = &profileDrainTestBody{ReadCloser: r.Body, closed: bodyClosed}
				r.Close = true
				resp, callErr := client.Do(r)
				close(callReturned)
				if resp != nil {
					resp.Body.Close()
				}
				// 超限先发生，取消可能打断 waitDone，也可能先被 RoundTrip 选中。
				limitError := callErr != nil && strings.Contains(callErr.Error(), "Chromium 控制帧队列超过上限")
				if !limitError && (!cancelRequest || !errors.Is(callErr, context.Canceled)) {
					t.Errorf("超限错误未传回原始原因：%v", callErr)
				}
				if err := <-serverDone; err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func (c *profileBIOObservedTLS) SetWritePendingCallback(callback func(bool)) {
	c.profileTLSConn.SetWritePendingCallback(func(pending bool) {
		callback(pending)
		if c.active != nil {
			c.active.Store(pending)
		}
		if pending {
			select {
			case c.pending <- struct{}{}:
			default:
			}
		}
	})
}

func (c *profileBIOObservedTLS) Read(p []byte) (int, error) {
	// 再次读 socket 表示先前完整的补窗帧已交给 HTTP/2 处理；
	// 不靠 sleep 猜测客户端是否收到了服务端的补窗。
	if c.sawUpdate {
		c.updatedOnce.Do(func() { close(c.updated) })
	}
	n, err := c.profileTLSConn.Read(p)
	c.frames = append(c.frames, p[:n]...)
	for len(c.frames) >= 9 {
		size := int(c.frames[0])<<16 | int(c.frames[1])<<8 | int(c.frames[2])
		if len(c.frames) < size+9 {
			break
		}
		if c.frames[3] == byte(http2.FrameWindowUpdate) && binary.BigEndian.Uint32(c.frames[5:9]) == c.target.Load() && c.target.Load() != 0 {
			c.sawUpdate = true
		}
		c.frames = c.frames[size+9:]
	}
	return n, err
}

func TestProfileHTTP2SocketBackpressureWindowUpdate(t *testing.T) {
	testProfileHTTP2SocketBackpressureWindowUpdate(t, "", []string{"window", "ping", "settings"})
}

func TestProfileHTTP2SocketBackpressureThroughProxy(t *testing.T) {
	for _, scheme := range []string{"http", "socks5", "socks5h", "https"} {
		t.Run(scheme, func(t *testing.T) {
			testProfileHTTP2SocketBackpressureWindowUpdate(t, scheme, []string{"ping"})
		})
	}
}

func testProfileHTTP2SocketBackpressureWindowUpdate(t *testing.T, proxyScheme string, controls []string) {
	t.Helper()
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		for _, control := range controls {
			t.Run(httpProfileName(profile, false)+"/"+control, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
				defer cancel()
				ln := profileDynamicListener(t)
				pending := make(chan struct{}, 1)
				updated := make(chan struct{})
				resumeProxy := make(chan struct{})
				proxyPaused := make(chan struct{})
				var writePending atomic.Bool
				resume := sync.OnceFunc(func() { close(resumeProxy) })
				defer resume()
				var target atomic.Uint32
				serverDone := make(chan error, 1)
				go func() {
					serverDone <- func() error {
						conn, err := ln.Accept()
						if err != nil {
							return err
						}
						defer conn.Close()
						_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
						tcp := conn.(*tls.Conn).NetConn().(*net.TCPConn)
						if err := tcp.SetReadBuffer(32768); err != nil {
							return err
						}
						if _, err := io.CopyN(io.Discard, conn, int64(len(http2.ClientPreface))); err != nil {
							return err
						}
						fr := http2.NewFramer(conn, conn)
						fr.ReadMetaHeaders = hpack.NewDecoder(65536, nil)
						if err := fr.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 32768}); err != nil {
							return err
						}
						if err := fr.WriteWindowUpdate(0, 16*1024*1024); err != nil {
							return err
						}
						ids := map[uint32]string{}
						streams := map[string]uint32{}
						counts := map[string]int{}
						paused, bResumed := false, false
						controlSent, controlAcked := false, false
						finished := 0
						for finished < 2 {
							f, err := fr.ReadFrame()
							if err != nil {
								return err
							}
							switch f := f.(type) {
							case *http2.SettingsFrame:
								if f.IsAck() {
									if controlSent && control == "settings" {
										controlAcked = true
									}
								} else {
									if err := fr.WriteSettingsAck(); err != nil {
										return err
									}
								}
							case *http2.PingFrame:
								if !f.IsAck() || f.Data != [8]byte{'f', 'i', 'x', 't', 'u', 'r', 'e', '!'} {
									return fmt.Errorf("PING ACK 不符合原输入：%v", f)
								}
								controlAcked = true
							case *http2.MetaHeadersFrame:
								id := f.PseudoValue("path")
								ids[f.StreamID] = id
								streams[id] = f.StreamID
							case *http2.DataFrame:
								id := ids[f.StreamID]
								if id != "/probe" && id != "/a" && id != "/b" {
									return fmt.Errorf("未知正文路径 %q", id)
								}
								if id != "/probe" {
									for _, b := range f.Data() {
										if b != id[1] {
											return fmt.Errorf("%s 正文损坏", id)
										}
									}
								}
								counts[id] += len(f.Data())
								if paused && id == "/b" && len(f.Data()) > 0 {
									if control != "window" && !controlAcked {
										return fmt.Errorf("补窗流在控制 ACK 前发送")
									}
									bResumed = true
								}
								if !paused && counts["/a"] == 32768 && counts["/b"] == 32768 {
									if err := fr.WriteWindowUpdate(streams["/a"], 16*1024*1024); err != nil {
										return err
									}
									if proxyScheme != "" {
										select {
										case <-proxyPaused:
										case <-ctx.Done():
											return fmt.Errorf("代理未暂停读取：%w", ctx.Err())
										}
									}
									for !writePending.Load() {
										select {
										case <-pending:
										case <-ctx.Done():
											return fmt.Errorf("没有进入 TLS pending：%w", ctx.Err())
										}
									}
									target.Store(streams["/b"])
									switch control {
									case "ping":
										if err := fr.WritePing(false, [8]byte{'f', 'i', 'x', 't', 'u', 'r', 'e', '!'}); err != nil {
											return err
										}
									case "settings":
										if err := fr.WriteSettings(http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: 100}); err != nil {
											return err
										}
									}
									controlSent = true
									if err := fr.WriteWindowUpdate(streams["/b"], 128*1024); err != nil {
										return err
									}
									select {
									case <-updated:
									case <-ctx.Done():
										return fmt.Errorf("阻塞期间没有处理补窗：%w", ctx.Err())
									}
									// 补窗可能落在代理线路的两次 pending 通知之间。
									// 代理仍暂停时等写循环再次让出，确保已排队补窗能够进入调度。
									if proxyScheme != "" {
										for !writePending.Load() {
											select {
											case <-pending:
											case <-ctx.Done():
												return fmt.Errorf("补窗后没有再次进入 TLS pending：%w", ctx.Err())
											}
										}
									}
									if err := tcp.SetReadBuffer(262144); err != nil {
										return err
									}
									resume()
									paused = true
								}
								if !f.StreamEnded() {
									continue
								}
								switch id {
								case "/a":
									if !bResumed || counts[id] != 8*1024*1024 {
										return fmt.Errorf("大上传结束前未恢复补窗流，或正文长度错误：b=%t a=%d", bResumed, counts[id])
									}
									finished++
								case "/b":
									if counts[id] != 128*1024 {
										return fmt.Errorf("第二流长度错误：%d", counts[id])
									}
									finished++
								case "/probe":
									if counts[id] != 2 {
										return fmt.Errorf("预热正文长度错误：%d", counts[id])
									}
								}
								if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
									return err
								}
								if err := fr.WriteData(f.StreamID, true, []byte("{}")); err != nil {
									return err
								}
							}
						}
						return nil
					}()
				}()
				addr := ln.Addr().String()
				var proxy *outboundProxy
				var seenProxy func() bool
				if proxyScheme != "" {
					_, port, err := net.SplitHostPort(addr)
					if err != nil {
						t.Fatal(err)
					}
					addr = net.JoinHostPort("example.com", port)
					proxy = &outboundProxy{url: &url.URL{Scheme: proxyScheme}}
					pause := &proxyUploadPause{
						after: 1 << 20, onPause: sync.OnceFunc(func() { close(proxyPaused) }), resume: resumeProxy,
					}
					switch proxyScheme {
					case "socks5", "socks5h":
						fixture := &fakeSOCKS5{backend: ln.Addr().String(), user: "fixture", pass: "proxy", pause: pause}
						proxy.url.Host = fixture.start(t)
						proxy.url.User = url.UserPassword(fixture.user, fixture.pass)
						seenProxy = func() bool {
							targets := fixture.seen()
							return len(targets) == 1 && targets[0] == addr
						}
					case "http", "https":
						fixture := &fakeConnectProxy{backend: ln.Addr().String(), pause: pause}
						var serverTLS *tls.Config
						if proxyScheme == "https" {
							tlsServer, roots, _ := hostSNIServer(t)
							serverTLS = &tls.Config{Certificates: tlsServer.TLS.Certificates}
							proxy.tlsConfig = &tls.Config{RootCAs: roots}
						}
						proxy.url.Host = fixture.start(t, serverTLS)
						seenProxy = func() bool {
							requests := fixture.seen()
							return len(requests) == 1 && requests[0].Method == http.MethodConnect && requests[0].Host == addr
						}
					}
				}
				base := newTransport(proxy)
				base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				tr := newProfileHTTP2(base, profile)
				defer tr.CloseIdleConnections()
				handshake := tr.TLSHandshakeContext
				tr.TLSHandshakeContext = func(ctx context.Context, host string, raw net.Conn) (net.Conn, *tls.ConnectionState, error) {
					conn, state, err := handshake(ctx, host, raw)
					if err != nil {
						return nil, nil, err
					}
					return &profileBIOObservedTLS{profileTLSConn: conn.(*profileTLSConn), pending: pending, active: &writePending, updated: updated, target: &target}, state, nil
				}
				client := &http.Client{Transport: tr}
				run := func(path string, body []byte) error {
					r, err := NewBufferedRequest(ctx, "POST", "https://"+addr+path, body)
					if err != nil {
						return err
					}
					resp, err := client.Do(r)
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					b, err := io.ReadAll(resp.Body)
					if err != nil {
						return err
					}
					if resp.StatusCode != 200 || string(b) != "{}" {
						return fmt.Errorf("响应不完整：status=%d bytes=%d", resp.StatusCode, len(b))
					}
					return nil
				}
				if err := run("/probe", []byte("{}")); err != nil {
					t.Fatal(err)
				}
				calls := make(chan error, 2)
				go func() { calls <- run("/a", bytes.Repeat([]byte{'a'}, 8*1024*1024)) }()
				go func() { calls <- run("/b", bytes.Repeat([]byte{'b'}, 128*1024)) }()
				for range 2 {
					if err := <-calls; err != nil {
						t.Error(err)
					}
				}
				if err := <-serverDone; err != nil {
					t.Fatal(err)
				}
				if seenProxy != nil && !seenProxy() {
					t.Fatal("请求未在同一条代理隧道完成")
				}
			})
		}
	}
}
