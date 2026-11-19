// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"bytes"
	"errors"
	"io"
	"testing"

	clienthpack "github.com/imroc/req/v3/internal/hpack"
	"golang.org/x/net/http2/hpack"
)

func TestChromiumHeaderSemanticsDecodeThroughStreamError(t *testing.T) {
	var wire bytes.Buffer
	w := NewFramer(&wire, nil)
	// 大写字段先触发语义错误；后续 CONTINUATION 仍须更新连接动态表。
	_ = w.WriteRawFrame(FrameHeaders, 0, 1, []byte{0x88, 0, 1, 'X', 1, 'v'})
	_ = w.WriteRawFrame(FrameContinuation, FlagContinuationEndHeaders, 1, []byte{0x40, 1, 'x', 1, 'v'})
	_ = w.WriteRawFrame(FrameHeaders, FlagHeadersEndHeaders, 3, []byte{0x88, 0xbe})
	fr := NewFramer(nil, &wire)
	fr.cc = uploadTestConn(io.Discard, 65535)
	fr.ReadMetaHeaders = hpack.NewDecoder(65536, nil)
	fr.chromiumReadMeta = clienthpack.NewChromiumDecoder(65536, nil)
	_, err := fr.ReadFrame()
	var stream StreamError
	if !errors.As(err, &stream) || stream.StreamID != 1 || stream.Code != ErrCodeProtocol {
		t.Fatalf("非法字段跨 CONTINUATION 被升级为连接错误：%v", err)
	}
	f, err := fr.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	fields := f.(*MetaHeadersFrame).Fields
	if len(fields) != 2 || fields[1].Name != "x" || fields[1].Value != "v" {
		t.Fatalf("流错误后连接动态表失步：%+v", fields)
	}
}

func TestChromiumHeaderSemanticsCompressionErrorAfterInvalidField(t *testing.T) {
	var wire bytes.Buffer
	_ = NewFramer(&wire, nil).WriteRawFrame(FrameHeaders, FlagHeadersEndHeaders, 1, []byte{0x88, 0, 1, 'X', 1, 'v', 0x80})
	fr := NewFramer(nil, &wire)
	fr.cc = uploadTestConn(io.Discard, 65535)
	fr.ReadMetaHeaders = hpack.NewDecoder(65536, nil)
	fr.chromiumReadMeta = clienthpack.NewChromiumDecoder(65536, nil)
	_, err := fr.ReadFrame()
	var detail uploadProtocolError
	if !errors.As(err, &detail) || detail.ConnectionError != ConnectionError(ErrCodeCompression) || detail.debugData != "Framer error: 19 (HPACK_INVALID_INDEX)." {
		t.Fatalf("流语义错误掩盖连接压缩错误：%v", err)
	}
}

func TestChromiumHeaderSemanticsExtraPseudoAndDefaultDecoder(t *testing.T) {
	for _, chromium := range []bool{false, true} {
		var wire bytes.Buffer
		_ = NewFramer(&wire, nil).WriteRawFrame(FrameHeaders, FlagHeadersEndHeaders, 1, []byte{0x88, 0, 8, ':', 'u', 'n', 'k', 'n', 'o', 'w', 'n', 1, 'v'})
		fr := NewFramer(nil, &wire)
		fr.ReadMetaHeaders = hpack.NewDecoder(65536, nil)
		if chromium {
			fr.cc = uploadTestConn(io.Discard, 65535)
			fr.chromiumReadMeta = clienthpack.NewChromiumDecoder(65536, nil)
		}
		_, err := fr.ReadFrame()
		if chromium && err != nil {
			t.Fatalf("官方接受的额外伪字段被拒绝：%v", err)
		}
		var stream StreamError
		if !chromium && (!errors.As(err, &stream) || stream.Code != ErrCodeProtocol) {
			t.Fatalf("普通解码器的原有校验改变：%v", err)
		}
	}
}

func TestChromiumHeaderSemanticsQueuesResetBeforeErrorNotification(t *testing.T) {
	var wire bytes.Buffer
	cc := uploadTestConn(&wire, 65535)
	cc.uploadReadState(true)
	defer cc.uploadReadState(false)
	cs := uploadTestStream(cc, t.Context(), 1, nil, 0)
	cs.sentHeaders, cs.donec = true, make(chan struct{})
	err := StreamError{StreamID: 1, Code: ErrCodeProtocol}
	(&clientConnReadLoop{cc: cc}).endStreamError(cs, err)
	select {
	case <-cs.abort:
	default:
		t.Fatal("读取错误未通知请求")
	}
	cc.mu.Lock()
	queued := len(cc.uploadQueue) != 0
	cc.mu.Unlock()
	if !queued {
		t.Fatal("已通知请求失败，但 RST 仍依赖请求 goroutine 稍后投递")
	}
	// 清理再次尝试重置，不能给同一流发两次；RST 在流删除后仍须写出。
	cs.cleanupWriteRequest(err)
	cc.uploadReadState(false)
	waitUploadBatchWriter(t, cc)
	fr := NewFramer(nil, &wire)
	f, readErr := fr.ReadFrame()
	reset, ok := f.(*RSTStreamFrame)
	if readErr != nil || !ok || reset.StreamID != 1 || reset.ErrCode != ErrCodeProtocol {
		t.Fatalf("读取错误的 RST 丢失：%v %v", f, readErr)
	}
	if f, readErr := fr.ReadFrame(); readErr != io.EOF {
		t.Fatalf("请求清理重复发送 RST：%v %v", f, readErr)
	}
}
