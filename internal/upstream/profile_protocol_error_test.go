// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestProfileHTTP2ProtocolErrorsMatchOfficial(t *testing.T) {
	testProfileHTTP2OfficialErrors(t, "clienthello/trae-http2-protocol-errors.json", 28)
}

func TestProfileHTTP2HPACKErrorsMatchOfficial(t *testing.T) {
	testProfileHTTP2OfficialErrors(t, "clienthello/trae-http2-hpack-errors.json", 30)
}

func TestProfileHTTP2HPACKFragmentsMatchOfficial(t *testing.T) {
	testProfileHTTP2OfficialErrors(t, "clienthello/trae-http2-hpack-fragments.json", 10)
}

func testProfileHTTP2OfficialErrors(t *testing.T, fixturePath string, caseCount int) {
	t.Helper()
	var fixture struct {
		Cases []struct {
			Name      string `json:"name"`
			InputHex  string `json:"input_hex"`
			Healthy   bool   `json:"healthy"`
			EmptyBody bool   `json:"empty_body"`
			Partial   bool   `json:"partial"`
			TailHex   string `json:"tail_hex"`
			GoAway    *struct {
				Last  uint32 `json:"last"`
				Code  uint32 `json:"code"`
				Debug string `json:"debug"`
			} `json:"goaway"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) != caseCount {
		t.Fatalf("官方异常矩阵不完整：%d", len(fixture.Cases))
	}
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		for _, tc := range fixture.Cases {
			t.Run(httpProfileName(profile, false)+"/"+tc.Name, func(t *testing.T) {
				input, err := hex.DecodeString(tc.InputHex)
				if err != nil {
					t.Fatal(err)
				}
				if tc.Name == "control-oversize" || tc.Name == "data-oversize" {
					// 官方在帧头处拒绝，不应等对端补齐非法的大正文。
					input = input[:9]
				}
				var batch bytes.Buffer
				_, _ = batch.Write(input)
				if !tc.Partial || tc.Healthy {
					if tc.TailHex != "" {
						tail, err := hex.DecodeString(tc.TailHex)
						if err != nil {
							t.Fatal(err)
						}
						_, _ = batch.Write(tail)
					}
					if err := http2.NewFramer(&batch, nil).WritePing(false, [8]byte([]byte("healthy!"))); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				ln := profileDynamicListener(t)
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
						if _, err := io.CopyN(io.Discard, conn, int64(len(http2.ClientPreface))); err != nil {
							return err
						}
						fr := http2.NewFramer(conn, conn)
						if err := fr.WriteSettings(); err != nil {
							return err
						}
						var injected, gotGoAway bool
						for {
							frame, err := fr.ReadFrame()
							if err != nil {
								if errors.Is(err, io.EOF) && gotGoAway {
									return nil
								}
								return fmt.Errorf("读取客户端异常处理结果：%w", err)
							}
							switch f := frame.(type) {
							case *http2.SettingsFrame:
								if !f.IsAck() {
									if err := fr.WriteSettingsAck(); err != nil {
										return err
									}
								}
							case *http2.HeadersFrame:
								if injected {
									return errors.New("异常响应被自动重发")
								}
								if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
									return err
								}
								if err := fr.WriteData(f.StreamID, false, bytes.Repeat([]byte{'p'}, 8192)); err != nil {
									return err
								}
								select {
								case <-ready:
								case <-ctx.Done():
									return ctx.Err()
								}
								if _, err := conn.Write(batch.Bytes()); err != nil {
									return err
								}
								injected = true
							case *http2.GoAwayFrame:
								if tc.GoAway == nil || gotGoAway || f.LastStreamID != tc.GoAway.Last || uint32(f.ErrCode) != tc.GoAway.Code || string(f.DebugData()) != tc.GoAway.Debug {
									return fmt.Errorf("GOAWAY 与官方不同：last=%d code=%v debug=%q", f.LastStreamID, f.ErrCode, f.DebugData())
								}
								gotGoAway = true
							case *http2.PingFrame:
								if !f.IsAck() || f.Data != [8]byte([]byte("healthy!")) || !tc.Healthy || gotGoAway {
									return fmt.Errorf("非法帧后错误地继续会话：%v", f)
								}
								if tc.EmptyBody {
									return nil
								}
								return fr.WriteData(1, true, []byte("tail"))
							case *http2.RSTStreamFrame:
								return fmt.Errorf("连接错误被改为单流重置：%v", f)
							}
						}
					}()
				}()
				base := newTransport(nil)
				base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				tr := newProfileHTTP2(base, profile)
				defer tr.CloseIdleConnections()
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+ln.Addr().String()+"/fixture", nil)
				res, err := (&http.Client{Transport: tr}).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				warm := make([]byte, 8192)
				if _, err := io.ReadFull(res.Body, warm); err != nil {
					t.Fatal(err)
				}
				close(ready)
				body, readErr := io.ReadAll(res.Body)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if tc.Healthy {
					expected := "tail"
					if tc.EmptyBody {
						expected = ""
					}
					if readErr != nil || string(body) != expected {
						t.Fatalf("应忽略的设置损坏后续正文：%q %v", body, readErr)
					}
				} else if readErr == nil || len(body) != 0 {
					t.Fatalf("连接错误被当作成功或交付非法正文：body=%d err=%v", len(body), readErr)
				}
			})
		}
	}
}
