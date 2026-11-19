// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package hpack

import (
	"bytes"
	"errors"
	"testing"
)

func TestChromiumDecoderClassifiesSplitFieldsAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire []byte
		want ChromiumError
	}{
		{"名称先失败", []byte{0, 0x81, 0xff}, chromiumNameHuffman},
		{"坏名称索引先检查值", []byte{0x0f, 0x2f, 0x81, 0xff}, chromiumValueHuffman},
		{"坏名称索引遇到截断值", []byte{0x0f, 0x2f, 2, 'v'}, chromiumTruncatedBlock},
		{"更新之前的整数截断", []byte{0x88, 0x3f}, chromiumTruncatedBlock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoder := NewChromiumDecoder(65536, func(HeaderField) {})
			var err error
			for _, b := range tc.wire {
				_, err = decoder.Write([]byte{b})
				if err != nil {
					break
				}
			}
			if err == nil {
				err = decoder.Close()
			}
			var code ChromiumError
			if !errors.As(err, &code) || code != tc.want {
				t.Fatalf("跨输入片段后错误被覆盖：got=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestChromiumDecoderTableUpdateBudgetResetsPerBlock(t *testing.T) {
	for _, chromium := range []bool{false, true} {
		decoder := NewDecoder(65536, func(HeaderField) {})
		if chromium {
			decoder = NewChromiumDecoder(65536, func(HeaderField) {})
		}
		for range 2 {
			if _, err := decoder.Write([]byte{0x20, 0x20, 0x88}); err != nil {
				t.Fatal(err)
			}
			if err := decoder.Close(); err != nil {
				t.Fatal(err)
			}
		}
		_, err := decoder.Write([]byte{0x20, 0x20, 0x20})
		if chromium {
			if err != chromiumTableUpdateNotAllowed {
				t.Fatalf("第三次更新未拒绝：%v", err)
			}
		} else if err != nil {
			t.Fatalf("原解码器策略被改变：%v", err)
		}
	}
}

func TestChromiumDecoderInitialCapacityRequiresPeerUpdate(t *testing.T) {
	var encoded bytes.Buffer
	encoder := NewEncoder(&encoded)
	encoder.SetMaxDynamicTableSizeLimit(65536)
	encoder.SetMaxDynamicTableSize(65536)
	field := HeaderField{Name: "x-test", Value: string(bytes.Repeat([]byte{'v'}, 5000))}
	if err := encoder.WriteField(field); err != nil {
		t.Fatal(err)
	}
	var fields []HeaderField
	decoder := NewChromiumDecoder(65536, func(h HeaderField) { fields = append(fields, h) })
	if _, err := decoder.Write(encoded.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Write([]byte{0xbe}); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Close(); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields[0] != field || fields[1] != field {
		t.Fatal("对端明确扩容后的动态表不能跨块复用")
	}
	decoder = NewChromiumDecoder(65536, func(HeaderField) {})
	if decoder.dynTab.maxSize != 4096 || decoder.dynTab.allowedMaxSize != 65536 {
		t.Fatal("初始容量被本端声明的最大值替代")
	}
}

func TestChromiumDecoderHuffmanPrefixWaitsForTermination(t *testing.T) {
	decoder := NewChromiumDecoder(65536, func(HeaderField) {})
	if _, err := decoder.Write([]byte{0, 100 | 0x80, 0xff}); err != nil {
		t.Fatalf("合法未完成前缀被当作坏 padding：%v", err)
	}
	if _, err := decoder.Write([]byte{0xff, 0xff, 0xff}); err != chromiumNameHuffman {
		t.Fatalf("EOS 已经非法时仍等待后续字节：%v", err)
	}
}
