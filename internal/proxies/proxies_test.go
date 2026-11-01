// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package proxies

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestOpen 文件不存在是空池；坏 JSON、代理所在的池不存在、读不了都报错；设置补默认值；落盘 0600。
func TestOpen(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(filepath.Join(dir, "none.json"))
	if err != nil || r.Enabled() || len(r.View().Proxies) != 0 {
		t.Fatalf("不存在的文件应是空池: %v", err)
	}
	if got := r.Settings(); got.HealthMinutes != DefaultHealthMinutes || got.ExitMinutes != DefaultExitMinutes ||
		got.RebindMinutes != DefaultRebindMinutes || got.ExpiryWarnDays != DefaultExpiryWarnDays {
		t.Fatalf("默认设置不对: %+v", got)
	}

	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, body := range map[string]string{
		"bad.json":  `{`,
		"orph.json": `{"pools":[],"proxies":[{"id":"x","name":"孤儿","url":"http://h:1","pool":"nope"}]}`,
	} {
		if _, err := Open(write(name, body)); err == nil {
			t.Fatalf("%s 应报错", name)
		}
	}
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "读代理池") {
		t.Fatalf("目录读不了应报错: %v", err)
	}

	p := write("ok.json", `{"settings":{"require_bound":true,"health_minutes":-1,"exit_ip_urls":[" ","http://x "]},
		"pools":[{"id":"p1","name":"池"}],"proxies":[{"id":"x1","name":"a","url":"http://h:1","pool":"p1"}]}`)
	r, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if s := r.Settings(); !s.RequireBound || s.HealthMinutes != DefaultHealthMinutes || !reflect.DeepEqual(s.ExitIPURLs, []string{"http://x"}) {
		t.Fatalf("设置没补默认值或没去空地址: %+v", s)
	}
	if !r.Enabled() {
		t.Fatal("有池就算启用")
	}
	if _, err := r.UpdateSettings(Settings{}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"exit.example", "ftp://x", "http://", "http://%zz"} {
		if _, err := r.UpdateSettings(Settings{ExitIPURLs: []string{bad}}); err == nil || !strings.Contains(err.Error(), "查出口 IP 的地址") {
			t.Fatalf("%q 应报错: %v", bad, err)
		}
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("落盘权限应为 0600: %v %v", st.Mode(), err)
	}
}

