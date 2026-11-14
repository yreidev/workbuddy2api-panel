// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const (
	ccIdentity = "You are Claude Code, Anthropic's official CLI for Claude."
	ccBranch   = "Main branch (you will usually use this for PRs)"
	ccHeader   = "x-anthropic-billing-header: cc_version=1.0; cc_entrypoint=cli;"
	// Codex instructions 首段（上游逐字精确指纹，三句缺一不可）。
	codexInstructions = "You are a coding agent running in the Codex CLI, a terminal-based coding assistant. Codex CLI is an open source project led by OpenAI. You are expected to be precise, safe, and helpful."
)

func TestIdentityRewritten(t *testing.T) {
	out := sanitizeText(ccIdentity)
	if !strings.Contains(out, "official CLI tool for Claude.") {
		t.Errorf("identity not rewritten: %q", out)
	}
	if strings.Contains(out, ccIdentity) {
		t.Errorf("original identity still present: %q", out)
	}
}

// 桌面版（claude-desktop-3p / Agent SDK）的身份句以逗号接后继内容，结尾不是句号。
// 回归用例：匹配串曾带结尾句号，导致该形态漏网、指纹原样发上游 → 400 code=11128。
func TestIdentityDesktopVariantRewritten(t *testing.T) {
	in := "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."
	out := sanitizeText(in)
	if strings.Contains(out, "official CLI for Claude") {
		t.Errorf("desktop identity not rewritten: %q", out)
	}
	if !strings.Contains(out, "official CLI tool for Claude, running within the Claude Agent SDK.") {
		t.Errorf("desktop identity suffix not preserved: %q", out)
	}
}

func TestBranchRewritten(t *testing.T) {
	out := sanitizeText(ccBranch)
	if !strings.Contains(out, "Default branch (you will usually use this for PRs)") {
		t.Errorf("branch not rewritten: %q", out)
	}
	if strings.Contains(out, "Main branch") {
		t.Errorf("original branch still present: %q", out)
	}
}

// 反馈句带 Anthropic 仓库链接，上游按整句拦截（实测只留链接或只留半边均不拦）。
// 回归用例：give→provide 一词之差即可绕过。
func TestFeedbackSentenceRewritten(t *testing.T) {
	in := "To give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues"
	out := sanitizeText(in)
	if strings.Contains(out, "To give feedback") {
		t.Errorf("feedback sentence not rewritten: %q", out)
	}
	if !strings.Contains(out, "To provide feedback, users should report the issue at https://github.com/anthropics/claude-code/issues") {
		t.Errorf("feedback sentence not rewritten as expected: %q", out)
	}
}

// 上游反探测：请求体里出现裸数字 11128 即整单拦截（与上下文无关）。
// 回归用例：该串会被改写为 11-128 以打断精确匹配。
func TestUpstreamErrorCodeRewritten(t *testing.T) {
	in := "upstream returned code=11128 for this request"
	out := sanitizeText(in)
	if strings.Contains(out, "11128") {
		t.Errorf("error code not rewritten: %q", out)
	}
	if !strings.Contains(out, "11-128") {
		t.Errorf("error code not rewritten as expected: %q", out)
	}
}

// 回归：工具调用消息的 content 常为 null，而旧版 sanitizeMessages 在 content 缺失时
// 直接 continue，整条消息连 tool_calls 一起被跳过 → arguments 里的被拦字符串原样漏出。
func TestToolCallArgumentsSanitized(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "run"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
			map[string]any{"id": "c1", "type": "function", "function": map[string]any{
				"name":      "Bash",
				"arguments": `{"command":"echo 11128"}`,
			}},
		}},
	}
	if !sanitizeMessages(msgs) {
		t.Fatal("sanitizeMessages 未报告任何改动，tool_calls 被跳过")
	}
	fn := msgs[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	got := fn["arguments"].(string)
	if strings.Contains(got, "11128") {
		t.Errorf("tool_call arguments 未被净化: %q", got)
	}
}

func TestBillingHeaderStrippedValueIrrelevant(t *testing.T) {
	out := sanitizeText(ccHeader)
	if strings.Contains(out, "x-anthropic-billing-header") {
		t.Errorf("header not stripped: %q", out)
	}
}

