// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import "time"

// 对齐 SpdyHttpStream：已有正文同步读；挂起读取收到小块后等待一毫秒，
// 填满调用方缓冲则投递零延迟回调；后续片段重新安排回调，流结束立即完成。
type chromiumResponseRead struct {
	cs      *clientStream
	buffer  []byte
	done    chan responseReadResult
	timer   timer
	version uint64
}

type responseReadResult struct {
	n   int
	err error
}

func (cs *clientStream) readChromiumResponse(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r := &chromiumResponseRead{cs: cs, buffer: p, done: make(chan responseReadResult, 1)}
	cs.cc.mu.Lock()
	cs.cc.postUploadTaskLocked(uploadEvent{responseRead: r})
	cs.cc.mu.Unlock()
	result := <-r.done
	return result.n, result.err
}

func (r *chromiumResponseRead) beginLocked() {
	cs := r.cs
	if n, err, ready := cs.bufPipe.readAvailable(r.buffer); ready {
		r.finishLocked(n, err)
		return
	}
	if cs.abortErr != nil {
		r.finishLocked(0, cs.abortErr)
		return
	}
	if cs.cc.closed || cs.cc.uploadDrainErr != nil {
		err := cs.cc.uploadDrainErr
		if err == nil {
			err = errClientConnClosed
		}
		r.finishLocked(0, err)
		return
	}
	cs.responseRead = r
}

func (r *chromiumResponseRead) scheduleLocked() {
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.version++
	version := r.version
	task := uploadEvent{responseRead: r, responseVersion: version}
	if r.cs.bufPipe.Len() >= len(r.buffer) {
		r.cs.cc.postUploadTaskLocked(task)
		return
	}
	r.timer = r.cs.cc.t.afterFunc(time.Millisecond, func() {
		cc := r.cs.cc
		cc.mu.Lock()
		defer cc.mu.Unlock()
		if r.cs.responseRead == r && r.version == version {
			cc.postUploadTaskLocked(task)
		}
	})
}

func (r *chromiumResponseRead) notifyLocked(version uint64) {
	if r.cs.responseRead == r && r.version == version {
		r.completeLocked()
	}
}

func (r *chromiumResponseRead) completeLocked() {
	n, err, ready := r.cs.bufPipe.readAvailable(r.buffer)
	if ready {
		r.finishLocked(n, err)
	}
}

func (r *chromiumResponseRead) finishLocked(n int, err error) {
	cs, cc := r.cs, r.cs.cc
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	if cs.responseRead == r {
		cs.responseRead = nil
	}
	if n > 0 && !cc.closed && cc.uploadDrainErr == nil {
		connAdd := cc.inflow.add(n)
		var streamAdd int32
		if err == nil && cs.chromiumStreamActiveLocked() {
			streamAdd = cs.inflow.add(n)
		}
		if writeErr := cc.queueWindowUpdatesLocked(cs.ID, connAdd, streamAdd, true); writeErr != nil {
			// 已交付的正文仍保留；错误交由现有协议排空路径处理。
			cc.postUploadTaskLocked(uploadEvent{failure: writeErr})
		}
	}
	r.done <- responseReadResult{n: n, err: err}
}
