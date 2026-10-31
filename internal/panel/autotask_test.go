// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

// autotask_test.go 面板「一键完成」任务动作的测试。
//
// 全部上游请求经假 Transport 接住：按「方法 + 主机 + 路径 + X-Client-Platform」
// 分发到各测试登记的处理器，未登记的请求记 t.Errorf 并回 400，绝不连真实上游。
// 三个域名（chat / billing / web）在假上游里用不同主机区分——上游代码里同一路径
// /v2/report 在不同域上含义不同（桌面指纹 / 对话活跃 / 小程序 / web）。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

const (
	autoChatHost    = "chat.test"
	autoBillingHost = "billing.test"
	autoWebHost     = "web.test"
	// autoStaticHost 灵感案例静态站点（官方 static.workbuddy.cn/workbuddy/playbook）。
	autoStaticHost = "static.test"

	autoTasksPath = "/v2/activity/growth/tasks"
	// autoSubscribePath 订阅任务状态：网页拉列表前先查（失败不影响列表）。
	autoSubscribePath = "/v2/activity/growth/subscribe-task/status"
	// autoAcceptPath 网页接受任务走这条路径（不带 /v2）；小程序 AcceptTasksMP 共用。
	autoAcceptPath   = "/activity/growth/tasks/accept"
	autoReportPath   = "/v2/report"
	autoChatPath     = "/v2/chat/completions"
	autoMarketPath   = "/portal/operation-platform/market/expert/list"
	autoChatSvcToken = "0123456789abcdef0123456789abcdef"
)

// autoChatSSE 假 chat 流：DesktopChatWithExpert 从 `"id":"<32hex>"` 抓服务端 requestId。
const autoChatSSE = `data: {"id":"` + autoChatSvcToken + `","object":"chat.completion.chunk"}` + "\n\ndata: [DONE]\n\n"

// autoReq 假上游收到的请求快照。
type autoReq struct {
	Method string
	Host   string
	Path   string
	Header http.Header
	Body   []byte
}

// autoToken 取请求的账号令牌：登录码请求带 Bearer，网页业务请求带 session Cookie。
func autoToken(r autoReq) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	for _, part := range strings.Split(r.Header.Get("Cookie"), ";") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "session="); ok {
			return v
		}
	}
	return ""
}

// autoHandler 按请求给出假响应；status 0 视为 200。
type autoHandler func(autoReq) (status int, body string)

// autoUpstream 可复用的假上游。
type autoUpstream struct {
	t     *testing.T
	mu    sync.Mutex
	reqs  []autoReq
	route map[string]autoHandler
}

func autoRouteKey(method, host, path, platform string) string {
	k := method + " " + host + path
	if platform != "" {
		k += " platform=" + platform
	}
	return k
}

// RoundTrip 实现 http.RoundTripper：记录请求 → 查表 → 回预设响应。
func (f *autoUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	// 网页登录态的两个端点统一在这里答复；不记入 reqs，用例只关心业务请求。
	if resp, ok := upstreamtest.WebSessionRoundTrip(r); ok {
		return resp, nil
	}
	var raw []byte
	if r.Body != nil {
		raw, _ = io.ReadAll(r.Body)
	}
	host := r.URL.Host
	if host == "" {
		host = r.Host
	}
	req := autoReq{Method: r.Method, Host: host, Path: r.URL.Path, Header: r.Header.Clone(), Body: raw}
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	h := f.route[autoRouteKey(r.Method, host, r.URL.Path, r.Header.Get("X-Client-Platform"))]
	f.mu.Unlock()
	if h == nil {
		// 积分汇总 / 付费包 / 签到状态是每个账号维护流程都会发的伴随请求，各用例不必逐条登记。
		// 免费包（余额本体）与签到仍须显式登记，避免用例漏测。
		// 订阅任务状态是网页拉任务列表前的伴随请求，失败也不影响列表，统一回成功。
		switch r.URL.Path {
		case upstreamtest.SummaryPath, upstreamtest.PaidPath, upstreamtest.CheckinStatusPath:
			status, body, _ := upstreamtest.Respond(r.URL.Path)
			return autoHTTPResponse(status, body), nil
		case autoSubscribePath:
			return autoHTTPResponse(200, autoEnvelope("{}")), nil
		}
		f.t.Errorf("假上游收到未登记的请求: %s %s%s", r.Method, host, r.URL.Path)
		return autoHTTPResponse(400, `{"code":400,"msg":"unexpected"}`), nil
	}
	status, body := h(req)
	if status == 0 {
		status = 200
	}
	return autoHTTPResponse(status, body), nil
}

func autoHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// on 登记一条路由（platform 为空表示请求不带 X-Client-Platform）。
func (f *autoUpstream) on(method, host, path, platform string, h autoHandler) {
	f.route[autoRouteKey(method, host, path, platform)] = h
}

// 固定端点登记辅助。
func (f *autoUpstream) onDefaultTasks(h autoHandler) {
	// 默认任务列表按官网网页形状发往 webBase，带 X-Client-Platform: web。
	f.on(http.MethodGet, autoWebHost, autoTasksPath, "web", h)
}
func (f *autoUpstream) onMPTasks(h autoHandler) {
	f.on(http.MethodGet, autoWebHost, autoTasksPath, "miniprogram", h)
}
func (f *autoUpstream) onAccept(h autoHandler) {
	f.on(http.MethodPost, autoWebHost, autoAcceptPath, "web", h)
}
func (f *autoUpstream) onAcceptMP(h autoHandler) {
	f.on(http.MethodPost, autoWebHost, autoAcceptPath, "miniprogram", h)
}
func (f *autoUpstream) onBillingReport(h autoHandler) {
	f.on(http.MethodPost, autoBillingHost, autoReportPath, "", h)
}
func (f *autoUpstream) onMPReport(h autoHandler) {
	f.on(http.MethodPost, autoBillingHost, autoReportPath, "mp-weixin", h)
}
func (f *autoUpstream) onDesktopReport(h autoHandler) {
	f.on(http.MethodPost, autoChatHost, autoReportPath, "", h)
}
func (f *autoUpstream) onWebReport(h autoHandler) {
	// 网页上报照官网 sendBeacon 形状，不带 X-Client-Platform。
	f.on(http.MethodPost, autoWebHost, autoReportPath, "", h)
}
func (f *autoUpstream) onChat(h autoHandler) {
	f.on(http.MethodPost, autoChatHost, autoChatPath, "", h)
}
func (f *autoUpstream) onMarket(h autoHandler) {
	f.on(http.MethodPost, autoChatHost, autoMarketPath, "", h)
}
func (f *autoUpstream) onMPClaim(code string, h autoHandler) {
	f.on(http.MethodPost, autoWebHost, "/activity/growth/tasks/"+code+"/claim", "miniprogram", h)
}
func (f *autoUpstream) onWebClaim(code string, h autoHandler) {
	f.on(http.MethodPost, autoWebHost, "/activity/growth/tasks/"+code+"/claim", "web", h)
}

// onPlaybook 登记灵感案例静态站点：索引里第一个案例带专家，第二个才是网关会挑的 plain 案例。
func (f *autoUpstream) onPlaybook() {
	f.on(http.MethodGet, autoStaticHost, "/playbook/registry.json", "", autoOK(`{"cases":[`+
		`{"id":"with-expert","title":"带专家","categories":["business"],"experts":[{"id":"x"}]},`+
		`{"id":"pm-plan","title":"产品计划","categories":["business"],"artifact_type":"html"}]}`))
	f.on(http.MethodGet, autoStaticHost, "/playbook/categories.json", "", autoOK(`{"categories":[{"id":"business","name":"商业分析"}]}`))
	f.on(http.MethodGet, autoStaticHost, "/playbook/cases/pm-plan/case.json", "",
		autoOK(`{"id":"pm-plan","title":"产品计划","categories":["business"],"artifact_type":"html","prompt":"写一份产品计划"}`))
}

// autoScenesJSON 首页模板列表：工作模式 6 个（其中一个只在 plan 下出现），外加一个编程模式的。
const autoScenesJSON = `[{"id":47,"name":"创作文档","mode":"working"},{"id":54,"name":"日常开发","mode":"coding"},` +
	`{"id":52,"name":"金融服务","mode":"working"},{"id":62,"name":"只在规划","mode":"working","interactionModes":["plan"]},` +
	`{"id":48,"name":"分析数据","mode":"working"},{"id":49,"name":"制作PPT","mode":"working"},{"id":50,"name":"深度研究","mode":"working"}]`

// onScenes 登记首页模板列表。
func (f *autoUpstream) onScenes() {
	f.on(http.MethodGet, autoStaticHost, "/scenes/scenes-v2.json", "", autoOK(autoScenesJSON))
}

// requests 返回已记录请求的快照。
func (f *autoUpstream) requests() []autoReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]autoReq(nil), f.reqs...)
}

// requestsTo 过滤指定端点（platform 为空表示不带该头）。
func (f *autoUpstream) requestsTo(method, host, path, platform string) []autoReq {
	var out []autoReq
	for _, r := range f.requests() {
		if r.Method == method && r.Host == host && r.Path == path && r.Header.Get("X-Client-Platform") == platform {
			out = append(out, r)
		}
	}
	return out
}

// autoOK / autoFail 是常量的成功 / 失败响应。
func autoOK(body string) autoHandler {
	return func(autoReq) (int, string) { return 200, body }
}
func autoFail(status int, body string) autoHandler {
	return func(autoReq) (int, string) { return status, body }
}

// autoEnvelope 上游统一信封（code=0 成功）。
func autoEnvelope(data string) string { return `{"code":0,"data":` + data + `}` }

// autoTaskJSON 构造任务列表项（target/current 平铺，parseGrowthTasks 两种形状都认）。
func autoTaskJSON(code string, target, current int64, acceptStatus string) string {
	return fmt.Sprintf(`{"task_code":%q,"target":%d,"current":%d,"accept_status":%q}`,
		code, target, current, acceptStatus)
}

// autoTasksJSON 构造任务列表信封。
func autoTasksJSON(items ...string) string {
	return autoEnvelope(`{"tasks":[` + strings.Join(items, ",") + `]}`)
}

// autoClaimJSON 构造领奖响应。
func autoClaimJSON(credit, energy int64) string {
	return autoEnvelope(fmt.Sprintf(`{"credit":%d,"energy":%d}`, credit, energy))
}

// autoExpertJSON 构造专家市场列表项。
func autoExpertJSON(id, typ, name, profession string) string {
	return fmt.Sprintf(`{"expert_id":%q,"expert_type":%q,"display_name_zh":%q,"profession_zh":%q,"version":"1.0.0"}`,
		id, typ, name, profession)
}

// autoExpertsJSON 构造专家市场列表信封。
func autoExpertsJSON(items ...string) string {
	return autoEnvelope(`{"experts":[` + strings.Join(items, ",") + `]}`)
}

// newAutoPanel 构建单账号面板 + 假上游；三个上游域用不同主机便于断言。
func newAutoPanel(t *testing.T) (*Panel, *autoUpstream) {
	t.Helper()
	up := &autoUpstream{t: t, route: map[string]autoHandler{}}
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", Nickname: "测试号"})
	panel := New(Config{
		Version: "test",
		Pool:    p,
		Upstream: &upstream.Client{
			HTTP:          &http.Client{Transport: up},
			ChatBaseCN:    "https://" + autoChatHost,
			BillingBaseCN: "https://" + autoBillingHost,
			WebBaseCN:     "https://" + autoWebHost,
			PlaybookBase:  "https://" + autoStaticHost + "/playbook",
			ScenesBase:    "https://" + autoStaticHost + "/scenes",
		},
		AuthDir: t.TempDir(),
	})
	up.onPlaybook()
	up.onScenes()
	return panel, up
}

func autoAuth(t *testing.T, p *Panel) *auth.Auth {
	t.Helper()
	a := p.cfg.Pool.AuthByUID("u1")
	if a == nil {
		t.Fatal("测试账号不在号池")
	}
	return a
}

// autoFastWaits 把所有节流压到 0/1ms 并在测试结束还原，避免真实等待拖慢测试。
func autoFastWaits(t *testing.T) {
	t.Helper()
	oldAttempts, oldPollGap := claimPollAttempts, claimPollGap
	oldMPAction, oldMPChat, oldMPJitter := mpActionGap, mpChatEventGap, mpChatEventJitter
	oldReport, oldAppearance := reportGap, appearanceGap
	oldTemplate, oldExpert := templateUseGap, expertSummonGap
	oldAcceptBatch := acceptBatchGap
	oldNight := nightNow

	claimPollAttempts = 2
	claimPollGap = time.Millisecond
	mpActionGap = 0
	mpChatEventGap = 0
	mpChatEventJitter = 0
	reportGap = 0
	appearanceGap = 0
	templateUseGap = 0
	expertSummonGap = 0
	acceptBatchGap = 0

	t.Cleanup(func() {
		claimPollAttempts, claimPollGap = oldAttempts, oldPollGap
		mpActionGap, mpChatEventGap, mpChatEventJitter = oldMPAction, oldMPChat, oldMPJitter
		reportGap, appearanceGap = oldReport, oldAppearance
		templateUseGap, expertSummonGap = oldTemplate, oldExpert
		acceptBatchGap = oldAcceptBatch
		nightNow = oldNight
	})
}

// autoSetNight 固定夜猫子窗口判断用的时钟。
func autoSetNight(t *testing.T, now time.Time) {
	t.Helper()
	old := nightNow
	nightNow = func() time.Time { return now }
	t.Cleanup(func() { nightNow = old })
}

// autoDecode 解析 HTTP 响应体。
func autoDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v body=%s", err, rec.Body)
	}
	return out
}

// autoWantCode 断言 HTTP 状态码。
func autoWantCode(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("HTTP %d，想要 %d，body=%s", rec.Code, want, rec.Body)
	}
}

// autoErrText 取错误响应里的 error 字段。
func autoErrText(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	s, _ := autoDecode(t, rec)["error"].(string)
	return s
}

// autoEvents 解析上报体（数组），取事件列表。
func autoEvents(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var evs []map[string]any
	if err := json.Unmarshal(raw, &evs); err != nil {
		t.Fatalf("上报体不是事件数组: %v body=%s", err, raw)
	}
	return evs
}

// autoEventByCode 在事件列表里找指定 eventCode 的事件。
func autoEventByCode(t *testing.T, evs []map[string]any, code string) map[string]any {
	t.Helper()
	for _, ev := range evs {
		if ev["eventCode"] == code {
			return ev
		}
	}
	t.Fatalf("事件列表里没有 %s: %v", code, evs)
	return nil
}

// autoBodyMap 解析单个 JSON 对象请求体。
func autoBodyMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("请求体不是 JSON 对象: %v body=%s", err, raw)
	}
	return m
}

// autoSequence 请求序列的可读形式，供断言先后顺序。跳过积分汇总与付费包两条：
// 三个积分请求并发发出，它们的先后顺序不确定，断言序列会随机失败。
func autoSequence(reqs []autoReq) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		if r.Path == upstreamtest.SummaryPath || r.Path == upstreamtest.PaidPath {
			continue
		}
		out = append(out, r.Method+" "+r.Host+r.Path)
	}
	return out
}

// autoWantSequence 断言请求序列完全一致。
func autoWantSequence(t *testing.T, reqs []autoReq, want ...string) {
	t.Helper()
	got := autoSequence(reqs)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("请求序列不符\n got: %v\nwant: %v", got, want)
	}
}