// 附加验证：正常对话里出现 github.com/anthropics/ 链接（但不是反馈句整句）
// 时，预检特征命中（进入净化），但改写层只动精确匹配的整句——普通链接文本
// 不该被改写。同理，既不含 11128 也不含反馈整句的文本原样返回。
func TestNormalAnthropicLinkNotRewritten(t *testing.T) {
	in := "see https://github.com/anthropics/anthropic-cookbook for examples"
	if out := sanitizeText(in); out != in {
		t.Errorf("normal anthropic link should be untouched: %q -> %q", in, out)
	}
}

// Codex instructions 首段：命中预告且整句改写，逐字指纹被破坏、语义保留。
func TestCodexInstructionsRewritten(t *testing.T) {
	out := sanitizeText(codexInstructions)
	if strings.Contains(out, codexInstructions) {
		t.Errorf("codex fingerprint still present: %q", out)
	}
	if !strings.Contains(out, "You are a coding agent running in the Codex CLI tool, a terminal-based coding assistant.") {
		t.Errorf("codex first sentence not rewritten: %q", out)
	}
	// 其余两句原样保留，语义不变。
	if !strings.Contains(out, "Codex CLI is an open source project led by OpenAI.") ||
		!strings.Contains(out, "You are expected to be precise, safe, and helpful.") {
		t.Errorf("codex remaining sentences altered: %q", out)
	}
}

// 预检必须能认出 Codex 特征（此前只含 Claude Code，导致提前放行）。
func TestCodexFingerprintDetected(t *testing.T) {
	if !hasFingerprint(codexInstructions) {
		t.Error("codex fingerprint not detected by precheck")
	}
}

// 非精确变体不应被改写：仅去掉其中一个词即视为已破坏，无需改动。
func TestCodexVariantNotTouched(t *testing.T) {
	in := "You are a coding agent running in a CLI, a terminal-based coding assistant."
	if out := sanitizeText(in); out != in {
		t.Errorf("already-broken variant should be untouched: %q -> %q", in, out)
	}
}

func TestBillingHeaderCaseInsensitive(t *testing.T) {
	alt := "X-Anthropic-Billing-Header: cc_version=1.0;"
	out := strings.ToLower(sanitizeText(alt))
	if strings.Contains(out, "billing") {
		t.Errorf("case-insensitive header not stripped: %q", out)
	}
}

func TestTrailingKVStripped(t *testing.T) {
	out := sanitizeText("...; cc_version=2.0; cc_entrypoint=cli;")
	if strings.Contains(out, "cc_version") || strings.Contains(out, "cc_entrypoint") {
		t.Errorf("trailing kv not stripped: %q", out)
	}
}

func TestExactMatchOnlyVariantNotTouched(t *testing.T) {
	in := "...official CLI for Claude!"
	if out := sanitizeText(in); out != in {
		t.Errorf("variant should be untouched: %q -> %q", in, out)
	}
}

func TestUserFreeTextNotTouched(t *testing.T) {
	in := "please use main branch for this repo"
	if out := sanitizeText(in); out != in {
		t.Errorf("free text should be untouched: %q -> %q", in, out)
	}
}

func TestNoFeatureReturnsSameString(t *testing.T) {
	in := "ordinary user message"
	if out := sanitizeText(in); out != in {
		t.Errorf("no-feature text should pass through unchanged: %q -> %q", in, out)
	}
}

func TestMultimodalTextPartOnly(t *testing.T) {
	imgPart := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "..."}}
	content := []any{
		map[string]any{"type": "text", "text": ccIdentity},
		imgPart,
	}
	out, changed := sanitizeContent(content)
	if !changed {
		t.Fatal("expected change")
	}
	parts := out.([]any)
	txt, _ := parts[0].(map[string]any)["text"].(string)
	if !strings.Contains(txt, "CLI tool") {
		t.Errorf("text part not sanitized: %q", txt)
	}
	img, _ := parts[1].(map[string]any)
	if img["type"] != "image" || img["source"].(map[string]any)["data"] != "..." {
		t.Error("image part modified")
	}
}

