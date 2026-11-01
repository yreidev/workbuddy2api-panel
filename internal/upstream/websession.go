// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// websession.go 每个账号的官网网页登录态（Cookie 罐）及其获取流程。
//
// 依据（10-20 读官方桌面端 5.8.3 与官网前端代码确认；生产实测见 web.go 文件头）：
//  1. 桌面端打开官网页面时，后台服务主进程用 Node 内置 fetch 发
//     POST <产品入口>/v2/plugin/device/auth/code（无请求体），头带账号令牌与刷新令牌，
//     换回一次性登录码 deviceCode（expiresIn 300）。
//  2. 浏览器再打开 <官网>/console/client-login?code=<登录码>&target=<页面路径>，网站 302 跳到
//     target 并种下 session Cookie。
//  3. 之后网页的 axios 实例只靠 Cookie 认人（withCredentials），不带 Authorization。
//
// client-login 的跳转照浏览器跟随：浏览器打开 client-login 后跟着 302 打开 target 页面。网关
// 手动跟随（Go 自动跟随会补 Referer，浏览器从外部打开的导航不带），每一跳用同一套导航头和
// Cookie 罐，页面内容读完丢掉；页面里的脚本、图片和页面自己发的请求不模拟。跟随出错不影响
// 登录态：Cookie 在第一跳就种下了，成败只看罐里有没有 session。
//
// 为什么被拒（HTTP 401）只重试一次：登录态是登录码换来的、有效期 7 天，401 通常意味着服务端
// 侧会话已失效；清罐重取一次即可覆盖这种失效。连续 401 说明凭据本身有问题（如 refresh token
// 失效），再重试只会放大请求，按原错误返回给调用方。
package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
)

const (
	// webDeviceAuthCodePath 一次性登录码（官方后台服务用 Node fetch 发，无请求体）。
	webDeviceAuthCodePath = "/v2/plugin/device/auth/code"
	// webClientLoginPath 浏览器打开后用登录码换 Cookie 的页面。
	webClientLoginPath = "/console/client-login"
	// webSessionCookieName 网页登录态的 Cookie 名；罐里没有它就要重新获取。
	webSessionCookieName = "session"
)

// webSessionStore 按「域 + uid」存每个账号的 Cookie 罐。零值可用，不需要构造函数。
type webSessionStore struct {
	mu   sync.Mutex
	jars map[string]*webSession
}

// webSession 一个账号在一个域上的登录态。mu 同时保护 jar 与「同时只有一个 goroutine
// 在获取登录态」——后来者阻塞在 mu 上，拿到后先查罐里已有 session 就直接用它的结果。
type webSession struct {
	mu  sync.Mutex
	jar http.CookieJar
	// gen 每次成功获取登录态 +1。并发 401 时用它判断「我发请求之后有没有别的 goroutine
	// 已经换过登录态」，避免同一个新罐被重复换。
	gen uint64
}

// get 取（必要时新建）某个账号的登录态。键是「域 + uid」。
func (s *webSessionStore) get(key string) *webSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jars == nil {
		s.jars = map[string]*webSession{}
	}
	ws := s.jars[key]
	if ws == nil {
		jar, _ := cookiejar.New(nil) // nil 选项不会失败；Cookie 都是 host-only，不需要公共后缀表。
		ws = &webSession{jar: jar}
		s.jars[key] = ws
	}
	return ws
}

// webSessionKey 登录态的键：域 + uid（同一账号在两个域各一个罐）。
func (c *Client) webSessionKey(a *auth.Auth) string {
	uid := ""
	if a != nil {
		uid = a.UID
	}
	return c.webBase(a) + "|" + uid
}

// jarNow 取当前 Cookie 罐与代数（短锁，避免和换罐并发读写）。
func (ws *webSession) jarNow() (http.CookieJar, uint64) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.jar, ws.gen
}

