// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/apikeys"
	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/providers"
	"github.com/yreidev/workbuddy2api-panel/internal/providers/cosy"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
)

type providerTransport struct{ base *url.URL }

func (p providerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == "center.qoder.sh" && r.URL.Path == "/algo/api/v3/service/region/endpoints" {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"inferNodes":["https://api2.qoder.sh"]}`))}, nil
	}
	cloned := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = p.base.Scheme
	u.Host = p.base.Host
	cloned.URL = &u
	return http.DefaultTransport.RoundTrip(cloned)
}

func TestProviderThreeProtocolsAndPermissions(t *testing.T) {
	var chats atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ide/v1/get_detail_param":
			fmt.Fprint(w, `{"config_info_list":[{"config_name":"model","display_config":{"display_name":"model"}}]}`)
		case "/algo/api/v2/model/list":
			fmt.Fprint(w, `{"code":200,"body":[{"key":"model","enable":true}]}`)
		case "/api/remote/v1/models":
			fmt.Fprint(w, `{"code":0,"data":{"list":[{"function":"solo_work_remote","models":[{"name":"model","config_source":1,"is_preset":true}]}]}}`)
		case "/api/remote/v1/chat_sessions":
			chats.Add(1)
			var payload struct {
				Initial struct {
					Query string `json:"query"`
					Model struct {
						ConfigName string `json:"config_name"`
						ModelName  string `json:"model_name"`
					} `json:"custom_model"`
				} `json:"initial_message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				return
			}
			var query []struct {
				Type string `json:"type"`
				Data struct {
					Content string `json:"content"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(payload.Initial.Query), &query); err != nil || len(query) != 1 || query[0].Type != "text" || !strings.Contains(query[0].Data.Content, "系统规则") || !strings.Contains(query[0].Data.Content, "问题一\n问题二") || payload.Initial.Model.ConfigName != "model" || payload.Initial.Model.ModelName != "model" {
				t.Error("三协议入口未生成官方 Work 文本块和模型描述", err, payload)
				w.WriteHeader(500)
				fmt.Fprint(w, `{"code":995000}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"chat_session_id":"s","message_id":"m"}}`)
		case "/api/v1/commercial/get_session_usage":
			fmt.Fprint(w, `{"user_usage_group_by_session":{"session_id":"m","credits_float":0.012345}}`)
		case "/api/remote/v1/chat_sessions/s/events":
			fmt.Fprint(w, "event: output\ndata: {\"response\":\"hello\"}\n\nevent: done\ndata: {}\n\n")
		case "/api/agent/v3/llm_utils_chat":
			chats.Add(1)
			fmt.Fprint(w, "event: progress_notice\ndata: \"正在处理请求\"\n\n")
			fmt.Fprint(w, "event: output\ndata: {\"response\":\"hello\"}\n\nevent: token_usage\ndata: {\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}\n\nevent: done\ndata: {}\n\n")
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			chats.Add(1)
			encoded, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			decoded, err := cosy.Decode(string(encoded))
			if err != nil {
				t.Error(err)
				return
			}
			var request struct {
				System      string `json:"system"`
				ChatContext struct {
					Text struct {
						Text string `json:"text"`
					} `json:"text"`
				} `json:"chat_context"`
			}
			if err := json.Unmarshal(decoded, &request); err != nil {
				t.Error(err)
				return
			}
			if !strings.Contains(request.System, "系统规则") || !strings.Contains(request.ChatContext.Text.Text, "问题一") || !strings.Contains(request.ChatContext.Text.Text, "问题二") {
				t.Errorf("入口翻译后 Qoder 系统提示词或问题丢失：%+v", request)
			}
			inner := map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "hello"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 2, "total_tokens": 6}}
			raw, _ := json.Marshal(inner)
			outer, _ := json.Marshal(map[string]any{"code": 200, "body": string(raw)})
			fmt.Fprintf(w, "data: %s\n\n", outer)
			billing, _ := json.Marshal(map[string]any{"code": 200, "body": `{"choices":[],"usage":{"credits":0.125,"original_credits":1,"billable":true}}`})
			fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", billing, billing)
		default:
			t.Errorf("未预期的平台请求: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	base, _ := url.Parse(backend.URL)
	manager, err := providers.Open("", &http.Client{Transport: providerTransport{base: base}})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"traework:cn", "traecode:cn", "qoder:cn", "qoder:global"} {
		_, err := manager.Import(context.Background(), providers.AccountInput{Target: target, Groups: []string{"allowed"}, Credential: providers.Credential{UID: "fixture", UserType: "teams", AccessToken: "fixture-token", ExpiresAt: time.Now().Add(time.Hour).Unix()}})
		if err != nil {
			t.Fatal(err)
		}
	}
	h, keys, wbCalls := keyRig(t, "sk-admin")
	h.cfg.Providers = manager
	logs := reqlog.New(reqlog.Config{})
	defer logs.Close()
	h.cfg.RequestLog = logs
	for _, target := range []string{"traework:cn", "traecode:cn", "qoder:cn", "qoder:global"} {
		for _, stream := range []bool{false, true} {
			for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
				key := createKey(t, keys, apikeys.Input{Name: fmt.Sprintf("%s %t %s", target, stream, path), Enabled: true, AllowedTargets: []string{target}, DefaultTarget: target, Groups: []string{"allowed"}})
				var content string
				switch path {
				case "/v1/chat/completions":
					content = `"messages":[{"role":"developer","content":[{"type":"text","text":"系统规则"}]},{"role":"user","content":[{"type":"text","text":"问题一\n"},{"type":"text","text":"问题二"}]}]`
				case "/v1/messages":
					content = `"system":[{"type":"text","text":"系统规则"}],"messages":[{"role":"user","content":[{"type":"text","text":"问题一\n"},{"type":"text","text":"问题二"}]}]`
				case "/v1/responses":
					content = `"instructions":"系统规则","input":[{"role":"user","content":[{"type":"input_text","text":"问题一\n"},{"type":"input_text","text":"问题二"}]}]`
				}
				body := fmt.Sprintf(`{"model":"model","stream":%t,"max_tokens":100,%s}`, stream, content)
				response := post(h, path, body, "Authorization", "Bearer "+key.Secret)
				if response.Code != 200 || !strings.Contains(response.Body.String(), "hello") {
					t.Fatalf("target=%s stream=%v path=%s status=%d body=%s", target, stream, path, response.Code, response.Body)
				}
				events := logs.Snapshot().Recent
				event := events[len(events)-1]
				eventFound := false
				// 环形日志按时间倒序，按本次密钥的展示名定位。
				for _, candidate := range events {
					if candidate.APIKey == key.Name {
						event = candidate
						eventFound = true
						break
					}
				}
				if !eventFound {
					t.Fatal("本次请求没有独立日志", key.Name)
				}
				savedKey, exists := keys.Get(key.ID, time.Now())
				if !exists || savedKey.Used.Credits != 0 || savedKey.Today.Credits != 0 {
					t.Fatal("新平台费用计入了 WorkBuddy 限额")
				}

				wantCredit := 0.125
				if target == "traework:cn" {
					wantCredit = 0.012345
				}
				if target != "traecode:cn" && (!event.HasCredit || event.Credit != wantCredit || event.CreditUnit != "credits") {
					t.Fatalf("三协议单次计费丢失 target=%s stream=%v path=%s event=%+v", target, stream, path, event)
				}
				if target == "traecode:cn" && event.HasCredit {
					t.Fatal("Code 凭空生成扣费", event)
				}
				if stream && path == "/v1/chat/completions" && target != "traework:cn" && !strings.Contains(response.Body.String(), `"prompt_tokens":4`) {
					t.Fatal("分帧费用覆盖了 token", response.Body)
				}

				if stream && path == "/v1/messages" && !strings.Contains(response.Body.String(), "message_stop") {
					t.Fatal("Anthropic 流未结束", response.Body)
				}
				if stream && path == "/v1/responses" && !strings.Contains(response.Body.String(), "response.completed") {
					t.Fatal("Responses 流未结束", response.Body)
				}
			}
		}
	}
	if *wbCalls != 0 {
		t.Fatal("新平台请求漏到 WorkBuddy", *wbCalls)
	}
	old := createKey(t, keys, apikeys.Input{Name: "legacy", Enabled: true, Models: []string{"*"}})
	before := chats.Load()
	response := post(h, "/v1/chat/completions", `{"model":"qoder:cn:model","messages":[{"role":"user","content":"hi"}]}`, "Authorization", "Bearer "+old.Secret)
	if response.Code != 403 || chats.Load() != before {
		t.Fatalf("旧密钥越权 %d %s", response.Code, response.Body)
	}
	outside := createKey(t, keys, apikeys.Input{Name: "outside", Enabled: true, AllowedTargets: []string{"qoder:cn"}, Groups: []string{"outside"}})
	response = post(h, "/v1/chat/completions", `{"model":"qoder:cn:model","messages":[{"role":"user","content":"hi"}]}`, "Authorization", "Bearer "+outside.Secret)
	if response.Code != 503 || chats.Load() != before {
		t.Fatal("组外账号被调用", response.Code, response.Body)
	}
	snapshot := logs.Snapshot()
	if len(snapshot.Recent) == 0 {
		t.Fatal("平台调用未记录")
	}
	found := false
	for _, event := range snapshot.Recent {
		if event.Target == "qoder:global" && event.AccountID != "" && event.TokensKnown {
			found = true
		}
	}
	if !found {
		t.Fatal("请求记录缺少平台、稳定账号或用量完整性")
	}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+old.Secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "qoder:") || strings.Contains(rec.Body.String(), "traecode:") {
		t.Fatal("旧密钥模型目录扩权", rec.Body)
	}
}

func TestProviderParallelToolsAndResultReturn(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, protocol := range []string{"openai", "claude", "codex"} {
			t.Run(fmt.Sprintf("%s/%t", protocol, stream), func(t *testing.T) {
				var messages []map[string]any
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/algo/api/v2/model/list" {
						fmt.Fprint(w, `{"code":200,"body":[{"key":"model","enable":true}]}`)
						return
					}
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					decoded, err := cosy.Decode(string(raw))
					if err != nil {
						t.Error(err)
					}
					var body struct {
						Messages []map[string]any `json:"messages"`
					}
					if err := json.Unmarshal(decoded, &body); err != nil {
						t.Error(err)
					}
					messages = body.Messages
					returned := false
					for _, message := range messages {
						if message["role"] == "tool" {
							returned = true
						}
					}
					if returned {
						fmt.Fprint(w, chunk(`{"role":"assistant","content":"completed"}`, "stop")+usageTail)
						return
					}
					fmt.Fprint(w, chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"shell","arguments":"{}"}},{"index":1,"id":"call_b","type":"function","function":{"name":"read","arguments":"{}"}}]}`, "tool_calls")+usageTail)
				}))
				defer backend.Close()
				base, _ := url.Parse(backend.URL)
				manager, err := providers.Open("", &http.Client{Transport: providerTransport{base: base}})
				if err != nil {
					t.Fatal(err)
				}
				_, err = manager.Import(context.Background(), providers.AccountInput{Target: "qoder:global", Credential: providers.Credential{UID: "fixture", AccessToken: "fixture", UserType: "teams", ExpiresAt: time.Now().Add(time.Hour).Unix()}})
				if err != nil {
					t.Fatal(err)
				}
				h, _, _ := keyRig(t, "sk-admin")
				h.cfg.Providers = manager
				path, first, follow := "", "", ""
				switch protocol {
				case "openai":
					path = "/v1/chat/completions"
					first = `{"model":"qoder:global:model","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"shell","parameters":{"type":"object"}}},{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`
					follow = `{"model":"qoder:global:model","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_a","type":"function","function":{"name":"shell","arguments":"{}"}},{"id":"call_b","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_a","content":"shell-result"},{"role":"tool","tool_call_id":"call_b","content":"read-result"}]}`
				case "claude":
					path = "/v1/messages"
					first = `{"model":"qoder:global:model","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"shell","input_schema":{"type":"object"}},{"name":"read","input_schema":{"type":"object"}}]}`
					follow = `{"model":"qoder:global:model","max_tokens":100,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"call_a","name":"shell","input":{}},{"type":"tool_use","id":"call_b","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a","content":[{"type":"text","text":"shell-result"}]},{"type":"tool_result","tool_use_id":"call_b","content":[{"type":"text","text":"read-result"}]}]}]}`
				case "codex":
					path = "/v1/responses"
					first = `{"model":"qoder:global:model","input":[{"role":"user","content":"hi"}],"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}},{"type":"function","name":"read","parameters":{"type":"object"}}]}`
					follow = `{"model":"qoder:global:model","input":[{"role":"user","content":"hi"},{"type":"function_call","call_id":"call_a","name":"shell","arguments":"{}"},{"type":"function_call","call_id":"call_b","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call_a","output":"shell-result"},{"type":"function_call_output","call_id":"call_b","output":"read-result"}]}`
				}
				if stream {
					first = strings.TrimSuffix(first, "}") + `,"stream":true}`
					follow = strings.TrimSuffix(follow, "}") + `,"stream":true}`
				}
				response := post(h, path, first, "Authorization", "Bearer sk-admin")
				if response.Code != 200 || !strings.Contains(response.Body.String(), "call_a") || !strings.Contains(response.Body.String(), "call_b") {
					t.Fatalf("并行工具丢失: %d %s", response.Code, response.Body)
				}
				response = post(h, path, follow, "Authorization", "Bearer sk-admin")
				if response.Code != 200 || !strings.Contains(response.Body.String(), "completed") {
					t.Fatalf("工具回传失败: %d %s", response.Code, response.Body)
				}
				results := map[string]string{}
				for _, message := range messages {
					if message["role"] == "tool" {
						id, _ := message["tool_call_id"].(string)
						content, _ := message["content"].(string)
						results[id] = content
					}
				}
				if results["call_a"] != "shell-result" || results["call_b"] != "read-result" {
					t.Fatalf("工具 ID 或结果未保留: %v", results)
				}
			})
		}
	}
}

