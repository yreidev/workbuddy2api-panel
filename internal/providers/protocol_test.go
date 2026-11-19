// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/providers/cosy"
)

func TestTraeCodeToolRoundTrip(t *testing.T) {
	body := `{"model":"model","tool_choice":{"type":"function","function":{"name":"shell"}},"messages":[{"role":"system","content":"system"},{"role":"assistant","content":null,"tool_calls":[{"id":"a","type":"function","function":{"name":"shell","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"result"},{"role":"user","content":"continue"}],"tools":[{"type":"function","function":{"name":"shell","parameters":{"type":"object"}}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v3/llm_utils_chat" {
			t.Error("错误产品通道", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Cloud-IDE-JWT fixture" || r.Header.Get("X-Machine-Id") != "machine" {
			t.Error("请求头缺失")
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if text(input["function"]) != "chat_v3" || text(input["config_name"]) != "model" {
			t.Error("目录模型未用于请求")
		}
		if input["tool_choice"] != "shell" {
			t.Error("指定工具必须转换为 Trae 的工具名字符串")
		}
		messages := input["messages"].([]any)
		if text(object(messages[2])["tool_call_id"]) != "a" || text(object(messages[2])["content"].([]any)[0].(map[string]any)["text"]) != "result" {
			t.Error("工具结果丢失")
		}
		call := object(object(messages[1])["tool_calls"].([]any)[0])
		if call["function_call"] == nil || call["function"] != nil {
			t.Error("工具历史形状错误")
		}
		tool := object(object(input["tools"].([]any)[0])["function"])
		if _, ok := tool["parameters"].(string); !ok {
			t.Error("Trae 参数 schema 必须是 JSON 字符串")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: progress_notice\ndata: \"正在处理请求\"\n\n")
		fmt.Fprint(w, "event: output\ndata: {\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"type\":\"function\",\"function_call\":{\"name\":\"shell\",\"arguments\":\"{\"}},{\"index\":1,\"id\":\"b\",\"type\":\"function\",\"function_call\":{\"name\":\"read\",\"arguments\":\"{}\"}}]}\n\nevent: output\ndata: {\"tool_calls\":[{\"index\":0,\"function_call\":{\"arguments\":\"}\"}}]}\n\nevent: token_usage\ndata: {\"prompt_tokens\":2,\"completion_tokens\":3}\n\nevent: done\ndata: {}\n\n")
	}))
	defer server.Close()
	adapter := &Trae{HTTP: server.Client(), CodeBase: server.URL}
	var frames []map[string]any
	err := adapter.Chat(context.Background(), "traecode:cn", Credential{AccessToken: "fixture", MachineID: "machine"}, Model{ID: "model"}, []byte(body), "session", func(frame map[string]any) error { frames = append(frames, frame); return nil })
	if err != nil || len(frames) != 4 {
		t.Fatalf("工具流: %v frames=%v", err, frames)
	}
	choices := frames[0]["choices"].([]any)
	calls := object(choices[0])["delta"].(map[string]any)["tool_calls"].([]any)
	if len(calls) != 2 || object(calls[0])["function"] == nil || object(calls[0])["function_call"] != nil {
		t.Fatal("并行工具调用形状错误")
	}
}

func TestTraeCodeToolChoiceNone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if input["tools"] != nil || input["functions"] != nil || input["tool_choice"] != nil {
			t.Error("禁止工具时不应向 Trae 发送工具定义或选择字段")
		}
		fmt.Fprint(w, "event: done\ndata: {}\n\n")
	}))
	defer server.Close()
	adapter := &Trae{HTTP: server.Client(), CodeBase: server.URL}
	body := `{"messages":[{"role":"user","content":"只回复文本"}],"tools":[{"type":"function","function":{"name":"shell","parameters":{"type":"object"}}}],"functions":[{"name":"legacy"}],"tool_choice":"none"}`
	if err := adapter.Chat(context.Background(), "traecode:cn", Credential{AccessToken: "fixture"}, Model{ID: "model"}, []byte(body), "session", discardFrame); err != nil {
		t.Fatal(err)
	}
}

func TestTraeWorkSnapshotsAndProductIsolation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/remote/v1/chat_sessions":
			var in map[string]any
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if text(in["mode"]) != "work" || text(object(in["initial_message"])["agent_type"]) != "solo_work_remote" {
				t.Error("Work 请求误入其他产品")
			}
			initial := object(in["initial_message"])
			var query []struct {
				Type string `json:"type"`
				Data struct {
					Content string `json:"content"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(text(initial["query"])), &query); err != nil || len(query) != 1 || query[0].Type != "text" || query[0].Data.Content != "user: hello\n" {
				t.Error("Work query 未按官方文本块数组编码", err, query)
			}
			custom := object(initial["custom_model"])
			if text(custom["config_name"]) != "model" || text(custom["model_name"]) != "model" || text(custom["display_model_name"]) != "显示名称" || number(custom["config_source"]) != 1 || custom["is_preset"] != true || custom["use_remote_service"] != true || custom["multimodal"] != true {
				t.Error("Work custom_model 缺少官方模型标识", custom)
			}
			if _, hasPricing := custom["features"]; hasPricing {
				t.Error("目录展示字段不应原样作为请求模型描述")
			}
			if in["__hit_shared_artifact_and_workroom"] != true || in["__hit_my_space"] != true || initial["__hit_my_space"] != nil {
				t.Error("Work 能力标记没有放在官方请求位置")
			}
			var common map[string]any
			if err := json.Unmarshal([]byte(text(initial["common_params"])), &common); err != nil || text(common["biz_session_id"]) != "session" || text(common["solo_chat_mode"]) != "work" {
				t.Error("Work 会话公共参数错误", err, common)
			}
			fmt.Fprint(w, `{"code":0,"data":{"chat_session_id":"s","message_id":"m"}}`)
		case "/api/v1/commercial/get_session_usage":
			fmt.Fprint(w, `{}`)
		case "/api/remote/v1/chat_sessions/s/events":
			fmt.Fprint(w, "data: {\"event\":\"plan_item\",\"id\":\"p\",\"content\":\"he\"}\n\ndata: {\"event_type\":\"plan_item\",\"id\":\"p\",\"content\":\"hello\"}\n\ndata: {\"event\":\"assistant_message\",\"content\":\"hello\"}\n\ndata: {\"event_type\":\"done\"}")
		default:
			t.Error("错误产品路径", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter := &Trae{HTTP: server.Client(), WorkBase: server.URL}
	output := ""
	model := Model{ID: "model", Name: "显示名称", Native: map[string]any{"name": "model", "display_name": "显示名称", "config_source": float64(1), "is_preset": true, "multimodal": true, "features": "目录展示数据"}}
	err := adapter.Chat(context.Background(), "traework:cn", Credential{AccessToken: "fixture"}, model, []byte(plainChat), "session", func(frame map[string]any) error {
		choice := object(frame["choices"].([]any)[0])
		output += text(object(choice["delta"])["content"])
		return nil
	})
	if err != nil || output != "hello" {
		t.Fatalf("Work 快照合并: %q %v", output, err)
	}
}