// autoItems 取 auto_all 响应里的结果数组。
func autoItems(t *testing.T, resp map[string]any) []map[string]any {
	t.Helper()
	raw, ok := resp["results"].([]any)
	if !ok {
		t.Fatalf("响应没有 results 数组: %v", resp)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("results 项不是对象: %v", v)
		}
		out = append(out, m)
	}
	return out
}

// autoItem 在结果数组里按 task_code 取项。
func autoItem(t *testing.T, items []map[string]any, code string) map[string]any {
	t.Helper()
	for _, it := range items {
		if it["task_code"] == code {
			return it
		}
	}
	t.Fatalf("结果里没有 %q 项: %v", code, items)
	return nil
}

// autoWaitUnlocked 带截止时间轮询账号锁是否已释放（超时流水线结束后才放锁）。
func autoWaitUnlocked(t *testing.T, p *Panel, uid string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if p.tryLockAccount(uid) {
			p.unlockAccount(uid)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("等待账号锁释放超时")
		}
		<-time.After(time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// 单个任务 / 一键完成的 HTTP 接口
// ---------------------------------------------------------------------------

// 参数校验与互斥：不存在的账号 404、坏 body 400、未知任务 501、同账号并发 409
// （409 时不得发出任何上游请求）。
func TestAccountTaskAutoValidation(t *testing.T) {
	autoFastWaits(t)

	t.Run("uid 不在号池返回 404", func(t *testing.T) {
		p, up := newAutoPanel(t)
		rec := postJSON(t, p, "/panel/api/accounts/nope/tasks/auto", `{"task_code":"chat_5"}`)
		autoWantCode(t, rec, http.StatusNotFound)
		if n := len(up.requests()); n != 0 {
			t.Fatalf("404 不应发出上游请求，实际 %d 个", n)
		}
	})

	t.Run("body 不是 JSON 返回 400", func(t *testing.T) {
		p, _ := newAutoPanel(t)
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{broken`), http.StatusBadRequest)
	})

	t.Run("没有 task_code 返回 400", func(t *testing.T) {
		p, _ := newAutoPanel(t)
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{}`), http.StatusBadRequest)
	})

	t.Run("任务码不在动作表返回 501", func(t *testing.T) {
		p, up := newAutoPanel(t)
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"not_a_task"}`)
		autoWantCode(t, rec, http.StatusNotImplemented)
		if n := len(up.requests()); n != 0 {
			t.Fatalf("501 不应发出上游请求，实际 %d 个", n)
		}
	})

	t.Run("同账号已有动作在跑返回 409 且不发请求", func(t *testing.T) {
		p, up := newAutoPanel(t)
		if !p.tryLockAccount("u1") {
			t.Fatal("测试前置加锁失败")
		}
		defer p.unlockAccount("u1")
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"chat_5"}`)
		autoWantCode(t, rec, http.StatusConflict)
		if n := len(up.requests()); n != 0 {
			t.Fatalf("409 不应发出上游请求，实际 %d 个", n)
		}
	})

	t.Run("auto_all 的 uid 不在号池返回 404", func(t *testing.T) {
		p, up := newAutoPanel(t)
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/nope/tasks/auto_all", `{}`), http.StatusNotFound)
		if n := len(up.requests()); n != 0 {
			t.Fatalf("404 不应发出上游请求，实际 %d 个", n)
		}
	})
}

// 前置读取的三种结果：列表失败 502、账号没有该任务 404、已领取直接跳过。
func TestAccountTaskAutoListFailures(t *testing.T) {
	autoFastWaits(t)

	t.Run("查任务列表失败返回 502", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoFail(400, `{"code":400,"msg":"boom"}`))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"chat_5"}`)
		autoWantCode(t, rec, http.StatusBadGateway)
		if txt := autoErrText(t, rec); !strings.Contains(txt, "list tasks") {
			t.Fatalf("错误信息应含 list tasks，got %q", txt)
		}
	})

	t.Run("列表没有这个任务返回 404", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"chat_5"}`)
		autoWantCode(t, rec, http.StatusNotFound)
	})

	t.Run("已领取的任务直接跳过且不执行动作", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, 1, "claimed"))))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["skipped"] != true {
			t.Fatalf("应返回 skipped=true: %v", resp)
		}
		if got := len(up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")); got != 0 {
			t.Fatalf("跳过时不应上报，实际 %d 次", got)
		}
	})
}

// 动作返回 error 时是 502，错误前缀「执行失败: 」。
func TestAccountTaskAutoActionError(t *testing.T) {
	autoFastWaits(t)
	p, up := newAutoPanel(t)
	up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, 0, "accepted"))))
	up.onDesktopReport(autoFail(400, `{"code":400,"msg":"report rejected"}`))
	rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
	autoWantCode(t, rec, http.StatusBadGateway)
	if txt := autoErrText(t, rec); !strings.HasPrefix(txt, "执行失败: ") {
		t.Fatalf("错误应以「执行失败: 」开头，got %q", txt)
	}
}

// 动作成功后的回读与自动领奖（Web 口径与小程序口径）。
func TestAccountTaskAutoClaim(t *testing.T) {
	autoFastWaits(t)

	t.Run("达标领奖成功带回奖励", func(t *testing.T) {
		p, up := newAutoPanel(t)
		progressed := false
		up.onDefaultTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, cur, "accepted"))
		})
		up.onDesktopReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onWebClaim("RichMeow_Chat", autoOK(autoClaimJSON(100, 5)))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["claimed"] != true || resp["claimable"] != true {
			t.Fatalf("应 claimed+claimable: %v", resp)
		}
		if resp["credit"] != float64(100) || resp["energy"] != float64(5) {
			t.Fatalf("奖励数应原样来自领奖接口: %v", resp)
		}
		msg, _ := resp["message"].(string)
		if !strings.Contains(msg, "已自动领奖 +100 分 +5 能") {
			t.Fatalf("message 应含领奖结果: %q", msg)
		}
		claims := up.requestsTo(http.MethodPost, autoWebHost,
			"/activity/growth/tasks/RichMeow_Chat/claim", "web")
		if len(claims) != 1 {
			t.Fatalf("应走 Web 领奖一次，实际 %d 次", len(claims))
		}
	})

	t.Run("领奖成功但奖励都是 0", func(t *testing.T) {
		p, up := newAutoPanel(t)
		progressed := false
		up.onDefaultTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, cur, "accepted"))
		})
		up.onDesktopReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onWebClaim("RichMeow_Chat", autoOK(autoClaimJSON(0, 0)))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		msg, _ := resp["message"].(string)
		if resp["claimed"] != true || !strings.Contains(msg, "奖励此前已领取") {
			t.Fatalf("奖励为 0 时应提示此前已领取: %v", resp)
		}
	})

	t.Run("领奖失败仍是 200 并带 claim_error", func(t *testing.T) {
		p, up := newAutoPanel(t)
		progressed := false
		up.onDefaultTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, cur, "accepted"))
		})
		up.onDesktopReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onWebClaim("RichMeow_Chat", autoFail(400, `{"code":400,"msg":"claim failed"}`))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		msg, _ := resp["message"].(string)
		if resp["claim_error"] == nil || resp["claim_error"] == "" {
			t.Fatalf("应带 claim_error: %v", resp)
		}
		if !strings.Contains(msg, "达标但领奖失败") {
			t.Fatalf("message 应提示领奖失败: %q", msg)
		}
	})

	t.Run("回读仍不可领则没有 claimed 字段", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, 0, "accepted"))))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["claimable"] != false {
			t.Fatalf("应 claimable=false: %v", resp)
		}
		if _, ok := resp["claimed"]; ok {
			t.Fatalf("不可领时不应有 claimed 字段: %v", resp)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost,
			"/activity/growth/tasks/RichMeow_Chat/claim", "web")); got != 0 {
			t.Fatalf("不可领时不应领奖，实际 %d 次", got)
		}
	})

	t.Run("小程序任务回读与领奖走 mp 口径", func(t *testing.T) {
		p, up := newAutoPanel(t)
		reported, claimed := false, false
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(func(autoReq) (int, string) {
			if claimed {
				return 200, autoTasksJSON(autoTaskJSON("school_season", 1, 1, "claimed"))
			}
			cur := int64(0)
			if reported {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("school_season", 1, cur, "accepted"))
		})
		up.onMPReport(func(autoReq) (int, string) { reported = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("school_season", func(autoReq) (int, string) {
			claimed = true
			return 200, autoClaimJSON(100, 5)
		})
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"school_season"}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if msg, _ := resp["message"].(string); !strings.Contains(msg, "任务点亮并领取奖励（+100c +5e）") {
			t.Fatalf("动作内部应完成领奖: %v", resp)
		}
		claims := up.requestsTo(http.MethodPost, autoWebHost,
			"/activity/growth/tasks/school_season/claim", "miniprogram")
		if len(claims) != 1 {
			t.Fatalf("应走 ClaimRewardMP 一次，实际 %d 次", len(claims))
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost,
			"/activity/growth/tasks/school_season/claim", "web")); got != 0 {
			t.Fatalf("mp 任务不应走 Web 领奖，实际 %d 次", got)
		}
		if got := len(up.requestsTo(http.MethodGet, autoWebHost, autoTasksPath, "miniprogram")); got < 2 {
			t.Fatalf("动作与回读都应查小程序列表，实际 %d 次", got)
		}
		reports := up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "mp-weixin")
		if len(reports) != 1 {
			t.Fatalf("应上报一条 mp 对话事件，实际 %d", len(reports))
		}
		evs := autoEvents(t, reports[0].Body)
		if evs[0]["activityId"] != "school_open_day_2026" {
			t.Fatalf("school_season 事件应带 activityId: %v", evs[0])
		}
	})

	t.Run("回读轮询到可领", func(t *testing.T) {
		p, up := newAutoPanel(t)
		calls := 0
		up.onDefaultTasks(func(autoReq) (int, string) {
			calls++
			cur := int64(0)
			if calls >= 3 { // 第 1 次前置、第 2 次首次回读、第 3 次轮询
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, cur, "accepted"))
		})
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		up.onWebClaim("RichMeow_Chat", autoOK(autoClaimJSON(100, 5)))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["claimed"] != true {
			t.Fatalf("轮询到可领后应自动领奖: %v", resp)
		}
	})

	t.Run("轮询查询失败保留上次结果", func(t *testing.T) {
		p, up := newAutoPanel(t)
		calls := 0
		up.onDefaultTasks(func(autoReq) (int, string) {
			calls++
			if calls == 3 {
				return 400, `{"code":400,"msg":"poll boom"}`
			}
			return 200, autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, 0, "accepted"))
		})
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["claimable"] != false {
			t.Fatalf("轮询失败应保留不可领结果: %v", resp)
		}
	})

	t.Run("mp 动作回读未落定时由 handler 领奖", func(t *testing.T) {
		p, up := newAutoPanel(t)
		reported, mpQueries, claimed := false, 0, false
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(func(autoReq) (int, string) {
			if claimed {
				return 200, autoTasksJSON(autoTaskJSON("school_season", 1, 1, "claimed"))
			}
			if !reported {
				return 200, autoTasksJSON(autoTaskJSON("school_season", 1, 0, "accepted"))
			}
			mpQueries++
			// 动作内部两轮回读都还是 0；handler 的回读（第 3 次）才看到进度。
			cur := int64(0)
			if mpQueries >= 3 {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("school_season", 1, cur, "accepted"))
		})
		up.onMPReport(func(autoReq) (int, string) { reported = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("school_season", func(autoReq) (int, string) {
			claimed = true
			return 200, autoClaimJSON(100, 5)
		})
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"school_season"}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["claimed"] != true {
			t.Fatalf("handler 回读可领后应走 ClaimRewardMP: %v", resp)
		}
		msg, _ := resp["message"].(string)
		if !strings.Contains(msg, "已自动领奖 +100 分 +5 能") {
			t.Fatalf("message 应含 handler 领奖结果: %v", resp)
		}
	})
}

// 一次调用结束后锁必须释放：同账号再调一次不是 409。
func TestAccountTaskAutoUnlock(t *testing.T) {
	autoFastWaits(t)
	p, up := newAutoPanel(t)
	up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, 1, "claimed"))))
	first := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
	autoWantCode(t, first, http.StatusOK)
	second := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
	if second.Code == http.StatusConflict {
		t.Fatalf("第一次调用结束后锁应已释放，第二次不应 409: %s", second.Body)
	}
	autoWantCode(t, second, http.StatusOK)
}

