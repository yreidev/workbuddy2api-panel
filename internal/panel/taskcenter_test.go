// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

// taskcenter_test.go 面板「任务中心」（taskcenter.go）的测试：扫描待办、
// 队列并发/执行/互斥/领奖，以及我的券码。队列在后台 goroutine 里跑，
// 断言前一律轮询 /panel/api/tasks/queue 等 running=false（带截止时间）。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// queueNow 读一次队列状态。
func queueNow(t *testing.T, p *Panel) map[string]any {
	t.Helper()
	rec := get(p, http.MethodGet, "/panel/api/tasks/queue")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET queue code=%d body=%s", rec.Code, rec.Body)
	}
	return autoDecode(t, rec)
}

// waitQueueIdle 带截止时间轮询到 running=false 并返回最后状态。
func waitQueueIdle(t *testing.T, p *Panel) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := queueNow(t, p)
		if st["running"] == false {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待队列结束超时: %v", st)
		}
		<-time.After(5 * time.Millisecond)
	}
}

// queueItemsOf 取队列状态里的条目数组。
func queueItemsOf(t *testing.T, st map[string]any) []map[string]any {
	t.Helper()
	raw, ok := st["items"].([]any)
	if !ok {
		t.Fatalf("队列状态没有 items 数组: %v", st)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("队列条目不是对象: %v", v)
		}
		out = append(out, m)
	}
	return out
}

// queueItemByCode 按任务码取队列条目。
func queueItemByCode(t *testing.T, items []map[string]any, code string) map[string]any {
	t.Helper()
	for _, it := range items {
		if it["code"] == code {
			return it
		}
	}
	t.Fatalf("队列里没有 %q: %v", code, items)
	return nil
}

// growthPending 的判定规则：已领取 / 已锁定不入队；有自动动作的未达标和已达标
// 都入队（已达标由队列执行时自动领奖）；没有自动动作的永不入队。
func TestGrowthPendingRules(t *testing.T) {
	cases := []struct {
		name string
		task upstream.Task
		want bool
	}{
		{"已领取", upstream.Task{TaskCode: "chat_5", Claimed: true, Target: 1, Current: 0}, false},
		{"已锁定", upstream.Task{TaskCode: "chat_5", Locked: true, Target: 1, Current: 0}, false},
		{"有动作未达标", upstream.Task{TaskCode: "chat_5", Target: 3, Current: 1}, true},
		{"有动作已达标", upstream.Task{TaskCode: "chat_5", Target: 1, Current: 1}, true},
		{"无自动动作", upstream.Task{TaskCode: "no_action", Target: 3, Current: 1}, false},
		{"无自动动作已达标", upstream.Task{TaskCode: "no_action", Target: 1, Current: 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := growthPending(tc.task); got != tc.want {
				t.Fatalf("growthPending(%+v)=%v want %v", tc.task, got, tc.want)
			}
		})
	}
}

func TestClaimPendingRules(t *testing.T) {
	cases := []struct {
		name string
		task upstream.Task
		want bool
	}{
		{"无自动动作已达标未领", upstream.Task{TaskCode: "no_action", Target: 1, Current: 1, Claimable: true}, true},
		{"无自动动作未达标", upstream.Task{TaskCode: "no_action", Target: 3, Current: 1}, false},
		{"无自动动作已领取", upstream.Task{TaskCode: "no_action", Target: 1, Current: 1, Claimed: true}, false},
		{"已锁定", upstream.Task{TaskCode: "no_action", Target: 1, Current: 1, Claimable: true, Locked: true}, false},
		{"有自动动作的归 growthPending", upstream.Task{TaskCode: "chat_5", Target: 1, Current: 1, Claimable: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimPending(tc.task); got != tc.want {
				t.Fatalf("claimPending(%+v)=%v want %v", tc.task, got, tc.want)
			}
		})
	}
}

