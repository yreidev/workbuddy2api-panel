// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

// tasks_test.go 面板「积分任务」接口（tasks.go）的测试：列表合并、接受、
// 批量接受分片/失败、领奖的小程序口径与错误分支。
//
// 上游一律经 autotask_test.go 的假 Transport 接住，按「方法 + 主机 + 路径 +
// X-Client-Platform」分发；未登记的请求会由假上游 t.Errorf 并回 400。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// newTasksPanel 构建多账号面板 + 假上游（tasks / taskcenter 测试共用）。
func newTasksPanel(t *testing.T, auths ...*auth.Auth) (*Panel, *autoUpstream) {
	t.Helper()
	up := &autoUpstream{t: t, route: map[string]autoHandler{}}
	p := pool.New("")
	for _, a := range auths {
		p.Add(a)
	}
	panel := New(Config{
		Version: "test",
		Pool:    p,
		Upstream: &upstream.Client{
			HTTP:          &http.Client{Transport: up},
			ChatBaseCN:    "https://" + autoChatHost,
			BillingBaseCN: "https://" + autoBillingHost,
			WebBaseCN:     "https://" + autoWebHost,
		},
		AuthDir: t.TempDir(),
	})
	return panel, up
}

// taskTestAuth 构造 CN 个人号凭证。
func taskTestAuth(uid, nick string) *auth.Auth {
	return &auth.Auth{UID: uid, AccessToken: "at-" + uid, RefreshToken: "rt-" + uid, Nickname: nick}
}

// 四个任务接口对未知 uid 一律 404，且不发上游请求。
func TestTasksUnknownAccount(t *testing.T) {
	p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
	paths := []struct {
		name string
		path string
		body string
	}{
		{"列表", "/panel/api/accounts/nope/tasks", ""},
		{"接受", "/panel/api/accounts/nope/tasks/accept", `{"task_codes":["chat_5"]}`},
		{"全部接受", "/panel/api/accounts/nope/tasks/accept_all", `{}`},
		{"领奖", "/panel/api/accounts/nope/tasks/claim", `{"task_code":"chat_5"}`},
	}
	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			var rec = get(p, http.MethodGet, tc.path)
			if tc.body != "" {
				rec = postJSON(t, p, tc.path, tc.body)
			}
			autoWantCode(t, rec, http.StatusNotFound)
		})
	}
	if n := len(up.requests()); n != 0 {
		t.Fatalf("未知账号不应发出上游请求，实际 %d 个", n)
	}
}

// 任务列表：默认与小程序列表按 task_code 去重合并；小程序失败静默；
// 默认列表失败 502。
func TestAccountTasksMerge(t *testing.T) {
	autoFastWaits(t)

	t.Run("两个口径去重合并", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(
			autoTaskJSON("t_a", 1, 0, "accepted"),
			autoTaskJSON("t_b", 1, 0, "accepted"),
		)))
		up.onMPTasks(autoOK(autoTasksJSON(
			autoTaskJSON("t_b", 1, 0, "accepted"), // 重复：不再追加
			autoTaskJSON("t_c", 1, 0, "accepted"),
		)))
		rec := get(p, http.MethodGet, "/panel/api/accounts/u1/tasks")
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		raw, ok := resp["tasks"].([]any)
		if !ok {
			t.Fatalf("响应没有 tasks 数组: %v", resp)
		}
		var got []string
		for _, v := range raw {
			m, _ := v.(map[string]any)
			got = append(got, fmt.Sprint(m["task_code"]))
		}
		if strings.Join(got, ",") != "t_a,t_b,t_c" {
			t.Fatalf("合并/去重结果不对: %v", got)
		}
	})

	t.Run("小程序列表失败只返回默认列表", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("t_a", 1, 0, "accepted"))))
		up.onMPTasks(autoFail(400, `{"code":400}`))
		rec := get(p, http.MethodGet, "/panel/api/accounts/u1/tasks")
		autoWantCode(t, rec, http.StatusOK)
		raw, _ := autoDecode(t, rec)["tasks"].([]any)
		if len(raw) != 1 {
			t.Fatalf("mp 列表失败时应只返回默认列表，实际 %d 项", len(raw))
		}
	})

	t.Run("默认列表失败 502", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoFail(400, `{"code":400,"msg":"boom"}`))
		rec := get(p, http.MethodGet, "/panel/api/accounts/u1/tasks")
		autoWantCode(t, rec, http.StatusBadGateway)
		if txt := autoErrText(t, rec); !strings.Contains(txt, "list tasks") {
			t.Fatalf("错误信息应含 list tasks: %q", txt)
		}
	})
}

