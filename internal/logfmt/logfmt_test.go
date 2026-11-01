// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package logfmt

import (
	"strings"
	"testing"
)

// TestTruncate 字节上限截断：n<=0、先 TrimSpace、短串原样、中文按 rune 边界回退。
func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"n 为 0", "abc", 0, ""},
		{"n 为负", "abc", -3, ""},
		{"先 TrimSpace", "  abc  ", 10, "abc"},
		{"短于 n", "abc", 5, "abc"},
		{"正好等于长度", "abc", 3, "abc"},
		{"ASCII 截断", "abcdef", 3, "abc"},
		{"中文切点回退到字符开头", "abc猫", 4, "abc"},
		{"中文切点落在字符中间", "abc猫", 5, "abc"},
		{"单字不足上限返回空", "猫", 1, ""},
		{"单字两字节切点也回退", "猫", 2, ""},
		{"单字完整保留", "猫", 3, "猫"},
		{"两字四字节只留第一个", "猫猫", 4, "猫"},
		{"多字节后还有 ASCII", "猫ab", 4, "猫a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Truncate(tc.s, tc.n); got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q，应为 %q", tc.s, tc.n, got, tc.want)
			}
		})
	}
}

// TestUID8 uid 前 8 位；空 uid 返回 "-"。
func TestUID8(t *testing.T) {
	cases := map[string]string{
		"":                 "-",
		"abc":              "abc",
		"12345678":         "12345678",
		"123456789":        "12345678",
		"abcdefghijklmnop": "abcdefgh",
	}
	for in, want := range cases {
		if got := UID8(in); got != want {
			t.Errorf("UID8(%q) = %q，应为 %q", in, got, want)
		}
	}
}

// TestLabel 昵称 + uid8；昵称为空或全空白退回 uid8；两者皆空 "-"。
func TestLabel(t *testing.T) {
	cases := []struct {
		uid, nick, want string
	}{
		{"a1b2c3d4e5", "示例昵称甲", "示例昵称甲(a1b2c3d4)"},
		{"a1b2c3d4", "", "a1b2c3d4"},
		{"a1b2c3d4", "   ", "a1b2c3d4"},
		{"short", " 猫 ", "猫(short)"},
		{"", "", "-"},
		{"", "昵称", "昵称(-)"},
	}
	for _, tc := range cases {
		if got := Label(tc.uid, tc.nick); got != tc.want {
			t.Errorf("Label(%q, %q) = %q，应为 %q", tc.uid, tc.nick, got, tc.want)
		}
	}
}

// TestDisplayWidth 显示列宽：ASCII 1 列，CJK/全角/emoji 2 列，控制字符 0 列。
func TestDisplayWidth(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want int
	}{
		{"空串", "", 0},
		{"ASCII", "abc", 3},
		{"中文", "猫", 2},
		{"中文混排", "a猫b", 4},
		{"全角字母", "ＡＢ", 4},
		{"全角感叹号", "！", 2},
		{"全角符号段", "￥", 2},
		{"Hangul Jamo", "ᄀ", 2},
		{"尖括号", "〈", 2},
		{"CJK 扩展 B", "\U00020000", 2},
		{"emoji", "🐱", 2},
		{"制表符", "\t", 0},
		{"NUL", "\x00", 0},
		{"Hangul 音节", "가", 2},
		{"CJK 兼容表意", "\uf900", 2},
		{"CJK 兼容形式", "﹐", 2},
		{"DEL", "\x7f", 0},
		{"窄字符 303f", "〿", 1},
		{"普通符号省略号", "…", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayWidth(tc.s); got != tc.want {
				t.Errorf("DisplayWidth(%q) = %d，应为 %d", tc.s, got, tc.want)
			}
		})
	}
}

// TestPad 右补空格到目标显示列宽；已够宽或 width<=0 原样返回，不截断。
func TestPad(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"width 为 0", "abc", 0, "abc"},
		{"width 为负", "abc", -1, "abc"},
		{"已经够宽", "abcdef", 3, "abcdef"},
		{"正好等宽", "abc", 3, "abc"},
		{"ASCII 补空格", "ab", 5, "ab   "},
		{"中文按显示宽补齐", "猫", 5, "猫" + strings.Repeat(" ", 3)},
		{"中文混排", "a猫", 4, "a猫 "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Pad(tc.s, tc.width); got != tc.want {
				t.Errorf("Pad(%q, %d) = %q，应为 %q", tc.s, tc.width, got, tc.want)
			}
		})
	}
}

// TestMaskIPs IPv4 和方括号里的 IPv6 换成 <IP>，端口、域名和别的数字不动。
func TestMaskIPs(t *testing.T) {
	cases := map[string]string{
		"连代理 http://203.0.113.7:8080：dial tcp 203.0.113.7:8080: connect: connection refused": "连代理 http://<IP>:8080：dial tcp <IP>:8080: connect: connection refused",
		"dial tcp [2001:db8::1]:443: i/o timeout":                                            "dial tcp <IP>:443: i/o timeout",
		"lookup proxy.example.com on [::1]:53: no such host":                                 "lookup proxy.example.com on <IP>:53: no such host",
		"代理 socks5://proxy.example.com:1080 拒绝 CONNECT www.workbuddy.cn:443：407":             "代理 socks5://proxy.example.com:1080 拒绝 CONNECT www.workbuddy.cn:443：407",
		"版本 5.9.4，耗时 12.5s":                                                                  "版本 5.9.4，耗时 12.5s",
	}
	for in, want := range cases {
		if got := MaskIPs(in); got != want {
			t.Errorf("MaskIPs(%q) = %q，应为 %q", in, got, want)
		}
	}
}
