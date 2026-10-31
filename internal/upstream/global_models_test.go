// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const (
	gmV3Path = "/v3/config"
	// gmEntPath 国际版企业号的模型目录（官方内置 CLI 的 ModelsProductProvider）。
	gmEntPath = "/console/enterprises/ent-g/config/models"
)

// upGlobalClient global 账号客户端：所有探测都落到假上游。
func upGlobalClient(f *upFake) *Client {
	c := upClient(f)
	c.ChatBaseGlobal = upBase
	return c
}

func v3ModelsJSON(models string) string {
	return `{"code":0,"data":{"models":[` + models + `]}}`
}

// entModelsJSON 企业模型目录响应：data 直接是模型数组（不是 data.models）。
func entModelsJSON(models string) string {
	return `{"code":0,"data":[` + models + `]}`
}

// sameStringSet 比较两个名字集合（顺序不敏感）。
func sameStringSet(got, want []string) bool {
	a := append([]string(nil), got...)
	b := append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(b)
	return reflect.DeepEqual(a, b)
}

func gmFindInfo(infos []ModelInfo, id string) (ModelInfo, bool) {
	for _, mi := range infos {
		if mi.ID == id {
			return mi, true
		}
	}
	return ModelInfo{}, false
}

// TestFetchGlobalModelsNonGlobal 非 global 账号或逃生门关闭时不探测、零请求。
func TestFetchGlobalModelsNonGlobal(t *testing.T) {
	f := newUpFake(t)
	c := upGlobalClient(f)
	cn := &auth.Auth{UID: "u-cn", AccessToken: "at", Domain: "www.codebuddy.cn"}
	if names := c.FetchGlobalModels(cn); names != nil {
		t.Errorf("国内版账号应返回 nil，got %v", names)
	}
	if infos := c.FetchGlobalModelInfos(cn); infos != nil {
		t.Errorf("国内版账号 infos 应为 nil，got %v", infos)
	}
	c.GlobalEnabled = false
	if names := c.FetchGlobalModels(globalAuth()); names != nil {
		t.Errorf("逃生门关闭应返回 nil，got %v", names)
	}
	if n := len(f.seen()); n != 0 {
		t.Errorf("不应发请求，实际 %d 次", n)
	}
}

// globalEntAuth 国际版企业号（拉目录时才会并上企业端点）。
func globalEntAuth() *auth.Auth {
	a := globalAuth()
	a.EnterpriseID = "ent-g"
	return a
}

// TestFetchGlobalModelsPersonalV3Only 个人号只用桌面端内置 CLI 的 UA 拉一次 /v3/config，不探企业端点。
func TestFetchGlobalModelsPersonalV3Only(t *testing.T) {
	f := newUpFake(t)
	f.onFunc(http.MethodGet, gmV3Path, func(r *upReq) (*http.Response, error) {
		if want := "WorkBuddy/5.9.4 workbuddy-ai/5.9.4 CLI/2.165.0"; r.header.Get("User-Agent") != want {
			t.Errorf("UA = %q, want %q", r.header.Get("User-Agent"), want)
		}
		return jsonResp(200, v3ModelsJSON(`{"id":"desktop-only","maxInputTokens":222}`)), nil
	})
	f.on(http.MethodGet, gmEntPath, 200, entModelsJSON(`{"id":"ent-only","tags":["chat"]}`))
	c := upGlobalClient(f)
	if names := c.FetchGlobalModels(globalAuth()); !sameStringSet(names, []string{"desktop-only"}) {
		t.Fatalf("names = %v", names)
	}
	if n := f.count(http.MethodGet, gmV3Path); n != 1 {
		t.Errorf("v3/config 应只拉 1 次，实际 %d", n)
	}
	if n := f.count(http.MethodGet, gmEntPath); n != 0 {
		t.Errorf("个人号不应探企业端点，实际 %d 次", n)
	}
}

