// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/yreidev/workbuddy2api-panel/internal/providers/cosy"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

func TestOfficialClientIdentity(t *testing.T) {
	c := Credential{AccessToken: "fixture-access", UID: "fixture-user", MachineID: "fixture-machine", DeviceID: "fixture-device"}
	work := traeHeaders(c, true)
	if !strings.Contains(work.Get("User-Agent"), "HeadlessChrome/156.0.0.0") || work.Get("Sec-Ch-Ua-Platform") != `"Linux"` || !strings.Contains(work.Get("Sec-Ch-Ua"), `"Chromium";v="156"`) || work.Get("X-Trae-Client-Type") != "web" || work.Get("X-Machine-Id") != "" {
		t.Fatal("Work 网页身份与官方 Chrome 156 样本不一致")
	}
	code := traeHeaders(c, false)
	if code.Get("User-Agent") != "TraeClient/TTNet Cronet/TTNetVersion:a61b3479 2026-08-04 QuicVersion:40cc763c 2025-07-01" || code.Get("Accept-Encoding") != "gzip, deflate" || code.Get("X-Ide-Version") != "3.4.20" || code.Get("X-Ide-Version-Code") != "20260212" || code.Get("X-Device-Type") != "linux" || code.Get("X-Machine-Id") != c.MachineID || code.Get("X-Device-Id") != c.DeviceID {
		t.Fatal("Code 的 TTNet、版本与持久化设备身份不一致")
	}
	openapi := qoderAPIHeaders("fixture-access")
	if openapi.Get("User-Agent") != "qoder/1.1.96" || openapi.Get("Cosy-Version") != "1.1.96" || openapi.Get("Cosy-Clienttype") != "5" || openapi.Get("Cosy-Machineos") != "x86_64_linux" {
		t.Fatal("Qoder OpenAPI 身份与官方 CLI 1.1.96 对齐值不一致")
	}
	sess := cosy.NewSession(cosy.AuthIdentity{UID: c.UID, SecurityOauthToken: c.AccessToken}, c.MachineID, "fixture-machine-token", "")
	h := cosy.Headers(sess, "/api/v2/model/list", "")
	date, err := strconv.ParseInt(h.Get("Cosy-Date"), 10, 64)
	if err != nil || date < time.Now().Add(-time.Minute).Unix() || date > time.Now().Add(time.Minute).Unix() {
		t.Fatal("COSY 日期不是官方秒级时间戳")
	}
	parts := strings.Split(strings.TrimPrefix(h.Get("Authorization"), "Bearer COSY."), ".")
	if len(parts) != 2 || parts[1] != cosy.SignRequest(parts[0], h.Get("Cosy-Key"), h.Get("Cosy-Date"), "", "/api/v2/model/list") {
		t.Fatal("COSY 请求头与签名未同步")
	}
	var payload map[string]any
	raw, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["cosyVersion"] != "1.1.96" || h.Get("Cosy-Version") != "1.1.96" || h.Get("User-Agent") != "Bun/1.4.4" || h.Get("Cosy-Machineid") != c.MachineID || h.Get("Accept-Language") != "" || h.Get("Sec-Fetch-Mode") != "" {
		t.Fatal("COSY 签名、请求头与设备身份发生混搭")
	}
}

