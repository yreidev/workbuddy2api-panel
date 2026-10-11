// dsml_test.go 覆盖模型原生工具调用标记修复层（dsml.go）。
//
// 所有夹具都用包内常量拼装（markupOpen / markupPipe / ...），不在源码里写死真实
// 分隔符字面量：分隔符是全角字符，字面量容易被编辑器/复制粘贴静默改坏，而拼装
// 出来的字节与上游实际下发的一致。
package upstream

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// --- 夹具构造 ---------------------------------------------------------------

// mkParam 拼一个 string="true" 的 parameter（取值按原文字符串处理）。
func mkParam(name, value string) string {
	return "<" + markupPipe + "DSML" + markupPipe + " parameter name=\"" + name + "\" string=\"true\">" +
		value + markupParamClose
}

// mkJSONParam 拼一个不带 string 属性的 parameter（取值按 JSON 解析）。
func mkJSONParam(name, raw string) string {
	return "<" + markupPipe + "DSML" + markupPipe + " parameter name=\"" + name + "\">" +
		raw + markupParamClose
}

// mkInvoke 拼一次 invoke，params 为已拼好的 parameter 串。
func mkInvoke(name string, params ...string) string {
	body := ""
	if len(params) > 0 {
		body = "\n" + strings.Join(params, "\n") + "\n"
	}
	return "<" + markupPipe + "DSML" + markupPipe + " invoke name=\"" + name + "\">" + body + markupInvokeClose
}

// mkBlock 拼一个完整的 calls 块。
func mkBlock(invokes ...string) string {
	return markupOpen + "\n" + strings.Join(invokes, "\n") + "\n" + markupClose
}

// repairAll 按给定分片顺序喂完整条正文，返回透出文本与还原出的调用。
func repairAll(allowed map[string]bool, chunks []string) (string, []MarkupCall) {
	r := NewMarkupRepair(allowed, false)
	var sb strings.Builder
	var calls []MarkupCall
	for _, c := range chunks {
		emit, got := r.Feed(c)
		sb.WriteString(emit)
		calls = append(calls, got...)
	}
	sb.WriteString(r.Flush())
	return sb.String(), calls
}

// argsOf 解析 arguments JSON 为 map，断言非法 JSON 直接失败。
func argsOf(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("arguments 不是合法 JSON 对象: %q (%v)", raw, err)
	}
	return m
}

var testTools = map[string]bool{"exec_command": true, "write_stdin": true}

// --- 基本解析 ---------------------------------------------------------------

func TestMarkupRepairSingleCall(t *testing.T) {
	in := mkBlock(mkInvoke("exec_command", mkParam("cmd", "ls -la /tmp")))
	out, calls := repairAll(testTools, []string{in})
	if out != "" {
		t.Fatalf("整条正文都是标记 → 透出应为空，实际 %q", out)
	}
	if len(calls) != 1 {
		t.Fatalf("应还原 1 次调用，实际 %d", len(calls))
	}
	if calls[0].Name != "exec_command" {
		t.Fatalf("工具名错误: %q", calls[0].Name)
	}
	if got := argsOf(t, calls[0].Arguments)["cmd"]; got != "ls -la /tmp" {
		t.Fatalf("cmd 参数错误: %#v", got)
	}
}

func TestMarkupRepairMultipleCallsAndOrder(t *testing.T) {
	in := mkBlock(
		mkInvoke("exec_command", mkParam("cmd", "pwd")),
		mkInvoke("write_stdin", mkParam("session_id", "s1"), mkParam("chars", "y\n")),
	)
	out, calls := repairAll(testTools, []string{in})
	if out != "" {
		t.Fatalf("透出应为空，实际 %q", out)
	}
	if len(calls) != 2 {
		t.Fatalf("应还原 2 次调用，实际 %d", len(calls))
	}
	if calls[0].Name != "exec_command" || calls[1].Name != "write_stdin" {
		t.Fatalf("调用顺序/名字错误: %q, %q", calls[0].Name, calls[1].Name)
	}
	if got := argsOf(t, calls[1].Arguments)["chars"]; got != "y\n" {
		t.Fatalf("chars 参数错误: %#v", got)
	}
}

