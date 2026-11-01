// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// main_test.go 进程入口：-version、配置读取失败、run 失败、正常启动后取消、
// 一键更新重启时 reexec 成功/失败。main 里会调 os.Exit 的地方换成 panic 捕获。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/selfupdate"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// serverExit 捕获 exitFunc 的退出码。
type serverExit int

// mainEnv main 测试的全局装置：替换 flag、退出函数、进程入口与日志输出。
type mainEnv struct {
	logs   *bytes.Buffer
	stderr *os.File
}

func (e *mainEnv) output(t *testing.T) string {
	t.Helper()
	tail, err := os.ReadFile(e.stderr.Name())
	if err != nil {
		t.Fatalf("读 stderr 捕获文件: %v", err)
	}
	return e.logs.String() + string(tail)
}

// stubMainEnv 保存并替换 main 用到的全局状态，测完还原。
func stubMainEnv(t *testing.T) *mainEnv {
	t.Helper()
	oldArgs, oldFlags, oldExit := os.Args, flag.CommandLine, exitFunc
	oldLaunch, oldNotify := launchUpdater, notifyContext
	oldNewUp, oldReexec := newUpstream, reexec
	oldLog, oldStderr := log.Writer(), os.Stderr

	flag.CommandLine = flag.NewFlagSet("wb2api-test", flag.ExitOnError)
	exitFunc = func(code int) { panic(serverExit(code)) }

	stderr, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = stderr
	logs := &bytes.Buffer{}
	log.SetOutput(logs)

	t.Cleanup(func() {
		os.Args, flag.CommandLine, exitFunc = oldArgs, oldFlags, oldExit
		launchUpdater, notifyContext = oldLaunch, oldNotify
		newUpstream, reexec = oldNewUp, oldReexec
		log.SetOutput(oldLog)
		os.Stderr = oldStderr
		_ = stderr.Close()
	})
	return &mainEnv{logs: logs, stderr: stderr}
}

// callMain 同步调 main，返回退出码与是否退出。
func callMain(t *testing.T) (code int, exited bool) {
	t.Helper()
	func() {
		defer func() {
			if r := recover(); r != nil {
				p, ok := r.(serverExit)
				if !ok {
					panic(r)
				}
				code, exited = int(p), true
			}
		}()
		main()
	}()
	return code, exited
}

type mainDone struct {
	code   int
	exited bool
}

// goMain 后台跑 main。
func goMain(t *testing.T) chan mainDone {
	t.Helper()
	out := make(chan mainDone, 1)
	go func() {
		code, exited := callMain(t)
		out <- mainDone{code, exited}
	}()
	return out
}

func awaitMain(t *testing.T, out chan mainDone) mainDone {
	t.Helper()
	select {
	case r := <-out:
		return r
	case <-time.After(15 * time.Second):
		t.Fatal("等待 main 返回超时")
		return mainDone{}
	}
}

// freeAddr 拿一个已释放的空端口地址。
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// writeMainConfig 把配置写到临时文件并返回路径。
func writeMainConfig(t *testing.T, cfg *Config) string {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// stubMainUpstream 把进程入口换成测试手里的 client，并让启动后的取消失控。
func stubMainUpstream(up *upstream.Client) chan context.CancelFunc {
	cancels := make(chan context.CancelFunc, 1)
	notifyContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		cancels <- cancel
		return ctx, cancel
	}
	newUpstream = func() *upstream.Client { return up }
	return cancels
}

// panelRequest 带 api_key 访问面板接口。
func panelRequest(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+runKey)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// -version：打印版本号并以 0 退出，不读配置、不起服务。
func TestMainVersionFlag(t *testing.T) {
	env := stubMainEnv(t)
	os.Args = []string{"wb2api", "-version"}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code, exited := callMain(t)
	_ = w.Close()
	os.Stdout = oldStdout
	raw, _ := io.ReadAll(r)
	_ = r.Close()

	if !exited || code != 0 {
		t.Fatalf("退出码 = %d exited=%v want 0,true", code, exited)
	}
	if got := strings.TrimSpace(string(raw)); got != appVersion {
		t.Fatalf("stdout = %q want %q", got, appVersion)
	}
	if env.output(t) != "" {
		t.Fatalf("-version 不应有日志: %s", env.output(t))
	}
}