// 集成：完整请求体经 PrepareBodyOpt 净化后无残留指纹，且 stream/tool_choice 行为不受影响。
func TestPrepareBodyOptSanitizesSystem(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[` +
		`{"role":"system","content":"` + ccIdentity + ` ` + ccHeader + `"},` +
		`{"role":"user","content":"hi"}]}`)
	out := PrepareBodyOpt(body, true)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["stream"] != true {
		t.Error("stream not forced")
	}
	msgs := obj["messages"].([]any)
	sys, _ := msgs[0].(map[string]any)["content"].(string)
	if strings.Contains(sys, "x-anthropic-billing-header") || strings.Contains(sys, ccIdentity) || strings.Contains(sys, ccBranch) {
		t.Errorf("fingerprints remain: %q", sys)
	}
	if !strings.Contains(sys, "CLI tool") {
		t.Errorf("rewrite missing: %q", sys)
	}
}

func TestPrepareBodyOptDisabledPreservesFingerprints(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"` + ccIdentity + `"}]}`)
	out := PrepareBodyOpt(body, false)
	if !strings.Contains(string(out), ccIdentity) {
		t.Error("sanitize=false should preserve fingerprints")
	}
	// 但 stream 仍强制
	var obj map[string]any
	_ = json.Unmarshal(out, &obj)
	if obj["stream"] != true {
		t.Error("stream should still be forced")
	}
}

// PrepareBody 默认行为 = 开启脱敏（保持向后兼容）。
func TestPrepareBodyDefaultSanitizes(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"` + ccIdentity + `"}]}`)
	out := PrepareBodyOpt(body, true)
	if strings.Contains(string(out), ccIdentity) {
		t.Error("PrepareBody default should sanitize")
	}
}

// 出站边界集成：ChatStream 发往上游的 wire body 必须无残留指纹。
func TestChatStreamWireBodySanitized(t *testing.T) {
	var gotBody []byte
	ts := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	})
	defer ts.Close()

	c := New()
	c.SanitizeFingerprints.Store(true)
	c.ChatBaseCN = ts.URL
	acct := &auth.Auth{AccessToken: "test-token", Domain: "copilot.tencent.com", UID: "u1"}

	body := []byte(`{"model":"glm-5.2","messages":[` +
		`{"role":"system","content":"` + ccIdentity + ` ` + ccHeader + `"},` +
		`{"role":"user","content":"hi"}]}`)
	rc, status, respBody, err := c.ChatStream(acct, body, "", ChatMeta{})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if status >= 400 {
		t.Fatalf("upstream status %d: %s", status, respBody)
	}
	// 上游收到的 body：stream 强制 + 指纹已净化
	var obj map[string]any
	if err := json.Unmarshal(gotBody, &obj); err != nil {
		t.Fatalf("wire body not json: %v", err)
	}
	if obj["stream"] != true {
		t.Error("wire body stream not forced")
	}
	sys, _ := obj["messages"].([]any)[0].(map[string]any)["content"].(string)
	for _, fp := range []string{"x-anthropic-billing-header", ccIdentity, ccBranch} {
		if strings.Contains(sys, fp) {
			t.Errorf("wire body contains fingerprint %q: %q", fp, sys)
		}
	}
}

// 出站边界（Codex 场景）：CPA 把 instructions 折成 role=system 的首条消息，
// 净化后 wire body 不得残留 Codex 逐字指纹；其余消息不受影响。
func TestChatStreamWireBodyCodexInstructionsSanitized(t *testing.T) {
	var gotBody []byte
	ts := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	})
	defer ts.Close()

	c := New()
	c.SanitizeFingerprints.Store(true)
	c.ChatBaseCN = ts.URL
	acct := &auth.Auth{AccessToken: "test-token", Domain: "copilot.tencent.com", UID: "u1"}

	body := []byte(`{"model":"kimi-k3","messages":[` +
		`{"role":"system","content":"` + codexInstructions + `"},` +
		`{"role":"user","content":"say ok"}]}`)
	rc, status, respBody, err := c.ChatStream(acct, body, "", ChatMeta{})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if status >= 400 {
		t.Fatalf("upstream status %d: %s", status, respBody)
	}
	var obj map[string]any
	if err := json.Unmarshal(gotBody, &obj); err != nil {
		t.Fatalf("wire body not json: %v", err)
	}
	sys, _ := obj["messages"].([]any)[0].(map[string]any)["content"].(string)
	if strings.Contains(sys, codexInstructions) ||
		strings.Contains(sys, "in the Codex CLI, a terminal-based coding assistant.") {
		t.Errorf("wire body still contains codex fingerprint: %q", sys)
	}
	if !strings.Contains(sys, "running in the Codex CLI tool, a terminal-based") {
		t.Errorf("codex rewrite missing on wire: %q", sys)
	}
	user, _ := obj["messages"].([]any)[1].(map[string]any)["content"].(string)
	if user != "say ok" {
		t.Errorf("user message altered: %q", user)
	}
}

// 出站边界：关闭脱敏后 wire body 原样保留指纹（验证开关真实有效）。
func TestChatStreamWireBodySanitizeDisabled(t *testing.T) {
	var gotBody []byte
	ts := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	defer ts.Close()

	c := New()
	c.SanitizeFingerprints.Store(false)
	c.ChatBaseCN = ts.URL
	acct := &auth.Auth{AccessToken: "test-token", Domain: "copilot.tencent.com", UID: "u1"}

	body := []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"` + ccIdentity + `"}]}`)
	rc, status, _, err := c.ChatStream(acct, body, "", ChatMeta{})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if status >= 400 {
		t.Fatalf("upstream status %d", status)
	}
	if !strings.Contains(string(gotBody), ccIdentity) {
		t.Error("sanitize disabled should preserve fingerprint on wire")
	}
}

