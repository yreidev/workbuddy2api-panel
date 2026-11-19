// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

//go:build !windows

package providers

import (
	"fmt"
	"os"
	"path/filepath"
)

func replaceProviderFile(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("替换平台文件: %w", err)
	}
	dir, err := os.Open(filepath.Dir(to))
	if err != nil {
		return fmt.Errorf("打开平台数据目录: %w", err)
	}
	err = dir.Sync()
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("同步平台数据目录: %w", err)
	}
	return nil
}
