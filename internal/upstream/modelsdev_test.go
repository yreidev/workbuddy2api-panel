// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

// mdFake models.dev 假客户端：记录请求次数与 URL，按 handler 回响应。
type mdFake struct {
	t       *testing.T
	mu      sync.Mutex
	n       int
	urls    []string
	handler func(*http.Request) (*http.Response, error)
}

func (f *mdFake) roundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.n++
	f.urls = append(f.urls, r.URL.String())
	f.mu.Unlock()
	return f.handler(r)
}

func (f *mdFake) client() *http.Client { return &http.Client{Transport: rtFunc(f.roundTrip)} }

func (f *mdFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *mdFake) firstURL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.urls) == 0 {
		return ""
	}
	return f.urls[0]
}

// mdWait 轮询直到条件成立（最多 2s，每 5ms 一次）：替代固定 sleep 等异步拉取。
func mdWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

func mdDocReady() bool {
	modelsDev.mu.Lock()
	defer modelsDev.mu.Unlock()
	return modelsDev.doc != nil
}

func mdDocEntry(model string) (modelsDevEntry, bool) {
	modelsDev.mu.Lock()
	defer modelsDev.mu.Unlock()
	if modelsDev.doc == nil {
		return modelsDevEntry{}, false
	}
	e, ok := modelsDev.doc[model]
	return e, ok
}

func mdNegativeCount() int {
	modelsDev.mu.Lock()
	defer modelsDev.mu.Unlock()
	return len(modelsDev.negatives)
}

// mdReset 每个碰单例的测试统一隔离（开头清、结束再清）。
func mdReset(t *testing.T) {
	t.Helper()
	ResetLookupChainForTest()
	t.Cleanup(ResetLookupChainForTest)
}