// 单个任务一键完成前先接受没接受的任务（上游不给 not_accepted 的任务记进度）；接受失败照样执行
// 动作；已接受、已完成、锁定的不再接受。
func TestAccountTaskAutoAccept(t *testing.T) {
	autoFastWaits(t)

	t.Run("没接受的先接受再执行", func(t *testing.T) {
		p, up := newAutoPanel(t)
		accepted := false
		up.onDefaultTasks(func(autoReq) (int, string) {
			status := "not_accepted"
			if accepted {
				status = "accepted"
			}
			return 200, autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, 0, status))
		})
		up.onAccept(func(autoReq) (int, string) { accepted = true; return 200, autoEnvelope("{}") })
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
		autoWantCode(t, rec, http.StatusOK)
		acceptAt, reportAt := -1, -1
		for i, r := range up.requests() {
			if r.Path == autoAcceptPath && acceptAt < 0 {
				acceptAt = i
			}
			if r.Host == autoChatHost && r.Path == autoReportPath && reportAt < 0 {
				reportAt = i
			}
		}
		if acceptAt < 0 || reportAt < 0 || acceptAt > reportAt {
			t.Fatalf("应先接受再上报: accept=%d report=%d", acceptAt, reportAt)
		}
		accepts := up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "web")
		var body struct {
			TaskCodes []string `json:"task_codes"`
		}
		if err := json.Unmarshal(accepts[0].Body, &body); err != nil || len(accepts) != 1 ||
			len(body.TaskCodes) != 1 || body.TaskCodes[0] != "RichMeow_Chat" {
			t.Fatalf("应只接受这一个任务一次: %d 次 %s", len(accepts), accepts[0].Body)
		}
	})

	t.Run("接受失败照样执行动作", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, 0, "not_accepted"))))
		up.onAccept(autoFail(400, `{"code":400,"msg":"accept rejected"}`))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
		autoWantCode(t, rec, http.StatusOK)
		if got := len(up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")); got != 1 {
			t.Fatalf("接受失败后动作应照常上报一次，实际 %d", got)
		}
	})

	for _, task := range []string{
		autoTaskJSON("RichMeow_Chat", 1, 0, "accepted"),
		autoTaskJSON("RichMeow_Chat", 1, 0, "completed"),
		`{"task_code":"RichMeow_Chat","target":1,"current":0,"accept_status":"not_accepted","locked":true}`,
	} {
		t.Run("不再接受 "+task, func(t *testing.T) {
			p, up := newAutoPanel(t)
			up.onDefaultTasks(autoOK(autoTasksJSON(task)))
			up.onDesktopReport(autoOK(autoEnvelope("{}")))
			rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto", `{"task_code":"RichMeow_Chat"}`)
			autoWantCode(t, rec, http.StatusOK)
			if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "web")); got != 0 {
				t.Fatalf("不应再接受，实际 %d 次", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 一键完成 auto_all
// ---------------------------------------------------------------------------

// 默认 / 小程序两个口径的批量接受：只接受未领取、未锁定、accept_status 既非
// accepted 也非 completed 的任务码；结果第一项是 (批量接受) / (批量接受-mp)。
func TestAccountTaskAutoAllBatchAccept(t *testing.T) {
	autoFastWaits(t)

	t.Run("默认列表先批量接受", func(t *testing.T) {
		p, up := newAutoPanel(t)
		list := autoTasksJSON(
			autoTaskJSON("t_new", 1, 0, "not_accepted"),
			autoTaskJSON("t_accepted", 1, 0, "accepted"),
			autoTaskJSON("t_completed", 1, 0, "completed"),
			autoTaskJSON("t_claimed", 1, 1, "claimed"),
			`{"task_code":"t_locked","target":1,"current":0,"accept_status":"not_accepted","locked":true}`,
		)
		up.onDefaultTasks(autoOK(list))
		up.onMPTasks(autoOK(autoTasksJSON()))
		var acceptedBody []byte
		up.onAccept(func(r autoReq) (int, string) {
			acceptedBody = r.Body
			return 200, autoEnvelope("{}")
		})
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		first := items[0]
		if first["task_code"] != "(批量接受)" || first["status"] != "done" {
			t.Fatalf("第一项应是已完成的批量接受: %v", first)
		}
		if first["message"] != "已接受 1 个任务" {
			t.Fatalf("批量接受条数不对: %v", first)
		}
		var body struct {
			TaskCodes []string `json:"task_codes"`
		}
		if err := json.Unmarshal(acceptedBody, &body); err != nil {
			t.Fatalf("accept 请求体不是 JSON: %v %s", err, acceptedBody)
		}
		if len(body.TaskCodes) != 1 || body.TaskCodes[0] != "t_new" {
			t.Fatalf("只应接受 t_new，实际 %v", body.TaskCodes)
		}
		for _, it := range items {
			if it["task_code"] == "(批量接受-mp)" {
				t.Fatalf("mp 列表为空不应有 (批量接受-mp): %v", it)
			}
		}
	})

	t.Run("小程序列表单独接受", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(
			autoTaskJSON("mp_extra_task", 1, 0, "not_accepted"),
			autoTaskJSON("mp_done", 1, 0, "accepted"),
		)))
		var acceptedBody []byte
		up.onAcceptMP(func(r autoReq) (int, string) {
			acceptedBody = r.Body
			return 200, autoEnvelope("{}")
		})
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		mpItem := autoItem(t, items, "(批量接受-mp)")
		if mpItem["status"] != "done" || mpItem["message"] != "已接受 1 个小程序任务" {
			t.Fatalf("mp 批量接受结果不对: %v", mpItem)
		}
		var body struct {
			TaskCodes []string `json:"task_codes"`
		}
		if err := json.Unmarshal(acceptedBody, &body); err != nil {
			t.Fatalf("accept 请求体不是 JSON: %v %s", err, acceptedBody)
		}
		if len(body.TaskCodes) != 1 || body.TaskCodes[0] != "mp_extra_task" {
			t.Fatalf("只应接受 mp_extra_task，实际 %v", body.TaskCodes)
		}
	})

	t.Run("小程序批量接受失败记 error 项", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("mp_extra_task", 1, 0, "not_accepted"))))
		up.onAcceptMP(autoFail(400, `{"code":400,"msg":"mp accept boom"}`))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		mpItem := autoItem(t, items, "(批量接受-mp)")
		if mpItem["status"] != "error" {
			t.Fatalf("mp 批量接受失败应记 error: %v", mpItem)
		}
		if msg, _ := mpItem["message"].(string); !strings.Contains(msg, "接受小程序任务失败（不阻塞后续）") {
			t.Fatalf("失败说明不对: %v", mpItem)
		}
	})
}

// 批量接受失败只记 error 项，后续任务照常执行。
func TestAccountTaskAutoAllBatchAcceptError(t *testing.T) {
	autoFastWaits(t)
	p, up := newAutoPanel(t)
	list := autoTasksJSON(
		autoTaskJSON("t_new", 1, 0, "not_accepted"),
		autoTaskJSON("RichMeow_Chat", 1, 0, "accepted"),
	)
	up.onDefaultTasks(autoOK(list))
	up.onMPTasks(autoOK(autoTasksJSON()))
	up.onAccept(autoFail(400, `{"code":400,"msg":"accept rejected"}`))
	up.onDesktopReport(autoOK(autoEnvelope("{}")))
	rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
	autoWantCode(t, rec, http.StatusOK)
	items := autoItems(t, autoDecode(t, rec))
	first := items[0]
	if first["task_code"] != "(批量接受)" || first["status"] != "error" {
		t.Fatalf("批量接受失败应记 error 项: %v", first)
	}
	if msg, _ := first["message"].(string); !strings.Contains(msg, "接受任务失败（不阻塞后续）") {
		t.Fatalf("失败说明不对: %v", first)
	}
	rich := autoItem(t, items, "RichMeow_Chat")
	if rich["status"] != "done" {
		t.Fatalf("批量接受失败后 RichMeow_Chat 应照常执行: %v", rich)
	}
	if got := len(up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")); got != 1 {
		t.Fatalf("RichMeow_Chat 应上报一次，实际 %d", got)
	}
}

// 逐项状态机：查询失败 / 无此任务 / 已完成 / Target=0 照常执行 / 执行出错 /
// 执行后领奖成功与失败。
func TestAccountTaskAutoAllTaskStatuses(t *testing.T) {
	autoFastWaits(t)

	t.Run("某个任务查询失败后面照常执行", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMPTasks(autoOK(autoTasksJSON()))
		calls := 0
		up.onDefaultTasks(func(autoReq) (int, string) {
			calls++
			if calls == 2 { // 第 1 次是批量接受阶段，第 2 次是 chat_5 的查询
				return 400, `{"code":400,"msg":"list boom"}`
			}
			return 200, autoTasksJSON()
		})
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		bad := autoItem(t, items, "chat_5")
		if bad["status"] != "error" {
			t.Fatalf("chat_5 查询失败应为 error: %v", bad)
		}
		if msg, _ := bad["message"].(string); !strings.Contains(msg, "查询失败: ") {
			t.Fatalf("错误说明不对: %v", bad)
		}
		next := autoItem(t, items, "first_buddy")
		if next["status"] != "skipped" || next["message"] != "该账号无此任务" {
			t.Fatalf("后续任务应照常执行: %v", next)
		}
	})

	t.Run("账号没有这个任务记为 skipped", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON()))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		if len(items) != len(autoActions) {
			t.Fatalf("结果应有 %d 项，实际 %d", len(autoActions), len(items))
		}
		it := autoItem(t, items, "chat_5")
		if it["status"] != "skipped" || it["message"] != "该账号无此任务" {
			t.Fatalf("无此任务应 skipped: %v", it)
		}
	})

	t.Run("已领取或进度已达标记为已完成", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON(
			autoTaskJSON("chat_5", 5, 5, "accepted"),
			autoTaskJSON("RichMeow_Chat", 0, 0, "claimed"),
		)))
		up.onMPTasks(autoOK(autoTasksJSON()))
		up.onWebClaim("chat_5", autoOK(autoEnvelope(`{"already_claimed":true}`)))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		for _, code := range []string{"chat_5", "RichMeow_Chat"} {
			it := autoItem(t, items, code)
			if it["status"] != "skipped" {
				t.Fatalf("%s 应 skipped: %v", code, it)
			}
			if msg, _ := it["message"].(string); !strings.HasPrefix(msg, "已完成") {
				t.Fatalf("%s 完成说明不对: %v", code, it)
			}
		}
		if msg, _ := autoItem(t, items, "chat_5")["message"].(string); !strings.HasSuffix(msg, "；奖励此前已领取") {
			t.Fatalf("领过的达标任务应说明奖励此前已领取: %q", msg)
		}
		if got := up.requestsTo(http.MethodPost, autoWebHost, "/activity/growth/tasks/RichMeow_Chat/claim", "web"); len(got) != 0 {
			t.Fatalf("已领取的任务不该再领奖，实际 %d 次", len(got))
		}
	})

	t.Run("已达标未领先补领再跳过", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("chat_5", 5, 5, "accepted"))))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("Sequential_Tasks_1", 1, 1, "accepted"))))
		up.onWebClaim("chat_5", autoOK(autoClaimJSON(100, 5)))
		up.onMPClaim("Sequential_Tasks_1", autoFail(500, `{"code":500,"msg":"claim boom"}`))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))

		it := autoItem(t, items, "chat_5")
		if it["status"] != "skipped" || it["claimed"] != true || it["credit"] != float64(100) || it["energy"] != float64(5) {
			t.Fatalf("达标未领应补领并记下数额: %v", it)
		}
		if msg, _ := it["message"].(string); !strings.HasSuffix(msg, "；已补领 +100 分 +5 能") {
			t.Fatalf("补领说明不对: %q", msg)
		}
		if got := up.requestsTo(http.MethodPost, autoWebHost, "/activity/growth/tasks/chat_5/claim", "web"); len(got) != 1 {
			t.Fatalf("chat_5 应领奖一次，实际 %d 次", len(got))
		}

		mp := autoItem(t, items, "Sequential_Tasks_1")
		if mp["status"] != "skipped" || mp["claimed"] != nil {
			t.Fatalf("补领失败仍记 skipped、不记 claimed: %v", mp)
		}
		if e, _ := mp["claim_error"].(string); !strings.Contains(e, "claim boom") {
			t.Fatalf("补领失败要带上错误: %v", mp)
		}
		if msg, _ := mp["message"].(string); !strings.HasSuffix(msg, "；补领失败（可在列表手动重试）") {
			t.Fatalf("补领失败说明不对: %q", msg)
		}
	})

	t.Run("Target=0 且未领取照常执行", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("RichMeow_Chat", 0, 0, "accepted"))))
		up.onMPTasks(autoOK(autoTasksJSON()))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		it := autoItem(t, items, "RichMeow_Chat")
		if it["status"] != "done" {
			t.Fatalf("Target=0 未领取应照常执行: %v", it)
		}
		if got := len(up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")); got != 1 {
			t.Fatalf("应上报一次，实际 %d", got)
		}
	})

	t.Run("某个任务执行出错其余继续", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON(
			autoTaskJSON("RichMeow_Chat", 1, 0, "accepted"),
			autoTaskJSON("Library_read", 1, 0, "accepted"),
		)))
		up.onMPTasks(autoOK(autoTasksJSON()))
		up.onDesktopReport(autoFail(400, `{"code":400,"msg":"desktop boom"}`))
		up.onWebReport(autoOK(autoEnvelope("{}")))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		if it := autoItem(t, items, "RichMeow_Chat"); it["status"] != "error" {
			t.Fatalf("RichMeow_Chat 应 error: %v", it)
		}
		if it := autoItem(t, items, "Library_read"); it["status"] != "done" {
			t.Fatalf("后续 Library_read 应继续执行: %v", it)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoReportPath, "")); got != 1 {
			t.Fatalf("Library_read 应上报一次，实际 %d", got)
		}
	})

	t.Run("执行后达标领奖成功", func(t *testing.T) {
		p, up := newAutoPanel(t)
		progressed := false
		up.onDefaultTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, cur, "accepted"))
		})
		up.onMPTasks(autoOK(autoTasksJSON()))
		up.onDesktopReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onWebClaim("RichMeow_Chat", autoOK(autoClaimJSON(100, 5)))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		it := autoItem(t, items, "RichMeow_Chat")
		if it["status"] != "done" || it["claimed"] != true {
			t.Fatalf("应 done+claimed: %v", it)
		}
		if it["credit"] != float64(100) || it["energy"] != float64(5) {
			t.Fatalf("奖励数不对: %v", it)
		}
		if msg, _ := it["message"].(string); !strings.Contains(msg, "已自动领奖 +100 分 +5 能") {
			t.Fatalf("message 应含领奖结果: %v", it)
		}
	})

	t.Run("执行后达标领奖失败仍为 done", func(t *testing.T) {
		p, up := newAutoPanel(t)
		progressed := false
		up.onDefaultTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, cur, "accepted"))
		})
		up.onMPTasks(autoOK(autoTasksJSON()))
		up.onDesktopReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onWebClaim("RichMeow_Chat", autoFail(400, `{"code":400,"msg":"claim boom"}`))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		it := autoItem(t, items, "RichMeow_Chat")
		if it["status"] != "done" {
			t.Fatalf("领奖失败不应掩盖主流程: %v", it)
		}
		if it["claim_error"] == nil || it["claim_error"] == "" {
			t.Fatalf("应带 claim_error: %v", it)
		}
		if msg, _ := it["message"].(string); !strings.Contains(msg, "达标但领奖失败") {
			t.Fatalf("message 应提示领奖失败: %v", it)
		}
	})

	t.Run("动作成功后回读失败进度记问号", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMPTasks(autoOK(autoTasksJSON()))
		calls := 0
		up.onDefaultTasks(func(autoReq) (int, string) {
			calls++
			if calls == 6 { // 批量接受 1 次 + 前三个动作查询 3 次 + RichMeow 前置 1 次，第 6 次是回读
				return 400, `{"code":400,"msg":"readback boom"}`
			}
			return 200, autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, 0, "accepted"))
		})
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		it := autoItem(t, items, "RichMeow_Chat")
		if it["status"] != "done" {
			t.Fatalf("动作本身成功应记 done: %v", it)
		}
		if it["progress_after"] != "?" {
			t.Fatalf("回读失败时进度应为问号: %v", it)
		}
		if _, ok := it["claimed"]; ok {
			t.Fatalf("回读失败不应领奖: %v", it)
		}
	})

	t.Run("mp 任务由 handler 回读领奖", func(t *testing.T) {
		p, up := newAutoPanel(t)
		reported, mpQueries, claimed := false, 0, false
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(func(autoReq) (int, string) {
			if claimed {
				return 200, autoTasksJSON(autoTaskJSON("school_season", 1, 1, "claimed"))
			}
			if !reported {
				return 200, autoTasksJSON(autoTaskJSON("school_season", 1, 0, "accepted"))
			}
			mpQueries++
			cur := int64(0)
			if mpQueries >= 3 {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("school_season", 1, cur, "accepted"))
		})
		up.onMPReport(func(autoReq) (int, string) { reported = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("school_season", func(autoReq) (int, string) {
			claimed = true
			return 200, autoClaimJSON(100, 5)
		})
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		it := autoItem(t, items, "school_season")
		if it["status"] != "done" || it["claimed"] != true {
			t.Fatalf("mp 任务应由 handler 走 ClaimRewardMP 领奖: %v", it)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost,
			"/activity/growth/tasks/school_season/claim", "miniprogram")); got != 1 {
			t.Fatalf("应走 ClaimRewardMP 一次，实际 %d", got)
		}
	})

	t.Run("领奖返回零奖励提示此前已领取", func(t *testing.T) {
		p, up := newAutoPanel(t)
		progressed := false
		up.onDefaultTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("RichMeow_Chat", 1, cur, "accepted"))
		})
		up.onMPTasks(autoOK(autoTasksJSON()))
		up.onDesktopReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onWebClaim("RichMeow_Chat", autoOK(autoClaimJSON(0, 0)))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		items := autoItems(t, autoDecode(t, rec))
		it := autoItem(t, items, "RichMeow_Chat")
		if it["claimed"] != true {
			t.Fatalf("零奖励也算领奖成功: %v", it)
		}
		if msg, _ := it["message"].(string); !strings.Contains(msg, "奖励此前已领取") {
			t.Fatalf("message 应提示此前已领取: %v", it)
		}
	})
}

