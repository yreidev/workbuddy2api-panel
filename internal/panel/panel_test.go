// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

// panel_test.go 面板其余接口（panel.go）：总览、日志、请求指标、模型页、
// 账号运维、批量触发、用量、积分包、券码以外的基础接口，以及 helper 的边界。
//
// 已有测试覆盖的接口不重复（见 notes.md 的接口表格）；上游一律经
// autotask_test.go 的假 Transport 接住。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/scheduler"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

// newPanelFor 构建带假上游的面板；cfg 里的 Pool/Upstream/AuthDir/Version
// 缺省时自动补齐。
func newPanelFor(t *testing.T, cfg Config) (*Panel, *autoUpstream) {
	t.Helper()
	up := &autoUpstream{t: t, route: map[string]autoHandler{}}
	if cfg.Pool == nil {
		cfg.Pool = pool.New("")
	}
	if cfg.Upstream == nil {
		cfg.Upstream = &upstream.Client{
			HTTP:          &http.Client{Transport: up},
			ChatBaseCN:    "https://" + autoChatHost,
			BillingBaseCN: "https://" + autoBillingHost,
			WebBaseCN:     "https://" + autoWebHost,
		}
	} else {
		cfg.Upstream.HTTP = &http.Client{Transport: up}
	}
	if cfg.AuthDir == "" {
		cfg.AuthDir = t.TempDir()
	}
	if cfg.Version == "" {
		cfg.Version = "test"
	}
	return New(cfg), up
}

// mustStatus 取号池状态，不存在直接失败。
func mustStatus(t *testing.T, p *Panel, uid string) pool.Status {
	t.Helper()
	st, ok := p.cfg.Pool.Status(uid)
	if !ok {
		t.Fatalf("号池里没有 %s", uid)
	}
	return st
}

// 解锁一个从未加锁的账号不应 panic（锁表里没有条目时是空操作）。
func TestUnlockAccountWithoutLock(t *testing.T) {
	p := &Panel{}
	p.unlockAccount("never-locked")
	if p.taskLocks != nil {
		if mu := p.taskLocks["never-locked"]; mu != nil {
			t.Fatal("解锁未加锁账号不应创建锁条目")
		}
	}
}

// expiringSoonWindow：无调度器为 0；有调度器取当前窗口，热改后立即生效。
func TestExpiringSoonWindow(t *testing.T) {
	p := &Panel{}
	if got := p.expiringSoonWindow(); got != 0 {
		t.Fatalf("无调度器应为 0，got %v", got)
	}
	s := scheduler.New(scheduler.Config{ExpiringSoonWindow: 12 * time.Hour})
	p.cfg.Scheduler = s
	if got := p.expiringSoonWindow(); got != 12*time.Hour {
		t.Fatalf("应取调度器窗口，got %v", got)
	}
	s.SetExpiringSoonWindow(90 * time.Minute)
	if got := p.expiringSoonWindow(); got != 90*time.Minute {
		t.Fatalf("热改后应生效，got %v", got)
	}
}

// apiKey：Live 配置优先于静态字段；Live 存在但为空也不回落静态值。
func TestPanelAPIKeyLive(t *testing.T) {
	staticOnly := New(Config{Pool: pool.New(""), APIKey: "static-key"})
	if got := staticOnly.apiKey(); got != "static-key" {
		t.Fatalf("无 Live 时应取静态密钥，got %q", got)
	}
	holder := livecfg.New(livecfg.Snapshot{APIKey: "live-key"})
	p := New(Config{Pool: pool.New(""), APIKey: "static-key", Live: holder})
	if got := p.apiKey(); got != "live-key" {
		t.Fatalf("Live 应优先，got %q", got)
	}
	holder.Store(livecfg.Snapshot{})
	if got := p.apiKey(); got != "" {
		t.Fatalf("Live 为空时不应回落静态密钥，got %q", got)
	}
}

// writeJSON / writeErr：Content-Type 与状态码。
func TestWriteJSONHelpers(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusCreated, map[string]any{"a": 1})
	if rr.Code != http.StatusCreated {
		t.Fatalf("code=%d want 201", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type=%q", ct)
	}
	if strings.TrimSpace(rr.Body.String()) != `{"a":1}` {
		t.Fatalf("body=%s", rr.Body)
	}

	rr = httptest.NewRecorder()
	writeErr(rr, http.StatusTeapot, "boom")
	if rr.Code != http.StatusTeapot {
		t.Fatalf("code=%d want 418", rr.Code)
	}
	resp := autoDecode(t, rr)
	if resp["ok"] != false || resp["error"] != "boom" {
		t.Fatalf("writeErr body=%v", resp)
	}
}

