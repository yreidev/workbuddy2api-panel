// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"io"
	"time"
)

// Chromium SpdySession 每次读取 8 KiB，同步读到真正阻塞，或超过
// 32 KiB／20ms 后才让出。必须处理完同一批输入，才能开始其触发的写任务。
type uploadBatchReader struct {
	r     io.Reader
	cc    *ClientConn
	bytes int
	since time.Time
}

func (r *uploadBatchReader) state(reading bool) {
	if !reading {
		r.idle()
		return
	}
	r.cc.uploadReadState(true)
	if r.since.IsZero() {
		r.since = time.Now()
	}
}

func (r *uploadBatchReader) idle() {
	r.bytes = 0
	r.since = time.Time{}
	r.cc.uploadReadState(false)
}

func (r *uploadBatchReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.bytes > 32*1024 || !r.since.IsZero() && time.Since(r.since) > 20*time.Millisecond {
		r.idle()
	}
	r.state(true)
	n, err := r.r.Read(p[:min(len(p), 8*1024)])
	if n > 0 {
		r.bytes += n
	} else if err != nil {
		r.idle()
	}
	return n, err
}

func (cc *ClientConn) uploadReadState(reading bool) {
	cc.mu.Lock()
	if reading {
		if cc.uploadReading {
			cc.mu.Unlock()
			return
		}
		ready := make(chan struct{})
		cc.postUploadTaskLocked(uploadEvent{readReady: ready})
		cc.mu.Unlock()
		<-ready
		return
	}
	cc.uploadReading = false
	cc.scheduleUploadTasksLocked()
	cc.cond.Broadcast()
	cc.mu.Unlock()
}
