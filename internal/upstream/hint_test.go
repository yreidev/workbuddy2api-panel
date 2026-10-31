// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"strings"
	"testing"
)

// TestGatewayHintKindTable 每个 ErrKind 映射到固定的英文补充说明；未覆盖形态返回空串。
func TestGatewayHintKindTable(t *testing.T) {
	cases := []struct {
		kind ErrKind
		want string
	}{
		{ErrPromptTooLong, "request context exceeds the model's limit; reduce history/message size"},
		{ErrImageInvalid, "image request was rejected by upstream; check image_url format and image data"},
		{ErrWafBlock, "upstream WAF blocked the gateway; retry after the block window"},
		{ErrSoftRate, "rate limited by upstream; retry after reset"},
		{ErrAccountFault, "account-level fault at upstream (auth/quota state); the gateway will rotate or disable this account"},
		{ErrSessionDead, "account session expired at upstream; the account is disabled until re-login"},
		{ErrHardCredit, "account credits exhausted at upstream; waiting for daily check-in to restore"},
		{ErrModelBlocked, "upstream has no such model on this backend; switch model or retry on another account"},
		{ErrContentBlocked, "request content was rejected by content policy; adjust the prompt and retry"},
		{ErrUpstreamBusy, "upstream capacity is full (queue.full); this is a channel-level issue, not an account problem; retry after a short wait"},
		{ErrQueueWaiting, "upstream queued this request for capacity (6020) and no slot came up in time; this is not an account problem; retry after a short wait"},
		{ErrQueueUserLimit, "this upstream account has too many requests waiting in the capacity queue (6022); the gateway tried other accounts; retry after a short wait"},
	}
	for _, tc := range cases {
		t.Run(tc.kind.String(), func(t *testing.T) {
			if got := GatewayHint(tc.kind, "plain upstream message", HintContext{}); got != tc.want {
				t.Errorf("GatewayHint(%s) = %q，应为 %q", tc.kind, got, tc.want)
			}
		})
	}

	for _, kind := range []ErrKind{ErrNone, ErrNotFound, ErrServer, ErrBadParams, ErrClient} {
		if got := GatewayHint(kind, "plain upstream message", HintContext{}); got != "" {
			t.Errorf("未覆盖形态 %s 应无 hint，got %q", kind, got)
		}
	}

	// 本地调度错误有自己的固定文案（不带上游 msg）。
	if h := NoHealthyAccountHint(); h == "" || !strings.Contains(h, "no healthy account") {
		t.Errorf("NoHealthyAccountHint = %q", h)
	}
}

// TestGatewayHint11133 11133 模型参数非法家族：code/文案多种形态都识别；
// 只有「请求带图 + 目录确认不支持图片」才给换模型指向，否则退中性参数说明。
func TestGatewayHint11133(t *testing.T) {
	const neutral = "request parameters were rejected by the model provider; check message format and model capabilities"
	bodies := []string{
		`{"code":11133,"msg":"bad"}`,
		`{"code": 11133}`,
		`{"code":"11133"}`,
		`{"code": "11133"}`,
		`{"code":" 11133"}`,
		`{"code": '11133'}`,
		`{"errCode":"MODEL_PARAM_INVALID"}`,
		`Request parameters do not meet the current model requirements`,
		`INVALID REQUEST PARAMETERS`,
	}
	for _, body := range bodies {
		if got := GatewayHint(ErrClient, body, HintContext{}); got != neutral {
			t.Errorf("body %q → %q，应为中性参数说明", body, got)
		}
	}

	ctx := HintContext{Model: "fast-model", HasImage: true, ModelInCatalog: true, ModelSupportsImages: false}
	want := "model fast-model does not support images; pick one with supports_images=true from /v1/models"
	if got := GatewayHint(ErrBadParams, `{"code":11133}`, ctx); got != want {
		t.Errorf("带图且目录不支持时 = %q，应为 %q", got, want)
	}

	// 三个前提缺一：都退中性说明。
	for _, c := range []HintContext{
		{Model: "fast-model", HasImage: false, ModelInCatalog: true},
		{Model: "fast-model", HasImage: true, ModelInCatalog: false},
		{Model: "fast-model", HasImage: true, ModelInCatalog: true, ModelSupportsImages: true},
	} {
		if got := GatewayHint(ErrClient, `{"code":11133}`, c); got != neutral {
			t.Errorf("上下文 %+v 应退中性说明，got %q", c, got)
		}
	}

	// 11133 先于 Kind 表：kind 即使被归成图片错误也走参数说明。
	if got := GatewayHint(ErrImageInvalid, `{"code":11133}`, HintContext{}); got != neutral {
		t.Errorf("11133 应优先于 Kind 表，got %q", got)
	}
}

