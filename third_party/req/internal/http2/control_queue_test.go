// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	reqhttp2 "github.com/imroc/req/v3/http2"
	"github.com/imroc/req/v3/internal/transport"
	"golang.org/x/net/http2/hpack"
)

func TestChromiumUploadControlDoesNotBlockReadLoop(t *testing.T) {
	for _, control := range []string{"ping", "settings"} {
		t.Run(control, func(t *testing.T) {
			w := &blockingUploadTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			unblock := func() { once.Do(func() { close(w.release) }) }
			defer unblock()
			cc := uploadTestConn(w, 1000000)
			cc.t.RespectHeaderTableSize = true
			first := uploadTestStream(cc, t.Context(), 1, &readyUploadTestBody{Reader: strings.NewReader(strings.Repeat("a", 65536))}, 65536)
			first.flow.n = 1000000
			aDone := make(chan error, 1)
			go func() { aDone <- first.writeRequestBody(&http.Request{}, nil) }()
			<-w.entered
			cc.uploadWriteState(true)
			body := &readyUploadTestBody{Reader: strings.NewReader("b"), read: make(chan struct{})}
			second := uploadTestStream(cc, t.Context(), 3, body, 1)
			second.flow.n = 0
			bDone := make(chan error, 1)
			go func() { bDone <- second.writeRequestBody(&http.Request{}, nil) }()
			<-body.read
			rl := &clientConnReadLoop{cc: cc}
			processed := make(chan error, 1)
			go func() {
				if control == "ping" {
					processed <- rl.processPing(&PingFrame{Data: [8]byte{'f', 'i', 'x', 't', 'u', 'r', 'e', '!'}})
					return
				}
				var wire bytes.Buffer
				fr := NewFramer(&wire, nil)
				_ = fr.WriteSettings(reqhttp2.Setting{ID: reqhttp2.SettingMaxConcurrentStreams, Val: 7}, reqhttp2.Setting{ID: reqhttp2.SettingHeaderTableSize, Val: 0}, reqhttp2.Setting{ID: reqhttp2.SettingHeaderTableSize, Val: 64}, reqhttp2.Setting{ID: reqhttp2.SettingHeaderTableSize, Val: 8192})
				f, err := NewFramer(nil, bytes.NewReader(wire.Bytes())).ReadFrame()
				if err != nil {
					processed <- err
					return
				}
				settings := f.(*SettingsFrame)
				err = rl.processSettings(settings)
				// 模拟读取下一帧，原缓冲和 FrameHeader 都失效。
				clear(settings.p)
				settings.invalidate()
				processed <- err
			}()
			if err := awaitUploadTest(t, processed); err != nil {
				t.Fatal(err)
			}
			if err := rl.processWindowUpdate(&WindowUpdateFrame{FrameHeader: FrameHeader{StreamID: 3}, Increment: 1}); err != nil {
				t.Fatal(err)
			}
			cc.uploadWriteState(true)
			unblock()
			for _, done := range []<-chan error{aDone, bDone} {
				if err := awaitUploadTest(t, done); err != nil {
					t.Fatal(err)
				}
			}
			fr := NewFramer(nil, bytes.NewReader(w.Bytes()))
			for _, want := range []struct {
				kind FrameType
				id   uint32
			}{{FrameData, 1}, {map[string]FrameType{"ping": FramePing, "settings": FrameSettings}[control], 0}, {FrameData, 3}} {
				f, err := fr.ReadFrame()
				if err != nil || f.Header().Type != want.kind || f.Header().StreamID != want.id {
					t.Fatalf("控制帧没有排在下一 DATA 前：want=%v frame=%v err=%v", want, f, err)
				}
				if data, ok := f.(*DataFrame); ok && data.StreamID == 3 && string(data.Data()) != "b" {
					t.Fatalf("补窗流正文损坏：%q", data.Data())
				}
			}
			if control == "settings" {
				cc.mu.Lock()
				concurrent := cc.maxConcurrentStreams
				cc.mu.Unlock()
				if concurrent != 7 {
					t.Fatalf("延后处理的 SETTINGS 被后续读取覆盖：%d", concurrent)
				}
				req, _ := http.NewRequest("GET", "https://fixture.invalid/sized", nil)
				cc.wmu.Lock()
				cc.mu.Lock()
				headers, err := cc.encodeHeaders(req, false, "", 0, nil)
				cc.mu.Unlock()
				cc.wmu.Unlock()
				if err != nil || !bytes.HasPrefix(headers, []byte{0x20}) {
					t.Fatalf("丢失中途缩到零的 HPACK 变更：%x %v", headers, err)
				}
				decoder := hpack.NewDecoder(4096, nil)
				decoder.SetAllowedMaxDynamicTableSize(8192)
				if _, err := decoder.DecodeFull(headers); err != nil {
					t.Fatalf("异步 SETTINGS 后的 HPACK 无法解码：%v", err)
				}
			}
		})
	}
}

