// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

// full_more_test.go 补齐 import/login/compensation/taskcenter/update/index/ring/tasks
// 的失败与边界分支。上游一律走假 Transport 或本地 httptest，不连真实地址。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/selfupdate"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// waitPanelUntil 轮询直到条件成立（最长 5s，每 5ms 一次）。
func waitPanelUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %s 超时", what)
		}
		<-time.After(5 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// 导入
// ---------------------------------------------------------------------------

// 导入：缺 file 字段、拆分非 JSON 数组、网关格式解析失败、uid 校验。
func TestImportInputErrors(t *testing.T) {
	t.Run("缺 file 字段", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if err := mw.WriteField("other", "x"); err != nil {
			t.Fatal(err)
		}
		_ = mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/panel/api/import", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		New(Config{Pool: pool.New("")}).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "missing file field") {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
		}
	})

	t.Run("拆分非 JSON 数组", func(t *testing.T) {
		if _, err := splitImportItems([]byte("[broken")); err == nil {
			t.Fatal("非法数组应返回错误")
		}
	})

	t.Run("网关格式解析失败", func(t *testing.T) {
		p := New(Config{Pool: pool.New("")})
		a, msg := p.gatewayImportAuth(json.RawMessage(`{"accessToken":123}`))
		if a != nil || msg == "" {
			t.Fatalf("a=%v msg=%q", a, msg)
		}
	})

	t.Run("uid 校验", func(t *testing.T) {
		if validImportUID("") || validImportUID(strings.Repeat("a", 65)) {
			t.Fatal("空/超长 uid 应拒绝")
		}
		if !validImportUID("abc-123_X") {
			t.Fatal("合法 uid 应放行")
		}
	})
}

// 导入 CN 个人号：签到 + 余额刷新成功；写盘失败时 skipped 并带原因。
func TestImportCNBranches(t *testing.T) {
	t.Run("成功导入并刷新余额", func(t *testing.T) {
		p, up, dir := newLoginPanel(t)
		up.on(http.MethodPost, autoBillingHost, checkinPathCN, "", autoOK(autoEnvelope("{}")))
		up.on(http.MethodPost, autoBillingHost, resourcePathCN, "", autoOK(loginResourceBody(88, 100)))
		item := `{"auth":{"accessToken":"at-imp","refreshToken":"rt-imp","domain":"www.codebuddy.cn"},` +
			`"account":{"uid":"u-imp","nickname":"导入号"}}`
		resp := postFile(t, p, "/panel/api/import", []byte(item))
		if resp["imported"] != float64(1) || resp["skipped"] != float64(0) {
			t.Fatalf("导入结果不对: %v", resp)
		}
		if a := loadAuth(t, dir, "u-imp"); a.UID != "u-imp" || a.Nickname != "导入号" {
			t.Fatalf("落盘凭证不对: %+v", a)
		}
		if got := len(up.requestsTo(http.MethodPost, autoBillingHost, checkinPathCN, "")); got != 1 {
			t.Fatalf("应签到一次，实际 %d", got)
		}
	})

	t.Run("写盘失败记 skipped", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root 下权限位不生效")
		}
		p, _, _ := newLoginPanel(t)
		ro := filepath.Join(t.TempDir(), "ro")
		if err := os.Mkdir(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
		p.cfg.AuthDir = ro
		item := `{"auth":{"accessToken":"at-ro","refreshToken":"rt-ro","domain":"www.codebuddy.cn"},` +
			`"account":{"uid":"u-ro","nickname":"只读"}}`
		resp := postFile(t, p, "/panel/api/import", []byte(item))
		if resp["imported"] != float64(0) || resp["skipped"] != float64(1) {
			t.Fatalf("结果不对: %v", resp)
		}
		errs, _ := resp["errors"].([]any)
		if len(errs) != 1 || !strings.Contains(errs[0].(string), "save auth failed") {
			t.Fatalf("错误原因不对: %v", resp["errors"])
		}
	})
}

