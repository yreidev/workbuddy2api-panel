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
)

func TestChromiumDataFrameDeliversBeforePayloadComplete(t *testing.T) {
	for _, padding := range []int{-1, 0, 1, 255} {
		t.Run(fmt.Sprintf("padding=%d", padding), func(t *testing.T) {
			var wire bytes.Buffer
			w := NewFramer(&wire, nil)
			body := bytes.Repeat([]byte{'r'}, 12000)
			var pad []byte
			if padding >= 0 {
				pad = make([]byte, padding)
			}
			if err := w.WriteDataPadded(1, true, body, pad); err != nil {
				t.Fatal(err)
			}
			all := bytes.Clone(wire.Bytes())
			cut := 9 + 6000
			if padding >= 0 {
				cut++
			}
			raw, writer := io.Pipe()
			defer raw.Close()
			defer writer.Close()
			fr := NewFramer(nil, raw)
			fr.cc = &ClientConn{t: &Transport{ChromiumUpload: true}}
			fr.SetReuseFrames()
			written := make(chan error, 1)
			go func() { _, err := writer.Write(all[:cut]); written <- err }()
			var data []byte
			var flow uint32
			for len(data) < 6000 {
				done := make(chan error, 1)
				go func() {
					frame, err := fr.ReadFrame()
					if err == nil {
						f := frame.(*DataFrame)
						data = append(data, f.Data()...)
						flow += f.Length
						if f.StreamEnded() {
							err = errors.New("余下 payload 未发送就交付 END_STREAM")
						}
					}
					done <- err
				}()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("整帧未收齐时没有交付已收到的正文")
				}
			}
			if err := <-written; err != nil {
				t.Fatal(err)
			}
			go func() { _, err := writer.Write(all[cut:]); _ = writer.Close(); written <- err }()
			for {
				frame, err := fr.ReadFrame()
				if err != nil {
					t.Fatal(err)
				}
				f := frame.(*DataFrame)
				flow += f.Length
				data = append(data, f.Data()...)
				if f.StreamEnded() {
					break
				}
			}
			if !bytes.Equal(data, body) || flow != uint32(len(all)-9) {
				t.Fatalf("正文或接收流控损坏：body=%d flow=%d want=%d", len(data), flow, len(all)-9)
			}
			if err := <-written; err != nil {
				t.Fatal(err)
			}
			if _, err := fr.ReadFrame(); err != io.EOF {
				t.Fatalf("物理帧消费后多读了片段：%v", err)
			}
		})
	}
}

func TestChromiumDataFrameMalformedAndTruncated(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      uint32
		flags   Flags
		payload []byte
		code    ErrCode
		debug   string
	}{
		{"流零", 0, 0, []byte("r"), ErrCodeProtocol, "Framer error: 1 (INVALID_STREAM_ID)."},
		{"padding过大", 1, FlagDataPadded, []byte{2, 0}, ErrCodeProtocol, "Framer error: 5 (INVALID_PADDING)."},
		{"padding字节缺失", 1, FlagDataPadded, nil, ErrCodeProtocol, "Framer error: 6 (INVALID_DATA_FRAME_FLAGS)."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire bytes.Buffer
			_ = NewFramer(&wire, nil).WriteRawFrame(FrameData, tc.flags, tc.id, tc.payload)
			fr := NewFramer(nil, &wire)
			fr.cc = &ClientConn{t: &Transport{ChromiumUpload: true}}
			_, err := fr.ReadFrame()
			var code ConnectionError
			var detail uploadProtocolError
			if !errors.As(err, &code) || ErrCode(code) != tc.code || !errors.As(err, &detail) || detail.debugData != tc.debug {
				t.Fatalf("错误代码不同：%v", err)
			}
		})
	}
	var wire bytes.Buffer
	_ = NewFramer(&wire, nil).WriteData(1, true, []byte("incomplete"))
	raw := wire.Bytes()[:12]
	fr := NewFramer(nil, bytes.NewReader(raw))
	fr.cc = &ClientConn{t: &Transport{ChromiumUpload: true}}
	frame, err := fr.ReadFrame()
	if err != nil || string(frame.(*DataFrame).Data()) != "inc" || frame.(*DataFrame).StreamEnded() {
		t.Fatalf("截断前已到达的正文未交付：%v %v", frame, err)
	}
	if _, err := fr.ReadFrame(); err != io.ErrUnexpectedEOF {
		t.Fatalf("截断的 DATA 被当作成功结束：%v", err)
	}
}

