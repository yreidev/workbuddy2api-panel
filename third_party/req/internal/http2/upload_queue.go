// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/imroc/req/v3/http2"
	"github.com/imroc/req/v3/internal/common"
	"github.com/imroc/req/v3/internal/dump"
)

// Chromium 107／142／156 的 kSpdySessionMaxQueuedCappedFrames；入队前比较 >。
const maxQueuedUploadControls = 10000

var errUploadControlQueueLimit = errors.New("http2: Chromium 控制帧队列超过上限")

// 可立即读取的正文与 Chromium 的同步 UploadDataStream 对应；不可立即读取时，
// ReadAvailable 必须不消费正文，由后台 Read 在完成后重新进入连接队列。
type availableUploadReader interface {
	ReadAvailable([]byte) (int, error, bool)
}

type uploadEvent struct {
	cs              *clientStream
	increment       int32
	body            *queuedUpload
	n               int
	err             error
	peek            bool
	control         func() error
	settings        *SettingsFrame
	headers         *queuedUploadHeaders
	normal          bool // 流级 WINDOW_UPDATE、RST_STREAM 与 DATA 同优先级
	fromRead        bool // 读批次内的帧处理；外部异步完成须等当前批次让出
	writeTask       bool
	readReady       chan struct{}
	writeReady      chan struct{}
	startRead       bool
	responseRead    *chromiumResponseRead
	responseVersion uint64
	failure         error
}

type uploadWrite struct {
	body    *queuedUpload
	control func() error
	headers *queuedUploadHeaders
}

type queuedUploadHeaders struct {
	cs      *clientStream
	initial bool
	end     bool
	encode  func() ([]byte, error) // 持有连接锁及写锁
	onSent  func()                 // HEADERS 写完后持有连接锁调用，正文立即进入同一写队列
	done    chan error
}

func (cs *clientStream) queueRequestHeaders(req *http.Request, dumps []*dump.Dumper, onSent func()) error {
	cc := cs.cc
	trailers, err := commaSeparatedTrailers(req)
	if err != nil {
		cc.mu.Lock()
		cc.releaseOpeningStreamLocked()
		cc.mu.Unlock()
		<-cc.reqHeaderMu
		return err
	}
	contentLen := actualContentLength(req)
	h := &queuedUploadHeaders{cs: cs, initial: true, end: contentLen == 0 && trailers == "", onSent: onSent, done: make(chan error, 1), encode: func() ([]byte, error) {
		return cc.encodeHeaders(req, cs.requestedGzip, trailers, contentLen, dumps)
	}}
	cc.queueHeaders(h)
	<-cc.reqHeaderMu
	return cc.waitQueuedHeaders(h)
}

func (cs *clientStream) queueUploadTrailers(req *http.Request, dumps []*dump.Dumper, maxFrameSize int) error {
	cc := cs.cc
	cc.mu.Lock()
	trailer := req.Trailer
	err := cs.abortErr
	cc.mu.Unlock()
	if err != nil {
		return err
	}
	for key, values := range trailer {
		if _, valid := lowerHeader(key); valid && len(values) != 0 {
			h := &queuedUploadHeaders{cs: cs, end: true, done: make(chan error, 1), encode: func() ([]byte, error) {
				return cc.encodeTrailers(trailer, dumps)
			}}
			cc.queueHeaders(h)
			return cc.waitQueuedHeaders(h)
		}
	}
	return cs.writeUploadTrailers(req, dumps, maxFrameSize)
}

func (cc *ClientConn) queueHeaders(h *queuedUploadHeaders) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if h.initial && cc.goAway != nil {
		cc.finishQueuedHeadersLocked(h, errClientConnGotGoAway)
		return
	}
	if err := cc.addUploadEventLocked(uploadEvent{headers: h}); err != nil {
		cc.finishQueuedHeadersLocked(h, err)
	}
}

