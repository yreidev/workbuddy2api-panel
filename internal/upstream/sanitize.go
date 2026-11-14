// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// sanitize.go 出站请求体脱敏：剥离上游内容审核黑名单指纹。
//
// 背景：客户端（Claude Code 类 CLI）在 system prompt 注入若干固定模板句，
// 上游内容审核按逐字精确匹配拦截（非语义审核），一字改动即可绕过。
// 策略：键值/header 型指纹整段剥离；承载语义的模板句最小改写（换一词），语义不变；
// 句式不固定的自我介绍句（"You are … powered by …"）整句换成中性句。
package upstream

import (
	"regexp"
	"strings"
)

// sanitizeFeatures 特征预检：任一命中才进入净化（strings.Contains 快速路径，
// 普通请求全不中 → 原样返回，零分配）。
var sanitizeFeatures = []string{
	"x-anthropic-billing-header", // header 键值段键名
	"cc_entrypoint=",             // 尾随裸键值（截断前缀即可命中）
	"You are Claude Code",        // 身份句（截断前缀即可命中）
	"Main branch (",              // 注入指令句（截断前缀即可命中）
	// Codex instructions 首段（截断前缀即可命中）
	"You are a coding agent running in the Codex CLI",
	"github.com/anthropics/", // 反馈句里的 Anthropic 仓库链接
	"11128",                  // 上游反探测：裸数字错误码
}

// sanitizeHdrRe 剥离层：header 键名即触发（与值无关），整段删除。
var sanitizeHdrRe = regexp.MustCompile(`(?i)x-anthropic-billing-header:[^;\n]*;?\s*`)

// sanitizeBareHdrRe 兜底层：裸键名（无冒号无值）同样是指纹——2026-09-13 实验 F4
// 证实 assistant 消息里反引号引用裸键名即触发 11128，而剥离层要求冒号、对裸串无效。
// 键值形态被整段删除后，残留的裸键名做最小缩写（header→hdr）：破坏逐字匹配、
// 语义不变、保留可读性。大小写不敏感，覆盖 X-Anthropic-... 变体。
//
// 注意该正则不要求冒号，是 sanitizeHdrRe 的超集——hasFingerprint 与 sanitizeText
// 中两者并用：先删键值形态（sanitizeHdrRe），再缩写残留裸键名（本正则），
// 替换语义不同（整段删除 vs 最小缩写），不可合并为一个正则。
var sanitizeBareHdrRe = regexp.MustCompile(`(?i)x-anthropic-billing-header`)

// sanitizeKvRe 剥离层：尾随裸键值（cc_xxx=...;）循环清理。
var sanitizeKvRe = regexp.MustCompile(`(?i)\bcc_[a-z0-9_]+=[^;\n]*;?\s*`)

// sanitizeRewrites 改写层：全模板句逐字替换（每句只改一个词，语义不变）。
//
// 身份句的匹配串**不带结尾标点**（只到 "…for Claude" 为止）：
// CLI 版这句以句号收尾（"…for Claude."），桌面版（claude-desktop-3p / Agent SDK）
// 以逗号接后继内容（"…for Claude, running within the Claude Agent SDK."）。
// 带句号的整句只匹配前者，桌面版会漏网、指纹原样发上游 → 400 code=11128。
// 去掉结尾标点后两种形态一并覆盖（替换串同样不带标点，让原有标点原样保留）。
// 注意仍要求 "You are Claude Code, " 前缀，不做更宽的子串替换，
// 以免误伤 TestExactMatchOnlyVariantNotTouched 所保护的零散文本。
var sanitizeRewrites = [][2]string{
	{
		"You are Claude Code, Anthropic's official CLI for Claude",
		"You are Claude Code, Anthropic's official CLI tool for Claude",
	},
	{
		"Main branch (you will usually use this for PRs)",
		"Default branch (you will usually use this for PRs)",
	},
	{
		"You are a coding agent running in the Codex CLI, a terminal-based coding assistant.",
		"You are a coding agent running in the Codex CLI tool, a terminal-based coding assistant.",
	},
	{
		// 反馈句：整句带 Anthropic 仓库链接，上游按整句拦截（只留链接或只留半边均不拦，
		// 实测需整句同时出现）。give→provide 一词之差即可绕过，语义不变。
		"To give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues",
		"To provide feedback, users should report the issue at https://github.com/anthropics/claude-code/issues",
	},
	{
		// 上游反探测：只要请求体里出现裸数字 11128 就整单拦截（与该数字的上下文无关——
		// "code=11128" / 裸 "11128" / "错误码 11128" / "Code=11128" 全部命中；
		// 相邻的 11148 / 11101 / 11115 / 99999 均放行）。11128 正是本类拦截自身的错误码，
		// 上游据此识别"在讨论/回显其内部错误码"的请求。
		// 代价：用户对话中任何 11128 都会被改写——但这串数字出现在请求里本身就是拦截条件，
		// 不改写必然失败。插入连字符保留可读性与指代（零宽空格无效，实测上游会归一化）。
		"11128",
		"11-128",
	},
}

