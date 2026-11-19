// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"io"
	"net"
	"sync"
)

const profileSocketBIOCapacity = 17 * 1024

type profileSocketWriter interface {
	tryWrite([]byte) (n int, pending bool, err error)
	write([]byte) (int, error)
}

// profileSocketBIO 对齐 Chromium 的有界密文写缓冲。原始 socket 暂不可写时，
// 只要缓冲还有容量，TLS 仍同步完成；只有 TLS 自己等空位时才让出上传循环。
type profileSocketBIO struct {
	net.Conn
	socket     profileSocketWriter
	writeMu    sync.Mutex
	mu         sync.Mutex
	cond       *sync.Cond
	buffer     [profileSocketBIOCapacity]byte
	head, used int
	working    bool
	writeErr   error
	pending    func(bool)
	readState  func(bool)
	workers    sync.WaitGroup
}

func newProfileSocketBIO(raw net.Conn) (net.Conn, error) {
	socket, err := profileSocketWriterFor(raw)
	if err != nil || socket == nil {
		return raw, err
	}
	return makeProfileSocketBIO(raw, socket), nil
}

func makeProfileSocketBIO(raw net.Conn, socket profileSocketWriter) *profileSocketBIO {
	c := &profileSocketBIO{Conn: raw, socket: socket}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *profileSocketBIO) SetWritePendingCallback(callback func(bool)) {
	c.mu.Lock()
	c.pending = callback
	c.mu.Unlock()
}

func (c *profileSocketBIO) SetReadStateCallback(callback func(bool)) bool {
	if _, ok := c.socket.(interface {
		tryRead([]byte) (int, bool, error)
	}); !ok {
		return false
	}
	c.mu.Lock()
	c.readState = callback
	c.mu.Unlock()
	return true
}

func (c *profileSocketBIO) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	length := len(p)
	c.mu.Lock()
	callback := c.pending
	yielded := false
	defer func() {
		c.mu.Unlock()
		if yielded && callback != nil {
			callback(false)
		}
	}()
	for len(p) > 0 {
		if c.writeErr != nil {
			return length - len(p), c.writeErr
		}
		if c.used == 0 {
			// 即使已有 worker 正准备退出，空缓冲也没有在途 syscall，
			// 可以保持同步写路径，不能因 goroutine 调度制造假 pending。
			n, pending, err := c.socket.tryWrite(p[:min(len(p), profileSocketBIOCapacity)])
			p = p[n:]
			if err != nil {
				c.writeErr = err
				return length - len(p), err
			}
			if !pending {
				continue
			}
		}
		if available := len(c.buffer) - c.used; available > 0 {
			n := min(len(p), available)
			tail := (c.head + c.used) % len(c.buffer)
			first := copy(c.buffer[tail:], p[:n])
			copy(c.buffer[:], p[first:n])
			c.used += n
			p = p[n:]
			if !c.working {
				c.working = true
				c.workers.Add(1)
				go c.flush()
			}
			continue
		}
		if !yielded {
			yielded = true
			c.mu.Unlock()
			if callback != nil {
				callback(true)
			}
			c.mu.Lock()
			continue
		}
		c.cond.Wait()
	}
	return length, nil
}

func (c *profileSocketBIO) flush() {
	defer c.workers.Done()
	for {
		c.mu.Lock()
		if c.writeErr != nil || c.used == 0 {
			c.working = false
			c.mu.Unlock()
			return
		}
		p := c.buffer[c.head : c.head+min(c.used, len(c.buffer)-c.head)]
		c.mu.Unlock()
		n, err := c.socket.write(p)
		if err == nil && n == 0 {
			err = io.ErrNoProgress
		}
		c.mu.Lock()
		c.head = (c.head + n) % len(c.buffer)
		c.used -= n
		if err != nil && c.writeErr == nil {
			c.writeErr = err
		}
		c.cond.Broadcast()
		failed := c.writeErr != nil
		c.mu.Unlock()
		if failed {
			// 异步写失败也须唤醒正在读 TLS 的调用者，并保留写错误来源。
			_ = c.Conn.Close()
		}
	}
}

func (c *profileSocketBIO) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	state := c.readState
	c.mu.Unlock()
	var n int
	var err error
	if state != nil {
		reader := c.socket.(interface {
			tryRead([]byte) (int, bool, error)
		})
		var pending bool
		n, pending, err = reader.tryRead(p)
		if pending {
			// RawConn.Read 已返回后才通知，避免连接关闭等待尚未释放的 fd 引用。
			state(false)
			n, err = c.Conn.Read(p)
			if n > 0 {
				state(true)
			}
		}
	} else {
		n, err = c.Conn.Read(p)
	}
	if n == 0 && err != nil {
		c.mu.Lock()
		if c.writeErr != nil {
			err = c.writeErr
		}
		c.mu.Unlock()
	}
	return n, err
}

func (c *profileSocketBIO) Close() error {
	c.mu.Lock()
	if c.writeErr == nil {
		c.writeErr = net.ErrClosed
	}
	c.cond.Broadcast()
	c.mu.Unlock()
	err := c.Conn.Close()
	c.workers.Wait()
	return err
}