func (cc *ClientConn) waitQueuedHeaders(h *queuedUploadHeaders) error {
	var err error
	select {
	case err = <-h.done:
		return err
	case <-h.cs.abort:
		err = h.cs.abortErr
	case <-h.cs.ctx.Done():
		err = h.cs.ctx.Err()
	case <-h.cs.reqCancel:
		err = common.ErrRequestCanceled
	}
	cc.mu.Lock()
	if i := slices.IndexFunc(cc.uploadEvents, func(e uploadEvent) bool { return e.headers == h }); i >= 0 {
		cc.uploadEvents = slices.Delete(cc.uploadEvents, i, i+1)
		cc.finishQueuedHeadersLocked(h, err)
	} else if i := slices.IndexFunc(cc.uploadQueue, func(w uploadWrite) bool { return w.headers == h }); i >= 0 {
		cc.uploadQueue = slices.Delete(cc.uploadQueue, i, i+1)
		cc.finishQueuedHeadersLocked(h, err)
	}
	cc.mu.Unlock()
	// 已出队的头块必须写完，HPACK 状态只能按实际发送顺序推进。
	return <-h.done
}

func (cc *ClientConn) releaseOpeningStreamLocked() {
	cc.uploadOpening--
	if cc.uploadOpening == 0 && len(cc.streams) == 0 && cc.idleTimer != nil && !cc.closed && cc.uploadDrainErr == nil {
		cc.lastIdle = time.Now()
		cc.idleTimer.Reset(cc.idleTimeout)
	}
	cc.cond.Broadcast()
}

func (cc *ClientConn) finishQueuedHeadersLocked(h *queuedUploadHeaders, err error) {
	if h.initial {
		cc.releaseOpeningStreamLocked()
	}
	h.done <- err
}

func (cc *ClientConn) writeQueuedHeaders(h *queuedUploadHeaders) (writeErr error) {
	cc.wmu.Lock()
	cc.mu.Lock()
	cs := h.cs
	err := cs.abortErr
	if err == nil {
		err = cs.ctx.Err()
	}
	if err == nil {
		select {
		case <-cs.reqCancel:
			err = common.ErrRequestCanceled
		default:
		}
	}
	if err == nil {
		switch {
		case cc.uploadDrainErr != nil:
			err = cc.uploadDrainErr
		case cc.closed:
			err = errClientConnClosed
		case h.initial && cc.goAway != nil:
			err = errClientConnGotGoAway
		case h.initial && cc.closing:
			err = errClientConnUnusable
		}
	}
	var block []byte
	if err == nil {
		block, err = h.encode()
	}
	if err != nil {
		cc.finishQueuedHeadersLocked(h, err)
		cc.mu.Unlock()
		cc.wmu.Unlock()
		return nil
	}
	priority := cc.t.HeaderPriority
	if h.initial {
		cc.addStreamLocked(cs)
		priority = cc.uploadHeaderPriorityLocked(cs.ID)
		cc.releaseOpeningStreamLocked()
		cs.sentHeaders = true
	}
	maxFrameSize := int(cc.maxFrameSize)
	cc.mu.Unlock()
	writeErr = cc.writeHeaders(cs.ID, h.end, maxFrameSize, block, priority)
	if h.initial {
		traceWroteHeaders(cs.trace)
	}
	cc.wmu.Unlock()
	cc.mu.Lock()
	cc.resumeUploadWritesLocked()
	if writeErr == nil {
		if h.end {
			cs.uploadEnded = true
		}
		if h.onSent != nil {
			h.onSent()
		}
	}
	cc.mu.Unlock()
	h.done <- writeErr
	return writeErr
}

// 同优先级请求依赖最新的存活流，对齐 Chromium Http2PriorityDependencies。
// 本连接的 HeaderPriority 是统一权重；显式指定父流时保留调用者的设置。
func (cc *ClientConn) uploadHeaderPriorityLocked(streamID uint32) http2.PriorityParam {
	priority := cc.t.HeaderPriority
	if priority.IsZero() || priority.StreamDep != 0 {
		return priority
	}
	for id, stream := range cc.streams {
		if id >= streamID || id <= priority.StreamDep || !stream.sentHeaders || !stream.chromiumStreamActiveLocked() {
			continue
		}
		priority.StreamDep = id
	}
	return priority
}

