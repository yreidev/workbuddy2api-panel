// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestProfileSessionResumptionAndRouteIsolation(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, r.TLS.DidResume)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	base := newTransport(nil)
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	routes := newRouteTable(base)
	defer (routedTransport{routes}).CloseIdleConnections()
	for _, profile := range []ClientProfile{ProfileTraeCode, ProfileTraeWork, ProfileTraeAuth, ProfileQoder} {
		t.Run(httpProfileName(profile, false), func(t *testing.T) {
			for i, ip := range []string{"", "", "127.0.0.1"} {
				tr, err := routes.transportForProfile("", ip, profile)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				r, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
				resp, err := tr.RoundTrip(r)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				cancel()
				want := fmt.Sprint(i == 1 && profile != ProfileQoder)
				if err != nil || string(body) != want {
					t.Fatalf("第 %d 次握手 resume=%s，期望 %s：%v", i+1, body, want, err)
				}
				roundTripCloseIdle(tr)
			}
		})
	}
}

func TestNativeHTTP1HeaderWire(t *testing.T) {
	certServer := httptest.NewTLSServer(nil)
	certs := certServer.TLS.Certificates
	certServer.Close()
	for _, tc := range []struct {
		profile ClientProfile
		stream  bool
	}{{ProfileQoder, false}, {ProfileQoder, true}, {ProfileTraeCode, true}} {
		profile, stream := tc.profile, tc.stream
		t.Run(fmt.Sprint(httpProfileName(profile, false), "/stream=", stream), func(t *testing.T) {
			ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: certs, NextProtos: []string{"http/1.1"}})
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			done := make(chan error, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				reader := bufio.NewReader(conn)
				line, err := reader.ReadString('\n')
				if err != nil || !strings.HasSuffix(line, " HTTP/1.1\r\n") {
					done <- fmt.Errorf("不是 HTTP/1.1 请求：%q %v", line, err)
					return
				}
				var names []string
				for {
					line, err = reader.ReadString('\n')
					if err != nil {
						done <- err
						return
					}
					if line == "\r\n" {
						break
					}
					name, _, _ := strings.Cut(line, ":")
					names = append(names, name)
				}
				want := []string{"Accept", "Accept-Encoding", "Authorization", "Content-Type", "Cosy-ClientType", "Cosy-MachineId", "Cosy-MachineOS", "Connection", "User-Agent", "Host"}
				if stream {
					want = []string{"Accept", "Authorization", "Cache-Control", "Connection", "Content-Type", "Cosy-ClientType", "Cosy-MachineId", "Cosy-MachineOS", "User-Agent", "Host", "Accept-Encoding", "Content-Length"}
				}
				if profile == ProfileTraeCode {
					want = []string{"Host", "Connection", "Transfer-Encoding", "accept", "authorization", "content-type", "x-ide-version", "x-machine-id", "x-request-id", "x-tt-trace-id", "User-Agent", "Accept-Encoding"}
					body := make([]byte, len("2\r\n{}\r\n0\r\n\r\n"))
					if _, err := io.ReadFull(reader, body); err != nil || string(body) != "2\r\n{}\r\n0\r\n\r\n" {
						done <- fmt.Errorf("TTNet h1 chunked 正文偏离样本：%q %v", body, err)
						return
					}
				}
				if !reflect.DeepEqual(names, want) {
					done <- fmt.Errorf("头序或大小写偏离原生样本：%v，期望 %v", names, want)
					return
				}
				_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
				done <- err
			}()
			base := newTransport(nil)
			base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			tr := newProfileHTTP2(base, profile)
			defer tr.CloseIdleConnections()
			method, path := "GET", "/algo/api/v2/model/list?Encode=1"
			var body io.Reader
			if stream {
				method, path, body = "POST", "/algo/api/v2/service/pro/sse/agent_chat_generation", strings.NewReader("{}")
			}
			r, _ := http.NewRequest(method, "https://"+ln.Addr().String()+path, body)
			r.Header = http.Header{"Accept": {"application/json"}, "Accept-Encoding": {"identity"}, "Authorization": {"Bearer fixture"}, "Content-Type": {"application/json"}, "Cosy-Clienttype": {"5"}, "Cosy-Machineid": {"fixture-device"}, "Cosy-Machineos": {"x86_64_linux"}, "User-Agent": {"Bun/1.4.4"}}
			if stream {
				r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
				r.Header.Set("Cache-Control", "no-cache")
				r.Header.Set("Connection", "keep-alive")
			}
			if profile == ProfileTraeCode {
				r.Header = http.Header{"Accept": {"application/json"}, "Authorization": {"Cloud-IDE-JWT fixture"}, "Content-Type": {"application/json"}, "X-Ide-Version": {"3.4.24"}, "X-Machine-Id": {"fixture"}, "User-Agent": {"TraeClient/TTNet"}, "Accept-Encoding": {"gzip, deflate"}}
			}
			resp, requestErr := tr.RoundTrip(r)
			if wireErr := <-done; wireErr != nil {
				t.Fatal(wireErr)
			}
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			_ = resp.Body.Close()
			if profile == ProfileQoder && r.Header.Get("Cosy-Machineid") != "fixture-device" {
				t.Fatal("传输层修改了调用方的 Header")
			}
		})
	}
}

