// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"bytes"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

type socketBIOTestWriter struct {
	try  func([]byte) (int, bool, error)
	wait func([]byte) (int, error)
}

func (w socketBIOTestWriter) tryWrite(p []byte) (int, bool, error) { return w.try(p) }
func (w socketBIOTestWriter) write(p []byte) (int, error)          { return w.wait(p) }

func TestProfileSocketBIOSynchronousWrites(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	var wire bytes.Buffer
	var pending []bool
	c := makeProfileSocketBIO(raw, socketBIOTestWriter{try: func(p []byte) (int, bool, error) {
		// 正数短写还可同步继续，不能误当作异步 pending。
		n := min(len(p), 31)
		_, _ = wire.Write(p[:n])
		return n, false, nil
	}})
	defer c.Close()
	c.SetWritePendingCallback(func(p bool) { pending = append(pending, p) })
	body := bytes.Repeat([]byte("fixture"), 16384)
	if n, err := c.Write(body); err != nil || n != len(body) {
		t.Fatalf("同步写：n=%d err=%v", n, err)
	}
	if !bytes.Equal(wire.Bytes(), body) || len(pending) != 0 || c.used != 0 || c.working {
		t.Fatalf("同步写损坏或制造假 pending：events=%v buffered=%d worker=%t", pending, c.used, c.working)
	}
}

func TestProfileSocketBIOBufferCapacityAndWrap(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	type write struct {
		body []byte
		done chan int
	}
	writes := make(chan write)
	events := make(chan bool, 2)
	c := makeProfileSocketBIO(raw, socketBIOTestWriter{
		try: func([]byte) (int, bool, error) { return 0, true, nil },
		wait: func(p []byte) (int, error) {
			w := write{bytes.Clone(p), make(chan int)}
			writes <- w
			return <-w.done, nil
		},
	})
	defer c.Close()
	c.SetWritePendingCallback(func(p bool) { events <- p })
	a := bytes.Repeat([]byte{'a'}, profileSocketBIOCapacity-100)
	b := bytes.Repeat([]byte{'b'}, 100)
	d := bytes.Repeat([]byte{'c'}, 101)
	if n, err := c.Write(a); err != nil || n != len(a) {
		t.Fatalf("首块入缓冲：n=%d err=%v", n, err)
	}
	first := <-writes
	if n, err := c.Write(b); err != nil || n != len(b) {
		t.Fatalf("填满缓冲：n=%d err=%v", n, err)
	}
	select {
	case p := <-events:
		t.Fatalf("仅 TCP pending 就让出：%t", p)
	default:
	}
	done := make(chan error, 1)
	go func() { _, err := c.Write(d); done <- err }()
	select {
	case p := <-events:
		if !p {
			t.Fatal("缺少 pending 通知")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("缓冲满后未让出")
	}
	c.mu.Lock()
	used := c.used
	c.mu.Unlock()
	if used != profileSocketBIOCapacity {
		t.Fatalf("突破有界缓冲：%d", used)
	}
	// 部分写入后腾出空位；随后绕回头部，不能覆盖仍在途的尾部。
	accepted := len(first.body) - 50
	var wire bytes.Buffer
	wire.Write(first.body[:accepted])
	first.done <- accepted
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p := <-events; p {
		t.Fatal("SSL 写已完成但仍 pending")
	}
	for range 2 {
		next := <-writes
		wire.Write(next.body)
		next.done <- len(next.body)
	}
	c.workers.Wait()
	want := append(append(bytes.Clone(a), b...), d...)
	if !bytes.Equal(wire.Bytes(), want) {
		t.Fatalf("环形缓冲正文损坏：got=%d want=%d", wire.Len(), len(want))
	}
}

func TestProfileSocketBIOAsyncFailureWakesReadAndWrite(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	failure := errors.New("fixture socket write failure")
	c := makeProfileSocketBIO(raw, socketBIOTestWriter{
		try:  func([]byte) (int, bool, error) { return 0, true, nil },
		wait: func([]byte) (int, error) { return 0, failure },
	})
	defer c.Close()
	read := make(chan error, 1)
	go func() { _, err := c.Read(make([]byte, 1)); read <- err }()
	if _, err := c.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-read:
		if !errors.Is(err, failure) {
			t.Fatalf("读错误失去写失败原因：%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("写失败未唤醒读")
	}
	if n, err := c.Write([]byte("later")); n != 0 || !errors.Is(err, failure) {
		t.Fatalf("后续写失去原错误：n=%d err=%v", n, err)
	}
	c.workers.Wait()
}

func TestProfileSocketBIOCloseUnblocksPendingWrite(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	closed := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(closed); raw.Close() }) }
	defer stop()
	c := makeProfileSocketBIO(raw, socketBIOTestWriter{
		try:  func([]byte) (int, bool, error) { return 0, true, nil },
		wait: func([]byte) (int, error) { <-closed; return 0, net.ErrClosed },
	})
	// 回调内取消连接也不能与缓冲锁互相等待。
	c.SetWritePendingCallback(func(p bool) {
		if p {
			stop()
			_ = c.Close()
		}
	})
	done := make(chan error, 1)
	go func() { _, err := c.Write(make([]byte, profileSocketBIOCapacity+1)); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("取消写错误：%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("关闭未打断写缓冲等待")
	}
	_ = c.Close()
}

func TestProfileSocketBIOWriteDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	peer := <-accepted
	defer peer.Close()
	wrapped, err := newProfileSocketBIO(raw)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := wrapped.(*profileSocketBIO)
	if !ok {
		t.Skip("当前平台未接入同步 socket 写探测")
	}
	defer c.Close()
	if err := c.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Write([]byte("late")); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("同步探测绕过截止时间：n=%d err=%v", n, err)
	}
	// 第一次写失败已是该 TLS 写方向的最终错误，不能在随后排入缓冲。
	if n, err := c.Write([]byte("later")); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("截止时间错误未保持：n=%d err=%v", n, err)
	}
}

func TestProfileSocketBIOPendingWriteInterrupted(t *testing.T) {
	for _, operation := range []string{"deadline", "close"} {
		t.Run(operation, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				conn, err := ln.Accept()
				if err == nil {
					accepted <- conn
				}
			}()
			raw, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			peer := <-accepted
			defer peer.Close()
			if err := raw.(*net.TCPConn).SetWriteBuffer(4096); err != nil {
				t.Fatal(err)
			}
			wrapped, err := newProfileSocketBIO(raw)
			if err != nil {
				t.Fatal(err)
			}
			c, ok := wrapped.(*profileSocketBIO)
			if !ok {
				t.Skip("当前平台未接入同步 socket 写探测")
			}
			defer c.Close()
			pending := make(chan struct{})
			c.SetWritePendingCallback(func(p bool) {
				if p {
					close(pending)
				}
			})
			done := make(chan error, 1)
			go func() { _, err := c.Write(make([]byte, 2*1024*1024)); done <- err }()
			select {
			case <-pending:
			case <-time.After(2 * time.Second):
				t.Fatal("真实 socket 未进入缓冲满等待")
			}
			want := error(net.ErrClosed)
			if operation == "deadline" {
				want = os.ErrDeadlineExceeded
				err = c.SetWriteDeadline(time.Now())
			} else {
				err = c.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("等待写入时原错误丢失：%v，want=%v", err, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("超时或关闭没有唤醒等待写入")
			}
			c.workers.Wait()
		})
	}
}