// TestFetchGlobalModelsMergesRoutes 企业号 /v3/config + 企业端点并发探测：
// v3 字段权威、企业端点只补缺失 id；结果缓存 1h（二次调用零请求）；
// effort 桶 / 倍率 / 域模型集合同步落缓存。
func TestFetchGlobalModelsMergesRoutes(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, gmV3Path, 200, v3ModelsJSON(`{"id":"shared","maxInputTokens":111,"maxOutputTokens":1000,"credits":"x0.5",
		"reasoning":{"supportedEfforts":["low","high"],"defaultEffort":"high"}},
		{"id":"desktop-only","maxInputTokens":222},
		{"id":"nonchat","tags":["text-to-video"]}`))
	f.on(http.MethodGet, gmEntPath, 200, entModelsJSON(`{"id":"ent-only","maxInputTokens":333,"tags":["chat"]},{"id":"desktop-only","maxInputTokens":444,"tags":["chat"]}`))

	c := upGlobalClient(f)
	c.ChatBaseCN = "https://cn.example" // 企业端点必须发往国际版 base
	names := c.FetchGlobalModels(globalEntAuth())
	if want := []string{"shared", "desktop-only", "ent-only"}; !sameStringSet(names, want) {
		t.Fatalf("names = %v，应为集合 %v", names, want)
	}
	if n := f.count(http.MethodGet, gmV3Path); n != 1 {
		t.Errorf("v3/config 应探测 1 次，实际 %d 次", n)
	}
	if n := f.count(http.MethodGet, gmEntPath); n != 1 {
		t.Errorf("企业端点应探测 1 次，实际 %d", n)
	}
	for _, r := range f.seen() {
		if r.path == gmEntPath && r.url != upBase+gmEntPath {
			t.Errorf("企业端点应发往国际版 base %s，实际 %s", upBase, r.url)
		}
		if strings.Contains(r.path, "/enterprises/personal/models") {
			t.Errorf("不应再请求旧企业端点 %s", r.path)
		}
	}

	infos := c.FetchGlobalModelInfos(globalEntAuth())
	if len(infos) == 0 {
		t.Fatal("对象形态探测应返回 infos")
	}
	shared, ok := gmFindInfo(infos, "shared")
	if !ok || shared.ContextWindow != 111 || shared.MaxTokens != 1000 || shared.Credits != "x0.5" {
		t.Errorf("shared 字段应取自主路: %+v/%v", shared, ok)
	}
	desktopOnly, ok := gmFindInfo(infos, "desktop-only")
	if !ok || desktopOnly.ContextWindow != 222 {
		t.Errorf("desktop-only 字段应取自主路（非企业端点）: %+v/%v", desktopOnly, ok)
	}
	entOnly, ok := gmFindInfo(infos, "ent-only")
	if !ok || entOnly.ContextWindow != 333 {
		t.Errorf("ent-only 应来自企业端点: %+v/%v", entOnly, ok)
	}

	efforts, defaults := c.GlobalEffortSnapshot()
	if !reflect.DeepEqual(efforts["shared"], []string{"low", "high"}) || defaults["shared"] != "high" {
		t.Errorf("effort 桶 = %v/%v", efforts["shared"], defaults["shared"])
	}
	if got := c.ModelRate("global", "shared"); got != "0.5" {
		t.Errorf("ModelRate = %q，应为 0.5", got)
	}
	if !c.RealmHasModel("global", "shared") || !c.RealmHasModel("global", "ent-only") {
		t.Error("域模型集合应包含并集模型")
	}

	// 缓存命中：1h 内再查零请求。
	before := len(f.seen())
	c.FetchGlobalModels(globalEntAuth())
	c.FetchGlobalModelInfos(globalEntAuth())
	if after := len(f.seen()); after != before {
		t.Errorf("缓存期内不应再请求，%d → %d", before, after)
	}
}

