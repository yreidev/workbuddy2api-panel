// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import "golang.org/x/net/http2/hpack"

// pathWithoutIndex 写 RFC 7541 §6.2.2 的 literal，静态名称索引 4。
// Huffman 编码复用 x/net 的实现，仅在能够缩短字符串时使用。
func pathWithoutIndex(value string) []byte {
	length, flag := uint64(len(value)), byte(0)
	if encoded := hpack.HuffmanEncodeLength(value); encoded < length {
		length, flag = encoded, 0x80
	}
	b := []byte{0x04}
	if length < 127 {
		b = append(b, flag|byte(length))
	} else {
		b = append(b, flag|127)
		for length -= 127; length >= 128; length >>= 7 {
			b = append(b, byte(length&127)|128)
		}
		b = append(b, byte(length))
	}
	if flag != 0 {
		return hpack.AppendHuffmanString(b, value)
	}
	return append(b, value...)
}