type uploadControlFailureWriter struct{ failure error }

func (w uploadControlFailureWriter) Write([]byte) (int, error) { return 0, w.failure }

func TestChromiumUploadControlWriteFailureAbortsWaitingBody(t *testing.T) {
	failure := errors.New("fixture control write failure")
	cc := uploadTestConn(uploadControlFailureWriter{failure}, 65535)
	raw, peer := net.Pipe()
	defer raw.Close()
	defer peer.Close()
	cc.tconn = raw
	reader, writer := io.Pipe()
	defer writer.Close()
	body := &waitingUploadTestBody{PipeReader: reader, entered: make(chan struct{}), exited: make(chan struct{})}
	cs := uploadTestStream(cc, t.Context(), 1, body, -1)
	done := make(chan error, 1)
	go func() { done <- cs.writeRequestBody(&http.Request{}, nil) }()
	<-body.entered
	rl := &clientConnReadLoop{cc: cc}
	if err := rl.processPing(&PingFrame{}); err != nil {
		t.Fatal(err)
	}
	if err := awaitUploadTest(t, done); !errors.Is(err, failure) {
		t.Fatalf("控制写失败没有保留原始原因：%v", err)
	}
	select {
	case <-body.exited:
	case <-time.After(2 * time.Second):
		t.Fatal("控制写失败后正文读取未退出")
	}
	<-cs.reqBodyClosed
}

func TestChromiumUploadControlQueueLimit(t *testing.T) {
	for _, pending := range []bool{false, true} {
		cc := uploadTestConn(io.Discard, 65535)
		cc.uploadWriting = true
		cc.uploadWriteOwner, cc.uploadWriteYielded = !pending, pending
		rl := &clientConnReadLoop{cc: cc}
		for range maxQueuedUploadControls + 1 {
			if err := rl.processPing(&PingFrame{}); err != nil {
				t.Fatalf("未达到官方关闭边界却拒绝控制帧：%v", err)
			}
		}
		if err := rl.processPing(&PingFrame{}); !errors.Is(err, errUploadControlQueueLimit) {
			t.Fatalf("控制队列没有按官方上限拒绝增长：pending=%t err=%v", pending, err)
		}
	}
}

