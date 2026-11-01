// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

// proxies_test.go 代理池的面板接口（proxies.go），以及登录、导入、导出、删号、总览里跟代理池有关的部分。
// 代理池放在临时目录；测连通换成假的（地址里带 down 的不通），代理都指向本机没人听的端口，查出口 IP
// 连不出去。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
)

// newProxyReg 临时目录里的代理池；返回 proxies.json 所在的目录（删掉它就写不进去）。
func newProxyReg(t *testing.T) (*proxies.Registry, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	reg, err := proxies.Open(filepath.Join(dir, "proxies.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg.SetProbes(func(_ context.Context, rawURL, _ string) (time.Duration, error) {
		if strings.Contains(rawURL, "down") {
			return 0, errors.New("连不上代理")
		}
		return 50 * time.Millisecond, nil
	}, map[string]string{"cn": "cn.example", "global": "global.example"}, "")
	return reg, dir
}

// proxyPool 建池，返回池 ID。
func proxyPool(t *testing.T, reg *proxies.Registry, in proxies.PoolInput) string {
	t.Helper()
	id, err := reg.CreatePool(in)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// proxyAdd 往池里导入代理（一行一个），返回新代理的 ID。
func proxyAdd(t *testing.T, reg *proxies.Registry, pool string, lines ...string) []string {
	t.Helper()
	res, err := reg.Import(strings.Join(lines, "\n"), "http", pool, nil)
	if err != nil || len(res.Added) != len(lines) {
		t.Fatalf("导入代理: %+v %v", res, err)
	}
	return res.Added
}

// proxyAcct 号池里加一个号，代理池跟着记下（同启动时）。global 号按域名认。
func proxyAcct(t *testing.T, p *Panel, uid, realm string) *auth.Auth {
	t.Helper()
	domain := "copilot.tencent.com"
	if realm == "global" {
		domain = "www.workbuddy.ai"
	}
	a := &auth.Auth{UID: uid, AccessToken: "at-" + uid, RefreshToken: "rt-" + uid, ExpiresAt: 1900000000,
		Domain: domain, FilePath: filepath.Join(p.cfg.AuthDir, "workbuddy-"+uid+".json")}
	if err := a.SaveAtomic(); err != nil {
		t.Fatal(err)
	}
	p.cfg.Pool.Add(a)
	p.cfg.Proxies.Track(a, false)
	return a
}

// proxyCall 发 POST，断言状态码，返回响应体。
func proxyCall(t *testing.T, p *Panel, path, body string, want int) map[string]any {
	t.Helper()
	rec := postJSON(t, p, path, body)
	autoWantCode(t, rec, want)
	return autoDecode(t, rec)
}

// 没装代理池：代理接口都回 501，总览里 proxies 是 null。
func TestProxiesWithoutRegistry(t *testing.T) {
	p, _ := newPanelFor(t, Config{})
	for _, route := range []string{
		"GET /panel/api/proxies", "POST /panel/api/proxies/preview", "POST /panel/api/proxies/import",
		"GET /panel/api/proxies/export", "POST /panel/api/proxies/x", "POST /panel/api/proxies/x/test",
		"POST /panel/api/proxies/test_all", "POST /panel/api/proxies/delete", "POST /panel/api/proxies/delete_unavailable",
		"POST /panel/api/proxies/settings", "POST /panel/api/proxies/bind", "POST /panel/api/proxy_pools",
		"POST /panel/api/proxy_pools/x", "POST /panel/api/proxy_pools/x/delete", "POST /panel/api/proxy_pools/x/rebalance",
	} {
		method, path, _ := strings.Cut(route, " ")
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: HTTP %d %s", route, rec.Code, rec.Body)
		}
	}
	resp := autoDecode(t, get(p, http.MethodGet, "/panel/api/overview"))
	if v, ok := resp["proxies"]; !ok || v != nil {
		t.Fatalf("没装代理池时总览的 proxies 应为 null: %v", resp["proxies"])
	}
}

