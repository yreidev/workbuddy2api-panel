// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// credit/main_test.go 积分查询工具：假 Transport 按 host + path 回官方积分接口的响应，
// 覆盖国内/国际个人号、企业号、查询失败、无 token、坏文件跳过、-pretty 与 WB2A_AUTH_DIR。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// enterpriseUsagePath 企业号额度接口（与 internal/upstream 同口径）。
const enterpriseUsagePath = "/v2/billing/meter/get-enterprise-user-usage"

type creditRT func(*http.Request) (*http.Response, error)

func (f creditRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func creditResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(strings.NewReader(body))}
}

// creditFake 记录请求，并按账号 UID 回官方三接口与企业用量接口的响应。
type creditFake struct {
	mu   sync.Mutex
	recs []string
}

// packages 各个人号的逐包响应（额度按 *Precise 字符串）。
func creditPackages(uid string) []string {
	switch uid {
	case "u-cn":
		return []string{upstreamtest.Package(100, 60, "")}
	case "u-glb":
		return []string{upstreamtest.Package(50, 30, "")}
	}
	return nil
}

func (f *creditFake) RoundTrip(r *http.Request) (*http.Response, error) {
	host, path, uid := r.URL.Host, r.URL.Path, r.Header.Get("X-User-Id")
	f.mu.Lock()
	f.recs = append(f.recs, host+path)
	f.mu.Unlock()

	if path == enterpriseUsagePath {
		if uid != "u-ent" {
			return creditResponse(400, `{"code":400,"msg":"not enterprise"}`), nil
		}
		return creditResponse(200, `{"code":0,"data":{"credit":20,"limitNum":100}}`), nil
	}
	// 未列出的个人号都返回失败，便于覆盖失败分支。
	if uid != "u-cn" && uid != "u-glb" && path == upstreamtest.FreePath {
		return creditResponse(400, `{"code":400,"msg":"balance boom"}`), nil
	}
	if status, body, ok := upstreamtest.Respond(path, creditPackages(uid)...); ok {
		return creditResponse(status, body), nil
	}
	return creditResponse(400, `{"code":400,"msg":"unexpected"}`), nil
}

func (f *creditFake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.recs...)
}

func billingHostCN() string     { return strings.TrimPrefix(billingBaseCN, "https://") }
func billingHostGlobal() string { return strings.TrimPrefix(billingBaseGlobal, "https://") }

func writeCreditAuth(t *testing.T, dir, uid, nick, token, realm string) {
	t.Helper()
	domain := "www.codebuddy.cn"
	if realm == "global" {
		domain = "www.workbuddy.ai"
	}
	writeCreditAuthWith(t, dir, uid, nick, token, realm, domain, "")
}

