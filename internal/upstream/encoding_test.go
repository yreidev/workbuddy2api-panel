// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

// encoding_test.go 出站请求的 Accept-Encoding 取值与响应解压（encoding.go）。不连外网。

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const encPlain = `{"code":0,"data":{"msg":"压缩前的原文"}}`

func encGzip(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := io.WriteString(w, s); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encZlib(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	if _, err := io.WriteString(w, s); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encFlate(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := flate.NewWriter(&b, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, s); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encBrotli(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := brotli.NewWriter(&b)
	if _, err := io.WriteString(w, s); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encZstd(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := zstd.NewWriter(&b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, s); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// encResp 带 Content-Encoding 与 Content-Length 的响应。
func encResp(status int, enc string, body []byte) *http.Response {
	h := http.Header{"Content-Type": []string{"application/json"}}
	if enc != "" {
		h.Set("Content-Encoding", enc)
	}
	h.Set("Content-Length", "123")
	return &http.Response{StatusCode: status, Header: h, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body))}
}

// TestDecodeResponseEncodings 各编码按 axios 的解法解开：gzip / compress 自动分辨 gzip 与 zlib，
// deflate 分辨 zlib 与裸 deflate，br、zstd 用库；解开后去掉 Content-Encoding / Content-Length。
func TestDecodeResponseEncodings(t *testing.T) {
	cases := []struct {
		enc  string
		body []byte
	}{
		{"gzip", encGzip(t, encPlain)},
		{"x-gzip", encGzip(t, encPlain)},
		{"GZIP", encGzip(t, encPlain)},
		{"gzip", encZlib(t, encPlain)},
		{"compress", encZlib(t, encPlain)},
		{"x-compress", encGzip(t, encPlain)},
		{"deflate", encZlib(t, encPlain)},
		{"deflate", encFlate(t, encPlain)},
		{"br", encBrotli(t, encPlain)},
		{"zstd", encZstd(t, encPlain)},
	}
	for _, tc := range cases {
		resp := decodeResponse(encResp(200, tc.enc, tc.body))
		got, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("%s: 读取: %v", tc.enc, err)
		}
		if string(got) != encPlain {
			t.Fatalf("%s: 解出 %q", tc.enc, got)
		}
		if resp.Header.Get("Content-Encoding") != "" || resp.Header.Get("Content-Length") != "" {
			t.Fatalf("%s: 解开后应删掉编码与长度头: %v", tc.enc, resp.Header)
		}
		if resp.ContentLength != -1 || !resp.Uncompressed {
			t.Fatalf("%s: ContentLength=%d Uncompressed=%v", tc.enc, resp.ContentLength, resp.Uncompressed)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("%s: Close: %v", tc.enc, err)
		}
	}
}

// TestDecodeResponsePassThrough 没编码、identity、不认识的编码、Go 已经解过、空响应原样交回。
func TestDecodeResponsePassThrough(t *testing.T) {
	for _, enc := range []string{"", "identity", "snappy"} {
		resp := decodeResponse(encResp(200, enc, []byte(encPlain)))
		got, _ := io.ReadAll(resp.Body)
		if string(got) != encPlain || resp.Header.Get("Content-Encoding") != enc || resp.Uncompressed {
			t.Fatalf("%q: 应原样交回，body=%q header=%v", enc, got, resp.Header)
		}
	}
	done := encResp(200, "gzip", []byte(encPlain))
	done.Uncompressed = true
	if got, _ := io.ReadAll(decodeResponse(done).Body); string(got) != encPlain {
		t.Fatalf("Uncompressed 的响应不应再解: %q", got)
	}
	if decodeResponse(nil) != nil {
		t.Fatal("nil 应原样返回")
	}
	noBody := &http.Response{Header: http.Header{"Content-Encoding": []string{"gzip"}}}
	if decodeResponse(noBody).Body != nil {
		t.Fatal("没有 Body 应原样返回")
	}
}

// TestDecodeEmptyBody 带了 Content-Encoding 但一个字节都没有（如 204、空的 2xx）：直接 EOF，不报错。
func TestDecodeEmptyBody(t *testing.T) {
	for _, enc := range []string{"gzip", "deflate", "br", "zstd"} {
		resp := decodeResponse(encResp(200, enc, nil))
		got, err := io.ReadAll(resp.Body)
		if err != nil || len(got) != 0 {
			t.Fatalf("%s: 空响应 got=%q err=%v", enc, got, err)
		}
		// 再读一次仍是 EOF。
		if n, err := resp.Body.Read(make([]byte, 8)); n != 0 || err != io.EOF {
			t.Fatalf("%s: 第二次读 n=%d err=%v", enc, n, err)
		}
	}
}

type encErrReader struct{ err error }

func (r encErrReader) Read([]byte) (int, error) { return 0, r.err }

type encCloser struct {
	io.Reader
	closed bool
}

func (c *encCloser) Close() error { c.closed = true; return nil }

// TestDecodeErrors 读响应体出错原样上抛并记住；数据坏了报解压错误；Close 关原响应体。
func TestDecodeErrors(t *testing.T) {
	boom := errors.New("连接断了")
	resp := encResp(200, "gzip", nil)
	resp.Body = io.NopCloser(encErrReader{err: boom})
	resp = decodeResponse(resp)
	for i := 0; i < 2; i++ {
		if _, err := resp.Body.Read(make([]byte, 8)); !errors.Is(err, boom) {
			t.Fatalf("第 %d 次读 err = %v，应为原错误", i+1, err)
		}
	}

	bad := decodeResponse(encResp(200, "gzip", []byte("not gzip at all")))
	if _, err := io.ReadAll(bad.Body); err == nil {
		t.Fatal("坏数据应报错")
	}
	badZstd := decodeResponse(encResp(200, "zstd", []byte("not zstd at all")))
	if _, err := io.ReadAll(badZstd.Body); err == nil {
		t.Fatal("坏的 zstd 应报错")
	}

	raw := &encCloser{Reader: bytes.NewReader(encGzip(t, encPlain))}
	resp = encResp(200, "gzip", nil)
	resp.Body = raw
	resp = decodeResponse(resp)
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !raw.closed {
		t.Fatal("Close 应关掉原响应体")
	}
	// 没读过就关：不建解压器，也关原响应体。
	raw2 := &encCloser{Reader: bytes.NewReader(nil)}
	resp = encResp(200, "br", nil)
	resp.Body = raw2
	_ = decodeResponse(resp).Body.Close()
	if !raw2.closed {
		t.Fatal("没读过也应关掉原响应体")
	}
}

// TestDecodeStreamingLazy 流式响应：doDecoded 不等响应体就返回；服务端每次 flush 的内容
// 立刻能读到，不用等整个流结束。
func TestDecodeStreamingLazy(t *testing.T) {
	pr, pw := io.Pipe()
	hc := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Encoding": []string{"gzip"}}, Body: pr}, nil
	})}
	req, _ := http.NewRequest(http.MethodPost, "https://chat.example/x", nil)
	req.Header.Set("Accept-Encoding", acceptEncodingAxios)
	resp, err := doDecoded(hc, req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	zw := gzip.NewWriter(pw)
	writeFlush := func(s string) {
		if _, err := io.WriteString(zw, s); err != nil {
			t.Error(err)
		}
		if err := zw.Flush(); err != nil {
			t.Error(err)
		}
	}
	first := make(chan struct{})
	go func() {
		defer close(first)
		writeFlush("data: 1\n\n")
	}()
	buf := make([]byte, 64)
	got := ""
	deadline := time.Now().Add(5 * time.Second)
	for got != "data: 1\n\n" && time.Now().Before(deadline) {
		n, err := resp.Body.Read(buf)
		if err != nil {
			t.Fatalf("读第一段: %v", err)
		}
		got += string(buf[:n])
	}
	if got != "data: 1\n\n" {
		t.Fatalf("第一段 = %q", got)
	}
	<-first
	go func() {
		writeFlush("data: 2\n\n")
		_ = zw.Close()
		_ = pw.Close()
	}()
	rest, err := io.ReadAll(resp.Body)
	if err != nil || string(rest) != "data: 2\n\n" {
		t.Fatalf("其余 = %q err=%v", rest, err)
	}
}