func TestMarkupRepairKeepsSurroundingProse(t *testing.T) {
	head := "先摸清接口清单，再落文档。\n\n"
	tail := "\n\n以上是第一批调用。"
	in := head + mkBlock(mkInvoke("exec_command", mkParam("cmd", "pwd"))) + tail
	out, calls := repairAll(testTools, []string{in})
	if len(calls) != 1 {
		t.Fatalf("应还原 1 次调用，实际 %d", len(calls))
	}
	if out != head+tail {
		t.Fatalf("正文应保留标记之外的部分\nwant %q\ngot  %q", head+tail, out)
	}
}

func TestMarkupRepairNoParameters(t *testing.T) {
	in := mkBlock(mkInvoke("exec_command"))
	_, calls := repairAll(testTools, []string{in})
	if len(calls) != 1 {
		t.Fatalf("应还原 1 次调用，实际 %d", len(calls))
	}
	if calls[0].Arguments != "{}" {
		t.Fatalf("无参工具应产出 {}，实际 %q", calls[0].Arguments)
	}
}

func TestMarkupRepairStringParamEscaping(t *testing.T) {
	// 真实场景：apply_patch 的命令里带换行、双引号、反斜杠、中文。
	value := "cd /tmp && apply_patch <<'PATCH'\n*** Begin Patch\n+  const s = \"a\\\\b\";\n+  // 中文注释\n*** End Patch\nPATCH"
	in := mkBlock(mkInvoke("exec_command", mkParam("cmd", value)))
	_, calls := repairAll(testTools, []string{in})
	if len(calls) != 1 {
		t.Fatalf("应还原 1 次调用，实际 %d", len(calls))
	}
	if got := argsOf(t, calls[0].Arguments)["cmd"]; got != value {
		t.Fatalf("参数值经 JSON 转义后未能原样还原\nwant %q\ngot  %q", value, got)
	}
}

func TestMarkupRepairJSONParam(t *testing.T) {
	in := mkBlock(mkInvoke("exec_command", mkJSONParam("opts", `{"timeout":30,"force":true}`)))
	_, calls := repairAll(testTools, []string{in})
	if len(calls) != 1 {
		t.Fatalf("应还原 1 次调用，实际 %d", len(calls))
	}
	opts, ok := argsOf(t, calls[0].Arguments)["opts"].(map[string]any)
	if !ok {
		t.Fatalf("opts 应解析为 JSON 对象")
	}
	if opts["timeout"] != float64(30) || opts["force"] != true {
		t.Fatalf("opts 内容错误: %#v", opts)
	}
}

func TestMarkupRepairJSONParamFallbackToString(t *testing.T) {
	// string 属性缺失且取值不是合法 JSON：退化为字符串，绝不丢值。
	in := mkBlock(mkInvoke("exec_command", mkJSONParam("cmd", "ls -la")))
	_, calls := repairAll(testTools, []string{in})
	if len(calls) != 1 {
		t.Fatalf("应还原 1 次调用，实际 %d", len(calls))
	}
	if got := argsOf(t, calls[0].Arguments)["cmd"]; got != "ls -la" {
		t.Fatalf("退化字符串取值错误: %#v", got)
	}
}

// --- 不转换（原文透出）的边界 -----------------------------------------------

func TestMarkupRepairDisabledWithoutTools(t *testing.T) {
	in := mkBlock(mkInvoke("exec_command", mkParam("cmd", "pwd")))
	out, calls := repairAll(nil, []string{in})
	if out != in || len(calls) != 0 {
		t.Fatalf("未声明 tools 时应恒等透传，实际 out=%q calls=%d", out, len(calls))
	}
}

func TestMarkupRepairUnknownToolNameStaysText(t *testing.T) {
	in := mkBlock(mkInvoke("rm_rf_everything", mkParam("cmd", "pwd")))
	out, calls := repairAll(testTools, []string{in})
	if len(calls) != 0 {
		t.Fatalf("未声明的工具名不得转换，实际 %d 次", len(calls))
	}
	if out != in {
		t.Fatalf("应逐字节原文透出\nwant %q\ngot  %q", in, out)
	}
}