func TestTraeWorkSessionErrorsKeepStageAndStatus(t *testing.T) {
	for _, stage := range []string{"创建会话", "订阅回复事件"} {
		t.Run(stage, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path == "/api/remote/v1/chat_sessions" && stage == "订阅回复事件" {
					fmt.Fprint(w, `{"code":0,"data":{"chat_session_id":"s","message_id":"m"}}`)
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"code":995000,"message":"fixture upstream error"}`)
			}))
			defer server.Close()
			adapter := &Trae{HTTP: server.Client(), WorkBase: server.URL}
			model := Model{ID: "model", Native: map[string]any{"config_source": float64(1)}}
			err := adapter.Chat(context.Background(), "traework:cn", Credential{AccessToken: "fixture"}, model, []byte(plainChat), "session", discardFrame)
			var upstream *Error
			if !errors.As(err, &upstream) || upstream.Status != 500 || upstream.Code != "995000" || !strings.Contains(err.Error(), stage) {
				t.Fatalf("Work 错误未保留操作阶段与原始状态: %v", err)
			}
			wantCalls := 1
			if stage == "订阅回复事件" {
				wantCalls = 3
			}
			if calls.Load() != int32(wantCalls) {
				t.Fatal("Work 失败后重复创建会话", calls.Load())
			}
		})
	}
}

func TestTraeWorkRejectsMissingModelDescriptorBeforeRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("模型目录不完整时不应创建 Work 会话")
	}))
	defer server.Close()
	adapter := &Trae{HTTP: server.Client(), WorkBase: server.URL}
	if err := adapter.Chat(context.Background(), "traework:cn", Credential{AccessToken: "fixture"}, Model{ID: "model"}, []byte(plainChat), "session", discardFrame); err == nil || !strings.Contains(err.Error(), "config_source") {
		t.Fatalf("不完整目录没有明确报错: %v", err)
	}
}

func TestTraeDeviceRefreshProof(t *testing.T) {
	key, err := newTraeKey()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cloudide/api/v3/trae/GetUserInfo" {
			if r.Header.Get("X-Cloudide-Token") != "new" {
				t.Error("身份查询未使用新令牌")
			}
			fmt.Fprint(w, `{"Result":{"UserID":"confirmed"}}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/trae/api/v3/oauth/ExchangeToken" {
			t.Error("设备刷新使用了旧接口")
		}
		device := object(body["DeviceInfo"])
		if device["DeviceID"] != "d" || device["MachineID"] != "m" || device["ClientVersion"] != "3.4.20" || device["OSInfo"] != "linux" || device["OSVersion"] != "Linux" {
			t.Error("官方 Linux 设备描述与持久化身份不一致")
		}
		if body["IDEVersion"] != "3.4.20" || !strings.Contains(r.UserAgent(), "Chrome/142.0.7444.235 Electron/39.2.7") {
			t.Error("授权运行时版本未对齐")
		}
		block, _ := pem.Decode([]byte(text(device["DevicePublicKey"])))
		if block == nil {
			t.Error("缺少设备公钥")
			http.Error(w, "bad", 400)
			return
		}
		public, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			t.Error(err)
			return
		}
		proof := object(body["DeviceProof"])
		payload := strings.Join([]string{"POST", r.URL.Path, "client", "refresh", fmt.Sprint(number(proof["Timestamp"])), text(proof["Nonce"])}, "\n")
		digest := sha256.Sum256([]byte(payload))
		signature, err := base64.StdEncoding.DecodeString(text(proof["Signature"]))
		if err != nil || !ecdsa.VerifyASN1(public.(*ecdsa.PublicKey), digest[:], signature) {
			t.Error("签名验证失败")
		}
		fmt.Fprintf(w, `{"Result":{"Token":"new","RefreshToken":"next","TokenExpireAt":%d}}`, time.Now().Add(time.Hour).Unix())
	}))
	defer server.Close()
	adapter := &Trae{HTTP: server.Client(), AuthBase: server.URL}
	c, err := adapter.Refresh(context.Background(), "traecode:cn", Credential{DeviceKey: key, ClientID: "client", RefreshToken: "refresh", MachineID: "m", DeviceID: "d"})
	if err != nil || c.RefreshToken != "next" || c.DeviceKey != key {
		t.Fatal("刷新丢失令牌或设备身份", err)
	}
}
func TestQoderEncodedCatalogAndTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/algo/api/v2/model/list":
			fmt.Fprint(w, `{"code":200,"body":"[{\"key\":\"model\",\"enable\":true,\"max_input_tokens\":1000},{\"key\":\"disabled\",\"enable\":false}]"}`)
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer COSY.") || r.Header.Get("Cosy-Key") == "" {
				t.Error("缺少 COSY 签名")
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			decoded, err := cosy.Decode(string(raw))
			if err != nil {
				t.Error(err)
			}
			var body map[string]any
			if err := json.Unmarshal(decoded, &body); err != nil {
				t.Error(err)
			}
			if text(object(body["model_config"])["key"]) != "model" || text(body["session_type"]) != "qodercli" || len(body["messages"].([]any)) != 3 {
				t.Error("Qoder 多轮形状错误")
			}
			messages := body["messages"].([]any)
			if text(object(messages[1])["role"]) != "tool" || text(object(messages[1])["tool_call_id"]) != "call" {
				t.Error("Qoder 工具回传丢失")
			}
			inner := `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`
			root, _ := json.Marshal(map[string]any{"code": 200, "body": inner})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", root)
		default:
			t.Error("未知 Qoder 路径", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	q := &Qoder{HTTP: server.Client(), Base: server.URL}
	credential := Credential{UID: "user", Name: "fixture", UserType: "teams", AccessToken: "token", MachineID: "machine"}
	models, err := q.Models(context.Background(), "qoder:global", credential)
	if err != nil || len(models) != 1 {
		t.Fatal("Qoder 目录数组解析错误", err, models)
	}
	err = q.Chat(context.Background(), "qoder:global", credential, models[0], []byte(`{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call","type":"function","function":{"name":"shell","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call","content":"result"},{"role":"user","content":"continue"}]}`), "session", discardFrame)
	if err != nil {
		t.Fatal(err)
	}
}