// TestSanitizeMessagesScrubsReasoningField thinking.go 的回填会把客户端送来的
// reasoning_content 镜像进 reasoning 字段；此前只洗 content / reasoning_content /
// tool_calls → 镜像进 reasoning 的指纹（裸 "11128" 这类反探测串）原样出站，
// 而请求体里出现裸 11128 本身就是上游整单拦截条件。
func TestSanitizeMessagesScrubsReasoningField(t *testing.T) {
	ms := []any{map[string]any{
		"role":      "assistant",
		"content":   "hi",
		"reasoning": "upstream said 11128",
	}}
	if !sanitizeMessages(ms) {
		t.Fatal("reasoning 字段里的指纹未被净化")
	}
	got, _ := ms[0].(map[string]any)["reasoning"].(string)
	if strings.Contains(got, "11128") {
		t.Fatalf("reasoning 仍含裸指纹: %q", got)
	}
}

const (
	dshGUIA = "You are interacting with the user through the DeepSeek Harness Web GUI whose URL the runtime context names."
	dshGUIB = `When the user refers to "this page", "this GUI", or "this app" without naming another target, they mean this GUI.`
)

// 自我介绍句（11128 渠道指纹）整句换成中性句，句子之后的内容原样保留。
func TestIdentitySentenceRewritten(t *testing.T) {
	cases := map[string]string{
		"You are an AI agent powered by DeepSeek Harness.":                                    "You are a helpful AI assistant.",
		"You are an AI assistant built on DeepSeek. Use tools wisely.":                        "You are a helpful AI assistant. Use tools wisely.",
		"You are Codex Agent, built by OpenAI.":                                               "You are a helpful AI assistant.",
		"you are an llm TRAINED BY someone.":                                                  "You are a helpful AI assistant.",
		"You are powered by the model named Claude Opus 4.5. The exact model ID is x-4-5.":    "You are a helpful AI assistant. The exact model ID is x-4-5.",
		"Intro.\nYou are a coding agent powered by the deepseek-v4.1-flash model.\nTools: a.": "Intro.\nYou are a helpful AI assistant.\nTools: a.",
		"You are an AI agent powered by DeepSeek Harness\nnext line":                          "You are a helpful AI assistant.\nnext line",
	}
	for in, want := range cases {
		if got := sanitizeText(in); got != want {
			t.Errorf("sanitizeText(%q) = %q, want %q", in, got, want)
		}
	}
}

// 分词修饰宾语、不是自我介绍的句子不动；只出现动词短语时连预检都不进。
func TestIdentityLookalikeNotTouched(t *testing.T) {
	for _, in := range []string{
		"You are free to use tools built by the community.",
		"This plugin is powered by DeepSeek and made by volunteers.",
		"You are an agent. The CLI was built by OpenAI.",
	} {
		if got := sanitizeText(in); got != in {
			t.Errorf("should be untouched: %q -> %q", in, got)
		}
	}
}