func TestProviderRejectsUnsupportedSourceContent(t *testing.T) {
	manager, err := providers.Open("", nil)
	if err != nil {
		t.Fatal(err)
	}
	h, keys, wbCalls := keyRig(t, "sk-admin")
	h.cfg.Providers = manager
	for _, target := range []string{"traework:cn", "traecode:cn", "qoder:cn", "qoder:global"} {
		key := createKey(t, keys, apikeys.Input{Name: target, Enabled: true, AllowedTargets: []string{target}, DefaultTarget: target})
		for _, tc := range []struct{ name, path, body string }{
			{"聊天图片", "/v1/chat/completions", `{"messages":[{"role":"user","content":[{"type":"text","text":"说明"},{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]}`},
			{"Messages图片", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"text","text":"说明"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}`},
			{"Messages文档", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"text","text":"说明"},{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"AA=="}}]}]}`},
			{"Messages工具图片", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":[{"type":"text","text":"结果"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}]}`},
			{"Messages工具文档", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"AA=="}}]}]}]}`},
			{"Responses图片", "/v1/responses", `{"input":[{"role":"user","content":[{"type":"input_text","text":"说明"},{"type":"input_image","image_url":"https://example.invalid/image.png"}]}]}`},
			{"Responses文件", "/v1/responses", `{"input":[{"role":"user","content":[{"type":"input_text","text":"说明"},{"type":"input_file","file_id":"file-fixture"}]}]}`},
			{"Responses工具图片", "/v1/responses", `{"input":[{"type":"function_call_output","call_id":"call","output":[{"type":"input_image","image_url":"https://example.invalid/image.png"}]}]}`},
		} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%t", target, tc.name, stream), func(t *testing.T) {
					body := fmt.Sprintf(`{"model":"model","stream":%t,"max_tokens":100,%s`, stream, strings.TrimPrefix(tc.body, "{"))
					response := post(h, tc.path, body, "Authorization", "Bearer "+key.Secret)
					var failure struct {
						Error struct {
							Message string `json:"message"`
						} `json:"error"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
						t.Fatal(err)
					}
					if response.Code != 400 || failure.Error.Message == "" || *wbCalls != 0 {
						t.Fatalf("不支持的内容被丢弃或误转发：status=%d wb=%d body=%s", response.Code, *wbCalls, response.Body)
					}
				})
			}
		}
	}
}

