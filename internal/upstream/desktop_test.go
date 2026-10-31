// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// testDesktopUA 任务中心桌面端请求的 UA：国内版桌面端 5.9.3、内置 CLI 2.165.0（跟随网关其余请求）。
const testDesktopUA = "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0"

// TestReportDesktopEventFingerprint 断言桌面指纹上报：走 chatBase、UA 为桌面形状、
// 事件数组自动注入 workbuddy-desktop 公共指纹（覆盖同名业务键）。
func TestReportDesktopEventFingerprint(t *testing.T) {
	var got []map[string]any
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/report" {
			t.Errorf("path=%s want /v2/report", r.URL.Path)
		}
		gotUA = r.Header.Get("User-Agent")
		if r.Header.Get("X-User-Id") != "u-dt" {
			t.Errorf("X-User-Id=%q want u-dt", r.Header.Get("X-User-Id"))
		}
		if r.Header.Get("X-Product") != "SaaS" {
			t.Errorf("X-Product=%q want SaaS", r.Header.Get("X-Product"))
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("body must be array: %v", err)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	err := c.ReportDesktopEvent(&auth.Auth{AccessToken: "at", UID: "u-dt", Nickname: "RenJie"},
		DesktopEvent{"eventCode": "agent_task_created", "mode": "craft"})
	if err != nil {
		t.Fatalf("report desktop: %v", err)
	}
	if gotUA != testDesktopUA {
		t.Errorf("UA=%q want %q", gotUA, testDesktopUA)
	}
	if len(got) != 1 {
		t.Fatalf("events=%d want 1", len(got))
	}
	ev := got[0]
	if ev["extName"] != "workbuddy-desktop" {
		t.Errorf("extName=%v want workbuddy-desktop", ev["extName"])
	}
	if ev["ideType"] != "WorkBuddy" || ev["ideName"] != "WorkBuddy" {
		t.Errorf("ideType/ideName=%v/%v want WorkBuddy", ev["ideType"], ev["ideName"])
	}
	if ev["userId"] != "u-dt" {
		t.Errorf("userId=%v want u-dt", ev["userId"])
	}
	if ev["userNickname"] != "RenJie" {
		t.Errorf("userNickname=%v want RenJie", ev["userNickname"])
	}
	if ev["mode"] != "craft" {
		t.Errorf("业务字段 mode=%v 被指纹覆盖或缺失", ev["mode"])
	}
	// machineId 应为 uid 稳定派生（36 hex）。
	if len(ev["machineId"].(string)) != 36 {
		t.Errorf("machineId 长度=%d want 36", len(ev["machineId"].(string)))
	}
}

// TestDesktopChatSequenceShape 断言完整对话事件链的事件码顺序与关键字段。
func TestDesktopChatSequenceShape(t *testing.T) {
	events := DesktopChatSequence("conv-1", "req-1", "msg-1", "fast-model", "fast-model")
	wantCodes := []string{
		"agent_task_created", "chat_message_send", "chat_request_send",
		"chat_message_response", "chat_message_status", "chat_request_response",
	}
	if len(events) != len(wantCodes) {
		t.Fatalf("events=%d want %d", len(events), len(wantCodes))
	}
	for i, want := range wantCodes {
		if events[i]["eventCode"] != want {
			t.Errorf("events[%d].eventCode=%v want %v", i, events[i]["eventCode"], want)
		}
	}
	resp := events[3]
	if resp["isSuccessful"] != true {
		t.Errorf("chat_message_response.isSuccessful=%v want true（RichMeow 判据）", resp["isSuccessful"])
	}
	if resp["finishReason"] != "stop" {
		t.Errorf("finishReason=%v want stop", resp["finishReason"])
	}
	if resp["conversationId"] != "conv-1" {
		t.Errorf("conversationId=%v want conv-1", resp["conversationId"])
	}
}

// TestSetAppearanceTheme 断言主题设置端点形状（点亮 Hp_Appearance 的 API）。
func TestSetAppearanceTheme(t *testing.T) {
	var got map[string]any
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/user-asset/appearance/set" {
			t.Errorf("path=%s want /v2/user-asset/appearance/set", r.URL.Path)
		}
		gotHeader = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &got)
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	a := &auth.Auth{AccessToken: "at", UID: "u1", Domain: "www.codebuddy.cn"}
	if err := c.SetAppearanceTheme(a, "theme-tkmw7j"); err != nil {
		t.Fatalf("set theme: %v", err)
	}
	if gotHeader.Get("User-Agent") != testDesktopUA {
		t.Errorf("UA=%q want desktop UA", gotHeader.Get("User-Agent"))
	}
	for k, want := range map[string]string{
		"Authorization":    "Bearer at",
		"Content-Type":     "application/json",
		"User-Agent":       testDesktopUA,
		"X-Domain":         "www.codebuddy.cn",
		"X-Product":        "SaaS",
		"X-Requested-With": "XMLHttpRequest",
		"X-User-Id":        "u1",
	} {
		if got := gotHeader.Get(k); got != want {
			t.Errorf("%s = %q，应为 %q", k, got, want)
		}
	}
	if got := gotHeader.Get("X-Request-ID"); !hex32.MatchString(got) {
		t.Errorf("X-Request-ID = %q，应为 32 位十六进制", got)
	}
	if got["kind"] != "theme" || got["resource_key"] != "theme-tkmw7j" {
		t.Errorf("body=%v want kind=theme resource_key=theme-tkmw7j", got)
	}
}

