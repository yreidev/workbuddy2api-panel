// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/apikeys"
)

func groupsPanel(t *testing.T) (*Panel, *apikeys.Store) {
	t.Helper()
	store, err := apikeys.Open(filepath.Join(t.TempDir(), "api_keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := newPanelFor(t, Config{Keys: store})
	for _, uid := range []string{"u1", "u2"} {
		p.cfg.Pool.Add(taskTestAuth(uid, uid))
	}
	return p, store
}

// createGroup 建分组，返回 id。
func createGroup(t *testing.T, p *Panel, body string) string {
	t.Helper()
	rec := postJSON(t, p, "/panel/api/groups", body)
	autoWantCode(t, rec, http.StatusOK)
	return autoDecode(t, rec)["group"].(map[string]any)["id"].(string)
}

// TestGroupsAPI 建、改、删分组和列表：名字和备注校验，同名回 409，还有密钥限定着的分组不让删，列表带号数和密钥名。
func TestGroupsAPI(t *testing.T) {
	p, store := groupsPanel(t)
	a := createGroup(t, p, `{"name":" 主力 ","note":"常用"}`)
	b := createGroup(t, p, `{"name":"备用"}`)
	for _, body := range []string{
		`{`, `{"name":"  "}`, `{"name":"` + strings.Repeat("名", groupNameMax+1) + `"}`,
		`{"name":"x","note":"` + strings.Repeat("字", groupNoteMax+1) + `"}`, `{"name":"a\nb"}`,
	} {
		autoWantCode(t, postJSON(t, p, "/panel/api/groups", body), http.StatusBadRequest)
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/groups", `{"name":"主力"}`), http.StatusConflict)

	rec := postJSON(t, p, "/panel/api/groups/"+b, `{"name":"后备","note":"夜里用"}`)
	autoWantCode(t, rec, http.StatusOK)
	if g := autoDecode(t, rec)["group"].(map[string]any); g["name"] != "后备" || g["note"] != "夜里用" {
		t.Fatalf("改后 = %v", g)
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/groups/"+b, `{"name":"主力"}`), http.StatusConflict)
	autoWantCode(t, postJSON(t, p, "/panel/api/groups/nope", `{"name":"x"}`), http.StatusNotFound)
	autoWantCode(t, postJSON(t, p, "/panel/api/groups/"+b, `{"name":""}`), http.StatusBadRequest)

	p.cfg.Pool.SetAccountGroups("u1", []string{a})
	if _, err := store.Create(apikeys.Input{Name: "只用主力", Enabled: true, Groups: []string{a}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	rec = get(p, http.MethodGet, "/panel/api/groups")
	autoWantCode(t, rec, http.StatusOK)
	list := autoDecode(t, rec)["groups"].([]any)
	first, second := list[0].(map[string]any), list[1].(map[string]any)
	if len(list) != 2 || first["name"] != "主力" || first["accounts"] != float64(1) || first["keys"].([]any)[0] != "只用主力" ||
		second["accounts"] != float64(0) || len(second["keys"].([]any)) != 0 {
		t.Fatalf("列表 = %v", list)
	}

	rec = postJSON(t, p, "/panel/api/groups/"+a+"/delete", `{}`)
	if autoWantCode(t, rec, http.StatusConflict); !strings.Contains(rec.Body.String(), "只用主力") {
		t.Fatalf("应列出在用的密钥: %s", rec.Body)
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/groups/"+b+"/delete", `{}`), http.StatusOK)
	autoWantCode(t, postJSON(t, p, "/panel/api/groups/"+b+"/delete", `{}`), http.StatusNotFound)
}

// TestGroupsWithoutKeyStore 没有密钥库时分组照常能删，列表里的密钥为空。
func TestGroupsWithoutKeyStore(t *testing.T) {
	p, _ := newPanelFor(t, Config{})
	id := createGroup(t, p, `{"name":"A"}`)
	if keys := autoDecode(t, get(p, http.MethodGet, "/panel/api/groups"))["groups"].([]any)[0].(map[string]any)["keys"]; len(keys.([]any)) != 0 {
		t.Fatalf("keys = %v", keys)
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/groups/"+id+"/delete", `{}`), http.StatusOK)
}

// TestAccountGroupsAndPriorityAPI 单号改分组（整份、去重）和优先级（0～99）；分组不存在、格式不对回 400，号不存在回 404。
func TestAccountGroupsAndPriorityAPI(t *testing.T) {
	p, _ := groupsPanel(t)
	a := createGroup(t, p, `{"name":"A"}`)
	b := createGroup(t, p, `{"name":"B"}`)
	rec := postJSON(t, p, "/panel/api/accounts/u1/groups", `{"groups":["`+a+`","`+b+`","`+a+`"]}`)
	autoWantCode(t, rec, http.StatusOK)
	if st := mustStatus(t, p, "u1"); !slices.Equal(st.Groups, []string{a, b}) {
		t.Fatalf("分组 = %v", st.Groups)
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/groups", `{`), http.StatusBadRequest)
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/groups", `{"groups":["nope"]}`), http.StatusBadRequest)
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/gone/groups", `{"groups":[]}`), http.StatusNotFound)

	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/priority", `{"priority":7}`), http.StatusOK)
	if st := mustStatus(t, p, "u1"); st.Priority != 7 {
		t.Fatalf("优先级 = %d", st.Priority)
	}
	for _, body := range []string{`{`, `{}`, `{"priority":-1}`, `{"priority":100}`} {
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/priority", body), http.StatusBadRequest)
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/gone/priority", `{"priority":1}`), http.StatusNotFound)
}

// TestAccountsBatchGroupsAndPriority 多选后批量加进、退出分组和设优先级：不在池里的号列进 failed；分组不存在、
// 优先级缺了或越界回 400。
func TestAccountsBatchGroupsAndPriority(t *testing.T) {
	p, _ := groupsPanel(t)
	a := createGroup(t, p, `{"name":"A"}`)
	rec := postJSON(t, p, "/panel/api/accounts/batch", `{"action":"join_group","uids":["u1","u2","gone"],"group":"`+a+`"}`)
	autoWantCode(t, rec, http.StatusOK)
	if resp := autoDecode(t, rec); resp["ok"] != float64(2) || len(resp["failed"].([]any)) != 1 {
		t.Fatalf("resp = %v", resp)
	}
	if !slices.Equal(mustStatus(t, p, "u2").Groups, []string{a}) {
		t.Fatalf("u2 分组 = %v", mustStatus(t, p, "u2").Groups)
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/batch", `{"action":"leave_group","uids":["u1"],"group":"`+a+`"}`), http.StatusOK)
	if len(mustStatus(t, p, "u1").Groups) != 0 || len(mustStatus(t, p, "u2").Groups) != 1 {
		t.Fatal("退出分组只动选中的号")
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/batch", `{"action":"join_group","uids":["u1"],"group":"nope"}`), http.StatusBadRequest)

	rec = postJSON(t, p, "/panel/api/accounts/batch", `{"action":"priority","uids":["u1","u2","gone"],"priority":3}`)
	autoWantCode(t, rec, http.StatusOK)
	if resp := autoDecode(t, rec); resp["ok"] != float64(2) || mustStatus(t, p, "u2").Priority != 3 {
		t.Fatalf("resp = %v", resp)
	}
	for _, body := range []string{`{"action":"priority","uids":["u1"]}`, `{"action":"priority","uids":["u1"],"priority":100}`} {
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/batch", body), http.StatusBadRequest)
	}
}

// TestKeyGroupsSaved 密钥限定的分组：存在才让存，空白和重复去掉；不存在回 400。
func TestKeyGroupsSaved(t *testing.T) {
	p, _ := groupsPanel(t)
	a := createGroup(t, p, `{"name":"A"}`)
	rec := postJSON(t, p, "/panel/api/keys", `{"name":"k","enabled":true,"groups":[" `+a+` ","`+a+`",""]}`)
	autoWantCode(t, rec, http.StatusOK)
	if gs := autoDecode(t, rec)["key"].(map[string]any)["groups"].([]any); len(gs) != 1 || gs[0] != a {
		t.Fatalf("groups = %v", gs)
	}
	rec = postJSON(t, p, "/panel/api/keys", `{"name":"k2","enabled":true,"groups":["nope"]}`)
	if autoWantCode(t, rec, http.StatusBadRequest); !strings.Contains(rec.Body.String(), "nope") {
		t.Fatalf("应说出哪个分组不存在: %s", rec.Body)
	}
}