// 收到 END_STREAM 只关闭远端一侧；本端尚在上传时，官方仍保留活动流。
// 流移除、取消或双向结束后，消费回调不再归还流窗口。
func (cs *clientStream) chromiumStreamActiveLocked() bool {
	if cs.cc.streams[cs.ID] != cs || cs.abortErr != nil {
		return false
	}
	if cs.uploadEnded {
		select {
		case <-cs.peerClosed:
			return false
		default:
		}
	}
	return true
}

func (cc *ClientConn) discardQueuedUploadHeadersLocked(err error, initialOnly bool) {
	discard := func(h *queuedUploadHeaders) bool {
		if h == nil || initialOnly && !h.initial {
			return false
		}
		cc.finishQueuedHeadersLocked(h, err)
		return true
	}
	cc.uploadQueue = slices.DeleteFunc(cc.uploadQueue, func(w uploadWrite) bool {
		return discard(w.headers)
	})
	cc.uploadEvents = slices.DeleteFunc(cc.uploadEvents, func(e uploadEvent) bool {
		return discard(e.headers)
	})
}

type queuedUpload struct {
	cs              *clientStream
	body            io.ReadCloser
	dumps           []*dump.Dumper
	buf             []byte
	remain          []byte
	remainingLength int64
	hasLength       bool
	trailers        bool
	eof             bool
	sentEnd         bool
	active          bool
	queued          bool
	stalled         bool
	inWindowQueue   bool
	grant           int32
	peekN           int
	done            chan error
}

func (cs *clientStream) newQueuedUploadLocked(req *http.Request, dumps []*dump.Dumper) *queuedUpload {
	cc := cs.cc
	frameSize := int(cc.maxFrameSize)
	if limit := cc.t.MaxUploadFrameSize; limit != 0 {
		frameSize = min(frameSize, int(limit))
	}
	u := &queuedUpload{cs: cs, body: cs.reqBody, dumps: dumps, buf: make([]byte, cs.frameScratchBufferLen(frameSize)), remainingLength: cs.reqBodyContentLength, hasLength: cs.reqBodyContentLength != -1, trailers: req.Trailer != nil, active: true, done: make(chan error, 1)}
	cs.upload = u
	if err := cc.uploadErrorLocked(u); err != nil {
		cc.finishUploadLocked(u, err)
	}
	return u
}

func (cs *clientStream) startQueuedUploadLocked(req *http.Request, dumps []*dump.Dumper) *queuedUpload {
	u := cs.newQueuedUploadLocked(req, dumps)
	if u.active {
		cs.cc.readUploadLocked(u, false)
	}
	return u
}

func (cs *clientStream) writeQueuedUpload(req *http.Request, dumps []*dump.Dumper) error {
	cc := cs.cc
	cc.mu.Lock()
	u := cs.newQueuedUploadLocked(req, dumps)
	if u.active {
		cc.postUploadTaskLocked(uploadEvent{body: u, startRead: true})
	}
	cc.mu.Unlock()
	return cs.awaitQueuedUpload(req, dumps, u)
}

func (cs *clientStream) awaitQueuedUpload(req *http.Request, dumps []*dump.Dumper, u *queuedUpload) error {
	cc := cs.cc
	var err error
	select {
	case err = <-u.done:
	case <-cs.abort:
		err = cs.abortErr
	case <-cs.ctx.Done():
		err = cs.ctx.Err()
	case <-cs.reqCancel:
		err = common.ErrRequestCanceled
	}
	cc.mu.Lock()
	cc.finishUploadLocked(u, err)
	sentEnd := u.sentEnd
	maxFrameSize := int(cc.maxFrameSize)
	cc.mu.Unlock()
	if err != nil || sentEnd {
		return err
	}
	return cs.queueUploadTrailers(req, dumps, maxFrameSize)
}

func (cc *ClientConn) uploadErrorLocked(u *queuedUpload) error {
	if cc.closed {
		return errClientConnClosed
	}
	if cc.uploadDrainErr != nil {
		return cc.uploadDrainErr
	}
	if u.cs.abortErr != nil {
		return u.cs.abortErr
	}
	if err := u.cs.ctx.Err(); err != nil {
		return err
	}
	if u.cs.reqBodyClosed != nil {
		return errStopReqBodyWrite
	}
	select {
	case <-u.cs.reqCancel:
		return common.ErrRequestCanceled
	default:
		return nil
	}
}

