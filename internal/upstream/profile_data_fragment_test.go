// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestProfileHTTP2PartialDataDeliveryAndCancellation(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		for _, padded := range []bool{false, true} {
			for _, abort := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/padded=%t/cancel=%t", httpProfileName(profile, false), padded, abort), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					ln := profileDynamicListener(t)
					first := make(chan struct{})
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
							var stream uint32
							for stream == 0 {
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
									stream = f.StreamID
								}
							}
							if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: stream, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
								return err
							}
							var wire bytes.Buffer
							var pad []byte
							cut := 9 + 6000
							if padded {
								pad = make([]byte, 32)
								cut++
							}
							if err := http2.NewFramer(&wire, nil).WriteDataPadded(stream, true, bytes.Repeat([]byte{'r'}, 12000), pad); err != nil {
								return err
							}
							packet := bytes.Clone(wire.Bytes())
							if _, err := conn.Write(packet[:cut]); err != nil {
								return err
							}
							select {
							case <-first:
							case <-time.After(time.Second):
								return fmt.Errorf("整帧尚未收齐时没有交付首块正文")
							}
							var probeStream uint32
							if abort {
								for {
									frame, err := fr.ReadFrame()
									if err != nil {
										return err
									}
									if f, ok := frame.(*http2.RSTStreamFrame); ok {
										if f.StreamID != stream || f.ErrCode != http2.ErrCodeCancel {
											return fmt.Errorf("分帧取消不正确：%v", f)
										}
										break
									}
									// context 取消允许调用者先返回；后续请求可能先入队。
									if f, ok := frame.(*http2.HeadersFrame); ok {
										probeStream = f.StreamID
									}
								}
							}
							if _, err := conn.Write(packet[cut:]); err != nil {
								return err
							}
							for probeStream == 0 {
								frame, err := fr.ReadFrame()
								if err != nil {
									return err
								}
								if f, ok := frame.(*http2.HeadersFrame); ok {
									probeStream = f.StreamID
								}
							}
							if probeStream <= stream {
								return fmt.Errorf("后续请求流编号未前进")
							}
							if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: probeStream, EndHeaders: true, EndStream: true, BlockFragment: []byte{0x88}}); err != nil {
								return err
							}
							<-ctx.Done()
							return nil
						}()
					}()
					base := newTransport(nil)
					base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
					original := base.DialContext
					var dials atomic.Int32
					base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
						dials.Add(1)
						return original(ctx, network, address)
					}
					tr := newProfileHTTP2(base, profile)
					defer tr.CloseIdleConnections()
					client := &http.Client{Transport: tr}
					streamCtx, abortStream := context.WithCancel(ctx)
					defer abortStream()
					req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, "https://"+ln.Addr().String()+"/stream", nil)
					if err != nil {
						t.Fatal(err)
					}
					res, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer res.Body.Close()
					prefix := make([]byte, 6000)
					if _, err := io.ReadFull(res.Body, prefix); err != nil || !bytes.Equal(prefix, bytes.Repeat([]byte{'r'}, 6000)) {
						t.Fatalf("首块读取失败：%v", err)
					}
					close(first)
					if abort {
						abortStream()
						_ = res.Body.Close()
					} else {
						tail, err := io.ReadAll(res.Body)
						if err != nil || !bytes.Equal(tail, bytes.Repeat([]byte{'r'}, 6000)) {
							t.Fatalf("余下正文或结束标志不正确：len=%d err=%v", len(tail), err)
						}
						_ = res.Body.Close()
					}
					probe, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+ln.Addr().String()+"/probe", nil)
					response, err := client.Do(probe)
					if err != nil {
						t.Fatal(err)
					}
					_ = response.Body.Close()
					if response.StatusCode != 200 || dials.Load() != 1 {
						t.Fatalf("分帧完成／取消后连接不可复用：status=%d dials=%d", response.StatusCode, dials.Load())
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