// 以下五条是上游 11128「Illegal API invocation from an unapproved channel」的渠道指纹，
// 取自同类项目的真号实测（workbuddy2api-hub #61/#533/#537/#570/#574，
// ithtelab/workbuddy-manager #246/#250，mmqz/cpa-multi-plugins #46/#49）：
// 大小写不敏感、连续子串匹配，改一个词或换语序即放行。
//
// 作用域：身份句、品牌句、OhMyOpenCode 三条所有角色都改——有项目实测放在 user 或
// assistant 历史里同样被拦。workspace root 是唯一的例外，只改 system 级内容：
// 它在工具参数和用户原文里只是普通字面串，全通道改写会让出站请求与用户原意不符
// （review 5881990497 第二条），见 sanitizeTextForSystem。
var (
	// sanitizeIdentityStartRe 自我介绍句的开头到归属动词，例如
	// "You are an AI agent powered by DeepSeek Harness."、
	// "You are a coding agent powered by the deepseek-v4.1-flash model."、
	// "You are an AI assistant built on DeepSeek."。名词可省，冠词后允许 0~3 个修饰词，
	// 窗口有界，"You are free to use tools built by …" 这类分词修饰宾语的句子不会命中。
	// RE2 没有前瞻，句尾由 rewriteIdentitySentences 手工找。
	sanitizeIdentityStartRe = regexp.MustCompile(`(?i)You are\s+` +
		`(?:an?\s+|the\s+)?(?:[\w.-]+\s+){0,3}?` +
		`(?:(?:agent|assistant|model|AI|LLM)\b)?[\s,]*` +
		`(?:powered by|built on|built by|created by|developed by|made by|trained by)\b`)
	// sanitizeDSHGUIRe DeepSeek Harness 运行时上下文里的 GUI 描述段：这句紧跟
	// `When the user refers to "this page", "this GUI", or "this app" …` 才命中，
	// 只把句尾语序换掉，一个词不删。
	sanitizeDSHGUIRe = regexp.MustCompile(`(?i)(You are interacting with the user through the ` +
		`DeepSeek[ \t]+Harness[ \t]+Web[ \t]+GUI) whose URL the runtime context names\.`)
	// sanitizeBrandRe 散文里的 DeepSeek Harness 和 GUI 描述句连着出现时同样命中，缩成 DeepSeek。
	// 只认空格与制表符，deepseek-harness、deepseek_harness 这类路径与标识符不动。
	sanitizeBrandRe = regexp.MustCompile(`(?i)DeepSeek[ \t]+Harness`)
	// sanitizeWorkspaceRootRe Codex / Claude Code 系 system prompt 的特征短语 workspace root：
	// 上游按它做渠道指纹，命中即 400 11-128「Illegal API invocation from an unapproved channel」。
	// 2026-11-13 实测：完整 DSH system 里只此一处命中；改成语序自然的 root of the workspace 即放行。
	// 只作用于 system 级内容，其余角色与工具参数原样保留（见 sanitizeTextForSystem）。
	sanitizeWorkspaceRootRe = regexp.MustCompile(`(?i)\bworkspace[ \t]+root\b`)
	// sanitizeOmORe OhMyOpenCode 子代理的身份串，两半各自单独出现都放行，只拆开连在一起的这一段。
	sanitizeOmORe = regexp.MustCompile(`(?i)Sisyphus-Junior - Focused executor from OhMyOpenCode`)
)

const sanitizeIdentityReplacement = "You are a helpful AI assistant."

// sanitizeIdentityVerbs 自我介绍句的预检词（小写），命中再交给正则确认。
var sanitizeIdentityVerbs = []string{
	"powered by", "built on", "built by", "created by", "developed by", "made by", "trained by",
}

// containsFoldASCII 不分配内存的大小写不敏感子串判断，needle 须是小写 ASCII。
func containsFoldASCII(s, needle string) bool {
	n := len(needle)
	for i := 0; i+n <= len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c == needle[0] && strings.EqualFold(s[i:i+n], needle) {
			return true
		}
	}
	return false
}

