// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// main_funcs_test.go 入口旁边可独立测试的函数：路径推导、配置合并/保存热生效、
// 模型倍率预热与企业号目录刷新。run/main 的进程装配不在本文件范围内。

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/logarchive"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/scheduler"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

// 路径推导：state 文件为空/相对/绝对三种形态。
func TestStateSiblingAndUsagePath(t *testing.T) {
	cases := []struct {
		stateFile string
		name      string
		want      string
	}{
		{"", "usage.json", "usage.json"},
		{"state.json", "usage.json", "usage.json"},
		{"data/state.json", "usage.json", filepath.Join("data", "usage.json")},
		{"/var/lib/wb/state.json", "output_probes.json", "/var/lib/wb/output_probes.json"},
	}
	for _, tc := range cases {
		if got := stateSibling(tc.stateFile, tc.name); got != tc.want {
			t.Fatalf("stateSibling(%q,%q)=%q want %q", tc.stateFile, tc.name, got, tc.want)
		}
	}
	if got := usagePathFor("/var/lib/wb/state.json"); got != "/var/lib/wb/usage.json" {
		t.Fatalf("usagePathFor=%q", got)
	}
	if got := usagePathFor("state.json"); got != "usage.json" {
		t.Fatalf("usagePathFor(相对)=%q", got)
	}
}

// panelListenPath：取最后一个冒号起的端口段；没有冒号原样返回。
func TestPanelListenPath(t *testing.T) {
	cases := map[string]string{
		":7863":          ":7863",
		"127.0.0.1:7863": ":7863",
		"[::]:7863":      ":7863",
		"7863":           "7863",
		"":               "",
	}
	for in, want := range cases {
		if got := panelListenPath(in); got != want {
			t.Fatalf("panelListenPath(%q)=%q want %q", in, got, want)
		}
	}
}

// retentionText / runtimeLogText 启动日志里留存设置的说法。
func TestRetentionText(t *testing.T) {
	if got := retentionText(7); got != "保留 7 天" {
		t.Fatalf("retentionText(7)=%q", got)
	}
	if got := retentionText(0); got != "永久保留" {
		t.Fatalf("retentionText(0)=%q", got)
	}
	if got := runtimeLogText(true, 30, 20); got != "保留 30 天、上限 20 MiB" {
		t.Fatalf("runtimeLogText 开着=%q", got)
	}
	if got := runtimeLogText(false, 30, 20); got != "已关闭" {
		t.Fatalf("runtimeLogText 关着=%q", got)
	}
}

