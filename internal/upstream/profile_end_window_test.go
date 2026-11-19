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
	"net/http"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// 官方 Work/TTNet 的空结束 DATA 同样等两个窗口为正；SETTINGS 缩窗可使流窗口为负。
func TestProfileHTTP2EmptyUploadEndWindow(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeCode} {
		for _, negative := range []bool{false, true} {
			for _, canceled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/negative=%t/canceled=%t", httpProfileName(profile, false), negative, canceled), func(t *testing.T) {
					ctx, finish := context.WithTimeout(t.Context(), 3*time.Second)
					defer finish()
					callctx, cancel := context.WithCancel(ctx)
					defer cancel()
					ln := profileDynamicListener(t)
					allowEOF := make(chan struct{})
					produced := make(chan error, 1)
					waiting := make(chan struct{})
					serverDone := make(chan error, 1)
					reader, writer := io.Pipe()
					defer reader.Close()
					defer writer.Close()
					go func() {
						if _, err := io.WriteString(writer, "x"); err != nil {
							produced <- err
							return
						}
						select {
						case <-allowEOF:
							produced <- writer.Close()
						case <-ctx.Done():
							produced <- writer.CloseWithError(ctx.Err())
						}
					}()
					go func() {
						serverDone <- func() error {
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
							if err := fr.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 1}); err != nil {
								return err
							}
							acks, needed := 0, 1
							if negative {
								needed = 2
							}
							var stream uint32
							for stream == 0 || acks < needed {
								frame, err := fr.ReadFrame()
								if err != nil {
									return err
								}
								switch f := frame.(type) {
								case *http2.SettingsFrame:
									if f.IsAck() {
										acks++
									} else if err := fr.WriteSettingsAck(); err != nil {
										return err
									}
								case *http2.DataFrame:
									if stream != 0 || string(f.Data()) != "x" || f.StreamEnded() {
										return fmt.Errorf("首块不是未结束的单字节正文：%v", f)
									}
									stream = f.StreamID
									if negative {
										if err := fr.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 0}); err != nil {
											return err
										}
									}
								}
							}
							close(allowEOF)
							select {
							case err := <-produced:
								if err != nil {
									return err
								}
							case <-ctx.Done():
								return ctx.Err()
							}
							type received struct {
								frame http2.Frame
								err   error
							}
							frames := make(chan received, 1)
							go func() {
								for {
									f, err := fr.ReadFrame()
									select {
									case frames <- received{f, err}:
									case <-ctx.Done():
										return
									}
									if err != nil {
										return
									}
								}
							}()
							timer := time.NewTimer(50 * time.Millisecond)
							defer timer.Stop()
							observing := true
							for observing {
								select {
								case got := <-frames:
									if got.err != nil {
										return got.err
									}
									if got.frame.Header().Type == http2.FrameData {
										return fmt.Errorf("零/负窗口提前发送结束 DATA")
									}
								case <-timer.C:
									observing = false
								case <-ctx.Done():
									return ctx.Err()
								}
							}
							close(waiting)
							if !canceled {
								increment := uint32(1)
								if negative {
									increment = 2
								}
								if err := fr.WriteWindowUpdate(stream, increment); err != nil {
									return err
								}
							}
							for {
								select {
								case got := <-frames:
									if got.err != nil {
										return got.err
									}
									switch f := got.frame.(type) {
									case *http2.DataFrame:
										if canceled || f.StreamID != stream || len(f.Data()) != 0 || !f.StreamEnded() {
											return fmt.Errorf("非预期结束帧：%v", f)
										}
										if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: stream, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
											return err
										}
										return fr.WriteData(stream, true, []byte("{}"))
									case *http2.RSTStreamFrame:
										if !canceled || f.StreamID != stream || f.ErrCode != http2.ErrCodeCancel {
											return fmt.Errorf("非预期取消：%v", f)
										}
										return nil
									}
								case <-ctx.Done():
									return ctx.Err()
								}
							}
						}()
					}()
					base := newTransport(nil)
					base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
					tr := newProfileHTTP2(base, profile)
					defer tr.CloseIdleConnections()
					client := &http.Client{Transport: tr}
					clientDone := make(chan error, 1)
					go func() {
						req, err := http.NewRequestWithContext(callctx, http.MethodPost, "https://"+ln.Addr().String()+"/fixture", reader)
						if err != nil {
							clientDone <- err
							return
						}
						res, err := client.Do(req)
						if err != nil {
							clientDone <- err
							return
						}
						defer res.Body.Close()
						raw, err := io.ReadAll(res.Body)
						if err == nil && (res.StatusCode != 200 || string(raw) != "{}") {
							err = fmt.Errorf("响应不完整：%d %q", res.StatusCode, raw)
						}
						clientDone <- err
					}()
					select {
					case <-waiting:
					case err := <-serverDone:
						t.Fatalf("窗口采样失败：%v", err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					if canceled {
						cancel()
					}
					err := <-clientDone
					if canceled && !errors.Is(err, context.Canceled) || !canceled && err != nil {
						t.Fatalf("结束或取消原因错误：%v", err)
					}
					if err := <-serverDone; err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}