// 总览：计数、粘性会话、模型锁、auth_required。
func TestPanelOverview(t *testing.T) {
	autoFastWaits(t)

	t.Run("计数与粘性会话", func(t *testing.T) {
		p, up := newPanelFor(t, Config{
			Pool:        pool.New(""),
			StickyCount: func() int { return 3 },
		})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
		p.cfg.Pool.Add(taskTestAuth("u2", "二号"))
		p.cfg.Pool.Disable("u2", "测试禁用")
		rec := get(p, http.MethodGet, "/panel/api/overview")
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["version"] != "test" || resp["redis_mode"] != "noop" || resp["auth_required"] != false {
			t.Fatalf("元信息不对: %v", resp)
		}
		if resp["sticky_sessions"] != float64(3) || resp["total"] != float64(2) ||
			resp["disabled"] != float64(1) || resp["healthy"] != float64(1) {
			t.Fatalf("计数不对: %v", resp)
		}
		if accounts, _ := resp["accounts"].([]any); len(accounts) != 2 {
			t.Fatalf("accounts 应有 2 个: %v", resp)
		}
		if resp["model_locks"] != nil {
			t.Fatalf("无模型锁时应为 null: %v", resp["model_locks"])
		}
		if n := len(up.requests()); n != 0 {
			t.Fatalf("总览不应发上游请求，实际 %d", n)
		}
	})

	t.Run("StickyCount 为 nil 时报告 0", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{})
		resp := autoDecode(t, get(p, http.MethodGet, "/panel/api/overview"))
		if resp["sticky_sessions"] != float64(0) {
			t.Fatalf("应报告 0: %v", resp)
		}
	})

	t.Run("配置了密钥时 auth_required=true", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{APIKey: "secret"})
		rec := get(p, http.MethodGet, "/panel/api/overview", "Authorization", "Bearer secret")
		autoWantCode(t, rec, http.StatusOK)
		if autoDecode(t, rec)["auth_required"] != true {
			t.Fatalf("auth_required 应为 true")
		}
	})
}

// 日志快照：按行入环并按前缀分频道。
func TestPanelLogs(t *testing.T) {
	p, _ := newPanelFor(t, Config{})
	if p.Logs() == nil {
		t.Fatal("Logs() 应返回环形缓冲")
	}
	_, _ = p.logs.Write([]byte("checkin u1: 签到成功\nplain sys line\n"))
	rec := get(p, http.MethodGet, "/panel/api/logs")
	autoWantCode(t, rec, http.StatusOK)
	entries, _ := autoDecode(t, rec)["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("应有 2 条日志: %v", entries)
	}
	want := []string{ChTask, ChSys}
	for i, v := range entries {
		m, _ := v.(map[string]any)
		if m["ch"] != want[i] {
			t.Fatalf("第 %d 条频道 = %v want %s", i, m["ch"], want[i])
		}
		if m["text"] == "" {
			t.Fatalf("第 %d 条正文为空: %v", i, m)
		}
	}
}

// 请求指标与归档列表：nil 记录器 501；有记录器时返回快照；limit 夹取。
func TestPanelRequestMetricsLogs(t *testing.T) {
	t.Run("nil 记录器 501", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{})
		autoWantCode(t, get(p, http.MethodGet, "/panel/api/request_metrics"), http.StatusNotImplemented)
		autoWantCode(t, get(p, http.MethodGet, "/panel/api/request_logs"), http.StatusNotImplemented)
	})

	t.Run("指标快照与 limit 夹取", func(t *testing.T) {
		rec := reqlog.New(reqlog.Config{})
		rec.Record(reqlog.Event{Status: 200, OK: true, DurationMs: 10})
		p, _ := newPanelFor(t, Config{RequestLog: rec})

		mrec := get(p, http.MethodGet, "/panel/api/request_metrics")
		autoWantCode(t, mrec, http.StatusOK)
		metrics := autoDecode(t, mrec)
		if metrics["completed"] != float64(1) || metrics["succeeded"] != float64(1) {
			t.Fatalf("指标计数不对: %v", metrics)
		}
		if recent, _ := metrics["recent"].([]any); len(recent) != 1 {
			t.Fatalf("recent 应有 1 条: %v", metrics)
		}

		for query, wantLimit := range map[string]float64{
			"":            200,
			"?limit=5000": 1000,
			"?limit=-1":   200,
			"?limit=abc":  200,
			"?limit=50":   50,
		} {
			lrec := get(p, http.MethodGet, "/panel/api/request_logs"+query)
			autoWantCode(t, lrec, http.StatusOK)
			resp := autoDecode(t, lrec)
			if resp["limit"] != wantLimit {
				t.Fatalf("%q limit=%v want %v", query, resp["limit"], wantLimit)
			}
			if entries, ok := resp["entries"].([]any); !ok || len(entries) != 0 {
				t.Fatalf("%q 空归档应回 []: %v", query, resp)
			}
		}
	})

	t.Run("归档读取失败 500", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "archive")
		if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil { // 归档目录的位置被文件占了
			t.Fatal(err)
		}
		rec := reqlog.New(reqlog.Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 7})
		rec.Close()
		p, _ := newPanelFor(t, Config{RequestLog: rec})
		rec2 := get(p, http.MethodGet, "/panel/api/request_logs")
		autoWantCode(t, rec2, http.StatusInternalServerError)
	})
}