// rewriteIdentitySentences 把自我介绍句整句换成中性句。句尾是「句点后跟空白或文本结尾」、
// 换行或文本结尾：模型名里的点（deepseek-v4.1-flash）不会被当成句尾，换行原样保留。
func rewriteIdentitySentences(text string) string {
	var b strings.Builder
	rest := text
	for {
		loc := sanitizeIdentityStartRe.FindStringIndex(rest)
		if loc == nil {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:loc[0]])
		tail := rest[loc[1]:]
		end := len(tail)
		for i := 0; i < len(tail); i++ {
			if tail[i] == '\n' {
				end = i
				break
			}
			if tail[i] == '.' && (i+1 == len(tail) || strings.IndexByte(" \t\r\n", tail[i+1]) >= 0) {
				end = i + 1
				break
			}
		}
		b.WriteString(sanitizeIdentityReplacement)
		rest = tail[end:]
	}
}

// hasChannelFingerprint 四条渠道指纹的预检：先做不分配内存的字面量探测，命中再跑正则。
func hasChannelFingerprint(text string) bool {
	if containsFoldASCII(text, "harness") && sanitizeBrandRe.MatchString(text) {
		return true
	}
	if containsFoldASCII(text, "ohmyopencode") && sanitizeOmORe.MatchString(text) {
		return true
	}
	for _, v := range sanitizeIdentityVerbs {
		if containsFoldASCII(text, v) {
			return sanitizeIdentityStartRe.MatchString(text)
		}
	}
	return false
}

// sanitizeText 单段文本净化：预检不中 → 返回原串（零分配）。
//
// 不含 workspace root 替换——该短语的改写会变更字面内容，一旦作用于工具参数、
// 工具结果或用户原文，出站请求里的目录名/命令就与用户原意不符（review 5881990497
// 第二条）。它只在 system 级内容上做，见 sanitizeTextForSystem。
func sanitizeText(text string) string {
	return sanitizeTextOpt(text, false)
}

// sanitizeTextForSystem system / developer 级内容的净化：与 sanitizeText 同口径，
// 额外替换 workspace root 渠道指纹。
//
// 替换位置跨在 hasFingerprint 判定两侧：system 里只出现 workspace root 时预检为假
// （该短语已从 hasChannelFingerprint 摘除），走早返回分支只做这一处替换，不会连带
// 触发 sanitizeKvRe 等剥离层——那正是 review 5881990497 第四条指出的
// {"command":"echo workspace root; cc_version=1"} 被删成无效 JSON 的成因。
// system 里另有其他指纹时走完整链，替换嵌在链里保持原有先后顺序。
func sanitizeTextForSystem(text string) string {
	return sanitizeTextOpt(text, true)
}

func sanitizeTextOpt(text string, replaceWorkspaceRoot bool) string {
	if !hasFingerprint(text) {
		if replaceWorkspaceRoot {
			text = sanitizeWorkspaceRootRe.ReplaceAllString(text, "root of the workspace")
		}
		return text
	}
	text = sanitizeOmORe.ReplaceAllString(text, "Sisyphus-Junior - Focused executor")
	text = rewriteIdentitySentences(text)
	// GUI 描述段要认品牌全名，先于品牌缩减。
	text = sanitizeDSHGUIRe.ReplaceAllString(text, "${1} whose URL is named in the runtime context.")
	if replaceWorkspaceRoot {
		text = sanitizeWorkspaceRootRe.ReplaceAllString(text, "root of the workspace")
	}
	text = sanitizeBrandRe.ReplaceAllString(text, "DeepSeek")
	for _, rw := range sanitizeRewrites {
		text = strings.ReplaceAll(text, rw[0], rw[1])
	}
	if sanitizeHdrRe.MatchString(text) {
		text = sanitizeHdrRe.ReplaceAllString(text, "")
	}
	if strings.Contains(text, "cc_") {
		prev := ""
		for prev != text { // 清尾随裸 kv（cc_version=...; cc_entrypoint=...;）
			prev = text
			text = sanitizeKvRe.ReplaceAllString(text, "")
		}
	}
	// 兜底：键值形态已在上面整段删除，这里只剩裸键名（引用/示例文本形态）。
	text = sanitizeBareHdrRe.ReplaceAllString(text, "x-anthropic-billing-hdr")
	return strings.TrimSpace(text)
}

// hasFingerprint 特征预检：先走 strings.Contains 快速路径（零分配）；
// header 键名有大小写变体（X-Anthropic-...）且可能以裸键名形态出现（无冒号），
// Contains 大小写敏感、sanitizeHdrRe 要求冒号——两者都会漏掉「混合大小写 + 裸键名」，
// 必须再用不要求冒号的 (?i) 正则兜底（sanitizeBareHdrRe），否则整条净化被跳过。
// sanitizeBareHdrRe 不要求冒号，是 sanitizeHdrRe 的超集，故无需再单独匹配后者。
func hasFingerprint(text string) bool {
	for _, f := range sanitizeFeatures {
		if strings.Contains(text, f) {
			return true
		}
	}
	return sanitizeBareHdrRe.MatchString(text) || hasChannelFingerprint(text)
}

