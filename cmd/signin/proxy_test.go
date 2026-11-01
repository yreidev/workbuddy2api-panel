// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// proxy_test.go 签到工具读网关的代理池：被代理池拦着的号（没绑代理、代理到期）记 SKIP、不发请求，
// 绑了能用代理的号照常签到；代理池文件坏了、参数不对都退出。

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// signinProxiesFile 开着「只许走号的代理」，甲已到期、乙能用。
const signinProxiesFile = `{"settings":{"require_bound":true},"pools":[{"id":"p1","name":"池"}],
"proxies":[{"id":"x1","name":"甲","url":"http://127.0.0.1:1","pool":"p1","expires_at":"2020-01-01T00:00:00Z"},
{"id":"x2","name":"乙","url":"http://127.0.0.1:2","pool":"p1"}]}`

// writeBoundAuth 写一份绑了代理 proxy 的凭证（proxy 为空不绑）。
func writeBoundAuth(t *testing.T, dir, uid, proxy string) {
	t.Helper()
	bind := ""
	if proxy != "" {
		bind = fmt.Sprintf(`,"proxy":{"pool":"p1","id":%q}`, proxy)
	}
	raw := fmt.Sprintf(`{"auth":{"accessToken":"at-%s","refreshToken":"rt-%s","expiresAt":9999999999,"domain":"www.codebuddy.cn"},`+
		`"account":{"uid":%q,"nickname":%q}%s}`, uid, uid, uid, uid, bind)
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-"+uid+".json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSigninProxies(t *testing.T) {
	dir := t.TempDir()
	writeBoundAuth(t, dir, "u-free", "")
	writeBoundAuth(t, dir, "u-exp", "x1")
	writeBoundAuth(t, dir, "u-ok", "x2")
	pf := filepath.Join(t.TempDir(), "proxies.json")
	if err := os.WriteFile(pf, []byte(signinProxiesFile), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &signinFake{handler: func(r *http.Request, _ string) (int, string) {
		switch r.URL.Path {
		case "/v2/billing/meter/daily-checkin":
			return 200, `{"code":0,"data":{}}`
		case upstreamtest.FreePath:
			return 200, upstreamtest.List(upstreamtest.Package(100, 42, ""))
		}
		if status, body, ok := signinDefaultResponse(r.URL.Path); ok {
			return status, body
		}
		t.Errorf("未登记的请求: %s %s", r.Method, r.URL.Path)
		return 400, `{"code":400}`
	}}
	up := &upstream.Client{HTTP: &http.Client{Transport: fake}, ChatBaseCN: "https://chat.fake", BillingBaseCN: "https://billing.fake"}

	var out, errb bytes.Buffer
	if code := run([]string{"-proxies", pf, dir}, &out, &errb, up); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	text := out.String()
	for _, want := range []string{"| SKIP         | -      | 代理：没绑代理（开着「只许走号的代理」）", "| SKIP         | -      | 代理：代理「甲」已到期",
		"total=3 ok=1 already=0 fail=0 skip=2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少 %q:\n%s", want, text)
		}
	}
	for _, r := range fake.requests() {
		if r.uid != "u-ok" {
			t.Fatalf("被拦着的号不应发请求: %+v", r)
		}
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