func TestChromiumEmptyDataFrameAndPaddingKeepNextFrame(t *testing.T) {
	for _, padding := range []int{-1, 0, 255} {
		t.Run(fmt.Sprintf("padding=%d", padding), func(t *testing.T) {
			var wire bytes.Buffer
			var pad []byte
			if padding >= 0 {
				pad = make([]byte, padding)
			}
			w := NewFramer(&wire, nil)
			_ = w.WriteDataPadded(1, true, nil, pad)
			wantFlow := wire.Len() - 9
			_ = w.WritePing(false, [8]byte{})
			fr := NewFramer(nil, &wire)
			fr.cc = &ClientConn{t: &Transport{ChromiumUpload: true}}
			flow := 0
			for {
				frame, err := fr.ReadFrame()
				if err != nil {
					t.Fatal(err)
				}
				f, ok := frame.(*DataFrame)
				if !ok || len(f.Data()) != 0 {
					t.Fatalf("空正文返回了其他内容：%v", frame)
				}
				flow += int(f.Length)
				if f.StreamEnded() {
					break
				}
			}
			if flow != wantFlow {
				t.Fatalf("padding 记账不同：%d want=%d", flow, wantFlow)
			}
			frame, err := fr.ReadFrame()
			if _, ok := frame.(*PingFrame); err != nil || !ok {
				t.Fatalf("错误消费了下一帧：%v %v", frame, err)
			}
		})
	}
}

func TestChromiumDataFrameFlowControlUsesReceivedFragments(t *testing.T) {
	var wire bytes.Buffer
	_ = NewFramer(&wire, nil).WriteData(1, true, []byte("abcdefghijkl"))
	p := wire.Bytes()
	r := io.MultiReader(bytes.NewReader(p[:9]), bytes.NewReader(p[9:13]), bytes.NewReader(p[13:17]), bytes.NewReader(p[17:]))
	cc := uploadTestConn(io.Discard, 65535)
	cc.nextStreamID = 3
	cc.inflow.initChromium(8)
	cs := uploadTestStream(cc, t.Context(), 1, io.NopCloser(bytes.NewReader(nil)), 0)
	cs.pastHeaders = true
	cs.inflow.initChromium(8)
	cs.bufPipe.setBuffer(new(bytes.Buffer))
	fr := NewFramer(nil, r)
	fr.cc = cc
	rl := &clientConnReadLoop{cc: cc}
	for i := range 3 {
		frame, err := fr.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		err = rl.processData(frame.(*DataFrame))
		if i < 2 && err != nil {
			t.Fatalf("预扣了尚未到达的整帧字节：片段=%d err=%v", i, err)
		}
		if i == 2 {
			var code ConnectionError
			var detail uploadProtocolError
			if !errors.As(err, &code) || code != ConnectionError(ErrCodeFlowControl) || !errors.As(err, &detail) || detail.debugData != "delta_window_size is 4 in DecreaseRecvWindowSize, which is larger than the receive window size of 0" {
				t.Fatalf("收到超过窗口的片段后未准确拒绝：%v", err)
			}
		}
	}
	if cs.bufPipe.Len() != 8 {
		t.Fatalf("越过窗口交付了正文：%d", cs.bufPipe.Len())
	}
}

