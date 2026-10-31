// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestRecordLivenessPersists 上次测活存进 state.json：Status 带出来、重启后还在；未知 uid 返回 false。
func TestRecordLivenessPersists(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "state.json")
	p := New(fp)
	p.Add(&auth.Auth{UID: "u1"})
	if st, _ := p.Status("u1"); st.LastLiveness != nil {
		t.Fatalf("没测过不应有上次测活: %+v", st.LastLiveness)
	}
	want := LivenessResult{
		At: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), OK: true, Model: "glm-5.2", Status: 200,
		TTFBMs: 800, DurationMs: 1200, Credit: 0.5, Scheduled: true, Recovered: true,
	}
	if !p.RecordLiveness("u1", want) || p.RecordLiveness("no-such", want) {
		t.Fatal("已有账号应记成功，未知 uid 应返回 false")
	}
	if !p.dirty.Load() {
		t.Fatal("记下测活应标记要落盘")
	}
	p.Flush()
	p2 := New(fp)
	p2.Add(&auth.Auth{UID: "u1"})
	st, _ := p2.Status("u1")
	if st.LastLiveness == nil || !reflect.DeepEqual(*st.LastLiveness, want) {
		t.Fatalf("重启后上次测活 = %+v，应为 %+v", st.LastLiveness, want)
	}
}

// TestRecoverAfterLiveness 测通后的恢复：网关自动加的禁用、账号级冷却、测的那个模型的冷却都解除；
// 人工禁用、暂停选号、风控波整域暂停不动；没什么可解除时返回 false。
func TestRecoverAfterLiveness(t *testing.T) {
	future := time.Now().Add(time.Hour)
	setup := func(f func(e *entry)) (*Pool, *entry) {
		p := New("")
		p.Add(&auth.Auth{UID: "u1"})
		e := p.byUID["u1"]
		f(e)
		p.dirty.Store(false)
		return p, e
	}

	t.Run("未知号", func(t *testing.T) {
		if New("").RecoverAfterLiveness("no-such", "m") {
			t.Fatal("未知号应返回 false")
		}
	})

	t.Run("没什么可解除", func(t *testing.T) {
		p, _ := setup(func(*entry) {})
		if p.RecoverAfterLiveness("u1", "m") || p.dirty.Load() {
			t.Fatal("健康的号不应返回 true，也不应标记落盘")
		}
	})

	t.Run("网关自动禁用", func(t *testing.T) {
		p, e := setup(func(e *entry) {
			e.disabled, e.reason, e.sessionDeadFails = true, "session dead x3", 3
		})
		if !p.RecoverAfterLiveness("u1", "m") || !p.dirty.Load() {
			t.Fatal("自动禁用应解除并落盘")
		}
		if e.disabled || e.reason != "" || e.sessionDeadFails != 0 {
			t.Fatalf("解除后仍有禁用状态: disabled=%v reason=%q fails=%d", e.disabled, e.reason, e.sessionDeadFails)
		}
	})

	t.Run("人工禁用和暂停选号不动", func(t *testing.T) {
		p, e := setup(func(e *entry) {
			e.disabled, e.reason, e.paused = true, ManualDisableReason, true
		})
		if p.RecoverAfterLiveness("u1", "m") {
			t.Fatal("人工禁用不应解除")
		}
		if !e.disabled || e.reason != ManualDisableReason || !e.paused {
			t.Fatalf("人工禁用、暂停选号被动了: disabled=%v reason=%q paused=%v", e.disabled, e.reason, e.paused)
		}
	})

	for _, kind := range []CoolKind{CoolSoft, CoolHard} {
		t.Run("账号级冷却 "+kind.String(), func(t *testing.T) {
			p, e := setup(func(e *entry) {
				e.until, e.coolKind, e.reason, e.softStreak = future, kind, "429", 2
			})
			if !p.RecoverAfterLiveness("u1", "m") {
				t.Fatal("冷却应解除")
			}
			if !e.until.IsZero() || e.reason != "" || e.softStreak != 0 {
				t.Fatalf("解除后仍在冷却: until=%v reason=%q streak=%d", e.until, e.reason, e.softStreak)
			}
		})
	}

	t.Run("风控波整域暂停不动", func(t *testing.T) {
		p, e := setup(func(e *entry) {
			e.until, e.coolKind, e.reason = future, CoolPause, "wave"
		})
		if p.RecoverAfterLiveness("u1", "m") {
			t.Fatal("整域暂停不应因一个号测通就解除")
		}
		if !e.until.Equal(future) || e.reason != "wave" {
			t.Fatalf("整域暂停被动了: until=%v reason=%q", e.until, e.reason)
		}
	})

	t.Run("模型级冷却只解测的那个", func(t *testing.T) {
		p, e := setup(func(e *entry) {
			e.modelCooldowns = map[string]modelCooldown{"m": {Until: future}, "other": {Until: future}}
		})
		if !p.RecoverAfterLiveness("u1", "m") {
			t.Fatal("测的模型的冷却应解除")
		}
		if _, ok := e.modelCooldowns["m"]; ok {
			t.Fatal("测的模型的冷却应删掉")
		}
		if _, ok := e.modelCooldowns["other"]; !ok {
			t.Fatal("别的模型的冷却不应动")
		}
	})

	t.Run("过期的模型级冷却删掉但不算解除", func(t *testing.T) {
		p, e := setup(func(e *entry) {
			e.modelCooldowns = map[string]modelCooldown{"m": {Until: time.Now().Add(-time.Minute)}}
		})
		if p.RecoverAfterLiveness("u1", "m") {
			t.Fatal("过期的冷却不算解除")
		}
		if _, ok := e.modelCooldowns["m"]; ok {
			t.Fatal("过期的冷却也应顺手删掉")
		}
	})
}