// 同账号已有动作在跑时 auto_all 直接 409。
func TestAccountTaskAutoAllLocked(t *testing.T) {
	autoFastWaits(t)
	p, up := newAutoPanel(t)
	if !p.tryLockAccount("u1") {
		t.Fatal("测试前置加锁失败")
	}
	defer p.unlockAccount("u1")
	rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
	autoWantCode(t, rec, http.StatusConflict)
	if n := len(up.requests()); n != 0 {
		t.Fatalf("409 不应发出上游请求，实际 %d 个", n)
	}
}

// 超时：HTTP 侧 504，但流水线仍在后台跑、锁不释放（再调 409）；
// 放行后台请求并等流水线结束后，锁恢复可用（再调不是 409）。
func TestAccountTaskAutoAllTimeout(t *testing.T) {
	autoFastWaits(t)
	old := autoAllTimeout
	autoAllTimeout = 30 * time.Millisecond
	t.Cleanup(func() { autoAllTimeout = old })

	p, up := newAutoPanel(t)
	release := make(chan struct{})
	up.onDefaultTasks(func(autoReq) (int, string) {
		<-release
		return 200, autoTasksJSON()
	})
	up.onMPTasks(func(autoReq) (int, string) {
		<-release
		return 200, autoTasksJSON()
	})

	rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
	autoWantCode(t, rec, http.StatusGatewayTimeout)

	again := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
	autoWantCode(t, again, http.StatusConflict)

	close(release)
	autoWaitUnlocked(t, p, "u1")

	// 这次要完整跑完流水线，换回正常的等待上限，免得慢机器上也超时。
	autoAllTimeout = old
	done := postJSON(t, p, "/panel/api/accounts/u1/tasks/auto_all", `{}`)
	if done.Code == http.StatusConflict {
		t.Fatalf("流水线结束后锁应已释放，不应 409: %s", done.Body)
	}
	autoWantCode(t, done, http.StatusOK)
}

// ---------------------------------------------------------------------------
// 各动作函数
// ---------------------------------------------------------------------------

// chat_5：按差额补报、单条失败不报错、已达标与任务缺失。
func TestRunChat5(t *testing.T) {
	autoFastWaits(t)

	t.Run("按差额补报三条", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("chat_5", 5, 2, "accepted"))))
		up.onBillingReport(autoOK(autoEnvelope("{}")))
		msg, err := runChat5(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if msg != "已补报 3 条对话事件" {
			t.Fatalf("message 不对: %q", msg)
		}
		reports := up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "")
		if len(reports) != 3 {
			t.Fatalf("应补报 3 条，实际 %d", len(reports))
		}
		seen := map[string]bool{}
		for _, r := range reports {
			evs := autoEvents(t, r.Body)
			if len(evs) != 1 || evs[0]["eventCode"] != "chat_request_send" {
				t.Fatalf("上报事件形状不对: %v", evs)
			}
			cid, _ := evs[0]["conversationId"].(string)
			if !upstreamtest.UUIDv7.MatchString(cid) {
				t.Fatalf("conversationId 应是官方会话 id 的形状: %q", cid)
			}
			if seen[cid] {
				t.Fatalf("conversationId 重复: %q", cid)
			}
			seen[cid] = true
		}
		// 先查订阅任务状态与任务列表，再逐条上报。
		all := up.requests()
		if len(all) != 5 || all[0].Path != autoSubscribePath || all[1].Path != autoTasksPath {
			t.Fatalf("请求顺序不对: %v", autoSequence(all))
		}
	})

	t.Run("单条上报失败返回提示不报错", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("chat_5", 5, 4, "accepted"))))
		up.onBillingReport(autoFail(400, `{"code":400,"msg":"report boom"}`))
		msg, err := runChat5(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("上报失败不应返回 error: %v", err)
		}
		if !strings.HasPrefix(msg, "上报第 1/1 条失败: ") {
			t.Fatalf("message 不对: %q", msg)
		}
	})

	t.Run("进度已达标无需上报", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("chat_5", 5, 5, "accepted"))))
		msg, err := runChat5(p, autoAuth(t, p))
		if err != nil || msg != "进度已达标，无需上报" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		// 订阅任务状态 + 任务列表各一次。
		if n := len(up.requests()); n != 2 {
			t.Fatalf("只应查一次列表，实际 %d 个请求", n)
		}
	})

	t.Run("target=0 时兜底补五条", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("chat_5", 0, 0, "accepted"))))
		up.onBillingReport(autoOK(autoEnvelope("{}")))
		msg, err := runChat5(p, autoAuth(t, p))
		if err != nil || msg != "已补报 5 条对话事件" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "")); got != 5 {
			t.Fatalf("target=0 应兜底补 5 条，实际 %d", got)
		}
	})

	t.Run("任务不存在", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		if _, err := runChat5(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "任务不存在") {
			t.Fatalf("应返回任务不存在: %v", err)
		}
	})

	t.Run("列表失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoFail(400, `{"code":400}`))
		if _, err := runChat5(p, autoAuth(t, p)); err == nil {
			t.Fatal("列表失败应返回 error")
		}
	})
}

// first_buddy：上报 → 同意协议 → 领养；门槛未过返回提示而非错误。
func TestRunFirstBuddy(t *testing.T) {
	autoFastWaits(t)

	agreementPath := "/activity/growth/buddy/agreement"
	firstPath := "/activity/growth/buddy/first"

	t.Run("成功链路顺序", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onBillingReport(autoOK(autoEnvelope("{}")))
		up.on(http.MethodPost, autoWebHost, agreementPath, "web", autoOK(autoEnvelope("{}")))
		up.on(http.MethodPost, autoWebHost, firstPath, "web", autoOK(autoEnvelope("{}")))
		msg, err := runFirstBuddy(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if !strings.Contains(msg, "已领取 Buddy") {
			t.Fatalf("message 不对: %q", msg)
		}
		autoWantSequence(t, up.requests(),
			"POST "+autoBillingHost+autoReportPath,
			"POST "+autoWebHost+agreementPath,
			"POST "+autoWebHost+firstPath,
		)
		agree := up.requestsTo(http.MethodPost, autoWebHost, agreementPath, "web")
		if len(agree) != 1 || autoBodyMap(t, agree[0].Body)["agree"] != true {
			t.Fatalf("同意协议请求体不对: %s", agree)
		}
	})

	t.Run("前置上报失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onBillingReport(autoFail(400, `{"code":400}`))
		if _, err := runFirstBuddy(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "前置上报") {
			t.Fatalf("应返回前置上报错误: %v", err)
		}
	})

	t.Run("同意协议失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onBillingReport(autoOK(autoEnvelope("{}")))
		up.on(http.MethodPost, autoWebHost, agreementPath, "web", autoFail(400, `{"code":400}`))
		if _, err := runFirstBuddy(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "同意协议") {
			t.Fatalf("应返回同意协议错误: %v", err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, firstPath, "web")); got != 0 {
			t.Fatalf("协议失败后不应领养，实际 %d 次", got)
		}
	})

	t.Run("门槛未过返回提示", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onBillingReport(autoOK(autoEnvelope("{}")))
		up.on(http.MethodPost, autoWebHost, agreementPath, "web", autoOK(autoEnvelope("{}")))
		up.on(http.MethodPost, autoWebHost, firstPath, "web",
			autoFail(400, `{"code":400,"msg":"first_buddy task not completed yet"}`))
		msg, err := runFirstBuddy(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("门槛未过不应返回 error: %v", err)
		}
		if !strings.Contains(msg, "领养门槛未过") {
			t.Fatalf("message 不对: %q", msg)
		}
	})

	t.Run("领取失败返回错误", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onBillingReport(autoOK(autoEnvelope("{}")))
		up.on(http.MethodPost, autoWebHost, agreementPath, "web", autoOK(autoEnvelope("{}")))
		up.on(http.MethodPost, autoWebHost, firstPath, "web", autoFail(500, `{"code":500}`))
		if _, err := runFirstBuddy(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "领取 Buddy") {
			t.Fatalf("应返回领取 Buddy 错误: %v", err)
		}
	})
}

// Model_chat_GLM5.2：accept → 真实对话 → 模型对齐上报；accept 失败不阻塞，
// 对话失败直接报错，上报失败只返回提示。
func TestRunModelChat(t *testing.T) {
	autoFastWaits(t)

	t.Run("成功链路顺序与模型字段", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onAccept(autoOK(autoEnvelope("{}")))
		up.onChat(autoOK(autoChatSSE))
		up.onBillingReport(autoOK(autoEnvelope("{}")))
		msg, err := runModelChat(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if msg != "已完成 glm-5.2 对话并上报" {
			t.Fatalf("message 不对: %q", msg)
		}
		autoWantSequence(t, up.requests(),
			"POST "+autoWebHost+autoAcceptPath,
			"POST "+autoChatHost+autoChatPath,
			"POST "+autoBillingHost+autoReportPath,
		)
		chat := up.requestsTo(http.MethodPost, autoChatHost, autoChatPath, "")
		if model := autoBodyMap(t, chat[0].Body)["model"]; model != "glm-5.2" {
			t.Fatalf("对话模型应为 glm-5.2，got %v", model)
		}
		report := up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "")
		evs := autoEvents(t, report[0].Body)
		if evs[0]["requestModelId"] != "glm-5.2" || evs[0]["requestModelName"] != "GLM-5.2" {
			t.Fatalf("上报模型字段未对齐: %v", evs[0])
		}
	})

	t.Run("accept 失败仍继续走行为链路", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onAccept(autoFail(400, `{"code":400}`))
		up.onChat(autoOK(autoChatSSE))
		up.onBillingReport(autoOK(autoEnvelope("{}")))
		msg, err := runModelChat(p, autoAuth(t, p))
		if err != nil || msg != "已完成 glm-5.2 对话并上报" {
			t.Fatalf("accept 失败不应阻塞: msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "web")); got != 1 {
			t.Fatalf("应尝试 accept 一次，实际 %d", got)
		}
	})

	t.Run("对话失败返回错误", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onAccept(autoOK(autoEnvelope("{}")))
		up.onChat(autoFail(400, `{"code":400,"msg":"chat boom"}`))
		_, err := runModelChat(p, autoAuth(t, p))
		if err == nil || !strings.Contains(err.Error(), "对话请求") || !strings.Contains(err.Error(), "http 400") {
			t.Fatalf("应返回对话请求错误: %v", err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "")); got != 0 {
			t.Fatalf("对话失败不应上报，实际 %d 次", got)
		}
	})

	t.Run("对话失败短响应", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onAccept(autoOK(autoEnvelope("{}")))
		up.onChat(autoFail(400, `bad`))
		if _, err := runModelChat(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "bad") {
			t.Fatalf("短响应应原样带出: %v", err)
		}
	})

	t.Run("进度上报失败返回提示", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onAccept(autoOK(autoEnvelope("{}")))
		up.onChat(autoOK(autoChatSSE))
		up.onBillingReport(autoFail(400, `{"code":400}`))
		msg, err := runModelChat(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("上报失败不应返回 error: %v", err)
		}
		if !strings.Contains(msg, "进度上报失败") {
			t.Fatalf("message 不对: %q", msg)
		}
	})
}

// 单次桌面指纹 / web 指纹上报类动作：成功断言事件形状，失败断言返回 error。
func TestRunDesktopReportActions(t *testing.T) {
	autoFastWaits(t)

	cases := []struct {
		name     string
		run      func(*Panel, *auth.Auth) (string, error)
		host     string
		path     string
		platform string
		events   []string
		msg      string
	}{
		{"rich_meow", runRichMeow, autoChatHost, autoReportPath, "", []string{"agent_task_created", "chat_message_response"}, "桌面端指纹"},
		{"buddy_app", runBuddyApp, autoChatHost, autoReportPath, "", []string{"buddyapp_discover_click", "buddyapp_show", "buddyapp_enter_click", "buddyapp_auth_confirm_click", "buddyapp_bindaccount_skip_click"}, "buddyapp"},
		{"automation", runAutomationCreate, autoChatHost, autoReportPath, "", []string{"automated_task_create_suc"}, "定时任务创建事件"},
		{"library_read", runLibraryRead, autoWebHost, autoReportPath, "", []string{"web_element_click"}, "资料库介绍"},
		{"playbook_prompt", runPlaybookPrompt, autoChatHost, autoReportPath, "", []string{"playbook_cta_click", "playbook_prompt_send"}, "playbook_cta_click"},
		{"create_canvas", runCreateCanvas, autoChatHost, autoReportPath, "", []string{"wbx_design_canvas_task_create", "wbx_design_canvas_open"}, "wbx_design_canvas"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"成功", func(t *testing.T) {
			p, up := newAutoPanel(t)
			up.on(http.MethodPost, tc.host, tc.path, tc.platform, autoOK(autoEnvelope("{}")))
			msg, err := tc.run(p, autoAuth(t, p))
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if !strings.Contains(msg, tc.msg) {
				t.Fatalf("message 应含 %q，got %q", tc.msg, msg)
			}
			reqs := up.requestsTo(http.MethodPost, tc.host, tc.path, tc.platform)
			if len(reqs) != 1 {
				t.Fatalf("应上报一次，实际 %d", len(reqs))
			}
			evs := autoEvents(t, reqs[0].Body)
			for _, code := range tc.events {
				autoEventByCode(t, evs, code)
			}
		})
		t.Run(tc.name+"失败", func(t *testing.T) {
			p, up := newAutoPanel(t)
			up.on(http.MethodPost, tc.host, tc.path, tc.platform, autoFail(400, `{"code":400}`))
			if _, err := tc.run(p, autoAuth(t, p)); err == nil {
				t.Fatal("上报失败应返回 error")
			}
		})
	}
}

// playbook_prompt：案例从静态站点现拉，事件里是它的真实字段；站点拉不到就不上报。
func TestRunPlaybookPrompt(t *testing.T) {
	autoFastWaits(t)

	t.Run("字段取真实案例", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		msg, err := runPlaybookPrompt(p, autoAuth(t, p))
		if err != nil || !strings.Contains(msg, "产品计划") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		evs := autoEvents(t, up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")[0].Body)
		cta := autoEventByCode(t, evs, "playbook_cta_click")
		if cta["id"] != "pm-plan" || cta["type"] != "html" || cta["categoryName"] != "商业分析" ||
			cta["position"] != float64(1) || cta["source"] != "scene" {
			t.Fatalf("playbook_cta_click = %v", cta)
		}
		send := autoEventByCode(t, evs, "playbook_prompt_send")
		if send["promptLength"] != float64(7) || send["ext1"] != "discover" || send["queryId"] != "pm-plan" {
			t.Fatalf("playbook_prompt_send = %v", send)
		}
		if !upstreamtest.UUIDv7.MatchString(send["conversationId"].(string)) ||
			!upstreamtest.UUIDv7Hex.MatchString(send["requestId"].(string)) {
			t.Fatalf("会话 / 轮次 id 形状不对: %v", send)
		}
	})

	t.Run("站点拉不到不上报", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.on(http.MethodGet, autoStaticHost, "/playbook/registry.json", "", autoFail(503, "busy"))
		if _, err := runPlaybookPrompt(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "灵感案例") {
			t.Fatalf("err = %v", err)
		}
		if n := len(up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")); n != 0 {
			t.Fatalf("不应上报，实际 %d 次", n)
		}
	})
}

