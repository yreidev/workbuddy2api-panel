// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/scheduler"
)

// TestAccountsBatchRequest 请求体不对回 400：坏 JSON、uids 为空、不认识的动作。
func TestAccountsBatchRequest(t *testing.T) {
	p, _ := newPanelFor(t, Config{})
	for _, body := range []string{`{`, `{"action":"disable","uids":[]}`, `{"action":"checkin","uids":["u1"]}`} {
		autoWantCode(t, postJSON(t, p, "/panel/api/accounts/batch", body), http.StatusBadRequest)
	}
}

// TestAccountsBatchActions 逐号执行，重复 uid 只做一次，不在池里的号列进 failed、不影响其余的号。
func TestAccountsBatchActions(t *testing.T) {
	p, _ := newPanelFor(t, Config{})
	for _, uid := range []string{"u1", "u2", "u3"} {
		p.cfg.Pool.Add(taskTestAuth(uid, uid))
	}
	batch := func(action, uids string) map[string]any {
		t.Helper()
		rec := postJSON(t, p, "/panel/api/accounts/batch", `{"action":"`+action+`","uids":`+uids+`}`)
		autoWantCode(t, rec, http.StatusOK)
		return autoDecode(t, rec)
	}

	resp := batch("disable", `["u1","u2","u1","nope"]`)
	failed, _ := resp["failed"].([]any)
	if resp["ok"] != float64(2) || len(failed) != 1 || failed[0].(map[string]any)["uid"] != "nope" {
		t.Fatalf("disable resp = %v", resp)
	}
	if !mustStatus(t, p, "u1").Disabled || !mustStatus(t, p, "u2").Disabled || mustStatus(t, p, "u3").Disabled {
		t.Fatal("应只禁用 u1、u2")
	}
	if resp := batch("revive", `["u1","u2"]`); resp["ok"] != float64(2) || mustStatus(t, p, "u1").Disabled {
		t.Fatalf("revive resp = %v", resp)
	}
	if resp := batch("pause", `["u2","u3"]`); resp["ok"] != float64(2) || !mustStatus(t, p, "u3").Paused {
		t.Fatalf("pause resp = %v", resp)
	}
	if resp := batch("resume", `["u2","u3"]`); resp["ok"] != float64(2) || mustStatus(t, p, "u3").Paused {
		t.Fatalf("resume resp = %v", resp)
	}
}

// TestAccountsBatchRemove 批量移除：出池并删凭证文件；文件删不掉的号照样出池，列进 file_errors。
func TestAccountsBatchRemove(t *testing.T) {
	p, _ := newPanelFor(t, Config{})
	dir := t.TempDir()
	good := filepath.Join(dir, "workbuddy-u1.json")
	if err := os.WriteFile(good, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "not-empty")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a1, a2 := taskTestAuth("u1", "一号"), taskTestAuth("u2", "二号")
	a1.FilePath, a2.FilePath = good, bad
	p.cfg.Pool.Add(a1)
	p.cfg.Pool.Add(a2)
	p.cfg.Pool.Add(taskTestAuth("u3", "三号"))

	rec := postJSON(t, p, "/panel/api/accounts/batch", `{"action":"remove","uids":["u1","u2"]}`)
	autoWantCode(t, rec, http.StatusOK)
	resp := autoDecode(t, rec)
	fileErrs, _ := resp["file_errors"].([]any)
	if resp["ok"] != float64(2) || len(fileErrs) != 1 || fileErrs[0].(map[string]any)["uid"] != "u2" {
		t.Fatalf("remove resp = %v", resp)
	}
	if p.cfg.Pool.AuthByUID("u1") != nil || p.cfg.Pool.AuthByUID("u2") != nil || p.cfg.Pool.AuthByUID("u3") == nil {
		t.Fatal("应只移除 u1、u2")
	}
	if _, err := os.Stat(good); !os.IsNotExist(err) {
		t.Fatalf("u1 的凭证文件应被删除，stat err=%v", err)
	}
}

// TestAccountsBatchLiveness 批量测活：没有调度器或没接上测活 501，开始了回要测的号数，已有一批在跑 409。
func TestAccountsBatchLiveness(t *testing.T) {
	p, _ := newPanelFor(t, Config{})
	const body = `{"action":"liveness","uids":["u1","u2"]}`
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/batch", body), http.StatusNotImplemented)
	p.cfg.Pool.Add(&auth.Auth{UID: "u1"})
	p.cfg.Pool.Add(&auth.Auth{UID: "u2"})
	p.cfg.Pool.Add(&auth.Auth{UID: "u3"})
	p.cfg.Scheduler = scheduler.New(scheduler.Config{Pool: p.cfg.Pool})
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/batch", body), http.StatusNotImplemented)

	gate := make(chan struct{})
	done := make(chan string, 2)
	p.cfg.Scheduler.SetLiveness(func(_ context.Context, uid, _ string, _ bool) (pool.LivenessResult, string, error) {
		<-gate
		done <- uid
		return pool.LivenessResult{OK: true}, "", nil
	})
	rec := postJSON(t, p, "/panel/api/accounts/batch", body)
	autoWantCode(t, rec, http.StatusOK)
	if resp := autoDecode(t, rec); resp["started"] != true || resp["total"] != float64(2) {
		t.Fatalf("resp = %v", resp)
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/batch", body), http.StatusConflict)
	close(gate)
	got := map[string]bool{<-done: true, <-done: true}
	if !got["u1"] || !got["u2"] {
		t.Fatalf("应只测选中的 u1、u2，实际 %v", got)
	}
}
