// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// desktop.go 桌面客户端行为指纹上报（事件链业务字段按 WorkBuddy Desktop 5.8.3 安装包核对，
// 公共字段与请求头照 5.8.3 安装包，2026-10-28 对过 5.9.3、10-31 对过国际版 5.9.4 没变，见
// eventFingerprint）。
//
// 来源：2026-09-12 Sunny 抓包实测（data/desktop-task-protocol.md）。桌面端点亮
// 「需电脑端」类任务的关键不是独立端点，而是同一 POST /v2/report 通道上
// **不同的客户端指纹**：
//
//	POST https://copilot.tencent.com/v2/report        ← chatBase（CLI 上报走 billingBase）
//	User-Agent: WorkBuddy/5.5.6 WorkBuddy/5.5.6 CLI/2.137.1
//	X-Domain: copilot.tencent.com, X-Product: SaaS, X-User-Id: <uid>
//	Body: [ {...event...} ]                           ← 数组
//
// 1.1.2 起国内版 chatBase 默认改为官方主入口 www.workbuddy.cn；X-Domain 取登录域
// （chatDomain，host 形式，不带 https://），不再跟着请求域走，
// 专家列表与空上报 10-20 生产实测两个域名结果一致。
//
// 每个事件除业务字段外必带桌面指纹（ideName/ideType=WorkBuddy、
// extName=workbuddy-desktop 等）。实测点亮记录：
//   - RichMeow_Chat（桌面端对话1次）：agent_task_created + 成功的
//     chat_message_response(isSuccessful=true) → 1/1 + UR Buddy。
//   - Hp_Appearance（主题任务）：独立 API
//     POST /v2/user-asset/appearance/set {kind:"theme",resource_key} → SetAppearanceTheme。
//
// 注意：service 端对事件链有一定真实性校验倾向（RichMeow 需要消息成功回执），
// 本模块按实测事件形状发送，不保证所有任务都能 API 侧点亮——autotask 侧
// 仍按 attempt 语义处理结果。
package upstream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
)

const (
	desktopReportPath          = "/v2/report"
	desktopAppearanceSet       = "/v2/user-asset/appearance/set"
	desktopAppearanceResources = "/v2/operation-platform/appearance/resources"
)

// desktopUA 任务中心这几条桌面端请求的 UA。请求形状照 5.5.6 桌面端实抓（当时 UA 为
// `WorkBuddy/5.5.6 WorkBuddy/5.5.6 CLI/2.137.1`），版本跟随网关其余请求（defaultDaemonUA），
// 免得同一个号出现两个客户端版本；X-IDE-Version 与事件里的 ideVersion 同样跟随。
// 不受 user_agent 覆盖影响（同以前）。
func (c *Client) desktopUA(a *auth.Auth) string { return c.defaultDaemonUA(a) }

// 两域桌面端（macOS arm64）安装包 cli/product.json 的 commit 与 date（毫秒）：国内版 5.9.3、国际版
// 5.9.4。上报事件按账号所在的域取：任务中心只对国内版个人号开放，每日活跃上报两域都发。配了
// client_version 时版本号跟着变，这两项不变。
const (
	desktopCommitCN          = "d46924219e79332f49c37ccebf27292f87001b19"
	desktopReleaseDateCN     = int64(1793093023713) // 2026-10-27T09:23:43.713Z
	desktopCommitGlobal      = "fa0a0901906addaa1a4259f6df347388a5563ded"
	desktopReleaseDateGlobal = int64(1793290653510) // 2026-10-29T16:17:33.510Z
)

// desktopBuild 账号所在域的桌面端 commit 与发布时间。
func desktopBuild(a *auth.Auth) (commit string, releaseDate int64) {
	if a.IsGlobal() {
		return desktopCommitGlobal, desktopReleaseDateGlobal
	}
	return desktopCommitCN, desktopReleaseDateCN
}

// desktopBase 桌面端 /v2/report 与 user-asset 走 chatBase（国内版 www.workbuddy.cn）。
func (c *Client) desktopBase(a *auth.Auth) string { return c.chatBase(a) }

// 上报事件里的设备信息按 macOS 版官方客户端（对照的两个安装包都是 darwin-arm64；用户 10-20
// 定按 macOS 对齐）。官方由事件处理器从本机取（os.platform()、arch()、release()、cpus()、
// totalmem()），网关挑一台常见配置：Apple M2、8 核、16 GB、macOS 15.6（Darwin 24.6.0）。
// macOS 版官方拿不到 qimei36（见 docs/official-client.md#登录），事件里不带。
const (
	deviceOS        = "darwin"
	deviceArch      = "arm64"
	deviceOSRelease = "24.6.0"
	deviceCPUModel  = "Apple M2"
	deviceCPUCores  = 8
	deviceMemoryGB  = 16
)

// eventReportDelay 官方上报服务攒 2 秒发一批（FIRE_DELAY），发送时按 now - timestamp 算
// reportDelay；网关立即发，把 timestamp 往前推同样的时长。
const eventReportDelay = 2000

// deviceDigest 由 uid 与用途稳定派生的摘要：同一账号每次相同，模拟固定设备。
func deviceDigest(a *auth.Auth, salt string) []byte {
	sum := sha256.Sum256([]byte(salt + ":" + a.UID))
	return sum[:16]
}

// formatUUID 按 8-4-4-4-12 排 16 字节。
func formatUUID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// desktopMachineID 桌面端的 machineId：官方 macOS 版取 ioreg 的 IOPlatformUUID（machineIdSync），
// 是大写的 UUID。
func desktopMachineID(a *auth.Auth) string {
	return strings.ToUpper(formatUUID(deviceDigest(a, "machine")))
}

// cliSessionID 内置 CLI 事件的 sessionId：官方是 CLI 进程启动时随机生成的 v4 UUID，网关按账号
// 固定一个（相当于 CLI 一直开着）。后台服务的事件没有 sessionId（官方取空串）。
func cliSessionID(a *auth.Auth) string {
	b := deviceDigest(a, "cli-session")
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return formatUUID(b)
}

