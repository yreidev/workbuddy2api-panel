// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// run_test.go 启动到停机：loadOrInitConfig 的四种情况、真实 TCP 监听下的
// 健康检查/模型列表/面板、优雅停机与在途请求、一键更新重启、监听失败，
// 以及 sticky/global 开关组合。上游一律走假 Transport（两路 client 都指向它）。

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/selfupdate"
	"github.com/yreidev/workbuddy2api-panel/internal/server"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// runRT 假上游：模型目录返回固定目录，对话返回 SSE；可让对话阻塞到 release。
type runRT struct {
	mu          sync.Mutex
	paths       []string
	chatStarted chan struct{}
	chatRelease chan struct{}
}

const runChatSSE = "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000," +
	"\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000," +
	"\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]," +
	"\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n" +
	"data: [DONE]\n\n"

func (f *runRT) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.mu.Unlock()
	switch r.URL.Path {
	case "/v3/config":
		body := `{"code":0,"data":{"models":[{"id":"glm-5.2","maxInputTokens":1000000,"maxOutputTokens":131072,` +
			`"reasoning":{"supportedEfforts":["high","xhigh"],"defaultEffort":"high"}}],` +
			`"agents":[{"name":"cli","models":["glm-5.2"]}]}}`
		return runResp(200, "application/json", body), nil
	case "/v2/chat/completions":
		if f.chatStarted != nil {
			select {
			case f.chatStarted <- struct{}{}:
			default:
			}
		}
		if f.chatRelease != nil {
			<-f.chatRelease
		}
		return runResp(200, "text/event-stream", runChatSSE), nil
	case "/v2/billing/meter/checkin-activity-status":
		return runResp(200, "application/json", `{"code":0,"data":{"active":true,"today_checked_in":false}}`), nil
	case "/v2/billing/meter/daily-checkin":
		return runResp(200, "application/json", `{"code":0,"data":{"credit":20}}`), nil
	default:
		// 后台任务（预热/企业目录/调度器）的其它路径：静默 400，不算失败。
		return runResp(400, "application/json", `{"code":400}`), nil
	}
}

func runResp(status int, ct, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{ct}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// runKey 测试用 api_key / access_token（同一常量，避免依赖具体值）。
const runKey = "test-key"

// newRunUpstream 两路 client 都指向同一个假 Transport。
func newRunUpstream(rt http.RoundTripper) *upstream.Client {
	return &upstream.Client{
		HTTP:     &http.Client{Transport: rt},
		ChatHTTP: &http.Client{Transport: rt},
	}
}

// setupRunGlobals 还原 run 改动的全局状态。
func setupRunGlobals(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		server.SetChatLogOutput(os.Stdout)
		auth.SetGlobalEnabled(true)
		upstream.SetModelCatalogPath("")
	})
}

// newRunTestConfig 默认配置 + 临时目录 + 一个假账号；关闭排程避免后台噪声。
func newRunTestConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	dir := t.TempDir()
	cfg.AuthDir = filepath.Join(dir, "auths")
	if err := os.MkdirAll(cfg.AuthDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.StateFile = filepath.Join(dir, "data", "state.json")
	cfg.APIKey = runKey
	cfg.Panel.UpdateCheck = false
	cfg.Upstash.URL, cfg.Upstash.Token = "", ""
	cfg.Schedule.CheckinEnabled = false
	cfg.Schedule.TravelEnabled = false
	cfg.Schedule.ActivityEnabled = false
	cfg.Schedule.KeepaliveEnabled = false
	cfg.Schedule.BlackcatEnabled = false
	cfg.Schedule.GrowthEnabled = false
	cfg.Schedule.BalanceRefreshEnabled = false
	cfg.Schedule.LivenessEnabled, cfg.LivenessInterval = false, 0

	a := &auth.Auth{
		UID: "u1", AccessToken: "at-u1", RefreshToken: "rt-u1",
		ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
		Domain:    "www.codebuddy.cn", Nickname: "测试号",
	}
	a.FilePath = filepath.Join(cfg.AuthDir, "workbuddy-u1.json")
	if err := a.SaveAtomic(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// waitHealthy 带截止时间轮询 /healthz（不用 /readyz：SetDraining 不可复位）。
func waitHealthy(t *testing.T, addr string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := client.Get("http://" + addr + "/healthz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 /healthz 超时: addr=%s err=%v", addr, err)
		}
		<-time.After(10 * time.Millisecond)
	}
}

// waitServing 带截止时间轮询 /healthz，有应答就算服务起来了，不管状态码：号都用不了时 /healthz 是 503，
// 只要接口能用的测试等它就行。
func waitServing(t *testing.T, addr string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := client.Get("http://" + addr + "/healthz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待服务起来超时: addr=%s err=%v", addr, err)
		}
		<-time.After(10 * time.Millisecond)
	}
}