func TestChromiumUploadControlLimitDrainsBeforeClose(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "同步写", true: "TLS等待"}[pending], func(t *testing.T) {
			w := &blockingUploadTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			unblock := func() { once.Do(func() { close(w.release) }) }
			defer unblock()
			cc := uploadTestConn(w, 1000000)
			raw, peer := net.Pipe()
			defer raw.Close()
			defer peer.Close()
			cc.tconn, cc.readerDone, cc.wantSettingsAck = raw, make(chan struct{}), true
			body := &readyUploadTestBody{Reader: strings.NewReader(strings.Repeat("a", 65536))}
			cs := uploadTestStream(cc, t.Context(), 1, body, 65536)
			uploadDone := make(chan error, 1)
			go func() { uploadDone <- cs.writeRequestBody(&http.Request{}, nil) }()
			<-w.entered
			cc.uploadWriteState(pending)
			var input bytes.Buffer
			fr := NewFramer(&input, nil)
			_ = fr.WriteSettingsAck()
			for i := range maxQueuedUploadControls + 2 {
				if i%2 == 0 {
					var data [8]byte
					binary.BigEndian.PutUint64(data[:], uint64(i))
					_ = fr.WritePing(false, data)
				} else {
					_ = fr.WriteSettings(reqhttp2.Setting{ID: reqhttp2.SettingMaxConcurrentStreams, Val: 100})
				}
			}
			cc.fr = NewFramer(cc.bw, bytes.NewReader(input.Bytes()))
			go cc.readLoop()
			if err := awaitUploadTest(t, uploadDone); !errors.Is(err, errUploadControlQueueLimit) {
				t.Fatalf("未将超限错误传给上传：%v", err)
			}
			// 请求被删除、禁止复用和主动回收空闲连接，都不能抢先关闭排空中的 socket。
			cc.mu.Lock()
			cc.doNotReuse = true
			cc.mu.Unlock()
			cc.forgetStreamID(cs.ID)
			cc.closeIfIdle()
			select {
			case <-cc.readerDone:
				t.Fatal("在途 DATA 尚未完成就关闭连接，排队 ACK 丢失")
			case <-time.After(20 * time.Millisecond):
			}
			unblock()
			select {
			case <-cc.readerDone:
			case <-time.After(2 * time.Second):
				t.Fatal("控制队列排空后没有关闭连接")
			}
			cc.mu.Lock()
			for cc.uploadWriting {
				cc.cond.Wait()
			}
			cc.mu.Unlock()
			frames := NewFramer(nil, bytes.NewReader(w.Bytes()))
			f, err := frames.ReadFrame()
			data, ok := f.(*DataFrame)
			if err != nil || !ok || data.StreamID != 1 || !bytes.Equal(data.Data(), bytes.Repeat([]byte{'a'}, len(data.Data()))) {
				t.Fatalf("关闭时损坏了在途 DATA：%v %v", f, err)
			}
			for i := range maxQueuedUploadControls + 1 {
				f, err := frames.ReadFrame()
				if err != nil {
					t.Fatalf("第 %d 个排队 ACK 丢失：%v", i, err)
				}
				if i%2 == 0 {
					ping, ok := f.(*PingFrame)
					if !ok || !ping.IsAck() || binary.BigEndian.Uint64(ping.Data[:]) != uint64(i) {
						t.Fatalf("第 %d 个 PING ACK 顺序错误：%v", i, f)
					}
				} else if settings, ok := f.(*SettingsFrame); !ok || !settings.IsAck() {
					t.Fatalf("第 %d 个 SETTINGS ACK 顺序错误：%v", i, f)
				}
			}
			if f, err := frames.ReadFrame(); err != io.EOF {
				t.Fatalf("超限后多发了流帧或控制帧：%v %v", f, err)
			}
			<-cs.reqBodyClosed
		})
	}
}

func TestChromiumUploadProtocolErrorDrainsGoAway(t *testing.T) {
	w := &blockingUploadTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(w.release) }) }
	defer unblock()
	raw, peer := net.Pipe()
	defer raw.Close()
	defer peer.Close()
	cc := uploadTestConn(w, 65535)
	cc.tconn, cc.readerDone, cc.wantSettingsAck = raw, make(chan struct{}), true
	cc.fr = NewFramer(cc.bw, raw)
	cs := uploadTestStream(cc, t.Context(), 1, &readyUploadTestBody{Reader: strings.NewReader(strings.Repeat("a", 32768))}, 32768)
	uploadDone := make(chan error, 1)
	go func() { uploadDone <- cs.writeRequestBody(&http.Request{}, nil) }()
	<-w.entered
	cc.uploadWriteState(true)
	go cc.readLoop()
	if err := peer.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	input := NewFramer(peer, nil)
	if err := input.WriteSettingsAck(); err != nil {
		t.Fatal(err)
	}
	payload := [8]byte{'p', 'r', 'o', 't', 'o', 'c', 'o', 'l'}
	if err := input.WritePing(false, payload); err != nil {
		t.Fatal(err)
	}
	if err := input.WritePushPromise(PushPromiseParam{StreamID: 1, PromiseID: 2, EndHeaders: true}); err != nil {
		t.Fatal(err)
	}
	if err := awaitUploadTest(t, uploadDone); !errors.Is(err, ConnectionError(ErrCodeProtocol)) {
		t.Fatalf("上传未收到原始协议错误：%v", err)
	}
	select {
	case <-cc.readerDone:
		t.Fatal("未排空在途帧就关闭了连接")
	default:
	}
	unblock()
	select {
	case <-cc.readerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("发出 GOAWAY 后连接未关闭")
	}
	output := NewFramer(nil, bytes.NewReader(w.Bytes()))
	for _, want := range []FrameType{FrameData, FramePing, FrameGoAway} {
		f, err := output.ReadFrame()
		if err != nil || f.Header().Type != want {
			t.Fatalf("应发出 %s，收到 %v (%v)", want, f, err)
		}
		switch f := f.(type) {
		case *PingFrame:
			if !f.IsAck() || f.Data != payload {
				t.Fatalf("已排队 PING ACK 不匹配：%v", f)
			}
		case *GoAwayFrame:
			if f.LastStreamID != 0 || f.ErrCode != ErrCodeProtocol || string(f.DebugData()) != "PUSH_PROMISE received" {
				t.Fatalf("GOAWAY 内容不匹配：%v %q", f, f.DebugData())
			}
		}
	}
	if f, err := output.ReadFrame(); err != io.EOF {
		t.Fatalf("GOAWAY 后仍发送了流帧：%v (%v)", f, err)
	}
	<-cs.reqBodyClosed
}

