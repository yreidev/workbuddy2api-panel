// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// full_more_test.go 补齐 run 的装配分支（排程开关、Redis 模式、优雅停机超时、
// Serve 出错）、企业目录定时第二轮、saveConfig 的 bind mount 回写失败、
// 倍率闭包，以及 config.go 的剩余错误/边界分支。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/scheduler"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// runLogs 同时捕获 log 输出与 os.Stderr 文件（run 内部会把 log 改成 MultiWriter(os.Stderr, 面板缓冲)）。
type runLogs struct {
	buf  bytes.Buffer
	file *os.File
}

func captureRunLogs(t *testing.T) *runLogs {
	t.Helper()
	oldOut, oldStderr := log.Writer(), os.Stderr
	f, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = f
	l := &runLogs{file: f}
	log.SetOutput(&l.buf)
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		os.Stderr = oldStderr
		_ = f.Close()
	})
	return l
}

func (l *runLogs) text(t *testing.T) string {
	t.Helper()
	tail, err := os.ReadFile(l.file.Name())
	if err != nil {
		t.Fatalf("读 stderr 捕获: %v", err)
	}
	return l.buf.String() + string(tail)
}

// 排程全部启用时打印启用日志；余额刷新/跨域/覆盖禁用号/归档关闭也各有日志；
// 非法 WB2A_SHUTDOWN_TIMEOUT 提示回落默认。
func TestRunScheduleTogglesAndArchiveOff(t *testing.T) {
	setupRunGlobals(t)
	logs := captureRunLogs(t)
	t.Setenv("WB2A_SHUTDOWN_TIMEOUT", "bogus")
	cfg := newRunTestConfig(t)
	cfg.Schedule.CheckinEnabled = true
	cfg.Schedule.TravelEnabled = true
	cfg.Schedule.ActivityEnabled = true
	cfg.Schedule.KeepaliveEnabled = true
	cfg.Schedule.BlackcatEnabled = true
	cfg.Schedule.GrowthEnabled = true
	cfg.Schedule.BalanceRefreshEnabled = true
	cfg.Schedule.BalanceRefreshMinutes = 5
	cfg.BalanceRefreshInterval = 5 * time.Minute
	cfg.Schedule.IncludeDisabledInTasks = true
	cfg.Pool.CrossRealm = livecfg.CrossRealmMerge
	cfg.Logging.RequestArchiveEnabled = false

	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, addr := startRun(t, ctx, cfg, newRunUpstream(&runRT{}), ln)
	waitHealthy(t, addr)
	cancel()
	if res := awaitRun(t, out); res.restart || res.err != nil {
		t.Fatalf("停机结果 = %+v", res)
	}

	text := logs.text(t)
	for _, want := range []string{
		"签到已启用",
		"猫猫旅行已启用",
		"活跃上报已启用",
		"token 保活已启用",
		"夜猫子已启用",
		"余额后台刷新：每 5m0s",
		"跨域负载均衡：合池（pool.cross_realm=merge）",
		"保号任务覆盖禁用账号",
		"JSONL 归档已关闭",
		"WB2A_SHUTDOWN_TIMEOUT 无效",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("日志缺少 %q:\n%s", want, text)
		}
	}
}

// 配置了 Upstash 且连得上（miniredis）时，面板总览报告 redis_mode=upstash。
func TestRunRedisMode(t *testing.T) {
	setupRunGlobals(t)
	mr := miniredis.RunT(t)
	cfg := newRunTestConfig(t)
	cfg.Upstash.URL = "redis://" + mr.Addr()
	cfg.SessionSticky.Enabled = true

	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, addr := startRun(t, ctx, cfg, newRunUpstream(&runRT{}), ln)
	waitHealthy(t, addr)
	code, body := panelRequest(t, http.MethodGet, "http://"+addr+"/panel/api/overview", "")
	if code != 200 || !strings.Contains(body, `"redis_mode":"upstash"`) {
		t.Fatalf("overview = %d %s", code, body)
	}
	cancel()
	if res := awaitRun(t, out); res.restart || res.err != nil {
		t.Fatalf("停机结果 = %+v", res)
	}
}

