// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// headers.go 构造上游请求头：对话（ChatHeaders）、桌面端后台服务经请求框架发的请求（BillingHeaders）、
// 后台服务 AuthService 自己发的积分与签到（authServiceHeaders）、刷新令牌（RefreshHeaders），以及请求
// 框架的公共形状（frameworkHeaders）。
package upstream

import (
	"net/http"
	"strings"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
)

// 官方取值依据：WorkBuddy 桌面端安装包（国内版 WorkBuddy-darwin-arm64-5.9.3、国际版 5.9.4，都内置 CLI
// @tencent-ai/codebuddy-code 2.165.0）。对话、刷新令牌、模型目录由内置
// CLI 发；积分、签到、成长任务、旅行、上报由桌面端后台服务发。两者用同一套请求框架，UA 段序不同。
const (
	// defaultClientVersionCN / defaultClientVersionGlobal 两域桌面端版本（UA 与 X-IDE-Version）。
	// config upstream.client_version 可覆盖（两域共用一个值，空 = 内置默认）。
	defaultClientVersionCN     = "5.9.3"
	defaultClientVersionGlobal = "5.9.4"
	// defaultCliVersionCN / defaultCliVersionGlobal 两域桌面端内置 CLI 的版本（UA 的 `CLI/<ver>` 段）。
	// config upstream.cli_version 可覆盖（两域共用一个值，空 = 内置默认）。
	defaultCliVersionCN     = "2.165.0"
	defaultCliVersionGlobal = "2.165.0"
	// deploymentType 两域 product.json 的 deploymentType。官方请求框架的 ProductEndpointHttpInterceptor
	// 把它作为 X-Product 加到经过框架的每个请求上（对话、刷新令牌、模型目录、积分等都在内）。
	deploymentType = "SaaS"
	// X-Stainless-* 的固定取值依据：内置 CLI 的对话经 OpenAI SDK（@openai 的 JS 版）发出，SDK 的
	// buildHeaders 给每个请求加这组头（见 cli/dist/lazy-lite-wb/3043.*.mjs，2.161.0 与 2.165.0 是同一份），
	// axiosToFetchAdapter 把 SDK 的头原样交给出站（只删 authorization 与 user-agent）。
	//   - stainlessPackageVersion：SDK 版本（X-Stainless-Package-Version），两域内置 CLI 同为 6.25.0。
	//   - stainlessTimeout：X-Stainless-Timeout，SDK 取客户端 timeout（桌面端 resolveRequestTimeoutMs
	//     默认 1200000ms，未设 CODEBUDDY_FIRST_TOKEN_TIMEOUT_MS）/1000 = 1200。
	//   - stainlessRetryCount：X-Stainless-Retry-Count 是当前重试计数；桌面端 maxRetries=0，只发一次，恒 0。
	stainlessPackageVersion = "6.25.0"
	stainlessTimeout        = "1200"
	stainlessRetryCount     = "0"
	// nodeRuntimeVersionCN / nodeRuntimeVersionGlobal 两域桌面端 Electron 内置的 Node 版本
	// （X-Stainless-Runtime-Version = process.version）。国内版 5.9.3、国际版 5.9.4 都是 Electron 37.10.3
	// 内置 v22.21.1（2026-10-28、10-31 读 Electron Framework 实测，与 5.8.x 相同）。
	nodeRuntimeVersionCN     = "v22.21.1"
	nodeRuntimeVersionGlobal = "v22.21.1"
)

// clientVersion 生效的桌面端版本：Client.ClientVersion 非空则取之，否则按账号域取内置默认。
func (c *Client) clientVersion(a *auth.Auth) string {
	if c != nil && c.ClientVersion != "" {
		return c.ClientVersion
	}
	if a != nil && a.IsGlobal() {
		return defaultClientVersionGlobal
	}
	return defaultClientVersionCN
}

// cliVersion 生效的 CLI 版本：Client.CliVersion 非空则取之，否则按账号域取内置默认。
func (c *Client) cliVersion(a *auth.Auth) string {
	if c != nil && c.CliVersion != "" {
		return c.CliVersion
	}
	if a != nil && a.IsGlobal() {
		return defaultCliVersionGlobal
	}
	return defaultCliVersionCN
}

