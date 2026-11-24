// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

func TestRequestBilling(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		frames       []map[string]any
		charge       float64
		known        bool
		bad          bool
	}{
		{"小数扣费", "qoder:cn", []map[string]any{{"credits": 0.012345, "original_credits": 9.0, "billable": true}}, 0.012345, true, false},
		{"国际版零扣费", "qoder:global", []map[string]any{{"credits": 0.0, "billable": true}}, 0, true, false},
		{"明确不计费", "qoder:cn", []map[string]any{{"credits": 5.0, "billable": false}}, 0, true, false},
		{"免费无credits", "qoder:cn", []map[string]any{{"billable": false}}, 0, true, false},
		{"未知", "qoder:cn", nil, 0, false, false},
		{"优惠前不能当实际扣费", "qoder:cn", []map[string]any{{"original_credits": 9.0, "billable": true}}, 0, false, false},
		{"缺billable", "qoder:cn", []map[string]any{{"credits": 1.0}}, 0, false, false},
		{"重复累计帧", "qoder:cn", []map[string]any{{"credits": 1.0, "billable": true}, {"credits": 1.0, "billable": true}, {"credits": 1.25, "billable": true}}, 1.25, true, false},
		{"分帧计费字段", "qoder:cn", []map[string]any{{"billable": true}, {"credits": 0.25}}, 0.25, true, false},
		{"免费时忽略无效credits", "qoder:cn", []map[string]any{{"credits": "unused", "billable": false}}, 0, true, false},
		{"后续坏帧不冒充已知", "qoder:cn", []map[string]any{{"credits": 0.25, "billable": true}, {"credits": -1.0}}, 0, false, true},
		{"负数", "qoder:cn", []map[string]any{{"credits": -1.0, "billable": true}}, 0, false, true},
		{"字符串", "qoder:cn", []map[string]any{{"credits": "0.1", "billable": true}}, 0, false, true},
		{"无穷", "qoder:cn", []map[string]any{{"credits": math.Inf(1), "billable": true}}, 0, false, true},
		{"NaN", "qoder:cn", []map[string]any{{"credits": math.NaN(), "billable": true}}, 0, false, true},
		{"billable类型错误", "qoder:cn", []map[string]any{{"credits": 1.0, "billable": "false"}}, 0, false, true},
		{"Code不猜测费用", "traecode:cn", []map[string]any{{"credit": 1.0, "credits": 1.0, "billable": true}}, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "providers.json")
			m, f := testManager(t, path)
			id := importFake(t, m, tc.target, "fixture")
			ledgerPath := filepath.Join(t.TempDir(), "usage.json")
			ledger := usage.New(ledgerPath)
			m.SetUsage(ledger)
			f.chat = func(_ context.Context, _ string, _ Credential, _ string, emit func(map[string]any) error) error {
				for _, u := range tc.frames {
					if err := emit(chunk("model", map[string]any{}, nil, u)); err != nil {
						return err
					}
				}
				return emit(chunk("model", map[string]any{}, "stop", map[string]any{"prompt_tokens": 2.0, "completion_tokens": 3.0}))
			}
			_, err := m.Execute(context.Background(), Request{Target: tc.target, Model: "model", KeyID: "key", Body: []byte(plainChat)}, discardFrame)
			if (err != nil) != tc.bad {
				t.Fatalf("error=%v bad=%v", err, tc.bad)
			}
			got := m.Usage()[0]
			count := int64(0)
			if tc.known {
				count = 1
			}
			if got.Charge != tc.charge || got.ChargeKnown != count || tc.known && got.Unit != "credits" {
				t.Fatalf("计费=%+v", got)
			}
			reopened, _ := Open(path, nil)
			if reopened.Usage()[0].Charge != tc.charge || reopened.Usage()[0].ChargeKnown != count {
				t.Fatal("计费未持久化")
			}
			ledger.Save()
			reload := usage.New(ledgerPath)
			snap := reload.SnapshotWindow(usage.Window{Target: tc.target, Key: "key", KeySet: true}, nil, nil)
			if snap.Totals.Credits != tc.charge || snap.Totals.CreditSamples != count || len(snap.ByAccount) != 1 || snap.ByAccount[0].Key != id {
				t.Fatalf("时序账本=%+v", snap)
			}
		})
	}
}

