// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yreidev/workbuddy2api-panel/internal/providers/cosy"
)

// Qoder 的签名参考 cli2api 5224c100、Magpie 175dfd25；请求身份已核对官方 CLI 1.1.89 样本并经 1.1.96 实包复核。
type Qoder struct {
	HTTP        *http.Client
	Base        string
	OpenAPIBase string
	endpointMu  sync.Mutex
	endpoints   map[[32]byte]*qoderEndpointEntry
}

// qoderAPIHeaders 对齐原生 CLI 的 OpenAPI 请求；COSY 网关使用 Bun 默认 UA。
func qoderAPIHeaders(token string) http.Header {
	h := jsonHeaders(token)
	h.Set("User-Agent", "qoder/"+cosy.CosyVersion)
	h.Set("Cosy-Version", cosy.CosyVersion)
	h.Set("Cosy-Clienttype", "5")
	h.Set("Cosy-Machineos", "x86_64_linux")
	h.Set("X-Request-ID", uuid.NewString())
	h.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	return h
}

func (q *Qoder) base(target string) string {
	if q.Base != "" {
		return q.Base
	}
	if target == "qoder:cn" {
		return "https://gateway.qoder.com.cn"
	}
	return "https://api2.qoder.sh"
}
func (q *Qoder) openAPI(target string) string {
	if q.OpenAPIBase != "" {
		return q.OpenAPIBase
	}
	_, base, _ := qoderSite(target)
	return base
}
func (q *Qoder) Refresh(ctx context.Context, target string, c Credential) (Credential, error) {
	if c.PAT == "" && c.RefreshToken == "" {
		return c, errors.New("Qoder 缺少 PAT 或刷新令牌，请重新授权")
	}
	ctx, err := q.withEndpoints(ctx, target, c)
	if err != nil {
		return c, err
	}
	if c.TokenKind == "job" || c.TokenKind == "device" {
		return q.refreshOAuth(ctx, target, c)
	}
	inner := map[string]any{"personalToken": c.PAT, "securityOauthToken": c.AccessToken, "refreshToken": c.RefreshToken, "needRefresh": c.RefreshToken != "", "authInfo": map[string]any{}}
	raw, _ := json.Marshal(inner)
	outer, _ := json.Marshal(map[string]any{"payload": string(raw), "encodeVersion": "1"})
	resp, err := request(ctx, q.HTTP, "POST", q.inferBase(ctx, target)+"/algo/api/v3/user/jobToken?Encode=1", []byte(cosy.Encode(outer)), cosy.CredentialHeaders(c.MachineID, c.MachineToken, c.MachineType, c.MachineOS))
	if err != nil {
		return c, err
	}
	defer resp.Body.Close()
	var root map[string]any
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&root); err != nil {
		return c, fmt.Errorf("Qoder 认证响应: %w", err)
	}
	data, err := qoderBody(root)
	if err != nil {
		return c, err
	}
	token := text(data["securityOauthToken"])
	if token == "" {
		return c, errors.New("Qoder 认证响应缺少 securityOauthToken")
	}
	c.AccessToken = token
	if rt := text(data["refreshToken"]); rt != "" {
		c.RefreshToken = rt
	}
	c.UID = text(data["id"])
	if name := text(data["name"]); name != "" {
		c.Name = name
	}
	if tier := text(data["userType"]); tier != "" {
		c.UserType = tier
	}
	c.ExpiresAt = seconds(data["expireTime"])
	if c.ExpiresAt == 0 {
		return c, errors.New("Qoder 认证响应缺少有效 expireTime")
	}
	if c.UID == "" {
		fresh, err := q.tokenUser(ctx, q.resolvedOpenAPI(ctx, target), c.AccessToken, c)
		return fresh, routeErrorAfterDispatch(err)
	}
	return c, nil
}
func (q *Qoder) user(ctx context.Context, target string, c Credential) (Credential, error) {
	ctx, err := q.withEndpoints(ctx, target, c)
	if err != nil {
		return c, err
	}
	inner := map[string]any{"userId": c.UID, "personalToken": "", "securityOauthToken": "", "refreshToken": "", "needRefresh": false, "authInfo": map[string]any{}}
	raw, _ := json.Marshal(inner)
	outer, _ := json.Marshal(map[string]any{"payload": string(raw), "encodeVersion": "1"})
	response, err := request(ctx, q.HTTP, "POST", q.inferBase(ctx, target)+"/algo/api/v3/user/status?Encode=1", []byte(cosy.Encode(outer)), cosy.CredentialHeaders(c.MachineID, c.MachineToken, c.MachineType, c.MachineOS))
	if err != nil {
		return c, err
	}
	root, err := decodeResponse(response)
	if err != nil {
		return c, err
	}
	data, err := qoderBody(root)
	if err != nil {
		return c, err
	}
	c.UserType = text(data["userType"])
	if c.UserType == "" {
		return c, errors.New("Qoder 账号状态缺少 userType，无法确定签名身份")
	}
	return c, nil
}
func qoderStatusError(root map[string]any) error {
	for _, field := range []string{"code", "statusCodeValue"} {
		if code := root[field]; code != nil && fmt.Sprint(code) != "0" && fmt.Sprint(code) != "200" {
			// 官方 SSE 会把 10605/RESOURCE_LIMIT 再包成两层 JSON 字符串和 403。
			// 只识别明确的业务码与 subCode，不能把普通权限拒绝误判成可重试。
			current := root
			for depth := 0; depth < 4; depth++ {
				if fmt.Sprint(current["code"]) == "10605" && text(current["subCode"]) == "RESOURCE_LIMIT" {
					status, err := decodeQoderQueue(current)
					if err != nil {
						return err
					}
					return &qoderQueuedError{status: status}
				}
				value := current["body"]
				if value == nil {
					value = current["message"]
				}
				next := object(value)
				if encoded, ok := value.(string); ok {
					if err := json.Unmarshal([]byte(encoded), &next); err != nil {
						break
					}
				}
				if next == nil {
					break
				}
				current = next
			}
			return &Error{Status: 502, Code: safeCode(code), Retry: fmt.Sprint(code) == "10605"}
		}
	}
	return nil
}
func qoderValue(root map[string]any) (any, error) {
	if err := qoderStatusError(root); err != nil {
		return nil, err
	}
	body := root["body"]
	if body == nil {
		body = root["data"]
	}
	if body == nil {
		return root, nil
	}
	switch body.(type) {
	case map[string]any, []any:
		return body, nil
	}
	s, ok := body.(string)
	if !ok {
		return nil, errors.New("Qoder 响应 body 类型错误")
	}
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("Qoder 响应 body: %w", err)
	}
	return out, nil
}
func qoderBody(root map[string]any) (map[string]any, error) {
	value, err := qoderValue(root)
	if err != nil {
		return nil, err
	}
	out, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("Qoder 响应需要对象 body")
	}
	return out, nil
}
func (q *Qoder) signed(ctx context.Context, target string, c Credential, method, path string, payload any) (*http.Response, error) {
	return q.signedHeaders(ctx, target, c, method, path, payload, nil)
}