// 扫描：禁用账号不发请求；global/企业号门控；默认列表失败只影响该账号；
// 小程序列表合并去重且失败静默；pending_count 是所有账号待办数之和。
func TestTasksScanAll(t *testing.T) {
	autoFastWaits(t)

	t.Run("门控与合并去重", func(t *testing.T) {
		global := taskTestAuth("u3", "国际号")
		if _, err := auth.BackfillRealmFor(global, "global"); err != nil {
			t.Fatalf("构造 global 账号失败: %v", err)
		}
		ent := taskTestAuth("u4", "企业号")
		ent.EnterpriseID = "corp-9"
		p, up := newTasksPanel(t,
			taskTestAuth("u1", "一号"),
			taskTestAuth("u2", "二号"),
			global, ent,
		)
		p.cfg.Pool.Disable("u2", "测试禁用")

		up.onDefaultTasks(autoOK(autoTasksJSON(
			autoTaskJSON("chat_5", 3, 1, "accepted"),
			autoTaskJSON("no_action", 3, 1, "accepted"), // 不可自动化，不入待办
			`{"task_code":"t_locked","target":1,"current":0,"accept_status":"accepted","locked":true}`,
			autoTaskJSON("t_claimed", 1, 1, "claimed"),
		)))
		up.onMPTasks(autoOK(autoTasksJSON(
			autoTaskJSON("chat_5", 3, 1, "accepted"), // 与默认列表重复
			autoTaskJSON("school_season", 1, 0, "accepted"),
		)))

		rec := postJSON(t, p, "/panel/api/tasks/scan_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["pending_count"] != float64(2) {
			t.Fatalf("pending_count 应为 2: %v", resp)
		}
		accounts, _ := resp["accounts"].([]any)
		byUID := map[string]map[string]any{}
		for _, v := range accounts {
			m, _ := v.(map[string]any)
			byUID[fmt.Sprint(m["uid"])] = m
		}
		u1 := byUID["u1"]
		grow, _ := u1["growth"].([]any)
		var codes []string
		for _, v := range grow {
			m, _ := v.(map[string]any)
			codes = append(codes, fmt.Sprint(m["task_code"]))
		}
		if strings.Join(codes, ",") != "chat_5,school_season" {
			t.Fatalf("待办合并/去重结果不对: %v", codes)
		}
		for _, uid := range []string{"u3", "u4"} {
			m := byUID[uid]
			if m == nil || m["nickname"] == "" {
				t.Fatalf("%s 应只返回 uid 和昵称: %v", uid, m)
			}
			if _, ok := m["growth"]; ok {
				t.Fatalf("%s 不应有任务: %v", uid, m)
			}
			if _, ok := m["growth_error"]; ok {
				t.Fatalf("%s 不应有错误: %v", uid, m)
			}
		}
		if _, ok := byUID["u2"]; ok {
			t.Fatalf("禁用账号不应有有效行: %v", byUID["u2"])
		}
		for _, r := range up.requests() {
			// 网页形状不再带 X-User-Id；登录码请求带 Bearer、业务请求带 session Cookie。
			if tok := autoToken(r); tok == "at-u2" || tok == "at-u3" || tok == "at-u4" {
				t.Fatalf("禁用/global/企业号不应发请求，收到 %s %s %s", tok, r.Method, r.Path)
			}
		}
	})

	t.Run("没有自动动作的已达标任务列出来等领奖", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(
			autoTaskJSON("no_action", 3, 1, "accepted"),
			autoTaskJSON("no_action_done", 1, 1, "accepted"),
		)))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("mp_only_done", 1, 1, "accepted"))))

		rec := postJSON(t, p, "/panel/api/tasks/scan_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		accounts, _ := resp["accounts"].([]any)
		u1, _ := accounts[0].(map[string]any)
		grow, _ := u1["growth"].([]any)
		var codes []string
		for _, v := range grow {
			m, _ := v.(map[string]any)
			codes = append(codes, fmt.Sprint(m["task_code"]))
		}
		if strings.Join(codes, ",") != "no_action_done,mp_only_done" || resp["pending_count"] != float64(2) {
			t.Fatalf("已达标未领的应列出、未达标的不列: %v %v", codes, resp["pending_count"])
		}
	})

	t.Run("默认列表失败只影响该账号", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"), taskTestAuth("u2", "二号"))
		up.onDefaultTasks(func(r autoReq) (int, string) {
			if autoToken(r) == "at-u1" {
				return 400, `{"code":400,"msg":"u1 boom"}`
			}
			return 200, autoTasksJSON(autoTaskJSON("chat_5", 3, 1, "accepted"))
		})
		up.onMPTasks(autoOK(autoTasksJSON()))
		rec := postJSON(t, p, "/panel/api/tasks/scan_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["pending_count"] != float64(1) {
			t.Fatalf("pending_count 只应算成功账号的 1 项: %v", resp)
		}
		accounts, _ := resp["accounts"].([]any)
		byUID := map[string]map[string]any{}
		for _, v := range accounts {
			m, _ := v.(map[string]any)
			byUID[fmt.Sprint(m["uid"])] = m
		}
		if msg, _ := byUID["u1"]["growth_error"].(string); msg == "" {
			t.Fatalf("u1 应有 growth_error: %v", byUID["u1"])
		}
		grow, _ := byUID["u2"]["growth"].([]any)
		if len(grow) != 1 {
			t.Fatalf("u2 不受影响，应有 1 项待办: %v", byUID["u2"])
		}
	})

	t.Run("小程序列表失败静默", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("chat_5", 3, 1, "accepted"))))
		up.onMPTasks(autoFail(400, `{"code":400,"msg":"mp boom"}`))
		rec := postJSON(t, p, "/panel/api/tasks/scan_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["pending_count"] != float64(1) {
			t.Fatalf("mp 失败时应只算默认列表: %v", resp)
		}
		accounts, _ := resp["accounts"].([]any)
		m, _ := accounts[0].(map[string]any)
		if _, ok := m["growth_error"]; ok {
			t.Fatalf("mp 失败应静默，不写 growth_error: %v", m)
		}
	})
}

