// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 测活接口：单号测活（等结果，回上游说了什么）和全部测活（后台跑，结果看各号卡片上的「上次测活」）。
// 测活本身见 server.Handler.TestAccount，排程见 scheduler/liveness.go。

package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/yreidev/workbuddy2api-panel/internal/scheduler"
)

// accountLiveness 测活一个号：给它发一条最短的真实对话。请求体可带 model，空 = 挑这个号能接的最便宜的模型。
// 关掉弹窗也测完，结果照样记进「上次测活」。
func (p *Panel) accountLiveness(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if _, ok := p.cfg.Pool.Status(uid); !ok {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	if p.cfg.Scheduler == nil {
		writeErr(w, http.StatusNotImplemented, "scheduler not available")
		return
	}
	var in struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	res, reply, err := p.cfg.Scheduler.TestAccount(context.WithoutCancel(r.Context()), uid, strings.TrimSpace(in.Model))
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "reply": reply})
}

// livenessAll 全部测活：后台测所有没被人工禁用的号，已有一批在跑时回 409。
func (p *Panel) livenessAll(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Scheduler == nil {
		writeErr(w, http.StatusNotImplemented, "scheduler not available")
		return
	}
	n, err := p.cfg.Scheduler.StartLivenessAll()
	switch {
	case errors.Is(err, scheduler.ErrLivenessBusy):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusNotImplemented, err.Error())
	default:
		log.Printf("panel: 手动全部测活已触发（%d 个号）", n)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": true, "total": n})
	}
}
