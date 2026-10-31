// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

// capqueue_test.go 容量排队（6020）：6020 回应里的排位与 retry_after、查询间隔、查询和取消的请求形状
// （照官方桌面端 5.9.3 主进程的 queue:getStatus、queue:cancel），以及查询回应怎么归成三种状态。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

func TestParseQueueWaiting(t *testing.T) {
	cases := []struct {
		body string
		want QueueWait
	}{
		{`{"code":6020,"queue_position":3,"retry_after":2}`, QueueWait{3, 2 * time.Second}},
		{`{"code":6020,"msg":"x","data":{"queue_position":12,"retry_after":"1.5"}}`, QueueWait{12, 1500 * time.Millisecond}},
		{`{"data":{"code":6020,"data":{"queue_position":0,"retry_after":0}}}`, QueueWait{0, 0}},
		// 只给了一个字段：另一个照默认。
		{`{"code":6020,"data":{"retry_after":8}}`, QueueWait{-1, 8 * time.Second}},
		{`{"code":6020,"data":{"queue_position":7}}`, QueueWait{7, 5 * time.Second}},
		// 负数、不是数字：当没给。
		{`{"code":6020,"queue_position":-1,"retry_after":"soon"}`, QueueWait{-1, 5 * time.Second}},
		{`{"code":6020,"msg":"queue.waiting"}`, QueueWait{-1, 5 * time.Second}},
		{`not json`, QueueWait{-1, 5 * time.Second}},
	}
	for _, c := range cases {
		if got := ParseQueueWaiting(c.body); got != c.want {
			t.Errorf("ParseQueueWaiting(%s) = %+v，应为 %+v", c.body, got, c.want)
		}
	}
}

func TestQueuePollInterval(t *testing.T) {
	for pos, want := range map[int]time.Duration{
		-1: 10 * time.Second, 0: 2 * time.Second, 5: 2 * time.Second, 6: 5 * time.Second, 20: 5 * time.Second,
		21: 10 * time.Second, 50: 10 * time.Second, 51: 15 * time.Second, 500: 15 * time.Second,
	} {
		if got := QueuePollInterval(pos); got != want {
			t.Errorf("QueuePollInterval(%d) = %v，应为 %v", pos, got, want)
		}
	}
}

// queueTestClient 查询、取消都打到 fn 的测试 Client；国际版入口单独给一个地址，看得出按账号的域选入口。
func queueTestClient(fn rtFunc) *Client {
	c := testClient(fn)
	c.ChatBaseGlobal = "https://global.example"
	c.GlobalEnabled = true
	return c
}

var queueMeta = ChatMeta{ConversationID: "conv-1", ConversationRequestID: "0123456789abcdef0123456789abcdef"}

