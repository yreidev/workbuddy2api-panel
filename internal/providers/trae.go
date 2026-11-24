// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"log"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// 官方 Linux 稳定包 3.4.24（2.3.106768）的产品信息与 AI 扩展配置。
const traeVersion = "3.4.24"
const traePluginVersion = "2.3.106768"
const traeAPIVersionCode = "20260212"
const traeWebUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/156.0.0.0 Safari/537.36"

func traeAuthHeaders(c Credential) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-Cloudide-Token", c.AccessToken)
	h.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) TraeCN/1.107.1 Chrome/142.0.7444.235 Electron/39.2.7 Safari/537.36")
	h.Set("Sec-Fetch-Site", "none")
	h.Set("Sec-Fetch-Mode", "no-cors")
	h.Set("Sec-Fetch-Dest", "empty")
	h.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	h.Set("Accept-Language", "en-US")
	h.Set("Priority", "u=4, i")
	return h
}

// Trae 分别访问 Work 与 Code，不因工具或错误隐式改变产品通道。
// 协议依据见 docs/official-client.md；Base 等覆盖值仅供假上游测试注入。
type Trae struct {
	HTTP                                  *http.Client
	CodeBase, WorkBase, AuthBase, PayBase string
}

func (t *Trae) codeBase() string {
	if t.CodeBase != "" {
		return t.CodeBase
	}
	return "https://trae-api-cn.mchost.guru"
}
func (t *Trae) workBase() string {
	if t.WorkBase != "" {
		return t.WorkBase
	}
	return "https://work.trae.cn"
}
func (t *Trae) authBase() string {
	if t.AuthBase != "" {
		return t.AuthBase
	}
	return "https://api.trae.com.cn"
}
func traeHeaders(c Credential, work bool) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("Authorization", "Cloud-IDE-JWT "+c.AccessToken)
	if work {
		h.Del("Content-Type")
		h.Set("Accept", "*/*")
		h.Set("User-Agent", traeWebUA)
		h.Set("Sec-Ch-Ua", `"Not:A-Brand";v="8", "Chromium";v="156", "Google Chrome";v="156"`)
		h.Set("Sec-Ch-Ua-Mobile", "?0")
		h.Set("Sec-Ch-Ua-Platform", `"Linux"`)
		h.Set("Sec-Fetch-Dest", "empty")
		h.Set("Sec-Fetch-Mode", "cors")
		h.Set("Sec-Fetch-Site", "same-origin")
		h.Set("Accept-Language", "zh-CN,zh;q=0.9")
		h.Set("Accept-Encoding", "gzip, deflate, br, zstd")
		h.Set("Priority", "u=1, i")
		h.Set("X-Trae-Client-Type", "web")
		h.Set("X-Trae-User-Timezone", "Asia/Taipei")
		h.Set("X-Preferenced-Language", "zh-cn")
		h.Set("Referer", "https://work.trae.cn/")
		return h
	}
	h.Set("User-Agent", "TraeClient/TTNet Cronet/TTNetVersion:a61b3479 2026-08-04 QuicVersion:40cc763c 2025-07-01")
	h.Set("Accept-Encoding", "gzip, deflate")
	h.Set("X-Cloudide-Token", c.AccessToken)
	h.Set("X-Ide-Token", c.AccessToken)
	h.Set("X-Uid", c.UID)
	h.Set("X-App-Id", "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8")
	h.Set("X-App-Version", "default")
	h.Set("X-Ide-Version", traeVersion)
	h.Set("X-Ide-Version-Code", traeAPIVersionCode)
	h.Set("X-Ide-Version-Type", "stable")
	h.Set("X-Device-Type", "linux")
	h.Set("X-OS-Version", "Linux")
	h.Set("Request-Traffic-Type", "prod")
	h.Set("X-Machine-Id", c.MachineID)
	h.Set("X-Device-Id", c.DeviceID)
	return h
}
func (t *Trae) Refresh(ctx context.Context, _ string, c Credential) (Credential, error) {
	ctx = upstream.WithClientProfile(ctx, upstream.ProfileTraeAuth)
	if c.RefreshToken == "" {
		return c, errors.New("Trae 缺少 refresh_token，请重新登录或导入完整凭证")
	}
	body := map[string]any{"ClientID": c.ClientID, "RefreshToken": c.RefreshToken, "ClientSecret": "-", "UserID": ""}
	path := "/cloudide/api/v3/trae/oauth/ExchangeToken"
	if c.DeviceKey != "" {
		path = "/trae/api/v3/oauth/ExchangeToken"
		device, err := traeDevice(c)
		if err != nil {
			return c, err
		}
		timestamp, nonce := time.Now().Unix(), uuid.NewString()
		signature, err := traeProof(c, path, nonce, timestamp)
		if err != nil {
			return c, err
		}
		body = map[string]any{"ClientID": c.ClientID, "ClientSecret": "", "RefreshToken": c.RefreshToken, "DeviceInfo": device, "IDEVersion": traeVersion, "DeviceProof": map[string]any{"Signature": signature, "Timestamp": timestamp, "Nonce": nonce}}
	}
	root, err := jsonRequest(ctx, t.HTTP, "POST", t.authBase()+path, body, traeAuthHeaders(c))
	if err != nil {
		return c, err
	}
	fresh, err := decodeTraeCredential(c, root)
	if err != nil {
		return c, err
	}
	if id := text(object(root["Result"])["UserID"]); id != "" {
		fresh.UID = id
		return fresh, nil
	}
	fresh, err = t.user(ctx, fresh)
	return fresh, routeErrorAfterDispatch(err)
}
func decodeTraeCredential(c Credential, root map[string]any) (Credential, error) {
	r := object(root["Result"])
	if r == nil {
		return c, errors.New("Trae 认证响应缺少 Result")
	}
	if token := text(r["Token"]); token != "" {
		c.AccessToken = token
	} else {
		return c, errors.New("Trae 认证响应缺少 Token")
	}
	if rt := text(r["RefreshToken"]); rt != "" {
		c.RefreshToken = rt
	}
	c.ExpiresAt = seconds(r["TokenExpireAt"])
	if c.ExpiresAt == 0 && number(r["TokenExpireDuration"]) > 0 {
		c.ExpiresAt = time.Now().Unix() + number(r["TokenExpireDuration"])
	}
	if c.ExpiresAt == 0 {
		return c, errors.New("Trae 认证响应缺少有效令牌过期时间")
	}
	return c, nil
}
func (t *Trae) user(ctx context.Context, c Credential) (Credential, error) {
	ctx = upstream.WithClientProfile(ctx, upstream.ProfileTraeAuth)
	root, err := jsonRequest(ctx, t.HTTP, "POST", t.authBase()+"/cloudide/api/v3/trae/GetUserInfo", map[string]any{"ReqSource": "IDE", "IDEVersion": traeVersion}, traeAuthHeaders(c))
	if err != nil {
		return c, err
	}
	r := object(root["Result"])
	c.UID = text(r["UserID"])
	if name := text(r["ScreenName"]); name != "" {
		c.Name = name
	}
	if c.UID == "" {
		return c, errors.New("Trae 用户响应缺少 UserID")
	}
	return c, nil
}
func (t *Trae) Models(ctx context.Context, target string, c Credential) ([]Model, error) {
	if target == "traework:cn" {
		ctx = upstream.WithClientProfile(ctx, upstream.ProfileTraeWork)
		root, err := jsonRequest(ctx, t.HTTP, "GET", t.workBase()+"/api/remote/v1/models?functions=solo_work_remote&show_custom_model=false", nil, traeHeaders(c, true))
		if err != nil {
			return nil, err
		}
		data := object(root["data"])
		list, _ := data["list"].([]any)
		out := []Model{}
		for _, v := range list {
			fn := object(v)
			if text(fn["function"]) != "solo_work_remote" {
				continue
			}
			models, _ := fn["models"].([]any)
			for _, v := range models {
				md := object(v)
				id := text(md["name"])
				if id == "" || strings.HasPrefix(id, "custom_model_") {
					continue
				}
				out = append(out, Model{ID: id, Name: text(md["display_name"]), Native: md})
			}
		}
		return out, nil
	}
	ctx = upstream.WithClientProfile(ctx, upstream.ProfileTraeCode)
	root, err := jsonRequest(ctx, t.HTTP, "POST", t.codeBase()+"/api/ide/v1/get_detail_param", map[string]any{"function": "chat_v3", "config_names": nil, "need_prompt": false, "current_config_info": nil, "poly_prompt": true, "mode_type": nil, "agent_type": nil}, traeHeaders(c, false))
	if err != nil {
		return nil, err
	}
	list, _ := root["config_info_list"].([]any)
	out := []Model{}
	seen := map[string]bool{}
	for _, v := range list {
		md := object(v)
		id := text(md["config_name"])
		display := object(md["display_config"])
		if id == "" || seen[id] || strings.HasPrefix(id, "custom_model_") || md["is_invisible_to_user"] == true || display["is_custom_model"] == true {
			continue
		}
		seen[id] = true
		out = append(out, Model{ID: id, Name: text(display["display_name"]), Tools: true, Context: number(object(md["context_window_tokens"])["dev"]), Native: md})
	}
	return out, nil
}
func (t *Trae) Chat(ctx context.Context, target string, c Credential, model Model, raw []byte, session string, emit func(map[string]any) error) error {
	var src map[string]any
	if err := json.Unmarshal(raw, &src); err != nil {
		return err
	}
	if target == "traework:cn" {
		return t.workChat(ctx, c, model, src, session, emit)
	}
	src["model"] = model.ID
	src["config_name"] = model.ID
	src["function"] = "chat_v3"
	src["stream"] = true
	src["session_id"] = session
	messages, _ := src["messages"].([]any)
	for _, v := range messages {
		msg := object(v)
		if text(msg["role"]) == "developer" {
			msg["role"] = "system"
		}
		if content, ok := msg["content"].(string); ok {
			msg["content"] = []any{map[string]any{"type": "text", "text": content}}
		}
		calls, _ := msg["tool_calls"].([]any)
		for _, v := range calls {
			call := object(v)
			if fn := call["function"]; fn != nil {
				call["function_call"] = fn
				delete(call, "function")
			}
		}
	}
	if choice, ok := src["tool_choice"].(map[string]any); ok {
		name := text(object(choice["function"])["name"])
		if text(choice["type"]) != "function" || strings.TrimSpace(name) == "" {
			return &Error{Status: 400, Code: "invalid_tool_choice", Message: "Trae Code 指定工具需要 function.name"}
		}
		src["tool_choice"] = name
	}
	if text(src["tool_choice"]) == "none" {
		delete(src, "tool_choice")
		delete(src, "tools")
		delete(src, "functions")
	}
	tools, _ := src["tools"].([]any)
	for _, v := range tools {
		tool := object(v)
		fn := object(tool["function"])
		if params, ok := fn["parameters"]; ok {
			b, e := json.Marshal(params)
			if e != nil {
				return e
			}
			fn["parameters"] = string(b)
		}
	}
	body, err := json.Marshal(src)
	if err != nil {
		return err
	}
	h := traeHeaders(c, false)
	h.Set("Accept", "text/event-stream")
	ctx = upstream.WithClientProfile(ctx, upstream.ProfileTraeCode)
	resp, err := request(ctx, t.HTTP, "POST", t.codeBase()+"/api/agent/v3/llm_utils_chat", body, h)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sawTools, sawFinish := false, false
	return scanSSE(ctx, resp.Body, func(event string, data map[string]any) error {
		switch event {
		case "error":
			return &Error{Status: 502, Code: safeCode(data["code"])}
		case "output":
			delta := map[string]any{}
			if v := data["response"]; v != nil {
				delta["content"] = v
			}
			if v := data["reasoning_content"]; v != nil {
				delta["reasoning_content"] = v
			}
			if calls, ok := data["tool_calls"].([]any); ok {
				for i, v := range calls {
					call := object(v)
					if call == nil {
						return fmt.Errorf("Trae Code tool_calls[%d] 必须为对象", i)
					}
					if call["index"] == nil {
						call["index"] = i
					}
					if fn := call["function_call"]; fn != nil {
						call["function"] = fn
						delete(call, "function_call")
					}
				}
				delta["tool_calls"] = calls
				sawTools = true
			}
			return emit(chunk(model.ID, delta, nil, nil))
		case "token_usage":
			return emit(chunk(model.ID, map[string]any{}, nil, data))
		case "done":
			if sawFinish {
				return nil
			}
			finish := "stop"
			if sawTools {
				finish = "tool_calls"
			}
			sawFinish = true
			return emit(chunk(model.ID, map[string]any{}, finish, nil))
		default:
			if choices, ok := data["choices"].([]any); ok {
				for _, v := range choices {
					if reason, ok := object(v)["finish_reason"].(string); ok && strings.TrimSpace(reason) != "" {
						sawFinish = true
					}
				}
				return emit(data)
			}
			return nil
		}
	})
}
func (t *Trae) workChat(ctx context.Context, c Credential, model Model, src map[string]any, session string, emit func(map[string]any) error) (chatErr error) {
	ctx = upstream.WithClientProfile(ctx, upstream.ProfileTraeWork)
	messages, _ := src["messages"].([]any)
	var query strings.Builder
	for _, v := range messages {
		msg := object(v)
		content := text(msg["content"])
		if parts, ok := msg["content"].([]any); ok {
			for _, part := range parts {
				content += text(object(part)["text"])
			}
		}
		fmt.Fprintf(&query, "%s: %s\n", text(msg["role"]), content)
	}
	// Work 网页将 query 编码为文本块数组，不能直接发送普通文本。
	encodedQuery, _ := json.Marshal([]map[string]any{{"type": "text", "data": map[string]string{"content": query.String()}}})
	configSource := number(model.Native["config_source"])
	if configSource <= 0 {
		return fmt.Errorf("TraeWork 模型 %s 的目录缺少有效 config_source，请重新获取模型目录", model.ID)
	}
	configName := text(model.Native["config_name"])
	if configName == "" {
		configName = model.ID
	}
	// 目录展示对象与请求模型描述不同，按官方 buildSendMessageRequest 的字段构造。
	customModel := map[string]any{"config_name": configName, "model_name": model.ID, "display_model_name": model.Name, "config_source": configSource, "is_preset": configSource == 1, "use_remote_service": model.Native["client_connect"] != true}
	for _, key := range []string{"provider", "ak", "base_url", "custom_model_id", "custom_model_type", "multimodal", "prompt_max_tokens"} {
		if value, ok := model.Native[key]; ok {
			customModel[key] = value
		}
	}
	common, _ := json.Marshal(map[string]string{"biz_session_id": session, "solo_chat_mode": "work"})
	initial := map[string]any{"chat_session_id": "", "content": []any{}, "query": string(encodedQuery), "model_name": model.ID, "agent_type": "solo_work_remote", "agent_id": "solo_work_remote", "model_selection_strategy": "manual", "custom_model": customModel, "common_params": string(common)}
	headers := traeHeaders(c, true)
	headers.Set("Origin", "https://work.trae.cn")
	root, err := jsonRequest(ctx, t.HTTP, "POST", t.workBase()+"/api/remote/v1/chat_sessions", map[string]any{"mode": "work", "environment_id": "default", "initial_message": initial, "env": "remote", "auto_create_project": false, "origin": "web", "__hit_shared_artifact_and_workroom": true, "__hit_my_space": true}, headers)
	if err != nil {
		return fmt.Errorf("TraeWork 创建会话: %w", err)
	}
	data := object(root["data"])
	sid, mid := text(data["chat_session_id"]), text(data["message_id"])
	if sid == "" || mid == "" {
		return errors.New("TraeWork 创建会话响应缺少 chat_session_id 或 message_id")
	}
	defer func() {
		// 官方按本条用户消息 ID 结算。短查询独立于下游取消，已发生的扣费仍要记录。
		billingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		retryAt := time.Now().Add(2 * time.Second)
		credit, known, billingErr := t.workCharge(billingCtx, c, mid)
		if billingErr == nil && !known {
			// 官方网页为新消息预留两秒结算时间。补查沿用本次查询的截止时间。
			wait := time.Until(retryAt)
			if wait < 0 {
				wait = 0
			}
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
				timer.Stop()
				credit, known, billingErr = t.workCharge(billingCtx, c, mid)
			case <-billingCtx.Done():
				timer.Stop()
				billingErr = billingCtx.Err()
			}
		}
		if billingErr != nil {
			log.Printf("[providers] TraeWork 查询单消息扣费: %v", billingErr)
		} else if known {
			if emitErr := emit(chunk(model.ID, map[string]any{}, nil, map[string]any{"credit": credit})); chatErr == nil {
				chatErr = emitErr
			}
		}
		// 会话创建成功后上游已受理本条消息并可能产生扣费，任何后续错误均标记已派发，禁止换号重放。
		if chatErr != nil {
			chatErr = &dispatchedError{chatErr}
		}
	}()
	h := traeHeaders(c, true)
	h.Set("Accept", "text/event-stream")
	resp, err := request(ctx, t.HTTP, "GET", t.workBase()+"/api/remote/v1/chat_sessions/"+url.PathEscape(sid)+"/events?reply_to_message_id="+url.QueryEscape(mid), nil, h)
	if err != nil {
		return fmt.Errorf("TraeWork 订阅回复事件: %w", err)
	}
	defer resp.Body.Close()
	snapshots := map[string]string{}
	visible := ""
	return scanSSE(ctx, resp.Body, func(event string, data map[string]any) error {
		if event == "error" {
			return &Error{Status: 502, Code: safeCode(data["code"])}
		}
		if usage := object(data["usage"]); usage != nil {
			delete(usage, "credit") // 推理流的费用字段没有单消息结算依据。
			if len(usage) > 0 {
				if err := emit(chunk(model.ID, map[string]any{}, nil, usage)); err != nil {
					return err
				}
			}
		}
		switch event {
		case "plan_item":
			key := text(data["id"])
			snapshot := workText(data)
			if snapshot == "" && (data["reasoning_content"] != nil || data["reasoningContent"] != nil) {
				snapshot = text(data["thought"])
			}
			previous := snapshots[key]
			if snapshot != "" && snapshot != previous {
				if !strings.HasPrefix(snapshot, previous) {
					return errors.New("TraeWork 文本快照发生回退，无法安全合并")
				}
				delta := strings.TrimPrefix(snapshot, previous)
				snapshots[key] = snapshot
				visible += delta
				return emit(chunk(model.ID, map[string]any{"content": delta}, nil, nil))
			}
		case "message", "assistant_message", "response", "text", "output":
			snapshot := workText(data)
			if snapshot != "" && snapshot != visible {
				if !strings.HasPrefix(snapshot, visible) {
					return errors.New("TraeWork 消息快照与已输出文本不一致")
				}
				delta := strings.TrimPrefix(snapshot, visible)
				visible = snapshot
				return emit(chunk(model.ID, map[string]any{"content": delta}, nil, nil))
			}
		case "token_usage":
			delete(data, "credit")
			return emit(chunk(model.ID, map[string]any{}, nil, data))
		case "done":
			if visible == "" {
				return errors.New("TraeWork 返回空文本，未完成文本对话")
			}
			return emit(chunk(model.ID, map[string]any{}, "stop", nil))
		}
		return nil
	})
}

