// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// login.go 登录请求：发起授权（auth/state）、轮询令牌（auth/token）、取账号（account），
// 形状对齐官方桌面端后台服务的登录。
//
// 依据：官方桌面端安装包（国内版 WorkBuddy-darwin-arm64-5.8.3、国际版 5.8.4）里登录由后台
// 服务发，国内版走 AccountScopedExternalLinkAuthenticationProvider，国际版走
// ExternalLinkAuthenticationProvider；发起授权另加
// WorkbuddyAuthStateHeaderProvider 的 X-Custom-Channel-Id 与设备令牌（官方拿得到 qimei36 时
// 还发 X-Device-Id-List，网关没有，不发）。10-20 读安装包确认形状，同日生产实测发起授权、
// 轮询（未登录时返回 code=11217）、取账号正常；授权页地址与参数和旧版 CLI 相同。10-31 与国内版
// 5.9.3、国际版 5.9.4 按函数比对，这几个登录函数没变。
//
// 三个请求都不带旧 CLI 的 Origin / Referer，域固定为国内版 www.workbuddy.cn、国际版
// www.workbuddy.ai（不读 ChatBaseCN 等配置）。
//
// 链路头见 trace.go：一次登录一条 trace（LoginTrace），发起授权（国内版）、轮询令牌、取账号带
// 根 span 的 X-Trace-ID / b3 / X-B3-*，每类请求再带各自子 span 的 traceparent。国际版的
// 发起授权官方把根 span 的头写在了请求头对象外面（ExternalLinkAuthenticationProvider.
// fetchAuthState 里 `...t.traceSpan.requestHeaders` 展开在 headers 之后），实际只带子 span
// 的 traceparent，网关照发。授权页地址按官方 openAuthUrl 追加 version，国内版另加
// traceparent、loginSessionId、login_owner_id。国际版取到账号后官方还会取一次账号列表
// （getAccountSnapshot：GET /v2/plugin/accounts，失败不影响登录），网关同样发。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
)

// ErrLoginAuthStateIncomplete 发起授权的响应缺 state 或 authUrl。
var ErrLoginAuthStateIncomplete = errors.New("auth state: missing state or authUrl")

const (
	// loginCodePending auth/token 的「还没登录完成」业务码。
	loginCodePending = 11217
	// loginCodeAccountNotReady account 的「账号还没准备好」业务码，等一会儿再试。
	loginCodeAccountNotReady = 12151
	// 发起授权被拒的三个授权业务码（官方 LICENSE_ERROR_CODES，随 HTTP 错误状态返回）：
	// 席位已满、授权已过期、试用已过期。官方只在发起授权这一步认，直接弹上游的 msg。
	loginCodeLicenseSeatLimit = 12005
	loginCodeLicenseExpired   = 11212
	loginCodeTrialExpired     = 11216
	// loginAccountRetries 取账号遇到 12151 时的最多重试次数（首次之外再试这么多次）。
	loginAccountRetries = 5
	// loginPlatformCN / loginPlatformGlobal auth/state 的 platform 参数。
	loginPlatformCN     = "workbuddy"
	loginPlatformGlobal = "workbuddy-ai"
)

// loginAccountRetryDelay 取账号遇到 12151 时的重试间隔；包级变量便于测试改成 0。
var loginAccountRetryDelay = time.Second

// 国内版登录请求的超时（官方桌面端：发起授权 8 秒，轮询令牌与取账号 4 秒）；
// 只在原超时更长或没有时下调；包级变量便于测试调整。国际版不设。
var (
	loginStateTimeout   = 8 * time.Second
	loginPollTimeout    = 4 * time.Second
	loginAccountTimeout = 4 * time.Second
)

// LoginAuthState 发起授权的结果。
type LoginAuthState struct {
	State   string     `json:"state"`
	AuthURL string     `json:"authUrl"`
	Trace   LoginTrace `json:"-"` // 这次登录的链路，后续轮询、取账号原样传回
}

// LoginToken 轮询到的令牌。Raw 是响应 data 原文，供调用方原样输出整个令牌包；
// Msg 只在 pending 时填上游提示（如 code=11217 msg=…），供面板回显等待原因。
type LoginToken struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"`
	Domain       string `json:"domain"`
	// TokenType 官方刷新令牌后核对身份用（见 auth.Auth.TokenType），没有这个键时为 nil。
	TokenType *string         `json:"tokenType"`
	Raw       json.RawMessage `json:"-"`
	Msg       string          `json:"-"`
}