// TestFetchGlobalModelsCooldown 全部路径失败：返回 nil、记负缓存；冷却期内
// 不再请求；冷却到期后重新探测。
func TestFetchGlobalModelsCooldown(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, gmV3Path, 500, `{"code":500,"msg":"boom"}`)
	f.on(http.MethodGet, gmEntPath, 500, `{"code":500,"msg":"boom"}`)
	c := upGlobalClient(f)

	if names := c.FetchGlobalModels(globalAuth()); names != nil {
		t.Fatalf("全失败应返回 nil，got %v", names)
	}
	if c.globalModels.lastFail.IsZero() {
		t.Error("失败应记负缓存时间")
	}
	first := len(f.seen())
	if first != 1 {
		t.Errorf("首次探测请求数 = %d，应为 1（个人号只拉 v3）", first)
	}

	c.FetchGlobalModels(globalAuth())
	if n := len(f.seen()); n != first {
		t.Errorf("冷却期内不应再请求，%d → %d", first, n)
	}

	c.globalModels.lastFail = time.Now().Add(-globalModelsFailCooldown - time.Minute)
	c.FetchGlobalModels(globalAuth())
	if n := len(f.seen()); n != first*2 {
		t.Errorf("冷却到期应重新探测，%d → %d", first, n)
	}
}

// TestFetchGlobalModelsPartialFailure 单路失败降级：
// v3 全失败时用企业端点；企业端点失败时用 v3。
func TestFetchGlobalModelsPartialFailure(t *testing.T) {
	t.Run("v3 失败用企业端点", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, gmV3Path, 500, `{"code":500,"msg":"v3 boom"}`)
		f.on(http.MethodGet, gmEntPath, 200, entModelsJSON(`{"id":"ent-only","maxInputTokens":333,"tags":["chat"]}`))
		c := upGlobalClient(f)
		names := c.FetchGlobalModels(globalEntAuth())
		if !sameStringSet(names, []string{"ent-only"}) {
			t.Fatalf("names = %v，应为 [ent-only]", names)
		}
		if infos := c.FetchGlobalModelInfos(globalEntAuth()); len(infos) != 1 || infos[0].ID != "ent-only" {
			t.Errorf("infos = %+v", infos)
		}
	})

	t.Run("企业端点失败用 v3", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, gmV3Path, 200, v3ModelsJSON(`{"id":"desktop-only"}`))
		f.on(http.MethodGet, gmEntPath, 500, `{"code":500,"msg":"ent boom"}`)
		c := upGlobalClient(f)
		names := c.FetchGlobalModels(globalEntAuth())
		if !sameStringSet(names, []string{"desktop-only"}) {
			t.Fatalf("names = %v", names)
		}
	})

	t.Run("企业端点空数组用 v3", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, gmV3Path, 200, v3ModelsJSON(`{"id":"desktop-only"}`))
		f.on(http.MethodGet, gmEntPath, 200, entModelsJSON(""))
		c := upGlobalClient(f)
		if names := c.FetchGlobalModels(globalEntAuth()); !sameStringSet(names, []string{"desktop-only"}) {
			t.Fatalf("names = %v", names)
		}
	})

	t.Run("企业端点超时用 v3", func(t *testing.T) {
		old := enterpriseModelsTimeout
		enterpriseModelsTimeout = 50 * time.Millisecond
		defer func() { enterpriseModelsTimeout = old }()
		c := &Client{GlobalEnabled: true, ChatBaseGlobal: upBase, HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, gmV3Path) {
				return jsonResp(200, v3ModelsJSON(`{"id":"desktop-only"}`)), nil
			}
			// 企业端点等到请求 context 到期（网关 5 秒超时的测试版）。
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}}
		if names := c.FetchGlobalModels(globalEntAuth()); !sameStringSet(names, []string{"desktop-only"}) {
			t.Fatalf("names = %v", names)
		}
	})

	t.Run("两路都失败返回错误", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, gmV3Path, 500, `{"code":500,"msg":"v3 boom"}`)
		f.on(http.MethodGet, gmEntPath, 500, `{"code":500,"msg":"ent boom"}`)
		c := upGlobalClient(f)
		if _, _, _, _, err := c.probeGlobalModels(globalEntAuth()); err == nil {
			t.Fatal("两路全失败应返回错误")
		}
	})
}

