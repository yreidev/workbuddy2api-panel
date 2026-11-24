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
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/providers/cosy"
	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
)

type qoderQueueTransport func(*http.Request) (*http.Response, error)

func (f qoderQueueTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func writeQoderQueued(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	queue, _ := json.Marshal(map[string]any{"isQueued": true, "serviceAvailable": false, "modelKey": "backend-model", "queueType": "slow", "retryAfterSeconds": 30})
	inner, _ := json.Marshal(map[string]any{"code": "10605", "subCode": "RESOURCE_LIMIT", "message": string(queue)})
	body, _ := json.Marshal(map[string]any{"code": "403", "message": string(inner)})
	outer, _ := json.Marshal(map[string]any{"statusCodeValue": 403, "body": string(body)})
	fmt.Fprintf(w, "data: %s\n\n", outer)
}

func readQoderQueueBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error(err)
		return nil
	}
	decoded, err := cosy.Decode(string(raw))
	if err != nil {
		t.Error(err)
		return nil
	}
	var body map[string]any
	if err = json.Unmarshal(decoded, &body); err != nil {
		t.Error(err)
	}
	return body
}

func TestQoderQueueResubmitsSameRequestSetAndReleases(t *testing.T) {
	var chats, polls, finishes atomic.Int32
	var original map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cosy-Machineid") != "fixture-machine" {
			t.Error("排队与重发没有保持账号设备身份")
		}
		switch r.URL.Path {
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			body := readQoderQueueBody(t, r)
			if r.Header.Get("X-Request-ID") != body["request_id"] || r.Header.Get("X-RequestSet-ID") != body["request_set_id"] || r.Header.Get("X-Session-ID") != "fixture-session" {
				t.Error("推理请求身份头与正文不一致")
			}
			if chats.Add(1) == 1 {
				original = body
				writeQoderQueued(t, w)
				return
			}
			if body["request_id"] == original["request_id"] {
				t.Error("重发没有更新 request_id")
			}
			delete(body, "request_id")
			delete(original, "request_id")
			if !reflect.DeepEqual(body, original) {
				t.Error("排队恢复重建了原会话、请求集或业务正文")
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case "/algo/api/v2/service/ask/queue/status":
			polls.Add(1)
			if r.Method != http.MethodGet || r.URL.Query().Get("requestSetId") != original["request_set_id"] || r.URL.Query().Get("modelKey") != "backend-model" || r.URL.Query().Get("queueType") != "slow" || r.Header.Get("X-Request-ID") != original["request_id"] || r.Header.Get("X-Session-ID") != "fixture-session" {
				t.Error("队列查询没有沿用原请求身份或官方模型键")
			}
			fmt.Fprint(w, `{"data":{"isQueued":false,"serviceAvailable":true,"retryAfterSeconds":0}}`)
		case "/algo/api/v2/service/ask/finish":
			finishes.Add(1)
			body := readQoderQueueBody(t, r)
			var payload map[string]any
			if json.Unmarshal([]byte(text(body["payload"])), &payload) != nil || body["encodeVersion"] != "1" || r.Method != http.MethodPost || r.URL.Query().Get("Encode") != "1" {
				t.Error("队列释放没有使用官方编码形状")
			}
			if payload["request_set_id"] != original["request_set_id"] || payload["model_key"] != "backend-model" || payload["user_id"] != "fixture-user" || r.Header.Get("X-Request-ID") != original["request_set_id"] {
				t.Error("队列释放身份或模型错误")
			}
		default:
			t.Error("意外请求路径", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	q := &Qoder{HTTP: server.Client(), Base: server.URL}
	credential := Credential{UID: "fixture-user", UserType: "personal", AccessToken: "fixture-token", MachineID: "fixture-machine"}
	before := credential
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	frames := 0
	err := q.Chat(ctx, "qoder:cn", credential, Model{ID: "auto"}, []byte(plainChat), "fixture-session", func(map[string]any) error { frames++; return nil })
	if err != nil || chats.Load() != 2 || polls.Load() != 1 || finishes.Load() != 1 || frames != 1 || credential != before {
		t.Fatalf("排队恢复不完整: err=%v chats=%d polls=%d finish=%d frames=%d", err, chats.Load(), polls.Load(), finishes.Load(), frames)
	}
}

func TestQoderQueueCancellationReleasesWithOriginalRoute(t *testing.T) {
	type routeKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), routeKey{}, "fixture-route"))
	defer cancel()
	var chats, finishes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			chats.Add(1)
			writeQoderQueued(t, w)
		case "/algo/api/v2/service/ask/queue/status":
			fmt.Fprint(w, `{"isQueued":true,"serviceAvailable":false,"retryAfterSeconds":30}`)
			w.(http.Flusher).Flush()
			cancel()
		case "/algo/api/v2/service/ask/finish":
			finishes.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := server.Client()
	base := client.Transport
	client.Transport = qoderQueueTransport(func(r *http.Request) (*http.Response, error) {
		if r.Context().Value(routeKey{}) != "fixture-route" {
			t.Error("队列请求丢失原线路上下文")
		}
		if strings.HasSuffix(r.URL.Path, "/finish") && r.Context().Err() != nil {
			t.Error("取消后沿用了已取消的释放上下文")
		}
		return base.RoundTrip(r)
	})
	q := &Qoder{HTTP: client, Base: server.URL}
	err := q.Chat(ctx, "qoder:cn", Credential{UID: "fixture", UserType: "personal"}, Model{ID: "auto"}, []byte(plainChat), "session", discardFrame)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "10605") || chats.Load() != 1 || finishes.Load() != 1 {
		t.Fatalf("取消后未退出或释放队列: err=%v chats=%d finish=%d", err, chats.Load(), finishes.Load())
	}
}

