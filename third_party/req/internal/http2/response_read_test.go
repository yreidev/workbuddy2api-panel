// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func responseReadTestStream(t *testing.T, size int) (*clientConnReadLoop, *chromiumResponseRead) {
	t.Helper()
	cc := uploadTestConn(io.Discard, 65535)
	cc.inflow.initChromium(65535)
	cs := uploadTestStream(cc, t.Context(), 1, nil, 0)
	cs.inflow.initChromium(65535)
	cs.bytesRemain = -1
	cs.pastHeaders = true
	cs.peerClosed = make(chan struct{})
	cs.bufPipe.setBuffer(new(bytes.Buffer))
	r := &chromiumResponseRead{cs: cs, buffer: make([]byte, size), done: make(chan responseReadResult, 1)}
	cc.mu.Lock()
	r.beginLocked()
	cc.mu.Unlock()
	return &clientConnReadLoop{cc: cc}, r
}

func responseReadTestData(t *testing.T, rl *clientConnReadLoop, body string, end bool) {
	t.Helper()
	f := &DataFrame{FrameHeader: FrameHeader{valid: true, StreamID: 1, Length: uint32(len(body))}, data: []byte(body)}
	if end {
		f.Flags |= FlagDataEndStream
	}
	if err := rl.processData(f); err != nil {
		t.Fatal(err)
	}
}

func awaitResponseRead(t *testing.T, r *chromiumResponseRead) responseReadResult {
	t.Helper()
	select {
	case result := <-r.done:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("响应读取没有完成")
		return responseReadResult{}
	}
}

func TestChromiumResponseReadCoalescesWithinReadBatch(t *testing.T) {
	rl, r := responseReadTestStream(t, 8192)
	cc := rl.cc
	cc.uploadReadState(true)
	defer cc.uploadReadState(false)
	for range 600 {
		responseReadTestData(t, rl, "r", false)
	}
	select {
	case result := <-r.done:
		t.Fatalf("读批次尚未让出就通知应用：%+v", result)
	default:
	}
	cc.uploadReadState(false)
	result := awaitResponseRead(t, r)
	if result.n != 600 || result.err != nil || !bytes.Equal(r.buffer[:600], bytes.Repeat([]byte{'r'}, 600)) {
		t.Fatalf("没有合并小 DATA：n=%d err=%v", result.n, result.err)
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.inflow.unsent != 600 || r.cs.inflow.unsent != 600 || r.cs.responseRead != nil || r.timer != nil {
		t.Fatal("消费记账不准确或挂起读取未清理")
	}
}

func TestChromiumResponseReadFullBufferAndAlreadyBuffered(t *testing.T) {
	rl, r := responseReadTestStream(t, 4)
	cc := rl.cc
	cc.uploadReadState(true)
	defer cc.uploadReadState(false)
	responseReadTestData(t, rl, "abcde", false)
	cc.mu.Lock()
	if r.timer != nil {
		t.Fatal("填满缓冲仍使用一毫秒定时器")
	}
	cc.mu.Unlock()
	select {
	case <-r.done:
		t.Fatal("零延迟回调没有经过网络任务队列")
	default:
	}
	cc.uploadReadState(false)
	result := awaitResponseRead(t, r)
	if result.n != 4 || result.err != nil || string(r.buffer) != "abcd" {
		t.Fatalf("填满缓冲的回调错误：%+v %q", result, r.buffer)
	}
	next := &chromiumResponseRead{cs: r.cs, buffer: make([]byte, 10), done: make(chan responseReadResult, 1)}
	cc.mu.Lock()
	next.beginLocked()
	cc.mu.Unlock()
	select {
	case result := <-next.done:
		if result.n != 1 || result.err != nil || next.buffer[0] != 'e' {
			t.Fatalf("已有缓冲读取错误：%+v", result)
		}
	default:
		t.Fatal("已有缓冲被当作异步读取")
	}
}

func TestChromiumResponseReadIgnoresSupersededCallback(t *testing.T) {
	rl, r := responseReadTestStream(t, 16)
	cc := rl.cc
	cc.uploadReadState(true)
	defer cc.uploadReadState(false)
	responseReadTestData(t, rl, "a", false)
	cc.mu.Lock()
	old := r.version
	cc.mu.Unlock()
	responseReadTestData(t, rl, "b", false)
	cc.mu.Lock()
	r.notifyLocked(old)
	cc.mu.Unlock()
	select {
	case <-r.done:
		t.Fatal("新片段到达后仍执行旧的读取回调")
	default:
	}
	cc.uploadReadState(false)
	result := awaitResponseRead(t, r)
	if result.n != 2 || result.err != nil || string(r.buffer[:2]) != "ab" {
		t.Fatalf("重设后的读取回调错误：%+v", result)
	}
}

func TestChromiumResponseReadEndAndCancellationStopTimer(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "正常结束", true: "取消"}[cancelRead], func(t *testing.T) {
			rl, r := responseReadTestStream(t, 16)
			cc := rl.cc
			cc.uploadReadState(true)
			defer cc.uploadReadState(false)
			responseReadTestData(t, rl, "ab", false)
			if cancelRead {
				cc.mu.Lock()
				r.cs.abortStreamLocked(context.Canceled)
				cc.mu.Unlock()
			} else {
				responseReadTestData(t, rl, "", true)
			}
			result := awaitResponseRead(t, r)
			if cancelRead {
				if result.n != 0 || !errors.Is(result.err, context.Canceled) {
					t.Fatalf("取消等待定时器或返回了旧数据：%+v", result)
				}
			} else if result.n != 2 || result.err != nil || string(r.buffer[:2]) != "ab" {
				t.Fatalf("正常结束没有立即交付剩余正文：%+v", result)
			}
			cc.mu.Lock()
			if r.timer != nil || r.cs.responseRead != nil {
				t.Fatal("结束后仍保留定时器或挂起读取")
			}
			cc.mu.Unlock()
			cc.uploadReadState(false)
			waitUploadTaskQueue(t, cc)
			select {
			case <-r.done:
				t.Fatal("结束后又执行了一次旧读取回调")
			default:
			}
		})
	}
}