// TestCapacityQueueRequestShape 查询和取消的地址、请求体与请求头：只有 axios 默认实例的头加上 Authorization、
// 会话请求 id，查询另带会话 id；官方请求框架的那些头一个都不带。
func TestCapacityQueueRequestShape(t *testing.T) {
	var got []*http.Request
	var bodies []string
	c := queueTestClient(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		got, bodies = append(got, r), append(bodies, string(b))
		if r.URL.Path == queueStatusPath {
			return jsonResp(200, `{"code":0,"data":{"status":"waiting","queue_position":4}}`), nil
		}
		return jsonResp(200, `{"code":0}`), nil
	})
	a := &auth.Auth{UID: "u1", AccessToken: "at-1", Domain: "www.workbuddy.cn"}
	st, err := c.CapacityQueueStatus(context.Background(), a, "glm-5.2", queueMeta)
	if err != nil || st != (QueueStatus{Status: "waiting", Position: 4}) {
		t.Fatalf("查询结果 = %+v, %v", st, err)
	}
	if err := c.CancelCapacityQueue(context.Background(), a, "glm-5.2", queueMeta); err != nil {
		t.Fatalf("取消: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("请求数 = %d，应为 2", len(got))
	}
	base := http.Header{
		"Accept":                    {"application/json, text/plain, */*"},
		"Content-Type":              {"application/json"},
		"User-Agent":                {"axios/1.16.1"},
		"Accept-Encoding":           {"gzip, compress, deflate, br"},
		"Authorization":             {"Bearer at-1"},
		"X-Conversation-Request-Id": {queueMeta.ConversationRequestID},
	}
	status := base.Clone()
	status.Set("X-Conversation-ID", "conv-1")
	for i, want := range []struct {
		url    string
		header http.Header
	}{
		{"https://chat.example/v2/chat/queue/status", status},
		{"https://chat.example/v2/chat/queue/cancel", base},
	} {
		r := got[i]
		if r.Method != http.MethodPost || r.URL.String() != want.url {
			t.Errorf("第 %d 个请求 = %s %s，应为 POST %s", i+1, r.Method, r.URL, want.url)
		}
		if !reflect.DeepEqual(r.Header, want.header) {
			t.Errorf("第 %d 个请求头 = %v\n应为 %v", i+1, r.Header, want.header)
		}
		if bodies[i] != `{"model":"glm-5.2"}` {
			t.Errorf("第 %d 个请求体 = %s", i+1, bodies[i])
		}
	}

	// 国际版号发到国际版入口；没有会话 id 时查询也不带 X-Conversation-ID。
	got = nil
	g := &auth.Auth{UID: "g1", AccessToken: "at-g", Domain: "www.workbuddy.ai"}
	if _, err := c.CapacityQueueStatus(context.Background(), g, "m", ChatMeta{ConversationRequestID: "rid"}); err != nil {
		t.Fatal(err)
	}
	if r := got[0]; r.URL.String() != "https://global.example/v2/chat/queue/status" || r.Header.Get("X-Conversation-ID") != "" {
		t.Errorf("国际版查询 = %s，X-Conversation-ID=%q", r.URL, r.Header.Get("X-Conversation-ID"))
	}
}

// TestCapacityQueueStatusResults 查询回应：code 0 带 data 看 data.status；code 不是 0、没有 data、
// 不是 JSON 都算过期；HTTP 出错、传输出错返回 error（调用方照排位间隔再查）。
func TestCapacityQueueStatusResults(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    QueueStatus
		wantErr bool
	}{
		{"到号", 200, `{"code":0,"data":{"status":"ready"}}`, QueueStatus{Status: "ready", Position: -1}, false},
		{"过期带说明", 200, `{"code":0,"data":{"status":"expired","message":"超时"}}`, QueueStatus{Status: "expired", Position: -1, Message: "超时"}, false},
		{"没见过的状态原样放着", 200, `{"code":0,"data":{"status":"paused","queue_position":2}}`, QueueStatus{Status: "paused", Position: 2}, false},
		{"code 不是 0", 200, `{"code":1001,"msg":"queue not found"}`, QueueStatus{Status: "expired", Position: -1, Message: "queue not found"}, false},
		{"没有 data", 200, `{"code":0}`, QueueStatus{Status: "expired", Position: -1, Message: "Unknown error"}, false},
		{"没有 code", 200, `{"data":{"status":"ready"}}`, QueueStatus{Status: "expired", Position: -1, Message: "Unknown error"}, false},
		{"不是 JSON", 200, `<html>`, QueueStatus{Status: "expired", Position: -1, Message: "Unknown error"}, false},
		{"HTTP 出错", 502, `bad gateway`, QueueStatus{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := queueTestClient(func(*http.Request) (*http.Response, error) { return jsonResp(tc.status, tc.body), nil })
			got, err := c.CapacityQueueStatus(context.Background(), &auth.Auth{AccessToken: "at"}, "m", queueMeta)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("= %+v, err=%v；应为 %+v, 出错=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
	// 传输出错、读回应体出错、地址不对。
	boom := errors.New("boom")
	c := queueTestClient(func(*http.Request) (*http.Response, error) { return nil, boom })
	if _, err := c.CapacityQueueStatus(context.Background(), &auth.Auth{}, "m", queueMeta); !errors.Is(err, boom) {
		t.Errorf("传输出错应原样返回，err=%v", err)
	}
	c = queueTestClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(errReader{})}, nil
	})
	if err := c.CancelCapacityQueue(context.Background(), &auth.Auth{}, "m", queueMeta); err == nil {
		t.Error("读回应体出错应返回")
	}
	c.ChatBaseCN = "://bad"
	if _, err := c.CapacityQueueStatus(context.Background(), &auth.Auth{}, "m", queueMeta); err == nil {
		t.Error("入口地址不对应报错")
	}
}
