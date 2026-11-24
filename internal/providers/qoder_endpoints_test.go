// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/providers/cosy"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

type qoderEndpointTransport func(*http.Request) (*http.Response, error)

func (f qoderEndpointTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 请求仍带官方 host；传输仅发往本地假上游，不解析外网域名。
func qoderEndpointClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	endpoint, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := srv.Client().Transport
	return &http.Client{Transport: qoderEndpointTransport(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.Host = r.URL.Host
		copy.URL.Scheme, copy.URL.Host = endpoint.Scheme, endpoint.Host
		return transport.RoundTrip(copy)
	})}
}

func TestQoderGlobalEndpointSelection(t *testing.T) {
	var elections atomic.Int32
	q := &Qoder{HTTP: qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/algo/api/v3/service/region/endpoints":
			elections.Add(1)
			if r.Host != "center.qoder.sh" || r.Header.Get("Cosy-Machineid") != "fixture-machine" || r.Header.Get("Signature") != cosy.Sign(r.Header.Get("Date")) || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
				t.Error("端点发现未使用中心服务、原设备与官方匿名签名")
			}
			fmt.Fprint(w, cosy.Encode([]byte(`{"inferNodes":[{"type":"enterprise","url":"https://enterprise.qoder.sh"},{"type":"public","url":"https://api3.qoder.sh"}],"openapiNodes":["https://openapi-api3.qoder.sh"],"fastMode":["api2.qoder.sh"]}`)))
		case "/algo/api/v2/model/list":
			if r.Host != "api3.qoder.sh" {
				t.Errorf("目录没有选官方下发的推理端点：%s", r.Host)
			}
			fmt.Fprint(w, `{"code":200,"body":[{"key":"auto","name":"Auto"}]}`)
		case "/sash/api/v2/me/usage":
			if r.Host != "openapi-api3.qoder.sh" || r.Header.Get("Authorization") != "Bearer fixture-access" {
				t.Error("国际额度没有使用当前账号的 OpenAPI 端点与令牌")
			}
			fmt.Fprint(w, qoderQuotaFixture)
		default:
			t.Errorf("未预期的上游请求：%s %s", r.Host, r.URL.Path)
			w.WriteHeader(400)
		}
	})}
	c := Credential{UID: "fixture-user", AccessToken: "fixture-access", MachineID: "fixture-machine"}
	for range 2 {
		models, err := q.Models(context.Background(), "qoder:global", c)
		if err != nil || len(models) != 1 || models[0].ID != "auto" {
			t.Fatalf("目录：%v %v", models, err)
		}
	}
	quota, err := q.Quota(context.Background(), "qoder:global", c)
	if err != nil || quota.Balance == nil || *quota.Balance != 33.5 {
		t.Fatalf("国际额度：%+v %v", quota, err)
	}
	if elections.Load() != 1 {
		t.Fatal("同一账号的端点缓存未复用")
	}
	c.UID = "fixture-other-user"
	if _, err = q.Models(context.Background(), "qoder:global", c); err != nil || elections.Load() != 2 {
		t.Fatal("不同账号共用了端点结果", err)
	}
}

