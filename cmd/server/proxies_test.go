// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// proxies_test.go 启动时装配代理池（proxies.go、run）：文件坏了不启动；有代理时打载入日志，号的绑定照
// 凭证文件，面板的代理接口接上。代理和查出口 IP 的地址都指到本机关着的端口，健康检查连不出去。

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/selfupdate"
)

// writeProxiesFile 在 state 文件旁边写 proxies.json。
func writeProxiesFile(t *testing.T, cfg *Config, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(cfg.StateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateSibling(cfg.StateFile, "proxies.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 代理池文件读不了就不启动：当成没有代理，本该走代理的号会从服务器 IP 直接出去。
func TestRunProxiesFileError(t *testing.T) {
	setupRunGlobals(t)
	cfg := newRunTestConfig(t)
	writeProxiesFile(t, cfg, `{`)
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restart, err := run(ctx, cfg, filepath.Join(t.TempDir(), "config.json"), &selfupdate.Updater{}, newRunUpstream(&runRT{}), ln)
	if restart || err == nil || !strings.Contains(err.Error(), "解析代理池") {
		t.Fatalf("结果 = %v, %v", restart, err)
	}
}

// 有代理：启动时打一行载入日志，号的绑定照凭证文件，面板的代理接口能用。
func TestRunProxiesLoaded(t *testing.T) {
	setupRunGlobals(t)
	cfg := newRunTestConfig(t)
	writeProxiesFile(t, cfg, `{"settings":{"exit_ip_urls":["http://127.0.0.1:1/json"]},
		"pools":[{"id":"p1","name":"池"}],"proxies":[{"id":"x1","name":"甲","url":"http://127.0.0.1:1","pool":"p1"}]}`)
	path := filepath.Join(cfg.AuthDir, "workbuddy-u1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	a.FilePath = path
	a.SetProxyBinding(auth.ProxyBinding{Pool: "p1", ID: "x1"})
	if err := a.SaveAtomic(); err != nil {
		t.Fatal(err)
	}
	// 载入日志在面板接管日志输出之前打，先接到这里（run 返回后才读）。
	var logs bytes.Buffer
	log.SetOutput(&logs)

	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, addr := startRun(t, ctx, cfg, newRunUpstream(&runRT{}), ln)
	// 不等 /healthz 转 200：唯一的号绑的代理连不上，代理池第一轮健康检查后号就停了，/healthz 一直 503。
	waitServing(t, addr)

	code, body := panelRequest(t, http.MethodGet, "http://"+addr+"/panel/api/proxies", "")
	var list struct {
		View struct {
			Proxies []struct {
				Name     string   `json:"name"`
				Accounts []string `json:"accounts"`
			} `json:"proxies"`
		} `json:"view"`
	}
	if err := json.Unmarshal([]byte(body), &list); code != http.StatusOK || err != nil ||
		len(list.View.Proxies) != 1 || list.View.Proxies[0].Name != "甲" || len(list.View.Proxies[0].Accounts) != 1 {
		t.Fatalf("代理接口 = %d %s（%v）", code, body, err)
	}

	cancel()
	if res := awaitRun(t, out); res.restart || res.err != nil {
		t.Fatalf("停机结果 = %+v", res)
	}
	if !strings.Contains(logs.String(), "[proxy] 代理池已载入：1 个池、1 个代理") {
		t.Fatalf("日志:\n%s", logs.String())
	}
}