// webVisitorID 官网网页事件的 machineId：网页用 FingerprintJS 的 visitorId（32 位小写十六进制）。
func webVisitorID(a *auth.Auth) string {
	return hex.EncodeToString(deviceDigest(a, "webmachine"))
}

// DesktopEvent 桌面端事件：业务字段任意（map），公共指纹由 ReportDesktopEvent 注入。
type DesktopEvent map[string]any

// rendererEvent 渲染层 reportEvent 发的事件（5.8.3 useAgentTelemetry → AdapterTelemetryService →
// WorkbuddyAdapter）要补的两样：上报服务把 mode 统一盖成本地桌面端的 LOCAL（建任务、推荐类等几个
// 事件另有改写，调用方自己设），适配器在 buddyId、buddyName 都没有时补两个空串
// （injectBuddyTelemetryContext）。走 wb.metrics（首轮回复后的建任务、做同款发送、专家使用、
// 设计画布）、界面自动埋点（data-track-id）和内置 CLI 的事件都不经过这两层，不用它。
func rendererEvent(ev DesktopEvent) DesktopEvent {
	ev["mode"] = "LOCAL"
	_, hasID := ev["buddyId"]
	_, hasName := ev["buddyName"]
	if !hasID && !hasName {
		ev["buddyId"], ev["buddyName"] = "", ""
	}
	return ev
}

// eventFingerprint 官方上报服务给每个事件补的公共字段（5.8.3 后台服务与内置 CLI 的事件处理器，
// 两边相同；国内版 5.9.3、国际版 5.9.4 的一样）：
//   - ProductInfo：product、releaseDate（product.json 的 date 转毫秒）、commit；
//   - Userinfo：userId、username 与 userNickname（官方都取昵称）、企业号的 enterpriseId 与
//     enterprise（部门全名）；
//   - Timezone；OS：os、arch、osVersion、cpuModel、cpuCores、memorySize；
//   - ExtensionInfo：extName、extVersion；HostIDEInfo：ideName、ideType、ideVersion、machineId、
//     sessionId。
//
// 官方有、网关没有值的不带：downloadChannel（安装包没配）、qimei36（macOS 版拿不到）、
// clientIp（官方只有国际版查本机的公网 IP；网关给国际版号发的只有每日活跃上报，没跟）、
// buddyId / buddyName、expKeys，以及 5.9.3 新加的 oneId（账号绑了 BuddyOne 才有）。
func (c *Client) eventFingerprint(a *auth.Auth, sessionID string) map[string]any {
	ver := c.clientVersion(a)
	commit, released := desktopBuild(a)
	fp := map[string]any{
		"timestamp":    time.Now().UnixMilli() - eventReportDelay,
		"reportDelay":  eventReportDelay,
		"timezone":     "Asia/Shanghai",
		"userId":       a.UID,
		"username":     a.Nickname,
		"userNickname": a.Nickname,
		"product":      "SaaS",
		"releaseDate":  released,
		"commit":       commit,
		"ideName":      "WorkBuddy",
		"ideType":      "WorkBuddy",
		"ideVersion":   ver,
		"machineId":    desktopMachineID(a),
		"sessionId":    sessionID,
		"extName":      "workbuddy-desktop",
		"extVersion":   ver,
		"os":           deviceOS,
		"arch":         deviceArch,
		"osVersion":    deviceOSRelease,
		"cpuModel":     deviceCPUModel,
		"cpuCores":     deviceCPUCores,
		"memorySize":   deviceMemoryGB,
	}
	if a.IsEnterprise() {
		fp["enterpriseId"] = a.EnterpriseID
	}
	if d := a.DepartmentValue(); d != "" {
		fp["enterprise"] = d
	}
	return fp
}

// desktopFingerprint 后台服务事件的公共字段（sessionId 为空，见 cliSessionID）。注入每个事件，
// 业务字段优先。
func (c *Client) desktopFingerprint(a *auth.Auth) map[string]any { return c.eventFingerprint(a, "") }

// ReportDesktopEvent 以桌面客户端指纹向 chatBase 的 /v2/report 批量上报事件。
// events 为业务载荷（eventCode 等字段由调用方给出）；公共指纹自动注入，
// 业务字段优先（可用于覆盖 qimei36/machineId 等设备标识做真实设备对齐）。
func (c *Client) ReportDesktopEvent(a *auth.Auth, events ...DesktopEvent) error {
	if len(events) == 0 {
		return fmt.Errorf("desktop report: no events")
	}
	fp := c.desktopFingerprint(a)
	arr := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		m := map[string]any{}
		for k, v := range fp {
			m[k] = v
		}
		for k, v := range ev {
			m[k] = v
		}
		arr = append(arr, m)
	}
	raw, err := json.Marshal(arr)
	if err != nil {
		return err
	}
	req, err := newAccountRequest(context.Background(), a, http.MethodPost, c.desktopBase(a)+desktopReportPath, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	// 官方上报服务 StandardEventService.send 显式把 Content-Type 设成带 charset 的写法，
	// 其余头由官方请求框架给（frameworkHeaders）。
	c.frameworkHeaders(req, a, c.desktopUA(a))
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	_, err = c.doJSON(req)
	return err
}