// 读取完成处理与排帧都持有连接锁；实际等待正文不占用连接写循环。
func (cc *ClientConn) readUploadLocked(u *queuedUpload, peek bool) {
	p := u.buf
	if peek {
		p = make([]byte, 1)
	}
	if reader, ok := u.body.(availableUploadReader); ok {
		if n, err, ready := reader.ReadAvailable(p); ready {
			cc.uploadReadLocked(u, n, err, peek)
			return
		}
	}
	cc.readUploadAsyncLocked(u, p, peek)
}

func (cc *ClientConn) readUploadAsyncLocked(u *queuedUpload, p []byte, peek bool) {
	go func() {
		n, err := u.body.Read(p)
		cc.mu.Lock()
		defer cc.mu.Unlock()
		if u.active {
			cc.postUploadTaskLocked(uploadEvent{body: u, n: n, err: err, peek: peek})
		}
	}()
}

func (cc *ClientConn) uploadReadLocked(u *queuedUpload, n int, err error, peek bool) {
	if e := cc.uploadErrorLocked(u); e != nil {
		cc.finishUploadLocked(u, e)
		return
	}
	if u.hasLength {
		u.remainingLength -= int64(n)
		if u.remainingLength < 0 {
			cc.finishUploadLocked(u, errReqBodyTooLong)
			return
		}
		if !peek && u.remainingLength == 0 && err == nil {
			u.peekN = n
			cc.readUploadLocked(u, true)
			return
		}
	}
	if peek {
		n = u.peekN
	}
	if err != nil && err != io.EOF {
		cc.finishUploadLocked(u, err)
		return
	}
	u.eof = err == io.EOF
	u.remain = u.buf[:n]
	if n == 0 && !u.eof {
		// 零字节重试已经让出同步读取；完成后也须经过事件 FIFO，
		// 不能再次借 ReadAvailable 绕过正在进行的同步写循环。
		cc.readUploadAsyncLocked(u, u.buf, false)
		return
	}
	if u.eof && n == 0 && u.trailers {
		cc.finishUploadLocked(u, nil)
		return
	}
	cc.queueUploadFrameLocked(u)
}

func (cc *ClientConn) queueUploadFrameLocked(u *queuedUpload) {
	if !u.active {
		return
	}
	if err := cc.uploadErrorLocked(u); err != nil {
		cc.finishUploadLocked(u, err)
		return
	}
	// 官方 CreateDataBuffer 即使没有 payload 也先检查两个窗口。
	// SETTINGS 缩窗可使流窗口为负，必须等它恢复为正后才排空结束帧。
	if u.cs.flow.available() <= 0 {
		u.stalled = true
		if !u.inWindowQueue {
			u.inWindowQueue = true
			cc.uploadStalled = append(cc.uploadStalled, u)
		}
		return
	}
	n := min(int32(len(u.remain)), u.cs.flow.available(), int32(cc.maxFrameSize))
	if limit := cc.t.MaxUploadFrameSize; limit != 0 {
		n = min(n, int32(limit))
	}
	u.cs.flow.take(n)
	u.grant = n
	u.queued = true
	u.stalled = false
	cc.uploadQueue = append(cc.uploadQueue, uploadWrite{body: u})
	cc.startUploadWriterLocked()
}

func (cc *ClientConn) resumeUploadsLocked(cs *clientStream) {
	if cs != nil {
		if u := cs.upload; u != nil && u.active && u.stalled && u.cs.flow.available() > 0 {
			// 单流补窗保留该流最早的等待位置。连接补窗扫描到这里时，
			// 只有再次受流控阻塞的流才会恢复，避免同一个流积累多个条目。
			cc.queueUploadFrameLocked(u)
		}
		return
	}
	waiting := cc.uploadStalled
	cc.uploadStalled = nil
	for len(waiting) > 0 && cc.flow.n > 0 {
		u := waiting[0]
		waiting[0] = nil
		waiting = waiting[1:]
		u.inWindowQueue = false
		if u.stalled {
			cc.queueUploadFrameLocked(u)
		}
	}
	// 连接窗口再次耗尽时，未扫描的流仍排在仅因单流缺窗而重新入队的流之前。
	cc.uploadStalled = append(waiting, cc.uploadStalled...)
}

