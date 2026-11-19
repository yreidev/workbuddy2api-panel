// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	clienthpack "github.com/imroc/req/v3/internal/hpack"
	"golang.org/x/net/http2/hpack"
)

func TestChromiumHeadersRejectInvalidPrefixBeforeFrameComplete(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  FrameType
		flags Flags
		body  []byte
		debug string
	}{
		{"HEADERS", FrameHeaders, 0, []byte{0x80}, "Framer error: 19 (HPACK_INVALID_INDEX)."},
		{"CONTINUATION", FrameContinuation, 0, []byte{0x80}, "Framer error: 19 (HPACK_INVALID_INDEX)."},
		{"padding", FrameHeaders, FlagHeadersPadded, []byte{32, 0x80}, "Framer error: 19 (HPACK_INVALID_INDEX)."},
		{"priority", FrameHeaders, FlagHeadersPriority, []byte{0x80, 0, 0, 0, 16, 0x80}, "Framer error: 19 (HPACK_INVALID_INDEX)."},
		{"完整坏Huffman", FrameHeaders, 0, []byte{0, 0x84, 255, 255, 255, 255}, "Framer error: 16 (HPACK_NAME_HUFFMAN_ERROR)."},
		{"未完成坏Huffman", FrameHeaders, 0, []byte{0, 100 | 0x80, 255, 255, 255, 255}, "Framer error: 16 (HPACK_NAME_HUFFMAN_ERROR)."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire bytes.Buffer
			if tc.kind == FrameContinuation {
				_ = NewFramer(&wire, nil).WriteRawFrame(FrameHeaders, 0, 1, nil)
			}
			length := len(tc.body) + 64
			wire.Write([]byte{byte(length >> 16), byte(length >> 8), byte(length), byte(tc.kind), byte(tc.flags), 0, 0, 0, 1})
			wire.Write(tc.body)
			r, w := io.Pipe()
			defer r.Close()
			defer w.Close()
			fr := NewFramer(nil, r)
			fr.cc = uploadTestConn(io.Discard, 65535)
			fr.ReadMetaHeaders = hpack.NewDecoder(65536, nil)
			fr.chromiumReadMeta = clienthpack.NewChromiumDecoder(65536, nil)
			done := make(chan error, 1)
			go func() { _, err := fr.ReadFrame(); done <- err }()
			if _, err := w.Write(wire.Bytes()); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				var detail uploadProtocolError
				if !errors.As(err, &detail) || detail.ConnectionError != ConnectionError(ErrCodeCompression) || detail.debugData != tc.debug {
					t.Fatalf("半帧已确定非法时分类不同：%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("收到非法前缀后仍等待整帧")
			}
		})
	}
}

func TestChromiumHeadersDoNotCompleteBeforePaddingAndContinuation(t *testing.T) {
	for _, padding := range []int{0, 32} {
		t.Run(fmt.Sprintf("padding=%d", padding), func(t *testing.T) {
			r, w := io.Pipe()
			defer r.Close()
			defer w.Close()
			fr := NewFramer(nil, r)
			fr.cc = uploadTestConn(io.Discard, 65535)
			fr.ReadMetaHeaders = hpack.NewDecoder(65536, nil)
			fr.chromiumReadMeta = clienthpack.NewChromiumDecoder(65536, nil)
			var head bytes.Buffer
			flags := FlagHeadersEndStream
			payload := []byte{0x88}
			if padding != 0 {
				flags |= FlagHeadersPadded
				payload = append([]byte{byte(padding)}, payload...)
			}
			_ = NewFramer(&head, nil).WriteRawFrame(FrameHeaders, flags, 1, append(payload, make([]byte, padding)...))
			prefix := head.Bytes()[:9+len(payload)]
			done := make(chan error, 1)
			go func() {
				frame, err := fr.ReadFrame()
				if err == nil {
					f := frame.(*MetaHeadersFrame)
					if !f.StreamEnded() || f.HeadersEnded() || f.Flags != flags || len(f.Fields) != 1 || f.Fields[0].Value != "200" {
						err = fmt.Errorf("物理首帧元数据或头块不同：%v", f)
					}
				}
				done <- err
			}()
			if _, err := w.Write(prefix); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				t.Fatalf("未收齐 padding／CONTINUATION 时结束：%v", err)
			default:
			}
			var tail bytes.Buffer
			tail.Write(make([]byte, padding))
			_ = NewFramer(&tail, nil).WriteContinuation(1, true, nil)
			if _, err := w.Write(tail.Bytes()); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("完整头块到达后没有结束")
			}
		})
	}
}

func TestChromiumTruncatedHeadersPreserveReadError(t *testing.T) {
	for _, wire := range [][]byte{
		{0, 0, 8, 1, 4, 0, 0, 0, 1, 0x88},
		{0, 0, 8, 1, 12, 0, 0, 0, 1, 4, 0x88},
		{0, 0, 8, 1, 36, 0, 0, 0, 1, 0x80, 0},
	} {
		fr := NewFramer(nil, bytes.NewReader(wire))
		fr.cc = uploadTestConn(io.Discard, 65535)
		fr.ReadMetaHeaders = hpack.NewDecoder(65536, nil)
		fr.chromiumReadMeta = clienthpack.NewChromiumDecoder(65536, nil)
		if _, err := fr.ReadFrame(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("读取中断被改成 padding／压缩错误：%v", err)
		}
	}
}
