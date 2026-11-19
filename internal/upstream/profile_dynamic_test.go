// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// 假上游只处理实际帧，避免标准服务器替测试自动补窗或重试。
func profileDynamicListener(t *testing.T) net.Listener {
	t.Helper()
	cert := httptest.NewTLSServer(nil)
	certificates := cert.TLS.Certificates
	cert.Close()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: certificates, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func TestProfileHTTP2UploadFramesAndWindows(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		t.Run(httpProfileName(profile, false), func(t *testing.T) {
			ln := profileDynamicListener(t)
			body := strings.Repeat("u", 256<<10)
			done := make(chan error, 1)
			go func() {
				done <- func() error {
					conn, err := ln.Accept()
					if err != nil {
						return err
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
					if _, err := io.CopyN(io.Discard, conn, int64(len(http2.ClientPreface))); err != nil {
						return err
					}
					fr := http2.NewFramer(conn, conn)
					if err := fr.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 32768}); err != nil {
						return err
					}
					bodies := map[uint32][]byte{}
					windows := map[uint32]int{}
					connectionWindow := 65535
					completed := 0
					largest := 0
					updates := 0
					for completed < 2 {
						frame, err := fr.ReadFrame()
						if err != nil {
							return err
						}
						switch f := frame.(type) {
						case *http2.SettingsFrame:
							if !f.IsAck() {
								if err := fr.WriteSettingsAck(); err != nil {
									return err
								}
							}
						case *http2.HeadersFrame:
							bodies[f.StreamID] = nil
							windows[f.StreamID] = 32768
						case *http2.DataFrame:
							n := len(f.Data())
							connectionWindow -= n
							windows[f.StreamID] -= n
							if n > 16375 {
								return fmt.Errorf("DATA 负载 %d 超出官方上限 16375", n)
							}
							if connectionWindow < 0 || windows[f.StreamID] < 0 {
								return fmt.Errorf("上传超出服务端连接或流额度")
							}
							largest = max(largest, n)
							bodies[f.StreamID] = append(bodies[f.StreamID], f.Data()...)
							if connectionWindow == 0 {
								if err := fr.WriteWindowUpdate(0, 65535); err != nil {
									return err
								}
								connectionWindow = 65535
								updates++
							}
							if windows[f.StreamID] == 0 {
								if err := fr.WriteWindowUpdate(f.StreamID, 32768); err != nil {
									return err
								}
								windows[f.StreamID] = 32768
								updates++
							}
							if !f.StreamEnded() {
								continue
							}
							completed++
							want := "{}"
							if completed == 2 {
								want = body
							}
							if string(bodies[f.StreamID]) != want {
								return fmt.Errorf("第 %d 个上传正文不完整", completed)
							}
							if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
								return err
							}
							if err := fr.WriteData(f.StreamID, true, []byte("{}")); err != nil {
								return err
							}
						}
					}
					if largest != 16375 || updates == 0 {
						return fmt.Errorf("没有覆盖最大分片与零窗口补充：max=%d，updates=%d", largest, updates)
					}
					return nil
				}()
			}()
			base := newTransport(nil)
			base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			tr := newProfileHTTP2(base, profile)
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
			for _, content := range []string{"{}", body} {
				response, err := client.Post("https://"+ln.Addr().String()+"/fixture", "text/plain", strings.NewReader(content))
				if err != nil {
					t.Fatal(err)
				}
				reply, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if err != nil || string(reply) != "{}" {
					t.Fatalf("上传响应不完整：%s: %v", reply, err)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProfileHTTP2PostReplayAfterPeerRejection(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		for _, goAway := range []bool{false, true} {
			for _, rejects := range []int{1, 2, 3} {
				for _, warmup := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/goaway=%t/rejections=%d/warmup=%t", httpProfileName(profile, false), goAway, rejects, warmup), func(t *testing.T) {
						ln := profileDynamicListener(t)
						warmupRequests := 0
						contents := []string{"charge-once", "followup"}
						if warmup {
							warmupRequests = 1
							contents = append([]string{"warmup"}, contents...)
						}
						done := make(chan error, 1)
						go func() {
							done <- func() error {
								requests := 0
								wantRequests := warmupRequests + 1 + min(rejects+1, 3)
								if profile == ProfileTraeCode {
									wantRequests = warmupRequests + 2
								}
								for requests < wantRequests {
									conn, err := ln.Accept()
									if err != nil {
										return err
									}
									err = func() error {
										defer conn.Close()
										_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
										if _, err := io.CopyN(io.Discard, conn, int64(len(http2.ClientPreface))); err != nil {
											return err
										}
										fr := http2.NewFramer(conn, conn)
										if err := fr.WriteSettings(); err != nil {
											return err
										}
										bodies := map[uint32][]byte{}
										lastAccepted := uint32(0)
										for requests < wantRequests {
											frame, err := fr.ReadFrame()
											if err != nil {
												return err
											}
											switch f := frame.(type) {
											case *http2.SettingsFrame:
												if !f.IsAck() {
													if err := fr.WriteSettingsAck(); err != nil {
														return err
													}
												}
											case *http2.DataFrame:
												bodies[f.StreamID] = append(bodies[f.StreamID], f.Data()...)
												if !f.StreamEnded() {
													continue
												}
												requests++
												want := "charge-once"
												switch requests {
												case 1:
													if warmup {
														want = "warmup"
													}
												case wantRequests:
													want = "followup"
												}
												if !bytes.Equal(bodies[f.StreamID], []byte(want)) {
													return fmt.Errorf("第 %d 个请求正文=%q，期望 %q", requests, bodies[f.StreamID], want)
												}
												if requests > warmupRequests && requests < wantRequests && requests <= rejects+warmupRequests {
													if goAway {
														if err := fr.WriteGoAway(lastAccepted, http2.ErrCodeNo, nil); err != nil {
															return err
														}
														// 排空控制帧后再关闭，避免未读 ACK 触发 TCP RST 掩盖 GOAWAY。
														_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
														for {
															frame, err := fr.ReadFrame()
															if err == io.EOF {
																return nil
															}
															if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
																return nil
															}
															if err != nil {
																return err
															}
															switch frame.(type) {
															case *http2.HeadersFrame, *http2.DataFrame:
																return fmt.Errorf("GOAWAY 后仍在原连接发送业务请求")
															}
														}
													}
													if err := fr.WriteRSTStream(f.StreamID, http2.ErrCodeRefusedStream); err != nil {
														return err
													}
													continue
												}
												if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
													return err
												}
												if err := fr.WriteData(f.StreamID, true, []byte("{}")); err != nil {
													return err
												}
												lastAccepted = f.StreamID
											}
										}
										return nil
									}()
									if err != nil {
										return err
									}
								}
								return nil
							}()
						}()
						base := newTransport(nil)
						base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
						tr := newProfileHTTP2(base, profile)
						defer tr.CloseIdleConnections()
						client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
						for i, content := range contents {
							request, err := http.NewRequest(http.MethodPost, "https://"+ln.Addr().String()+"/fixture", strings.NewReader(content))
							if err != nil {
								t.Fatal(err)
							}
							request.Header.Set("Content-Type", "text/plain")
							var replays atomic.Int32
							getBody := request.GetBody
							request.GetBody = func() (io.ReadCloser, error) {
								replays.Add(1)
								return getBody()
							}
							response, err := client.Do(request)
							wantReplays := 0
							if profile != ProfileTraeCode && i == warmupRequests {
								wantReplays = min(rejects, 2)
							}
							if got := replays.Load(); int(got) != wantReplays {
								t.Fatalf("重建正文 %d 次，期望 %d 次；预算耗尽后不能创建未使用的正文", got, wantReplays)
							}
							if (profile == ProfileTraeCode || rejects >= 3) && i == warmupRequests {
								if response != nil {
									_ = response.Body.Close()
								}
								if err == nil {
									t.Fatalf("官方应停止重发，但请求仍成功：%v", err)
								}
								cause := "REFUSED_STREAM"
								if goAway {
									cause = "GOAWAY"
								}
								if !strings.Contains(err.Error(), cause) {
									t.Fatalf("无法回放的错误丢失上游拒绝原因：%v", err)
								}
								continue
							}
							if err != nil {
								t.Fatal(err)
							}
							reply, err := io.ReadAll(response.Body)
							_ = response.Body.Close()
							if err != nil || string(reply) != "{}" {
								t.Fatalf("后续请求未完整恢复：%s: %v", reply, err)
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
}

func TestProfileHTTP2CancellationDuringRejection(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		t.Run(httpProfileName(profile, false), func(t *testing.T) {
			ln := profileDynamicListener(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- func() error {
					conn, err := ln.Accept()
					if err != nil {
						return err
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
					if _, err := io.CopyN(io.Discard, conn, int64(len(http2.ClientPreface))); err != nil {
						return err
					}
					fr := http2.NewFramer(conn, conn)
					if err := fr.WriteSettings(); err != nil {
						return err
					}
					bodies := map[uint32][]byte{}
					completed := 0
					for completed < 2 {
						frame, err := fr.ReadFrame()
						if err != nil {
							return err
						}
						switch f := frame.(type) {
						case *http2.SettingsFrame:
							if !f.IsAck() {
								if err := fr.WriteSettingsAck(); err != nil {
									return err
								}
							}
						case *http2.DataFrame:
							bodies[f.StreamID] = append(bodies[f.StreamID], f.Data()...)
							if !f.StreamEnded() {
								continue
							}
							completed++
							if completed == 1 {
								if string(bodies[f.StreamID]) != "charge-once" {
									return fmt.Errorf("首个请求正文不符")
								}
								// 在明确拒绝与重试判定之间取消，后续不得再次发出这份正文。
								cancel()
								if err := fr.WriteRSTStream(f.StreamID, http2.ErrCodeRefusedStream); err != nil {
									return err
								}
								continue
							}
							if string(bodies[f.StreamID]) != "followup" {
								return fmt.Errorf("取消后仍然重发旧正文：%q", bodies[f.StreamID])
							}
							if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
								return err
							}
							return fr.WriteData(f.StreamID, true, []byte("{}"))
						}
					}
					return nil
				}()
			}()
			base := newTransport(nil)
			base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			tr := newProfileHTTP2(base, profile)
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+ln.Addr().String()+"/fixture", strings.NewReader("charge-once"))
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if response != nil {
				_ = response.Body.Close()
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("取消被重试或拒绝错误覆盖：%v", err)
			}
			response, err = client.Post("https://"+ln.Addr().String()+"/fixture", "text/plain", strings.NewReader("followup"))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || string(raw) != "{}" {
				t.Fatalf("取消影响了下一请求：%s: %v", raw, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProfileTTNetUploadEndFrames(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		for _, size := range []int{2, 16374, 16375, 16376, 32750, 32751} {
			t.Run(fmt.Sprintf("%d/buffered=%t", size, buffered), func(t *testing.T) {
				ln := profileDynamicListener(t)
				done := make(chan error, 1)
				go func() {
					done <- func() error {
						conn, err := ln.Accept()
						if err != nil {
							return err
						}
						defer conn.Close()
						_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
						if _, err := io.CopyN(io.Discard, conn, int64(len(http2.ClientPreface))); err != nil {
							return err
						}
						fr := http2.NewFramer(conn, conn)
						if err := fr.WriteSettings(); err != nil {
							return err
						}
						received := 0
						frames := 0
						for {
							frame, err := fr.ReadFrame()
							if err != nil {
								return err
							}
							switch f := frame.(type) {
							case *http2.SettingsFrame:
								if !f.IsAck() {
									if err := fr.WriteSettingsAck(); err != nil {
										return err
									}
								}
							case *http2.DataFrame:
								frames++
								received += len(f.Data())
								if len(f.Data()) > 16375 {
									return fmt.Errorf("TTNet 上传分片越界：%d", len(f.Data()))
								}
								if !f.StreamEnded() {
									continue
								}
								wantFrames := (size + 16374) / 16375
								if size <= 16375 {
									wantFrames++
								}
								wantLast := size % 16375
								if size > 16375 && wantLast == 0 {
									wantLast = 16375
								} else if size <= 16375 {
									wantLast = 0
								}
								if received != size || frames != wantFrames || len(f.Data()) != wantLast {
									return fmt.Errorf("TTNet 结束帧不符：正文=%d，帧数=%d，最后负载=%d；期望 %d/%d/%d", received, frames, len(f.Data()), size, wantFrames, wantLast)
								}
								if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
									return err
								}
								return fr.WriteData(f.StreamID, true, []byte("{}"))
							}
						}
					}()
				}()
				base := newTransport(nil)
				base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				tr := newProfileHTTP2(base, ProfileTraeCode)
				defer tr.CloseIdleConnections()
				client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
				endpoint := "https://" + ln.Addr().String() + "/fixture"
				var request *http.Request
				var err error
				if buffered {
					request, err = NewBufferedRequest(t.Context(), http.MethodPost, endpoint, []byte(strings.Repeat("u", size)))
				} else {
					request, err = http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(strings.Repeat("u", size)))
				}
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "text/plain")
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
