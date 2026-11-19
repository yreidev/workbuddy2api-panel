// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// 只记录已成功写入 socket 的 TLS 记录，避免把 TCP 分段当成 TLS 分段。
type profileRecordCapture struct {
	net.Conn
	enabled bool
	pending []byte
	lengths []int
}

func (c *profileRecordCapture) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if c.enabled {
		c.pending = append(c.pending, p[:n]...)
		for len(c.pending) >= 5 {
			size := int(binary.BigEndian.Uint16(c.pending[3:5]))
			if len(c.pending) < 5+size {
				break
			}
			if c.pending[0] == 23 {
				c.lengths = append(c.lengths, size)
			}
			c.pending = c.pending[5+size:]
		}
	}
	return n, err
}

func TestClientProfileTLSRecordSizing(t *testing.T) {
	certServer := httptest.NewTLSServer(nil)
	certs := certServer.TLS.Certificates
	certServer.Close()
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		for _, profile := range []ClientProfile{ProfileWorkBuddy, ProfileQoder, ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
			t.Run(fmt.Sprintf("%s/%x", httpProfileName(profile, false), version), func(t *testing.T) {
				ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
					Certificates: certs, MinVersion: version, MaxVersion: version,
					CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer ln.Close()
				payload := bytes.Repeat([]byte{'x'}, 98305)
				done := make(chan error, 1)
				go func() {
					c, err := ln.Accept()
					if err != nil {
						done <- err
						return
					}
					defer c.Close()
					_ = c.SetDeadline(time.Now().Add(5 * time.Second))
					got := make([]byte, len(payload))
					_, err = io.ReadFull(c, got)
					if err == nil && !bytes.Equal(got, payload) {
						err = fmt.Errorf("TLS 接收的正文损坏")
					}
					done <- err
				}()
				raw, err := net.Dial("tcp", ln.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
				tap := &profileRecordCapture{Conn: raw}
				conn, err := handshakeTLSProfile(context.Background(), tap, "fixture.invalid", &tls.Config{InsecureSkipVerify: true}, profile)
				if err != nil {
					t.Fatal(err)
				}
				tap.enabled = true
				offset := 0
				for _, size := range []int{32768, 1, 65536} {
					if n, err := conn.Write(payload[offset : offset+size]); err != nil || n != size {
						t.Fatalf("写入 %d 字节正文：n=%d err=%v", size, n, err)
					}
					offset += size
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				// 官方运行时不采用 Go 的递增记录大小；每次 Write
				// 从 16 KiB 上限切分，短写后也保持上限，尾部不填充。
				overhead := 17 // TLS 1.3：AEAD tag 与加密的 content type。
				if version == tls.VersionTLS12 {
					overhead = 24 // AES-GCM：显式 nonce 与 AEAD tag。
				}
				want := []int{16384, 16384, 1, 16384, 16384, 16384, 16384}
				for i := range want {
					want[i] += overhead
				}
				if len(tap.pending) != 0 || !reflect.DeepEqual(tap.lengths, want) {
					t.Fatalf("TLS 记录长度 = %v，待解析 %d 字节，官方策略要求 %v", tap.lengths, len(tap.pending), want)
				}
			})
		}
	}
}
