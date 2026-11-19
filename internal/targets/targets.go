// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// Package targets 定义网关的稳定上游身份，不把产品通道和账号地区混为一谈。
package targets

import "strings"

var All = []string{"workbuddy:cn", "workbuddy:global", "traework:cn", "traecode:cn", "qoder:cn", "qoder:global"}

func Valid(target string) bool {
	for _, v := range All {
		if target == v {
			return true
		}
	}
	return false
}

func WorkBuddy(target string) bool { return target == "workbuddy:cn" || target == "workbuddy:global" }

// Parse 只识别已注册的平台前缀；原生模型 ID 内部的冒号原样保留。
func Parse(model string) (target, native string, explicit bool) {
	for _, t := range All {
		if strings.HasPrefix(model, t+":") {
			return t, strings.TrimPrefix(model, t+":"), true
		}
	}
	for _, region := range []string{"cn", "global"} {
		if strings.HasPrefix(model, region+":") {
			return "workbuddy:" + region, strings.TrimPrefix(model, region+":"), true
		}
	}
	return "", model, false
}

func Public(target, native string) string {
	if WorkBuddy(target) {
		return strings.TrimPrefix(target, "workbuddy:") + ":" + native
	}
	return target + ":" + native
}