// writeCreditAuthWith 可指定 domain 与企业 ID，用来构造企业号等组合。
func writeCreditAuthWith(t *testing.T, dir, uid, nick, token, realm, domain, enterprise string) {
	t.Helper()
	raw := fmt.Sprintf(`{"auth":{"accessToken":%q,"domain":%q,"realm":%q},`+
		`"account":{"uid":%q,"enterpriseId":%q,"nickname":%q}}`, token, domain, realm, uid, enterprise, nick)
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-"+uid+".json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

type creditOutput struct {
	Service string `json:"service"`
	TS      int64  `json:"ts"`
	Total   struct {
		Remain   int64 `json:"remain"`
		Used     int64 `json:"used"`
		Size     int64 `json:"size"`
		Accounts int   `json:"accounts"`
		OK       int   `json:"ok"`
		Failed   int   `json:"failed"`
	} `json:"total"`
	Accounts []struct {
		UID       string `json:"uid"`
		Nickname  string `json:"nickname"`
		Remain    *int64 `json:"remain"`
		Used      *int64 `json:"used"`
		Size      *int64 `json:"size"`
		Packages  int    `json:"packages"`
		Unlimited bool   `json:"unlimited"`
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
	} `json:"accounts"`
}

// setAccountGap0 测试里去掉账号间的 200ms 间隔。
func setAccountGap0(t *testing.T) {
	t.Helper()
	old := accountGap
	accountGap = 0
	t.Cleanup(func() { accountGap = old })
}

// JSON 输出：国内/国际个人号、企业号、失败号、无 token、坏文件跳过与请求域。
func TestCreditRunJSON(t *testing.T) {
	setAccountGap0(t)
	dir := t.TempDir()
	writeCreditAuth(t, dir, "u-cn", "国内号", "at-cn", "cn")
	writeCreditAuth(t, dir, "u-notoken", "无令牌", "", "cn")
	writeCreditAuth(t, dir, "u-err", "接口错", "at-err", "cn")
	writeCreditAuth(t, dir, "u-glb", "国际号", "at-glb", "global")
	writeCreditAuthWith(t, dir, "u-ent", "企业号", "at-ent", "cn", "www.codebuddy.cn", "ent-9")
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-broken.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "workbuddy-dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WB2A_AUTH_DIR", dir)
	fake := &creditFake{}
	var out bytes.Buffer
	if code := run(nil, &out, io.Discard, &upstream.Client{HTTP: &http.Client{Transport: fake}}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	var got creditOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("输出不是 JSON: %v\n%s", err, out.String())
	}
	if got.Service != "workbuddy" || got.TS == 0 {
		t.Fatalf("service/ts 不对: %+v", got)
	}
	if got.Total.Remain != 170 || got.Total.Used != 80 || got.Total.Size != 250 ||
		got.Total.Accounts != 5 || got.Total.OK != 3 || got.Total.Failed != 2 {
		t.Fatalf("total 不对: %+v", got.Total)
	}
	if len(got.Accounts) != 5 {
		t.Fatalf("accounts = %d", len(got.Accounts))
	}
	idx := map[string]int{}
	for i, a := range got.Accounts {
		idx[a.UID] = i
	}
	cn := got.Accounts[idx["u-cn"]]
	if !cn.OK || cn.Remain == nil || *cn.Remain != 60 || *cn.Used != 40 || *cn.Size != 100 || cn.Packages != 1 {
		t.Fatalf("CN 成功账号字段不对: %+v", cn)
	}
	nt := got.Accounts[idx["u-notoken"]]
	if nt.OK || nt.Error != "no accessToken" {
		t.Fatalf("无 token 账号不对: %+v", nt)
	}
	er := got.Accounts[idx["u-err"]]
	if er.OK || !strings.Contains(er.Error, "http 400") {
		t.Fatalf("接口报错账号不对: %+v", er)
	}
	glb := got.Accounts[idx["u-glb"]]
	if !glb.OK || glb.Remain == nil || *glb.Remain != 30 || *glb.Used != 20 || *glb.Size != 50 || glb.Packages != 1 {
		t.Fatalf("global 个人号字段不对: %+v", glb)
	}
	ent := got.Accounts[idx["u-ent"]]
	if !ent.OK || ent.Remain == nil || *ent.Remain != 80 || *ent.Used != 20 || *ent.Size != 100 || ent.Packages != 0 {
		t.Fatalf("企业号字段不对（企业额度不在资源包体系，packages 应为 0）: %+v", ent)
	}

	// 请求域：国内号打 www.workbuddy.cn，国际号打 www.workbuddy.ai；企业号只打企业用量接口。
	cnReqs, globalReqs, entUsage := 0, 0, 0
	for _, r := range fake.requests() {
		switch {
		case strings.HasPrefix(r, billingHostCN()+upstreamtest.SummaryPath),
			strings.HasPrefix(r, billingHostCN()+upstreamtest.PaidPath),
			strings.HasPrefix(r, billingHostCN()+upstreamtest.FreePath):
			cnReqs++
		case strings.HasPrefix(r, billingHostGlobal()+upstreamtest.SummaryPath),
			strings.HasPrefix(r, billingHostGlobal()+upstreamtest.PaidPath),
			strings.HasPrefix(r, billingHostGlobal()+upstreamtest.FreePath):
			globalReqs++
		case r == billingHostCN()+enterpriseUsagePath:
			entUsage++
		default:
			t.Fatalf("请求域/路径不对: %s", r)
		}
	}
	// u-cn 与 u-err 在 CN 域，u-glb 的 3 条全发往 www.workbuddy.ai，企业号只发企业用量接口。
	if cnReqs != 6 || globalReqs != 3 || entUsage != 1 {
		t.Fatalf("请求分布 = CN %d / global %d / 企业 %d，want 6/3/1", cnReqs, globalReqs, entUsage)
	}
}