// 模型页：无号 503；CN 个人号拉目录成功；目录拉取失败 502；global 探测失败 502。
func TestPanelModels(t *testing.T) {
	t.Run("没有可用账号 503", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{})
		autoWantCode(t, get(p, http.MethodGet, "/panel/api/models"), http.StatusServiceUnavailable)
	})

	t.Run("CN 个人号目录成功", func(t *testing.T) {
		p, up := newPanelFor(t, Config{Pool: pool.New("")})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
		// 个人号只拉 /v3/config。
		up.on(http.MethodGet, autoChatHost, "/v3/config", "", autoOK(
			`{"code":0,"data":{"models":[{"id":"glm-5.2","name":"GLM-5.2","maxInputTokens":1000000,`+
				`"maxOutputTokens":131072,"reasoning":{"supportedEfforts":["low","high"],"defaultEffort":"high"}}]}}`))

		rec := get(p, http.MethodGet, "/panel/api/models")
		autoWantCode(t, rec, http.StatusOK)
		models, _ := autoDecode(t, rec)["models"].([]any)
		if len(models) != 1 {
			t.Fatalf("应返回 1 个模型: %v", models)
		}
		m, _ := models[0].(map[string]any)
		if m["id"] != "cn:glm-5.2" || m["context_length"] != float64(1000000) ||
			m["max_output_tokens"] != float64(131072) {
			t.Fatalf("模型字段不对: %v", m)
		}
		efforts, _ := m["supported_efforts"].([]any)
		if len(efforts) != 2 || efforts[0] != "low" || m["default_effort"] != "high" {
			t.Fatalf("档位字段不对: %v", m)
		}
	})

	t.Run("上游目录失败 502", func(t *testing.T) {
		p, up := newPanelFor(t, Config{Pool: pool.New("")})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
		up.on(http.MethodGet, autoChatHost, "/v3/config", "", autoFail(400, `{"code":400}`))
		rec := get(p, http.MethodGet, "/panel/api/models")
		autoWantCode(t, rec, http.StatusBadGateway)
		if txt := autoErrText(t, rec); !strings.Contains(txt, "fetch models") {
			t.Fatalf("错误信息不对: %q", txt)
		}
	})

	t.Run("仅 global 且探测失败 502", func(t *testing.T) {
		g := taskTestAuth("g1", "国际号")
		if _, err := auth.BackfillRealmFor(g, "global"); err != nil {
			t.Fatal(err)
		}
		p, up := newPanelFor(t, Config{Pool: pool.New("")})
		p.cfg.Pool.Add(g)
		p.cfg.Upstream.GlobalEnabled = true
		p.cfg.Upstream.ChatBaseGlobal = "https://" + loginGlobalHost
		up.on(http.MethodGet, loginGlobalHost, "/v3/config", "", autoFail(400, `{"code":400}`))
		rec := get(p, http.MethodGet, "/panel/api/models")
		autoWantCode(t, rec, http.StatusBadGateway)
		if txt := autoErrText(t, rec); !strings.Contains(txt, "global: 上游未返回可用模型") {
			t.Fatalf("错误信息不对: %q", txt)
		}
	})

	t.Run("global 目录成功", func(t *testing.T) {
		g := taskTestAuth("g1", "国际号")
		if _, err := auth.BackfillRealmFor(g, "global"); err != nil {
			t.Fatal(err)
		}
		p, up := newPanelFor(t, Config{Pool: pool.New("")})
		p.cfg.Pool.Add(g)
		p.cfg.Upstream.GlobalEnabled = true
		p.cfg.Upstream.ChatBaseGlobal = "https://" + loginGlobalHost
		up.on(http.MethodGet, loginGlobalHost, "/v3/config", "", autoOK(
			`{"code":0,"data":{"models":[{"id":"gpt-5.5","maxInputTokens":1050000,"maxOutputTokens":128000,`+
				`"reasoning":{"supportedEfforts":["low","high"],"defaultEffort":"high"}}]}}`))

		rec := get(p, http.MethodGet, "/panel/api/models")
		autoWantCode(t, rec, http.StatusOK)
		models, _ := autoDecode(t, rec)["models"].([]any)
		if len(models) == 0 {
			t.Fatalf("global 应返回模型: %s", rec.Body)
		}
		m, _ := models[0].(map[string]any)
		if m["id"] != "global:gpt-5.5" || m["context_length"] != float64(1050000) {
			t.Fatalf("global 模型字段不对: %v", m)
		}
	})
}

