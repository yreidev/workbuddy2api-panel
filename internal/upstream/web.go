// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// web.go 官网网页形状的请求头与请求发送：成长中心（任务、猫猫、连登、抽奖、热力图、
// 补签卡）、积分补偿、新手礼包、试用。
//
// 依据：这些接口只由官网网页发（桌面端点“成长中心”会在浏览器里打开 www.workbuddy.cn 的
// /profile/growth-center）。10-20 读官网前端代码：growthSpace-*.js 是成长中心接口模块，config-*.js
// 是 axios 实例，index-*.js 里有积分补偿、新手礼包、试用。网页的 axios 实例只靠 Cookie 认人
// （withCredentials），不带 Authorization；X-Client-Platform 在普通浏览器里是 web。10-20 生产
// 实测（国内版、国际版各一个个人号）：按官方流程用登录码换到 session Cookie 后，只带这些 Cookie
// 不带 Authorization 时，国内版的成长任务列表、账号资料、抽奖次数都返回 200 code=0；国际版
// 账号资料、抽奖次数也 200（国际版没有成长体系，网关对国际版不调成长中心功能）；什么都不带时
// 回 401、响应体不是 JSON。登录态与 Cookie 罐见 websession.go。
//
// 请求头形状对照网页的 axios 实例：不带桌面端后台服务那套 X-Product、X-Request-ID、
// X-Requested-With、X-User-Id、X-Domain、X-Tenant-Id 等头；另补 Chrome 自动带的
// Sec-CH-UA* / Accept-Language / Sec-Fetch-*（见 webClientHints）。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// browserUA 网页请求的默认 UA。网页在用户自己的浏览器里打开；网关上报的桌面端是 macOS
// （见 desktop.go 的 deviceOS），网页 UA 取同一台 Mac 上的 Chrome，版本取任务中心抓包里的
// 网页上报（2026-09，Chrome 152）。Chrome 的精简 UA 在 Mac 上固定写 Intel Mac OS X 10_15_7，
// Apple 芯片也一样。
const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"

// 网页页面路径（Referer = webBase + 页面路径）。
const (
	webPageGrowthCenter    = "/profile/growth-center"    // 成长中心：任务、猫猫旅行、连登、抽奖、热力图、补签卡
	webPageUsage           = "/profile/usage"            // 用量管理页：积分补偿、新手礼包
	webPageProfile         = "/profile"                  // 个人主页：试用加油包
	webPageAccountSettings = "/profile/account-settings" // 账号设置页：账号资料（昵称同步）
)

// webUserAgent 网页请求的 UA：config user_agent 显式覆盖优先，否则 browserUA。
func (c *Client) webUserAgent() string {
	if c != nil && c.UserAgent != "" {
		return c.UserAgent
	}
	return browserUA
}

// growthAPIPath 成长中心接口的路径片段。官网 config-*.js 的请求拦截器对地址含它的请求
// （含 /v2/activity/growth/...）另设 Accept-Language，见 webAcceptLanguage。
const growthAPIPath = "/activity/growth"

// webAcceptLanguage 网页请求的 Accept-Language。成长中心接口照官网拦截器显式设成 zh-CN，
// 页面地址带 lang=en… 时才是 en-US（国际版按英文页面算，网关不对国际版调成长中心）；
// 其余网页请求是 Chrome 按界面语言自动带的形状（带 q 权重），国内版 zh-CN、国际版 en-US。
func webAcceptLanguage(a *auth.Auth, path string) string {
	global := a != nil && a.IsGlobal()
	if strings.Contains(path, growthAPIPath) {
		if global {
			return "en-US"
		}
		return "zh-CN"
	}
	if global {
		return "en-US,en;q=0.9"
	}
	return "zh-CN,zh;q=0.9"
}

// webClientHints 补 Chrome 自动带的 UA 客户端提示与 Accept-Language（成长中心接口另有取值，
// 见 webAcceptLanguage）。只在用内置 browserUA 时补 Sec-CH-UA*——配置了 user_agent 后
// UA 与客户端提示对不上，浏览器不会这样发。
// Sec-CH-UA 的取值按 Chromium 的 GREASE 规则由主版本 152 推出（推算，未抓包）。
func (c *Client) webClientHints(req *http.Request, a *auth.Auth) {
	if c == nil || c.UserAgent == "" {
		req.Header.Set("Sec-CH-UA", `"Chromium";v="152", "Not?A_Brand";v="24", "Google Chrome";v="152"`)
		req.Header.Set("Sec-CH-UA-Mobile", "?0")
		req.Header.Set("Sec-CH-UA-Platform", `"macOS"`)
	}
	req.Header.Set("Accept-Language", webAcceptLanguage(a, req.URL.Path))
	req.Header.Set("Accept-Encoding", acceptEncodingChrome)
}

