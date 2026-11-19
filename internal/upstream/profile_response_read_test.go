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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// 一条 TLS 记录中的小 DATA 在网络读批次内合并。仅锁定明确的记录输入，
// 不把不同 socket 时序的任意应用读取长度当作固定向量。
func TestProfileHTTP2ResponseReadCoalescesSmallFrames(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		t.Run(httpProfileName(profile, false), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cert := httptest.NewTLSServer(nil)
			certificates := cert.TLS.Certificates
			cert.Close()
			ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: certificates, NextProtos: []string{"h2"}, DynamicRecordSizingDisabled: true})
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			ready, first := make(chan struct{}), make(chan int, 1)
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
					if err := fr.WriteData(stream, false, bytes.Repeat([]byte{'p'}, 8192)); err != nil {
						return err
					}
					select {
					case <-ready:
					case <-ctx.Done():
						return ctx.Err()
					}
					var batch bytes.Buffer
					bf := http2.NewFramer(&batch, nil)
					for range 600 {
						if err := bf.WriteData(stream, false, []byte("r")); err != nil {
							return err
						}
					}
					if _, err := conn.Write(batch.Bytes()); err != nil {
						return err
					}
					select {
					case n := <-first:
						if n != 600 {
							return fmt.Errorf("同批小 DATA 没有合并后交付：首读=%d", n)
						}
					case <-ctx.Done():
						return ctx.Err()
					}
					if err := fr.WriteData(stream, true, nil); err != nil {
						return err
					}
					<-ctx.Done()
					return nil
				}()
			}()
			base := newTransport(nil)
			base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			tr := newProfileHTTP2(base, profile)
			defer tr.CloseIdleConnections()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+ln.Addr().String()+"/response", nil)
			res, err := (&http.Client{Transport: tr}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			warm := make([]byte, 8192)
			if _, err := io.ReadFull(res.Body, warm); err != nil || !bytes.Equal(warm, bytes.Repeat([]byte{'p'}, 8192)) {
				t.Fatalf("预热正文错误：%v", err)
			}
			close(ready)
			body := make([]byte, 8192)
			n, err := res.Body.Read(body)
			first <- n
			if err != nil || n != 600 || !bytes.Equal(body[:n], bytes.Repeat([]byte{'r'}, n)) {
				t.Fatalf("同批正文未合并或损坏：n=%d err=%v", n, err)
			}
			if tail, err := io.ReadAll(res.Body); err != nil || len(tail) != 0 {
				t.Fatalf("结束通知错误：tail=%d err=%v", len(tail), err)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
