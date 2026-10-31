// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestErrKindString 错误分类的稳定字符串名（日志/指标口径）。
func TestErrKindString(t *testing.T) {
	cases := map[ErrKind]string{
		ErrHardCredit:     "hard_credit",
		ErrSoftRate:       "soft_rate",
		ErrSessionDead:    "session_dead",
		ErrNotFound:       "not_found",
		ErrServer:         "server",
		ErrContentBlocked: "content_blocked",
		ErrBadParams:      "bad_params",
		ErrModelBlocked:   "model_blocked",
		ErrWafBlock:       "waf_block",
		ErrPromptTooLong:  "prompt_too_long",
		ErrImageInvalid:   "image_invalid",
		ErrAccountFault:   "account_fault",
		ErrUpstreamBusy:   "upstream_busy",
		ErrQueueWaiting:   "queue_waiting",
		ErrQueueUserLimit: "queue_user_limit",
		ErrClient:         "client",
		ErrNone:           "none",
		ErrKind(999):      "none",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Errorf("ErrKind(%d).String() = %q，应为 %q", int(kind), got, want)
		}
	}
}

// TestIsModelBlocked 11102「该后端无此模型」：只认 400/404、code 精确 11102 或
// 窄短语 service info not found；顶层与 error 子对象两层都看。
func TestIsModelBlocked(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"非 400/404 即使有 code", 429, `{"code":11102}`, false},
		{"空 body", 400, ``, false},
		{"无关 body", 400, `{"code":400,"msg":"bad request"}`, false},
		{"含 11102 但不是 JSON", 400, `11102`, false},
		{"顶层 code 数字", 400, `{"code":11102}`, true},
		{"顶层 code 字符串", 404, `{"code":"11102"}`, true},
		{"error 子对象 code", 400, `{"error":{"code":"11102"}}`, true},
		{"error_code 别名", 400, `{"error_code":11102}`, true},
		{"窄短语命中", 400, `{"error":{"message":"Service Info Not Found"}}`, true},
		{"msg 窄短语命中", 404, `{"code":0,"msg":"service info not found"}`, true},
		{"其他文案不命中", 400, `{"msg":"model not found"}`, false},
		{"11158 无权限", 400, `{"code":11158,"msg":"model [gpt-5.6-sol] is not allowed for user type [personal_free_user]"}`, true},
		{"11158 只有文案", 400, `{"msg":"model is not allowed for user type"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsModelBlocked(tc.status, tc.body); got != tc.want {
				t.Errorf("IsModelBlocked(%d, %q) = %v，应为 %v", tc.status, tc.body, got, tc.want)
			}
		})
	}

	// 负缓存原因：11158（码或文案）记成无权限，其余沿用 11102。
	for body, want := range map[string]string{
		`{"code":11158,"msg":"x"}`:                                   ModelForbiddenReason,
		`{"error":{"message":"model is not allowed for user type"}}`: ModelForbiddenReason,
		`{"code":11102,"msg":"service info not found"}`:              ModelBlockReason,
	} {
		if got := ModelBlockReasonFor(body); got != want {
			t.Errorf("ModelBlockReasonFor(%s) = %q，应为 %q", body, got, want)
		}
	}

	if SoftRateResetLoc().String() != "UTC+8" {
		t.Errorf("SoftRateResetLoc = %v", SoftRateResetLoc())
	}
	if _, off := time.Now().In(SoftRateResetLoc()).Zone(); off != 8*3600 {
		t.Errorf("UTC+8 偏移 = %d", off)
	}
}

// TestParseRetryAfter 限流等待时长解析：三个头的口径、优先级、非法与越界丢弃。
func TestParseRetryAfter(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name   string
		header map[string]string
		want   time.Duration
		ok     bool
	}{
		{"Retry-After 秒", map[string]string{"Retry-After": "120"}, 120 * time.Second, true},
		{"Retry-After 零丢弃", map[string]string{"Retry-After": "0"}, 0, false},
		{"Retry-After 非数字丢弃", map[string]string{"Retry-After": "abc"}, 0, false},
		{"Retry-After 超上限丢弃", map[string]string{"Retry-After": "999999"}, 0, false},
		{"Retry-After 超长丢弃", map[string]string{"Retry-After": "12345678901234567"}, 0, false},
		{"毫秒头", map[string]string{"Retry-After-Ms": "1500"}, 1500 * time.Millisecond, true},
		{"毫秒头超上限", map[string]string{"Retry-After-Ms": "9999999999"}, 0, false},
		{"第一个头非法时降级第二个", map[string]string{"Retry-After": "abc", "Retry-After-Ms": "2500"}, 2500 * time.Millisecond, true},
		{"X-Ratelimit-Reset epoch 秒", map[string]string{"X-Ratelimit-Reset": strconv.FormatInt(now+90, 10)}, 90 * time.Second, false /* 允许 ±1s，单独断言 */},
		{"X-Ratelimit-Reset 过去时刻丢弃", map[string]string{"X-Ratelimit-Reset": strconv.FormatInt(now-100, 10)}, 0, false},
		{"没有头", map[string]string{}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.header {
				h.Set(k, v)
			}
			got, ok := ParseRetryAfter(h)
			if tc.name == "X-Ratelimit-Reset epoch 秒" {
				if !ok || got < 88*time.Second || got > 92*time.Second {
					t.Errorf("got %v/%v，应约为 90s", got, ok)
				}
				return
			}
			if got != tc.want || ok != tc.ok {
				t.Errorf("got %v/%v，应为 %v/%v", got, ok, tc.want, tc.ok)
			}
		})
	}

	t.Run("X-Ratelimit-Reset 毫秒口径", func(t *testing.T) {
		h := http.Header{}
		h.Set("X-Ratelimit-Reset", strconv.FormatInt((now+120)*1000, 10))
		got, ok := ParseRetryAfter(h)
		if !ok || got < 118*time.Second || got > 122*time.Second {
			t.Errorf("got %v/%v，应约为 120s", got, ok)
		}
	})

	// isAllDigits 边界。
	for _, s := range []string{"", "12a", " 1", "1.5"} {
		if isAllDigits(s) {
			t.Errorf("isAllDigits(%q) = true", s)
		}
	}
	for _, s := range []string{"0", "123"} {
		if !isAllDigits(s) {
			t.Errorf("isAllDigits(%q) = false", s)
		}
	}
	// parseRetryNumber 超 16 位直接非法。
	if _, ok := parseRetryNumber("12345678901234567", "Retry-After"); ok {
		t.Error("超 16 位应非法")
	}
}

// TestDoJSONErrors 信封解码失败语义：传输错误、读 body 失败、HTTP 4xx、非 JSON、
// 业务 code 非 0。
func TestDoJSONErrors(t *testing.T) {
	newReq := func(t *testing.T) *http.Request {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, upBase+"/x", nil)
		if err != nil {
			t.Fatalf("建请求: %v", err)
		}
		return req
	}
	t.Run("传输错误", func(t *testing.T) {
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial boom")
		})}}
		if _, err := c.doJSON(newReq(t)); err == nil || !strings.Contains(err.Error(), "dial boom") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("读 body 失败", func(t *testing.T) {
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(errReader{})}, nil
		})}}
		if _, err := c.doJSON(newReq(t)); err == nil || !strings.Contains(err.Error(), "read body") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("HTTP 4xx", func(t *testing.T) {
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return jsonResp(400, `{"code":400,"msg":"bad"}`), nil
		})}}
		_, err := c.doJSON(newReq(t))
		var ue *Error
		if !errors.As(err, &ue) || ue.Status != 400 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("响应非 JSON", func(t *testing.T) {
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `oops`), nil
		})}}
		if _, err := c.doJSON(newReq(t)); err == nil || !strings.Contains(err.Error(), "parse failed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("业务 code 非 0", func(t *testing.T) {
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{"code":7,"msg":"biz boom"}`), nil
		})}}
		_, err := c.doJSON(newReq(t))
		if err == nil || !strings.Contains(err.Error(), "biz boom") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestRefreshTokenEdges 刷新令牌的边界：无 refreshToken、URL 非法、响应缺
