// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package hpack

import "fmt"

// ChromiumError 对应 Quiche decoder 的错误编号，保留官方的线上诊断字段。
type ChromiumError uint8

const (
	chromiumIndexVarint ChromiumError = 11 + iota
	chromiumNameLengthVarint
	chromiumValueLengthVarint
	chromiumNameTooLong
	chromiumValueTooLong
	chromiumNameHuffman
	chromiumValueHuffman
)

const (
	chromiumInvalidIndex ChromiumError = 19 + iota
	chromiumInvalidNameIndex
	chromiumTableUpdateNotAllowed
)

const (
	chromiumTableUpdateTooLarge ChromiumError = 23
	chromiumTruncatedBlock      ChromiumError = 24
)

func (e ChromiumError) Error() string {
	names := [...]string{
		"HPACK_INDEX_VARINT_ERROR", "HPACK_NAME_LENGTH_VARINT_ERROR", "HPACK_VALUE_LENGTH_VARINT_ERROR",
		"HPACK_NAME_TOO_LONG", "HPACK_VALUE_TOO_LONG", "HPACK_NAME_HUFFMAN_ERROR", "HPACK_VALUE_HUFFMAN_ERROR",
		"HPACK_MISSING_DYNAMIC_TABLE_SIZE_UPDATE", "HPACK_INVALID_INDEX", "HPACK_INVALID_NAME_INDEX",
		"HPACK_DYNAMIC_TABLE_SIZE_UPDATE_NOT_ALLOWED", "HPACK_INITIAL_DYNAMIC_TABLE_SIZE_UPDATE_IS_ABOVE_LOW_WATER_MARK",
		"HPACK_DYNAMIC_TABLE_SIZE_UPDATE_IS_ABOVE_ACKNOWLEDGED_SETTING", "HPACK_TRUNCATED_BLOCK",
	}
	return fmt.Sprintf("Framer error: %d (%s).", e, names[e-chromiumIndexVarint])
}

// NewChromiumDecoder 复用现有 HPACK 解码器；仅校准官方已采样的错误范围、
// 字段错误分类及每块至多两次且必须位于普通字段之前的动态表更新。
func NewChromiumDecoder(maxDynamicTableSize uint32, emitFunc func(HeaderField)) *Decoder {
	d := NewDecoder(maxDynamicTableSize, emitFunc)
	d.chromium = true
	d.dynTab.setMaxSize(min(maxDynamicTableSize, 4096))
	return d
}

func (d *Decoder) classifiedError(err error, code ChromiumError) error {
	if d.chromium && err != nil && err != errNeedMore {
		return code
	}
	return err
}
