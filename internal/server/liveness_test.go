// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

// livenessUpstream 测活用的假上游：对话以外的请求回模型目录（带倍率），对话按令牌决定成败，
// 记下每次对话用的令牌、host 和请求体里的模型。
type livenessUpstream struct {
	mu          sync.Mutex
	tokens      []string
	hosts       []string
	models      []string
	maxTokens   []float64
	fail        map[string]bool // 这些令牌的对话回 429 限流
	forbid      map[string]bool // 这些模型的对话回 400 11158（这类账号不能用）
	catalogFail bool
}

func (u *livenessUpstream) client() *upstream.Client {
	catalog, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{
		"models": []map[string]any{
			{"id": "m-dear", "credits": "x2"}, {"id": "m-cheap", "credits": "x0.1"}, {"id": "m-hidden", "credits": "x0"},
		},
		"agents": []map[string]any{{"name": "cli", "models": []string{"m-dear", "m-cheap", "m-hidden"}}},
	}})
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			resp := func(status int, ct, body string) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{ct}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			}
			if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
				if u.catalogFail {
					return resp(500, "application/json", `boom`)
				}
				return resp(200, "application/json", string(catalog))
			}
			var req struct {
				Model     string  `json:"model"`
				MaxTokens float64 `json:"max_tokens"`
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &req)
			authz := r.Header.Get("Authorization")
			u.mu.Lock()
			u.tokens = append(u.tokens, authz)
			u.hosts = append(u.hosts, r.URL.Host)
			u.models = append(u.models, req.Model)
			u.maxTokens = append(u.maxTokens, req.MaxTokens)
			fail, forbid := u.fail[authz], u.forbid[req.Model]
			u.mu.Unlock()
			if fail {
				return resp(http.StatusTooManyRequests, "application/json", `{"code":1,"msg":"rate limit"}`)
			}
			if forbid {
				return resp(http.StatusBadRequest, "application/json",
					`{"code":11158,"msg":"model [`+req.Model+`] is not allowed for user type [personal_free_user]","requestId":"r1"}`)
			}
			return resp(200, "text/event-stream", sseOK)
		})},
		ChatBaseCN:        "https://cn.fake",
		BillingBaseCN:     "https://cn.fake",
		ChatBaseGlobal:    "https://global.fake",
		BillingBaseGlobal: "https://global.fake",
		GlobalEnabled:     true,
	}
}

func (u *livenessUpstream) calls() (tokens, hosts, models []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.tokens...), append([]string(nil), u.hosts...), append([]string(nil), u.models...)
}