// TestDeviceIDs 设备标识按账号稳定派生、形状照官方：桌面端 machineId 是大写 UUID
// （IOPlatformUUID），CLI 的 sessionId 是小写 v4 UUID，网页 machineId 是 32 位小写十六进制。
func TestDeviceIDs(t *testing.T) {
	a := &auth.Auth{UID: "u-1"}
	upperUUID := regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}$`)
	v4UUID := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if id := desktopMachineID(a); !upperUUID.MatchString(id) {
		t.Errorf("desktopMachineID = %q，应为大写 UUID", id)
	}
	if id := cliSessionID(a); !v4UUID.MatchString(id) {
		t.Errorf("cliSessionID = %q，应为小写 v4 UUID", id)
	}
	if id := webVisitorID(a); !hex32.MatchString(id) || strings.ToLower(id) != id {
		t.Errorf("webVisitorID = %q，应为 32 位小写十六进制", id)
	}
	if desktopMachineID(a) != desktopMachineID(&auth.Auth{UID: "u-1"}) {
		t.Error("同账号结果应相同")
	}
	if desktopMachineID(a) == desktopMachineID(&auth.Auth{UID: "u-2"}) {
		t.Error("换账号结果应变")
	}
	if strings.EqualFold(desktopMachineID(a), cliSessionID(a)) {
		t.Error("不同用途结果应不同")
	}
}

// TestReportDesktopEventErrors 空事件不请求；上游 400 返回错误。
func TestReportDesktopEventErrors(t *testing.T) {
	f := newUpFake(t)
	c := upClient(f)
	if err := c.ReportDesktopEvent(upAuth()); err == nil {
		t.Fatal("空事件应返回错误")
	}
	if n := len(f.seen()); n != 0 {
		t.Fatalf("空事件不应发请求，实际 %d", n)
	}

	f.on(http.MethodPost, desktopReportPath, 400, `{"code":400,"msg":"report rejected"}`)
	if err := c.ReportDesktopEvent(upAuth(), DesktopEvent{"eventCode": "x"}); err == nil {
		t.Fatal("上游 400 应返回错误")
	}
}

// TestReportDesktopEventHeadersAndOverride 桌面上报的请求头与业务字段覆盖指纹
// （设备标识对齐真实设备）。
func TestReportDesktopEventHeadersAndOverride(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, desktopReportPath, 200, `{"code":0,"msg":"OK"}`)
	c := upClient(f)
	a := &auth.Auth{AccessToken: "at", UID: "u-9", Nickname: "昵称", Domain: "www.codebuddy.cn"}
	err := c.ReportDesktopEvent(a, DesktopEvent{
		"eventCode": "chat_message_response", "machineId": "real-machine", "qimei36": "q-36",
	})
	if err != nil {
		t.Fatalf("ReportDesktopEvent: %v", err)
	}
	r := f.seen()[0]
	for k, want := range map[string]string{
		"Authorization":    "Bearer at",
		"Accept":           "application/json, text/plain, */*",
		"Content-Type":     "application/json;charset=UTF-8",
		"User-Agent":       testDesktopUA,
		"X-Domain":         "www.codebuddy.cn",
		"X-Product":        "SaaS",
		"X-Requested-With": "XMLHttpRequest",
		"X-User-Id":        "u-9",
	} {
		if got := r.header.Get(k); got != want {
			t.Errorf("%s = %q，应为 %q", k, got, want)
		}
	}
	// 官方 CommonHeaderHttpInterceptor 的 X-Request-ID：32 位十六进制，不是十进制时间戳。
	if got := r.header.Get("X-Request-ID"); !hex32.MatchString(got) {
		t.Errorf("X-Request-ID = %q，应为 32 位十六进制", got)
	}
	var events []map[string]any
	if err := json.Unmarshal(r.body, &events); err != nil {
		t.Fatalf("body 应为数组: %v", err)
	}
	ev := events[0]
	if ev["machineId"] != "real-machine" || ev["qimei36"] != "q-36" {
		t.Errorf("业务字段应覆盖指纹: %v", ev)
	}
	if ev["eventCode"] != "chat_message_response" || ev["userId"] != "u-9" {
		t.Errorf("事件字段 = %v", ev)
	}
}

// TestSetAppearanceThemeError 主题接口失败返回错误（成功形状已有用例）。
func TestSetAppearanceThemeError(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, desktopAppearanceSet, 400, `{"code":400,"msg":"theme rejected"}`)
	c := upClient(f)
	err := c.SetAppearanceTheme(upAuth(), "light")
	if err == nil || !strings.Contains(err.Error(), "theme rejected") {
		t.Fatalf("应返回带 msg 的错误，got %v", err)
	}
	r := f.seen()[0]
	if r.header.Get("X-Product") != "SaaS" || r.header.Get("X-User-Id") != "u-1" {
		t.Errorf("请求头 = %v", r.header)
	}
}

// TestDesktopChatSequencePassthrough 对话链的关键 id 与模型字段逐一透传。
func TestDesktopChatSequencePassthrough(t *testing.T) {
	events := DesktopChatSequence("conv-9", "req-9", "msg-9", "fast-model", "Fast Model")
	if len(events) != 6 {
		t.Fatalf("events = %d，应为 6", len(events))
	}
	created := events[0]
	if created["conversationId"] != "conv-9" || created["messageId"] != "msg-9" {
		t.Errorf("agent_task_created id = %v/%v", created["conversationId"], created["messageId"])
	}
	if created["requestModelId"] != "fast-model" || created["requestModelName"] != "Fast Model" {
		t.Errorf("模型字段 = %v/%v", created["requestModelId"], created["requestModelName"])
	}
	for i, ev := range events[1:] {
		if ev["rootRequestId"] != "req-9" || ev["parentConversationId"] != "conv-9" {
			t.Errorf("events[%d] 的 JOIN 字段不全: %v", i+1, ev)
		}
	}
	// traceId 只出现在前四个请求事件里（chat_request_response 不带）。
	for i := 1; i <= 4; i++ {
		if events[i]["traceId"] != "req-9" {
			t.Errorf("events[%d].traceId = %v，应为 req-9", i, events[i]["traceId"])
		}
	}
	// 三个 chat_message_* 带同一个模型消息 id（官方每次调模型新生成），不是用户消息的 id。
	modelMsg, _ := events[1]["messageId"].(string)
	if !upstreamtest.UUIDv7Hex.MatchString(modelMsg) || events[4]["messageId"] != modelMsg || created["messageId"] != "msg-9" {
		t.Errorf("messageId：chat_message_send %v、chat_message_status %v、建任务 %v", events[1]["messageId"], events[4]["messageId"], created["messageId"])
	}
	if events[2]["codebuddy.session_id"] != "conv-9" || events[2]["codebuddy.conversation_request_id"] != "req-9" {
		t.Errorf("chat_request_send codebuddy 字段 = %v", events[2])
	}
	if events[3]["responseModelId"] != "fast-model" || events[3]["messageId"] != modelMsg {
		t.Errorf("chat_message_response 字段 = %v", events[3])
	}

	// 5.8.3 各事件补的业务字段：请求/会话/模型 id；运行时时间与步数取典型值。
	if created["requestId"] != "req-9" || created["is_worktree_task"] != false || created["is_from_code"] != false ||
		created["task_target"] != "local" {
		t.Errorf("agent_task_created 5.8.3 字段 = %v", created)
	}
	for _, k := range []string{"buddyId", "buddyName"} {
		if _, ok := created[k]; ok {
			t.Errorf("agent_task_created 不应再有空 %s: %v", k, created)
		}
	}
	for i, ev := range events[1:] {
		if ev["conversationId"] != "conv-9" || ev["requestId"] != "req-9" {
			t.Errorf("events[%d] 缺会话/请求 id: %v", i+1, ev)
		}
	}
	if events[1]["requestModelId"] != "fast-model" || events[1]["totalStepCount"] != 1 || events[1]["presentAt"] == nil {
		t.Errorf("chat_message_send 5.8.3 字段 = %v", events[1])
	}
	if events[2]["mode"] != "craft" || events[2]["requestModelName"] != "Fast Model" || events[2]["fileUri"] != "" || events[2]["presentAt"] == nil {
		t.Errorf("chat_request_send 5.8.3 字段 = %v", events[2])
	}
	if events[3]["requestModelId"] != "fast-model" || events[3]["totalStepCount"] != 1 || events[3]["presentAt"] == nil {
		t.Errorf("chat_message_response 5.8.3 字段 = %v", events[3])
	}
	if events[4]["requestModelName"] != "Fast Model" {
		t.Errorf("chat_message_status 5.8.3 字段 = %v", events[4])
	}
	if events[5]["traceId"] != "req-9" || events[5]["agentName"] != "cli" || events[5]["agentType"] != "main" || events[5]["presentAt"] == nil {
		t.Errorf("chat_request_response 5.8.3 字段 = %v", events[5])
	}
}

// TestRendererEvent 渲染层事件：mode 盖成 LOCAL；buddyId、buddyName 都没有才补空串，有一个就不动。
func TestRendererEvent(t *testing.T) {
	ev := rendererEvent(DesktopEvent{"eventCode": "x", "mode": "craft"})
	if ev["mode"] != "LOCAL" || ev["buddyId"] != "" || ev["buddyName"] != "" {
		t.Errorf("补字段 = %v", ev)
	}
	ev = rendererEvent(DesktopEvent{"eventCode": "x", "buddyName": "应用"})
	if _, ok := ev["buddyId"]; ok || ev["buddyName"] != "应用" {
		t.Errorf("已有 buddyName 时不该补 buddyId: %v", ev)
	}
}

// TestDesktopBuddyAppSequence 进入 Buddy 应用五连事件：都经渲染层上报服务（mode LOCAL）；发现、
// 曝光、进入的 buddyId/buddyName 是适配器补的空串，授权确认、跳过绑定才带这个应用。
func TestDesktopBuddyAppSequence(t *testing.T) {
	events := DesktopBuddyAppSequence("cb_1", "企鹅教师助手")
	wantCodes := []string{
		"buddyapp_discover_click", "buddyapp_show", "buddyapp_enter_click",
		"buddyapp_auth_confirm_click", "buddyapp_bindaccount_skip_click",
	}
	if len(events) != len(wantCodes) {
		t.Fatalf("events = %d，应为 %d", len(events), len(wantCodes))
	}
	for i, want := range wantCodes {
		if events[i]["eventCode"] != want {
			t.Errorf("events[%d].eventCode = %v，应为 %v", i, events[i]["eventCode"], want)
		}
		wantBuddy, wantName := "", ""
		if i >= 3 {
			wantBuddy, wantName = "cb_1", "企鹅教师助手"
		}
		if events[i]["mode"] != "LOCAL" || events[i]["buddyId"] != wantBuddy || events[i]["buddyName"] != wantName {
			t.Errorf("events[%d] 公共字段 = %v", i, events[i])
		}
	}
	if _, ok := events[0]["elementId"]; ok {
		t.Errorf("buddyapp_discover_click 官方不带字段: %v", events[0])
	}
	for i := 1; i < len(events); i++ {
		if events[i]["elementId"] != "cb_1" || events[i]["elementName"] != "企鹅教师助手" {
			t.Errorf("events[%d] 应用标识 = %v", i, events[i])
		}
	}
	if events[1]["elementId"] != "cb_1" || events[1]["position"] != 2 {
		t.Errorf("buddyapp_show = %v", events[1])
	}
	if events[2]["isFirstPage"] != true {
		t.Errorf("buddyapp_enter_click.isFirstPage = %v，应为布尔 true", events[2]["isFirstPage"])
	}
}

// TestDesktopAutomationCreateEvent 定时任务创建事件。
func TestDesktopAutomationCreateEvent(t *testing.T) {
	ev := DesktopAutomationCreateEvent("每日备份")
	if ev["eventCode"] != "automated_task_create_suc" || ev["name"] != "每日备份" {
		t.Errorf("事件 = %v", ev)
	}
	if ev["source"] != "manually" || ev["modelId"] != "fast-model" || ev["scheduleType"] != "once" ||
		ev["mode"] != "LOCAL" || ev["buddyId"] != "" || ev["buddyName"] != "" {
		t.Errorf("事件字段 = %v", ev)
	}
	if ev["pushToWeChat"] != false || ev["pushToWecomBot"] != false {
		t.Errorf("推送开关应为布尔 false: %v", ev)
	}
}

// TestReportWebEvent Web 端指纹上报：走 webBase、浏览器头、事件字段；400 返回错误。
func TestReportWebEvent(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, "/v2/report", 200, `{"code":0,"msg":"OK"}`)
	c := upClient(f)
	c.WebBaseCN = "https://www.workbuddy.cn"
	a := &auth.Auth{AccessToken: "at", UID: "u-web", Nickname: "网页号", EnterpriseID: "ent-1"}
	if err := c.ReportWebEvent(a, "library_doc_intro_click", "https://www.workbuddy.cn/library/1", "btn-intro", "介绍"); err != nil {
		t.Fatalf("ReportWebEvent: %v", err)
	}
	r := f.bizSeen()[0]
	if r.url != "https://www.workbuddy.cn/v2/report" {
		t.Errorf("URL = %q", r.url)
	}
	// 官网网页的 sendBeacon 形状：text/plain、Accept: */*、Origin/Referer/浏览器 UA，
	// 靠网页登录态 Cookie 认人（不带 Authorization）。
	for k, want := range map[string]string{
		"Authorization":      "",
		"Content-Type":       "text/plain;charset=UTF-8",
		"Accept":             "*/*",
		"Origin":             "https://www.workbuddy.cn",
		"Referer":            "https://www.workbuddy.cn/library/1",
		"User-Agent":         browserUA,
		"Sec-Fetch-Site":     "same-origin",
		"Sec-Fetch-Mode":     "no-cors",
		"Sec-Fetch-Dest":     "empty",
		"Accept-Language":    "zh-CN,zh;q=0.9",
		"Sec-CH-UA-Mobile":   "?0",
		"Sec-CH-UA-Platform": `"macOS"`,
	} {
		if got := r.header.Get(k); got != want {
			t.Errorf("%s = %q，应为 %q", k, got, want)
		}
	}
	if got := r.header.Get("Cookie"); !strings.Contains(got, "session=at") {
		t.Errorf("Cookie = %q，应含登录态 session=at", got)
	}
	if got := r.header.Get("Sec-CH-UA"); got != `"Chromium";v="152", "Not?A_Brand";v="24", "Google Chrome";v="152"` {
		t.Errorf("Sec-CH-UA = %q", got)
	}
	for _, k := range []string{"X-User-Id", "X-Client-Platform", "X-Requested-With", "X-Product", "X-Domain"} {
		if got := r.header.Get(k); got != "" {
			t.Errorf("网页上报不应带 %s，got %q", k, got)
		}
	}
	var events []map[string]any
	if err := json.Unmarshal(r.body, &events); err != nil || len(events) != 1 {
		t.Fatalf("body 应为单元素数组: %v %s", err, r.body)
	}
	ev := events[0]
	if ev["eventCode"] != "library_doc_intro_click" || ev["pageURL"] != "https://www.workbuddy.cn/library/1" ||
		ev["elementId"] != "btn-intro" || ev["elementName"] != "介绍" {
		t.Errorf("事件字段 = %v", ev)
	}
	if ev["os"] != "macOS" || ev["osVersion"] != "10.15.7" || ev["userId"] != "u-web" || ev["userNickname"] != "网页号" ||
		ev["username"] != "网页号" || ev["enterpriseId"] != "ent-1" || ev["product"] != "UserCenter" ||
		ev["type"] != "library_doc_intro_click" || ev["machineId"] != webVisitorID(a) || ev["reportDelay"] != float64(0) {
		t.Errorf("web 指纹字段 = %v", ev)
	}
	if sid, _ := ev["sessionId"].(string); !regexp.MustCompile(`^\d{13}-[0-9a-z]{9}$`).MatchString(sid) {
		t.Errorf("sessionId = %q，应为「毫秒时间戳-9 位 36 进制」", sid)
	}
	if _, ok := ev["arch"]; ok {
		t.Errorf("官网网页事件不带 arch: %v", ev)
	}
	// 事件里的 userAgent 与请求头同一个 UA。
	if ev["userAgent"] != r.header.Get("User-Agent") {
		t.Errorf("事件 userAgent = %v，应等于请求头 %q", ev["userAgent"], r.header.Get("User-Agent"))
	}

	f2 := newUpFake(t)
	f2.on(http.MethodPost, "/v2/report", 400, `{"code":400,"msg":"web report rejected"}`)
	c2 := upClient(f2)
	c2.WebBaseCN = "https://www.workbuddy.cn"
	if err := c2.ReportWebEvent(a, "x", "u", "e", "n"); err == nil {
		t.Fatal("上游 400 应返回错误")
	}
}

// TestDesktopTemplateUseSequence 模板任务：点模板按钮的自动埋点 + template_used（渲染层，模板 id 是
// 数字）→ 对话链（建任务事件带模板）→ agent_task_created_with_template（id 是模板 id 的字符串）。
func TestDesktopTemplateUseSequence(t *testing.T) {
	events := DesktopTemplateUseSequence("conv-t", "req-t", HomeScene{ID: 52, Name: "金融服务", Mode: "working"})
	if len(events) != 9 {
		t.Fatalf("events = %d，应为 9", len(events))
	}
	click := events[0]
	if click["eventCode"] != "web_element_click" || click["elementId"] != "agent_template_clicked" ||
		click["elementName"] != "点击模板" || click["source"] != "金融服务" || click["pageURL"] != "#/" ||
		click["mode"] != nil || click["buddyId"] != nil {
		t.Errorf("模板按钮点击 = %v", click)
	}
	used := events[1]
	if used["eventCode"] != "template_used" || used["template_id"] != 52 || used["template_name"] != "金融服务" ||
		used["task_mode"] != "working" || used["mode"] != "LOCAL" || used["buddyId"] != "" || used["buddyName"] != "" {
		t.Errorf("template_used = %v", used)
	}
	if _, ok := used["prompt_index"]; ok {
		t.Errorf("没选示例提示词不该带 prompt_index: %v", used)
	}
	created := events[2]
	if created["eventCode"] != "agent_task_created" || created["conversationId"] != "conv-t" ||
		created["has_template"] != true || created["action"] != "52" || created["template_name"] != "金融服务" {
		t.Errorf("建任务事件 = %v", created)
	}
	if id, _ := created["messageId"].(string); !upstreamtest.UUIDv7Hex.MatchString(id) {
		t.Errorf("模板对话 messageId = %v，应是官方消息 id 的形状", created["messageId"])
	}
	if events[5]["eventCode"] != "chat_message_response" {
		t.Errorf("中间应是对话链: %v", events[5]["eventCode"])
	}
	withTpl := events[8]
	if withTpl["eventCode"] != "agent_task_created_with_template" || withTpl["id"] != "52" ||
		withTpl["name"] != "金融服务" || withTpl["requestId"] != "req-t" ||
		withTpl["mode"] != "LOCAL" || withTpl["isCustomModel"] != true || withTpl["buddyId"] != nil {
		t.Errorf("agent_task_created_with_template = %v", withTpl)
	}
}

// TestDesktopPlaybookPromptSequence 灵感案例做同款：发现页三次点击（分类、卡片、做同款按钮）→
// playbook_cta_click → 对话链（输入长度是案例提示词长度）→ playbook_prompt_send，字段取案例的真实值。
func TestDesktopPlaybookPromptSequence(t *testing.T) {
	pc := PlaybookCase{ID: "case-7", Title: "案例七", Type: "html", CategoryID: "business",
		CategoryName: "商业分析", PromptLength: 441, Position: 3}
	events := DesktopPlaybookPromptSequence("conv-p", "req-p", pc)
	if len(events) != 12 {
		t.Fatalf("events = %d，应为 12", len(events))
	}
	clicks := []struct{ page, id, name string }{
		{"playbook_list", "playbook_sceneFilter", "business"},
		{"playbook_list", "playbookCard_case-7", "案例七"},
		{"playbook_detail", "playbook_ctaClick", "案例七"},
	}
	for i, want := range clicks {
		ev := events[i]
		if ev["eventCode"] != "web_element_click" || ev["pageName"] != want.page || ev["elementId"] != want.id ||
			ev["elementName"] != want.name || ev["source"] != "scene" || ev["pageURL"] != nil ||
			ev["mode"] != "LOCAL" || ev["buddyId"] != "" || ev["buddyName"] != "" {
			t.Errorf("第 %d 次点击 = %v", i+1, ev)
		}
	}
	cta := events[3]
	if cta["eventCode"] != "playbook_cta_click" || cta["id"] != "case-7" || cta["name"] != "案例七" ||
		cta["type"] != "html" || cta["position"] != 3 || cta["source"] != "scene" ||
		cta["categoryId"] != "business" || cta["categoryName"] != "商业分析" ||
		cta["skills"] != "" || cta["skillNames"] != "" || cta["expertId"] != nil ||
		cta["mode"] != "LOCAL" || cta["buddyId"] != "" {
		t.Errorf("playbook_cta_click = %v", cta)
	}
	rec := events[4]
	if rec["eventCode"] != "query_recommend_prompt_send" || rec["batchId"] != "" || rec["recommendId"] != "case-7" ||
		rec["queryId"] != "case-7" || rec["type"] != "商业分析" || rec["query"] != "案例七" || rec["position"] != 0 ||
		rec["mode"] != "work" || rec["buddyId"] != "" || rec["buddyName"] != "" {
		t.Errorf("query_recommend_prompt_send = %v", rec)
	}
	if noCat := DesktopPlaybookPromptSequence("c", "r", PlaybookCase{ID: "x", Type: "html"})[4]; noCat["type"] != "html" {
		t.Errorf("没有分类名时推荐类型应取案例类型: %v", noCat)
	}
	for _, ev := range events[5:11] {
		if ev["conversationId"] != "conv-p" || ev["requestId"] != "req-p" {
			t.Errorf("对话链 id 不对: %v", ev)
		}
		if ev["eventCode"] == "chat_request_send" && ev["inputLength"] != 441 {
			t.Errorf("chat_request_send.inputLength = %v，应为提示词长度", ev["inputLength"])
		}
	}
	send := events[11]
	if send["mode"] != nil || send["buddyId"] != nil {
		t.Errorf("playbook_prompt_send 走 wb.metrics，不该补渲染层字段: %v", send)
	}
	if send["eventCode"] != "playbook_prompt_send" || send["conversationId"] != "conv-p" ||
		send["requestId"] != "req-p" || send["id"] != "case-7" || send["type"] != "html" ||
		send["promptLength"] != 441 || send["isOfficial"] != 1 || send["source"] != "scene" ||
		send["categoryId"] != "business" || send["categoryName"] != "商业分析" ||
		send["ext1"] != "discover" || send["queryId"] != "case-7" || send["skills"] != "" ||
		send["skillNames"] != "" || send["position"] != nil {
		t.Errorf("playbook_prompt_send = %v", send)
	}
}

// TestDesktopDesignCanvasSequence 设计画布事件组：对话链 + 创建/打开画布。
func TestDesktopDesignCanvasSequence(t *testing.T) {
	req := "0123456789abcdef0123456789abcdef"
	events := DesktopDesignCanvasSequence("conv-c", req)
	if len(events) != 8 {
		t.Fatalf("events = %d，应为 8", len(events))
	}
	create := events[6]
	if create["eventCode"] != "wbx_design_canvas_task_create" || create["conversationId"] != "conv-c" ||
		create["requestId"] != req || create["source"] != "summon_keyword" || create["isSuccessful"] != true ||
		create["isCustomModel"] != false || create["name"] != "" || create["inputLength"] != 0 {
		t.Errorf("create = %v", create)
	}
	open := events[7]
	wantID := "ardot-file-" + req[len(req)-8:]
	if open["eventCode"] != "wbx_design_canvas_open" || open["id"] != wantID || open["type"] != "page" {
		t.Errorf("open = %v，id 应为 %v", open, wantID)
	}
}

// TestMarketExpertList 专家市场列表：请求形状、解析、expert_type 可选、
// 400 与 data 非法返回错误。
func TestMarketExpertList(t *testing.T) {
	const path = "/portal/operation-platform/market/expert/list"
	t.Run("成功且带 expert_type", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, `{"code":0,"data":{"experts":[
			{"expert_id":"ex_1","expert_type":"agent","display_name_zh":"写作专家","profession_zh":"写作","version":"1.2.0","categories":["expert-writing"]},
			{"expert_id":"ex_2","expert_type":"team","display_name_zh":"团队","profession_zh":"综合","version":"","categories":[]}
		]}}`)
		c := upClient(f)
		a := upAuth()
		a.Domain = "www.codebuddy.cn"
		experts, err := c.MarketExpertList(a, "agent")
		if err != nil {
			t.Fatalf("MarketExpertList: %v", err)
		}
		if len(experts) != 2 || experts[0].ExpertID != "ex_1" || experts[0].DisplayNameZH != "写作专家" ||
			experts[0].Version != "1.2.0" || experts[1].ExpertType != "team" {
			t.Errorf("experts = %+v", experts)
		}
		r := f.seen()[0]
		if r.method != http.MethodPost || r.path != path {
			t.Errorf("请求 = %s %s", r.method, r.path)
		}
		for k, want := range map[string]string{
			"Authorization":    "Bearer at",
			"Content-Type":     "application/json",
			"User-Agent":       testDesktopUA,
			"X-Domain":         "www.codebuddy.cn",
			"X-Product":        "SaaS",
			"X-Requested-With": "XMLHttpRequest",
			"X-User-Id":        "u-1",
		} {
			if got := r.header.Get(k); got != want {
				t.Errorf("%s = %q，应为 %q", k, got, want)
			}
		}
		if got := r.header.Get("X-Request-ID"); !hex32.MatchString(got) {
			t.Errorf("X-Request-ID = %q，应为 32 位十六进制", got)
		}
		if got := r.header.Get("X-Language"); got != "" {
			t.Errorf("不搜索时不带 X-Language，got %q", got)
		}
		var body map[string]any
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Fatalf("请求体: %v", err)
		}
		if body["expert_type"] != "agent" || body["page"] != float64(1) || body["page_size"] != float64(20) ||
			body["sort_by"] != "reco_rank" || body["sort_order"] != "desc" {
			t.Errorf("请求体 = %v", body)
		}
		if _, ok := body["keyword"]; ok {
			t.Errorf("不搜索时不带 keyword: %v", body)
		}
	})

	t.Run("响应先取 items", func(t *testing.T) {
		for _, tc := range []struct{ name, data, want string }{
			{"两个都有取 items", `{"items":[{"expert_id":"ex_i"}],"experts":[{"expert_id":"ex_e"}]}`, "ex_i"},
			{"只有 experts", `{"experts":[{"expert_id":"ex_e"}]}`, "ex_e"},
			{"items 为空数组不再看 experts", `{"items":[],"experts":[{"expert_id":"ex_e"}]}`, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newUpFake(t)
				f.on(http.MethodPost, path, 200, `{"code":0,"data":`+tc.data+`}`)
				experts, err := upClient(f).MarketExpertList(upAuth(), "agent")
				if err != nil {
					t.Fatalf("MarketExpertList: %v", err)
				}
				got := ""
				if len(experts) > 0 {
					got = experts[0].ExpertID
				}
				if got != tc.want || len(experts) > 1 {
					t.Errorf("experts = %+v，应为 %q", experts, tc.want)
				}
			})
		}
	})

	t.Run("按关键词搜", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, `{"code":0,"data":{"items":[{"expert_id":"ex_v","display_name_zh":"无畏契约拼豆专家"}]}}`)
		experts, err := upClient(f).MarketExpertSearch(upAuth(), "agent", "无畏契约拼豆专家")
		if err != nil || len(experts) != 1 || experts[0].ExpertID != "ex_v" {
			t.Fatalf("experts=%+v err=%v", experts, err)
		}
		r := f.seen()[0]
		if got := r.header.Get("X-Language"); got != "zh-CN" {
			t.Errorf("X-Language = %q，应为 zh-CN", got)
		}
		var body map[string]any
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Fatalf("请求体: %v", err)
		}
		if body["keyword"] != "无畏契约拼豆专家" || body["expert_type"] != "agent" || body["page"] != float64(1) ||
			body["page_size"] != float64(20) || body["sort_by"] != "reco_rank" || body["sort_order"] != "desc" {
			t.Errorf("请求体 = %v", body)
		}
	})

	t.Run("翻页只改 page", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, `{"code":0,"data":{"experts":[]}}`)
		if _, err := upClient(f).MarketExpertListPage(upAuth(), "agent", 3); err != nil {
			t.Fatalf("MarketExpertListPage: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(f.seen()[0].body, &body); err != nil {
			t.Fatalf("请求体: %v", err)
		}
		if body["page"] != float64(3) || body["page_size"] != float64(20) || body["expert_type"] != "agent" ||
			body["sort_by"] != "reco_rank" || body["sort_order"] != "desc" {
			t.Errorf("请求体 = %v", body)
		}
	})

	t.Run("空 expert_type 不传", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, `{"code":0,"data":{"experts":[]}}`)
		experts, err := upClient(f).MarketExpertList(upAuth(), "")
		if err != nil || len(experts) != 0 {
			t.Fatalf("experts=%v err=%v", experts, err)
		}
		var body map[string]any
		json.Unmarshal(f.seen()[0].body, &body)
		if _, ok := body["expert_type"]; ok {
			t.Errorf("空 expert_type 不应写入请求体: %v", body)
		}
	})

	t.Run("HTTP 400", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 400, `{"code":400,"msg":"market boom"}`)
		if _, err := upClient(f).MarketExpertList(upAuth(), "agent"); err == nil {
			t.Fatal("上游 400 应返回错误")
		}
	})

	t.Run("data 非法", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, `{"code":0,"data":"nope"}`)
		if _, err := upClient(f).MarketExpertList(upAuth(), "agent"); err == nil {
			t.Fatal("data 类型不符应返回解析错误")
		}
	})
}

// chunkReader 按块返回响应体（模拟 SSE 分块到达）。
type chunkReader struct {
	chunks [][]byte
	i      int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.i])
	r.i++
	return n, nil
}

func (r *chunkReader) Close() error { return nil }

// TestMarketExpertProtected 专家对话要不要带 X-Moderation-Type 的判定（对齐官方 shouldProtectSession）：
// 云端（is_cloud===1）或付费（pay_detail.pay_mode / payMode，回落 need_pay===1）为真。
func TestMarketExpertProtected(t *testing.T) {
	pm := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name string
		e    MarketExpert
		want bool
	}{
		{"云端 is_cloud=1", MarketExpert{IsCloud: 1}, true},
		{"付费 pay_detail.pay_mode", MarketExpert{PayDetail: &expertPayDetail{PayMode: pm(2)}}, true},
		{"付费 pay_detail.payMode", MarketExpert{PayDetail: &expertPayDetail{PayModeCamel: pm(1)}}, true},
		{"付费回落 need_pay=1", MarketExpert{NeedPay: 1}, true},
		{"pay_mode=0 不算付费", MarketExpert{PayDetail: &expertPayDetail{PayMode: pm(0)}}, false},
		{"免费非云端", MarketExpert{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.e.Protected(); got != tc.want {
				t.Errorf("Protected() = %v，want %v", got, tc.want)
			}
		})
	}
}

// TestDesktopChatWithExpert 真实桌面 chat：请求头/体形状、从 SSE 抓服务端
// requestId（跳过不合规的 id）、上游失败与找不到 id 的错误。
func TestDesktopChatWithExpert(t *testing.T) {
	const path = "/v2/chat/completions"
	validID := "cmb-0123456789abcdef0123456789abcdef"
	const bareID = "fedcba9876543210fedcba9876543210"

	t.Run("成功并透出会话与请求 id", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, "data: {\"id\":\""+validID+"\"}\n\n")
		c := upClient(f)
		// 用配置覆盖的 UA 证明专家对话走的是 CLI 的 UA（c.userAgent），不是后台服务的 UA。
		c.UserAgent = "custom-cli/9.9"
		a := upAuth()
		a.Domain = "www.codebuddy.cn"
		conv, reqID, err := c.DesktopChatWithExpert(a, "ex_1", false, false)
		if err != nil {
			t.Fatalf("DesktopChatWithExpert: %v", err)
		}
		if reqID != validID {
			t.Errorf("requestID = %q，应为 %q", reqID, validID)
		}
		// 会话 id 同官方新会话：带连字符的 UUID v7。
		if !upstreamtest.UUIDv7.MatchString(conv) {
			t.Errorf("conversationID = %q，应是官方会话 id 的形状", conv)
		}
		r := f.seen()[0]
		for k, want := range map[string]string{
			"Authorization":       "Bearer at",
			"Accept":              "application/json",
			"User-Agent":          "custom-cli/9.9",
			"X-Domain":            "www.codebuddy.cn",
			"X-Product":           "SaaS",
			"X-User-Id":           "u-1",
			"X-Conversation-ID":   conv,
			"X-Agent-Intent":      "craft",
			"X-Agent-Type":        "main",
			"X-IDE-Name":          "WorkBuddy",
			"X-IDE-Type":          "WorkBuddy",
			"X-IDE-Version":       "5.9.3",
			"X-Expert-Id":         "ex_1",
			"X-Expert-Team-Task":  "",
			"X-Moderation-Type":   "",
			"x-codebuddy-request": "",
		} {
			if got := r.header.Get(k); got != want {
				t.Errorf("%s = %q，应为 %q", k, got, want)
			}
		}
		for _, k := range []string{"X-Request-ID", "X-Conversation-Request-ID"} {
			if got := r.header.Get(k); !hex32.MatchString(got) {
				t.Errorf("%s = %q，应为 32 位十六进制", k, got)
			}
		}
		var body map[string]any
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Fatalf("请求体: %v", err)
		}
		if body["model"] != "fast-model" || body["stream"] != true || body["agent"] != "cli" {
			t.Errorf("请求体 = %v", body)
		}
	})

	t.Run("专家团另带 X-Expert-Team-Task", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, "data: {\"id\":\""+validID+"\"}\n\n")
		if _, _, err := upClient(f).DesktopChatWithExpert(upAuth(), "ex_team", true, false); err != nil {
			t.Fatalf("DesktopChatWithExpert: %v", err)
		}
		h := f.seen()[0].header
		if h.Get("X-Expert-Id") != "ex_team" || h.Get("X-Expert-Team-Task") != "true" {
			t.Errorf("X-Expert-Id = %q，X-Expert-Team-Task = %q", h.Get("X-Expert-Id"), h.Get("X-Expert-Team-Task"))
		}
	})

	t.Run("云端 / 付费专家带 X-Moderation-Type", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, "data: {\"id\":\""+validID+"\"}\n\n")
		if _, _, err := upClient(f).DesktopChatWithExpert(upAuth(), "ex_cloud", false, true); err != nil {
			t.Fatalf("DesktopChatWithExpert: %v", err)
		}
		if got := f.seen()[0].header.Get("X-Moderation-Type"); got != `["protected"]` {
			t.Errorf("X-Moderation-Type = %q，应为 [\"protected\"]", got)
		}
	})

	t.Run("先遇到不合规 id 再遇到合规 id", func(t *testing.T) {
		f := newUpFake(t)
		f.onFunc(http.MethodPost, path, func(*upReq) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: &chunkReader{chunks: [][]byte{
				[]byte("data: {\"id\":\"chatcmpl-not-a-request-id\"}\n\n"),
				[]byte("data: {\"id\":\"" + validID + "\"}\n\n"),
			}}}, nil
		})
		_, reqID, err := upClient(f).DesktopChatWithExpert(upAuth(), "", false, false)
		if err != nil || reqID != validID {
			t.Fatalf("reqID=%q err=%v，应为 %q", reqID, err, validID)
		}
	})

	t.Run("同一块里先有不合规 id、空 id，后有合规 id", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200,
			"data: {\"id\":\"chatcmpl-not-a-request-id\",\"x\":{\"id\":\"\"}}\n\ndata: {\"id\":\""+validID+"\"}\n\n")
		_, reqID, err := upClient(f).DesktopChatWithExpert(upAuth(), "", false, false)
		if err != nil || reqID != validID {
			t.Fatalf("reqID=%q err=%v，应为 %q", reqID, err, validID)
		}
	})

	t.Run("id 被拆在两块里", func(t *testing.T) {
		f := newUpFake(t)
		f.onFunc(http.MethodPost, path, func(*upReq) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: &chunkReader{chunks: [][]byte{
				[]byte("data: {\"id\":\"" + validID[:10]),
				[]byte(validID[10:] + "\"}\n\n"),
			}}}, nil
		})
		_, reqID, err := upClient(f).DesktopChatWithExpert(upAuth(), "", false, false)
		if err != nil || reqID != validID {
			t.Fatalf("reqID=%q err=%v，应为 %q", reqID, err, validID)
		}
	})

	t.Run("裸 32hex 也接受", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, "data: {\"id\":\""+bareID+"\"}\n\n")
		_, reqID, err := upClient(f).DesktopChatWithExpert(upAuth(), "", false, false)
		if err != nil || reqID != bareID {
			t.Fatalf("reqID=%q err=%v", reqID, err)
		}
		if f.seen()[0].header.Get("X-Expert-Id") != "" {
			t.Error("空专家 id 不应带 X-Expert-Id")
		}
	})

	t.Run("上游 400", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 400, `{"code":400,"msg":"bad chat"}`)
		_, _, err := upClient(f).DesktopChatWithExpert(upAuth(), "", false, false)
		if err == nil || !strings.Contains(err.Error(), "chat http 400") || !strings.Contains(err.Error(), "bad chat") {
			t.Fatalf("err = %v，应含 chat http 400 与响应体", err)
		}
	})

	t.Run("SSE 没有合规 id", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, path, 200, "data: {\"id\":\"nope\"}\n\n")
		_, _, err := upClient(f).DesktopChatWithExpert(upAuth(), "", false, false)
		if err == nil || !strings.Contains(err.Error(), "未找到服务端 requestId") {
			t.Fatalf("err = %v，应提示未找到 requestId", err)
		}
	})
}

// 专家页搜索事件：搜索词、结果数、专家中心、官方专家、单专家页签，带渲染层的 mode 和 buddy 字段。
func TestDesktopExpertSearchEvent(t *testing.T) {
	ev := DesktopExpertSearchEvent("无畏契约拼豆专家", 3)
	want := DesktopEvent{
		"eventCode": "expert_search", "query": "无畏契约拼豆专家", "count": 3, "scope": "center",
		"source": "builtin", "type": "expert", "mode": "LOCAL", "buddyId": "", "buddyName": "",
	}
	if !reflect.DeepEqual(ev, want) {
		t.Errorf("event = %v，应为 %v", ev, want)
	}
}

// TestDesktopExpertSequences 专家事件构造：召唤三连与使用事件（craft/LOCAL），
// 分类与版本兜底。
func TestDesktopExpertSequences(t *testing.T) {
	full := MarketExpert{
		ExpertID: "ex_1", ExpertType: "agent", DisplayNameZH: "写作专家", ProfessionZH: "写作",
		Version: "2.0.0", Categories: []any{"expert-writing"},
	}
	summon := DesktopExpertSummonSequence(full)
	codes := []string{"web_element_click", "expert_summon_click", "expert_summoned"}
	if len(summon) != len(codes) {
		t.Fatalf("events = %d", len(summon))
	}
	for i, want := range codes {
		if summon[i]["eventCode"] != want {
			t.Errorf("summon[%d] = %v", i, summon[i]["eventCode"])
		}
	}
	if summon[0]["source"] != "ex_1" || summon[0]["type"] != "expert-writing" ||
		summon[0]["version"] != "2.0.0" || summon[0]["pageURL"] != "#/experts" {
		t.Errorf("web_element_click = %v", summon[0])
	}
	if summon[1]["id"] != "ex_1" || summon[1]["name"] != "写作专家" || summon[1]["expertTitle"] != "写作" ||
		summon[1]["expertType"] != "agent" || summon[1]["version"] != "2.0.0" || summon[1]["mode"] != "LOCAL" ||
		summon[1]["buddyId"] != "" || summon[1]["buddyName"] != "" {
		t.Errorf("expert_summon_click = %v", summon[1])
	}
	if summon[2]["id"] != "ex_1" || summon[2]["name"] != "写作专家" || summon[2]["mode"] != "LOCAL" || summon[2]["buddyId"] != "" {
		t.Errorf("expert_summoned = %v", summon[2])
	}
	if summon[0]["mode"] != nil || summon[0]["buddyId"] != nil {
		t.Errorf("召唤按钮点击是界面自动埋点，不经渲染层上报服务: %v", summon[0])
	}

	// 分类/版本缺失时兜底：元素点击 type=expert、召唤事件 type=all、version=1.0.0。
	bare := MarketExpert{ExpertID: "ex_2", DisplayNameZH: "裸专家", ProfessionZH: "综合"}
	bareSummon := DesktopExpertSummonSequence(bare)
	if bareSummon[0]["type"] != "expert" || bareSummon[0]["version"] != "1.0.0" {
		t.Errorf("兜底 = %v/%v", bareSummon[0]["type"], bareSummon[0]["version"])
	}
	if bareSummon[1]["type"] != "all" || bareSummon[2]["type"] != "all" || bareSummon[2]["version"] != "1.0.0" {
		t.Errorf("召唤/使用兜底 = %v/%v", bareSummon[1]["type"], bareSummon[2]["type"])
	}
	// Categories[0] 不是字符串时也兜底。
	odd := MarketExpert{ExpertID: "ex_3", Categories: []any{42}}
	if got := DesktopExpertSummonSequence(odd)[0]["type"]; got != "expert" {
		t.Errorf("非字符串分类应兜底，got %v", got)
	}
	// 专家团的元素点击 type 固定 team。
	team := DesktopExpertSummonSequence(MarketExpert{ExpertID: "ex_4", ExpertType: "team", Categories: []any{"expert-team"}})
	if team[0]["type"] != "team" {
		t.Errorf("专家团元素点击 type = %v，应为 team", team[0]["type"])
	}

	requestID := "abcdef0123456789abcdef0123456789"
	craft := DesktopExpertActualUseEvent(full, "conv-1", requestID, "msg-1", time.Now().Add(-3*time.Second))
	if craft["mode"] != "LOCAL" || craft["eventCode"] != "expert_actual_use" || craft["id"] != "ex_1" ||
		craft["type"] != "expert-writing" || craft["version"] != "2.0.0" {
		t.Errorf("使用事件 = %v", craft)
	}
	// cost 是召唤到使用的毫秒数，characterCount 是提示词「1+1等于几？直接回答。」的 JS 长度。
	if cost, _ := craft["cost"].(int64); cost < 3000 || cost > 60000 || craft["characterCount"] != 12 {
		t.Errorf("使用事件 cost / characterCount = %v / %v", craft["cost"], craft["characterCount"])
	}
	if craft["messageId"] != "msg-1" || craft["conversationId"] != "conv-1" || craft["requestId"] != requestID {
		t.Errorf("使用事件 JOIN 字段 = %v", craft)
	}
	local := DesktopExpertActualUseLocal(bare, "conv-2", requestID, "msg-2", time.Now().Add(time.Second))
	if local["cost"] != int64(0) {
		t.Errorf("召唤时刻在后时 cost 应为 0，got %v", local["cost"])
	}
	// 没有分类的专家：type 与召唤时一样是 "all"（官方 industryId 来自召唤的 type）。
	if local["mode"] != "LOCAL" || local["type"] != "all" || local["version"] != "1.0.0" {
		t.Errorf("LOCAL 使用事件 = %v", local)
	}
}

// TestDesktopFingerprintFollowsClientVersion 事件指纹的版本跟 UA 用同一个客户端版本，commit / releaseDate
// 按域取安装包的值（国内版 5.9.3、国际版 5.9.4），配了 client_version 也不变。
func TestDesktopFingerprintFollowsClientVersion(t *testing.T) {
	for _, tc := range []struct {
		name, domain, ver, commit string
		released                  int64
	}{
		{"国内版", "", "5.9.3", "d46924219e79332f49c37ccebf27292f87001b19", 1793093023713},
		{"国际版", "www.workbuddy.ai", "5.9.4", "fa0a0901906addaa1a4259f6df347388a5563ded", 1793290653510},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &auth.Auth{UID: "u-1", Domain: tc.domain}
			fp := (&Client{}).desktopFingerprint(a)
			if fp["ideVersion"] != tc.ver || fp["extVersion"] != tc.ver {
				t.Errorf("默认版本 = %v / %v，应为 %s", fp["ideVersion"], fp["extVersion"], tc.ver)
			}
			if fp["commit"] != tc.commit || fp["releaseDate"] != tc.released {
				t.Errorf("commit / releaseDate = %v / %v", fp["commit"], fp["releaseDate"])
			}
			fp = (&Client{ClientVersion: "6.0.0"}).desktopFingerprint(a)
			if fp["ideVersion"] != "6.0.0" || fp["extVersion"] != "6.0.0" || fp["commit"] != tc.commit {
				t.Errorf("配了 client_version 后 = %v / %v / %v，版本应为 6.0.0、commit 不变", fp["ideVersion"], fp["extVersion"], fp["commit"])
			}
		})
	}
}

// TestDesktopExpertChatSequence 和专家对话的对话链：建任务事件带专家四项，内置 CLI 的四个对话事件带
// expertId（chat_message_status 不带），inputLength 是真实提示词的长度。
func TestDesktopExpertChatSequence(t *testing.T) {
	e := MarketExpert{ExpertID: "ex_1", DisplayNameZH: "写作专家", Categories: []any{"expert-writing"}}
	requestID := "abcdef0123456789abcdef0123456789"
	events := DesktopExpertChatSequence(e, "conv-1", requestID, "msg-1")
	seen := map[string]DesktopEvent{}
	for _, ev := range events {
		seen[ev["eventCode"].(string)] = ev
	}
	task := seen["agent_task_created"]
	if task["has_expert"] != true || task["expert_id"] != "ex_1" || task["expert_name"] != "写作专家" ||
		task["expert_industry_id"] != "expert-writing" || task["messageId"] != "msg-1" {
		t.Errorf("建任务事件 = %v", task)
	}
	for _, code := range []string{"chat_message_send", "chat_request_send", "chat_message_response", "chat_request_response"} {
		if seen[code]["expertId"] != "ex_1" {
			t.Errorf("%s expertId = %v", code, seen[code]["expertId"])
		}
	}
	if _, ok := seen["chat_message_status"]["expertId"]; ok {
		t.Error("chat_message_status 不应带 expertId")
	}
	if seen["chat_request_send"]["inputLength"] != 12 {
		t.Errorf("inputLength = %v", seen["chat_request_send"]["inputLength"])
	}
	// 普通对话链不带 expertId。
	for _, ev := range DesktopChatSequence("conv-2", requestID, "msg-1", "m", "m") {
		if _, ok := ev["expertId"]; ok {
			t.Errorf("普通对话链 %v 不应带 expertId", ev["eventCode"])
		}
	}
}