func TestQoderCatalogUsesAssistantScene(t *testing.T) {
	for _, tc := range []struct {
		name, response, wantModel, wantError string
	}{
		{"仅助理目录", `{"chat":[{"key":"chat-only","enable":true}],"assistant":[{"key":"disabled","enable":false},{"key":"assistant-model","display_name":"Assistant","enable":true}]}`, "assistant-model", ""},
		{"全部停用", `{"assistant":[{"key":"bool-disabled","enable":false},{"key":"number-disabled","enable":0},{"key":"string-disabled","enable":"0"}],"quest":[{"key":"quest-only","enable":true}]}`, "", "全部停用"},
		{"空助理目录", `{"assistant":[],"inline":[{"key":"inline-only","enable":true}]}`, "", "未返回模型"},
		{"缺失助理目录", `{"chat":[{"key":"chat-only","enable":true}]}`, "", "缺少 assistant"},
		{"目录类型错误", `{"assistant":{}}`, "", "必须为数组"},
		{"模型结构错误", `{"assistant":[{"name":"missing-key"}]}`, "", "缺少 key"},
		{"业务错误", `{"code":403,"body":{"assistant":[]}}`, "", "403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			q := &Qoder{HTTP: server.Client(), Base: server.URL}
			models, err := q.Models(t.Context(), "qoder:global", Credential{})
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || len(models) != 0 {
					t.Fatalf("目录=%v 错误=%v，预期 %q", models, err, tc.wantError)
				}
				return
			}
			if err != nil || len(models) != 1 || models[0].ID != tc.wantModel {
				t.Fatalf("目录=%v 错误=%v，预期 %q", models, err, tc.wantModel)
			}
		})
	}
}