// applicationNameFor 两域桌面端 product.json 的 applicationName：国内版 "WorkBuddy"，国际版
// "workbuddy-ai"（productName 是 "WorkBuddy AI"，UA 里用的是 applicationName）。
func applicationNameFor(a *auth.Auth) string {
	if a != nil && a.IsGlobal() {
		return "workbuddy-ai"
	}
	return "WorkBuddy"
}

// defaultCLIUA 内置 CLI 的 UA（UserAgentHttpInterceptor）：
// `<platform>/<ver> <applicationName>/<ver> CLI/<cli>`，platform 由桌面端固定传 "WorkBuddy"。
// 国内版 `WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0`，国际版 `WorkBuddy/5.9.4 workbuddy-ai/5.9.4 CLI/2.165.0`。
func (c *Client) defaultCLIUA(a *auth.Auth) string {
	v := c.clientVersion(a)
	return "WorkBuddy/" + v + " " + applicationNameFor(a) + "/" + v + " CLI/" + c.cliVersion(a)
}

// defaultDaemonUA 桌面端后台服务的 UA（WorkbuddyUserAgentHttpInterceptor）：
// `<applicationName>/<ver> <platform>/<ver> CLI/<cli>`。后台服务的 platform 取产品配置的
// applicationName（WorkbuddyClientInfoProvider.initClientInfo）：桌面端只给内置 CLI 的进程设
// CLIENT_INFO_PLATFORM=WorkBuddy，后台服务的子进程环境里没有（buildDaemonProcessEnv），所以两段都是
// 应用名。国内版 `WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0`，和 CLI 的 UA 一样；国际版
// `workbuddy-ai/5.9.4 workbuddy-ai/5.9.4 CLI/2.165.0`。2026-10-31 读 5.8.3、5.9.4 安装包核对，同类项目
// misakano7545/workbuddy2api-panel 的真机抓包也是这样；1.8.0 及以前国际版第二段发的是 WorkBuddy。
func (c *Client) defaultDaemonUA(a *auth.Auth) string {
	app, v := applicationNameFor(a), c.clientVersion(a)
	return app + "/" + v + " " + app + "/" + v + " CLI/" + c.cliVersion(a)
}

// userAgent 内置 CLI 发的请求（对话、模型目录）的 UA。
// config user_agent 显式覆盖优先（几类请求共用这一个值）。
func (c *Client) userAgent(a *auth.Auth) string {
	if c != nil && c.UserAgent != "" {
		return c.UserAgent
	}
	return c.defaultCLIUA(a)
}

// daemonUserAgent 桌面端后台服务经请求框架发的请求（登录、刷新令牌、上报）的 UA。
// config user_agent 显式覆盖优先；显式 client_name="SaaS"（旧行为）时不设 UA。
func (c *Client) daemonUserAgent(a *auth.Auth) string {
	if c != nil && c.UserAgent != "" {
		return c.UserAgent
	}
	if c.attributionClientName() == "SaaS" {
		return ""
	}
	return c.defaultDaemonUA(a)
}

// refreshUserAgent 刷新令牌和刷新后取账号的 UA。桌面端的令牌由后台服务刷新（common2.js 里两个登录
// 提供者的 refreshSession，走后台服务的请求框架），所以取后台服务的 UA；国内版和对话的 UA 一样，国际版
// 两段都是 workbuddy-ai（1.8.0 及以前用的是对话的 UA）。client_name="SaaS"（旧行为）时后台服务的请求
// 不带 UA，刷新仍照以前带对话的 UA。
func (c *Client) refreshUserAgent(a *auth.Auth) string {
	if ua := c.daemonUserAgent(a); ua != "" {
		return ua
	}
	return c.userAgent(a)
}

// resolveDeviceToken 解析本次请求的 X-Device-Token 取值。
// 优先级：auth.Auth.DeviceToken（每号）> Client.DeviceToken（config 全局）> 文件兜底。
// 三者皆空/读失败则返回空串（调用方不注入该头，优雅降级）。
func (c *Client) resolveDeviceToken(a *auth.Auth) string {
	if a != nil && a.DeviceToken != "" {
		return a.DeviceToken
	}
	if c != nil && c.DeviceToken != "" {
		return c.DeviceToken
	}
	if c != nil && c.DeviceTokenFile != "" {
		return readDeviceTokenFile(c.DeviceTokenFile)
	}
	return ""
}