func TestMarkupRepairProseInsideBlockStaysText(t *testing.T) {
	// 块里夹带正文 → 不是纯调用，整块原文透出。
	in := markupOpen + "\n我先说明一下：\n" + mkInvoke("exec_command", mkParam("cmd", "pwd")) + "\n" + markupClose
	out, calls := repairAll(testTools, []string{in})
	if len(calls) != 0 || out != in {
		t.Fatalf("块内夹带正文应原文透出，实际 calls=%d out=%q", len(calls), out)
	}
}

func TestMarkupRepairIncompleteBlockFlushesVerbatim(t *testing.T) {
	in := "前置说明\n" + markupOpen + "\n" + mkInvoke("exec_command", mkParam("cmd", "pwd"))
	out, calls := repairAll(testTools, []string{in})
	if len(calls) != 0 {
		t.Fatalf("未闭合的块不得转换，实际 %d 次", len(calls))
	}
	if out != in {
		t.Fatalf("未闭合的块应连起始标记一起回吐\nwant %q\ngot  %q", in, out)
	}
}

func TestMarkupRepairPartialStartTokenIsHeldThenFlushed(t *testing.T) {
	// 正文恰好以起始标记的前缀结尾（增量边界切断）：不能提前吐出半个标记，
	// 也不能吞掉——流结束时原样交还。
	partial := markupOpen[:len(markupOpen)-3]
	out, calls := repairAll(testTools, []string{"说明" + partial})
	if len(calls) != 0 || out != "说明"+partial {
		t.Fatalf("半截起始标记应原样回吐，实际 calls=%d out=%q", len(calls), out)
	}
}

func TestMarkupRepairPlainAngleBracketsUnaffected(t *testing.T) {
	// 单个全角竖线（半角全角混排）不能触发缓冲；按码点构造，避免源码里出现裸字面量。
	singlePipe := string(rune(0xFF5C))
	in := "普通 HTML：<div class=\"x\">a < b</div>，还有 3 < 5 与 <" + singlePipe + " 这种半角全角混排。"
	out, calls := repairAll(testTools, []string{in})
	if len(calls) != 0 || out != in {
		t.Fatalf("普通尖括号文本应逐字节透传，实际 calls=%d out=%q", len(calls), out)
	}
}

// --- 增量边界 ---------------------------------------------------------------

func TestMarkupRepairAnyChunkBoundary(t *testing.T) {
	in := "先读一遍控制器。\n\n" +
		mkBlock(
			mkInvoke("exec_command", mkParam("cmd", "cd /tmp && ls -la")),
			mkInvoke("exec_command", mkParam("cmd", "pwd")),
		) + "\n收尾说明。"
	want := "先读一遍控制器。\n\n\n收尾说明。"

	// 整条、逐字节、以及一个错位的固定步长，三种切法必须同结果。
	chunkings := map[string][]string{"whole": {in}}
	byByte := make([]string, 0, len(in))
	for i := 0; i < len(in); i++ {
		byByte = append(byByte, in[i:i+1])
	}
	chunkings["byByte"] = byByte
	step := make([]string, 0, len(in)/7+1)
	for i := 0; i < len(in); i += 7 {
		end := i + 7
		if end > len(in) {
			end = len(in)
		}
		step = append(step, in[i:end])
	}
	chunkings["step7"] = step

	for name, chunks := range chunkings {
		out, calls := repairAll(testTools, chunks)
		if out != want {
			t.Fatalf("%s: 透出正文错误\nwant %q\ngot  %q", name, want, out)
		}
		if len(calls) != 2 {
			t.Fatalf("%s: 应还原 2 次调用，实际 %d", name, len(calls))
		}
		if got := argsOf(t, calls[0].Arguments)["cmd"]; got != "cd /tmp && ls -la" {
			t.Fatalf("%s: 首个调用参数错误: %#v", name, got)
		}
	}
}