// GUI 描述段：A 句只换句尾语序，品牌再缩成 DeepSeek，B 句不动。
func TestDSHGUIParagraphRewritten(t *testing.T) {
	want := "You are interacting with the user through the DeepSeek Web GUI whose URL is named in the runtime context. " + dshGUIB
	if got := sanitizeText(dshGUIA + " " + dshGUIB); got != want {
		t.Errorf("GUI paragraph:\n got %q\nwant %q", got, want)
	}
	lower := strings.ToLower(dshGUIA)
	if got := sanitizeText(lower); strings.Contains(got, "the runtime context names") || strings.Contains(got, "harness") {
		t.Errorf("case-insensitive GUI rewrite failed: %q", got)
	}
}

// 品牌词只认散文里的空格形式，路径、包名、标识符里的连字符和下划线形式不动。
func TestDSHBrandRewrittenOnlyInProse(t *testing.T) {
	if got := sanitizeText("Built with DeepSeek\tHarness today"); got != "Built with DeepSeek today" {
		t.Errorf("brand not reduced: %q", got)
	}
	for _, in := range []string{
		"github.com/deepseek-ai/deepseek-harness/tree/main",
		"import deepseek_harness as dsh",
		"/opt/deepseek-harness/app",
		"a test harness for DeepSeek",
	} {
		if got := sanitizeText(in); got != in {
			t.Errorf("identifier should be untouched: %q -> %q", in, got)
		}
	}
}

// OhMyOpenCode 子代理身份串：连在一起才是指纹，两半各自出现不动。
func TestOmOIdentityRewritten(t *testing.T) {
	in := "You are Sisyphus-Junior - Focused executor from OhMyOpenCode, a helper."
	if got := sanitizeText(in); got != "You are Sisyphus-Junior - Focused executor, a helper." {
		t.Errorf("OmO not rewritten: %q", got)
	}
	for _, in := range []string{"Sisyphus-Junior alone", "from OhMyOpenCode alone"} {
		if got := sanitizeText(in); got != in {
			t.Errorf("half should be untouched: %q -> %q", in, got)
		}
	}
}

// 改写结果再过一遍不再变化。
func TestChannelFingerprintRewriteIdempotent(t *testing.T) {
	for _, in := range []string{
		dshGUIA + " " + dshGUIB,
		"You are an AI agent powered by DeepSeek Harness. Be brief.",
		"Sisyphus-Junior - Focused executor from OhMyOpenCode",
	} {
		once := sanitizeText(in)
		if twice := sanitizeText(once); twice != once {
			t.Errorf("not idempotent: %q -> %q -> %q", in, once, twice)
		}
	}
}

// 作用域：和其他规则一样，user、assistant、tool 消息里的渠道指纹也改。
func TestChannelFingerprintScrubbedInEveryRole(t *testing.T) {
	ms := []any{
		map[string]any{"role": "system", "content": "You are an AI agent powered by DeepSeek Harness."},
		map[string]any{"role": "user", "content": dshGUIA + " " + dshGUIB},
		map[string]any{"role": "assistant", "content": "You are a coding agent powered by the deepseek-v4.1-flash model."},
		map[string]any{"role": "tool", "content": "Sisyphus-Junior - Focused executor from OhMyOpenCode"},
	}
	if !sanitizeMessages(ms) {
		t.Fatal("expected change")
	}
	for i, m := range ms {
		c := m.(map[string]any)["content"].(string)
		if containsFoldASCII(c, "harness") || strings.Contains(c, "powered by") || strings.Contains(c, "OhMyOpenCode") {
			t.Errorf("message %d still carries fingerprint: %q", i, c)
		}
	}
}

// Codex / Claude Code 系 system prompt 的 workspace root 短语：system 级内容里命中后
// 改成语序自然的 root of the workspace，语义不变，同时不再撞上游的渠道指纹。
func TestWorkspaceRootRewritten(t *testing.T) {
	in := "Relative paths resolve from the workspace root; absolute paths identify files."
	want := "Relative paths resolve from the root of the workspace; absolute paths identify files."
	if got := systemContent(t, in); got != want {
		t.Errorf("not rewritten:\n got %q\nwant %q", got, want)
	}
}

// systemContent 把单段内容包成 system 消息走一遍 sanitizeMessages，返回净化后的 content。
func systemContent(t *testing.T, in string) string {
	t.Helper()
	msgs := []any{map[string]any{"role": "system", "content": in}}
	sanitizeMessages(msgs)
	got, ok := msgs[0].(map[string]any)["content"].(string)
	if !ok {
		t.Fatalf("content 不是字符串: %#v", msgs[0])
	}
	return got
}

