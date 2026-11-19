// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"fmt"
	"strings"
	"testing"

	clienthpack "github.com/imroc/req/v3/internal/hpack"
	"golang.org/x/net/http2/hpack"
)

func TestPathWithoutIndex(t *testing.T) {
	for _, path := range []string{"/a", "/api/ide/v1/get_detail_param", "/" + strings.Repeat("a", 126), "/" + strings.Repeat("z", 512), "/" + strings.Repeat("0123456789abcdef", 2048), "/\x01\x02\x03\x04"} {
		t.Run(fmt.Sprint(len(path)), func(t *testing.T) {
			block := pathWithoutIndex(path)
			if block[0] != 4 {
				t.Fatal("路径没有使用 without-indexing / static index 4")
			}
			decoder := hpack.NewDecoder(4096, nil)
			fields, err := decoder.DecodeFull(block)
			if err != nil || len(fields) != 1 || fields[0].Name != ":path" || fields[0].Value != path || fields[0].Sensitive {
				t.Fatalf("路径解码错误：%v %v", fields, err)
			}
			// literal 不能占据动态表，否则索引 62 会意外有效。
			if _, err := decoder.DecodeFull([]byte{0xbe}); err == nil {
				t.Fatal("without-indexing 路径进入了动态表")
			}
		})
	}
}

func TestPathWithoutIndexDynamicTableUpdates(t *testing.T) {
	for _, size := range []uint32{0, 64, 4096} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			cc := &ClientConn{t: &Transport{PathWithoutIndex: true}}
			// 实际 ClientConn 的 henc 与 hbuf 使用同一个缓冲。
			cc.henc = clienthpack.NewEncoder(&cc.hbuf)
			cc.henc.SetMaxDynamicTableSize(size)
			decoder := hpack.NewDecoder(4096, nil)
			for range 2 {
				cc.hbuf.Reset()
				cc.writeHeader(":method", "POST")
				cc.writeHeader(":authority", "fixture.invalid")
				cc.writeHeader(":scheme", "https")
				cc.writeHeader(":path", "/fixture-long-path")
				cc.writeHeader("authorization", "Bearer fixture")
				fields, err := decoder.DecodeFull(cc.hbuf.Bytes())
				if err != nil || len(fields) != 5 || fields[3].Value != "/fixture-long-path" || fields[4].Value != "Bearer fixture" {
					t.Fatalf("表大小变化后动态索引不同步：%v %v", fields, err)
				}
			}
		})
	}
}