func TestQoderQueueNeverReplaysAfterOutput(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		writeQoderQueued(t, w)
	}))
	defer server.Close()
	q := &Qoder{HTTP: server.Client(), Base: server.URL}
	frames := 0
	err := q.Chat(context.Background(), "qoder:cn", Credential{UID: "fixture", UserType: "personal"}, Model{ID: "auto"}, []byte(plainChat), "session", func(map[string]any) error { frames++; return nil })
	var upstream *Error
	if !errors.As(err, &upstream) || upstream.Code != "10605" || requests.Load() != 1 || frames != 1 {
		t.Fatalf("已输出后发生排队或重放: err=%v requests=%d frames=%d", err, requests.Load(), frames)
	}
}

func TestQoderQueueRecoveryIsBounded(t *testing.T) {
	var chats, polls, finishes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			chats.Add(1)
			writeQoderQueued(t, w)
		case "/algo/api/v2/service/ask/queue/status":
			polls.Add(1)
			fmt.Fprint(w, `{"isQueued":false,"serviceAvailable":true,"retryAfterSeconds":0}`)
		case "/algo/api/v2/service/ask/finish":
			finishes.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	q := &Qoder{HTTP: server.Client(), Base: server.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := q.Chat(ctx, "qoder:cn", Credential{UID: "fixture", UserType: "personal"}, Model{ID: "auto"}, []byte(plainChat), "session", discardFrame)
	var upstream *Error
	if !errors.As(err, &upstream) || upstream.Code != "10605" || chats.Load() != 11 || polls.Load() != 10 || finishes.Load() != 1 {
		t.Fatalf("反复就绪和重排没有上限: err=%v chats=%d polls=%d finish=%d", err, chats.Load(), polls.Load(), finishes.Load())
	}
}

func TestQoderQueueStatusRejectsInvalidResponse(t *testing.T) {
	for _, input := range []string{`{}`, `{"data":{"isQueued":"false"}}`, `{"isQueued":false,"retryAfterSeconds":-1}`, `{"code":403,"message":"forbidden"}`} {
		t.Run(input, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, input) }))
			defer server.Close()
			q := &Qoder{HTTP: server.Client(), Base: server.URL}
			_, err := q.queueStatus(context.Background(), "qoder:cn", Credential{UID: "fixture", UserType: "personal"}, map[string]any{"request_set_id": "set", "request_id": "request", "session_id": "session"}, qoderQueueStatus{ModelKey: "auto"})
			if err == nil {
				t.Fatal("非法排队响应被视为就绪")
			}
		})
	}
}

