// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/scheduler"
)

// TestAccountLiveness 单号测活接口：未知号 404、没有调度器 501、坏 JSON 400、测不了 422；
// 能测时回结果和上游回复。模型去掉首尾空白，空 = 自动挑；关掉弹窗（请求取消）也测完。
func TestAccountLiveness(t *testing.T) {
	p, _ := newPanelFor(t, Config{})
	p.cfg.Pool.Add(&auth.Auth{UID: "u1"})
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/nope/liveness", ``), http.StatusNotFound)
	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/liveness", ``), http.StatusNotImplemented)

	var mu sync.Mutex
	var models []string
	var cancellable bool
	p.cfg.Scheduler = scheduler.New(scheduler.Config{Pool: p.cfg.Pool})
	p.cfg.Scheduler.SetLiveness(func(ctx context.Context, uid, model string, scheduled bool) (pool.LivenessResult, string, error) {
		mu.Lock()
		defer mu.Unlock()
		models = append(models, model)
		cancellable = cancellable || ctx.Done() != nil || scheduled
		if model == "bad" {
			return pool.LivenessResult{}, "", errors.New("account u1: 模型目录还没拉到，指定一个模型再测")
		}
		return pool.LivenessResult{At: time.Now(), OK: true, Model: "m-cheap", Status: 200}, "你好", nil
	})

	autoWantCode(t, postJSON(t, p, "/panel/api/accounts/u1/liveness", `{bad`), http.StatusBadRequest)
	rec := postJSON(t, p, "/panel/api/accounts/u1/liveness", `{"model":"bad"}`)
	autoWantCode(t, rec, http.StatusUnprocessableEntity)
	if resp := autoDecode(t, rec); resp["error"] != "account u1: 模型目录还没拉到，指定一个模型再测" {
		t.Fatalf("422 的错误 = %v", resp)
	}

	for _, body := range []string{``, `{"model":"  glm-5.2 "}`} {
		rec = postJSON(t, p, "/panel/api/accounts/u1/liveness", body)
		autoWantCode(t, rec, http.StatusOK)
		var out struct {
			Result pool.LivenessResult `json:"result"`
			Reply  string              `json:"reply"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.Result.OK || out.Result.Model != "m-cheap" || out.Reply != "你好" {
			t.Fatalf("body=%s err=%v", rec.Body, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(models) != 3 || models[1] != "" || models[2] != "glm-5.2" {
		t.Fatalf("传给测活的模型 = %q", models)
	}
	if cancellable {
		t.Fatal("单测应不随请求取消、不算定时测活")
	}
}

// TestLivenessAll 全部测活接口：没有调度器或没接上测活 501，开始了回要测的号数，已有一批在跑 409。
func TestLivenessAll(t *testing.T) {
	p, _ := newPanelFor(t, Config{})
	autoWantCode(t, postJSON(t, p, "/panel/api/liveness_all", ``), http.StatusNotImplemented)
	p.cfg.Pool.Add(&auth.Auth{UID: "u1"})
	p.cfg.Pool.Add(&auth.Auth{UID: "u2"})
	p.cfg.Scheduler = scheduler.New(scheduler.Config{Pool: p.cfg.Pool})
	autoWantCode(t, postJSON(t, p, "/panel/api/liveness_all", ``), http.StatusNotImplemented)

	gate := make(chan struct{})
	done := make(chan string, 2)
	p.cfg.Scheduler.SetLiveness(func(_ context.Context, uid, _ string, _ bool) (pool.LivenessResult, string, error) {
		<-gate
		done <- uid
		return pool.LivenessResult{OK: true}, "", nil
	})
	rec := postJSON(t, p, "/panel/api/liveness_all", ``)
	autoWantCode(t, rec, http.StatusOK)
	if resp := autoDecode(t, rec); resp["ok"] != true || resp["started"] != true || resp["total"] != float64(2) {
		t.Fatalf("resp = %v", resp)
	}
	rec = postJSON(t, p, "/panel/api/liveness_all", ``)
	autoWantCode(t, rec, http.StatusConflict)
	if resp := autoDecode(t, rec); resp["error"] != scheduler.ErrLivenessBusy.Error() {
		t.Fatalf("409 的错误 = %v", resp)
	}
	close(gate)
	for range 2 {
		<-done
	}
}