// TestMarkupRepairRealLeakedSample 用生产环境真实泄漏的正文做回归：夹具是 2026-10-06
// 13:20 那次 Codex 会话里落盘的助手原文（testdata/leaked_markup.txt，逐字节复制）。
// 它同时覆盖两个真实特征：标记前带一段正文、一次两个 invoke。
func TestMarkupRepairRealLeakedSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/leaked_markup.txt")
	if err != nil {
		t.Fatalf("读取夹具失败: %v", err)
	}
	in := string(raw)
	out, calls := repairAll(map[string]bool{"exec_command": true}, []string{in})
	if len(calls) != 2 {
		t.Fatalf("真实样本应还原 2 次调用，实际 %d（out=%q）", len(calls), out)
	}
	if strings.Contains(out, markupPipe) {
		t.Fatalf("透出正文里仍残留标记: %q", out)
	}
	if !strings.HasPrefix(out, "我先看一下仓库结构和图片内容，再定位价格同步逻辑。") {
		t.Fatalf("标记前的正文丢了: %q", out)
	}
	for i, c := range calls {
		if c.Name != "exec_command" {
			t.Fatalf("第 %d 次调用工具名错误: %q", i, c.Name)
		}
		args := argsOf(t, c.Arguments)
		if _, ok := args["cmd"]; !ok {
			t.Fatalf("第 %d 次调用缺 cmd 参数: %s", i, c.Arguments)
		}
	}
}

// TestMarkupRepairByteEquivalence 是修复层最重要的一条不变量：不转换时，透出字节
// 与输入逐字节相同；转换时，透出字节等于「输入去掉被转换的标记块」。
func TestMarkupRepairByteEquivalence(t *testing.T) {
	cases := []struct {
		name    string
		allowed map[string]bool
		in      string
		want    string
		calls   int
	}{
		{"空正文", testTools, "", "", 0},
		{"纯正文", testTools, "hello 世界", "hello 世界", 0},
		{"未声明工具", nil, mkBlock(mkInvoke("exec_command", mkParam("cmd", "pwd"))),
			mkBlock(mkInvoke("exec_command", mkParam("cmd", "pwd"))), 0},
		{"畸形参数", testTools,
			markupOpen + mkInvoke("exec_command", "垃圾参数") + markupClose,
			markupOpen + mkInvoke("exec_command", "垃圾参数") + markupClose, 0},
		{"单块", testTools,
			"a" + mkBlock(mkInvoke("exec_command", mkParam("cmd", "pwd"))) + "b", "ab", 1},
		{"双块", testTools,
			mkBlock(mkInvoke("exec_command", mkParam("cmd", "a"))) + "中间" +
				mkBlock(mkInvoke("exec_command", mkParam("cmd", "b"))), "中间", 2},
	}
	for _, tc := range cases {
		out, calls := repairAll(tc.allowed, []string{tc.in})
		if out != tc.want {
			t.Fatalf("%s: 透出正文错误\nwant %q\ngot  %q", tc.name, tc.want, out)
		}
		if len(calls) != tc.calls {
			t.Fatalf("%s: 调用数错误 want %d got %d", tc.name, tc.calls, len(calls))
		}
	}
}

func TestMarkupRepairConvertedCounter(t *testing.T) {
	r := NewMarkupRepair(testTools, false)
	in := mkBlock(
		mkInvoke("exec_command", mkParam("cmd", "a")),
		mkInvoke("exec_command", mkParam("cmd", "b")),
	)
	if _, calls := r.Feed(in); len(calls) != 2 {
		t.Fatalf("应还原 2 次调用，实际 %d", len(calls))
	}
	if r.Converted() != 2 {
		t.Fatalf("Converted 应为 2，实际 %d", r.Converted())
	}
	// 未启用的修复器计数恒为 0。
	if got := NewMarkupRepair(nil, false).Converted(); got != 0 {
		t.Fatalf("未启用时 Converted 应为 0，实际 %d", got)
	}
}

// --- 工具名单提取 -----------------------------------------------------------

