// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// capqueue.go 容量排队（业务码 6020）：上游这一路满了时，对话请求回 6020 和排位，客户端在原来的号上
// 排队，到号了原样重发。网关怎么等、等多久见 server/capqueue.go。
//
// 依据：国内版桌面端 5.9.3 渲染层的 QueuePollingManager（app/renderer/assets/workbuddy-provider-*.js）
// 和主进程替它发请求的 queue:getStatus、queue:cancel（app/main/server.js），读安装包确认，见
// docs/official-client.md「容量排队」：
//   - 查询：POST {入口}/v2/chat/queue/status，请求体 {"model":模型}。主进程直接用 axios 默认实例发，
//     头只有 Authorization、Content-Type、X-Conversation-Request-ID（排进队的那一轮），有会话 id 时加
//     X-Conversation-ID，再就是 axios 自带的 Accept、Accept-Encoding、User-Agent，没有官方请求框架的
//     那些头。15 秒超时。回 code 0 且带 data 时看 data.status：waiting 接着等、ready 到号、expired 过期；
//     code 不是 0 或没有 data 算过期；HTTP 出错（axios 抛错）照排位间隔再查。
//   - 取消：POST {入口}/v2/chat/queue/cancel，请求体同上，头不带 X-Conversation-ID。官方只在使用者
//     点取消时发，过期、超时都不发。
//   - 节奏：第一次在 6020 带的 retry_after 秒后查（没带按 5 秒），之后按排位，见 QueuePollInterval。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const (
	queueStatusPath = "/v2/chat/queue/status"
	queueCancelPath = "/v2/chat/queue/cancel"
	// queueRequestTimeout 查询、取消的超时，同官方。
	queueRequestTimeout = 15 * time.Second
	// queueFirstPollDefault 6020 没带 retry_after 时第一次查询前等多久，同官方。
	queueFirstPollDefault = 5 * time.Second
)

// QueueWait 6020 回应里的排队信息。
type QueueWait struct {
	Position   int           // 排位；-1 = 上游没给
	RetryAfter time.Duration // 第一次查询前等多久
}

// ParseQueueWaiting 取 6020 回应体里的排位和 retry_after（秒）。官方两处读法不同：渲染层直接读错误数据，
// 主进程先取 data、再看 data.data，所以顶层、data、data.data 依次找，哪层有这两个字段就用哪层。
// retry_after 没给或不是非负数时按 5 秒。
func ParseQueueWaiting(body string) QueueWait {
	w := QueueWait{Position: -1, RetryAfter: queueFirstPollDefault}
	var root map[string]any
	if json.Unmarshal([]byte(body), &root) != nil {
		return w
	}
	node := root
	for depth := 0; node != nil && depth < 3; depth++ {
		_, hasPos := node["queue_position"]
		_, hasRetry := node["retry_after"]
		if hasPos || hasRetry {
			if n, ok := queueNumber(node["queue_position"]); ok {
				w.Position = int(n)
			}
			if n, ok := queueNumber(node["retry_after"]); ok {
				w.RetryAfter = time.Duration(n * float64(time.Second))
			}
			return w
		}
		node, _ = node["data"].(map[string]any)
	}
	return w
}

// queueNumber 取非负数：数字，或者纯数字的字符串（官方按 JS 的乘法取值，两样都认）。
func queueNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, n >= 0
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil && f >= 0
	}
	return 0, false
}

// QueuePollInterval 下一次查排队状态前等多久，按排位（官方 getPollingInterval）：前 5 名 2 秒、前 20 名
// 5 秒、前 50 名 10 秒、再往后 15 秒。position < 0 表示不知道，按 50 算。
func QueuePollInterval(position int) time.Duration {
	if position < 0 {
		position = 50
	}
	switch {
	case position <= 5:
		return 2 * time.Second
	case position <= 20:
		return 5 * time.Second
	case position <= 50:
		return 10 * time.Second
	}
	return 15 * time.Second
}

// QueueStatus 一次排队查询的结果。
type QueueStatus struct {
	// Status waiting、ready、expired；回 code 不是 0、没有 data 时为 expired，上游给了别的值就原样放着。
	Status   string
	Position int    // 排位；-1 = 没给
	Message  string // 上游的说明（过期时有）
}

// CapacityQueueStatus 查 a 在 model 上、meta 那一轮的排队状态。传输出错、HTTP 非 2xx 返回 error
// （官方照排位间隔再查）。
func (c *Client) CapacityQueueStatus(ctx context.Context, a *auth.Auth, model string, meta ChatMeta) (QueueStatus, error) {
	raw, err := c.queueCall(ctx, a, queueStatusPath, model, meta, true)
	if err != nil {
		return QueueStatus{}, err
	}
	var env struct {
		Code *int   `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			Status        string   `json:"status"`
			QueuePosition *float64 `json:"queue_position"`
			Message       string   `json:"message"`
		} `json:"data"`
	}
	// 回应不是 JSON 和没有 data 一样算过期（官方读不到 code 0 就当过期）。
	if json.Unmarshal(raw, &env) != nil || env.Code == nil || *env.Code != 0 || env.Data == nil {
		msg := env.Msg
		if msg == "" {
			msg = "Unknown error"
		}
		return QueueStatus{Status: "expired", Position: -1, Message: msg}, nil
	}
	st := QueueStatus{Status: env.Data.Status, Position: -1, Message: env.Data.Message}
	if p := env.Data.QueuePosition; p != nil && *p >= 0 {
		st.Position = int(*p)
	}
	return st, nil
}

// CancelCapacityQueue 取消 a 在 model 上、meta 那一轮的排队。
func (c *Client) CancelCapacityQueue(ctx context.Context, a *auth.Auth, model string, meta ChatMeta) error {
	_, err := c.queueCall(ctx, a, queueCancelPath, model, meta, false)
	return err
}

// queueCall 发一次查询或取消，返回 2xx 的回应体。withConv 为 true 时带 X-Conversation-ID（查询带、取消不带，
// 同官方）。
func (c *Client) queueCall(ctx context.Context, a *auth.Auth, path, model string, meta ChatMeta, withConv bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, queueRequestTimeout)
	defer cancel()
	body, _ := json.Marshal(struct {
		Model string `json:"model"`
	}{model})
	req, err := newAccountRequest(ctx, a, http.MethodPost, c.chatBase(a)+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", axiosUserAgent) // 主进程 axios 默认实例的 UA
	req.Header.Set("Accept-Encoding", acceptEncodingAxios)
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	req.Header.Set("X-Conversation-Request-ID", meta.ConversationRequestID)
	if withConv && meta.ConversationID != "" {
		req.Header.Set("X-Conversation-ID", meta.ConversationID)
	}
	resp, err := doDecoded(c.HTTP, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, &Error{Kind: Classify(resp.StatusCode, string(raw)), Status: resp.StatusCode, Msg: truncate(string(raw), 200)}
	}
	return raw, nil
}