// template_5：现拉首页模板列表，取工作模式下 craft 能看到的前 5 个，按列表顺序各报一组；第 N 组失败
// 返回说明文字但不报错；模板列表拉不到不上报。
func TestRunTemplateUse(t *testing.T) {
	autoFastWaits(t)

	t.Run("五组模板按序上报", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		msg, err := runTemplateUse(p, autoAuth(t, p))
		if err != nil || msg != "已上报 template_used ×5" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		reqs := up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")
		if len(reqs) != 5 {
			t.Fatalf("应上报 5 组，实际 %d", len(reqs))
		}
		want := []struct {
			id   float64
			name string
		}{{47, "创作文档"}, {52, "金融服务"}, {48, "分析数据"}, {49, "制作PPT"}, {50, "深度研究"}}
		for i, r := range reqs {
			evs := autoEvents(t, r.Body)
			ev := autoEventByCode(t, evs, "template_used")
			if ev["template_id"] != want[i].id || ev["template_name"] != want[i].name {
				t.Fatalf("第 %d 组模板不对: %v", i+1, ev)
			}
			withTpl := autoEventByCode(t, evs, "agent_task_created_with_template")
			if withTpl["id"] != strconv.Itoa(int(want[i].id)) {
				t.Fatalf("第 %d 组建任务模板 id 不对: %v", i+1, withTpl)
			}
		}
	})

	t.Run("模板列表拉不到不上报", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.on(http.MethodGet, autoStaticHost, "/scenes/scenes-v2.json", "", autoFail(502, "bad"))
		if _, err := runTemplateUse(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "首页模板") {
			t.Fatalf("err = %v", err)
		}
		if n := len(up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")); n != 0 {
			t.Fatalf("不应上报，实际 %d 次", n)
		}
	})

	t.Run("第二组失败返回说明", func(t *testing.T) {
		p, up := newAutoPanel(t)
		n := 0
		up.onDesktopReport(func(autoReq) (int, string) {
			n++
			if n == 2 {
				return 400, `{"code":400,"msg":"tpl boom"}`
			}
			return 200, autoEnvelope("{}")
		})
		msg, err := runTemplateUse(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("不应返回 error: %v", err)
		}
		if !strings.HasPrefix(msg, "第 2 组模板事件上报失败: ") {
			t.Fatalf("message 不对: %q", msg)
		}
		if got := len(up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")); got != 2 {
			t.Fatalf("失败后应停止，实际上报 %d 次", got)
		}
	})
}

// Hp_Appearance：照官方外观设置页，先拉主题列表、取账号版本档，再 set 主题、上报皮肤生效事件。
func TestRunAppearance(t *testing.T) {
	autoFastWaits(t)
	setPath := "/v2/user-asset/appearance/set"
	listPath := "/v2/operation-platform/appearance/resources"
	themes := autoEnvelope(`{"resources":[` +
		`{"id":"theme-tkbera","kind":"theme","vip_level":"free","series":"craft"},` +
		`{"id":"theme-tkmw7j","kind":"theme","vip_level":"standard","series":"collection"}]}`)
	// 免费包里有一个专业版试用包：版本档应为 pro_trial。
	trial := upstreamtest.List(upstreamtest.Package(100, 50, `"PackageCode":"TCACA_code_039_KRcQj7wUat"`))
	ready := func(up *autoUpstream) {
		up.on(http.MethodPost, autoChatHost, listPath, "", autoOK(themes))
		up.on(http.MethodPost, autoBillingHost, upstreamtest.FreePath, "", autoOK(trial))
		up.on(http.MethodPost, autoChatHost, setPath, "", autoOK(autoEnvelope("{}")))
	}
	chatSeq := func(up *autoUpstream) []string {
		var out []string
		for _, r := range up.requests() {
			if r.Host == autoChatHost {
				out = append(out, r.Method+" "+r.Path)
			}
		}
		return out
	}
	noSet := func(t *testing.T, up *autoUpstream) {
		t.Helper()
		if got := len(up.requestsTo(http.MethodPost, autoChatHost, setPath, "")); got != 0 {
			t.Fatalf("不应设置主题，实际 %d 次", got)
		}
	}

	t.Run("设置主题后上报生效事件", func(t *testing.T) {
		p, up := newAutoPanel(t)
		ready(up)
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		msg, err := runAppearance(p, autoAuth(t, p))
		if err != nil || !strings.Contains(msg, "已设置主题") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		want := []string{"POST " + listPath, "POST " + setPath, "POST " + autoReportPath}
		if got := chatSeq(up); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("请求顺序不对\n got: %v\nwant: %v", got, want)
		}
		list := autoBodyMap(t, up.requestsTo(http.MethodPost, autoChatHost, listPath, "")[0].Body)
		if list["platform"] != "client" || list["kind"] != "theme" || list["lang"] != "zh" || list["version"] == "" {
			t.Fatalf("主题列表请求体不对: %v", list)
		}
		body := autoBodyMap(t, up.requestsTo(http.MethodPost, autoChatHost, setPath, "")[0].Body)
		if body["kind"] != "theme" || body["resource_key"] != "theme-tkmw7j" {
			t.Fatalf("set 请求体不对: %v", body)
		}
		reports := up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")
		ev := autoEventByCode(t, autoEvents(t, reports[0].Body), "appearance_skin_apply")
		if ev["id"] != "theme-tkmw7j" || ev["action"] != "apply" || ev["source"] != "settings_close" {
			t.Fatalf("皮肤生效事件不对: %v", ev)
		}
		// vip_level standard 照官方对照表换成 pro；series 与 type 取列表和版本档的值。
		if ev["vipLevel"] != "pro" || ev["series"] != "collection" || ev["type"] != "pro_trial" {
			t.Fatalf("皮肤生效事件类型字段不对: %v", ev)
		}
	})

	t.Run("主题列表失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.on(http.MethodPost, autoChatHost, listPath, "", autoFail(400, `{"code":400}`))
		if _, err := runAppearance(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "主题列表") {
			t.Fatalf("应返回主题列表错误: %v", err)
		}
		noSet(t, up)
	})

	t.Run("主题已下架", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.on(http.MethodPost, autoChatHost, listPath, "", autoOK(autoEnvelope(
			`{"resources":[{"id":"theme-tkbera","kind":"theme","vip_level":"free","series":"craft"},`+
				`{"id":"theme-tkmw7j","kind":"color","vip_level":"free"}]}`)))
		if _, err := runAppearance(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "可能已下架") {
			t.Fatalf("应返回主题已下架: %v", err)
		}
		noSet(t, up)
	})

	t.Run("账号版本查不到", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.on(http.MethodPost, autoChatHost, listPath, "", autoOK(themes))
		up.on(http.MethodPost, autoBillingHost, upstreamtest.FreePath, "", autoFail(400, `{"code":400}`))
		if _, err := runAppearance(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "账号版本") {
			t.Fatalf("应返回账号版本错误: %v", err)
		}
		noSet(t, up)
	})

	t.Run("设置主题失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		ready(up)
		up.on(http.MethodPost, autoChatHost, setPath, "", autoFail(400, `{"code":400}`))
		if _, err := runAppearance(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "设置主题") {
			t.Fatalf("应返回设置主题错误: %v", err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")); got != 0 {
			t.Fatalf("设置失败不应上报事件，实际 %d", got)
		}
	})

	t.Run("上报事件失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		ready(up)
		up.onDesktopReport(autoFail(400, `{"code":400}`))
		if _, err := runAppearance(p, autoAuth(t, p)); err == nil {
			t.Fatal("事件上报失败应返回 error")
		}
	})
}

// 专家召唤 + 使用链：列表为空报错、不足数量只做到够数、单个专家失败继续。
func TestRunExpertBatch(t *testing.T) {
	autoFastWaits(t)

	agents := autoExpertsJSON(
		autoExpertJSON("ex_1", "agent", "专家一", "职业一"),
		autoExpertJSON("ex_2", "agent", "专家二", "职业二"),
		autoExpertJSON("ex_3", "agent", "专家三", "职业三"),
		autoExpertJSON("ex_4", "agent", "专家四", "职业四"),
		autoExpertJSON("ex_5", "agent", "专家五", "职业五"),
	)

	t.Run("五位专家各走召唤+对话+使用", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoOK(agents))
		up.onChat(autoOK(autoChatSSE))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		msg, err := runExpertUse(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if !strings.Contains(msg, "已对 5 位真实专家") {
			t.Fatalf("message 不对: %q", msg)
		}
		// 每位专家：召唤上报 → 真实 chat → 使用上报，共 15 个请求。
		var want []string
		for i := 0; i < 5; i++ {
			want = append(want,
				"POST "+autoChatHost+autoReportPath,
				"POST "+autoChatHost+autoChatPath,
				"POST "+autoChatHost+autoReportPath,
			)
		}
		autoWantSequence(t, up.requests()[1:], want...)
		reports := up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")
		if len(reports) != 10 {
			t.Fatalf("应 5 次召唤 + 5 次使用，实际 %d", len(reports))
		}
		autoEventByCode(t, autoEvents(t, reports[0].Body), "expert_summon_click")
		use := autoEventByCode(t, autoEvents(t, reports[1].Body), "expert_actual_use")
		if use["requestId"] != autoChatSvcToken {
			t.Fatalf("使用事件应 JOIN 服务端 requestId: %v", use)
		}
		// 同一批里的对话链带这位专家（官方会话有专家时的形状）。
		task := autoEventByCode(t, autoEvents(t, reports[1].Body), "agent_task_created")
		send := autoEventByCode(t, autoEvents(t, reports[1].Body), "chat_request_send")
		if task["has_expert"] != true || task["expert_id"] != use["id"] || send["expertId"] != use["id"] {
			t.Fatalf("对话链应带专家: task=%v send=%v", task, send)
		}
		market := up.requestsTo(http.MethodPost, autoChatHost, autoMarketPath, "")
		if got := autoBodyMap(t, market[0].Body)["expert_type"]; got != "agent" {
			t.Fatalf("专家列表 expert_type 应为 agent，got %v", got)
		}
	})

	t.Run("专家团只做到三个", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoOK(autoExpertsJSON(
			autoExpertJSON("tm_1", "team", "团一", "团一"),
			autoExpertJSON("tm_2", "team", "团二", "团二"),
			autoExpertJSON("tm_3", "team", "团三", "团三"),
			autoExpertJSON("tm_4", "team", "团四", "团四"),
		)))
		up.onChat(autoOK(autoChatSSE))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		msg, err := runExpertTeamUse(p, autoAuth(t, p))
		if err != nil || !strings.Contains(msg, "已对 3 位真实专家") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoChatHost, autoChatPath, "")); got != 3 {
			t.Fatalf("只应使用 3 位专家，实际 %d 次对话", got)
		}
	})

	t.Run("列表为空报错", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoOK(autoExpertsJSON()))
		if _, err := runExpertUse(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "专家市场列表为空") {
			t.Fatalf("应返回列表为空: %v", err)
		}
	})

	t.Run("列表失败报错", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoFail(400, `{"code":400}`))
		if _, err := runExpertUse(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "拉取专家列表") {
			t.Fatalf("应返回拉取失败: %v", err)
		}
	})

	t.Run("单个专家失败后继续", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoOK(autoExpertsJSON(
			autoExpertJSON("ex_1", "agent", "专家一", "职业一"),
			autoExpertJSON("ex_2", "agent", "专家二", "职业二"),
		)))
		n := 0
		up.onChat(func(autoReq) (int, string) {
			n++
			if n == 1 {
				return 400, `{"code":400,"msg":"chat boom"}`
			}
			return 200, autoChatSSE
		})
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		msg, err := runExpertUse(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if !strings.Contains(msg, "已对 1 位真实专家") {
			t.Fatalf("失败一位后应只成功一位: %q", msg)
		}
	})

	t.Run("召唤与使用上报失败后继续", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoOK(autoExpertsJSON(
			autoExpertJSON("ex_1", "agent", "专家一", "职业一"),
			autoExpertJSON("ex_2", "agent", "专家二", "职业二"),
			autoExpertJSON("ex_3", "agent", "专家三", "职业三"),
		)))
		up.onChat(autoOK(autoChatSSE))
		n := 0
		up.onDesktopReport(func(autoReq) (int, string) {
			n++
			// 第 1 次召唤失败；第 3 次（专家二的使用事件）失败；其余成功。
			if n == 1 || n == 3 {
				return 400, `{"code":400,"msg":"report boom"}`
			}
			return 200, autoEnvelope("{}")
		})
		msg, err := runExpertUse(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if !strings.Contains(msg, "已对 1 位真实专家") {
			t.Fatalf("两次失败后应只成功一位: %q", msg)
		}
		if got := len(up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")); got != 5 {
			t.Fatalf("应尝试 5 次上报，实际 %d", got)
		}
	})
}

// skill_1：真实对话 + skill_info 事件；对话失败与上报失败分别报错。
func TestRunSkillFresh(t *testing.T) {
	autoFastWaits(t)

	t.Run("上报真实对话与 skill_info", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onChat(autoOK(autoChatSSE))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		msg, err := runSkillFresh(p, autoAuth(t, p))
		if err != nil || !strings.Contains(msg, "skill_info") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		autoWantSequence(t, up.requests(),
			"POST "+autoChatHost+autoChatPath,
			"POST "+autoChatHost+autoReportPath,
		)
		evs := autoEvents(t, up.requests()[1].Body)
		info := autoEventByCode(t, evs, "skill_info")
		if info["conversationId"] == "" || info["requestId"] == "" {
			t.Fatalf("skill_info 应带真实会话 id: %v", info)
		}
		resp := autoEventByCode(t, evs, "chat_message_response")
		if resp["finishReason"] != "tool_calls" {
			t.Fatalf("对话链应标记工具调用: %v", resp)
		}
	})

	t.Run("对话失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onChat(autoFail(400, `{"code":400}`))
		if _, err := runSkillFresh(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "真实对话") {
			t.Fatalf("应返回对话失败: %v", err)
		}
	})

	t.Run("上报失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onChat(autoOK(autoChatSSE))
		up.onDesktopReport(autoFail(400, `{"code":400}`))
		if _, err := runSkillFresh(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "skill_info") {
			t.Fatalf("应返回 skill_info 上报失败: %v", err)
		}
	})
}

// Expert_lighthouse：市场列表命中时用服务端专家信息；列表失败回落内置信息；
// 使用事件必须 mode=LOCAL、type 为空、cost=0、chat 链带 has_expert。
func TestRunExpertLighthouse(t *testing.T) {
	autoFastWaits(t)
	const lhID = "ex_2cvvUZQhDyeJ"

	t.Run("命中市场专家", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoOK(autoExpertsJSON(autoExpertJSON(lhID, "agent", "腾讯轻量云专家", "腾讯轻量云专家"))))
		up.onChat(autoOK(autoChatSSE))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		msg, err := runExpertLighthouse(p, autoAuth(t, p))
		if err != nil || !strings.Contains(msg, "轻量云专家") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		autoWantSequence(t, up.requests(),
			"POST "+autoChatHost+autoMarketPath,
			"POST "+autoChatHost+autoReportPath,
			"POST "+autoChatHost+autoChatPath,
			"POST "+autoChatHost+autoReportPath,
		)
		reports := up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")
		chatEvents := autoEvents(t, reports[1].Body)
		created := autoEventByCode(t, chatEvents, "agent_task_created")
		if created["has_expert"] != true || created["expert_id"] != lhID {
			t.Fatalf("chat 链应带 has_expert: %v", created)
		}
		use := autoEventByCode(t, chatEvents, "expert_actual_use")
		if use["mode"] != "LOCAL" || use["type"] != "" || use["cost"] != float64(0) {
			t.Fatalf("轻量云使用事件形状不对: %v", use)
		}
	})

	t.Run("市场列表失败回落内置专家", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoFail(400, `{"code":400}`))
		up.onChat(autoOK(autoChatSSE))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		if _, err := runExpertLighthouse(p, autoAuth(t, p)); err != nil {
			t.Fatalf("列表失败不应阻塞: %v", err)
		}
		reports := up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")
		summon := autoEvents(t, reports[0].Body)
		if autoEventByCode(t, summon, "expert_summon_click")["id"] != lhID {
			t.Fatalf("应使用内置轻量云专家: %v", summon)
		}
	})

	t.Run("对话失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoOK(autoExpertsJSON()))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		up.onChat(autoFail(400, `{"code":400}`))
		if _, err := runExpertLighthouse(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "真实对话") {
			t.Fatalf("应返回对话失败: %v", err)
		}
	})

	t.Run("召唤链上报失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoOK(autoExpertsJSON()))
		up.onDesktopReport(autoFail(400, `{"code":400}`))
		if _, err := runExpertLighthouse(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "召唤链") {
			t.Fatalf("应返回召唤链错误: %v", err)
		}
	})

	t.Run("使用事件上报失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoOK(autoExpertsJSON()))
		up.onChat(autoOK(autoChatSSE))
		n := 0
		up.onDesktopReport(func(autoReq) (int, string) {
			n++
			if n == 2 {
				return 400, `{"code":400}`
			}
			return 200, autoEnvelope("{}")
		})
		if _, err := runExpertLighthouse(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), "使用事件") {
			t.Fatalf("应返回使用事件错误: %v", err)
		}
	})
}

