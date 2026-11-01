// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号运维动作（解冻、禁用、暂停选号、恢复选号、移除）：单号接口和账号表多选后的批量接口共用一份做法。

package panel

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"

	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/scheduler"
)

// errAccountNotFound 号不在池里。
var errAccountNotFound = errors.New("account not found")

// accountActions 单号、批量都认的动作；批量另外认 liveness（测活，后台跑）。
var accountActions = map[string]bool{"revive": true, "disable": true, "pause": true, "resume": true, "remove": true}

// doAccountAction 对一个号执行运维动作。号不在池里返回 errAccountNotFound；移除时凭证文件删不掉不算失败，
// 原因放在 fileErr（号已经出池）。
func (p *Panel) doAccountAction(action, uid string) (fileErr string, err error) {
	switch action {
	case "revive":
		// 手动复活：清禁用 + 冷却 + 熔断（运维口径无条件恢复）。
		if _, ok := p.cfg.Pool.Status(uid); !ok {
			return "", errAccountNotFound
		}
		p.cfg.Pool.Revive(uid)
		log.Printf("panel: revive uid=%s（人工清除禁用/冷却/熔断）", uid)
	case "disable":
		// 人工禁用（不再参与选号，需面板 revive 或重登恢复）。
		if _, ok := p.cfg.Pool.Status(uid); !ok {
			return "", errAccountNotFound
		}
		p.cfg.Pool.Disable(uid, pool.ManualDisableReason)
		log.Printf("panel: disable uid=%s（人工禁用）", uid)
	case "pause":
		// 暂停选号：账号退出选号候选，但**照常参与**签到 / 活跃上报 / 保活 / 余额刷新。与 disable 的区别：
		// 不写 reason、不清冷却域、不重置计数——账号是「临时让位」而非「判死」，点「恢复选号」即可立刻回到池子。
		if !p.cfg.Pool.Pause(uid) {
			return "", errAccountNotFound
		}
		log.Printf("panel: pause uid=%s（暂停选号，保号任务照常）", uid)
	case "resume":
		// 解除暂停选号（幂等，对未暂停账号为空操作）。
		if !p.cfg.Pool.Resume(uid) {
			return "", errAccountNotFound
		}
		log.Printf("panel: resume uid=%s（恢复参与选号）", uid)
	case "remove":
		// 移除账号：先出池（立即落盘 state），再删 auth 文件。
		a := p.cfg.Pool.Remove(uid)
		if a == nil {
			return "", errAccountNotFound
		}
		if p.cfg.Proxies != nil {
			p.cfg.Proxies.Untrack(uid) // 号走了，它占的代理空出来给等代理的号
		}
		if a.FilePath != "" {
			if err := os.Remove(a.FilePath); err != nil && !os.IsNotExist(err) {
				fileErr = err.Error()
			}
		}
		if fileErr != "" {
			log.Printf("panel: remove uid=%s（auth 文件删除失败: %s）", uid, fileErr)
		} else {
			log.Printf("panel: remove uid=%s（已出池并删除凭证文件）", uid)
		}
	}
	return fileErr, nil
}

// accountAction 单号接口：POST /panel/api/accounts/{uid}/<action>。
func (p *Panel) accountAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fileErr, err := p.doAccountAction(action, r.PathValue("uid"))
		switch {
		case err != nil:
			writeErr(w, http.StatusNotFound, err.Error())
		case fileErr != "":
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file_error": fileErr})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		}
	}
}

// batchFailure 批量动作里没做成的一个号。
type batchFailure struct {
	UID   string `json:"uid"`
	Error string `json:"error"`
}

// accountsBatch 账号表多选后的批量动作：逐号执行，一个号失败不影响其余的号。
//
//	POST /panel/api/accounts/batch
//	Body: {"action":"remove|disable|revive|pause|resume|liveness","uids":["..."]}
//
// 返回 {"ok":成功个数,"failed":[{uid,error}],"file_errors":[{uid,error}]}（重复 uid 只做一次）；file_errors 是
// 移除时凭证文件没删掉的号（号已出池，算成功）。liveness 不等结果：后台测选中的号，回
// {"started":true,"total":要测的号数}，已有一批测活在跑时回 409。
func (p *Panel) accountsBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string   `json:"action"`
		UIDs   []string `json:"uids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.UIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "请求体需要 action 和非空的 uids 列表")
		return
	}
	if body.Action == "liveness" {
		p.batchLiveness(w, body.UIDs)
		return
	}
	if !accountActions[body.Action] {
		writeErr(w, http.StatusBadRequest, "不认识的批量动作："+body.Action)
		return
	}
	ok := 0
	failed, fileErrs := []batchFailure{}, []batchFailure{}
	seen := make(map[string]bool, len(body.UIDs))
	for _, uid := range body.UIDs {
		if seen[uid] {
			continue
		}
		seen[uid] = true
		fileErr, err := p.doAccountAction(body.Action, uid)
		if err != nil {
			failed = append(failed, batchFailure{uid, err.Error()})
			continue
		}
		ok++
		if fileErr != "" {
			fileErrs = append(fileErrs, batchFailure{uid, fileErr})
		}
	}
	log.Printf("panel: 批量 %s：%d 个成功，%d 个失败", body.Action, ok, len(failed))
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "failed": failed, "file_errors": fileErrs})
}

// batchLiveness 批量测活：后台测选中的号（见 scheduler.StartLivenessFor）。
func (p *Panel) batchLiveness(w http.ResponseWriter, uids []string) {
	if p.cfg.Scheduler == nil {
		writeErr(w, http.StatusNotImplemented, "scheduler not available")
		return
	}
	n, err := p.cfg.Scheduler.StartLivenessFor(uids)
	switch {
	case errors.Is(err, scheduler.ErrLivenessBusy):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusNotImplemented, err.Error())
	default:
		log.Printf("panel: 批量测活已触发（%d 个号）", n)
		writeJSON(w, http.StatusOK, map[string]any{"started": true, "total": n})
	}
}