// 启动队列：没有待办 started=false 且之后还能再启动；并发夹取 [1,4]；
// 已在运行 409。
func TestTasksRunQueueStart(t *testing.T) {
	autoFastWaits(t)

	t.Run("没有待办不启动且可再次启动", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON()))
		for i := 0; i < 2; i++ {
			rec := postJSON(t, p, "/panel/api/tasks/run_queue", `{}`)
			autoWantCode(t, rec, http.StatusOK)
			resp := autoDecode(t, rec)
			if resp["started"] != false || resp["message"] != "全部账号没有待办任务" {
				t.Fatalf("无待办时应 started=false: %v", resp)
			}
		}
		st := queueNow(t, p)
		if st["running"] != false || st["seq"] != float64(0) {
			t.Fatalf("无待办不应改队列状态: %v", st)
		}
	})

	t.Run("并发参数夹取", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("chat_5", 1, 1, "accepted"))))
		up.onMPTasks(autoOK(autoTasksJSON()))
		up.onWebClaim("chat_5", autoOK(autoClaimJSON(100, 5)))

		rec := postJSON(t, p, "/panel/api/tasks/run_queue", `{"concurrency":0}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["started"] != true || resp["seq"] != float64(1) {
			t.Fatalf("第一次启动应 started=true seq=1: %v", resp)
		}
		st := waitQueueIdle(t, p)
		if st["conc"] != float64(1) {
			t.Fatalf("concurrency=0 应夹到 1: %v", st)
		}

		rec = postJSON(t, p, "/panel/api/tasks/run_queue", `{"concurrency":9}`)
		autoWantCode(t, rec, http.StatusOK)
		resp = autoDecode(t, rec)
		if resp["started"] != true || resp["seq"] != float64(2) {
			t.Fatalf("第二次启动 seq 应加 1: %v", resp)
		}
		st = waitQueueIdle(t, p)
		if st["conc"] != float64(4) {
			t.Fatalf("concurrency=9 应夹到 4: %v", st)
		}
	})

	t.Run("已在运行返回 409", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		up.onDefaultTasks(func(autoReq) (int, string) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return 200, autoTasksJSON(autoTaskJSON("chat_5", 1, 1, "accepted"))
		})
		up.onMPTasks(autoOK(autoTasksJSON()))
		up.onWebClaim("chat_5", autoOK(autoClaimJSON(100, 5)))

		var firstRec *httptest.ResponseRecorder
		done := make(chan struct{})
		go func() {
			defer close(done)
			firstRec = postJSON(t, p, "/panel/api/tasks/run_queue", `{}`)
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("等待队列扫描进入上游请求超时")
		}
		rec := postJSON(t, p, "/panel/api/tasks/run_queue", `{}`)
		autoWantCode(t, rec, http.StatusConflict)

		close(release)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("等待第一次启动返回超时")
		}
		if firstRec.Code != http.StatusOK {
			t.Fatalf("第一次启动应成功: code=%d body=%s", firstRec.Code, firstRec.Body)
		}
		if first := autoDecode(t, firstRec); first["started"] != true {
			t.Fatalf("第一次启动应成功: %v", first)
		}
		waitQueueIdle(t, p)
	})
}

// 队列执行：批量接受 → 按 autoActions 顺序逐项执行；单错不阻塞；账号被锁全跳过；
// 已领取/无此任务记 done；达标领奖（含 mp 口径）；RunGrowthQueueOnce 同一管线。
func TestTasksRunQueueExecution(t *testing.T) {
	autoFastWaits(t)

	t.Run("先接受再按动作顺序执行", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(
			autoTaskJSON("chat_5", 1, 0, "not_accepted"),
			autoTaskJSON("RichMeow_Chat", 1, 0, "not_accepted"),
		)))
		up.onMPTasks(autoOK(autoTasksJSON()))
		up.onAccept(autoOK(autoEnvelope("{}")))
		up.onBillingReport(autoOK(autoEnvelope("{}")))
		up.onDesktopReport(autoOK(autoEnvelope("{}")))

		rec := postJSON(t, p, "/panel/api/tasks/run_queue", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		st := waitQueueIdle(t, p)
		items := queueItemsOf(t, st)
		if len(items) != 2 || items[0]["code"] != "chat_5" || items[1]["code"] != "RichMeow_Chat" {
			t.Fatalf("队列项应按 autoActions 顺序: %v", items)
		}
		for _, it := range items {
			if it["status"] != "done" {
				t.Fatalf("两项都应 done: %v", it)
			}
		}

		acceptIdx, chatIdx, richIdx := -1, -1, -1
		for i, r := range up.requests() {
			switch {
			case r.Path == autoAcceptPath:
				acceptIdx = i
			case r.Host == autoBillingHost && r.Path == autoReportPath:
				chatIdx = i
			case r.Host == autoChatHost && r.Path == autoReportPath:
				richIdx = i
			}
		}
		if acceptIdx < 0 || chatIdx < 0 || richIdx < 0 || acceptIdx >= chatIdx || chatIdx >= richIdx {
			t.Fatalf("顺序应为 accept → chat_5 上报 → RichMeow 上报: accept=%d chat=%d rich=%d\n%v",
				acceptIdx, chatIdx, richIdx, autoSequence(up.requests()))
		}
		accepts := up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "web")
		if len(accepts) != 1 {
			t.Fatalf("应批量接受一次，实际 %d", len(accepts))
		}
		if body := string(accepts[0].Body); !strings.Contains(body, "chat_5") || !strings.Contains(body, "RichMeow_Chat") {
			t.Fatalf("批量接受应含两个待接受任务: %s", body)
		}
	})

	t.Run("某项执行出错后面照常", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(
			autoTaskJSON("RichMeow_Chat", 1, 0, "accepted"),
			autoTaskJSON("Library_read", 1, 0, "accepted"),
		)))
		up.onMPTasks(autoOK(autoTasksJSON()))
		up.onDesktopReport(autoFail(400, `{"code":400,"msg":"desktop boom"}`))
		up.onWebReport(autoOK(autoEnvelope("{}")))

		rec := postJSON(t, p, "/panel/api/tasks/run_queue", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		st := waitQueueIdle(t, p)
		items := queueItemsOf(t, st)
		if it := queueItemByCode(t, items, "RichMeow_Chat"); it["status"] != "error" {
			t.Fatalf("RichMeow_Chat 应 error: %v", it)
		}
		if it := queueItemByCode(t, items, "Library_read"); it["status"] != "done" {
			t.Fatalf("后面的 Library_read 应继续执行: %v", it)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoReportPath, "")); got != 1 {
			t.Fatalf("Library_read 应上报一次，实际 %d", got)
		}
	})

	t.Run("账号被别的动作锁着全部跳过", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(
			autoTaskJSON("chat_5", 1, 0, "accepted"),
			autoTaskJSON("RichMeow_Chat", 1, 0, "accepted"),
		)))
		up.onMPTasks(autoOK(autoTasksJSON()))
		if !p.tryLockAccount("u1") {
			t.Fatal("测试前置加锁失败")
		}
		defer p.unlockAccount("u1")

		rec := postJSON(t, p, "/panel/api/tasks/run_queue", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		st := waitQueueIdle(t, p)
		items := queueItemsOf(t, st)
		if len(items) != 2 {
			t.Fatalf("应有两个队列项: %v", items)
		}
		for _, it := range items {
			if it["status"] != "skipped" || it["message"] != "该账号有其它任务动作在执行，跳过" {
				t.Fatalf("锁着时应全部 skipped: %v", it)
			}
		}
		for _, r := range up.requests() {
			if r.Method == http.MethodPost {
				t.Fatalf("被锁账号不应发写请求: %s %s", r.Method, r.Path)
			}
		}
	})

	t.Run("已领取与无此任务记 done", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onMPTasks(autoOK(autoTasksJSON()))
		calls := 0
		up.onDefaultTasks(func(autoReq) (int, string) {
			calls++
			switch calls {
			case 1, 2: // 扫描 + 批量接受前的列表
				return 200, autoTasksJSON(
					autoTaskJSON("chat_5", 1, 0, "accepted"),
					autoTaskJSON("RichMeow_Chat", 1, 0, "accepted"),
				)
			case 3: // chat_5 执行前回读：已领取
				return 200, autoTasksJSON(autoTaskJSON("chat_5", 1, 0, "claimed"))
			default: // RichMeow_Chat 执行前：列表里没有这个任务
				return 200, autoTasksJSON()
			}
		})

		rec := postJSON(t, p, "/panel/api/tasks/run_queue", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		st := waitQueueIdle(t, p)
		items := queueItemsOf(t, st)
		if it := queueItemByCode(t, items, "chat_5"); it["status"] != "done" || it["message"] != "已完成（已领取）" {
			t.Fatalf("已领取应 done: %v", it)
		}
		if it := queueItemByCode(t, items, "RichMeow_Chat"); it["status"] != "done" || it["message"] != "该账号无此任务" {
			t.Fatalf("无此任务应 done: %v", it)
		}
	})

	t.Run("达标领奖带奖励与进度", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
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

		rec := postJSON(t, p, "/panel/api/tasks/run_queue", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		st := waitQueueIdle(t, p)
		it := queueItemByCode(t, queueItemsOf(t, st), "RichMeow_Chat")
		msg, _ := it["message"].(string)
		if it["status"] != "done" || !strings.Contains(msg, "自动领奖 +100 分 +5 能") || !strings.Contains(msg, "（进度 1/1）") {
			t.Fatalf("领奖结果与进度应写进 message: %v", it)
		}
	})

	t.Run("mp 任务走 ClaimRewardMP", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		claimed := false
		up.onMPTasks(func(autoReq) (int, string) {
			status := "accepted"
			if claimed {
				status = "claimed"
			}
			return 200, autoTasksJSON(autoTaskJSON("school_season", 1, 1, status))
		})
		up.onMPClaim("school_season", func(autoReq) (int, string) {
			claimed = true
			return 200, autoClaimJSON(100, 5)
		})

		rec := postJSON(t, p, "/panel/api/tasks/run_queue", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		st := waitQueueIdle(t, p)
		it := queueItemByCode(t, queueItemsOf(t, st), "school_season")
		msg, _ := it["message"].(string)
		if it["status"] != "done" || !strings.Contains(msg, "已领取奖励（+100c +5e）") || !strings.Contains(msg, "（进度 1/1）") {
			t.Fatalf("mp 任务结果不对: %v", it)
		}
		claims := up.requestsTo(http.MethodPost, autoWebHost,
			"/activity/growth/tasks/school_season/claim", "miniprogram")
		if len(claims) != 1 {
			t.Fatalf("应走 ClaimRewardMP 一次，实际 %d", len(claims))
		}
	})

	t.Run("没有自动动作的已达标任务只领奖", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(
			autoTaskJSON("no_action_done", 1, 1, "accepted"),
			autoTaskJSON("no_action_old", 1, 1, "accepted"),
			autoTaskJSON("no_action_bad", 1, 1, "accepted"),
		)))
		up.onMPTasks(autoOK(autoTasksJSON(
			autoTaskJSON("no_action_done", 1, 1, "accepted"), // 和默认列表重复，按默认口径领
			autoTaskJSON("mp_only_done", 1, 1, "accepted"),
		)))
		up.onWebClaim("no_action_done", autoOK(autoClaimJSON(50, 5)))
		up.onWebClaim("no_action_old", autoOK(autoEnvelope(`{"already_claimed":true}`)))
		up.onWebClaim("no_action_bad", autoFail(500, `{"code":500,"msg":"claim boom"}`))
		up.onMPClaim("mp_only_done", autoOK(autoClaimJSON(20, 0)))

		p.RunGrowthQueueOnce()
		items := queueItemsOf(t, waitQueueIdle(t, p))
		want := map[string][2]string{
			"no_action_done": {"done", "已领奖 +50 分 +5 能"},
			"no_action_old":  {"done", "奖励此前已领取"},
			"mp_only_done":   {"done", "已领奖 +20 分 +0 能"},
		}
		for code, w := range want {
			it := queueItemByCode(t, items, code)
			if it["kind"] != "claim" || it["status"] != w[0] || it["message"] != w[1] {
				t.Fatalf("%s 结果不对: %v", code, it)
			}
		}
		if bad := queueItemByCode(t, items, "no_action_bad"); bad["status"] != "error" || !strings.Contains(fmt.Sprint(bad["message"]), "claim boom") {
			t.Fatalf("领奖失败应记 error 并带上错误: %v", bad)
		}
		if n := len(up.requestsTo(http.MethodPost, autoWebHost, "/activity/growth/tasks/no_action_done/claim", "web")); n != 1 {
			t.Fatalf("默认列表的任务走网页口径领一次，实际 %d", n)
		}
		if n := len(up.requestsTo(http.MethodPost, autoWebHost, "/activity/growth/tasks/mp_only_done/claim", "miniprogram")); n != 1 {
			t.Fatalf("只在小程序列表里的任务走小程序口径领一次，实际 %d", n)
		}
	})

	t.Run("RunGrowthQueueOnce 有待办时启动", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("chat_5", 1, 1, "accepted"))))
		up.onMPTasks(autoOK(autoTasksJSON()))
		up.onWebClaim("chat_5", autoOK(autoClaimJSON(100, 5)))

		p.RunGrowthQueueOnce()
		st := queueNow(t, p)
		if st["seq"] != float64(1) || st["total"] != float64(1) {
			t.Fatalf("RunGrowthQueueOnce 应启动一轮队列: %v", st)
		}
		st = waitQueueIdle(t, p)
		if it := queueItemByCode(t, queueItemsOf(t, st), "chat_5"); it["status"] != "done" {
			t.Fatalf("定时队列应执行完: %v", it)
		}
	})

	t.Run("RunGrowthQueueOnce 无待办不启动", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON()))
		up.onMPTasks(autoOK(autoTasksJSON()))
		p.RunGrowthQueueOnce()
		st := queueNow(t, p)
		if st["running"] != false || st["seq"] != float64(0) {
			t.Fatalf("无待办不应启动队列: %v", st)
		}
	})
}

// 我的券码：global 行标错且不发请求；单账号失败只影响该行；禁用账号不出现。
func TestSchoolVouchers(t *testing.T) {
	autoFastWaits(t)
	global := taskTestAuth("u3", "国际号")
	if _, err := auth.BackfillRealmFor(global, "global"); err != nil {
		t.Fatalf("构造 global 账号失败: %v", err)
	}
	p, up := newTasksPanel(t,
		taskTestAuth("u1", "一号"),
		taskTestAuth("u2", "二号"),
		global,
		taskTestAuth("u4", "四号"),
	)
	p.cfg.Pool.Disable("u4", "测试禁用")

	schoolPath := "/portal/activity/school/vouchers"
	up.on(http.MethodGet, autoBillingHost, schoolPath, "", func(r autoReq) (int, string) {
		switch r.Header.Get("X-User-Id") {
		case "u1":
			return 200, autoEnvelope(`{"items":[{"grant_id":7,"sku_code":"kfc_ice_cream","prize_name":"肯德基冰淇淋","code":"KFC-123","valid_to":"2026-10-24"}]}`)
		case "u2":
			return 400, `{"code":400,"msg":"voucher boom"}`
		default:
			return 200, autoEnvelope(`{"items":[]}`)
		}
	})

	rec := get(p, http.MethodGet, "/panel/api/school/vouchers")
	autoWantCode(t, rec, http.StatusOK)
	resp := autoDecode(t, rec)
	rows, _ := resp["accounts"].([]any)
	if len(rows) != 3 {
		t.Fatalf("禁用账号应被剔除，应有 3 行: %v", resp)
	}
	byUID := map[string]map[string]any{}
	for _, v := range rows {
		m, _ := v.(map[string]any)
		byUID[fmt.Sprint(m["uid"])] = m
	}
	u1 := byUID["u1"]
	if err, _ := u1["error"].(string); err != "" {
		t.Fatalf("u1 不应有错误: %v", u1)
	}
	vs, _ := u1["vouchers"].([]any)
	if len(vs) != 1 {
		t.Fatalf("u1 应返回 1 张券: %v", u1)
	}
	v, _ := vs[0].(map[string]any)
	if v["code"] != "KFC-123" || v["prize_name"] != "肯德基冰淇淋" {
		t.Fatalf("券字段不对: %v", v)
	}
	if msg, _ := byUID["u2"]["error"].(string); msg == "" {
		t.Fatalf("u2 查询失败应只在该行带 error: %v", byUID["u2"])
	}
	if msg, _ := byUID["u3"]["error"].(string); msg != "global realm（无开学季活动）" {
		t.Fatalf("global 行应标无活动: %v", byUID["u3"])
	}
	if _, ok := byUID["u4"]; ok {
		t.Fatalf("禁用账号不应出现: %v", byUID["u4"])
	}
	for _, r := range up.requests() {
		if uid := r.Header.Get("X-User-Id"); uid == "u3" || uid == "u4" {
			t.Fatalf("global/禁用账号不应发券码请求，收到 uid=%s", uid)
		}
	}
}
