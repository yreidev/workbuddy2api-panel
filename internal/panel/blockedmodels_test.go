// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"net/http"
	"strings"
	"testing"
)

func blockedOf(t *testing.T, p *Panel, uid string) string {
	t.Helper()
	var names []string
	for _, b := range mustStatus(t, p, uid).BlockedModels {
		names = append(names, b.Model)
	}
	return strings.Join(names, ",")
}

// TestNormalizeBlockedModels 去空白、去空项、不区分大小写去重；带前缀、有空格、太长、太多的报错。
func TestNormalizeBlockedModels(t *testing.T) {
	got, err := normalizeBlockedModels([]string{" gpt-* ", "", "GPT-*", "deepseek-v4.1-flash"})
	if err != nil || strings.Join(got, ",") != "gpt-*,deepseek-v4.1-flash" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	many := make([]string, blockedModelsMax+1)
	for i := range many {
		many[i] = "m" + strings.Repeat("x", i)
	}
	for _, in := range [][]string{{"global:gpt-5"}, {"gpt 5"}, {strings.Repeat("x", blockedModelMax+1)}, many} {
		if _, err := normalizeBlockedModels(in); err == nil {
			t.Errorf("%.40v 应报错", in)
		}
	}
}

// TestAccountBlockedModels 账号详情整份保存：自动加的留着就保留原因，去掉就解除；不合法回 400，号不存在回 404。
func TestAccountBlockedModels(t *testing.T) {
	p, _ := newPanelFor(t, Config{})
	p.cfg.Pool.Add(taskTestAuth("u1", "一号"))
	p.cfg.Pool.BlockModelForever("u1", "gpt-5.6-sol", "11158 model not allowed for account type")
	p.cfg.Pool.BlockModelForever("u1", "gemini-3-pro", "11158 model not allowed for account type")

	rec := postJSON(t, p, "/panel/api/accounts/u1/blocked_models", `{"models":["gpt-5.6-sol"," kimi-* "]}`)
	autoWantCode(t, rec, http.StatusOK)
	list, _ := autoDecode(t, rec)["blocked_models"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["auto"] != true || list[1].(map[string]any)["model"] != "kimi-*" {
		t.Fatalf("返回的名单 = %v", list)
	}
	if got := blockedOf(t, p, "u1"); got != "gpt-5.6-sol,kimi-*" {
		t.Fatalf("名单 = %s", got)
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/blocked_models", `{`), http.StatusBadRequest)
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/blocked_models", `{"models":["cn:glm-5"]}`), http.StatusBadRequest)
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/nope/blocked_models", `{"models":[]}`), http.StatusNotFound)
	if got := blockedOf(t, p, "u1"); got != "gpt-5.6-sol,kimi-*" {
		t.Fatalf("不合法的请求不应改名单: %s", got)
	}
}

// TestAccountsBatchBlockedModels 多选后批量加、删不接的模型：删的时候自动加的也删；不在池里的号列进 failed；
// 没填模型、填得不合法回 400。
func TestAccountsBatchBlockedModels(t *testing.T) {
	p, _ := newPanelFor(t, Config{})
	for _, uid := range []string{"u1", "u2"} {
		p.cfg.Pool.Add(taskTestAuth(uid, uid))
	}
	p.cfg.Pool.BlockModelForever("u2", "gpt-5.6-sol", "11158 model not allowed for account type")

	rec := postJSON(t, p, "/panel/api/accounts/batch", `{"action":"block_models","uids":["u1","u2","nope"],"models":["gpt-*","GPT-*"]}`)
	autoWantCode(t, rec, http.StatusOK)
	if resp := autoDecode(t, rec); resp["ok"] != float64(2) || len(resp["failed"].([]any)) != 1 {
		t.Fatalf("resp = %v", resp)
	}
	if blockedOf(t, p, "u1") != "gpt-*" || blockedOf(t, p, "u2") != "gpt-5.6-sol,gpt-*" {
		t.Fatalf("加完: u1=%s u2=%s", blockedOf(t, p, "u1"), blockedOf(t, p, "u2"))
	}
	rec = postJSON(t, p, "/panel/api/accounts/batch", `{"action":"unblock_models","uids":["u1","u2"],"models":["gpt-*","gpt-5.6-sol"]}`)
	autoWantCode(t, rec, http.StatusOK)
	if blockedOf(t, p, "u1") != "" || blockedOf(t, p, "u2") != "" {
		t.Fatalf("删完应为空: u1=%s u2=%s", blockedOf(t, p, "u1"), blockedOf(t, p, "u2"))
	}
	for _, body := range []string{
		`{"action":"block_models","uids":["u1"],"models":[" "]}`,
		`{"action":"unblock_models","uids":["u1"],"models":["global:gpt-5"]}`,
	} {
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/batch", body), http.StatusBadRequest)
	}
}
