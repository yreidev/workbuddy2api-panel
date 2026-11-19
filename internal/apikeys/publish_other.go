// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

//go:build !linux && !darwin && !windows

package apikeys

import "os"

// 非发行平台沿用原子的排他发布方式。
func publishNoReplace(from, to string) error {
	return os.Link(from, to)
}
