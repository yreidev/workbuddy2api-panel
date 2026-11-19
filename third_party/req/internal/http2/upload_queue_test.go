// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	clienthpack "github.com/imroc/req/v3/internal/hpack"
	"github.com/imroc/req/v3/internal/transport"
)

type readyUploadTestBody struct {
	*strings.Reader
	read chan struct{}
	once sync.Once
}

func (b *readyUploadTestBody) Close() error { return nil }
func (b *readyUploadTestBody) ReadAvailable(p []byte) (int, error, bool) {
	if b.read != nil {
		b.once.Do(func() { close(b.read) })
	}
	n, err := b.Read(p)
	return n, err, true
}

func uploadTestConn(w io.Writer, window int32) *ClientConn {
	cc := &ClientConn{t: &Transport{Options: &transport.Options{}, ChromiumUpload: true, MaxUploadFrameSize: 16375}, maxFrameSize: 16384, streams: make(map[uint32]*clientStream), peerMaxHeaderListSize: 65536}
	cc.cond = sync.NewCond(&cc.mu)
	cc.flow.n = window
	cc.bw = bufio.NewWriter(w)
	cc.fr = NewFramer(cc.bw, nil)
	cc.henc = clienthpack.NewEncoder(&cc.hbuf)
	return cc
}

func uploadTestStream(cc *ClientConn, ctx context.Context, id uint32, body io.ReadCloser, length int64) *clientStream {
	cs := &clientStream{cc: cc, ctx: ctx, ID: id, reqBody: body, reqBodyContentLength: length, abort: make(chan struct{})}
	cs.flow.n = 65535
	cs.flow.setConnFlow(&cc.flow)
	cc.mu.Lock()
	cc.streams[id] = cs
	cc.mu.Unlock()
	return cs
}

func awaitUploadTest(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("上传循环未退出")
		return nil
	}
}

type pendingHeaderUploadTestBody struct {
	*readyUploadTestBody
	release     chan struct{}
	releaseOnce sync.Once
}

func (b *pendingHeaderUploadTestBody) ReadAvailable([]byte) (int, error, bool) {
	b.once.Do(func() { close(b.read) })
	return 0, nil, false
}
func (b *pendingHeaderUploadTestBody) Read(p []byte) (int, error) {
	<-b.release
	return b.Reader.Read(p)
}
func (b *pendingHeaderUploadTestBody) Close() error {
	b.releaseOnce.Do(func() { close(b.release) })
	return nil
}

func TestChromiumUploadStartsBodyWithHeaders(t *testing.T) {
	for _, tc := range []struct {
		name     string
		length   int64
		trailers bool
		wait100  bool
		pending  bool
		cancel   bool
		wantErr  error
	}{
		{name: "固定正文", length: 4},
		{name: "未知长度", length: -1},
		{name: "尾部字段", length: 4, trailers: true},
		{name: "长度超限", length: 1, wantErr: errReqBodyTooLong},
		{name: "等待继续", length: 4, wait100: true},
		{name: "异步读取", length: -1, pending: true},
		{name: "继续前取消", length: 4, wait100: true, cancel: true, wantErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire bytes.Buffer
			cc := uploadTestConn(&wire, 65535)
			cc.reqHeaderMu = make(chan struct{}, 1)
			cc.nextStreamID, cc.maxConcurrentStreams, cc.initialWindowSize = 1, 1, 65535
			ready := &readyUploadTestBody{Reader: strings.NewReader("body"), read: make(chan struct{})}
			var body io.ReadCloser = ready
			if tc.pending {
				body = &pendingHeaderUploadTestBody{readyUploadTestBody: ready, release: make(chan struct{})}
				t.Cleanup(func() { _ = body.Close() })
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://sample.invalid/body", body)
			if err != nil {
				t.Fatal(err)
			}
			req.ContentLength = tc.length
			if tc.trailers {
				req.Trailer = http.Header{"X-Final": {"done"}}
			}
			if tc.wait100 {
				cc.t.ExpectContinueTimeout = time.Hour
				req.Header.Set("Expect", "100-continue")
			}
			cs := &clientStream{cc: cc, ctx: req.Context(), reqBody: body, reqBodyContentLength: tc.length, abort: make(chan struct{}), peerClosed: make(chan struct{})}
			close(cs.peerClosed)
			started := make(chan bool, 1)
			done := make(chan error, 1)
			go func() {
				done <- cs.writeRequest(req, func(cs *clientStream) {
					// 此回调运行在请求 goroutine，模拟它获得调度时检查写完成边界。
					// 同步可用的正文必须已由连接写循环开始，不能依赖这里再唤醒它。
					select {
					case <-ready.read:
						started <- true
					default:
						started <- false
					}
					if tc.pending {
						_ = body.Close()
					}
					if tc.cancel {
						cs.abortStream(context.Canceled)
					} else if tc.wait100 {
						cs.on100 <- struct{}{}
					}
				})
			}()
			if err := awaitUploadTest(t, done); !errors.Is(err, tc.wantErr) {
				t.Fatalf("正文结果 = %v，应为 %v", err, tc.wantErr)
			}
			if got := <-started; got == tc.wait100 {
				t.Fatalf("HEADERS 完成后正文已开始 = %t，等待 100 Continue = %t", got, tc.wait100)
			}
			if tc.wantErr != nil {
				if tc.cancel {
					<-cs.reqBodyClosed
					select {
					case <-ready.read:
						t.Fatal("等待继续时取消仍读取了正文")
					default:
					}
				}
				return
			}
			fr := NewFramer(nil, bytes.NewReader(wire.Bytes()))
			var data strings.Builder
			ends := 0
			for {
				f, err := fr.ReadFrame()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				switch f := f.(type) {
				case *DataFrame:
					data.Write(f.Data())
					if f.StreamEnded() {
						ends++
					}
				case *HeadersFrame:
					if f.StreamEnded() {
						ends++
					}
				}
			}
			if data.String() != "body" || ends != 1 {
				t.Fatalf("正文重复或未结束：body=%q end=%d", data.String(), ends)
			}
		})
	}
}

