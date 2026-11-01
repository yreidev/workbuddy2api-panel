// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

import (
	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// wireProxies 把代理池（按号代理，见 docs/proxy.md）接到号池和上游：号现在能不能出站推给号池（选号、
// 定时任务跳过被拦的号），风控波按出口分层，号的请求按绑定走代理，健康检查用和真实请求一样的 TLS 握手
// 测官方入口。auths 是号池里现有的号，逐个登记（启动时已有的号没绑就照旧走，不自动放进默认池）。
func wireProxies(reg *proxies.Registry, p *pool.Pool, up *upstream.Client, auths []*auth.Auth) {
	reg.SetGateHook(p.SetProxyBlocked)
	reg.SetProbes(up.ProbeProxy, up.EntryHosts(), up.EnvProxyURL())
	p.SetExitKey(reg.ExitKey)
	for _, a := range auths {
		reg.Track(a, false)
	}
	up.SetRouter(reg)
}
