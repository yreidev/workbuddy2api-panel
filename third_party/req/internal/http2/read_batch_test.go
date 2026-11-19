// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitUploadBatchWriter(t *testing.T, cc *ClientConn) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		cc.mu.Lock()
		for cc.uploadWriting {
			cc.cond.Wait()
		}
		cc.mu.Unlock()
		done <- nil
	}()
	_ = awaitUploadTest(t, done)
}

// 官方同批补窗／PING 先排 ACK；分成两个读批次时，第一批 DATA 可先发。
// 输入批次由测试明确控制，逐字节锁定结果，不列举任意 socket 时序的允许向量。
func TestChromiumUploadReadBatchWireOrder(t *testing.T) {
	for _, together := range []bool{false, true} {
		t.Run(fmt.Sprintf("同批=%t", together), func(t *testing.T) {
			var wire bytes.Buffer
			cc := uploadTestConn(&wire, 0)
			cc.uploadReadState(true)
			cc.mu.Lock()
			a := uploadTestStreamLocked(cc, t, 1, "abcd")
			b := uploadTestStreamLocked(cc, t, 3, "efgh")
			cc.mu.Unlock()
			rl := &clientConnReadLoop{cc: cc}
			window := uint32(4)
			if together {
				window = 8
			}
			if err := rl.processWindowUpdate(&WindowUpdateFrame{Increment: window}); err != nil {
				t.Fatal(err)
			}
			if !together {
				cc.uploadReadState(false)
				waitUploadBatchWriter(t, cc)
				cc.uploadReadState(true)
				if err := rl.processWindowUpdate(&WindowUpdateFrame{Increment: 4}); err != nil {
					t.Fatal(err)
				}
			}
			before := wire.Len()
			ping := [8]byte{'f', 'i', 'x', 't', 'u', 'r', 'e', '!'}
			if err := rl.processPing(&PingFrame{Data: ping}); err != nil {
				t.Fatal(err)
			}
			cc.mu.Lock()
			writing := cc.uploadWriteOwner
			cc.mu.Unlock()
			if writing || wire.Len() != before {
				t.Fatal("读批次尚未让出就启动了写任务")
			}
			cc.uploadReadState(false)
			for _, u := range []*queuedUpload{a, b} {
				if err := awaitUploadTest(t, u.done); err != nil {
					t.Fatal(err)
				}
			}
			waitUploadBatchWriter(t, cc)
			var expected bytes.Buffer
			fr := NewFramer(&expected, nil)
			if !together {
				_ = fr.WriteData(1, true, []byte("abcd"))
			}
			_ = fr.WritePing(true, ping)
			if together {
				_ = fr.WriteData(1, true, []byte("abcd"))
			}
			_ = fr.WriteData(3, true, []byte("efgh"))
			if !bytes.Equal(wire.Bytes(), expected.Bytes()) {
				t.Fatalf("批次的精确帧序不同：got=%x want=%x", wire.Bytes(), expected.Bytes())
			}
		})
	}
}

func uploadTestStreamLocked(cc *ClientConn, t *testing.T, id uint32, body string) *queuedUpload {
	cs := &clientStream{cc: cc, ctx: t.Context(), ID: id, reqBody: &readyUploadTestBody{Reader: strings.NewReader(body)}, reqBodyContentLength: int64(len(body)), abort: make(chan struct{})}
	cs.flow.n = int32(len(body))
	cs.flow.setConnFlow(&cc.flow)
	cc.streams[id] = cs
	return cs.startQueuedUploadLocked(&http.Request{}, nil)
}

func TestChromiumUploadReadBatchYield(t *testing.T) {
	for _, reason := range []string{"字节上限", "时间上限", "读取结束"} {
		t.Run(reason, func(t *testing.T) {
			var wire bytes.Buffer
			cc := uploadTestConn(&wire, 65535)
			r := &uploadBatchReader{r: strings.NewReader(strings.Repeat("r", 64*1024)), cc: cc}
			defer r.idle()
			p := make([]byte, 16384)
			if n, err := r.Read(p); n != 8192 || err != nil {
				t.Fatalf("单次明文读取不符合 8 KiB：%d %v", n, err)
			}
			if err := (&clientConnReadLoop{cc: cc}).processPing(&PingFrame{}); err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "字节上限":
				// 单独验证字节预算，避免慢机器先触发时间预算。
				r.since = time.Now().Add(time.Hour)
				for range 4 {
					if _, err := r.Read(p); err != nil {
						t.Fatal(err)
					}
				}
				if wire.Len() != 0 {
					t.Fatal("未超过字节预算就让出了读批次")
				}
			case "时间上限":
				r.since = time.Now().Add(-21 * time.Millisecond)
			case "读取结束":
				r.r = strings.NewReader("")
			}
			if _, err := r.Read(p); err != nil && err != io.EOF {
				t.Fatal(err)
			}
			if reason == "读取结束" {
				waitUploadBatchWriter(t, cc)
			}
			if wire.Len() != 17 {
				t.Fatalf("读循环让出后没有运行待发 ACK：%d", wire.Len())
			}
		})
	}
}

type uploadBatchBody struct {
	*readyUploadTestBody
	reads atomic.Int32
}

func (b *uploadBatchBody) ReadAvailable(p []byte) (int, error, bool) {
	b.reads.Add(1)
	return b.readyUploadTestBody.ReadAvailable(p)
}

type uploadBatchPendingWriter struct {
	bytes.Buffer
	entered, release, written chan struct{}
	once                      sync.Once
}

func (w *uploadBatchPendingWriter) Write(p []byte) (int, error) {
	first := false
	w.once.Do(func() { first = true; close(w.entered); <-w.release })
	n, err := w.Buffer.Write(p)
	if first {
		close(w.written)
	}
	return n, err
}

func TestChromiumUploadWriteCompletionWaitsForReadBatch(t *testing.T) {
	w := &uploadBatchPendingWriter{entered: make(chan struct{}), release: make(chan struct{}), written: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(w.release) }) }
	defer unblock()
	cc := uploadTestConn(w, 65535)
	defer cc.uploadReadState(false)
	body := &uploadBatchBody{readyUploadTestBody: &readyUploadTestBody{Reader: strings.NewReader(strings.Repeat("a", 32750))}}
	cs := uploadTestStream(cc, t.Context(), 1, body, 32750)
	done := make(chan error, 1)
	go func() { done <- cs.writeRequestBody(&http.Request{}, nil) }()
	<-w.entered
	cc.uploadWriteState(true)
	cc.uploadReadState(true)
	unblock()
	<-w.written
	cc.uploadWriteState(false)
	select {
	case err := <-done:
		t.Fatalf("读批次仍在处理时已完成下一块正文：%v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if body.reads.Load() != 1 {
		t.Fatalf("写完成越过了读批次：reads=%d", body.reads.Load())
	}
	cc.uploadReadState(false)
	if err := awaitUploadTest(t, done); err != nil {
		t.Fatal(err)
	}
	waitUploadBatchWriter(t, cc)
}