// autoExpertPage 一页满员的专家列表（MarketExpertPageSize 个），专家 id 按页码编号，名字都不是任务要找的。
func autoExpertPage(page int) string {
	items := make([]string, 0, upstream.MarketExpertPageSize)
	for i := range upstream.MarketExpertPageSize {
		id := fmt.Sprintf("ex_p%d_%d", page, i)
		items = append(items, autoExpertJSON(id, "agent", "专家"+id, "职业"+id))
	}
	return autoExpertsJSON(items...)
}

// 「体验某位专家」类任务：带 id 的按 id 翻页找（满页才翻下一页，最多 expertTaskSearchPages 页），
// 列表里没有也用任务里的 id 照样召唤；只有名字的在专家页搜，搜不到不召唤别的专家。
func TestRunExpertTask(t *testing.T) {
	autoFastWaits(t)
	const id = "ex_task1"
	byID := runExpertTask(expertTaskTarget{ID: id, Name: "任务里的专家"})
	byName := runExpertTask(expertTaskTarget{Name: "无畏契约拼豆专家"})

	t.Run("带 id 时用列表里这位专家的信息", func(t *testing.T) {
		p, up := newAutoPanel(t)
		e := `{"expert_id":"` + id + `","expert_type":"agent","display_name_zh":"列表里的名字","profession_zh":"列表里的职业","version":"2.1.0","categories":["ecommerce"]}`
		up.onMarket(autoOK(autoExpertsJSON(autoExpertJSON("ex_other", "agent", "别的专家", "别的职业"), e)))
		up.onChat(autoOK(autoChatSSE))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		msg, err := byID(p, autoAuth(t, p))
		if err != nil || msg != "已召唤「任务里的专家」并完成一次真实对话" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		autoWantSequence(t, up.requests(),
			"POST "+autoChatHost+autoMarketPath,
			"POST "+autoChatHost+autoReportPath,
			"POST "+autoChatHost+autoChatPath,
			"POST "+autoChatHost+autoReportPath,
		)
		market := autoBodyMap(t, up.requests()[0].Body)
		if market["expert_type"] != "agent" || market["page"] != float64(1) {
			t.Fatalf("应拉单专家列表第一页: %v", market)
		}
		chats := up.requestsTo(http.MethodPost, autoChatHost, autoChatPath, "")
		if got := chats[0].Header.Get("X-Expert-Id"); got != id {
			t.Fatalf("对话应带这位专家的 X-Expert-Id，got %q", got)
		}
		reports := up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")
		summon := autoEventByCode(t, autoEvents(t, reports[0].Body), "expert_summon_click")
		if summon["id"] != id || summon["name"] != "列表里的名字" || summon["version"] != "2.1.0" || summon["type"] != "ecommerce" {
			t.Fatalf("召唤事件应用列表里的专家信息: %v", summon)
		}
		use := autoEventByCode(t, autoEvents(t, reports[1].Body), "expert_actual_use")
		if use["id"] != id || use["requestId"] != autoChatSvcToken || use["mode"] != "LOCAL" {
			t.Fatalf("使用事件应 JOIN 服务端 requestId: %v", use)
		}
	})

	t.Run("带 id 时列表里没有也照样召唤", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoOK(autoExpertsJSON(autoExpertJSON("ex_other", "agent", "别的专家", "别的职业"))))
		up.onChat(autoOK(autoChatSSE))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		if _, err := byID(p, autoAuth(t, p)); err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoChatHost, autoMarketPath, "")); got != 1 {
			t.Fatalf("不满一页就是最后一页，应只拉 1 页，实际 %d", got)
		}
		reports := up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")
		summon := autoEventByCode(t, autoEvents(t, reports[0].Body), "expert_summon_click")
		if summon["id"] != id || summon["name"] != "任务里的专家" || summon["version"] != "1.0.0" || summon["type"] != "all" {
			t.Fatalf("应用任务里的 id 和名字，版本号、分类取缺省值: %v", summon)
		}
	})

	t.Run("带 id 时满页才往下翻", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(func(r autoReq) (int, string) {
			if autoBodyMap(t, r.Body)["page"] == float64(1) {
				return 200, autoExpertPage(1)
			}
			return 200, autoExpertsJSON(autoExpertJSON(id, "agent", "第二页的名字", "职业"))
		})
		up.onChat(autoOK(autoChatSSE))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		if _, err := byID(p, autoAuth(t, p)); err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		market := up.requestsTo(http.MethodPost, autoChatHost, autoMarketPath, "")
		if len(market) != 2 || autoBodyMap(t, market[1].Body)["page"] != float64(2) {
			t.Fatalf("第一页满了没有应翻到第二页，实际拉了 %d 页", len(market))
		}
		if _, ok := autoBodyMap(t, market[0].Body)["keyword"]; ok {
			t.Fatal("带 id 时不搜索")
		}
		reports := up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")
		if summon := autoEventByCode(t, autoEvents(t, reports[0].Body), "expert_summon_click"); summon["name"] != "第二页的名字" {
			t.Fatalf("应用第二页里这位专家的信息: %v", summon)
		}
	})

	t.Run("带 id 时列表失败也照样召唤", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoFail(400, `{"code":400}`))
		up.onChat(autoOK(autoChatSSE))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		if _, err := byID(p, autoAuth(t, p)); err != nil {
			t.Fatalf("列表失败不应阻塞: %v", err)
		}
		if got := up.requestsTo(http.MethodPost, autoChatHost, autoChatPath, "")[0].Header.Get("X-Expert-Id"); got != id {
			t.Fatalf("应召唤任务里的专家，got %q", got)
		}
	})

	t.Run("只有名字时在专家页搜", func(t *testing.T) {
		p, up := newAutoPanel(t)
		// 名字两边带空格也认；响应用 items（官方先取 items）。
		up.onMarket(autoOK(autoEnvelope(`{"items":[` + autoExpertJSON("ex_other", "agent", "无畏契约攻略", "攻略") + `,` +
			autoExpertJSON("ex_valorant", "agent", " 无畏契约拼豆专家 ", "拼豆专家") + `]}`)))
		up.onChat(autoOK(autoChatSSE))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		msg, err := byName(p, autoAuth(t, p))
		if err != nil || !strings.Contains(msg, "「无畏契约拼豆专家」") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		autoWantSequence(t, up.requests(),
			"POST "+autoChatHost+autoMarketPath,
			"POST "+autoChatHost+autoReportPath,
			"POST "+autoChatHost+autoReportPath,
			"POST "+autoChatHost+autoChatPath,
			"POST "+autoChatHost+autoReportPath,
		)
		search := up.requests()[0]
		body := autoBodyMap(t, search.Body)
		if body["keyword"] != "无畏契约拼豆专家" || body["expert_type"] != "agent" || body["page"] != float64(1) {
			t.Fatalf("应在单专家页签按名字搜第一页: %v", body)
		}
		if got := search.Header.Get("X-Language"); got != "zh-CN" {
			t.Fatalf("搜索应带 X-Language: zh-CN，got %q", got)
		}
		reports := up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")
		ev := autoEventByCode(t, autoEvents(t, reports[0].Body), "expert_search")
		if ev["query"] != "无畏契约拼豆专家" || ev["count"] != float64(2) || ev["scope"] != "center" ||
			ev["source"] != "builtin" || ev["type"] != "expert" || ev["mode"] != "LOCAL" {
			t.Fatalf("expert_search 事件 = %v", ev)
		}
		if got := up.requestsTo(http.MethodPost, autoChatHost, autoChatPath, "")[0].Header.Get("X-Expert-Id"); got != "ex_valorant" {
			t.Fatalf("应召唤按名字搜到的专家，got %q", got)
		}
	})

	t.Run("单专家页签没搜到再不分类型搜", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(func(r autoReq) (int, string) {
			if _, ok := autoBodyMap(t, r.Body)["expert_type"]; ok {
				return 200, autoExpertsJSON(autoExpertJSON("ex_other", "agent", "别的专家", "别的职业"))
			}
			// 按职业名也认。
			return 200, autoExpertsJSON(autoExpertJSON("ex_valorant", "team", "小拼", "无畏契约拼豆专家"))
		})
		up.onChat(autoOK(autoChatSSE))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		if _, err := byName(p, autoAuth(t, p)); err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		market := up.requestsTo(http.MethodPost, autoChatHost, autoMarketPath, "")
		if len(market) != 2 || autoBodyMap(t, market[1].Body)["keyword"] != "无畏契约拼豆专家" {
			t.Fatalf("应再搜一次，实际拉了 %d 次", len(market))
		}
		chat := up.requestsTo(http.MethodPost, autoChatHost, autoChatPath, "")[0]
		if chat.Header.Get("X-Expert-Id") != "ex_valorant" || chat.Header.Get("X-Expert-Team-Task") != "true" {
			t.Fatalf("应召唤第二次搜到的专家团: %v", chat.Header)
		}
	})

	t.Run("只有名字时搜不到就不召唤", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onMarket(autoOK(autoExpertsJSON(autoExpertJSON("ex_other", "agent", "别的专家", "别的职业"))))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		_, err := byName(p, autoAuth(t, p))
		if err == nil || !strings.Contains(err.Error(), "在专家页搜「无畏契约拼豆专家」没有找到") {
			t.Fatalf("应返回找不到: %v", err)
		}
		autoWantSequence(t, up.requests(),
			"POST "+autoChatHost+autoMarketPath,
			"POST "+autoChatHost+autoReportPath,
			"POST "+autoChatHost+autoMarketPath,
		)
	})

	// 搜索、搜索事件、第二次搜索各自失败时报错，不再往下做。
	for _, tc := range []struct {
		name, want string
		failSearch int // 第几次搜索失败
		failReport bool
		requests   int
	}{
		{name: "搜索失败", want: "搜索专家", failSearch: 1, requests: 1},
		{name: "搜索事件上报失败", want: "搜索事件", failReport: true, requests: 2},
		{name: "第二次搜索失败", want: "搜索专家", failSearch: 2, requests: 3},
	} {
		t.Run("只有名字时"+tc.name+"报错", func(t *testing.T) {
			p, up := newAutoPanel(t)
			n := 0
			up.onMarket(func(autoReq) (int, string) {
				n++
				if n == tc.failSearch {
					return 400, `{"code":400}`
				}
				return 200, autoExpertsJSON()
			})
			if tc.failReport {
				up.onDesktopReport(autoFail(400, `{"code":400}`))
			} else {
				up.onDesktopReport(autoOK(autoEnvelope("{}")))
			}
			if _, err := byName(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应返回%s错误: %v", tc.want, err)
			}
			if got := len(up.requests()); got != tc.requests {
				t.Fatalf("应发 %d 个请求就停，实际 %d 个", tc.requests, got)
			}
		})
	}

	// 召唤链、真实对话、使用事件三步各自失败时报对应的错。
	for _, tc := range []struct {
		name, want string
		chatFail   bool
		failReport int // 第几次上报失败，0 表示上报都成功
	}{
		{name: "召唤链上报失败", want: "召唤链", failReport: 1},
		{name: "对话失败", want: "真实对话", chatFail: true},
		{name: "使用事件上报失败", want: "使用事件", failReport: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, up := newAutoPanel(t)
			up.onMarket(autoOK(autoExpertsJSON()))
			if tc.chatFail {
				up.onChat(autoFail(400, `{"code":400}`))
			} else {
				up.onChat(autoOK(autoChatSSE))
			}
			n := 0
			up.onDesktopReport(func(autoReq) (int, string) {
				n++
				if n == tc.failReport {
					return 400, `{"code":400}`
				}
				return 200, autoEnvelope("{}")
			})
			if _, err := byID(p, autoAuth(t, p)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应返回%s错误: %v", tc.want, err)
			}
		})
	}
}

// black_cat：窗口外不发请求；窗口内 need<=0 提示达标；need=1 走一次真实对话
// （上游夜聊间隔固定 4s，只测一次成功）；对话失败返回进度提示。
func TestRunBlackCat(t *testing.T) {
	autoFastWaits(t)

	t.Run("窗口外不动作", func(t *testing.T) {
		p, up := newAutoPanel(t)
		autoSetNight(t, time.Date(2026, 10, 19, 12, 0, 0, 0, time.Local))
		msg, err := runBlackCat(p, autoAuth(t, p))
		if err != nil || !strings.Contains(msg, "不在 23:00–08:00") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if n := len(up.requests()); n != 0 {
			t.Fatalf("窗口外不应发请求，实际 %d 个", n)
		}
	})

	t.Run("窗口内进度已达标", func(t *testing.T) {
		p, up := newAutoPanel(t)
		autoSetNight(t, time.Date(2026, 10, 19, 23, 30, 0, 0, time.Local))
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("black_cat", 3, 3, "accepted"))))
		msg, err := runBlackCat(p, autoAuth(t, p))
		if err != nil || msg != "进度已达标，无需补足" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoChatHost, autoChatPath, "")); got != 0 {
			t.Fatalf("达标时不应对话，实际 %d 次", got)
		}
	})

	t.Run("窗口内补一次对话", func(t *testing.T) {
		p, up := newAutoPanel(t)
		autoSetNight(t, time.Date(2026, 10, 19, 2, 0, 0, 0, time.Local))
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("black_cat", 3, 2, "accepted"))))
		up.onChat(autoOK(autoChatSSE))
		up.onBillingReport(autoOK(autoEnvelope("{}")))
		msg, err := runBlackCat(p, autoAuth(t, p))
		if err != nil || !strings.Contains(msg, "已完成 1 次夜间对话") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		autoWantSequence(t, up.requests(),
			"GET "+autoWebHost+autoSubscribePath,
			"GET "+autoWebHost+autoTasksPath,
			"POST "+autoChatHost+autoChatPath,
			"POST "+autoBillingHost+autoReportPath,
		)
		if model := autoBodyMap(t, up.requests()[2].Body)["model"]; model != "glm-5.2" {
			t.Fatalf("夜间对话应用 glm-5.2，got %v", model)
		}
		evs := autoEvents(t, up.requests()[3].Body)
		if evs[0]["requestModelId"] != "glm-5.2" {
			t.Fatalf("夜间上报应带 glm-5.2: %v", evs[0])
		}
	})

	t.Run("窗口内查询失败返回错误", func(t *testing.T) {
		p, up := newAutoPanel(t)
		autoSetNight(t, time.Date(2026, 10, 19, 23, 30, 0, 0, time.Local))
		up.onDefaultTasks(autoFail(400, `{"code":400}`))
		if _, err := runBlackCat(p, autoAuth(t, p)); err == nil {
			t.Fatal("查询失败应返回 error")
		}
	})

	t.Run("窗口内对话失败返回进度", func(t *testing.T) {
		p, up := newAutoPanel(t)
		autoSetNight(t, time.Date(2026, 10, 19, 23, 30, 0, 0, time.Local))
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("black_cat", 3, 2, "accepted"))))
		up.onChat(autoFail(400, `{"code":400,"msg":"night boom"}`))
		msg, err := runBlackCat(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("夜聊失败不应返回 error: %v", err)
		}
		if !strings.HasPrefix(msg, "完成 0/1 次后中断: ") {
			t.Fatalf("message 不对: %q", msg)
		}
	})
}