func TestQoderCatalogRevocationClearsCachedModels(t *testing.T) {
	m, _ := testManager(t, "")
	id := importFake(t, m, "qoder:global", "fixture")
	var reply, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/algo/api/v2/model/list" {
			t.Errorf("已停用模型仍收到请求 %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		switch reply.Load() {
		case 0:
			fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			fmt.Fprint(w, `{"assistant":[{"key":"model","enable":false}]}`)
		}
	}))
	defer server.Close()
	m.adapters["qoder"] = &Qoder{HTTP: server.Client(), Base: server.URL}
	for _, stage := range []int32{0, 1, 2} {
		reply.Store(stage)
		_, err := m.Models(t.Context(), id, "qoder:global", true)
		if (err == nil) != (stage == 0) {
			t.Fatalf("阶段 %d：目录刷新错误=%v", stage, err)
		}
		wantCached := stage < 2
		if got := m.List()[0]; (len(got.Models) == 1) != wantCached {
			t.Fatalf("阶段 %d：账号视图目录=%v", stage, got.Models)
		}
	}
	if _, err := m.Execute(t.Context(), Request{Target: "qoder:global", Model: "model", Body: []byte(plainChat)}, discardFrame); err == nil {
		t.Fatal("已停用模型仍可路由")
	}
	reply.Store(0)
	if _, err := m.Models(t.Context(), id, "qoder:global", false); !errors.Is(err, errQoderCatalogUnavailable) || calls.Load() != 3 {
		t.Fatalf("空目录未复用: %v 调用 %d", err, calls.Load())
	}
	if got, err := m.Models(t.Context(), id, "qoder:global", true); err != nil || len(got) != 1 || calls.Load() != 4 {
		t.Fatalf("强制刷新未恢复目录: %v 调用 %d", err, calls.Load())
	}
	reply.Store(2)
	if _, err := m.Models(t.Context(), id, "qoder:global", true); !errors.Is(err, errQoderCatalogUnavailable) {
		t.Fatal(err)
	}
	cached := m.running[id].catalogs["qoder:global"]
	cached.fetchedAt = time.Now().Add(-31 * time.Minute)
	m.running[id].catalogs["qoder:global"] = cached
	reply.Store(0)
	if got, err := m.Models(t.Context(), id, "qoder:global", false); err != nil || len(got) != 1 || calls.Load() != 6 {
		t.Fatalf("空目录过期后未重新获取: %v 调用 %d", err, calls.Load())
	}

}

