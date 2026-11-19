// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// 默认预算和恢复次数来自官方 CLI 1.1.89；客户端取消或更短的请求期限优先。
const qoderQueueMaxWait = time.Hour
const qoderQueueMaxRecoveries = 10

type qoderQueueStatus struct {
	IsQueued          *bool    `json:"isQueued"`
	ServiceAvailable  *bool    `json:"serviceAvailable"`
	ModelKey          string   `json:"modelKey"`
	QueueType         string   `json:"queueType"`
	RetryAfterSeconds *float64 `json:"retryAfterSeconds"`
}

type qoderQueuedError struct {
	status *qoderQueueStatus
}

func (e *qoderQueuedError) Error() string { return e.Unwrap().Error() }
func (e *qoderQueuedError) Unwrap() error {
	return &Error{Status: 503, Code: "10605", Retry: true, Message: "Qoder 模型请求正在排队（10605），请稍后重试"}
}

func decodeQoderQueue(root map[string]any) (*qoderQueueStatus, error) {
	var find func(map[string]any, int) map[string]any
	find = func(current map[string]any, depth int) map[string]any {
		if depth > 8 {
			return nil
		}
		if current["isQueued"] != nil || current["serviceAvailable"] != nil {
			return current
		}
		for _, field := range []string{"data", "result", "message", "body"} {
			next := object(current[field])
			if encoded, ok := current[field].(string); ok {
				// message 也可能是普通文本；只展开可解析的 JSON 包络。
				if json.Unmarshal([]byte(encoded), &next) != nil {
					continue
				}
			}
			if next != nil {
				if found := find(next, depth+1); found != nil {
					return found
				}
			}
		}
		return nil
	}
	record := find(root, 0)
	if record == nil {
		return nil, nil
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("Qoder 排队信息编码: %w", err)
	}
	var status qoderQueueStatus
	if err = json.Unmarshal(raw, &status); err != nil {
		return nil, fmt.Errorf("Qoder 排队信息字段: %w", err)
	}
	if n := status.RetryAfterSeconds; n != nil && (*n < 0 || math.IsNaN(*n) || math.IsInf(*n, 0)) {
		return nil, errors.New("Qoder 排队重试秒数必须为有限非负数")
	}
	return &status, nil
}

func (s qoderQueueStatus) ready() bool {
	return s.IsQueued != nil && !*s.IsQueued && (s.ServiceAvailable == nil || *s.ServiceAvailable)
}

func (s qoderQueueStatus) delay() time.Duration {
	if n := s.RetryAfterSeconds; n != nil && *n > 0 {
		return time.Duration(math.Max(500, math.Min(30000, *n*1000))) * time.Millisecond
	}
	return 30 * time.Second
}

func (q *Qoder) queueStatus(ctx context.Context, target string, c Credential, body map[string]any, status qoderQueueStatus) (*qoderQueueStatus, error) {
	query := url.Values{"requestSetId": {text(body["request_set_id"])}, "modelKey": {status.ModelKey}}
	if status.QueueType != "" {
		query.Set("queueType", status.QueueType)
	}
	headers := http.Header{"X-Request-ID": {text(body["request_id"])}, "X-Session-ID": {text(body["session_id"])}}
	resp, err := q.signedHeaders(ctx, target, c, http.MethodGet, "/algo/api/v2/service/ask/queue/status?"+query.Encode(), nil, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var root map[string]any
	if err = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&root); err != nil {
		return nil, fmt.Errorf("Qoder 查询排队状态: %w", err)
	}
	if err = qoderStatusError(root); err != nil {
		return nil, err
	}
	next, err := decodeQoderQueue(root)
	if err == nil && next == nil {
		err = errors.New("Qoder 查询排队状态缺少 isQueued 和 serviceAvailable")
	}
	return next, err
}