// restartRequiredFields：空字段不列，固定项恒在，upstash 有 URL 或 token 时出现；日志留存的键都热生效，不列。
func TestRestartRequiredFields(t *testing.T) {
	fixed := []string{
		"upstream.timeout_seconds", "upstream.header_timeout_seconds", "upstream.idle_timeout_seconds",
		"upstream.user_agent", "session_sticky.ttl", "session_sticky.gc_interval", "server.read_timeout",
	}
	empty := Config{}
	if got := restartRequiredFields(&empty); !reflect.DeepEqual(got, fixed) {
		t.Fatalf("空配置应只有固定项: %v", got)
	}

	var full Config
	full.Listen = ":7863"
	full.AuthDir = "auths"
	full.StateFile = "state.json"
	full.Upstash.URL = "redis://x"
	want := append([]string{"listen", "auth_dir", "state_file"},
		"upstream.timeout_seconds", "upstream.header_timeout_seconds", "upstream.idle_timeout_seconds",
		"upstream.user_agent", "upstash", "session_sticky.ttl", "session_sticky.gc_interval", "server.read_timeout")
	if got := restartRequiredFields(&full); !reflect.DeepEqual(got, want) {
		t.Fatalf("完整配置清单不对:\n got %v\nwant %v", got, want)
	}

	var tokenOnly Config
	tokenOnly.Upstash.Token = "t"
	if got := restartRequiredFields(&tokenOnly); !containsStr(got, "upstash") {
		t.Fatalf("仅有 token 也应列 upstash: %v", got)
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// mergeConfigMaps：平铺覆盖、嵌套逐键合并、类型冲突整体替换、返回 cur 本身。
func TestMergeConfigMaps(t *testing.T) {
	t.Run("平铺与嵌套", func(t *testing.T) {
		cur := map[string]any{
			"a":      1,
			"nested": map[string]any{"x": 1, "y": 2},
		}
		incoming := map[string]any{
			"a":      2,
			"nested": map[string]any{"y": 3},
		}
		got := mergeConfigMaps(cur, incoming)
		got["marker"] = true // 返回的是 cur 本身（共享同一 map）
		if cur["marker"] != true {
			t.Fatal("返回值应是 cur 本身")
		}
		if cur["a"] != 2 {
			t.Fatalf("平铺键应覆盖: %v", cur["a"])
		}
		nested, _ := cur["nested"].(map[string]any)
		if nested["x"] != 1 || nested["y"] != 3 {
			t.Fatalf("嵌套应逐键合并: %v", nested)
		}
	})

	t.Run("类型冲突整体替换", func(t *testing.T) {
		cur := map[string]any{"a": 1, "b": map[string]any{"x": 1}}
		mergeConfigMaps(cur, map[string]any{"a": map[string]any{"y": 2}, "b": 5})
		if m, ok := cur["a"].(map[string]any); !ok || m["y"] != 2 {
			t.Fatalf("incoming 对象而 cur 不是对象应整体替换: %v", cur["a"])
		}
		if cur["b"] != 5 {
			t.Fatalf("incoming 非对象而 cur 是对象应整体替换: %v", cur["b"])
		}
	})

	t.Run("incoming 为空不改变 cur", func(t *testing.T) {
		cur := map[string]any{"a": 1}
		if got := mergeConfigMaps(cur, nil); got["a"] != 1 {
			t.Fatalf("空 incoming 应原样返回: %v", got)
		}
		if got := mergeConfigMaps(nil, map[string]any{}); got != nil {
			t.Fatalf("nil cur + 空 incoming 应返回 nil: %v", got)
		}
	})
}

// mergedJSON：正常序列化；无法序列化（channel / NaN）回 "{}"。
func TestMergedJSON(t *testing.T) {
	raw := mergedJSON(map[string]any{"a": 1})
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil || back["a"] != float64(1) {
		t.Fatalf("正常序列化失败: %s err=%v", raw, err)
	}
	if got := string(mergedJSON(map[string]any{"bad": make(chan int)})); got != "{}" {
		t.Fatalf("channel 应序列化成 {}: %s", got)
	}
	if got := string(mergedJSON(map[string]any{"nan": math.NaN()})); got != "{}" {
		t.Fatalf("NaN 应序列化成 {}: %s", got)
	}
}

// fakeResp 构造假上游响应。
func fakeResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// saveConfig 的错误与落盘保真：读不到、提交非法、磁盘非法、校验失败、成功合并。
func TestSaveConfig(t *testing.T) {
	const base = `{
  "listen": ":7863",
  "api_key": "old-key",
  "auth_dir": "auths",
  "state_file": "data/state.json",
  "cooldown": {"soft_rate": "1m", "soft_rate_max": "2h"},
  "features": {"sanitize_blacklist_fingerprints": false},
  "logging": {"request_client_info": false},
  "pool": {"cross_realm": false, "credit_floor": 7, "breaker_threshold": 3},
  "panel": {"update_check": false},
  "user_note": "手写字段",
  "custom": {"keep": 1}
}`
	newRig := func(t *testing.T, content string) (string, *livecfg.Holder, *pool.Pool, *upstream.Client, *scheduler.Scheduler) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		if content != "" {
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		live := livecfg.New(livecfg.Snapshot{APIKey: "old-key"})
		p := pool.New("")
		p.SetCreditFloor(7)
		up := &upstream.Client{}
		sch := scheduler.New(scheduler.Config{Pool: p, Upstream: up})
		return path, live, p, up, sch
	}

	t.Run("配置文件读不到", func(t *testing.T) {
		path, live, p, up, sch := newRig(t, "")
		_, err := saveConfig([]byte(`{}`), path, live, p, up, sch, logStores{})
		if err == nil || !strings.Contains(err.Error(), "read current config") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("提交的不是 JSON 且磁盘不变", func(t *testing.T) {
		path, live, p, up, sch := newRig(t, base)
		before, _ := os.ReadFile(path)
		_, err := saveConfig([]byte(`{broken`), path, live, p, up, sch, logStores{})
		if err == nil || !strings.Contains(err.Error(), "parse submitted config") {
			t.Fatalf("err=%v", err)
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("解析失败不应改磁盘文件")
		}
	})

	t.Run("磁盘文件不是 JSON 当空配置继续", func(t *testing.T) {
		path, live, p, up, sch := newRig(t, `{broken`)
		if _, err := saveConfig([]byte(`{"api_key":"k2"}`), path, live, p, up, sch, logStores{}); err != nil {
			t.Fatalf("磁盘坏 JSON 不应阻塞保存: %v", err)
		}
		if got := live.Load().APIKey; got != "k2" {
			t.Fatalf("热生效 api_key=%q", got)
		}
		raw, _ := os.ReadFile(path)
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil || m["api_key"] != "k2" {
			t.Fatalf("落盘文件应可解析: %s err=%v", raw, err)
		}
	})

	t.Run("日志留存保存即生效", func(t *testing.T) {
		path, live, p, up, sch := newRig(t, base)
		dir := t.TempDir()
		logs := logStores{
			requests: reqlog.New(reqlog.Config{Dir: filepath.Join(dir, "request-logs"), Enabled: true, RetentionDays: 7}),
			task:     logarchive.New(logarchive.Config{Dir: filepath.Join(dir, "runtime-logs"), Prefix: "task", Enabled: true, RetentionDays: 30}),
			system:   logarchive.New(logarchive.Config{Dir: filepath.Join(dir, "runtime-logs"), Prefix: "system", Enabled: true, RetentionDays: 30}),
			credit:   logarchive.New(logarchive.Config{Dir: filepath.Join(dir, "credit-logs"), Prefix: "credit", Enabled: true}),
			usage:    usage.New(""),
		}
		defer logs.requests.Close()
		defer logs.task.Close()
		defer logs.system.Close()
		defer logs.credit.Close()
		restart, err := saveConfig([]byte(`{"logging":{"request_archive_enabled":false,"request_retention_days":0,"request_archive_max_mb":10,
			"task_log_enabled":false,"task_log_retention_days":3,"task_log_max_mb":2,
			"system_log_retention_days":0,"system_log_max_mb":4,"usage_retention_days":14,
			"credit_log_enabled":false,"credit_log_retention_days":90,"credit_log_max_mb":1}}`), path, live, p, up, sch, logs)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range restart {
			if strings.HasPrefix(f, "logging.") {
				t.Fatalf("日志留存不应要求重启: %v", restart)
			}
		}
		if a := logs.requests.ArchiveStats(); a.Enabled || a.RetentionDays != 0 || a.MaxBytes != 10<<20 {
			t.Fatalf("请求记录: %+v", a)
		}
		if a := logs.task.Stats(); a.Enabled || a.RetentionDays != 3 || a.MaxBytes != 2<<20 {
			t.Fatalf("任务日志: %+v", a)
		}
		if a := logs.system.Stats(); !a.Enabled || a.RetentionDays != 0 || a.MaxBytes != 4<<20 {
			t.Fatalf("系统日志（没提交开关，沿用缺省开）: %+v", a)
		}
		if u := logs.usage.StoreStats(); u.RetentionDays != 14 {
			t.Fatalf("用量统计: %+v", u)
		}
		if a := logs.credit.Stats(); a.Enabled || a.RetentionDays != 90 || a.MaxBytes != 1<<20 {
			t.Fatalf("积分获取: %+v", a)
		}
	})

	t.Run("临时文件写不进去", func(t *testing.T) {
		path, live, p, up, sch := newRig(t, base)
		// tmp 路径被目录占住 → os.WriteFile 失败（root 下权限限制不成立，用类型冲突）。
		if err := os.Mkdir(path+".tmp", 0o700); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		_, err := saveConfig([]byte(`{"api_key":"k2"}`), path, live, p, up, sch, logStores{})
		if err == nil || !strings.Contains(err.Error(), "write config") {
			t.Fatalf("err=%v", err)
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("写临时文件失败不应改原文件")
		}
	})

	t.Run("rename 报 EBUSY 时原地改写", func(t *testing.T) {
		path, live, p, up, sch := newRig(t, base)
		stubRename(t, func(oldPath, newPath string) error {
			return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.EBUSY}
		})
		if _, err := saveConfig([]byte(`{"api_key":"bind-key"}`), path, live, p, up, sch, logStores{}); err != nil {
			t.Fatalf("EBUSY 回退应成功: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil || m["api_key"] != "bind-key" || m["user_note"] != "手写字段" {
			t.Fatalf("原地改写后的文件不对: %s err=%v", raw, err)
		}
		if got := live.Load().APIKey; got != "bind-key" {
			t.Fatalf("热生效 api_key=%q", got)
		}
		if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
			t.Fatalf("回退成功后不应残留 tmp: %v", err)
		}
	})

	t.Run("rename 报其他错误", func(t *testing.T) {
		path, live, p, up, sch := newRig(t, base)
		before, _ := os.ReadFile(path)
		stubRename(t, func(oldPath, newPath string) error {
			return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.EXDEV}
		})
		_, err := saveConfig([]byte(`{"api_key":"k2"}`), path, live, p, up, sch, logStores{})
		if err == nil || !strings.Contains(err.Error(), "replace config") || strings.Contains(err.Error(), "bind mount") {
			t.Fatalf("err=%v", err)
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("rename 失败不应改原文件")
		}
		if got := live.Load().APIKey; got != "old-key" {
			t.Fatalf("rename 失败不应热生效: %q", got)
		}
	})

	t.Run("EBUSY 回退时打不开原文件", func(t *testing.T) {
		path, live, p, up, sch := newRig(t, base)
		// 假 rename 先删掉原文件再报 EBUSY：回退的 O_WRONLY|O_TRUNC 打开（不带 O_CREATE）必然失败
		stubRename(t, func(oldPath, newPath string) error {
			if err := os.Remove(newPath); err != nil {
				t.Fatal(err)
			}
			return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.EBUSY}
		})
		_, err := saveConfig([]byte(`{"api_key":"k2"}`), path, live, p, up, sch, logStores{})
		if err == nil || !strings.Contains(err.Error(), "bind mount fallback") {
			t.Fatalf("err=%v", err)
		}
		if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
			t.Fatalf("打不开原文件时应清掉 tmp: %v", err)
		}
		if got := live.Load().APIKey; got != "old-key" {
			t.Fatalf("回退失败不应热生效: %q", got)
		}
	})

	t.Run("校验失败不落盘不热生效", func(t *testing.T) {
		path, live, p, up, sch := newRig(t, base)
		before, _ := os.ReadFile(path)
		incoming := `{"pool":{"credit_floor":9},"schedule":{"checkin_hours":[99]}}`
		_, err := saveConfig([]byte(incoming), path, live, p, up, sch, logStores{})
		if err == nil || !strings.Contains(err.Error(), "schedule.checkin_hours") {
			t.Fatalf("err=%v", err)
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("校验失败不应改磁盘文件")
		}
		if got := live.Load().APIKey; got != "old-key" {
			t.Fatalf("校验失败不应改 live 快照: %q", got)
		}
		if got := p.CreditFloor(); got != 7 {
			t.Fatalf("校验失败不应改号池参数: %d", got)
		}
		if got := sch.ExpiringSoonWindow(); got != 0 {
			t.Fatalf("校验失败不应改调度器参数: %v", got)
		}
	})

	t.Run("成功合并落盘并热生效", func(t *testing.T) {
		path, live, p, up, sch := newRig(t, base)
		incoming := `{
  "api_key": "new-key",
  "cooldown": {"soft_rate": "90s"},
  "features": {"sanitize_blacklist_fingerprints": true},
  "logging": {"request_client_info": true},
  "pool": {"cross_realm": true, "credit_floor": 9, "expiring_soon": "30m"},
  "panel": {"update_check": true},
  "upstream": {"timeout_seconds": 45, "model_optimization": true},
  "model_routes": [{"match": " claude-*haiku* ", "target": "cn:glm-5.2 "}],
  "models": {"hidden": ["glm-4.7", "global:gpt-5"]}
}`
		got, err := saveConfig([]byte(incoming), path, live, p, up, sch, logStores{})
		if err != nil {
			t.Fatalf("saveConfig: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var merged map[string]any
		if err := json.Unmarshal(raw, &merged); err != nil {
			t.Fatalf("落盘文件不是 JSON: %v", err)
		}
		if merged["user_note"] != "手写字段" {
			t.Fatalf("未知键应保留: %v", merged["user_note"])
		}
		custom, _ := merged["custom"].(map[string]any)
		if custom["keep"] != float64(1) {
			t.Fatalf("未知嵌套键应保留: %v", merged["custom"])
		}
		cooldown, _ := merged["cooldown"].(map[string]any)
		if cooldown["soft_rate"] != "90s" || cooldown["soft_rate_max"] != "2h" {
			t.Fatalf("嵌套对象应逐键覆盖: %v", cooldown)
		}
		if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
			t.Fatalf("目录里不应残留 .tmp: %v", err)
		}

		snap := live.Load()
		if snap.APIKey != "new-key" || snap.SoftCooldown != 90*time.Second ||
			!snap.SanitizeFingerprints || !snap.RecordClientInfo || snap.CrossRealm != livecfg.CrossRealmMerge || !snap.UpdateCheck {
			t.Fatalf("live 快照热生效不对: %+v", snap)
		}
		if want := []livecfg.ModelRoute{{Match: "claude-*haiku*", Target: "cn:glm-5.2"}}; !reflect.DeepEqual(snap.ModelRoutes, want) {
			t.Fatalf("模型路由未热更新（应去掉两端空白）: %+v", snap.ModelRoutes)
		}
		if want := livecfg.NormalizeHiddenModels([]string{"glm-4.7", "global:gpt-5"}); !reflect.DeepEqual(snap.HiddenModels, want) {
			t.Fatalf("隐藏模型未热更新: %+v", snap.HiddenModels)
		}
		if !up.SanitizeFingerprints.Load() {
			t.Fatal("up.SanitizeFingerprints 未热更新")
		}
		if !up.ModelOptimization.Load() {
			t.Fatal("up.ModelOptimization 未热更新")
		}
		if p.CreditFloor() != 9 {
			t.Fatalf("号池 credit_floor 未热更新: %d", p.CreditFloor())
		}
		if sch.ExpiringSoonWindow() != 30*time.Minute {
			t.Fatalf("调度器快过期窗口未热更新: %v", sch.ExpiringSoonWindow())
		}

		finalCfg, err := ParseConfig(raw)
		if err != nil {
			t.Fatalf("最终文件应通过校验: %v", err)
		}
		if want := restartRequiredFields(finalCfg); !reflect.DeepEqual(got, want) {
			t.Fatalf("返回值应等于 restartRequiredFields: got %v want %v", got, want)
		}
	})
}

// stubRename 把 saveConfig 的 rename 换成 fn，测完还原。
func stubRename(t *testing.T, fn func(oldPath, newPath string) error) {
	t.Helper()
	old := renameConfig
	renameConfig = fn
	t.Cleanup(func() { renameConfig = old })
}

// catalogRefresh.once 的剩余分支：CN 失败、无 CN 号、global 空/成功、ctx 在 global 前取消。
func TestCatalogRefreshOnceBranches(t *testing.T) {
	cnAuth := func() *auth.Auth {
		return &auth.Auth{UID: "cn1", AccessToken: "at", RefreshToken: "rt", Domain: "www.codebuddy.cn"}
	}
	globalAuth := func(t *testing.T) *auth.Auth {
		t.Helper()
		g := &auth.Auth{UID: "g1", AccessToken: "at", RefreshToken: "rt"}
		if _, err := auth.BackfillRealmFor(g, "global"); err != nil {
			t.Fatal(err)
		}
		return g
	}
	newClient := func(handler func(*http.Request) (int, string)) *upstream.Client {
		return &upstream.Client{
			HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
				status, body := handler(r)
				return fakeResp(status, body), nil
			})},
			GlobalEnabled:  true,
			ChatBaseCN:     "https://cn.fake",
			BillingBaseCN:  "https://cn.fake",
			ChatBaseGlobal: "https://global.fake",
		}
	}

	t.Run("CN 拉取失败只记 WARN", func(t *testing.T) {
		p := pool.New("")
		p.Add(cnAuth())
		up := newClient(func(*http.Request) (int, string) { return 400, `{"code":400}` })
		newCatalogRefresh().once(context.Background(), up, p, time.Now()) // 不 panic 即通过
	})

	t.Run("没有 CN 号跳过", func(t *testing.T) {
		p := pool.New("")
		var calls atomic.Int32
		up := newClient(func(*http.Request) (int, string) { calls.Add(1); return 200, `{}` })
		up.GlobalEnabled = false
		newCatalogRefresh().once(context.Background(), up, p, time.Now())
		if calls.Load() != 0 {
			t.Fatalf("空池不应发请求，实际 %d", calls.Load())
		}
	})

	t.Run("global 目录为空", func(t *testing.T) {
		p := pool.New("")
		p.Add(globalAuth(t))
		up := newClient(func(*http.Request) (int, string) { return 400, `{"code":400}` })
		newCatalogRefresh().once(context.Background(), up, p, time.Now())
	})

	t.Run("global 目录成功", func(t *testing.T) {
		p := pool.New("")
		p.Add(globalAuth(t))
		var globalCalls atomic.Int32
		up := newClient(func(r *http.Request) (int, string) {
			if r.URL.Host == "global.fake" && strings.HasSuffix(r.URL.Path, "/v3/config") {
				globalCalls.Add(1)
				return 200, `{"code":0,"data":{"models":[{"id":"gpt-5.5"}]}}`
			}
			return 400, `{"code":400}`
		})
		newCatalogRefresh().once(context.Background(), up, p, time.Now())
		if globalCalls.Load() == 0 {
			t.Fatal("global 应被探测")
		}
	})

	t.Run("ctx 在 global 前取消", func(t *testing.T) {
		p := pool.New("")
		p.Add(cnAuth())
		p.Add(globalAuth(t))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var globalCalls atomic.Int32
		up := newClient(func(r *http.Request) (int, string) {
			if r.URL.Host == "global.fake" {
				globalCalls.Add(1)
			}
			cancel() // CN 请求期间取消 → global 阶段应跳过
			return 200, `{"code":0,"data":{"models":[{"id":"glm-5.2"}],"agents":[{"name":"cli","models":["glm-5.2"]}]}}`
		})
		newCatalogRefresh().once(ctx, up, p, time.Now())
		if globalCalls.Load() != 0 {
			t.Fatalf("ctx 取消后不应探测 global，实际 %d 次", globalCalls.Load())
		}
	})
}