func TestQoderQueueDeadlineStopsWaitingAndReleases(t *testing.T) {
	var finishes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			writeQoderQueued(t, w)
		case "/algo/api/v2/service/ask/queue/status":
			fmt.Fprint(w, `{"isQueued":true,"serviceAvailable":false,"retryAfterSeconds":30}`)
		case "/algo/api/v2/service/ask/finish":
			finishes.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	q := &Qoder{HTTP: server.Client(), Base: server.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := q.Chat(ctx, "qoder:cn", Credential{UID: "fixture", UserType: "personal"}, Model{ID: "auto"}, []byte(plainChat), "session", discardFrame)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "10605") || finishes.Load() != 1 {
		t.Fatalf("请求到期没有停止等待并释放: err=%v finish=%d", err, finishes.Load())
	}
}

func TestQoderHTTPQueuedRecoversWithoutQueueDetails(t *testing.T) {
	var chats atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			if chats.Add(1) == 1 {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"code":10605}`)
				return
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case "/algo/api/v2/service/ask/queue/status":
			if r.URL.Query().Get("modelKey") != "auto" {
				t.Error("没有队列详情时没有使用原模型")
			}
			fmt.Fprint(w, `{"isQueued":false,"retryAfterSeconds":0}`)
		case "/algo/api/v2/service/ask/finish":
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	q := &Qoder{HTTP: server.Client(), Base: server.URL}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Chat(ctx, "qoder:cn", Credential{UID: "fixture", UserType: "personal"}, Model{ID: "auto"}, []byte(plainChat), "session", discardFrame); err != nil || chats.Load() != 2 {
		t.Fatalf("HTTP 排队业务码没有恢复: err=%v chats=%d", err, chats.Load())
	}
}

func TestAcceptedQoderQueueNeverRotates(t *testing.T) {
	for _, tc := range []struct {
		status int
		revoke bool
	}{{404, false}, {200, false}, {200, true}} {
		t.Run(fmt.Sprintf("status=%d/revoke=%t", tc.status, tc.revoke), func(t *testing.T) {
			var polls, finishes, chats atomic.Int32
			var revoked atomic.Bool
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/algo/api/v2/model/list":
					fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
				case "/algo/api/v2/service/pro/sse/agent_chat_generation":
					chats.Add(1)
					writeQoderQueued(t, w)
				case "/algo/api/v2/service/ask/queue/status":
					polls.Add(1)
					revoked.Store(tc.revoke)
					w.WriteHeader(tc.status)
					fmt.Fprint(w, `{"isQueued":false,"serviceAvailable":true,"retryAfterSeconds":0}`)
				case "/algo/api/v2/service/ask/finish":
					finishes.Add(1)
				default:
					t.Errorf("意外接口 %s", r.URL.Path)
				}
			}))
			defer s.Close()
			m, _ := testManager(t, "")
			first := importFake(t, m, "qoder:cn", "a")
			importFake(t, m, "qoder:cn", "b")
			m.adapters["qoder"] = &Qoder{HTTP: s.Client(), Base: s.URL}
			result, err := m.Execute(t.Context(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat), MaxRotate: 2, Allow: func(string, []string) bool { return !revoked.Load() }}, discardFrame)
			var denied *Error
			if tc.revoke && (!errors.As(err, &denied) || denied.Status != 403 || chats.Load() != 1 || m.running[first].inFlight != 0) {
				t.Fatalf("排队撤权后仍派发或未释放槽位: err=%v chats=%d", err, chats.Load())
			}
			if err == nil || result.Started || result.Attempts != 1 || result.AccountID != first || finishes.Load() != 1 || polls.Load() == 0 || chats.Load() == 0 {
				t.Fatalf("排队后重放或未清理: %+v %v finish=%d", result, err, finishes.Load())
			}
		})
	}
}

func TestQueuedQoderRechecksProxyPolicy(t *testing.T) {
	m, _ := testManager(t, "")
	id := importFake(t, m, "qoder:cn", "fixture")
	reg, err := proxies.Open(filepath.Join(t.TempDir(), "proxies.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.SetProxies(reg)
	var calls, finished atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/algo/api/v2/model/list":
			fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			calls.Add(1)
			writeQoderQueued(t, w)
		case "/algo/api/v2/service/ask/queue/status":
			if _, err := reg.UpdateSettings(proxies.Settings{RequireBound: true}); err != nil {
				t.Error(err)
			}
			fmt.Fprint(w, `{"isQueued":false,"retryAfterSeconds":0}`)
		case "/algo/api/v2/service/ask/finish":
			finished.Add(1)
		default:
			t.Error("意外请求", r.URL.Path)
		}
	}))
	defer server.Close()
	m.adapters["qoder"] = &Qoder{HTTP: server.Client(), Base: server.URL}
	r, err := m.Execute(t.Context(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame)
	var e *Error
	if !errors.As(err, &e) || e.Code != "proxy_required" || calls.Load() != 1 || finished.Load() != 0 || r.Attempts != 1 || m.running[id].inFlight != 0 {
		t.Fatal("收紧代理策略后仍有默认线路请求", r, err)
	}
}

func TestQoderQueueRefreshesExpiringTokenAcrossPollAndFinish(t *testing.T) {
	// 1. 直接验证 Qoder.Chat 排队期间凭据快过期时通过 credentialRefreshKey 刷新，
	// 且后续轮询、重试对话与 defer finishQueue 全部使用刷新后的新凭据。
	var polls, chats, finishes, refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			if chats.Add(1) == 1 {
				writeQoderQueued(t, w)
				return
			}
			if r.Header.Get("Cosy-Machineid") != "refreshed-machine" {
				t.Errorf("排队恢复重试未使用刷新后凭据: %s", r.Header.Get("Cosy-Machineid"))
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case "/algo/api/v2/service/ask/queue/status":
			n := polls.Add(1)
			if r.Header.Get("Cosy-Machineid") != "refreshed-machine" {
				t.Errorf("第 %d 次排队轮询未使用刷新后凭据: %s", n, r.Header.Get("Cosy-Machineid"))
			}
			if n == 1 {
				fmt.Fprint(w, `{"isQueued":true,"serviceAvailable":false,"retryAfterSeconds":0.5}`)
				return
			}
			fmt.Fprint(w, `{"isQueued":false,"serviceAvailable":true,"retryAfterSeconds":0}`)
		case "/algo/api/v2/service/ask/finish":
			finishes.Add(1)
			if r.Header.Get("Cosy-Machineid") != "refreshed-machine" {
				t.Errorf("排队结束 finishQueue 未使用刷新后凭据: %s", r.Header.Get("Cosy-Machineid"))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	q := &Qoder{HTTP: server.Client(), Base: server.URL}
	ctx := context.WithValue(t.Context(), credentialRefreshKey{}, func(context.Context) (Credential, error) {
		refreshes.Add(1)
		return Credential{
			UID:         "fixture-user",
			UserType:    "personal",
			AccessToken: "refreshed-token",
			MachineID:   "refreshed-machine",
			ExpiresAt:   time.Now().Add(time.Hour).Unix(),
		}, nil
	})
	expiring := Credential{
		UID:         "fixture-user",
		UserType:    "personal",
		AccessToken: "old-token",
		MachineID:   "old-machine",
		ExpiresAt:   time.Now().Add(time.Minute).Unix(),
	}
	if err := q.Chat(ctx, "qoder:cn", expiring, Model{ID: "auto"}, []byte(plainChat), "fixture-session", discardFrame); err != nil {
		t.Fatalf("排队期刷新凭据后未正常完成: %v", err)
	}
	if refreshes.Load() != 1 || polls.Load() != 2 || chats.Load() != 2 || finishes.Load() != 1 {
		t.Fatalf("排队刷新与调用次数不符: refresh=%d poll=%d chat=%d finish=%d", refreshes.Load(), polls.Load(), chats.Load(), finishes.Load())
	}

	// 2. 端到端通过 Manager.Execute：首次进入对话前尚未到期（>5m），排队等待 1s 后进入 5m 刷新窗口，
	// 触发 deviceToken/refresh 更新并持久化新令牌，完成对话。
	var e2eRefreshes, e2eChats, e2ePolls, e2eFinishes atomic.Int32
	e2eSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/algo/api/v2/model/list":
			fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			if e2eChats.Add(1) == 1 {
				writeQoderQueued(t, w)
				return
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case "/algo/api/v2/service/ask/queue/status":
			if e2ePolls.Add(1) == 1 {
				fmt.Fprint(w, `{"isQueued":true,"serviceAvailable":false,"retryAfterSeconds":1.2}`)
				return
			}
			fmt.Fprint(w, `{"isQueued":false,"serviceAvailable":true,"retryAfterSeconds":0}`)
		case "/api/v1/deviceToken/refresh":
			e2eRefreshes.Add(1)
			fmt.Fprint(w, `{"device_token":"e2e-new-token","refresh_token":"e2e-new-rt","expires_in":3600}`)
		case "/api/v1/userinfo":
			fmt.Fprint(w, `{"user_id":"e2e-user"}`)
		case "/algo/api/v2/service/ask/finish":
			e2eFinishes.Add(1)
		default:
			t.Errorf("意外路径: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer e2eSrv.Close()

	m, _ := testManager(t, filepath.Join(t.TempDir(), "providers.json"))
	m.adapters["qoder"] = &Qoder{HTTP: e2eSrv.Client(), Base: e2eSrv.URL, OpenAPIBase: e2eSrv.URL}
	if _, err := m.Import(t.Context(), AccountInput{
		Target: "qoder:cn",
		Credential: Credential{
			UID:          "e2e-user",
			UserType:     "teams",
			AccessToken:  "e2e-old-token",
			RefreshToken: "e2e-old-rt",
			TokenKind:    "device",
			MachineID:    "e2e-machine",
			MachineToken: "e2e-mt",
			ExpiresAt:    time.Now().Add(5*time.Minute + time.Second).Unix(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Execute(t.Context(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame); err != nil {
		t.Fatalf("端到端排队续期失败: %v", err)
	}
	if e2eRefreshes.Load() != 1 || e2eChats.Load() != 2 || e2ePolls.Load() != 2 || e2eFinishes.Load() != 1 || m.data.Accounts[0].Credential.AccessToken != "e2e-new-token" {
		t.Fatalf("端到端排队续期状态异常: refresh=%d chat=%d poll=%d finish=%d token=%s", e2eRefreshes.Load(), e2eChats.Load(), e2ePolls.Load(), e2eFinishes.Load(), m.data.Accounts[0].Credential.AccessToken)
	}
}

func TestQoderQueueMidPollKeyRevocationAndBoundProxyDown(t *testing.T) {
	t.Run("轮询间隙撤权立刻403中止且不发第二次轮询", func(t *testing.T) {
		var chats, polls, finishes atomic.Int32
		var revoked atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/algo/api/v2/model/list":
				fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
			case "/algo/api/v2/service/pro/sse/agent_chat_generation":
				chats.Add(1)
				writeQoderQueued(t, w)
			case "/algo/api/v2/service/ask/queue/status":
				if polls.Add(1) == 1 {
					revoked.Store(true)
					fmt.Fprint(w, `{"isQueued":true,"serviceAvailable":false,"retryAfterSeconds":0.5}`)
					return
				}
				t.Error("撤权后仍发出了第二次排队轮询")
				fmt.Fprint(w, `{"isQueued":false,"serviceAvailable":true,"retryAfterSeconds":0}`)
			case "/algo/api/v2/service/ask/finish":
				finishes.Add(1)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		m, _ := testManager(t, "")
		id := importFake(t, m, "qoder:cn", "revoked")
		m.adapters["qoder"] = &Qoder{HTTP: srv.Client(), Base: srv.URL}
		_, err := m.Execute(t.Context(), Request{
			Target: "qoder:cn",
			Model:  "model",
			Body:   []byte(plainChat),
			Allow:  func(string, []string) bool { return !revoked.Load() },
		}, discardFrame)
		var e *Error
		if !errors.As(err, &e) || e.Status != 403 || e.Code != "target_not_allowed" {
			t.Fatalf("轮询间隙撤权未直接返回 403 target_not_allowed: %v", err)
		}
		if chats.Load() != 1 || polls.Load() != 1 || finishes.Load() != 1 || m.running[id].inFlight != 0 {
			t.Fatalf("轮询撤权后多发请求或未释放队列/槽位: chats=%d polls=%d finish=%d inFlight=%d", chats.Load(), polls.Load(), finishes.Load(), m.running[id].inFlight)
		}
	})

	t.Run("排队期间已绑代理变down后中止后续出站", func(t *testing.T) {
		reg, err := proxies.Open(filepath.Join(t.TempDir(), "proxies.json"))
		if err != nil {
			t.Fatal(err)
		}
		poolID, err := reg.CreatePool(proxies.PoolInput{Name: "测试池"})
		if err != nil {
			t.Fatal(err)
		}
		added, err := reg.Import("http://bound-queue.invalid:8888 绑定代理", "http", poolID, nil)
		if err != nil || len(added.Added) != 1 {
			t.Fatalf("导入代理失败: %+v %v", added, err)
		}
		proxyID := added.Added[0]

		var chats, polls, finishes atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/algo/api/v2/model/list":
				fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
			case "/algo/api/v2/service/pro/sse/agent_chat_generation":
				chats.Add(1)
				writeQoderQueued(t, w)
			case "/algo/api/v2/service/ask/queue/status":
				if polls.Add(1) == 1 {
					// 模拟排队期间该代理被 ReportProxy 标为 down。
					reg.ReportProxy(proxyID, errors.New("proxy connection reset"))
					fmt.Fprint(w, `{"isQueued":true,"serviceAvailable":false,"retryAfterSeconds":0.5}`)
					return
				}
				t.Error("绑定的代理已变 down 仍发出了第二次轮询")
				fmt.Fprint(w, `{"isQueued":false,"serviceAvailable":true,"retryAfterSeconds":0}`)
			case "/algo/api/v2/service/ask/finish":
				finishes.Add(1)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		m, _ := testManager(t, "")
		m.SetProxies(reg)
		m.adapters["qoder"] = &Qoder{HTTP: srv.Client(), Base: srv.URL}
		id, err := m.Import(t.Context(), AccountInput{
			Target:     "qoder:cn",
			ProxyID:    proxyID,
			Credential: Credential{UID: "bound-q", UserType: "teams", AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour).Unix()},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = m.Execute(t.Context(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame)
		var e *Error
		if !errors.As(err, &e) || e.Code != "proxy_not_available" {
			t.Fatalf("已绑代理变 down 未阻断返回 proxy_not_available: %v", err)
		}
		if chats.Load() != 1 || polls.Load() != 1 || finishes.Load() != 0 || m.running[id].inFlight != 0 {
			t.Fatalf("已绑代理 down 后仍有后续出站或未释放槽位: chats=%d polls=%d finish=%d inFlight=%d", chats.Load(), polls.Load(), finishes.Load(), m.running[id].inFlight)
		}
	})
}
