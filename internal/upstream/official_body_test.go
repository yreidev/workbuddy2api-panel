// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
)

// TestInjectOfficialBodyFields 按官方 CLI 2.176.0 补齐 cache_retention="long"、
// reasoning_summary="auto"（仅带思考档位时）、extra_vars.harnessMetadata（带 messages 时）与
// extra_vars.initiator="user"（末尾消息为 user 时）；客户端显式携带的任何字段均原样保留；
// 空 body / 坏 body 原样返回。
func TestInjectOfficialBodyFields(t *testing.T) {
	decode := func(b []byte) map[string]any {
		t.Helper()
		var obj map[string]any
		if err := json.Unmarshal(b, &obj); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		return obj
	}
	for _, tc := range []struct {
		name, in       string
		cache, summary any
	}{
		{"都没带、没有思考档位", `{"model":"m"}`, "long", nil},
		{"reasoning_effort", `{"model":"m","reasoning_effort":"high"}`, "long", "auto"},
		{"reasoning.effort", `{"model":"m","reasoning":{"effort":"low"}}`, "long", "auto"},
		{"reasoning 不是对象", `{"model":"m","reasoning":true}`, "long", nil},
		{"客户端都带了", `{"model":"m","reasoning_effort":"high","cache_retention":"short","reasoning_summary":"none"}`, "short", "none"},
	} {
		obj := decode(InjectOfficialBodyFields([]byte(tc.in)))
		if obj["cache_retention"] != tc.cache || obj["reasoning_summary"] != tc.summary || obj["model"] != "m" {
			t.Errorf("%s: cache_retention=%v reasoning_summary=%v（%v）", tc.name, obj["cache_retention"], obj["reasoning_summary"], obj)
		}
		if _, hasExtra := obj["extra_vars"]; hasExtra {
			t.Errorf("%s: 没有 messages 时不应注入 extra_vars: %v", tc.name, obj["extra_vars"])
		}
	}
	keep := `{"model":"m","cache_retention":"short","reasoning_effort":"high","reasoning_summary":"none"}`
	if got := string(InjectOfficialBodyFields([]byte(keep))); got != keep {
		t.Fatalf("不需要补时应原样返回: %s", got)
	}
	for _, in := range []string{"", "not json", "[]", "null"} {
		if got := string(InjectOfficialBodyFields([]byte(in))); got != in {
			t.Fatalf("%q 应原样返回: %q", in, got)
		}
	}
}

// TestInjectOfficialBodyExtraVars 验证 extra_vars.harnessMetadata 与 extra_vars.initiator：
//   - 首步（stepIndex=0）无 previous；后续步带 previous.conversationMessageId；
//   - 末尾为 user 补 initiator="user"，末尾为 tool 不补 initiator；
//   - 保留客户端原有其他 extra_vars 字段，以及显式自带的 harnessMetadata / initiator；
//   - 不把 HTML/XML 字符（如 <system-reminder>）转义为 \u003c。
func TestInjectOfficialBodyExtraVars(t *testing.T) {
	decode := func(b []byte) map[string]any {
		t.Helper()
		var obj map[string]any
		if err := json.Unmarshal(b, &obj); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		return obj
	}

	// 1. 首步 + 末尾 user + 保留原有 extra_vars + 不转义 <>&
	in := `{"model":"m","messages":[{"role":"user","content":"<tag>&hi</tag>"}],"extra_vars":{"customKey":"keep"}}`
	out := InjectOfficialBodyFieldsWithOpts([]byte(in), OfficialBodyOpts{
		CliVersion:  "2.176.0",
		HarnessStep: &session.HarnessStep{Index: 0},
	})
	if strings.Contains(string(out), `\u003c`) {
		t.Fatalf("不应转义 < 字符: %s", out)
	}
	obj := decode(out)
	if obj["cache_retention"] != "long" {
		t.Fatalf("cache_retention = %v", obj["cache_retention"])
	}
	ev, _ := obj["extra_vars"].(map[string]any)
	if ev["customKey"] != "keep" || ev["initiator"] != "user" {
		t.Fatalf("extra_vars 异常: %v", ev)
	}
	hmStr, ok := ev["harnessMetadata"].(string)
	if !ok {
		t.Fatalf("harnessMetadata 应为 JSON 字符串: %T %v", ev["harnessMetadata"], ev["harnessMetadata"])
	}
	wantHM0 := `{"schemaVersion":1,"harnessVersion":"2.176.0","stepIndex":0,"agent":{"depth":0}}`
	if hmStr != wantHM0 {
		t.Fatalf("首步 harnessMetadata = %s, want %s", hmStr, wantHM0)
	}

	// 2. 工具调用续步：stepIndex=2 + previous + 末尾为 tool → 不写 initiator
	inTool := `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":""},{"role":"tool","content":"ok"}]}`
	outTool := InjectOfficialBodyFieldsWithOpts([]byte(inTool), OfficialBodyOpts{
		CliVersion:  "2.176.0",
		HarnessStep: &session.HarnessStep{Index: 2, PreviousMessageID: "019a0000000070008000000000000001"},
	})
	evTool, _ := decode(outTool)["extra_vars"].(map[string]any)
	if _, hasInit := evTool["initiator"]; hasInit {
		t.Fatalf("末尾为 tool 时不应写 initiator: %v", evTool)
	}
	wantHM2 := `{"schemaVersion":1,"harnessVersion":"2.176.0","stepIndex":2,"previous":{"conversationMessageId":"019a0000000070008000000000000001"},"agent":{"depth":0}}`
	if evTool["harnessMetadata"] != wantHM2 {
		t.Fatalf("续步 harnessMetadata = %v, want %s", evTool["harnessMetadata"], wantHM2)
	}

	// 3. 客户端显式带了 harnessMetadata 与 initiator 时原样保留
	inExplicit := `{"model":"m","cache_retention":"short","messages":[{"role":"user","content":"hi"}],"extra_vars":{"harnessMetadata":"{\"custom\":1}","initiator":"agent"}}`
	outExplicit := InjectOfficialBodyFieldsWithOpts([]byte(inExplicit), OfficialBodyOpts{
		CliVersion:  "2.176.0",
		HarnessStep: &session.HarnessStep{Index: 1, PreviousMessageID: "prev-1"},
	})
	if string(outExplicit) != inExplicit {
		t.Fatalf("显式字段均已存在时应原样返回: %s", outExplicit)
	}
}