func (cc *ClientConn) refundUploadLocked(u *queuedUpload) {
	if u.grant > 0 {
		cc.flow.add(u.grant)
		u.cs.flow.add(u.grant)
		u.grant = 0
		cc.resumeUploadsLocked(nil)
	}
}

func (cc *ClientConn) finishUploadLocked(u *queuedUpload, err error) {
	if !u.active {
		return
	}
	u.active = false
	u.cs.upload = nil
	if u.inWindowQueue {
		cc.uploadStalled = slices.DeleteFunc(cc.uploadStalled, func(v *queuedUpload) bool { return v == u })
		u.inWindowQueue = false
	}
	u.stalled = false
	if u.queued {
		cc.uploadQueue = slices.DeleteFunc(cc.uploadQueue, func(v uploadWrite) bool { return v.body == u })
		u.queued = false
		cc.refundUploadLocked(u)
	}
	u.done <- err
}

// 只由能区分同步完成与 TLS pending 的连接调用；没有该能力的连接保留原路径。
func (cc *ClientConn) uploadWriteState(pending bool) {
	cc.mu.Lock()
	if pending && cc.uploadWriteOwner {
		cc.uploadWriteOwner = false
		cc.uploadWriteYielded = true
	}
	cc.scheduleUploadTasksLocked()
	cc.cond.Broadcast()
	cc.mu.Unlock()
}

func (cc *ClientConn) queueUploadControlLocked(write func() error, normal bool) {
	if normal {
		cc.uploadQueue = append(cc.uploadQueue, uploadWrite{control: write})
	} else {
		cc.uploadControls = append(cc.uploadControls, write)
	}
	cc.startUploadWriterLocked()
}

func (cc *ClientConn) startUploadWriterLocked() {
	if !cc.uploadWriting {
		cc.uploadWriting = true
		cc.postUploadTaskLocked(uploadEvent{writeTask: true})
	}
}

func (cc *ClientConn) addUploadEventLocked(event uploadEvent) error {
	if cc.closed {
		return errClientConnClosed
	}
	if cc.uploadDrainErr != nil {
		return cc.uploadDrainErr
	}
	if event.control != nil || event.settings != nil && !event.settings.IsAck() {
		if cc.uploadControlCount > maxQueuedUploadControls {
			return errUploadControlQueueLimit
		}
		cc.uploadControlCount++
	}
	if event.fromRead && (cc.uploadReading || cc.uploadApplying) {
		return cc.applyUploadEventLocked(event)
	}
	cc.postUploadTaskLocked(event)
	return nil
}

func (cc *ClientConn) applyUploadEventLocked(event uploadEvent) error {
	if event.failure != nil {
		return event.failure
	}
	if event.responseRead != nil {
		if event.responseVersion == 0 {
			event.responseRead.beginLocked()
		} else {
			event.responseRead.notifyLocked(event.responseVersion)
		}
		return nil
	}
	if event.headers != nil {
		cc.uploadQueue = append(cc.uploadQueue, uploadWrite{headers: event.headers})
		cc.startUploadWriterLocked()
		return nil
	}
	if event.control != nil {
		cc.queueUploadControlLocked(event.control, event.normal)
		return nil
	}
	if event.settings != nil {
		rl := clientConnReadLoop{cc: cc}
		if err := rl.processSettingsLocked(event.settings); err != nil {
			return err
		}
		if !event.settings.IsAck() {
			cc.queueUploadControlLocked(cc.fr.WriteSettingsAck, false)
		}
		cc.cond.Broadcast()
		return nil
	}
	if event.body != nil {
		if event.body.active {
			if event.startRead {
				cc.readUploadLocked(event.body, false)
			} else {
				cc.uploadReadLocked(event.body, event.n, event.err, event.peek)
			}
		}
		return nil
	}
	cs := event.cs
	if cs != nil && cc.streams[cs.ID] != cs {
		return nil
	}
	if event.increment == 0 {
		if cs == nil {
			return uploadProtocolError{ConnectionError(ErrCodeProtocol), "Received WINDOW_UPDATE with an invalid delta_window_size 0"}
		}
		return cs.resetFromReadLocked(StreamError{StreamID: cs.ID, Code: ErrCodeFlowControl})
	}
	flow := &cc.flow
	if cs != nil {
		flow = &cs.flow
	}
	if !flow.add(event.increment) {
		if cs == nil {
			return uploadProtocolError{ConnectionError(ErrCodeProtocol), fmt.Sprintf("Received WINDOW_UPDATE [delta: %d] for session overflows session_send_window_size_ [current: %d]", event.increment, flow.n)}
		}
		return cs.resetFromReadLocked(StreamError{StreamID: cs.ID, Code: ErrCodeFlowControl})
	}
	cc.resumeUploadsLocked(cs)
	return nil
}

