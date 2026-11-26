// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newIOPanel 构建导入导出测试用的面板：上游一律 400（导入后的签到 / 激活 / 余额查询
// 只会失败记日志，不联网），凭证落到临时目录。
func newIOPanel(t *testing.T, auths ...*auth.Auth) (*Panel, string) {
	t.Helper()
	p := pool.New("")
	for _, a := range auths {
		p.Add(a)
	}
	up := &upstream.Client{
		HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":400}`))}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
	dir := t.TempDir()
	return New(Config{Version: "test", Pool: p, Upstream: up, AuthDir: dir}), dir
}

func postFile(t *testing.T, p *Panel, path string, content []byte) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "accounts.json")
	_, _ = fw.Write(content)
	_ = mw.Close()
	req := httptest.NewRequest("POST", path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%s: code=%d body=%s", path, rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: bad json %s", path, rec.Body)
	}
	return out
}

func loadAuth(t *testing.T, dir, uid string) *auth.Auth {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "workbuddy-"+uid+".json"))
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	a, err := auth.Parse(raw)
	if err != nil {
		t.Fatalf("parse auth file: %v", err)
	}
	return a
}

// 导出再导入不丢信息：企业 ID、域、设备 token 原样回来（cockpit 格式做不到）；
// 批量导出得到数组，单个对象文件同样能导入。
func TestAccountsExportImportRoundTrip(t *testing.T) {
	ent := &auth.Auth{
		UID: "ent-1", AccessToken: "at-ent", RefreshToken: "rt-ent", ExpiresAt: 1900000000,
		Domain: "copilot.tencent.com", EnterpriseID: "corp-9", Nickname: "企业号", DeviceToken: "dev-tok",
	}
	_, _ = ent.BackfillRealm()
	glb := &auth.Auth{
		UID: "glb-1", AccessToken: "at-glb", RefreshToken: "rt-glb", ExpiresAt: 1900000000,
		Domain: "www.workbuddy.ai", Nickname: "国际号",
	}
	if _, err := auth.BackfillRealmFor(glb, "global"); err != nil {
		t.Fatal(err)
	}
	src, _ := newIOPanel(t, ent, glb)

	rec := postJSON(t, src, "/panel/api/accounts/export", `{"uids":["ent-1","glb-1","ent-1"]}`)
	if rec.Code != 200 {
		t.Fatalf("export code=%d body=%s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("凭证响应必须 no-store，got %q", rec.Header().Get("Cache-Control"))
	}
	var exp struct {
		Accounts []json.RawMessage `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &exp); err != nil || len(exp.Accounts) != 2 {
		t.Fatalf("want 2 docs (重复 uid 只出一次), got %d err=%v body=%s", len(exp.Accounts), err, rec.Body)
	}

	dst, dir := newIOPanel(t)
	batch, _ := json.Marshal(exp.Accounts)
	if out := postFile(t, dst, "/panel/api/import", batch); out["imported"] != float64(2) {
		t.Fatalf("batch import: %v", out)
	}
	got := loadAuth(t, dir, "ent-1")
	if got.EnterpriseID != "corp-9" || got.DeviceToken != "dev-tok" || got.Realm() != "cn" ||
		got.AccessToken != "at-ent" || got.RefreshToken != "rt-ent" || got.Nickname != "企业号" || got.ExpiresAt != 1900000000 {
		t.Fatalf("企业号导回来丢了字段: %+v realm=%s", got, got.Realm())
	}
	if g := loadAuth(t, dir, "glb-1"); g.Realm() != "global" {
		t.Fatalf("国际号导回来域变了: %s", g.Realm())
	}
	if dst.cfg.Pool.AuthByUID("ent-1") == nil || dst.cfg.Pool.AuthByUID("glb-1") == nil {
		t.Fatal("导入后应热加载进池")
	}

	// 单个导出的文件是一个对象（workbuddy-<uid>.json 原样），也要能导入。
	single, dir2 := newIOPanel(t)
	if out := postFile(t, single, "/panel/api/import", exp.Accounts[0]); out["imported"] != float64(1) {
		t.Fatalf("single import: %v", out)
	}
	if loadAuth(t, dir2, "ent-1").EnterpriseID != "corp-9" {
		t.Fatal("单个对象导入丢了企业 ID")
	}
}

// 有 uid 不在池里时整单拒绝：少导一个号而用户以为导全了，比报错更糟。
func TestAccountsExportMissingUID(t *testing.T) {
	p, _ := newIOPanel(t, &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt"})
	if rec := postJSON(t, p, "/panel/api/accounts/export", `{"uids":["u1","nope"]}`); rec.Code != 404 || !strings.Contains(rec.Body.String(), "nope") {
		t.Fatalf("missing uid: code=%d body=%s", rec.Code, rec.Body)
	}
	if rec := postJSON(t, p, "/panel/api/accounts/export", `{"uids":[]}`); rec.Code != 400 {
		t.Fatalf("empty uids: code=%d", rec.Code)
	}
}

// cockpit tools 格式照常可导入（新旧两个路径），expires_at 兼容毫秒与秒，域按显式 realm 或 domain（含 codebuddy.ai）推断；缺字段的项跳过并报原因。
func TestImportCockpitFormat(t *testing.T) {
	file := []byte(`[
		{"id":"c1","uid":"ck-1","access_token":"at1","refresh_token":"rt1","expires_at":1900000000000,"domain":"www.workbuddy.ai","email":"a@example.com"},
		{"id":"c2","uid":"ck-2","access_token":"","refresh_token":"rt2"},
		{"id":"c3","uid":"ck-sec","access_token":"at3","refresh_token":"rt3","expires_at":1900000000,"domain":"www.codebuddy.ai","nickname":"秒级"},
		{"id":"c4","uid":"ck-rlm","access_token":"at4","refresh_token":"rt4","expires_at":0,"realm":"global","nickname":"显式国际"}
	]`)
	for _, path := range []string{"/panel/api/import", "/panel/api/import/cockpit"} {
		p, dir := newIOPanel(t)
		out := postFile(t, p, path, file)
		if out["imported"] != float64(3) || out["skipped"] != float64(1) {
			t.Fatalf("%s: %v", path, out)
		}
		a := loadAuth(t, dir, "ck-1")
		if a.Realm() != "global" || a.ExpiresAt != 1900000000 || a.Nickname != "a@example.com" {
			t.Fatalf("%s: cockpit 毫秒转换不对: %+v realm=%s", path, a, a.Realm())
		}
		sec := loadAuth(t, dir, "ck-sec")
		if sec.Realm() != "global" || sec.ExpiresAt != 1900000000 || sec.Nickname != "秒级" {
			t.Fatalf("%s: cockpit 秒级或 codebuddy.ai 转换不对: %+v realm=%s", path, sec, sec.Realm())
		}
		rlm := loadAuth(t, dir, "ck-rlm")
		if rlm.Realm() != "global" || rlm.ExpiresAt != 0 || !rlm.NeedsRefresh(0) {
			t.Fatalf("%s: cockpit 显式 realm 或空到期时间不对: %+v realm=%s", path, rlm, rlm.Realm())
		}
	}
}

// 坏文件整体拒收（400）；坏的单项跳过并报原因，其余照常导入。
func TestImportRejectsBadInput(t *testing.T) {
	p, dir := newIOPanel(t)
	for name, content := range map[string]string{
		"不是 JSON":  `{broken`,
		"空数组":      `[]`,
		"数组元素不是对象": `["x"]`,
	} {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "a.json")
		_, _ = fw.Write([]byte(content))
		_ = mw.Close()
		req := httptest.NewRequest("POST", "/panel/api/import", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		want := 400
		if name == "数组元素不是对象" {
			want = 200 // 能拆成项，单项解析失败按跳过处理
		}
		if rec.Code != want {
			t.Errorf("%s: code=%d want %d body=%s", name, rec.Code, want, rec.Body)
		}
	}
	// 没带 file 字段 / 不是 multipart。
	for _, ct := range []string{"multipart/form-data; boundary=x", "application/json"} {
		req := httptest.NewRequest("POST", "/panel/api/import", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Errorf("Content-Type=%s: code=%d want 400", ct, rec.Code)
		}
	}

	out := postFile(t, p, "/panel/api/import", []byte(`[
		{"auth":{"accessToken":"at","refreshToken":""},"account":{"uid":"gw-1"}},
		{"auth":{"accessToken":"at","refreshToken":"rt"},"account":{"uid":"../evil"}},
		{"id":"c3","uid":"../evil","access_token":"at","refresh_token":"rt"},
		{"id":"c4","uid":"ck-4","access_token":"at","refresh_token":"rt","expires_at":0,"nickname":"四号"}
	]`))
	if out["imported"] != float64(1) || out["skipped"] != float64(3) {
		t.Fatalf("应导入 1 个、跳过 3 个: %v", out)
	}
	errs, _ := out["errors"].([]any)
	joined := ""
	for _, e := range errs {
		joined += e.(string) + "\n"
	}
	for _, want := range []string{"missing required fields (uid=gw-1)", "invalid uid (uid=../evil)", "invalid uid (id=c3)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("跳过原因缺 %q: %s", want, joined)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.json")); err == nil {
		t.Error("非法 uid 不得写出目录外的文件")
	}
	// cockpit 没给过期时间：保留 0 让首次使用或保活刷新补齐真实到期时间，昵称原样保留。
	a := loadAuth(t, dir, "ck-4")
	if a.ExpiresAt != 0 || !a.NeedsRefresh(0) || a.Nickname != "四号" {
		t.Errorf("缺省过期时间或昵称不对: exp=%d nick=%q", a.ExpiresAt, a.Nickname)
	}
}