func TestChromiumUploadInvalidHeadersDoNotStartBody(t *testing.T) {
	cc := uploadTestConn(io.Discard, 65535)
	cc.reqHeaderMu = make(chan struct{}, 1)
	cc.nextStreamID, cc.maxConcurrentStreams, cc.initialWindowSize = 1, 1, 65535
	cc.peerMaxHeaderListSize = 1
	body := &readyUploadTestBody{Reader: strings.NewReader("body"), read: make(chan struct{})}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://sample.invalid/body", body)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = 4
	cs := &clientStream{cc: cc, ctx: req.Context(), reqBody: body, reqBodyContentLength: 4, abort: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- cs.writeRequest(req, nil) }()
	if err := awaitUploadTest(t, done); !errors.Is(err, errRequestHeaderListSize) {
		t.Fatalf("请求头错误丢失：%v", err)
	}
	select {
	case <-body.read:
		t.Fatal("请求头失败仍读取了正文")
	default:
	}
}

func TestChromiumUploadEmptyEndAndTrailersAtZeroWindow(t *testing.T) {
	for _, tc := range []struct {
		name               string
		connection, stream int32
		trailers           bool
	}{
		{"连接零窗口", 0, 65535, false},
		{"流零窗口", 65535, 0, false},
		{"流负窗口", 65535, -1, false},
		{"尾部字段", 4, 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire bytes.Buffer
			text, length := "", int64(-1)
			if tc.trailers {
				text, length = "body", 4
			}
			cc := uploadTestConn(&wire, tc.connection)
			body := &readyUploadTestBody{Reader: strings.NewReader(text), read: make(chan struct{})}
			cs := uploadTestStream(cc, t.Context(), 1, body, length)
			cs.flow.n = tc.stream
			req := &http.Request{}
			if tc.trailers {
				req.Trailer = http.Header{"X-Final": {"done"}}
			}
			done := make(chan error, 1)
			go func() { done <- cs.writeRequestBody(req, nil) }()
			<-body.read
			if !tc.trailers {
				select {
				case err := <-done:
					t.Fatalf("窗口不为正时提前发送空结束 DATA：%v", err)
				case <-time.After(30 * time.Millisecond):
				}
				cc.mu.Lock()
				if cc.flow.n <= 0 {
					cc.flow.add(1 - cc.flow.n)
				}
				if cs.flow.n <= 0 {
					cs.flow.add(1 - cs.flow.n)
				}
				cc.resumeUploadsLocked(nil)
				cc.mu.Unlock()
			}
			if err := awaitUploadTest(t, done); err != nil {
				t.Fatal(err)
			}
			fr := NewFramer(nil, bytes.NewReader(wire.Bytes()))
			first, err := fr.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			data, ok := first.(*DataFrame)
			if !ok || string(data.Data()) != text || data.StreamEnded() == tc.trailers {
				t.Fatalf("DATA 结束标志错误：%v", first)
			}
			if tc.trailers {
				last, err := fr.ReadFrame()
				headers, ok := last.(*HeadersFrame)
				if err != nil || !ok || !headers.StreamEnded() {
					t.Fatalf("正文耗尽窗口后未发送 trailers：%v %v", last, err)
				}
			}
			if _, err := fr.ReadFrame(); err != io.EOF {
				t.Fatalf("多发了结束帧：%v", err)
			}
		})
	}
}