// TestDoDecodedOnlyWhenRequested 请求没显式设 Accept-Encoding（Go 自己补 gzip、自己解）时不插手；
// 出错时原样返回错误。
func TestDoDecodedOnlyWhenRequested(t *testing.T) {
	body := encGzip(t, encPlain)
	hc := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return encResp(200, "gzip", body), nil
	})}
	req, _ := http.NewRequest(http.MethodGet, "https://x.example/", nil)
	resp, err := doDecoded(hc, req)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(resp.Body); !bytes.Equal(got, body) || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("没设 Accept-Encoding 的请求不应解压")
	}

	boom := errors.New("拨号失败")
	hc = &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) { return nil, boom })}
	req.Header.Set("Accept-Encoding", acceptEncodingAxios)
	if _, err := doDecoded(hc, req); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// TestAcceptEncodingPerRequestKind 各类请求按官方各自的取值设 Accept-Encoding（见 encoding.go 文件头）。
func TestAcceptEncodingPerRequestKind(t *testing.T) {
	c := &Client{WebBaseCN: "https://web.example"}
	a := upAuth()
	newReq := func(method string) *http.Request {
		r, _ := http.NewRequest(method, "https://x.example/", nil)
		return r
	}
	check := func(name string, r *http.Request, want string) {
		t.Helper()
		if got := r.Header.Get("Accept-Encoding"); got != want {
			t.Errorf("%s: Accept-Encoding = %q，want %q", name, got, want)
		}
	}

	r := newReq(http.MethodGet)
	c.BillingHeaders(r, a)
	check("后台服务请求", r, acceptEncodingAxios)
	r = newReq(http.MethodPost)
	c.RefreshHeaders(r, &auth.Auth{AccessToken: "at", RefreshToken: "rt", UID: "u-1"})
	check("刷新令牌", r, acceptEncodingAxios)
	r = newReq(http.MethodPost)
	c.ChatHeaders(r, a, "", ChatMeta{ConversationRequestID: "rq"})
	check("对话", r, acceptEncodingAxios)
	r = newReq(http.MethodGet)
	c.webHeaders(r, a, webPageGrowthCenter)
	check("官网网页 XHR", r, acceptEncodingChrome)
	r = newReq(http.MethodGet)
	c.webNavigationHeaders(r, a)
	check("client-login 导航", r, acceptEncodingChrome)
	r = newReq(http.MethodGet)
	c.webMPHeaders(r, a)
	check("小程序网页", r, acceptEncodingBrowser)
	r, err := c.globalRegisterReq(&auth.Auth{}, http.MethodPost, "https://x.example/", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	check("国际版注册", r, acceptEncodingChrome)

	var code *http.Request
	cc := &Client{WebBaseCN: "https://web.example", HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		code = r
		return jsonResp(200, `{"code":0,"data":{}}`), nil
	})}}
	_, _ = cc.fetchDeviceAuthCode(&auth.Auth{AccessToken: "at", RefreshToken: "rt", UID: "u-1"})
	if code == nil {
		t.Fatal("登录码请求没发出")
	}
	check("网页登录码", code, acceptEncodingNodeFetch)
	wantHeaders(t, code.Header, map[string]string{"Accept": "*/*", "Accept-Language": "*", "Sec-Fetch-Mode": "cors", "User-Agent": "node"})

	var login *http.Request
	f := &loginFoFake{handler: func(_ string, _ bool, r *http.Request) (*http.Response, error) {
		login = r
		return jsonResp(http.StatusOK, loginFoStateBody), nil
	}}
	if _, err := loginFoClient(f).LoginAuthState("cn", ""); err != nil {
		t.Fatalf("LoginAuthState: %v", err)
	}
	check("登录", login, acceptEncodingAxios)
}

