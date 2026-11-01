// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package proxies

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestParseExit 认 ip-api、ipify、别的带 ip 字段的 JSON，Cloudflare trace 那样的 key=value，纯文本 IP。
func TestParseExit(t *testing.T) {
	for _, c := range []struct {
		body string
		want Exit
		err  string
	}{
		{`{"status":"success","query":"1.2.3.4","country":"中国","regionName":"香港","city":"香港","isp":"某运营商"}`,
			Exit{IP: "1.2.3.4", Country: "中国", Region: "香港", City: "香港", ISP: "某运营商"}, ""},
		{` {"ip":"2001:db8::1"} `, Exit{IP: "2001:db8::1"}, ""},
		{`{"ip":"5.6.7.8","region":"东京","org":"某公司"}`, Exit{IP: "5.6.7.8", Region: "东京", ISP: "某公司"}, ""},
		{`{"status":"fail","message":"private range"}`, Exit{}, "查询失败：private range"},
		{`{bad`, Exit{}, "不是合法的 JSON"},
		{`{"country":"中国"}`, Exit{}, "没有 IP"},
		{"fl=1\nip=9.9.9.9\nloc=HK\n", Exit{IP: "9.9.9.9", Country: "HK"}, ""},
		{"ip=not-ip", Exit{}, "没有 IP"},
		{"1.1.1.1\n", Exit{IP: "1.1.1.1"}, ""},
		{"<html>", Exit{}, "没有 IP"},
	} {
		e, err := parseExit([]byte(c.body))
		switch {
		case c.err != "":
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("parseExit(%q) 错误 = %v，应含 %q", c.body, err, c.err)
			}
		case err != nil || *e != c.want:
			t.Errorf("parseExit(%q) = %+v %v，应为 %+v", c.body, e, err, c.want)
		}
	}
}

// TestProbeExit 按顺序问，第一个查到的为准；都没查到记下每个地址的原因；经代理问；没配地址用内置的。
func TestProbeExit(t *testing.T) {
	ctx := context.Background()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"query":"7.7.7.7","country":"日本"}`))
	}))
	defer ok.Close()
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusTooManyRequests)
	}))
	defer fail.Close()
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("{"))
	}))
	defer cut.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	e := probeExit(ctx, "", []string{"http://[::1", fail.URL, ok.URL})
	if e.IP != "7.7.7.7" || e.Country != "日本" || e.Source != ok.URL || e.Error != "" {
		t.Fatalf("前面的失败了应接着问: %+v", e)
	}
	e = probeExit(ctx, "", []string{fail.URL, cut.URL, closed.URL})
	if e.IP != "" || !strings.Contains(e.Error, hostOf(fail.URL)+"：HTTP 429") ||
		!strings.Contains(e.Error, hostOf(cut.URL)+"：unexpected EOF") || !strings.Contains(e.Error, hostOf(closed.URL)+"：") ||
		strings.Count(e.Error, "；") != 2 {
		t.Fatalf("都没查到应记下每个的原因: %q", e.Error)
	}
	if e := probeExit(ctx, "::bad", nil); e.Error != "代理地址不对" {
		t.Fatalf("坏的代理地址: %+v", e)
	}

	// 假的 HTTP 代理：收到的是完整地址的请求，替目标回答。
	var mu sync.Mutex
	var seen []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ip":"8.8.4.4"}`))
	}))
	defer proxy.Close()
	if e := probeExit(ctx, proxy.URL, []string{"http://exit.example/json"}); e.IP != "8.8.4.4" {
		t.Fatalf("经代理: %+v", e)
	}
	old := builtinExitURLs
	builtinExitURLs = []string{"http://builtin.example/"}
	t.Cleanup(func() { builtinExitURLs = old })
	if e := probeExit(ctx, proxy.URL, nil); e.Source != "http://builtin.example/" {
		t.Fatalf("没配地址用内置的: %+v", e)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(seen, " ") != "http://exit.example/json http://builtin.example/" {
		t.Fatalf("代理收到 %v", seen)
	}
	if hostOf("::bad") != "::bad" || hostOf("no-host") != "no-host" {
		t.Fatal("hostOf 解析不了原样返回")
	}
}