type blockingUploadTestWriter struct {
	bytes.Buffer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingUploadTestWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered); <-w.release })
	return w.Buffer.Write(p)
}

func TestChromiumUploadQueuedCancelRefund(t *testing.T) {
	w := &blockingUploadTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(w.release) }) }
	defer unblock()
	cc := uploadTestConn(w, 5)
	start := func(id uint32, text string) (*clientStream, <-chan struct{}, <-chan error) {
		body := &readyUploadTestBody{Reader: strings.NewReader(text), read: make(chan struct{})}
		cs := uploadTestStream(cc, t.Context(), id, body, int64(len(text)))
		done := make(chan error, 1)
		go func() { done <- cs.writeRequestBody(&http.Request{}, nil) }()
		return cs, body.read, done
	}
	_, _, first := start(1, "a")
	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("未进入阻塞写入")
	}
	cc.uploadWriteState(true)
	cancelled, read, aborted := start(3, "bbbb")
	<-read
	cc.mu.Lock()
	if cc.flow.n != 0 {
		t.Fatalf("排队 DATA 没有预留窗口：%d", cc.flow.n)
	}
	cancelled.abortStreamLocked(context.Canceled)
	if cc.flow.n != 4 {
		t.Fatalf("取消排队 DATA 没有归还窗口：%d", cc.flow.n)
	}
	cc.mu.Unlock()
	if err := awaitUploadTest(t, aborted); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消原因丢失：%v", err)
	}
	_, _, next := start(5, "cccc")
	unblock()
	for _, done := range []<-chan error{first, next} {
		if err := awaitUploadTest(t, done); err != nil {
			t.Fatal(err)
		}
	}
	fr := NewFramer(nil, bytes.NewReader(w.Bytes()))
	for _, want := range []uint32{1, 5} {
		frame, err := fr.ReadFrame()
		if err != nil || frame.Header().StreamID != want {
			t.Fatalf("取消流仍被发送或其他流未完成：want=%d frame=%v err=%v", want, frame, err)
		}
	}
	if _, err := fr.ReadFrame(); err != io.EOF {
		t.Fatalf("迟到的取消流 DATA：%v", err)
	}
	<-cancelled.reqBodyClosed
}