func TestQoderCatalogEndpointFailover(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		status         int
		disconnected   bool
		proxyFailed    bool
		wantSwitch     bool
	}{
		{name: "网关错误", status: 502, wantSwitch: true},
		{name: "节点不可用", status: 503, wantSwitch: true},
		{name: "节点超时", status: 504, wantSwitch: true},
		{name: "连接断开", disconnected: true, wantSwitch: true},
		{name: "绑定代理断开", proxyFailed: true, status: 200, response: `{"assistant":[{"key":"model","enable":true}]}`},
		{name: "鉴权拒绝", status: 401},
		{name: "权限拒绝", status: 403},
		{name: "限流", status: 429},
		{name: "业务错误", status: 200, response: `{"code":403,"body":{"assistant":[]}}`},
		{name: "全部停用", status: 200, response: `{"assistant":[{"key":"model","enable":false}]}`},
		{name: "错误响应", status: 200, response: `{"assistant":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var discoveries, primary, backup atomic.Int32
			client := qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Host {
				case "center.qoder.sh":
					discoveries.Add(1)
					fmt.Fprint(w, `{"inferNodes":["https://api2.qoder.sh","https://api3.qoder.sh"]}`)
				case "api2.qoder.sh":
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.response)
				case "api3.qoder.sh":
					if r.Header.Get("Cosy-MachineId") != "fixture-machine" || r.Header.Get("Authorization") == "" {
						t.Error("备用节点丢失账号签名或设备身份")
					}
					fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
				default:
					t.Errorf("请求越过官方发现节点：%s", r.Host)
					http.NotFound(w, r)
				}
			})
			base := client.Transport
			client.Transport = qoderEndpointTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "api2.qoder.sh" {
					attempt := primary.Add(1)
					if tc.proxyFailed && attempt == 1 {
						return nil, &upstream.RouteError{Err: io.ErrUnexpectedEOF}
					}
					if tc.disconnected {
						return nil, io.ErrUnexpectedEOF
					}
				}
				if r.URL.Host == "api3.qoder.sh" {
					backup.Add(1)
				}
				return base.RoundTrip(r)
			})
			q := &Qoder{HTTP: client}
			c := Credential{UID: "fixture-user", MachineID: "fixture-machine", AccessToken: "fixture-access", UserType: "personal"}
			models, err := q.Models(t.Context(), "qoder:global", c)
			if tc.wantSwitch {
				if err != nil || len(models) != 1 || primary.Load() != 1 || backup.Load() != 1 {
					t.Fatalf("未切到备用节点：models=%v err=%v 主=%d 备=%d", models, err, primary.Load(), backup.Load())
				}
				if _, err = q.Models(t.Context(), "qoder:global", c); err != nil || primary.Load() != 1 || backup.Load() != 2 || discoveries.Load() != 1 {
					t.Fatalf("后续目录没有复用已选节点：err=%v 主=%d 备=%d 发现=%d", err, primary.Load(), backup.Load(), discoveries.Load())
				}
			} else if err == nil || primary.Load() != 1 || backup.Load() != 0 || discoveries.Load() != 1 {
				t.Fatalf("业务失败引发了节点切换：err=%v 主=%d 备=%d", err, primary.Load(), backup.Load())
			} else if tc.proxyFailed {
				if !upstream.IsRouteError(err) {
					t.Fatalf("目录丢失线路错误：%v", err)
				}
				if _, err = q.Models(t.Context(), "qoder:global", c); err != nil || primary.Load() != 2 || backup.Load() != 0 || discoveries.Load() != 1 {
					t.Fatalf("线路恢复后未沿用原首选节点：err=%v 主=%d 备=%d 发现=%d", err, primary.Load(), backup.Load(), discoveries.Load())
				}
			}
		})
	}
}

func TestQoderCatalogFailoverHasAttemptLimitAndRouteIsolation(t *testing.T) {
	var attempts []string
	client := qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "center.qoder.sh" {
			fmt.Fprint(w, `{"inferNodes":["https://api2.qoder.sh","https://api2.qoder.sh","https://api3.qoder.sh","https://api1.qoder.sh","https://api4.qoder.sh"]}`)
			return
		}
		if r.Host == "api4.qoder.sh" {
			fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	base := client.Transport
	type fixtureRouteKey struct{}
	client.Transport = qoderEndpointTransport(func(r *http.Request) (*http.Response, error) {
		if r.Context().Value(fixtureRouteKey{}) != "bound-route" {
			t.Error("节点切换丢失绑定线路")
		}
		if r.URL.Path == "/algo/api/v2/model/list" {
			attempts = append(attempts, r.URL.Host)
		}
		return base.RoundTrip(r)
	})
	ctx := context.WithValue(t.Context(), fixtureRouteKey{}, "bound-route")
	c := Credential{UID: "fixture"}
	q := &Qoder{HTTP: client}
	models, err := q.Models(ctx, "qoder:global", c)
	var upstream *Error
	if !errors.As(err, &upstream) || upstream.Status != 503 || len(models) != 0 || !reflect.DeepEqual(attempts, []string{"api2.qoder.sh", "api3.qoder.sh", "api1.qoder.sh"}) {
		t.Fatalf("超出尝试上限或重复节点：%v %v %v", models, err, attempts)
	}
	models, err = q.Models(ctx, "qoder:global", c)
	if err != nil || len(models) != 1 || len(attempts) != 4 || attempts[3] != "api4.qoder.sh" {
		t.Fatalf("后续请求没有排除失败节点：%v %v %v", models, err, attempts)
	}
	// 更换账号或绑定线路要使用独立发现结果，不能继承其他线路的失败节点。
	for _, call := range []struct {
		ctx context.Context
		c   Credential
	}{
		{ctx: context.WithValue(ctx, providerRouteKey{}, "another-route"), c: c},
		{ctx: ctx, c: Credential{UID: "another-user"}},
	} {
		start := len(attempts)
		_, err := q.Models(call.ctx, "qoder:global", call.c)
		if !errors.As(err, &upstream) || upstream.Status != 503 || len(attempts) != start+3 || attempts[start] != "api2.qoder.sh" {
			t.Fatalf("节点故障状态跨账号或线路泄漏：%v %v", attempts[start:], err)
		}
	}
}

func TestQoderCatalogFailoverKeepsActiveQueueOnOriginalEndpoint(t *testing.T) {
	var chats, finishes atomic.Int32
	var q *Qoder
	c := Credential{UID: "fixture", UserType: "personal"}
	q = &Qoder{HTTP: qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/algo/api/v3/service/region/endpoints":
			fmt.Fprint(w, `{"inferNodes":["https://api2.qoder.sh","https://api3.qoder.sh"]}`)
		case "/algo/api/v2/model/list":
			switch r.Host {
			case "api2.qoder.sh":
				w.WriteHeader(http.StatusServiceUnavailable)
			case "api3.qoder.sh":
				fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
			default:
				t.Errorf("目录切换到了未发现节点：%s", r.Host)
				http.NotFound(w, r)
			}
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			if r.Host != "api2.qoder.sh" {
				t.Errorf("已开始的生成换了节点：%s", r.Host)
			}
			if chats.Add(1) == 1 {
				writeQoderQueued(t, w)
			} else {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}
		case "/algo/api/v2/service/ask/queue/status":
			if r.Host != "api2.qoder.sh" {
				t.Error("队列查询换了节点")
			}
			models, err := q.Models(t.Context(), "qoder:global", c)
			if err != nil || len(models) != 1 {
				t.Errorf("并发目录切换失败：%v %v", models, err)
			}
			fmt.Fprint(w, `{"data":{"isQueued":false,"serviceAvailable":true,"retryAfterSeconds":0}}`)
		case "/algo/api/v2/service/ask/finish":
			if r.Host != "api2.qoder.sh" {
				t.Error("队列释放换了节点")
			}
			finishes.Add(1)
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("非预期请求：%s", r.URL.Path)
			http.NotFound(w, r)
		}
	})}
	err := q.Chat(t.Context(), "qoder:global", c, Model{ID: "model"}, []byte(plainChat), "session", discardFrame)
	if err != nil || chats.Load() != 2 || finishes.Load() != 1 {
		t.Fatalf("目录切换干扰活动队列：err=%v chats=%d finishes=%d", err, chats.Load(), finishes.Load())
	}
	ctx, err := q.withEndpoints(t.Context(), "qoder:global", c)
	if err != nil || q.inferBase(ctx, "qoder:global") != "https://api3.qoder.sh" {
		t.Fatalf("新请求没有使用备用节点：%v", err)
	}
}

func TestQoderCatalogCancellationDoesNotRejectEndpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var catalogs atomic.Int32
	client := qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "center.qoder.sh" {
			fmt.Fprint(w, `{"inferNodes":["https://api2.qoder.sh","https://api3.qoder.sh"]}`)
			return
		}
		if r.Host != "api2.qoder.sh" {
			t.Error("取消请求切换了备用节点")
		}
		fmt.Fprint(w, `{"assistant":[{"key":"model","enable":true}]}`)
	})
	base := client.Transport
	client.Transport = qoderEndpointTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/algo/api/v2/model/list" && catalogs.Add(1) == 1 {
			cancel()
			return nil, context.Canceled
		}
		return base.RoundTrip(r)
	})
	q := &Qoder{HTTP: client}
	if _, err := q.Models(ctx, "qoder:global", Credential{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消原因丢失：%v", err)
	}
	if _, err := q.Models(t.Context(), "qoder:global", Credential{}); err != nil || catalogs.Load() != 2 {
		t.Fatalf("取消后节点状态被污染：%v", err)
	}
}

func TestQoderGlobalEndpointFailureUsesOfficialDefault(t *testing.T) {
	var elections atomic.Int32
	q := &Qoder{HTTP: qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "center.qoder.sh" {
			elections.Add(1)
			w.WriteHeader(503)
			return
		}
		if r.Host != "api2.qoder.sh" {
			t.Errorf("端点发现失败没有使用官方默认地址：%s", r.Host)
		}
		fmt.Fprint(w, `{"body":[{"key":"fixture"}]}`)
	})}
	for range 2 {
		if _, err := q.Models(context.Background(), "qoder:global", Credential{}); err != nil {
			t.Fatal(err)
		}
	}
	if elections.Load() != 1 {
		t.Fatal("发现失败导致每次业务请求都重复等待")
	}
}

func TestQoderGlobalEndpointDiscoveryAllowsColdConnection(t *testing.T) {
	q := &Qoder{HTTP: qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "center.qoder.sh" {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(1100 * time.Millisecond):
				fmt.Fprint(w, `{"inferNodes":["https://api3.qoder.sh"]}`)
				return
			}
		}
		fmt.Fprintf(w, `{"body":[{"key":%q}]}`, r.Host)
	})}
	models, err := q.Models(t.Context(), "qoder:global", Credential{})
	if err != nil || len(models) != 1 || models[0].ID != "api3.qoder.sh" {
		t.Fatalf("冷连接未能使用发现的端点：%v %v", models, err)
	}
}

func TestQoderGlobalQuotaUsesConfirmedOpenAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/sash/api/v2/me/usage" || r.Header.Get("Authorization") != "Bearer fixture-access" || r.UserAgent() != cosy.UserAgent || r.Header.Get("Cosy-Clienttype") != "5" || r.Header.Get("Content-Type") != "" || r.Header.Get("Traceparent") == "" {
			t.Error("国际额度请求不符已核对的官方接口")
		}
		fmt.Fprint(w, qoderQuotaFixture)
	}))
	defer srv.Close()
	q := &Qoder{HTTP: srv.Client(), OpenAPIBase: srv.URL}
	got, err := q.Quota(context.Background(), "qoder:global", Credential{AccessToken: "fixture-access"})
	if err != nil || got.Unit != "credits" || got.Total == nil || *got.Total != 50 {
		t.Fatalf("国际额度：%+v %v", got, err)
	}
}

func TestQoderEndpointRejectsUntrustedOrigins(t *testing.T) {
	for _, raw := range []any{
		"https://qoder.sh.evil.example", "http://api2.qoder.sh", "https://127.0.0.1", "https://api2.qoder.sh:8443",
		"https://user:password@api2.qoder.sh", "https://api2.qoder.sh/path", "https://api2.qoder.sh?token=fixture",
		"https://api2.qoder.sh#fragment", float64(3), map[string]any{"url": "https://api2.qoder.sh", "type": "unknown"},
	} {
		if _, err := qoderEndpointNodes([]any{raw}, false); err == nil {
			t.Errorf("接受了非官方端点：%v", raw)
		}
	}
	if _, err := qoderEndpointNodes("https://api2.qoder.sh", false); err == nil {
		t.Fatal("接受了非数组端点")
	}
}

func TestQoderDeviceEndpointAuthFallback(t *testing.T) {
	var v5, v3 atomic.Int32
	q := &Qoder{HTTP: qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/algo/api/v5/service/region/endpoints":
			v5.Add(1)
			if r.Header.Get("Authorization") != "Bearer fixture-access" || r.Header.Get("X-Gw-User-Id") != "fixture-user" || r.Header.Get("Cosy-MachineToken") != "fixture-machine" {
				t.Error("设备端点认证缺少原令牌或设备身份")
			}
			w.WriteHeader(403)
		case "/algo/api/v3/service/region/endpoints":
			v3.Add(1)
			fmt.Fprint(w, `{"inferNodes":["https://api3.qoder.sh"]}`)
		default:
			if r.Host != "api3.qoder.sh" {
				t.Error("设备认证回落后没有使用发现结果")
			}
			fmt.Fprint(w, `{"body":[{"key":"fixture"}]}`)
		}
	})}
	_, err := q.Models(context.Background(), "qoder:global", Credential{TokenKind: "device", UID: "fixture-user", AccessToken: "fixture-access", MachineID: "fixture-machine"})
	if err != nil || v5.Load() != 1 || v3.Load() != 1 {
		t.Fatalf("V5 拒绝未回落 V3：%v %d %d", err, v5.Load(), v3.Load())
	}
}

func TestQoderEndpointSingleFlightAndRouteIsolation(t *testing.T) {
	var elections atomic.Int32
	q := &Qoder{HTTP: qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "center.qoder.sh" {
			elections.Add(1)
			fmt.Fprint(w, `{"inferNodes":["https://api2.qoder.sh"]}`)
			return
		}
		fmt.Fprint(w, `{"body":[{"key":"fixture"}]}`)
	})}
	c := Credential{UID: "fixture-user", AccessToken: "fixture-access", MachineID: "fixture-machine"}
	errorsCh := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := q.Models(context.Background(), "qoder:global", c)
			errorsCh <- err
		}()
	}
	for range 8 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
	if elections.Load() != 1 {
		t.Fatal("并发目录重复发起端点发现")
	}
	ctx := context.WithValue(context.Background(), providerRouteKey{}, "fixture-proxy")
	if _, err := q.Models(ctx, "qoder:global", c); err != nil || elections.Load() != 2 {
		t.Fatal("换线路后沿用了旧端点缓存", err)
	}
	c.AccessToken = "fixture-refreshed"
	if _, err := q.Models(ctx, "qoder:global", c); err != nil || elections.Load() != 3 {
		t.Fatal("换令牌后沿用了旧账号发现结果", err)
	}
}

func TestQoderCancelledEndpointDiscoveryDoesNotPoisonCache(t *testing.T) {
	started := make(chan struct{})
	var elections atomic.Int32
	q := &Qoder{HTTP: qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "center.qoder.sh" {
			if elections.Add(1) == 1 {
				close(started)
				<-r.Context().Done()
				return
			}
			fmt.Fprint(w, `{"inferNodes":["https://api3.qoder.sh"]}`)
			return
		}
		if r.Host != "api3.qoder.sh" {
			t.Error("取消后缓存了默认端点")
		}
		fmt.Fprint(w, `{"body":[{"key":"fixture"}]}`)
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := q.Models(ctx, "qoder:global", Credential{})
		finished <- err
	}()
	<-started
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal("发现取消未传给调用方", err)
	}
	if _, err := q.Models(context.Background(), "qoder:global", Credential{}); err != nil || elections.Load() != 2 {
		t.Fatal("取消后没有重新发现端点", err)
	}
}

func TestQoderEndpointDiscoveryRetriesAfterProxyRecovery(t *testing.T) {
	client := qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "center.qoder.sh":
			fmt.Fprint(w, `{"inferNodes":["https://api3.qoder.sh"]}`)
		case "api3.qoder.sh":
			fmt.Fprint(w, `{"body":[{"key":"fixture"}]}`)
		default:
			t.Error("代理故障期间仍向默认端点发请求", r.Host)
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	base := client.Transport
	var discoveries int
	client.Transport = qoderEndpointTransport(func(r *http.Request) (*http.Response, error) {
		if r.Host == "center.qoder.sh" {
			discoveries++
			if discoveries == 1 {
				return nil, &upstream.RouteError{Err: io.ErrUnexpectedEOF}
			}
		}
		return base.RoundTrip(r)
	})
	q := &Qoder{HTTP: client}
	if _, err := q.Models(t.Context(), "qoder:global", Credential{}); !upstream.IsRouteError(err) {
		t.Fatalf("发现端点丢失线路错误：%v", err)
	}
	models, err := q.Models(t.Context(), "qoder:global", Credential{})
	if err != nil || len(models) != 1 || discoveries != 2 {
		t.Fatalf("线路恢复后未重新发现端点：%v %v，发现=%d", models, err, discoveries)
	}
}

func TestQoderEndpointRefreshPreservesLastRoute(t *testing.T) {
	refreshStarted := make(chan struct{})
	var refreshes atomic.Int32
	q := &Qoder{HTTP: qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "center.qoder.sh" {
			fmt.Fprintf(w, `{"body":[{"key":%q}]}`, r.Host)
			return
		}
		if r.Header.Get("Cosy-MachineId") == "second-machine" {
			fmt.Fprint(w, `{"inferNodes":["https://api2.qoder.sh"]}`)
			return
		}
		switch refreshes.Add(1) {
		case 1:
			fmt.Fprint(w, `{"inferNodes":["https://api3.qoder.sh"]}`)
		case 2:
			close(refreshStarted)
			<-r.Context().Done()
		case 3, 5:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 4:
			fmt.Fprint(w, `{"inferNodes":["https://api1.qoder.sh","https://api2.qoder.sh"]}`)
		default:
			t.Error("重复发现端点")
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})}
	first := Credential{UID: "first-user", MachineID: "first-machine"}
	second := Credential{UID: "second-user", MachineID: "second-machine"}
	assertCatalog := func(c Credential, host string) {
		t.Helper()
		models, err := q.Models(t.Context(), "qoder:global", c)
		if err != nil || len(models) != 1 || models[0].ID != host {
			t.Fatalf("目录未走预期端点 %s：%v %v", host, models, err)
		}
	}
	expire := func(age time.Duration) {
		q.endpointMu.Lock()
		defer q.endpointMu.Unlock()
		for _, entry := range q.endpoints {
			entry.expires = time.Now().Add(-age)
		}
	}
	assertCatalog(first, "api3.qoder.sh")
	expire(time.Second)
	assertCatalog(second, "api2.qoder.sh")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := q.Models(ctx, "qoder:global", first)
		finished <- err
	}()
	select {
	case <-refreshStarted:
	case err := <-finished:
		t.Fatalf("未刷新过期端点：%v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("未收到端点刷新")
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal("发现取消未传给调用方", err)
	}
	assertCatalog(first, "api3.qoder.sh")
	assertCatalog(first, "api3.qoder.sh")
	expire(time.Second)
	assertCatalog(first, "api1.qoder.sh")
	expire(qoderEndpointRetention + time.Second)
	assertCatalog(first, "api2.qoder.sh")
	if refreshes.Load() != 5 {
		t.Fatalf("端点发现调用次数错误：%d", refreshes.Load())
	}
}

func TestQoderQueuedFastRequestPinsEndpoint(t *testing.T) {
	var elections, chats, polls, finishes atomic.Int32
	var q *Qoder
	q = &Qoder{HTTP: qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "center.qoder.sh" {
			elections.Add(1)
			fmt.Fprint(w, `{"inferNodes":["https://api3.qoder.sh"],"fastMode":["api2.qoder.sh"]}`)
			return
		}
		if r.Host != "api2.qoder.sh" {
			t.Errorf("fast 的推理、排队或释放换了端点：%s", r.Host)
		}
		switch r.URL.Path {
		case "/algo/api/v2/service/pro/sse/agent_chat_generation":
			if chats.Add(1) == 1 {
				q.endpointMu.Lock()
				for _, entry := range q.endpoints {
					entry.expires = time.Now().Add(-time.Minute)
				}
				q.endpointMu.Unlock()
				writeQoderQueued(t, w)
				return
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case "/algo/api/v2/service/ask/queue/status":
			polls.Add(1)
			fmt.Fprint(w, `{"data":{"isQueued":false,"serviceAvailable":true,"retryAfterSeconds":0}}`)
		case "/algo/api/v2/service/ask/finish":
			finishes.Add(1)
			fmt.Fprint(w, `{}`)
		default:
			t.Error("非预期请求", r.URL.Path)
			w.WriteHeader(400)
		}
	})}
	err := q.Chat(context.Background(), "qoder:global", Credential{UID: "fixture-user"}, Model{ID: "fast"}, []byte(plainChat), "fixture-session", discardFrame)
	if err != nil || elections.Load() != 1 || chats.Load() != 2 || polls.Load() != 1 || finishes.Load() != 1 {
		t.Fatalf("排队时重新选路：%v elections=%d chats=%d polls=%d finish=%d", err, elections.Load(), chats.Load(), polls.Load(), finishes.Load())
	}
}

func TestQoderEndpointExplicitEmptyInferNodesFallsBackToDefault(t *testing.T) {
	var discoveries atomic.Int32
	var hitHosts []string
	q := &Qoder{HTTP: qoderEndpointClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "center.qoder.sh" {
			if discoveries.Add(1) == 1 {
				fmt.Fprint(w, `{"inferNodes":["https://api3.qoder.sh"]}`)
				return
			}
			// 显式返回空 inferNodes 数组时，应回落到默认推理端点 q.base("qoder:global")（api2.qoder.sh）。
			fmt.Fprint(w, `{"inferNodes":[]}`)
			return
		}
		hitHosts = append(hitHosts, r.Host)
		fmt.Fprintf(w, `{"body":[{"key":%q}]}`, r.Host)
	})}

	c := Credential{UID: "empty-infer-user", MachineID: "empty-infer-machine"}
	models, err := q.Models(t.Context(), "qoder:global", c)
	if err != nil || len(models) != 1 || models[0].ID != "api3.qoder.sh" {
		t.Fatalf("首次发现未切到 api3.qoder.sh: %v %v", models, err)
	}
	q.endpointMu.Lock()
	for _, entry := range q.endpoints {
		entry.expires = time.Now().Add(-time.Second)
	}
	q.endpointMu.Unlock()

	models, err = q.Models(t.Context(), "qoder:global", c)
	if err != nil || len(models) != 1 || models[0].ID != "api2.qoder.sh" || discoveries.Load() != 2 {
		t.Fatalf("显式空 inferNodes 未回落到默认推理端点 api2.qoder.sh: models=%v err=%v discoveries=%d hosts=%v", models, err, discoveries.Load(), hitHosts)
	}
}
