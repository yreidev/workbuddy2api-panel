// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

// proxies.go 代理池的管理接口（internal/proxies，规则见 docs/proxy.md）：代理的列表、批量导入（先预览）、
// 导出、修改、测试、删除，池的增删改和池内重新均分，号的批量绑定，代理池设置。
// 代理地址回给面板时密码都打了码；导出是唯一带密码的出口（备份用）。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
)

// proxiesBodyLimit 导入文本的上限（几千行代理也够）。
const proxiesBodyLimit = 4 << 20

// proxyTestTimeout 面板里点「测试」最多等多久（连通加出口 IP，代理池自己的上限是 30 秒）。
const proxyTestTimeout = 40 * time.Second

// proxiesReady 没装代理池时回 501。
func (p *Panel) proxiesReady(w http.ResponseWriter) bool {
	if p.cfg.Proxies == nil {
		writeErr(w, http.StatusNotImplemented, "proxy pool not available")
		return false
	}
	return true
}

// decodeProxyBody 读请求体 JSON；读不了回 400。
func decodeProxyBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, proxiesBodyLimit)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对："+err.Error())
		return false
	}
	return true
}

// writeProxyErr 池或代理不存在回 404，写不进 proxies.json 回 500，其余（输入不合法）回 400。
func writeProxyErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, proxies.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, proxies.ErrSave):
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

// proxiesList GET /panel/api/proxies：代理页的全部数据，外加每个号的代理情况（账号池卡片也用）。
func (p *Panel) proxiesList(w http.ResponseWriter, r *http.Request) {
	if !p.proxiesReady(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"view": p.cfg.Proxies.View(), "accounts": p.cfg.Proxies.AccountViews()})
}

