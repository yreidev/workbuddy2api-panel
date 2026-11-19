// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

type profileHeaderResult struct {
	Failed     bool     `json:"failed"`
	Status     int      `json:"status"`
	BodyLength int      `json:"body_length"`
	Order      []string `json:"order"`
	Resets     []struct {
		Stream uint32 `json:"stream"`
		Code   uint32 `json:"code"`
	} `json:"resets"`
	GoAway []struct {
		Last  uint32 `json:"last"`
		Code  uint32 `json:"code"`
		Debug string `json:"debug"`
	} `json:"goaway"`
}

func TestProfileHTTP2HeaderSemanticsMatchOfficial(t *testing.T) {
	testProfileHTTP2HeaderSemantics(t, "clienthello/trae-http2-header-semantics.json", 47)
}

func TestProfileHTTP2ResponseLifecycleMatchOfficial(t *testing.T) {
	testProfileHTTP2HeaderSemantics(t, "clienthello/trae-http2-response-lifecycle.json", 37)
}

func testProfileHTTP2HeaderSemantics(t *testing.T, fixturePath string, caseCount int) {
	t.Helper()
	var fixture struct {
		Cases []struct {
			profileHeaderResult
			Name      string                         `json:"name"`
			Stage     string                         `json:"stage"`
			InputHash string                         `json:"input_sha256"`
			Profiles  map[string]profileHeaderResult `json:"profiles"`
			Parts     []struct {
				Hex    string `json:"hex"`
				Repeat int    `json:"repeat"`
			} `json:"input_parts"`
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
		t.Fatalf("官方头部语义矩阵不完整：%d", len(fixture.Cases))
	}
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		for _, tc := range fixture.Cases {
			if tc.Profiles != nil {
				tc.profileHeaderResult = tc.Profiles[map[ClientProfile]string{ProfileTraeWork: "work", ProfileTraeAuth: "auth", ProfileTraeCode: "ttnet"}[profile]]
			}
			t.Run(httpProfileName(profile, false)+"/"+tc.Name, func(t *testing.T) {
				var input bytes.Buffer
				for _, part := range tc.Parts {
					p, err := hex.DecodeString(part.Hex)
					if err != nil {
						t.Fatal(err)
					}
					input.Write(bytes.Repeat(p, part.Repeat))
				}
				if got := fmt.Sprintf("%x", sha256.Sum256(input.Bytes())); got != tc.InputHash {
					t.Fatalf("回归输入与官方原始样本摘要不同：%s", got)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				ln := profileDynamicListener(t)
				ready, bodyDone := make(chan struct{}), make(chan struct{})
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
						injected, drained, reused := false, false, false
						resets, goaways := 0, 0
						var order []string
						for {
							frame, err := fr.ReadFrame()
							if err != nil {
								if errors.Is(err, io.EOF) && goaways == len(tc.GoAway) && goaways == 1 && resets == len(tc.Resets) && slices.Equal(order, tc.Order) {
									return nil
								}
								return fmt.Errorf("读取头部校验结果：%w", err)
							}
							if settings, ok := frame.(*http2.SettingsFrame); ok && !settings.IsAck() {
								if err := fr.WriteSettingsAck(); err != nil {
									return err
								}
							}
							switch f := frame.(type) {
							case *http2.HeadersFrame:
								if injected {
									if f.StreamID != 3 || reused || len(tc.GoAway) != 0 {
										return fmt.Errorf("异常重发或未沿用原连接的新流：%d", f.StreamID)
									}
									reused = true
									if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: 3, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
										return err
									}
									if err := fr.WriteData(3, true, []byte("probe")); err != nil {
										return err
									}
									break
								}
								if f.StreamID != 1 {
									return fmt.Errorf("首请求流编号不同：%d", f.StreamID)
								}
								if tc.Stage == "trailer" {
									if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
										return err
									}
									if err := fr.WriteData(1, false, bytes.Repeat([]byte{'p'}, 8192)); err != nil {
										return err
									}
									select {
									case <-ready:
									case <-ctx.Done():
										return ctx.Err()
									}
								}
								injected = true
								// 与官方采样一致：输入和 PING 在同一次 TLS 写入，
								// 避免额外记录及两次 Write 的调度改变读批次。
								var batch bytes.Buffer
								batch.Write(input.Bytes())
								if err := http2.NewFramer(&batch, nil).WritePing(false, [8]byte([]byte("healthy!"))); err != nil {
									return err
								}
								if _, err := conn.Write(batch.Bytes()); err != nil {
									return err
								}
							case *http2.RSTStreamFrame:
								order = append(order, "reset")
								if resets >= len(tc.Resets) || f.StreamID != tc.Resets[resets].Stream || uint32(f.ErrCode) != tc.Resets[resets].Code {
									return fmt.Errorf("RST 与官方不同：%v", f)
								}
								resets++
							case *http2.GoAwayFrame:
								order = append(order, "goaway")
								if goaways >= len(tc.GoAway) || f.LastStreamID != tc.GoAway[goaways].Last || uint32(f.ErrCode) != tc.GoAway[goaways].Code || string(f.DebugData()) != tc.GoAway[goaways].Debug {
									return fmt.Errorf("GOAWAY 与官方不同：last=%d code=%v debug=%q", f.LastStreamID, f.ErrCode, f.DebugData())
								}
								goaways++
							case *http2.PingFrame:
								if f.IsAck() && f.Data == [8]byte([]byte("healthy!")) {
									order = append(order, "healthy!")
									select {
									case <-bodyDone:
									case <-ctx.Done():
										return ctx.Err()
									}
									if err := fr.WritePing(false, [8]byte([]byte("drained!"))); err != nil {
										return err
									}
								} else if f.IsAck() && f.Data == [8]byte([]byte("drained!")) {
									order = append(order, "drained!")
									drained = true
								}
							}
							if drained && reused {
								if !slices.Equal(order, tc.Order) {
									return fmt.Errorf("RST／GOAWAY 与探测 PING 的顺序不同：%v，期望 %v", order, tc.Order)
								}
								if resets != len(tc.Resets) || goaways != len(tc.GoAway) {
									return fmt.Errorf("错误帧数量与官方不同：reset=%d goaway=%d", resets, goaways)
								}
								return nil
							}
						}
					}()
				}()
				base := newTransport(nil)
				base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				var dials atomic.Int32
				dial := base.DialContext
				base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
					dials.Add(1)
					return dial(ctx, network, address)
				}
				tr := newProfileHTTP2(base, profile)
				defer tr.CloseIdleConnections()
				client := &http.Client{Transport: tr}
				get := func() (*http.Response, error) {
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+ln.Addr().String()+"/fixture", nil)
					if err != nil {
						return nil, err
					}
					return client.Do(req)
				}
				res, readErr := get()
				var body []byte
				status := 0
				if readErr == nil {
					status = res.StatusCode
					if tc.Stage == "trailer" {
						if _, err := io.CopyN(io.Discard, res.Body, 8192); err != nil {
							t.Fatal(err)
						}
						close(ready)
					}
					body, readErr = io.ReadAll(res.Body)
					_ = res.Body.Close()
				}
				close(bodyDone)
				if (readErr != nil) != tc.Failed || status != tc.Status || len(body) != tc.BodyLength {
					t.Fatalf("响应与官方不同：status=%d body=%d err=%v；期望 status=%d body=%d failed=%t", status, len(body), readErr, tc.Status, tc.BodyLength, tc.Failed)
				}
				if len(tc.GoAway) == 0 {
					probe, err := get()
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(probe.Body)
					_ = probe.Body.Close()
					if err != nil || probe.StatusCode != 200 || string(data) != "probe" {
						t.Fatalf("流校验后连接不能继续处理请求：body=%q err=%v", data, err)
					}
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if dials.Load() != 1 {
					t.Fatalf("头部校验或后续探测另建了 %d 条连接", dials.Load())
				}
			})
		}
	}
}