func TestConcurrentQoderCatalogRefreshKeepsNewerResult(t *testing.T) {
	const available = `{"assistant":[{"key":"model","enable":true}]}`
	const unavailable = `{"assistant":[{"key":"model","enable":false}]}`
	for _, tc := range []struct {
		name, older, newer string
		wantUnavailable    bool
	}{
		{name: "迟到的启用目录不能恢复停用模型", older: available, newer: unavailable, wantUnavailable: true},
		{name: "迟到的停用目录不能清除新模型", older: unavailable, newer: available},
		{name: "临时错误不替代成功目录", older: available},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstStarted := make(chan struct{})
			releaseFirst := make(chan struct{})
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					close(firstStarted)
					select {
					case <-releaseFirst:
						fmt.Fprint(w, tc.older)
					case <-r.Context().Done():
					}
					return
				}
				if tc.newer == "" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				fmt.Fprint(w, tc.newer)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			m, _ := testManager(t, "")
			const target = "qoder:global"
			id := importFake(t, m, target, "fixture")
			m.adapters["qoder"] = &Qoder{HTTP: server.Client(), Base: server.URL}
			type reply struct {
				models []Model
				err    error
			}
			first := make(chan reply, 1)
			go func() {
				models, err := m.Models(ctx, id, target, true)
				first <- reply{models, err}
			}()
			select {
			case <-firstStarted:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			_, newerErr := m.Models(ctx, id, target, true)
			close(releaseFirst)
			if (newerErr == nil) != (tc.newer == available) {
				t.Fatalf("新查询错误=%v", newerErr)
			}
			old := <-first
			view := m.List()[0]
			if tc.wantUnavailable {
				if !errors.Is(old.err, errQoderCatalogUnavailable) || len(old.models) != 0 || len(view.Models) != 0 || !strings.Contains(view.Error, "全部停用") {
					t.Fatalf("停用结果被覆盖：旧查询=%+v，账号=%+v", old, view)
				}
			} else if old.err != nil || len(old.models) != 1 || len(view.Models) != 1 || view.Error != "" {
				t.Fatalf("启用结果被覆盖：旧查询=%+v，账号=%+v", old, view)
			}
		})
	}
}

func TestQoderUnavailableCatalogRotatesWithinAllowedAccounts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		maxRotate  int
		restrict   bool
		probe      bool
		wantRotate bool
	}{
		{name: "可换到同地区可用账号", maxRotate: 3, wantRotate: true},
		{name: "尝试上限", maxRotate: 1},
		{name: "分组限制", maxRotate: 3, restrict: true},
		{name: "指定账号测活", maxRotate: 3, probe: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var firstCatalogs, nextCatalogs, chats atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				device := r.Header.Get("Cosy-MachineId")
				switch {
				case r.URL.Path == "/algo/api/v2/model/list" && device == "disabled-device":
					firstCatalogs.Add(1)
					fmt.Fprint(w, `{"assistant":[{"key":"model","enable":false}]}`)
				case r.URL.Path == "/algo/api/v2/model/list" && device == "eligible-device":
					nextCatalogs.Add(1)
					fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
				case r.URL.Path == "/algo/api/v2/service/pro/sse/agent_chat_generation" && device == "eligible-device":
					chats.Add(1)
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"credits\":0.125,\"billable\":true}}\n\ndata: [DONE]\n\n")
				default:
					t.Errorf("请求发往未授权账号或路径：%s %s", device, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			m, _ := testManager(t, "")
			add := func(target, device, group string, priority int) string {
				t.Helper()
				id, err := m.Import(t.Context(), AccountInput{Target: target, Groups: []string{group}, Priority: priority, Credential: Credential{
					UID: device, MachineID: device, UserType: "teams", AccessToken: "fixture-token", ExpiresAt: time.Now().Add(time.Hour).Unix(),
				}})
				if err != nil {
					t.Fatal(err)
				}
				return id
			}
			first := add("qoder:global", "disabled-device", "initial", 1)
			next := add("qoder:global", "eligible-device", "backup", 2)
			add("qoder:cn", "other-region-device", "initial", 0)
			m.adapters["qoder"] = &Qoder{HTTP: server.Client(), Base: server.URL}
			req := Request{Target: "qoder:global", Model: "model", Body: []byte(plainChat), MaxRotate: tc.maxRotate}
			if tc.restrict {
				req.Allow = func(_ string, groups []string) bool { return len(groups) == 1 && groups[0] == "initial" }
			}
			if tc.probe {
				req.TestAccountID = first
			}

			result, err := m.Execute(t.Context(), req, discardFrame)
			if tc.wantRotate {
				if err != nil || result.AccountID != next || result.Attempts != 2 || nextCatalogs.Load() != 1 || chats.Load() != 1 || !result.ChargeKnown || result.Charge != 0.125 {
					t.Fatalf("换号或费用归属错误：结果=%+v 错误=%v 备用目录=%d 推理=%d", result, err, nextCatalogs.Load(), chats.Load())
				}
			} else if !errors.Is(err, errQoderCatalogUnavailable) || result.AccountID != first || result.Attempts != 1 || nextCatalogs.Load() != 0 || chats.Load() != 0 || result.ChargeKnown {
				t.Fatalf("应保留原账号目录错误：结果=%+v 错误=%v 备用目录=%d 推理=%d", result, err, nextCatalogs.Load(), chats.Load())
			}
			if firstCatalogs.Load() != 1 || !m.running[first].coolUntil.IsZero() || m.running[first].inFlight != 0 {
				t.Fatal("停用目录触发了额外请求、冷却或占用槽位")
			}
			recorded := m.Usage()
			if len(recorded) != result.Attempts || recorded[0].AccountID != first || recorded[0].Requests != 1 || recorded[0].Failed != 1 || recorded[0].ChargeKnown != 0 || recorded[0].TokensKnown != 0 {
				t.Fatalf("目录失败计量错误：%+v", recorded)
			}
			if tc.wantRotate && (recorded[1].AccountID != next || recorded[1].ChargeKnown != 1 || recorded[1].Charge != 0.125) {
				t.Fatalf("备用账号费用归属错误：%+v", recorded)
			}
		})
	}
}

