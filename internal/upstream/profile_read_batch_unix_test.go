// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

//go:build unix

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
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// 同一 TLS 记录的补窗不能在 PING 被处理前触发 DATA；实际读取 pending
// 后则须让出，否则拆开送来的第二批输入会与上传互相等待。
func TestProfileHTTP2ReadBatchControlOrder(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		for _, together := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/同批=%t", httpProfileName(profile, false), together), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				ln := profileDynamicListener(t)
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
						if err := fr.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 1}); err != nil {
							return err
						}
						ping := [8]byte{'f', 'i', 'x', 't', 'u', 'r', 'e', '!'}
						var stream uint32
						var body []byte
						var sent, ack, ended bool
						warm := false
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
							case *http2.HeadersFrame:
								if !warm {
									if !f.StreamEnded() {
										return fmt.Errorf("预热 GET 带了正文")
									}
									warm = true
									if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndHeaders: true, EndStream: true, BlockFragment: []byte{0x88}}); err != nil {
										return err
									}
								} else {
									stream = f.StreamID
								}
							case *http2.PingFrame:
								if !f.IsAck() || f.Data != ping || !sent {
									return fmt.Errorf("非预期 PING：%v", f)
								}
								ack = true
							case *http2.DataFrame:
								if f.StreamID != stream {
									return fmt.Errorf("DATA 发到了其他流")
								}
								if len(body) == 0 {
									if string(f.Data()) != "u" {
										return fmt.Errorf("未遵守一个字节的初始流窗口：%q", f.Data())
									}
									var batch bytes.Buffer
									bf := http2.NewFramer(&batch, nil)
									if err := bf.WriteWindowUpdate(stream, 5); err != nil {
										return err
									}
									if together {
										if err := bf.WritePing(false, ping); err != nil {
											return err
										}
										sent = true
									}
									if _, err := conn.Write(batch.Bytes()); err != nil {
										return err
									}
								} else if len(f.Data()) > 0 {
									if together && !ack {
										return fmt.Errorf("同一读批次的 PING ACK 前已发 DATA")
									}
									if !together && !sent {
										if err := fr.WritePing(false, ping); err != nil {
											return err
										}
										sent = true
									}
								}
								body = append(body, f.Data()...)
								ended = f.StreamEnded()
								// TTNet 小正文另发空结束 DATA；官方同样要求流窗口为正。
								if len(body) == 6 && !ended {
									if err := fr.WriteWindowUpdate(stream, 1); err != nil {
										return err
									}
								}
							}
							if ended && ack {
								if string(body) != "upload" {
									return fmt.Errorf("正文损坏：%q", body)
								}
								if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: stream, EndHeaders: true, EndStream: true, BlockFragment: []byte{0x88}}); err != nil {
									return err
								}
								<-ctx.Done()
								return nil
							}
						}
					}()
				}()
				base := newTransport(nil)
				base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				tr := newProfileHTTP2(base, profile)
				defer tr.CloseIdleConnections()
				client := &http.Client{Transport: tr}
				for _, post := range []bool{false, true} {
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+ln.Addr().String()+"/warmup", nil)
					if post {
						req, err = NewBufferedRequest(ctx, http.MethodPost, "https://"+ln.Addr().String()+"/upload", []byte("upload"))
					}
					if err != nil {
						t.Fatal(err)
					}
					res, err := client.Do(req)
					if err != nil {
						select {
						case serverErr := <-done:
							t.Fatalf("请求失败：%v；采样服务：%v", err, serverErr)
						case <-ctx.Done():
							t.Fatal(err)
						}
					}
					_, readErr := io.Copy(io.Discard, res.Body)
					_ = res.Body.Close()
					if readErr != nil || res.StatusCode != http.StatusOK {
						t.Fatalf("响应不完整：status=%d err=%v", res.StatusCode, readErr)
					}
				}
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestProfileSocketBIOReadPendingAndCallbackClose(t *testing.T) {
	for _, closeInCallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("回调关闭=%t", closeInCallback), func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			raw, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			peer, err := ln.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			wrapped, err := newProfileSocketBIO(raw)
			if err != nil {
				t.Fatal(err)
			}
			c := wrapped.(*profileSocketBIO)
			defer c.Close()
			states := make(chan bool, 2)
			c.SetReadStateCallback(func(reading bool) {
				if closeInCallback && !reading {
					_ = c.Close()
				}
				states <- reading
			})
			done := make(chan error, 1)
			go func() {
				p := make([]byte, 4)
				_, err := io.ReadFull(c, p)
				if err == nil && string(p) != "read" {
					err = fmt.Errorf("读取正文损坏：%q", p)
				}
				done <- err
			}()
			select {
			case reading := <-states:
				if reading {
					t.Fatal("缺少读取 pending 通知")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("读取 pending 回调阻塞，可能仍持有 fd 引用")
			}
			if !closeInCallback {
				if _, err := peer.Write([]byte("read")); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if closeInCallback && !errors.Is(err, net.ErrClosed) || !closeInCallback && err != nil {
					t.Fatalf("读取结果错误：%v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("读取或关闭没有退出")
			}
			if !closeInCallback && !<-states {
				t.Fatal("读取恢复后未交还读循环")
			}
		})
	}
}