// 小程序通用闭环 runMPMiniChatTask：已可领直接领奖、差额补事件（activityId 有无）、
// accept 登记验证、回读未达标 / 已 claimed、上报失败、任务缺失与领奖失败。
func TestRunMPMiniChatTask(t *testing.T) {
	autoFastWaits(t)

	t.Run("已可领直接领奖", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("school_season", 1, 1, "accepted"))))
		up.onMPClaim("school_season", autoOK(autoClaimJSON(100, 5)))
		msg, err := runSchoolSeason(p, autoAuth(t, p))
		if err != nil || msg != "已领取奖励（+100c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "mp-weixin")); got != 0 {
			t.Fatalf("已可领不应补事件，实际 %d 次", got)
		}
	})

	t.Run("差额补事件带 activityId", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 2
			}
			return 200, autoTasksJSON(autoTaskJSON("school_season", 2, cur, "accepted"))
		})
		up.onMPReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("school_season", autoOK(autoClaimJSON(100, 5)))
		oldJitter := mpChatEventJitter
		mpChatEventJitter = time.Nanosecond // 覆盖 randDuration 正数分支
		t.Cleanup(func() { mpChatEventJitter = oldJitter })
		msg, err := runSchoolSeason(p, autoAuth(t, p))
		if err != nil || msg != "任务点亮并领取奖励（+100c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		reports := up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "mp-weixin")
		if len(reports) != 2 {
			t.Fatalf("应补 2 条事件，实际 %d", len(reports))
		}
		for _, r := range reports {
			ev := autoEvents(t, r.Body)[0]
			if ev["eventCode"] != "chat_request_send" || ev["activityId"] != "school_open_day_2026" {
				t.Fatalf("校园日事件形状不对: %v", ev)
			}
		}
	})

	t.Run("Sequential_Tasks_1 事件不带 activityId", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			// target=0：未 accept 的 mp 任务下发 target 为 0，动作按兜底 1 补报。
			return 200, autoTasksJSON(autoTaskJSON("Sequential_Tasks_1", 0, cur, "accepted"))
		})
		up.onMPReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("Sequential_Tasks_1", autoOK(autoClaimJSON(100, 5)))
		msg, err := runSequentialChat(p, autoAuth(t, p))
		if err != nil || msg != "任务点亮并领取奖励（+100c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		reports := up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "mp-weixin")
		if len(reports) != 1 {
			t.Fatalf("应补 1 条事件，实际 %d", len(reports))
		}
		ev := autoEvents(t, reports[0].Body)[0]
		if _, ok := ev["activityId"]; ok {
			t.Fatalf("Tasks_1 事件不应带 activityId: %v", ev)
		}
	})

	t.Run("accept 未登记生效重试两次", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("school_season", 1, 0, "not_accepted"))))
		up.onAcceptMP(autoOK(autoEnvelope("{}")))
		msg, err := runSchoolSeason(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if !strings.HasPrefix(msg, "accept 未登记生效") {
			t.Fatalf("message 不对: %q", msg)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "miniprogram")); got != 2 {
			t.Fatalf("应重试 accept 两次，实际 %d", got)
		}
		if got := len(up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "mp-weixin")); got != 0 {
			t.Fatalf("accept 未生效不应上报，实际 %d", got)
		}
	})

	t.Run("accept 请求本身失败也重试", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("school_season", 1, 0, "not_accepted"))))
		up.onAcceptMP(autoFail(400, `{"code":400,"msg":"accept boom"}`))
		msg, err := runSchoolSeason(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if !strings.HasPrefix(msg, "accept 未登记生效") {
			t.Fatalf("message 不对: %q", msg)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "miniprogram")); got != 2 {
			t.Fatalf("accept 失败也应重试两次，实际 %d", got)
		}
	})

	t.Run("accept 生效后回读真实 target", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		accepted, progressed := false, false
		up.onMPTasks(func(autoReq) (int, string) {
			status := "not_accepted"
			if accepted {
				status = "accepted"
			}
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("school_season", 1, cur, status))
		})
		up.onAcceptMP(func(autoReq) (int, string) { accepted = true; return 200, autoEnvelope("{}") })
		up.onMPReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("school_season", autoOK(autoClaimJSON(100, 5)))
		msg, err := runSchoolSeason(p, autoAuth(t, p))
		if err != nil || msg != "任务点亮并领取奖励（+100c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "miniprogram")); got != 1 {
			t.Fatalf("accept 登记生效后不应重试，实际 %d 次", got)
		}
	})

	t.Run("上报失败中断", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("school_season", 1, 0, "accepted"))))
		up.onMPReport(autoFail(400, `{"code":400}`))
		msg, err := runSchoolSeason(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("上报失败不应返回 error: %v", err)
		}
		if !strings.HasPrefix(msg, "完成 0/1 次上报后中断: ") {
			t.Fatalf("message 不对: %q", msg)
		}
	})

	t.Run("回读未达标", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("school_season", 1, 0, "accepted"))))
		up.onMPReport(autoOK(autoEnvelope("{}")))
		msg, err := runSchoolSeason(p, autoAuth(t, p))
		if err != nil || msg != "已上报 1 次但进度未达 0/1（异步计分未归账，下次重试）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost,
			"/activity/growth/tasks/school_season/claim", "miniprogram")); got != 0 {
			t.Fatalf("未达标不应领奖，实际 %d 次", got)
		}
	})

	t.Run("回读已 claimed", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			status := "accepted"
			if progressed {
				status = "claimed"
			}
			return 200, autoTasksJSON(autoTaskJSON("school_season", 1, 0, status))
		})
		up.onMPReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		msg, err := runSchoolSeason(p, autoAuth(t, p))
		if err != nil || msg != "本轮已入账（claimed）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("回读查询失败继续下一轮", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		reported, mpQueries := false, 0
		up.onMPTasks(func(autoReq) (int, string) {
			if !reported {
				return 200, autoTasksJSON(autoTaskJSON("school_season", 1, 0, "accepted"))
			}
			mpQueries++
			if mpQueries == 1 {
				return 400, `{"code":400,"msg":"readback boom"}`
			}
			return 200, autoTasksJSON(autoTaskJSON("school_season", 1, 0, "accepted"))
		})
		up.onMPReport(func(autoReq) (int, string) { reported = true; return 200, autoEnvelope("{}") })
		msg, err := runSchoolSeason(p, autoAuth(t, p))
		if err != nil || msg != "已上报 1 次但进度未达 0/1（异步计分未归账，下次重试）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("任务未下发", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON()))
		msg, err := runSchoolSeason(p, autoAuth(t, p))
		if err != nil || !strings.Contains(msg, "mp 口径未下发该任务") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("已领取", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("school_season", 1, 1, "claimed"))))
		msg, err := runSchoolSeason(p, autoAuth(t, p))
		if err != nil || msg != "已领取" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("列表失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoFail(400, `{"code":400}`))
		if _, err := runSchoolSeason(p, autoAuth(t, p)); err == nil {
			t.Fatal("列表失败应返回 error")
		}
	})

	t.Run("领奖失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("school_season", 1, 1, "accepted"))))
		up.onMPClaim("school_season", autoFail(400, `{"code":400}`))
		up.onWebClaim("school_season", autoFail(400, `{"code":400}`))
		if _, err := runSchoolSeason(p, autoAuth(t, p)); err == nil {
			t.Fatal("领奖失败应返回 error")
		}
		// ClaimRewardMP 400 直接返回错误，不再降级 Web 领奖。
		if got := len(up.requestsTo(http.MethodPost, autoWebHost,
			"/activity/growth/tasks/school_season/claim", "miniprogram")); got != 1 {
			t.Fatalf("应走小程序口径领奖一次，实际 %d", got)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost,
			"/activity/growth/tasks/school_season/claim", "web")); got != 0 {
			t.Fatalf("不应降级 Web 领奖，实际 %d", got)
		}
	})

	t.Run("补事件后领奖失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("school_season", 1, cur, "accepted"))
		})
		up.onMPReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("school_season", autoFail(400, `{"code":400}`))
		up.onWebClaim("school_season", autoFail(400, `{"code":400}`))
		if _, err := runSchoolSeason(p, autoAuth(t, p)); err == nil {
			t.Fatal("补事件后领奖失败应返回 error")
		}
	})

	t.Run("Sequential_Tasks_3 补足后领奖", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			cur := int64(4)
			if progressed {
				cur = 5
			}
			return 200, autoTasksJSON(autoTaskJSON("Sequential_Tasks_3", 5, cur, "accepted"))
		})
		up.onMPReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("Sequential_Tasks_3", autoOK(autoClaimJSON(300, 5)))
		msg, err := runSequentialChat5(p, autoAuth(t, p))
		if err != nil || msg != "任务点亮并领取奖励（+300c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("Sequential_Tasks_6 按差额补报", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			cur := int64(9)
			if progressed {
				cur = 10
			}
			return 200, autoTasksJSON(autoTaskJSON("Sequential_Tasks_6", 10, cur, "accepted"))
		})
		up.onMPReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("Sequential_Tasks_6", autoOK(autoClaimJSON(500, 5)))
		msg, err := runSequentialChat10(p, autoAuth(t, p))
		if err != nil || msg != "任务点亮并领取奖励（+500c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "mp-weixin")); got != 1 {
			t.Fatalf("只应补 1 条，实际 %d", got)
		}
	})
}

// Sequential 链通用骨架 runSequentialEventTask：主判据点亮、未点亮走备用判据、
// 主判据失败、备用判据失败、已 claimed、任务缺失。
func TestRunSequentialEventTask(t *testing.T) {
	autoFastWaits(t)

	t.Run("automation 主判据点亮", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("Sequential_Tasks_4", 0, cur, "accepted"))
		})
		up.onDesktopReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("Sequential_Tasks_4", autoOK(autoClaimJSON(100, 5)))
		msg, err := runSequentialAutomation(p, autoAuth(t, p))
		if err != nil || msg != "任务点亮并领取奖励（+100c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		reports := up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")
		autoEventByCode(t, autoEvents(t, reports[0].Body), "automated_task_create_suc")
	})

	t.Run("已可领直接领奖", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("Sequential_Tasks_4", 1, 1, "accepted"))))
		up.onMPClaim("Sequential_Tasks_4", autoOK(autoClaimJSON(100, 5)))
		msg, err := runSequentialAutomation(p, autoAuth(t, p))
		if err != nil || msg != "已领取奖励（+100c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")); got != 0 {
			t.Fatalf("已可领不应上报判据，实际 %d", got)
		}
	})

	t.Run("已可领但领奖失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("Sequential_Tasks_4", 1, 1, "accepted"))))
		up.onMPClaim("Sequential_Tasks_4", autoFail(400, `{"code":400}`))
		up.onWebClaim("Sequential_Tasks_4", autoFail(400, `{"code":400}`))
		if _, err := runSequentialAutomation(p, autoAuth(t, p)); err == nil {
			t.Fatal("领奖失败应返回 error")
		}
	})

	t.Run("not_accepted 先接受再点亮", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		accepted, progressed := false, false
		up.onMPTasks(func(autoReq) (int, string) {
			status := "not_accepted"
			if accepted {
				status = "accepted"
			}
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("Sequential_Tasks_4", 1, cur, status))
		})
		up.onAcceptMP(func(autoReq) (int, string) { accepted = true; return 200, autoEnvelope("{}") })
		up.onDesktopReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("Sequential_Tasks_4", autoOK(autoClaimJSON(100, 5)))
		msg, err := runSequentialAutomation(p, autoAuth(t, p))
		if err != nil || msg != "任务点亮并领取奖励（+100c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "miniprogram")); got != 1 {
			t.Fatalf("应接受一次，实际 %d", got)
		}
	})

	t.Run("accept 未登记生效", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("Sequential_Tasks_4", 1, 0, "not_accepted"))))
		up.onAcceptMP(autoOK(autoEnvelope("{}")))
		msg, err := runSequentialAutomation(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if !strings.HasPrefix(msg, "accept 未登记生效") {
			t.Fatalf("message 不对: %q", msg)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "miniprogram")); got != 2 {
			t.Fatalf("应重试两次，实际 %d", got)
		}
	})

	t.Run("回读查询失败继续下一轮", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		reported, mpQueries := false, 0
		up.onMPTasks(func(autoReq) (int, string) {
			if !reported {
				return 200, autoTasksJSON(autoTaskJSON("Sequential_Tasks_4", 1, 0, "accepted"))
			}
			mpQueries++
			if mpQueries == 1 {
				return 400, `{"code":400,"msg":"readback boom"}`
			}
			return 200, autoTasksJSON(autoTaskJSON("Sequential_Tasks_4", 1, 0, "accepted"))
		})
		up.onDesktopReport(func(autoReq) (int, string) { reported = true; return 200, autoEnvelope("{}") })
		msg, err := runSequentialAutomation(p, autoAuth(t, p))
		if err != nil || msg != "已上报但进度未点亮（判据形态待解锁后校正，下次重试）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("点亮后领奖失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("Sequential_Tasks_4", 1, cur, "accepted"))
		})
		up.onDesktopReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("Sequential_Tasks_4", autoFail(400, `{"code":400}`))
		up.onWebClaim("Sequential_Tasks_4", autoFail(400, `{"code":400}`))
		if _, err := runSequentialAutomation(p, autoAuth(t, p)); err == nil {
			t.Fatal("点亮后领奖失败应返回 error")
		}
	})

	t.Run("主判据未点亮走备用判据", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		queries, progressed := 0, false
		up.onMPTasks(func(autoReq) (int, string) {
			queries++
			cur := int64(0)
			if progressed || queries >= 3 {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("Sequential_Tasks_5", 1, cur, "accepted"))
		})
		up.onMPReport(autoOK(autoEnvelope("{}")))
		up.onBillingReport(func(autoReq) (int, string) {
			progressed = true
			return 200, autoEnvelope("{}")
		})
		up.onMPClaim("Sequential_Tasks_5", autoOK(autoClaimJSON(100, 5)))
		msg, err := runSequentialModelChat(p, autoAuth(t, p))
		if err != nil || msg != "任务点亮并领取奖励（+100c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		// 主判据（mp 事件）先发生，备用判据（PC 模型活跃上报）后发生。
		mp := up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "mp-weixin")
		pc := up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "")
		if len(mp) != 1 || len(pc) != 1 {
			t.Fatalf("主/备判据各应上报一次: mp=%d pc=%d", len(mp), len(pc))
		}
		if ev := autoEvents(t, pc[0].Body)[0]; ev["requestModelId"] != "glm-5.2" {
			t.Fatalf("备用判据应带 glm-5.2: %v", ev)
		}
		all := up.requests()
		mpIdx, pcIdx := -1, -1
		for i, r := range all {
			if r.Host == autoBillingHost && r.Path == autoReportPath {
				if r.Header.Get("X-Client-Platform") == "mp-weixin" && mpIdx < 0 {
					mpIdx = i
				}
				if r.Header.Get("X-Client-Platform") == "" && pcIdx < 0 {
					pcIdx = i
				}
			}
		}
		if mpIdx < 0 || pcIdx < 0 || mpIdx > pcIdx {
			t.Fatalf("主判据应先于备用判据: mp=%d pc=%d", mpIdx, pcIdx)
		}
	})

	t.Run("playbook 主判据点亮", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("Sequential_Tasks_7", 1, cur, "accepted"))
		})
		up.onDesktopReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("Sequential_Tasks_7", autoOK(autoClaimJSON(500, 5)))
		msg, err := runSequentialPlaybook(p, autoAuth(t, p))
		if err != nil || msg != "任务点亮并领取奖励（+500c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		evs := autoEvents(t, up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")[0].Body)
		autoEventByCode(t, evs, "playbook_cta_click")
		autoEventByCode(t, evs, "playbook_prompt_send")
	})

	t.Run("playbook 未点亮走 mp 备用判据", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON("Sequential_Tasks_7", 1, cur, "accepted"))
		})
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		up.onMPReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim("Sequential_Tasks_7", autoOK(autoClaimJSON(500, 5)))
		msg, err := runSequentialPlaybook(p, autoAuth(t, p))
		if err != nil || msg != "任务点亮并领取奖励（+500c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		mp := up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "mp-weixin")
		if len(mp) != 1 {
			t.Fatalf("备用判据应上报一次，实际 %d", len(mp))
		}
		evs := autoEvents(t, mp[0].Body)
		autoEventByCode(t, evs, "playbook_cta_click")
		autoEventByCode(t, evs, "playbook_prompt_send")
	})

	t.Run("playbook 案例拉不到不上报", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("Sequential_Tasks_7", 1, 0, "accepted"))))
		up.on(http.MethodGet, autoStaticHost, "/playbook/categories.json", "", autoFail(404, "no"))
		msg, err := runSequentialPlaybook(p, autoAuth(t, p))
		if err != nil || !strings.HasPrefix(msg, "判据上报失败: 灵感案例: ") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if n := len(up.requestsTo(http.MethodPost, autoChatHost, autoReportPath, "")); n != 0 {
			t.Fatalf("不应上报，实际 %d 次", n)
		}
	})

	t.Run("主判据上报失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("Sequential_Tasks_4", 1, 0, "accepted"))))
		up.onDesktopReport(autoFail(400, `{"code":400}`))
		msg, err := runSequentialAutomation(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("不应返回 error: %v", err)
		}
		if !strings.HasPrefix(msg, "判据上报失败: ") {
			t.Fatalf("message 不对: %q", msg)
		}
	})

	t.Run("备用判据上报失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("Sequential_Tasks_5", 1, 0, "accepted"))))
		up.onMPReport(autoOK(autoEnvelope("{}")))
		up.onBillingReport(autoFail(400, `{"code":400}`))
		msg, err := runSequentialModelChat(p, autoAuth(t, p))
		if err != nil {
			t.Fatalf("不应返回 error: %v", err)
		}
		if !strings.HasPrefix(msg, "备选判据上报失败: ") {
			t.Fatalf("message 不对: %q", msg)
		}
	})

	t.Run("两轮回读仍未点亮", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("Sequential_Tasks_4", 1, 0, "accepted"))))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))
		msg, err := runSequentialAutomation(p, autoAuth(t, p))
		if err != nil || msg != "已上报但进度未点亮（判据形态待解锁后校正，下次重试）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("回读已 claimed", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			status := "accepted"
			if progressed {
				status = "claimed"
			}
			return 200, autoTasksJSON(autoTaskJSON("Sequential_Tasks_4", 1, 0, status))
		})
		up.onDesktopReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		msg, err := runSequentialAutomation(p, autoAuth(t, p))
		if err != nil || msg != "本轮已入账（claimed）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("任务未下发", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON()))
		msg, err := runSequentialAutomation(p, autoAuth(t, p))
		if err != nil || !strings.Contains(msg, "mp 口径未下发该任务") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("已领取", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("Sequential_Tasks_4", 1, 1, "claimed"))))
		msg, err := runSequentialAutomation(p, autoAuth(t, p))
		if err != nil || msg != "已领取" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("列表失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoFail(400, `{"code":400}`))
		if _, err := runSequentialAutomation(p, autoAuth(t, p)); err == nil {
			t.Fatal("列表失败应返回 error")
		}
	})
}

