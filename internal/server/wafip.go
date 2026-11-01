// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// wafip.go WAF IP 级拦截 fail-fast 状态机（任务书 waf-ip-failfast）。
//
// 背景（fork-scan-absorb T-1 / BulidH 实测）：WAF 403 拦的是网关出口 IP 而非
// 账号——3 个账号 1 秒内全 403。既有 ErrWafBlock 账号级软冷却（wafCooldownBase
// 起的有界退避）在 IP 级拦截时不够：轮转会把一次客户端请求放大 MaxRotate 倍，
// 同一出口 IP 继续打上游只会加重风控。
//
// 判定：ErrWafBlock 基础上的短窗多号计数——wafIPWindow（60s 滑动窗）内
// ≥ wafIPThreshold 个**不同** UID 接连命中 WAF 403 → 判定 IP 级拦截，激活至
// now+wafIPWindow。单号反复 403（账号级偶发）永不触发：只数不同号。
// 激活期内新命中不续期（保守：不做主动探测，窗口自然解除）。
//
// 归属层评估：放 server（Handler 局部）而非 pool——IP 级状态唯一消费者是
// chatCompletions 轮转循环（是否继续轮转），pool 是账号级记账层，跨 UID 语义
// 不属于任何账号；server 已有进程级状态先例 degradeGate（mu+until 同风格）。
// 账号级软冷却照常记账（applyErrorPolicy 不变），IP 级状态只改变「是否继续
// 轮转」——协同不叠加。进程内状态、重启清零（窗口 60s，重建成本极低）。
//
// 按出口分开算（代理池，pool.ExitOf）：号挂了不同代理时出口 IP 各不相同，只有同一个出口的号接连被拦
// 才算这个出口被拦。被拦以后同出口的号这次不再试；别的出口还有号能接就接着换，没有才止损。没挂代理时
// 所有号同一个出口，和原来一样。
package server

import (
	"log"
	"sync"
	"time"
)

// wafIPWindow IP 级判定滑动窗 + 激活时长：窗内不同账号命中 WAF 403 达阈值即
// 判 IP 级拦截，激活同样长（到期自然解除）。var 仅供测试注入短窗（生产恒 60s，
// 任务书「如 60s 滑动窗」口径）。
var wafIPWindow = 60 * time.Second

// wafIPThreshold 判定阈值：窗内不同 UID 数达到该值激活。取 2——「多号」的最小
// 定义：单号反复 403 永不触发（账号级偶发归软冷却管），两个不同号在 60s 内接连
// 被拦（同一出口 IP）已是 IP 级证据（BulidH 实测 3 号 1s 全拦，阈值 2 更早止损，
// 少放大一次轮转）。
const wafIPThreshold = 2

// wafIPGate WAF IP 级拦截状态机（Handler 内嵌，零值可用），按出口分开记。
type wafIPGate struct {
	mu    sync.Mutex
	hits  map[string]map[string]time.Time // 出口 → uid → 最近一次 WAF 403 时刻（判定窗内，惰性剪枝）
	until map[string]time.Time            // 出口 → IP 级拦截激活截止；没有 = 未激活
}

// noteWaf 记一次某账号在出口 exit（label 是写日志用的名字，不含 IP）上的 WAF 403，返回记账后这个出口的
// IP 级拦截是否激活（调用方据此 fail-fast，优先于 rotateBackoff 退避）。
//   - 已激活（now < until）：不续期、不记账（窗口期内不重置——保守自然解除）→ true；
//   - 未激活：记 hits[uid]=now（同号重复命中覆盖不累计，判定口径是「不同号数」），
//     剪掉窗外的过期命中；不同 UID 数达 wafIPThreshold → 激活到 now+wafIPWindow
//     （打一条 WARN 供观测），清空判定窗（解除后需全新命中重新判定，不叠旧账）。
func (g *wafIPGate) noteWaf(exit, label, uid string) bool {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Before(g.until[exit]) {
		return true // 激活期内新命中：不续期（自然解除语义，任务书第 3 条）
	}
	if g.hits == nil {
		g.hits, g.until = map[string]map[string]time.Time{}, map[string]time.Time{}
	}
	hits := g.hits[exit]
	if hits == nil {
		hits = map[string]time.Time{}
		g.hits[exit] = hits
	}
	hits[uid] = now
	for u, t := range hits {
		if now.Sub(t) > wafIPWindow {
			delete(hits, u)
		}
	}
	if len(hits) >= wafIPThreshold {
		g.until[exit] = now.Add(wafIPWindow)
		on := ""
		if label != "" {
			on = " on " + label
		}
		log.Printf("WARN: [server] waf ip-level block: %d accounts hit waf 403%s within %s, rotate fail-fast until %s", len(hits), on, wafIPWindow, g.until[exit].Format(time.RFC3339))
		delete(g.hits, exit)
		return true
	}
	return false
}

// active 报告出口 exit 的 IP 级拦截是否激活（末端错误文案区分 IP 级/账号级措辞用）。
func (g *wafIPGate) active(exit string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Now().Before(g.until[exit])
}

// noteWafBlock 换号循环里记一次号 uid 的 WAF 403。它所在的出口被整个拦了时把同出口的号都记进 tried（这次
// 不再试），返回 stop：别的出口也没有号能接 model（realm 同选号）了，该止损。exit 是这个号的出口。
func (h *Handler) noteWafBlock(uid string, tried map[string]bool, model, realm string) (exit string, stop bool) {
	exit, label := h.cfg.Pool.ExitOf(uid)
	if !h.wafIP.noteWaf(exit, label, uid) {
		return exit, false
	}
	for _, u := range h.cfg.Pool.SameExit(uid) {
		tried[u] = true
	}
	return exit, !h.cfg.Pool.ServableIgnoringInFlight(tried, model, realm)
}
