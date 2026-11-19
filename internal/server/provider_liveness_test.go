// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/providers"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

func TestQoderLivenessWrappedDone(t *testing.T) {
	var chats atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/algo/api/v2/model/list":
			fmt.Fprint(w, `{"statusCodeValue":200,"body":[{"key":"auto","enable":true}]}`)
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			chats.Add(1)
			for _, body := range []string{
				`{"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6,"credits":0.012345,"billable":true}}`,
				`[DONE]`,
			} {
				frame, _ := json.Marshal(map[string]any{"statusCodeValue": 200, "body": body})
				fmt.Fprintf(w, "data: %s\n\n", frame)
			}
		default:
			t.Errorf("Qoder 测活发出未预期请求: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	base, _ := url.Parse(backend.URL)
	path := filepath.Join(t.TempDir(), "providers.json")
	m, err := providers.Open(path, &http.Client{Transport: providerTransport{base: base}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.Import(context.Background(), providers.AccountInput{Target: "qoder:cn", Credential: providers.Credential{UID: "fixture", AccessToken: "fixture-token", UserType: "teams", ExpiresAt: time.Now().Add(time.Hour).Unix()}})
	if err != nil {
		t.Fatal(err)
	}
	h, _, wbCalls := keyRig(t, "sk-admin")
	h.cfg.Providers = m
	logs := reqlog.New(reqlog.Config{})
	defer logs.Close()
	h.cfg.RequestLog = logs
	out, err := h.TestProviderAccount(context.Background(), id, "qoder:cn:auto")
	if err != nil || !out.Result.OK || out.Result.Error != "" || out.Result.Status != 200 || out.Reply != "hello" || !out.Result.TokensKnown || out.Result.PromptTokens != 4 || out.Result.CompletionTokens != 2 {
		t.Fatalf("Qoder 回复完成后被误判失败或丢失用量: %+v %v", out, err)
	}
	if chats.Load() != 1 || *wbCalls != 0 {
		t.Fatalf("Qoder 测活重复调用或跨平台: chats=%d wb=%d", chats.Load(), *wbCalls)
	}
	if !out.Result.HasCredit || out.Result.Credit != 0.012345 || out.Result.CreditUnit != "credits" {
		t.Fatal("测活结果丢失单次扣费", out.Result)
	}
	if event := logs.Snapshot().Recent[0]; !event.OK || event.Error != "" || !event.TokensKnown || event.TotalTokens != 6 {
		t.Fatalf("成功的 Qoder 测活被记为失败: %+v", event)
	}
	ledger := m.Usage()
	if len(ledger) != 1 || ledger[0].Requests != 1 || ledger[0].Failed != 0 || ledger[0].TokensKnown != 1 {
		t.Fatalf("Qoder 成功测活计量错误: %+v", ledger)
	}
	reopened, err := providers.Open(path, nil)
	if err != nil || reopened.List()[0].LastLiveness == nil || !reopened.List()[0].LastLiveness.OK || !reopened.List()[0].CoolUntil.IsZero() {
		t.Fatalf("Qoder 成功结果未保存或仍在冷却: %v", err)
	}
	if last := reopened.List()[0].LastLiveness; !last.HasCredit || last.Credit != 0.012345 || last.CreditUnit != "credits" {
		t.Fatal("测活费用未持久化", last)
	}
}

func TestProviderLivenessUsesChatLogsUsageAndPersistsResult(t *testing.T) {
	var chats atomic.Int32
	var fail atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ide/v1/get_detail_param":
			fmt.Fprint(w, `{"config_info_list":[{"config_name":"model","display_config":{"display_name":"model"}}]}`)
		case "/api/agent/v3/llm_utils_chat":
			chats.Add(1)
			var body struct {
				MaxTokens int `json:"max_tokens"`
				Messages  []struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MaxTokens != 16 || len(body.Messages) != 1 || len(body.Messages[0].Content) != 1 || body.Messages[0].Content[0].Text != "hi" {
				t.Errorf("测活请求未使用短消息: body=%+v err=%v", body, err)
			}
			if fail.Load() {
				http.Error(w, `{"code":"rate_limit"}`, http.StatusTooManyRequests)
				return
			}
			fmt.Fprint(w, "event: output\ndata: {\"response\":\"hi\"}\n\nevent: token_usage\ndata: {\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}\n\nevent: done\ndata: {}\n\n")
		default:
			t.Errorf("测活发出未预期请求: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	base, _ := url.Parse(backend.URL)
	path := filepath.Join(t.TempDir(), "providers.json")
	m, err := providers.Open(path, &http.Client{Transport: providerTransport{base: base}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.Import(context.Background(), providers.AccountInput{Target: "traecode:cn", Credential: providers.Credential{UID: "fixture", AccessToken: "fixture-token", ExpiresAt: time.Now().Add(time.Hour).Unix()}})
	if err != nil {
		t.Fatal(err)
	}
	h, _, wbCalls := keyRig(t, "sk-admin")
	h.cfg.Providers = m
	recorder := usage.New("")
	m.SetUsage(recorder)
	logs := reqlog.New(reqlog.Config{})
	defer logs.Close()
	h.cfg.RequestLog = logs
	for _, failure := range []bool{false, true} {
		fail.Store(failure)
		out, err := h.TestProviderAccount(context.Background(), id, "traecode:cn:model")
		if err != nil || out.Result.OK == failure || out.Result.Model != "traecode:cn:model" {
			t.Fatalf("测活结果错误: %+v %v", out, err)
		}
		if !failure && (out.Reply != "hi" || !out.Result.TokensKnown || out.Result.PromptTokens != 4 || out.Result.CompletionTokens != 2) {
			t.Fatalf("回复或用量丢失: %+v", out)
		}
		if failure && (out.Result.Status != 429 || !strings.Contains(out.Result.Error, "rate_limit")) {
			t.Fatalf("错误未保留: %+v", out)
		}
		events := logs.Snapshot().Recent
		e := events[0]
		if e.Path != livenessPath || e.APIKey != livenessLabel || e.AccountID != id || e.Target != "traecode:cn" || e.Attempts != 1 || e.OK == failure {
			t.Fatalf("请求记录归属不对: %+v", e)
		}
		reopened, err := providers.Open(path, nil)
		if err != nil || reopened.List()[0].LastLiveness == nil || reopened.List()[0].LastLiveness.OK == failure {
			t.Fatalf("上次测活未落盘: %v", err)
		}
	}
	if chats.Load() != 2 || *wbCalls != 0 {
		t.Fatal("测活重复调用或漏到 WorkBuddy", chats.Load(), *wbCalls)
	}
	u := m.Usage()
	if len(u) != 1 || u[0].KeyID != livenessUsageKey || u[0].Requests != 2 || u[0].Failed != 1 {
		t.Fatalf("平台测活用量错误: %+v", u)
	}
	snapshot := recorder.SnapshotWindow(usage.Window{Target: "traecode:cn", Key: livenessUsageKey, KeySet: true}, nil, nil)
	if snapshot.Totals.Requests != 2 || snapshot.Totals.Errors != 1 || snapshot.Totals.UnknownTokens != 1 || snapshot.Totals.TotalTokens != 6 {
		t.Fatal("测活没有进入时序账本", snapshot)
	}
	for _, settings := range []providers.AccountInput{
		{Disabled: true, LivenessModel: "traecode:cn:model"},
		{Paused: true, LivenessModel: "traecode:cn:model"},
		{},
	} {
		if err := m.Update(id, settings, false); err != nil {
			t.Fatal(err)
		}
		h.RunProviderLivenessNow()
		if chats.Load() != 2 {
			t.Fatal("定时测活没有尊重停用、暂停或缺少模型")
		}
	}
	if err := m.Update(id, providers.AccountInput{LivenessModel: "traecode:cn:model"}, false); err != nil {
		t.Fatal(err)
	}
	fail.Store(false)
	h.RunProviderLivenessNow()
	if chats.Load() != 3 || !m.List()[0].LastLiveness.Scheduled || !m.List()[0].LastLiveness.OK || !m.List()[0].CoolUntil.IsZero() {
		t.Fatal("定时恢复未接入", m.List())
	}
	for _, test := range []struct{ id, model string }{{"missing", "traecode:cn:model"}, {id, "qoder:cn:model"}, {id, "model"}, {id, "cn:model"}} {
		if _, err := h.TestProviderAccount(context.Background(), test.id, test.model); err == nil {
			t.Fatalf("非法测活被接受: %+v", test)
		}
	}
	if chats.Load() != 3 {
		t.Fatal("错误参数发出了真实请求")
	}
}