func TestChromiumUploadConnectionCreditKeepsStallOrder(t *testing.T) {
	var wire bytes.Buffer
	cc := uploadTestConn(&wire, 2)
	first := uploadTestStream(cc, t.Context(), 1, &readyUploadTestBody{Reader: strings.NewReader("abcd")}, 4)
	second := uploadTestStream(cc, t.Context(), 3, &readyUploadTestBody{Reader: strings.NewReader("ef")}, 2)
	rl := &clientConnReadLoop{cc: cc}
	update := func(id, credit uint32) {
		t.Helper()
		if err := rl.processWindowUpdate(&WindowUpdateFrame{FrameHeader: FrameHeader{StreamID: id}, Increment: credit}); err != nil {
			t.Fatal(err)
		}
	}

	cc.uploadReadState(true)
	cc.mu.Lock()
	first.flow.n = 0
	second.flow.n = 0
	a := first.startQueuedUploadLocked(&http.Request{}, nil)
	b := second.startQueuedUploadLocked(&http.Request{}, nil)
	cc.mu.Unlock()
	update(1, 4)
	update(3, 2)
	cc.uploadReadState(false)
	waitUploadBatchWriter(t, cc)

	cc.uploadReadState(true)
	update(0, 4)
	cc.uploadReadState(false)
	for _, upload := range []*queuedUpload{a, b} {
		if err := awaitUploadTest(t, upload.done); err != nil {
			t.Fatal(err)
		}
	}
	waitUploadBatchWriter(t, cc)

	fr := NewFramer(nil, bytes.NewReader(wire.Bytes()))
	want := []struct {
		id   uint32
		body string
		end  bool
	}{{1, "ab", false}, {1, "cd", true}, {3, "ef", true}}
	for _, frame := range want {
		f, err := fr.ReadFrame()
		data, ok := f.(*DataFrame)
		if err != nil || !ok || data.StreamID != frame.id || string(data.Data()) != frame.body || data.StreamEnded() != frame.end {
			t.Fatalf("连接补窗后的帧序不同：want=%+v got=%v err=%v", frame, f, err)
		}
	}
	if _, err := fr.ReadFrame(); err != io.EOF {
		t.Fatalf("正文重复写出：%v", err)
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if len(cc.uploadStalled) != 0 || cc.flow.n != 0 {
		t.Fatalf("正文结束后等待条目或窗口未清理：waiting=%d window=%d", len(cc.uploadStalled), cc.flow.n)
	}
}

func TestChromiumUploadTLSPendingResumesOtherStream(t *testing.T) {
	w := &blockingUploadTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(w.release) }) }
	defer unblock()
	cc := uploadTestConn(w, 1000000)
	first := uploadTestStream(cc, t.Context(), 1, &readyUploadTestBody{Reader: strings.NewReader(strings.Repeat("a", 65536))}, 65536)
	first.flow.n = 1000000
	aDone := make(chan error, 1)
	go func() { aDone <- first.writeRequestBody(&http.Request{}, nil) }()
	<-w.entered
	body := &readyUploadTestBody{Reader: strings.NewReader("b"), read: make(chan struct{})}
	second := uploadTestStream(cc, t.Context(), 3, body, 1)
	second.flow.n = 0
	bDone := make(chan error, 1)
	go func() { bDone <- second.writeRequestBody(&http.Request{}, nil) }()
	rl := &clientConnReadLoop{cc: cc}
	if err := rl.processWindowUpdate(&WindowUpdateFrame{FrameHeader: FrameHeader{StreamID: 3}, Increment: 1}); err != nil {
		t.Fatal(err)
	}
	cc.mu.Lock()
	deferred := second.flow.n == 0 && slices.ContainsFunc(cc.uploadEvents, func(e uploadEvent) bool { return e.increment == 1 })
	cc.mu.Unlock()
	if !deferred {
		t.Fatal("同步发送阶段没有延后补窗")
	}
	cc.uploadWriteState(true)
	<-body.read
	waitUploadTaskQueue(t, cc)
	cc.mu.Lock()
	resumed := second.upload != nil && second.upload.queued && len(cc.uploadEvents) == 0
	cc.mu.Unlock()
	if !resumed {
		t.Fatal("TLS pending 后仍将第二流补窗压到第一流结束")
	}
	cc.uploadWriteState(false)
	unblock()
	for _, done := range []<-chan error{aDone, bDone} {
		if err := awaitUploadTest(t, done); err != nil {
			t.Fatal(err)
		}
	}
	fr := NewFramer(nil, bytes.NewReader(w.Bytes()))
	for _, want := range []uint32{1, 3} {
		f, err := fr.ReadFrame()
		if err != nil || f.Header().StreamID != want {
			t.Fatalf("补窗流没有按 FIFO 插在下一帧前：want=%d got=%v err=%v", want, f, err)
		}
	}
}

type asyncUploadEventTestBody struct {
	release chan struct{}
	once    sync.Once
}

func (b *asyncUploadEventTestBody) Read(p []byte) (int, error) {
	<-b.release
	return copy(p, "b"), io.EOF
}
func (b *asyncUploadEventTestBody) Close() error { b.once.Do(func() { close(b.release) }); return nil }

type zeroUploadEventTestBody struct {
	readyUploadTestBody
	zero bool
}

func (b *zeroUploadEventTestBody) ReadAvailable(p []byte) (int, error, bool) {
	if !b.zero {
		b.zero = true
		return 0, nil, true
	}
	return b.readyUploadTestBody.ReadAvailable(p)
}