func TestChromiumUploadShutdownClosesWithoutGoAway(t *testing.T) {
	w := new(bytes.Buffer)
	cc := uploadTestConn(w, 65535)
	cc.nextStreamID, cc.maxConcurrentStreams = 3, 2
	raw, peer := net.Pipe()
	defer peer.Close()
	defer raw.Close()
	cc.tconn = raw
	cs := uploadTestStream(cc, t.Context(), 1, nil, 0)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cc.Shutdown(ctx) }()
	deadline := time.Now().Add(time.Second)
	for cc.CanTakeNewRequest() {
		if time.Now().After(deadline) {
			t.Fatal("关闭时未停止接收新请求")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("活动流完成前关闭了连接：%v", err)
	default:
	}
	cc.forgetStreamID(cs.ID)
	if err := awaitUploadTest(t, done); err != nil {
		t.Fatal(err)
	}
	if w.Len() != 0 {
		t.Fatalf("正常关闭多发了 %d 字节", w.Len())
	}
}

type uploadDrainTestConn struct {
	net.Conn
	entered chan struct{}
	once    sync.Once
}

func (c *uploadDrainTestConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	return c.Conn.Write(p)
}

func TestChromiumUploadControlDrainInterrupted(t *testing.T) {
	for _, cause := range []string{"写失败", "显式关闭", "写超时"} {
		t.Run(cause, func(t *testing.T) {
			raw, peer := net.Pipe()
			defer raw.Close()
			defer peer.Close()
			conn := &uploadDrainTestConn{Conn: raw, entered: make(chan struct{})}
			cc := uploadTestConn(conn, 1000000)
			cc.bw.Reset(stickyErrWriter{conn: conn, err: &cc.werr})
			cc.tconn, cc.readerDone, cc.wantSettingsAck = conn, make(chan struct{}), true
			var input bytes.Buffer
			fr := NewFramer(&input, nil)
			_ = fr.WriteSettingsAck()
			for range maxQueuedUploadControls + 2 {
				_ = fr.WritePing(false, [8]byte{})
			}
			cc.fr = NewFramer(cc.bw, bytes.NewReader(input.Bytes()))
			body := &readyUploadTestBody{Reader: strings.NewReader(strings.Repeat("a", 65536))}
			cs := uploadTestStream(cc, t.Context(), 1, body, 65536)
			uploadDone := make(chan error, 1)
			go func() { uploadDone <- cs.writeRequestBody(&http.Request{}, nil) }()
			<-conn.entered
			go cc.readLoop()
			if err := awaitUploadTest(t, uploadDone); !errors.Is(err, errUploadControlQueueLimit) {
				t.Fatalf("排空前未中断请求：%v", err)
			}
			switch cause {
			case "写失败":
				_ = peer.Close()
			case "显式关闭":
				_ = cc.Close()
			case "写超时":
				_ = raw.SetWriteDeadline(time.Now())
			}
			select {
			case <-cc.readerDone:
			case <-time.After(2 * time.Second):
				t.Fatal("排空等待没有被中断")
			}
			cc.mu.Lock()
			for cc.uploadWriting {
				cc.cond.Wait()
			}
			cc.mu.Unlock()
			cc.wmu.Lock()
			writeErr := cc.werr
			cc.wmu.Unlock()
			if writeErr == nil {
				t.Fatal("排空写失败未保留错误")
			}
			if cause == "写超时" {
				var timeout net.Error
				if !errors.As(writeErr, &timeout) || !timeout.Timeout() {
					t.Fatalf("丢失写超时原因：%v", writeErr)
				}
			}
			<-cs.reqBodyClosed
		})
	}
}