// 配置读不了：打印 load config 并以 1 退出。
func TestMainLoadConfigError(t *testing.T) {
	env := stubMainEnv(t)
	dir := filepath.Join(t.TempDir(), "config-is-dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"wb2api", "-config", dir}

	code, exited := callMain(t)
	if !exited || code != 1 {
		t.Fatalf("退出码 = %d exited=%v want 1,true", code, exited)
	}
	logs := env.output(t)
	if !strings.Contains(logs, "load config:") || !strings.Contains(logs, "是目录而非文件") {
		t.Fatalf("日志 = %q", logs)
	}
}

// run 返回错误：打印错误原文并以 1 退出。
func TestMainRunError(t *testing.T) {
	env := stubMainEnv(t)
	cfg := newRunTestConfig(t)
	cfg.AuthDir = filepath.Join(t.TempDir(), "[") // Glob ErrBadPattern
	path := writeMainConfig(t, cfg)

	launchUpdater = func(string, string) *selfupdate.Updater { return &selfupdate.Updater{} }
	stubMainUpstream(newRunUpstream(&runRT{}))
	os.Args = []string{"wb2api", "-config", path}

	code, exited := callMain(t)
	if !exited || code != 1 {
		t.Fatalf("退出码 = %d exited=%v want 1,true", code, exited)
	}
	if logs := env.output(t); !strings.Contains(logs, "load auths") {
		t.Fatalf("日志 = %q", logs)
	}
}

// 正常启动：面板接口可用（含 Config 读写闭包与 StickyCount），取消后不调 exitFunc/reexec，
// 状态文件已落盘。
func TestMainStartupAndShutdown(t *testing.T) {
	env := stubMainEnv(t)
	cfg := newRunTestConfig(t)
	cfg.Listen = freeAddr(t)
	path := writeMainConfig(t, cfg)

	upd := &selfupdate.Updater{}
	launchUpdater = func(string, string) *selfupdate.Updater { return upd }
	var reexecCalls atomic.Int32
	reexec = func(*selfupdate.Updater) error { reexecCalls.Add(1); return nil }
	cancels := stubMainUpstream(newRunUpstream(&runRT{}))
	os.Args = []string{"wb2api", "-config", path}

	out := goMain(t)
	cancel := <-cancels
	startURL := "http://" + cfg.Listen
	waitHealthy(t, cfg.Listen)

	if code, body := panelRequest(t, http.MethodGet, startURL+"/panel/api/overview", ""); code != 200 {
		t.Fatalf("overview = %d %s", code, body)
	} else if !strings.Contains(body, `"sticky_sessions":0`) || !strings.Contains(body, `"redis_mode":"noop"`) {
		t.Fatalf("overview 内容不对: %s", body)
	}
	if code, body := panelRequest(t, http.MethodGet, startURL+"/panel/api/config", ""); code != 200 {
		t.Fatalf("get config = %d %s", code, body)
	}
	// 一次对话让号池记账 dirty，停机 Flush 时才写 state.json。
	doChat(t, cfg.Listen)
	if code, body := panelRequest(t, http.MethodPost, startURL+"/panel/api/config", `{"api_key":"`+runKey+`"}`); code != 200 {
		t.Fatalf("save config = %d %s", code, body)
	}

	cancel()
	res := awaitMain(t, out)
	if res.exited {
		t.Fatalf("正常停机不应调 exitFunc（code=%d 日志=%s）", res.code, env.output(t))
	}
	if reexecCalls.Load() != 0 {
		t.Fatal("正常停机不应调 reexec")
	}
	if _, err := os.Stat(cfg.StateFile); err != nil {
		t.Fatalf("停机后状态文件应已落盘: %v", err)
	}
}

// 首字节超时接到上游客户端（各条线路的 Transport 由 SetHeaderTimeout 设，见 upstream 的测试）；sticky
// 关闭时 StickyCount 返回 0。
func TestMainTransportTimeoutAndStickyOff(t *testing.T) {
	stubMainEnv(t)
	cfg := newRunTestConfig(t)
	cfg.Listen = freeAddr(t)
	cfg.SessionSticky.Enabled = false
	path := writeMainConfig(t, cfg)

	up := upstream.New()
	up.HTTP = &http.Client{Transport: &runRT{}}
	launchUpdater = func(string, string) *selfupdate.Updater { return &selfupdate.Updater{} }
	cancels := stubMainUpstream(up)
	os.Args = []string{"wb2api", "-config", path}

	out := goMain(t)
	cancel := <-cancels
	waitHealthy(t, cfg.Listen)

	startURL := "http://" + cfg.Listen
	if code, body := panelRequest(t, http.MethodGet, startURL+"/panel/api/overview", ""); code != 200 ||
		!strings.Contains(body, `"sticky_sessions":0`) {
		t.Fatalf("overview = %d %s", code, body)
	}
	if want := time.Duration(cfg.Upstream.HeaderTimeoutSeconds) * time.Second; want == 0 || up.HeaderTimeout != want {
		t.Fatalf("header timeout 未接线: up=%v want=%v", up.HeaderTimeout, want)
	}

	cancel()
	if res := awaitMain(t, out); res.exited {
		t.Fatalf("正常停机不应调 exitFunc: %+v", res)
	}
}

// 一键更新重启：reexec 被调用一次，不调 exitFunc。
func TestMainRestartReexecOK(t *testing.T) {
	stubMainEnv(t)
	cfg := newRunTestConfig(t)
	cfg.Listen = freeAddr(t)
	path := writeMainConfig(t, cfg)

	upd := &selfupdate.Updater{}
	launchUpdater = func(string, string) *selfupdate.Updater { return upd }
	var reexecCalls atomic.Int32
	reexec = func(*selfupdate.Updater) error { reexecCalls.Add(1); return nil }
	stubMainUpstream(newRunUpstream(&runRT{}))
	os.Args = []string{"wb2api", "-config", path}

	out := goMain(t)
	waitHealthy(t, cfg.Listen)
	upd.Restart()

	res := awaitMain(t, out)
	if res.exited {
		t.Fatalf("reexec 成功不应调 exitFunc: %+v", res)
	}
	if got := reexecCalls.Load(); got != 1 {
		t.Fatalf("reexec 调用 %d 次 want 1", got)
	}
}

// reexec 失败：打印“重启失败”并以 1 退出。
func TestMainRestartReexecError(t *testing.T) {
	env := stubMainEnv(t)
	cfg := newRunTestConfig(t)
	cfg.Listen = freeAddr(t)
	path := writeMainConfig(t, cfg)

	upd := &selfupdate.Updater{}
	launchUpdater = func(string, string) *selfupdate.Updater { return upd }
	reexec = func(*selfupdate.Updater) error { return errors.New("exec boom") }
	stubMainUpstream(newRunUpstream(&runRT{}))
	os.Args = []string{"wb2api", "-config", path}

	out := goMain(t)
	waitHealthy(t, cfg.Listen)
	upd.Restart()

	res := awaitMain(t, out)
	if !res.exited || res.code != 1 {
		t.Fatalf("退出码 = %d exited=%v want 1,true", res.code, res.exited)
	}
	if logs := env.output(t); !strings.Contains(logs, "重启中") || !strings.Contains(logs, "重启失败：exec boom") {
		t.Fatalf("日志 = %q", logs)
	}
}