func TestBillingSurvivesFailedOrCancelledRequest(t *testing.T) {
	for _, end := range []error{errors.New("上游中断"), context.Canceled} {
		m, f := testManager(t, "")
		importFake(t, m, "qoder:cn", "fixture")
		f.chat = func(_ context.Context, _ string, _ Credential, _ string, emit func(map[string]any) error) error {
			if err := emit(chunk("model", map[string]any{}, nil, map[string]any{"credits": 0.125, "billable": true})); err != nil {
				return err
			}
			return end
		}
		_, err := m.Execute(context.Background(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame)
		got := m.Usage()[0]
		if !errors.Is(err, end) || got.Failed != 1 || got.ChargeKnown != 1 || got.Charge != 0.125 {
			t.Fatalf("失败丢失已知扣费 %+v %v", got, err)
		}
	}
}

func TestTraeWorkSingleMessageCharge(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		known, bad bool
	}{
		{"单消息已结算", `{"user_usage_group_by_session":{"session_id":"message","credits_float":0.012345}}`, true, false},
		{"data包装", `{"code":0,"data":{"user_usage_group_by_session":{"session_id":"message","credits_float":0.012345}}}`, true, false},
		{"未结算不当免费", `{"user_usage_group_by_session":{"session_id":"message","credits_float":0}}`, false, false},
		{"空响应", `{}`, false, false},
		{"错误消息归属", `{"user_usage_group_by_session":{"session_id":"another","credits_float":0.012345}}`, false, true},
		{"缺消息归属", `{"user_usage_group_by_session":{"credits_float":0.012345}}`, false, true},
		{"负费用", `{"user_usage_group_by_session":{"session_id":"message","credits_float":-1}}`, false, true},
		{"类型错误", `{"user_usage_group_by_session":{"session_id":"message","credits_float":"0.1"}}`, false, true},
		{"错误码", `{"code":500,"data":{"user_usage_group_by_session":{"session_id":"message","credits_float":0.1}}}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.Method != "POST" || r.URL.Path != "/api/v1/commercial/get_session_usage" || body["session_id"] != "message" || r.Header.Get("Authorization") != "Cloud-IDE-JWT fixture" || r.Header.Get("Content-Type") != "application/json" || r.UserAgent() != traeWebUA {
					t.Error("未对齐官方单消息扣费请求")
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			a := Trae{HTTP: server.Client(), CodeBase: server.URL}
			v, known, err := a.workCharge(context.Background(), Credential{AccessToken: "fixture"}, "message")
			if known != tc.known || (err != nil) != tc.bad || known && v != 0.012345 {
				t.Fatalf("charge=%v known=%v err=%v", v, known, err)
			}
		})
	}
}

func TestTraeWorkChatOnlyRecordsSettledCharge(t *testing.T) {
	for _, tc := range []struct {
		name         string
		feeResponses []string
		wantCharge   float64
	}{
		{"延迟结算", []string{`{"user_usage_group_by_session":{"session_id":"message","credits_float":0}}`, `{"user_usage_group_by_session":{"session_id":"message","credits_float":0.25}}`}, 0.25},
		{"仍未结算", []string{`{}`, `{"user_usage_group_by_session":{"session_id":"message","credits_float":0}}`}, 0},
		{"归属错误", []string{`{"user_usage_group_by_session":{"session_id":"other","credits_float":0.25}}`}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var queries atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/remote/v1/chat_sessions":
					fmt.Fprint(w, `{"data":{"chat_session_id":"chat","message_id":"message"}}`)
				case "/api/remote/v1/chat_sessions/chat/events":
					fmt.Fprint(w, "event: output\ndata: {\"response\":\"hello\",\"usage\":{\"credit\":1}}\n\n")
					fmt.Fprint(w, "event: token_usage\ndata: {\"prompt_tokens\":4,\"completion_tokens\":2,\"credit\":\"unverified\"}\n\nevent: done\ndata: {}\n\n")
				case "/api/v1/commercial/get_session_usage":
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["session_id"] != "message" {
						t.Errorf("费用查询偏离原消息: %v", err)
					}
					index := int(queries.Add(1)) - 1
					if index >= len(tc.feeResponses) {
						t.Error("单消息费用查询次数超限")
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					fmt.Fprint(w, tc.feeResponses[index])
				default:
					t.Error("错误路径", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			adapter := Trae{HTTP: server.Client(), WorkBase: server.URL}
			meter := requestMeter{target: "traework:cn"}
			var chargeFrames int
			var tokensKnown bool
			err := adapter.Chat(context.Background(), "traework:cn", Credential{AccessToken: "fixture"}, Model{ID: "model", Native: map[string]any{"config_source": 1.0}}, []byte(plainChat), "session", func(frame map[string]any) error {
				u := object(frame["usage"])
				if _, ok := u["credit"]; ok {
					chargeFrames++
				}
				tokensKnown = tokensKnown || u["prompt_tokens"] == 4.0 && u["completion_tokens"] == 2.0
				return meter.observe(u)
			})
			charge, known := meter.charge()
			wantKnown := tc.wantCharge > 0
			wantChargeFrames := 0
			if wantKnown {
				wantChargeFrames = 1
			}
			if err != nil || !tokensKnown || charge != tc.wantCharge || known != wantKnown || chargeFrames != wantChargeFrames || int(queries.Load()) != len(tc.feeResponses) {
				t.Fatalf("扣费=%v 已知=%v 费用帧=%d 查询=%d Token已知=%v 错误=%v", charge, known, chargeFrames, queries.Load(), tokensKnown, err)
			}
		})
	}
}

func TestTraeWorkCancelledStreamStillRecordsCharge(t *testing.T) {
	var queries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/remote/v1/chat_sessions":
			fmt.Fprint(w, `{"code":0,"data":{"chat_session_id":"chat","message_id":"message"}}`)
		case "/api/remote/v1/chat_sessions/chat/events":
			fmt.Fprint(w, "event: output\ndata: {\"response\":\"hello\"}\n\nevent: done\ndata: {}\n\n")
		case "/api/v1/commercial/get_session_usage":
			queries.Add(1)
			fmt.Fprint(w, `{"user_usage_group_by_session":{"session_id":"message","credits_float":0.25}}`)
		default:
			t.Error("错误路径", r.URL.Path)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := Trae{HTTP: server.Client(), WorkBase: server.URL}
	known := false
	err := a.Chat(ctx, "traework:cn", Credential{AccessToken: "fixture"}, Model{ID: "model", Native: map[string]any{"config_source": 1.0}}, []byte(plainChat), "chat", func(frame map[string]any) error {
		if u := object(frame["usage"]); u != nil && u["credit"] == 0.25 {
			known = true
			return context.Canceled
		}
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) || !known || queries.Load() != 1 {
		t.Fatalf("取消丢失费用 known=%v queries=%v err=%v", known, queries.Load(), err)
	}
}

func TestBillingDoesNotLeakAcrossRequestsKeysOrRetry(t *testing.T) {
	m, f := testManager(t, "")
	importFake(t, m, "qoder:cn", "first")
	importFake(t, m, "qoder:cn", "second")
	ledger := usage.New("")
	m.SetUsage(ledger)
	calls := 0
	f.chat = func(_ context.Context, _ string, _ Credential, _ string, emit func(map[string]any) error) error {
		calls++
		if calls == 1 {
			return &Error{Status: 429, Retry: true}
		}
		u := map[string]any{"prompt_tokens": 1.0, "completion_tokens": 1.0, "credits": 0.125}
		if calls == 2 {
			u["billable"] = true
		}
		return emit(chunk("model", map[string]any{}, "stop", u))
	}
	req := Request{Target: "qoder:cn", Model: "model", KeyID: "first-key", Body: []byte(plainChat), MaxRotate: 2}
	result, err := m.Execute(context.Background(), req, discardFrame)
	if err != nil || result.Attempts != 2 || !result.ChargeKnown || result.Charge != 0.125 {
		t.Fatalf("重试计费=%+v %v", result, err)
	}
	req.KeyID = "second-key"
	result, err = m.Execute(context.Background(), req, discardFrame)
	if err != nil || result.ChargeKnown {
		t.Fatal("新请求继承旧请求的 billable", result, err)
	}
	one := ledger.SnapshotWindow(usage.Window{Key: "first-key", KeySet: true}, nil, nil)
	two := ledger.SnapshotWindow(usage.Window{Key: "second-key", KeySet: true}, nil, nil)
	if one.Totals.Requests != 2 || one.Totals.Errors != 1 || one.Totals.Credits != 0.125 || one.Totals.CreditSamples != 1 || two.Totals.Credits != 0 || two.Totals.CreditSamples != 0 || two.Totals.Requests != 1 {
		t.Fatal("重试或密钥计费隔离错误", one.Totals, two.Totals)
	}
}

func TestTraeWorkPreStreamFailureDoesNotStartOrEmitBillingFrame(t *testing.T) {
	var sessions, feeQueries atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/remote/v1/models":
			fmt.Fprint(w, `{"code":0,"data":{"list":[{"function":"solo_work_remote","models":[{"name":"model","config_source":1,"is_preset":true}]}]}}`)
		case "/api/remote/v1/chat_sessions":
			sessions.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"chat_session_id":"chat","message_id":"msg-1"}}`)
		case "/api/remote/v1/chat_sessions/chat/events":
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"code":500,"message":"pre-stream failure"}`)
		case "/api/v1/commercial/get_session_usage":
			feeQueries.Add(1)
			fmt.Fprint(w, `{"user_usage_group_by_session":{"session_id":"msg-1","credits_float":0.25}}`)
		default:
			t.Errorf("意外路径: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	m, _ := testManager(t, "")
	ledger := usage.New("")
	m.SetUsage(ledger)
	m.adapters["traework"] = &Trae{HTTP: srv.Client(), WorkBase: srv.URL}
	id := importFake(t, m, "traework:cn", "fixture-1")
	importFake(t, m, "traework:cn", "fixture-2")

	emitted := 0
	res, err := m.Execute(t.Context(), Request{Target: "traework:cn", Model: "model", KeyID: "k1", MaxRotate: 2, Body: []byte(plainChat)}, func(map[string]any) error {
		emitted++
		return nil
	})
	if err == nil {
		t.Fatal("前置 500 失败却返回成功")
	}
	if sessions.Load() != 1 || res.Attempts != 1 {
		t.Fatalf("已创建会话后发生换号重放: sessions=%d attempts=%d err=%v", sessions.Load(), res.Attempts, err)
	}
	if res.Started || emitted != 0 {
		t.Fatalf("纯内部计费帧污染了 Started 或下发给了 emit: started=%v emitted=%d", res.Started, emitted)
	}
	if !res.ChargeKnown || res.Charge != 0.25 || res.Unit != "credits" || feeQueries.Load() != 1 {
		t.Fatalf("前置失败丢失已结算扣费: res=%+v feeQueries=%d", res, feeQueries.Load())
	}
	snap := ledger.SnapshotWindow(usage.Window{Target: "traework:cn", Key: "k1", KeySet: true}, nil, nil)
	if snap.Totals.Credits != 0.25 || snap.Totals.CreditSamples != 1 || len(snap.ByAccount) != 1 || snap.ByAccount[0].Key != id {
		t.Fatalf("账本未记入前置失败扣费或出现重复扣费: %+v", snap)
	}
}
