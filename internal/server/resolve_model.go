// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

import (
	"strings"

	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
)

// resolveModel 解析模型名协议（PLAN D6）：
//
//	分布式前缀： "[realm:]model"
//
// 取第一个 ":"，前段恰为 "cn"/"global" 才剥离；否则视为裸名，realm=cn、bare=原串。
// 大小写敏感（前缀必须是精确的小写枚举）。bare 即出站/选号/账本使用的裸模型名。
func resolveModel(model string) (realm, bare string) {
	idx := strings.IndexByte(model, ':')
	if idx < 0 {
		return "cn", model
	}
	prefix := model[:idx]
	if prefix != "cn" && prefix != "global" {
		return "cn", model
	}
	return prefix, model[idx+1:]
}

// crossRealmOn pool.cross_realm 开着没有（合池或国内版优先）：开着时不带前缀的模型名可以分到两个域。
func crossRealmOn(mode string) bool {
	return mode == livecfg.CrossRealmMerge || mode == livecfg.CrossRealmCNFirst
}

// crossRealmFor 跨域负载均衡（pool.cross_realm）下，不带前缀的模型名该在哪个域选号：
// 只在模型目录里确有这个模型的域里选。两域都有 → ""（合池，选号不加域过滤）；
// 只有一域有 → 该域；两边目录都没有（未拉到或模型不存在）→ "cn"，与关闭开关时一致。
func crossRealmFor(bare string, has func(realm, model string) bool) string {
	cn, global := has("cn", bare), has("global", bare)
	switch {
	case cn && global:
		return ""
	case global:
		return "global"
	default:
		return "cn"
	}
}

// cnFirstRealm 国内版优先（pool.cross_realm 为 cn_first，两个域都有这个模型）这次在哪个域选号：国内版此刻
// 有能接的号（tried 里的除外，口径见 pool.ServableForModel）就只在国内版选；没有而国际版有，就在国际版选；
// 两边都没有时为 ""，照合池走全冷却兜底和排队，两个域的号都算。
func (h *Handler) cnFirstRealm(tried map[string]bool, model string) string {
	switch {
	case h.cfg.Pool.ServableForModel(tried, model, "cn"):
		return "cn"
	case h.cfg.Pool.ServableForModel(tried, model, "global"):
		return "global"
	}
	return ""
}