type runOutcome struct {
	restart bool
	err     error
}

// startRun 在后台跑 run，返回结果通道与监听地址。
func startRun(t *testing.T, ctx context.Context, cfg *Config, up *upstream.Client, ln net.Listener) (chan runOutcome, string) {
	t.Helper()
	out := make(chan runOutcome, 1)
	go func() {
		restart, err := run(ctx, cfg, filepath.Join(t.TempDir(), "config.json"), &selfupdate.Updater{}, up, ln)
		out <- runOutcome{restart, err}
	}()
	return out, ln.Addr().String()
}

// awaitRun 等 run 返回。
func awaitRun(t *testing.T, out chan runOutcome) runOutcome {
	t.Helper()
	select {
	case r := <-out:
		return r
	case <-time.After(15 * time.Second):
		t.Fatal("等待 run 返回超时")
		return runOutcome{}
	}
}

// doChat 发一次非流式对话（触发号池记账 → 状态文件 dirty）。
func doChat(t *testing.T, addr string) {
	t.Helper()
	body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+runKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("对话请求失败: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("对话 HTTP %d", resp.StatusCode)
	}
}

// loadOrInitConfig：文件存在/不存在（可写与不可写）/内容非法。
func TestLoadOrInitConfig(t *testing.T) {
	t.Run("文件存在且合法", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"api_key":"k-existing"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadOrInitConfig(path)
		if err != nil || cfg.APIKey != "k-existing" {
			t.Fatalf("cfg=%+v err=%v", cfg, err)
		}
	})

	t.Run("文件不存在时生成推荐配置", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		cfg, err := loadOrInitConfig(path)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if cfg.APIKey == "" {
			t.Fatal("自动生成的配置应带随机 api_key")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("推荐配置文件应已写出: %v", err)
		}
	})

	t.Run("目录不可写时退回默认配置", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root 下权限位不生效")
		}
		dir := filepath.Join(t.TempDir(), "ro")
		if err := os.MkdirAll(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		t.Setenv("WB2A_API_KEY", "")
		cfg, err := loadOrInitConfig(filepath.Join(dir, "config.json"))
		if err != nil || cfg == nil {
			t.Fatalf("应退回默认配置: cfg=%v err=%v", cfg, err)
		}
		if cfg.APIKey != "" || cfg.Listen != ":7863" {
			t.Fatalf("默认配置不对: %+v", cfg)
		}
	})

	t.Run("内容非法返回错误", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{broken`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadOrInitConfig(path); err == nil {
			t.Fatal("非法配置应报错")
		}
	})
}

// run 正常启动：健康检查、模型列表、面板可打开；取消后优雅停机并落盘。
func TestRunStartupAndShutdown(t *testing.T) {
	setupRunGlobals(t)
	cfg := newRunTestConfig(t)
	rt := &runRT{}
	up := newRunUpstream(rt)
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, addr := startRun(t, ctx, cfg, up, ln)
	waitHealthy(t, addr)

	// /v1/models：带 api_key，返回假上游的目录。
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+runKey)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "cn:glm-5.2") {
		t.Fatalf("models = %d %s", resp.StatusCode, body)
	}

	// 面板页面能打开。
	presp, err := (&http.Client{Timeout: 10 * time.Second}).Get("http://" + addr + "/panel/")
	if err != nil || presp.StatusCode != http.StatusOK {
		t.Fatalf("panel = %v, %v", presp, err)
	}
	_, _ = io.Copy(io.Discard, presp.Body)
	_ = presp.Body.Close()

	// 一次对话让号池记账 dirty，停机 Flush 时才会写 state.json。
	doChat(t, addr)

	// 面板上签到一次：领到的积分记进积分获取流水，停机时刷盘。
	creq, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/panel/api/accounts/u1/checkin", nil)
	creq.Header.Set("Authorization", "Bearer "+runKey)
	cresp, err := (&http.Client{Timeout: 10 * time.Second}).Do(creq)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, cresp.Body)
	_ = cresp.Body.Close()
	if cresp.StatusCode != http.StatusOK {
		t.Fatalf("签到 = %d", cresp.StatusCode)
	}

	cancel()
	res := awaitRun(t, out)
	if res.restart || res.err != nil {
		t.Fatalf("停机结果 = %+v", res)
	}
	if _, err := os.Stat(cfg.StateFile); err != nil {
		t.Fatalf("停机后状态文件应已落盘: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(usagePathFor(cfg.StateFile))); err != nil {
		t.Fatalf("用量文件所在目录应存在: %v", err)
	}
	earned, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg.StateFile), "credit-logs", "credit-*.jsonl"))
	if len(earned) != 1 {
		t.Fatalf("积分获取流水应有一个文件: %v", earned)
	}
	if b, _ := os.ReadFile(earned[0]); !strings.Contains(string(b), `"source":"checkin"`) || !strings.Contains(string(b), `"credit":20`) {
		t.Fatalf("积分获取流水应记下这次签到: %s", b)
	}
	// 停机后 /readyz 要么连接已关，要么 503。
	if r, err := (&http.Client{Timeout: time.Second}).Get("http://" + addr + "/readyz"); err == nil {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		if r.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("停机后 /readyz = %d", r.StatusCode)
		}
	}
}

// 停机时有在途请求：run 要等在途请求完成才返回（宽限期调短避免拖慢）。
func TestRunShutdownWaitsInFlight(t *testing.T) {
	setupRunGlobals(t)
	t.Setenv("WB2A_SHUTDOWN_TIMEOUT", "10s")
	cfg := newRunTestConfig(t)
	rt := &runRT{chatStarted: make(chan struct{}, 1), chatRelease: make(chan struct{})}
	up := newRunUpstream(rt)
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, addr := startRun(t, ctx, cfg, up, ln)
	waitHealthy(t, addr)

	chatDone := make(chan error, 1)
	go func() {
		body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":false}`
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+runKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = io.ErrUnexpectedEOF
			}
		}
		chatDone <- err
	}()
	select {
	case <-rt.chatStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("对话请求没有到达上游")
	}

	cancel()
	select {
	case r := <-out:
		t.Fatalf("在途请求未结束时 run 不应返回: %+v", r)
	case <-time.After(150 * time.Millisecond):
	}
	close(rt.chatRelease)
	if err := <-chatDone; err != nil {
		t.Fatalf("在途请求应完成: %v", err)
	}
	if res := awaitRun(t, out); res.restart || res.err != nil {
		t.Fatalf("停机结果 = %+v", res)
	}
}