func TestChromiumUploadZeroReadRetryUsesEventFIFO(t *testing.T) {
	var wire bytes.Buffer
	cc := uploadTestConn(&wire, 65535)
	// 模拟另一流仍在同步写循环中，读取重试不能提前排入 DATA。
	cc.uploadWriting = true
	cc.uploadWriteOwner = true
	body := &zeroUploadEventTestBody{readyUploadTestBody: readyUploadTestBody{Reader: strings.NewReader("b")}}
	cs := uploadTestStream(cc, t.Context(), 3, body, -1)
	done := make(chan error, 1)
	cc.mu.Lock()
	pendingBody := cs.startQueuedUploadLocked(&http.Request{}, nil)
	cc.mu.Unlock()
	go func() { done <- cs.awaitQueuedUpload(&http.Request{}, nil, pendingBody) }()
	defer func() {
		cc.uploadWriteState(true)
		waitUploadTaskQueue(t, cc)
		cc.mu.Lock()
		cc.uploadWriteOwner = true
		cc.uploadWriteYielded = false
		cc.mu.Unlock()
		cc.runUploadWrites()
		if err := awaitUploadTest(t, done); err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		cc.mu.Lock()
		events := len(cc.uploadEvents)
		queued := cs.upload != nil && cs.upload.queued
		cc.mu.Unlock()
		if queued {
			t.Fatal("零字节后的读取重试绕过异步事件 FIFO")
		}
		if events == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("零字节后的读取重试未返回事件队列")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestChromiumUploadAsyncReadAndWindowUpdateOrder(t *testing.T) {
	for _, readFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("read_first=%t", readFirst), func(t *testing.T) {
			w := &blockingUploadTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			unblock := func() { release.Do(func() { close(w.release) }) }
			defer unblock()
			cc := uploadTestConn(w, 1000000)
			first := uploadTestStream(cc, t.Context(), 1, &readyUploadTestBody{Reader: strings.NewReader("a")}, 1)
			aDone := make(chan error, 1)
			go func() { aDone <- first.writeRequestBody(&http.Request{}, nil) }()
			<-w.entered
			body := &asyncUploadEventTestBody{release: make(chan struct{})}
			defer body.Close()
			second := uploadTestStream(cc, t.Context(), 3, body, -1)
			second.flow.n = math.MaxInt32
			bDone := make(chan error, 1)
			cc.mu.Lock()
			pendingBody := second.startQueuedUploadLocked(&http.Request{}, nil)
			cc.mu.Unlock()
			go func() { bDone <- second.awaitQueuedUpload(&http.Request{}, nil, pendingBody) }()
			update := func() {
				t.Helper()
				rl := &clientConnReadLoop{cc: cc}
				if err := rl.processWindowUpdate(&WindowUpdateFrame{FrameHeader: FrameHeader{StreamID: 3}, Increment: 1}); err != nil {
					t.Fatal(err)
				}
			}
			if !readFirst {
				update()
			}
			body.Close()
			deadline := time.Now().Add(2 * time.Second)
			for {
				cc.mu.Lock()
				queued := false
				for _, event := range cc.uploadEvents {
					if event.body == second.upload && event.body != nil {
						queued = true
					}
				}
				cc.mu.Unlock()
				if queued {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("同步写期间没有排入异步读取完成事件")
				}
				time.Sleep(time.Millisecond)
			}
			if readFirst {
				update()
			}
			cc.uploadWriteState(true)
			cc.uploadWriteState(false)
			unblock()
			if err := awaitUploadTest(t, aDone); err != nil {
				t.Fatal(err)
			}
			err := awaitUploadTest(t, bDone)
			if readFirst {
				if err != nil {
					t.Fatalf("先排 DATA 归还合法增长空间后补窗仍报错：%v", err)
				}
			} else {
				var streamErr StreamError
				if !errors.As(err, &streamErr) || streamErr.Code != ErrCodeFlowControl {
					t.Fatalf("先到达的溢出补窗没有保留错误：%v", err)
				}
			}
			waitUploadBatchWriter(t, cc)
			fr := NewFramer(nil, bytes.NewReader(w.Bytes()))
			seenB := false
			resets := 0
			for {
				f, err := fr.ReadFrame()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if reset, ok := f.(*RSTStreamFrame); ok && reset.StreamID == 3 {
					if reset.ErrCode != ErrCodeFlowControl {
						t.Fatalf("溢出补窗的错误码不同：%v", reset.ErrCode)
					}
					resets++
				} else if data, ok := f.(*DataFrame); ok && data.StreamID == 3 {
					seenB = true
				}
			}
			if seenB != readFirst {
				t.Fatalf("读取完成与补窗顺序没有决定出站行为：seenB=%t readFirst=%t", seenB, readFirst)
			}
			if want := map[bool]int{false: 1, true: 0}[readFirst]; resets != want {
				t.Fatalf("溢出重置帧数量不同：got=%d want=%d", resets, want)
			}
		})
	}
}

