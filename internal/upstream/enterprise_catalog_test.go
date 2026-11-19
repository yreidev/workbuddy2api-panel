// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 企业模型目录（官方内置 CLI 的 ModelsProductProvider）：国内版企业号单独探测
// GET <chatBase>/console/enterprises/{enterpriseId}/config/models，与 /v3/config 合并。

func entModelsPath(id string) string {
	return "/console/enterprises/" + id + "/config/models"
}

// TestEnterpriseCatalogRequest 国内版企业号：路径带 enterpriseId（特殊字符转义）、
// 企业身份头与内置 CLI 的 UA，且不再请求旧的 personal 端点。
func TestEnterpriseCatalogRequest(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, entModelsPath("ent/1"), 200,
		`{"code":0,"data":[{"id":"m1","maxOutputTokens":1000,"tags":["chat"]}]}`)
	a := fullEntAuth()
	a.EnterpriseID = "ent/1"
	out, err := upClient(f).fetchEnterpriseModels(a)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(out) != 1 || out[0].ID != "m1" {
		t.Fatalf("out = %+v", out)
	}
	reqs := f.seen()
	if len(reqs) != 1 {
		t.Fatalf("请求数 = %d，应为 1", len(reqs))
	}
	r := reqs[0]
	if r.method != http.MethodGet || r.path != entModelsPath("ent/1") {
		t.Errorf("请求 = %s %s", r.method, r.path)
	}
	if want := upBase + "/console/enterprises/" + url.PathEscape("ent/1") + "/config/models"; r.url != want {
		t.Errorf("URL = %q，企业 id 应转义为 %q", r.url, want)
	}
	if r.header.Get("X-User-Id") != "u-1" || r.header.Get("X-Enterprise-Id") != "ent/1" || r.header.Get("X-Tenant-Id") != "ent/1" {
		t.Errorf("企业身份头不全: %v", r.header)
	}
	if ua := r.header.Get("User-Agent"); !strings.Contains(ua, "CLI/2.176.0") {
		t.Errorf("UA 应为内置 CLI: %q", ua)
	}
	if n := f.count(http.MethodGet, "/console/enterprises/personal/models"); n != 0 {
		t.Errorf("不应再请求旧企业端点，实际 %d 次", n)
	}
}

// TestEnterpriseCatalogMerge 国内版企业号：/v3/config 为主，同 id 以 v3 为准，
// 企业目录独有的模型补进来。
func TestEnterpriseCatalogMerge(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/v3/config", 200,
		`{"code":0,"data":{"models":[{"id":"shared","maxInputTokens":111},{"id":"v3-only","maxInputTokens":222}]}}`)
	f.on(http.MethodGet, entModelsPath("ent-1"), 200,
		`{"code":0,"data":[{"id":"shared","maxInputTokens":999,"tags":["chat"]},{"id":"ent-only","maxInputTokens":333,"tags":["chat"]}]}`)
	out, err := upClient(f).probeCatalog(fullEntAuth())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	byID := map[string]int64{}
	for _, mi := range out {
		byID[mi.ID] = mi.ContextWindow
	}
	if byID["shared"] != 111 {
		t.Errorf("shared 应以 /v3/config 为准 = %d，期望 111", byID["shared"])
	}
	if byID["v3-only"] != 222 {
		t.Errorf("v3-only = %d，期望 222", byID["v3-only"])
	}
	if byID["ent-only"] != 333 {
		t.Errorf("ent-only 应补进来 = %d，期望 333", byID["ent-only"])
	}
	if len(out) != 3 {
		t.Errorf("out = %+v", out)
	}
}

// TestEnterpriseCatalogDegrade 单路失败降级：企业端点失败只用 /v3/config；
// /v3/config 失败只用企业端点；两路都失败才报错。
func TestEnterpriseCatalogDegrade(t *testing.T) {
	entOK := `{"code":0,"data":[{"id":"ent-only","maxOutputTokens":1000,"tags":["chat"]}]}`

	t.Run("企业端点 500 用 v3", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, "/v3/config", 200, `{"code":0,"data":{"models":[{"id":"v3-only"}]}}`)
		f.on(http.MethodGet, entModelsPath("ent-1"), 500, `{"code":500,"msg":"boom"}`)
		out, err := upClient(f).probeCatalog(fullEntAuth())
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(out) != 1 || out[0].ID != "v3-only" {
			t.Fatalf("out = %+v", out)
		}
	})

	t.Run("企业端点空数组用 v3", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, "/v3/config", 200, `{"code":0,"data":{"models":[{"id":"v3-only"}]}}`)
		f.on(http.MethodGet, entModelsPath("ent-1"), 200, `{"code":0,"data":[]}`)
		out, err := upClient(f).probeCatalog(fullEntAuth())
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(out) != 1 || out[0].ID != "v3-only" {
			t.Fatalf("out = %+v", out)
		}
	})

	t.Run("企业端点超时用 v3", func(t *testing.T) {
		old := enterpriseModelsTimeout
		enterpriseModelsTimeout = 50 * time.Millisecond
		defer func() { enterpriseModelsTimeout = old }()
		c := &Client{HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/v3/config") {
				return jsonResp(200, `{"code":0,"data":{"models":[{"id":"v3-only"}]}}`), nil
			}
			// 企业端点等到请求 context 到期（网关 5 秒超时的测试版）。
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}, ChatBaseCN: upBase}
		out, err := c.probeCatalog(fullEntAuth())
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(out) != 1 || out[0].ID != "v3-only" {
			t.Fatalf("out = %+v", out)
		}
	})

	t.Run("v3 失败用企业端点", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, "/v3/config", 500, `{"code":500,"msg":"boom"}`)
		f.on(http.MethodGet, entModelsPath("ent-1"), 200, entOK)
		out, err := upClient(f).probeCatalog(fullEntAuth())
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(out) != 1 || out[0].ID != "ent-only" {
			t.Fatalf("out = %+v", out)
		}
	})

	t.Run("两路都失败报错", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, "/v3/config", 500, `{"code":500,"msg":"v3 boom"}`)
		f.on(http.MethodGet, entModelsPath("ent-1"), 500, `{"code":500,"msg":"ent boom"}`)
		if _, err := upClient(f).probeCatalog(fullEntAuth()); err == nil {
			t.Fatal("两路全失败应返回错误")
		}
	})
}

// TestEnterpriseCatalogPersonalOnlyV3 个人号只拉 /v3/config，不发企业目录请求。
func TestEnterpriseCatalogPersonalOnlyV3(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/v3/config", 200, `{"code":0,"data":{"models":[{"id":"v3-only"}]}}`)
	out, err := upClient(f).probeCatalog(upAuth())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(out) != 1 || out[0].ID != "v3-only" {
		t.Fatalf("out = %+v", out)
	}
	for _, r := range f.seen() {
		if strings.Contains(r.path, "/enterprises/") {
			t.Errorf("个人号不应请求企业目录: %s", r.path)
		}
	}
}