// 导入 global 号：注册激活完成与 trial 已领都打日志，余额刷新成功。
func TestImportGlobalBranches(t *testing.T) {
	var logs bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	p, up, _ := newLoginPanel(t)
	up.on(http.MethodGet, loginGlobalHost, globalRegisterStatus, "", autoOK(`{"code":200}`))
	// 试用改走官网网页形状（webBase 国际版固定 www.workbuddy.ai，带 X-Client-Platform: web）。
	up.on(http.MethodPost, loginGlobalHost, globalTrialPath, "web", autoOK(autoEnvelope("{}")))
	up.on(http.MethodPost, loginGlobalHost, resourcePathGlobal, "", autoOK(loginResourceBody(5, 10)))
	item := `{"auth":{"accessToken":"at-imp-g","refreshToken":"rt-imp-g","domain":"www.workbuddy.ai"},` +
		`"account":{"uid":"u-imp-g","nickname":"国际导入"}}`
	resp := postFile(t, p, "/panel/api/import", []byte(item))
	if resp["imported"] != float64(1) {
		t.Fatalf("导入结果不对: %v", resp)
	}
	text := logs.String()
	if !strings.Contains(text, "注册激活 uid=u-imp-g 完成") {
		t.Fatalf("缺少注册激活成功日志: %q", text)
	}
	if !strings.Contains(text, "trial uid=u-imp-g 已领") {
		t.Fatalf("缺少 trial 已领日志: %q", text)
	}
	if got := len(up.requestsTo(http.MethodPost, loginGlobalHost, resourcePathGlobal, "")); got != 1 {
		t.Fatalf("应查一次余额，实际 %d", got)
	}
}

// ---------------------------------------------------------------------------
// 登录
// ---------------------------------------------------------------------------

// 发起授权的传输层错误 → 502，错误原因带进文案。
func TestLoginStartTransportError(t *testing.T) {
	p, _, _ := newLoginPanel(t)
	p.cfg.Upstream.HTTP = &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial boom")
	})}
	rec := postJSON(t, p, "/panel/api/login/start", `{}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "dial boom") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

// 登录完成时目录建不出来 → 500 mkdir auth dir。
func TestLoginPollMkdirFailure(t *testing.T) {
	p, up, _ := newLoginPanel(t)
	seedLogin(p, "st-mk", "cn")
	loginRouteToken(up, loginCNHost, `{"accessToken":"at","refreshToken":"rt","expiresIn":3600,"domain":"copilot.tencent.com"}`)
	loginRouteAccount(up, loginCNHost, `{"uid":"u-mk","nickname":"x"}`)

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.cfg.AuthDir = filepath.Join(blocker, "auths")
	rec := loginPollState(p, "st-mk")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "mkdir auth dir") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

// 登录完成时目录只读 → SaveAtomic 失败 500 save auth。
func TestLoginPollSaveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下权限位不生效")
	}
	p, up, _ := newLoginPanel(t)
	seedLogin(p, "st-save", "cn")
	loginRouteToken(up, loginCNHost, `{"accessToken":"at","refreshToken":"rt","expiresIn":3600,"domain":"copilot.tencent.com"}`)
	loginRouteAccount(up, loginCNHost, `{"uid":"u-save","nickname":"x"}`)
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	p.cfg.AuthDir = ro
	rec := loginPollState(p, "st-save")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "save auth") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

// ---------------------------------------------------------------------------
// 积分补偿
// ---------------------------------------------------------------------------

// 上游未接入：查询与单号领取都 501；未知 uid 404；global 号 400。
func TestCompensationEndpointEdges(t *testing.T) {
	noUp := New(Config{Pool: pool.New("")})
	if rec := get(noUp, http.MethodGet, "/panel/api/compensation"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("查询 code=%d", rec.Code)
	}

	accounts := pool.New("")
	accounts.Add(&auth.Auth{UID: "u1", AccessToken: "at"})
	noUp2 := New(Config{Pool: accounts})
	if rec := postJSON(t, noUp2, "/panel/api/accounts/u1/compensation/claim", ""); rec.Code != http.StatusNotImplemented {
		t.Fatalf("领取 code=%d", rec.Code)
	}

	p := New(Config{Pool: accounts, Upstream: &upstream.Client{HTTP: http.DefaultClient}})
	if rec := postJSON(t, p, "/panel/api/accounts/nope/compensation/claim", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("未知 uid code=%d", rec.Code)
	}

	g := &auth.Auth{UID: "g1", AccessToken: "at", Domain: "www.workbuddy.ai"}
	accounts.Add(g)
	rec := postJSON(t, p, "/panel/api/accounts/g1/compensation/claim", "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "仅支持国内版个人账号") {
		t.Fatalf("global code=%d body=%s", rec.Code, rec.Body)
	}
}

// 单号领取上游报错（非 busy）→ 502 原文。
func TestCompensationClaimUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":10001,"msg":"claim boom"}`))
	}))
	defer srv.Close()
	accounts := pool.New("")
	accounts.Add(&auth.Auth{UID: "u1", AccessToken: "at"})
	p := New(Config{Pool: accounts, Upstream: &upstream.Client{HTTP: srv.Client(), BillingBaseCN: srv.URL, WebBaseCN: srv.URL}})
	rec := postJSON(t, p, "/panel/api/accounts/u1/compensation/claim", "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "claim boom") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

// 批量查询时请求 context 取消：等已派发的子任务收尾后直接返回。
func TestCompensationContextCancel(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		_, _ = w.Write([]byte(`{"code":0,"data":{"active":true}}`))
	}))
	defer srv.Close()

	accounts := pool.New("")
	for _, uid := range []string{"a", "b", "c", "d"} {
		accounts.Add(&auth.Auth{UID: uid, AccessToken: "at"})
	}
	p := New(Config{Pool: accounts, Upstream: &upstream.Client{HTTP: srv.Client(), BillingBaseCN: srv.URL, WebBaseCN: srv.URL}})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/panel/api/compensation", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		p.ServeHTTP(rec, req)
		close(done)
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("等待并发请求进入超时")
		}
	}
	cancel()
	for i := 0; i < 1000; i++ {
		runtime.Gosched() // 让第 4 个等待槽位的 goroutine 先看到 ctx 取消
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后 handler 未返回")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("取消路径不应写响应: %s", rec.Body)
	}
}