// 一键更新触发重启：run 返回 (true, nil)。
func TestRunRestart(t *testing.T) {
	setupRunGlobals(t)
	cfg := newRunTestConfig(t)
	up := newRunUpstream(&runRT{})
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upd := &selfupdate.Updater{}
	out := make(chan runOutcome, 1)
	go func() {
		restart, err := run(ctx, cfg, filepath.Join(t.TempDir(), "config.json"), upd, up, ln)
		out <- runOutcome{restart, err}
	}()
	waitHealthy(t, ln.Addr().String())
	upd.Restart()
	if res := awaitRun(t, out); !res.restart || res.err != nil {
		t.Fatalf("重启结果 = %+v", res)
	}
}

// ln 为 nil 且监听端口被占：返回 http 错误，不卡住。
func TestRunListenFailure(t *testing.T) {
	setupRunGlobals(t)
	cfg := newRunTestConfig(t)
	occupied, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	cfg.Listen = occupied.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan runOutcome, 1)
	go func() {
		restart, err := run(ctx, cfg, filepath.Join(t.TempDir(), "config.json"), &selfupdate.Updater{}, newRunUpstream(&runRT{}), nil)
		done <- runOutcome{restart, err}
	}()
	res := awaitRun(t, done)
	if res.restart || res.err == nil || !strings.Contains(res.err.Error(), "http:") {
		t.Fatalf("监听失败结果 = %+v", res)
	}
}

// SessionSticky / Global 开关组合都能正常启动和停机。
func TestRunToggleVariants(t *testing.T) {
	cases := []struct {
		name   string
		sticky bool
		global bool
	}{
		{"sticky-off-global-off", false, false},
		{"sticky-on-global-off", true, false},
		{"sticky-off-global-on", false, true},
		{"sticky-on-global-on", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupRunGlobals(t)
			cfg := newRunTestConfig(t)
			cfg.SessionSticky.Enabled = tc.sticky
			cfg.Global.Enabled = tc.global
			up := newRunUpstream(&runRT{})
			ln, err := net.Listen("tcp", "localhost:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			out, addr := startRun(t, ctx, cfg, up, ln)
			waitHealthy(t, addr)
			cancel()
			if res := awaitRun(t, out); res.restart || res.err != nil {
				t.Fatalf("停机结果 = %+v", res)
			}
		})
	}
}

// 账号目录读不了：返回 load auths 错误，不监听。
func TestRunLoadAuthsError(t *testing.T) {
	setupRunGlobals(t)
	cfg := newRunTestConfig(t)
	// 目录名含未闭合的 "["：filepath.Glob 返回 ErrBadPattern → LoadDir 报错。
	cfg.AuthDir = filepath.Join(t.TempDir(), "[")
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restart, err := run(ctx, cfg, filepath.Join(t.TempDir(), "config.json"), &selfupdate.Updater{}, newRunUpstream(&runRT{}), ln)
	if restart || err == nil || !strings.Contains(err.Error(), "load auths") {
		t.Fatalf("结果 = %v, %v", restart, err)
	}
}