func TestChromiumUploadPaddedDataDoesNotBlockReadLoop(t *testing.T) {
	w := &blockingUploadTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(w.release) }) }
	defer unblock()
	cc := uploadTestConn(w, 1000000)
	first := uploadTestStream(cc, t.Context(), 1, &readyUploadTestBody{Reader: strings.NewReader(strings.Repeat("a", 65536))}, 65536)
	first.flow.n = 1000000
	aDone := make(chan error, 1)
	go func() { aDone <- first.writeRequestBody(&http.Request{}, nil) }()
	<-w.entered
	cc.uploadWriteState(true)
	body := &readyUploadTestBody{Reader: strings.NewReader("b"), read: make(chan struct{})}
	second := uploadTestStream(cc, t.Context(), 3, body, 1)
	second.flow.n, second.pastHeaders = 0, true
	cc.inflow.initChromium(65535)
	cc.inflow.take(2)
	cc.inflow.add(2)
	second.inflow.initChromium(65535)
	cc.inflow.lastUpdate = time.Now().Add(-6 * time.Second)
	second.inflow.lastUpdate = cc.inflow.lastUpdate
	bDone := make(chan error, 1)
	go func() { bDone <- second.writeRequestBody(&http.Request{}, nil) }()
	<-body.read
	var input bytes.Buffer
	fr := NewFramer(&input, nil)
	_ = fr.WriteDataPadded(3, false, nil, make([]byte, 255))
	f, err := NewFramer(nil, bytes.NewReader(input.Bytes())).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	rl := &clientConnReadLoop{cc: cc}
	processed := make(chan error, 1)
	go func() { processed <- rl.processData(f.(*DataFrame)) }()
	if err := awaitUploadTest(t, processed); err != nil {
		t.Fatal(err)
	}
	if err := rl.processWindowUpdate(&WindowUpdateFrame{FrameHeader: FrameHeader{StreamID: 3}, Increment: 1}); err != nil {
		t.Fatal(err)
	}
	if err := rl.processPing(&PingFrame{Data: [8]byte{'f', 'i', 'x', 't', 'u', 'r', 'e', '!'}}); err != nil {
		t.Fatal(err)
	}
	cc.uploadWriteState(true)
	unblock()
	for _, done := range []<-chan error{aDone, bDone} {
		if err := awaitUploadTest(t, done); err != nil {
			t.Fatal(err)
		}
	}
	frames := NewFramer(nil, bytes.NewReader(w.Bytes()))
	for i, want := range []struct {
		kind FrameType
		id   uint32
	}{{FrameData, 1}, {FrameWindowUpdate, 0}, {FramePing, 0}, {FrameWindowUpdate, 3}, {FrameData, 3}} {
		f, err := frames.ReadFrame()
		if err != nil || f.Header().Type != want.kind || f.Header().StreamID != want.id {
			t.Fatalf("第 %d 帧未按官方优先级与 FIFO 发送：want=%v got=%v err=%v", i, want, f, err)
		}
		if update, ok := f.(*WindowUpdateFrame); ok {
			increment := uint32(1)
			if update.StreamID == 0 {
				increment = 3
			}
			if update.Increment != increment {
				t.Fatalf("padding 长度字节与尾部填充被合并：stream=%d increment=%d want=%d", update.StreamID, update.Increment, increment)
			}
		}
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.inflow.unsent != 255 || second.inflow.unsent != 255 {
		t.Fatalf("尾部 padding 未按实际消费保留：conn=%d stream=%d", cc.inflow.unsent, second.inflow.unsent)
	}
}

func TestChromiumUploadResetReleasesSlotWhileWritePending(t *testing.T) {
	w := &blockingUploadTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(w.release) }) }
	defer unblock()
	cc := uploadTestConn(w, 1000000)
	active := uploadTestStream(cc, t.Context(), 1, &readyUploadTestBody{Reader: strings.NewReader(strings.Repeat("a", 65536))}, 65536)
	active.flow.n = 1000000
	aDone := make(chan error, 1)
	go func() { aDone <- active.writeRequestBody(&http.Request{}, nil) }()
	<-w.entered
	cc.uploadWriteState(true)

	cancelled := uploadTestStream(cc, t.Context(), 3, nil, 0)
	cancelled.sentHeaders = true
	cancelled.donec = make(chan struct{})
	go cancelled.cleanupWriteRequest(context.Canceled)

	deadline := time.Now().Add(time.Second)
	for {
		cc.mu.Lock()
		_, exists := cc.streams[cancelled.ID]
		cc.mu.Unlock()
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RST_STREAM 未释放取消流的并发槽位，仍在等待 socket 写入")
		}
		time.Sleep(time.Millisecond)
	}
	rl := &clientConnReadLoop{cc: cc}
	ping := [8]byte{'f', 'i', 'x', 't', 'u', 'r', 'e', '!'}
	if err := rl.processPing(&PingFrame{Data: ping}); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := awaitUploadTest(t, aDone); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled.donec:
	case <-time.After(time.Second):
		t.Fatal("取消流清理未退出")
	}
	fr := NewFramer(nil, bytes.NewReader(w.Bytes()))
	for i, want := range []FrameType{FrameData, FramePing, FrameRSTStream, FrameData} {
		f, err := fr.ReadFrame()
		if err != nil || f.Header().Type != want {
			t.Fatalf("第 %d 帧 = %v (%v)，应为 %s", i, f, err, want)
		}
		switch f := f.(type) {
		case *PingFrame:
			if !f.IsAck() || f.Data != ping {
				t.Fatalf("PING ACK 不匹配：%v", f)
			}
		case *RSTStreamFrame:
			if f.StreamID != cancelled.ID || f.ErrCode != ErrCodeCancel {
				t.Fatalf("取消帧不匹配：%v", f)
			}
		}
	}
}