func TestProviderStreamCompletionAndFailureAccounting(t *testing.T) {
	content := chunk(`{"role":"assistant","content":"partial"}`, "")
	finish := chunk(`{}`, "stop")
	usage := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\n"
	for _, tc := range []struct {
		name, tail   string
		ok, saveFail bool
	}{
		{"完整结束", finish + usage + "data: [DONE]\n\n", true, false},
		{"Qoder 包络结束", finish + usage + "event: finish\ndata: {\"statusCodeValue\":200,\"body\":\"[DONE]\"}\n\n", true, false},
		{"Qoder 包络无末尾空行", finish + usage + "data: {\"code\":200,\"body\":\"  [DONE] \\r\\n\"}", true, false},
		{"Qoder 包络状态失败", finish + usage + "data: {\"statusCodeValue\":500,\"body\":\"[DONE]\"}\n\n", false, false},
		{"Qoder 包络业务失败", finish + usage + "data: {\"code\":10605,\"body\":\"[DONE]\"}\n\n", false, false},
		{"Qoder 包络显式错误", finish + usage + "event: error\ndata: {\"statusCodeValue\":200,\"body\":\"[DONE]\"}\n\n", false, false},
		{"Qoder 包络根错误", finish + usage + "data: {\"statusCodeValue\":200,\"error\":{\"message\":\"broken\"},\"body\":\"[DONE]\"}\n\n", false, false},
		{"Qoder 包络坏正文", finish + usage + "data: {\"statusCodeValue\":200,\"body\":\"broken\"}\n\n", false, false},
		{"缺少完成标记", finish + usage, false, false},
		{"用量后错误", finish + usage + "data: {\"error\":{\"message\":\"broken\"}}\n\n", false, false},
		{"显式错误事件", finish + usage + "event: error\ndata: {\"message\":\"broken\"}\n\ndata: [DONE]\n\n", false, false},
		{"用量后坏帧", finish + usage + "data: broken\n\n", false, false},
		{"缺少结束原因", usage + "data: [DONE]\n\n", false, false},
		{"Qoder 包络缺少结束原因", usage + "data: {\"statusCodeValue\":200,\"body\":\"[DONE]\"}\n\n", false, false},
		{"空结束原因", "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"\"}]}\n\n" + usage + "data: [DONE]\n\n", false, false},
		{"结束原因类型错误", usage + "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":true}]}\n\ndata: [DONE]\n\n", false, false},
		{"用量保存失败", finish + usage + "data: [DONE]\n\n", false, true},
	} {
		for _, stream := range []bool{false, true} {
			for _, protocol := range []struct {
				path, done string
			}{
				{"/v1/chat/completions", "data: [DONE]"},
				{"/v1/messages", "event: message_stop"},
				{"/v1/responses", "event: response.completed"},
			} {
				t.Run(fmt.Sprintf("%s/%t%s", tc.name, stream, protocol.path), func(t *testing.T) {
					var chats atomic.Int32
					backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/algo/api/v2/model/list" {
							fmt.Fprint(w, `{"code":200,"body":[{"key":"model","enable":true}]}`)
							return
						}
						chats.Add(1)
						fmt.Fprint(w, content+tc.tail)
					}))
					defer backend.Close()
					base, _ := url.Parse(backend.URL)
					statePath := ""
					if tc.saveFail {
						statePath = filepath.Join(t.TempDir(), "providers.json")
					}
					manager, err := providers.Open(statePath, &http.Client{Transport: providerTransport{base: base}})
					if err != nil {
						t.Fatal(err)
					}
					for _, uid := range []string{"first", "second"} {
						if _, err := manager.Import(context.Background(), providers.AccountInput{Target: "qoder:global", Credential: providers.Credential{UID: uid, AccessToken: "fixture", UserType: "teams"}}); err != nil {
							t.Fatal(err)
						}
					}
					if tc.saveFail {
						if err := os.Remove(statePath + ".attempts.json"); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(statePath+".attempts.json", 0700); err != nil {
							t.Fatal(err)
						}
					}
					h, keys, wbCalls := keyRig(t, "sk-admin")
					h.cfg.Providers = manager
					logs := reqlog.New(reqlog.Config{})
					defer logs.Close()
					h.cfg.RequestLog = logs
					key := createKey(t, keys, apikeys.Input{Name: "计量", Enabled: true, AllowedTargets: []string{"qoder:global"}})
					body := fmt.Sprintf(`{"model":"qoder:global:model","stream":%t,"max_tokens":100,"messages":[{"role":"user","content":"hi"}],"input":"hi"}`, stream)
					response := post(h, protocol.path, body, "Authorization", "Bearer "+key.Secret)
					wantStatus := 502
					if tc.ok || stream {
						wantStatus = 200
					}
					if response.Code != wantStatus || chats.Load() != 1 || *wbCalls != 0 {
						t.Fatalf("失败状态或重试错误: status=%d chats=%d wb=%d body=%s", response.Code, chats.Load(), *wbCalls, response.Body)
					}
					if stream && (strings.Contains(response.Body.String(), protocol.done) != tc.ok || !strings.Contains(response.Body.String(), "partial")) {
						t.Fatalf("中途失败被正常收尾，或正文丢失: %s", response.Body)
					}
					if stream && !tc.ok && strings.Contains(response.Body.String(), `"finish_reason":"stop"`) {
						t.Fatalf("失败请求提前发出结束原因: %s", response.Body)
					}
					wantRequests, wantFailed := int64(0), int64(1)
					if tc.ok {
						wantRequests, wantFailed = 1, 0
					}
					if tc.saveFail {
						wantFailed = 0 // 上游已成功，客户端因落盘失败收到错误。
					}
					stored, _ := keys.Get(key.ID, time.Now())
					if stored.Used.Requests != wantRequests || stored.Used.Tokens != 6 {
						t.Fatalf("密钥失败退款或已知 token 丢失: %+v", stored.Used)
					}
					ledger := manager.Usage()
					if len(ledger) != 1 || ledger[0].Requests != 1 || ledger[0].Failed != wantFailed || ledger[0].TokensKnown != 1 || ledger[0].PromptTokens != 4 || ledger[0].CompletionTokens != 2 {
						t.Fatalf("平台失败尝试或 token 计量错误: %+v", ledger)
					}
					event := logs.Snapshot().Recent[0]
					if tc.saveFail && event.Outcome != reqlog.OutcomeLedgerPending {
						t.Fatalf("上游完成但账本失败应单独记录：%+v", event)
					}
					if event.OK != tc.ok || !event.TokensKnown || event.TotalTokens != 6 {
						t.Fatalf("请求记录状态或用量错误: %+v", event)
					}
				})
			}
		}
	}
}