// TestCallSitesDecode 出站请求的几个发送点都会解压：业务 JSON（zstd）、对话流（br）、对话
// 错误体（gzip）。
func TestCallSitesDecode(t *testing.T) {
	sse := "data: {\"choices\":[]}\n\ndata: [DONE]\n\n"
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Accept-Encoding") != acceptEncodingAxios {
			t.Errorf("%s Accept-Encoding = %q", r.URL.Path, r.Header.Get("Accept-Encoding"))
		}
		switch {
		case r.URL.Path == "/biz":
			return encResp(200, "zstd", encZstd(t, encPlain)), nil
		case strings.Contains(r.URL.Path, "chat/completions"):
			if strings.Contains(r.Header.Get("X-Conversation-Request-ID"), "fail") {
				return encResp(402, "gzip", encGzip(t, `{"code":1,"msg":"余额不足"}`)), nil
			}
			resp := encResp(200, "br", encBrotli(t, sse))
			resp.Header.Set("Content-Type", "text/event-stream")
			return resp, nil
		}
		return jsonResp(404, `{}`), nil
	})
	a := upAuth()

	req, _ := http.NewRequest(http.MethodGet, "https://billing.example/biz", nil)
	c.BillingHeaders(req, a)
	data, err := c.doJSON(req)
	if err != nil || !strings.Contains(string(data), "压缩前的原文") {
		t.Fatalf("doJSON data=%s err=%v", data, err)
	}

	rc, status, _, err := c.ChatStream(a, []byte(`{"model":"glm-5.2","messages":[]}`), "", ChatMeta{ConversationRequestID: "ok"})
	if err != nil || status != 200 {
		t.Fatalf("chat: status=%d err=%v", status, err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != sse {
		t.Fatalf("对话流 = %q", got)
	}

	_, status, respBody, _ := c.ChatStream(a, []byte(`{"model":"glm-5.2","messages":[]}`), "", ChatMeta{ConversationRequestID: "fail"})
	if status != 402 || !strings.Contains(string(respBody), "余额不足") {
		t.Fatalf("错误体 status=%d body=%q", status, respBody)
	}
}

func TestDecodeBodyPassThrough(t *testing.T) {
	raw := io.NopCloser(strings.NewReader("x"))
	for _, enc := range []string{"", "identity", "snappy"} {
		if got, ok := DecodeBody(enc, raw); ok || got != raw {
			t.Errorf("DecodeBody(%q) 应原样交回", enc)
		}
	}
	if _, ok := DecodeBody(" GZIP ", raw); !ok {
		t.Error("DecodeBody 应不区分大小写、忽略空白")
	}
}