func TestChromiumUploadLastStreamFlushesResetBeforeClose(t *testing.T) {
	raw, peer := net.Pipe()
	defer raw.Close()
	defer peer.Close()
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	cc := uploadTestConn(raw, 65535)
	cc.tconn = raw
	cc.singleUse = true
	cs := uploadTestStream(cc, t.Context(), 1, nil, 0)
	cs.sentHeaders = true
	cs.donec = make(chan struct{})
	go cs.cleanupWriteRequest(context.Canceled)

	fr := NewFramer(nil, peer)
	f, err := fr.ReadFrame()
	reset, ok := f.(*RSTStreamFrame)
	if err != nil || !ok || reset.StreamID != cs.ID || reset.ErrCode != ErrCodeCancel {
		t.Fatalf("单次连接关闭前未写完取消帧：%v (%v)", f, err)
	}
	if f, err := fr.ReadFrame(); err != io.EOF {
		t.Fatalf("取消帧之后应关闭连接：%v (%v)", f, err)
	}
	select {
	case <-cs.donec:
	case <-time.After(time.Second):
		t.Fatal("最后一条流的清理未退出")
	}
}

func TestChromiumUploadQueuedRequestHeaders(t *testing.T) {
	w := &blockingUploadTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(w.release) }) }
	defer unblock()
	cc := uploadTestConn(w, 1000000)
	cc.reqHeaderMu = make(chan struct{}, 1)
	cc.nextStreamID, cc.maxConcurrentStreams = 3, 3
	data := strings.Repeat("a", 32768)
	active := uploadTestStream(cc, t.Context(), 1, &readyUploadTestBody{Reader: strings.NewReader(data)}, int64(len(data)))
	aDone := make(chan error, 1)
	go func() { aDone <- active.writeRequestBody(&http.Request{}, nil) }()
	<-w.entered
	cc.uploadWriteState(true)

	start := func(ctx context.Context) (*clientStream, <-chan error) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, "GET", "https://sample.invalid/queued", nil)
		if err != nil {
			t.Fatal(err)
		}
		// Huffman 编码后的头块仍会超过一帧，Framer 必须能连续解完整块。
		req.Header.Set("X-Queue-Sample", strings.Repeat("x", 40000))
		cs := &clientStream{cc: cc, ctx: ctx, abort: make(chan struct{}), peerClosed: make(chan struct{})}
		close(cs.peerClosed)
		done := make(chan error, 1)
		go func() { done <- cs.writeRequest(req, nil) }()
		return cs, done
	}
	awaitOpening := func(want int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			cc.mu.Lock()
			opening, next := cc.uploadOpening, cc.nextStreamID
			cc.mu.Unlock()
			if next != 3 {
				t.Fatalf("尚未发送 HEADERS 就消耗了流编号：%d", next)
			}
			if opening == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("待打开流数量 = %d，应为 %d", opening, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	cancelled, cancelledDone := start(ctx)
	awaitOpening(1)
	survived, survivedDone := start(t.Context())
	awaitOpening(2)
	stop()
	if err := awaitUploadTest(t, cancelledDone); !errors.Is(err, context.Canceled) || cancelled.ID != 0 {
		t.Fatalf("未发送就取消的流：id=%d err=%v", cancelled.ID, err)
	}
	awaitOpening(1)
	rl := &clientConnReadLoop{cc: cc}
	payload := [8]byte{'h', 'e', 'a', 'd', 'e', 'r', 's', '!'}
	if err := rl.processPing(&PingFrame{Data: payload}); err != nil {
		t.Fatal(err)
	}
	unblock()
	for _, done := range []<-chan error{aDone, survivedDone} {
		if err := awaitUploadTest(t, done); err != nil {
			t.Fatal(err)
		}
	}
	if survived.ID != 3 {
		t.Fatalf("活动流编号 = %d，应为 3", survived.ID)
	}
	fr := NewFramer(nil, bytes.NewReader(w.Bytes()))
	fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	for i, want := range []FrameType{FrameData, FramePing, FrameHeaders, FrameData} {
		f, err := fr.ReadFrame()
		if err != nil || f.Header().Type != want {
			t.Fatalf("第 %d 帧 = %v (%v)，应为 %s", i, f, err, want)
		}
		switch f := f.(type) {
		case *PingFrame:
			if !f.IsAck() || f.Data != payload {
				t.Fatalf("PING ACK 不匹配：%v", f)
			}
		case *MetaHeadersFrame:
			if f.StreamID != survived.ID || f.PseudoValue("path") != "/queued" || !f.StreamEnded() {
				t.Fatalf("请求头不匹配：%v", f)
			}
		}
	}
}

func TestChromiumUploadHeaderDependencies(t *testing.T) {
	for _, weight := range []uint8{109, 219} {
		t.Run(fmt.Sprint(weight), func(t *testing.T) {
			var wire bytes.Buffer
			cc := uploadTestConn(&wire, 65535)
			cc.nextStreamID = 1
			cc.initialWindowSize = 65535
			cc.t.HeaderPriority = reqhttp2.PriorityParam{Exclusive: true, Weight: weight}
			fr := NewFramer(nil, &wire)
			send := func(cs *clientStream, initial, end bool, parent uint32) {
				t.Helper()
				if initial {
					cc.uploadOpening++
				}
				h := &queuedUploadHeaders{cs: cs, initial: initial, end: end, done: make(chan error, 1), encode: func() ([]byte, error) { return []byte{0x82}, nil }}
				if err := cc.writeQueuedHeaders(h); err != nil {
					t.Fatal(err)
				}
				if err := <-h.done; err != nil {
					t.Fatal(err)
				}
				f, err := fr.ReadFrame()
				headers, ok := f.(*HeadersFrame)
				if err != nil || !ok || !headers.HasPriority() || headers.Priority.StreamDep != parent || headers.Priority.Weight != weight || !headers.Priority.Exclusive {
					t.Fatalf("流 %d HEADERS 优先级不符：parent=%d frame=%v err=%v", cs.ID, parent, f, err)
				}
			}
			start := func(end bool, parent uint32) *clientStream {
				cs := &clientStream{cc: cc, ctx: t.Context(), abort: make(chan struct{}), peerClosed: make(chan struct{})}
				send(cs, true, end, parent)
				return cs
			}
			first := start(true, 0)
			second := start(false, first.ID)
			// 仅远端结束时，仍在上传的流继续参与依赖。
			close(second.peerClosed)
			cancelled := start(true, second.ID)
			cc.mu.Lock()
			cancelled.abortStreamLocked(context.Canceled)
			cc.mu.Unlock()
			fourth := start(true, second.ID)
			close(fourth.peerClosed)
			// trailers 完成后第二流两端都已结束，即使尚未从 map 清理也不能再作父流。
			send(second, false, true, 0)
			fifth := start(true, first.ID)
			close(fifth.peerClosed)
			close(first.peerClosed)
			last := start(false, 0)
			close(last.peerClosed)
			last.reqBody = &readyUploadTestBody{Reader: strings.NewReader("x")}
			last.reqBodyContentLength = 1
			if err := last.writeRequestBody(&http.Request{}, nil); err != nil {
				t.Fatal(err)
			}
			f, err := fr.ReadFrame()
			data, ok := f.(*DataFrame)
			if err != nil || !ok || !data.StreamEnded() {
				t.Fatalf("末尾 DATA 未结束上传：%v (%v)", f, err)
			}
			start(true, 0)
		})
	}
}

func TestChromiumUploadGoAwayDropsUnopenedHeaders(t *testing.T) {
	cc := uploadTestConn(io.Discard, 65535)
	cc.uploadWriting = true
	cc.uploadWriteOwner = true
	cc.uploadOpening = 1
	next := &queuedUploadHeaders{initial: true, done: make(chan error, 1)}
	trailer := &queuedUploadHeaders{done: make(chan error, 1)}
	cc.queueHeaders(trailer)
	cc.queueHeaders(next)
	var wire bytes.Buffer
	if err := NewFramer(&wire, nil).WriteGoAway(1, ErrCodeNo, nil); err != nil {
		t.Fatal(err)
	}
	f, err := NewFramer(nil, &wire).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	cc.setGoAway(f.(*GoAwayFrame))
	if err := awaitUploadTest(t, next.done); !errors.Is(err, errClientConnGotGoAway) {
		t.Fatalf("未开始的请求应转移到新连接：%v", err)
	}
	if cc.uploadOpening != 0 || len(cc.uploadEvents) != 1 || cc.uploadEvents[0].headers != trailer {
		t.Fatal("GOAWAY 未释放新流槽位，或错误移除了已开启流的尾部字段")
	}
}

func TestChromiumUploadReadCompletedBodyAfterConnectionCloses(t *testing.T) {
	cc := uploadTestConn(io.Discard, 65535)
	cs := uploadTestStream(cc, t.Context(), 1, nil, -1)
	cs.bytesRemain = -1
	cs.bufPipe.setBuffer(bytes.NewBufferString("ok"))
	cs.bufPipe.CloseWithError(io.EOF)
	cc.inflow.initChromium(65535)
	cc.inflow.take(2)
	cc.inflow.lastUpdate = time.Now().Add(-6 * time.Second)
	cc.closed = true
	data, err := io.ReadAll(transportResponseBody{cs: cs})
	if err != nil || string(data) != "ok" {
		t.Fatalf("连接关闭后读取完整响应：body=%q err=%v", data, err)
	}
}

func TestChromiumUploadResponseReadCapWakesReadLoop(t *testing.T) {
	w := &blockingUploadTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(w.release) }) }
	defer unblock()
	cc := uploadTestConn(w, 65535)
	raw, peer := net.Pipe()
	defer raw.Close()
	defer peer.Close()
	cc.tconn, cc.readerDone = raw, make(chan struct{})
	cc.fr = NewFramer(cc.bw, raw)
	cs := uploadTestStream(cc, t.Context(), 1, nil, -1)
	cs.bytesRemain = -1
	cs.bufPipe.setBuffer(bytes.NewBufferString("x"))
	cc.inflow.initChromium(65535)
	cc.inflow.take(1)
	cc.inflow.lastUpdate = time.Now().Add(-6 * time.Second)
	cs.inflow.initChromium(65535)
	cc.mu.Lock()
	cc.queueUploadControlLocked(func() error { return cc.fr.WriteSettingsAck() }, false)
	cc.uploadControlCount++
	cc.mu.Unlock()
	<-w.entered
	cc.uploadWriteState(true)
	rl := &clientConnReadLoop{cc: cc}
	for range maxQueuedUploadControls + 1 {
		if err := rl.processPing(&PingFrame{}); err != nil {
			t.Fatal(err)
		}
	}
	go cc.readLoop()
	if n, err := (transportResponseBody{cs: cs}).Read(make([]byte, 1)); n != 1 || err != nil {
		t.Fatalf("正文消费结果受补窗超限影响：n=%d err=%v", n, err)
	}
	unblock()
	select {
	case <-cc.readerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("正文消费触发超限后读取循环未退出")
	}
	if !errors.Is(cc.readerErr, errUploadControlQueueLimit) {
		t.Fatalf("未保留补窗超限原因：%v", cc.readerErr)
	}
	frames := NewFramer(nil, bytes.NewReader(w.Bytes()))
	if _, err := frames.ReadFrame(); err != nil {
		t.Fatal(err)
	}
	for i := range maxQueuedUploadControls + 1 {
		f, err := frames.ReadFrame()
		ping, ok := f.(*PingFrame)
		if err != nil || !ok || !ping.IsAck() {
			t.Fatalf("超限后第 %d 个排队 ACK 无效：%v %v", i, f, err)
		}
	}
	if f, err := frames.ReadFrame(); err != io.EOF {
		t.Fatalf("不应发出触发超限的补窗：%v %v", f, err)
	}
}

func TestConcurrentConnectionsKeepHeaderLimitLocal(t *testing.T) {
	tr := &Transport{Options: &transport.Options{}, MaxHeaderListSize: 8192, Settings: []reqhttp2.Setting{{ID: reqhttp2.SettingMaxHeaderListSize, Val: 65536}}}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			conn, peer := net.Pipe()
			defer peer.Close()
			go func() { _, _ = io.Copy(io.Discard, peer) }()
			cc, err := tr.NewClientConn(conn)
			if err != nil {
				t.Error(err)
				return
			}
			if cc.fr.MaxHeaderListSize != 65536 {
				t.Error("连接未使用配置的报头上限")
			}
			cc.Close()
		})
	}
	wg.Wait()
	if tr.MaxHeaderListSize != 8192 {
		t.Fatal("建连修改了共享传输配置")
	}
}