// ---------------------------------------------------------------------------
// 任务中心
// ---------------------------------------------------------------------------

// startGrowthQueue：并发夹取 [1,4]；禁用/global/企业号都不入队。
func TestStartGrowthQueueClampAndGates(t *testing.T) {
	autoFastWaits(t)

	t.Run("并发夹取", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("chat_5", 1, 1, "accepted"))))
		up.onMPTasks(autoOK(autoTasksJSON()))
		up.onWebClaim("chat_5", autoOK(autoClaimJSON(100, 5)))

		started, total, _, _ := p.startGrowthQueue(0, true)
		if !started || total != 1 {
			t.Fatalf("started=%v total=%d", started, total)
		}
		if st := waitQueueIdle(t, p); st["conc"] != float64(1) {
			t.Fatalf("concurrency=0 应夹到 1: %v", st)
		}

		started, _, _, _ = p.startGrowthQueue(9, true)
		if !started {
			t.Fatal("第二次启动应成功")
		}
		if st := waitQueueIdle(t, p); st["conc"] != float64(4) {
			t.Fatalf("concurrency=9 应夹到 4: %v", st)
		}
	})

	t.Run("门控账号不入队", func(t *testing.T) {
		disabled := taskTestAuth("dis", "禁用")
		global := taskTestAuth("glb", "国际")
		global.Domain = "www.workbuddy.ai"
		ent := taskTestAuth("ent", "企业")
		ent.EnterpriseID = "corp-1"
		p, _ := newTasksPanel(t, disabled, global, ent)
		p.cfg.Pool.Disable("dis", "测试")

		started, total, _, msg := p.startGrowthQueue(1, true)
		if started || total != 0 || msg != "全部账号没有待办任务" {
			t.Fatalf("started=%v total=%d msg=%q", started, total, msg)
		}
	})
}

