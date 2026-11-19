// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

//go:build !unix

package upstream

import "net"

// 非 Unix 的 RawConn.Write 不提供相同的 poller／deadline 语义，继续保留
// 原传输；没有把 Unix 上的背压校准宣称为这些平台也已验证。
func profileSocketWriterFor(net.Conn) (profileSocketWriter, error) { return nil, nil }
