// cross_realm_test.go 跨域负载均衡（pool.cross_realm）：不带前缀的模型名在国内版 + 国际版合池选号，
// 只进模型目录里有这个模型的域；带前缀的照旧钉死一域；开关关闭时与原来逐字一致。
package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/livecfg"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestCrossRealmFor(t *testing.T) {
	has := func(cn, global []string) func(realm, model string) bool {
		return func(realm, model string) bool {
			list := cn
			if realm == "global" {
				list = global
			}
			for _, m := range list {
				if m == model {
					return true
				}
			}
			return false
		}
	}
	cases := []struct {
		name       string
		cn, global []string
		want       string
	}{
		{"两域都有 → 合池", []string{"m"}, []string{"m"}, ""},
		{"只有国际版有", nil, []string{"m"}, "global"},
		{"只有国内版有", []string{"m"}, nil, "cn"},
		{"两边目录都没有 → 与关闭开关一致走国内版", nil, nil, "cn"},
	}
	for _, c := range cases {
		if got := crossRealmFor("m", has(c.cn, c.global)); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestBareModelEntries 不带前缀的条目：两域并集按名字去重、国内版元数据优先、realms 标出分到哪些域、
// 不带 is_default；生成时复制条目，不改动原来带前缀的条目。
func TestBareModelEntries(t *testing.T) {
	prefixed := []map[string]any{
		{"id": "cn:a", "credits": "x1.00", "is_default": true},
		{"id": "cn:b", "credits": "x0.50"},
		{"id": "global:a", "credits": "x0.00"},
		{"id": "global:c", "credits": "x2.00"},
	}
	got := bareModelEntries(prefixed)
	ids := make([]string, 0, len(got))
	for _, e := range got {
		ids = append(ids, e["id"].(string))
	}
	if !reflect.DeepEqual(ids, []string{"a", "b", "c"}) {
		t.Fatalf("ids=%v want [a b c]", ids)
	}
	if got[0]["credits"] != "x1.00" {
		t.Errorf("a 应取国内版元数据，credits=%v", got[0]["credits"])
	}
	if !reflect.DeepEqual(got[0]["realms"], []string{"cn", "global"}) || !reflect.DeepEqual(got[2]["realms"], []string{"global"}) {
		t.Errorf("realms a=%v c=%v", got[0]["realms"], got[2]["realms"])
	}
	if _, has := got[0]["is_default"]; has {
		t.Error("不带前缀的条目不应带 is_default")
	}
	if prefixed[0]["id"] != "cn:a" || prefixed[0]["is_default"] != true {
		t.Errorf("原条目被改动：%v", prefixed[0])
	}
}

// crossRealmUpstream 假上游：两个域各自的 chat / 模型目录 host，记录每次 chat 打到哪个域。
// 目录按 Authorization 区分：国内版号拉到 cnModels，国际版号拉到 globalModels。
type crossRealmUpstream struct {
	mu    sync.Mutex
	chats []string // 每次 chat 请求的 host
}

func (u *crossRealmUpstream) client(t *testing.T, cnModels, globalModels []string) *upstream.Client {
	t.Helper()
	catalog := func(ids []string) string {
		ms := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			ms = append(ms, map[string]any{"id": id})
		}
		b, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{
			"models": ms, "agents": []map[string]any{{"name": "cli", "models": ids}},
		}})
		return string(b)
	}
	c := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body, ct := "", "application/json"
			switch {
			case strings.HasSuffix(r.URL.Path, "/chat/completions"):
				u.mu.Lock()
				u.chats = append(u.chats, r.URL.Host)
				u.mu.Unlock()
				body, ct = sseOK, "text/event-stream"
			case r.Header.Get("Authorization") == "Bearer at-global":
				body = catalog(globalModels)
			default:
				body = catalog(cnModels)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{ct}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
		ChatBaseCN:        "https://cn.fake",
		BillingBaseCN:     "https://cn.fake",
		ChatBaseGlobal:    "https://global.fake",
		BillingBaseGlobal: "https://global.fake",
		GlobalEnabled:     true,
	}
	return c
}

func (u *crossRealmUpstream) lastChat() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.chats) == 0 {
		return ""
	}
	return u.chats[len(u.chats)-1]
}

