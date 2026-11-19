// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/apikeys"
	"github.com/yreidev/workbuddy2api-panel/internal/providers"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
)

// 先交付已有的 SSE 事件，再模拟客户端取消，避免依赖网络调度或 sleep。
type providerCancelBody struct {
	io.Reader
	cancel context.CancelFunc
}

func (b *providerCancelBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		b.cancel()
		return n, context.Canceled
	}
	return n, err
}

func (*providerCancelBody) Close() error { return nil }

type providerRejectWriter struct {
	*httptest.ResponseRecorder
	match string
}

func (w providerRejectWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.match) {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(p)
}

func TestProviderCancellationAccounting(t *testing.T) {
	const content = "data: {\"code\":200,\"body\":{\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}}\n\n"
	const tail = "data: {\"code\":200,\"body\":{\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}}\n\ndata: [DONE]\n\n"
	for _, protocol := range []struct{ path, done string }{
		{"/v1/chat/completions", "data: [DONE]"},
		{"/v1/messages", "event: message_stop"},
		{"/v1/responses", "event: response.completed"},
	} {
		for _, stream := range []bool{false, true} {
			for _, stage := range []string{"输出前取消", "输出后取消", "排队取消", "正文写失败", "收尾写失败"} {
				if !stream && strings.HasSuffix(stage, "写失败") {
					continue
				}
				t.Run(fmt.Sprintf("%s/%t/%s", protocol.path, stream, stage), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					var chats atomic.Int32
					client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
						var body io.ReadCloser
						switch r.URL.Path {
						case "/algo/api/v3/service/region/endpoints":
							body = io.NopCloser(strings.NewReader(`{"inferNodes":["https://api2.qoder.sh"]}`))
						case "/algo/api/v2/model/list":
							body = io.NopCloser(strings.NewReader(`{"code":200,"body":[{"key":"model","enable":true}]}`))
						case "/algo/api/v2/service/pro/sse/agent_chat_generation":
							chats.Add(1)
							switch stage {
							case "输出前取消":
								body = &providerCancelBody{Reader: strings.NewReader(""), cancel: cancel}
							case "输出后取消":
								body = &providerCancelBody{Reader: strings.NewReader(content), cancel: cancel}
							default:
								body = io.NopCloser(strings.NewReader(content + tail))
							}
						default:
							t.Errorf("未知请求: %s", r.URL.Path)
							return nil, fmt.Errorf("未知平台路径 %s", r.URL.Path)
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
					})}
					manager, err := providers.Open("", client)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := manager.Import(context.Background(), providers.AccountInput{Target: "qoder:global", Credential: providers.Credential{UID: "fixture", AccessToken: "fixture", UserType: "teams"}}); err != nil {
						t.Fatal(err)
					}
					h, keys, _ := keyRig(t, "sk-admin")
					h.cfg.Providers = manager
					logs := reqlog.New(reqlog.Config{})
					defer logs.Close()
					h.cfg.RequestLog = logs
					key := createKey(t, keys, apikeys.Input{Name: "取消计量", Enabled: true, AllowedTargets: []string{"qoder:global"}})
					var release, busyDone chan struct{}
					var busyErr error
					if stage == "排队取消" {
						started := make(chan struct{})
						release, busyDone = make(chan struct{}), make(chan struct{})
						var block sync.Once
						go func() {
							defer close(busyDone)
							_, busyErr = manager.Execute(context.Background(), providers.Request{Target: "qoder:global", Model: "model", KeyID: "busy", Body: []byte(`{"messages":[{"role":"user","content":"busy"}]}`)}, func(map[string]any) error {
								block.Do(func() { close(started); <-release })
								return nil
							})
						}()
						t.Cleanup(func() {
							select {
							case <-release:
							default:
								close(release)
							}
							<-busyDone
						})
						waitFor(t, started, "占用平台账号")
						cancel()
					}
					body := fmt.Sprintf(`{"model":"qoder:global:model","stream":%t,"max_tokens":100,"messages":[{"role":"user","content":"hi"}],"input":"hi"}`, stream)
					req := httptest.NewRequest("POST", protocol.path, strings.NewReader(body)).WithContext(ctx)
					req.Header.Set("Authorization", "Bearer "+key.Secret)
					response := httptest.NewRecorder()
					var writer http.ResponseWriter = response
					if strings.HasSuffix(stage, "写失败") {
						match := "partial"
						if stage == "收尾写失败" {
							match = protocol.done
						}
						writer = providerRejectWriter{ResponseRecorder: response, match: match}
					}
					h.ServeHTTP(writer, req)
					if stage == "排队取消" {
						close(release)
						<-busyDone
						if busyErr != nil {
							t.Fatal("占用账号的请求失败", busyErr)
						}
					}
					wantRequests, wantTokens, wantAttempts := int64(1), int64(6), 1
					switch stage {
					case "排队取消":
						wantRequests, wantTokens, wantAttempts = 0, 0, 0
					case "输出前取消":
						wantTokens = 0
					}
					stored, _ := keys.Get(key.ID, time.Now())
					if stored.Used.Requests != wantRequests || stored.Used.Tokens != wantTokens || stored.Today.Requests != wantRequests || stored.Today.Tokens != wantTokens {
						t.Fatalf("取消或写失败的密钥计量错误: used=%+v today=%+v", stored.Used, stored.Today)
					}
					event := logs.Snapshot().Recent[0]
					if event.Outcome != reqlog.OutcomeInterrupted || event.OK || event.Attempts != wantAttempts || event.TotalTokens != wantTokens || event.TokensKnown != (wantTokens > 0) {
						t.Fatalf("取消或写失败未按中断记录: %+v", event)
					}
					if strings.Contains(response.Body.String(), protocol.done) || chats.Load() != 1 || manager.List()[0].InFlight != 0 {
						t.Fatal("中断后发出成功结束事件、重复出站或未释放账号", response.Body, chats.Load())
					}
					clientAttempts := int64(0)
					for _, usage := range manager.Usage() {
						if usage.KeyID != key.ID {
							continue
						}
						clientAttempts += usage.Requests
						wantFailed := int64(1)
						wantKnown := int64(0)
						if wantTokens > 0 {
							wantKnown = 1
						}
						if stage == "收尾写失败" {
							wantFailed = 0 // 上游正常完成，仅客户端收尾写入失败。
						}
						if usage.Failed != wantFailed || usage.PromptTokens+usage.CompletionTokens != wantTokens || usage.TokensKnown != wantKnown {
							t.Fatalf("平台尝试计量错误: %+v", usage)
						}
					}
					if clientAttempts != int64(wantAttempts) {
						t.Fatalf("排队取消被记成上游尝试: %d，应为 %d", clientAttempts, wantAttempts)
					}
				})
			}
		}
	}
}