func TestRequestLogPlatformAccountID(t *testing.T) {
	for _, target := range []string{"", "workbuddy:cn", "workbuddy:global", "qoder:cn", "traecode:cn"} {
		uid, wantID := "workbuddy-fixture-user", ""
		if target == "qoder:cn" || target == "traecode:cn" {
			uid, wantID = "pa_fixture-account", "pa_fixture-account"
		}
		trace := requestTrace{start: time.Now(), stat: &chatStat{target: target, uid: uid}}
		event := trace.event(200, nil)
		if event.AccountID != wantID {
			t.Fatalf("target=%q account_id=%q，应为 %q", target, event.AccountID, wantID)
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if wantID == "" && strings.Contains(string(raw), uid) {
			t.Fatalf("WorkBuddy 完整 UID 写入请求记录: %s", raw)
		}
	}
}

func TestProviderKeyWorkBuddyBareModelListing(t *testing.T) {
	for _, tc := range []struct {
		name       string
		input      apikeys.Input
		hidden     []string
		wantIDs    []string
		wantRealms []string
		wantHost   string
	}{
		{"旧密钥", apikeys.Input{Models: []string{"*"}}, nil, []string{"cn:m-both", "cn:m-cn", "global:m-both", "global:m-global", "m-both", "m-cn", "m-global"}, []string{"cn", "global"}, "cn.fake"},
		{"仅国内", apikeys.Input{AllowedTargets: []string{"workbuddy:cn"}}, nil, []string{"cn:m-both", "cn:m-cn", "m-both", "m-cn"}, []string{"cn"}, "cn.fake"},
		{"仅国际", apikeys.Input{AllowedTargets: []string{"workbuddy:global"}}, nil, []string{"global:m-both", "global:m-global", "m-both", "m-global"}, []string{"global"}, "global.fake"},
		{"默认国际", apikeys.Input{DefaultTarget: "workbuddy:global"}, nil, []string{"cn:m-both", "cn:m-cn", "global:m-both", "global:m-global", "m-both", "m-global"}, []string{"global"}, "global.fake"},
		{"默认新平台", apikeys.Input{AllowedTargets: []string{"workbuddy:cn", "workbuddy:global", "qoder:global"}, DefaultTarget: "qoder:global"}, nil, []string{"cn:m-both", "cn:m-cn", "global:m-both", "global:m-global"}, nil, ""},
		{"仅新平台", apikeys.Input{AllowedTargets: []string{"qoder:global"}, DefaultTarget: "qoder:global"}, nil, []string{}, nil, ""},
		{"裸名白名单", apikeys.Input{Models: []string{"m-cn"}}, nil, []string{"cn:m-cn", "m-cn"}, nil, ""},
		{"隐藏国内模型", apikeys.Input{}, []string{"cn:m-both"}, []string{"cn:m-cn", "global:m-both", "global:m-global", "m-both", "m-cn", "m-global"}, []string{"global"}, "global.fake"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetModelsCache(t)
			cn := &auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
			global := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
			fake := &crossRealmUpstream{}
			up := fake.client(t, []string{"m-both", "m-cn"}, []string{"m-both", "m-global"})
			transport := up.HTTP.Transport
			up.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(r)
				if err != nil || strings.HasSuffix(r.URL.Path, "/chat/completions") {
					return response, err
				}
				raw, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					return nil, err
				}
				vendor := "cn-vendor"
				if r.Header.Get("Authorization") == "Bearer at-global" {
					vendor = "global-vendor"
				}
				response.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(string(raw), `"id":`, `"vendor":"`+vendor+`","id":`)))
				return response, nil
			})
			keys, err := apikeys.Open("")
			if err != nil {
				t.Fatal(err)
			}
			tc.input.Name, tc.input.Enabled = tc.name, true
			key := createKey(t, keys, tc.input)
			h := NewHandler(Config{Pool: testPoolWith(cn, global), Upstream: up, Keys: keys, GlobalEnabled: true,
				Live: livecfg.New(livecfg.Snapshot{APIKey: "sk-admin", CrossRealm: livecfg.CrossRealmMerge, HiddenModels: livecfg.NormalizeHiddenModels(tc.hidden)})})
			req := httptest.NewRequest("GET", "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer "+key.Secret)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			var listing struct {
				Data []struct {
					ID     string   `json:"id"`
					Realms []string `json:"realms"`
					Vendor string   `json:"vendor"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || response.Code != 200 {
				t.Fatal("读取密钥模型目录失败", response.Code, response.Body, err)
			}
			ids := make([]string, 0, len(listing.Data))
			for _, model := range listing.Data {
				ids = append(ids, model.ID)
				if len(model.Realms) > 0 && model.Vendor != model.Realms[0]+"-vendor" {
					t.Fatalf("裸名元数据来自不可选的地区: id=%s realms=%v vendor=%s", model.ID, model.Realms, model.Vendor)
				}
				if model.ID == "m-both" && !reflect.DeepEqual(model.Realms, tc.wantRealms) {
					t.Fatalf("裸名暴露了不可访问的地区: %v，应为 %v", model.Realms, tc.wantRealms)
				}
			}
			slices.Sort(ids)
			if !reflect.DeepEqual(ids, tc.wantIDs) {
				t.Fatalf("密钥目录不符: %v，应为 %v", ids, tc.wantIDs)
			}
			if tc.wantHost != "" {
				response := post(h, "/v1/chat/completions", `{"model":"m-both","stream":true,"messages":[{"role":"user","content":"hi"}]}`, "Authorization", "Bearer "+key.Secret)
				if response.Code != 200 || fake.lastChat() != tc.wantHost {
					t.Fatal("目录裸名与实际选路不一致", response.Code, fake.lastChat(), response.Body)
				}
			}
		})
	}
}

func TestProviderCatalogRechecksKeyChanges(t *testing.T) {
	for _, change := range []string{"保持权限", "收回平台", "收回分组", "收窄白名单", "隐藏模型", "停用", "到期", "删除"} {
		t.Run(change, func(t *testing.T) {
			h, keys, _ := keyRig(t, "sk-admin")
			input := apikeys.Input{Name: "目录权限", Enabled: true, AllowedTargets: []string{"qoder:global"}, Groups: []string{"allowed"}}
			key := createKey(t, keys, input)
			var catalogs atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/algo/api/v2/model/list" {
					t.Error("未知请求", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				catalogs.Add(1)
				// 模拟上游目录仍在获取时，管理员修改本次请求使用的密钥。
				switch change {
				case "收回平台":
					input.AllowedTargets = []string{"workbuddy:cn"}
				case "收回分组":
					input.Groups = []string{"outside"}
				case "收窄白名单":
					input.Models = []string{"qoder:global:remaining-model"}
				case "隐藏模型":
					live := h.loadLive()
					live.HiddenModels = livecfg.NormalizeHiddenModels([]string{"qoder:global:scoped-model"})
					h.cfg.Live.Store(live)
				case "停用":
					input.Enabled = false
				case "到期":
					input.ExpiresAt = time.Now().Add(-time.Hour)
				}
				var err error
				if change == "删除" {
					err = keys.Delete(key.ID)
				} else {
					_, err = keys.Update(key.ID, input)
				}
				if err != nil {
					t.Error("修改目录权限失败", err)
					w.WriteHeader(500)
					return
				}
				fmt.Fprint(w, `{"code":200,"body":[{"key":"scoped-model","enable":true},{"key":"remaining-model","enable":true}]}`)
			}))
			defer backend.Close()
			base, _ := url.Parse(backend.URL)
			manager, err := providers.Open("", &http.Client{Transport: providerTransport{base: base}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Import(context.Background(), providers.AccountInput{Target: "qoder:global", Groups: []string{"allowed"}, Credential: providers.Credential{UID: "fixture", AccessToken: "fixture", UserType: "teams"}}); err != nil {
				t.Fatal(err)
			}
			h.cfg.Providers = manager
			req := httptest.NewRequest("GET", "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer "+key.Secret)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			wantStatus, wantScoped, wantRemaining := 200, false, false
			switch change {
			case "保持权限":
				wantScoped, wantRemaining = true, true
			case "收窄白名单", "隐藏模型":
				wantRemaining = true
			case "停用", "到期", "删除":
				wantStatus = 401
			}
			if catalogs.Load() != 1 || response.Code != wantStatus || strings.Contains(response.Body.String(), "scoped-model") != wantScoped || strings.Contains(response.Body.String(), "remaining-model") != wantRemaining {
				t.Fatalf("目录仍按旧权限返回: catalogs=%d status=%d body=%s", catalogs.Load(), response.Code, response.Body)
			}
			if wantStatus == 401 {
				wantCode := map[string]string{"停用": "key_disabled", "到期": "key_expired", "删除": "key_deleted"}[change]
				if code, _ := openAIErr(t, response); code != wantCode {
					t.Fatalf("密钥失效原因不符: %s，应为 %s", code, wantCode)
				}
			}
		})
	}
}

func TestProviderModelMissDoesNotCoolAccount(t *testing.T) {
	hold, started := make(chan struct{}), make(chan struct{})
	var busy atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/algo/api/v2/model/list" {
			fmt.Fprint(w, `{"code":200,"body":[{"key":"model","enable":true}]}`)
			return
		}
		if busy.CompareAndSwap(true, false) {
			close(started)
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, chunk(`{"role":"assistant","content":"ok"}`, "")+chunk(`{}`, "stop")+"data: [DONE]\n\n")
	}))
	defer backend.Close()
	finish := sync.OnceFunc(func() { close(hold) })
	defer finish()
	base, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := providers.Open("", &http.Client{Transport: providerTransport{base: base}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Import(context.Background(), providers.AccountInput{Target: "qoder:global", Credential: providers.Credential{UID: "fixture", AccessToken: "fixture", UserType: "teams"}}); err != nil {
		t.Fatal(err)
	}
	h, _, _ := keyRig(t, "sk-admin")
	h.cfg.Providers = manager
	for _, tc := range []struct {
		model  string
		status int
		code   string
	}{{"model", 200, "ok"}, {"other-model", 404, "model_not_available_for_account"}, {"model", 200, "ok"}} {
		response := post(h, "/v1/chat/completions", fmt.Sprintf(`{"model":"qoder:global:%s","messages":[{"role":"user","content":"hi"}]}`, tc.model), "Authorization", "Bearer sk-admin")
		if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.code) {
			t.Fatalf("模型 %s 状态 %d 内容 %s", tc.model, response.Code, response.Body)
		}
	}
	// 在同一账号运行正常请求时热调排队期限，通过实际入口检查 503 及上游次数。
	busy.Store(true)
	const body = `{"model":"qoder:global:model","messages":[{"role":"user","content":"hi"}]}`
	complete := make(chan int, 1)
	go func() { complete <- post(h, "/v1/chat/completions", body, "Authorization", "Bearer sk-admin").Code }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("首个请求未到达上游")
	}
	for _, limit := range []time.Duration{0, 10 * time.Millisecond} {
		h.cfg.Pool.SetInFlightWait(limit)
		startedWaiting := time.Now()
		resp := post(h, "/v1/chat/completions", body, "Authorization", "Bearer sk-admin")
		if time.Since(startedWaiting) > 2*time.Second || resp.Code != 503 || !strings.Contains(resp.Body.String(), "account_queue_timeout") {
			t.Fatal("等待期限未生效", resp.Code, resp.Body)
		}
	}
	finish()
	if status := <-complete; status != 200 {
		t.Fatal("队满影响已在途的请求", status)
	}
	if u := manager.Usage(); len(u) != 1 || u[0].Requests != 3 {
		t.Fatal("目录不匹配或等位超时被计入尝试", u)
	}
}

func TestProviderCancelledDuringFinalBilling(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/remote/v1/models":
					fmt.Fprint(w, `{"code":0,"data":{"list":[{"function":"solo_work_remote","models":[{"name":"model","config_source":1,"is_preset":true}]}]}}`)
				case "/api/remote/v1/chat_sessions":
					fmt.Fprint(w, `{"code":0,"data":{"chat_session_id":"s","message_id":"m"}}`)
				case "/api/remote/v1/chat_sessions/s/events":
					fmt.Fprint(w, "event: output\ndata: {\"response\":\"hello\"}\n\nevent: done\ndata: {}\n\n")
				case "/api/v1/commercial/get_session_usage":
					cancel()
					fmt.Fprint(w, `{"user_usage_group_by_session":{"session_id":"m","credits_float":0.125}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer backend.Close()
			base, _ := url.Parse(backend.URL)
			manager, err := providers.Open("", &http.Client{Transport: providerTransport{base: base}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = manager.Import(t.Context(), providers.AccountInput{Target: "traework:cn", Credential: providers.Credential{UID: "fixture", AccessToken: "fixture"}})
			if err != nil {
				t.Fatal(err)
			}
			h, _, _ := keyRig(t, "sk-admin")
			h.cfg.Providers = manager
			logs := reqlog.New(reqlog.Config{})
			defer logs.Close()
			h.cfg.RequestLog = logs
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"traework:cn:model","stream":%t,"messages":[{"role":"user","content":"hi"}]}`, stream))).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer sk-admin")
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			events := logs.Snapshot().Recent
			if len(events) != 1 || events[0].Outcome != reqlog.OutcomeInterrupted || !events[0].HasCredit || events[0].Credit != 0.125 || strings.Contains(resp.Body.String(), "[DONE]") || !stream && resp.Body.Len() != 0 {
				t.Fatalf("取消后仍交付成功或丢失用量：%+v body=%s", events, resp.Body.String())
			}
		})
	}
}

func TestQoderQueuedDispatchUsesCurrentKey(t *testing.T) {
	const target = "qoder:cn"
	for _, change := range []string{"停用", "分组", "上游", "模型"} {
		t.Run(change, func(t *testing.T) {
			h, keys, _ := keyRig(t, "")
			in := apikeys.Input{Name: "排队", Enabled: true, AllowedTargets: []string{target}, Groups: []string{"allowed"}}
			key := createKey(t, keys, in)
			var chats, finishes atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/algo/api/v2/model/list":
					fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
				case "/algo/api/v2/service/pro/sse/agent_chat_generation":
					chats.Add(1)
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `{"code":10605}`)
				case "/algo/api/v2/service/ask/queue/status":
					switch change {
					case "停用":
						in.Enabled = false
					case "分组":
						in.Groups = []string{"other"}
					case "上游":
						in.AllowedTargets = []string{"qoder:global"}
					case "模型":
						in.Models = []string{"qoder:cn:other"}
					}
					if _, err := keys.Update(key.ID, in); err != nil {
						t.Error(err)
					}
					fmt.Fprint(w, `{"isQueued":false,"retryAfterSeconds":0}`)
				case "/algo/api/v2/service/ask/finish":
					finishes.Add(1)
				default:
					t.Errorf("意外请求 %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer backend.Close()
			base, _ := url.Parse(backend.URL)
			manager, err := providers.Open("", &http.Client{Transport: providerTransport{base: base}})
			if err != nil {
				t.Fatal(err)
			}
			h.cfg.Providers = manager
			_, err = manager.Import(t.Context(), providers.AccountInput{Target: target, Groups: []string{"allowed"}, Credential: providers.Credential{UID: "fixture", UserType: "teams", AccessToken: "fixture", ExpiresAt: time.Now().Add(time.Hour).Unix()}})
			if err != nil {
				t.Fatal(err)
			}
			body := strings.Replace(chatBody, "glm-5.2", "qoder:cn:model", 1)
			rec := post(h, "/v1/chat/completions", body, "Authorization", "Bearer "+key.Secret)
			code, _ := openAIErr(t, rec)
			after, _ := keys.Get(key.ID, time.Now())
			if rec.Code != 403 || code != "target_not_allowed" || chats.Load() != 1 || finishes.Load() != 1 || after.Used.Requests != 0 {
				t.Fatalf("排队撤权未生效: 响应=%d %s 生成=%d 释放=%d 次数=%d", rec.Code, code, chats.Load(), finishes.Load(), after.Used.Requests)
			}
		})
	}
}