// webHeaders 设置官网网页形状的 XHR 请求头。referer 是页面路径常量（webPageGrowthCenter 等）。
// 不带 Authorization（网页靠 Cookie 认人，罐由 webJSON 套上）、X-User-Id、X-Domain、X-Tenant-Id、
// X-Product、X-Request-ID、X-Requested-With、X-CodeBuddy-Request、X-Device-Token——网页都不发。
func (c *Client) webHeaders(req *http.Request, a *auth.Auth, referer string) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	// axios 没有请求体时会去掉 Content-Type；有 body 的 POST 才带。
	if req.Body != nil && req.Body != http.NoBody {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Client-Platform", "web")
	if a != nil && a.IsEnterprise() {
		// 网页企业号只带 X-Enterprise-Id，不带桌面端那套同值的 X-Tenant-Id。
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
	}
	base := c.webBase(a)
	// 浏览器同源 GET 不带 Origin；非 GET 才带。
	if req.Method != http.MethodGet {
		req.Header.Set("Origin", base)
	}
	req.Header.Set("Referer", base+referer)
	req.Header.Set("User-Agent", c.webUserAgent())
	c.webClientHints(req, a)
	// XHR 的 Sec-Fetch-*：同源 cors 请求。
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
}

// webJSON 按官网网页的形状发请求并解信封：请求发往 webBase(a)+path，请求头见 webHeaders，
// 带上这个账号的网页登录态（Cookie 罐，见 websession.go）；body 为 nil 时不带请求体。
// HTTP 401 时清罐重取登录态并重发一次。
// 错误语义与 doJSON 一致：HTTP 非 2xx / 业务 code != 0 → *Error。
func (c *Client) webJSON(a *auth.Auth, method, path, referer string, body any) (json.RawMessage, error) {
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	build := func() (*http.Request, error) {
		var rdr io.Reader
		if raw != nil {
			rdr = bytes.NewReader(raw)
		}
		req, err := newAccountRequest(context.Background(), a, method, c.webBase(a)+path, rdr)
		if err != nil {
			return nil, err
		}
		c.webHeaders(req, a, referer)
		return req, nil
	}
	return c.doWebJSON(a, referer, build)
}

// 官网前端从 UA 解析系统（q4）的几条规则，按顺序匹配。
var (
	webUAMac     = regexp.MustCompile(`Mac OS X ([\d._]+)`)
	webUAWindows = regexp.MustCompile(`Windows NT ([\d.]+)`)
	webUAAndroid = regexp.MustCompile(`Android ([\d.]+)`)
	webUAIOS     = regexp.MustCompile(`iPhone OS ([\d._]+)|iPad; CPU OS ([\d._]+)`)
	webUAIOSVer  = regexp.MustCompile(`OS ([\d._]+)`)
)

// webOSFromUA 照官网前端的 q4 从 UA 取 os 与 osVersion：macOS / Windows / Android / iOS / Linux，
// 版本里的下划线换成点；都不匹配时两个都是空串。
func webOSFromUA(ua string) (string, string) {
	switch {
	case webUAMac.MatchString(ua):
		return "macOS", strings.ReplaceAll(webUAMac.FindStringSubmatch(ua)[1], "_", ".")
	case webUAWindows.MatchString(ua):
		return "Windows", webUAWindows.FindStringSubmatch(ua)[1]
	case webUAAndroid.MatchString(ua):
		return "Android", webUAAndroid.FindStringSubmatch(ua)[1]
	case webUAIOS.MatchString(ua):
		return "iOS", strings.ReplaceAll(webUAIOSVer.FindStringSubmatch(ua)[1], "_", ".")
	case strings.Contains(ua, "Linux"):
		return "Linux", ""
	}
	return "", ""
}

// webPageSessionID 官网前端每次打开页面生成的 sessionId：毫秒时间戳加 9 位随机 36 进制
// （Date.now() + "-" + Math.random().toString(36).substring(2, 11)）。
func webPageSessionID(nowMs int64) string {
	var b strings.Builder
	for b.Len() < 9 {
		b.WriteString(strconv.FormatUint(rand.Uint64N(36), 36))
	}
	return fmt.Sprintf("%d-%s", nowMs, b.String())
}