// 账号运维：解冻 / 禁用 / 暂停 / 恢复的状态副作用与未知 uid 404。
func TestPanelAccountOps(t *testing.T) {
	t.Run("未知 uid 全部 404", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{})
		for _, action := range []string{"revive", "disable", "pause", "resume"} {
			rec := postJSON(t, p, "/panel/api/accounts/nope/"+action, ``)
			autoWantCode(t, rec, http.StatusNotFound)
		}
	})

	t.Run("解冻与禁用", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
		p.cfg.Pool.Disable("u1", "测试禁用")
		rec := postJSON(t, p, "/panel/api/accounts/u1/revive", ``)
		autoWantCode(t, rec, http.StatusOK)
		if st := mustStatus(t, p, "u1"); st.Disabled {
			t.Fatalf("revive 后应恢复: %+v", st)
		}
		rec = postJSON(t, p, "/panel/api/accounts/u1/disable", ``)
		autoWantCode(t, rec, http.StatusOK)
		st := mustStatus(t, p, "u1")
		if !st.Disabled || st.DisabledReason != "manual disable (panel)" {
			t.Fatalf("disable 后状态不对: %+v", st)
		}
	})

	t.Run("暂停与恢复", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/pause", ``), http.StatusOK)
		if st := mustStatus(t, p, "u1"); !st.Paused {
			t.Fatalf("pause 后应暂停: %+v", st)
		}
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/resume", ``), http.StatusOK)
		if st := mustStatus(t, p, "u1"); st.Paused {
			t.Fatalf("resume 后应恢复: %+v", st)
		}
		// 未暂停账号再 resume：幂等成功。
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/resume", ``), http.StatusOK)
	})
}

// 账号备注：去掉首尾空白后存下、正好 40 个字可以、空串清除；没有 note 字段、有换行、超长、
// 账号不存在都拒绝，原来的备注不动。
func TestPanelAccountNote(t *testing.T) {
	p, _ := newPanelFor(t, Config{})
	p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
	note := func() string { return mustStatus(t, p, "u1").Note }
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/note", `{"note":"  主力号 "}`), http.StatusOK)
	if got := note(); got != "主力号" {
		t.Fatalf("备注 = %q，应为 主力号", got)
	}
	for _, tc := range []struct {
		name, path, body, want string
		code                   int
	}{
		{"没有 note 字段", "u1", `{}`, "note 字段", http.StatusBadRequest},
		{"不是 JSON", "u1", `x`, "note 字段", http.StatusBadRequest},
		{"有换行", "u1", `{"note":"a\nb"}`, "换行", http.StatusBadRequest},
		{"超过 40 个字", "u1", `{"note":"` + strings.Repeat("号", 41) + `"}`, "40 个字", http.StatusBadRequest},
		{"账号不存在", "nope", `{"note":"x"}`, "not found", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postJSON(t, p, "/panel/api/accounts/"+tc.path+"/note", tc.body)
			autoWantCode(t, rec, tc.code)
			if msg := autoErrText(t, rec); !strings.Contains(msg, tc.want) {
				t.Errorf("错误信息 = %q，应含 %q", msg, tc.want)
			}
			if got := note(); got != "主力号" {
				t.Errorf("被拒后备注变成了 %q", got)
			}
		})
	}
	full := strings.Repeat("号", 40)
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/note", `{"note":"`+full+`"}`), http.StatusOK)
	if got := note(); got != full {
		t.Fatalf("40 个字应能存下，实际 %q", got)
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/note", `{"note":""}`), http.StatusOK)
	if got := note(); got != "" {
		t.Fatalf("空串应清除备注，实际 %q", got)
	}
}