// TestChatCrossRealmRouting 端到端：国际版号积分更高（确定性随机源下合池必选它），
// 据此区分「合池选号」与「只在国内版选」。
func TestChatCrossRealmRouting(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	cnModels := []string{"m-both", "m-cn"}
	globalModels := []string{"m-both", "m-global"}

	cases := []struct {
		name          string
		on            bool
		globalEnabled bool
		model         string
		want          string
	}{
		{"开关关闭：不带前缀照旧走国内版", false, true, "m-both", "cn.fake"},
		{"开关打开：两域都有 → 合池，选到积分更高的国际版号", true, true, "m-both", "global.fake"},
		{"开关打开：带 cn: 前缀仍钉死国内版", true, true, "cn:m-both", "cn.fake"},
		{"开关打开：只有国内版有的模型不进国际版", true, true, "m-cn", "cn.fake"},
		{"开关打开：只有国际版有的模型直接走国际版", true, true, "m-global", "global.fake"},
		{"开关打开：目录里都没有 → 走国内版", true, true, "m-unknown", "cn.fake"},
		{"开关打开但国际版没启用：照旧走国内版", true, false, "m-both", "cn.fake"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cn := &auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
			gl := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
			p := testPoolWith(cn, gl)
			p.SetCredits("cn1", 100, 0)
			p.SetCredits("g1", 1000, 0)

			fake := &crossRealmUpstream{}
			up := fake.client(t, cnModels, globalModels)
			// 启动预热同款：两个域的目录各拉一次
			if _, err := up.FetchModels(cn); err != nil {
				t.Fatalf("cn 目录: %v", err)
			}
			if _, err := up.FetchModels(gl); err != nil {
				t.Fatalf("global 目录: %v", err)
			}

			h := NewHandler(Config{
				Pool: p, Upstream: up, GlobalEnabled: c.globalEnabled,
				Live: livecfg.New(livecfg.Snapshot{CrossRealm: c.on}),
			})
			body := `{"model":"` + c.model + `","messages":[{"role":"user","content":"hi"}],"stream":true}`
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
			if rec.Code != 200 {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
			}
			if got := fake.lastChat(); got != c.want {
				t.Errorf("chat 打到 %q want %q", got, c.want)
			}
		})
	}
}

// TestChatCrossRealmHotToggle 开关热生效：同一个 handler，改快照后下一个请求即按新开关选号。
func TestChatCrossRealmHotToggle(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	cn := &auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	gl := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
	p := testPoolWith(cn, gl)
	p.SetCredits("cn1", 100, 0)
	p.SetCredits("g1", 1000, 0)
	fake := &crossRealmUpstream{}
	up := fake.client(t, []string{"m-both"}, []string{"m-both"})
	_, _ = up.FetchModels(cn)
	_, _ = up.FetchModels(gl)
	live := livecfg.New(livecfg.Snapshot{})
	h := NewHandler(Config{Pool: p, Upstream: up, GlobalEnabled: true, Live: live})

	send := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"m-both","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
		if rec.Code != 200 {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
		}
		return fake.lastChat()
	}
	if got := send(); got != "cn.fake" {
		t.Fatalf("关闭时 got %q want cn.fake", got)
	}
	live.Store(livecfg.Snapshot{CrossRealm: true})
	if got := send(); got != "global.fake" {
		t.Fatalf("热打开后 got %q want global.fake", got)
	}
}

// TestModelsCrossRealmBareEntries /v1/models：开关打开时在最前面额外列出不带前缀的名字（两域并集去重），
// 带前缀的条目照旧都在；关闭时不列。
func TestModelsCrossRealmBareEntries(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	// dynamicModelsCache 是包级缓存：测完还原，免得后面的 /v1/models 测试读到这里的假目录
	dynamicModelsCache.Lock()
	saved := struct {
		ids             []upstream.ModelInfo
		fetched, failed time.Time
	}{dynamicModelsCache.ids, dynamicModelsCache.fetched, dynamicModelsCache.lastFail}
	dynamicModelsCache.Unlock()
	t.Cleanup(func() {
		dynamicModelsCache.Lock()
		dynamicModelsCache.ids, dynamicModelsCache.fetched, dynamicModelsCache.lastFail = saved.ids, saved.fetched, saved.failed
		dynamicModelsCache.Unlock()
	})
	list := func(on bool) []string {
		dynamicModelsCache.Lock()
		dynamicModelsCache.ids = nil
		dynamicModelsCache.fetched = time.Time{}
		dynamicModelsCache.lastFail = time.Time{}
		dynamicModelsCache.Unlock()
		cn := &auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
		gl := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
		up := (&crossRealmUpstream{}).client(t, []string{"m-both", "m-cn"}, []string{"m-both", "m-global"})
		h := NewHandler(Config{
			Pool: testPoolWith(cn, gl), Upstream: up, GlobalEnabled: true,
			Live: livecfg.New(livecfg.Snapshot{CrossRealm: on}),
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
		for _, m := range resp.Data {
			ids = append(ids, m["id"].(string))
		}
		return ids
	}
	prefixed := []string{"cn:m-both", "cn:m-cn", "global:m-both", "global:m-global"}
	got := list(false)
	// 上游目录的域内顺序不固定；这里只约束条目集合，以及裸名整体位于前缀条目前。
	sort.Strings(got)
	if !reflect.DeepEqual(got, prefixed) {
		t.Errorf("关闭时 ids=%v want %v", got, prefixed)
	}
	bare := []string{"m-both", "m-cn", "m-global"}
	want := append(bare, prefixed...)
	got = list(true)
	if len(got) != len(want) {
		t.Fatalf("打开时 ids=%v want %v", got, want)
	}
	sort.Strings(got[:len(bare)])
	sort.Strings(got[len(bare):])
	if !reflect.DeepEqual(got, want) {
		t.Errorf("打开时 ids=%v want %v", got, want)
	}
}