// LoginAccount 授权成功后取到的账号信息。
type LoginAccount struct {
	UID          string `json:"uid"`
	Nickname     string `json:"nickname"`
	EnterpriseID string `json:"enterpriseId"`
	// DepartmentFullName 企业号的部门全名（个人号没有这个键，为 nil），官方请求头 X-Department-Info 的值。
	DepartmentFullName *string `json:"departmentFullName"`
	// Type、OneIDAccountID、IDP 官方刷新令牌后核对身份用（见 auth.Auth.AccountType），没有这个键时为 nil。
	Type           *string `json:"type"`
	OneIDAccountID *string `json:"oneidAccountId"`
	IDP            *string `json:"idp"`
}

// ApplyTo 把登录取到的账号与令牌里官方核对身份用的字段写进凭证（部门全名、类型、
// oneidAccountId、idp、tokenType）。
func (acct LoginAccount) ApplyTo(a *auth.Auth, tok LoginToken) {
	if acct.DepartmentFullName != nil {
		a.DepartmentFullName, a.DepartmentSet = *acct.DepartmentFullName, true
	}
	a.AccountType, a.OneIDAccountID, a.IDP, a.TokenType = acct.Type, acct.OneIDAccountID, acct.IDP, tok.TokenType
}

// LoginError 登录端点的上游错误：Status 是 HTTP 状态码（网络错误时调用方拿到的是普通
// error，没有状态码），Code 是业务码（HTTP 错误时取响应体的信封，没有信封或解析错误时为空）。
// 错误文本保留旧形状（http_error: upstream <状态码>[ code=<n> msg=<msg>] / code=<n> msg=<msg>），
// 调用方照旧拼提示。
type LoginError struct {
	Status int
	Code   int
	Msg    string
	text   string
}

func (e *LoginError) Error() string {
	if e.text != "" {
		return e.text
	}
	if e.Status >= 300 {
		if e.Code != 0 {
			return fmt.Sprintf("http_error: upstream %d code=%d msg=%s", e.Status, e.Code, e.Msg)
		}
		return fmt.Sprintf("http_error: upstream %d", e.Status)
	}
	return fmt.Sprintf("code=%d msg=%s", e.Code, e.Msg)
}

// LicenseReason 发起授权因账号授权被拒时的中文原因（席位已满 / 授权已过期 / 试用已过期），
// 其他错误返回空串。对应官方 toSignLicenseError：只认 HTTP 错误响应里的这三个业务码。
func (e *LoginError) LicenseReason() string {
	if e.Status < 300 {
		return ""
	}
	switch e.Code {
	case loginCodeLicenseSeatLimit:
		return "席位已满"
	case loginCodeLicenseExpired:
		return "授权已过期"
	case loginCodeTrialExpired:
		return "试用已过期"
	}
	return ""
}

// loginKind 登录请求的种类，决定带哪些头。
type loginKind int

const (
	loginKindState loginKind = iota
	loginKindToken
	loginKindAccount
	loginKindAccounts // 国际版取到账号后的账号列表（getAccountSnapshot）
)

// loginEndpoint 按 realm 返回登录域与 platform：只有 "global" 为国际版，其他一律国内版。
func loginEndpoint(realm string) (base, platform string, global bool) {
	if realm == "global" {
		return defaultGlobalBase, loginPlatformGlobal, true
	}
	return cnProductEndpoint, loginPlatformCN, false
}

// loginRealmAuth 给登录请求造一个只标了域的账号，供 daemonUserAgent 选国内版 / 国际版 UA。
func loginRealmAuth(realm string) *auth.Auth {
	a := &auth.Auth{}
	r := "cn"
	if realm == "global" {
		r = "global"
	}
	_, _ = auth.BackfillRealmFor(a, r)
	return a
}

// loginClient 登录请求用的 client；未注入时回落 http.DefaultClient。
func (c *Client) loginClient() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// loginRequest 构造登录请求并设置头。
func (c *Client) loginRequest(method, rawURL, realm string, kind loginKind, tok LoginToken, tr LoginTrace) (*http.Request, error) {
	var body io.Reader
	if kind == loginKindState {
		body = bytes.NewReader([]byte("{}"))
	}
	req, err := http.NewRequestWithContext(withLoginRoute(context.Background(), tr.Proxy), method, rawURL, body)
	if err != nil {
		return nil, err
	}
	c.loginHeaders(req, realm, kind, tok, tr)
	return req, nil
}