// refreshEnterpriseCatalogsOnce 的剩余分支：ctx 取消提前返回、拉取失败保留旧目录。
func TestRefreshEnterpriseCatalogsOnceBranches(t *testing.T) {
	entAuth := func() *auth.Auth {
		return &auth.Auth{UID: "ent1", EnterpriseID: "ent-1", AccessToken: "at", Domain: "www.codebuddy.cn"}
	}

	t.Run("ctx 已取消不拉取", func(t *testing.T) {
		p := pool.New("")
		p.Add(entAuth())
		var calls atomic.Int32
		up := &upstream.Client{
			HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return fakeResp(200, `{}`), nil
			})},
			ChatBaseCN: "https://cn.fake",
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		refreshEnterpriseCatalogsOnce(ctx, up, p, time.Now())
		if calls.Load() != 0 {
			t.Fatalf("ctx 已取消不应发请求，实际 %d", calls.Load())
		}
	})

	t.Run("拉取失败不记录目录", func(t *testing.T) {
		p := pool.New("")
		p.Add(entAuth())
		up := &upstream.Client{
			HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
				return fakeResp(400, `{"code":400}`), nil
			})},
			ChatBaseCN: "https://cn.fake",
		}
		refreshEnterpriseCatalogsOnce(context.Background(), up, p, time.Now())
		if _, ok := up.AccountCatalogFetched("ent1"); ok {
			t.Fatal("拉取失败不应记录目录")
		}
	})
}

// refreshEnterpriseCatalogs：启动先刷一轮，ctx 取消后退出（ticker 是 5min，
// 拿不到第二轮，只验证“首轮 + 取消退出”）。
func TestRefreshEnterpriseCatalogsLoop(t *testing.T) {
	fetched := make(chan struct{}, 1)
	up := &upstream.Client{
		HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			select {
			case fetched <- struct{}{}:
			default:
			}
			return fakeResp(200, `{"code":0,"data":{"models":[{"id":"claude-x"}],"agents":[{"name":"cli","models":["claude-x"]}]}}`), nil
		})},
		ChatBaseCN: "https://cn.fake",
	}
	p := pool.New("")
	p.Add(&auth.Auth{UID: "ent1", EnterpriseID: "ent-1", AccessToken: "at", Domain: "www.codebuddy.cn"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		refreshEnterpriseCatalogs(ctx, up, p)
	}()
	select {
	case <-fetched:
	case <-time.After(5 * time.Second):
		t.Fatal("等待首轮目录刷新超时")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后 refreshEnterpriseCatalogs 未退出")
	}
	if _, ok := up.AccountCatalogFetched("ent1"); !ok {
		t.Fatal("首轮应拉到企业号目录")
	}
}
