// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// Package livecfg 运行期可变配置的并发安全持有者。
//
// 背景：进程启动时读入的配置是普通字段（读多写零），但管理面板允许在线改配置，
// 于是少量"可热生效"的字段需要有并发安全的读写点。此处用不可变快照 + atomic 指针：
// 读方 Load 拿到一致视图，写方 Store 整体替换，无锁无数据竞争。
//
// 只承载**读路径深、热改需求强**的少数字段；池参数/排程参数等各有既有 setter
// （pool.SetBreaker、scheduler.Reconfigure 等），不重复收编到这里。
package livecfg

import (
	"strings"
	"sync/atomic"
	"time"
)

// Snapshot 一次读取的不可变配置视图。
type Snapshot struct {
	APIKey               string        // 网关/面板共同鉴权密钥；空 = 不鉴权
	SoftCooldown         time.Duration // 429 软冷却基数（<=0 时调用方回退内置默认）
	SanitizeFingerprints bool          // 出站请求体指纹脱敏
	RecordClientInfo     bool          // 请求日志是否记录调用来源（客户端 IP / UA）
	CrossRealm           string        // 跨域负载均衡（pool.cross_realm）：CrossRealmMerge、CrossRealmCNFirst，空或 CrossRealmOff 为关
	UpdateCheck          bool          // 面板定时检查新版本（读 GitHub 上的更新日志）
	ModelRoutes          []ModelRoute  // 模型路由规则：按顺序匹配，只改写模型目录里没有的名字
	// HiddenModels 不对外的模型，键是带域前缀的全名（cn:x / global:x），见 NormalizeHiddenModels；
	// nil = 不隐藏。只读共享，不得修改。
	HiddenModels map[string]struct{}
}

// pool.cross_realm 的三档（Snapshot.CrossRealm）：不带前缀的模型名怎么在两个域之间分。
const (
	CrossRealmOff     = "off"      // 只走国内版
	CrossRealmMerge   = "merge"    // 两个域都有这个模型时合池选号
	CrossRealmCNFirst = "cn_first" // 国内版优先：国内版此刻没有能接的号才用国际版
)

// NormalizeHiddenModels 把 models.hidden 的条目归一成带域前缀的全名集合：带 cn: / global:
// 前缀的只藏那个域，裸名两个域都藏。精确匹配、区分大小写（前缀规则同 server.resolveModel：
// 第一个冒号前恰好是 cn 或 global 才算前缀）。没有有效条目时返回 nil。
func NormalizeHiddenModels(hidden []string) map[string]struct{} {
	out := make(map[string]struct{}, len(hidden)*2)
	for _, m := range hidden {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if prefix, bare, ok := strings.Cut(m, ":"); ok && (prefix == "cn" || prefix == "global") {
			out[prefix+":"+bare] = struct{}{}
		} else {
			out["cn:"+m] = struct{}{}
			out["global:"+m] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ModelRoute 一条模型路由：客户端发来的模型名匹配 Match（只认 * 通配，不区分大小写）时改用 Target。
type ModelRoute struct {
	Match  string `json:"match"`
	Target string `json:"target"`
}

// Holder 原子持有当前快照。
type Holder struct {
	p atomic.Pointer[Snapshot]
}

// New 以初始快照构建。
func New(s Snapshot) *Holder {
	h := &Holder{}
	h.Store(s)
	return h
}

// Load 返回当前快照（Holder 为 nil 或从未 Store 时返回零值快照，调用方无需判空）。
func (h *Holder) Load() Snapshot {
	if h == nil {
		return Snapshot{}
	}
	if s := h.p.Load(); s != nil {
		return *s
	}
	return Snapshot{}
}

// Store 整体替换快照。
func (h *Holder) Store(s Snapshot) { h.p.Store(&s) }
