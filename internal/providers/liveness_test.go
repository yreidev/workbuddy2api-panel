// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLivenessPinsAccountAndLeavesBindingsAndManualDisable(t *testing.T) {
	m, f := testManager(t, filepath.Join(t.TempDir(), "providers.json"))
	other := importFake(t, m, "qoder:cn", "other")
	id := importFake(t, m, "qoder:cn", "pinned")
	m.data.Accounts[1].Disabled = true
	m.data.Accounts[1].Priority = 99
	m.data.Accounts[1].Credential.ExpiresAt = 1
	m.running[id].coolUntil = time.Now().Add(time.Hour)
	m.data.Bindings["existing"] = Binding{AccountID: other, SessionID: "existing-session", ExpiresAt: time.Now().Add(time.Hour)}
	bindings := map[string]Binding{"existing": m.data.Bindings["existing"]}
	var called []string
	f.chat = func(_ context.Context, target string, c Credential, session string, emit func(map[string]any) error) error {
		called = append(called, c.UID)
		if target != "qoder:cn" || session == "existing-session" {
			t.Errorf("测活串台或借用会话: target=%s session=%s", target, session)
		}
		return emit(chunk("model", map[string]any{"content": "hi"}, "stop", nil))
	}
	result, err := m.Execute(context.Background(), Request{Target: "qoder:cn", Model: "model", KeyID: "liveness", TestAccountID: id, Session: "ignored", MaxRotate: 9, Body: []byte(plainChat)}, discardFrame)
	if err != nil || result.AccountID != id || result.Attempts != 1 || !reflect.DeepEqual(called, []string{"pinned"}) {
		t.Fatalf("固定账号测活失败: result=%+v called=%v err=%v", result, called, err)
	}
	if !m.data.Accounts[1].Disabled || !m.running[id].coolUntil.IsZero() || !reflect.DeepEqual(m.data.Bindings, bindings) || f.refreshed.Load() != 1 {
		t.Fatal("测活未走凭证刷新、解除冷却，或改变人工停用及会话")
	}
	if u := m.Usage(); len(u) != 1 || u[0].KeyID != "liveness" || u[0].AccountID != id {
		t.Fatalf("测活用量归属错误: %+v", u)
	}
}

func TestLivenessFailureNeverRotates(t *testing.T) {
	m, f := testManager(t, "")
	id := importFake(t, m, "qoder:cn", "pinned")
	importFake(t, m, "qoder:cn", "other")
	calls := 0
	f.chat = func(_ context.Context, _ string, c Credential, _ string, _ func(map[string]any) error) error {
		calls++
		if c.UID != "pinned" {
			t.Errorf("测活换了账号: %s", c.UID)
		}
		return &Error{Status: 429, Code: "rate_limit", Retry: true}
	}
	result, err := m.Execute(context.Background(), Request{Target: "qoder:cn", Model: "model", TestAccountID: id, Body: []byte(plainChat)}, discardFrame)
	if err == nil || calls != 1 || result.Attempts != 1 || m.running[id].inFlight != 0 || !time.Now().Before(m.running[id].coolUntil) {
		t.Fatalf("失败测活轮换、漏冷却或未归还槽位: result=%+v calls=%d err=%v", result, calls, err)
	}
}

func TestLivenessRespectsBusyGuardScopeAndCancellation(t *testing.T) {
	for _, scenario := range []string{"busy", "guard", "missing", "wrong-target", "permission", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := testManager(t, "")
			id := importFake(t, m, "qoder:cn", "pinned")
			calls := 0
			f.chat = func(context.Context, string, Credential, string, func(map[string]any) error) error {
				calls++
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := Request{Target: "qoder:cn", Model: "model", TestAccountID: id, Body: []byte(plainChat)}
			switch scenario {
			case "busy":
				m.running[id].inFlight = 1
			case "guard":
				m.running[id].blocked = true
			case "missing":
				req.TestAccountID = "missing"
			case "wrong-target":
				req.Target = "qoder:global"
			case "permission":
				req.Allow = func(string, []string) bool { return false }
			case "cancelled":
				cancel()
			}
			result, err := m.Execute(ctx, req, discardFrame)
			if err == nil || calls != 0 || result.Attempts != 0 || f.refreshed.Load() != 0 {
				t.Fatalf("不应发出测活: result=%+v calls=%d err=%v", result, calls, err)
			}
			if scenario == "busy" {
				var e *Error
				if !errors.As(err, &e) || e.Status != 409 {
					t.Fatalf("忙碌状态错误: %v", err)
				}
			}
		})
	}
}

func TestRecordProviderLivenessPersistenceAndSaveFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	m, _ := testManager(t, path)
	id := importFake(t, m, "qoder:cn", "pinned")
	result := LivenessResult{At: time.Now().UTC().Truncate(time.Second), OK: true, Model: "qoder:cn:model", Status: 200, TTFBMs: 12, DurationMs: 20, TokensKnown: true, PromptTokens: 2, CompletionTokens: 1}
	if err := m.RecordLiveness(id, result); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, nil)
	if err != nil || !reflect.DeepEqual(reopened.List()[0].LastLiveness, &result) {
		t.Fatalf("上次测活未持久化: %v", err)
	}
	m.path = t.TempDir()
	failed := result
	failed.OK = false
	if err := m.RecordLiveness(id, failed); err == nil || !reflect.DeepEqual(m.List()[0].LastLiveness, &result) {
		t.Fatal("保存失败未报错或未保留旧结果")
	}
	if err := m.RecordLiveness("missing", result); err == nil {
		t.Fatal("不存在的账号接受了测活结果")
	}
}

func TestNormalChatClearsExpiredCooldown(t *testing.T) {
	m, _ := testManager(t, "")
	id := importFake(t, m, "qoder:cn", "fixture")
	m.data.Accounts[0].LivenessModel = "qoder:cn:model"
	m.running[id].coolUntil = time.Now().Add(-time.Minute)
	if _, err := m.Execute(t.Context(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame); err != nil {
		t.Fatal(err)
	}
	if !m.List()[0].CoolUntil.IsZero() {
		t.Fatal("普通对话已成功，账号仍等待定时测活")
	}
}