// 解析 models.dev api.json：裸 id 索引、命名空间尾段、
// 无 limit / 空 id 跳过、非法值拒绝。
func TestModelsDevParseDoc(t *testing.T) {
	raw := `{
		"zai": {"models": {
			"glm-5.2": {"limit": {"context": 1000000, "output": 131072}},
			"openai/gpt-5.5": {"limit": {"context": 1050000, "output": 128000}},
			"no-limit": {},
			"": {"limit": {"context": 999, "output": 9}},
			"a/": {"limit": {"context": 999, "output": 9}},
			"bad-ctx-zero": {"limit": {"context": 0, "output": 10}},
			"bad-ctx-neg": {"limit": {"context": -5, "output": 10}},
			"bad-ctx-huge": {"limit": {"context": 1000000001, "output": 10}},
			"bad-out-neg": {"limit": {"context": 1000, "output": -1}},
			"bad-out-huge": {"limit": {"context": 1000, "output": 1000000001}},
			"zero-out-ok": {"limit": {"context": 1000, "output": 0}}
		}}
	}`
	got, err := parseModelsDevDoc([]byte(raw))
	if err != nil {
		t.Fatalf("解析: %v", err)
	}
	want := map[string]modelsDevEntry{
		"glm-5.2":     {Context: 1000000, Output: 131072},
		"gpt-5.5":     {Context: 1050000, Output: 128000},
		"zero-out-ok": {Context: 1000, Output: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("索引 = %+v，应为 %+v", got, want)
	}
	if _, err := parseModelsDevDoc([]byte(`{not json`)); err == nil {
		t.Error("非法 JSON 应返回错误")
	}
}

// 同名模型多 provider 采值优先级：
// 官方 vendor 源 > 票数众数 > provider 名字典序。
func TestModelsDevParseDocPriority(t *testing.T) {
	t.Run("vendor 源压过票数", func(t *testing.T) {
		raw := `{
			"zai": {"models": {"m": {"limit": {"context": 1000, "output": 100}}}},
			"gw1": {"models": {"m": {"limit": {"context": 2000, "output": 200}}}},
			"gw2": {"models": {"m": {"limit": {"context": 2000, "output": 200}}}},
			"gw3": {"models": {"m": {"limit": {"context": 2000, "output": 200}}}}
		}`
		got, err := parseModelsDevDoc([]byte(raw))
		if err != nil {
			t.Fatalf("解析: %v", err)
		}
		if got["m"] != (modelsDevEntry{Context: 1000, Output: 100}) {
			t.Errorf("vendor 源应胜出，got %+v", got["m"])
		}
	})

	t.Run("非 vendor 取众数", func(t *testing.T) {
		raw := `{
			"alpha": {"models": {"m": {"limit": {"context": 3000, "output": 300}}}},
			"beta": {"models": {"m": {"limit": {"context": 4000, "output": 400}}}},
			"gamma": {"models": {"m": {"limit": {"context": 4000, "output": 400}}}}
		}`
		got, err := parseModelsDevDoc([]byte(raw))
		if err != nil {
			t.Fatalf("解析: %v", err)
		}
		if got["m"] != (modelsDevEntry{Context: 4000, Output: 400}) {
			t.Errorf("应取票数多的值，got %+v", got["m"])
		}
	})

	t.Run("同票按 provider 字典序", func(t *testing.T) {
		raw := `{
			"zeta": {"models": {"m": {"limit": {"context": 6000, "output": 600}}}},
			"alpha": {"models": {"m": {"limit": {"context": 5000, "output": 500}}}}
		}`
		got, err := parseModelsDevDoc([]byte(raw))
		if err != nil {
			t.Fatalf("解析: %v", err)
		}
		if got["m"] != (modelsDevEntry{Context: 5000, Output: 500}) {
			t.Errorf("同票应选 provider 字典序最前的值，got %+v", got["m"])
		}
	})

	t.Run("重复值聚合后仍保留 vendor 标记", func(t *testing.T) {
		// zai 与 gw1 同值（聚合时补充 vendor 标记、minProvider 更新），
		// 另外三个非 vendor provider 同值票数更多——vendor 标记仍应让 zai 的值胜出。
		raw := `{
			"zai": {"models": {"m": {"limit": {"context": 10, "output": 1}}}},
			"gw1": {"models": {"m": {"limit": {"context": 10, "output": 1}}}},
			"gw2": {"models": {"m": {"limit": {"context": 20, "output": 2}}}},
			"gw3": {"models": {"m": {"limit": {"context": 20, "output": 2}}}},
			"gw4": {"models": {"m": {"limit": {"context": 20, "output": 2}}}}
		}`
		got, err := parseModelsDevDoc([]byte(raw))
		if err != nil {
			t.Fatalf("解析: %v", err)
		}
		if got["m"] != (modelsDevEntry{Context: 10, Output: 1}) {
			t.Errorf("同值聚合后的 vendor 源应胜出，got %+v", got["m"])
		}
	})

	t.Run("同一文档多次解析结果相同", func(t *testing.T) {
		raw := `{
			"gw1": {"models": {"m": {"limit": {"context": 111, "output": 11}}}},
			"gw2": {"models": {"m": {"limit": {"context": 222, "output": 22}}}},
			"gw3": {"models": {"m": {"limit": {"context": 333, "output": 33}}}},
			"gw4": {"models": {"m": {"limit": {"context": 222, "output": 22}}}}
		}`
		first, err := parseModelsDevDoc([]byte(raw))
		if err != nil {
			t.Fatalf("解析: %v", err)
		}
		for i := 0; i < 20; i++ {
			again, err := parseModelsDevDoc([]byte(raw))
			if err != nil {
				t.Fatalf("第 %d 次解析: %v", i, err)
			}
			if !reflect.DeepEqual(again, first) {
				t.Fatalf("第 %d 次解析结果不同: %+v vs %+v", i, again, first)
			}
		}
	})
}

// lookup：索引命中返回值；未命中记负缓存且重复 miss 不刷新首次时刻。
func TestModelsDevLookup(t *testing.T) {
	mdReset(t)

	t.Run("索引未就绪记 miss", func(t *testing.T) {
		if _, ok := modelsDev.lookup("m1"); ok {
			t.Fatal("索引未就绪不应命中")
		}
		if !modelsDev.negativeFresh("m1") {
			t.Error("miss 应记入负缓存")
		}
	})

	t.Run("索引命中", func(t *testing.T) {
		modelsDev.doc = map[string]modelsDevEntry{"m2": {Context: 123, Output: 45}}
		e, ok := modelsDev.lookup("m2")
		if !ok || e != (modelsDevEntry{Context: 123, Output: 45}) {
			t.Errorf("命中值 = %+v/%v", e, ok)
		}
		if modelsDev.negativeFresh("m2") {
			t.Error("命中不应记负缓存")
		}
	})

	t.Run("重复 miss 不刷新首次时刻", func(t *testing.T) {
		modelsDev.doc = map[string]modelsDevEntry{}
		modelsDev.lookup("m3")
		modelsDev.mu.Lock()
		first := modelsDev.negatives["m3"]
		modelsDev.mu.Unlock()
		modelsDev.lookup("m3")
		modelsDev.mu.Lock()
		second := modelsDev.negatives["m3"]
		modelsDev.mu.Unlock()
		if !first.Equal(second) {
			t.Errorf("重复 miss 不应续期: %v → %v", first, second)
		}
	})

}

// negativeFresh 的三种状态：有效期内、已过期、无记录。
func TestModelsDevNegativeFresh(t *testing.T) {
	mdReset(t)
	modelsDev.mu.Lock()
	modelsDev.negatives = map[string]time.Time{
		"fresh": time.Now(),
		"stale": time.Now().Add(-modelsDevNegativeTTL - time.Minute),
	}
	modelsDev.mu.Unlock()
	if !modelsDev.negativeFresh("fresh") {
		t.Error("有效期内应为 true")
	}
	if modelsDev.negativeFresh("stale") {
		t.Error("过期应为 false")
	}
	if modelsDev.negativeFresh("absent") {
		t.Error("无记录应为 false")
	}
}

// 负缓存淘汰：超过软上限只清过期条目；超过硬上限按
// 时间序丢弃最旧条目、削回软上限。
func TestModelsDevNegativesEviction(t *testing.T) {
	mdReset(t)
	modelsDev.mu.Lock()
	modelsDev.negatives = make(map[string]time.Time, 1100)
	old := time.Now().Add(-modelsDevNegativeTTL - time.Hour)
	for i := 0; i < 1030; i++ {
		modelsDev.negatives[fmtModelName("expired", i)] = old
	}
	modelsDev.mu.Unlock()
	modelsDev.lookup("just-missed")
	if got := mdNegativeCount(); got != 1 {
		t.Errorf("超过软上限应清掉过期条目，剩余 %d，应为 1", got)
	}

	mdReset(t)
	modelsDev.mu.Lock()
	modelsDev.negatives = make(map[string]time.Time, modelsDevNegativesHardCap+10)
	base := time.Now().Add(-time.Hour)
	for i := 0; i < modelsDevNegativesHardCap+2; i++ {
		modelsDev.negatives[fmtModelName("fresh", i)] = base.Add(time.Duration(i) * time.Millisecond)
	}
	modelsDev.mu.Unlock()
	modelsDev.lookup("newcomer")
	if got := mdNegativeCount(); got != modelsDevNegativesSoftCap {
		t.Errorf("超过硬上限应削回软上限 %d，实际 %d", modelsDevNegativesSoftCap, got)
	}
	modelsDev.mu.Lock()
	_, keptOldest := modelsDev.negatives[fmtModelName("fresh", 0)]
	_, keptNewest := modelsDev.negatives[fmtModelName("fresh", modelsDevNegativesHardCap+1)]
	_, keptNew := modelsDev.negatives["newcomer"]
	modelsDev.mu.Unlock()
	if keptOldest {
		t.Error("最旧条目应被丢弃")
	}
	if !keptNewest || !keptNew {
		t.Error("较新条目与本次新条目应保留")
	}
}

// fmtModelName 生成稳定的模型名（避免测试里手拼字符串）。
func fmtModelName(prefix string, i int) string {
	return prefix + "-" + strconv.Itoa(i)
}

// ensureDocAsync：已有索引 / 冷却期内不发请求；首发只拉一次
// （并发调用去重）；冷却到期再拉。
func TestModelsDevEnsureDocAsync(t *testing.T) {
	mdReset(t)

	t.Run("已有索引不拉取", func(t *testing.T) {
		modelsDev.doc = map[string]modelsDevEntry{}
		f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{}`), nil
		}}
		modelsDev.ensureDocAsync(f.client(), "")
		if f.count() != 0 {
			t.Errorf("已有索引不应发请求，实际 %d", f.count())
		}
	})

	t.Run("冷却期内不拉取", func(t *testing.T) {
		mdReset(t)
		modelsDev.fetched = true
		modelsDev.lastFetch = time.Now()
		f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{}`), nil
		}}
		modelsDev.ensureDocAsync(f.client(), "")
		if f.count() != 0 {
			t.Errorf("冷却期内不应发请求，实际 %d", f.count())
		}
	})

	t.Run("并发调用只拉一次", func(t *testing.T) {
		mdReset(t)
		f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{"zai":{"models":{"m":{"limit":{"context":10,"output":1}}}}}`), nil
		}}
		cl := f.client()
		modelsDev.ensureDocAsync(cl, "https://models.example/api.json")
		modelsDev.ensureDocAsync(cl, "https://models.example/api.json")
		mdWait(t, "文档就绪", mdDocReady)
		if f.count() != 1 {
			t.Errorf("并发调用应只拉 1 次，实际 %d", f.count())
		}
		if got := f.firstURL(); got != "https://models.example/api.json" {
			t.Errorf("URL = %q，应使用 baseOverride", got)
		}
	})

	t.Run("冷却到期再拉", func(t *testing.T) {
		mdReset(t)
		modelsDev.fetched = true
		modelsDev.lastFetch = time.Now().Add(-modelsDevFetchCooldown - time.Minute)
		f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{}`), nil
		}}
		modelsDev.ensureDocAsync(f.client(), "")
		mdWait(t, "冷却到期后发起拉取", func() bool { return f.count() == 1 })
		mdWait(t, "文档就绪", mdDocReady)
	})
}

