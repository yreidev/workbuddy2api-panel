// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

// 同一网络序列上的已投递任务。读批次和同步写循环各自执行到让出时，
// 才按投递顺序处理正文完成、下一次读取、写循环及 pending 写完成。
func (cc *ClientConn) postUploadTaskLocked(task uploadEvent) {
	cc.uploadEvents = append(cc.uploadEvents, task)
	cc.scheduleUploadTasksLocked()
}

func (cc *ClientConn) scheduleUploadTasksLocked() {
	if cc.uploadTaskRunning || len(cc.uploadEvents) == 0 || !cc.closed && (cc.uploadReading || cc.uploadWriteOwner) {
		return
	}
	cc.uploadTaskRunning = true
	go cc.runUploadTasks()
}

func (cc *ClientConn) runUploadTasks() {
	for {
		cc.mu.Lock()
		if cc.closed {
			cc.discardQueuedUploadHeadersLocked(errClientConnClosed, false)
			for _, task := range cc.uploadEvents {
				if task.responseRead != nil && task.responseVersion == 0 {
					task.responseRead.beginLocked()
				}
				if task.readReady != nil {
					close(task.readReady)
				}
				if task.writeReady != nil {
					close(task.writeReady)
				}
				if task.writeTask {
					cc.uploadWriting = false
				}
			}
			cc.uploadEvents = nil
			cc.uploadTaskRunning = false
			cc.cond.Broadcast()
			cc.mu.Unlock()
			return
		}
		if cc.uploadReading || cc.uploadWriteOwner || len(cc.uploadEvents) == 0 {
			if len(cc.uploadEvents) == 0 {
				cc.uploadEvents = nil
			}
			cc.uploadTaskRunning = false
			cc.cond.Broadcast()
			cc.mu.Unlock()
			return
		}
		task := cc.uploadEvents[0]
		cc.uploadEvents[0] = uploadEvent{}
		cc.uploadEvents = cc.uploadEvents[1:]
		var err error
		switch {
		case task.readReady != nil:
			cc.uploadReading = cc.uploadDrainErr == nil
			close(task.readReady)
		case task.writeReady != nil:
			cc.uploadWriteOwner = true
			close(task.writeReady)
		case task.writeTask:
			cc.uploadWriteOwner = true
			go cc.runUploadWrites()
		default:
			cc.uploadApplying = true
			err = cc.applyUploadEventLocked(task)
			cc.uploadApplying = false
		}
		cc.cond.Broadcast()
		if err != nil {
			cc.uploadTaskRunning = false
			cc.mu.Unlock()
			cc.uploadControlError(err)
			cc.mu.Lock()
			cc.scheduleUploadTasksLocked()
			cc.mu.Unlock()
			return
		}
		cc.mu.Unlock()
	}
}

// BIO 可以在一次 TLS 写中多次让出；逻辑写完成任务只在整个 Flush 返回后
// 投递。此时 TLS 的写锁已释放，等待网络序列不会阻塞 TLS 读取自身的写入。
func (cc *ClientConn) resumeUploadWritesLocked() {
	if !cc.uploadWriteYielded {
		return
	}
	cc.uploadWriteYielded = false
	ready := make(chan struct{})
	cc.postUploadTaskLocked(uploadEvent{writeReady: ready})
	cc.mu.Unlock()
	<-ready
	cc.mu.Lock()
}
