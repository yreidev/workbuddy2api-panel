// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func waitUploadTaskQueue(t *testing.T, cc *ClientConn) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		cc.mu.Lock()
		done := len(cc.uploadEvents) == 0 && !cc.uploadTaskRunning
		cc.mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("网络任务队列没有让出")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestChromiumUploadCloseIdleDrainsPostedTasks(t *testing.T) {
	var wire bytes.Buffer
	cc := uploadTestConn(&wire, 65535)
	raw, peer := net.Pipe()
	defer raw.Close()
	defer peer.Close()
	cc.tconn, cc.nextStreamID, cc.maxConcurrentStreams = raw, 3, 100
	cc.uploadReadState(true)
	defer cc.uploadReadState(false)
	cc.mu.Lock()
	cc.queueUploadControlLocked(func() error { return cc.fr.WritePing(true, [8]byte{1}) }, false)
	cc.mu.Unlock()
	cc.closeIfIdle()
	cc.closeIfIdle()
	cc.mu.Lock()
	reusable, closed := cc.canTakeNewRequestLocked(), cc.closed
	cc.mu.Unlock()
	if reusable || closed {
		t.Fatalf("空闲回收须先禁止复用并等待已投递 ACK：reusable=%t closed=%t", reusable, closed)
	}
	cc.uploadReadState(false)
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("任务排空后未关闭连接：%v", err)
	}
	cc.mu.Lock()
	closed = cc.closed
	cc.mu.Unlock()
	if !closed {
		t.Fatal("关闭 socket 前未更新连接状态")
	}
	fr := NewFramer(nil, &wire)
	f, err := fr.ReadFrame()
	ping, ok := f.(*PingFrame)
	if err != nil || !ok || !ping.IsAck() || ping.Data != [8]byte{1} {
		t.Fatalf("空闲回收丢失已投递 ACK：%v %v", f, err)
	}
	if f, err := fr.ReadFrame(); err != io.EOF {
		t.Fatalf("空闲回收不应追加 GOAWAY：%v %v", f, err)
	}
}

func TestChromiumUploadCloseIdlePreservesActiveRequests(t *testing.T) {
	for _, state := range []string{"活动流", "预留请求", "待分配流"} {
		t.Run(state, func(t *testing.T) {
			cc := uploadTestConn(io.Discard, 65535)
			raw, peer := net.Pipe()
			defer raw.Close()
			defer peer.Close()
			cc.tconn, cc.nextStreamID, cc.maxConcurrentStreams = raw, 3, 100
			switch state {
			case "活动流":
				cc.streams[1] = &clientStream{}
			case "预留请求":
				cc.streamsReserved = 1
			case "待分配流":
				cc.uploadOpening = 1
			}
			cc.uploadReadState(true)
			defer cc.uploadReadState(false)
			cc.mu.Lock()
			cc.queueUploadControlLocked(func() error { return cc.fr.WritePing(true, [8]byte{}) }, false)
			cc.mu.Unlock()
			cc.closeIfIdle()
			cc.uploadReadState(false)
			waitUploadTaskQueue(t, cc)
			cc.mu.Lock()
			reusable, closing, closed := cc.canTakeNewRequestLocked(), cc.closing, cc.closed
			cc.mu.Unlock()
			if !reusable || closing || closed {
				t.Fatalf("空闲回收影响在途请求：reusable=%t closing=%t closed=%t", reusable, closing, closed)
			}
		})
	}
}

