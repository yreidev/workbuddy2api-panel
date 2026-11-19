// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"context"
	"log"

	"github.com/yreidev/workbuddy2api-panel/internal/glob"
	"github.com/yreidev/workbuddy2api-panel/internal/targets"
)

// routedKey 请求上下文标记：模型名已经在入口路由过（Claude Code / Codex 入口先路由再翻译），
// chatCompletions 不再路由第二遍——规则目标写错、不在目录里时，第二遍可能又命中别的规则。
type routedKey struct{}

// routeModel 模型路由：不带域前缀、模型目录里也没有的名字，按规则表从上往下找第一条
// 匹配的，改写成它的目标。没有规则、目录判断不了或没命中时原样返回，ok=false。
//
// 只改目录里没有的名字：客户端发的是上游真实模型名就直通，规则里写 * 兜底也不会动到它。
// 带 cn: / global: 前缀的名字是明确指定，不改。
func (h *Handler) routeModel(ctx context.Context, model string) (string, bool) {
	if _, _, explicit := targets.Parse(model); explicit {
		return model, false
	}
	if p := principalFrom(ctx); p.key != nil && p.key.DefaultTarget != "" && !targets.WorkBuddy(p.key.DefaultTarget) {
		return model, false
	}
	routes := h.loadLive().ModelRoutes
	if len(routes) == 0 || model == "" || h.cfg.Upstream == nil || ctx.Value(routedKey{}) != nil {
		return model, false
	}
	if _, bare := resolveModel(model); bare != model {
		return model, false
	}
	has, known := h.catalogHas(model)
	if has || !known && (h.cfg.Pool.HasAccounts() || len(h.cfg.Providers.List()) == 0) {
		return model, false
	}
	for _, rt := range routes {
		if glob.Match(rt.Match, model) {
			log.Printf("INFO: [server] model route %s -> %s (rule %q)", model, rt.Target, rt.Match)
			return rt.Target, true
		}
	}
	return model, false
}

// catalogHas 模型目录里有没有这个名字：国内版域目录与企业号目录，开了国际版再加国际版目录。
// known=false 表示国内版目录拉不到，判断不了。目录按需拉取，各自有缓存与失败冷却。
func (h *Handler) catalogHas(model string) (has, known bool) {
	if len(h.fetchDynamicModels()) == 0 {
		return false, false
	}
	if h.cfg.Upstream.RealmHasModel("cn", model) {
		return true, true
	}
	if h.cfg.GlobalEnabled {
		h.fetchGlobalModels()
		return h.cfg.Upstream.RealmHasModel("global", model), true
	}
	return false, true
}
