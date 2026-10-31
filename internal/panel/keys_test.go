// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/apikeys"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

func keysPanel(t *testing.T, admin string) (*Panel, *apikeys.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api_keys.json")
	store, err := apikeys.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Version: "test", APIKey: admin, Keys: store, Pool: pool.New("")}), store, path
}

func keyReq(p *Panel, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-admin")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

type keyResp struct {
	OK    bool    `json:"ok"`
	Error string  `json:"error"`
	Key   keyView `json:"key"`
}

func decodeKey(t *testing.T, rec *httptest.ResponseRecorder) keyResp {
	t.Helper()
	var out keyResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body=%s", rec.Body)
	}
	return out
}

// TestKeysAPI 新建、列出、修改、重新生成、删除；面板只认管理员密钥，客户端密钥登不了面板。
func TestKeysAPI(t *testing.T) {
	p, _, path := keysPanel(t, "sk-admin")

	rec := keyReq(p, "GET", "/panel/api/keys", "")
	var list struct {
		Keys        []keyView `json:"keys"`
		AdminKeySet bool      `json:"admin_key_set"`
		Required    bool      `json:"required"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list.Keys) != 0 || !list.AdminKeySet || !list.Required {
		t.Fatalf("空列表 code=%d body=%s", rec.Code, rec.Body)
	}

	exp := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	rec = keyReq(p, "POST", "/panel/api/keys", `{"name":"张三","note":"朋友","enabled":true,"expires_at":"`+exp+`",
		"daily_limit":{"credits":10},"total_limit":{"requests":100},"models":["glm-*"]}`)
	created := decodeKey(t, rec)
	if rec.Code != 200 || !created.OK || created.Key.Name != "张三" || !strings.HasPrefix(created.Key.Secret, "sk-") ||
		!created.Key.Expired || created.Key.DailyLimit.Credits != 10 || created.Key.TotalLimit.Requests != 100 {
		t.Fatalf("新建 code=%d body=%s", rec.Code, rec.Body)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("密钥文件应为 0600: %v %v", st, err)
	}

	// 客户端密钥登不了面板
	req := httptest.NewRequest("GET", "/panel/api/keys", nil)
	req.Header.Set("Authorization", "Bearer "+created.Key.Secret)
	out := httptest.NewRecorder()
	p.ServeHTTP(out, req)
	if out.Code != http.StatusUnauthorized {
		t.Fatalf("客户端密钥不该能登面板：code=%d", out.Code)
	}

	id := created.Key.ID
	rec = keyReq(p, "POST", "/panel/api/keys/"+id, `{"name":"张三2","enabled":false,"expires_at":null}`)
	upd := decodeKey(t, rec)
	if rec.Code != 200 || upd.Key.Name != "张三2" || upd.Key.Enabled || upd.Key.Expired || upd.Key.Secret != created.Key.Secret ||
		upd.Key.DailyLimit.Credits != 0 || len(upd.Key.Models) != 0 {
		t.Fatalf("修改 code=%d body=%s", rec.Code, rec.Body)
	}

	rec = keyReq(p, "POST", "/panel/api/keys/"+id+"/regenerate", "")
	if regen := decodeKey(t, rec); rec.Code != 200 || regen.Key.Secret == created.Key.Secret {
		t.Fatalf("重新生成 code=%d body=%s", rec.Code, rec.Body)
	}

	for name, tc := range map[string]struct {
		method, path, body string
		code               int
		want               string
	}{
		"名字为空":       {"POST", "/panel/api/keys", `{"name":" "}`, 400, "不能为空"},
		"请求体不是 JSON": {"POST", "/panel/api/keys", `{x`, 400, "请求格式不对"},
		"到期时间格式不对":   {"POST", "/panel/api/keys", `{"name":"a","expires_at":"明天"}`, 400, "请求格式不对"},
		"改不存在的":      {"POST", "/panel/api/keys/k_none", `{"name":"a"}`, 404, "不存在"},
		"重新生成不存在的":   {"POST", "/panel/api/keys/k_none/regenerate", "", 404, "不存在"},
		"删不存在的":      {"POST", "/panel/api/keys/k_none/delete", "", 404, "不存在"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := keyReq(p, tc.method, tc.path, tc.body)
			if rec.Code != tc.code || !strings.Contains(decodeKey(t, rec).Error, tc.want) {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
			}
		})
	}

	rec = keyReq(p, "POST", "/panel/api/keys/"+id+"/delete", "")
	if rec.Code != 200 || !decodeKey(t, rec).OK {
		t.Fatalf("删除 code=%d body=%s", rec.Code, rec.Body)
	}
	rec = keyReq(p, "GET", "/panel/api/keys", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Keys) != 0 {
		t.Fatalf("删除后列表 = %+v", list.Keys)
	}
}

// TestKeysRequired 管理员密钥为空时：没建密钥 required=false，建了就是 true。
func TestKeysRequired(t *testing.T) {
	p, store, _ := keysPanel(t, "")
	required := func() bool {
		rec := keyReq(p, "GET", "/panel/api/keys", "")
		var out struct {
			AdminKeySet bool `json:"admin_key_set"`
			Required    bool `json:"required"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if out.AdminKeySet {
			t.Fatal("管理员密钥为空")
		}
		return out.Required
	}
	if required() {
		t.Fatal("没建密钥不该要求带密钥")
	}
	if _, err := store.Create(apikeys.Input{Name: "a"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !required() {
		t.Fatal("建了密钥就要求带密钥")
	}
}

// TestKeysSaveFailure 写盘失败回 500。
func TestKeysSaveFailure(t *testing.T) {
	p, _, path := keysPanel(t, "sk-admin")
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	rec := keyReq(p, "POST", "/panel/api/keys", `{"name":"a"}`)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(decodeKey(t, rec).Error, "写密钥文件失败") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

// TestKeysNotAvailable 没有密钥库时各接口回 501。
func TestKeysNotAvailable(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "sk-admin"})
	for _, tc := range []struct{ method, path string }{
		{"GET", "/panel/api/keys"}, {"POST", "/panel/api/keys"}, {"POST", "/panel/api/keys/k1"},
		{"POST", "/panel/api/keys/k1/regenerate"}, {"POST", "/panel/api/keys/k1/delete"},
	} {
		if rec := keyReq(p, tc.method, tc.path, `{}`); rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s → %d", tc.method, tc.path, rec.Code)
		}
	}
}

// TestUsageByKey 用量按密钥：补上显示名（管理员、测活、未区分、已删除的密钥），key 参数按密钥筛。
func TestUsageByKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api_keys.json")
	store, _ := apikeys.Open(path)
	k, _ := store.Create(apikeys.Input{Name: "张三"}, time.Now())
	rec := usage.New("")
	for _, key := range []string{k.ID, "admin", "liveness", "", "k_gone"} {
		rec.Add(time.Now(), "cn", "u1", "glm-5.2", usage.Delta{TotalTokens: 5, HasTotal: true, Key: key}, true)
	}
	p := New(Config{Version: "test", APIKey: "sk-admin", Usage: rec, Keys: store, Pool: pool.New("")})
	snap := func(query string) usage.Snapshot {
		t.Helper()
		out := keyReq(p, "GET", "/panel/api/usage"+query, "")
		var s usage.Snapshot
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &s) != nil {
			t.Fatalf("usage%s -> %d %s", query, out.Code, out.Body)
		}
		return s
	}
	names := map[string]string{}
	for _, row := range snap("").ByKey {
		names[row.Key] = row.Extra
	}
	if names[k.ID] != "张三" || names["admin"] != "管理员" || names["liveness"] != "测活" || names[""] != "未区分" || names["k_gone"] != "已删除的密钥" {
		t.Fatalf("names = %v", names)
	}
	if s := snap("?key=" + k.ID); s.Totals.Requests != 1 {
		t.Fatalf("按密钥筛 = %+v", s.Totals)
	}
	if s := snap("?key=-"); s.Totals.Requests != 1 || s.ByKey[0].Key != "" {
		t.Fatalf("只看未区分 = %+v", s.Totals)
	}
	if s := snap("?key="); s.Totals.Requests != 5 {
		t.Fatalf("空 key 不筛 = %+v", s.Totals)
	}

	// 没有密钥库时名字只有固定的几种
	p2 := New(Config{Version: "test", APIKey: "sk-admin", Usage: rec, Pool: pool.New("")})
	out := keyReq(p2, "GET", "/panel/api/usage", "")
	if !strings.Contains(out.Body.String(), "已删除的密钥") {
		t.Fatalf("body=%s", out.Body)
	}
}

// TestRequestLogsFilterAPIKey 请求记录按密钥名筛。
func TestRequestLogsFilterAPIKey(t *testing.T) {
	dir := t.TempDir()
	rl := reqlog.New(reqlog.Config{Dir: dir, Enabled: true})
	for _, name := range []string{"张三", "李四"} {
		rl.Begin()
		rl.Record(reqlog.Event{Time: time.Now(), RequestID: "req-" + name, Status: 200, OK: true, Outcome: reqlog.OutcomeSuccess, APIKey: name})
	}
	rl.Close()
	rl = reqlog.New(reqlog.Config{Dir: dir, Enabled: true})
	t.Cleanup(rl.Close)
	p := New(Config{Version: "test", APIKey: "sk-admin", RequestLog: rl})
	out := keyReq(p, "GET", "/panel/api/request_logs?api_key=张", "")
	var body struct {
		Entries []reqlog.Event `json:"entries"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil || len(body.Entries) != 1 || body.Entries[0].APIKey != "张三" {
		t.Fatalf("code=%d body=%s", out.Code, out.Body)
	}
}
