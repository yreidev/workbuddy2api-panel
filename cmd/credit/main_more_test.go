// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// main_more_test.go 覆盖 main 的退出码与 -pretty 在无成功账号时的输出。

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// creditExit 捕获 exitFunc 的退出码。
type creditExit int

func stubCreditExit(t *testing.T) {
	t.Helper()
	old := exitFunc
	exitFunc = func(code int) { panic(creditExit(code)) }
	t.Cleanup(func() { exitFunc = old })
}

// 正常路径（空目录）main 以 0 退出。
func TestCreditMainExitZero(t *testing.T) {
	stubCreditExit(t)
	setAccountGap0(t)
	t.Setenv("WB2A_AUTH_DIR", t.TempDir())
	oldArgs := os.Args
	os.Args = []string{"credit"}
	t.Cleanup(func() { os.Args = oldArgs })

	code := -1
	func() {
		defer func() {
			if r := recover(); r != nil {
				p, ok := r.(creditExit)
				if !ok {
					panic(r)
				}
				code = int(p)
				return
			}
			t.Fatal("main 应调用 exitFunc")
		}()
		main()
	}()
	if code != 0 {
		t.Fatalf("退出码 = %d want 0", code)
	}
}

// 全部账号失败：账号 0/N、总计 0/0、剩余 0%，失败明细照常列出。
func TestCreditPrettyAllFailed(t *testing.T) {
	setAccountGap0(t)
	dir := t.TempDir()
	writeCreditAuth(t, dir, "u-err", "", "at-err", "cn")
	t.Setenv("WB2A_AUTH_DIR", dir)

	var out bytes.Buffer
	if code := run([]string{"-pretty"}, &out, io.Discard, &upstream.Client{HTTP: &http.Client{Transport: &creditFake{}}}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	text := out.String()
	for _, want := range []string{"账号: 0/1", "总计: 0/0", "剩余: 0%", "⚠️  upstream client (http 400)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("pretty 输出缺少 %q:\n%s", want, text)
		}
	}
}
