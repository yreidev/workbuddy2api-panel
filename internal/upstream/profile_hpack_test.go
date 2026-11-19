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
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestTTNetOfficialHPACKBytes(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		count   int
		sizes   map[int]uint32
	}{
		{"trae-code-http2.bin", 2, nil},
		{"trae-code-http2-dynamic.bin", 8, map[int]uint32{0: 64, 2: 0, 4: 4096}},
		{"trae-code-http2-oversized.bin", 8, map[int]uint32{0: 64, 2: 0, 4: 4096}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			testProfileOfficialHPACKBytes(t, ProfileTraeCode, tc.fixture, tc.count, tc.sizes, nil)
		})
	}
}

func TestChromiumOfficialHPACKBytes(t *testing.T) {
	for _, tc := range []struct {
		profile ClientProfile
		fixture string
		sizes   map[int]uint32
	}{
		{ProfileTraeWork, "trae-work-http2-dynamic.bin", nil},
		{ProfileTraeAuth, "trae-auth-http2-dynamic.bin", nil},
		{ProfileTraeWork, "trae-work-http2-settings.bin", map[int]uint32{0: 64, 2: 0, 4: 4096}},
		{ProfileTraeAuth, "trae-auth-http2-settings.bin", map[int]uint32{0: 64, 2: 0, 4: 4096}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			testProfileOfficialHPACKBytes(t, tc.profile, tc.fixture, 8, tc.sizes, nil)
		})
	}
}

func TestProfileOfficialHPACKInitialTable(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		name := map[ClientProfile]string{ProfileTraeWork: "work", ProfileTraeAuth: "auth", ProfileTraeCode: "code"}[profile]
		for _, tc := range []struct {
			name    string
			initial uint32
			sizes   map[int]uint32
		}{
			{"zero", 0, nil},
			{"small", 64, nil},
			{"large", 8192, nil},
			{"megabyte", 1 << 20, nil},
			{"resize", 8192, map[int]uint32{0: 64, 2: 16384, 4: 0, 6: 8192}},
		} {
			fixture := "trae-" + name + "-http2-initial-" + tc.name + ".bin"
			t.Run(fixture, func(t *testing.T) {
				testProfileOfficialHPACKBytes(t, profile, fixture, 8, tc.sizes, &tc.initial)
			})
		}
	}
}

func testProfileOfficialHPACKBytes(t *testing.T, profile ClientProfile, fixture string, count int, sizes map[int]uint32, initial *uint32) {
	t.Helper()
	raw, err := os.ReadFile("clienthello/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	fr := http2.NewFramer(io.Discard, bytes.NewReader(raw[len(http2.ClientPreface):]))
	var blocks [][]byte
	var fields [][]hpack.HeaderField
	dec := hpack.NewDecoder(4096, nil)
	dec.SetAllowedMaxDynamicTableSize(1 << 20)
	for {
		f, e := fr.ReadFrame()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if h, ok := f.(*http2.HeadersFrame); ok {
			block := append([]byte(nil), h.HeaderBlockFragment()...)
			blocks = append(blocks, block)
			v, e := dec.DecodeFull(block)
			if e != nil {
				t.Fatal(e)
			}
			fields = append(fields, v)
		}
	}
	if len(blocks) != count {
		t.Fatalf("官方样本请求数 = %d，期望 %d", len(blocks), count)
	}
	cs := httptest.NewTLSServer(nil)
	certs := cs.TLS.Certificates
	cs.Close()
	ln, e := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: certs, NextProtos: []string{"h2"}})
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		c, e := ln.Accept()
		if e != nil {
			done <- e
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.CopyN(io.Discard, c, int64(len(http2.ClientPreface))); err != nil {
			done <- err
			return
		}
		fr := http2.NewFramer(c, c)
		var settings []http2.Setting
		if initial != nil {
			settings = append(settings, http2.Setting{ID: http2.SettingHeaderTableSize, Val: *initial})
		}
		if err := fr.WriteSettings(settings...); err != nil {
			done <- err
			return
		}
		i := 0
		pending := 1
		var waiting uint32
		changed := false
		for i < len(blocks) {
			f, e := fr.ReadFrame()
			if e != nil {
				done <- e
				return
			}
			switch f := f.(type) {
			case *http2.SettingsFrame:
				if f.IsAck() {
					pending--
				} else {
					if err := fr.WriteSettingsAck(); err != nil {
						done <- err
						return
					}
				}
			case *http2.HeadersFrame:
				b := f.HeaderBlockFragment()
				// 初始 SETTINGS 与在途请求的先后不固定，前两次仅建立两端表状态。
				// 从第三次起比较完整字节，包含之后所有表大小更新。
				if (initial == nil || i >= 2) && !bytes.Equal(b, blocks[i]) {
					done <- fmt.Errorf("第 %d 请求 HPACK 偏离官方样本：native=%x gateway=%x", i+1, blocks[i], b)
					return
				}
			case *http2.DataFrame:
				if f.StreamEnded() {
					waiting = f.StreamID
				}
			}
			if waiting != 0 && pending == 0 {
				if size, ok := sizes[i]; ok && !changed {
					if err := fr.WriteSettings(http2.Setting{ID: http2.SettingHeaderTableSize, Val: size}); err != nil {
						done <- err
						return
					}
					pending++
					changed = true
				} else {
					if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: waiting, EndStream: true, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
						done <- err
						return
					}
					waiting = 0
					changed = false
					i++
				}
			}
		}
		done <- nil
	}()
	base := newTransport(nil)
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return newDialer().DialContext(ctx, network, ln.Addr().String())
	}
	tr := newProfileHTTP2(base, profile)
	defer tr.CloseIdleConnections()
	for _, ff := range fields {
		var method, scheme, authority, path string
		for _, f := range ff {
			switch f.Name {
			case ":method":
				method = f.Value
			case ":scheme":
				scheme = f.Value
			case ":authority":
				authority = f.Value
			case ":path":
				path = f.Value
			}
		}
		r, err := http.NewRequest(method, scheme+"://"+authority+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range ff {
			if f.Name[0] != ':' {
				r.Header.Set(f.Name, f.Value)
			}
		}
		var rt http.RoundTripper = tr
		if profile == ProfileTraeWork {
			// 回环页面只发简单 fetch，应用头与线上 Work 页面不同。
			// 同值、同序重放仅比较编码器；业务头序仍按线上 Work 样本设置。
			var order []string
			for _, f := range ff {
				if f.Name[0] != ':' {
					order = append(order, f.Name)
				}
			}
			r.Header[req.HeaderOderKey] = order
			r.Header[req.PseudoHeaderOderKey] = []string{":method", ":authority", ":scheme", ":path"}
			r = r.WithContext(context.WithValue(r.Context(), profileSessionRequestKey{}, profileSessionRequest{
				origin: authority,
				ctx:    r.Context(),
			}))
			rt = tr.Transport
		}
		res, e := rt.RoundTrip(r)
		if e != nil {
			select {
			case wireErr := <-done:
				t.Fatalf("请求：%v；字节校验：%v", e, wireErr)
			default:
				t.Fatal(e)
			}
		}
		_ = res.Body.Close()
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