func TestChromiumDataStreamOverflowDoesNotDrainConnection(t *testing.T) {
	for _, padding := range []bool{false, true} {
		t.Run(fmt.Sprintf("padding=%t", padding), func(t *testing.T) {
			cc := uploadTestConn(io.Discard, 65535)
			cc.nextStreamID = 5
			cc.inflow.initChromium(32)
			cs := uploadTestStream(cc, t.Context(), 1, nil, 0)
			cs.pastHeaders = true
			cs.bytesRemain = -1
			cs.inflow.initChromium(1)
			cs.bufPipe.setBuffer(new(bytes.Buffer))
			f := &DataFrame{FrameHeader: FrameHeader{valid: true, StreamID: 1, Length: 2}, data: []byte("ab")}
			if padding {
				f.Flags = FlagDataPadded
				f.data = nil
			}
			if err := (&clientConnReadLoop{cc: cc}).processData(f); err != nil {
				t.Fatalf("单流超窗被升级为连接错误：%v", err)
			}
			cc.mu.Lock()
			var streamErr StreamError
			if !errors.As(cs.abortErr, &streamErr) || streamErr.Code != ErrCodeFlowControl || streamErr.StreamID != 1 || !cs.readAborted {
				t.Fatalf("未标记准确的流错误：%v", cs.abortErr)
			}
			if cc.closed || cc.uploadDrainErr != nil || cs.bufPipe.Len() != 0 || cs.inflow.avail != 1 || cc.inflow.avail+cc.inflow.unsent != 32 {
				t.Fatal("单流失败影响连接、交付了违规正文或错误扣除窗口")
			}
			cc.mu.Unlock()
			late := &DataFrame{FrameHeader: FrameHeader{valid: true, StreamID: 1, Length: 4, Flags: FlagDataEndStream}, data: []byte("late")}
			if err := (&clientConnReadLoop{cc: cc}).processData(late); err != nil || cs.bufPipe.Len() != 0 || cc.inflow.avail+cc.inflow.unsent != 32 {
				t.Fatalf("迟到正文影响连接、被交付或重复记账：%v", err)
			}
			other := uploadTestStream(cc, t.Context(), 3, nil, 0)
			other.pastHeaders = true
			other.inflow.initChromium(32)
			other.bufPipe.setBuffer(new(bytes.Buffer))
			if err := (&clientConnReadLoop{cc: cc}).processData(&DataFrame{FrameHeader: FrameHeader{valid: true, StreamID: 3, Length: 2}, data: []byte("ok")}); err != nil || other.bufPipe.Len() != 2 {
				t.Fatalf("另一流无法继续接收：%v", err)
			}
		})
	}
}

func TestChromiumDataConnectionOverflowRemainsConnectionError(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprintf("active=%t", active), func(t *testing.T) {
			cc := uploadTestConn(io.Discard, 65535)
			cc.nextStreamID = 3
			cc.inflow.initChromium(1)
			var cs *clientStream
			if active {
				cs = uploadTestStream(cc, t.Context(), 1, nil, 0)
				cs.pastHeaders = true
				cs.inflow.initChromium(32)
				cs.bufPipe.setBuffer(new(bytes.Buffer))
			}
			f := &DataFrame{FrameHeader: FrameHeader{valid: true, StreamID: 1, Length: 2}, data: []byte("ab")}
			err := (&clientConnReadLoop{cc: cc}).processData(f)
			var code ConnectionError
			var detail uploadProtocolError
			if !errors.As(err, &code) || code != ConnectionError(ErrCodeFlowControl) || !errors.As(err, &detail) || detail.debugData != "delta_window_size is 2 in DecreaseRecvWindowSize, which is larger than the receive window size of 1" {
				t.Fatalf("连接超窗没有保留连接错误：%v", err)
			}
			if cc.inflow.avail != 1 || cc.inflow.unsent != 0 || cs != nil && (cs.abortErr != nil || cs.inflow.avail != 32 || cs.bufPipe.Len() != 0) {
				t.Fatal("连接超窗后仍交付正文、归还未消费额度或误记为单流错误")
			}
		})
	}
}
