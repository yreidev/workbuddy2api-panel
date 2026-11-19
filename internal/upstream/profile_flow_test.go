// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestProfileHTTP2ConcurrentStreamsShareConnection(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		for _, strict := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/strict=%t", httpProfileName(profile, false), strict), func(t *testing.T) {
				var connections, active, peak atomic.Int32
				srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := active.Add(1)
					defer active.Add(-1)
					for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
					}
					if r.URL.Path != "/warmup" {
						select {
						case <-time.After(30 * time.Millisecond):
						case <-r.Context().Done():
							return
						}
					}
					_, _ = io.WriteString(w, "{}")
				}))
				srv.EnableHTTP2 = true
				srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateNew {
						connections.Add(1)
					}
				}
				if err := http2.ConfigureServer(srv.Config, &http2.Server{MaxConcurrentStreams: 2}); err != nil {
					t.Fatal(err)
				}
				srv.StartTLS()
				defer srv.Close()
				base := newTransport(nil)
				base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				tr := newProfileHTTP2(base, profile)
				// false 额外覆盖 req 的 HTTP/2 独立拨号路径，防止仅靠排队掩盖 TLS 接口 panic。
				tr.SetHTTP2StrictMaxConcurrentStreams(strict)
				defer tr.CloseIdleConnections()
				client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
				run := func(path string) error {
					response, err := client.Get(srv.URL + path)
					if err != nil {
						return err
					}
					if response.TLS == nil || response.TLS.NegotiatedProtocol != "h2" {
						_ = response.Body.Close()
						return fmt.Errorf("并发连接未保留 HTTP/2 TLS 状态")
					}
					n, err := io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					if err != nil || n != 2 {
						return fmt.Errorf("并发响应 bytes=%d: %v", n, err)
					}
					return nil
				}
				if err := run("/warmup"); err != nil {
					t.Fatal(err)
				}
				var wg sync.WaitGroup
				failed := make(chan error, 8)
				for i := range 8 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if err := run(fmt.Sprintf("/fixture/%d", i)); err != nil {
							failed <- err
						}
					}()
				}
				wg.Wait()
				close(failed)
				for err := range failed {
					t.Error(err)
				}
				if strict && (connections.Load() != 1 || peak.Load() > 2) {
					t.Fatalf("没有按官方单连接排队：连接=%d，同时流=%d", connections.Load(), peak.Load())
				}
			})
		}
	}
}

func TestProfileHTTP2AdvertisedReceiveWindow(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		t.Run(httpProfileName(profile, false), func(t *testing.T) {
			cert := httptest.NewTLSServer(nil)
			certificates := cert.TLS.Certificates
			cert.Close()
			ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: certificates, NextProtos: []string{"h2"}})
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			written := make(chan error, 1)
			release := make(chan struct{})
			defer close(release)
			go func() {
				var result error
				notify := true
				defer func() {
					if notify {
						written <- result
					}
				}()
				conn, err := ln.Accept()
				if err != nil {
					result = err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := io.CopyN(io.Discard, conn, int64(len(http2.ClientPreface))); err != nil {
					result = err
					return
				}
				fr := http2.NewFramer(conn, conn)
				if err := fr.WriteSettings(); err != nil {
					result = err
					return
				}
				var stream uint32
				for stream == 0 {
					frame, err := fr.ReadFrame()
					if err != nil {
						result = err
						return
					}
					switch f := frame.(type) {
					case *http2.SettingsFrame:
						if !f.IsAck() {
							if err := fr.WriteSettingsAck(); err != nil {
								result = err
								return
							}
						}
					case *http2.HeadersFrame:
						stream = f.StreamID
					}
				}
				if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: stream, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
					result = err
					return
				}
				// 先完整发送 5 MiB，客户端随后才消费；仍在其声明的 6 MiB 窗口内。
				chunk := make([]byte, 16384)
				for sent := 0; sent < 5<<20; sent += len(chunk) {
					if err := fr.WriteData(stream, sent+len(chunk) == 5<<20, chunk); err != nil {
						result = fmt.Errorf("发送到 %d 字节: %w", sent, err)
						return
					}
				}
				notify = false
				written <- nil
				<-release
			}()
			base := newTransport(nil)
			base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			tr := newProfileHTTP2(base, profile)
			defer tr.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+ln.Addr().String()+"/fixture", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := tr.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			select {
			case err := <-written:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("未消费的 5 MiB 响应在声明的接收窗口内受阻")
			}
			n, err := io.Copy(io.Discard, response.Body)
			if err != nil || n != 5<<20 {
				t.Fatalf("声明 6 MiB 窗口却拒绝合法响应: bytes=%d, err=%v", n, err)
			}
		})
	}
}