// TestProbeGlobalModelsFilter 国际版企业号只收企业模型目录里 tags 含 chat 的条目；
// 非对话、disabled 的都不进并集。
func TestProbeGlobalModelsFilter(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, gmV3Path, 500, `{"code":500,"msg":"v3 boom"}`)
	f.on(http.MethodGet, gmEntPath, 200, entModelsJSON(`{"id":"chat-ok","tags":["chat"]},
		{"id":"no-tag","tags":["craft"]},{"id":"nes-x","tags":["chat"]},
		{"id":"tiny","maxOutputTokens":128,"tags":["chat"]},{"id":"off","disabled":true,"tags":["chat"]}`))
	names, infos, _, _, err := upGlobalClient(f).probeGlobalModels(globalEntAuth())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(names, []string{"chat-ok"}) {
		t.Fatalf("names = %v，应为 [chat-ok]", names)
	}
	if len(infos) != 1 || infos[0].ID != "chat-ok" {
		t.Fatalf("infos = %+v", infos)
	}
}

// TestMergeGlobalCatalog 两路合并：主列优先、去重、空 id 跳过、企业端点只补
// 缺失条目；窄表 infos 保持 nil 语义。
func TestMergeGlobalCatalog(t *testing.T) {
	t.Run("副列表为空直接返回主列表", func(t *testing.T) {
		names, infos := mergeGlobalCatalog([]string{"a"}, []ModelInfo{{ID: "a"}}, nil, nil)
		if !reflect.DeepEqual(names, []string{"a"}) || len(infos) != 1 {
			t.Errorf("names=%v infos=%v", names, infos)
		}
	})

	t.Run("去重并保持主列表顺序", func(t *testing.T) {
		primary := []string{"", "a", "b", "a"}
		primaryInfos := []ModelInfo{{ID: "a", ContextWindow: 1}, {ID: "b", ContextWindow: 2}}
		secondary := []string{"b", "c", "", "d"}
		secondaryInfos := []ModelInfo{{ID: "b", ContextWindow: 999}, {ID: "c", ContextWindow: 3}}
		names, infos := mergeGlobalCatalog(primary, primaryInfos, secondary, secondaryInfos)
		if want := []string{"a", "b", "c", "d"}; !reflect.DeepEqual(names, want) {
			t.Errorf("names = %v，应为 %v", names, want)
		}
		if len(infos) != 3 {
			t.Fatalf("infos = %+v", infos)
		}
		if infos[1].ID != "b" || infos[1].ContextWindow != 2 {
			t.Errorf("同 id 应保留主路字段: %+v", infos[1])
		}
		if infos[2].ID != "c" || infos[2].ContextWindow != 3 {
			t.Errorf("副列表缺失 id 应补其条目: %+v", infos[2])
		}
	})

	t.Run("主列表窄表时副列表对象条目按 id 匹配", func(t *testing.T) {
		names, infos := mergeGlobalCatalog(
			[]string{"a"}, nil,
			[]string{"b", "c"},
			[]ModelInfo{{ID: "c", ContextWindow: 8}, {ID: "zzz", ContextWindow: 9}},
		)
		if want := []string{"a", "b", "c"}; !reflect.DeepEqual(names, want) {
			t.Errorf("names = %v", names)
		}
		if len(infos) != 1 || infos[0].ID != "c" || infos[0].ContextWindow != 8 {
			t.Errorf("infos = %+v", infos)
		}
	})

	t.Run("两路都窄表不编造 infos", func(t *testing.T) {
		names, infos := mergeGlobalCatalog([]string{"a"}, nil, []string{"b"}, nil)
		if want := []string{"a", "b"}; !reflect.DeepEqual(names, want) {
			t.Errorf("names = %v", names)
		}
		if infos != nil {
			t.Errorf("窄表应保持 infos=nil，got %+v", infos)
		}
	})

	t.Run("副列表全重复", func(t *testing.T) {
		names, infos := mergeGlobalCatalog([]string{"a"}, []ModelInfo{{ID: "a"}}, []string{"a"}, nil)
		if !reflect.DeepEqual(names, []string{"a"}) || len(infos) != 1 {
			t.Errorf("names=%v infos=%v", names, infos)
		}
	})
}

