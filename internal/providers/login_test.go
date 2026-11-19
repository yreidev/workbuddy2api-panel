// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTraeLoginStateAndSaveRetry(t *testing.T) {
	var exchanges atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/trae/api/v3/oauth/ExchangeToken":
			exchanges.Add(1)
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["AuthCode"] != "fixture-code" || input["CodeVerifier"] == "" {
				t.Error("未按官方 authCodeInfo 兑换授权码", err)
			}
			fmt.Fprintf(w, `{"Result":{"Token":"fixture","RefreshToken":"refresh","TokenExpireAt":%d}}`, time.Now().Add(time.Hour).Unix())
		case "/cloudide/api/v3/trae/GetUserInfo":
			fmt.Fprint(w, `{"Result":{"UserID":"fixture","ScreenName":"测试账号"}}`)
		default:
			t.Errorf("未知请求: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	path := filepath.Join(t.TempDir(), "providers.json")
	manager, err := Open(path, backend.Client())
	if err != nil {
		t.Fatal(err)
	}
	manager.adapters["traecode"] = &Trae{HTTP: backend.Client(), AuthBase: backend.URL}
	login, err := manager.StartLogin("traecode:cn", "")
	if err != nil {
		t.Fatal(err)
	}
	if server := manager.logins[login.State].callback; server != nil {
		t.Cleanup(func() { _ = server.Close() })
	}
	if !strings.Contains(login.URL, "code_challenge_method=S256") || strings.Contains(login.URL, manager.logins[login.State].verifier) {
		t.Fatal("PKCE verifier 泄露或 challenge 缺失")
	}
	authorization, err := url.Parse(login.URL)
	if err != nil || authorization.Query().Get("auth_callback_url") != "http://127.0.0.1:1024/authorize" {
		t.Fatal("官方授权页会拒绝回调地址", err)
	}
	if (&Trae{}).authBase() != "https://api.trae.com.cn" {
		t.Fatal("Trae 授权兑换域名与官方回调不符")
	}
	if _, err := manager.CompleteLogin(context.Background(), login.State, "http://localhost:1024/authorize?state=wrong&code=code"); err == nil || exchanges.Load() != 0 {
		t.Fatal("错误 state 被接受")
	}
	for _, info := range []string{"{", `{"AuthCode":123}`, `{"ExpireAt":1}`, "null"} {
		callback := "http://127.0.0.1:1024/authorize?" + url.Values{"loginTraceID": {login.State}, "authCodeInfo": {info}}.Encode()
		if _, err := manager.CompleteLogin(context.Background(), login.State, callback); err == nil || exchanges.Load() != 0 {
			t.Fatal("无效 authCodeInfo 被接受")
		}
	}
	if server := manager.logins[login.State].callback; server != nil {
		if err := server.Close(); err != nil {
			t.Fatal(err)
		}
	}
	callbackClosed := make(chan struct{})
	callbackServer := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	callbackServer.Config.RegisterOnShutdown(func() { close(callbackClosed) })
	callbackServer.Start()
	defer callbackServer.Close()
	manager.logins[login.State].callback = callbackServer.Config
	manager.logins[login.State].localCallback = true
	pending, err := manager.CompleteLogin(context.Background(), login.State, "")
	if err != nil || !pending.Pending || exchanges.Load() != 0 {
		t.Fatalf("本地回调到达前未返回等待状态: %+v %v", pending, err)
	}
	manager.path = filepath.Join(t.TempDir(), "missing", "not-a-file")
	// 目录替代文件，模拟授权兑换成功后无法原子替换文件。
	if err := os.MkdirAll(manager.path, 0700); err != nil {
		t.Fatal(err)
	}
	callback := "http://127.0.0.1:1024/authorize?" + url.Values{"loginTraceID": {login.State}, "authCodeInfo": {`{"AuthCode":"fixture-code","ExpireAt":1893456000000}`}}.Encode()
	if _, err := manager.CompleteLogin(context.Background(), login.State, callback); err == nil {
		t.Fatal("写盘失败被忽略")
	}
	manager.path = path
	completed, err := manager.CompleteLogin(context.Background(), login.State, callback)
	if err != nil || completed.Pending || completed.AccountID == "" || exchanges.Load() != 1 {
		t.Fatalf("重试重复兑换 code: %+v %v exchanges=%d", completed, err, exchanges.Load())
	}
	select {
	case <-callbackClosed:
	case <-time.After(time.Second):
		t.Fatal("重试保存授权后未关闭本地回调服务")
	}
	again, err := manager.CompleteLogin(context.Background(), login.State, callback)
	if err != nil || again.AccountID != completed.AccountID || exchanges.Load() != 1 {
		t.Fatal("成功授权重入不幂等")
	}
}
func TestQoderLoginPendingAndJobToken(t *testing.T) {
	var polls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/deviceToken/poll":
			if r.URL.Query().Get("verifier") == "" {
				t.Error("缺少 PKCE verifier")
			}
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprint(w, `{"token":"device","user_id":"fixture"}`)
		case "/api/v1/me/jobToken":
			if r.Header.Get("Authorization") != "Bearer device" {
				t.Error("设备令牌未用于兑换")
			}
			fmt.Fprint(w, `{"token":"job","refresh_token":"refresh","expires_in":3600000}`)
		case "/api/v1/userinfo":
			fmt.Fprint(w, `{"id":"fixture","name":"测试账号"}`)
		case "/algo/api/v3/user/status":
			fmt.Fprint(w, `{"code":200,"body":{"userType":"teams"}}`)
		default:
			t.Error("未知请求", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	manager, err := Open("", backend.Client())
	if err != nil {
		t.Fatal(err)
	}
	manager.adapters["qoder"] = &Qoder{HTTP: backend.Client(), Base: backend.URL, OpenAPIBase: backend.URL}
	login, err := manager.StartLogin("qoder:global", "")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := manager.CompleteLogin(context.Background(), login.State, "")
	if err != nil || !pending.Pending {
		t.Fatal("pending 状态错误", err)
	}
	pending, err = manager.CompleteLogin(context.Background(), login.State, "")
	if err != nil || !pending.Pending || polls.Load() != 1 {
		t.Fatal("等待授权时没有限制轮询频率", err)
	}
	manager.logins[login.State].lastPoll = time.Now().Add(-3 * time.Second)
	completed, err := manager.CompleteLogin(context.Background(), login.State, "")
	if err != nil || completed.Pending {
		t.Fatal("Qoder 登录失败", err)
	}
	account := manager.data.Accounts[0]
	if account.Credential.TokenKind != "job" || account.Credential.UserType != "teams" || account.Credential.RefreshToken != "refresh" {
		t.Fatal("OAuth 凭证或权威身份丢失")
	}
	manager.logins[login.State].ExpiresAt = time.Now().Add(-time.Second)
	if _, err := manager.CompleteLogin(context.Background(), login.State, ""); err == nil {
		t.Fatal("接受过期授权")
	}
}

func TestQoderLoginRetriesCompletedStages(t *testing.T) {
	for _, target := range []string{"qoder:cn", "qoder:global"} {
		for _, failure := range []string{"兑换失败", "兑换结果无效", "用户信息失败", "用户信息缺少身份"} {
			if target == "qoder:cn" && strings.HasPrefix(failure, "兑换") {
				continue
			}
			t.Run(target+"/"+failure, func(t *testing.T) {
				var polls, jobs, users atomic.Int32
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v1/deviceToken/poll":
						if polls.Add(1) != 1 {
							w.WriteHeader(http.StatusGone)
							return
						}
						fmt.Fprint(w, `{"token":"device-fixture-token","refresh_token":"device-refresh-fixture-token","expires_in":3600,"user_id":"poll-user"}`)
					case "/api/v1/me/jobToken":
						if r.Header.Get("Authorization") != "Bearer device-fixture-token" {
							t.Error("重试兑换没有复用设备令牌")
						}
						attempt := jobs.Add(1)
						if attempt == 1 && failure == "兑换失败" {
							w.WriteHeader(http.StatusBadGateway)
							return
						}
						if attempt == 1 && failure == "兑换结果无效" {
							fmt.Fprint(w, `{"token":"invalid-fixture-token"}`)
							return
						}
						fmt.Fprint(w, `{"token":"job-fixture-token","refresh_token":"refresh-fixture-token","expires_in":3600000}`)
					case "/api/v1/userinfo":
						if r.Header.Get("Authorization") != "Bearer device-fixture-token" {
							t.Error("用户信息请求没有使用设备令牌")
						}
						attempt := users.Add(1)
						if attempt == 1 && failure == "用户信息失败" {
							w.WriteHeader(http.StatusBadGateway)
							return
						}
						if attempt == 1 && failure == "用户信息缺少身份" {
							fmt.Fprint(w, `{"name":"测试账号"}`)
							return
						}
						fmt.Fprint(w, `{"id":"fixture-user","name":"测试账号"}`)
					case "/algo/api/v3/user/status":
						fmt.Fprint(w, `{"code":200,"body":{"userType":"teams"}}`)
					default:
						t.Error("未知请求", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer backend.Close()
				path := filepath.Join(t.TempDir(), "providers.json")
				manager, err := Open(path, backend.Client())
				if err != nil {
					t.Fatal(err)
				}
				manager.adapters["qoder"] = &Qoder{HTTP: backend.Client(), Base: backend.URL, OpenAPIBase: backend.URL}
				login, err := manager.StartLogin(target, "")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := manager.CompleteLogin(context.Background(), login.State, ""); err == nil || len(manager.List()) != 0 {
					t.Fatal("失败的授权被保存", err)
				}
				// 已获得的设备令牌和 job token 应能继续使用，落盘失败也不能重新兑换。
				manager.path = filepath.Join(t.TempDir(), "not-a-file")
				if err := os.Mkdir(manager.path, 0700); err != nil {
					t.Fatal(err)
				}
				if _, err := manager.CompleteLogin(context.Background(), login.State, ""); err == nil || len(manager.List()) != 0 {
					t.Fatal("重试没有进入保存阶段，或忽略了保存错误", err)
				}
				session := manager.logins[login.State]
				if session.credential.UID != "fixture-user" || session.credential.Name != "测试账号" {
					t.Fatal("重试跳过了用户信息核验")
				}
				raw, err := json.Marshal(loginView(session))
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{"device-fixture-token", "job-fixture-token", "refresh-fixture-token", session.verifier, "credential"} {
					if strings.Contains(string(raw), secret) {
						t.Fatal("待保存授权的响应泄露凭证")
					}
				}
				manager.path = path
				completed, err := manager.CompleteLogin(context.Background(), login.State, "")
				if err != nil || completed.Pending || completed.AccountID != stableAccountID(target, "fixture-user") {
					t.Fatal("重试未保存权威账号身份", err)
				}
				again, err := manager.CompleteLogin(context.Background(), login.State, "")
				if err != nil || again.Pending || again.AccountID != completed.AccountID {
					t.Fatal("成功授权重入不幂等", err)
				}
				wantJobs, wantUsers := int32(1), int32(1)
				if strings.HasPrefix(failure, "兑换") {
					wantJobs = 2
				} else {
					wantUsers = 2
				}
				wantKind := "job"
				if target == "qoder:cn" {
					wantJobs = 0
					wantKind = "device"
				}
				if polls.Load() != 1 || jobs.Load() != wantJobs || users.Load() != wantUsers {
					t.Fatalf("重复执行已完成的授权阶段: poll=%d job=%d user=%d", polls.Load(), jobs.Load(), users.Load())
				}
				if session.credential != (Credential{}) || session.deviceToken != "" || session.verifier != "" {
					t.Fatal("成功授权后没有清理会话凭证")
				}
				restarted, err := Open(manager.path, nil)
				if err != nil || len(restarted.List()) != 1 || restarted.data.Accounts[0].Credential.TokenKind != wantKind || restarted.data.Accounts[0].Credential.UserType != "teams" {
					t.Fatal("授权凭证没有完整持久化", err)
				}
			})
		}
	}
}

func TestQoderCNDeviceLogin(t *testing.T) {
	var polls, jobs atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/deviceToken/poll":
			if r.URL.Query().Get("nonce") == "" || r.URL.Query().Get("verifier") == "" || r.URL.Query().Get("challenge_method") != "S256" {
				t.Error("设备授权轮询参数缺失")
			}
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprint(w, `{"token":"device-fixture","refresh_token":"device-refresh-fixture","expires_in":3600,"user_id":"unverified-poll-user"}`)
		case "/api/v1/me/jobToken":
			jobs.Add(1)
			t.Error("Qoder 国内设备授权不应兑换 job token")
			w.WriteHeader(http.StatusBadRequest)
		case "/api/v1/userinfo":
			if r.Header.Get("Authorization") != "Bearer device-fixture" {
				t.Error("用户身份核验未使用设备令牌")
			}
			fmt.Fprint(w, `{"id":"fixture-user","name":"测试账号"}`)
		case "/algo/api/v3/user/status":
			fmt.Fprint(w, `{"code":200,"body":{"userType":"personal"}}`)
		default:
			t.Error("未知请求", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	path := filepath.Join(t.TempDir(), "providers.json")
	manager, err := Open(path, backend.Client())
	if err != nil {
		t.Fatal(err)
	}
	manager.adapters["qoder"] = &Qoder{HTTP: backend.Client(), Base: backend.URL, OpenAPIBase: backend.URL}
	login, err := manager.StartLogin("qoder:cn", "")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := manager.CompleteLogin(context.Background(), login.State, "")
	if err != nil || !pending.Pending {
		t.Fatal("404 应表示等待网页授权", err)
	}
	manager.logins[login.State].lastPoll = time.Now().Add(-3 * time.Second)
	completed, err := manager.CompleteLogin(context.Background(), login.State, "")
	if err != nil || completed.Pending || completed.AccountID != stableAccountID("qoder:cn", "fixture-user") {
		t.Fatal("国内设备授权未保存经核验的账号", err)
	}
	restarted, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := restarted.data.Accounts[0].Credential
	if c.AccessToken != "device-fixture" || c.RefreshToken != "device-refresh-fixture" || c.TokenKind != "device" || c.UserType != "personal" || c.MachineID == "" {
		t.Fatal("设备凭证没有完整持久化")
	}
	if remaining := time.Until(time.Unix(c.ExpiresAt, 0)); remaining < 59*time.Minute || remaining > time.Hour {
		t.Fatal("设备令牌 expires_in 未按秒读取", remaining)
	}
	if polls.Load() != 2 || jobs.Load() != 0 {
		t.Fatal("国内授权执行了多余的令牌兑换")
	}
}

func TestQoderOAuthRefreshKinds(t *testing.T) {
	for _, tc := range []struct {
		name, kind, response string
		remaining            time.Duration
		wantError            bool
	}{
		{name: "设备令牌秒数", kind: "device", response: `{"device_token":"new-fixture","refresh_token":"new-refresh-fixture","expires_in":3600}`, remaining: time.Hour},
		{name: "设备令牌日期", kind: "device", response: fmt.Sprintf(`{"device_token":"new-fixture","refresh_token":"new-refresh-fixture","expires_at":%q}`, time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339)), remaining: 2 * time.Hour},
		{name: "job令牌毫秒数", kind: "job", response: `{"token":"new-fixture","refresh_token":"new-refresh-fixture","expires_in":3600000}`, remaining: time.Hour},
		{name: "设备令牌缺少token", kind: "device", response: `{"refresh_token":"new-refresh-fixture","expires_in":3600}`, wantError: true},
		{name: "设备令牌缺少刷新令牌", kind: "device", response: `{"device_token":"new-fixture","expires_in":3600}`, wantError: true},
		{name: "设备令牌缺少过期时间", kind: "device", response: `{"device_token":"new-fixture","refresh_token":"new-refresh-fixture"}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/userinfo" {
					if r.Header.Get("Authorization") != qoderAPIHeaders("new-fixture").Get("Authorization") {
						t.Error("身份查询未使用新令牌")
					}
					fmt.Fprint(w, `{"user_id":"fixture-user"}`)
					return
				}
				path := "/api/v1/" + tc.kind + "Token/refresh"
				if r.Method != http.MethodPost || r.URL.Path != path {
					t.Error("刷新请求使用了错误令牌类型", r.Method, r.URL.Path)
				}
				var input map[string]any
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				if input["refresh_token"] != "refresh-fixture" {
					t.Error("未发送原刷新令牌")
				}
				if tc.kind == "device" && (input["machine_id"] != "machine-fixture" || input["machine_token"] != "machine-token-fixture") {
					t.Error("设备刷新未沿用登录时的机器身份")
				}
				if tc.kind == "job" && len(input) != 1 {
					t.Error("job 令牌刷新混入设备字段")
				}
				fmt.Fprint(w, tc.response)
			}))
			defer backend.Close()
			q := Qoder{HTTP: backend.Client(), OpenAPIBase: backend.URL}
			credential := Credential{AccessToken: "old-fixture", RefreshToken: "refresh-fixture", TokenKind: tc.kind, MachineID: "machine-fixture", MachineToken: "machine-token-fixture", UID: "fixture-user"}
			refreshed, err := q.Refresh(context.Background(), "qoder:cn", credential)
			if tc.wantError {
				if err == nil {
					t.Fatal("不完整的设备刷新响应被接受")
				}
				return
			}
			if err != nil || refreshed.AccessToken != "new-fixture" || refreshed.RefreshToken != "new-refresh-fixture" || refreshed.TokenKind != tc.kind || refreshed.MachineID != credential.MachineID || refreshed.UID != credential.UID {
				t.Fatal("刷新结果丢失令牌或原身份", err)
			}
			if remaining := time.Until(time.Unix(refreshed.ExpiresAt, 0)); remaining < tc.remaining-time.Minute || remaining > tc.remaining {
				t.Fatal("令牌过期时间单位不符", remaining)
			}
		})
	}
}
