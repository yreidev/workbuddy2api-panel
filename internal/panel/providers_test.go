// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/providers"
	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
)

func TestProviderQuotaPanel(t *testing.T) {
	calls := 0
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/sash/api/v2/me/usage" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("额度查询没有使用账号凭证")
		}
		if fail {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"code":"fixture_unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"displayMode":"qoder","qoderUsage":{"userQuota":{"total":10,"used":10,"remaining":0,"unit":"credits"}}}`))
	}))
	defer srv.Close()
	endpoint, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := providers.Open("", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "openapi.qoder.com.cn" {
			t.Error("额度 host 错误", r.URL.Host)
		}
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = endpoint.Scheme, endpoint.Host
		return srv.Client().Transport.RoundTrip(r)
	})})
	if err != nil {
		t.Fatal(err)
	}
	id, err := manager.Import(context.Background(), providers.AccountInput{Target: "qoder:cn", Credential: providers.Credential{UID: "fixture", AccessToken: "fixture-token", UserType: "teams"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := newPanelFor(t, Config{Providers: manager})
	path := "/panel/api/providers/accounts/" + id + "/quota"
	autoWantCode(t, postJSON(t, p, path, `{bad`), 400)
	autoWantCode(t, postJSON(t, p, "/panel/api/providers/accounts/missing/quota", `{}`), 404)
	response := postJSON(t, p, path, `{}`)
	autoWantCode(t, response, 200)
	if !strings.Contains(response.Body.String(), `"balance":0`) || !strings.Contains(response.Body.String(), `"unit":"credits"`) || strings.Contains(response.Body.String(), "fixture-token") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("额度零值、单位或缓存控制错误", response.Body)
	}
	_ = get(p, http.MethodGet, "/panel/api/providers")
	autoWantCode(t, postJSON(t, p, path, `{}`), 200)
	if calls != 1 {
		t.Fatal("列表轮询或缓存查询反复打上游", calls)
	}
	fail = true
	response = postJSON(t, p, path, `{"force":true}`)
	autoWantCode(t, response, 200)
	if !strings.Contains(response.Body.String(), "fixture_unavailable") || !strings.Contains(response.Body.String(), `"balance":0`) {
		t.Fatal("额度失败未保留快照或错误", response.Body)
	}
	p.cfg.APIKey = "sk-admin"
	for _, token := range []string{"", "sk-client"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"force":true}`))
		request.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, request)
		if rec.Code != 401 {
			t.Fatal("额度绕过管理员权限", rec.Code)
		}
	}
	if calls != 2 {
		t.Fatal("未授权请求发到了额度接口", calls)
	}
}

func TestProviderLivenessPanel(t *testing.T) {
	manager, err := providers.Open("", nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := manager.Import(context.Background(), providers.AccountInput{Target: "qoder:cn", Credential: providers.Credential{UID: "fixture", AccessToken: "fixture-token", UserType: "teams"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := newPanelFor(t, Config{Providers: manager})
	path := "/panel/api/providers/accounts/" + id + "/liveness"
	autoWantCode(t, postJSON(t, p, "/panel/api/providers/accounts/missing/liveness", `{"model":"qoder:cn:model"}`), 404)
	autoWantCode(t, postJSON(t, p, path, `{"model":"qoder:cn:model"}`), 501)
	calls := 0
	p.SetProviderLiveness(func(ctx context.Context, account, model string) (providers.LivenessResponse, error) {
		calls++
		if account != id || model != "qoder:cn:model" || ctx.Err() != nil {
			t.Fatalf("测活参数错误: account=%s model=%s err=%v", account, model, ctx.Err())
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("测活缺少超时")
		}
		return providers.LivenessResponse{Result: providers.LivenessResult{OK: true, Model: model, Status: 200}, Reply: "hi"}, nil
	})
	for _, body := range []string{`{bad`, `{}`, `{"model":"  "}`} {
		autoWantCode(t, postJSON(t, p, path, body), 400)
	}
	response := postJSON(t, p, path, `{"model":" qoder:cn:model "}`)
	autoWantCode(t, response, 200)
	if !strings.Contains(response.Body.String(), `"reply":"hi"`) || calls != 1 {
		t.Fatal("测活结果未返回", response.Body)
	}
	p.SetProviderLiveness(func(context.Context, string, string) (providers.LivenessResponse, error) {
		return providers.LivenessResponse{}, errors.New("测活无法保存")
	})
	autoWantCode(t, postJSON(t, p, path, `{"model":"qoder:cn:model"}`), 422)
	p.cfg.APIKey = "sk-admin"
	autoWantCode(t, postJSON(t, p, path, `{"model":"qoder:cn:model"}`), 401)
}

func TestPlatformAccountPanelAndGroups(t *testing.T) {
	manager, err := providers.Open("", nil)
	if err != nil {
		t.Fatal(err)
	}
	panel, _ := newPanelFor(t, Config{Providers: manager})
	group := createGroup(t, panel, `{"name":"平台组"}`)
	body := fmt.Sprintf(`{"target":"qoder:cn","name":"平台账号","groups":[%q],"credential":{"uid":"fixture","access_token":"private-token","refresh_token":"private-refresh","user_type":"teams"}}`, group)
	response := postJSON(t, panel, "/panel/api/providers/import", body)
	autoWantCode(t, response, 200)
	id := autoDecode(t, response)["account_id"].(string)
	response = get(panel, http.MethodGet, "/panel/api/providers")
	autoWantCode(t, response, 200)
	if strings.Contains(response.Body.String(), "private-token") || strings.Contains(response.Body.String(), "private-refresh") || !strings.Contains(response.Body.String(), id) {
		t.Fatal("平台列表泄露凭证或丢失账号", response.Body)
	}
	response = get(panel, http.MethodGet, "/panel/api/groups")
	if !strings.Contains(response.Body.String(), `"accounts":1`) {
		t.Fatal("分组未计平台账号", response.Body)
	}
	autoWantCode(t, postJSON(t, panel, "/panel/api/providers/accounts/"+id, `{"name":"账号","priority":1,"groups":["missing"]}`), 400)
	autoWantCode(t, postJSON(t, panel, "/panel/api/providers/accounts/"+id, fmt.Sprintf(`{"name":"账号","priority":1,"groups":[" %s ", "", %q]}`, group, group)), 200)
	if list := manager.List(); len(list) != 1 || len(list[0].Groups) != 1 || list[0].Groups[0] != group {
		t.Fatalf("分组未去空白去重: %+v", list)
	}
	// 删分组时同步将组内平台账号移出，与 WorkBuddy 行为一致。
	autoWantCode(t, postJSON(t, panel, "/panel/api/groups/"+group+"/delete", `{}`), 200)
	if list := manager.List(); len(list) != 1 || len(list[0].Groups) != 0 {
		t.Fatalf("删分组后平台账号未退出该组: %+v", list)
	}
	autoWantCode(t, postJSON(t, panel, "/panel/api/providers/accounts/"+id+"/delete", `{}`), 200)
	if len(manager.List()) != 0 {
		t.Fatal("平台账号未移除")
	}
	panel.cfg.APIKey = "sk-admin"
	request := httptest.NewRequest(http.MethodGet, "/panel/api/providers", nil)
	request.Header.Set("Authorization", "Bearer sk-client")
	rec := httptest.NewRecorder()
	panel.ServeHTTP(rec, request)
	if rec.Code != 401 {
		t.Fatal("平台管理绕过管理员鉴权", rec.Code)
	}
}

func TestPlatformAccountProxyExitAndWarningsInStatusAndProxies(t *testing.T) {
	proxyFile := filepath.Join(t.TempDir(), "proxies.json")
	raw := `{
		"version": 1,
		"pools": [{"id": "pool-1", "name": "测试池"}],
		"proxies": [
			{
				"id": "px-ok",
				"name": "东京出口",
				"url": "http://proxy-ok.invalid:8080",
				"pool": "pool-1",
				"health": {
					"state": "ok",
					"exit": {"ip": "203.0.113.7", "country": "日本", "city": "东京"}
				}
			},
			{
				"id": "px-down",
				"name": "故障代理",
				"url": "http://proxy-down.invalid:8080",
				"pool": "pool-1",
				"health": {"state": "ok"}
			},
			{
				"id": "px-del",
				"name": "待删代理",
				"url": "http://proxy-del.invalid:8080",
				"pool": "pool-1",
				"health": {"state": "ok"}
			}
		]
	}`
	if err := os.WriteFile(proxyFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := proxies.Open(proxyFile)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := providers.Open("", nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetProxies(reg)
	goodID, err := manager.Import(context.Background(), providers.AccountInput{
		Target:     "qoder:cn",
		ProxyID:    "px-ok",
		Credential: providers.Credential{UID: "good-u", UserType: "teams", AccessToken: "at-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	downID, err := manager.Import(context.Background(), providers.AccountInput{
		Target:     "traecode:cn",
		ProxyID:    "px-down",
		Credential: providers.Credential{UID: "down-u", AccessToken: "at-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	missingID, err := manager.Import(context.Background(), providers.AccountInput{
		Target:     "traework:cn",
		ProxyID:    "px-del",
		Credential: providers.Credential{UID: "miss-u", AccessToken: "at-3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg.ReportProxy("px-down", errors.New("proxy offline"))
	if _, err := reg.DeleteProxies([]string{"px-del"}); err != nil {
		t.Fatal(err)
	}

	p, _ := newPanelFor(t, Config{Providers: manager, Proxies: reg})

	checkAccountsAndWarnings := func(label string, warnings proxies.Warnings, accounts map[string]proxies.AccountView) {
		t.Helper()
		if warnings.Blocked != 2 {
			t.Fatalf("%s warnings.blocked = %d，期望 2", label, warnings.Blocked)
		}
		good := accounts[goodID]
		if good.ProxyName != "东京出口" || good.Pool != "pool-1" || good.PoolName != "测试池" || good.ExitIP != "203.0.113.7" || good.ExitGeo != "日本 东京" || good.Blocked != "" {
			t.Fatalf("%s 正常平台账号代理视图缺失出口或名称: %+v", label, good)
		}
		if accounts[downID].Blocked == "" || accounts[downID].ProxyName != "故障代理" {
			t.Fatalf("%s down 代理平台账号未标记 blocked: %+v", label, accounts[downID])
		}
		if accounts[missingID].Blocked == "" {
			t.Fatalf("%s 已删代理平台账号未标记 blocked: %+v", label, accounts[missingID])
		}
	}

	// 1. /panel/api/proxies
	rec := get(p, http.MethodGet, "/panel/api/proxies")
	autoWantCode(t, rec, 200)
	var proxiesResp struct {
		View     proxies.View                   `json:"view"`
		Accounts map[string]proxies.AccountView `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &proxiesResp); err != nil {
		t.Fatal(err)
	}
	checkAccountsAndWarnings("/panel/api/proxies", proxiesResp.View.Warnings, proxiesResp.Accounts)
	foundPlatform := false
	for _, px := range proxiesResp.View.Proxies {
		if px.ID == "px-ok" && len(px.PlatformAccounts) == 1 && px.PlatformAccounts[0] == goodID {
			foundPlatform = true
		}
	}
	if !foundPlatform {
		t.Fatalf("/panel/api/proxies 未汇总 platform_accounts: %+v", proxiesResp.View.Proxies)
	}

	// 2. /panel/api/overview
	rec = get(p, http.MethodGet, "/panel/api/overview")
	autoWantCode(t, rec, 200)
	var overviewResp struct {
		Proxies struct {
			Warnings proxies.Warnings               `json:"warnings"`
			Accounts map[string]proxies.AccountView `json:"accounts"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &overviewResp); err != nil {
		t.Fatal(err)
	}
	checkAccountsAndWarnings("/panel/api/overview", overviewResp.Proxies.Warnings, overviewResp.Proxies.Accounts)
}