// 接受任务：坏 body 400、上游失败 502（含 accept）、成功把 task_codes 原样带给上游。
func TestAccountTaskAccept(t *testing.T) {
	autoFastWaits(t)

	t.Run("坏 body 与空 task_codes", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		for _, body := range []string{`{broken`, `{}`, `{"task_codes":[]}`} {
			rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/accept", body)
			autoWantCode(t, rec, http.StatusBadRequest)
		}
		if n := len(up.requests()); n != 0 {
			t.Fatalf("坏 body 不应发请求，实际 %d 个", n)
		}
	})

	t.Run("上游失败 502", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onAccept(autoFail(400, `{"code":400,"msg":"accept boom"}`))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/accept", `{"task_codes":["chat_5"]}`)
		autoWantCode(t, rec, http.StatusBadGateway)
		if txt := autoErrText(t, rec); !strings.Contains(txt, "accept") {
			t.Fatalf("错误信息应含 accept: %q", txt)
		}
	})

	t.Run("成功透传 task_codes", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onAccept(autoOK(autoEnvelope("{}")))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/accept", `{"task_codes":["chat_5","RichMeow_Chat"]}`)
		autoWantCode(t, rec, http.StatusOK)
		if ok, _ := autoDecode(t, rec)["ok"].(bool); !ok {
			t.Fatalf("成功响应应 ok=true: %s", rec.Body)
		}
		reqs := up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "web")
		if len(reqs) != 1 {
			t.Fatalf("应发一次 accept，实际 %d", len(reqs))
		}
		var body struct {
			TaskCodes []string `json:"task_codes"`
		}
		if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
			t.Fatalf("accept 请求体不是 JSON: %v", err)
		}
		if strings.Join(body.TaskCodes, ",") != "chat_5,RichMeow_Chat" {
			t.Fatalf("task_codes 未原样透传: %v", body.TaskCodes)
		}
	})
}

// 全部接受：跳过规则、超过 20 个分片、单批失败、小程序口径。
func TestTaskAcceptAllBatches(t *testing.T) {
	autoFastWaits(t)

	t.Run("没有要接受的任务", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(
			autoTaskJSON("t_claimed", 1, 1, "claimed"),
			`{"task_code":"t_locked","target":1,"current":0,"accept_status":"not_accepted","locked":true}`,
			autoTaskJSON("t_accepted", 1, 0, "accepted"),
			autoTaskJSON("t_completed", 1, 0, "completed"),
		)))
		up.onMPTasks(autoOK(autoTasksJSON()))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/accept_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["accepted"] != float64(0) || resp["message"] != "所有任务均已接受" {
			t.Fatalf("无待接受任务时结果不对: %v", resp)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "web")); got != 0 {
			t.Fatalf("无待接受任务不应发 accept，实际 %d 次", got)
		}
	})

	t.Run("超过 20 个按 20 一批", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		var items []string
		for i := 0; i < 25; i++ {
			items = append(items, autoTaskJSON(fmt.Sprintf("t%02d", i), 1, 0, "not_accepted"))
		}
		up.onDefaultTasks(autoOK(autoTasksJSON(items...)))
		up.onMPTasks(autoOK(autoTasksJSON()))
		var batches [][]string
		up.onAccept(func(r autoReq) (int, string) {
			var body struct {
				TaskCodes []string `json:"task_codes"`
			}
			_ = json.Unmarshal(r.Body, &body)
			batches = append(batches, body.TaskCodes)
			return 200, autoEnvelope("{}")
		})
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/accept_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		if got := autoDecode(t, rec)["accepted"]; got != float64(25) {
			t.Fatalf("accepted 应为 25: %v", got)
		}
		if len(batches) != 2 || len(batches[0]) != 20 || len(batches[1]) != 5 {
			t.Fatalf("应分 20+5 两批，实际 %v", batches)
		}
		if batches[0][0] != "t00" || batches[0][19] != "t19" || batches[1][0] != "t20" || batches[1][4] != "t24" {
			t.Fatalf("分批内容不对: %v", batches)
		}
	})

	t.Run("其中一批失败", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		var items []string
		for i := 0; i < 25; i++ {
			items = append(items, autoTaskJSON(fmt.Sprintf("t%02d", i), 1, 0, "not_accepted"))
		}
		up.onDefaultTasks(autoOK(autoTasksJSON(items...)))
		up.onMPTasks(autoOK(autoTasksJSON()))
		n := 0
		up.onAccept(func(autoReq) (int, string) {
			n++
			if n == 1 {
				return 400, `{"code":400,"msg":"batch boom"}`
			}
			return 200, autoEnvelope("{}")
		})
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/accept_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["accepted"] != float64(5) {
			t.Fatalf("只应计入成功批的 5 个: %v", resp)
		}
		msg, _ := resp["message"].(string)
		if !strings.Contains(msg, "部分任务接受失败") {
			t.Fatalf("应带部分失败提示: %v", resp)
		}
		failed, _ := resp["failed"].([]any)
		if len(failed) != 20 || fmt.Sprint(failed[0]) != "t00" || fmt.Sprint(failed[19]) != "t19" {
			t.Fatalf("失败批的码应进 failed: %v", failed)
		}
	})

	t.Run("默认列表都接受过、小程序列表还有没接受的", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("t_def", 1, 0, "accepted"))))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("mp_a", 1, 0, "not_accepted"))))
		var mpCodes []string
		up.onAcceptMP(func(r autoReq) (int, string) {
			var body struct {
				TaskCodes []string `json:"task_codes"`
			}
			_ = json.Unmarshal(r.Body, &body)
			mpCodes = body.TaskCodes
			return 200, autoEnvelope("{}")
		})
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/accept_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		if resp := autoDecode(t, rec); resp["accepted"] != float64(1) || resp["message"] != nil {
			t.Fatalf("应接受 1 个小程序任务: %v", resp)
		}
		if strings.Join(mpCodes, ",") != "mp_a" {
			t.Fatalf("mp 接受内容不对: %v", mpCodes)
		}
		if got := len(up.requestsTo(http.MethodPost, autoWebHost, autoAcceptPath, "web")); got != 0 {
			t.Fatalf("默认列表没有待接受的，不应发默认口径的 accept，实际 %d 次", got)
		}
	})

	t.Run("小程序列表单独接受", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("t_def", 1, 0, "not_accepted"))))
		up.onMPTasks(autoOK(autoTasksJSON(
			autoTaskJSON("mp_a", 1, 0, "not_accepted"),
			autoTaskJSON("mp_b", 1, 0, "not_accepted"),
			autoTaskJSON("mp_done", 1, 0, "completed"),
		)))
		up.onAccept(autoOK(autoEnvelope("{}")))
		var mpCodes []string
		up.onAcceptMP(func(r autoReq) (int, string) {
			var body struct {
				TaskCodes []string `json:"task_codes"`
			}
			_ = json.Unmarshal(r.Body, &body)
			mpCodes = body.TaskCodes
			return 200, autoEnvelope("{}")
		})
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/accept_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		if got := autoDecode(t, rec)["accepted"]; got != float64(3) {
			t.Fatalf("accepted 应含默认 1 个 + mp 2 个: %v", got)
		}
		if strings.Join(mpCodes, ",") != "mp_a,mp_b" {
			t.Fatalf("mp 接受内容不对: %v", mpCodes)
		}
	})

	t.Run("小程序接受失败进 failed", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onDefaultTasks(autoOK(autoTasksJSON(autoTaskJSON("t_def", 1, 0, "not_accepted"))))
		up.onMPTasks(autoOK(autoTasksJSON(autoTaskJSON("mp_a", 1, 0, "not_accepted"))))
		up.onAccept(autoOK(autoEnvelope("{}")))
		up.onAcceptMP(autoFail(400, `{"code":400,"msg":"mp boom"}`))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/accept_all", `{}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["accepted"] != float64(1) {
			t.Fatalf("只应计入默认列表成功的 1 个: %v", resp)
		}
		failed, _ := resp["failed"].([]any)
		if len(failed) != 1 || fmt.Sprint(failed[0]) != "mp_a" {
			t.Fatalf("mp 码应进 failed: %v", failed)
		}
	})
}

