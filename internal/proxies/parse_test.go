// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package proxies

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestParseLines 认几种常见写法、行尾名字，跳过空行和注释，标出错行和这批里的重复。
func TestParseLines(t *testing.T) {
	text := strings.Join([]string{
		"socks5://user:pass@1.2.3.4:1080  香港-1",
		"",
		"# 注释",
		"// 也是注释",
		"HTTP://Example.COM:8080",
		"1.2.3.4:8080",
		"1.2.3.4:8080:user:pass",
		"user:pass@5.6.7.8:3128#东京",
		"user:pass:9.9.9.9:3128",
		"socks5h://only-user@h:1",
		"1.2.3.4:99999",
		"ftp://h:21",
		"http://:80",
		"a:b:c",
		"user:pass:host:port",
		"http://[::1",
		"bad@%zz:1",
		"1.2.3.4:8080",
	}, "\r\n")
	lines := ParseLines(text, "socks5")
	type want struct {
		no               int
		url, name, error string
		dup              string
	}
	wants := []want{
		{1, "socks5://user:***@1.2.3.4:1080", "香港-1", "", ""},
		{5, "http://example.com:8080", "example.com:8080", "", ""},
		{6, "socks5://1.2.3.4:8080", "1.2.3.4:8080", "", ""},
		{7, "socks5://user:***@1.2.3.4:8080", "1.2.3.4:8080", "", ""},
		{8, "socks5://user:***@5.6.7.8:3128", "东京", "", ""},
		{9, "socks5://user:***@9.9.9.9:3128", "9.9.9.9:3128", "", ""},
		{10, "socks5h://only-user@h:1", "h:1", "", ""},
		{11, "", "", "端口不对", ""},
		{12, "", "", "不支持的协议 ftp", ""},
		{13, "", "", "缺主机", ""},
		{14, "", "", "认不出写法", ""},
		{15, "", "", "端口不对", ""},
		{16, "", "", "不是合法的代理地址", ""},
		{17, "", "", "不是合法的代理地址", ""},
		{18, "socks5://1.2.3.4:8080", "1.2.3.4:8080", "", "batch"},
	}
	if len(lines) != len(wants) {
		t.Fatalf("解析出 %d 行，应为 %d: %+v", len(lines), len(wants), lines)
	}
	for i, w := range wants {
		l := lines[i]
		if l.No != w.no || l.URL != w.url || (w.error == "" && l.Name != w.name) || !strings.Contains(l.Error, w.error) || l.Duplicate != w.dup {
			t.Errorf("第 %d 项 = %+v，应为 %+v", i, l, w)
		}
		if strings.Contains(l.Text, "pass") && !strings.Contains(l.Text, "***") {
			t.Errorf("原文没打码: %q", l.Text)
		}
	}
	if lines[14].DuplicateOf != "6" {
		t.Fatalf("重复应指向第 6 行: %+v", lines[14])
	}
	if got := ParseLines("1.2.3.4:80", "随便")[0].URL; got != "http://1.2.3.4:80" {
		t.Fatalf("不认的默认协议按 http: %q", got)
	}
}

// TestMasking 打码、去重键、主机端口。
func TestMasking(t *testing.T) {
	for in, want := range map[string]string{
		"http://u:p@h:1":   "http://u:***@h:1",
		"http://u@h:1":     "http://u@h:1",
		"http://h:1":       "http://h:1",
		"::not a url":      "::not a url",
		"socks5://u:@h:1":  "socks5://u:***@h:1",
		"http://u%20x:p@h": "http://u x:***@h",
	} {
		if got := Masked(in); got != want {
			t.Errorf("Masked(%q) = %q，应为 %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"user:secret@bad host:1": "user:***@bad host:1",
		"no-at-sign":             "no-at-sign",
		"http://x@y:z":           "http://x@y:z",
		"user:pass:host:port":    "user:***:***:***",
		"1.2.3.4:99999:u:p":      "1.2.3.4:99999:***:***",
		"a:b":                    "a:b",
		"http://a:b:c":           "http://a:b:c",
	} {
		if got := maskLine(in); got != want {
			t.Errorf("maskLine(%q) = %q，应为 %q", in, got, want)
		}
	}
	if dedupKey("HTTP://U:p1@H:1") != dedupKey("http://U:p2@h:1") || dedupKey("http://h:1") == dedupKey("socks5://h:1") {
		t.Fatal("去重键：只改密码算重复，换协议不算")
	}
	if dedupKey("::bad") != "::bad" || hostPort("::bad") != "::bad" || hostPort("http://h:1") != "h:1" {
		t.Fatal("解析不了的地址原样返回")
	}
}

// TestPreviewImportExport 预览标出和已有的重复；导入只加能用的行、设到期日、马上排进检查；导出能再导入。
func TestPreviewImportExport(t *testing.T) {
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "池"})
	tr.add(t, p1, "http://u:p@h1:1 一号")
	lines := tr.Preview("http://u:other@h1:1\nhttp://h2:1\nbad", "http")
	if lines[0].Duplicate != "existing" || lines[0].DuplicateOf != "一号" || lines[1].Duplicate != "" || lines[2].Error == "" {
		t.Fatalf("预览 = %+v", lines)
	}
	if _, err := tr.Import("http://h3:1", "http", "nope", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("导入到不存在的池: %v", err)
	}
	exp := t0.Add(72 * time.Hour)
	res, err := tr.Import("http://u:other@h1:1\nhttp://h2:1 二号\nbad", "http", p1, &exp)
	if err != nil || len(res.Added) != 1 {
		t.Fatalf("导入: %+v %v", res, err)
	}
	px := tr.View().Proxies[1]
	if px.Name != "二号" || px.ExpiresAt == nil || !px.ExpiresAt.Equal(exp) || !tr.due[res.Added[0]] {
		t.Fatalf("新代理: %+v", px)
	}
	if res, err := tr.Import("bad", "http", p1, nil); err != nil || len(res.Added) != 0 {
		t.Fatalf("全是错行: %+v %v", res, err)
	}
	out := tr.Export()
	if !strings.Contains(out, "# 池：池\n") || !strings.Contains(out, "http://u:p@h1:1 一号\n") || !strings.Contains(out, "http://h2:1 二号\n") {
		t.Fatalf("导出 = %q", out)
	}
	tr2 := newReg(t)
	p2 := tr2.pool(t, PoolInput{Name: "池"})
	if res, err := tr2.Import(out, "http", p2, nil); err != nil || len(res.Added) != 2 {
		t.Fatalf("导出的内容应能原样再导入: %+v %v", res, err)
	}
}