// 优雅停机宽限期到点仍有在途请求：记录“强制退出”，run 仍正常返回。
func TestRunShutdownTimeoutLog(t *testing.T) {
	setupRunGlobals(t)
	logs := captureRunLogs(t)
	t.Setenv("WB2A_SHUTDOWN_TIMEOUT", "1ms")
	cfg := newRunTestConfig(t)
	rt := &runRT{chatStarted: make(chan struct{}, 1), chatRelease: make(chan struct{})}
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, addr := startRun(t, ctx, cfg, newRunUpstream(rt), ln)
	waitHealthy(t, addr)

	chatDone := make(chan struct{})
	go func() {
		defer close(chatDone)
		body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":false}`
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+runKey)
		req.Header.Set("Content-Type", "application/json")
		if resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-rt.chatStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("对话请求没有到达上游")
	}

	cancel()
	res := awaitRun(t, out)
	if res.restart || res.err != nil {
		t.Fatalf("停机结果 = %+v", res)
	}
	close(rt.chatRelease)
	<-chatDone
	if text := logs.text(t); !strings.Contains(text, "强制退出") {
		t.Fatalf("日志缺少强制退出: %s", text)
	}
}

// Serve 返回非 ErrServerClosed（外部关掉监听）时 run 先走完停机（落盘）再返回 http 错误。
func TestRunServeError(t *testing.T) {
	setupRunGlobals(t)
	logs := captureRunLogs(t)
	cfg := newRunTestConfig(t)
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, addr := startRun(t, ctx, cfg, newRunUpstream(&runRT{}), ln)
	waitHealthy(t, addr)
	_ = ln.Close()
	res := awaitRun(t, out)
	if res.restart || res.err == nil || !strings.Contains(res.err.Error(), "http:") {
		t.Fatalf("结果 = %+v", res)
	}
	if text := logs.text(t); !strings.Contains(text, "监听出错，停止服务") {
		t.Fatalf("run 返回前停机没走完: %s", text)
	}
}

// 企业目录刷新：tick 调短后第二轮仍会检查（拉取失败不缓存，因此会重拉）。
func TestRefreshEnterpriseCatalogsTicker(t *testing.T) {
	old := enterpriseCatalogTick
	enterpriseCatalogTick = 5 * time.Millisecond
	t.Cleanup(func() { enterpriseCatalogTick = old })

	var calls atomic.Int32
	hit := make(chan struct{}, 8)
	up := &upstream.Client{
		HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			select {
			case hit <- struct{}{}:
			default:
			}
			return fakeResp(400, `{"code":400}`), nil
		})},
		ChatBaseCN: "https://cn.fake",
	}
	p := pool.New("")
	p.Add(&auth.Auth{UID: "ent1", EnterpriseID: "ent-1", AccessToken: "at", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		refreshEnterpriseCatalogs(ctx, up, p)
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-hit:
		case <-time.After(5 * time.Second):
			t.Fatalf("第 %d 轮目录刷新超时（calls=%d）", i+1, calls.Load())
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后未退出")
	}
	if calls.Load() < 2 {
		t.Fatalf("应至少刷新两轮，实际 %d", calls.Load())
	}
}

// 装配后的倍率闭包：触底号对目录里收费的模型不再中选。
func TestWireModelCatalogModelRate(t *testing.T) {
	up := &upstream.Client{
		HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			body := `{"code":0,"data":{"models":[{"id":"paid","credits":"x0.5"}],` +
				`"agents":[{"name":"cli","models":["paid"]}]}}`
			return fakeResp(200, body), nil
		})},
		ChatBaseCN:    "https://cn.fake",
		BillingBaseCN: "https://cn.fake",
	}
	p := pool.New("")
	p.SetCreditFloor(100)
	p.Add(&auth.Auth{UID: "a1", AccessToken: "at", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"})
	wireModelCatalog(p, up)
	if _, err := up.FetchModels(p.AuthByUID("a1")); err != nil {
		t.Fatal(err)
	}
	if rate := up.ModelRate("cn", "paid"); rate == "" {
		t.Fatal("倍率表未从目录填充")
	}
	if got := p.PickExcludingForModel(nil, "paid"); got != nil {
		t.Fatalf("触底号不应中选收费模型，got %s", got.UID)
	}
}

// saveConfig 的 bind mount 回写失败：目标文件不可写（/dev/full）时保留 tmp。
func TestSaveConfigEBUSYFallbackWriteError(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("没有 /dev/full")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"api_key":"old-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	live := livecfg.New(livecfg.Snapshot{APIKey: "old-key"})
	p := pool.New("")
	up := &upstream.Client{}
	sch := scheduler.New(scheduler.Config{Pool: p, Upstream: up})

	stubRename(t, func(oldPath, newPath string) error {
		if err := os.Remove(newPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/dev/full", newPath); err != nil {
			t.Fatal(err)
		}
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.EBUSY}
	})
	_, err := saveConfig([]byte(`{"api_key":"new-key"}`), path, live, p, up, sch, logStores{})
	if err == nil || !strings.Contains(err.Error(), "完整新内容保留在") ||
		!strings.Contains(err.Error(), "no space left on device") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(path + ".tmp"); statErr != nil {
		t.Fatalf("写失败时应保留 tmp: %v", statErr)
	}
	if got := live.Load().APIKey; got != "old-key" {
		t.Fatalf("失败不应热生效: %q", got)
	}
}