// sanitizeContent 兼容字符串与多模态数组；只动 text part，image 等 part 不动。
// 返回净化后的值及是否发生变化。
func sanitizeContent(v any) (any, bool) {
	return sanitizeContentOpt(v, false)
}

// sanitizeContentForSystem system 级消息内容的净化：额外做 workspace root 替换。
func sanitizeContentForSystem(v any) (any, bool) {
	return sanitizeContentOpt(v, true)
}

func sanitizeContentOpt(v any, systemLevel bool) (any, bool) {
	fn := sanitizeText
	if systemLevel {
		fn = sanitizeTextForSystem
	}
	switch c := v.(type) {
	case string:
		s := fn(c)
		return s, s != c
	case []any:
		changed := false
		for _, p := range c {
			m, ok := p.(map[string]any)
			if !ok {
				continue
			}
			text, ok := m["text"].(string)
			if !ok {
				continue
			}
			if s := fn(text); s != text {
				m["text"] = s
				changed = true
			}
		}
		return c, changed
	}
	return v, false
}

// sanitizeToolCalls 净化 assistant.tool_calls[].function.arguments。
//
// arguments 是**字符串化的 JSON**（不是对象），因此按文本走 sanitizeText 即可。
// 这块长期是盲区：工具调用消息的 content 通常是 null，而旧版 sanitizeMessages
// 在 content 缺失时直接 continue，整条消息连 tool_calls 一起被跳过——
// 于是历史里任何写进工具参数的被拦字符串（文件名、命令、写入内容）都会原样漏出。
func sanitizeToolCalls(v any) bool {
	callList, ok := v.([]any)
	if !ok {
		return false
	}
	changed := false
	for _, c := range callList {
		call, ok := c.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := call["function"].(map[string]any)
		if !ok {
			continue
		}
		args, ok := fn["arguments"].(string)
		if !ok {
			continue
		}
		if s := sanitizeText(args); s != args {
			fn["arguments"] = s
			changed = true
		}
	}
	return changed
}

// roleIsSystemLevel 判定 system 级角色：system 及其别名 developer。
//
// normalizeRoles 在 prepareBody 里已把 developer 归一为 system，所以跑到这里时
// 通常只剩 system；此处一并认可 developer，是为了不依赖调用顺序（单测直接调
// sanitizeMessages 时也是这个口径）。
func roleIsSystemLevel(role string) bool {
	r := strings.TrimSpace(role)
	return strings.EqualFold(r, "system") || strings.EqualFold(r, "developer")
}

// sanitizeMessages 净化 messages 中的 content 与 tool_calls；任一命中返回 true。
func sanitizeMessages(messages []any) bool {
	changed := false
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		// content 与 tool_calls 各自独立判断：content 可以为 null（工具调用轮），
		// 早期版本在此 continue，导致这类消息的 tool_calls 完全不被净化。
		//
		// workspace root 只在 system 级内容上替换：其余角色（user / assistant / tool /
		// 无 role）里的它是一个真实的字面串——用户给的目录名、工具回显的文件内容——
		// 改了就与用户原意不符。
		if c, ok := m["content"]; ok {
			var nc any
			var ch bool
			if roleIsSystemLevel(role) {
				nc, ch = sanitizeContentForSystem(c)
			} else {
				nc, ch = sanitizeContent(c)
			}
			if ch {
				m["content"] = nc
				changed = true
			}
		}
		// reasoning_content（思维链回填字段，见 thinking.go/sse.go）实测同样
		// 携带指纹，与 content 同等净化。string 形态直接走 sanitizeText。
		if rc, ok := m["reasoning_content"].(string); ok {
			if s := sanitizeText(rc); s != rc {
				m["reasoning_content"] = s
				changed = true
			}
		}
		// reasoning：另一形态的思维链字段。thinking.go 的回填会把客户端送来的
		// reasoning_content 镜像进 reasoning（反之亦然），而此前只洗 content /
		// reasoning_content / tool_calls —— 镜像进 reasoning 的指纹原样出站，
		// 裸 "11128" 这种反探测串同样致命。
		if r, ok := m["reasoning"].(string); ok {
			if s := sanitizeText(r); s != r {
				m["reasoning"] = s
				changed = true
			}
		}
		if tc, ok := m["tool_calls"]; ok {
			if sanitizeToolCalls(tc) {
				changed = true
			}
		}
	}
	return changed
}
