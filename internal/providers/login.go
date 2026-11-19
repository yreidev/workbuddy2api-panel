// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yreidev/workbuddy2api-panel/internal/targets"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

type Login struct {
	ProxyID       string
	mu            sync.Mutex
	Target        string    `json:"target"`
	State         string    `json:"state"`
	URL           string    `json:"url"`
	ExpiresAt     time.Time `json:"expires_at"`
	AccountID     string    `json:"account_id,omitempty"`
	verifier      string
	credential    Credential
	lastPoll      time.Time
	callback      *http.Server
	localCallback bool
	deviceToken   string
}

type LoginView struct {
	Target        string    `json:"target"`
	State         string    `json:"state"`
	URL           string    `json:"url"`
	ExpiresAt     time.Time `json:"expires_at"`
	AccountID     string    `json:"account_id,omitempty"`
	Pending       bool      `json:"pending"`
	LocalCallback bool      `json:"local_callback"`
}

func loginView(l *Login) LoginView {
	return LoginView{Target: l.Target, State: l.State, URL: l.URL, ExpiresAt: l.ExpiresAt, AccountID: l.AccountID, Pending: l.AccountID == "", LocalCallback: l.localCallback}
}

func (m *Manager) StartLogin(target, proxyID string) (LoginView, error) {
	if !targets.Valid(target) || targets.WorkBuddy(target) {
		return LoginView{}, errors.New("未知登录平台")
	}
	if _, err := m.route(context.Background(), proxyID); err != nil {
		return LoginView{}, err
	}
	var b [48]byte
	if _, err := rand.Read(b[:]); err != nil {
		return LoginView{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	l := &Login{ProxyID: proxyID, Target: target, State: uuid.NewString(), ExpiresAt: time.Now().Add(15 * time.Minute), verifier: verifier, credential: Credential{MachineID: uuid.NewString(), DeviceID: uuid.NewString(), ClientID: "ono9krqynydwx5"}}
	if strings.HasPrefix(target, "qoder:") {
		site, _, client := qoderSite(target)
		q := url.Values{"challenge": {challenge}, "challenge_method": {"S256"}, "nonce": {l.State}, "machine_id": {l.credential.MachineID}, "client_id": {client}}
		l.URL = site + "/device/selectAccounts?" + q.Encode()
	} else {
		key, err := newTraeKey()
		if err != nil {
			return LoginView{}, err
		}
		l.credential.DeviceKey = key
		q := url.Values{"login_version": {"1"}, "auth_from": {"trae"}, "login_channel": {"native_ide"}, "plugin_version": {traePluginVersion}, "auth_type": {"local"}, "client_id": {l.credential.ClientID}, "redirect": {"0"}, "login_trace_id": {l.State}, "auth_callback_url": {"http://127.0.0.1:1024/authorize"}, "machine_id": {l.credential.MachineID}, "device_id": {l.credential.DeviceID}, "x_device_id": {l.credential.DeviceID}, "x_machine_id": {l.credential.MachineID}, "x_device_brand": {""}, "x_device_type": {"PC"}, "x_os_version": {"Linux"}, "x_app_version": {traeVersion}, "x_app_type": {"stable"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
		l.URL = "https://www.trae.cn/authorization?" + q.Encode()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, v := range m.logins {
		if time.Now().After(v.ExpiresAt) {
			delete(m.logins, id)
		}
	}
	m.logins[l.State] = l
	if !strings.HasPrefix(target, "qoder:") {
		listener, err := net.Listen("tcp4", "127.0.0.1:1024")
		if err == nil {
			mux := http.NewServeMux()
			server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
			l.callback = server
			l.localCallback = true
			mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				_, err := m.CompleteLogin(ctx, l.State, "http://127.0.0.1:1024"+r.URL.RequestURI())
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				if err != nil {
					w.WriteHeader(400)
					_, _ = w.Write([]byte("授权未完成，请回到面板检查授权结果。"))
					return
				}
				_, _ = w.Write([]byte("授权已保存，请返回管理面板。"))
			})
			go func() {
				if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Printf("[providers] 本地授权回调服务: %v", err)
				}
			}()
			go func() {
				timer := time.NewTimer(time.Until(l.ExpiresAt))
				defer timer.Stop()
				<-timer.C
				if err := server.Close(); err != nil {
					log.Printf("[providers] 关闭本地授权回调: %v", err)
				}
			}()
		} else {
			log.Printf("[providers] 本地授权端口 1024 不可用，使用面板粘贴回调")
		}
	}
	return loginView(l), nil
}
func (m *Manager) CompleteLogin(ctx context.Context, state, callback string) (LoginView, error) {
	m.mu.Lock()
	l := m.logins[state]
	m.mu.Unlock()
	if l == nil {
		return LoginView{}, errors.New("授权会话不存在或已过期")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	defer func() {
		if l.AccountID == "" {
			return
		}
		l.credential = Credential{}
		l.verifier = ""
		l.deviceToken = ""
		if l.callback != nil {
			callbackServer := l.callback
			l.callback = nil
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := callbackServer.Shutdown(ctx); err != nil {
					log.Printf("[providers] 停止本地授权回调: %v", err)
				}
			}()
		}
	}()
	if time.Now().After(l.ExpiresAt) {
		return LoginView{}, errors.New("授权已过期，请重新开始")
	}
	if l.AccountID != "" {
		return loginView(l), nil
	}
	routed, err := m.route(ctx, l.ProxyID)
	if err != nil {
		return LoginView{}, err
	}
	ctx = routed
	c := l.credential
	c, err = m.qoderIdentity(ctx, l.Target, l.ProxyID, c)
	if err != nil {
		return LoginView{}, err
	}
	l.credential = c
	if strings.HasPrefix(l.Target, "qoder:") {
		q := m.adapters["qoder"].(*Qoder)
		api := q.openAPI(l.Target)
		if l.deviceToken == "" {
			if time.Since(l.lastPoll) < 2*time.Second {
				return loginView(l), nil
			}
			l.lastPoll = time.Now()
			query := url.Values{"nonce": {l.State}, "verifier": {l.verifier}, "challenge_method": {"S256"}}
			resp, err := request(ctx, q.HTTP, "GET", api+"/api/v1/deviceToken/poll?"+query.Encode(), nil, qoderAPIHeaders(""))
			if err != nil {
				var upstreamErr *Error
				if errors.As(err, &upstreamErr) && upstreamErr.Status == http.StatusNotFound {
					return loginView(l), nil
				}
				return LoginView{}, fmt.Errorf("%s 领取设备令牌: %w", l.Target, err)
			}
			if resp.StatusCode == 202 || resp.StatusCode == 204 {
				resp.Body.Close()
				return loginView(l), nil
			}
			dt, err := decodeResponse(resp)
			if err != nil {
				return LoginView{}, err
			}
			// 先保留一次性轮询结果，后续失败时从未完成的阶段重试。
			l.deviceToken = text(dt["token"])
			if l.deviceToken == "" {
				return loginView(l), nil
			}
			if l.Target == "qoder:cn" {
				// 国内 CLI 直接使用设备令牌，不再兑换 job token。
				c.AccessToken = l.deviceToken
				c.RefreshToken = text(dt["refresh_token"])
				c.TokenKind = "device"
				c.ExpiresAt = oauthExpiry(dt, c.TokenKind)
				l.credential = c
			}
		}
		if c.AccessToken == "" {
			_, _, client := qoderSite(l.Target)
			jt, err := jsonRequest(ctx, q.HTTP, "POST", api+"/api/v1/me/jobToken", map[string]any{"clientId": client}, qoderAPIHeaders(l.deviceToken))
			if err != nil {
				return LoginView{}, fmt.Errorf("%s 兑换 job token: %w", l.Target, err)
			}
			c.AccessToken = text(jt["token"])
			c.RefreshToken = text(jt["refresh_token"])
			c.TokenKind = "job"
			c.ExpiresAt = oauthExpiry(jt, c.TokenKind)
			if c.AccessToken == "" || c.ExpiresAt == 0 {
				return LoginView{}, errors.New("Qoder 授权结果缺少有效 token 或过期时间")
			}
			l.credential = c
		}
		if c.ExpiresAt == 0 {
			return LoginView{}, errors.New("Qoder 设备令牌缺少有效过期时间，请重新授权")
		}
		if c.UID == "" {
			c, err = q.tokenUser(ctx, api, l.deviceToken, c)
			if err != nil {
				return LoginView{}, fmt.Errorf("%s 核验授权用户信息: %w", l.Target, err)
			}
		}
	} else if c.AccessToken == "" {
		if callback == "" && l.localCallback {
			return loginView(l), nil
		}
		u, err := url.Parse(callback)
		if err != nil {
			return LoginView{}, errors.New("授权回调地址格式不对")
		}
		values := u.Query()
		trace := values.Get("loginTraceID")
		if trace == "" {
			trace = values.Get("loginTraceId")
		}
		if trace == "" {
			trace = values.Get("login_trace_id")
		}
		if trace == "" {
			trace = values.Get("state")
		}
		if trace != l.State {
			return LoginView{}, errors.New("授权回调不属于本次登录")
		}
		code := values.Get("code")
		if code == "" {
			code = values.Get("auth_code")
		}
		if code == "" {
			code = values.Get("authCode")
		}
		if info := values.Get("authCodeInfo"); info != "" {
			var authCodeInfo struct {
				AuthCode string `json:"AuthCode"`
			}
			if err := json.Unmarshal([]byte(info), &authCodeInfo); err != nil {
				return LoginView{}, errors.New("Trae 授权回调的 authCodeInfo 格式不对")
			}
			code = authCodeInfo.AuthCode
		}
		if code == "" {
			return LoginView{}, errors.New("授权回调缺少授权码")
		}
		t := m.adapters[strings.Split(l.Target, ":")[0]].(*Trae)
		device, err := traeDevice(c)
		if err != nil {
			return LoginView{}, err
		}
		root, err := jsonRequest(upstream.WithClientProfile(ctx, upstream.ProfileTraeAuth), t.HTTP, "POST", t.authBase()+"/trae/api/v3/oauth/ExchangeToken", map[string]any{"ClientID": c.ClientID, "AuthCode": code, "CodeVerifier": l.verifier, "IDEVersion": traeVersion, "DeviceInfo": device}, traeAuthHeaders(c))
		if err != nil {
			return LoginView{}, fmt.Errorf("%s 兑换授权码: %w", l.Target, err)
		}
		c, err = decodeTraeCredential(c, root)
		if err != nil {
			return LoginView{}, err
		}
		l.credential = c
		c, err = t.user(ctx, c)
		if err != nil {
			return LoginView{}, fmt.Errorf("%s 核验授权用户信息: %w", l.Target, err)
		}
	}
	l.credential = c
	id, err := m.Import(ctx, AccountInput{Target: l.Target, ProxyID: l.ProxyID, Credential: c})
	if err != nil {
		return LoginView{}, fmt.Errorf("%s 保存授权账号: %w", l.Target, err)
	}
	l.AccountID = id
	return loginView(l), nil
}
func qoderSite(target string) (string, string, string) {
	if target == "qoder:cn" {
		return "https://qoder.cn", "https://openapi.qoder.com.cn", "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"
	}
	return "https://qoder.com", "https://openapi.qoder.sh", "732aef47-9cf2-46a2-95fe-4cebb5d0d1fa"
}
func oauthExpiry(v map[string]any, tokenKind string) int64 {
	for _, k := range []string{"expire_time", "expires_at"} {
		if n := seconds(v[k]); n > 0 {
			return n
		}
	}
	if n := number(v["expires_in"]); n > 0 {
		if tokenKind == "job" {
			n /= 1000
		}
		return time.Now().Unix() + n
	}
	return 0
}
func (q *Qoder) tokenUser(ctx context.Context, api, token string, c Credential) (Credential, error) {
	user, err := jsonRequest(ctx, q.HTTP, "GET", api+"/api/v1/userinfo", nil, qoderAPIHeaders(token))
	if err != nil {
		return c, err
	}
	c.UID = text(user["id"])
	if c.UID == "" {
		c.UID = text(user["user_id"])
	}
	if c.UID == "" {
		return c, errors.New("Qoder 用户信息缺少稳定账号身份")
	}
	if name := text(user["name"]); name != "" {
		c.Name = name
	}
	return c, nil
}

func (q *Qoder) refreshOAuth(ctx context.Context, target string, c Credential) (Credential, error) {
	api := q.resolvedOpenAPI(ctx, target)
	path := "/api/v1/jobToken/refresh"
	body := map[string]any{"refresh_token": c.RefreshToken}
	if c.TokenKind == "device" {
		path = "/api/v1/deviceToken/refresh"
		body["machine_id"] = c.MachineID
		if c.MachineToken != "" {
			body["machine_token"] = c.MachineToken
		}
	}
	root, err := jsonRequest(ctx, q.HTTP, "POST", api+path, body, qoderAPIHeaders(""))
	if err != nil {
		return c, err
	}
	token := text(root["token"])
	if c.TokenKind == "device" && text(root["device_token"]) != "" {
		token = text(root["device_token"])
	}
	rt := text(root["refresh_token"])
	if token == "" || rt == "" {
		return c, errors.New("Qoder 刷新响应缺少新令牌对")
	}
	c.AccessToken = token
	c.RefreshToken = rt
	c.ExpiresAt = oauthExpiry(root, c.TokenKind)
	if c.ExpiresAt == 0 {
		return c, errors.New("Qoder 刷新响应缺少有效过期时间")
	}
	if uid := text(root["user_id"]); uid != "" {
		c.UID = uid
		return c, nil
	}
	fresh, err := q.tokenUser(ctx, api, c.AccessToken, c)
	return fresh, routeErrorAfterDispatch(err)
}
