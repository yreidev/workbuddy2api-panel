// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"strconv"
	"testing"
)

func TestBizCode(t *testing.T) {
	cases := []struct {
		body string
		want int
	}{
		{`{"code":14003,"msg":"too many requests"}`, 14003},
		{`{"code":"6004","msg":"x"}`, 6004},
		{`{"code":" 10105 "}`, 10105},
		{`{"code":0,"data":{"code":14018}}`, 14018},
		{`{"error":{"message":"quota","code":14001}}`, 14001},
		{`{"response":{"data":{"code":14015}}}`, 14015},
		{`{"code":429,"error":{"code":14003}}`, 14003}, // 状态码一类的小数字不算业务码
		{`{"code":14003.5}`, 0},
		{`{"code":"abc"}`, 0},
		{`{"msg":"no code"}`, 0},
		{`not json "code"`, 0},
		{``, 0},
		{`{"errors":[{"code":14018}]}`, 0}, // 官方也不看数组
	}
	for _, c := range cases {
		if got := BizCode(c.body); got != c.want {
			t.Errorf("BizCode(%s)=%d want %d", c.body, got, c.want)
		}
	}
}

// TestClassifyBizCodes 业务码先于状态码与关键词（码表照官方客户端）。
func TestClassifyBizCodes(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   ErrKind
	}{
		{429, `{"code":14003,"msg":"too many requests"}`, ErrSoftRate},
		{400, `{"code":14003,"msg":"too many requests"}`, ErrSoftRate},
		{429, `{"code":6001}`, ErrSoftRate},
		{400, `{"code":6006}`, ErrSoftRate},
		{200, `{"code":6008}`, ErrSoftRate},
		{400, `{"code":10105,"msg":"conversation limit"}`, ErrSoftRate},
		{429, `{"code":14001,"msg":"usage limit exceeded"}`, ErrHardCredit},
		{403, `{"code":14012}`, ErrHardCredit},
		{429, `{"code":14013}`, ErrHardCredit},
		{429, `{"code":14014}`, ErrHardCredit},
		{400, `{"code":14018}`, ErrHardCredit},
		{429, `{"code":14019}`, ErrHardCredit},
		{429, `{"code":14022,"msg":"department usage limit exceeded"}`, ErrHardCredit},
		{403, `{"code":14023}`, ErrHardCredit},
		{429, `{"code":14015,"msg":"license expired"}`, ErrAccountFault},
		{429, `{"code":14016}`, ErrAccountFault},
		{403, `{"code":14017}`, ErrAccountFault},
		// 码表外的码照旧走状态码与关键词
		{429, `{"code":14002,"msg":"x"}`, ErrSoftRate},
		{400, `{"code":11141,"msg":"x"}`, ErrClient},
	}
	for _, c := range cases {
		if got := Classify(c.status, c.body); got != c.want {
			t.Errorf("Classify(%d, %s)=%s want %s", c.status, c.body, got, c.want)
		}
	}
}

// TestClassifyPromptTooLongOfficialMarkers 上下文超限照官方 isInputLengthError 的词表与正则。
func TestClassifyPromptTooLongOfficialMarkers(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
	}{
		{400, `{"code":11115,"msg":"x"}`},
		{400, `{"code": "11115"}`},
		{400, `{"error":{"message":"Input length too long"}}`},
		{400, `{"error":{"message":"This model's maximum context length is 131072 tokens"}}`},
		{400, `{"msg":"too many total text bytes"}`}, // 不能被限流词 "too many" 抢走
		{413, `<html><title>413 Request Entity Too Large</title></html>`},
		{404, `{"msg":"Prompt is too long"}`},
		{400, `{"msg":"input tokens 300000 exceeds the limit"}`},
		{400, `{"msg":"Input length 200001 exceeds max"}`},
	} {
		if got := Classify(c.status, c.body); got != ErrPromptTooLong {
			t.Errorf("Classify(%d, %s)=%s want prompt_too_long", c.status, c.body, got)
		}
	}
	// 429 仍按限流，5xx 仍按服务端故障
	if got := Classify(429, `{"msg":"maximum context length"}`); got != ErrSoftRate {
		t.Errorf("429 + 超限文案=%s want soft_rate", got)
	}
	if got := Classify(500, `{"msg":"maximum context length"}`); got != ErrServer {
		t.Errorf("500 + 超限文案=%s want server", got)
	}
}