// 官方 TTNet 的受控采样：正文完成先投递，随后同一读批次补连接窗口
// 1／32768。已排出的 B 一字节之后，必须先发 A，再发 B 的剩余缓冲。
func TestChromiumUploadBodyCompletionBeforePostedWrite(t *testing.T) {
	for _, bodyFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("正文先投递=%t", bodyFirst), func(t *testing.T) {
			var wire bytes.Buffer
			cc := uploadTestConn(&wire, 0)
			cc.uploadReadState(true)
			defer cc.uploadReadState(false)
			b := uploadTestStream(cc, t.Context(), 5, &readyUploadTestBody{Reader: strings.NewReader(strings.Repeat("b", 16343))}, 16343)
			aBody := &asyncUploadEventTestBody{release: make(chan struct{})}
			defer aBody.Close()
			a := uploadTestStream(cc, t.Context(), 3, aBody, -1)
			cc.mu.Lock()
			bu := b.startQueuedUploadLocked(&http.Request{}, nil)
			au := a.startQueuedUploadLocked(&http.Request{}, nil)
			cc.mu.Unlock()
			postBody := func() {
				t.Helper()
				aBody.Close()
				deadline := time.Now().Add(2 * time.Second)
				for {
					cc.mu.Lock()
					posted := slices.ContainsFunc(cc.uploadEvents, func(e uploadEvent) bool { return e.body == au })
					cc.mu.Unlock()
					if posted {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("A 正文完成没有投递到网络任务队列")
					}
					time.Sleep(time.Millisecond)
				}
			}
			if bodyFirst {
				postBody()
			}
			rl := &clientConnReadLoop{cc: cc}
			for _, n := range []uint32{1, 32768} {
				if err := rl.processWindowUpdate(&WindowUpdateFrame{Increment: n}); err != nil {
					t.Fatal(err)
				}
			}
			if !bodyFirst {
				postBody()
			}
			cc.uploadReadState(false)
			for _, u := range []*queuedUpload{au, bu} {
				if err := awaitUploadTest(t, u.done); err != nil {
					t.Fatal(err)
				}
			}
			waitUploadBatchWriter(t, cc)
			fr := NewFramer(nil, bytes.NewReader(wire.Bytes()))
			wants := []struct {
				id uint32
				n  int
			}{{5, 1}, {3, 1}, {5, 16342}}
			if !bodyFirst {
				wants[1], wants[2] = wants[2], wants[1]
			}
			for _, want := range wants {
				frame, err := fr.ReadFrame()
				data, ok := frame.(*DataFrame)
				if err != nil || !ok || data.StreamID != want.id || len(data.Data()) != want.n {
					t.Fatalf("已投递正文任务被写循环越过：want=%+v frame=%v err=%v", want, frame, err)
				}
			}
		})
	}
}

func TestChromiumUploadClosedTaskQueueReleasesWaiters(t *testing.T) {
	var wire bytes.Buffer
	cc := uploadTestConn(&wire, 65535)
	raw, peer := net.Pipe()
	defer peer.Close()
	cc.tconn = raw
	cc.uploadReadState(true)
	cc.uploadOpening = 1
	h := &queuedUploadHeaders{initial: true, done: make(chan error, 1)}
	read, write := make(chan struct{}), make(chan struct{})
	cc.mu.Lock()
	cc.postUploadTaskLocked(uploadEvent{headers: h})
	cc.postUploadTaskLocked(uploadEvent{readReady: read})
	cc.postUploadTaskLocked(uploadEvent{writeReady: write})
	cc.mu.Unlock()
	failure := errors.New("fixture queue close")
	cc.closeForError(failure)
	if err := awaitUploadTest(t, h.done); !errors.Is(err, failure) {
		t.Fatalf("待发 HEADERS 没有保留关闭原因：%v", err)
	}
	for _, ready := range []<-chan struct{}{read, write} {
		select {
		case <-ready:
		case <-time.After(time.Second):
			t.Fatal("连接关闭没有唤醒已投递的读写恢复")
		}
	}
	waitUploadTaskQueue(t, cc)
	if wire.Len() != 0 || cc.uploadOpening != 0 {
		t.Fatal("关闭后仍有输出或占用开启流槽位")
	}
}

func TestChromiumUploadCancelledBodyTaskDoesNotReserveWindow(t *testing.T) {
	cc := uploadTestConn(io.Discard, 7)
	cc.uploadReadState(true)
	defer cc.uploadReadState(false)
	cs := uploadTestStream(cc, t.Context(), 1, io.NopCloser(strings.NewReader("cancel")), 6)
	cc.mu.Lock()
	u := cs.newQueuedUploadLocked(&http.Request{}, nil)
	cc.postUploadTaskLocked(uploadEvent{body: u, n: 1, err: io.EOF})
	cs.abortStreamLocked(context.Canceled)
	cc.mu.Unlock()
	cc.uploadReadState(false)
	waitUploadTaskQueue(t, cc)
	if err := awaitUploadTest(t, u.done); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消原因丢失：%v", err)
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.flow.n != 7 || len(cc.uploadQueue) != 0 || u.active {
		t.Fatal("已取消的正文完成任务仍预留窗口或恢复了上传")
	}
}