func TestToolNameAllowlist(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"openai 形态", `{"tools":[{"type":"function","function":{"name":"exec_command"}},
			{"type":"function","function":{"name":"write_stdin"}}]}`, []string{"exec_command", "write_stdin"}},
		{"responses 裸 name", `{"tools":[{"type":"function","name":"exec_command"}]}`, []string{"exec_command"}},
		{"旧版 functions", `{"functions":[{"name":"exec_command"}]}`, []string{"exec_command"}},
		{"历史 tool_calls", `{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"exec_command"}}]}]}`, []string{"exec_command"}},
		{"历史 function_call", `{"messages":[{"role":"assistant","function_call":{"name":"exec_command"}}]}`, []string{"exec_command"}},
		{"无工具", `{"model":"x","messages":[]}`, nil},
		{"空 tools", `{"tools":[]}`, nil},
		{"非法 JSON", `{`, nil},
		{"名字为空", `{"tools":[{"type":"function","function":{"name":""}}]}`, nil},
	}
	for _, tc := range cases {
		got := ToolNameAllowlist([]byte(tc.body))
		if len(got) != len(tc.want) {
			t.Fatalf("%s: 工具数错误 want %v got %v", tc.name, tc.want, got)
		}
		for _, n := range tc.want {
			if !got[n] {
				t.Fatalf("%s: 缺少工具 %q（got %v）", tc.name, n, got)
			}
		}
	}
}

// --- 非流式聚合响应修复 -----------------------------------------------------

func aggregatedResponse(content, finish string) map[string]any {
	return map[string]any{
		"id": "chatcmpl-test", "object": "chat.completion", "model": "global:deepseek-v4.1-flash",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": finish,
		}},
	}
}

func TestRepairAggregatedResponse(t *testing.T) {
	content := "先读一遍控制器。\n\n" + mkBlock(mkInvoke("exec_command", mkParam("cmd", "ls -la")))
	resp := aggregatedResponse(content, "stop")
	if RepairAggregatedResponse(resp, testTools, false).Converted() == 0 {
		t.Fatal("应报告发生改写")
	}
	choice := resp["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	if got := msg["content"].(string); got != "先读一遍控制器。\n\n" {
		t.Fatalf("正文应剥掉标记，实际 %q", got)
	}
	tcs, ok := msg["tool_calls"].([]any)
	if !ok || len(tcs) != 1 {
		t.Fatalf("应产出 1 个 tool_call，实际 %#v", msg["tool_calls"])
	}
	tc := tcs[0].(map[string]any)
	if tc["type"] != "function" || tc["id"] == "" {
		t.Fatalf("tool_call 形态不合法: %#v", tc)
	}
	fn := tc["function"].(map[string]any)
	if fn["name"] != "exec_command" {
		t.Fatalf("工具名错误: %#v", fn["name"])
	}
	if got := argsOf(t, fn["arguments"].(string))["cmd"]; got != "ls -la" {
		t.Fatalf("参数错误: %#v", got)
	}
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason 应改写为 tool_calls，实际 %#v", choice["finish_reason"])
	}
}

