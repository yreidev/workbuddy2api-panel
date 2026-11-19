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
	"net"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// 官方三套运行时都在两流上限内排队；单流或连接窗口归零不阻塞 CANCEL。
// 原始 socket 的读取批次会改变跨流切分，Work/Auth 也有官方反例；
// 确定的批次及其逐帧顺序由固定 req 的读批次回归另外锁定。
func TestProfileHTTP2MultiUploadCancellation(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		for _, connectionZero := range []bool{false, true} {
			for _, unknown := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/connection_zero=%t/unknown=%t", httpProfileName(profile, false), connectionZero, unknown), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					ln := profileDynamicListener(t)
					started := map[string]chan struct{}{"a": make(chan struct{}), "b": make(chan struct{})}
					ready := make(chan struct{})
					done := make(chan error, 1)
					go func() {
						done <- func() error {
							conn, err := ln.Accept()
							if err != nil {
								return err
							}
							defer conn.Close()
							_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
							if _, err = io.CopyN(io.Discard, conn, int64(len(http2.ClientPreface))); err != nil {
								return err
							}
							fr := http2.NewFramer(conn, conn)
							fr.ReadMetaHeaders = hpack.NewDecoder(65536, nil)
							if err = fr.WriteSettings(http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: 2}, http2.Setting{ID: http2.SettingInitialWindowSize, Val: 1}); err != nil {
								return err
							}
							bodies := map[uint32][]byte{}
							windows := map[uint32]int{}
							ids := map[uint32]string{}
							connectionWindow := 65535
							shorts, completed := 0, 0
							var cancelled uint32
							gated := false
							var aFrames []int
							progress := func() string {
								received := map[string]int{}
								remaining := map[string]int{}
								for stream, id := range ids {
									received[id] = len(bodies[stream])
									remaining[id] = windows[stream]
								}
								return fmt.Sprintf("连接窗口=%d 各流窗口=%v 已收正文=%v 已响应上传=%d", connectionWindow, remaining, received, completed)
							}
							update := func(s uint32, n int) error {
								if err := fr.WriteWindowUpdate(s, uint32(n)); err != nil {
									return err
								}
								if s == 0 {
									connectionWindow += n
								} else {
									windows[s] += n
								}
								return nil
							}
							for shorts < 2 {
								frame, err := fr.ReadFrame()
								if err != nil {
									return fmt.Errorf("读取上传帧：%w（%s）", err, progress())
								}
								switch f := frame.(type) {
								case *http2.SettingsFrame:
									if !f.IsAck() {
										if err := fr.WriteSettingsAck(); err != nil {
											return err
										}
									}
								case *http2.PingFrame:
									if !f.IsAck() {
										if err := fr.WritePing(true, f.Data); err != nil {
											return err
										}
									}
								case *http2.MetaHeadersFrame:
									id := strings.TrimPrefix(f.PseudoValue("path"), "/")
									if id != "a" && id != "b" && id != "c" && id != "probe" {
										return fmt.Errorf("未知上传路径 %q", id)
									}
									if id == "c" && cancelled == 0 {
										return fmt.Errorf("第三流在取消前绕过并发上限")
									}
									parentLabel := ""
									switch id {
									case "b":
										parentLabel = "a"
									case "c":
										parentLabel = "b"
									}
									var parent uint32
									for sid, label := range ids {
										if label == parentLabel {
											parent = sid
										}
									}
									if parentLabel != "" && parent == 0 {
										return fmt.Errorf("流 %s 的父流 %s 尚未建立", id, parentLabel)
									}
									weight := uint8(219)
									if profile == ProfileTraeAuth {
										weight = 109
									}
									// Auth 先收完整份未知正文，C 开头写出时 B 可能已经完成。
									// 服务端无法知道响应何时被客户端处理，只在父流仍上传时锁定依赖。
									parentStillUploading := id != "c" || completed == 0
									if !f.HasPriority() || parentStillUploading && f.Priority.StreamDep != parent || f.Priority.Weight != weight || !f.Priority.Exclusive {
										return fmt.Errorf("流 %s 的 HEADERS 优先级与官方不同：parent=%d want=%d weight=%d exclusive=%t", id, f.Priority.StreamDep, parent, f.Priority.Weight, f.Priority.Exclusive)
									}
									for sid, old := range ids {
										if old == id && id != "probe" {
											return fmt.Errorf("正文被重复发送：%s，stream=%d/%d", id, sid, f.StreamID)
										}
									}
									ids[f.StreamID] = id
									windows[f.StreamID] = 1
									// 预热正文可能在客户端收到初始 SETTINGS 前发出，允许其原始窗口。
									if shorts == 0 {
										windows[f.StreamID] = 65535
									}
								case *http2.RSTStreamFrame:
									if ids[f.StreamID] != "a" || f.ErrCode != http2.ErrCodeCancel || cancelled != 0 || !gated {
										return fmt.Errorf("非预期取消：stream=%d code=%s（%s）", f.StreamID, f.ErrCode, progress())
									}
									cancelled = f.StreamID
									// 已取消流的迟到补窗必须忽略，不得复活其正文或阻塞其他流。
									if err := update(cancelled, 16375); err != nil {
										return err
									}
									if err := update(0, 65535-connectionWindow); err != nil {
										return err
									}
									for sid, id := range ids {
										if id == "b" {
											if err := update(sid, 32768); err != nil {
												return err
											}
										}
									}
								case *http2.DataFrame:
									s := f.StreamID
									id := ids[s]
									n := len(f.Data())
									flow := int(f.Header().Length)
									if s == cancelled && n > 0 {
										return fmt.Errorf("取消后仍发送 a 正文")
									}
									connectionWindow -= flow
									windows[s] -= flow
									if connectionWindow < 0 || windows[s] < 0 || n > 16375 {
										return fmt.Errorf("上传越过窗口或官方分片上限：id=%s n=%d cw=%d sw=%d", id, n, connectionWindow, windows[s])
									}
									if id == "a" {
										aFrames = append(aFrames, n)
									}
									first := len(bodies[s]) == 0
									bodies[s] = append(bodies[s], f.Data()...)
									if first && n > 0 {
										amount := 32767
										if id == "b" && !connectionZero {
											amount = 16374
										}
										if id == "probe" {
											amount = 2
										}
										if err := update(s, amount); err != nil {
											return err
										}
										if ch := started[id]; ch != nil {
											close(ch)
										}
									}
									if !gated && cancelled == 0 {
										amounts := map[string]int{}
										for sid, i := range ids {
											amounts[i] = len(bodies[sid])
										}
										gate := amounts["a"] == 32768 && amounts["b"] == 16375
										if connectionZero {
											gate = connectionWindow == 0 && amounts["a"] > 0 && amounts["b"] > 0
										}
										if gate {
											if profile == ProfileTraeCode && unknown && !connectionZero && !slices.Equal(aFrames, []int{1, 16374, 16375, 18}) {
												return fmt.Errorf("TTNet 上传读取边界与官方不同：%v", aFrames)
											}
											gated = true
											close(ready)
										}
									}
									if cancelled != 0 && (id == "b" || id == "c") {
										// 结束帧仍消耗连接窗口。即使本流已完成，也要补给其余流。
										if connectionWindow == 0 {
											if err := update(0, 65535); err != nil {
												return err
											}
										}
										if !f.StreamEnded() && windows[s] == 0 {
											if err := update(s, 32768); err != nil {
												return err
											}
										}
									}
									if !f.StreamEnded() {
										continue
									}
									expected := "{}"
									switch id {
									case "b", "c":
										expected = strings.Repeat(id, 131072)
										completed++
									case "probe":
										shorts++
									default:
										return fmt.Errorf("a 不应完成")
									}
									if string(bodies[s]) != expected {
										return fmt.Errorf("%s 正文损坏：%d 字节", id, len(bodies[s]))
									}
									if shorts == 2 && (completed != 2 || cancelled == 0) {
										return fmt.Errorf("后续请求早于其他流完成")
									}

									if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: s, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
										return err
									}
									if err := fr.WriteData(s, true, []byte("{}")); err != nil {
										return err
									}
								}
							}
							<-ctx.Done()
							return nil
						}()
					}()
					base := newTransport(nil)
					base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
					originalDial := base.DialContext
					var dials atomic.Int32
					base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
						dials.Add(1)
						return originalDial(ctx, network, address)
					}
					tr := newProfileHTTP2(base, profile)
					defer tr.CloseIdleConnections()
					client := &http.Client{Transport: tr}
					run := func(callctx context.Context, id string, body io.Reader) error {
						req, err := http.NewRequestWithContext(callctx, http.MethodPost, "https://"+ln.Addr().String()+"/"+id, body)
						if reader, ok := body.(*strings.Reader); ok {
							data, e := io.ReadAll(reader)
							if e != nil {
								return e
							}
							req, err = NewBufferedRequest(callctx, http.MethodPost, "https://"+ln.Addr().String()+"/"+id, data)
						}
						if err != nil {
							return err
						}
						res, err := client.Do(req)
						if err != nil {
							return err
						}
						defer res.Body.Close()
						raw, err := io.ReadAll(res.Body)
						if err != nil || res.StatusCode != 200 || string(raw) != "{}" {
							return fmt.Errorf("%s 响应不完整：%q: %v", id, raw, err)
						}
						return nil
					}
					// 失败时唤醒服务端，避免悬挂 goroutine 掩盖真正的错误。
					await := func(ch <-chan struct{}) {
						t.Helper()
						select {
						case <-ch:
						case err := <-done:
							t.Fatalf("采样服务失败：%v", err)
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					if err := run(ctx, "probe", strings.NewReader("{}")); err != nil {
						select {
						case serverErr := <-done:
							t.Fatalf("请求失败：%v；服务端：%v", err, serverErr)
						case <-time.After(time.Second):
							t.Fatal(err)
						}
					}
					launch := func(callctx context.Context, id string) <-chan error {
						result := make(chan error, 1)
						go func() {
							var source io.Reader = strings.NewReader(strings.Repeat(id, 131072))
							var produced chan error
							if unknown {
								reader, writer := io.Pipe()
								source = reader
								defer reader.Close()
								produced = make(chan error, 1)
								go func() {
									for range 8 {
										time.Sleep(10 * time.Millisecond)
										if _, err := io.WriteString(writer, strings.Repeat(id, 16384)); err != nil {
											produced <- err
											return
										}
									}
									// 官方 ReadableStream 在第九次 pull 才通知结束。
									time.Sleep(10 * time.Millisecond)
									produced <- writer.Close()
								}()
							}
							err := run(callctx, id, source)
							if produced != nil {
								_ = source.(io.Closer).Close()
								production := <-produced
								if err == nil && production != nil {
									err = production
								}
							}
							result <- err
						}()
						return result
					}
					actx, abort := context.WithCancel(ctx)
					defer abort()
					a := launch(actx, "a")
					await(started["a"])
					b := launch(ctx, "b")
					await(started["b"])
					c := launch(ctx, "c")
					await(ready)
					select {
					case err := <-c:
						t.Fatalf("第三流未排队：%v", err)
					case err := <-done:
						t.Fatalf("取消前服务失败：%v", err)
					case <-time.After(30 * time.Millisecond):
					}
					abort()
					if err := <-a; !errors.Is(err, context.Canceled) {
						t.Fatalf("取消原因丢失：%v", err)
					}
					for _, result := range []<-chan error{b, c} {
						if err := <-result; err != nil {
							select {
							case serverErr := <-done:
								t.Fatalf("请求失败：%v；服务端：%v", err, serverErr)
							case <-time.After(time.Second):
								t.Fatal(err)
							}
						}
					}
					if err := run(ctx, "probe", strings.NewReader("{}")); err != nil {
						select {
						case serverErr := <-done:
							t.Fatalf("请求失败：%v；服务端：%v", err, serverErr)
						case <-time.After(time.Second):
							t.Fatal(err)
						}
					}
					if dials.Load() != 1 {
						t.Fatalf("多流取消后未复用同一连接：拨号 %d 次", dials.Load())
					}
					cancel()
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}