// TestOpenReadOnly 只读打开：改动只在内存里，不写 proxies.json，也不写凭证文件。
func TestOpenReadOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "proxies.json")
	if _, err := OpenReadOnly(filepath.Join(dir, "x", "bad")); err != nil {
		t.Fatalf("不存在的文件只读打开也应成功: %v", err)
	}
	if err := os.WriteFile(p, []byte(`{"pools":[{"id":"p1","name":"池"}],"proxies":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(filepath.Join(dir, "missing-dir", "f.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(filepath.Join(dir, "bad.json")); err == nil {
		t.Fatal("坏文件只读打开也应报错")
	}
	r, err := OpenReadOnly(p)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	if _, err := r.Import("http://h:1 a", "http", "p1", nil); err != nil {
		t.Fatal(err)
	}
	a := &auth.Auth{UID: "u1", AccessToken: "at", FilePath: filepath.Join(dir, "workbuddy-u1.json")}
	r.Track(a, true)
	if _, err := r.Bind([]string{"u1"}, BindTarget{Pool: "p1"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("只读打开不应写 proxies.json")
	}
	if _, err := os.Stat(a.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("只读打开不应写凭证文件")
	}
	if a.ProxyBinding().ID != "" {
		t.Fatal("只读打开不应改凭证对象里的绑定")
	}
	if r.Binding("u1").ID == "" {
		t.Fatal("内存里应已分到代理")
	}
}

// TestPools 建池、改池、删池的校验；默认池每个域只能有一个；设默认池可把现有没绑的号放进来。
func TestPools(t *testing.T) {
	tr := newReg(t)
	for _, in := range []PoolInput{{Name: " "}, {Name: "a", MaxPerProxy: -1}} {
		if _, err := tr.CreatePool(in); err == nil {
			t.Fatalf("%+v 应报错", in)
		}
	}
	p1 := tr.pool(t, PoolInput{Name: "海外", DefaultGlobal: true})
	if _, err := tr.CreatePool(PoolInput{Name: "海外"}); err == nil {
		t.Fatal("重名应报错")
	}
	tr.add(t, p1, "http://h1:1 a", "http://h2:1 b")
	u1, u2, u3 := tr.acct(t, "u1", "cn"), tr.acct(t, "u2", "cn"), tr.acct(t, "u3", "global")
	for _, a := range []*auth.Auth{u1, u2, u3} {
		tr.Track(a, false)
	}
	if !tr.Binding("u3").Empty() {
		t.Fatal("启动加载的老号不应自动进默认池")
	}
	p2 := tr.pool(t, PoolInput{Name: "国内", DefaultCN: true, DefaultGlobal: true, AdoptUnbound: true, MaxPerProxy: 1})
	v := tr.View()
	if !reflect.DeepEqual(v.Pools[0].DefaultFor, []string(nil)) && len(v.Pools[0].DefaultFor) != 0 {
		t.Fatalf("新默认池应取消旧池的默认: %+v", v.Pools[0])
	}
	if !reflect.DeepEqual(v.Pools[1].DefaultFor, []string{"cn", "global"}) {
		t.Fatalf("默认池 = %v", v.Pools[1].DefaultFor)
	}
	for _, uid := range []string{"u1", "u2", "u3"} {
		if b := tr.Binding(uid); b.Pool != p2 || b.ID != "" {
			t.Fatalf("%s 应进国内池等代理: %+v", uid, b)
		}
	}
	ids := tr.add(t, p2, "http://c1:1 c1")
	if tr.Binding("u1").ID != ids[0] || tr.Binding("u2").ID != "" {
		t.Fatalf("上限 1：只有 u1 分到代理: %+v %+v", tr.Binding("u1"), tr.Binding("u2"))
	}
	if err := tr.UpdatePool(p2, PoolInput{Name: "国内", MaxPerProxy: 3, DefaultCN: true}); err != nil {
		t.Fatal(err)
	}
	if tr.Binding("u2").ID != ids[0] || tr.Binding("u3").ID != ids[0] {
		t.Fatal("调高上限后等代理的号应补上")
	}
	if err := tr.UpdatePool("nope", PoolInput{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("改不存在的池: %v", err)
	}
	if err := tr.UpdatePool(p2, PoolInput{Name: "海外"}); err == nil {
		t.Fatal("改成重名应报错")
	}
	if err := tr.DeletePool(p1); err == nil || !strings.Contains(err.Error(), "还有代理") {
		t.Fatalf("池里有代理不让删: %v", err)
	}
	if err := tr.DeletePool("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删不存在的池: %v", err)
	}
	p3 := tr.pool(t, PoolInput{Name: "空池"})
	if _, err := tr.Bind([]string{"u1"}, BindTarget{Pool: p3}); err != nil {
		t.Fatal(err)
	}
	if b := tr.Binding("u1"); b.Pool != p3 || b.ID != "" {
		t.Fatalf("绑空池应等代理: %+v", b)
	}
	if err := tr.DeletePool(p3); err != nil {
		t.Fatal(err)
	}
	if !tr.Binding("u1").Empty() {
		t.Fatal("删池后在等代理的号应解绑")
	}
}

// TestSaveFailureReverts 写盘失败就整个撤回这次改动。
func TestSaveFailureReverts(t *testing.T) {
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "池"})
	tr.path = filepath.Join(tr.dir, "missing", "proxies.json")
	if _, err := tr.CreatePool(PoolInput{Name: "新池"}); !errors.Is(err, ErrSave) || !strings.Contains(err.Error(), "写不进代理池文件") {
		t.Fatalf("写不了应报错: %v", err)
	}
	if _, err := tr.Import("http://h:1", "http", p1, nil); err == nil {
		t.Fatal("导入写不了应报错")
	}
	if v := tr.View(); len(v.Pools) != 1 || len(v.Proxies) != 0 {
		t.Fatalf("写不了应撤回: %+v", v)
	}
	// 目标是个目录：WriteFile 成功、Rename 失败。
	tr.path = tr.dir
	if _, err := tr.CreatePool(PoolInput{Name: "新池"}); err == nil {
		t.Fatal("改名失败应报错")
	}
	_ = os.Remove(tr.dir + ".tmp")
}

// TestUpdateProxy 改名、换地址（健康状态清空）、换池（号跟着换）、到期日设与清；各种报错。
func TestUpdateProxy(t *testing.T) {
	tr := newReg(t)
	p1, p2 := tr.pool(t, PoolInput{Name: "一"}), tr.pool(t, PoolInput{Name: "二"})
	ids := tr.add(t, p1, "http://u:pw@h1:1 a", "http://h2:1 b")
	tr.Track(tr.acct(t, "u1", "cn"), false)
	if _, err := tr.Bind([]string{"u1"}, BindTarget{Proxy: ids[0]}); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	tr.proxyLocked(ids[0]).Health = Health{State: "ok", LatencyMs: 5}
	tr.mu.Unlock()

	if err := tr.UpdateProxy("nope", ProxyInput{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在: %v", err)
	}
	if err := tr.UpdateProxy(ids[0], ProxyInput{URL: "ftp://h:1"}); err == nil {
		t.Fatal("坏地址应报错")
	}
	if err := tr.UpdateProxy(ids[0], ProxyInput{URL: "http://h2:1"}); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("改成和别的重复应报错: %v", err)
	}
	if err := tr.UpdateProxy(ids[0], ProxyInput{Pool: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("换到不存在的池: %v", err)
	}
	// 打码地址原样提交 = 不改。
	if err := tr.UpdateProxy(ids[0], ProxyInput{Name: " 新名 ", URL: "http://u:***@h1:1"}); err != nil {
		t.Fatal(err)
	}
	v := tr.View()
	if v.Proxies[0].Name != "新名" || v.Proxies[0].Health.State != "ok" {
		t.Fatalf("改名不应动健康状态: %+v", v.Proxies[0])
	}
	// 只改端口、密码段留着 ***：沿用原密码。
	if err := tr.UpdateProxy(ids[0], ProxyInput{URL: "http://u:***@h1:2"}); err != nil || tr.ProxyURL("u1") != "http://u:pw@h1:2" {
		t.Fatalf("应沿用原密码: %q %v", tr.ProxyURL("u1"), err)
	}
	// 原来没密码、新地址写的 ***，或者新地址根本没有用户名：照新地址。
	if keepPassword("http://u:***@h:1", "http://h:1") != "http://u:***@h:1" ||
		keepPassword("http://h:1", "http://u:pw@h:1") != "http://h:1" ||
		keepPassword("http://u:***@h:1", "http://u@h:1") != "http://u:***@h:1" ||
		keepPassword("http://u:x@h:1", "http://u:pw@h:1") != "http://u:x@h:1" {
		t.Fatal("keepPassword 不对")
	}
	exp := t0.Add(48 * time.Hour)
	if err := tr.UpdateProxy(ids[0], ProxyInput{URL: "http://u:new@h1:1", Pool: p2, ExpiresAt: &exp}); err != nil {
		t.Fatal(err)
	}
	v = tr.View()
	if v.Proxies[0].Health.State != "" || v.Proxies[0].Pool != p2 || v.Proxies[0].ExpiresAt == nil || !v.Proxies[0].ExpiresAt.Equal(exp) {
		t.Fatalf("换地址、换池、设到期: %+v", v.Proxies[0])
	}
	if b := tr.Binding("u1"); b.Pool != p2 || b.ID != ids[0] || !b.Manual {
		t.Fatalf("号应跟着代理换池: %+v", b)
	}
	if err := tr.UpdateProxy(ids[0], ProxyInput{ClearExpiry: true}); err != nil {
		t.Fatal(err)
	}
	if tr.View().Proxies[0].ExpiresAt != nil {
		t.Fatal("应去掉到期日")
	}
}

// TestDeleteProxies 删代理后绑在上面的号改绑到同池别的代理，没有空位就等代理；一键删不可用的。
func TestDeleteProxies(t *testing.T) {
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "池", MaxPerProxy: 1})
	ids := tr.add(t, p1, "http://h1:1", "http://h2:1", "http://h3:1")
	for _, uid := range []string{"u1", "u2"} {
		tr.Track(tr.acct(t, uid, "cn"), false)
	}
	if _, err := tr.Bind([]string{"u1", "u2"}, BindTarget{Pool: p1}); err != nil {
		t.Fatal(err)
	}
	if n, err := tr.DeleteProxies([]string{tr.Binding("u1").ID, "nope"}); err != nil || n != 1 {
		t.Fatalf("删了 %d 个（%v）", n, err)
	}
	if b := tr.Binding("u1"); b.ID == "" {
		t.Fatalf("u1 应改绑到空着的代理: %+v", b)
	}
	if n, err := tr.DeleteProxies(nil); err != nil || n != 0 {
		t.Fatalf("什么都不删: %d %v", n, err)
	}
	if _, _, err := tr.HoldForLogin(p1, "cn"); err == nil {
		t.Fatal("两个代理都绑满了，登录占位应失败")
	}
	tr.add(t, p1, "http://h4:1")
	hold, proxy, err := tr.HoldForLogin(p1, "cn")
	if err != nil || hold == "" || proxy == "" {
		t.Fatalf("登录占位: %q %q %v", hold, proxy, err)
	}
	// 让两个绑了号的代理都不通，一键删掉；号没地方去就等代理，登录占位也放掉。
	tr.mu.Lock()
	for _, px := range tr.proxies {
		px.Health.State = "down"
	}
	tr.mu.Unlock()
	n, err := tr.DeleteUnavailable()
	if err != nil || n != len(ids) {
		t.Fatalf("一键删不可用: %d %v", n, err)
	}
	if b := tr.Binding("u1"); b.Pool != p1 || b.ID != "" {
		t.Fatalf("没空位应等代理: %+v", b)
	}
	if len(tr.logins) != 0 {
		t.Fatal("被删代理上的登录占位应放掉")
	}
	if !strings.Contains(tr.gate("u1"), "等代理") {
		t.Fatalf("等代理应推给号池: %q", tr.gate("u1"))
	}
}

// TestChangeSavesAuth 绑定改了写回凭证文件；写不进去只记 WARN。
func TestChangeSavesAuth(t *testing.T) {
	logs := captureLog(t)
	tr := newReg(t)
	p1 := tr.pool(t, PoolInput{Name: "池"})
	ids := tr.add(t, p1, "http://h1:1")
	a := tr.acct(t, "u1", "cn")
	tr.Track(a, false)
	if _, err := tr.Bind([]string{"u1"}, BindTarget{Pool: p1}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(a.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Proxy auth.ProxyBinding `json:"proxy"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Proxy != (auth.ProxyBinding{Pool: p1, ID: ids[0]}) {
		t.Fatalf("凭证里的绑定 = %+v（%v）", doc.Proxy, err)
	}
	b := tr.acct(t, "u2", "cn")
	b.FilePath = filepath.Join(tr.dir, "missing", "workbuddy-u2.json")
	tr.Track(b, false)
	if _, err := tr.Bind([]string{"u2"}, BindTarget{Pool: p1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "写不进凭证文件") {
		t.Fatalf("写不进凭证应记 WARN:\n%s", logs)
	}
	c := tr.acct(t, "u3", "cn")
	c.FilePath = ""
	tr.Track(c, false)
	if _, err := tr.Bind([]string{"u3"}, BindTarget{Pool: p1}); err != nil || c.ProxyBinding().ID == "" {
		t.Fatalf("没有凭证文件的号也要改内存里的绑定: %v %+v", err, c.ProxyBinding())
	}
}

// TestExitGeo 地区去重、运营商用「·」接在后面。
func TestExitGeo(t *testing.T) {
	for _, c := range []struct {
		e    *Exit
		want string
	}{
		{nil, ""},
		{&Exit{}, ""},
		{&Exit{Country: "中国", Region: "香港", City: "香港", ISP: "某运营商"}, "中国 香港 · 某运营商"},
		{&Exit{ISP: "只有运营商"}, "只有运营商"},
	} {
		if got := c.e.Geo(); got != c.want {
			t.Errorf("Geo(%+v) = %q，应为 %q", c.e, got, c.want)
		}
	}
	if short("12345678901") != "12345678" || short("abc") != "abc" {
		t.Fatal("short 不对")
	}
}