// DesktopChatSequence 构造一次「桌面端成功对话」的完整事件链
// （agent_task_created → chat_message_send → chat_request_send →
// chat_message_response(isSuccessful) → chat_message_status → chat_request_response）。
// 实测该链点亮 RichMeow_Chat。conversationID/requestID/messageID 由调用方生成，messageID 是用户
// 消息的 id（建任务事件用）；三个 chat_message_* 事件带的是模型消息 id，同官方每次调模型新生成
// （withModelMessageCorrelation：uuidv7() 去掉连字符）。
func DesktopChatSequence(conversationID, requestID, messageID, modelID, modelName string) []DesktopEvent {
	uuid := func() string { return requestID }
	modelMessageID := session.NewUUIDv7Hex()
	// 官方 presentAt/firstTokenAt 取运行时时刻；网关一次构造批量发，统一取构造时刻。
	now := time.Now().UnixMilli()
	mk := func(code string, extra map[string]any) DesktopEvent {
		ev := DesktopEvent{"eventCode": code}
		for k, v := range extra {
			ev[k] = v
		}
		return ev
	}
	return []DesktopEvent{
		mk("agent_task_created", map[string]any{
			"source": "LOCAL", "name": "working", "task_target": "local", "mode": "craft",
			"requestModelId": modelID, "requestModelName": modelName,
			"is_worktree_task": false, "is_from_code": false,
			"has_repo": false, "repo_type": "none", "workspace_type": "empty",
			"has_connector": false, "connector_types": []any{},
			"has_mention": false, "mention_types": []any{},
			"has_template": false, "action": "", "template_name": "",
			"has_expert": false, "expert_id": "", "expert_name": "", "expert_industry_id": "",
			"has_skill": false, "skill_names": []any{},
			"conversationId": conversationID, "requestId": requestID, "messageId": messageID,
		}),
		mk("chat_message_send", map[string]any{
			"conversationId": conversationID, "requestId": requestID,
			"messageId": modelMessageID, "historyCount": 0,
			"isContextTruncated": false, "currentStepCount": 1, "totalStepCount": 1, // 步数取典型值
			"requestModelId": modelID, "requestModelName": modelName,
			"presentAt": now, // 网关取的典型值（事件发生时刻）
			"traceId":   uuid(), "rootRequestId": requestID,
			"parentConversationId": conversationID,
			"agentName":            "cli", "agentType": "main",
		}),
		mk("chat_request_send", map[string]any{
			"mode": "craft", "conversationId": conversationID, "requestId": requestID,
			"requestModelId": modelID, "requestModelName": modelName,
			"inputLength": 24, "isPlan": false, "isAutoExecuteTerminal": false,
			"isAutoModify": false, "codebaseEnable": false, "maxToken": 0,
			"maxSteps": 500, "temperature": 0, "maxRetries": 0,
			"mentionContexts": []any{}, "knowledgeId": []any{}, "knowledgeName": []any{},
			"codebaseId": "", "mentionContextCount": 0, "command": "",
			"recommendId": "", "skillId": "", "skillCount": 0, "totalCount": 0,
			"fileUri": "", "presentAt": now, // 空工作区；时间取构造时刻
			"traceId": uuid(), "rootRequestId": requestID,
			"parentConversationId": conversationID,
			"agentName":            "cli", "agentType": "main",
			// codebuddy.* 是 CLI baggage 处理器补的公共字段，保留。
			"codebuddy.session_id":              conversationID,
			"codebuddy.conversation_request_id": requestID,
		}),
		mk("chat_message_response", map[string]any{
			"conversationId": conversationID, "requestId": requestID,
			"messageId": modelMessageID, "totalStepCount": 1, // 步数取典型值
			"requestModelId": modelID, "requestModelName": modelName,
			"responseModelId": modelID,
			"inputToken":      120, "outputToken": 80, "totalToken": 200,
			"cachedTokens": 0, "cachedWriteTokens": 0, "cachedMissTokens": 0,
			"isSuccessful": true, "messageErrorCode": "", "finishReason": "stop",
			"firstTokenAt": now, "presentAt": now, "traceId": uuid(),
			"rootRequestId": requestID, "parentConversationId": conversationID,
			"agentName": "cli", "agentType": "main",
			// codebuddy.* 是 CLI baggage 处理器补的公共字段，保留。
			"codebuddy.session_id":              conversationID,
			"codebuddy.conversation_request_id": requestID,
		}),
		mk("chat_message_status", map[string]any{
			"conversationId": conversationID, "requestId": requestID,
			"messageId": modelMessageID, "messageErrorCode": "0",
			"requestModelId": modelID, "requestModelName": modelName,
			"traceId": uuid(), "rootRequestId": requestID,
			"parentConversationId": conversationID,
			"agentName":            "cli", "agentType": "main",
		}),
		mk("chat_request_response", map[string]any{
			"mode": "craft", "conversationId": conversationID, "requestId": requestID,
			"requestModelId": modelID, "requestModelName": modelName,
			"toolCallCount": 0,
			"inputToken":    120, "outputToken": 80, "totalToken": 200,
			"cachedTokens": 0, "cachedWriteTokens": 0, "cachedMissTokens": 0,
			"isSuccessful": true, "messageErrorCode": "", "finishReason": "stop",
			"presentAt": now, "traceId": requestID,
			"rootRequestId": requestID, "parentConversationId": conversationID,
			"agentName": "cli", "agentType": "main",
		}),
	}
}