// TestGatewayHint11135 11135 图片数据非法家族：code/文案形态识别为图片数据说明。
func TestGatewayHint11135(t *testing.T) {
	const want = "image data rejected by upstream; use a real/valid image, may need a new conversation"
	for _, body := range []string{
		`{"code":11135,"msg":"bad image"}`,
		`{"code": 11135}`,
		`{"code":"11135"}`,
		`{"extError":{"code":"invalid_image_data"}}`,
		`Please replace the image and retry`,
	} {
		if got := GatewayHint(ErrNone, body, HintContext{}); got != want {
			t.Errorf("body %q → %q，应为 %q", body, got, want)
		}
	}
}

// TestFrameKind SSE error 帧分类：6004 模型限流优先；其余取 error.message 走 Classify；
// 判不出的返回 ErrNone。
func TestFrameKind(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    ErrKind
	}{
		{"6004 模型级限流", `{"code":6004,"msg":"rate limited"}`, ErrSoftRate},
		{"6004 字符串形态", `{"error":{"code":"6004"}}`, ErrSoftRate},
		{"上下文超限", `{"error":{"message":"prompt is too long"}}`, ErrPromptTooLong},
		{"内容策略", `{"error":{"message":"blocked by security policy"}}`, ErrContentBlocked},
		{"余额耗尽", `{"error":{"message":"insufficient credit"}}`, ErrHardCredit},
		{"session 失效", `{"error":{"message":"Offline user session not found"}}`, ErrSessionDead},
		{"普通客户端错误", `{"error":{"message":"something else"}}`, ErrClient},
		{"没有 error.message", `{"error":{}}`, ErrNone},
		{"不是 JSON", `data: oops`, ErrNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FrameKind(tc.payload); got != tc.want {
				t.Errorf("FrameKind(%q) = %s，应为 %s", tc.payload, got, tc.want)
			}
		})
	}
}

// TestFrameHintFunc SSE 帧 hint：空帧/[DONE] 不调用上下文函数；错误帧惰性求值
// 上下文并给出对应 hint。
func TestFrameHintFunc(t *testing.T) {
	calls := 0
	ctxFn := func() HintContext {
		calls++
		return HintContext{}
	}
	hint := FrameHintFunc(ctxFn)

	for _, payload := range []string{"", "[DONE]"} {
		if got := hint(payload); got != "" {
			t.Errorf("payload %q 应无 hint，got %q", payload, got)
		}
	}
	if calls != 0 {
		t.Errorf("空帧不应求值上下文，实际调用 %d 次", calls)
	}

	if got := hint(`{"code":6004,"msg":"rate limited"}`); got != "usage limit of this model reached at upstream; switch model or retry after reset" {
		t.Errorf("6004 帧 hint = %q", got)
	}
	if calls != 1 {
		t.Errorf("错误帧应求值上下文一次，实际 %d 次", calls)
	}
	if got := hint(`{"error":{"message":"prompt is too long"}}`); !strings.Contains(got, "context exceeds") {
		t.Errorf("上下文超限帧 hint = %q", got)
	}
	if got := hint(`not json`); got != "" {
		t.Errorf("不可解析帧应无 hint，got %q", got)
	}
}

// TestHintMarkers 判定函数的各个字面量分支（与 GatewayHint 互补，覆盖全部 marker）。
func TestHintMarkers(t *testing.T) {
	for _, body := range []string{
		`{"code":11133}`,
		`{"code": 11133}`,
		`{"code":"11133"}`,
		`{"code": "11133"}`,
		`{"code":" 11133"}`,
		`{"code": '11133'}`,
		`X model_param_invalid Y`,
		`invalid request parameters`,
		`request parameters do not meet the current model requirements`,
	} {
		if !isModelParamInvalid(body) {
			t.Errorf("isModelParamInvalid(%q) = false，应为 true", body)
		}
	}
	for _, body := range []string{`ok`, `{"code":11132}`, ``} {
		if isModelParamInvalid(body) {
			t.Errorf("isModelParamInvalid(%q) = true，应为 false", body)
		}
	}

	for _, body := range []string{
		`{"code":11135}`,
		`{"code": 11135}`,
		`{"code":"11135"}`,
		`{"code": "11135"}`,
		`{"code":" 11135"}`,
		`{"code": '11135'}`,
		`invalid_image_data`,
		`please REPLACE THE IMAGE`,
	} {
		if !isInvalidImageData(body) {
			t.Errorf("isInvalidImageData(%q) = false，应为 true", body)
		}
	}
	for _, body := range []string{`ok`, `replace image`} {
		if isInvalidImageData(body) {
			t.Errorf("isInvalidImageData(%q) = true，应为 false", body)
		}
	}
}