type waitingUploadTestBody struct {
	*io.PipeReader
	entered chan struct{}
	exited  chan struct{}
}

func (b *waitingUploadTestBody) Read(p []byte) (int, error) {
	close(b.entered)
	defer close(b.exited)
	return b.PipeReader.Read(p)
}

func TestChromiumUploadWaitingBodyDoesNotBlockOtherStreams(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	body := &waitingUploadTestBody{PipeReader: reader, entered: make(chan struct{}), exited: make(chan struct{})}
	cc := uploadTestConn(io.Discard, 65535)
	cs := uploadTestStream(cc, t.Context(), 1, body, -1)
	done := make(chan error, 1)
	go func() { done <- cs.writeRequestBody(&http.Request{}, nil) }()
	<-body.entered
	ready := uploadTestStream(cc, t.Context(), 3, &readyUploadTestBody{Reader: strings.NewReader("ready")}, 5)
	other := make(chan error, 1)
	go func() { other <- ready.writeRequestBody(&http.Request{}, nil) }()
	if err := awaitUploadTest(t, other); err != nil {
		t.Fatal(err)
	}
	cs.abortStream(context.Canceled)
	if err := awaitUploadTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消等待正文没有返回原错误：%v", err)
	}
	select {
	case <-body.exited:
	case <-time.After(2 * time.Second):
		t.Fatal("关闭正文后读取 goroutine 没有退出")
	}
	<-cs.reqBodyClosed
}

func TestChromiumUploadRejectsOverlongBodyAndReadError(t *testing.T) {
	failure := errors.New("fixture upload failure")
	for _, badLength := range []bool{false, true} {
		cc := uploadTestConn(io.Discard, 65535)
		var body io.ReadCloser
		length, want := int64(-1), failure
		if badLength {
			body = &readyUploadTestBody{Reader: strings.NewReader("long")}
			length, want = 3, errReqBodyTooLong
		} else {
			reader, writer := io.Pipe()
			body = reader
			writer.CloseWithError(failure)
		}
		cs := uploadTestStream(cc, t.Context(), 1, body, length)
		done := make(chan error, 1)
		go func() { done <- cs.writeRequestBody(&http.Request{}, nil) }()
		if err := awaitUploadTest(t, done); !errors.Is(err, want) {
			t.Fatalf("上传错误原因被覆盖：%v，want=%v", err, want)
		}
		body.Close()
	}
}

func TestChromiumUploadWindowOverflowDoesNotDeadlock(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		cc := uploadTestConn(io.Discard, 65535)
		cs := uploadTestStream(cc, t.Context(), 1, &readyUploadTestBody{Reader: strings.NewReader("body")}, 4)
		cs.flow.n = math.MaxInt32
		cc.uploadWriting = deferred
		cc.uploadWriteOwner = deferred
		done := make(chan error, 1)
		go func() {
			rl := &clientConnReadLoop{cc: cc}
			err := rl.processWindowUpdate(&WindowUpdateFrame{FrameHeader: FrameHeader{StreamID: 1}, Increment: 1})
			if deferred {
				cc.uploadWriteState(true)
				waitUploadTaskQueue(t, cc)
				cc.mu.Lock()
				cc.uploadWriteYielded = false
				cc.uploadWriteOwner = true
				cc.mu.Unlock()
				cc.runUploadWrites()
			} else {
				waitUploadTaskQueue(t, cc)
			}
			done <- err
		}()
		if err := awaitUploadTest(t, done); err != nil {
			t.Fatal(err)
		}
		cc.mu.Lock()
		err := cs.abortErr
		closed := cs.reqBodyClosed
		cc.mu.Unlock()
		var streamErr StreamError
		if !errors.As(err, &streamErr) || streamErr.Code != ErrCodeFlowControl {
			t.Fatalf("溢出补窗未终止流：deferred=%t err=%v", deferred, err)
		}
		<-closed
	}
}