// fetchDoc 的失败路径全部静默：nil client、非法 URL、
// 网络错误、非 200、超大响应、非 JSON——都不 panic 且不落索引。
func TestModelsDevFetchDocFailures(t *testing.T) {
	mdReset(t)

	t.Run("nil client 拒绝", func(t *testing.T) {
		modelsDev.fetchDoc(nil, "")
		if mdDocReady() {
			t.Error("nil client 不应建立索引")
		}
	})

	t.Run("非法 URL", func(t *testing.T) {
		f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
			t.Error("非法 URL 不应发出请求")
			return jsonResp(200, `{}`), nil
		}}
		modelsDev.fetchDoc(f.client(), "://bad-url")
		if f.count() != 0 {
			t.Errorf("非法 URL 不应发出请求，实际 %d", f.count())
		}
	})

	t.Run("网络错误", func(t *testing.T) {
		f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("boom")
		}}
		modelsDev.fetchDoc(f.client(), "https://models.example/api.json")
		if mdDocReady() {
			t.Error("网络错误不应建立索引")
		}
	})

	t.Run("非 200", func(t *testing.T) {
		f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
			return jsonResp(500, `{"oops":1}`), nil
		}}
		modelsDev.fetchDoc(f.client(), "https://models.example/api.json")
		if mdDocReady() {
			t.Error("非 200 不应建立索引")
		}
	})

	t.Run("读 body 失败", func(t *testing.T) {
		f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(errReader{})}, nil
		}}
		modelsDev.fetchDoc(f.client(), "https://models.example/api.json")
		if mdDocReady() {
			t.Error("读取失败不应建立索引")
		}
	})

	t.Run("超大响应被截断", func(t *testing.T) {
		f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(&repeatReader{n: modelsDevMaxBody + 1024})}, nil
		}}
		modelsDev.fetchDoc(f.client(), "https://models.example/api.json")
		if mdDocReady() {
			t.Error("超大响应解析失败，不应建立索引")
		}
	})

	t.Run("非 JSON", func(t *testing.T) {
		f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `<html>not json</html>`), nil
		}}
		modelsDev.fetchDoc(f.client(), "https://models.example/api.json")
		if mdDocReady() {
			t.Error("非 JSON 不应建立索引")
		}
	})

	t.Run("空 base 用默认 URL 并成功", func(t *testing.T) {
		mdReset(t)
		f := &mdFake{t: t, handler: func(*http.Request) (*http.Response, error) {
			return jsonResp(200, `{"zai":{"models":{"m":{"limit":{"context":10,"output":1}}}}}`), nil
		}}
		modelsDev.fetchDoc(f.client(), "")
		if got := f.firstURL(); got != ModelsDevURL {
			t.Errorf("URL = %q，应为 %q", got, ModelsDevURL)
		}
		if e, ok := mdDocEntry("m"); !ok || e != (modelsDevEntry{Context: 10, Output: 1}) {
			t.Errorf("索引 = %+v/%v", e, ok)
		}
	})
}

