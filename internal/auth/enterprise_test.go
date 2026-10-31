// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package auth

import (
	"encoding/base64"
	"testing"
)

// TestIsEnterprise 企业版判定：enterpriseId 非空即企业版（纯空白视为非企业）。
// 该判定驱动 scheduler 五类任务与 panel 成长任务/签到入口的门控——企业版在这些
// 端点上被上游一律拒绝（400 code 10001 / 403 growth-only），误判为个人号会导致
// 每日对注定失败的端点空发请求。
func TestIsEnterprise(t *testing.T) {
	cases := []struct {
		name string
		ent  string
		want bool
	}{
		{"个人号（空）", "", false},
		{"纯空白", "   ", false},
		{"企业号", "fn7xvyc51dkw", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Auth{UID: "u1", EnterpriseID: c.ent}
			if got := a.IsEnterprise(); got != c.want {
				t.Errorf("EnterpriseID=%q IsEnterprise()=%v want %v", c.ent, got, c.want)
			}
		})
	}
}

// TestIsEnterpriseAndIsGlobalIndependent 企业标识与 realm 是正交维度：
// 企业号恒为 CN realm，但两者判定互不干扰（门控引用处并列书写）。
func TestIsEnterpriseAndIsGlobalIndependent(t *testing.T) {
	a := &Auth{UID: "u1", EnterpriseID: "ent-1"}
	if !a.IsEnterprise() {
		t.Error("应判为企业版")
	}
	if a.IsGlobal() {
		t.Error("企业号不应被判为 global")
	}
}

// TestIsEnterpriseSession 企业上下文：企业版，或访问令牌的 JWT 载荷带
// token_source=enterprise_switch；不是 JWT、解不开、别的取值都不算。
func TestIsEnterpriseSession(t *testing.T) {
	jwt := func(payload string, enc *base64.Encoding) string {
		return "eyJhbGciOiJSUzI1NiJ9." + enc.EncodeToString([]byte(payload)) + ".sig"
	}
	cases := []struct {
		name  string
		ent   string
		token string
		want  bool
	}{
		{"企业切换会话", "", jwt(`{"sub":"u1","token_source":"enterprise_switch"}`, base64.RawURLEncoding), true},
		{"带填充的 base64url 也认", "", jwt(`{"token_source":"enterprise_switch","x":1}`, base64.URLEncoding), true},
		{"企业版（令牌不看）", "ent-1", "opaque", true},
		{"个人号普通令牌", "", jwt(`{"sub":"u1","token_source":"login"}`, base64.RawURLEncoding), false},
		{"没有 token_source", "", jwt(`{"sub":"u1"}`, base64.RawURLEncoding), false},
		{"不是 JWT", "", "opaque-token", false},
		{"载荷不是 base64", "", "a.@@@.c", false},
		{"载荷不是 JSON", "", jwt(`not json`, base64.RawURLEncoding), false},
		{"空令牌", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Auth{UID: "u1", EnterpriseID: c.ent, AccessToken: c.token}
			if got := a.IsEnterpriseSession(); got != c.want {
				t.Errorf("IsEnterpriseSession()=%v want %v", got, c.want)
			}
		})
	}
}
