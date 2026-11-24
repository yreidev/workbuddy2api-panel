// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/providers"
	"github.com/yreidev/workbuddy2api-panel/internal/targets"
)

func (p *Panel) registerProviderRoutes() {
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /panel/api/providers":                         p.providersList,
		"POST /panel/api/providers/batch":                  p.providerBatch,
		"POST /panel/api/providers/export":                 p.providerExport,
		"POST /panel/api/providers/import":                 p.providerImport,
		"POST /panel/api/providers/login":                  p.providerLogin,
		"POST /panel/api/providers/login/complete":         p.providerLoginComplete,
		"POST /panel/api/providers/accounts/{id}":          p.providerUpdate,
		"POST /panel/api/providers/accounts/{id}/delete":   p.providerDelete,
		"POST /panel/api/providers/accounts/{id}/models":   p.providerModels,
		"POST /panel/api/providers/accounts/{id}/liveness": p.providerLiveness,
		"POST /panel/api/providers/accounts/{id}/quota":    p.providerQuota,
	} {
		p.mux.HandleFunc(pattern, p.withAuth(func(w http.ResponseWriter, r *http.Request) {
			if p.cfg.Providers == nil {
				writeErr(w, 501, "平台账号管理未启用")
				return
			}
			handler(w, r)
		}))
	}
}

func (p *Panel) providerQuota(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Force bool `json:"force"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&in); err != nil {
		writeErr(w, 400, "额度查询请求格式错误")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, err := p.cfg.Providers.Quota(ctx, r.PathValue("id"), in.Force)
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, 404, "平台账号不存在")
		return
	}
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	// 上游查询失败也是一份有时间和错误的状态；上次成功结果随状态一起返回。
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

func (p *Panel) providerExport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil || len(in.IDs) == 0 {
		writeErr(w, 400, "请选择要导出的平台账号")
		return
	}
	accounts, err := p.cfg.Providers.Export(in.IDs)
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"accounts": accounts})
}

// providerBatch 逐账号报告结果，错误不会被汇总成成功；测活只使用用户保存的完整模型名。
func (p *Panel) providerBatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs      []string `json:"ids"`
		Action   string   `json:"action"`
		Group    string   `json:"group"`
		Priority int      `json:"priority"`
		Models   []string `json:"models"`
		ProxyID  string   `json:"proxy_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil || len(in.IDs) == 0 || len(in.IDs) > 1000 {
		writeErr(w, 400, "批量操作需要选择 1～1000 个账号")
		return
	}
	if in.Action == "join_group" || in.Action == "leave_group" {
		if in.Group == "" {
			writeErr(w, 400, "请选择分组")
			return
		}
		if err := p.checkGroups([]string{in.Group}); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if in.Action == "proxy" && in.ProxyID != "" {
		if p.cfg.Proxies == nil {
			writeErr(w, 400, "代理管理未启用")
			return
		}
		if _, _, err := p.cfg.Proxies.RouteProxy(in.ProxyID); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel()
	results := []map[string]any{}
	failed := []map[string]string{}
	ok := 0
	seen := map[string]bool{}
	for _, id := range in.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		var err error
		if in.Action == "liveness" {
			accounts := p.cfg.Providers.List()
			i := slices.IndexFunc(accounts, func(a providers.AccountView) bool { return a.ID == id })
			switch {
			case p.cfg.ProviderLiveness == nil:
				err = errors.New("平台测活未启用")
			case i < 0:
				err = errors.New("平台账号不存在")
			case accounts[i].LivenessModel == "":
				err = errors.New("请先在账号设置中选择批量与定时测活模型")
			default:
				var out providers.LivenessResponse
				out, err = p.cfg.ProviderLiveness(ctx, id, accounts[i].LivenessModel)
				if err == nil && !out.Result.OK {
					err = fmt.Errorf("测活失败 HTTP %d: %s", out.Result.Status, out.Result.Error)
				}
			}
		} else {
			err = p.cfg.Providers.Change(id, in.Action, in.Group, in.Priority, in.Models, in.ProxyID)
		}
		item := map[string]any{"id": id, "ok": err == nil}
		if err != nil {
			item["error"] = err.Error()
			failed = append(failed, map[string]string{"uid": id, "error": err.Error()})
		} else {
			ok++
		}
		results = append(results, item)
	}
	writeJSON(w, 200, map[string]any{"results": results, "ok": ok, "failed": failed})
}