func TestRepairAggregatedResponseSkips(t *testing.T) {
	markup := mkBlock(mkInvoke("exec_command", mkParam("cmd", "ls -la")))

	// 已有结构化 tool_calls → 不覆盖。
	withCalls := aggregatedResponse(markup, "stop")
	withCalls["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["tool_calls"] =
		[]any{map[string]any{"id": "real", "type": "function"}}
	if RepairAggregatedResponse(withCalls, testTools, false).Converted() != 0 {
		t.Fatal("已有 tool_calls 时不得改写")
	}

	// finish_reason=length（正文被截断）→ 不解释。
	if RepairAggregatedResponse(aggregatedResponse(markup, "length"), testTools, false).Converted() != 0 {
		t.Fatal("finish_reason=length 时不得改写")
	}

	// 客户端没声明 tools → 不启用。
	if RepairAggregatedResponse(aggregatedResponse(markup, "stop"), nil, false).Converted() != 0 {
		t.Fatal("未声明 tools 时不得改写")
	}

	// 纯正文 → 零改写。
	plain := aggregatedResponse("普通回答，没有任何标记。", "stop")
	if RepairAggregatedResponse(plain, testTools, false).Converted() != 0 {
		t.Fatal("纯正文不得改写")
	}
	if plain["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] !=
		"普通回答，没有任何标记。" {
		t.Fatal("纯正文内容被改动")
	}
}

// --- 弱判定（名单为空，实测最常见的泄漏形态）--------------------------------

// TestMarkupRepairWeakModeRealSample 生产场景回归：请求体里没有 tools 名单（tools 在
// 中转环节丢失），夹具是真实泄漏正文。弱判定必须照样还原出 2 次调用。
func TestMarkupRepairWeakModeRealSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/leaked_markup.txt")
	if err != nil {
		t.Fatalf("读取夹具失败: %v", err)
	}
	r := NewMarkupRepair(nil, true)
	out, calls := r.Feed(string(raw))
	out += r.Flush()
	if len(calls) != 2 {
		t.Fatalf("弱判定应还原 2 次调用，实际 %d（out=%q）", len(calls), out)
	}
	if strings.Contains(out, markupPipe) {
		t.Fatalf("透出正文里仍残留标记: %q", out)
	}
	if r.Converted() != 2 || r.Seen() != 1 || r.Rejected() != 0 {
		t.Fatalf("计数错误 converted=%d seen=%d rejected=%d", r.Converted(), r.Seen(), r.Rejected())
	}
}

// TestMarkupRepairWeakModeNameShape 弱判定靠名字形态把关：标识符放行，占位符/畸形拒绝。
func TestMarkupRepairWeakModeNameShape(t *testing.T) {
	cases := []struct {
		name   string
		tool   string
		wantOK bool
	}{
		{"下划线", "exec_command", true},
		{"点号冒号", "mcp.server:tool", true},
		{"短横线", "write-stdin", true},
		{"大写占位符", "NAME", true}, // 形态合法；拦它的是严格判定那条路
		{"尖括号占位符", "<tool_name>", false},
		{"空名", "", false},
		{"带空格", "exec command", false},
		{"超长", strings.Repeat("a", 65), false},
	}
	for _, tc := range cases {
		in := mkBlock(mkInvoke(tc.tool, mkParam("cmd", "ls")))
		r := NewMarkupRepair(nil, true)
		out, got := r.Feed(in)
		out += r.Flush()
		if ok := len(got) == 1; ok != tc.wantOK {
			t.Fatalf("%s: 弱判定结果=%v want %v（out=%q）", tc.name, ok, tc.wantOK, out)
		}
	}
}

// TestMarkupRepairWeakModeStillRejectsMalformed 弱判定不放松结构要求：夹带正文 /
// 参数畸形 / 未闭合，一律原文透出。
func TestMarkupRepairWeakModeStillRejectsMalformed(t *testing.T) {
	cases := []string{
		markupOpen + "\n说明文字\n" + mkInvoke("exec_command", mkParam("cmd", "ls")) + "\n" + markupClose,
		markupOpen + mkInvoke("exec_command", "垃圾参数") + markupClose,
		markupOpen + mkInvoke("exec_command", mkParam("cmd", "ls")),
	}
	for i, in := range cases {
		r := NewMarkupRepair(nil, true)
		out, calls := r.Feed(in)
		out += r.Flush()
		if len(calls) != 0 || out != in {
			t.Fatalf("case %d: 弱判定不得放行畸形块（calls=%d out=%q）", i, len(calls), out)
		}
	}
}

// TestMarkupRepairDisabled 两个开关都为假（以及 nil 修复器）时整体禁用，恒等透传。
func TestMarkupRepairDisabled(t *testing.T) {
	in := mkBlock(mkInvoke("exec_command", mkParam("cmd", "ls")))
	for _, r := range []*MarkupRepair{NewMarkupRepair(nil, false), nil} {
		if r.Enabled() {
			t.Fatal("Enabled 应为 false")
		}
		out, calls := r.Feed(in)
		if out != in || len(calls) != 0 || r.Flush() != "" {
			t.Fatalf("禁用时应恒等透传: out=%q calls=%d", out, len(calls))
		}
	}
}