func TestQoderTextPartsPreserveSystemPromptAndToolResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		decoded, err := cosy.Decode(string(raw))
		if err != nil {
			t.Error(err)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(decoded, &body); err != nil {
			t.Error(err)
			return
		}
		if body["system"] != "系统一系统二\n附加规则\n" {
			t.Errorf("文本段系统提示词丢失：%q", body["system"])
		}
		ctx := object(body["chat_context"])
		if object(ctx["text"])["text"] != "问题一\n问题二" || object(object(ctx["extra"])["originalContent"])["text"] != "问题一\n问题二" {
			t.Errorf("文本段问题未进入当前上下文：%v", ctx)
		}
		messages := body["messages"].([]any)
		if object(messages[0])["role"] != "system" || object(messages[0])["content"] != "系统一系统二" || object(messages[3])["content"] != "" || object(messages[4])["content"] != "工具结果" || object(messages[4])["tool_call_id"] != "call" {
			t.Errorf("文本段归一化损坏了历史或工具结果：%v", messages)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	q := &Qoder{HTTP: server.Client(), Base: server.URL}
	body := `{"messages":[{"role":"developer","content":[{"type":"text","text":"系统一"},{"type":"text","text":"系统二"}]},{"role":"system","content":"附加规则"},{"role":"user","content":"旧问题"},{"role":"assistant","content":null,"tool_calls":[{"id":"call","type":"function","function":{"name":"shell","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call","content":[{"type":"text","text":"工具"},{"type":"text","text":"结果"}]},{"role":"user","content":[{"type":"text","text":"问题一\n"},{"type":"text","text":"问题二"}]}]}`
	if err := q.Chat(context.Background(), "qoder:global", Credential{UID: "fixture", UserType: "teams", AccessToken: "fixture"}, Model{ID: "model"}, []byte(body), "session", discardFrame); err != nil {
		t.Fatal(err)
	}
}

func TestQoderWrappedDoneDoesNotWaitForEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"statusCodeValue\":200,\"body\":\"[DONE]\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	q := &Qoder{HTTP: server.Client(), Base: server.URL}
	if err := q.Chat(ctx, "qoder:cn", Credential{UID: "fixture", UserType: "teams", AccessToken: "fixture"}, Model{ID: "model"}, []byte(plainChat), "session", func(map[string]any) error {
		t.Error("完成标记不应作为正文发出")
		return nil
	}); err != nil {
		t.Fatalf("包络完成标记仍等待连接结束: %v", err)
	}
}

func TestQoderWrappedResourceLimit(t *testing.T) {
	for _, tc := range []struct {
		name, code, subCode string
		status              int
		retry               bool
	}{
		{"官方资源排队包络", "10605", "RESOURCE_LIMIT", 503, true},
		{"普通权限拒绝", "403", "FORBIDDEN", 502, false},
		{"资源文字不能代替业务码", "403", "RESOURCE_LIMIT", 502, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner, _ := json.Marshal(map[string]any{"code": tc.code, "subCode": tc.subCode, "message": `{"isQueued":true,"retryAfterSeconds":30}`})
			body, _ := json.Marshal(map[string]any{"code": "403", "message": string(inner)})
			outer, _ := json.Marshal(map[string]any{"statusCodeValue": 403, "body": string(body)})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/algo/api/v2/service/ask/queue/status" {
					http.NotFound(w, r)
					return
				}
				if r.URL.Path == "/algo/api/v2/service/ask/finish" {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", outer)
			}))
			defer server.Close()
			q := &Qoder{HTTP: server.Client(), Base: server.URL}
			err := q.Chat(context.Background(), "qoder:cn", Credential{UID: "fixture", UserType: "personal", AccessToken: "fixture"}, Model{ID: "auto"}, []byte(plainChat), "session", func(map[string]any) error {
				t.Error("失败包络不能输出正文或完成事件")
				return nil
			})
			var upstream *Error
			if !errors.As(err, &upstream) || upstream.Status != tc.status || upstream.Retry != tc.retry {
				t.Fatalf("包络错误分类不正确：%v", err)
			}
			if tc.retry && upstream.Code != "10605" || !tc.retry && upstream.Code != "403" {
				t.Fatalf("丢失业务码：%s", upstream.Code)
			}
		})
	}
}