// accessToken、并发刷新让位、超长 expiresIn 保留旧值。
func TestRefreshTokenEdges(t *testing.T) {
	t.Run("无 refreshToken", func(t *testing.T) {
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			t.Error("不应发请求")
			return jsonResp(200, `{}`), nil
		})}, ChatBaseCN: upBase}
		if err := c.RefreshToken(&auth.Auth{AccessToken: "at"}); err == nil || !strings.Contains(err.Error(), "no refreshToken") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("URL 非法", func(t *testing.T) {
		c := &Client{HTTP: newUpFake(t).client(), ChatBaseCN: "://bad"}
		if err := c.RefreshToken(&auth.Auth{AccessToken: "at", RefreshToken: "rt"}); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})

	t.Run("响应缺 accessToken", func(t *testing.T) {
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{"code":0,"data":{"refreshToken":"rt2"}}`), nil
		})}, ChatBaseCN: upBase}
		err := c.RefreshToken(&auth.Auth{AccessToken: "at", RefreshToken: "rt"})
		if err == nil || !strings.Contains(err.Error(), "no accessToken") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("并发刷新后本次让位", func(t *testing.T) {
		a := &auth.Auth{AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 111}
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			// 模拟锁外期间另一 goroutine 完成刷新。
			a.AccessToken = "at1"
			a.RefreshToken = "rt1"
			return jsonResp(200, `{"code":0,"data":{"accessToken":"new-at","refreshToken":"new-rt","expiresIn":3600}}`), nil
		})}, ChatBaseCN: upBase}
		if err := c.RefreshToken(a); err != nil {
			t.Fatalf("RefreshToken: %v", err)
		}
		if a.AccessToken != "at1" || a.RefreshToken != "rt1" || a.ExpiresAt != 111 {
			t.Errorf("并发刷新后不应覆盖: %+v", a)
		}
	})

	t.Run("正常写回与超长 expiresIn", func(t *testing.T) {
		a := &auth.Auth{AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 222}
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/v2/plugin/auth/token/refresh" && r.Header.Get("X-Refresh-Token") != "rt0" {
				t.Errorf("X-Refresh-Token = %q", r.Header.Get("X-Refresh-Token"))
			}
			return jsonResp(200, `{"code":0,"data":{"accessToken":"new-at","refreshToken":"new-rt","expiresIn":999999999999,"domain":"www.workbuddy.ai"}}`), nil
		})}, ChatBaseCN: upBase}
		if err := c.RefreshToken(a); err != nil {
			t.Fatalf("RefreshToken: %v", err)
		}
		if a.AccessToken != "new-at" || a.RefreshToken != "new-rt" || a.Domain != "www.workbuddy.ai" {
			t.Errorf("写回 = %+v", a)
		}
		if a.ExpiresAt != 222 {
			t.Errorf("超长 expiresIn 应保留旧过期时间，got %d", a.ExpiresAt)
		}
	})
}

// TestChatStreamGlobalAndErrors chat 出站：global 账号固定 /v2 并前置兜底 system；
// 传输错误清空闲连接池；非法 base 在构造请求时失败；4xx 读 body 失败归传输层错误。
func TestChatStreamGlobalAndErrors(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"model":    "fast-model",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"stream":   true,
	})
	t.Run("global 走 /v2 且前置 system", func(t *testing.T) {
		var gotPath string
		var gotBody []byte
		c := &Client{
			HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
				gotPath = r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				return jsonResp(200, "data: {}\n\n"), nil
			})},
			GlobalEnabled:  true,
			ChatBaseGlobal: upBase,
		}
		rc, status, _, err := c.ChatStream(globalAuth(), body, "", ChatMeta{})
		if err != nil || status != 200 || rc == nil {
			t.Fatalf("status=%d err=%v", status, err)
		}
		rc.Close()
		if gotPath != chatCompletionsPath {
			t.Errorf("path = %q，应为 %q", gotPath, chatCompletionsPath)
		}
		var sent struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(gotBody, &sent); err != nil {
			t.Fatalf("出站体: %v", err)
		}
		if len(sent.Messages) < 2 || sent.Messages[0]["role"] != "system" {
			t.Errorf("global 应前置 system 兜底: %v", sent.Messages)
		}
	})

	t.Run("传输错误并清空闲池", func(t *testing.T) {
		rt := &idleClosingRT{}
		c := &Client{HTTP: &http.Client{Transport: rt}, ChatBaseCN: upBase}
		_, _, _, err := c.ChatStream(upAuth(), body, "", ChatMeta{})
		if err == nil {
			t.Fatal("传输错误应返回错误")
		}
		if rt.closed != 1 {
			t.Errorf("CloseIdleConnections 调用 %d 次，应为 1", rt.closed)
		}
	})

	t.Run("非法 base", func(t *testing.T) {
		c := &Client{HTTP: newUpFake(t).client(), ChatBaseCN: "://bad"}
		if _, _, _, err := c.ChatStream(upAuth(), body, "", ChatMeta{}); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})

	t.Run("4xx 读 body 失败", func(t *testing.T) {
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 400, Body: io.NopCloser(errReader{})}, nil
		})}, ChatBaseCN: upBase}
		_, status, _, err := c.ChatStream(upAuth(), body, "", ChatMeta{})
		if status != 0 || err == nil || !strings.Contains(err.Error(), "read body") {
			t.Fatalf("status=%d err=%v", status, err)
		}
	})
}

// TestPromoClock 优惠时段 "HH:MM" 解析。
func TestPromoClock(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"23:00", 23 * 60, true},
		{"7:50", 7*60 + 50, true},
		{" 7 : 5 ", 7*60 + 5, true},
		{"00:00", 0, true},
		{"24:00", 24 * 60, true},
		{"25:00", -1, false},
		{"12:60", -1, false},
		{"-1:00", -1, false},
		{"12", -1, false},
		{"ab:cd", -1, false},
		{"1:2:3", -1, false},
	}
	for _, tc := range cases {
		got, ok := promoClock(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("promoClock(%q) = %d/%v，应为 %d/%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestPromoActive 优惠生效判定：开关、有效期、普通/跨午夜每日窗口、坏时段跳过。
func TestPromoActive(t *testing.T) {
	mk := func(mut func(*v3ModelPromotion)) *v3ModelPromotion {
		p := &v3ModelPromotion{Enabled: true}
		mut(p)
		return p
	}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 19, h, m, 0, 0, promoZone) }

	if promoActive(mk(func(p *v3ModelPromotion) { p.Enabled = false }), at(10, 0)) {
		t.Error("未启用应不生效")
	}
	if !promoActive(mk(func(*v3ModelPromotion) {}), at(10, 0)) {
		t.Error("无 schedule 应全天生效")
	}

	if promoActive(mk(func(p *v3ModelPromotion) {
		p.Schedule = &struct {
			Daily []struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"daily"`
			Timezone   string `json:"timezone"`
			ValidFrom  string `json:"validFrom"`
			ValidUntil string `json:"validUntil"`
		}{ValidFrom: "2999-01-01T00:00:00Z"}
	}), at(10, 0)) {
		t.Error("validFrom 未来应不生效")
	}
	if promoActive(mk(func(p *v3ModelPromotion) {
		p.Schedule = &struct {
			Daily []struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"daily"`
			Timezone   string `json:"timezone"`
			ValidFrom  string `json:"validFrom"`
			ValidUntil string `json:"validUntil"`
		}{ValidUntil: "2000-01-01T00:00:00Z"}
	}), at(10, 0)) {
		t.Error("validUntil 已过应不生效")
	}
	// 坏日期串解析失败视为不限制。
	if !promoActive(mk(func(p *v3ModelPromotion) {
		p.Schedule = &struct {
			Daily []struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"daily"`
			Timezone   string `json:"timezone"`
			ValidFrom  string `json:"validFrom"`
			ValidUntil string `json:"validUntil"`
		}{ValidFrom: "bad", ValidUntil: "bad"}
	}), at(10, 0)) {
		t.Error("坏日期串不应限制")
	}

	normal := mk(func(p *v3ModelPromotion) {
		p.Schedule = &struct {
			Daily []struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"daily"`
			Timezone   string `json:"timezone"`
			ValidFrom  string `json:"validFrom"`
			ValidUntil string `json:"validUntil"`
		}{}
		p.Schedule.Daily = append(p.Schedule.Daily, struct {
			Start string `json:"start"`
			End   string `json:"end"`
		}{"09:00", "18:00"})
	})
	if !promoActive(normal, at(10, 0)) {
		t.Error("普通窗口内应生效")
	}
	if promoActive(normal, at(20, 0)) {
		t.Error("普通窗口外应不生效")
	}
	if promoActive(normal, at(18, 0)) {
		t.Error("窗口右边界不含")
	}

	cross := mk(func(p *v3ModelPromotion) {
		p.Schedule = &struct {
			Daily []struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"daily"`
			Timezone   string `json:"timezone"`
			ValidFrom  string `json:"validFrom"`
			ValidUntil string `json:"validUntil"`
		}{}
		p.Schedule.Daily = append(p.Schedule.Daily, struct {
			Start string `json:"start"`
			End   string `json:"end"`
		}{"23:00", "07:50"})
	})
	for _, tc := range []struct {
		at   time.Time
		want bool
	}{
		{at(23, 30), true},
		{at(3, 0), true},
		{at(12, 0), false},
		{at(7, 50), false},
	} {
		if got := promoActive(cross, tc.at); got != tc.want {
			t.Errorf("跨午夜窗口 %s = %v，应为 %v", tc.at.Format("15:04"), got, tc.want)
		}
	}

	badClock := mk(func(p *v3ModelPromotion) {
		p.Schedule = &struct {
			Daily []struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"daily"`
			Timezone   string `json:"timezone"`
			ValidFrom  string `json:"validFrom"`
			ValidUntil string `json:"validUntil"`
		}{}
		p.Schedule.Daily = append(p.Schedule.Daily, struct {
			Start string `json:"start"`
			End   string `json:"end"`
		}{"bad", "18:00"})
	})
	if promoActive(badClock, at(10, 0)) {
		t.Error("坏时段应跳过、无有效窗口则不生效")
	}
}

// TestApplyModelPromotions 目录挂优惠：同模型取 priority 最高；目录外/未启用/
// 未到生效窗口忽略；badge/hover/discount 分别挂载。
func TestApplyModelPromotions(t *testing.T) {
	out := map[string]ModelInfo{
		"m1": {ID: "m1"},
		"m2": {ID: "m2"},
		"m3": {ID: "m3"},
	}
	mk := func(mut func(*v3ModelPromotion)) v3ModelPromotion {
		p := v3ModelPromotion{Enabled: true}
		mut(&p)
		return p
	}
	promos := []v3ModelPromotion{
		mk(func(p *v3ModelPromotion) {
			p.Priority = 50
			p.ModelIDs = []string{"m1"}
			p.Badge = &struct {
				Label string `json:"label"`
			}{"低优先"}
		}),
		mk(func(p *v3ModelPromotion) {
			p.Priority = 100
			p.ModelIDs = []string{"m1"}
			p.Badge = &struct {
				Label string `json:"label"`
			}{"夜间五折"}
			p.Hover = &struct {
				TextZh string `json:"textZh"`
			}{"23:00–07:50 五折"}
			p.Discount = &struct {
				DiscountedCredits string  `json:"discountedCredits"`
				Factor            float64 `json:"factor"`
			}{"0.50x", 0.5}
		}),
		mk(func(p *v3ModelPromotion) {
			p.ModelIDs = []string{"m2"}
			p.Enabled = false
		}),
		mk(func(p *v3ModelPromotion) {
			p.ModelIDs = []string{"not-in-catalog"}
			p.Badge = &struct {
				Label string `json:"label"`
			}{"x"}
		}),
		mk(func(p *v3ModelPromotion) {
			p.ModelIDs = []string{"m3"}
			p.Badge = &struct {
				Label string `json:"label"`
			}{"全天"}
		}),
		mk(func(p *v3ModelPromotion) {
			p.ModelIDs = []string{"m3"}
			p.Priority = 999
			p.Schedule = &struct {
				Daily []struct {
					Start string `json:"start"`
					End   string `json:"end"`
				} `json:"daily"`
				Timezone   string `json:"timezone"`
				ValidFrom  string `json:"validFrom"`
				ValidUntil string `json:"validUntil"`
			}{ValidFrom: "2999-01-01T00:00:00Z"}
		}),
	}
	applyModelPromotions(out, promos)

	if out["m1"].PromoLabel != "夜间五折" || out["m1"].PromoNote != "23:00–07:50 五折" ||
		out["m1"].PromoCredits != "0.50x" || out["m1"].PromoFactor == nil || *out["m1"].PromoFactor != 0.5 {
		t.Errorf("m1 = %+v", out["m1"])
	}
	if out["m2"].PromoLabel != "" || out["m2"].PromoFactor != nil {
		t.Errorf("未启用优惠不应挂载: %+v", out["m2"])
	}
	if out["m3"].PromoLabel != "全天" {
		t.Errorf("m3 = %+v", out["m3"])
	}
	if _, ok := out["not-in-catalog"]; ok {
		t.Error("目录外模型不应被创建")
	}

	// 空输入直接返回。
	applyModelPromotions(nil, promos)
	applyModelPromotions(out, nil)
}

// TestNormalizeModelRate 倍率原文规范化与生效倍率选择。
func TestNormalizeModelRate(t *testing.T) {
	cases := map[string]string{
		"x0.05":         "0.05",
		"x0.05 credits": "0.05",
		"0.50x":         "0.5",
		" 2 ":           "2",
		"free":          "free",
		"":              "",
		"credits":       "",
	}
	for in, want := range cases {
		if got := normalizeModelRate(in); got != want {
			t.Errorf("normalizeModelRate(%q) = %q，应为 %q", in, got, want)
		}
	}
	f := 0.25
	mi := ModelInfo{Credits: "x1.0", PromoCredits: "0.25x", PromoFactor: &f}
	if got := effectiveModelRate(mi); got != "0.25" {
		t.Errorf("有优惠时应取折扣价，got %q", got)
	}
	mi.PromoFactor = nil
	if got := effectiveModelRate(mi); got != "1" {
		t.Errorf("无优惠时应取牌价，got %q", got)
	}
}

// TestStoreEffortsEmptyDeletes 空桶写入删除该 realm 的 effort 缓存。
func TestStoreEffortsEmptyDeletes(t *testing.T) {
	c := New()
	c.storeEfforts("global", map[string][]string{"m": {"low"}}, map[string]string{"m": "low"})
	if e, _ := c.GlobalEffortSnapshot(); len(e) == 0 {
		t.Fatal("应先写入桶")
	}
	c.storeEfforts("global", nil, nil)
	e, d := c.GlobalEffortSnapshot()
	if e != nil || d != nil {
		t.Errorf("空桶写入应删除该 realm：%v/%v", e, d)
	}
}

// TestFetchV3ConfigTrialBanner /v3/config 解析：试用横幅继承目标模型能力但清空
// 计费/标签；重复/空 id 跳过；限时优惠挂载；空目录报错。
func TestFetchV3ConfigTrialBanner(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, gmV3Path, 200, `{"code":0,"data":{
		"models":[
			{"id":"hy4-preview","maxInputTokens":1000,"maxOutputTokens":100,"credits":"x0.29","tags":["badge:free"]},
			{"id":"normal","maxInputTokens":2000}
		],
		"productFeaturesConfig":{"ModelTrialBanner":{"banners":[
			{"modelId":"hy4-preview-f","targetModelId":"hy4-preview"},
			{"modelId":"hy4-preview-f"},
			{"modelId":"  "},
			{"modelId":"bare-trial"}
		]}},
		"modelPromotions":[{"enabled":true,"priority":1,"modelIds":["normal"],
			"badge":{"label":"限时免费"},"discount":{"discountedCredits":"0x","factor":0}}]
	}}`)
	c := upGlobalClient(f)
	c.UserAgent = "test-ua"
	out, err := c.fetchV3ConfigModelMap(globalAuth())
	if err != nil {
		t.Fatalf("fetchV3ConfigModelMap: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("模型数 = %d，应为 4: %v", len(out), out)
	}
	trial, ok := out["hy4-preview-f"]
	if !ok || trial.ContextWindow != 1000 || trial.MaxTokens != 100 || trial.Credits != "" || trial.Tags != nil {
		t.Errorf("试用横幅应继承能力但清空计费/标签: %+v/%v", trial, ok)
	}
	if _, ok := out["bare-trial"]; !ok {
		t.Error("无 targetModelId 的横幅也应入目录")
	}
	n := out["normal"]
	if n.PromoLabel != "限时免费" || n.PromoCredits != "0x" || n.PromoFactor == nil || *n.PromoFactor != 0 {
		t.Errorf("限时优惠 = %+v", n)
	}
	r := f.seen()[0]
	if r.method != http.MethodGet || r.path != gmV3Path {
		t.Errorf("请求 = %s %s", r.method, r.path)
	}
	if r.header.Get("User-Agent") != "test-ua" || r.header.Get("X-User-Id") != "u-global" {
		t.Errorf("请求头 = %v", r.header)
	}

	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP 400", 400, `{"code":400,"msg":"boom"}`},
		{"业务 code 非 0", 200, `{"code":7,"msg":"boom"}`},
		{"非 JSON", 200, `oops`},
		{"空目录", 200, `{"code":0,"data":{"models":[]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodGet, gmV3Path, tc.status, tc.body)
			if _, err := upGlobalClient(f).fetchV3ConfigModelMap(globalAuth()); err == nil {
				t.Fatal("应返回错误")
			}
		})
	}

	t.Run("读 body 失败", func(t *testing.T) {
		c := &Client{
			HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(errReader{})}, nil
			})},
			GlobalEnabled:  true,
			ChatBaseGlobal: upBase,
		}
		if _, err := c.fetchV3ConfigModelMap(globalAuth()); err == nil || !strings.Contains(err.Error(), "read body") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("URL 非法", func(t *testing.T) {
		c := &Client{HTTP: newUpFake(t).client(), GlobalEnabled: true, ChatBaseGlobal: "://bad"}
		if _, err := c.fetchV3ConfigModelMap(globalAuth()); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
}

// TestCreditPackageBranches 逐包构成解析：扣减窗口时间、三种到期字段回退、额度与剩余
// （剩余超过额度按额度算）、缺字段当 0 与面额降序。
func TestCreditPackageBranches(t *testing.T) {
	deduct := time.Now().Add(48 * time.Hour).UnixMilli()
	created := time.Now().Add(-24 * time.Hour).UnixMilli()
	f := newUpFake(t)
	f.onResources("",
		`{"PackageName":"deduct","DeductionEndTime":`+strconv.FormatInt(deduct, 10)+`,"CreateTime":`+strconv.FormatInt(created, 10)+`,
		 "CycleCapacityRemainPrecise":"10","CycleCapacitySizePrecise":"30"}`,
		`{"PackageName":"expired","ExpiredTime":"2026-10-24 00:00:00","CycleCapacityRemainPrecise":"5","CycleCapacitySizePrecise":"5"}`,
		`{"PackageName":"nocycle","CycleCapacityRemainPrecise":"6","CycleCapacitySizePrecise":"6"}`,
		`{"PackageName":"cycle","CycleCapacitySizePrecise":"100","CycleCapacityRemainPrecise":"120"}`,
		`{"PackageName":"nosize","CycleCapacityRemainPrecise":"7"}`,
		`{}`)
	c := upClient(f)
	packs, remain, size, err := c.CreditPackages(upAuth())
	if err != nil {
		t.Fatalf("CreditPackages: %v", err)
	}
	if len(packs) != 6 {
		t.Fatalf("packs = %d", len(packs))
	}
	byName := map[string]CreditPackage{}
	for _, p := range packs {
		byName[p.Name] = p
	}
	if d := byName["deduct"]; d.ExpiresAt != deduct || d.EndTime == "" || d.CreatedAt == "" || d.Used != 20 {
		t.Errorf("deduct = %+v", d)
	}
	if e := byName["expired"]; !strings.HasPrefix(e.EndTime, "2026-10-24") || e.ExpiresAt == 0 {
		t.Errorf("expired = %+v", e)
	}
	if p := byName["nocycle"]; p.EndTime != "" || p.ExpiresAt != 0 {
		t.Errorf("没有到期字段的包不该有到期时刻: %+v", p)
	}
	if cy := byName["cycle"]; !cy.Cycle || cy.Remain != 100 || cy.Size != 100 || cy.Used != 0 {
		t.Errorf("cycle = %+v（剩余超过额度应按额度算）", cy)
	}
	if n := byName["nosize"]; n.Remain != 0 || n.Size != 0 {
		t.Errorf("nosize = %+v（没有额度时剩余也按 0 算）", n)
	}
	if remain != 10+5+6+100 || size != 30+5+6+100 {
		t.Errorf("合集 remain/size = %d/%d", remain, size)
	}
	// 面额降序：cycle(100) 在 nosize(0) 之前。
	if packs[0].Size < packs[len(packs)-1].Size {
		t.Errorf("应按面额降序: %+v", packs)
	}
}

// TestPackageExpiryByType 到期时刻照桌面端按包类型取：一次性发放的包看 DeductionEndTime，
// 每日免费包和按周期刷新的包看 CycleEndTime（刷新的包标 refresh），缺字段时依次回落；国际版另把
// freeMon、gift 算作一次性的。
func TestPackageExpiryByType(t *testing.T) {
	deduct := time.Date(2026, 11, 20, 23, 59, 59, 0, softRateResetLoc)
	cycle := "2026-11-01 00:00:00"
	both := `"DeductionEndTime":` + strconv.FormatInt(deduct.UnixMilli(), 10) + `,"CycleEndTime":"` + cycle + `"`
	cases := []struct {
		name, code, fields string
		want               string // 到期日（北京时间）
		refresh            bool
		global             bool
	}{
		{"活动包看 DeductionEndTime", "TCACA_code_007_nzdH5h4Nl0", both, "2026-11-20", false, false},
		{"加油包看 DeductionEndTime", "TCACA_code_009_0XmEQc2xOf", both, "2026-11-20", false, false},
		{"一次性的包缺 DeductionEndTime 回落 ExpiredTime", "TCACA_code_007_nzdH5h4Nl0", `"ExpiredTime":"2026-11-05 00:00:00","CycleEndTime":"` + cycle + `"`, "2026-11-05", false, false},
		{"一次性的包只有 CycleEndTime", "TCACA_code_036_lupO5WgNdG", `"CycleEndTime":"` + cycle + `"`, "2026-11-01", false, false},
		{"每日免费包看 CycleEndTime", "TCACA_code_001_PqouKr6QWV", both, "2026-11-01", false, false},
		{"体验包看刷新时间", "TCACA_code_039_KRcQj7wUat", both, "2026-11-01", true, false},
		{"国际版 Bonus Pack 看刷新时间", "TCACA_code_037_WxOD3MpI2o", both, "2026-11-01", true, false},
		{"刷新的包缺 CycleEndTime 回落 DeductionEndTime", "TCACA_code_002_AkiJS3ZHF5", `"DeductionEndTime":` + strconv.FormatInt(deduct.UnixMilli(), 10), "2026-11-20", true, false},
		{"国内版 freeMon 看刷新时间", "TCACA_code_008_cfWoLwvjU4", both, "2026-11-01", true, false},
		{"国际版 freeMon 是一次性的", "TCACA_code_008_cfWoLwvjU4", both, "2026-11-20", false, true},
		{"国内版 gift 看刷新时间", "TCACA_code_006_DbXS0lrypC", both, "2026-11-01", true, false},
		{"国际版 gift 是一次性的", "TCACA_code_006_DbXS0lrypC", both, "2026-11-20", false, true},
		{"国际版体验包照样看刷新时间", "TCACA_code_039_KRcQj7wUat", both, "2026-11-01", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r resourceAccount
			if err := json.Unmarshal([]byte(`{"PackageCode":"`+tc.code+`",`+tc.fields+`}`), &r); err != nil {
				t.Fatal(err)
			}
			r.global = tc.global
			_, at, ok := r.expiry()
			if !ok || at.In(softRateResetLoc).Format(time.DateOnly) != tc.want {
				t.Fatalf("expiry = %v %v，want %s", at, ok, tc.want)
			}
			if r.refreshes() != tc.refresh {
				t.Fatalf("refreshes = %v，want %v", r.refreshes(), tc.refresh)
			}
		})
	}
}

// TestParsePackageEndTime 到期时间字符串解析（UTC+8 墙钟）。
func TestParsePackageEndTime(t *testing.T) {
	if _, ok := parsePackageEndTime("2026-10-24 00:00:00"); !ok {
		t.Error("应解析成功")
	}
	if _, ok := parsePackageEndTime("bad"); ok {
		t.Error("非法格式应失败")
	}
	if _, ok := parsePackageEndTime(""); ok {
		t.Error("空串应失败")
	}
}

// TestTruncate 截断辅助：短串原样、超长按字节截断。
func TestTruncate(t *testing.T) {
	if got := truncate("abc", 5); got != "abc" {
		t.Errorf("短串 = %q", got)
	}
	if got := truncate("abcdef", 3); got != "abc" {
		t.Errorf("超长 = %q", got)
	}
}
