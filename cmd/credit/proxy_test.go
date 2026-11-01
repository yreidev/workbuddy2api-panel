// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// proxy_test.go 查积分工具读网关的代理池：凭证里的绑定和 proxy_url 都认，被代理池拦着的号不查、error 写
// 原因；代理池文件坏了、参数不对都退出。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// creditUIDs 记下每个请求是哪个号发的，其余交给 creditFake。
type creditUIDs struct {
	fake *creditFake
	mu   sync.Mutex
	uids []string
}

func (c *creditUIDs) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.uids = append(c.uids, r.Header.Get("X-User-Id"))
	c.mu.Unlock()
	return c.fake.RoundTrip(r)
}

func TestCreditProxies(t *testing.T) {
	setAccountGap0(t)
	dir := t.TempDir()
	writeCreditAuth(t, dir, "u-cn", "国内号", "at-cn", "cn")
	for uid, extra := range map[string]string{
		"u-exp": `"proxy":{"pool":"p1","id":"x1"}`,
		"u-url": `"proxy_url":"http://127.0.0.1:1"`, // 认出是甲（已到期）
	} {
		raw := `{"auth":{"accessToken":"at-` + uid + `","domain":"www.codebuddy.cn","realm":"cn"},"account":{"uid":"` + uid + `"},` + extra + `}`
		if err := os.WriteFile(filepath.Join(dir, "workbuddy-"+uid+".json"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WB2A_AUTH_DIR", dir)
	pf := filepath.Join(t.TempDir(), "proxies.json")
	body := `{"pools":[{"id":"p1","name":"池"}],` +
		`"proxies":[{"id":"x1","name":"甲","url":"http://127.0.0.1:1","pool":"p1","expires_at":"2020-01-01T00:00:00Z"}]}`
	if err := os.WriteFile(pf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &creditFake{}
	rec := &creditUIDs{fake: fake}
	var out, errb bytes.Buffer
	if code := run([]string{"-proxies", pf}, &out, &errb, &upstream.Client{HTTP: &http.Client{Transport: rec}}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	var res creditOutput
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("输出不是 JSON: %v %s", err, out.String())
	}
	errs := map[string]string{}
	for _, a := range res.Accounts {
		errs[a.UID] = a.Error
	}
	if errs["u-cn"] != "" || errs["u-exp"] != "代理：代理「甲」已到期" || errs["u-url"] != "代理：代理「甲」已到期" || res.Total.Failed != 2 {
		t.Fatalf("结果: %+v", res)
	}
	if len(rec.uids) == 0 {
		t.Fatal("u-cn 应照常查")
	}
	for _, uid := range rec.uids {
		if uid != "u-cn" {
			t.Fatalf("被拦着的号不应发请求: %v", rec.uids)
		}
	}

	if err := os.WriteFile(pf, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	if code := run([]string{"-proxies", pf}, &out, &errb, &upstream.Client{HTTP: &http.Client{Transport: fake}}); code != 1 ||
		!strings.Contains(errb.String(), "解析代理池") {
		t.Fatalf("代理池文件坏了应退出: code=%d stderr=%q", code, errb.String())
	}
	if code := run([]string{"-nope"}, &out, io.Discard, &upstream.Client{HTTP: &http.Client{Transport: fake}}); code != 2 {
		t.Fatalf("参数不对应退出 2: %d", code)
	}
}