func TestQoderImportedMachineIdentitySurvivesRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	m, _ := testManager(t, path)
	identity := Credential{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", UID: "fixture-user", UserType: "free", ExpiresAt: time.Now().Add(time.Hour).Unix(), MachineID: "fixture-id", MachineToken: "fixture-token", MachineCode: "fixture-code", MachineType: "5", MachineOS: "x86_64_linux", MachineHostname: "fixture-host"}
	id, err := m.Import(context.Background(), AccountInput{Target: "qoder:cn", Credential: identity})
	if err != nil {
		t.Fatal(err)
	}
	m.data.Accounts[0].Credential.ExpiresAt = 1
	if _, err := m.credential(context.Background(), id, "qoder:cn"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.data.Accounts[0].Credential
	identity.AccessToken, identity.RefreshToken, identity.ExpiresAt, identity.DeviceID, identity.ClientID = got.AccessToken, got.RefreshToken, got.ExpiresAt, got.DeviceID, got.ClientID
	if !reflect.DeepEqual(got, identity) {
		t.Fatal("刷新或重启替换了导入的设备身份")
	}
	view, err := json.Marshal(reopened.List())
	if err != nil || strings.Contains(string(view), "fixture-code") || strings.Contains(string(view), "fixture-host") {
		t.Fatal("账号列表泄漏机器身份", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`).MatchString(r.Header.Get("Traceparent")) {
			t.Error("Qoder 推理缺少有效的 traceparent")
		}
		for key, want := range map[string]string{"Cosy-MachineId": identity.MachineID, "Cosy-MachineToken": identity.MachineToken, "Cosy-MachineCode": identity.MachineCode, "Cosy-MachineType": identity.MachineType, "Cosy-MachineOS": identity.MachineOS, "Cosy-MachineHostname": identity.MachineHostname, "X-Model-Key": "fixture-model", "X-Model-Source": "system", "Accept-Encoding": "gzip, deflate, br, zstd"} {
			if r.Header.Get(key) != want {
				t.Errorf("导入身份未发出：%s", key)
			}
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	q := Qoder{HTTP: srv.Client(), Base: srv.URL}
	resp, err := q.signed(context.Background(), "qoder:cn", got, "POST", "/algo/api/v2/service/pro/sse/agent_chat_generation", map[string]any{"model_config": map[string]any{"key": "fixture-model", "source": "system"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestQoderMachineIdentityImportRejectsInvalidHeaders(t *testing.T) {
	m, adapter := testManager(t, "")
	for _, credential := range []Credential{{MachineCode: "invalid\r\nvalue"}, {MachineType: "invalid\x00value"}, {MachineOS: "invalid\nvalue"}, {MachineHostname: "未规范化"}, {MachineHostname: strings.Repeat("a", 97)}} {
		credential.PAT = "fixture-pat"
		if _, err := m.Import(context.Background(), AccountInput{Target: "qoder:cn", Credential: credential}); err == nil {
			t.Fatal("接受了无效的机器身份字段")
		}
	}
	if adapter.refreshed.Load() != 0 || len(m.List()) != 0 {
		t.Fatal("验证设备字段前消费了凭证或保存了账号")
	}
}

func TestTraeWorkCompressedCatalog(t *testing.T) {
	for _, encodings := range [][]string{{"gzip"}, {"gzip, deflate"}, {"gzip", "deflate"}} {
		t.Run(strings.Join(encodings, "/"), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.UserAgent() != traeWebUA || r.Header.Get("Accept-Encoding") != "gzip, deflate, br, zstd" || r.Header.Get("Content-Type") != "" || r.Header.Get("Origin") != "" {
					t.Error("网页目录请求头与采样不一致")
				}
				for _, encoding := range encodings {
					w.Header().Add("Content-Encoding", encoding)
				}
				var outer io.Writer = w
				if strings.Contains(strings.Join(encodings, ","), "deflate") {
					zw := zlib.NewWriter(w)
					defer zw.Close()
					outer = zw
				}
				gz := gzip.NewWriter(outer)
				defer gz.Close()
				catalog := `{"data":{"list":[{"function":"solo_work_remote","models":[{"name":"fixture-model","display_name":"Fixture"}]}]}}`
				if _, err := io.WriteString(gz, catalog); err != nil {
					t.Errorf("写入压缩目录: %v", err)
				}
			}))
			defer srv.Close()
			adapter := Trae{HTTP: srv.Client(), WorkBase: srv.URL}
			models, err := adapter.Models(context.Background(), "traework:cn", Credential{AccessToken: "fixture"})
			if err != nil || len(models) != 1 || models[0].ID != "fixture-model" {
				t.Fatalf("压缩目录未正确解码：%v %v", models, err)
			}
		})
	}
}

func TestTraeGzipResponse(t *testing.T) {
	const plain = "data: 响应正文\n\n"
	var gzipped, zlibBody bytes.Buffer
	for _, writer := range []io.WriteCloser{gzip.NewWriter(&gzipped), zlib.NewWriter(&zlibBody)} {
		if _, err := io.WriteString(writer, plain); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	member := gzipped.Bytes()
	corrupt := bytes.Clone(member)
	corrupt[len(corrupt)-8] ^= 1
	for _, profile := range []struct {
		name  string
		value upstream.ClientProfile
	}{
		{"Work", upstream.ProfileTraeWork},
		{"Auth", upstream.ProfileTraeAuth},
		{"Code", upstream.ProfileTraeCode},
	} {
		for _, tc := range []struct {
			name, coding string
			wire         []byte
			want         string
			err          error
		}{
			{"截断头部", "gzip", member[:3], "", io.ErrUnexpectedEOF},
			{"误标 zlib", "gzip", zlibBody.Bytes(), "", gzip.ErrHeader},
			{"零填充", "x-gzip", append(bytes.Clone(member), make([]byte, 8193)...), plain, nil},
			{"非零尾部", "gzip", append(bytes.Clone(member), 0, 1), plain, gzip.ErrHeader},
			{"两成员", "gzip", append(bytes.Clone(member), member...), plain + plain, nil},
			{"CRC 错误", "gzip", corrupt, plain, gzip.ErrChecksum},
			{"缺失尾部", "gzip", member[:len(member)-4], plain, io.ErrUnexpectedEOF},
		} {
			t.Run(profile.name+"/"+tc.name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Encoding", tc.coding)
					if _, err := w.Write(tc.wire); err != nil {
						t.Errorf("发送压缩正文: %v", err)
					}
				}))
				defer srv.Close()
				ctx := upstream.WithClientProfile(context.Background(), profile.value)
				resp, err := request(ctx, srv.Client(), http.MethodGet, srv.URL, nil, http.Header{"Accept-Encoding": {"gzip"}})
				if err != nil {
					t.Fatal(err)
				}
				got, readErr := io.ReadAll(resp.Body)
				if err := resp.Body.Close(); err != nil {
					t.Errorf("关闭响应体: %v", err)
				}
				if string(got) != tc.want || !errors.Is(readErr, tc.err) {
					t.Fatalf("正文 %q，错误 %v。预期正文 %q，错误 %v", got, readErr, tc.want, tc.err)
				}
			})
		}
	}
}

func TestTraeCatalogRejectsGzipChecksumError(t *testing.T) {
	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	catalog := `{"data":{"list":[{"function":"solo_work_remote","models":[{"name":"fixture-model"}]}]}}`
	if _, err := io.WriteString(zw, catalog); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	wire := body.Bytes()
	wire[len(wire)-8] ^= 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		if _, err := w.Write(wire); err != nil {
			t.Errorf("发送压缩目录: %v", err)
		}
	}))
	defer srv.Close()
	adapter := Trae{HTTP: srv.Client(), WorkBase: srv.URL}
	models, err := adapter.Models(context.Background(), "traework:cn", Credential{AccessToken: "fixture"})
	if !errors.Is(err, gzip.ErrChecksum) || len(models) != 0 {
		t.Fatalf("CRC 错误的目录应拒绝，实际目录 %v，错误 %v", models, err)
	}
}

func TestTraeCodeRejectsUnadvertisedBrotli(t *testing.T) {
	for _, encodings := range [][]string{{"br"}, {"gzip, br"}, {"gzip", "br"}} {
		t.Run(strings.Join(encodings, "/"), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Accept-Encoding"); got != "gzip, deflate" {
					t.Errorf("Trae Code 发送了意外的 Accept-Encoding %q", got)
				}
				for _, encoding := range encodings {
					w.Header().Add("Content-Encoding", encoding)
				}
				br := brotli.NewWriter(w)
				defer br.Close()
				var out io.Writer = br
				if strings.Contains(encodings[0], "gzip") {
					gz := gzip.NewWriter(br)
					defer gz.Close()
					out = gz
				}
				if _, err := io.WriteString(out, `{"config_info_list":[]}`); err != nil {
					t.Errorf("写入压缩目录: %v", err)
				}
			}))
			defer srv.Close()
			adapter := Trae{HTTP: srv.Client(), CodeBase: srv.URL}
			_, err := adapter.Models(context.Background(), "traecode:cn", Credential{AccessToken: "fixture"})
			if err == nil || !strings.Contains(err.Error(), "未声明的压缩编码") {
				t.Fatalf("Trae Code 没有拒绝未声明的 Brotli：%v", err)
			}
		})
	}
}

func TestTraeCodeLeavesUnknownCodingOnWire(t *testing.T) {
	zw, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer zw.Close()
	ctx := upstream.WithClientProfile(context.Background(), upstream.ProfileTraeCode)
	for _, encoding := range []string{"zstd", "gzip, zstd"} {
		t.Run(encoding, func(t *testing.T) {
			payload := []byte(`{"config_info_list":[]}`)
			if encoding == "gzip, zstd" {
				var gzipped bytes.Buffer
				gz := gzip.NewWriter(&gzipped)
				if _, err := gz.Write(payload); err != nil {
					t.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
				payload = gzipped.Bytes()
			}
			wire := zw.EncodeAll(payload, nil)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Encoding", encoding)
				if _, err := w.Write(wire); err != nil {
					t.Errorf("写入原始压缩正文: %v", err)
				}
			}))
			defer srv.Close()
			resp, err := request(ctx, srv.Client(), http.MethodGet, srv.URL, nil, traeHeaders(Credential{}, false))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			got, err := io.ReadAll(resp.Body)
			if err != nil || !bytes.Equal(got, wire) || resp.Uncompressed || resp.Header.Get("Content-Encoding") != encoding {
				t.Fatalf("Trae Code 未保留原始压缩正文及编码头：%v", err)
			}
		})
	}
}