// saveConfig 的 bind mount 回写后关闭失败：报错、清掉 tmp、不热生效。
func TestSaveConfigEBUSYFallbackCloseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"api_key":"old-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	live := livecfg.New(livecfg.Snapshot{APIKey: "old-key"})
	p := pool.New("")
	up := &upstream.Client{}
	sch := scheduler.New(scheduler.Config{Pool: p, Upstream: up})

	stubRename(t, func(oldPath, newPath string) error {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.EBUSY}
	})
	oldClose := configFileClose
	configFileClose = func(*os.File) error { return errors.New("close boom") }
	t.Cleanup(func() { configFileClose = oldClose })

	_, err := saveConfig([]byte(`{"api_key":"new-key"}`), path, live, p, up, sch, logStores{})
	if err == nil || !strings.Contains(err.Error(), "bind mount fallback") || !strings.Contains(err.Error(), "close boom") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
		t.Fatalf("关闭失败也应清掉 tmp: %v", statErr)
	}
	if got := live.Load().APIKey; got != "old-key" {
		t.Fatalf("失败不应热生效: %q", got)
	}
}

// Load：env 覆盖后的时长非法时返回 normalize 的错误（文件分支的同类错误由
// ParseConfigInto 先行拦截，这里走 applyEnv 之后的第二次 normalize）。
func TestLoadNormalizeError(t *testing.T) {
	t.Setenv("WB2A_SOFT_RATE", "bogus")
	_, err := Load("")
	if err == nil || !strings.Contains(err.Error(), "cooldown.soft_rate") {
		t.Fatalf("err=%v", err)
	}
}

// WriteDefault 的错误分支：随机数失败、目录建不出来、写文件失败。
func TestWriteDefaultErrors(t *testing.T) {
	t.Run("随机数失败", func(t *testing.T) {
		old := randRead
		randRead = func([]byte) (int, error) { return 0, errors.New("rand boom") }
		t.Cleanup(func() { randRead = old })
		if _, err := WriteDefault(filepath.Join(t.TempDir(), "config.json")); err == nil ||
			!strings.Contains(err.Error(), "gen api_key: rand boom") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("配置目录建不出来", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := WriteDefault(filepath.Join(file, "config.json")); err == nil ||
			!strings.Contains(err.Error(), "mkdir config dir") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("写文件失败", func(t *testing.T) {
		old := configFileWrite
		configFileWrite = func(*os.File, []byte) (int, error) { return 0, errors.New("write boom") }
		t.Cleanup(func() { configFileWrite = old })
		if _, err := WriteDefault(filepath.Join(t.TempDir(), "config.json")); err == nil ||
			!strings.Contains(err.Error(), "write config: write boom") {
			t.Fatalf("err=%v", err)
		}
	})
}

// normalize：负的探索窗口、负的排队上限钳 0（空值回落默认的分支在解析之前就报错，走不到）。
func TestNormalizeDegradeAndExploreDefaults(t *testing.T) {
	c := Default()
	c.Pool.CostExploreInterval = "-5m"
	c.Pool.InFlightWait = "-5s"
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	if c.CostExploreIntervalDur != 0 || c.InFlightWaitDur != 0 {
		t.Fatalf("负值应钳 0，got %v %v", c.CostExploreIntervalDur, c.InFlightWaitDur)
	}
	if d := Default(); d.normalize() != nil || d.InFlightWaitDur != 30*time.Second {
		t.Fatalf("排队等空位默认应是 30s，got %v", d.InFlightWaitDur)
	}
	if c.DegradeCooldownDur != 10*time.Minute || c.DegradeCooldownMaxD != 2*time.Hour {
		t.Fatalf("默认降权时长不对: %v %v", c.DegradeCooldownDur, c.DegradeCooldownMaxD)
	}
}