func (q *Qoder) signedHeaders(ctx context.Context, target string, c Credential, method, path string, payload any, extra http.Header) (*http.Response, error) {
	ctx, err := q.withEndpoints(ctx, target, c)
	if err != nil {
		return nil, err
	}
	return q.signedAt(ctx, q.inferBase(ctx, target), c, method, path, payload, extra)
}

func (q *Qoder) signedAt(ctx context.Context, base string, c Credential, method, path string, payload any, extra http.Header) (*http.Response, error) {
	raw := []byte{}
	if payload != nil {
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	encoded := ""
	if len(raw) > 0 {
		encoded = cosy.Encode(raw)
	}
	sess := cosy.NewSession(cosy.AuthIdentity{Name: c.Name, Aid: c.UID, UID: c.UID, UserType: c.UserType, SecurityOauthToken: c.AccessToken, RefreshToken: c.RefreshToken}, c.MachineID, c.MachineToken, c.MachineType)
	sess.MachineCode, sess.MachineOS, sess.MachineHostname = c.MachineCode, c.MachineOS, c.MachineHostname
	headers := cosy.Headers(sess, strings.Split(path, "?")[0], encoded)
	headers.Set("Accept", "application/json")
	if strings.Contains(path, "sse/") {
		headers.Set("Accept", "text/event-stream")
		headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
		headers.Set("Cache-Control", "no-cache")
		headers.Set("Connection", "keep-alive")
		config := payload.(map[string]any)["model_config"].(map[string]any)
		headers.Set("X-Model-Key", text(config["key"]))
		headers.Set("X-Model-Source", text(config["source"]))
		body := payload.(map[string]any)
		for header, field := range map[string]string{"X-Request-ID": "request_id", "X-RequestSet-ID": "request_set_id", "X-Session-ID": "session_id"} {
			if value := text(body[field]); value != "" {
				headers.Set(header, value)
			}
		}
		var trace [24]byte
		rand.Read(trace[:])
		headers.Set("Traceparent", fmt.Sprintf("00-%x-%x-01", trace[:16], trace[16:]))
	}
	for key, values := range extra {
		headers[key] = values
	}
	return request(ctx, q.HTTP, method, base+path, []byte(encoded), headers)
}

var errQoderCatalogUnavailable = errors.New("Qoder 没有可用模型")

func (q *Qoder) Models(ctx context.Context, target string, c Credential) ([]Model, error) {
	// 官方 models.list 默认十秒预算，目录读取最多尝试三个不同节点。
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ctx, err := q.withEndpoints(ctx, target, c)
	if err != nil {
		return nil, err
	}
	nodes := []string{q.inferBase(ctx, target)}
	routes, _ := ctx.Value(qoderEndpointsKey{}).(qoderEndpoints)
	for _, node := range routes.inferenceNodes {
		if len(nodes) == 3 {
			break
		}
		if !slices.Contains(nodes, node) {
			nodes = append(nodes, node)
		}
	}
	var resp *http.Response
	for i, node := range nodes {
		resp, err = q.signedAt(ctx, node, c, http.MethodGet, "/algo/api/v2/model/list?Encode=1", nil, nil)
		if err == nil {
			break
		}
		if ctx.Err() != nil || !qoderEndpointFailure(err) {
			return nil, err
		}
		q.rejectInferenceEndpoint(routes, node)
		if i+1 < len(nodes) {
			log.Printf("[providers] Qoder 目录节点失败，尝试备用节点 endpoint=%s: %v", node, err)
		}
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var root map[string]any
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&root); err != nil {
		return nil, fmt.Errorf("解析 Qoder 模型目录: %w", err)
	}
	data, err := qoderValue(root)
	if err != nil {
		return nil, err
	}
	if scenes, ok := data.(map[string]any); ok {
		if err := qoderStatusError(scenes); err != nil {
			return nil, err
		}
		// CLI 默认只从 assistant 场景读取系统模型。
		var present bool
		data, present = scenes["assistant"]
		if !present {
			return nil, errors.New("Qoder 模型目录缺少 assistant 场景")
		}
	}
	rows, ok := data.([]any)
	if !ok {
		return nil, errors.New("Qoder 模型目录必须为数组")
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w（上游未返回模型）", errQoderCatalogUnavailable)
	}
	out := make([]Model, 0, len(rows))
	for i, row := range rows {
		model := object(row)
		key := text(model["key"])
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("Qoder 模型目录第 %d 条记录缺少 key", i+1)
		}
		switch model["enable"] {
		case false, float64(0), "0":
			continue
		}
		if model["enabled"] == false {
			continue
		}
		out = append(out, Model{ID: key, Name: text(model["display_name"]), Tools: true, Context: number(model["max_input_tokens"]), Native: model})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w（上游目录全部停用）", errQoderCatalogUnavailable)
	}
	return out, nil
}
func (q *Qoder) Chat(ctx context.Context, target string, c Credential, model Model, raw []byte, session string, emit func(map[string]any) error) error {
	var src map[string]any
	if err := json.Unmarshal(raw, &src); err != nil {
		return err
	}
	messages, _ := src["messages"].([]any)
	system := ""
	prompt := ""
	for _, v := range messages {
		msg := object(v)
		if parts, ok := msg["content"].([]any); ok {
			var content strings.Builder
			for _, part := range parts {
				content.WriteString(text(object(part)["text"]))
			}
			msg["content"] = content.String()
		}
		role := text(msg["role"])
		if role == "developer" {
			msg["role"] = "system"
		}
		if text(msg["role"]) == "system" {
			system += text(msg["content"]) + "\n"
		}
		if role == "user" {
			prompt = text(msg["content"])
		}
		if role == "assistant" && msg["content"] == nil {
			msg["content"] = ""
		}
	}
	params := map[string]any{}
	for _, k := range []string{"temperature", "top_p", "stop", "response_format", "reasoning_effort", "tool_choice", "parallel_tool_calls", "max_tokens"} {
		if v, ok := src[k]; ok {
			params[k] = v
		}
	}
	if _, ok := params["max_tokens"]; !ok {
		params["max_tokens"] = 32000
	}
	if v, ok := src["max_completion_tokens"]; ok {
		params["max_tokens"] = v
	}
	reasoning := text(src["reasoning_effort"]) != "" && text(src["reasoning_effort"]) != "none"
	params["enable_thinking"] = reasoning
	cfg := map[string]any{"key": model.ID, "format": "openai", "source": "system", "is_reasoning": reasoning, "max_input_tokens": model.Context, "api_key": "", "url": ""}
	if model.Native != nil {
		for _, k := range []string{"format", "source", "max_input_tokens"} {
			if v, ok := model.Native[k]; ok {
				cfg[k] = v
			}
		}
	}
	body := map[string]any{"request_id": uuid.NewString(), "request_set_id": uuid.NewString(), "chat_record_id": uuid.NewString(), "session_id": session, "stream": true, "chat_task": "FREE_INPUT", "is_reply": false, "is_retry": false, "source": 1, "version": "3", "agent_id": "agent_common", "task_id": "common", "session_type": "qodercli", "model_config": cfg, "custom_model": nil, "system": system, "messages": messages, "tools": src["tools"], "parameters": params, "business": map[string]any{"product": "cli", "version": cosy.CosyVersion, "type": "agent", "model_key": model.ID, "id": uuid.NewString(), "name": "chat", "begin_at": time.Now().UnixMilli(), "stage": "start"}, "chat_context": map[string]any{"text": map[string]any{"type": "text", "text": prompt}, "features": []any{}, "extra": map[string]any{"modelConfig": map[string]any{"key": model.ID, "is_reasoning": reasoning}, "originalContent": map[string]any{"type": "text", "text": prompt}}, "chatPrompt": "", "imageUrls": nil}}
	ctx, err := q.withEndpoints(ctx, target, c)
	if err != nil {
		return err
	}
	if routes, ok := ctx.Value(qoderEndpointsKey{}).(qoderEndpoints); ok && strings.EqualFold(model.ID, "fast") && text(cfg["source"]) == "system" && routes.fast != "" {
		routes.inference = routes.fast
		ctx = context.WithValue(ctx, qoderEndpointsKey{}, routes)
	}
	return q.chatQueued(ctx, target, c, model, body, emit)
}

func (q *Qoder) chatAttempt(ctx context.Context, target string, c Credential, model Model, body map[string]any, emit func(map[string]any) error) error {
	resp, err := q.signed(ctx, target, c, "POST", "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return scanSSE(ctx, resp.Body, func(event string, root map[string]any) error {
		if event == "error" || root["error"] != nil {
			return &Error{Status: 502, Code: "stream_error"}
		}
		// Qoder 也会把完成标记放在包络 body 中；它不是 JSON 正文。
		if strings.TrimSpace(text(root["body"])) == "[DONE]" {
			if err := qoderStatusError(root); err != nil {
				return err
			}
			return errSSEDone
		}
		data, err := qoderBody(root)
		if err != nil {
			return err
		}
		if data["error"] != nil {
			return &Error{Status: 502, Code: "stream_error"}
		}
		if data["choices"] == nil && data["usage"] == nil {
			return nil
		}
		data["model"] = model.ID
		return emit(data)
	})
}