// 企业号查询失败：错误原样带出，不退回个人资源包接口。
func TestCreditEnterpriseError(t *testing.T) {
	setAccountGap0(t)
	dir := t.TempDir()
	writeCreditAuthWith(t, dir, "u-ent", "企业号", "at-ent", "cn", "www.codebuddy.cn", "ent-9")
	t.Setenv("WB2A_AUTH_DIR", dir)

	paths := map[string]int{}
	client := &http.Client{Transport: creditRT(func(r *http.Request) (*http.Response, error) {
		paths[r.URL.Path]++
		return creditResponse(400, `{"code":10001,"msg":"enterprise boom"}`), nil
	})}
	var out bytes.Buffer
	if code := run(nil, &out, io.Discard, &upstream.Client{HTTP: client}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	var got creditOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("输出不是 JSON: %v", err)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].OK || !strings.Contains(got.Accounts[0].Error, "http 400") {
		t.Fatalf("企业号失败结果不对: %+v", got.Accounts)
	}
	if got.Total.OK != 0 || got.Total.Failed != 1 {
		t.Fatalf("total 不对: %+v", got.Total)
	}
	if paths[enterpriseUsagePath] != 1 || paths[upstreamtest.FreePath] != 0 {
		t.Fatalf("企业号应只调企业用量接口: %v", paths)
	}
}

// -pretty：四行汇总 + 失败明细，不输出 JSON；WB2A_AUTH_DIR 生效。
func TestCreditPrettyAndAuthDir(t *testing.T) {
	setAccountGap0(t)
	dir := t.TempDir()
	writeCreditAuth(t, dir, "u-cn", "国内号", "at-cn", "cn")
	writeCreditAuth(t, dir, "u-error-long", "", "at-err", "cn") // 无昵称：失败行回落 uid 前 8 位
	t.Setenv("WB2A_AUTH_DIR", dir)

	fake := &creditFake{}
	var out bytes.Buffer
	if code := run([]string{"-pretty"}, &out, io.Discard, &upstream.Client{HTTP: &http.Client{Transport: fake}}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	text := out.String()
	if strings.Contains(text, `{"service"`) {
		t.Fatalf("pretty 不应输出 JSON:\n%s", text)
	}
	for _, want := range []string{
		"📊 WorkBuddy 积分日报",
		"账号: 1/2",
		"总计: 60/100",
		"剩余: 60%",
		"⚠️ u-error- upstream client (http 400)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("pretty 输出缺少 %q:\n%s", want, text)
		}
	}
}

// 没有凭证：输出空 accounts 与零汇总，仍返回 0。
func TestCreditEmpty(t *testing.T) {
	setAccountGap0(t)
	t.Setenv("WB2A_AUTH_DIR", t.TempDir())
	var out bytes.Buffer
	if code := run(nil, &out, io.Discard, &upstream.Client{HTTP: &http.Client{Transport: &creditFake{}}}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	var got creditOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("输出不是 JSON: %v", err)
	}
	if got.Total.Accounts != 0 || got.Total.OK != 0 || got.Total.Failed != 0 || len(got.Accounts) != 0 {
		t.Fatalf("空目录输出不对: %+v", got)
	}
}