// 单号签到：个人号 / 已签到 / 普通失败 / 企业号 / 余额失败。
func TestPanelAccountCheckin(t *testing.T) {
	checkinPath := "/v2/billing/meter/daily-checkin"
	resourcePath := upstreamtest.FreePath
	enterprisePath := "/v2/billing/meter/get-enterprise-user-usage"

	t.Run("未知 uid 404", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{})
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/nope/checkin", ``), http.StatusNotFound)
	})

	t.Run("个人号成功", func(t *testing.T) {
		p, up := newPanelFor(t, Config{Pool: pool.New("")})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
		up.on(http.MethodPost, autoBillingHost, checkinPath, "", autoOK(autoEnvelope("{}")))
		up.on(http.MethodPost, autoBillingHost, resourcePath, "", autoOK(loginResourceBody(77, 100)))
		rec := postJSON(t, p, "/panel/api/accounts/u1/checkin", ``)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["checkin_done"] != true || resp["credits"] != float64(77) || resp["credits_total"] != float64(100) {
			t.Fatalf("签到结果不对: %v", resp)
		}
		if _, ok := resp["checkin_message"]; ok {
			t.Fatalf("成功时不应有 checkin_message: %v", resp)
		}
		if st := mustStatus(t, p, "u1"); st.Credits != 77 {
			t.Fatalf("池余额未更新: %+v", st)
		}
		if got := len(up.requestsTo(http.MethodPost, autoBillingHost, checkinPath, "")); got != 1 {
			t.Fatalf("应签到一次，实际 %d", got)
		}
	})

	t.Run("已签到按幂等成功", func(t *testing.T) {
		p, up := newPanelFor(t, Config{Pool: pool.New("")})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
		up.on(http.MethodPost, autoBillingHost, checkinPath, "",
			autoFail(400, `{"code":10001,"msg":"今天已签到"}`))
		up.on(http.MethodPost, autoBillingHost, resourcePath, "", autoOK(loginResourceBody(60, 100)))
		resp := autoDecode(t, postJSON(t, p, "/panel/api/accounts/u1/checkin", ``))
		if resp["checkin_done"] != true {
			t.Fatalf("已签到应 done=true: %v", resp)
		}
		if msg, _ := resp["checkin_message"].(string); !strings.Contains(msg, "已签到") {
			t.Fatalf("应带签到提示: %v", resp)
		}
	})

	t.Run("签到失败仍查余额", func(t *testing.T) {
		p, up := newPanelFor(t, Config{Pool: pool.New("")})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
		up.on(http.MethodPost, autoBillingHost, checkinPath, "",
			autoFail(400, `{"code":500,"msg":"checkin boom"}`))
		up.on(http.MethodPost, autoBillingHost, resourcePath, "", autoOK(loginResourceBody(60, 100)))
		resp := autoDecode(t, postJSON(t, p, "/panel/api/accounts/u1/checkin", ``))
		if resp["checkin_done"] != false || resp["credits"] != float64(60) {
			t.Fatalf("失败也应查余额: %v", resp)
		}
	})

	t.Run("企业号跳过签到", func(t *testing.T) {
		p, up := newPanelFor(t, Config{Pool: pool.New("")})
		ent := taskTestAuth("u2", "企业号")
		ent.EnterpriseID = "corp-9"
		p.cfg.Pool.Add(ent)
		up.on(http.MethodPost, autoBillingHost, enterprisePath, "", autoOK(autoEnvelope(`{"credit":10,"limitNum":50}`)))
		resp := autoDecode(t, postJSON(t, p, "/panel/api/accounts/u2/checkin", ``))
		if resp["enterprise"] != true || resp["credits"] != float64(40) {
			t.Fatalf("企业号结果不对: %v", resp)
		}
		if msg, _ := resp["checkin_message"].(string); !strings.Contains(msg, "无签到体系") {
			t.Fatalf("企业号应提示跳过签到: %v", resp)
		}
		if got := len(up.requestsTo(http.MethodPost, autoBillingHost, checkinPath, "")); got != 0 {
			t.Fatalf("企业号不应签到，实际 %d", got)
		}
	})

	// 国际版没有签到活动（上游 400「签到活动未开启或已过期」）：不发签到，只刷新余额。
	t.Run("国际版号跳过签到", func(t *testing.T) {
		p, up := newPanelFor(t, Config{Pool: pool.New(""), Upstream: &upstream.Client{
			BillingBaseCN: "https://" + autoBillingHost, BillingBaseGlobal: "https://" + autoBillingHost, GlobalEnabled: true,
		}})
		glb := taskTestAuth("g1", "国际号")
		glb.Domain = "www.workbuddy.ai"
		p.cfg.Pool.Add(glb)
		up.on(http.MethodPost, autoBillingHost, upstreamtest.FreePath, "", autoOK(loginResourceBody(475, 480)))
		resp := autoDecode(t, postJSON(t, p, "/panel/api/accounts/g1/checkin", ``))
		if resp["checkin_done"] != false || resp["credits"] != float64(475) {
			t.Fatalf("国际版号结果不对: %v", resp)
		}
		if msg, _ := resp["checkin_message"].(string); !strings.Contains(msg, "国际版没有签到活动") {
			t.Fatalf("国际版号应提示跳过签到: %v", resp)
		}
		// 两个域现在只有一条签到路径（/v2/billing/meter/daily-checkin），国际版照旧跳过。
		if got := len(up.requestsTo(http.MethodPost, autoBillingHost, upstreamtest.CheckinPath, "")); got != 0 {
			t.Fatalf("国际版号不应签到，实际 %d", got)
		}
	})

	t.Run("余额失败返回 balance_error", func(t *testing.T) {
		p, up := newPanelFor(t, Config{Pool: pool.New("")})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
		up.on(http.MethodPost, autoBillingHost, checkinPath, "", autoOK(autoEnvelope("{}")))
		up.on(http.MethodPost, autoBillingHost, resourcePath, "", autoFail(400, `{"code":400,"msg":"res boom"}`))
		rec := postJSON(t, p, "/panel/api/accounts/u1/checkin", ``)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["balance_error"] == nil || resp["balance_error"] == "" {
			t.Fatalf("应带 balance_error: %v", resp)
		}
		if _, ok := resp["credits"]; ok {
			t.Fatalf("余额失败不应返回 credits: %v", resp)
		}
	})
}

