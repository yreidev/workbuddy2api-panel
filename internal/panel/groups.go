// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号分组与优先级（见 pool/groups.go）：分组的增删改、号的分组和优先级。多选后批量加进、退出分组和设优先级在
// accounts_batch.go。客户端密钥限定分组在 keys.go 保存时查分组在不在。

package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yreidev/workbuddy2api-panel/internal/pool"
)

const (
	// groupNameMax、groupNoteMax 分组名字、备注最长几个字。
	groupNameMax = 20
	groupNoteMax = 100
)

// groupView 分组列表的一项：多带限定了这个分组的密钥名字。
type groupView struct {
	pool.GroupView
	Keys []string `json:"keys"`
}

// groupsList GET /panel/api/groups：全部分组，带组里几个号、哪些密钥限定了它。
func (p *Panel) groupsList(w http.ResponseWriter, _ *http.Request) {
	groups := p.cfg.Pool.Groups()
	out := make([]groupView, 0, len(groups))
	for _, g := range groups {
		out = append(out, groupView{GroupView: g, Keys: p.keysUsingGroup(g.ID)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

// groupCreate POST /panel/api/groups：建分组，请求体 {"name","note"}。
func (p *Panel) groupCreate(w http.ResponseWriter, r *http.Request) {
	name, note, ok := groupInput(w, r)
	if !ok {
		return
	}
	g, err := p.cfg.Pool.CreateGroup(name, note)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	log.Printf("panel: 建分组 %s（%s）", g.Name, g.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": g})
}

// groupUpdate POST /panel/api/groups/{id}：改名字和备注，请求体同建分组。
func (p *Panel) groupUpdate(w http.ResponseWriter, r *http.Request) {
	name, note, ok := groupInput(w, r)
	if !ok {
		return
	}
	g, err := p.cfg.Pool.UpdateGroup(r.PathValue("id"), name, note)
	switch {
	case errors.Is(err, pool.ErrGroupNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeErr(w, http.StatusConflict, err.Error())
	default:
		log.Printf("panel: 改分组 %s（%s）", g.Name, g.ID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": g})
	}
}

// groupDelete POST /panel/api/groups/{id}/delete：删分组，组里的号退出这个组。还有密钥限定了它时回 409，
// 免得那些密钥变成不限、什么号都能用。
func (p *Panel) groupDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if keys := p.keysUsingGroup(id); len(keys) > 0 {
		writeErr(w, http.StatusConflict, "这些密钥限定了这个分组，先在密钥里去掉它："+strings.Join(keys, "、"))
		return
	}
	if err := p.cfg.Pool.DeleteGroup(id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	log.Printf("panel: 删分组 %s", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// accountGroups POST /panel/api/accounts/{uid}/groups：整份替换号所在的分组，请求体 {"groups":["分组 id"]}。
func (p *Panel) accountGroups(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	var body struct {
		Groups []string `json:"groups"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体要有 groups 列表")
		return
	}
	var ids []string
	for _, id := range body.Groups {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	found, err := p.cfg.Pool.SetAccountGroups(uid, ids)
	switch {
	case !found:
		writeErr(w, http.StatusNotFound, errAccountNotFound.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("panel: 号 uid=%s 的分组改成 %v", uid, ids)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "groups": ids})
	}
}

// accountPriority POST /panel/api/accounts/{uid}/priority：改号的优先级，请求体 {"priority":0}，0～99，越小越先用。
func (p *Panel) accountPriority(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	n, ok := priorityInput(w, r)
	if !ok {
		return
	}
	if !p.cfg.Pool.SetPriority(uid, n) {
		writeErr(w, http.StatusNotFound, errAccountNotFound.Error())
		return
	}
	log.Printf("panel: 号 uid=%s 的优先级改成 %d", uid, n)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "priority": n})
}

// keysUsingGroup 限定了这个分组的密钥名字；没有密钥库时为空。
func (p *Panel) keysUsingGroup(id string) []string {
	if p.cfg.Keys == nil {
		return []string{}
	}
	return append([]string{}, p.cfg.Keys.UsingGroup(id)...)
}

// checkGroups 密钥限定的分组都在不在，不在的报错。
func (p *Panel) checkGroups(ids []string) error {
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !p.cfg.Pool.HasGroup(id) {
			return fmt.Errorf("%w：%s", pool.ErrGroupNotFound, id)
		}
	}
	return nil
}

// groupInput 读建、改分组的请求体：名字去掉首尾空白后 1～20 个字，备注最多 100 个字，都不能有换行等控制字符。
func groupInput(w http.ResponseWriter, r *http.Request) (name, note string, ok bool) {
	var body struct {
		Name string `json:"name"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对："+err.Error())
		return "", "", false
	}
	name, note = strings.TrimSpace(body.Name), strings.TrimSpace(body.Note)
	switch {
	case name == "":
		writeErr(w, http.StatusBadRequest, "分组名字不能为空")
	case utf8.RuneCountInString(name) > groupNameMax:
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("分组名字最多 %d 个字", groupNameMax))
	case utf8.RuneCountInString(note) > groupNoteMax:
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("分组备注最多 %d 个字", groupNoteMax))
	case strings.IndexFunc(name+note, unicode.IsControl) >= 0:
		writeErr(w, http.StatusBadRequest, "分组名字和备注不能有换行或控制字符")
	default:
		return name, note, true
	}
	return "", "", false
}

// priorityInput 读 {"priority":n}，n 要在 0～pool.MaxPriority。
func priorityInput(w http.ResponseWriter, r *http.Request) (int, bool) {
	var body struct {
		Priority *int `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Priority == nil {
		writeErr(w, http.StatusBadRequest, "请求体要有 priority 数字")
		return 0, false
	}
	if n := *body.Priority; n < 0 || n > pool.MaxPriority {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("优先级要在 0～%d 之间", pool.MaxPriority))
		return 0, false
	}
	return *body.Priority, true
}
