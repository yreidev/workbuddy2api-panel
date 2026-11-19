// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package hpack

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	standard "golang.org/x/net/http2/hpack"
)

func TestChromiumOversizedFieldsRemainDecodable(t *testing.T) {
	for _, size := range []uint32{0, 64, 4096} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var wire bytes.Buffer
			encoder := NewEncoder(&wire)
			encoder.IndexOversized = true
			encoder.SetMaxDynamicTableSize(size)
			decoder := standard.NewDecoder(size, nil)
			fields := []HeaderField{
				{Name: "x-fixture", Value: "small"},
				{Name: "x-fixture", Value: strings.Repeat("a", 5000)},
				{Name: "x-fixture", Value: "small"},
				{Name: "x-private", Value: "fixture", Sensitive: true},
			}
			for range 2 {
				for _, field := range fields {
					wire.Reset()
					if err := encoder.WriteField(field); err != nil {
						t.Fatal(err)
					}
					got, err := decoder.DecodeFull(wire.Bytes())
					want := []standard.HeaderField{{Name: field.Name, Value: field.Value, Sensitive: field.Sensitive}}
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("表大小 %d 的连续字段不能由标准解码器读取：%v %v", size, got, err)
					}
				}
			}
		})
	}
}