// 代理页的接口走一遍：建池改池、预览导入导出、改测删代理、绑号、设置、重新均分、删池；各种出错的状态码。
func TestProxiesAPI(t *testing.T) {
	reg, dir := newProxyReg(t)
	p, _ := newPanelFor(t, Config{Proxies: reg})
	reg.SetGateHook(p.cfg.Pool.SetProxyBlocked)

	out := proxyCall(t, p, "/panel/api/proxy_pools", `{"name":"国内池","max_per_proxy":1,"default_cn":true}`, 200)
	pid, _ := out["id"].(string)
	if pid == "" {
		t.Fatalf("建池应回 ID: %v", out)
	}
	proxyCall(t, p, "/panel/api/proxy_pools", `{"name":" "}`, 400)
	proxyCall(t, p, "/panel/api/proxy_pools", `{`, 400)
	proxyCall(t, p, "/panel/api/proxy_pools/"+pid, `{"name":"国内","max_per_proxy":2,"default_cn":true}`, 200)
	proxyCall(t, p, "/panel/api/proxy_pools/nope", `{"name":"x"}`, 404)
	proxyCall(t, p, "/panel/api/proxy_pools/"+pid, `[]`, 400)

	// 预览只标出问题，不导入。
	text := "127.0.0.1:1 甲\nnot a proxy\nhttp://down:secret@127.0.0.1:2 乙\n127.0.0.1:1"
	body, _ := json.Marshal(map[string]any{"text": text, "scheme": "http", "pool": pid})
	out = proxyCall(t, p, "/panel/api/proxies/preview", string(body), 200)
	lines, _ := out["lines"].([]any)
	if len(lines) != 4 || lines[1].(map[string]any)["error"] == nil || lines[3].(map[string]any)["duplicate"] != "batch" {
		t.Fatalf("预览结果: %v", out)
	}
	if strings.Contains(fmt.Sprint(out), "secret") {
		t.Fatalf("预览不能带密码: %v", out)
	}
	if len(reg.View().Proxies) != 0 {
		t.Fatal("预览不导入")
	}
	proxyCall(t, p, "/panel/api/proxies/preview", `{`, 400)

	out = proxyCall(t, p, "/panel/api/proxies/import", string(body), 200)
	added, _ := out["added"].([]any)
	if len(added) != 2 {
		t.Fatalf("应导入能用的 2 行: %v", out)
	}
	jia, yi := added[0].(string), added[1].(string)
	proxyCall(t, p, "/panel/api/proxies/import", `{"text":"127.0.0.1:3","pool":"nope"}`, 404)
	proxyCall(t, p, "/panel/api/proxies/import", `{`, 400)

	rec := get(p, http.MethodGet, "/panel/api/proxies")
	autoWantCode(t, rec, 200)
	var list struct {
		View     proxies.View                   `json:"view"`
		Accounts map[string]proxies.AccountView `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.View.Proxies) != 2 || len(list.View.Pools) != 1 {
		t.Fatalf("列表: %v %s", err, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("列表里的密码要打码: %s", rec.Body)
	}

	rec = get(p, http.MethodGet, "/panel/api/proxies/export")
	autoWantCode(t, rec, 200)
	if !strings.Contains(rec.Body.String(), "http://down:secret@127.0.0.1:2") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "proxies.txt") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("导出应带密码、是附件、不缓存: %v %s", rec.Header(), rec.Body)
	}

	proxyCall(t, p, "/panel/api/proxies/"+jia, `{"name":"甲改"}`, 200)
	proxyCall(t, p, "/panel/api/proxies/nope", `{"name":"x"}`, 404)
	proxyCall(t, p, "/panel/api/proxies/"+jia, `{"url":"ftp://127.0.0.1:1"}`, 400)
	proxyCall(t, p, "/panel/api/proxies/"+jia, `{`, 400)

	proxyCall(t, p, "/panel/api/proxies/"+jia+"/test", ``, 200)
	proxyCall(t, p, "/panel/api/proxies/"+yi+"/test", ``, 200)
	v := reg.View()
	if v.Proxies[0].Name != "甲改" || v.Proxies[0].Health.State != "ok" || v.Proxies[1].Health.State != "down" {
		t.Fatalf("测完应记下结果: %+v", v.Proxies)
	}
	proxyCall(t, p, "/panel/api/proxies/nope/test", ``, 404)
	if out = proxyCall(t, p, "/panel/api/proxies/test_all", ``, 200); out["total"] != float64(2) {
		t.Fatalf("全部重测: %v", out)
	}

	proxyAcct(t, p, "u1", "cn")
	proxyAcct(t, p, "u2", "cn")
	proxyCall(t, p, "/panel/api/proxies/bind", `{"uids":[],"pool":"`+pid+`"}`, 400)
	proxyCall(t, p, "/panel/api/proxies/bind", `{"uids":["ghost"],"pool":"`+pid+`"}`, 404)
	proxyCall(t, p, "/panel/api/proxies/bind", `{"uids":["u1"],"proxy":"nope"}`, 404)
	proxyCall(t, p, "/panel/api/proxies/bind", `{"uids":["u1"]}`, 400)
	proxyCall(t, p, "/panel/api/proxies/bind", `{`, 400)
	out = proxyCall(t, p, "/panel/api/proxies/bind", `{"uids":["u1","u2"],"pool":"`+pid+`"}`, 200)
	res, _ := out["results"].([]any)
	if len(res) != 2 || res[0].(map[string]any)["proxy"] != jia || res[1].(map[string]any)["proxy"] != jia {
		t.Fatalf("绑池：乙不通，两个号都应分到甲: %v", out)
	}
	if b := p.cfg.Proxies.Binding("u1"); b.ID != jia || b.Manual {
		t.Fatalf("u1 的绑定: %+v", b)
	}

	out = proxyCall(t, p, "/panel/api/proxies/settings", `{"require_bound":true}`, 200)
	if s, _ := out["settings"].(map[string]any); s["require_bound"] != true || s["health_minutes"] != float64(proxies.DefaultHealthMinutes) {
		t.Fatalf("设置应补默认值: %v", out)
	}
	proxyCall(t, p, "/panel/api/proxies/settings", `{"exit_ip_urls":["ftp://x"]}`, 400)
	proxyCall(t, p, "/panel/api/proxies/settings", `{`, 400)

	if out = proxyCall(t, p, "/panel/api/proxy_pools/"+pid+"/rebalance", ``, 200); out["moved"] != float64(0) {
		t.Fatalf("只有甲能用，均分不动: %v", out)
	}
	proxyCall(t, p, "/panel/api/proxy_pools/nope/rebalance", ``, 404)
	proxyCall(t, p, "/panel/api/proxy_pools/"+pid+"/delete", ``, 400) // 池里还有代理

	// 写不进 proxies.json：500，改动撤回。
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	proxyCall(t, p, "/panel/api/proxy_pools", `{"name":"新池"}`, 500)
	proxyCall(t, p, "/panel/api/proxies/delete_unavailable", ``, 500)
	proxyCall(t, p, "/panel/api/proxies/delete", `{"ids":["`+yi+`"]}`, 500)
	if len(reg.View().Proxies) != 2 || len(reg.View().Pools) != 1 {
		t.Fatal("写不进去应撤回")
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if out = proxyCall(t, p, "/panel/api/proxies/delete_unavailable", ``, 200); out["deleted"] != float64(1) {
		t.Fatalf("应删掉不通的乙: %v", out)
	}
	proxyCall(t, p, "/panel/api/proxies/delete", `{`, 400)
	if out = proxyCall(t, p, "/panel/api/proxies/delete", `{"ids":["`+jia+`","nope"]}`, 200); out["deleted"] != float64(1) {
		t.Fatalf("删代理: %v", out)
	}
	if b := p.cfg.Proxies.Binding("u1"); b.Pool != pid || b.ID != "" {
		t.Fatalf("代理删了，号在池里等代理: %+v", b)
	}
	proxyCall(t, p, "/panel/api/proxy_pools/"+pid+"/delete", ``, 200)
	proxyCall(t, p, "/panel/api/proxy_pools/"+pid+"/delete", ``, 404)
}

// 总览带上代理池的概况和每个号的代理情况；删号后代理池也不再记它，空出来的位子补给等代理的号。
func TestProxiesOverviewAndRemove(t *testing.T) {
	reg, _ := newProxyReg(t)
	p, _ := newPanelFor(t, Config{Proxies: reg})
	reg.SetGateHook(p.cfg.Pool.SetProxyBlocked)
	pid := proxyPool(t, reg, proxies.PoolInput{Name: "池", MaxPerProxy: 1})
	ids := proxyAdd(t, reg, pid, "127.0.0.1:1 甲")
	proxyAcct(t, p, "u1", "cn")
	proxyAcct(t, p, "u2", "cn")
	if _, err := reg.Bind([]string{"u1", "u2"}, proxies.BindTarget{Pool: pid}); err != nil {
		t.Fatal(err)
	}

	resp := autoDecode(t, get(p, http.MethodGet, "/panel/api/overview"))
	px, _ := resp["proxies"].(map[string]any)
	accts, _ := px["accounts"].(map[string]any)
	warn, _ := px["warnings"].(map[string]any)
	if px["pools"] != float64(1) || px["proxies"] != float64(1) || px["require_bound"] != false ||
		warn["waiting"] != float64(1) || accts["u1"].(map[string]any)["proxy_name"] != "甲" || accts["u2"].(map[string]any)["waiting"] != true {
		t.Fatalf("总览的代理池部分: %v", px)
	}
	if resp["proxy_blocked"] != float64(1) || resp["cooling"] != float64(0) || resp["healthy"] != float64(1) {
		t.Fatalf("等代理的号在总览里单独计: proxy_blocked=%v cooling=%v healthy=%v", resp["proxy_blocked"], resp["cooling"], resp["healthy"])
	}
	if st := mustStatus(t, p, "u2"); !strings.Contains(st.ProxyBlocked, "等代理") {
		t.Fatalf("等代理的号应被号池拦着: %+v", st)
	}

	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/remove", ``), 200)
	if b := reg.Binding("u1"); !b.Empty() {
		t.Fatalf("删掉的号代理池不应再记着: %+v", b)
	}
	if b := reg.Binding("u2"); b.ID != ids[0] {
		t.Fatalf("u1 走了，u2 应补上甲: %+v", b)
	}
	if st := mustStatus(t, p, "u2"); st.ProxyBlocked != "" {
		t.Fatalf("补上代理后号池应放开: %+v", st)
	}
}

// newProxyLoginPanel 登录测试用的面板，装上代理池：国内版默认池「池」，里面一个代理甲，每个代理最多 1 个号。
func newProxyLoginPanel(t *testing.T) (*Panel, *autoUpstream, string, *proxies.Registry, string, string) {
	t.Helper()
	p, up, dir := newLoginPanel(t)
	reg, _ := newProxyReg(t)
	p.cfg.Proxies = reg
	reg.SetGateHook(p.cfg.Pool.SetProxyBlocked)
	pid := proxyPool(t, reg, proxies.PoolInput{Name: "池", MaxPerProxy: 1, DefaultCN: true})
	jia := proxyAdd(t, reg, pid, "127.0.0.1:1 甲")[0]
	return p, up, dir, reg, pid, jia
}

// 登录选池：先在池里占一个代理（登录请求走它），成功后新号绑在它上面、放掉占位；池不存在、
// 没有空位、只许走代理时选了不绑都发不起来；上游失败、会话过期都放掉占位。
func TestLoginProxyPool(t *testing.T) {
	cnOK := func(up *autoUpstream, uid string) {
		loginRouteState(up, loginCNHost, "st-"+uid, "https://auth.example/c")
		loginRouteToken(up, loginCNHost, `{"accessToken":"at","refreshToken":"rt","expiresIn":3600,"domain":"copilot.tencent.com"}`)
		loginRouteAccount(up, loginCNHost, `{"uid":"`+uid+`","nickname":"新号"}`)
		up.on(http.MethodPost, autoBillingHost, checkinPathCN, "", autoOK(autoEnvelope("{}")))
		up.on(http.MethodPost, autoBillingHost, resourcePathCN, "", autoOK(loginResourceBody(88, 100)))
	}

	t.Run("默认池占位，新号绑在登录走的代理上", func(t *testing.T) {
		p, up, dir, reg, pid, jia := newProxyLoginPanel(t)
		cnOK(up, "u-new")
		start := loginStartOK(t, p, `{}`)
		p.loginMu.Lock()
		sess := p.logins[fmt.Sprint(start["state"])]
		p.loginMu.Unlock()
		if sess.proxy != jia || sess.hold == "" || sess.pool != "" {
			t.Fatalf("会话应记下占的代理: %+v", sess)
		}
		if _, _, err := reg.HoldForLogin(pid, "cn"); err == nil {
			t.Fatal("占位占着唯一的位子，再登录应没有空位")
		}
		rec := postJSON(t, p, "/panel/api/login/start", `{}`)
		autoWantCode(t, rec, http.StatusBadRequest)
		if !strings.Contains(autoErrText(t, rec), "没有可用的空位") {
			t.Fatalf("没有空位: %s", rec.Body)
		}

		rec = loginPollState(p, fmt.Sprint(start["state"]))
		autoWantCode(t, rec, 200)
		resp := autoDecode(t, rec)
		view, _ := resp["proxy"].(map[string]any)
		if resp["done"] != true || view["proxy_name"] != "甲" || resp["proxy_kept"] != false || resp["checkin_message"] != "" {
			t.Fatalf("登录结果: %v", resp)
		}
		if b := loadAuth(t, dir, "u-new").ProxyBinding(); b.ID != jia || b.Pool != pid || b.Manual {
			t.Fatalf("凭证里应记下绑定: %+v", b)
		}
		// 占位放掉了：甲的位子由新号占着，池满；新号走了以后又能占。
		if _, _, err := reg.HoldForLogin(pid, "cn"); err == nil {
			t.Fatal("新号占着甲，池应还是满的")
		}
		reg.Untrack("u-new")
		if _, _, err := reg.HoldForLogin(pid, "cn"); err != nil {
			t.Fatalf("占位应已放掉: %v", err)
		}
	})

	t.Run("选了别的池，占的代理没了就在那个池里重新分", func(t *testing.T) {
		p, up, _, reg, _, _ := newProxyLoginPanel(t)
		other := proxyPool(t, reg, proxies.PoolInput{Name: "别的池"})
		yi := proxyAdd(t, reg, other, "127.0.0.1:2 乙", "127.0.0.1:3 丙")
		cnOK(up, "u-other")
		start := loginStartOK(t, p, `{"pool":"`+other+`"}`)
		if _, err := reg.DeleteProxies([]string{yi[0]}); err != nil {
			t.Fatal(err)
		}
		resp := autoDecode(t, loginPollState(p, fmt.Sprint(start["state"])))
		if b := reg.Binding("u-other"); b.Pool != other || b.ID != yi[1] {
			t.Fatalf("应在选的池里重新分到丙（不是默认池）: %+v %v", b, resp)
		}
	})

	t.Run("选了不绑", func(t *testing.T) {
		p, up, dir, reg, _, _ := newProxyLoginPanel(t)
		cnOK(up, "u-free")
		start := loginStartOK(t, p, `{"pool":"-"}`)
		p.loginMu.Lock()
		sess := p.logins[fmt.Sprint(start["state"])]
		p.loginMu.Unlock()
		if sess.hold != "" || sess.proxy != "" {
			t.Fatalf("不绑就不占位: %+v", sess)
		}
		autoWantCode(t, loginPollState(p, fmt.Sprint(start["state"])), 200)
		if b := reg.Binding("u-free"); !b.Empty() {
			t.Fatalf("不绑的新号不进默认池: %+v", b)
		}
		if b := loadAuth(t, dir, "u-free").ProxyBinding(); !b.Empty() {
			t.Fatalf("凭证里不该有绑定: %+v", b)
		}

		if _, err := reg.UpdateSettings(proxies.Settings{RequireBound: true}); err != nil {
			t.Fatal(err)
		}
		rec := postJSON(t, p, "/panel/api/login/start", `{"pool":"-"}`)
		autoWantCode(t, rec, http.StatusBadRequest)
		if !strings.Contains(autoErrText(t, rec), "只许走号的代理") {
			t.Fatalf("只许走代理时不能选不绑: %s", rec.Body)
		}
	})

	t.Run("池不存在", func(t *testing.T) {
		p, _, _, _, _, _ := newProxyLoginPanel(t)
		autoWantCode(t, postJSON(t, p, "/panel/api/login/start", `{"pool":"nope"}`), http.StatusNotFound)
	})

	t.Run("上游失败、会话过期都放掉占位", func(t *testing.T) {
		p, up, _, reg, pid, _ := newProxyLoginPanel(t)
		up.on(http.MethodPost, loginCNHost, loginStatePath, "", autoFail(500, `boom`))
		autoWantCode(t, postJSON(t, p, "/panel/api/login/start", `{}`), http.StatusBadGateway)
		hold, _, err := reg.HoldForLogin(pid, "cn")
		if err != nil {
			t.Fatalf("发起失败应放掉占位: %v", err)
		}
		p.loginMu.Lock()
		p.logins["st-old"] = loginSession{created: time.Now().Add(-2 * loginTTL), realm: "cn", hold: hold}
		p.loginMu.Unlock()
		up.on(http.MethodPost, loginCNHost, loginStatePath, "", autoOK(autoEnvelope(`{"state":"st-new","authUrl":"https://a"}`)))
		loginStartOK(t, p, `{}`) // 过期会话的占位放掉了，新的登录才占得到
	})

	t.Run("原来就在号池里的号沿用原来的绑定", func(t *testing.T) {
		p, up, dir, reg, pid, jia := newProxyLoginPanel(t)
		if err := reg.UpdatePool(pid, proxies.PoolInput{Name: "池", DefaultCN: true}); err != nil {
			t.Fatal(err)
		}
		mine := proxyAdd(t, reg, pid, "127.0.0.1:4 自己的")[0]
		proxyAcct(t, p, "u-old", "cn")
		if _, err := reg.Bind([]string{"u-old"}, proxies.BindTarget{Proxy: mine}); err != nil {
			t.Fatal(err)
		}
		cnOK(up, "u-old")
		start := loginStartOK(t, p, `{}`)
		resp := autoDecode(t, loginPollState(p, fmt.Sprint(start["state"])))
		if resp["proxy_kept"] != true || resp["proxy"].(map[string]any)["proxy_name"] != "自己的" {
			t.Fatalf("登录结果: %v", resp)
		}
		if b := loadAuth(t, dir, "u-old").ProxyBinding(); b.ID != mine || !b.Manual {
			t.Fatalf("应沿用单独指定的代理: %+v（登录走的是 %s）", b, jia)
		}
	})

	t.Run("代理不能用就先不签到", func(t *testing.T) {
		p, up, _, reg, _, jia := newProxyLoginPanel(t)
		cnOK(up, "u-late")
		start := loginStartOK(t, p, `{}`)
		past := time.Now().Add(-time.Hour)
		if err := reg.UpdateProxy(jia, proxies.ProxyInput{ExpiresAt: &past}); err != nil {
			t.Fatal(err)
		}
		resp := autoDecode(t, loginPollState(p, fmt.Sprint(start["state"])))
		if msg, _ := resp["checkin_message"].(string); resp["done"] != true || !strings.Contains(msg, "先不签到：代理「甲」已到期") ||
			resp["credits"] != float64(-1) {
			t.Fatalf("登录结果: %v", resp)
		}
		if n := len(up.requestsTo(http.MethodPost, autoBillingHost, checkinPathCN, "")) +
			len(up.requestsTo(http.MethodPost, autoBillingHost, resourcePathCN, "")); n != 0 {
			t.Fatalf("号不能出站时不该签到、查余额，实际 %d 次", n)
		}
		if st := mustStatus(t, p, "u-late"); !strings.Contains(st.ProxyBlocked, "已到期") {
			t.Fatalf("号池应拦着: %+v", st)
		}
	})
}

// postImport 导入账号，可带 proxy_pool。
func postImport(t *testing.T, p *Panel, content []byte, pool string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "accounts.json")
	_, _ = fw.Write(content)
	if pool != "" {
		_ = mw.WriteField("proxy_pool", pool)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/panel/api/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

// 导出凭证带上代理地址，导进另一个网关认出（或新建）同一个代理；导入可选池、选不绑；已有的号沿用原来的
// 绑定；导入后不能出站的号先不签到。
func TestImportExportProxy(t *testing.T) {
	srcReg, _ := newProxyReg(t)
	src, _ := newIOPanel(t)
	src.cfg.Proxies = srcReg
	sp := proxyPool(t, srcReg, proxies.PoolInput{Name: "源池"})
	jia := proxyAdd(t, srcReg, sp, "http://u:secret@127.0.0.1:1 甲")[0]
	proxyAcct(t, src, "a1", "cn")
	proxyAcct(t, src, "a2", "cn")
	if _, err := srcReg.Bind([]string{"a1"}, proxies.BindTarget{Proxy: jia}); err != nil {
		t.Fatal(err)
	}
	rec := postJSON(t, src, "/panel/api/accounts/export", `{"uids":["a1","a2"]}`)
	autoWantCode(t, rec, 200)
	var exp struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &exp); err != nil || len(exp.Accounts) != 2 {
		t.Fatalf("导出: %v %s", err, rec.Body)
	}
	if exp.Accounts[0]["proxy_url"] != "http://u:secret@127.0.0.1:1" || exp.Accounts[1]["proxy_url"] != nil {
		t.Fatalf("绑了代理的才带 proxy_url: %v", exp.Accounts)
	}
	batch, _ := json.Marshal(exp.Accounts)

	t.Run("选了池", func(t *testing.T) {
		reg, _ := newProxyReg(t)
		dst, dir := newIOPanel(t)
		dst.cfg.Proxies = reg
		dp := proxyPool(t, reg, proxies.PoolInput{Name: "目标池"})
		bing := proxyAdd(t, reg, dp, "127.0.0.1:9 丙")[0]
		rec := postImport(t, dst, batch, "nope")
		autoWantCode(t, rec, http.StatusNotFound)
		rec = postImport(t, dst, batch, dp)
		autoWantCode(t, rec, 200)
		if out := autoDecode(t, rec); out["imported"] != float64(2) || out["notes"] != nil {
			t.Fatalf("导入结果: %v", out)
		}
		v := reg.View()
		if len(v.Proxies) != 2 || v.Proxies[1].Pool != dp || v.Proxies[1].Name != "127.0.0.1:1" {
			t.Fatalf("凭证里带的代理应新建在选的池里: %+v", v.Proxies)
		}
		if b := loadAuth(t, dir, "a1").ProxyBinding(); b.ID != v.Proxies[1].ID || !b.Manual {
			t.Fatalf("a1 应单独指定到认出的代理: %+v", b)
		}
		if b := reg.Binding("a2"); b.Pool != dp || b.ID != bing {
			t.Fatalf("a2 应放进选的池: %+v", b)
		}
	})

	t.Run("选了不绑，已有的号沿用原来的绑定", func(t *testing.T) {
		reg, _ := newProxyReg(t)
		dst, _ := newIOPanel(t)
		dst.cfg.Proxies = reg
		dp := proxyPool(t, reg, proxies.PoolInput{Name: "默认池", DefaultCN: true})
		ding := proxyAdd(t, reg, dp, "127.0.0.1:8 丁")[0]
		proxyAcct(t, dst, "a2", "cn")
		if _, err := reg.Bind([]string{"a2"}, proxies.BindTarget{Proxy: ding}); err != nil {
			t.Fatal(err)
		}
		rec := postImport(t, dst, batch, proxies.NoPool)
		autoWantCode(t, rec, 200)
		if b := reg.Binding("a2"); b.ID != ding || !b.Manual {
			t.Fatalf("a2 原来就在号池里，凭证里没带代理，应沿用原来的绑定: %+v", b)
		}
		if b := reg.Binding("a1"); b.ID == "" || b.Pool != dp {
			t.Fatalf("a1 凭证里带了代理，照样认出（新代理放进默认池）: %+v", b)
		}
	})

	t.Run("导入后不能出站的号先不签到", func(t *testing.T) {
		reg, _ := newProxyReg(t)
		dst, _ := newIOPanel(t)
		dst.cfg.Proxies = reg
		if _, err := reg.UpdateSettings(proxies.Settings{RequireBound: true}); err != nil {
			t.Fatal(err)
		}
		one, _ := json.Marshal(exp.Accounts[1])
		rec := postImport(t, dst, one, "")
		autoWantCode(t, rec, 200)
		out := autoDecode(t, rec)
		notes, _ := out["notes"].([]any)
		if out["imported"] != float64(1) || len(notes) != 1 || !strings.Contains(fmt.Sprint(notes[0]), "uid=a2: 先不签到，没绑代理") {
			t.Fatalf("导入结果: %v", out)
		}
	})
}