func TestTTNetRequestHeadersAndDataFrames(t *testing.T) {
	fixture, err := os.ReadFile("clienthello/trae-code-http2.bin")
	if err != nil {
		t.Fatal(err)
	}
	reference := http2.NewFramer(io.Discard, bytes.NewReader(fixture[len(http2.ClientPreface):]))
	reference.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	var wantNames []string
	for len(wantNames) == 0 {
		frame, err := reference.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if headers, ok := frame.(*http2.MetaHeadersFrame); ok {
			for _, field := range headers.Fields {
				wantNames = append(wantNames, field.Name)
			}
		}
	}
	certServer := httptest.NewTLSServer(nil)
	certs := certServer.TLS.Certificates
	certServer.Close()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: certs, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err = io.CopyN(io.Discard, conn, int64(len(http2.ClientPreface))); err != nil {
			done <- err
			return
		}
		fr := http2.NewFramer(conn, conn)
		fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
		_ = fr.WriteSettings()
		var previousTrace string
		for completed := 0; completed < 2; {
			frame, err := fr.ReadFrame()
			if err != nil {
				done <- err
				return
			}
			switch f := frame.(type) {
			case *http2.SettingsFrame:
				if !f.IsAck() {
					_ = fr.WriteSettingsAck()
				}
			case *http2.MetaHeadersFrame:
				var names []string
				for _, field := range f.Fields {
					names = append(names, field.Name)
					if field.Name == "x-request-id" && !regexp.MustCompile(`^[a-zA-Z0-9]{10}$`).MatchString(field.Value) {
						done <- fmt.Errorf("请求 ID 形状错误")
						return
					}
					if field.Name == "x-tt-trace-id" {
						if !regexp.MustCompile(`^00-[0-9a-f]{8}010[0-9a-f]{17}ffff-[0-9a-f]{16}-01$`).MatchString(field.Value) || strings.Split(field.Value, "-")[2] != field.Value[3:19] || field.Value == previousTrace {
							done <- fmt.Errorf("原生 trace 形状错误或复用了上次请求的 trace")
							return
						}
						previousTrace = field.Value
					}
				}
				if !reflect.DeepEqual(names, wantNames) || f.StreamID != uint32(completed*2+1) {
					done <- fmt.Errorf("TTNet 头序或流号错误：%v stream=%d", names, f.StreamID)
					return
				}
			case *http2.DataFrame:
				if len(f.Data()) > 0 {
					if string(f.Data()) != "{}" || f.StreamEnded() {
						done <- fmt.Errorf("TTNet 正文应随后单独发送空 END_STREAM")
						return
					}
				} else if f.StreamEnded() {
					completed++
					_ = fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndHeaders: true, EndStream: true, BlockFragment: []byte{0x88}})
				}
			}
		}
		done <- nil
	}()
	base := newTransport(nil)
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	tr := newProfileHTTP2(base, ProfileTraeCode)
	defer tr.CloseIdleConnections()
	for range 2 {
		r, _ := http.NewRequest("POST", "https://"+ln.Addr().(*net.TCPAddr).String()+"/api/ide/v1/get_detail_param", strings.NewReader("{}"))
		r.Header = http.Header{"Accept": {"application/json"}, "Authorization": {"Cloud-IDE-JWT fixture"}, "Content-Type": {"application/json"}, "X-Ide-Version": {"3.4.24"}, "X-Ide-Version-Code": {"20260212"}, "X-Machine-Id": {"fixture"}, "User-Agent": {"TraeClient/TTNet"}, "Accept-Encoding": {"gzip, deflate"}}
		resp, err := tr.RoundTrip(r)
		if err != nil {
			select {
			case wireErr := <-done:
				t.Fatalf("%v；服务端：%v", err, wireErr)
			default:
				t.Fatal(err)
			}
		}
		_ = resp.Body.Close()
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProfileCertificateVerification(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	for _, profile := range []ClientProfile{ProfileQoder, ProfileTraeCode, ProfileTraeWork, ProfileTraeAuth} {
		for _, tc := range []struct {
			name string
			cfg  *tls.Config
			ok   bool
		}{{"trusted", &tls.Config{RootCAs: roots}, true}, {"untrusted", nil, false}, {"wrong-name", &tls.Config{RootCAs: roots, ServerName: "fixture.invalid"}, false}} {
			t.Run(httpProfileName(profile, false)+"/"+tc.name, func(t *testing.T) {
				base := newTransport(nil)
				base.TLSClientConfig = tc.cfg
				tr := newProfileHTTP2(base, profile)
				defer tr.CloseIdleConnections()
				r, _ := http.NewRequest("GET", srv.URL, nil)
				resp, err := tr.RoundTrip(r)
				if resp != nil {
					_ = resp.Body.Close()
				}
				if (err == nil) != tc.ok {
					t.Fatalf("证书校验结果错误：%v", err)
				}
			})
		}
	}
}
