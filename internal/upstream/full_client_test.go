// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// 第七轮补齐：client.go、headers.go、payload.go 的剩余分支。

func TestFull7ClientBaseSelectors(t *testing.T) {
	if got := (&Client{}).globalChatBase(); got != defaultGlobalBase {
		t.Fatalf("globalChatBase 缺省 = %q，期望 %q", got, defaultGlobalBase)
	}
	if got := (&Client{ChatBaseGlobal: "https://g.example"}).globalChatBase(); got != "https://g.example" {
		t.Fatalf("globalChatBase 覆盖 = %q", got)
	}
	if got := (&Client{BillingBaseGlobal: "https://b.example"}).globalBillingBase(); got != "https://b.example" {
		t.Fatalf("globalBillingBase 覆盖 = %q", got)
	}
	if got := (&Client{}).globalBillingBase(); got != defaultGlobalBase {
		t.Fatalf("globalBillingBase 缺省 = %q", got)
	}

	c := &Client{GlobalEnabled: true, ChatBaseCN: "https://cn.example", ChatBaseGlobal: "https://g.example"}
	ga := globalAuth()
	ca := cnAuth()
	if got := c.chatBase(ga); got != "https://g.example" {
		t.Fatalf("global chatBase = %q", got)
	}
	if got := c.chatBase(ca); got != "https://cn.example" {
		t.Fatalf("cn chatBase = %q", got)
	}

	if got := (&Client{GlobalEnabled: true}).webBase(ga); got != defaultGlobalBase {
		t.Fatalf("global webBase = %q", got)
	}
	if got := (&Client{}).webBase(ca); got != "https://www.workbuddy.cn" {
		t.Fatalf("cn webBase 缺省 = %q", got)
	}
}