// loginHeaders 按官方桌面端后台服务的登录请求设头。三种请求的公共头：Accept、
// X-Requested-With、每个请求新生成的 X-Request-ID（32 位 hex）、X-Product、后台服务 UA。
// X-Domain 发起授权为登录域 host，轮询不带，取账号用令牌的 Domain（空则登录域 host）。
// 发起授权另带 Content-Type、X-Custom-Channel-Id 与（有值时）设备令牌。登录前的
// X-No-Authorization / X-No-User-Id / X-No-Enterprise-Id / X-No-Department-Info 见规格：
// 发起授权与轮询全带，取账号只有国际版带。旧 CLI 的 Origin / Referer、账号头一概不发。
// 链路头见文件头。
func (c *Client) loginHeaders(req *http.Request, realm string, kind loginKind, tok LoginToken, tr LoginTrace) {
	base, _, global := loginEndpoint(realm)
	host := strings.TrimPrefix(base, "https://")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Encoding", acceptEncodingAxios)
	if kind == loginKindState {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Request-ID", session.NewMessageID())
	req.Header.Set("X-Product", deploymentType)
	if ua := c.daemonUserAgent(loginRealmAuth(realm)); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	dt := tr.daemon()
	switch kind {
	case loginKindState:
		if !global {
			dt.setRoot(req)
		}
		req.Header.Set("traceparent", dt.traceparent(tr.childSpan("get_auth_state")))
		req.Header.Set("X-Domain", host)
		req.Header.Set("X-Custom-Channel-Id", "unknown")
		if t := c.resolveDeviceToken(nil); t != "" {
			req.Header.Set("X-Device-Token", t)
		}
		req.Header.Set("X-No-Authorization", "true")
		loginNoIdentity(req)
	case loginKindToken:
		dt.setRoot(req)
		req.Header.Set("traceparent", dt.traceparent(tr.childSpan("get_auth_token")))
		req.Header.Set("X-No-Authorization", "true")
		loginNoIdentity(req)
	case loginKindAccount, loginKindAccounts:
		dt.setRoot(req)
		span := "get_account"
		if kind == loginKindAccounts {
			span = "get_account_list"
		}
		req.Header.Set("traceparent", dt.traceparent(tr.childSpan(span)))
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		d := tok.Domain
		if d == "" {
			d = host
		}
		req.Header.Set("X-Domain", d)
		if global && kind == loginKindAccount {
			loginNoIdentity(req)
		}
	}
}

// loginNoIdentity 登录前的「不认用户身份」头（官方三个都是 true）。
func loginNoIdentity(req *http.Request) {
	req.Header.Set("X-No-User-Id", "true")
	req.Header.Set("X-No-Enterprise-Id", "true")
	req.Header.Set("X-No-Department-Info", "true")
}

// loginDo 构造并发出一次登录请求；构造失败（URL 非法）与请求失败都返回 err。
// 国内版按官方超时（8/4 秒）加 ctx 期限；发起授权失败时按地址层换 IP 重发。
// 国际版保持现状：不设超时、不换 IP。
func (c *Client) loginDo(method, rawURL, realm string, kind loginKind, tok LoginToken, tr LoginTrace) (code int, msg string, data json.RawMessage, status int, err error) {
	req, err := c.loginRequest(method, rawURL, realm, kind, tok, tr)
	if err != nil {
		return 0, "", nil, 0, err
	}
	_, _, global := loginEndpoint(realm)
	if global {
		return c.loginCall(req, false, 0)
	}
	timeout := loginPollTimeout
	switch kind {
	case loginKindState:
		timeout = loginStateTimeout
	case loginKindAccount, loginKindAccounts:
		timeout = loginAccountTimeout
	}
	return c.loginCall(req, kind == loginKindState, timeout)
}

// loginCall 发一次登录请求并解信封；allowAddr 时失败按地址层换 IP 重发（只用于国内版
// 发起授权）。timeout > 0 时每次尝试各自限时（官方每次重发都是一个新请求，超时重新算）。
// HTTP ≥300 与解析失败返回 *LoginError；业务码非 0 不在这里报错，交给调用方判断
// （11217 / 12151 各有专门处理）。
func (c *Client) loginCall(req *http.Request, allowAddr bool, timeout time.Duration) (code int, msg string, data json.RawMessage, status int, err error) {
	attempt := func(r *http.Request, rt http.RoundTripper) (int, string, json.RawMessage, int, error) {
		if timeout > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel() // loginCallOnce 读完响应体才返回，返回后再 cancel 不影响结果
			r = r.WithContext(ctx)
		}
		return c.loginCallOnce(r, rt)
	}
	code, msg, data, status, err = attempt(req, nil)
	if !allowAddr {
		return
	}
	host := req.URL.Hostname()
	if !c.addrLayerOn() || !failoverTrustedHost(host) {
		return
	}
	var slot *addrSlot
	pinned := ""
	for {
		if !loginAuthStateRetryable(status, data, err) {
			if pinned != "" && err == nil && loginAuthStateValid(data) {
				c.noteAddrSuccess(host, pinned)
			}
			return
		}
		next := c.nextAddrSlot(req.Context(), slot, host)
		if next == nil {
			return
		}
		slot = next
		pinned = slot.current()
		retryReq, cerr := retryRequest(req)
		if cerr != nil {
			return
		}
		code, msg, data, status, err = attempt(retryReq, c.PinIP(pinned))
	}
}