// 单号余额刷新：成功写回池、失败 502。
func TestPanelAccountBalance(t *testing.T) {
	resourcePath := upstreamtest.FreePath

	t.Run("未知 uid 404", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{})
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/nope/balance", ``), http.StatusNotFound)
	})

	t.Run("成功写回池", func(t *testing.T) {
		p, up := newPanelFor(t, Config{Pool: pool.New("")})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
		up.on(http.MethodPost, autoBillingHost, resourcePath, "", autoOK(loginResourceBody(55, 100)))
		rec := postJSON(t, p, "/panel/api/accounts/u1/balance", ``)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["credits"] != float64(55) || resp["credits_total"] != float64(100) {
			t.Fatalf("余额结果不对: %v", resp)
		}
		if st := mustStatus(t, p, "u1"); st.Credits != 55 {
			t.Fatalf("池余额未更新: %+v", st)
		}
	})

	t.Run("上游失败 502", func(t *testing.T) {
		p, up := newPanelFor(t, Config{Pool: pool.New("")})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
		up.on(http.MethodPost, autoBillingHost, resourcePath, "", autoFail(400, `{"code":400,"msg":"res boom"}`))
		rec := postJSON(t, p, "/panel/api/accounts/u1/balance", ``)
		autoWantCode(t, rec, http.StatusBadGateway)
		if txt := autoErrText(t, rec); !strings.Contains(txt, "user resource") {
			t.Fatalf("错误信息不对: %q", txt)
		}
	})
}

// 移除账号：出池 + 删文件；文件删除失败时仍出池并回 file_error。
func TestPanelAccountRemove(t *testing.T) {
	t.Run("未知 uid 404", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{})
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/nope/remove", ``), http.StatusNotFound)
	})

	t.Run("成功出池并删文件", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{Pool: pool.New("")})
		dir := t.TempDir()
		path := filepath.Join(dir, "workbuddy-u1.json")
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		a := taskTestAuth("u1", "一号")
		a.FilePath = path
		p.cfg.Pool.Add(a)
		rec := postJSON(t, p, "/panel/api/accounts/u1/remove", ``)
		autoWantCode(t, rec, http.StatusOK)
		if autoDecode(t, rec)["ok"] != true {
			t.Fatalf("应返回 ok: %s", rec.Body)
		}
		if p.cfg.Pool.AuthByUID("u1") != nil {
			t.Fatal("移除后不应还在号池")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("凭证文件应被删除，stat err=%v", err)
		}
	})

	t.Run("没有 FilePath 也能移除", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{Pool: pool.New("")})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/remove", ``), http.StatusOK)
		if p.cfg.Pool.AuthByUID("u1") != nil {
			t.Fatal("移除后不应还在号池")
		}
	})

	t.Run("文件删除失败返回 file_error", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{Pool: pool.New("")})
		dir := filepath.Join(t.TempDir(), "not-empty-dir")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		a := taskTestAuth("u1", "一号")
		a.FilePath = dir
		p.cfg.Pool.Add(a)
		rec := postJSON(t, p, "/panel/api/accounts/u1/remove", ``)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["file_error"] == nil || resp["file_error"] == "" {
			t.Fatalf("应带 file_error: %v", resp)
		}
		if p.cfg.Pool.AuthByUID("u1") != nil {
			t.Fatal("即使文件删除失败也应出池")
		}
	})
}

// 批量触发：nil 调度器 501；有调度器返回 started；balance_all 同步刷余额+昵称同步。
func TestPanelBatchTriggers(t *testing.T) {
	autoFastWaits(t)
	endpoints := []string{"checkin_all", "travel_all", "activity_all", "keepalive_all", "balance_all"}

	t.Run("nil 调度器 501", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{})
		for _, ep := range endpoints {
			autoWantCode(t, postJSON(t, p, "/panel/api/"+ep, ``), http.StatusNotImplemented)
		}
	})

	t.Run("异步触发接口返回 started", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{})
		p.cfg.Scheduler = scheduler.New(scheduler.Config{Pool: p.cfg.Pool, Upstream: p.cfg.Upstream})
		for _, ep := range endpoints[:4] {
			rec := postJSON(t, p, "/panel/api/"+ep, ``)
			autoWantCode(t, rec, http.StatusOK)
			resp := autoDecode(t, rec)
			if resp["ok"] != true || resp["started"] != true {
				t.Fatalf("%s 应返回 started: %v", ep, resp)
			}
		}
		// balance_all 空池同样 200 + accounts 空数组。
		rec := postJSON(t, p, "/panel/api/balance_all", ``)
		autoWantCode(t, rec, http.StatusOK)
		if accounts, ok := autoDecode(t, rec)["accounts"].([]any); !ok || len(accounts) != 0 {
			t.Fatalf("balance_all 空池应返回空 accounts: %s", rec.Body)
		}
	})

	t.Run("balance_all 刷余额并同步昵称", func(t *testing.T) {
		p, up := newPanelFor(t, Config{Pool: pool.New("")})
		a1 := taskTestAuth("u1", "旧昵称一")
		// SetNickname 会回写凭证文件；没有 FilePath 时保存失败会返回 false（内存仍更新）。
		a1.FilePath = filepath.Join(t.TempDir(), "workbuddy-u1.json")
		p.cfg.Pool.Add(a1)
		p.cfg.Pool.Add(taskTestAuth("u2", "旧昵称二"))
		disabled := taskTestAuth("u3", "禁用号")
		p.cfg.Pool.Add(disabled)
		p.cfg.Pool.Disable("u3", "测试禁用")
		p.cfg.Scheduler = scheduler.New(scheduler.Config{Pool: p.cfg.Pool, Upstream: p.cfg.Upstream})

		up.on(http.MethodPost, autoBillingHost, upstreamtest.FreePath, "", func(r autoReq) (int, string) {
			return 200, loginResourceBody(88, 100)
		})
		up.on(http.MethodGet, autoWebHost, "/console/account", "web", func(r autoReq) (int, string) {
			// FetchAccountProfile 不带 X-User-Id，登录态 Cookie 里的令牌区分账号。
			if autoToken(r) == "at-u1" {
				return 200, autoEnvelope(`{"uid":"u1","nickname":"新昵称一"}`)
			}
			return 400, `{"code":400,"msg":"profile boom"}`
		})

		rec := postJSON(t, p, "/panel/api/balance_all", ``)
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		accounts, _ := resp["accounts"].([]any)
		if len(accounts) != 3 {
			t.Fatalf("应返回 3 个账号: %v", resp)
		}
		if st := mustStatus(t, p, "u1"); st.Nickname != "新昵称一" || st.Credits != 88 {
			t.Fatalf("u1 昵称/余额未更新: %+v", st)
		}
		if st := mustStatus(t, p, "u2"); st.Nickname != "旧昵称二" || st.Credits != 88 {
			t.Fatalf("u2 昵称失败应保留原值、余额照常: %+v", st)
		}
		if st := mustStatus(t, p, "u3"); st.Credits == 88 || st.Nickname != "禁用号" {
			t.Fatalf("禁用号不应刷新: %+v", st)
		}
		profiles := up.requestsTo(http.MethodGet, autoWebHost, "/console/account", "web")
		if len(profiles) != 2 {
			t.Fatalf("只应为两个启用账号拉昵称，实际 %d 次", len(profiles))
		}
		for _, r := range profiles {
			if autoToken(r) == "at-u3" {
				t.Fatal("禁用号不应拉取昵称")
			}
		}
	})
}

