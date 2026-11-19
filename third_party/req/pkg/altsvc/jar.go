// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package altsvc

// Jar is a container of AltSvc.
type Jar interface {
	// SetAltSvc store the AltSvc.
	SetAltSvc(addr string, as *AltSvc)
	// GetAltSvc get the AltSvc.
	GetAltSvc(addr string) *AltSvc
}