// webSessionValue 返回罐里对 base 有效的 session Cookie 值；没有或已过期（cookiejar 自己
// 过滤过期项）返回空串。
func webSessionValue(jar http.CookieJar, base string) string {
	if jar == nil {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	for _, ck := range jar.Cookies(u) {
		if ck.Name == webSessionCookieName && ck.Value != "" {
			return ck.Value
		}
	}
	return ""
}

// ensureWebSession 保证这个账号有可用的网页登录态：罐里有 session 直接返回，没有就获取。
// 同一账号同时只有一个 goroutine 真正获取，其余在 ws.mu 上等它的结果。
func (c *Client) ensureWebSession(a *auth.Auth, referer string) (*webSession, error) {
	ws := c.webSessions.get(c.webSessionKey(a))
	base := c.webBase(a)
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if webSessionValue(ws.jar, base) != "" {
		return ws, nil
	}
	if err := c.acquireWebSessionLocked(a, ws, referer); err != nil {
		return nil, err
	}
	return ws, nil
}

// renewWebSession 网页请求被 401 拒后重新获取登录态。gen 是发请求时罐的代数：如果别的
// goroutine 已经换过（代数变了），直接用它的结果重发，不重复换。调用方之后用返回值的罐重发一次。
func (c *Client) renewWebSession(a *auth.Auth, ws *webSession, referer string, gen uint64) (*webSession, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.gen != gen {
		return ws, nil
	}
	jar, _ := cookiejar.New(nil)
	ws.jar = jar
	if err := c.acquireWebSessionLocked(a, ws, referer); err != nil {
		return nil, err
	}
	return ws, nil
}

// acquireWebSessionLocked 获取一次登录态：登录码 → client-login。调用方必须持有 ws.mu。
func (c *Client) acquireWebSessionLocked(a *auth.Auth, ws *webSession, referer string) error {
	code, err := c.fetchDeviceAuthCode(a)
	if err == nil {
		err = c.loginWebSession(a, ws, code, referer)
	}
	label := "-"
	if a != nil {
		label = logfmt.Label(a.UID, a.Nickname)
	}
	if err != nil {
		// 只打账号标签与错误文本；不打印 Cookie、令牌、登录码。
		log.Printf("WARN: [upstream] web_session acct=%s: 获取网页登录态失败: %v", label, err)
		return err
	}
	ws.gen++
	log.Printf("INFO: [upstream] web_session acct=%s: 已获取网页登录态", label)
	return nil
}

// fetchDeviceAuthCode 发登录码请求。形状是官方后台服务的裸 fetch：无请求体，只带账号头与
// 刷新令牌，不带 X-Request-ID / X-Product / X-Requested-With 等请求框架的头。
// 默认头取安装包里 undici 6.25.0 的源码（未抓包）。
func (c *Client) fetchDeviceAuthCode(a *auth.Auth) (string, error) {
	req, err := newAccountRequest(context.Background(), a, http.MethodPost, c.webBase(a)+webDeviceAuthCodePath, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// Node 内置 fetch（undici）补的默认头：Accept、Accept-Language、Sec-Fetch-Mode、UA、
	// Accept-Encoding（见 encoding.go）。
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "*")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("User-Agent", "node")
	req.Header.Set("Accept-Encoding", acceptEncodingNodeFetch)
	if a != nil {
		req.Header.Set("X-User-Id", a.UID)
	}
	if at := a.AccessTokenValue(); at != "" {
		req.Header.Set("Authorization", "Bearer "+at)
	}
	if a != nil && a.IsEnterprise() {
		// 企业号两个头同值；个人号都不带。
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	if d := a.DomainValue(); d != "" {
		req.Header.Set("X-Domain", d)
	}
	if rt := a.RefreshTokenValue(); rt != "" {
		req.Header.Set("X-Refresh-Token", rt)
	}

	resp, err := doDecoded(c.HTTP, req)
	if err != nil {
		return "", fmt.Errorf("登录码请求: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("登录码响应读取: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &Error{Kind: Classify(resp.StatusCode, string(raw)), Status: resp.StatusCode,
			Msg: "登录码: " + truncate(string(raw), 160)}
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("登录码响应解析失败: %w", err)
	}
	if env.Code != 0 {
		kind := Classify(resp.StatusCode, env.Msg)
		if kind == ErrNone {
			kind = ErrClient
		}
		return "", &Error{Kind: kind, Status: resp.StatusCode,
			Msg: fmt.Sprintf("登录码 code=%d msg=%s", env.Code, truncate(env.Msg, 160))}
	}
	var data struct {
		DeviceCode string `json:"deviceCode"`
		ExpiresIn  int    `json:"expiresIn"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return "", fmt.Errorf("登录码数据解析失败: %w", err)
	}
	if data.DeviceCode == "" {
		return "", &Error{Kind: ErrClient, Status: resp.StatusCode, Msg: "登录码为空"}
	}
	return data.DeviceCode, nil
}

// loginWebSession 用登录码打开 client-login 页面换 Cookie，再照浏览器跟随跳转（见文件头）。
// client-login 本身被拒、或罐里没出现 session 视为失败。调用方必须持有 ws.mu。
func (c *Client) loginWebSession(a *auth.Auth, ws *webSession, deviceCode, referer string) error {
	u, err := url.Parse(c.webBase(a) + webClientLoginPath)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("code", deviceCode)
	q.Set("target", webLoginTarget(referer))
	u.RawQuery = q.Encode()

	req, err := newAccountRequest(context.Background(), a, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	c.webNavigationHeaders(req, a)

	hc := *c.HTTP
	hc.Jar = ws.jar
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := doDecoded(&hc, req)
	if err != nil {
		return fmt.Errorf("client-login 请求: %w", err)
	}
	status := resp.StatusCode
	drainClose(resp)
	if status < 200 || status >= 400 {
		return &Error{Kind: Classify(status, ""), Status: status, Msg: "client-login 被拒"}
	}
	c.followNavigation(&hc, a, req.URL, resp)
	if webSessionValue(ws.jar, c.webBase(a)) == "" {
		return &Error{Kind: ErrClient, Status: status, Msg: "client-login 未种下 session Cookie"}
	}
	return nil
}

// webMaxRedirects 跟随跳转的上限（Chrome 是 20 跳）。
const webMaxRedirects = 20

// followNavigation 照浏览器跟随导航的跳转：GET Location（相对地址按 from 解析），导航头不变、
// 不带 Referer，页面内容读完丢掉。只跟 http / https；出错就停（不影响调用方的结果）。
func (c *Client) followNavigation(hc *http.Client, a *auth.Auth, from *url.URL, resp *http.Response) {
	for hop := 0; hop < webMaxRedirects; hop++ {
		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
			http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		default:
			return
		}
		ref, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || resp.Header.Get("Location") == "" {
			return
		}
		loc := from.ResolveReference(ref)
		if loc.Scheme != "http" && loc.Scheme != "https" {
			return
		}
		loc.Fragment = ""
		req, err := newAccountRequest(context.Background(), a, http.MethodGet, loc.String(), nil)
		if err != nil {
			return
		}
		c.webNavigationHeaders(req, a)
		next, err := doDecoded(hc, req)
		if err != nil {
			// 只记底层错误，不记地址（跳转地址可能带参数）。
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			log.Printf("upstream: 网页登录跟随跳转出错（不影响登录态）: %v", err)
			return
		}
		drainClose(next)
		from, resp = loc, next
	}
}

// drainClose 读完（最多 1 MiB）并关闭响应体，让连接能复用。
func drainClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

// webNavigationHeaders 浏览器直接打开页面（导航）的请求头。没有 Referer / Origin。
func (c *Client) webNavigationHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("User-Agent", c.webUserAgent())
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	c.webClientHints(req, a)
}

// webLoginTarget 登录码的 target 参数：触发这次获取的网页请求的 Referer 页面。传入路径常量
// （webPageGrowthCenter 等）时原样用；传入完整 URL（网页上报的 pageURL）时取路径 + 查询串。
func webLoginTarget(referer string) string {
	if referer == "" {
		return webPageGrowthCenter
	}
	if u, err := url.Parse(referer); err == nil && u.Scheme != "" && u.Host != "" {
		if u.Path == "" {
			return "/"
		}
		return u.RequestURI()
	}
	return referer
}

// doWebJSON 发网页请求：先确保登录态，带这个账号的 Cookie 罐发送；HTTP 401 时清罐重取
// 登录态并重发一次（见文件头），还是 401 就按原错误返回。build 每次调用都造一个新请求
// （重发要一个新 body reader）。
func (c *Client) doWebJSON(a *auth.Auth, referer string, build func() (*http.Request, error)) (json.RawMessage, error) {
	ws, err := c.ensureWebSession(a, referer)
	if err != nil {
		return nil, err
	}
	req, err := build()
	if err != nil {
		return nil, err
	}
	jar, gen := ws.jarNow()
	data, err := c.doWebOnce(jar, req)
	ue, ok := err.(*Error)
	if !ok || ue.Status != http.StatusUnauthorized {
		return data, err
	}
	ws, err = c.renewWebSession(a, ws, referer, gen)
	if err != nil {
		return nil, err
	}
	req, err = build()
	if err != nil {
		return nil, err
	}
	jar, _ = ws.jarNow()
	return c.doWebOnce(jar, req)
}

// doWebOnce 用指定 Cookie 罐发一次请求并解信封。语义与 client.go 的 doJSON 一致
// （HTTP 非 2xx / 业务 code != 0 → *Error）；不能直接复用 doJSON，因为它固定用
// c.HTTP（没有 Cookie 罐）。
func (c *Client) doWebOnce(jar http.CookieJar, req *http.Request) (json.RawMessage, error) {
	hc := *c.HTTP
	hc.Jar = jar
	resp, err := doDecoded(&hc, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		kind := Classify(resp.StatusCode, string(raw))
		return nil, &Error{Kind: kind, Status: resp.StatusCode, Msg: truncate(string(raw), 200)}
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse failed: %w (body: %s)", err, truncate(string(raw), 120))
	}
	if env.Code != 0 {
		kind := Classify(resp.StatusCode, env.Msg)
		if kind == ErrNone {
			kind = ErrClient
		}
		return nil, &Error{Kind: kind, Status: resp.StatusCode, Msg: fmt.Sprintf("code=%d msg=%s", env.Code, truncate(env.Msg, 160))}
	}
	return env.Data, nil
}