// 用量接口：nil 501；成功返回聚合；hours 参数各分支；usage/save 落盘。
func TestPanelUsage(t *testing.T) {
	t.Run("nil 记录器 501", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{})
		autoWantCode(t, get(p, http.MethodGet, "/panel/api/usage"), http.StatusNotImplemented)
		autoWantCode(t, postJSON(t, p, "/panel/api/usage/save", ``), http.StatusNotImplemented)
	})

	t.Run("聚合与 hours 分支", func(t *testing.T) {
		rec := usage.New("")
		rec.Add(time.Now(), "cn", "u1", "glm-5.2",
			usage.Delta{PromptTokens: 10, HasPromptTokens: true, TotalTokens: 10, HasTotal: true}, true)
		p, _ := newPanelFor(t, Config{Usage: rec})
		p.cfg.Pool.Add(taskTestAuth("u1", "一号")) // 覆盖昵称映射分支
		for _, query := range []string{"", "?hours=9999", "?hours=0", "?hours=abc", "?hours=-1"} {
			urec := get(p, http.MethodGet, "/panel/api/usage"+query)
			autoWantCode(t, urec, http.StatusOK)
			totals, _ := autoDecode(t, urec)["totals"].(map[string]any)
			if totals["requests"] != float64(1) {
				t.Fatalf("%q 请求数不对: %v", query, totals)
			}
		}
	})

	t.Run("没有上游客户端也能出聚合", func(t *testing.T) {
		rec := usage.New("")
		p := New(Config{Version: "test", Pool: pool.New(""), Usage: rec})
		autoWantCode(t, get(p, http.MethodGet, "/panel/api/usage"), http.StatusOK)
	})

	t.Run("usage/save 同步落盘", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "usage.json")
		rec := usage.New(path)
		rec.Add(time.Now(), "cn", "u1", "glm-5.2",
			usage.Delta{PromptTokens: 5, HasPromptTokens: true}, true)
		p, _ := newPanelFor(t, Config{Usage: rec})
		rec2 := postJSON(t, p, "/panel/api/usage/save", ``)
		autoWantCode(t, rec2, http.StatusOK)
		fi, err := os.Stat(path)
		if err != nil || fi.Size() == 0 {
			t.Fatalf("用量文件应已落盘: fi=%v err=%v", fi, err)
		}
	})
}

// 积分构成：逐号查包，单号失败只标该行；按剩余量降序。
func TestPanelPackages(t *testing.T) {
	p, up := newPanelFor(t, Config{Pool: pool.New("")})
	p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
	p.cfg.Pool.Add(taskTestAuth("u2", "二号"))
	up.on(http.MethodPost, autoBillingHost, upstreamtest.FreePath, "", func(r autoReq) (int, string) {
		if r.Header.Get("X-User-Id") == "u2" {
			return 400, `{"code":400,"msg":"pkg boom"}`
		}
		return 200, upstreamtest.List(upstreamtest.Package(100, 60, `"PackageName":"包A"`))
	})

	rec := get(p, http.MethodGet, "/panel/api/packages")
	autoWantCode(t, rec, http.StatusOK)
	rows, _ := autoDecode(t, rec)["accounts"].([]any)
	if len(rows) != 2 {
		t.Fatalf("应有 2 行: %v", rows)
	}
	first, _ := rows[0].(map[string]any)
	second, _ := rows[1].(map[string]any)
	if first["uid"] != "u1" || first["remain"] != float64(60) || first["size"] != float64(100) {
		t.Fatalf("成功行应按 remain 排第一: %v", first)
	}
	packs, _ := first["packages"].([]any)
	if len(packs) != 1 {
		t.Fatalf("应有 1 个包: %v", first)
	}
	pack, _ := packs[0].(map[string]any)
	if pack["name"] != "包A" || pack["remain"] != float64(60) {
		t.Fatalf("包字段不对: %v", pack)
	}
	if second["uid"] != "u2" {
		t.Fatalf("失败行应是 u2: %v", second)
	}
	if msg, _ := second["error"].(string); msg == "" {
		t.Fatalf("失败行应有 error: %v", second)
	}
}