func TestChromiumResponseQueuedReadSurvivesConnectionClose(t *testing.T) {
	rl, pending := responseReadTestStream(t, 1)
	cc := rl.cc
	raw, peer := net.Pipe()
	defer peer.Close()
	cc.tconn = raw
	cc.uploadReadState(true)
	next := &chromiumResponseRead{cs: pending.cs, buffer: make([]byte, 1), done: make(chan responseReadResult, 1)}
	cc.mu.Lock()
	cc.postUploadTaskLocked(uploadEvent{responseRead: next})
	cc.mu.Unlock()
	failure := errors.New("fixture response connection close")
	cc.closeForError(failure)
	for _, r := range []*chromiumResponseRead{pending, next} {
		if result := awaitResponseRead(t, r); result.n != 0 || !errors.Is(result.err, failure) {
			t.Fatalf("关闭没有保留错误并唤醒响应读取：%+v", result)
		}
	}
	waitUploadTaskQueue(t, cc)
}

func TestChromiumResponseReadUsesStreamEndInsteadOfContentLength(t *testing.T) {
	for _, tooLong := range []bool{false, true} {
		t.Run(map[bool]string{false: "提前结束", true: "超过声明长度"}[tooLong], func(t *testing.T) {
			rl, r := responseReadTestStream(t, 16)
			cc := rl.cc
			cc.mu.Lock()
			r.cs.bytesRemain = 4
			if tooLong {
				r.cs.bytesRemain = 1
			}
			cc.mu.Unlock()
			cc.uploadReadState(true)
			defer cc.uploadReadState(false)
			responseReadTestData(t, rl, "ab", true)
			first := awaitResponseRead(t, r)
			if first.n != 2 || first.err != nil || string(r.buffer[:first.n]) != "ab" {
				t.Fatalf("Content-Length 改变了官方按流结束交付的正文：%+v", first)
			}
			next := &chromiumResponseRead{cs: r.cs, buffer: make([]byte, 16), done: make(chan responseReadResult, 1)}
			cc.mu.Lock()
			next.beginLocked()
			cc.mu.Unlock()
			result := awaitResponseRead(t, next)
			if result.n != 0 || result.err != io.EOF {
				t.Fatalf("正常流结束被误判为长度错误：%+v", result)
			}
		})
	}
}

func TestResponseReadContentLengthErrorsRemainVisibleWithoutChromium(t *testing.T) {
	for _, limit := range []int64{1, 4} {
		cc := uploadTestConn(io.Discard, 65535)
		cc.t.ChromiumUpload = false
		cs := uploadTestStream(cc, t.Context(), 1, nil, 0)
		cs.bufPipe.setBuffer(bytes.NewBufferString("ab"))
		cs.bufPipe.CloseWithError(io.EOF)
		cs.bytesRemain = limit
		body := transportResponseBody{cs: cs}
		data, err := io.ReadAll(body)
		if limit == 1 {
			if string(data) != "a" || err == nil {
				t.Fatalf("普通 req 超长正文检查改变：data=%q err=%v", data, err)
			}
		} else if string(data) != "ab" || err != io.ErrUnexpectedEOF {
			t.Fatalf("普通 req 提前结束检查改变：data=%q err=%v", data, err)
		}
	}
}

func TestChromiumResponseWindowUpdateRequiresActiveStream(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		localEnd, remoteEnd, remove bool
		wantStream                  bool
	}{
		{name: "活动响应", localEnd: true, wantStream: true},
		{name: "远端半关闭", remoteEnd: true, wantStream: true},
		{name: "双向结束", localEnd: true, remoteEnd: true},
		{name: "流已移除", remove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rl, read := responseReadTestStream(t, 16)
			cc, cs := rl.cc, read.cs
			var wire bytes.Buffer
			cc.bw.Reset(&wire)
			cc.uploadReadState(true)
			defer cc.uploadReadState(false)
			cc.mu.Lock()
			cs.uploadEnded = tc.localEnd
			cc.inflow.lastUpdate = time.Now().Add(-6 * time.Second)
			cs.inflow.lastUpdate = cc.inflow.lastUpdate
			cc.mu.Unlock()
			responseReadTestData(t, rl, "r", tc.remoteEnd)
			cc.mu.Lock()
			if tc.remove {
				delete(cc.streams, cs.ID)
			}
			if !tc.remoteEnd {
				read.completeLocked()
			}
			cc.mu.Unlock()
			if result := awaitResponseRead(t, read); result.n != 1 || result.err != nil {
				t.Fatalf("正文交付错误：%+v", result)
			}
			cc.uploadReadState(false)
			waitUploadTaskQueue(t, cc)
			waitUploadBatchWriter(t, cc)
			frames := NewFramer(nil, bytes.NewReader(wire.Bytes()))
			wantIDs := []uint32{0}
			if tc.wantStream {
				wantIDs = append(wantIDs, cs.ID)
			}
			for _, id := range wantIDs {
				frame, err := frames.ReadFrame()
				update, ok := frame.(*WindowUpdateFrame)
				if err != nil || !ok || update.StreamID != id || update.Increment != 1 {
					t.Fatalf("补窗不同：want=%d/1 frame=%v err=%v", id, frame, err)
				}
			}
			if frame, err := frames.ReadFrame(); err != io.EOF {
				t.Fatalf("已关闭流仍补了窗口：%v err=%v", frame, err)
			}
		})
	}
}
