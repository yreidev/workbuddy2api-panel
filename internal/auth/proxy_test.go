// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProxyBindingParse 嵌套形、扁平形都读顶层 proxy 与 proxy_url；proxy_url 取一次就清空。
func TestProxyBindingParse(t *testing.T) {
	for name, raw := range map[string]string{
		"嵌套形": `{"auth":{"accessToken":"at"},"account":{"uid":"u1"},"proxy":{"pool":"p1","id":"x1","manual":true},"proxy_url":" socks5://h:1080 "}`,
		"扁平形": `{"accessToken":"at","uid":"u1","proxy":{"pool":"p1","id":"x1","manual":true},"proxy_url":"socks5://h:1080"}`,
	} {
		t.Run(name, func(t *testing.T) {
			a, err := Parse([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := a.ProxyBinding(), (ProxyBinding{Pool: "p1", ID: "x1", Manual: true}); got != want {
				t.Fatalf("ProxyBinding = %+v，应为 %+v", got, want)
			}
			if got := a.TakeProxyURL(); got != "socks5://h:1080" {
				t.Fatalf("TakeProxyURL = %q", got)
			}
			if got := a.TakeProxyURL(); got != "" {
				t.Fatalf("第二次 TakeProxyURL 应为空，得到 %q", got)
			}
			if a.SetProxyURL(" http://h:2 "); a.TakeProxyURL() != "http://h:2" {
				t.Fatal("SetProxyURL 应去掉首尾空白记下")
			}
		})
	}
}

// TestProxyBindingDocument 绑了才写 proxy，proxy_url 从不写回；SaveAtomic 落盘后再读回一致。
func TestProxyBindingDocument(t *testing.T) {
	a, err := Parse([]byte(`{"accessToken":"at","uid":"u1","proxy_url":"http://h:8080"}`))
	if err != nil {
		t.Fatal(err)
	}
	if b := a.ProxyBinding(); !b.Empty() {
		t.Fatalf("没绑时应为空: %+v", b)
	}
	if _, ok := a.Document()["proxy"]; ok {
		t.Fatal("没绑代理不应写 proxy")
	}
	a.SetProxyBinding(ProxyBinding{Pool: "p1"})
	if a.ProxyBinding().Empty() {
		t.Fatal("只绑了池也不算空")
	}
	a.FilePath = filepath.Join(t.TempDir(), "workbuddy-u1.json")
	if err := a.SaveAtomic(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(a.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "proxy_url") {
		t.Fatalf("不应写回 proxy_url:\n%s", raw)
	}
	var doc struct {
		Proxy ProxyBinding `json:"proxy"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Proxy != (ProxyBinding{Pool: "p1"}) {
		t.Fatalf("落盘的 proxy = %+v（%v）:\n%s", doc.Proxy, err, raw)
	}
	b, err := Parse(raw)
	if err != nil || b.ProxyBinding() != (ProxyBinding{Pool: "p1"}) {
		t.Fatalf("读回 = %+v（%v）", b.ProxyBinding(), err)
	}
}