// SetAppearanceTheme 应用外观主题（实测：POST <chatBase>/v2/user-asset/appearance/set，
// 和品主题 resource_key 为 "theme-tkmw7j"，浅色 "light"、深色 "dark"）。纯 API set 不计
// Hp_Appearance 分（需客户端切主题后真实活跃），保留供调色/还原与后续验证用。
func (c *Client) SetAppearanceTheme(a *auth.Auth, resourceKey string) error {
	body := map[string]string{"kind": "theme", "resource_key": resourceKey}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := newAccountRequest(context.Background(), a, http.MethodPost, c.desktopBase(a)+desktopAppearanceSet, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	// 官方桌面端 axios 对对象请求体只发 application/json，框架的默认值正好一致。
	c.frameworkHeaders(req, a, c.desktopUA(a))
	_, err = c.doJSON(req)
	return err
}

// AppearanceTheme 主题列表里的一个主题（只取皮肤事件用到的字段）。
type AppearanceTheme struct {
	ID       string
	VipLevel string // 已按官方 mapThemeDTO 换成客户端的等级名
	Series   string
}

// appearanceVipLevels 官方 mapThemeDTO 把接口的 vip_level 换成客户端等级名的对照表（5.8.3 后台服务 ld）。
var appearanceVipLevels = map[string]string{
	"free": "free", "standard": "pro", "pro": "pro", "advanced": "advanced",
	"ultimate": "ultimate", "flagship": "ultimate", "exclusive": "ultimate",
}

// AppearanceThemes 拉主题列表，照官方 DesktopAppearanceRepo.listCatalog（打开外观设置页时调）：
// POST <chatBase>/v2/operation-platform/appearance/resources
// {platform:"client",kind:"theme",version:<桌面端版本>,lang:"zh"|"en"}，取 data.resources 里
// kind 为 theme 的。10-20 生产实测 theme-tkmw7j 是 vip_level=free、series=craft。
func (c *Client) AppearanceThemes(a *auth.Auth) ([]AppearanceTheme, error) {
	raw, err := json.Marshal(map[string]string{
		"platform": "client", "kind": "theme", "version": c.clientVersion(a),
		// 官方取界面语言（zh 开头为 zh，否则 en），网关按账号域取，同积分包查询。
		"lang": billingAcceptLanguage(a),
	})
	if err != nil {
		return nil, err
	}
	req, err := newAccountRequest(context.Background(), a, http.MethodPost, c.desktopBase(a)+desktopAppearanceResources, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c.frameworkHeaders(req, a, c.desktopUA(a))
	data, err := c.doJSON(req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Resources []struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			VipLevel string `json:"vip_level"`
			Series   string `json:"series"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("appearance resources parse: %w", err)
	}
	var out []AppearanceTheme
	for _, r := range resp.Resources {
		if r.Kind == "theme" {
			out = append(out, AppearanceTheme{ID: r.ID, VipLevel: appearanceVipLevels[r.VipLevel], Series: r.Series})
		}
	}
	return out, nil
}

// DesktopBuddyAppSequence 构造「进入 Buddy 应用」五连事件（实测两账号纯 API 点亮
// Buddy_App 与 Buddy_App_QQ）：discover → show → enter_click → auth_confirm →
// bindaccount_skip。buddyID 固定用企鹅教师助手 cb_y5Dy46tPQGGWtueMxXbe
// （Buddy_App_QQ 的判据应用），同时满足 Buddy_App「进入任一应用」。
//
// 字段照 5.8.3 的 reportBuddyApp*：打开发现面板不带字段，曝光、进入带应用的 elementId、
// elementName（buddyId、buddyName 由适配器补空串），授权确认、跳过绑定这类授权动作才把
// buddyId、buddyName 设成这个应用（reportBuddyAppConsentAction）。
func DesktopBuddyAppSequence(buddyID, buddyName string) []DesktopEvent {
	mk := func(code string, extra map[string]any) DesktopEvent {
		ev := DesktopEvent{"eventCode": code}
		for k, v := range extra {
			ev[k] = v
		}
		return rendererEvent(ev)
	}
	consent := map[string]any{"elementId": buddyID, "elementName": buddyName, "buddyId": buddyID, "buddyName": buddyName}
	return []DesktopEvent{
		mk("buddyapp_discover_click", nil),
		mk("buddyapp_show", map[string]any{"elementId": buddyID, "elementName": buddyName, "position": 2}),
		mk("buddyapp_enter_click", map[string]any{"elementId": buddyID, "elementName": buddyName, "position": 2, "isFirstPage": true}),
		mk("buddyapp_auth_confirm_click", consent),
		mk("buddyapp_bindaccount_skip_click", consent),
	}
}

// DesktopAutomationCreateEvent 构造「定时任务创建成功」事件（实测两账号纯 API
// 点亮 automation_1）。name 为任务名，可与真实创建语义对齐。
func DesktopAutomationCreateEvent(name string) DesktopEvent {
	return rendererEvent(DesktopEvent{
		"eventCode": "automated_task_create_suc", "name": name,
		"source": "manually", "modelId": "fast-model", "modelIsThinking": true,
		"connectorCount": 0, "skills": "", "skillCount": 0,
		"scheduleType": "once",
		"pushToWeChat": false, "pushToWecomBot": false, // 未开推送时官方表单字段为 false
	})
}

// DesktopAppearanceApplyEvent 外观设置页离开时的「皮肤生效」事件（5.8.3 buildSkinApplyPayload）：
// vipLevel、series 取主题列表里这个主题的值，type 是账号的版本档（AccountEdition）。
func DesktopAppearanceApplyEvent(theme AppearanceTheme, edition string) DesktopEvent {
	return rendererEvent(DesktopEvent{
		"eventCode": "appearance_skin_apply", "action": "apply", "source": "settings_close",
		"id": theme.ID, "vipLevel": theme.VipLevel, "series": theme.Series, "type": edition,
	})
}

// ReportWebEvent 以 Web 端指纹向 www.workbuddy.cn/v2/report 上报单事件。
// 与桌面指纹（copilot 域）不同：web 域事件是浏览器形状（os/machineId/userAgent），
// 用于 Library_read 等页面行为类任务（实测 library_doc_intro_click 4 秒点亮）。
//
// 形状照官网网页：上报走 navigator.sendBeacon(url, JSON 字符串)，浏览器给它带
// Content-Type: text/plain;charset=UTF-8、Accept: */*、Origin、Referer 与浏览器 UA；
// 网页靠 Cookie 认人，这里用同一个账号的网页登录态（见 websession.go），不带 Authorization，
// 其余网页不发的头（X-User-Id、X-Client-Platform、X-Requested-With、X-Product）都不带。
func (c *Client) ReportWebEvent(a *auth.Auth, eventCode, pageURL, elementID, elementName string) error {
	ua := c.webUserAgent()
	osName, osVersion := webOSFromUA(ua)
	now := time.Now().UnixMilli()
	// 官网前端的上报（W4 + report）：公共参数 os / osVersion 由 UA 解析，sessionId 是
	// 「毫秒时间戳-9 位随机 36 进制」，machineId 是浏览器指纹，product 固定 UserCenter；
	// 元素点击另带 type、elementId、elementName。username 网页取账号资料，网关没有，用昵称（未确认）。
	ev := map[string]any{
		"type": eventCode, "elementId": elementID, "elementName": elementName,
		"eventCode": eventCode, "os": osName, "osVersion": osVersion, "userAgent": ua,
		"sessionId": webPageSessionID(now), "machineId": webVisitorID(a),
		"userId": a.UID, "userNickname": a.Nickname, "username": a.Nickname,
		"enterpriseId": a.EnterpriseID, "pageURL": pageURL, "product": "UserCenter",
		"timestamp": now, "reportDelay": 0,
	}
	raw, err := json.Marshal([]map[string]any{ev})
	if err != nil {
		return err
	}
	build := func() (*http.Request, error) {
		req, err := newAccountRequest(context.Background(), a, http.MethodPost, c.webBase(a)+"/v2/report", bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Origin", c.webBase(a))
		req.Header.Set("Referer", pageURL)
		req.Header.Set("User-Agent", ua)
		c.webClientHints(req, a)
		// sendBeacon 的 Sec-Fetch-*：同源 no-cors 请求。
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Sec-Fetch-Mode", "no-cors")
		req.Header.Set("Sec-Fetch-Dest", "empty")
		return req, nil
	}
	_, err = c.doWebJSON(a, pageURL, build)
	return err
}

// ---------------------------------------------------------------------------
// 2026-09-12 第五轮：客户端 asar 逆向 + Sunny 真实样本驱动的新判据（三账号实测点亮）。
// 来源：WorkBuddy.exe 5.5.6 app.asar 渲染层事件枚举/载荷 + 抓包真实专家召唤样本
// （data/desktop-task-protocol.md §7.3）。
// ---------------------------------------------------------------------------

// DesktopTemplateUseSequence 构造「使用模板创建任务」事件组（实测 template_5 计数；三账号实测
// 5 组不同模板一次上报 → 5/5 点亮）。照官方 5.8.3 首页点模板再发送的顺序：
//   - 点模板按钮：界面自动埋点的 web_element_click（agent_template_clicked，source 是模板名，
//     pageURL 取首页的 hash "#/"，未确认）；同时 reportTemplateUsed 报 template_used，
//     template_id 是模板的数字 id，没选模板下的示例提示词就不带 prompt_index；
//   - 首轮回复后：建任务事件带 has_template、action（模板 id 的字符串）、template_name，
//     再报 agent_task_created_with_template（首页 taskCreatedWithTemplate：mode 是 LOCAL，
//     isCustomModel 固定 true，id 同 action）。
//
// 只发事件不真发对话，用户自己写的输入长度取对话链的典型值。
func DesktopTemplateUseSequence(conversationID, requestID string, sc HomeScene) []DesktopEvent {
	id := strconv.Itoa(sc.ID)
	events := []DesktopEvent{
		{"eventCode": "web_element_click", "elementId": "agent_template_clicked", "elementName": "点击模板",
			"source": sc.Name, "pageURL": "#/"},
		rendererEvent(DesktopEvent{"eventCode": "template_used", "task_mode": "working",
			"template_name": sc.Name, "template_id": sc.ID}),
	}
	chat := DesktopChatSequence(conversationID, requestID, NewRequestID(), "fast-model", "fast-model")
	created := chat[0]
	created["has_template"], created["action"], created["template_name"] = true, id, sc.Name
	events = append(events, chat...)
	return append(events, DesktopEvent{
		"eventCode": "agent_task_created_with_template", "mode": "LOCAL",
		"isCustomModel": true, "id": id, "name": sc.Name, "requestId": requestID,
	})
}

// DesktopPlaybookPromptSequence 构造「灵感案例做同款」事件组（实测 playbook_prompt 计数），照官方
// 5.8.3 发现页从分类列表进详情再做同款的顺序：分类筛选、案例卡片、做同款按钮三个 web_element_click
// （发现页的 reportEvent，不带 pageURL）→ playbook_cta_click → 回到首页发送时的
// query_recommend_prompt_send（首页把案例当一条推荐报，buildPlaybookQueryRecommendSendEvent）→
// 对话链 → playbook_prompt_send（首轮回复后经 wb.metrics 上报，带 conversationId/requestId JOIN
// chat 链）。来源是分类列表（scene），ext1 发现页为 discover。案例字段取 PickPlaybookCase 的真实值；
// 案例不带技能、专家，skills 两项为空串，专家两项官方为 undefined、不出现。
func DesktopPlaybookPromptSequence(conversationID, requestID string, pc PlaybookCase) []DesktopEvent {
	const source = "scene"
	events := []DesktopEvent{
		rendererEvent(DesktopEvent{"eventCode": "web_element_click", "pageName": "playbook_list",
			"elementId": "playbook_sceneFilter", "elementName": pc.CategoryID, "source": source}),
		rendererEvent(DesktopEvent{"eventCode": "web_element_click", "pageName": "playbook_list",
			"elementId": "playbookCard_" + pc.ID, "elementName": pc.Title, "source": source}),
		rendererEvent(DesktopEvent{"eventCode": "web_element_click", "pageName": "playbook_detail",
			"elementId": "playbook_ctaClick", "elementName": pc.Title, "source": source}),
	}
	payload := func(code string) DesktopEvent {
		return DesktopEvent{
			"eventCode": code, "id": pc.ID, "name": pc.Title, "type": pc.Type,
			"categoryId": pc.CategoryID, "categoryName": pc.CategoryName,
			"skills": "", "skillNames": "", "source": source,
		}
	}
	cta := rendererEvent(payload("playbook_cta_click"))
	cta["position"] = pc.Position
	recommendType := pc.CategoryName
	if recommendType == "" {
		recommendType = pc.Type
	}
	// 推荐类事件的 mode 被上报服务改成首页模式（mapQueryRecommendPayload：sceneMode），
	// 案例没指定 task_mode 时首页停在默认的工作模式，即 work。
	recommend := rendererEvent(DesktopEvent{
		"eventCode": "query_recommend_prompt_send", "batchId": "", "recommendId": pc.ID, "queryId": pc.ID,
		"type": recommendType, "query": pc.Title, "position": 0,
	})
	recommend["mode"] = "work"
	events = append(events, cta, recommend)
	chat := DesktopChatSequence(conversationID, requestID, NewRequestID(), "fast-model", "fast-model")
	for _, ev := range chat {
		if ev["eventCode"] == "chat_request_send" {
			ev["inputLength"] = pc.PromptLength // 发出去的就是案例提示词
		}
	}
	events = append(events, chat...)
	send := payload("playbook_prompt_send")
	for k, v := range map[string]any{
		"promptLength": pc.PromptLength, "isOfficial": 1,
		"conversationId": conversationID, "requestId": requestID,
		"ext1": "discover", "queryId": pc.ID,
	} {
		send[k] = v
	}
	return append(events, send)
}

// DesktopDesignCanvasSequence 构造「设计创意画布」事件组（实测 create_canvas 计数）：
// wbx_design_canvas_task_create + wbx_design_canvas_open（Ardot create_design 工具
// 完成时客户端经 metrics 通道上报，同一 /v2/report 端点）。三账号实测 1/1 点亮。
func DesktopDesignCanvasSequence(conversationID, requestID string) []DesktopEvent {
	events := DesktopChatSequence(conversationID, requestID, NewRequestID(), "fast-model", "fast-model")
	return append(events,
		DesktopEvent{
			"eventCode": "wbx_design_canvas_task_create", "conversationId": conversationID,
			"requestId": requestID, "source": "summon_keyword", "cost": 12000, "isSuccessful": true,
			// 官方 tool result 缺失时这些字段的默认值。
			"isCustomModel": false, "name": "", "inputLength": 0,
		},
		DesktopEvent{
			"eventCode": "wbx_design_canvas_open", "conversationId": conversationID,
			"requestId": requestID, "id": "ardot-file-" + requestID[len(requestID)-8:],
			"source": "summon_keyword", "type": "page", "cost": 13000, "isSuccessful": true,
		},
	)
}

// MarketExpert 专家市场的单个专家（/portal/operation-platform/market/expert/list 响应子集）。
type MarketExpert struct {
	ExpertID      string `json:"expert_id"`
	ExpertType    string `json:"expert_type"`
	DisplayNameZH string `json:"display_name_zh"`
	ProfessionZH  string `json:"profession_zh"`
	Version       string `json:"version"`
	Categories    []any  `json:"categories"`
	// 云端 / 付费判定字段（决定专家对话带不带 X-Moderation-Type，见 Protected）：
	// is_cloud===1 为云端专家；付费看 pay_detail 的 pay_mode，缺省回落 need_pay===1。
	IsCloud   int              `json:"is_cloud"`
	NeedPay   int              `json:"need_pay"`
	PayDetail *expertPayDetail `json:"pay_detail"`
}

// expertPayDetail 专家付费信息（pay_detail），官方 resolveExpertPayMode 读其 pay_mode / payMode。
type expertPayDetail struct {
	PayMode      *float64 `json:"pay_mode"`
	PayModeCamel *float64 `json:"payMode"`
}

// Protected 该专家的对话要不要带 X-Moderation-Type: ["protected"]——对齐官方后台的
// shouldProtectSession：云端专家（is_cloud===1）或付费专家（pay_detail 的 pay_mode，回落
// need_pay===1）为真。官方还判「启用的技能里有云端技能」，网关的专家对话不加载技能，这条恒假，
// 故只看专家本身的云端 / 付费属性。依据见 docs/official-client.md「几类请求」里的对话。
func (e MarketExpert) Protected() bool {
	if e.IsCloud == 1 {
		return true
	}
	pm, ok := e.payMode()
	return ok && pm > 0
}

// payMode 对齐官方 resolveExpertPayModeWithFallback：pay_detail 的 pay_mode / payMode（取到即用），
// 缺省回落 need_pay===1 → 1。
func (e MarketExpert) payMode() (float64, bool) {
	if e.PayDetail != nil {
		if e.PayDetail.PayMode != nil {
			return *e.PayDetail.PayMode, true
		}
		if e.PayDetail.PayModeCamel != nil {
			return *e.PayDetail.PayModeCamel, true
		}
	}
	if e.NeedPay == 1 {
		return 1, true
	}
	return 0, false
}

// MarketExpertList 拉取专家市场真实专家列表（expertType: "agent" 单专家 / "team" 专家团）。
// expert_actual_use 的判据校验要求 id 是平台上真实存在的专家（编造 id 不计数）。
func (c *Client) MarketExpertList(a *auth.Auth, expertType string) ([]MarketExpert, error) {
	return c.MarketExpertListPage(a, expertType, 1)
}

// MarketExpertPageSize 网关拉专家市场列表时每页的专家数。
const MarketExpertPageSize = 20

// MarketExpertListPage 拉取专家市场列表的第 page 页，除了 page 都同第一页；官方专家页往下翻时
// 发什么请求没有抓包（未确认）。
func (c *Client) MarketExpertListPage(a *auth.Auth, expertType string, page int) ([]MarketExpert, error) {
	return c.marketExpertList(a, expertType, "", page)
}

// MarketExpertSearch 在专家市场按关键词搜，取第一页。依据桌面端 5.9.3 安装包：专家页搜索框发的
// 还是列表请求，请求体多一个 keyword，请求头多一个 X-Language（界面语言，没设过就是 zh-CN）。
// expertType 同列表：专家页按所在页签带 agent 或 team，对话框里选专家时的搜索不带。
func (c *Client) MarketExpertSearch(a *auth.Auth, expertType, keyword string) ([]MarketExpert, error) {
	return c.marketExpertList(a, expertType, keyword, 1)
}

func (c *Client) marketExpertList(a *auth.Auth, expertType, keyword string, page int) ([]MarketExpert, error) {
	body := map[string]any{"page": page, "page_size": MarketExpertPageSize, "sort_by": "reco_rank", "sort_order": "desc"}
	if expertType != "" {
		body["expert_type"] = expertType
	}
	if keyword != "" {
		body["keyword"] = keyword
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := newAccountRequest(context.Background(), a, http.MethodPost, c.chatBase(a)+"/portal/operation-platform/market/expert/list", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c.frameworkHeaders(req, a, c.desktopUA(a))
	if keyword != "" {
		// 5.9.3 的专家页拉列表每次都带，对话框里的搜索只在有关键词时带；5.8.3 的没核对，网关只在搜索时带。
		req.Header.Set("X-Language", "zh-CN")
	}
	// 官方先取 items，没有再取 experts（5.9.3 的 data.items ?? data.experts）。
	var out struct {
		Items   []MarketExpert `json:"items"`
		Experts []MarketExpert `json:"experts"`
	}
	data, err := c.doJSON(req)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("expert list parse: %w", err)
	}
	if out.Items != nil {
		return out.Items, nil
	}
	return out.Experts, nil
}

// DesktopExpertSearchEvent 专家页（单专家页签）搜索框的 expert_search 事件：桌面端 5.9.3 渲染层
// 在搜索词停了 500 毫秒、结果出来后报一次，query 是搜索词，count 是搜到几位专家，scope 固定
// center（专家中心），source 是 builtin（官方专家），type 是页签。
func DesktopExpertSearchEvent(query string, count int) DesktopEvent {
	return rendererEvent(DesktopEvent{
		"eventCode": "expert_search", "query": query, "count": count,
		"scope": "center", "source": "builtin", "type": "expert",
	})
}

// NewConversationID 任务中心和各类上报用的会话 id，同官方新会话（带连字符的 UUID v7）。
func NewConversationID() string { return session.NewUUIDv7() }

// NewRequestID 任务中心和各类上报用的轮次 id、消息 id，同官方（去掉连字符的 UUID v7）。
func NewRequestID() string { return session.NewUUIDv7Hex() }

// expertChatPrompt 专家对话发的用户消息。专家使用事件的 characterCount、对话链的 inputLength
// 都按它算。
const expertChatPrompt = "1+1等于几？直接回答。"

// jsLength JS 字符串的 length（UTF-16 码元数），官方事件里的文本长度都是它。
func jsLength(s string) int { return len(utf16.Encode([]rune(s))) }

// DesktopChatWithExpert 走桌面端内置 CLI 的普通对话请求（可带 X-Expert-Id），从 SSE 流
// 解析**服务端返回的 requestId**（data.id，如 cmb-xxxx / 32hex）并返回。
// expert_actual_use 等 JOIN 事件的 requestId 必须是该服务端 id——自造 UUID 不计数
// （客户端 resolveRealRequestId 同款语义，Sunny row 2113 实证）。
//
// 依据：官方专家对话就是一次普通的内置 CLI 对话，只多带 X-Expert-Id: <专家 id>；
// team 为 true（专家团，市场列表的 expert_type=team）时另带 X-Expert-Team-Task: true；
// protected 为真（云端 / 付费专家，见 MarketExpert.Protected）时另带 X-Moderation-Type: ["protected"]。
// 会话 id、轮次 id 同官方新会话（NewConversationID、NewRequestID）。
func (c *Client) DesktopChatWithExpert(a *auth.Auth, expertID string, team, protected bool) (conversationID, requestID string, err error) {
	conversationID = NewConversationID()
	body := map[string]any{
		"model": "fast-model",
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a helpful assistant. 当前处于中文环境，使用简体中文回答。"},
			map[string]any{"role": "user", "content": expertChatPrompt},
		},
		"agent":          "cli",
		"temperature":    1,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", "", err
	}
	rc, status, respBody, err := c.ChatStreamContext(context.Background(), a, raw, "", ChatMeta{
		ConversationID:        conversationID,
		ConversationRequestID: NewRequestID(),
		ExpertID:              expertID,
		ExpertTeamTask:        team,
		Protected:             protected,
	})
	if err != nil && status == 0 {
		// 传输错误原样返回。
		return "", "", err
	}
	if status != 200 {
		if rc != nil {
			b, _ := io.ReadAll(io.LimitReader(rc, 200))
			rc.Close()
			respBody = b
		}
		if len(respBody) > 200 {
			respBody = respBody[:200]
		}
		return "", "", fmt.Errorf("chat http %d: %s", status, respBody)
	}
	defer rc.Close()
	// 从 SSE 流抓第一个 data.id 作为服务端 requestId（读干流避免残留连接）。
	buf := make([]byte, 0, 1<<20)
	tmp := make([]byte, 8192)
	searchFrom := 0
	for {
		n, rerr := rc.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			// 从上次搜索位置继续，把已读到的每个 `"id":"` 都检查一遍：SSE 里它可能先
			// 出现在消息 id 等字段，同一块数据里后面才是服务端 requestId。只看第一个
			// 的话，整段响应一次读完时后面的 id 永远轮不到，误报"未找到 requestId"。
			for {
				i := bytes.Index(buf[searchFrom:], []byte(`"id":"`))
				if i < 0 {
					break
				}
				abs := searchFrom + i
				rest := buf[abs+6:]
				end := bytes.IndexByte(rest, '"')
				if end < 0 {
					break // 这个 id 还没读完整，等下一块
				}
				id := string(rest[:end])
				if idRegex.MatchString(id) {
					return conversationID, id, nil
				}
				searchFrom = abs + 1
			}
		}
		if rerr != nil || len(buf) > 1<<20 {
			break
		}
	}
	return "", "", fmt.Errorf("SSE 中未找到服务端 requestId")
}

// idRegex 服务端 requestId 形状（cmb- 前缀 32hex 或裸 32hex）。
var idRegex = regexp.MustCompile(`^(cmb-)?[0-9a-f]{32}$`)

// DesktopExpertSummonSequence 构造「召唤平台专家」事件组（expert_summon_click 等），
// 载荷对齐真实抓包样本（Sunny row 2644）。需配合 DesktopChatWithExpert +
// DesktopExpertActualUseEvent 完成一次完整「召唤+使用」。
func DesktopExpertSummonSequence(e MarketExpert) []DesktopEvent {
	cat, hasCat := expertCategory(e)
	// 元素点击的 data-track props：专家团为 "team"，否则分类缺省 "expert"。
	webType := "expert"
	if e.ExpertType == "team" {
		webType = "team"
	} else if hasCat {
		webType = cat
	}
	ver := e.Version
	if ver == "" {
		ver = "1.0.0"
	}
	return []DesktopEvent{
		{
			"eventCode": "web_element_click", "source": e.ExpertID, "type": webType, "version": ver,
			"elementId": "expert_summon_click", "elementName": "立即召唤",
			// 官方 derivePageURL：有 hash 取 hash（界面是 HashRouter），召唤按钮在专家中心 /experts。
			"pageURL": "#/experts",
		},
		rendererEvent(DesktopEvent{
			"eventCode": "expert_summon_click", "id": e.ExpertID, "name": e.DisplayNameZH,
			"expertTitle": e.ProfessionZH, "type": cat, "position": 0,
			"expertType": e.ExpertType, "version": ver,
		}),
		rendererEvent(DesktopEvent{
			"eventCode": "expert_summoned", "id": e.ExpertID, "name": e.DisplayNameZH,
			"expertTitle": e.ProfessionZH, "type": cat, "version": ver,
		}),
	}
}

// DesktopExpertChatSequence 和专家对话的对话链（DesktopChatSequence 加上专家）：官方会话里有
// 专家时，建任务事件带 has_expert 等四项（expert_industry_id 是召唤时的 type，见
// expertCategory），内置 CLI 的 chat_message_send、chat_request_send、chat_message_response、
// chat_request_response 带会话专家 expertId（getSessionExpertId），chat_message_status 不带。
// inputLength 是专家对话真实发的用户消息的长度。
func DesktopExpertChatSequence(e MarketExpert, conversationID, requestID, messageID string) []DesktopEvent {
	cat, _ := expertCategory(e)
	events := DesktopChatSequence(conversationID, requestID, messageID, "fast-model", "fast-model")
	for _, ev := range events {
		switch ev["eventCode"] {
		case "agent_task_created":
			ev["has_expert"], ev["expert_id"], ev["expert_name"], ev["expert_industry_id"] = true, e.ExpertID, e.DisplayNameZH, cat
		case "chat_request_send":
			ev["inputLength"] = jsLength(expertChatPrompt)
			ev["expertId"] = e.ExpertID
		case "chat_message_send", "chat_message_response", "chat_request_response":
			ev["expertId"] = e.ExpertID
		}
	}
	return events
}

// DesktopExpertActualUseEvent 构造「专家真实使用」事件（expert_5/Expert_team_use_3 计数）。
// requestID 必须是 DesktopChatWithExpert 返回的服务端 requestId，messageID 是对话链里用户消息的 id，
// summonedAt 是上报召唤链的时刻。mode 官方取会话 transport：本地对话为 LOCAL（5.8.3 resolveMode）。
func DesktopExpertActualUseEvent(e MarketExpert, conversationID, requestID, messageID string, summonedAt time.Time) DesktopEvent {
	ev := desktopExpertActualUse(e, conversationID, requestID, messageID, summonedAt)
	ev["mode"] = "LOCAL"
	return ev
}

// DesktopExpertActualUseLocal mode:"LOCAL" 变体（Expert_lighthouse 判据要求 LOCAL，
// 对齐真实样本 Sunny row 868：轻量云专家使用时 mode=LOCAL、type 为空、cost=0）。
func DesktopExpertActualUseLocal(e MarketExpert, conversationID, requestID, messageID string, summonedAt time.Time) DesktopEvent {
	ev := desktopExpertActualUse(e, conversationID, requestID, messageID, summonedAt)
	ev["mode"] = "LOCAL"
	return ev
}

// expertCategory 专家中心召唤时的 type：专家的第一个分类（官方 categoryId），没有时 "all"
// （5.8.3 的 categoryId ?? "all"）。召唤时它也记成会话专家的 industryId，expert_actual_use 的
// type 就是它。hasCat 为假表示用的是缺省值。
func expertCategory(e MarketExpert) (cat string, hasCat bool) {
	if len(e.Categories) > 0 {
		if s, ok := e.Categories[0].(string); ok && s != "" {
			return s, true
		}
	}
	return "all", false
}

// desktopExpertActualUse expert_actual_use 公共载荷。
// type 官方取会话专家的 industryId，从专家中心召唤时就是召唤的 type（见 expertCategory）。
// cost 官方取召唤到使用的毫秒数（不小于 0），characterCount 是发出的提示词文本长度（大于 0
// 才带），网关按召唤时刻和 expertChatPrompt 算。
func desktopExpertActualUse(e MarketExpert, conversationID, requestID, messageID string, summonedAt time.Time) DesktopEvent {
	cat, _ := expertCategory(e)
	ver := e.Version
	if ver == "" {
		ver = "1.0.0"
	}
	return DesktopEvent{
		"eventCode": "expert_actual_use",
		"id":        e.ExpertID, "name": e.DisplayNameZH, "expertTitle": e.ProfessionZH,
		"type": cat, "expertType": e.ExpertType, "source": "builtin", "version": ver,
		"cost": max(int64(0), time.Since(summonedAt).Milliseconds()), "characterCount": jsLength(expertChatPrompt),
		"conversationId": conversationID, "requestId": requestID, "messageId": messageID,
		"requestModelId": "fast-model", "requestModelName": "fast-model",
	}
}