// injectDeviceToken 在 req 注入 X-Device-Token 头（仅当取到非空 token）。用在签到
// （官方 buildHeadersWithTuringToken）、登录前的 auth/state，以及对话上。官方的对话也带：桌面端后台
// 服务每次提问前现取图灵盾设备令牌加在模型请求上（取不到时改发 X-Device-Token-Error，两样都没有
// 时都不发）。网关在 Linux 做不出真令牌，对话只在使用者填了 device_token 时照发，没填就不发——对应
// 官方「两样都没有」的状态，不造 X-Device-Token-Error，见 docs/official-client.md「几类请求」。
func (c *Client) injectDeviceToken(req *http.Request, a *auth.Auth) {
	if tok := c.resolveDeviceToken(a); tok != "" {
		req.Header.Set("X-Device-Token", tok)
	}
}

// frameworkHeaders 官方请求框架（restOperations）给非对话请求加的头：axios 的 Accept、框架默认的
// X-Requested-With，以及各拦截器补的头——CommonHeaderHttpInterceptor 的 X-Request-ID（32 位
// 十六进制，每个请求新生成）、ProductEndpointHttpInterceptor 的 X-Product、UA、
// AuthenticationHttpInterceptor 的账号头（Authorization、X-User-Id、企业号的 X-Enterprise-Id 与同值的
// X-Tenant-Id、X-Domain，账号有部门全名时的 X-Department-Info）。
// Content-Type 只在 GET 以外的请求上设（官方 GET 不带，POST 都带 JSON 体；网关几个不带体的 POST
// 不是桌面端的接口，保持以前也带的样子）。官方不发的 Origin / Referer / Accept-Language /
// X-CodeBuddy-Request / X-Machine-ID / X-Session-ID 一概不发；需要的个别请求（积分的
// Accept-Language、签到的 X-Device-Token、刷新的 X-Refresh-Token）由调用方另加。ua 为空不设 UA。
func (c *Client) frameworkHeaders(req *http.Request, a *auth.Auth, ua string) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Encoding", acceptEncodingAxios)
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if req.Header.Get("X-Request-ID") == "" {
		req.Header.Set("X-Request-ID", session.NewMessageID())
	}
	req.Header.Set("X-Product", deploymentType)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if at := a.AccessTokenValue(); at != "" {
		req.Header.Set("Authorization", "Bearer "+at)
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	if a.IsEnterprise() {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	injectDepartment(req, a)
	if d := chatDomain(a); d != "" {
		req.Header.Set("X-Domain", d)
	}
}

// injectDepartment 账号有部门全名（企业号）时带 X-Department-Info，值原样（官方
// AuthenticationHttpInterceptor：account.departmentFullName 非空才加）。官方用 Node 发请求，
// 头的值里有超出 Latin-1 的字符时 Node 会直接报错；上游返回的部门全名是什么样子没有企业号
// 实测过（未确认），网关照原样发 UTF-8。
func injectDepartment(req *http.Request, a *auth.Auth) {
	if d := a.DepartmentValue(); d != "" {
		req.Header.Set("X-Department-Info", d)
	}
}

// billingAcceptLanguage 积分包查询的 Accept-Language：官方按界面语言只发 "zh" 或 "en"
// （AuthService 的 resolveAcceptLanguage），国内版取 zh、国际版取 en。
func billingAcceptLanguage(a *auth.Auth) string {
	if a != nil && a.IsGlobal() {
		return "en"
	}
	return "zh"
}

// ChatMeta 一次 chat 出站的会话头族元数据（issue #35：后台按 X-Conversation-Request-ID
// 聚合请求，官方客户端一次 user send 内所有 tool call/重试/换号复用同一个 ID）。
// handler 在轮转循环外生成 conversationID / conversationRequestID，循环内每次出站
// 复用同值；TraceID 透传入站值（空 = 回落 conversationRequestID）。
// messageID（消息级，每条独立）由 ChatHeaders 内部生成，无需外部可见。
type ChatMeta struct {
	ConversationID        string // X-Conversation-ID：入站对话由 handler 定好（session.OutboundConversationID）；空则新生成
	ConversationRequestID string // X-Conversation-Request-ID：聚合主键，必发
	TraceID               string // X-Trace-ID：入站透传值，空则回落 conversationRequestID
	ExpertID              string // X-Expert-Id：专家对话的专家 id；空 = 普通对话，不发
	ExpertTeamTask        bool   // X-Expert-Team-Task：专家团（市场 expert_type=team）为 true，只和 ExpertID 一起发
	Protected             bool   // X-Moderation-Type: ["protected"]：云端 / 付费专家的对话才发（见 MarketExpert.Protected）
}

// ChatHeaders 设置 chat 出站请求头。形状对齐桌面端内置 CLI 的对话请求（两域都是 2.165.0，见文件头），
// 10-19 生产用国内版、国际版个人号逐项实测均 200（当时两域都是 2.161.0）：
//   - 官方对话请求不带 Origin / Referer / Accept-Language / X-CodeBuddy-Request / X-Machine-ID /
//     X-Session-ID，也不带 X-No-*（那几个只在登录前的 auth/state、auth/token 上发）。
//   - 账号头：Authorization、X-User-Id；企业号加 X-Enterprise-Id 与同值的 X-Tenant-Id，个人号
//     两个都不发；有部门全名时加 X-Department-Info；X-Domain 取登录域（国际版固定
//     www.workbuddy.ai），没有就不发。
//   - X-Agent-Intent=craft、X-Agent-Type=main（主对话）、X-Private-Data（见 privateDataValue）。
//   - X-Agent-Runtime=local-agent（见 agentRuntime）。
//   - UA、X-IDE-* 取桌面端传给内置 CLI 的值（见 defaultCLIUA、injectAttribution）；X-Product 是
//     请求框架给每个请求加的 deploymentType。
//   - Accept 与 8 个 X-Stainless-* 照 OpenAI SDK 的 buildHeaders 发（见 injectSDKHeaders）：官方对话
//     经 SDK 发出，这组头是 SDK 加的、出站适配器原样保留的。
//   - X-Device-Token：官方后台服务每次提问前加（图灵盾令牌）；网关在 Linux 做不出真令牌，只在
//     使用者填了 device_token 时照发（injectDeviceToken），没填不发。
//   - X-Moderation-Type：官方后台对云端 / 付费专家的对话加（shouldProtectSession）；网关只在
//     专家对话且该专家云端 / 付费时发（meta.Protected，见 MarketExpert.Protected），普通对话不带。
//
// clientIP 为本次请求的客户端 IP（按参数传递，不读共享字段——避免并发串扰）；
// PassthroughIP=false 或 clientIP 为空时不注入 IP 头。
// meta 为会话头族元数据，见 injectConversationHeaders。
func (c *Client) ChatHeaders(req *http.Request, a *auth.Auth, clientIP string, meta ChatMeta) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", acceptEncodingAxios)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", c.userAgent(a))
	// AccessToken 加锁快照：keepalive 定时刷新会在 a.mu 内改写它，锁外直读构成数据竞争
	// （见 auth.AccessTokenValue 注释）。
	if at := a.AccessTokenValue(); at != "" {
		req.Header.Set("Authorization", "Bearer "+at)
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	// 安全红线：绝不在 chat 请求里携带 X-Refresh-Token。
	// 企业号带企业 ID 与 cli2api、dsh-workbuddy-connect 的对话头一致；国际版企业号本仓库未实测。
	if a.IsEnterprise() {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	injectDepartment(req, a)
	if d := chatDomain(a); d != "" {
		req.Header.Set("X-Domain", d)
	}
	req.Header.Set("X-Agent-Intent", "craft")
	req.Header.Set("X-Agent-Type", "main")
	req.Header.Set("X-Agent-Runtime", agentRuntime)
	req.Header.Set("X-Private-Data", c.privateDataValue(a))
	req.Header.Set("X-Product", deploymentType)
	// 用量归属头（X-Agent-Purpose / X-IDE-*）与 OpenAI SDK 的对话头（Accept、X-Stainless-*）：
	// 默认都伪装官方桌面端；client_name="SaaS" 还原旧行为（见各自函数注释）。
	c.injectAttribution(req, a)
	c.injectSDKHeaders(req, a)
	// X-Device-Token：官方后台服务每次提问前现取图灵盾设备令牌加在模型请求上；网关在 Linux
	// 做不出真令牌，只在使用者填了 device_token（账号 / config / 文件）时照发，没填就不发——
	// 对应官方「令牌和错误码都没有时都不发」的状态，不凭空造 X-Device-Token-Error。
	c.injectDeviceToken(req, a)
	// X-Expert-Id 是官方专家插件激活时追加的内部请求头，专家团另带 X-Expert-Team-Task: true
	// （桌面端 applyPreparedExpertSettings：expertType === "team"）；普通对话都不带。
	if meta.ExpertID != "" {
		req.Header.Set("X-Expert-Id", meta.ExpertID)
		if meta.ExpertTeamTask {
			req.Header.Set("X-Expert-Team-Task", "true")
		}
	}
	// X-Moderation-Type: ["protected"]：官方后台对云端 / 付费专家的对话加（shouldProtectSession）；
	// 网关只在专家对话且该专家云端 / 付费时发（meta.Protected，见 MarketExpert.Protected）。普通对话不发。
	if meta.Protected {
		req.Header.Set("X-Moderation-Type", `["protected"]`)
	}
	// 客户端 IP 透传（仅 PassthroughIP=true 且本次请求带 IP）。
	c.injectClientIP(req, clientIP)
	// X-Conversation-ID 官方每条都发（值是整段对话不变的会话 id）。国际版不带一律 400
	// code=11128 "request illegal"（10-19 生产实测），国内版不带也能用，同样补上对齐官方。
	// 入站对话的 id 由 handler 定好（同一段对话不变，换号也不变，上游据此命中前缀缓存，
	// 见 session/convids.go）；调用方没给就新生成一个，形状同官方新会话的 id（带连字符的 UUID v7）。
	if meta.ConversationID == "" {
		meta.ConversationID = session.NewUUIDv7()
	}
	// 会话头族（对话/请求/消息/链路），见 injectConversationHeaders。
	c.injectConversationHeaders(req, meta)
}

// agentRuntime 对话和联网搜索的 X-Agent-Runtime：2.165.0 每条都带，取会话的 agentRuntime，两域桌面端
// （国内版 5.9.3、国际版 5.9.4）给本地对话（含专家对话）设的都是 local-agent。国际版 5.8.4 内置的
// 2.161.0 没有这个头，网关 2026-10-31 随国际版换到 5.9.4 起两域都发。
const agentRuntime = "local-agent"

// chatDomain chat 出站的 X-Domain：国际版固定 www.workbuddy.ai（与国际版登录域相同，不回退
// 登录会话原值——对齐 intl 项目出站头）；国内版取登录域，没有就不发。
func chatDomain(a *auth.Auth) string {
	if a.IsGlobal() {
		return "www.workbuddy.ai"
	}
	return a.DomainValue()
}

// privateDataValue X-Private-Data 取值，告诉上游这段对话能不能拿去做模型优化。默认一律
// "true"（不允许）。两域内置的 2.165.0 在用户没动过「模型优化」开关时按产品开关
// DefaultModelOptimizationOff 算：国际版 5.9.4 包里是 true，个人版、企业版都发 "true"，和网关的默认
// 一样；国内版 5.9.3 包里是 false，个人版发 "false"、企业版发 "true"，所以对国内版来说网关的默认
// 等于用户在桌面端手动关了这个开关。2026-10-28 用户定：默认仍不允许。
// upstream.model_optimization 打开后个人版发 "false"；企业版（有企业 id，或账号类型是 ultimate /
// exclusive，同官方 isEnterpriseEditionAccount）仍发 "true"，和官方没动过开关时一样。
func (c *Client) privateDataValue(a *auth.Auth) string {
	if c != nil && c.ModelOptimization.Load() && isPersonalEdition(a.EnterpriseID, a.AccountTypeValue()) {
		return "false"
	}
	return "true"
}

// injectConversationHeaders 注入官方客户端会话头族（issue #35 后台聚合），各司其职：
//   - X-Conversation-ID：会话级（ChatHeaders 已保证非空：客户端给的会话 id、派生的或新生成的）。
//   - X-Conversation-Request-ID：**对话轮级聚合主键**，必发。一次 user send 内的
//     所有 tool call/重试/换号/降级复用同一个 → 后台按它聚合成一条（不再碎片化）。
//   - X-Conversation-Message-ID = X-Request-ID：消息级，每条独立（去掉连字符的 UUID v7）。
//   - X-Trace-ID：入站透传或 = conversationRequestID。
//   - 链路族：X-B3-TraceId / X-B3-SpanId / X-B3-Sampled，外加官方同发的 W3C traceparent
//     与单行 b3（同一组 trace/span id）。B3 规范只认 16/32 hex TraceId 与 16 hex SpanId；
//     入站 conversationRequestID 非法时 TraceId 回落 messageID（恒 32 hex），SpanId 每条消息
//     随机新生成。traceparent 只认 32 位小写 hex，16 位的左补零。
//
// 官方主对话不发 X-Root-Request-ID（只有子代理带），这里也不发。
func (c *Client) injectConversationHeaders(req *http.Request, meta ChatMeta) {
	convReqID := meta.ConversationRequestID
	if convReqID == "" {
		// 零值 meta（直接调 injectConversationHeaders 的测试）也要保证聚合主键必发：
		// 本级补一个，ChatHeaders 已生成稳定的不走到这里。
		convReqID = session.NewUUIDv7Hex()
	}
	// 消息 id 同官方 uuidv7() 去掉连字符；span id 是链路自己的随机 16 位（UUID v7 的前 16 位是
	// 时间戳，截它当 span id 会让同一毫秒的两条消息撞上）。
	messageID := session.NewUUIDv7Hex()
	if meta.ConversationID != "" {
		req.Header.Set("X-Conversation-ID", meta.ConversationID)
	}
	req.Header.Set("X-Conversation-Request-ID", convReqID)
	req.Header.Set("X-Conversation-Message-ID", messageID)
	req.Header.Set("X-Request-ID", messageID)
	traceID := meta.TraceID
	if traceID == "" {
		traceID = convReqID
	}
	req.Header.Set("X-Trace-ID", traceID)
	b3Trace := convReqID
	if !validTraceID(b3Trace) {
		b3Trace = messageID // 非法 B3 TraceId → 回落恒 32 hex 的消息级 ID
	}
	spanID := session.NewMessageID()[:16]
	req.Header.Set("X-B3-TraceId", b3Trace)
	req.Header.Set("X-B3-SpanId", spanID)
	req.Header.Set("X-B3-Sampled", "1")
	w3cTrace := strings.ToLower(b3Trace)
	if len(w3cTrace) == 16 {
		w3cTrace = strings.Repeat("0", 16) + w3cTrace
	}
	req.Header.Set("traceparent", "00-"+w3cTrace+"-"+spanID+"-01")
	req.Header.Set("b3", strings.ToLower(b3Trace)+"-"+spanID+"-1")
}

// validTraceID 判断 B3 TraceId 是否合法：16 或 32 位 hex（大小写均可）。
// 官方客户端生成的 conversationRequestId 是 32 位 hex（UUID 去横线），入站透传值
// 可能是任意形状（含横线/超长/非 hex），直接塞进 B3 头会破坏链路关联（issue #35）。
func validTraceID(s string) bool {
	if len(s) != 16 && len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

// attributionClientName 生效的用量归属名：ClientName 非空取之；
// 空默认 "WorkBuddy"（伪造官方桌面端指纹；显式配 "SaaS" 可还原旧行为）。
func (c *Client) attributionClientName() string {
	if c != nil && c.ClientName != "" {
		return c.ClientName
	}
	return "WorkBuddy"
}

// injectAttribution 注入用量归属头（X-Agent-Purpose / X-IDE-*）。仅在 chat/completions 路径生效
// （ChatHeaders 调用；X-Product 由 ChatHeaders 自己设）。
//
// 默认（ClientName 空）同官方桌面端：X-Agent-Purpose="conversation"，X-IDE-Name / X-IDE-Type
// 为桌面端传给 CLI 的 IDE_TYPE "WorkBuddy"，X-IDE-Version 为桌面端版本。显式 ClientName="SaaS"
// 还原旧行为（不设 X-Agent-Purpose 与 X-IDE-*）；配其他值则 X-IDE-Name / X-IDE-Type 跟随该值。
func (c *Client) injectAttribution(req *http.Request, a *auth.Auth) {
	name := c.attributionClientName()
	if name == "SaaS" {
		return
	}
	req.Header.Set("X-Agent-Purpose", "conversation")
	req.Header.Set("X-IDE-Name", name)
	req.Header.Set("X-IDE-Type", name)
	req.Header.Set("X-IDE-Version", c.clientVersion(a))
}

// injectSDKHeaders 注入官方对话经 OpenAI SDK 发出时 SDK 自带的头：Accept 与 8 个 X-Stainless-*。
// 官方内置 CLI 用 OpenAI SDK 的 fetch 适配器发对话，SDK buildHeaders 给每个请求加 Accept:
// application/json（chat/completions 不是二进制端点，走默认 Accept；这版 SDK 流式也不改 Accept，
// 由请求体 stream:true 决定流式）与这组 X-Stainless-* 指纹头，适配器只删 authorization 与
// user-agent，其余原样发出。取值见 stainless* 常量；OS / Arch 由设备指纹（deviceOS / deviceArch）
// 按 SDK 的 normalizePlatform / normalizeArch 映射而来，与 desktop.go 的设备信息同源，不会自相矛盾；
// Runtime-Version 取对应域桌面端 Electron 内置的 Node 版本。
//
// client_name="SaaS"（旧行为）不伪装桌面端：不发 X-Stainless-*，Accept 保持 1.x 起的两值。
func (c *Client) injectSDKHeaders(req *http.Request, a *auth.Auth) {
	if c.attributionClientName() == "SaaS" {
		req.Header.Set("Accept", "application/json, text/event-stream")
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Stainless-Retry-Count", stainlessRetryCount)
	req.Header.Set("X-Stainless-Timeout", stainlessTimeout)
	req.Header.Set("X-Stainless-Lang", "js")
	req.Header.Set("X-Stainless-Package-Version", stainlessPackageVersion)
	req.Header.Set("X-Stainless-OS", stainlessOS(deviceOS))
	req.Header.Set("X-Stainless-Arch", stainlessArch(deviceArch))
	req.Header.Set("X-Stainless-Runtime", "node")
	req.Header.Set("X-Stainless-Runtime-Version", nodeRuntimeVersion(a))
}

// nodeRuntimeVersion 本次对话 X-Stainless-Runtime-Version 的取值：按账号域取桌面端 Electron 的 Node 版本。
func nodeRuntimeVersion(a *auth.Auth) string {
	if a != nil && a.IsGlobal() {
		return nodeRuntimeVersionGlobal
	}
	return nodeRuntimeVersionCN
}

// stainlessOS 复刻 OpenAI SDK 的 normalizePlatform（lazy-lite-wb/3043）：把 Node 的 process.platform
// 映射成 X-Stainless-OS。设备指纹是 darwin（→ MacOS），这里一并覆盖桌面端可能的取值，换指纹时跟着变。
func stainlessOS(platform string) string {
	switch strings.ToLower(platform) {
	case "darwin":
		return "MacOS"
	case "win32":
		return "Windows"
	case "linux":
		return "Linux"
	case "freebsd":
		return "FreeBSD"
	case "openbsd":
		return "OpenBSD"
	case "":
		return "Unknown"
	default:
		return "Other:" + strings.ToLower(platform)
	}
}

// stainlessArch 复刻 OpenAI SDK 的 normalizeArch（lazy-lite-wb/3043）：把 Node 的 process.arch
// 映射成 X-Stainless-Arch。设备指纹是 arm64（→ arm64）。
func stainlessArch(arch string) string {
	switch arch {
	case "x32":
		return "x32"
	case "x64", "x86_64":
		return "x64"
	case "arm":
		return "arm"
	case "arm64", "aarch64":
		return "arm64"
	case "":
		return "unknown"
	default:
		return "other:" + arch
	}
}

// injectClientIP 在 PassthroughIP 开启时把 clientIP 参数透传给上游（三等价头）。
func (c *Client) injectClientIP(req *http.Request, clientIP string) {
	if c == nil || !c.PassthroughIP || clientIP == "" {
		return
	}
	req.Header.Set("X-Forwarded-For", clientIP)
	req.Header.Set("X-Real-IP", clientIP)
	req.Header.Set("X-Client-IP", clientIP)
}

// ExtractClientIP 从入站请求提取客户端 IP 首段（X-Forwarded-For 首段，回落 X-Real-IP）。
func ExtractClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return strings.TrimSpace(xff[:i])
			}
		}
		return strings.TrimSpace(xff)
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	return ""
}