// model_probes 的补充分支：读取失败 500、缺 probes 字段返回空 map。
func TestPanelModelProbesExtra(t *testing.T) {
	t.Run("路径是目录读取失败 500", func(t *testing.T) {
		p, _ := newPanelFor(t, Config{ProbeFile: t.TempDir()})
		rec := get(p, http.MethodGet, "/panel/api/model_probes")
		autoWantCode(t, rec, http.StatusInternalServerError)
		if txt := autoErrText(t, rec); !strings.Contains(txt, "read probes") {
			t.Fatalf("错误信息不对: %q", txt)
		}
	})

	t.Run("缺 probes 字段返回空 map", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "probes.json")
		if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		p, _ := newPanelFor(t, Config{ProbeFile: path})
		rec := get(p, http.MethodGet, "/panel/api/model_probes")
		autoWantCode(t, rec, http.StatusOK)
		resp := autoDecode(t, rec)
		if resp["exists"] != true {
			t.Fatalf("exists 应为 true: %v", resp)
		}
		probes, ok := resp["probes"].(map[string]any)
		if !ok || len(probes) != 0 {
			t.Fatalf("缺 probes 应回空 map: %v", resp)
		}
	})
}

// panelModelEntry：档位远端/静态兜底、context 与 max output 的来源、优惠字段。
func TestPanelModelEntry(t *testing.T) {
	t.Run("远端字段与优惠", func(t *testing.T) {
		factor := 0.5
		mi := upstream.ModelInfo{
			ID: "glm-5.2", Name: "GLM-5.2", ContextWindow: 500000, MaxTokens: 12345,
			Efforts: []string{"low"}, DefaultEffort: "low", Credits: "x1",
			Tags: []string{"t"}, Vendor: "v", IsDefault: true,
			SupportsReasoning: true, SupportsImages: true, SupportsToolCall: true,
			OnlyReasoning: true, CanDisableThinking: true,
			ReasoningEffort: "high", ReasoningSummary: "auto", MaxAllowedSize: 200000,
			PromoFactor: &factor, PromoCredits: "0.5x", PromoLabel: "限时", PromoNote: "说明",
		}
		entry := panelModelEntry("cn", mi, []string{"low", "high"}, "high", nil)
		if entry["id"] != "cn:glm-5.2" || entry["context_length"] != int64(500000) ||
			entry["max_output_tokens"] != int64(12345) {
			t.Fatalf("id/context/输出不对: %v", entry)
		}
		efforts, _ := entry["supported_efforts"].([]string)
		if len(efforts) != 2 || efforts[0] != "low" || entry["default_effort"] != "high" {
			t.Fatalf("远端档位应覆盖: %v", entry)
		}
		if entry["promo_factor"] != 0.5 || entry["promo_credits"] != "0.5x" ||
			entry["promo_label"] != "限时" || entry["promo_note"] != "说明" ||
			entry["max_allowed_size"] != int64(200000) {
			t.Fatalf("优惠/上限字段不对: %v", entry)
		}
	})

	t.Run("没有上游档位走静态兜底", func(t *testing.T) {
		mi := upstream.ModelInfo{ID: "glm-5.2"}
		entry := panelModelEntry("cn", mi, nil, "", nil)
		efforts, _ := entry["supported_efforts"].([]string)
		if len(efforts) != 2 || efforts[0] != "high" || efforts[1] != "xhigh" || entry["default_effort"] != "high" {
			t.Fatalf("静态档位兜底不对: %v", entry)
		}
		if entry["context_length"] != upstream.DefaultContextWindow || entry["max_output_tokens"] != int64(131072) {
			t.Fatalf("静态 context/输出不对: %v", entry)
		}
	})

	t.Run("未知模型省略输出上限", func(t *testing.T) {
		mi := upstream.ModelInfo{ID: "test-unknown-model-xyz"}
		entry := panelModelEntry("cn", mi, nil, "", nil)
		if entry["context_length"] != upstream.DefaultContextWindow {
			t.Fatalf("未知模型 context 应回落 1M: %v", entry)
		}
		if _, ok := entry["max_output_tokens"]; ok {
			t.Fatalf("未知模型不应编造输出上限: %v", entry)
		}
	})
}