func TestFull7ChatStreamContextNilContext(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, "/v2/chat/completions", 200, "data: [DONE]\n\n")
	c := upClient(f)
	rc, status, respBody, err := c.ChatStreamContext(nil, upAuth(), //nolint:staticcheck // 测的就是传 nil 时回退到 Background
		[]byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`), "", ChatMeta{})
	if err != nil {
		t.Fatalf("nil ctx 应回落 Background: %v", err)
	}
	if status != 200 || respBody != nil {
		t.Fatalf("status=%d respBody=%q", status, respBody)
	}
	if rc == nil {
		t.Fatal("成功分支应返回响应体")
	}
	raw, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !strings.Contains(string(raw), "[DONE]") {
		t.Fatalf("响应体 = %q", raw)
	}
}

func TestFull7ChatStreamRetryAfter(t *testing.T) {
	f := newUpFake(t)
	f.onFunc(http.MethodPost, "/v2/chat/completions", func(*upReq) (*http.Response, error) {
		resp := jsonResp(429, `{"code":10000,"msg":"rate limited"}`)
		resp.Header.Set("Retry-After", "30")
		return resp, nil
	})
	c := upClient(f)
	_, status, _, err := c.ChatStreamContext(context.Background(), upAuth(), []byte(`{"model":"m","messages":[]}`), "", ChatMeta{})
	if status != 429 {
		t.Fatalf("status = %d", status)
	}
	var ue *Error
	if !errors.As(err, &ue) || ue.Kind != ErrSoftRate {
		t.Fatalf("err = %v，期望 ErrSoftRate", err)
	}
	if ue.RetryAfter != 30*time.Second {
		t.Fatalf("RetryAfter = %v", ue.RetryAfter)
	}
}

func TestFull7NonChatModelShortMax(t *testing.T) {
	if !nonChatModel("some-model", 100, nil) {
		t.Fatal("maxOutputTokens<=256 应判非对话模型")
	}
	if nonChatModel("some-model", 300, nil) {
		t.Fatal("maxOutputTokens>256 不应判非对话模型")
	}
}

func TestFull7ProbeCatalogV3Only(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/v3/config", 200,
		`{"code":0,"data":{"models":[{"id":"m1","maxOutputTokens":1000}]}}`)
	c := upClient(f)
	out, err := c.probeCatalog(upAuth())
	if err != nil {
		t.Fatalf("v3 成功时不应失败: %v", err)
	}
	if len(out) != 1 || out[0].ID != "m1" {
		t.Fatalf("out = %+v", out)
	}
}

func TestFull7MergeModelInfosEdges(t *testing.T) {
	primary := []ModelInfo{{ID: "a"}}
	if got := mergeModelInfos(primary, nil); !reflect.DeepEqual(got, primary) {
		t.Fatalf("secondary 为空应原样返回 primary: %+v", got)
	}
	got := mergeModelInfos(
		[]ModelInfo{{ID: ""}, {ID: "a"}, {ID: "a"}},
		[]ModelInfo{{ID: "b"}, {ID: "a"}},
	)
	want := []ModelInfo{{ID: "a"}, {ID: "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("合并 = %+v，期望 %+v", got, want)
	}
}

func fullEntClient(rt rtFunc) *Client {
	return &Client{HTTP: &http.Client{Transport: rt}, ChatBaseCN: "https://ent.example"}
}

// fullEntAuth 国内版企业号（企业模型目录按账号身份下发，路径里带 enterpriseId）。
func fullEntAuth() *auth.Auth {
	a := upAuth()
	a.EnterpriseID = "ent-1"
	return a
}

func TestFull7FetchEnterpriseModelsErrors(t *testing.T) {
	t.Run("URL 非法", func(t *testing.T) {
		c := &Client{ChatBaseCN: "http://[::1"}
		if _, err := c.fetchEnterpriseModels(fullEntAuth()); err == nil {
			t.Fatal("非法 base 应返回 NewRequest 错误")
		}
	})
	t.Run("传输层错误", func(t *testing.T) {
		c := fullEntClient(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial boom")
		})
		if _, err := c.fetchEnterpriseModels(fullEntAuth()); err == nil || !strings.Contains(err.Error(), "dial boom") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("读响应体失败", func(t *testing.T) {
		c := fullEntClient(func(*http.Request) (*http.Response, error) {
			resp := jsonResp(200, "")
			resp.Body = io.NopCloser(errReader{})
			return resp, nil
		})
		if _, err := c.fetchEnterpriseModels(fullEntAuth()); err == nil || !strings.Contains(err.Error(), "read body") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("响应不是 JSON", func(t *testing.T) {
		c := fullEntClient(func(*http.Request) (*http.Response, error) {
			return jsonResp(200, "not-json"), nil
		})
		if _, err := c.fetchEnterpriseModels(fullEntAuth()); err == nil || !strings.Contains(err.Error(), "models parse") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("业务码非 0", func(t *testing.T) {
		c := fullEntClient(func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{"code":7}`), nil
		})
		if _, err := c.fetchEnterpriseModels(fullEntAuth()); err == nil || !strings.Contains(err.Error(), "code=7") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("data 不是数组", func(t *testing.T) {
		c := fullEntClient(func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{"code":0,"data":{"models":[{"id":"m","tags":["chat"]}]}}`), nil
		})
		if _, err := c.fetchEnterpriseModels(fullEntAuth()); err == nil || !strings.Contains(err.Error(), "models parse") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("HTTP 非 200", func(t *testing.T) {
		c := fullEntClient(func(*http.Request) (*http.Response, error) {
			return jsonResp(500, `{"code":500,"msg":"boom"}`), nil
		})
		if _, err := c.fetchEnterpriseModels(fullEntAuth()); err == nil || !strings.Contains(err.Error(), "models api status 500") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("只收 chat 标签并剔除禁用与非对话模型", func(t *testing.T) {
		c := fullEntClient(func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{"code":0,"data":[
				{"id":"no-tag","tags":["craft"]},
				{"id":"nes-x","tags":["chat"]},
				{"id":"tiny","maxOutputTokens":128,"tags":["chat"]},
				{"id":"off","disabled":true,"tags":["chat"]},
				{"id":"good","maxOutputTokens":1000,"tags":["chat"]}]}`), nil
		})
		out, err := c.fetchEnterpriseModels(fullEntAuth())
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(out) != 1 || out[0].ID != "good" {
			t.Fatalf("out = %+v", out)
		}
	})
	t.Run("过滤后为空", func(t *testing.T) {
		c := fullEntClient(func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{"code":0,"data":[{"id":"nes-x","tags":["chat"]}]}`), nil
		})
		if _, err := c.fetchEnterpriseModels(fullEntAuth()); err == nil || !strings.Contains(err.Error(), "empty list") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("空数组", func(t *testing.T) {
		c := fullEntClient(func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{"code":0,"data":[]}`), nil
		})
		if _, err := c.fetchEnterpriseModels(fullEntAuth()); err == nil || !strings.Contains(err.Error(), "empty list") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestFull7FetchV3ModelsFilters(t *testing.T) {
	t.Run("剔除非对话模型", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, "/v3/config", 200,
			`{"code":0,"data":{"models":[{"id":"nes-x"},{"id":"good","maxOutputTokens":1000}]}}`)
		out, err := upClient(f).fetchV3Models(upAuth())
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(out) != 1 || out[0].ID != "good" {
			t.Fatalf("out = %+v", out)
		}
	})
	t.Run("过滤后为空", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, "/v3/config", 200,
			`{"code":0,"data":{"models":[{"id":"nes-x"}]}}`)
		if _, err := upClient(f).fetchV3Models(upAuth()); err == nil || !strings.Contains(err.Error(), "empty models") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestFull7StoreModelRatesSkipsEmptyID(t *testing.T) {
	c := &Client{}
	c.storeModelRates("cn", []ModelInfo{{ID: "", Credits: "x0.5"}})
	c.effortsMu.RLock()
	n := len(c.modelRates["cn"])
	c.effortsMu.RUnlock()
	if n != 0 {
		t.Fatalf("空 ID 不应进倍率表，实际 %d 项", n)
	}
}

