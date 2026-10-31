// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// hidden_models_test.go models.hidden：被藏起来的模型 /v1/models 不列，点名调用回 400 model_unavailable；
// 只藏一个域时跨域合池避开那个域；按模型路由改写后的名字判。
package server

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
)

func TestChatHiddenModels(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	cases := []struct {
		name   string
		hidden []string
		cross  bool
		routes []livecfg.ModelRoute
		model  string
		want   string // 打到的上游 host；空 = 应回 400 不发上游
	}{
		{"裸名两个域都藏", []string{"m-cn"}, false, nil, "m-cn", ""},
		{"只藏国内版：不带前缀默认走国内版被拒", []string{"cn:m-both"}, false, nil, "m-both", ""},
		{"只藏国内版：global: 前缀照常", []string{"cn:m-both"}, false, nil, "global:m-both", "global.fake"},
		{"只藏国内版：合池时只进国际版", []string{"cn:m-both"}, true, nil, "m-both", "global.fake"},
		{"只藏国际版：合池时只进国内版", []string{"global:m-both"}, true, nil, "m-both", "cn.fake"},
		{"两个域都藏：合池也拒", []string{"m-both"}, true, nil, "m-both", ""},
		{"路由改写成被藏的模型也拒", []string{"m-cn"}, false, []livecfg.ModelRoute{{Match: "claude-*", Target: "m-cn"}}, "claude-sonnet-4-6", ""},
		{"没藏的模型不受影响", []string{"m-cn"}, false, nil, "m-both", "cn.fake"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetModelsCache(t) // 路由要看包级目录缓存，别的测试留下的不能算
			cn := &auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
			gl := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
			fake := &crossRealmUpstream{}
			up := fake.client(t, []string{"m-both", "m-cn"}, []string{"m-both", "m-global"})
			if _, err := up.FetchModels(cn); err != nil {
				t.Fatalf("cn 目录: %v", err)
			}
			if _, err := up.FetchModels(gl); err != nil {
				t.Fatalf("global 目录: %v", err)
			}
			h := NewHandler(Config{
				Pool: testPoolWith(cn, gl), Upstream: up, GlobalEnabled: true,
				Live: livecfg.New(livecfg.Snapshot{
					CrossRealm: mergeIf(c.cross), ModelRoutes: c.routes, HiddenModels: livecfg.NormalizeHiddenModels(c.hidden),
				}),
			})
			body := `{"model":"` + c.model + `","messages":[{"role":"user","content":"hi"}],"stream":true}`
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
			if c.want == "" {
				if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"model_unavailable"`) || !strings.Contains(rec.Body.String(), "models.hidden") {
					t.Fatalf("应回 400 model_unavailable，code=%d body=%s", rec.Code, rec.Body)
				}
				if got := fake.lastChat(); got != "" {
					t.Fatalf("被藏的模型不应发上游，打到了 %s", got)
				}
				return
			}
			if rec.Code != 200 {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
			}
			if got := fake.lastChat(); got != c.want {
				t.Errorf("chat 打到 %q want %q", got, c.want)
			}
		})
	}
}

// TestModelsHidden /v1/models 不列被藏的模型；合池时不带前缀的条目也跟着去掉被藏的域。
func TestModelsHidden(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	resetModelsCache(t)
	cn := &auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	gl := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
	up := (&crossRealmUpstream{}).client(t, []string{"m-both", "m-cn"}, []string{"m-both", "m-global"})
	h := NewHandler(Config{
		Pool: testPoolWith(cn, gl), Upstream: up, GlobalEnabled: true,
		Live: livecfg.New(livecfg.Snapshot{CrossRealm: livecfg.CrossRealmMerge, HiddenModels: livecfg.NormalizeHiddenModels([]string{"m-cn", "global:m-both"})}),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	ids := make([]string, 0, len(resp.Data))
	realms := map[string]any{}
	for _, m := range resp.Data {
		id := m["id"].(string)
		ids = append(ids, id)
		realms[id] = m["realms"]
	}
	sort.Strings(ids)
	if want := []string{"cn:m-both", "global:m-global", "m-both", "m-global"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids=%v want %v", ids, want)
	}
	if got, ok := realms["m-both"].([]any); !ok || len(got) != 1 || got[0] != "cn" {
		t.Errorf("m-both 只该分到国内版，realms=%v", realms["m-both"])
	}
}
