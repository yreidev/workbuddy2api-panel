// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// proxy_test.go 领试用工具读网关的代理池：代理不能用的国际版号记 SKIP、不发请求；代理池文件坏了、
// 参数不对都退出。

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

func TestTrialProxies(t *testing.T) {
	dir := t.TempDir()
	writeTrialAuth(t, dir, "g-ok", "国际成功", "global")
	raw := `{"auth":{"accessToken":"at-g-exp","refreshToken":"rt","expiresAt":9999999999,"domain":"www.workbuddy.ai","realm":"global"},` +
		`"account":{"uid":"g-exp","nickname":"到期号"},"proxy":{"pool":"p1","id":"x1"}}`
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-g-exp.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	pf := filepath.Join(t.TempDir(), "proxies.json")
	body := `{"pools":[{"id":"p1","name":"池"}],` +
		`"proxies":[{"id":"x1","name":"甲","url":"http://127.0.0.1:1","pool":"p1","expires_at":"2020-01-01T00:00:00Z"}]}`
	if err := os.WriteFile(pf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &trialFake{}
	up := &upstream.Client{HTTP: &http.Client{Transport: fake}, BillingBaseGlobal: "https://global.fake"}
	var out, errb bytes.Buffer
	if code := run([]string{"-proxies", pf, dir}, &out, &errb, up); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	text := out.String()
	if !strings.Contains(text, "| SKIP    | 代理：代理「甲」已到期") || !strings.Contains(text, "total=2 ok=1 already=0 na=0 fail=0 skip=1") {
		t.Fatalf("输出:\n%s", text)
	}
	if reqs := fmt.Sprint(fake.requests()); strings.Count(reqs, "/billing/ide/trial") != 1 {
		t.Fatalf("只有 g-ok 发了请求: %s", reqs)
	}

	if err := os.WriteFile(pf, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	if code := run([]string{"-proxies", pf, dir}, &out, &errb, up); code != 1 || !strings.Contains(errb.String(), "解析代理池") {
		t.Fatalf("代理池文件坏了应退出: code=%d stderr=%q", code, errb.String())
	}
	if code := run([]string{"-nope"}, &out, &errb, up); code != 2 {
		t.Fatalf("参数不对应退出 2: %d", code)
	}
}