func TestFull7ModelRateEmptyInput(t *testing.T) {
	var nilClient *Client
	if got := nilClient.ModelRate("cn", "m"); got != "" {
		t.Fatalf("nil client = %q", got)
	}
	if got := (&Client{}).ModelRate("cn", ""); got != "" {
		t.Fatalf("空模型名 = %q", got)
	}
}

func TestFull7FetchV3ConfigModelMapErrors(t *testing.T) {
	t.Run("传输层错误", func(t *testing.T) {
		c := fullEntClient(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial boom")
		})
		if _, err := c.fetchV3ConfigModelMap(upAuth()); err == nil || !strings.Contains(err.Error(), "dial boom") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("跳过空 ID", func(t *testing.T) {
		c := fullEntClient(func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{"code":0,"data":{"models":[{"id":""},{"id":"good"}]}}`), nil
		})
		out, err := c.fetchV3ConfigModelMap(upAuth())
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(out) != 1 || out["good"].ID != "good" {
			t.Fatalf("out = %+v", out)
		}
	})
}

func TestFull7CreditPackagesErrors(t *testing.T) {
	t.Run("计费请求失败", func(t *testing.T) {
		f := newUpFake(t)
		f.onResources("")
		f.on(http.MethodPost, resourceFreePath, 400, `{"code":10001,"msg":"no"}`)
		if _, _, _, err := upClient(f).CreditPackages(upAuth()); err == nil {
			t.Fatal("应返回上游错误")
		}
	})
	t.Run("响应解析失败", func(t *testing.T) {
		f := newUpFake(t)
		f.onResources("")
		f.on(http.MethodPost, resourcePaidPath, 200, `{"code":0,"data":"oops"}`)
		if _, _, _, err := upClient(f).CreditPackages(upAuth()); err == nil || !strings.Contains(err.Error(), "resource parse") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("剩余超过额度按额度算", func(t *testing.T) {
		f := newUpFake(t)
		f.onResources("", `{"PackageName":"p","CycleCapacitySizePrecise":"10","CycleCapacityRemainPrecise":"12.5"}`)
		packs, remain, size, err := upClient(f).CreditPackages(upAuth())
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(packs) != 1 || !packs[0].Cycle || packs[0].Remain != 10 || packs[0].Used != 0 || remain != 10 || size != 10 {
			t.Fatalf("packs = %+v remain=%d size=%d", packs, remain, size)
		}
	})
}

func TestFull7EnterpriseResourceBranches(t *testing.T) {
	ea := &auth.Auth{UID: "ue", AccessToken: "at", EnterpriseID: "ent-1", ExpiresAt: 9999999999}
	call := func(t *testing.T, status int, body string) (int64, int64, error) {
		t.Helper()
		f := newUpFake(t)
		f.on(http.MethodPost, "/v2/billing/meter/get-enterprise-user-usage", status, body)
		remain, total, _, _, _, err := upClient(f).enterpriseResource(ea, 0)
		return remain, total, err
	}
	t.Run("计费请求失败", func(t *testing.T) {
		if _, _, err := call(t, 400, `{"code":10001,"msg":"no"}`); err == nil {
			t.Fatal("应返回上游错误")
		}
	})
	t.Run("响应解析失败", func(t *testing.T) {
		if _, _, err := call(t, 200, `{"code":0,"data":"oops"}`); err == nil || !strings.Contains(err.Error(), "enterprise resource parse") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("浮点用量四舍五入", func(t *testing.T) {
		remain, total, err := call(t, 200, `{"code":0,"data":{"credit":1.6,"limitNum":10}}`)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if remain != 8 || total != 10 {
			t.Fatalf("remain=%d total=%d，期望 8/10", remain, total)
		}
	})
	t.Run("剩余额度不落负", func(t *testing.T) {
		remain, total, err := call(t, 200, `{"code":0,"data":{"credit":10,"limitNum":5}}`)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if remain != 0 || total != 5 {
			t.Fatalf("remain=%d total=%d，期望 0/5", remain, total)
		}
	})
}

func TestFull7UserResourceDetailedWithExpiryBranches(t *testing.T) {
	call := func(t *testing.T, status int, body string) (int64, int64, error) {
		t.Helper()
		f := newUpFake(t)
		f.onResources("")
		f.on(http.MethodPost, resourceFreePath, status, body)
		remain, total, _, _, _, err := upClient(f).UserResourceDetailedWithExpiry(upAuth(), 0)
		return remain, total, err
	}
	t.Run("计费请求失败", func(t *testing.T) {
		if _, _, err := call(t, 400, `{"code":10001,"msg":"no"}`); err == nil {
			t.Fatal("应返回上游错误")
		}
	})
	t.Run("响应解析失败", func(t *testing.T) {
		if _, _, err := call(t, 200, `{"code":0,"data":"oops"}`); err == nil || !strings.Contains(err.Error(), "resource parse") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("负数当 0、剩余按额度夹取", func(t *testing.T) {
		remain, total, err := call(t, 200, resourceList(
			`{"CycleCapacityRemainPrecise":"-5","CycleCapacitySizePrecise":"10"}`,
			`{"CycleCapacityRemainPrecise":"10","CycleCapacitySizePrecise":"5"}`))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if remain != 5 || total != 15 {
			t.Fatalf("remain=%d total=%d，期望 5/15", remain, total)
		}
	})
}

func TestFull7IsAlreadyCheckinEdges(t *testing.T) {
	if IsAlreadyCheckin(errors.New("network")) {
		t.Fatal("非 *Error 不应判为已签到")
	}
	if IsAlreadyCheckin(&Error{Msg: "something else"}) {
		t.Fatal("无已签到标记不应判为已签到")
	}
	if !IsAlreadyCheckin(&Error{Msg: "already checked in"}) {
		t.Fatal("已签到标记应命中")
	}
}

// --- headers.go ------------------------------------------------------------

func TestFull7HeaderVersionHelpers(t *testing.T) {
	if got := (&Client{CliVersion: "9.9.9"}).cliVersion(globalAuth()); got != "9.9.9" {
		t.Fatalf("cliVersion = %q", got)
	}
	// 没覆盖时按账号域：国内版 5.9.3、国际版 5.9.4 都内置 2.165.0；没有账号按国内版
	for _, tc := range []struct {
		name string
		a    *auth.Auth
		want string
	}{
		{"国内版", cnAuth(), "2.165.0"},
		{"国际版", globalAuth(), "2.165.0"},
		{"没有账号", nil, "2.165.0"},
	} {
		if got := (&Client{}).cliVersion(tc.a); got != tc.want {
			t.Fatalf("%s 默认 cliVersion = %q，want %q", tc.name, got, tc.want)
		}
	}
	if got := (&Client{}).defaultCLIUA(nil); !strings.Contains(got, "CLI/"+defaultCliVersionCN) {
		t.Fatalf("默认 UA = %q", got)
	}
}

// 没有 token / uid 时对应的头就不发，也不补 X-No-*：官方对话请求从不带 X-No-*。
func TestFull7ChatHeadersMissingFields(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{}).ChatHeaders(req, &auth.Auth{}, "", ChatMeta{})
	for _, k := range []string{"Authorization", "X-User-Id", "X-No-Authorization", "X-No-User-Id", "X-Domain"} {
		if v, ok := req.Header[http.CanonicalHeaderKey(k)]; ok {
			t.Fatalf("%s 不应发: %q", k, v)
		}
	}
}

func TestFull7ConversationHeaders(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{}).injectConversationHeaders(req, ChatMeta{
		ConversationID:        "conv-1",
		ConversationRequestID: "not-hex",
		TraceID:               "trace-x",
	})
	if got := req.Header.Get("X-Conversation-ID"); got != "conv-1" {
		t.Fatalf("X-Conversation-ID = %q", got)
	}
	if req.Header.Get("X-B3-TraceId") != req.Header.Get("X-Conversation-Message-ID") {
		t.Fatalf("非法 TraceId 应回落消息 ID: b3=%q msg=%q",
			req.Header.Get("X-B3-TraceId"), req.Header.Get("X-Conversation-Message-ID"))
	}
	if got := req.Header.Get("X-Trace-ID"); got != "trace-x" {
		t.Fatalf("X-Trace-ID = %q", got)
	}
}

func TestFull7ValidTraceIDEdges(t *testing.T) {
	if validTraceID("abc") {
		t.Fatal("长度不符应判非法")
	}
	if validTraceID("zzzzzzzzzzzzzzzz") {
		t.Fatal("非 hex 应判非法")
	}
	if !validTraceID("0123456789ABCDEF") {
		t.Fatal("16 位大写 hex 应合法")
	}
}

func TestFull7AttributionSaaS(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{ClientName: "SaaS"}).ChatHeaders(req, &auth.Auth{}, "", ChatMeta{})
	if got := req.Header.Get("X-Product"); got != "SaaS" {
		t.Fatalf("X-Product = %q", got)
	}
	for _, k := range []string{"X-IDE-Name", "X-IDE-Type", "X-IDE-Version", "X-Agent-Purpose"} {
		if got := req.Header.Get(k); got != "" {
			t.Fatalf("SaaS 不应设 %s: %q", k, got)
		}
	}
}

func TestFull7InjectClientIP(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{PassthroughIP: true}).injectClientIP(req, "1.2.3.4")
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "X-Client-IP"} {
		if got := req.Header.Get(h); got != "1.2.3.4" {
			t.Fatalf("%s = %q", h, got)
		}
	}
}

func TestFull7ExtractClientIP(t *testing.T) {
	if got := ExtractClientIP(nil); got != "" {
		t.Fatalf("nil 请求 = %q", got)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://x/", nil)
	if got := ExtractClientIP(req); got != "" {
		t.Fatalf("无头 = %q", got)
	}
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 2.2.2.2")
	if got := ExtractClientIP(req); got != "1.1.1.1" {
		t.Fatalf("XFF 首段 = %q", got)
	}
	req.Header.Set("X-Forwarded-For", " 3.3.3.3 ")
	if got := ExtractClientIP(req); got != "3.3.3.3" {
		t.Fatalf("XFF 单值 = %q", got)
	}
	req.Header.Del("X-Forwarded-For")
	req.Header.Set("X-Real-IP", " 4.4.4.4 ")
	if got := ExtractClientIP(req); got != "4.4.4.4" {
		t.Fatalf("X-Real-IP = %q", got)
	}
}

func TestFull7RefreshHeadersEnterprise(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	(&Client{}).RefreshHeaders(req, &auth.Auth{EnterpriseID: "ent-1"})
	if got := req.Header.Get("X-Enterprise-Id"); got != "ent-1" {
		t.Fatalf("X-Enterprise-Id = %q", got)
	}
}

// --- payload.go ------------------------------------------------------------

func TestFull7PrepareBodyEmpty(t *testing.T) {
	if got := PrepareBodyOpt(nil, true); len(got) != 0 {
		t.Fatalf("空 body 应原样返回: %q", got)
	}
}

func TestFull7TranslateMaxCompletionTokensTypes(t *testing.T) {
	obj := map[string]any{"max_completion_tokens": float64(100), "max_tokens": float64(5)}
	translateMaxCompletionTokens(obj)
	if _, has := obj["max_completion_tokens"]; has {
		t.Fatal("别名应被删除")
	}
	if obj["max_tokens"] != float64(5) {
		t.Fatalf("显式 max_tokens 应优先: %v", obj["max_tokens"])
	}

	obj = map[string]any{"max_completion_tokens": int64(7)}
	translateMaxCompletionTokens(obj)
	if obj["max_tokens"] != int64(7) {
		t.Fatalf("int64 别名 = %v", obj["max_tokens"])
	}

	obj = map[string]any{"max_completion_tokens": 8}
	translateMaxCompletionTokens(obj)
	if obj["max_tokens"] != int64(8) {
		t.Fatalf("int 别名 = %v", obj["max_tokens"])
	}
}

func TestFull7ClampGPTMinMaxTokensInt(t *testing.T) {
	obj := map[string]any{"model": "gpt-x", "max_tokens": 5}
	clampGPTMinMaxTokens(obj)
	if obj["max_tokens"] != int64(gptMinMaxTokens) {
		t.Fatalf("max_tokens = %v", obj["max_tokens"])
	}
}

func TestFull7NormalizeReasoningEffortSkips(t *testing.T) {
	efforts := map[string][]string{"m": {"high"}}
	obj := map[string]any{"reasoning_effort": "low"}
	normalizeReasoningEffort(obj, efforts)
	if obj["reasoning_effort"] != "low" {
		t.Fatalf("无 model 应透传: %v", obj)
	}
	obj = map[string]any{"model": "m", "reasoning_effort": 5}
	normalizeReasoningEffort(obj, efforts)
	if obj["reasoning_effort"] != 5 {
		t.Fatalf("非字符串档位应透传: %v", obj)
	}
}

func TestFull7NormalizeRolesMissingRole(t *testing.T) {
	obj := map[string]any{"messages": []any{map[string]any{"content": "x"}}}
	normalizeRoles(obj)
	msg := obj["messages"].([]any)[0].(map[string]any)
	if _, has := msg["role"]; has {
		t.Fatalf("缺 role 的消息不应被改写: %v", msg)
	}
}

func TestFull7EnsureConsoleSystemEdges(t *testing.T) {
	if got := ensureConsoleSystem(nil); len(got) != 0 {
		t.Fatalf("空 body = %q", got)
	}
	if got := ensureConsoleSystem([]byte("not-json")); string(got) != "not-json" {
		t.Fatalf("坏 JSON = %q", got)
	}
	if got := ensureConsoleSystem([]byte(`{"messages":[]}`)); string(got) != `{"messages":[]}` {
		t.Fatalf("空 messages = %q", got)
	}
	in := `{"messages":[{"role":"system","content":"s"}]}`
	if got := ensureConsoleSystem([]byte(in)); string(got) != in {
		t.Fatalf("首条 system 不应注入: %q", got)
	}
}

func TestFull7NormalizeToolChoiceEdges(t *testing.T) {
	obj := map[string]any{"tool_choice": map[string]any{
		"type": "function", "function": map[string]any{}, "name": "foo",
	}}
	normalizeToolChoice(obj)
	if obj["tool_choice"] != "foo" {
		t.Fatalf("顶层 name 回退 = %v", obj["tool_choice"])
	}

	obj = map[string]any{"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": ""}}}
	normalizeToolChoice(obj)
	if obj["tool_choice"] != "auto" {
		t.Fatalf("无 name 应回落 auto = %v", obj["tool_choice"])
	}

	obj = map[string]any{"tool_choice": map[string]any{"type": "weird"}}
	normalizeToolChoice(obj)
	if _, has := obj["tool_choice"]; has {
		t.Fatalf("未知 type 应删除: %v", obj)
	}

	obj = map[string]any{"tool_choice": float64(5)}
	normalizeToolChoice(obj)
	if _, has := obj["tool_choice"]; has {
		t.Fatalf("非对象应删除: %v", obj)
	}
}

func TestFull7NormalizeToolPatternsNonMapTool(t *testing.T) {
	obj := map[string]any{"tools": []any{"x", map[string]any{
		"function": map[string]any{"parameters": map[string]any{"pattern": `a\_b`}},
	}}}
	normalizeToolPatterns(obj)
	tool := obj["tools"].([]any)[1].(map[string]any)
	params := tool["function"].(map[string]any)["parameters"].(map[string]any)
	if params["pattern"] != "a_b" {
		t.Fatalf("pattern = %v", params["pattern"])
	}
}
