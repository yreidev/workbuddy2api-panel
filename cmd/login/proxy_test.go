// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// proxy_test.go 登录工具走代理池：url 在选的池里按绑号最少分代理（凭证目录里的号算进绑号数），记进
// state 文件；poll 输出带上绑定；池不存在、只许走代理时选了不绑、代理池文件坏了、凭证目录名不对都退出。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loginProxyFixture 代理池文件（国内版默认池「国内」，每个代理最多 1 个号，甲、乙两个代理）和凭证目录
// （u-old 绑在甲上，外加一个坏文件）。
func loginProxyFixture(t *testing.T, settings string) (proxiesPath, authDir string) {
	t.Helper()
	proxiesPath = filepath.Join(t.TempDir(), "proxies.json")
	body := `{"settings":` + settings + `,"pools":[{"id":"p1","name":"国内","max_per_proxy":1,"default_for":["cn"]}],` +
		`"proxies":[{"id":"x1","name":"甲","url":"http://127.0.0.1:1","pool":"p1"},{"id":"x2","name":"乙","url":"http://127.0.0.1:2","pool":"p1"}]}`
	if err := os.WriteFile(proxiesPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	authDir = t.TempDir()
	old := `{"auth":{"accessToken":"at-old","domain":"www.codebuddy.cn"},"account":{"uid":"u-old"},"proxy":{"pool":"p1","id":"x1"}}`
	if err := os.WriteFile(filepath.Join(authDir, "workbuddy-u-old.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "workbuddy-bad.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	return proxiesPath, authDir
}

func TestParseProxyArgs(t *testing.T) {
	o, rest, err := parseProxyArgs([]string{"--pool", " 国内 ", "url", "--proxies=p.json", "--auths", "a"})
	if err != nil || o != (loginOpts{pool: "国内", proxies: "p.json", auths: "a"}) || !reflect.DeepEqual(rest, []string{"url"}) {
		t.Fatalf("o=%+v rest=%v err=%v", o, rest, err)
	}
	if o, _, _ := parseProxyArgs(nil); o.proxies != filepath.Join("data", "proxies.json") || o.auths != "auths" || o.pool != "" {
		t.Fatalf("默认值: %+v", o)
	}
	if _, _, err := parseProxyArgs([]string{"--pool"}); err == nil || !strings.Contains(err.Error(), "--pool requires a value") {
		t.Fatalf("缺值应报错: %v", err)
	}
}

// url 分到乙（甲已经绑着 u-old，绑满了），state 文件记下代理和池；poll 输出带上绑定。
func TestRunLoginViaProxyPool(t *testing.T) {
	statePath := useStateFile(t)
	pf, dir := loginProxyFixture(t, `{}`)
	fake := loginFakeClient(t)
	_, errb, fatal, _ := runLogin(t, []string{"--proxies", pf, "--auths=" + dir, "url"}, strings.NewReader(""), &http.Client{Transport: fake})
	if fatal {
		t.Fatalf("不应 fatal: %s", errb)
	}
	if !strings.Contains(errb, "登录走代理「乙」") || !strings.Contains(errb, "跳过 "+filepath.Join(dir, "workbuddy-bad.json")) {
		t.Fatalf("stderr=%q", errb)
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st loginState
	if err := json.Unmarshal(raw, &st); err != nil || st.Trace.Proxy != "x2" || st.Pool != "p1" {
		t.Fatalf("state 文件 = %s (%v)", raw, err)
	}

	out, errb, fatal, _ := runLogin(t, []string{"--proxies", pf, "poll"}, strings.NewReader(""), &http.Client{Transport: fake})
	if fatal {
		t.Fatalf("不应 fatal: %s", errb)
	}
	var got struct {
		Proxy map[string]any `json:"proxy"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || !reflect.DeepEqual(got.Proxy, map[string]any{"pool": "p1", "id": "x2"}) {
		t.Fatalf("poll 输出应带绑定: %s (%v)", out, err)
	}

	// 按池名选也一样；池不存在退出。
	if _, errb, fatal, _ := runLogin(t, []string{"--proxies", pf, "--auths", dir, "--pool", "国内", "url"}, strings.NewReader(""),
		&http.Client{Transport: fake}); fatal || !strings.Contains(errb, "登录走代理「乙」") {
		t.Fatalf("按池名选: fatal=%v stderr=%q", fatal, errb)
	}
	if _, errb, fatal, _ := runLogin(t, []string{"--proxies", pf, "--auths", dir, "--pool", "nope", "url"}, strings.NewReader(""),
		&http.Client{Transport: fake}); !fatal || !strings.Contains(errb, `代理池 "nope" 不存在`) {
		t.Fatalf("池不存在: fatal=%v stderr=%q", fatal, errb)
	}
}

// 选了不绑：不走代理，state 文件和 poll 输出都没有代理。
func TestRunLoginNoPool(t *testing.T) {
	statePath := useStateFile(t)
	pf, dir := loginProxyFixture(t, `{}`)
	fake := loginFakeClient(t)
	if _, errb, fatal, _ := runLogin(t, []string{"--proxies", pf, "--auths", dir, "--pool=-", "url"}, strings.NewReader(""),
		&http.Client{Transport: fake}); fatal || strings.Contains(errb, "登录走代理") {
		t.Fatalf("fatal=%v stderr=%q", fatal, errb)
	}
	raw, _ := os.ReadFile(statePath)
	if strings.Contains(string(raw), `"proxy"`) || strings.Contains(string(raw), `"pool"`) {
		t.Fatalf("state 文件不该有代理: %s", raw)
	}
	out, _, fatal, _ := runLogin(t, []string{"--proxies", filepath.Join(dir, "workbuddy-bad.json"), "poll"}, strings.NewReader(""), &http.Client{Transport: fake})
	if fatal || strings.Contains(out, `"proxy"`) {
		t.Fatalf("没走代理的 poll 不读代理池、输出不带绑定: fatal=%v %s", fatal, out)
	}
}

// 出错退出：只许走代理时选了不绑、代理池文件坏了（url 和要走代理的 poll 都要读）、凭证目录名不对、参数缺值。
func TestRunLoginProxyErrors(t *testing.T) {
	statePath := useStateFile(t)
	fake := loginFakeClient(t)
	pf, dir := loginProxyFixture(t, `{"require_bound":true}`)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"只许走代理时选了不绑", []string{"--proxies", pf, "--auths", dir, "--pool", "-", "url"}, "只许走号的代理"},
		{"代理池文件坏了", []string{"--proxies", dir + "/workbuddy-bad.json", "url"}, "解析代理池"},
		{"凭证目录名不对", []string{"--proxies", pf, "--auths", filepath.Join(t.TempDir(), "["), "url"}, "list auths"},
		{"参数缺值", []string{"--auths"}, "--auths requires a value"},
	}
	for _, tc := range cases {
		if _, errb, fatal, _ := runLogin(t, tc.args, strings.NewReader(""), &http.Client{Transport: fake}); !fatal || !strings.Contains(errb, tc.want) {
			t.Errorf("%s: fatal=%v stderr=%q", tc.name, fatal, errb)
		}
	}

	if err := os.WriteFile(statePath, []byte(`{"state":"st-1","realm":"cn","trace":{"proxy":"x2"},"pool":"p1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errb, fatal, _ := runLogin(t, []string{"--proxies", dir + "/workbuddy-bad.json", "poll"}, strings.NewReader(""),
		&http.Client{Transport: fake}); !fatal || !strings.Contains(errb, "解析代理池") {
		t.Fatalf("要走代理的 poll 读不了代理池应退出: fatal=%v stderr=%q", fatal, errb)
	}
}
