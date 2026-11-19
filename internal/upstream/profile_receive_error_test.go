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
	"sync/atomic"
	"testing"
	"time"

	reqhttp2 "github.com/imroc/req/v3/http2"
	"golang.org/x/net/http2"
)

// 回归中明确缩小声明的流窗口，验证错误范围与连接复用；官方独立取样
// 使用原本的 6 MiB 窗口，不用这个受控设置作为官方传输参数。
func TestProfileHTTP2ReceiveWindowOverflowKeepsOtherStream(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		t.Run(httpProfileName(profile, false), func(t *testing.T) {
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
					if err := fr.WriteSettings(); err != nil {
						return err
					}
					var bad, survivor uint32
					var reset bool
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
						case *http2.GoAwayFrame:
							return fmt.Errorf("单流超窗导致连接 GOAWAY：%v", f.ErrCode)
						case *http2.HeadersFrame:
							if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
								return err
							}
							switch {
							case bad == 0:
								bad = f.StreamID
								if err := fr.WriteData(bad, false, []byte("warmwarm")); err != nil {
									return err
								}
							case survivor == 0:
								survivor = f.StreamID
								if err := fr.WriteData(bad, false, make([]byte, 32)); err != nil {
									return err
								}
							case reset:
								if err := fr.WriteData(f.StreamID, true, []byte("probe")); err != nil {
									return err
								}
								<-ctx.Done()
								return nil
							default:
								return errors.New("后续请求早于异常流关闭")
							}
						case *http2.RSTStreamFrame:
							if f.StreamID != bad || f.ErrCode != http2.ErrCodeFlowControl || reset {
								return fmt.Errorf("重置范围或错误码不同：%v", f)
							}
							reset = true
							if err := fr.WriteData(bad, true, []byte("late")); err != nil {
								return err
							}
							if err := fr.WriteData(survivor, true, []byte("alive")); err != nil {
								return err
							}
						}
					}
				}()
			}()
			base := newTransport(nil)
			base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			original := base.DialContext
			var dials atomic.Int32
			base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				dials.Add(1)
				return original(ctx, network, addr)
			}
			tr := newProfileHTTP2(base, profile)
			tr.SetHTTP2SettingsFrame(reqhttp2.Setting{ID: reqhttp2.SettingEnablePush, Val: 0}, reqhttp2.Setting{ID: reqhttp2.SettingInitialWindowSize, Val: 32})
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr}
			get := func(path string) (*http.Response, error) {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+ln.Addr().String()+path, nil)
				return client.Do(req)
			}
			bad, err := get("/bad")
			if err != nil {
				t.Fatal(err)
			}
			defer bad.Body.Close()
			warm := make([]byte, 8)
			if _, err := io.ReadFull(bad.Body, warm); err != nil || string(warm) != "warmwarm" {
				t.Fatalf("预热响应不完整：%v", err)
			}
			survivor := make(chan error, 1)
			go func() {
				res, err := get("/survivor")
				if err == nil {
					defer res.Body.Close()
					var body []byte
					body, err = io.ReadAll(res.Body)
					if err == nil && (res.StatusCode != 200 || string(body) != "alive") {
						err = fmt.Errorf("另一流响应损坏：%d %q", res.StatusCode, body)
					}
				}
				survivor <- err
			}()
			if body, err := io.ReadAll(bad.Body); err == nil || len(body) != 0 {
				t.Fatalf("违规正文被交付或错误丢失：body=%d err=%v", len(body), err)
			}
			_ = bad.Body.Close()
			if err := <-survivor; err != nil {
				select {
				case serverErr := <-done:
					t.Fatalf("另一流失败：%v；采样服务：%v", err, serverErr)
				case <-ctx.Done():
					t.Fatal(err)
				}
			}
			probe, err := get("/probe")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(probe.Body)
			_ = probe.Body.Close()
			if err != nil || probe.StatusCode != 200 || string(body) != "probe" || dials.Load() != 1 {
				t.Fatalf("异常后连接复用失败：status=%d body=%q dials=%d err=%v", probe.StatusCode, body, dials.Load(), err)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