type uploadProtocolError struct {
	ConnectionError
	debugData string
}

func (e uploadProtocolError) Error() string { return e.ConnectionError.Error() + ": " + e.debugData }

func (e uploadProtocolError) Unwrap() error { return e.ConnectionError }

func uploadDrainable(err error) bool {
	var code ConnectionError
	return errors.As(err, &code) || errors.Is(err, errUploadControlQueueLimit)
}

// 终止活动流，保留已开始的帧和排队的会话控制帧。
// 协议错误追加 GOAWAY；写失败及显式 Close 仍立即中断连接。
func (cc *ClientConn) beginUploadDrainLocked(err error) {
	if cc.closed || cc.uploadDrainErr != nil {
		return
	}
	cc.closing = true
	cc.uploadDrainErr = err
	if cc.idleTimer != nil {
		cc.idleTimer.Stop()
	}
	for _, cs := range cc.streams {
		cs.abortStreamLocked(err)
	}
	cc.discardQueuedUploadHeadersLocked(err, false)
	// FIFO 中尚未应用的 SETTINGS 不再影响已终止的流，但其 ACK 仍须保留。
	events := cc.uploadEvents
	cc.uploadEvents = nil
	for _, event := range events {
		if event.control != nil {
			cc.queueUploadControlLocked(event.control, event.normal)
		} else if event.settings != nil && !event.settings.IsAck() {
			cc.queueUploadControlLocked(cc.fr.WriteSettingsAck, false)
		} else if event.writeTask || event.readReady != nil || event.writeReady != nil || event.responseRead != nil && event.responseVersion == 0 {
			cc.uploadEvents = append(cc.uploadEvents, event)
		}
	}
	var code ConnectionError
	if errors.As(err, &code) && cc.uploadControlCount <= maxQueuedUploadControls {
		var detail uploadProtocolError
		errors.As(err, &detail)
		cc.uploadControlCount++
		cc.queueUploadControlLocked(func() error {
			return cc.fr.WriteGoAway(0, ErrCode(code), []byte(detail.debugData))
		}, false)
	}
	cc.scheduleUploadTasksLocked()
	cc.cond.Broadcast()
}

func (cc *ClientConn) drainUploadControls(err error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.beginUploadDrainLocked(err)
	for (cc.uploadWriting || len(cc.uploadEvents) > 0 || cc.uploadTaskRunning) && !cc.closed {
		cc.cond.Wait()
	}
}

func (cc *ClientConn) queueWindowUpdatesLocked(streamID uint32, connAdd, streamAdd int32, fromRead bool) error {
	for _, update := range []struct {
		id    uint32
		delta int32
	}{{0, connAdd}, {streamID, streamAdd}} {
		if update.delta > 0 {
			if err := cc.addUploadEventLocked(uploadEvent{normal: update.id != 0, fromRead: fromRead, control: func() error { return cc.fr.WriteWindowUpdate(update.id, uint32(update.delta)) }}); err != nil {
				return err
			}
		}
	}
	return nil
}

