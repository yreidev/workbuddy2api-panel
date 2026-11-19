// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func newTTNetTraceID(deviceID string) string {
	var entropy [17]byte
	_, _ = rand.Read(entropy[:])
	return formatTTNetTraceID(time.Now().UnixMilli(), deviceID, entropy)
}

// formatTTNetTraceID 按官方 TTNet a61b3479 的原生函数和固定输入样本编码。
// 国内官方 appId 801256 超过四位十六进制上限，原函数截到 ffff。
func formatTTNetTraceID(millis int64, deviceID string, entropy [17]byte) string {
	device, err := strconv.ParseInt(deviceID, 10, 64)
	if err != nil {
		// 原函数对非十进制、越界或空设备 ID 使用数值 0。
		device = 0
	}
	hexDevice := strconv.FormatUint(uint64(device), 16)
	var trace strings.Builder
	fmt.Fprintf(&trace, "%08x%02x%s", millis%0xffffffff, len(hexDevice), hexDevice)
	const hex = "0123456789abcdef"
	for _, b := range entropy[:18-len(hexDevice)] {
		trace.WriteByte(hex[b&15])
	}
	trace.WriteString("ffff")
	id := trace.String()
	return "00-" + id + "-" + id[:16] + "-01"
}
