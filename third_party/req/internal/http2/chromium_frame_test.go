// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

func TestChromiumInvalidFrameHeaderDoesNotWaitForPayload(t *testing.T) {
	for _, tc := range []struct {
		header string
		code   ErrCode
		debug  string
	}{
		{"004001000000000001", ErrCodeFrameSize, "Framer error: 10 (OVERSIZED_PAYLOAD)."},
		{"004001060000000000", ErrCodeFrameSize, "Framer error: 3 (CONTROL_PAYLOAD_TOO_LARGE)."},
		{"000008060000000001", ErrCodeProtocol, "Framer error: 1 (INVALID_STREAM_ID)."},
		{"000007070000000000", ErrCodeProtocol, "Framer error: 2 (INVALID_CONTROL_FRAME)."},
		{"000001040000000000", ErrCodeFrameSize, "Framer error: 9 (INVALID_CONTROL_FRAME_SIZE)."},
	} {
		t.Run(tc.header, func(t *testing.T) {
			wire, err := hex.DecodeString(tc.header)
			if err != nil {
				t.Fatal(err)
			}
			fr := NewFramer(nil, bytes.NewReader(wire))
			fr.SetMaxReadFrameSize(16384)
			fr.cc = uploadTestConn(io.Discard, 65535)
			_, err = fr.ReadFrame()
			var code ConnectionError
			var detail uploadProtocolError
			if !errors.As(err, &code) || ErrCode(code) != tc.code || !errors.As(err, &detail) || detail.debugData != tc.debug || !strings.Contains(err.Error(), tc.debug) {
				t.Fatalf("帧头已确定非法时仍等正文或分类不同：%v", err)
			}
		})
	}
}

func TestChromiumOutOfRangeInitialWindowIsIgnored(t *testing.T) {
	for _, chromium := range []bool{false, true} {
		var wire bytes.Buffer
		_ = NewFramer(&wire, nil).WriteRawFrame(FrameSettings, 0, 0, []byte{0, 4, 128, 0, 0, 0})
		cc := uploadTestConn(io.Discard, 65535)
		cc.t.ChromiumUpload = chromium
		cc.initialWindowSize = 65535
		cs := uploadTestStream(cc, t.Context(), 1, nil, 0)
		fr := NewFramer(nil, &wire)
		fr.cc = cc
		frame, err := fr.ReadFrame()
		if !chromium {
			if err != ConnectionError(ErrCodeFlowControl) {
				t.Fatalf("普通路径的原始错误改变：%v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		cc.mu.Lock()
		err = (&clientConnReadLoop{cc: cc}).processSettingsLocked(frame.(*SettingsFrame))
		cc.mu.Unlock()
		if err != nil || cc.initialWindowSize != 65535 || cs.flow.n != 65535 || cs.abortErr != nil {
			t.Fatalf("忽略非法设置时改变窗口或终止请求：window=%d stream=%d err=%v", cc.initialWindowSize, cs.flow.n, err)
		}
	}
}

func TestChromiumContinuationZeroStreamPrecedesFrameOrderError(t *testing.T) {
	for _, previous := range []uint32{0, 1} {
		wire, err := hex.DecodeString("000000090400000000")
		if err != nil {
			t.Fatal(err)
		}
		fr := NewFramer(nil, bytes.NewReader(wire))
		fr.cc = uploadTestConn(io.Discard, 65535)
		fr.lastHeaderStream = previous
		_, err = fr.ReadFrame()
		var detail uploadProtocolError
		if !errors.As(err, &detail) || detail.ConnectionError != ConnectionError(ErrCodeProtocol) || detail.debugData != "Framer error: 1 (INVALID_STREAM_ID)." {
			t.Fatalf("CONTINUATION 的非法流编号被顺序错误覆盖：previous=%d err=%v", previous, err)
		}
	}
}

func TestChromiumSettingsOverflowReportsFirstStream(t *testing.T) {
	cc := uploadTestConn(io.Discard, 65535)
	cc.initialWindowSize = 65535
	for _, id := range []uint32{5, 3, 1} {
		cs := uploadTestStream(cc, t.Context(), id, nil, 0)
		cs.flow.n = math.MaxInt32
	}
	f := &SettingsFrame{FrameHeader: FrameHeader{valid: true}, p: []byte{0, 4, 0, 1, 0, 0}}
	cc.mu.Lock()
	err := (&clientConnReadLoop{cc: cc}).processSettingsLocked(f)
	cc.mu.Unlock()
	var detail uploadProtocolError
	if !errors.As(err, &detail) || detail.ConnectionError != ConnectionError(ErrCodeFlowControl) || detail.debugData != "New spdy::SETTINGS_INITIAL_WINDOW_SIZE value overflows flow control window of stream 1." {
		t.Fatalf("SETTINGS 溢出没有按官方次序报告：%v", err)
	}
	for _, cs := range cc.streams {
		if cs.flow.n != math.MaxInt32 {
			t.Fatal("溢出后仍更改其他流窗口")
		}
	}
}