func TestSSEIntegrity(t *testing.T) {
	for _, tc := range []struct {
		input string
		valid bool
	}{
		{"event: done\r\ndata: {}\r\n\r\n", true},
		{"event: done\ndata: {}", true},
		{"event: done\n\n", true},
		{"data: {\n" + "data: \"choices\": []}\n\ndata: [DONE]\n\n", true},
		{"data: {\"event\":\"done\"}\n\n", true},
		{"data: {\"event_type\":\"done\"}", true},
		{"event: output\ndata: {\"event\":\"done\"}\n\n", false},
		{"event: progress_notice\ndata: \"正在处理请求\"\n\nevent: done\ndata: {}\n\n", true},
		{"event: progress_notice\ndata: \"正在处理请求\"\n\n", false},
		{"event: progress_notice\ndata: \"未闭合\n\nevent: done\ndata: {}\n\n", false},
		{"event: output\ndata: \"不能把正文协议错误当成进度通知\"\n\nevent: done\ndata: {}\n\n", false},
		{"data: {}\n\n", false},
		{"data: broken\n\n", false},
	} {
		completed := 0
		err := scanSSE(context.Background(), strings.NewReader(tc.input), func(event string, _ map[string]any) error {
			if event == "done" {
				completed++
			}
			return nil
		})
		if (err == nil) != tc.valid || (tc.valid && completed != 1) {
			t.Fatalf("%q: %v", tc.input, err)
		}
	}
}

func TestRealAdaptersProtectUnverifiedRefresh(t *testing.T) {
	for _, kind := range []string{"trae", "pat", "device", "job"} {
		for _, uid := range []string{"", "different", "original"} {
			t.Run(kind+"/"+uid, func(t *testing.T) {
				target, product := "qoder:cn", "qoder"
				if kind == "trae" {
					target, product = "traecode:cn", "traecode"
				}
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/cloudide/api/v3/trae/oauth/ExchangeToken":
						fmt.Fprint(w, `{"Result":{"Token":"new","RefreshToken":"rotated","TokenExpireDuration":3600}}`)
					case "/cloudide/api/v3/trae/GetUserInfo":
						if r.Header.Get("X-Cloudide-Token") != "new" {
							t.Error("Trae 未按新令牌核验")
						}
						fmt.Fprintf(w, `{"Result":{"UserID":%q}}`, uid)
					case "/algo/api/v3/user/jobToken":
						fmt.Fprint(w, `{"code":200,"body":{"securityOauthToken":"new","refreshToken":"rotated","expireTime":2000000000}}`)
					case "/api/v1/jobToken/refresh", "/api/v1/deviceToken/refresh":
						fmt.Fprint(w, `{"token":"new","refresh_token":"rotated","expires_at":2000000000}`)
					case "/api/v1/userinfo":
						if r.Header.Get("Authorization") != "Bearer new" {
							t.Error("Qoder 未按新令牌核验")
						}
						fmt.Fprintf(w, `{"id":%q}`, uid)
					default:
						t.Errorf("意外身份接口 %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer s.Close()
				path := filepath.Join(t.TempDir(), "providers.json")
				m, _ := testManager(t, path)
				id := importFake(t, m, target, "original")
				old := m.data.Accounts[0].Credential
				old.ExpiresAt = 1
				old.TokenKind = kind
				if kind == "pat" {
					old.PAT = "fixture-pat"
				}
				m.data.Accounts[0].Credential = old
				if err := m.saveLocked(); err != nil {
					t.Fatal(err)
				}
				if kind == "trae" {
					m.adapters[product] = &Trae{HTTP: s.Client(), AuthBase: s.URL}
				} else {
					m.adapters[product] = &Qoder{HTTP: s.Client(), Base: s.URL, OpenAPIBase: s.URL}
				}
				_, err := m.credential(t.Context(), id, target)
				reopened, _ := testManager(t, path)
				if uid == "original" {
					if err != nil || reopened.List()[0].Disabled || reopened.data.Accounts[0].Credential.RefreshToken != "rotated" {
						t.Fatal("未保存已核验的新令牌", err)
					}
				} else if err == nil || !reopened.List()[0].Disabled || reopened.data.Accounts[0].Credential != old {
					t.Fatal("身份不明时覆盖了旧凭证或未保留刷新保护", err)
				}
			})
		}
	}
}

func TestTraeCodeRejectsInvalidToolItem(t *testing.T) {
	m, _ := testManager(t, "")
	id := importFake(t, m, "traecode:cn", "fixture")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ide/v1/get_detail_param":
			fmt.Fprint(w, `{"config_info_list":[{"config_name":"model"}]}`)
		case "/api/agent/v3/llm_utils_chat":
			fmt.Fprint(w, "event: output\ndata: {\"tool_calls\":[null]}\n\n")
		default:
			t.Error("意外请求", r.URL.Path)
		}
	}))
	defer server.Close()
	m.adapters["traecode"] = &Trae{HTTP: server.Client(), CodeBase: server.URL}
	result, err := m.Execute(t.Context(), Request{Target: "traecode:cn", Model: "model", Body: []byte(plainChat)}, discardFrame)
	if err == nil || !strings.Contains(err.Error(), "tool_calls[0]") || result.Started || m.running[id].inFlight != 0 || len(m.Usage()) != 1 || m.Usage()[0].Failed != 1 {
		t.Fatal("异常工具项未正确收尾", result, err)
	}
}

