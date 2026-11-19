// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package http2

import "time"

// A timer is a time.Timer, as an interface which can be replaced in tests.
type timer = interface {
	C() <-chan time.Time
	Reset(d time.Duration) bool
	Stop() bool
}

// timeTimer adapts a time.Timer to the timer interface.
type timeTimer struct {
	*time.Timer
}

func (t timeTimer) C() <-chan time.Time { return t.Timer.C }