// 领奖：缺 task_code 400、小程序码走 ClaimRewardMP、上游失败 502、
// 零奖励 already_claimed、有奖励返回 credit/energy。
func TestAccountTaskClaim(t *testing.T) {
	autoFastWaits(t)

	t.Run("缺 task_code", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		for _, body := range []string{`{broken`, `{}`} {
			autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/tasks/claim", body), http.StatusBadRequest)
		}
		if n := len(up.requests()); n != 0 {
			t.Fatalf("坏 body 不应发请求，实际 %d 个", n)
		}
	})

	t.Run("小程序任务码走 mp 领奖", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onMPClaim("Sequential_Tasks_1", autoOK(autoClaimJSON(100, 5)))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/claim", `{"task_code":"Sequential_Tasks_1"}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["credit"] != float64(100) || resp["energy"] != float64(5) {
			t.Fatalf("奖励数不对: %v", resp)
		}
		claims := up.requestsTo(http.MethodPost, autoWebHost,
			"/activity/growth/tasks/Sequential_Tasks_1/claim", "miniprogram")
		if len(claims) != 1 {
			t.Fatalf("应走 ClaimRewardMP 一次，实际 %d", len(claims))
		}
	})

	t.Run("上游失败 502", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onWebClaim("chat_5", autoFail(400, `{"code":400,"msg":"claim boom"}`))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/claim", `{"task_code":"chat_5"}`)
		autoWantCode(t, rec, http.StatusBadGateway)
		if txt := autoErrText(t, rec); !strings.Contains(txt, "claim") {
			t.Fatalf("错误信息应含 claim: %q", txt)
		}
	})

	t.Run("奖励都是 0 视为已领取", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onWebClaim("chat_5", autoOK(autoClaimJSON(0, 0)))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/claim", `{"task_code":"chat_5"}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["already_claimed"] != true || resp["message"] != "该奖励此前已领取" {
			t.Fatalf("零奖励应提示已领取: %v", resp)
		}
	})

	t.Run("有奖励返回 credit 与 energy", func(t *testing.T) {
		p, up := newTasksPanel(t, taskTestAuth("u1", "一号"))
		up.onWebClaim("chat_5", autoOK(autoClaimJSON(120, 3)))
		rec := postJSON(t, p, "/panel/api/accounts/u1/tasks/claim", `{"task_code":"chat_5"}`)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["ok"] != true || resp["credit"] != float64(120) || resp["energy"] != float64(3) {
			t.Fatalf("奖励结果不对: %v", resp)
		}
	})
}