// TestMergeEfforts effort 桶合并：主路权威，企业端点只补缺失键。
func TestMergeEfforts(t *testing.T) {
	got := mergeEffortBuckets(
		map[string][]string{"a": {"low"}},
		map[string][]string{"a": {"max"}, "b": {"high"}},
	)
	if !reflect.DeepEqual(got["a"], []string{"low"}) || !reflect.DeepEqual(got["b"], []string{"high"}) {
		t.Errorf("bucket 合并 = %v", got)
	}
	if m := mergeEffortBuckets(map[string][]string{"a": {"low"}}, nil); len(m) != 1 {
		t.Errorf("副桶为空应原样返回主桶: %v", m)
	}
	if m := mergeEffortBuckets(nil, map[string][]string{"a": {"low"}, "b": {"high"}}); len(m) != 2 {
		t.Errorf("主桶为空应复制副桶: %v", m)
	}

	gotD := mergeEffortDefaults(
		map[string]string{"a": "low"},
		map[string]string{"a": "max", "b": "high"},
	)
	if gotD["a"] != "low" || gotD["b"] != "high" {
		t.Errorf("defaults 合并 = %v", gotD)
	}
	if m := mergeEffortDefaults(map[string]string{"a": "low"}, nil); len(m) != 1 {
		t.Errorf("副表为空应原样返回主表: %v", m)
	}
	if m := mergeEffortDefaults(nil, map[string]string{"a": "low", "b": "high"}); len(m) != 2 {
		t.Errorf("主表为空应复制副表: %v", m)
	}
}

// TestExtractEfforts 从条目抽 effort 桶并生成有序 names；空 id 跳过。
func TestExtractEfforts(t *testing.T) {
	infos := []ModelInfo{
		{ID: ""},
		{ID: "a", Efforts: []string{"low", "high"}, DefaultEffort: "high"},
		{ID: "b", DefaultEffort: "medium"},
		{ID: "c"},
	}
	names, out, efforts, defaults := extractEfforts(infos)
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v，应为 %v", names, want)
	}
	if len(out) != 3 || out[0].ID != "a" {
		t.Errorf("out = %+v", out)
	}
	if !reflect.DeepEqual(efforts, map[string][]string{"a": {"low", "high"}}) {
		t.Errorf("efforts = %v", efforts)
	}
	if !reflect.DeepEqual(defaults, map[string]string{"a": "high", "b": "medium"}) {
		t.Errorf("defaults = %v", defaults)
	}

	names, out, efforts, defaults = extractEfforts(nil)
	if len(names) != 0 || len(out) != 0 || efforts != nil || defaults != nil {
		t.Errorf("空输入 = %v/%v/%v/%v", names, out, efforts, defaults)
	}
}

// TestFetchGlobalModelsStableOrder 只有 v3 主路给出模型时，名单按 id 排序，
// 不随 map 遍历顺序变化（每次换新客户端重新探测，连测多次）。
func TestFetchGlobalModelsStableOrder(t *testing.T) {
	ids := []string{"m-h", "m-c", "m-f", "m-a", "m-g", "m-b", "m-e", "m-d"}
	var models []string
	for _, id := range ids {
		models = append(models, `{"id":"`+id+`","maxInputTokens":1000}`)
	}
	want := append([]string(nil), ids...)
	sort.Strings(want)
	for i := 0; i < 20; i++ {
		f := newUpFake(t)
		f.on(http.MethodGet, gmV3Path, 200, v3ModelsJSON(strings.Join(models, ",")))
		f.on(http.MethodGet, gmEntPath, 400, `{"code":400}`)
		if names := upGlobalClient(f).FetchGlobalModels(globalAuth()); !reflect.DeepEqual(names, want) {
			t.Fatalf("第 %d 次 names = %v，应按 id 排序为 %v", i, names, want)
		}
	}
}