// 等到每个调用者实际进入 select，才能核验并发查询只占一个槽位。
type catalogWaitContext struct {
	context.Context
	once    sync.Once
	waiting chan<- struct{}
}

func (c *catalogWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { c.waiting <- struct{}{} })
	return c.Context.Done()
}

func TestOrdinaryCatalogQueriesShareFlight(t *testing.T) {
	m, _ := testManager(t, "")
	id := importFake(t, m, "qoder:cn", "fixture")
	start, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			close(start)
			<-finish
		case 2:
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
	}))
	defer server.Close()
	m.adapters["qoder"] = &Qoder{HTTP: server.Client(), Base: server.URL}
	result := make(chan error, 10)
	query := func(ctx context.Context) {
		models, err := m.Models(ctx, id, "qoder:cn", false)
		if err == nil && len(models) != 1 {
			err = errors.New("目录结果为空")
		}
		result <- err
	}
	go query(t.Context())
	<-start
	if _, err := m.Models(t.Context(), id, "qoder:cn", true); err == nil {
		close(finish)
		t.Fatal("强制刷新未返回上游错误")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := m.Models(ctx, id, "qoder:cn", false)
	waiting := make(chan struct{}, 9)
	for range 9 {
		go query(&catalogWaitContext{Context: t.Context(), waiting: waiting})
	}
	for range 9 {
		select {
		case <-waiting:
		case <-time.After(3 * time.Second):
			close(finish)
			t.Fatal("普通查询未进入共享等待")
		}
	}
	oneSlot := m.List()[0].InFlight == 1
	close(finish)
	if !errors.Is(err, context.Canceled) || !oneSlot {
		t.Fatal("等待者取消改变了共享查询", err)
	}
	for range 10 {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 || m.List()[0].InFlight != 0 {
		t.Fatal("普通目录重复查询或槽位未释放", calls.Load())
	}
	if _, err := m.Models(t.Context(), id, "qoder:cn", true); err != nil || calls.Load() != 3 {
		t.Fatal("强制刷新未独立派发", err)
	}
}

func TestCatalogWaiterSurvivesOwnerCancellation(t *testing.T) {
	m, _ := testManager(t, "")
	id := importFake(t, m, "qoder:cn", "fixture")
	first := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(first)
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
	}))
	defer server.Close()
	m.adapters["qoder"] = &Qoder{HTTP: server.Client(), Base: server.URL}
	owner, cancel := context.WithCancel(t.Context())
	defer cancel()
	ownerErr, waiterErr := make(chan error, 1), make(chan error, 1)
	go func() { _, err := m.Models(owner, id, "qoder:cn", false); ownerErr <- err }()
	<-first
	waiting := make(chan struct{}, 1)
	go func() {
		_, err := m.Models(&catalogWaitContext{Context: t.Context(), waiting: waiting}, id, "qoder:cn", false)
		waiterErr <- err
	}()
	select {
	case <-waiting:
	case <-time.After(3 * time.Second):
		t.Fatal("等待者未进入共享查询")
	}
	cancel()
	if err := <-ownerErr; !errors.Is(err, context.Canceled) {
		t.Fatal("发起者未取消", err)
	}
	if err := <-waiterErr; err != nil || calls.Load() != 2 || m.List()[0].InFlight != 0 {
		t.Fatal("发起者取消影响了其他调用者", err, calls.Load())
	}
}
