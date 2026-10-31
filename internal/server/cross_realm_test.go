// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

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

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// mergeIf 开关类测试用：on 为 true 时合池，否则关。
func mergeIf(on bool) string {
	if on {
		return livecfg.CrossRealmMerge
	}
	return livecfg.CrossRealmOff
}

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
	mu     sync.Mutex
	chats  []string // 每次 chat 请求的 host
	models []string // 每次 chat 请求体里的模型名
	failCN bool     // 为 true 时国内版的 chat 一律回 429（限流），用来测换号
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
			body, ct, status := "", "application/json", http.StatusOK
			switch {
			case strings.HasSuffix(r.URL.Path, "/chat/completions"):
				var req struct {
					Model string `json:"model"`
				}
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &req)
				u.mu.Lock()
				u.chats = append(u.chats, r.URL.Host)
				u.models = append(u.models, req.Model)
				fail := u.failCN && r.URL.Host == "cn.fake"
				u.mu.Unlock()
				if fail {
					status, body = http.StatusTooManyRequests, `{"code":1,"msg":"rate limit"}`
				} else {
					body, ct = sseOK, "text/event-stream"
				}
			case r.Header.Get("Authorization") == "Bearer at-global":
				body = catalog(globalModels)
			default:
				body = catalog(cnModels)
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{ct}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
		ChatBaseCN:        "https://cn.fake",
		BillingBaseCN:     "https://cn.fake",
		ChatBaseGlobal:    "https://global.fake",
		BillingBaseGlobal: "https://global.fake",
		GlobalEnabled:     true,
	}
	return c
}

// lastModel 最近一次 chat 打到上游的模型名。
func (u *crossRealmUpstream) lastModel() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.models) == 0 {
		return ""
	}
	return u.models[len(u.models)-1]
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
				Live: livecfg.New(livecfg.Snapshot{CrossRealm: mergeIf(c.on)}),
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
	live.Store(livecfg.Snapshot{CrossRealm: livecfg.CrossRealmMerge})
	if got := send(); got != "global.fake" {
		t.Fatalf("热打开后 got %q want global.fake", got)
	}
}

// TestChatCrossRealmSticky 会话粘性按跨域口径分配：合池时会话能绑到国际版号，开关关闭时不会粘到国际版号。
// 国内版号积分更高，不走粘性的普通选号会挑它，所以打到国际版只能是粘性分配的结果。
// 粘性路由的接法与 main.go 一致（AvailableForModel 用 pool.WeightedAvailableUIDsForModelRealm）。
func TestChatCrossRealmSticky(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	cases := []struct {
		name  string
		on    bool
		bound map[string]string // 请求前已有的会话绑定
		want  string
	}{
		{"合池：已绑国际版号的会话继续用它", true, map[string]string{"conv-1": "g1"}, "global.fake"},
		{"合池：新会话可以分到还没绑会话的国际版号", true, map[string]string{"conv-other": "cn1"}, "global.fake"},
		{"开关关闭：绑在国际版号上的会话改回国内版", false, map[string]string{"conv-1": "g1"}, "cn.fake"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cn := &auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
			gl := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
			p := testPoolWith(cn, gl)
			p.SetCredits("cn1", 1000, 0)
			p.SetCredits("g1", 100, 0)
			fake := &crossRealmUpstream{}
			up := fake.client(t, []string{"m-both"}, []string{"m-both"})
			_, _ = up.FetchModels(cn)
			_, _ = up.FetchModels(gl)
			sess := session.New(session.Config{
				TTL:               time.Minute,
				Available:         p.AvailableUIDs,
				AvailableForModel: p.WeightedAvailableUIDsForModelRealm,
			})
			for key, uid := range c.bound {
				sess.Bind(key, uid)
			}
			h := NewHandler(Config{
				Pool: p, Upstream: up, Session: sess, GlobalEnabled: true,
				Live: livecfg.New(livecfg.Snapshot{CrossRealm: mergeIf(c.on)}),
			})
			// 同一会话连发两次：第二次走绑定快路径，仍应留在同一域
			for i := 0; i < 2; i++ {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
					`{"model":"m-both","messages":[{"role":"user","content":"hi"}],"stream":true,"metadata":{"conversation_id":"conv-1"}}`)))
				if rec.Code != 200 {
					t.Fatalf("第 %d 次 code=%d body=%s", i+1, rec.Code, rec.Body)
				}
				if got := fake.lastChat(); got != c.want {
					t.Fatalf("第 %d 次 chat 打到 %q want %q", i+1, got, c.want)
				}
			}
		})
	}
}