// workCharge 与官方 getCnSessionUsage 同口径。0 尚无法区分免费与尚未结算，保持未知。
func (t *Trae) workCharge(ctx context.Context, c Credential, messageID string) (float64, bool, error) {
	base := t.codeBase()
	if t.CodeBase == "" && t.WorkBase != "" {
		base = t.WorkBase // 假上游测试使用同一服务器。
	}
	h := traeHeaders(c, true)
	h.Set("Content-Type", "application/json")
	h.Set("Origin", "https://work.trae.cn")
	root, err := jsonRequest(ctx, t.HTTP, "POST", base+"/api/v1/commercial/get_session_usage", map[string]string{"session_id": messageID}, h)
	if err != nil {
		return 0, false, err
	}
	data := root
	if nested := object(root["data"]); nested != nil {
		data = nested
	}
	if nested := object(data["user_usage_group_by_session"]); nested != nil {
		data = nested
	}
	value, exists := data["credits_float"]
	if !exists {
		return 0, false, nil
	}
	credit, ok := value.(float64)
	if !ok || math.IsNaN(credit) || math.IsInf(credit, 0) || credit < 0 {
		return 0, false, errors.New("单消息用量 credits_float 必须为有限非负数")
	}
	if text(data["session_id"]) != messageID {
		return 0, false, errors.New("单消息用量 session_id 与用户消息不符，拒绝记账")
	}
	return credit, credit > 0, nil
}

func workText(data map[string]any) string {
	for _, key := range []string{"message", "agent_message", "assistant_message"} {
		if nested := object(data[key]); nested != nil {
			if value := workText(nested); value != "" {
				return value
			}
		}
	}
	for _, key := range []string{"content", "text", "response", "answer", "output"} {
		if value, ok := data[key].(string); ok && value != "" {
			return value
		}
		if parts, ok := data[key].([]any); ok {
			var result strings.Builder
			for _, part := range parts {
				switch p := part.(type) {
				case string:
					result.WriteString(p)
				case map[string]any:
					if text(p["type"]) == "text" {
						result.WriteString(text(p["text"]))
					}
				}
			}
			if result.Len() > 0 {
				return result.String()
			}
		}
	}
	return ""
}
