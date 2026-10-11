package server

import "strings"

// resolveModel 解析模型名协议（PLAN D6）：
//
//	分布式前缀： "[realm:]model"
//
// 取第一个 ":"，前段恰为 "cn"/"global" 才剥离；否则视为裸名，realm=cn、bare=原串。
// 大小写敏感（前缀必须是精确的小写枚举）。bare 即出站/选号/账本使用的裸模型名。
//
// 导出为 ResolveModel（cmd/server/main.go 粘性闭包需要），包内简写 resolveModel。
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

// ResolveModel 是 resolveModel 的导出面（跨包调用）。
func ResolveModel(model string) (realm, bare string) { return resolveModel(model) }

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