// BillingHeaders 桌面端后台服务经请求框架发的请求（上报）的请求头：官方请求框架的形状
// （frameworkHeaders）+ 后台服务的 UA。积分、签到不走这里，见 authServiceHeaders。
func (c *Client) BillingHeaders(req *http.Request, a *auth.Auth) {
	c.frameworkHeaders(req, a, c.daemonUserAgent(a))
}

// axiosUserAgent 桌面端后台进程打包的 axios（1.16.1）不设 UA 时自带的 User-Agent。AuthService 的积分、
// 签到和容量排队的查询、取消都是它。
const axiosUserAgent = "axios/1.16.1"

// authServiceHeaders 桌面端后台服务 AuthService 发的请求（积分查询的汇总与两个逐包列表、企业用量、
// 签到状态与签到）的请求头。官方这几个请求不走请求框架，用 AuthService 自己的 axios 实例
// （authority-snapshot.js 的 get http：axios.create，keepAlive、proxy:false），头只有 buildHeaders 的
// Accept: application/json、Authorization、Content-Type: application/json、X-User-Id（没有 uid 时是
// 空串）、企业号的 X-Enterprise-Id 与同值的 X-Tenant-Id、X-Domain，加上拦截器补的 X-Request-ID 和
// axios 自带的 User-Agent、Accept-Encoding；没有请求框架的 X-Requested-With、X-Product、
// X-Department-Info。积分查询另带 Accept-Language、签到另带 X-Device-Token，由调用方加。
//
// 5.8.3 起就是这样（2026-10-31 读 5.8.3、5.9.4 安装包核对，同类项目 misakano7545/workbuddy2api-panel
// 的真机抓包里这几个请求的 UA 也是 axios/1.16.1）；1.8.0 及以前网关按请求框架发（BillingHeaders）。
// config user_agent 显式覆盖照样生效；client_name="SaaS"（旧行为）时同以前不带 UA。
func (c *Client) authServiceHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", acceptEncodingAxios)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", session.NewMessageID())
	switch {
	case c != nil && c.UserAgent != "":
		req.Header.Set("User-Agent", c.UserAgent)
	case c.attributionClientName() != "SaaS":
		req.Header.Set("User-Agent", axiosUserAgent)
	}
	if at := a.AccessTokenValue(); at != "" {
		req.Header.Set("Authorization", "Bearer "+at)
	}
	req.Header.Set("X-User-Id", a.UID)
	if a.IsEnterprise() {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	if d := chatDomain(a); d != "" {
		req.Header.Set("X-Domain", d)
	}
}

// RefreshHeaders 刷新令牌的请求头（X-Refresh-Token 只允许出现在这里）。官方后台服务的 refreshSession
// （内置 CLI 里也有同一份）：请求框架的头 + X-Domain（登录域，没有就用产品入口域）+ X-Refresh-Token +
// X-Auth-Refresh-Source: plugin，UA 见 refreshUserAgent。官方另带的链路头（X-Trace-ID、b3、X-B3-*）由
// RefreshToken 按这次刷新的根 span 补上（trace.go）。
func (c *Client) RefreshHeaders(req *http.Request, a *auth.Auth) {
	c.frameworkHeaders(req, a, c.refreshUserAgent(a))
	d := chatDomain(a)
	if d == "" {
		d = "www.workbuddy.cn" // 国内版产品入口 https://www.workbuddy.cn 的 authority
	}
	req.Header.Set("X-Domain", d)
	req.Header.Set("X-Refresh-Token", a.RefreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
}
