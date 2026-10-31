// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestNonChatModelGenerationTags 生成类（图片/视频）模型不得进对话目录。
// 回归用例：nonChatModel 早期只拦 text-to-image，桌面端目录下的
// text-to-video / image-to-video（seedance 系列）会漏过并混进 /v1/models。
func TestNonChatModelGenerationTags(t *testing.T) {
	cases := []struct {
		name string
		id   string
		out  int64
		tags []string
		want bool
	}{
		{"图片生成（既有规则）", "hunyuan-image-alpha", 0, []string{"text-to-image"}, true},
		{"图生图", "gpt-image-2.5-sunburst", 0, []string{"text-to-image", "image-to-image"}, true},
		{"文生视频（本次修复）", "seedance-2.5", 0, []string{"text-to-video", "image-to-video"}, true},
		{"图生视频（本次修复）", "seedance-2.5-pro", 1024, []string{"image-to-video"}, true},
		{"普通对话模型", "gpt-6-sol", 128000, nil, false},
		{"新模型无 tags", "grok-4.7", 128000, []string{}, false},
		{"对话模型带无关 tag", "balanced-model", 32000, []string{"craft"}, false},
		{"tiny 输出仍剔除", "completion-1.0", 256, nil, true},
		{"nes 前缀仍剔除", "nes-1.2", 8192, nil, true},
	}
	for _, c := range cases {
		if got := nonChatModel(c.id, c.out, c.tags); got != c.want {
			t.Errorf("%s: nonChatModel(%q, %d, %v) = %v, want %v", c.name, c.id, c.out, c.tags, got, c.want)
		}
	}
}

// TestDesktopUAForRealm 两域桌面端的 UA：内置 CLI 是 `WorkBuddy/<ver> <applicationName>/<ver> CLI/<cli>`，
// 后台服务两段都是 applicationName；国际版 applicationName 是 workbuddy-ai（不是 productName「WorkBuddy AI」）。
func TestDesktopUAForRealm(t *testing.T) {
	c := New()
	global := &auth.Auth{UID: "u1", Domain: "www.workbuddy.ai"}
	global.BackfillRealm()
	cn := &auth.Auth{UID: "u2", Domain: "copilot.tencent.com"}
	cn.BackfillRealm()
	for _, tc := range []struct {
		name, got, want string
	}{
		{"国际版 CLI", c.defaultCLIUA(global), "WorkBuddy/5.9.4 workbuddy-ai/5.9.4 CLI/2.165.0"},
		{"国际版后台服务", c.defaultDaemonUA(global), "workbuddy-ai/5.9.4 workbuddy-ai/5.9.4 CLI/2.165.0"},
		{"国内版 CLI", c.defaultCLIUA(cn), "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0"},
		{"国内版后台服务", c.defaultDaemonUA(cn), "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s UA = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if !contains(c.defaultCLIUA(global), "CLI/") {
		t.Error("UA 应带 CLI 段")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