// proxyImportInput 批量导入：一行一个代理；Scheme 没写协议的行按什么算（http / socks5）；Pool 导入到哪个
// 池；ExpiresAt 这批代理的到期时间（可空）。
type proxyImportInput struct {
	Text      string     `json:"text"`
	Scheme    string     `json:"scheme"`
	Pool      string     `json:"pool"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// proxiesPreview POST /panel/api/proxies/preview：只解析不导入，标出认不出的行和重复的行。
func (p *Panel) proxiesPreview(w http.ResponseWriter, r *http.Request) {
	var in proxyImportInput
	if !p.proxiesReady(w) || !decodeProxyBody(w, r, &in) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": p.cfg.Proxies.Preview(in.Text, in.Scheme)})
}

// proxiesImport POST /panel/api/proxies/import：导入能用的行，导完马上测一遍（测不通的照样导入、标不可用）。
func (p *Panel) proxiesImport(w http.ResponseWriter, r *http.Request) {
	var in proxyImportInput
	if !p.proxiesReady(w) || !decodeProxyBody(w, r, &in) {
		return
	}
	res, err := p.cfg.Proxies.Import(in.Text, in.Scheme, in.Pool, in.ExpiresAt)
	if err != nil {
		writeProxyErr(w, err)
		return
	}
	log.Printf("panel: 导入代理 %d 个（共 %d 行）", len(res.Added), len(res.Lines))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "lines": res.Lines, "added": res.Added})
}

// proxiesExport GET /panel/api/proxies/export：导出全部代理（带密码，按池分组，可以原样导回来），备份用。
func (p *Panel) proxiesExport(w http.ResponseWriter, r *http.Request) {
	if !p.proxiesReady(w) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="proxies.txt"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, p.cfg.Proxies.Export())
}

// proxyUpdate POST /panel/api/proxies/{id}：改名字、地址（密码还是 *** 就沿用原密码）、池、到期日。
func (p *Panel) proxyUpdate(w http.ResponseWriter, r *http.Request) {
	var in proxies.ProxyInput
	if !p.proxiesReady(w) || !decodeProxyBody(w, r, &in) {
		return
	}
	if err := p.cfg.Proxies.UpdateProxy(r.PathValue("id"), in); err != nil {
		writeProxyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// proxyTest POST /panel/api/proxies/{id}/test：马上测一个代理（连通和出口 IP），测完才返回。
func (p *Panel) proxyTest(w http.ResponseWriter, r *http.Request) {
	if !p.proxiesReady(w) {
		return
	}
	// 关掉弹窗也测完，结果照样记进列表（同测活）。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), proxyTestTimeout)
	defer cancel()
	if err := p.cfg.Proxies.TestNow(ctx, r.PathValue("id")); err != nil {
		writeProxyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// proxiesTestAll POST /panel/api/proxies/test_all：所有代理马上重测一遍（后台跑，结果看列表）。
func (p *Panel) proxiesTestAll(w http.ResponseWriter, r *http.Request) {
	if !p.proxiesReady(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "total": p.cfg.Proxies.TestAll()})
}

// proxiesDelete POST /panel/api/proxies/delete {ids}：删代理，绑在上面的号改绑到同池别的代理（没有空位就等）。
func (p *Panel) proxiesDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if !p.proxiesReady(w) || !decodeProxyBody(w, r, &in) {
		return
	}
	n, err := p.cfg.Proxies.DeleteProxies(in.IDs)
	if err != nil {
		writeProxyErr(w, err)
		return
	}
	log.Printf("panel: 删掉代理 %d 个", n)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": n})
}

// proxiesDeleteUnavailable POST /panel/api/proxies/delete_unavailable：一键删掉不通和已到期的代理。
func (p *Panel) proxiesDeleteUnavailable(w http.ResponseWriter, r *http.Request) {
	if !p.proxiesReady(w) {
		return
	}
	n, err := p.cfg.Proxies.DeleteUnavailable()
	if err != nil {
		writeProxyErr(w, err)
		return
	}
	log.Printf("panel: 删掉不可用的代理 %d 个", n)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": n})
}

// proxiesSettings POST /panel/api/proxies/settings：改代理池设置（只许走号的代理、检查间隔、查出口 IP 的地址等）。
func (p *Panel) proxiesSettings(w http.ResponseWriter, r *http.Request) {
	var in proxies.Settings
	if !p.proxiesReady(w) || !decodeProxyBody(w, r, &in) {
		return
	}
	s, err := p.cfg.Proxies.UpdateSettings(in)
	if err != nil {
		writeProxyErr(w, err)
		return
	}
	log.Printf("panel: 改了代理池设置（只许走号的代理=%v）", s.RequireBound)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": s})
}

// proxiesBind POST /panel/api/proxies/bind {uids, pool | proxy | unbind}：批量绑定，逐个回结果。
func (p *Panel) proxiesBind(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UIDs []string `json:"uids"`
		proxies.BindTarget
	}
	if !p.proxiesReady(w) || !decodeProxyBody(w, r, &in) {
		return
	}
	if len(in.UIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "没选号")
		return
	}
	for _, uid := range in.UIDs {
		if _, ok := p.cfg.Pool.Status(uid); !ok {
			writeErr(w, http.StatusNotFound, "号不在池里："+uid)
			return
		}
	}
	res, err := p.cfg.Proxies.Bind(in.UIDs, in.BindTarget)
	if err != nil {
		writeProxyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": res})
}

// proxyPoolCreate POST /panel/api/proxy_pools：新建池。
func (p *Panel) proxyPoolCreate(w http.ResponseWriter, r *http.Request) {
	var in proxies.PoolInput
	if !p.proxiesReady(w) || !decodeProxyBody(w, r, &in) {
		return
	}
	id, err := p.cfg.Proxies.CreatePool(in)
	if err != nil {
		writeProxyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// proxyPoolUpdate POST /panel/api/proxy_pools/{id}：改池的名字、每个代理最多几个号、默认池。
func (p *Panel) proxyPoolUpdate(w http.ResponseWriter, r *http.Request) {
	var in proxies.PoolInput
	if !p.proxiesReady(w) || !decodeProxyBody(w, r, &in) {
		return
	}
	if err := p.cfg.Proxies.UpdatePool(r.PathValue("id"), in); err != nil {
		writeProxyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// proxyPoolDelete POST /panel/api/proxy_pools/{id}/delete：删池（池里还有代理不让删）。
func (p *Panel) proxyPoolDelete(w http.ResponseWriter, r *http.Request) {
	if !p.proxiesReady(w) {
		return
	}
	if err := p.cfg.Proxies.DeletePool(r.PathValue("id")); err != nil {
		writeProxyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// proxyPoolRebalance POST /panel/api/proxy_pools/{id}/rebalance：池内重新均分（会改号的出口 IP，面板先二次确认）。
func (p *Panel) proxyPoolRebalance(w http.ResponseWriter, r *http.Request) {
	if !p.proxiesReady(w) {
		return
	}
	n, err := p.cfg.Proxies.Rebalance(r.PathValue("id"))
	if err != nil {
		writeProxyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "moved": n})
}
