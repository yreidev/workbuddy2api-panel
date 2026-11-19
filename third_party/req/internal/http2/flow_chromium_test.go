// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"testing"
	"time"
)

func TestChromiumReceiveWindowThresholdAndSlowConsumption(t *testing.T) {
	for _, window := range []int32{6 << 20, 15 << 20} {
		var f inflow
		f.initChromium(window)
		if !f.take(uint32(window/2)) || f.add(int(window/2)) != 0 {
			t.Fatal("消费恰好半窗时不应更新")
		}
		if !f.take(1) || f.add(1) != window/2+1 || f.avail != window {
			t.Fatal("超过半窗没有合并更新或额度不守恒")
		}
		if !f.take(8192) || f.add(8192) != 0 {
			t.Fatal("未超过半窗的小数据不应立即更新")
		}
		f.lastUpdate = time.Now().Add(-5 * time.Second)
		if f.add(0) != 0 {
			t.Fatal("没有新的消费不应因时间触发更新")
		}
		if !f.take(1) || f.add(1) != 8193 || f.avail != window {
			t.Fatal("五秒后的下一次消费没有归还累计额度")
		}
	}
}

func TestDefaultReceiveWindowRefreshUnchanged(t *testing.T) {
	var f inflow
	f.init(6 << 20)
	if !f.take(8192) || f.add(8192) != 8192 {
		t.Fatal("未开启 Chromium 时改变了原库补窗策略")
	}
}
