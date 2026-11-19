// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"regexp"
	"strings"
	"testing"
)

func TestTTNetNativeTraceVectors(t *testing.T) {
	// 官方原生函数固定时间和 RandBytes 的输出；来源见 docs/official-client.md。
	var entropy [17]byte
	for i := range entropy {
		entropy[i] = 0xef
	}
	for _, tc := range []struct{ device, trace string }{
		{"fixture-device", "e7b9dfdd010fffffffffffffffffffff"},
		{"123456789", "e7b9dfdd0775bcd15fffffffffffffff"},
		{"0", "e7b9dfdd010fffffffffffffffffffff"},
		{"1", "e7b9dfdd011fffffffffffffffffffff"},
		{"15", "e7b9dfdd01ffffffffffffffffffffff"},
		{"16", "e7b9dfdd0210ffffffffffffffffffff"},
		{"18446744073709551615", "e7b9dfdd010fffffffffffffffffffff"},
		{"-1", "e7b9dfdd10ffffffffffffffffffffff"},
		{"+42", "e7b9dfdd022affffffffffffffffffff"},
		{" 42 ", "e7b9dfdd010fffffffffffffffffffff"},
		{"042", "e7b9dfdd022affffffffffffffffffff"},
	} {
		want := "00-" + tc.trace + "-" + tc.trace[:16] + "-01"
		if got := formatTTNetTraceID(1794889080380, tc.device, entropy); got != want {
			t.Errorf("device=%q：%s，原生样本 %s", tc.device, got, want)
		}
	}
}

func TestTTNetTraceFreshness(t *testing.T) {
	pattern := regexp.MustCompile(`^00-[0-9a-f]{8}010[0-9a-f]{17}ffff-[0-9a-f]{16}-01$`)
	a, b := newTTNetTraceID("fixture-device"), newTTNetTraceID("fixture-device")
	for _, value := range []string{a, b} {
		if !pattern.MatchString(value) || strings.Split(value, "-")[2] != value[3:19] {
			t.Fatalf("trace 偏离原生格式：%s", value)
		}
	}
	if a == b {
		t.Fatal("不同请求复用了 trace")
	}
}