// TestPrepareBodyInjectsOfficialFields chat 出站 body 经 prepareBody 后两域均补 cache_retention="long"、
// extra_vars.harnessMetadata 与 extra_vars.initiator="user"；deepseek 被补默认思考档位后也跟着带 reasoning_summary。
func TestPrepareBodyInjectsOfficialFields(t *testing.T) {
	for _, realm := range []string{"cn", "global"} {
		out := string((&Client{}).prepareBody([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), realm))
		if !strings.Contains(out, `"cache_retention":"long"`) || strings.Contains(out, "reasoning_summary") ||
			!strings.Contains(out, `"harnessMetadata"`) || !strings.Contains(out, `"initiator":"user"`) {
			t.Fatalf("%s: 普通模型出站字段不对: %s", realm, out)
		}
		out = string((&Client{}).prepareBody([]byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`), realm))
		if !strings.Contains(out, `"cache_retention":"long"`) || !strings.Contains(out, `"reasoning_effort"`) || !strings.Contains(out, `"reasoning_summary":"auto"`) {
			t.Fatalf("%s: deepseek 应同时带 cache_retention 与 reasoning_summary: %s", realm, out)
		}
	}
}

// TestChatStreamHarnessStepAndMessageIDAligned 验证 ChatStreamContext 复用 ChatMeta.MessageID，
// 并将 HarnessStep 精确写入出站 body 的 extra_vars.harnessMetadata。
func TestChatStreamHarnessStepAndMessageIDAligned(t *testing.T) {
	var gotHeader http.Header
	var gotBody []byte
	c := testClient(func(r *http.Request) (*http.Response, error) {
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		return jsonResp(200, "data: [DONE]\n\n"), nil
	})
	meta := ChatMeta{
		ConversationID:        "conv-1",
		ConversationRequestID: "019a0000000070008000000000000010",
		MessageID:             "019a0000000070008000000000000022",
		HarnessStep:           &session.HarnessStep{Index: 1, PreviousMessageID: "019a0000000070008000000000000011"},
	}
	rc, status, _, err := c.ChatStreamContext(context.Background(), &auth.Auth{UID: "u1", AccessToken: "at"}, []byte(`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"res"}]}`), "", meta)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	rc.Close()
	if gotHeader.Get("X-Conversation-Message-ID") != meta.MessageID || gotHeader.Get("X-Request-ID") != meta.MessageID {
		t.Fatalf("消息头未复用 ChatMeta.MessageID: msg=%q req=%q want=%q",
			gotHeader.Get("X-Conversation-Message-ID"), gotHeader.Get("X-Request-ID"), meta.MessageID)
	}
	var bodyObj map[string]any
	if err := json.Unmarshal(gotBody, &bodyObj); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	ev, _ := bodyObj["extra_vars"].(map[string]any)
	wantHM := `{"schemaVersion":1,"harnessVersion":"2.176.0","stepIndex":1,"previous":{"conversationMessageId":"019a0000000070008000000000000011"},"agent":{"depth":0}}`
	if ev["harnessMetadata"] != wantHM {
		t.Fatalf("harnessMetadata = %v, want %s", ev["harnessMetadata"], wantHM)
	}
	if _, hasInit := ev["initiator"]; hasInit {
		t.Fatalf("末尾 tool 不应写 initiator: %v", ev)
	}
}