// captureLog 收下这个测试期间的 log 输出。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// TestTestAccountRecovers 测活钉死指定的号：冷却、熔断中也照发，测通就恢复；只发一次、发「hi」限 16 token，
// 不绑会话粘性；请求日志记成测活入口、密钥一列「测活」，用量算在测活名下，结果记进上次测活。
func TestTestAccountRecovers(t *testing.T) {
	logs := captureLog(t)
	fake := &livenessUpstream{}
	u1 := &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}
	u2 := &auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999}
	p := testPoolWith(u1, u2)
	p.Cooldown("u1", pool.CoolSoft, time.Hour, "429 限流")
	for range 10 {
		p.NoteError("u1")
	}
	if st, _ := p.Status("u1"); !st.Cooling || !time.Now().Before(st.BreakerUntil) {
		t.Fatalf("precondition: u1 应在冷却和熔断中: %+v", st)
	}
	sess := session.New(session.Config{TTL: time.Minute, Store: newBindStore(), Available: p.AvailableUIDs, AvailableForModel: p.WeightedAvailableUIDsForModelRealm})
	rl := reqlog.New(reqlog.Config{})
	t.Cleanup(rl.Close)
	u := usage.New("")
	h := NewHandler(Config{Pool: p, Upstream: fake.client(), Session: sess, RequestLog: rl, Usage: u, SoftCooldown: time.Minute})

	res, reply, err := h.TestAccount(context.Background(), "u1", "m-cheap", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Status != 200 || res.Model != "m-cheap" || !res.Recovered || res.Scheduled || res.At.IsZero() || res.DurationMs < 1 {
		t.Fatalf("测活结果 = %+v", res)
	}
	if reply != "你好" {
		t.Fatalf("上游回复 = %q", reply)
	}
	tokens, hosts, models := fake.calls()
	if len(tokens) != 1 || tokens[0] != "Bearer at1" || hosts[0] != "cn.fake" || models[0] != "m-cheap" || fake.maxTokens[0] != livenessMaxTokens {
		t.Fatalf("对话请求: tokens=%v hosts=%v models=%v max_tokens=%v", tokens, hosts, models, fake.maxTokens)
	}
	st, _ := p.Status("u1")
	if st.Cooling || st.Reason != "" || time.Now().Before(st.BreakerUntil) || st.InFlight != 0 {
		t.Fatalf("测通后应已恢复、名额已还: %+v", st)
	}
	if st.LastLiveness == nil || *st.LastLiveness != res {
		t.Fatalf("上次测活 = %+v，应为 %+v", st.LastLiveness, res)
	}
	if n := sess.Count(); n != 0 {
		t.Fatalf("测活不应绑会话粘性，绑了 %d 个", n)
	}
	recent := rl.Snapshot().Recent
	if len(recent) != 1 || recent[0].Path != livenessPath || recent[0].APIKey != livenessLabel || !recent[0].OK || recent[0].Model != "m-cheap" {
		t.Fatalf("请求日志 = %+v", recent)
	}
	snap := u.SnapshotWindow(usage.Window{}, nil, nil)
	if len(snap.ByKey) != 1 || snap.ByKey[0].Key != livenessUsageKey {
		t.Fatalf("用量按密钥 = %+v", snap.ByKey)
	}
	if !strings.Contains(logs.String(), "liveness u1: ok model=m-cheap") || !strings.Contains(logs.String(), "recovered=true") {
		t.Fatalf("任务日志没记下测活:\n%s", logs)
	}

	// 健康的号测通：没什么可解除
	res, _, err = h.TestAccount(context.Background(), "u2", "m-cheap", true)
	if err != nil || !res.OK || res.Recovered || !res.Scheduled {
		t.Fatalf("健康的号: res=%+v err=%v", res, err)
	}
}

// TestTestAccountFailureNoRotate 测活失败：照真实请求罚号（限流冷却），但只试这一个号、不换号，
// 结果记成不通。没开请求日志也照常测。
func TestTestAccountFailureNoRotate(t *testing.T) {
	old := rotateBackoffBase
	rotateBackoffBase = 0
	t.Cleanup(func() { rotateBackoffBase = old })
	logs := captureLog(t)
	fake := &livenessUpstream{fail: map[string]bool{"Bearer at1": true}}
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: fake.client(), SoftCooldown: time.Minute})

	res, reply, err := h.TestAccount(context.Background(), "u1", "m-cheap", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Status < 400 || res.Error == "" || res.Recovered || reply != "" {
		t.Fatalf("测活结果 = %+v reply=%q", res, reply)
	}
	if tokens, _, _ := fake.calls(); len(tokens) != 1 || tokens[0] != "Bearer at1" {
		t.Fatalf("只应试 u1 一次，实际 %v", tokens)
	}
	st, _ := p.Status("u1")
	if !st.Cooling || st.LastLiveness == nil || st.LastLiveness.OK || st.InFlight != 0 {
		t.Fatalf("失败应照真实请求罚号、记成不通、名额已还: %+v", st)
	}
	if !strings.Contains(logs.String(), "liveness u1: WARN 失败 model=m-cheap") {
		t.Fatalf("任务日志没记下失败:\n%s", logs)
	}

	// 冷却中的号照样能再测：还是只发给它
	if res, _, _ = h.TestAccount(context.Background(), "u1", "m-cheap", false); res.OK {
		t.Fatal("上游仍限流，不应测通")
	}
	if tokens, _, _ := fake.calls(); len(tokens) != 2 || tokens[1] != "Bearer at1" {
		t.Fatalf("冷却中的号也应照发，实际 %v", tokens)
	}
}