// 队列跨账号条目：不属于当前账号的项保持 pending；accept 列表失败/接受失败都返回 0。
func TestQueueItemsAndAcceptErrors(t *testing.T) {
	autoFastWaits(t)

	t.Run("跨账号条目跳过", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON()))
		q := p.queue()
		q.mu.Lock()
		q.items = []queueItem{
			{UID: "u2", Nickname: "别号", Kind: "growth", Code: "chat_5", Status: "pending"},
			{UID: "u1", Nickname: "一号", Kind: "growth", Code: "chat_5", Status: "pending"},
		}
		items := append([]queueItem(nil), q.items...)
		q.mu.Unlock()
		p.runQueueItems([]queueAccount{{a: p.cfg.Pool.AuthByUID("u1")}}, items, 1)
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.items[0].Status != "pending" {
			t.Fatalf("别的账号的项不应被改: %+v", q.items[0])
		}
		if q.items[1].Status != "done" {
			t.Fatalf("本账号的项应执行: %+v", q.items[1])
		}
	})

	t.Run("accept 列表失败返回 0", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoFail(400, `{"code":400,"msg":"list boom"}`))
		if got := p.acceptPendingTasks(p.cfg.Pool.AuthByUID("u1")); got != 0 {
			t.Fatalf("accept 列表失败应返回 0，got %d", got)
		}
	})

	t.Run("accept 调用失败返回 0", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("chat_5", 1, 0, "not_accepted"))))
		up.onAccept(autoFail(400, `{"code":400,"msg":"accept boom"}`))
		if got := p.acceptPendingTasks(p.cfg.Pool.AuthByUID("u1")); got != 0 {
			t.Fatalf("accept 调用失败应返回 0，got %d", got)
		}
	})
}

// runGrowthQueued：无自动动作、任务列表查询失败、mp 口径领奖。
func TestRunGrowthQueuedMoreBranches(t *testing.T) {
	autoFastWaits(t)

	t.Run("无自动动作", func(t *testing.T) {
		p, _ := newTasksPanel(t, taskTestAuth("u1", "一号"))
		_, err := p.runGrowthQueued(p.cfg.Pool.AuthByUID("u1"), "not-a-task")
		if err == nil || !strings.Contains(err.Error(), "无自动动作") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("任务列表查询失败", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoFail(400, `{"code":400,"msg":"list boom"}`))
		_, err := p.runGrowthQueued(p.cfg.Pool.AuthByUID("u1"), "chat_5")
		if err == nil || !strings.Contains(err.Error(), "list boom") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("mp 口径达标自动领奖", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("Sequential_Tasks_2", 1, 1, "accepted"))))
		up.onMPClaim("Sequential_Tasks_2", autoOK(autoClaimJSON(200, 5)))
		msg, err := p.runGrowthQueued(p.cfg.Pool.AuthByUID("u1"), "Sequential_Tasks_2")
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if !strings.Contains(msg, "自动领奖") {
			t.Fatalf("message=%q", msg)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost,
			"/activity/growth/tasks/Sequential_Tasks_2/claim", "miniprogram")); got == 0 {
			t.Fatal("应走小程序口径领奖")
		}
	})
}

// ---------------------------------------------------------------------------
// 更新检查
// ---------------------------------------------------------------------------