// cnFirstHandler 国内版优先（cn_first）的测试网关：国内版号 cn1 积分 100、国际版号 g1 积分 1000
// （合池时必选 g1，打到国内版只能是国内版优先的结果）；两个域的目录都有 m-both，另各有一个独有的模型。
// sess 不为 nil 时接上会话粘性，接法同 main.go。
func cnFirstHandler(t *testing.T, fake *crossRealmUpstream, sess func(*pool.Pool) *session.Router) (*Handler, *pool.Pool) {
	t.Helper()
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	cn := &auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	gl := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
	p := testPoolWith(cn, gl)
	p.SetCredits("cn1", 100, 0)
	p.SetCredits("g1", 1000, 0)
	up := fake.client(t, []string{"m-both", "m-cn"}, []string{"m-both", "m-global"})
	if _, err := up.FetchModels(cn); err != nil {
		t.Fatalf("cn 目录: %v", err)
	}
	if _, err := up.FetchModels(gl); err != nil {
		t.Fatalf("global 目录: %v", err)
	}
	cfg := Config{Pool: p, Upstream: up, GlobalEnabled: true, Live: livecfg.New(livecfg.Snapshot{CrossRealm: livecfg.CrossRealmCNFirst})}
	if sess != nil {
		cfg.Session = sess(p)
	}
	return NewHandler(cfg), p
}