// Sequential_Tasks_2（选专家对话）：市场专家 id 必须真实、接受后上报
// expert_actual_use、回读达标领奖；市场不可用时不留下半程态。
func TestRunMiniExpert(t *testing.T) {
	autoFastWaits(t)
	const code = "Sequential_Tasks_2"

	t.Run("accept 生效后上报并领奖", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		accepted, progressed := false, false
		up.onMPTasks(func(autoReq) (int, string) {
			status := "not_accepted"
			if accepted {
				status = "accepted"
			}
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON(code, 1, cur, status))
		})
		up.onAcceptMP(func(autoReq) (int, string) { accepted = true; return 200, autoEnvelope("{}") })
		up.onMarket(autoOK(autoExpertsJSON(autoExpertJSON("ex_real_1", "agent", "", "职业一"))))
		up.onMPReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim(code, autoOK(autoClaimJSON(200, 5)))
		msg, err := runMiniExpert(p, autoAuth(t, p))
		if err != nil || msg != "任务点亮并领取奖励（+200c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "miniprogram")); got != 1 {
			t.Fatalf("accept 应一次生效，实际 %d", got)
		}
		reports := up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "mp-weixin")
		if len(reports) != 1 {
			t.Fatalf("应上报一次 expert_actual_use，实际 %d", len(reports))
		}
		ev := autoEventByCode(t, autoEvents(t, reports[0].Body), "expert_actual_use")
		if ev["id"] != "ex_real_1" || ev["type"] != "send_message" || ev["expertTitle"] != "职业一" {
			t.Fatalf("expert_actual_use 形状不对: %v", ev)
		}
	})

	t.Run("市场列表为空跳过", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON(code, 1, 0, "accepted"))))
		up.onMarket(autoOK(autoExpertsJSON()))
		msg, err := runMiniExpert(p, autoAuth(t, p))
		if err != nil || !strings.Contains(msg, "专家市场不可用") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoBillingHost, autoReportPath, "mp-weixin")); got != 0 {
			t.Fatalf("市场不可用不应上报，实际 %d", got)
		}
	})

	t.Run("市场列表失败跳过", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON(code, 1, 0, "accepted"))))
		up.onMarket(autoFail(400, `{"code":400}`))
		msg, err := runMiniExpert(p, autoAuth(t, p))
		if err != nil || !strings.HasPrefix(msg, "专家市场不可用（") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("上报失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON(code, 1, 0, "accepted"))))
		up.onMarket(autoOK(autoExpertsJSON(autoExpertJSON("ex_real_1", "agent", "专家", "专家"))))
		up.onMPReport(autoFail(400, `{"code":400}`))
		msg, err := runMiniExpert(p, autoAuth(t, p))
		if err != nil || !strings.HasPrefix(msg, "上报 expert_actual_use 失败: ") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("accept 未登记生效", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON(code, 1, 0, "not_accepted"))))
		up.onMarket(autoOK(autoExpertsJSON(autoExpertJSON("ex_real_1", "agent", "专家", "专家"))))
		up.onAcceptMP(autoOK(autoEnvelope("{}")))
		msg, err := runMiniExpert(p, autoAuth(t, p))
		if err != nil || !strings.HasPrefix(msg, "accept 未登记生效") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "miniprogram")); got != 2 {
			t.Fatalf("应重试两次，实际 %d", got)
		}
	})

	t.Run("回读未归账", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		// target=0：未 accept 时下发 0，动作按兜底 1 处理。
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON(code, 0, 0, "accepted"))))
		up.onMarket(autoOK(autoExpertsJSON(autoExpertJSON("ex_real_1", "agent", "专家", "专家"))))
		up.onMPReport(autoOK(autoEnvelope("{}")))
		msg, err := runMiniExpert(p, autoAuth(t, p))
		if err != nil || msg != "已上报但进度未归账（异步计分，下次重试）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("回读已 claimed", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			status := "accepted"
			if progressed {
				status = "claimed"
			}
			return 200, autoTasksJSON(autoTaskJSON(code, 1, 0, status))
		})
		up.onMarket(autoOK(autoExpertsJSON(autoExpertJSON("ex_real_1", "agent", "专家", "专家"))))
		up.onMPReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		msg, err := runMiniExpert(p, autoAuth(t, p))
		if err != nil || msg != "本轮已入账（claimed）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("回读查询失败继续下一轮", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		reported, mpQueries := false, 0
		up.onMPTasks(func(autoReq) (int, string) {
			if !reported {
				return 200, autoTasksJSON(autoTaskJSON(code, 1, 0, "accepted"))
			}
			mpQueries++
			if mpQueries == 1 {
				return 400, `{"code":400,"msg":"readback boom"}`
			}
			return 200, autoTasksJSON(autoTaskJSON(code, 1, 0, "accepted"))
		})
		up.onMarket(autoOK(autoExpertsJSON(autoExpertJSON("ex_real_1", "agent", "专家", "专家"))))
		up.onMPReport(func(autoReq) (int, string) { reported = true; return 200, autoEnvelope("{}") })
		msg, err := runMiniExpert(p, autoAuth(t, p))
		if err != nil || msg != "已上报但进度未归账（异步计分，下次重试）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("补报后领奖失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		progressed := false
		up.onMPTasks(func(autoReq) (int, string) {
			cur := int64(0)
			if progressed {
				cur = 1
			}
			return 200, autoTasksJSON(autoTaskJSON(code, 1, cur, "accepted"))
		})
		up.onMarket(autoOK(autoExpertsJSON(autoExpertJSON("ex_real_1", "agent", "专家", "专家"))))
		up.onMPReport(func(autoReq) (int, string) { progressed = true; return 200, autoEnvelope("{}") })
		up.onMPClaim(code, autoFail(400, `{"code":400}`))
		up.onWebClaim(code, autoFail(400, `{"code":400}`))
		if _, err := runMiniExpert(p, autoAuth(t, p)); err == nil {
			t.Fatal("补报后领奖失败应返回 error")
		}
	})

	t.Run("已可领直接领奖且不拉市场", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON(code, 1, 1, "accepted"))))
		up.onMPClaim(code, autoOK(autoClaimJSON(200, 5)))
		msg, err := runMiniExpert(p, autoAuth(t, p))
		if err != nil || msg != "已领取奖励（+200c +5e）" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		if got := len(up.requestsTo(http.MethodPost, autoChatHost, autoMarketPath, "")); got != 0 {
			t.Fatalf("已可领不应拉专家市场，实际 %d 次", got)
		}
	})

	t.Run("任务未下发与已领取", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON()))
		if msg, err := runMiniExpert(p, autoAuth(t, p)); err != nil || !strings.Contains(msg, "mp 口径未下发该任务") {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
		p2, up2 := newAutoPanel(t)
		up2.onDefaultTasks(autoOK(autoTasksJSON()))
		up2.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON(code, 1, 1, "claimed"))))
		if msg, err := runMiniExpert(p2, autoAuth(t, p2)); err != nil || msg != "已领取" {
			t.Fatalf("msg=%q err=%v", msg, err)
		}
	})

	t.Run("列表失败与领奖失败", func(t *testing.T) {
		p, up := newAutoPanel(t)
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoFail(400, `{"code":400}`))
		if _, err := runMiniExpert(p, autoAuth(t, p)); err == nil {
			t.Fatal("列表失败应返回 error")
		}
		p2, up2 := newAutoPanel(t)
		up2.onDefaultTasks(autoOK(autoTasksJSON()))
		up2.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON(code, 1, 1, "accepted"))))
		up2.onMPClaim(code, autoFail(400, `{"code":400}`))
		up2.onWebClaim(code, autoFail(400, `{"code":400}`))
		if _, err := runMiniExpert(p2, autoAuth(t, p2)); err == nil {
			t.Fatal("领奖失败应返回 error")
		}
	})
}

// 动作表辅助函数：任务码查表 / 排序索引 / mp 码判定。
func TestAutoActionHelpers(t *testing.T) {
	if act := autoActionFor(" chat_5 "); act == nil || act.TaskCode != "chat_5" {
		t.Fatalf("应忽略首尾空白找到 chat_5: %v", act)
	}
	if act := autoActionFor("not_a_task"); act != nil {
		t.Fatalf("未知任务应返回 nil: %v", act)
	}
	if autoActionIndex("chat_5") >= autoActionIndex("first_buddy") {
		t.Fatal("chat_5 应先于 first_buddy（依赖顺序）")
	}
	if got := autoActionIndex("not_a_task"); got != 1<<20 {
		t.Fatalf("未知任务索引应为大值，got %d", got)
	}
	if !isMPTaskCode("school_season") || isMPTaskCode("chat_5") {
		t.Fatal("mp 任务码判定不对")
	}
	if got := truncateStr("short", 10); got != "short" {
		t.Fatalf("短文本不应截断: %q", got)
	}
	if got := truncateStr(strings.Repeat("x", 10), 4); got != "xxxx…" {
		t.Fatalf("长文本应截断并加省略号: %q", got)
	}
	if acceptStatusOr(nil) != "?" || acceptStatusOr(&upstream.Task{}) != "?" {
		t.Fatal("空任务 / 空状态的 accept_status 应显示问号")
	}
	if got := acceptStatusOr(&upstream.Task{AcceptStatus: "accepted"}); got != "accepted" {
		t.Fatalf("accept_status 应原样返回: %q", got)
	}
}

// 面板给不给「一键完成」按钮看前端 web/src/lib/tasks.ts 的 AUTO_TASKS，能不能做看后端 autoActions：
// 两边的任务码要一致，否则面板有按钮、后端回 501，或者后端能做、面板却不给按钮。
func TestAutoActionsMatchFrontend(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash("../../web/src/lib/tasks.ts"))
	if err != nil {
		t.Fatalf("读前端任务表: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "export const AUTO_TASKS")
	if start < 0 {
		t.Fatal("web/src/lib/tasks.ts 里找不到 AUTO_TASKS")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatal("AUTO_TASKS 没有结尾的 }")
	}
	var frontCodes []string
	front := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*'([^']+)':`).FindAllStringSubmatch(src[start:start+end], -1) {
		frontCodes = append(frontCodes, m[1])
		front[m[1]] = true
	}
	back := map[string]bool{}
	for _, act := range autoActions {
		back[act.TaskCode] = true
		if !front[act.TaskCode] {
			t.Errorf("后端能自动完成 %s，前端 AUTO_TASKS 没有，面板不会给按钮", act.TaskCode)
		}
	}
	for _, code := range frontCodes {
		if !back[code] {
			t.Errorf("前端 AUTO_TASKS 有 %s，后端 autoActions 没有，点了会回 501", code)
		}
	}
}