// loginCallOnce 发一次登录请求（rt 非 nil 时用地址层的钉 IP Transport）并解信封。
func (c *Client) loginCallOnce(req *http.Request, rt http.RoundTripper) (code int, msg string, data json.RawMessage, status int, err error) {
	hc := c.loginClient()
	if rt != nil {
		cp := *hc
		cp.Transport = rt
		hc = &cp
	}
	resp, err := doDecoded(hc, req)
	if err != nil {
		return 0, "", nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, "", nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	status = resp.StatusCode
	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if status >= 300 {
		// 错误响应也可能带信封（授权被拒的 12005 等），解得出就把业务码和提示带上
		_ = json.Unmarshal(raw, &env)
		return 0, "", nil, status, &LoginError{Status: status, Code: env.Code, Msg: env.Msg}
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, "", nil, status, &LoginError{Status: status, text: fmt.Sprintf("parse failed: %v", err)}
	}
	return env.Code, env.Msg, env.Data, status, nil
}

// loginAuthStateValid auth/state 的 200 响应是否合格：data 是对象且 state、authUrl
// 都是非空字符串（官方把不合格的 200 当「可重试的无效响应」）。
func loginAuthStateValid(data json.RawMessage) bool {
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return false
	}
	state, ok := obj["state"].(string)
	if !ok || state == "" {
		return false
	}
	authURL, ok := obj["authUrl"].(string)
	return ok && authURL != ""
}

// loginAuthStateRetryable 报告发起授权这次失败是否换 IP：HTTP 408/502/503、没有 HTTP
// 状态的传输错误（含 DNS 失败），或 200 但响应体不合格。4xx/500/504 等不换。
func loginAuthStateRetryable(status int, data json.RawMessage, err error) bool {
	if status == 0 {
		return err != nil && !errors.Is(err, context.Canceled) && !IsRouteError(err)
	}
	if status >= 300 {
		switch status {
		case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable:
			return true
		}
		return false
	}
	// HTTP 200：解析失败或 data 不合格都算「可重试的无效响应」。
	if err != nil {
		return true
	}
	return !loginAuthStateValid(data)
}

// LoginAuthState 发起授权：POST <域>/v2/plugin/auth/state?platform=<P>，body 为 {}。
// State 或 AuthURL 缺失算错误。每次新开一条登录链路（Trace），授权页地址按官方 openAuthUrl 修饰。
// proxy 是这次登录走的代理（代理池里的 ID，空为默认线路），记在 Trace 里，后面的请求都走它。
func (c *Client) LoginAuthState(realm, proxy string) (LoginAuthState, error) {
	base, platform, _ := loginEndpoint(realm)
	tr := NewLoginTrace()
	tr.Proxy = proxy
	code, msg, data, status, err := c.loginDo(http.MethodPost,
		base+"/v2/plugin/auth/state?platform="+platform, realm, loginKindState, LoginToken{}, tr)
	if err != nil {
		return LoginAuthState{}, err
	}
	if code != 0 {
		return LoginAuthState{}, &LoginError{Status: status, Code: code, Msg: msg}
	}
	var st LoginAuthState
	if err := json.Unmarshal(data, &st); err != nil || st.State == "" || st.AuthURL == "" {
		return LoginAuthState{}, ErrLoginAuthStateIncomplete
	}
	st.Trace = tr
	st.AuthURL = c.decorateAuthURL(st.AuthURL, realm, tr)
	return st, nil
}