// 读取循环外触发超限时，先进入排空状态，再唤醒等待读 socket 的循环。
func (cc *ClientConn) uploadControlError(err error) {
	if err == nil {
		return
	}
	if !uploadDrainable(err) {
		cc.closeForError(err)
		return
	}
	cc.mu.Lock()
	cc.beginUploadDrainLocked(err)
	cc.mu.Unlock()
	if wakeErr := cc.tconn.SetReadDeadline(time.Now()); wakeErr != nil {
		cc.logf("http2: 异常排空后唤醒读取失败：%v", wakeErr)
		cc.closeForError(wakeErr)
	}
}

func (cc *ClientConn) runUploadWrites() {
	for {
		cc.mu.Lock()
		if cc.closed {
			cc.discardQueuedUploadHeadersLocked(errClientConnClosed, false)
			cc.uploadQueue = nil
			cc.uploadControls = nil
			cc.uploadControlCount = 0
			cc.uploadWriting = false
			cc.uploadWriteOwner = false
			cc.scheduleUploadTasksLocked()
			cc.cond.Broadcast()
			cc.mu.Unlock()
			return
		}
		var write func() error
		if len(cc.uploadControls) > 0 {
			write = cc.uploadControls[0]
			cc.uploadControls[0] = nil
			cc.uploadControls = cc.uploadControls[1:]
		} else if len(cc.uploadQueue) > 0 && cc.uploadQueue[0].control != nil {
			write = cc.uploadQueue[0].control
			cc.uploadQueue[0] = uploadWrite{}
			cc.uploadQueue = cc.uploadQueue[1:]
		}
		if write != nil {
			cc.uploadControlCount--
			cc.mu.Unlock()
			cc.wmu.Lock()
			err := write()
			if err == nil {
				err = cc.bw.Flush()
			}
			cc.wmu.Unlock()
			cc.mu.Lock()
			cc.resumeUploadWritesLocked()
			cc.mu.Unlock()
			if err != nil {
				cc.closeForError(err)
			}
			continue
		}
		if len(cc.uploadQueue) == 0 {
			cc.uploadWriting = false
			cc.uploadWriteOwner = false
			cc.scheduleUploadTasksLocked()
			cc.cond.Broadcast()
			cc.mu.Unlock()
			return
		}
		if h := cc.uploadQueue[0].headers; h != nil {
			cc.uploadQueue[0] = uploadWrite{}
			cc.uploadQueue = cc.uploadQueue[1:]
			cc.mu.Unlock()
			if err := cc.writeQueuedHeaders(h); err != nil {
				cc.closeForError(err)
			}
			continue
		}
		u := cc.uploadQueue[0].body
		cc.uploadQueue[0] = uploadWrite{}
		cc.uploadQueue = cc.uploadQueue[1:]
		u.queued = false
		cc.mu.Unlock()
		// 取写锁后再检查流是否有效，保持 wmu -> mu 的锁序。
		cc.wmu.Lock()
		cc.mu.Lock()
		if err := cc.uploadErrorLocked(u); err != nil {
			cc.finishUploadLocked(u, err)
		}
		if !u.active {
			cc.refundUploadLocked(u)
			cc.mu.Unlock()
			cc.wmu.Unlock()
			continue
		}
		n := int(u.grant)
		data := u.remain[:n]
		end := u.eof && n == len(u.remain) && !u.trailers
		cc.mu.Unlock()
		for _, d := range u.dumps {
			d.DumpRequestBody(data)
		}
		err := cc.fr.WriteData(u.cs.ID, end, data)
		if err == nil {
			err = cc.bw.Flush()
		}
		cc.wmu.Unlock()
		cc.mu.Lock()
		cc.resumeUploadWritesLocked()
		u.grant = 0
		if err == nil && end {
			u.cs.uploadEnded = true
		}
		if u.active {
			if err != nil {
				cc.finishUploadLocked(u, err)
			} else {
				u.remain = u.remain[n:]
				u.sentEnd = end
				if len(u.remain) > 0 {
					cc.queueUploadFrameLocked(u)
				} else if u.eof {
					cc.finishUploadLocked(u, nil)
				} else {
					cc.readUploadLocked(u, false)
				}
			}
		}
		cc.mu.Unlock()
		if err != nil {
			cc.closeForError(err)
		}
	}
}
