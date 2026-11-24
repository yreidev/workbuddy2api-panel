// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

type Error struct {
	Status  int
	Code    string
	Retry   bool
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("上游请求失败：HTTP %d，代码 %s", e.Status, e.Code)
}

// dispatchedError 保留原错误，同时标记上游已接受操作，禁止换号重放。
type dispatchedError struct{ error }

func (e *dispatchedError) Unwrap() error { return e.error }

func routeErrorAfterDispatch(err error) error {
	if upstream.IsRouteError(err) {
		return &dispatchedError{err}
	}
	return err
}

func canRetryRouteError(err error) bool {
	var dispatched *dispatchedError
	return upstream.IsRouteError(err) && !errors.As(err, &dispatched)
}

func request(ctx context.Context, client *http.Client, method, endpoint string, body []byte, headers http.Header) (*http.Response, error) {
	req, err := upstream.NewBufferedRequest(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	if len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	// 平台使用自定义令牌头和签名正文，任何重定向均在原站终止。
	local := *client
	local.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// 默认线路的准入与已绑代理的可用性均可在排队期间变化，每次真正发送（包括重送和清理）前重检。
	if registry, ok := ctx.Value(unboundRouteKey{}).(*proxies.Registry); ok && registry.Settings().RequireBound {
		return nil, proxyUnavailable("proxy_required")
	}
	if bound, ok := ctx.Value(boundRouteKey{}).(boundRouteCheck); ok && bound.registry != nil {
		if _, _, usable, code := bound.registry.CheckProxy(bound.proxyID); !usable {
			return nil, proxyUnavailable(code)
		}
	}
	resp, err := local.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("平台请求 %s %s%s: %w", method, req.URL.Host, req.URL.Path, err)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		resp.Body.Close()
		return nil, &Error{Status: http.StatusBadGateway, Code: "upstream_redirect", Message: "平台端点返回了重定向"}
	}
	if !resp.Uncompressed {
		encoding := strings.TrimSpace(strings.Join(resp.Header.Values("Content-Encoding"), ","))
		var codings []string
		if encoding != "" && !strings.EqualFold(encoding, "identity") {
			codings = strings.Split(encoding, ",")
		}
		decode := true
		profile := upstream.ClientProfileFromContext(ctx)
		if profile == upstream.ProfileTraeCode {
			// TTNet 声明 gzip、deflate。它识别 Brotli 并拒绝未声明的 Brotli；
			// 遇到 Zstandard 等未知编码时，整条解码链以线上原始正文交付。
			for _, coding := range codings {
				switch strings.ToLower(strings.TrimSpace(coding)) {
				case "br":
					_ = resp.Body.Close()
					return nil, fmt.Errorf("平台响应 %s%s 使用了 Trae Code 未声明的压缩编码 %q", req.URL.Host, req.URL.Path, strings.TrimSpace(coding))
				case "gzip", "x-gzip", "deflate":
				default:
					decode = false
				}
			}
		}
		if decode && len(codings) != 0 {
			body := resp.Body
			// 编码按施加顺序列出，解压时从最外层开始。
			for i := len(codings) - 1; i >= 0; i-- {
				var ok bool
				body, ok = upstream.DecodeProfileBody(profile, codings[i], body)
				if !ok {
					_ = resp.Body.Close()
					return nil, fmt.Errorf("平台响应 %s%s 使用未支持的压缩编码 %q", req.URL.Host, req.URL.Path, strings.TrimSpace(codings[i]))
				}
			}
			resp.Body = body
			resp.Header.Del("Content-Encoding")
			resp.Header.Del("Content-Length")
			resp.ContentLength = -1
			resp.Uncompressed = true
		}
	}
	resp.Body = newIdleBody(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return nil, fmt.Errorf("读取平台错误响应: %w", readErr)
		}
		var e map[string]any
		_ = json.Unmarshal(raw, &e)
		code := safeCode(e["code"])
		if code == "" {
			code = http.StatusText(resp.StatusCode)
		}
		return nil, &Error{Status: resp.StatusCode, Code: code, Retry: resp.StatusCode == 429 || resp.StatusCode >= 500}
	}
	return resp, nil
}
func safeCode(v any) string {
	s := fmt.Sprint(v)
	if v == nil {
		return ""
	}
	if len(s) > 48 {
		return "upstream_error"
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r == '-':
		default:
			return "upstream_error"
		}
	}
	return s
}
func jsonRequest(ctx context.Context, client *http.Client, method, endpoint string, body any, headers http.Header) (map[string]any, error) {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	resp, err := request(ctx, client, method, endpoint, raw, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// 读到响应结束才能收到解压器校验尾部时返回的错误。
	const maxJSONBody = 8 << 20
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody+1))
	if err != nil {
		return nil, fmt.Errorf("读取平台 JSON 响应: %w", err)
	}
	if len(responseBody) > maxJSONBody {
		return nil, errors.New("平台 JSON 响应超过 8 MiB")
	}
	var obj map[string]any
	if err := json.Unmarshal(responseBody, &obj); err != nil {
		return nil, fmt.Errorf("解析平台 JSON 响应: %w", err)
	}
	if code, ok := obj["code"]; ok && code != nil && fmt.Sprint(code) != "0" && fmt.Sprint(code) != "200" {
		return nil, &Error{Status: 502, Code: safeCode(code)}
	}
	return obj, nil
}
func number(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		x, _ := n.Int64()
		return x
	case string:
		x, _ := strconv.ParseInt(n, 10, 64)
		return x
	}
	return 0
}
func text(v any) string           { s, _ := v.(string); return s }
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func seconds(v any) int64 {
	n := number(v)
	if n > 1e12 {
		return n / 1000
	}
	if n > 0 {
		return n
	}
	if s, ok := v.(string); ok {
		if t, e := time.Parse(time.RFC3339, s); e == nil {
			return t.Unix()
		}
	}
	return 0
}
func jsonHeaders(token string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	return h
}