// decorateAuthURL 按官方 openAuthUrl 给授权页地址追加参数：两个域都追加 version=<客户端版本>
// （clientInfoProvider.pluginVersion）；国内版（platform=workbuddy）另加登录观测的
// traceparent、loginSessionId、login_owner_id（appendLoginTraceParams、
// appendHostLoginJourneyParams）。直接拼在原查询串后面，保持官方 URLSearchParams 的先后顺序。
func (c *Client) decorateAuthURL(raw, realm string, tr LoginTrace) string {
	q := "version=" + url.QueryEscape(c.clientVersion(loginRealmAuth(realm)))
	if realm != "global" {
		q += "&traceparent=" + url.QueryEscape(tr.daemon().traceparent(tr.childSpan("open_external_link"))) +
			"&loginSessionId=" + url.QueryEscape(tr.SessionID) +
			"&login_owner_id=" + url.QueryEscape(tr.OwnerID)
	}
	base, frag, hasFrag := strings.Cut(raw, "#")
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	out := base + sep + q
	if hasFrag {
		out += "#" + frag
	}
	return out
}

// LoginPollToken 轮询一次令牌：GET <域>/v2/plugin/auth/token?state=<state>。
// 业务码 11217 → pending=true、err=nil（提示在 LoginToken.Msg）；成功 → 令牌；
// 其他业务码、HTTP 错误、空 accessToken 都是错误。
// tr 是 LoginAuthState 返回的 Trace（零值时现生成一条）。
func (c *Client) LoginPollToken(realm, state string, tr LoginTrace) (LoginToken, bool, error) {
	base, _, _ := loginEndpoint(realm)
	code, msg, data, status, err := c.loginDo(http.MethodGet,
		base+"/v2/plugin/auth/token?state="+url.QueryEscape(state), realm, loginKindToken, LoginToken{}, tr.orNew())
	if err != nil {
		return LoginToken{}, false, err
	}
	if code == loginCodePending {
		return LoginToken{Msg: fmt.Sprintf("code=%d msg=%s", code, msg)}, true, nil
	}
	if code != 0 {
		return LoginToken{}, false, &LoginError{Status: status, Code: code, Msg: msg}
	}
	var tok LoginToken
	if err := json.Unmarshal(data, &tok); err != nil {
		return LoginToken{}, false, &LoginError{Status: status, text: fmt.Sprintf("parse failed: %v", err)}
	}
	if tok.AccessToken == "" {
		return LoginToken{}, false, &LoginError{Status: status, text: "waiting for login"}
	}
	tok.Raw = data
	return tok, false, nil
}

// LoginFetchAccount 取账号：国内版 GET <域>/v2/plugin/account，国际版
// GET <域>/v2/plugin/login/account?state=<state>，带 Bearer 令牌。业务码 12151
// （账号还没准备好）时等 loginAccountRetryDelay 再试，最多再试 loginAccountRetries 次。
// 国际版取到账号后再取一次账号列表（官方 getAccountSnapshot），失败只记日志。
// tr 是 LoginAuthState 返回的 Trace（零值时现生成一条）。
func (c *Client) LoginFetchAccount(realm, state string, tok LoginToken, tr LoginTrace) (LoginAccount, error) {
	base, _, global := loginEndpoint(realm)
	tr = tr.orNew()
	path := "/v2/plugin/account"
	if global {
		path = "/v2/plugin/login/account?state=" + url.QueryEscape(state)
	}
	for attempt := 0; ; attempt++ {
		code, msg, data, status, err := c.loginDo(http.MethodGet, base+path, realm, loginKindAccount, tok, tr)
		if err != nil {
			return LoginAccount{}, err
		}
		if code == loginCodeAccountNotReady {
			if attempt >= loginAccountRetries {
				return LoginAccount{}, &LoginError{Status: status, Code: code, Msg: msg}
			}
			time.Sleep(loginAccountRetryDelay)
			continue
		}
		if code != 0 {
			return LoginAccount{}, &LoginError{Status: status, Code: code, Msg: msg}
		}
		var acct LoginAccount
		if err := json.Unmarshal(data, &acct); err != nil {
			return LoginAccount{}, &LoginError{Status: status, text: fmt.Sprintf("parse failed: %v", err)}
		}
		if global {
			if code, _, _, status, err := c.loginDo(http.MethodGet, base+"/v2/plugin/accounts", realm, loginKindAccounts, tok, tr); err != nil || code != 0 {
				log.Printf("WARN: [upstream] login accounts snapshot: status=%d code=%d err=%v", status, code, err)
			}
		}
		return acct, nil
	}
}