// serveCrossRealm 发一个流式对话请求，返回回应。conv 不为空时带上会话 id。
func serveCrossRealm(h *Handler, model, conv string) *httptest.ResponseRecorder {
	meta := ""
	if conv != "" {
		meta = `,"metadata":{"conversation_id":"` + conv + `"}`
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],"stream":true`+meta+`}`)))
	return rec
}

// sendCrossRealm 同 serveCrossRealm，要求 200。
func sendCrossRealm(t *testing.T, h *Handler, model, conv string) {
	t.Helper()
	if rec := serveCrossRealm(h, model, conv); rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

// TestChatCrossRealmCNFirst 国内版优先：国内版此刻有能接的号就只在国内版选；国内版的号冷却、在途满、
// 禁用、暂停、这个模型冷却时改用国际版；两边都没有能接的号时照合池兜底；只有一个域有的模型和
// 带前缀的名字照旧。
func TestChatCrossRealmCNFirst(t *testing.T) {
	coolCN := func(p *pool.Pool) { p.Cooldown("cn1", pool.CoolSoft, time.Hour, "test") }
	cases := []struct {
		name  string
		model string
		set   func(*pool.Pool)
		want  string
	}{
		{"国内版有号：只在国内版选", "m-both", func(*pool.Pool) {}, "cn.fake"},
		{"国内版号冷却：用国际版", "m-both", coolCN, "global.fake"},
		{"国内版号在途满：用国际版，不排队", "m-both", func(p *pool.Pool) {
			p.SetMaxInFlight(1)
			p.SetInFlightWait(time.Second)
			if !p.Acquire("cn1") {
				t.Fatal("Acquire 应成功")
			}
		}, "global.fake"},
		{"国内版号禁用：用国际版", "m-both", func(p *pool.Pool) { p.Disable("cn1", "test") }, "global.fake"},
		{"国内版号暂停：用国际版", "m-both", func(p *pool.Pool) { p.Pause("cn1") }, "global.fake"},
		{"国内版号这个模型冷却：用国际版", "m-both", func(p *pool.Pool) {
			p.CooldownModel("cn1", "m-both", time.Hour, "test")
		}, "global.fake"},
		{"两边都冷却：照合池兜底，选先到期的国际版号", "m-both", func(p *pool.Pool) {
			coolCN(p)
			p.Cooldown("g1", pool.CoolSoft, time.Minute, "test")
		}, "global.fake"},
		{"只有国内版有的模型：国内版号冷却也不进国际版", "m-cn", coolCN, "cn.fake"},
		{"只有国际版有的模型：直接走国际版", "m-global", func(*pool.Pool) {}, "global.fake"},
		{"带 cn: 前缀：国内版号冷却也钉死国内版", "cn:m-both", coolCN, "cn.fake"},
		{"带 global: 前缀：钉死国际版", "global:m-both", func(*pool.Pool) {}, "global.fake"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &crossRealmUpstream{}
			h, p := cnFirstHandler(t, fake, nil)
			c.set(p)
			sendCrossRealm(t, h, c.model, "")
			if got := fake.lastChat(); got != c.want {
				t.Errorf("chat 打到 %q want %q", got, c.want)
			}
		})
	}
}

// TestChatCrossRealmCNFirstRotates 国内版的号这次失败了（上游限流），同一个请求换号时国内版已经没有
// 能接的号，改到国际版。
func TestChatCrossRealmCNFirstRotates(t *testing.T) {
	fastRotate(t)
	fake := &crossRealmUpstream{failCN: true}
	h, _ := cnFirstHandler(t, fake, nil)
	sendCrossRealm(t, h, "m-both", "")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if want := []string{"cn.fake", "global.fake"}; !reflect.DeepEqual(fake.chats, want) {
		t.Errorf("chat 依次打到 %v want %v", fake.chats, want)
	}
}

// TestChatCrossRealmCNFirstSticky 会话粘性：国内版的号暂停时会话绑到国际版号，暂停期间一直用它；
// 国内版的号恢复选号后，同一个会话回到国内版。
func TestChatCrossRealmCNFirstSticky(t *testing.T) {
	fake := &crossRealmUpstream{}
	h, p := cnFirstHandler(t, fake, func(p *pool.Pool) *session.Router {
		return session.New(session.Config{
			TTL:               time.Minute,
			Available:         p.AvailableUIDs,
			AvailableForModel: p.WeightedAvailableUIDsForModelRealm,
		})
	})
	p.Pause("cn1")
	for i := 0; i < 2; i++ {
		sendCrossRealm(t, h, "m-both", "conv-1")
		if got := fake.lastChat(); got != "global.fake" {
			t.Fatalf("国内版暂停时第 %d 次打到 %q want global.fake", i+1, got)
		}
	}
	p.Resume("cn1")
	for i := 0; i < 2; i++ {
		sendCrossRealm(t, h, "m-both", "conv-1")
		if got := fake.lastChat(); got != "cn.fake" {
			t.Fatalf("国内版恢复后第 %d 次打到 %q want cn.fake", i+1, got)
		}
	}
}

// TestChatCrossRealmCNFirstSlotWait 两个域能接的号在途都满时排队；醒来时重新定域，哪个域放出名额就用哪个。
func TestChatCrossRealmCNFirstSlotWait(t *testing.T) {
	shortSlotPoll(t, 5*time.Millisecond)
	for _, release := range []string{"cn1", "g1"} {
		t.Run("放出 "+release, func(t *testing.T) {
			fake := &crossRealmUpstream{}
			h, p := cnFirstHandler(t, fake, nil)
			p.SetMaxInFlight(1)
			p.SetInFlightWait(5 * time.Second)
			for _, uid := range []string{"cn1", "g1"} {
				if !p.Acquire(uid) {
					t.Fatalf("Acquire %s 应成功", uid)
				}
			}
			waiting := make(chan struct{})
			var once sync.Once
			old := slotWaitStarted
			slotWaitStarted = func() { once.Do(func() { close(waiting) }) }
			t.Cleanup(func() { slotWaitStarted = old })
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- serveCrossRealm(h, "m-both", "") }()
			waitFor(t, waiting, "开始等空位")
			p.Release(release)
			if rec := waitFor(t, done, "返回"); rec.Code != http.StatusOK {
				t.Fatalf("放出名额后应转发成功: code=%d body=%s", rec.Code, rec.Body)
			}
			want := map[string]string{"cn1": "cn.fake", "g1": "global.fake"}[release]
			if got := fake.lastChat(); got != want {
				t.Errorf("chat 打到 %q want %q", got, want)
			}
		})
	}
}

// TestModelsCrossRealmBareEntries /v1/models：合池和国内版优先时在最前面额外列出不带前缀的名字
// （两域并集去重），带前缀的条目照旧都在；关闭时不列。
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
	list := func(mode string) []string {
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
			Live: livecfg.New(livecfg.Snapshot{CrossRealm: mode}),
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
	got := list(livecfg.CrossRealmOff)
	// 上游目录的域内顺序不固定；这里只约束条目集合，以及裸名整体位于前缀条目前。
	sort.Strings(got)
	if !reflect.DeepEqual(got, prefixed) {
		t.Errorf("关闭时 ids=%v want %v", got, prefixed)
	}
	bare := []string{"m-both", "m-cn", "m-global"}
	want := append(bare, prefixed...)
	for _, mode := range []string{livecfg.CrossRealmMerge, livecfg.CrossRealmCNFirst} {
		got = list(mode)
		if len(got) != len(want) {
			t.Fatalf("%s 时 ids=%v want %v", mode, got, want)
		}
		sort.Strings(got[:len(bare)])
		sort.Strings(got[len(bare):])
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s 时 ids=%v want %v", mode, got, want)
		}
	}
}