// TestTestAccountAutoModel 没指定模型：挑这个号目录里倍率最低的，models.hidden 藏起来的不算；
// 目录还没拉到时先拉一次。国际版号发给国际版。目录拉不到、号不存在时报错。
func TestTestAccountAutoModel(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	t.Run("国内版", func(t *testing.T) {
		resetModelsCache(t)
		fake := &livenessUpstream{}
		p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
		h := NewHandler(Config{
			Pool: p, Upstream: fake.client(),
			Live: livecfg.New(livecfg.Snapshot{HiddenModels: livecfg.NormalizeHiddenModels([]string{"m-hidden"})}),
		})
		res, _, err := h.TestAccount(context.Background(), "u1", "", false)
		if err != nil || !res.OK || res.Model != "m-cheap" {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if _, _, models := fake.calls(); len(models) != 1 || models[0] != "m-cheap" {
			t.Fatalf("对话请求的模型 = %v", models)
		}
		// 目录已经拉到：直接挑，不再拉
		if res, _, err := h.TestAccount(context.Background(), "u1", "", false); err != nil || res.Model != "m-cheap" {
			t.Fatalf("第二次: res=%+v err=%v", res, err)
		}
	})

	t.Run("国际版", func(t *testing.T) {
		fake := &livenessUpstream{}
		gl := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
		h := NewHandler(Config{Pool: testPoolWith(gl), Upstream: fake.client(), GlobalEnabled: true})
		res, _, err := h.TestAccount(context.Background(), "g1", "", false)
		if err != nil || !res.OK || res.Model != "m-hidden" {
			t.Fatalf("没藏模型时应挑倍率 0 的: res=%+v err=%v", res, err)
		}
		if _, hosts, models := fake.calls(); len(hosts) != 1 || hosts[0] != "global.fake" || models[0] != "m-hidden" {
			t.Fatalf("国际版号应发给国际版: hosts=%v models=%v", hosts, models)
		}
	})

	t.Run("目录拉不到", func(t *testing.T) {
		resetModelsCache(t)
		fake := &livenessUpstream{catalogFail: true}
		h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}), Upstream: fake.client()})
		if _, _, err := h.TestAccount(context.Background(), "u1", "", false); err == nil || !strings.Contains(err.Error(), "模型目录还没拉到") {
			t.Fatalf("err = %v", err)
		}
		if tokens, _, _ := fake.calls(); len(tokens) != 0 {
			t.Fatalf("挑不出模型不应发对话: %v", tokens)
		}
	})

	t.Run("号不存在", func(t *testing.T) {
		h := NewHandler(Config{Pool: testPoolWith(), Upstream: (&livenessUpstream{}).client()})
		if _, _, err := h.TestAccount(context.Background(), "nope", "m", false); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestTestAccountRepicksRefusedModel 自动挑的模型被上游拒了（11158，号本身没问题）：加进号不接的模型名单，换下一个
// 最便宜的再测，最多换 livenessRepicks 次；之后自动挑模型跳过名单里的。别的失败不换模型；都被拒过就挑不出模型。
func TestTestAccountRepicksRefusedModel(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	newGlobal := func(fake *livenessUpstream) (*Handler, *pool.Pool) {
		resetModelsCache(t)
		gl := &auth.Auth{UID: "g1", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
		p := testPoolWith(gl)
		return NewHandler(Config{Pool: p, Upstream: fake.client(), GlobalEnabled: true}), p
	}

	t.Run("换模型测通", func(t *testing.T) {
		logs := captureLog(t)
		fake := &livenessUpstream{forbid: map[string]bool{"m-hidden": true, "m-cheap": true}}
		h, p := newGlobal(fake)
		res, _, err := h.TestAccount(context.Background(), "g1", "", false)
		if err != nil || !res.OK || res.Model != "m-dear" {
			t.Fatalf("应换到 m-dear 测通: res=%+v err=%v", res, err)
		}
		if _, _, models := fake.calls(); strings.Join(models, ",") != "m-hidden,m-cheap,m-dear" {
			t.Fatalf("对话请求的模型 = %v", models)
		}
		if st, _ := p.Status("g1"); len(st.BlockedModels) != 2 || !st.BlockedModels[0].Auto || st.LastLiveness == nil || !st.LastLiveness.OK {
			t.Fatalf("两个被拒的模型应自动加进名单、记下最后一次测通: %+v %+v", st.BlockedModels, st.LastLiveness)
		}
		if !strings.Contains(logs.String(), "这个号不能用 model=m-hidden，换个模型再测") {
			t.Fatalf("日志应说明换模型:\n%s", logs)
		}
		if res, _, _ := h.TestAccount(context.Background(), "g1", "", false); res.Model != "m-dear" {
			t.Fatalf("之后自动挑模型应跳过名单里的: %+v", res)
		}
	})

	t.Run("换满次数", func(t *testing.T) {
		fake := &livenessUpstream{forbid: map[string]bool{"m-hidden": true, "m-cheap": true, "m-dear": true}}
		h, _ := newGlobal(fake)
		res, _, err := h.TestAccount(context.Background(), "g1", "", false)
		if err != nil || res.OK || res.Model != "m-dear" || res.Status != http.StatusBadRequest {
			t.Fatalf("三个都被拒应记不通: res=%+v err=%v", res, err)
		}
		if _, _, models := fake.calls(); len(models) != 1+livenessRepicks {
			t.Fatalf("最多换 %d 次模型: %v", livenessRepicks, models)
		}
		if _, _, err := h.TestAccount(context.Background(), "g1", "", false); err == nil || !strings.Contains(err.Error(), "能接的模型都不能用") {
			t.Fatalf("都被拒过应挑不出模型: %v", err)
		}
	})

	t.Run("别的失败不换模型", func(t *testing.T) {
		fake := &livenessUpstream{fail: map[string]bool{"Bearer at-global": true}}
		h, _ := newGlobal(fake)
		if res, _, err := h.TestAccount(context.Background(), "g1", "", false); err != nil || res.OK {
			t.Fatalf("限流应记不通: res=%+v err=%v", res, err)
		}
		if _, _, models := fake.calls(); len(models) != 1 {
			t.Fatalf("限流不是模型的问题，不应换模型: %v", models)
		}
	})
}

// TestTestAccountBusy 这个号在途满了：测活不排队，直接回 503 不通，不换号。
func TestTestAccountBusy(t *testing.T) {
	old := acquireInFlight
	acquireInFlight = func(*pool.Pool, string) bool { return false }
	t.Cleanup(func() { acquireInFlight = old })
	fake := &livenessUpstream{}
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: fake.client()})
	res, _, err := h.TestAccount(context.Background(), "u1", "m-cheap", false)
	if err != nil || res.OK || res.Status != http.StatusServiceUnavailable || !strings.Contains(res.Error, "in-flight or requests-per-minute limit") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if tokens, _, _ := fake.calls(); len(tokens) != 0 {
		t.Fatalf("在途满了不应发对话: %v", tokens)
	}
}

// TestLivenessReply 从流式响应里拼出回复：正文优先，没有正文用思考内容；跳过非 data 行、[DONE] 和坏 JSON；超长截断。
func TestLivenessReply(t *testing.T) {
	chunk := func(field, text string) string {
		b, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]string{field: text}}}})
		return "data: " + string(b) + "\n\n"
	}
	cases := []struct{ name, raw, want string }{
		{"正文", chunk("content", "你") + ": ping\n\n" + chunk("content", "好") + "data: [DONE]\n\n", "你好"},
		{"正文优先于思考", chunk("reasoning_content", "想") + chunk("content", " hi "), "hi"},
		{"只有思考", chunk("reasoning_content", "想一想") + "data: {bad\n\n", "想一想"},
		{"空", "", ""},
	}
	for _, c := range cases {
		if got := livenessReply([]byte(c.raw)); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	if got := livenessReply([]byte(chunk("content", strings.Repeat("长", 1000)))); len(got) > livenessReplyMax+len("…") || !strings.HasPrefix(got, "长") {
		t.Errorf("超长回复应截到 %d 字节左右，got %d", livenessReplyMax, len(got))
	}
}

// TestLivenessWriter 收测活响应的 ResponseWriter：正文最多留 256KB，其余照收不报错。
func TestLivenessWriter(t *testing.T) {
	w := &livenessWriter{header: http.Header{}}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusTeapot)
	w.Flush()
	big := bytes.Repeat([]byte("a"), 200*1024)
	for range 2 {
		if n, err := w.Write(big); n != len(big) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if n, _ := w.Write(big); n != len(big) || w.body.Len() != 2*len(big) {
		t.Fatalf("超过 256KB 后不再留，body=%d", w.body.Len())
	}
	if w.header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("Header 应可写")
	}
}