func TestRateLimitPredicates(t *testing.T) {
	for code := 5999; code <= 6009; code++ {
		body := `{"code":` + strconv.Itoa(code) + `}`
		if got, want := IsModelRateLimit(body), code >= 6000 && code <= 6008; got != want {
			t.Errorf("IsModelRateLimit(%d)=%v want %v", code, got, want)
		}
	}
	for code, want := range map[int]bool{14003: true, 6001: true, 6002: true, 6005: true, 6006: true, 6003: false, 6004: false, 6007: false, 6008: false, 10105: false} {
		if got := IsTransientRateLimit(`{"code":` + strconv.Itoa(code) + `}`); got != want {
			t.Errorf("IsTransientRateLimit(%d)=%v want %v", code, got, want)
		}
	}
	if !IsActiveSessionLimit(`{"code":10105}`) || IsActiveSessionLimit(`{"code":14003}`) {
		t.Error("IsActiveSessionLimit 判错")
	}
}

func TestBizCodeReasons(t *testing.T) {
	for body, want := range map[string]string{
		`{"code":14003}`:  "14003 model busy",
		`{"code":6004}`:   "6004 model rate limit",
		`{"code":6006}`:   "6006 model rate limit",
		`{"code":10105}`:  "10105 too many active sessions",
		`{"code":11140}`:  "429 rate limit",
		`{"msg":"x"}`:     "429 rate limit",
		`not json at all`: "429 rate limit",
	} {
		if got := RateLimitReason(body); got != want {
			t.Errorf("RateLimitReason(%s)=%q want %q", body, got, want)
		}
	}
	for body, want := range map[string]string{
		`{"code":14015}`: "account fault (14015 license expired)",
		`{"code":14016}`: "account fault (14016 enterprise not activated)",
		`{"code":14017}`: "account fault (14017)",
		`{"msg":"The trial version is not yet activated"}`: "account fault (14017)",
	} {
		if got := AccountFaultReason(body); got != want {
			t.Errorf("AccountFaultReason(%s)=%q want %q", body, got, want)
		}
	}
}

func TestFrameKindBizCodes(t *testing.T) {
	cases := map[string]ErrKind{
		`{"error":{"message":"busy","code":14003}}`:  ErrSoftRate,
		`{"code":6004,"msg":"x"}`:                    ErrSoftRate,
		`{"error":{"message":"quota","code":14001}}`: ErrHardCredit,
		`{"error":{"message":"quota","code":14023}}`: ErrHardCredit,
		`{"error":{"message":"x","code":14016}}`:     ErrAccountFault,
		`{"error":{"message":"x","code":6021}}`:      ErrUpstreamBusy,
		`{"error":{"message":"x","code":6020}}`:      ErrQueueWaiting,
		`{"error":{"message":"x","code":6022}}`:      ErrQueueUserLimit,
	}
	for payload, want := range cases {
		if got := FrameKind(payload); got != want {
			t.Errorf("FrameKind(%s)=%s want %s", payload, got, want)
		}
	}
}

func TestGatewayHintSoftRateByCode(t *testing.T) {
	for body, want := range map[string]string{
		`{"code":14003}`: "upstream model is busy; retry in a few seconds or switch model",
		`{"code":6002}`:  "upstream model is busy; retry in a few seconds or switch model",
		`{"code":6004}`:  "usage limit of this model reached at upstream; switch model or retry after reset",
		`{"code":10105}`: "too many active sessions on the upstream account; retry shortly",
		`{"msg":"x"}`:    "rate limited by upstream; retry after reset",
	} {
		if got := GatewayHint(ErrSoftRate, body, HintContext{}); got != want {
			t.Errorf("GatewayHint(%s)=%q want %q", body, got, want)
		}
	}
}