// 大小写不敏感，空格与制表符都算分隔。
func TestWorkspaceRootCaseAndSeparator(t *testing.T) {
	for _, in := range []string{
		"from the workspace root here",
		"from the Workspace root here",
		"from the WORKSPACE ROOT here",
		"from the workspace\troot here",
	} {
		if got := systemContent(t, in); got != "from the root of the workspace here" {
			t.Errorf("%q -> %q", in, got)
		}
	}
}

// 词边界：只有独立的 workspace root 才动，更长单词里的子串保持原样。
func TestWorkspaceRootWordBoundary(t *testing.T) {
	for _, in := range []string{
		"a workspace rooter tool",
		"myworkspace root probe",
		"the workspaceroot is fine",
	} {
		if got := systemContent(t, in); got != in {
			t.Errorf("should be untouched: %q -> %q", in, got)
		}
	}
}

// 改写结果再过一遍不再变化。
func TestWorkspaceRootIdempotent(t *testing.T) {
	once := systemContent(t, "from the workspace root here")
	if twice := systemContent(t, once); twice != once {
		t.Errorf("not idempotent: %q -> %q", once, twice)
	}
}

// 作用域：只有 system / developer 级内容替换；user、assistant、tool、无 role
// 以及工具调用的 arguments 一律逐字保留——那是用户的真实字面内容。
func TestWorkspaceRootOnlySystemLevel(t *testing.T) {
	const in = `mkdir "workspace root"`
	const wantIn = `mkdir "root of the workspace"`
	argsJSON, err := json.Marshal(map[string]string{"cmd": in})
	if err != nil {
		t.Fatalf("构造工具参数失败: %v", err)
	}
	wantArgs := string(argsJSON)
	msgs := []any{
		map[string]any{"role": "system", "content": in},
		map[string]any{"role": "developer", "content": in},
		map[string]any{"role": "user", "content": in},
		map[string]any{"role": "assistant", "content": in},
		map[string]any{"role": "tool", "content": in},
		map[string]any{"content": in},
		map[string]any{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []any{map[string]any{
				"id":   "c1",
				"type": "function",
				"function": map[string]any{
					"name":      "run",
					"arguments": wantArgs,
				},
			}},
		},
	}
	sanitizeMessages(msgs)

	wantSystems := map[int]string{0: wantIn, 1: wantIn}
	for i, m := range msgs {
		got, _ := m.(map[string]any)["content"].(string)
		if want, ok := wantSystems[i]; ok {
			if got != want {
				t.Errorf("msg %d 应替换: got %q want %q", i, got, want)
			}
			continue
		}
		if i == 6 {
			continue // content 为 nil，arguments 单独断言
		}
		if got != in {
			t.Errorf("msg %d 不应改动: got %q want %q", i, got, in)
		}
	}
	fn := msgs[6].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	args := fn["arguments"].(string)
	if args != wantArgs {
		t.Errorf("工具参数不应改动: %q", args)
	}
	if !json.Valid([]byte(args)) {
		t.Fatalf("工具参数不是合法 JSON: %q", args)
	}
}

// workspace root 不再单独把文本拖进完整净化链：review 5881990497 第四条指出，
// 只含该短语的工具参数曾被 sanitizeKvRe 删掉 cc_version=1 而变成无效 JSON。
func TestWorkspaceRootDoesNotTriggerKvStrip(t *testing.T) {
	const args = `{"command":"echo workspace root; cc_version=1"}`
	msgs := []any{map[string]any{
		"role":    "assistant",
		"content": nil,
		"tool_calls": []any{map[string]any{
			"id":   "c1",
			"type": "function",
			"function": map[string]any{
				"name":      "run",
				"arguments": args,
			},
		}},
	}}
	if sanitizeMessages(msgs) {
		t.Fatal("不应有任何改动")
	}
	got := msgs[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
	if got != args {
		t.Fatalf("arguments 被改动: %q", got)
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("arguments 已不是合法 JSON: %q", got)
	}
}

func TestContainsFoldASCII(t *testing.T) {
	if !containsFoldASCII("xx DeepSeek HARNESS", "harness") || containsFoldASCII("harnes", "harness") || containsFoldASCII("", "a") {
		t.Fatal("containsFoldASCII wrong")
	}
}

// newTestUpstream 起一个假上游并捕获请求。
func newTestUpstream(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(h)
}