// credentialAuth：显式 realm 优先、缺省按 domain 推断、没有到期时间补远期。
func TestCredentialAuthRealm(t *testing.T) {
	cases := []struct {
		name   string
		realm  string
		domain string
		want   string
	}{
		{"显式 global", "global", "www.codebuddy.cn", "global"},
		{"GLOBAL 大写同 global（与旧 billingBaseFor 一致）", "GLOBAL", "www.codebuddy.cn", "global"},
		{"workbuddy.ai 域", "", "www.workbuddy.ai", "global"},
		{"裸 workbuddy.ai", "", "workbuddy.ai", "global"},
		{"CN 域", "", "www.codebuddy.cn", "cn"},
		{"空", "", "", "cn"},
		{"非法 realm 按域回落", "weird", "www.workbuddy.ai", "global"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var af authFile
			af.Auth.Realm, af.Auth.Domain = c.realm, c.domain
			a := credentialAuth(&af)
			if got := a.Realm(); got != c.want {
				t.Fatalf("Realm() = %q want %q", got, c.want)
			}
			if a.ExpiresAt <= 0 {
				t.Fatalf("没有到期时间时应补远期，实际 %d", a.ExpiresAt)
			}
		})
	}
}

// 企业号不限量（limitNum=-1）：标 unlimited、额度留空、不计入总计，-pretty 算作有余额；
// 凭证里 realm 写成大写 GLOBAL 也按国际版查。
func TestCreditUnlimitedAndRealmCase(t *testing.T) {
	setAccountGap0(t)
	dir := t.TempDir()
	writeCreditAuthWith(t, dir, "u-ent", "不限量", "at-ent", "cn", "www.codebuddy.cn", "ent-1")
	writeCreditAuthWith(t, dir, "u-glb", "国际号", "at-glb", "GLOBAL", "", "")
	t.Setenv("WB2A_AUTH_DIR", dir)

	var hosts []string
	client := &http.Client{Transport: creditRT(func(r *http.Request) (*http.Response, error) {
		hosts = append(hosts, r.URL.Host+r.URL.Path)
		if r.URL.Path == enterpriseUsagePath {
			return creditResponse(200, `{"code":0,"data":{"credit":5,"limitNum":-1}}`), nil
		}
		status, body, _ := upstreamtest.Respond(r.URL.Path, upstreamtest.Package(50, 30, ""))
		return creditResponse(status, body), nil
	})}
	var out bytes.Buffer
	if code := run(nil, &out, io.Discard, &upstream.Client{HTTP: client}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	var got creditOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("输出不是 JSON: %v", err)
	}
	var ent, glb = got.Accounts[0], got.Accounts[1]
	if ent.UID != "u-ent" || !ent.OK || !ent.Unlimited || ent.Remain != nil || ent.Used != nil || ent.Size != nil {
		t.Fatalf("不限量企业号结果不对: %+v", ent)
	}
	if !glb.OK || glb.Remain == nil || *glb.Remain != 30 {
		t.Fatalf("国际号结果不对: %+v", glb)
	}
	if got.Total.Remain != 30 || got.Total.Size != 50 || got.Total.OK != 2 {
		t.Fatalf("total 不该算进不限量账号: %+v", got.Total)
	}
	for _, h := range hosts {
		if strings.HasSuffix(h, upstreamtest.FreePath) && !strings.HasPrefix(h, billingHostGlobal()) {
			t.Fatalf("realm=GLOBAL 应发往国际版域: %v", hosts)
		}
	}

	out.Reset()
	if code := run([]string{"-pretty"}, &out, io.Discard, &upstream.Client{HTTP: client}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out.String(), "账号: 2/2") {
		t.Fatalf("-pretty 应把不限量算作有余额:\n%s", out.String())
	}
}
