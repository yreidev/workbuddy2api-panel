// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

func TestExecuteBoundProxyFailover(t *testing.T) {
	const target = "traecode:cn"
	for _, stage := range []string{"目录", "推理", "刷新", "刷新重定向", "预检", "全部不可用", "取消"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var generations, refreshes atomic.Int32
			var rejected atomic.Bool
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Connection", "close")
				switch r.URL.Path {
				case "/api/ide/v1/get_detail_param":
					fmt.Fprint(w, `{"config_info_list":[{"config_name":"model"}]}`)
				case "/cloudide/api/v3/trae/oauth/ExchangeToken":
					refreshes.Add(1)
					if stage == "刷新重定向" {
						rejected.Store(true)
						http.Redirect(w, r, "/auth-next", http.StatusTemporaryRedirect)
						return
					}
					t.Error("线路断开时刷新请求仍到达上游")
					w.WriteHeader(http.StatusInternalServerError)
				case "/api/agent/v3/llm_utils_chat":
					generations.Add(1)
					if r.Header.Get("X-Uid") != "second" {
						t.Error("生成用了故障线路上的账号")
					}
					fmt.Fprint(w, "event: output\ndata: {\"response\":\"ok\"}\n\nevent: done\ndata: {}\n\n")
				default:
					t.Error("意外请求", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(origin.Close)
			var firstConnect, secondConnect atomic.Int32
			firstURL := routeTestProxy(t, origin.Listener.Addr().String(), &firstConnect, func() bool {
				if stage == "取消" {
					cancel()
				}
				return rejected.Load()
			})
			secondURL := routeTestProxy(t, origin.Listener.Addr().String(), &secondConnect, func() bool { return stage == "全部不可用" })
			dir := t.TempDir()
			registry, err := proxies.Open(filepath.Join(dir, "proxies.json"))
			if err != nil {
				t.Fatal(err)
			}
			pool, err := registry.CreatePool(proxies.PoolInput{Name: "线路验收"})
			if err != nil {
				t.Fatal(err)
			}
			added, err := registry.Import(firstURL+"\n"+secondURL, "http", pool, nil)
			if err != nil || len(added.Added) != 2 {
				t.Fatalf("导入代理：%+v %v", added, err)
			}
			firstProxy, secondProxy := added.Added[0], added.Added[1]
			client := upstream.New()
			client.SetRouter(registry)
			t.Cleanup(client.ChatHTTP.CloseIdleConnections)
			path := filepath.Join(dir, "providers.json")
			m, err := Open(path, client.ChatHTTP)
			if err != nil {
				t.Fatal(err)
			}
			m.SetProxies(registry)
			trae := m.adapters["traecode"].(*Trae)
			trae.CodeBase = "http://route-fixture.invalid"
			trae.AuthBase = trae.CodeBase
			var ids []string
			for priority, uid := range []string{"first", "second"} {
				id, err := m.Import(ctx, AccountInput{Target: target, Priority: priority, ProxyID: added.Added[priority],
					Credential: Credential{UID: uid, AccessToken: "fixture-" + uid, RefreshToken: "fixture-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}})
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
			}
			if stage == "推理" || stage == "刷新" || stage == "刷新重定向" {
				if _, err := m.Models(ctx, ids[0], target, false); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "刷新" || stage == "刷新重定向" {
				m.data.Accounts[0].Credential.ExpiresAt = 1
				if err := m.saveLocked(); err != nil {
					t.Fatal(err)
				}
			}
			original := m.data.Accounts[0].Credential
			if stage == "预检" {
				registry.ReportProxy(firstProxy, errors.New("模拟探测失败"))
			}
			firstBefore := firstConnect.Load()
			rejected.Store(stage != "刷新重定向")
			result, err := m.Execute(ctx, Request{Target: target, Model: "model", KeyID: "fixture-key", Session: "fixture-session", Body: []byte(plainChat)}, discardFrame)
			switch stage {
			case "全部不可用":
				var failure *Error
				if !errors.As(err, &failure) || failure.Status != 503 || failure.Code != "proxy_not_available" || strings.Contains(err.Error(), "fixture") ||
					result.Attempts != 2 || result.ProxyID != secondProxy || result.AccountID != ids[1] || secondConnect.Load() != 1 || generations.Load() != 0 || len(m.data.Bindings) != 0 {
					t.Fatalf("线路全部不可用时的结果：%+v err=%v，备用 CONNECT=%d，生成=%d", result, err, secondConnect.Load(), generations.Load())
				}
			case "取消":
				if !errors.Is(err, context.Canceled) || result.Attempts != 1 || secondConnect.Load() != 0 || generations.Load() != 0 || len(m.data.Bindings) != 0 {
					t.Fatalf("请求取消后仍有出站操作：%+v err=%v，备用 CONNECT=%d", result, err, secondConnect.Load())
				}
			case "刷新重定向":
				var failure *Error
				if !errors.As(err, &failure) || failure.Code != "upstream_redirect" || result.Attempts != 1 || secondConnect.Load() != 0 || generations.Load() != 0 || len(m.data.Bindings) != 0 {
					t.Fatalf("刷新已到达上游后仍换号：%+v err=%v，备用 CONNECT=%d", result, err, secondConnect.Load())
				}
			default:
				if err != nil || result.Attempts != 2 || result.AccountID != ids[1] || result.ProxyID != secondProxy || secondConnect.Load() != 2 || generations.Load() != 1 || !result.Started {
					t.Fatalf("没有切到正常绑定：%+v err=%v，备用 CONNECT=%d，生成=%d", result, err, secondConnect.Load(), generations.Load())
				}
				for _, binding := range m.data.Bindings {
					if binding.AccountID != ids[1] || binding.SessionID == "" {
						t.Fatalf("会话写到错误账号：%+v", binding)
					}
				}
				if len(m.data.Bindings) != 1 {
					t.Fatal("未保存成功会话")
				}
			}
			wantFirst := firstBefore + 1
			var wantRefresh int32
			if stage == "预检" {
				wantFirst = firstBefore
			}
			if stage == "刷新重定向" {
				wantRefresh = 1
			}
			if firstConnect.Load() != wantFirst || refreshes.Load() != wantRefresh {
				t.Fatalf("首条线路尝试次数错误：CONNECT=%d want=%d，刷新=%d", firstConnect.Load(), wantFirst, refreshes.Load())
			}
			for _, proxy := range registry.View().Proxies {
				wantDown := proxy.ID == firstProxy && stage != "取消" && stage != "刷新重定向" || proxy.ID == secondProxy && stage == "全部不可用"
				if (proxy.Health.State == "down") != wantDown {
					t.Fatalf("线路健康状态错误：id=%s state=%s", proxy.ID, proxy.Health.State)
				}
			}
			for _, id := range ids {
				rt := m.running[id]
				wantBlocked := stage == "刷新重定向" && id == ids[0]
				if rt.inFlight != 0 || rt.blocked != wantBlocked || !rt.coolUntil.IsZero() {
					t.Fatalf("线路故障改变账号状态：id=%s 在途=%d 刷新保护=%t 冷却=%v", id, rt.inFlight, rt.blocked, rt.coolUntil)
				}
			}
			_, guardErr := os.Stat(m.guardPath(ids[0]))
			wantBlocked := stage == "刷新重定向"
			if wantBlocked && guardErr != nil || !wantBlocked && !errors.Is(guardErr, os.ErrNotExist) {
				t.Fatalf("刷新标记与操作是否到达上游不符：%v", guardErr)
			}
			reopened, err := Open(path, client.ChatHTTP)
			if err != nil || reopened.List()[0].Disabled != wantBlocked || reopened.data.Accounts[0].Credential != original {
				t.Fatalf("线路故障改变了持久化账号：%v", err)
			}
		})
	}
}

func TestExecuteProxyFailureAfterDispatch(t *testing.T) {
	for _, target := range []string{"traework:cn", "qoder:cn"} {
		t.Run(target, func(t *testing.T) {
			m, adapter := testManager(t, "")
			first := importFake(t, m, target, "first")
			importFake(t, m, target, "second")
			var generated, billing, finished int
			transport := qoderEndpointTransport(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				switch r.URL.Path {
				case "/api/remote/v1/chat_sessions":
					generated++
					fmt.Fprint(w, `{"data":{"chat_session_id":"work-session","message_id":"work-message"}}`)
				case "/api/remote/v1/chat_sessions/work-session/events", "/algo/api/v2/service/ask/queue/status":
					return nil, &upstream.RouteError{Proxy: "fixture-private-name", Err: io.ErrUnexpectedEOF}
				case "/api/v1/commercial/get_session_usage":
					billing++
					w.WriteHeader(http.StatusServiceUnavailable)
				case "/algo/api/v2/service/pro/sse/agent_chat_generation":
					generated++
					writeQoderQueued(t, w)
				case "/algo/api/v2/service/ask/finish":
					finished++
					fmt.Fprint(w, `{"code":200,"body":{}}`)
				default:
					t.Errorf("意外上游请求：%s", r.URL.Path)
					w.WriteHeader(http.StatusServiceUnavailable)
				}
				return w.Result(), nil
			})
			client := &http.Client{Transport: transport}
			var real Adapter = &Qoder{HTTP: client, Base: "https://route-fixture.invalid"}
			if target == "traework:cn" {
				real = &Trae{HTTP: client, WorkBase: "https://route-fixture.invalid", PayBase: "https://route-fixture.invalid"}
			}
			adapter.chat = func(ctx context.Context, target string, c Credential, session string, emit func(map[string]any) error) error {
				return real.Chat(ctx, target, c, Model{ID: "model", Native: map[string]any{"config_source": float64(1)}}, []byte(plainChat), session, emit)
			}
			r, err := m.Execute(t.Context(), Request{Target: target, Model: "model", Session: "fixture", Body: []byte(plainChat)}, discardFrame)
			var failure *Error
			if !errors.As(err, &failure) || failure.Status != 503 || failure.Code != "proxy_not_available" || strings.Contains(err.Error(), "fixture") ||
				r.Attempts != 1 || r.AccountID != first || r.Started || generated != 1 || len(m.data.Bindings) != 0 || !m.running[first].coolUntil.IsZero() {
				t.Fatalf("提交上游后故障处理错误：%+v err=%v 生成=%d", r, err, generated)
			}
			if target == "traework:cn" && billing != 1 || target == "qoder:cn" && finished != 1 {
				t.Fatalf("漏掉原请求清理：扣费查询=%d 队列释放=%d", billing, finished)
			}
		})
	}
}

func routeTestProxy(t *testing.T, origin string, connects *atomic.Int32, reject func() bool) string {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connects.Add(1)
		if r.Method != http.MethodConnect || r.Host != "route-fixture.invalid:80" || r.Header.Get("Proxy-Authorization") != "Basic dGVzdDpmaXh0dXJl" {
			t.Errorf("CONNECT 目标或代理认证错误：%s %s", r.Method, r.Host)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if reject() {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		backend, err := net.Dial("tcp", origin)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer backend.Close()
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		if err := buf.Flush(); err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = io.Copy(backend, buf)
			_ = backend.(*net.TCPConn).CloseWrite()
		}()
		_, _ = io.Copy(conn, backend)
		_ = conn.Close()
		<-done
	}))
	t.Cleanup(proxy.Close)
	return strings.Replace(proxy.URL, "http://", "http://test:fixture@", 1)
}

func TestProxyProbeUsesCurrentPlatformTargets(t *testing.T) {
	m, _ := testManager(t, "")
	code := importFake(t, m, "traecode:cn", "shared")
	importFake(t, m, "traework:cn", "shared")
	qoder := importFake(t, m, "qoder:global", "global")
	m.adapters["traecode"] = &Trae{CodeBase: "https://code.example"}
	m.adapters["traework"] = &Trae{WorkBase: "https://work.example"}
	q := &Qoder{}
	m.adapters["qoder"] = q
	for _, id := range []string{code, qoder} {
		if err := m.Update(id, AccountInput{ProxyID: "p"}, false); err != nil {
			t.Fatal(err)
		}
	}
	q.endpoints = map[[32]byte]*qoderEndpointEntry{endpointCacheKey(m.data.Accounts[1].Credential, "p"): {routes: qoderEndpoints{inference: "https://node.example"}}}
	hosts := m.proxyHosts()["p"]
	slices.Sort(hosts)
	if !slices.Equal(hosts, []string{"code.example", "node.example", "work.example"}) {
		t.Fatal("绑定入口与实际通道不符", hosts)
	}
	if err := m.Update(code, AccountInput{ProxyID: "other"}, false); err != nil {
		t.Fatal(err)
	}
	if hosts := m.proxyHosts(); len(hosts["other"]) != 2 || !slices.Equal(hosts["p"], []string{"node.example"}) {
		t.Fatal("改绑未更新探测入口", hosts)
	}
}

func TestPlatformRejectsRedirects(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		calls := 0
		client := &http.Client{Transport: qoderQueueTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 307, Header: http.Header{"Location": {"https://next.example/"}}, Body: http.NoBody}, nil
		})}
		_, err := request(t.Context(), client, method, "https://platform.example/", nil, http.Header{})
		var e *Error
		if calls != 1 || !errors.As(err, &e) || e.Code != "upstream_redirect" || e.Retry {
			t.Fatalf("平台重定向策略: %s calls=%d err=%v", method, calls, err)
		}
	}
}