// errReader 每次 Read 都返回错误（模拟响应体读取失败）。
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

// repeatReader 产生 n 字节 'x'（模拟超大响应，不预先分配整块内存）。
type repeatReader struct{ n int }

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.EOF
	}
	if len(p) > r.n {
		p = p[:r.n]
	}
	if len(p) > 0 {
		p[0] = 'x'
		// 成倍复制相同内容，避免 race 为 32 MiB 的每个字节执行一次写插桩。
		for filled := 1; filled < len(p); {
			filled += copy(p[filled:], p[:filled])
		}
	}
	r.n -= len(p)
	return len(p), nil
}

// backfillMisses：文档没拉到时不动作；拉到后把曾 miss 的模型
// 写进 model.json 并清负缓存，仍查不到的保留负缓存。
func TestModelsDevBackfillMisses(t *testing.T) {
	mdReset(t)
	loadModelCatalogAt(t.TempDir() + "/model.json")
	modelsDev.mu.Lock()
	modelsDev.negatives = map[string]time.Time{"found": time.Now(), "missing": time.Now()}
	modelsDev.mu.Unlock()

	modelsDev.backfillMisses() // doc 仍为 nil：不动作
	if _, ok := modelCatalogGet("found"); ok {
		t.Error("文档未就绪时不应回填")
	}
	if mdNegativeCount() != 2 {
		t.Errorf("文档未就绪时负缓存不应变化，实际 %d", mdNegativeCount())
	}

	modelsDev.mu.Lock()
	modelsDev.doc = map[string]modelsDevEntry{"found": {Context: 777, Output: 88}}
	modelsDev.mu.Unlock()
	modelsDev.backfillMisses()

	e, ok := modelCatalogGet("found")
	if !ok || e.ContextLength != 777 || e.MaxOutputTokens != 88 || e.Source != "modelsdev" {
		t.Errorf("回填条目 = %+v/%v", e, ok)
	}
	if modelsDev.negativeFresh("found") {
		t.Error("回填成功应清除负缓存")
	}
	if !modelsDev.negativeFresh("missing") {
		t.Error("仍查不到的模型应保留负缓存")
	}
}

// resetModelsDev 清空全部单例状态。
func TestModelsDevReset(t *testing.T) {
	modelsDev.mu.Lock()
	modelsDev.doc = map[string]modelsDevEntry{"m": {Context: 1}}
	modelsDev.fetched = true
	modelsDev.lastFetch = time.Now()
	modelsDev.negatives = map[string]time.Time{"m": time.Now()}
	modelsDev.mu.Unlock()

	resetModelsDev()

	modelsDev.mu.Lock()
	defer modelsDev.mu.Unlock()
	if modelsDev.doc != nil || modelsDev.fetched || !modelsDev.lastFetch.IsZero() || modelsDev.negatives != nil {
		t.Errorf("reset 后仍有状态: doc=%v fetched=%v lastFetch=%v negatives=%v",
			modelsDev.doc, modelsDev.fetched, modelsDev.lastFetch, modelsDev.negatives)
	}
}