func (q *Qoder) waitQueue(ctx context.Context, target string, c Credential, body map[string]any, status qoderQueueStatus) error {
	failures := 0
	for {
		pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		next, err := q.queueStatus(pollCtx, target, c, body, status)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if upstream.IsRouteError(err) {
			return fmt.Errorf("Qoder 轮询模型队列: %w", err)
		}
		if err != nil {
			var upstream *Error
			failures++
			if errors.As(err, &upstream) && upstream.Status == http.StatusNotFound || failures >= 3 {
				return fmt.Errorf("Qoder 轮询模型队列: %w", err)
			}
			log.Printf("[providers] Qoder 队列查询失败 target=%s model=%s attempt=%d: %v", target, status.ModelKey, failures, err)
		} else {
			failures = 0
			if next.ModelKey == "" {
				next.ModelKey = status.ModelKey
			}
			status = *next
		}
		ready := err == nil && status.ready()
		if ready && (status.RetryAfterSeconds == nil || *status.RetryAfterSeconds == 0) {
			return nil
		}
		timer := time.NewTimer(status.delay())
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if ready {
			return nil
		}
	}
}

func (q *Qoder) finishQueue(ctx context.Context, target string, c Credential, body map[string]any, modelKey string, started time.Time) error {
	inner, err := json.Marshal(map[string]any{"model_key": modelKey, "request_set_id": body["request_set_id"], "user_id": c.UID, "time_consumed": time.Since(started).Milliseconds()})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	resp, err := q.signedHeaders(ctx, target, c, http.MethodPost, "/algo/api/v2/service/ask/finish?Encode=1", map[string]any{"payload": string(inner), "encodeVersion": "1"}, http.Header{"X-Request-ID": {text(body["request_set_id"])}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil || len(raw) == 0 {
		return err
	}
	var root map[string]any
	if err = json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("Qoder 释放队列响应: %w", err)
	}
	return qoderStatusError(root)
}

type dispatchCheckKey struct{}

func (q *Qoder) chatQueued(ctx context.Context, target string, c Credential, model Model, body map[string]any, emit func(map[string]any) error) (chatErr error) {
	started := time.Now()
	queued, emitted := false, false
	queueModel := model.ID
	defer func() {
		if queued {
			if err := q.finishQueue(ctx, target, c, body, queueModel, started); err != nil {
				log.Printf("[providers] Qoder 释放模型队列失败 target=%s model=%s: %v", target, queueModel, err)
			}
			if !emitted && (errors.Is(chatErr, context.Canceled) || errors.Is(chatErr, context.DeadlineExceeded)) {
				chatErr = fmt.Errorf("Qoder 请求进入模型队列（10605）后，在输出前中断: %w", chatErr)
			}
			if chatErr != nil {
				chatErr = &dispatchedError{chatErr}
			}
		}
	}()
	budget := qoderQueueMaxWait
	for recoveries := 0; ; recoveries++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if check, ok := ctx.Value(dispatchCheckKey{}).(func() error); ok {
			if err := check(); err != nil {
				return err
			}
		}
		err := q.chatAttempt(ctx, target, c, model, body, func(frame map[string]any) error {
			emitted = true
			return emit(frame)
		})
		var queueErr *qoderQueuedError
		if !errors.As(err, &queueErr) {
			// HTTP 错误正文也可能直接提供业务码；没有队列详情时按原模型查询。
			var upstream *Error
			if errors.As(err, &upstream) && upstream.Code == "10605" {
				queueErr = &qoderQueuedError{}
			}
		}
		if queueErr == nil || emitted {
			return err
		}
		queued = true
		if recoveries >= qoderQueueMaxRecoveries {
			return &Error{Status: 503, Code: "10605", Retry: true, Message: "Qoder 模型排队恢复次数已达上限，请稍后重试"}
		}
		status := qoderQueueStatus{ModelKey: model.ID}
		if queueErr.status != nil {
			status = *queueErr.status
			if status.ModelKey == "" {
				status.ModelKey = model.ID
			}
		}
		queueModel = status.ModelKey
		waitCtx, cancel := context.WithTimeout(ctx, budget)
		waitStarted := time.Now()
		waitErr := q.waitQueue(waitCtx, target, c, body, status)
		budget -= time.Since(waitStarted)
		cancel()
		if waitErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if upstream.IsRouteError(waitErr) {
				return waitErr
			}
			return fmt.Errorf("Qoder 等待模型队列: %v: %w", waitErr, queueErr)
		}
		body["request_id"] = uuid.NewString()
	}
}