// SetProviderLiveness 在开始监听前接入 handler 的单号测活链路。
func (p *Panel) SetProviderLiveness(test func(context.Context, string, string) (providers.LivenessResponse, error)) {
	p.cfg.ProviderLiveness = test
}

func (p *Panel) providerLiveness(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !slices.ContainsFunc(p.cfg.Providers.List(), func(a providers.AccountView) bool { return a.ID == id }) {
		writeErr(w, 404, "平台账号不存在")
		return
	}
	if p.cfg.ProviderLiveness == nil {
		writeErr(w, 501, "平台测活未启用")
		return
	}
	var in struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&in); err != nil {
		writeErr(w, 400, "测活请求格式错误："+err.Error())
		return
	}
	model := strings.TrimSpace(in.Model)
	if model == "" {
		writeErr(w, 400, "请选择要测试的模型")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 90*time.Second)
	defer cancel()
	out, err := p.cfg.ProviderLiveness(ctx, id, model)
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, out)
}
func (p *Panel) providersList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"targets": targets.All, "accounts": p.cfg.Providers.List(), "usage": p.cfg.Providers.Usage()})
}
func (p *Panel) providerInput(w http.ResponseWriter, r *http.Request) (providers.AccountInput, bool) {
	var in providers.AccountInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeErr(w, 400, "平台账号请求格式错误："+err.Error())
		return in, false
	}
	if in.ProxyID != "" && p.cfg.Proxies == nil {
		writeErr(w, 400, "代理管理未启用")
		return in, false
	}
	if in.ProxyID != "" {
		if _, _, err := p.cfg.Proxies.RouteProxy(in.ProxyID); err != nil {
			writeErr(w, 400, err.Error())
			return in, false
		}
	}
	if err := p.checkGroups(in.Groups); err != nil {
		writeErr(w, 400, err.Error())
		return in, false
	}
	var cleanGroups []string
	for _, g := range in.Groups {
		if g = strings.TrimSpace(g); g != "" && !slices.Contains(cleanGroups, g) {
			cleanGroups = append(cleanGroups, g)
		}
	}
	in.Groups = cleanGroups
	return in, true
}
func (p *Panel) providerImport(w http.ResponseWriter, r *http.Request) {
	in, ok := p.providerInput(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	id, err := p.cfg.Providers.Import(ctx, in)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"account_id": id})
}
func (p *Panel) providerUpdate(w http.ResponseWriter, r *http.Request) {
	in, ok := p.providerInput(w, r)
	if !ok {
		return
	}
	providerResult(w, p.cfg.Providers.Update(r.PathValue("id"), in, false))
}
func (p *Panel) providerDelete(w http.ResponseWriter, r *http.Request) {
	providerResult(w, p.cfg.Providers.Update(r.PathValue("id"), providers.AccountInput{}, true))
}
func providerResult(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, 404, "平台账号不存在")
		return
	}
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (p *Panel) providerModels(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		writeErr(w, 400, "缺少 target")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	models, err := p.cfg.Providers.Models(ctx, r.PathValue("id"), in.Target, true)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"models": models})
}
func (p *Panel) providerLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Target  string `json:"target"`
		ProxyID string `json:"proxy_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		writeErr(w, 400, "缺少 target")
		return
	}
	login, err := p.cfg.Providers.StartLogin(in.Target, in.ProxyID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, login)
}
func (p *Panel) providerLoginComplete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State    string `json:"state"`
		Callback string `json:"callback"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&in); err != nil {
		writeErr(w, 400, "授权结果格式错误")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	login, err := p.cfg.Providers.CompleteLogin(ctx, in.State, in.Callback)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, login)
}