// 开启更新检查，到期记录结果，取消后退出。
func TestUpdateCheckTickEnabled(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(remoteChangelog))
	}))
	defer srv.Close()

	p := New(Config{
		Version:   "1.1.0",
		UpdateURL: srv.URL,
		Live:      livecfg.New(livecfg.Snapshot{UpdateCheck: true}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runUpdateCheck(ctx, 5*time.Millisecond)
	}()
	defer func() {
		cancel()
		<-done
	}()
	waitPanelUntil(t, "更新检查成功并保存结果", func() bool {
		p.upd.mu.Lock()
		defer p.upd.mu.Unlock()
		return !p.upd.checkedAt.IsZero()
	})
	if got := hits.Load(); got != 1 {
		t.Fatalf("同一检查周期应只请求一次，实际 %d 次", got)
	}
}

// 开关关着：tick 到期也不请求远端。
func TestUpdateCheckTickDisabled(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(remoteChangelog))
	}))
	defer srv.Close()

	p := New(Config{
		Version:   "1.1.0",
		UpdateURL: srv.URL,
		Live:      livecfg.New(livecfg.Snapshot{UpdateCheck: false}),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	p.runUpdateCheck(ctx, 5*time.Millisecond)
	if got := hits.Load(); got != 0 {
		t.Fatalf("开关关闭时不应请求远端，实际 %d 次", got)
	}
}

// fetchChangelog：响应体读取失败原样返回。
func TestFetchChangelogReadError(t *testing.T) {
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: panelRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(errReader{}),
		}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = oldClient })

	p := New(Config{UpdateURL: "https://example.invalid/changelog"})
	if _, err := p.fetchChangelog(context.Background()); err == nil || !strings.Contains(err.Error(), "read boom") {
		t.Fatalf("err=%v", err)
	}
}

// errReader 读总是失败。
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

// 回滚成功（没跑着面板装的版本）：200 + restarting=false，文件被删掉。
func TestRollbackSuccessResponse(t *testing.T) {
	dataDir := t.TempDir()
	upd := selfupdate.Launch(dataDir, "1.1.0")
	binDir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "wb2api"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "current.json"), []byte(`{"version":"1.2.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := New(Config{Version: "1.1.0", Updater: upd})
	rec := postJSON(t, p, "/panel/api/update/rollback", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if resp := autoDecode(t, rec); resp["restarting"] != false {
		t.Fatalf("restarting 应为 false: %v", resp)
	}
	entries, err := os.ReadDir(binDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("回滚后目录应清空: %v %v", entries, err)
	}
}

// ---------------------------------------------------------------------------
// 静态资源 / 日志环 / 任务接受
// ---------------------------------------------------------------------------

// serveBytes：未登记扩展名回落 mime 表，未知扩展名回落 octet-stream。
func TestServeBytesContentTypes(t *testing.T) {
	p := New(Config{})
	rec := httptest.NewRecorder()
	p.serveBytes(rec, httptest.NewRequest(http.MethodGet, "/x.json", nil), "x.json", []byte(`{}`))
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "json") {
		t.Fatalf("已知扩展名 Content-Type=%q", got)
	}
	rec = httptest.NewRecorder()
	p.serveBytes(rec, httptest.NewRequest(http.MethodGet, "/x.zzz", nil), "x.zzz", []byte("x"))
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("未知扩展名 Content-Type=%q", got)
	}
}

// gzipOnce：同名文件第二次直接返回缓存结果。
func TestGzipOnceCacheHit(t *testing.T) {
	p := New(Config{})
	first := p.gzipOnce("a.js", bytes.Repeat([]byte("abc"), 1000))
	second := p.gzipOnce("a.js", []byte("different"))
	if !bytes.Equal(first, second) {
		t.Fatal("第二次应命中缓存（忽略新内容）")
	}
}

// Ring：非正容量回落 500；空行丢弃；超容量淘汰最旧。
func TestRingDefaultsAndEviction(t *testing.T) {
	r := NewRing(0)
	if r.cap != 500 {
		t.Fatalf("非正容量应回落 500，got %d", r.cap)
	}
	r = NewRing(2)
	if n, err := r.Write([]byte("\n\na\nb\nc\n")); err != nil || n != len("\n\na\nb\nc\n") {
		t.Fatalf("Write = %d, %v", n, err)
	}
	snap := r.Snapshot()
	if len(snap) != 2 || snap[0].Text != "b" || snap[1].Text != "c" {
		t.Fatalf("应只保留最后两行: %+v", snap)
	}
}

// 批量接受：任务列表失败 → 502 带原文。
func TestTaskAcceptAllListError(t *testing.T) {
	p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
	up.onDefaultTasks(autoFail(400, `{"code":400,"msg":"list boom"}`))
	rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/accept_all", `{}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "list tasks") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}