// TestLivenessDue 定时测活只挑整个号在限流冷却、熔断或连败降权中的号，按 UID 排序；
// 余额不足、风控波暂停、只是模型级限额、禁用、暂停选号的不挑。
func TestLivenessDue(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	p := New("")
	cases := map[string]func(e *entry){
		"a-ok":       func(*entry) {},
		"b-soft":     func(e *entry) { e.until, e.coolKind = future, CoolSoft },
		"c-hard":     func(e *entry) { e.until, e.coolKind = future, CoolHard },
		"d-pause":    func(e *entry) { e.until, e.coolKind = future, CoolPause },
		"e-breaker":  func(e *entry) { e.breakerUntil = future },
		"f-degrade":  func(e *entry) { e.degradeUntil = future },
		"g-model":    func(e *entry) { e.modelCooldowns = map[string]modelCooldown{"m": {Until: future}} },
		"h-disabled": func(e *entry) { e.disabled = true; e.breakerUntil = future },
		"i-paused":   func(e *entry) { e.paused = true; e.breakerUntil = future },
		"j-expired": func(e *entry) {
			e.until, e.coolKind, e.breakerUntil = now.Add(-time.Minute), CoolSoft, now.Add(-time.Minute)
		},
	}
	for uid, f := range cases {
		p.Add(&auth.Auth{UID: uid})
		f(p.byUID[uid])
	}
	got := p.LivenessDue(now)
	want := []string{"b-soft", "e-breaker", "f-degrade"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LivenessDue = %v，应为 %v", got, want)
	}
}

// TestDisableKeepsManual 人工禁用优先：网关的自动禁用（12153、11140、连续登录失效）不改掉人工禁用的原因，
// 免得之后测通被当成自动禁用解除；人工禁用可以盖过自动禁用。
func TestDisableKeepsManual(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.Disable("u1", ManualDisableReason)
	p.Disable("u1", "12153 session dead")
	for range sessionDeadThreshold {
		p.NoteSessionDead("u1")
	}
	if st, _ := p.Status("u1"); !st.Disabled || st.DisabledReason != ManualDisableReason {
		t.Fatalf("人工禁用的原因被改了: %+v", st)
	}
	if p.RecoverAfterLiveness("u1", "m") {
		t.Fatal("人工禁用的号测通也不解除")
	}
	p.Disable("u2", "12153 session dead")
	p.Disable("u2", ManualDisableReason)
	if st, _ := p.Status("u2"); st.DisabledReason != ManualDisableReason {
		t.Fatalf("人工禁用应盖过自动禁用: %q", st.DisabledReason)
	}
}