// errSSEDone 由适配器识别平台包络内的完成标记，扫描器收到后立即结束。
var errSSEDone = errors.New("平台 SSE 已完成")

// scanSSE 按事件边界读取，支持 CRLF、多行 data、最后一帧无空行和完整性检查。
func scanSSE(ctx context.Context, r io.Reader, fn func(string, map[string]any) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	event := ""
	var lines []string
	done := false
	dispatch := func() error {
		if len(lines) == 0 {
			if event == "done" {
				done = true
				return fn("done", map[string]any{})
			}
			return nil
		}
		payload := strings.Join(lines, "\n")
		lines = nil
		// Trae 的进度通知可能是 JSON 字符串，不属于正文或完成事件。
		if event == "progress_notice" && strings.HasPrefix(strings.TrimSpace(payload), `"`) {
			var notice string
			if err := json.Unmarshal([]byte(payload), &notice); err != nil {
				return fmt.Errorf("解析平台 SSE 事件 %s: %w", event, err)
			}
			event = ""
			return nil
		}
		if payload == "[DONE]" {
			done = true
			return fn("done", map[string]any{})
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			return fmt.Errorf("解析平台 SSE 事件 %s: %w", event, err)
		}
		if event == "" {
			event = text(obj["event"])
			if event == "" {
				event = text(obj["event_type"])
			}
		}
		if event == "done" {
			done = true
		}
		err := fn(event, obj)
		event = ""
		if errors.Is(err, errSSEDone) {
			done = true
			return nil
		}
		return err
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return err
			}
			if done {
				return nil
			}
			event = ""
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取平台 SSE: %w", err)
	}
	if err := dispatch(); err != nil {
		return err
	}
	if !done {
		return errors.New("平台 SSE 提前结束，缺少完成事件")
	}
	return nil
}

func chunk(model string, delta map[string]any, finish any, usage any) map[string]any {
	out := map[string]any{"object": "chat.completion.chunk", "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	if usage != nil {
		out["usage"] = usage
	}
	return out
}

func decodeResponse(resp *http.Response) (map[string]any, error) {
	defer resp.Body.Close()
	var obj map[string]any
	err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&obj)
	if err != nil {
		return nil, fmt.Errorf("解析平台响应: %w", err)
	}
	return obj, nil
}

// idleBody 在流无数据时关闭连接；收到数据会重新计时，客户端取消仍由请求上下文终止。
type idleBody struct {
	io.ReadCloser
	mu     sync.Mutex
	timer  *time.Timer
	closed bool
}

func newIdleBody(body io.ReadCloser) *idleBody {
	b := &idleBody{ReadCloser: body}
	b.mu.Lock()
	b.timer = time.AfterFunc(90*time.Second, func() { _ = b.Close() })
	b.mu.Unlock()
	return b
}
func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	if !b.closed && n > 0 {
		b.timer.Reset(90 * time.Second)
	}
	b.mu.Unlock()
	return n, err
}
func (b *idleBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	b.timer.Stop()
	return b.ReadCloser.Close()
}
