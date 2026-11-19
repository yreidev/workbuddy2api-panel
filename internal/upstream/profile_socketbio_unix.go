// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

//go:build unix

package upstream

import (
	"io"
	"net"
	"os"
	"syscall"
)

func (w *profileTCPSocketWriter) tryRead(p []byte) (n int, pending bool, err error) {
	var readErr error
	err = w.raw.Read(func(fd uintptr) bool {
		for {
			n, readErr = syscall.Read(int(fd), p)
			if readErr != syscall.EINTR {
				break
			}
		}
		if n < 0 {
			n = 0
		}
		pending = readErr == syscall.EAGAIN || readErr == syscall.EWOULDBLOCK
		return true
	})
	if err != nil || pending {
		return n, pending && err == nil, err
	}
	if readErr != nil {
		return n, false, &net.OpError{Op: "read", Net: w.conn.LocalAddr().Network(), Source: w.conn.LocalAddr(), Addr: w.conn.RemoteAddr(), Err: os.NewSyscallError("read", readErr)}
	}
	if n == 0 {
		return 0, false, io.EOF
	}
	return n, false, nil
}

type profileTCPSocketWriter struct {
	raw  syscall.RawConn
	conn *net.TCPConn
}

func profileSocketWriterFor(conn net.Conn) (profileSocketWriter, error) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		// TLS 代理等抽象连接不能绕过其外层协议直接操作 fd。
		return nil, nil
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return nil, err
	}
	return &profileTCPSocketWriter{raw: raw, conn: tcp}, nil
}

func (w *profileTCPSocketWriter) send(p []byte, wait bool) (n int, pending bool, err error) {
	var writeErr error
	err = w.raw.Write(func(fd uintptr) bool {
		for {
			n, writeErr = syscall.Write(int(fd), p)
			if writeErr != syscall.EINTR {
				break
			}
		}
		if n < 0 {
			n = 0
		}
		pending = writeErr == syscall.EAGAIN || writeErr == syscall.EWOULDBLOCK
		return !pending || !wait
	})
	if err != nil {
		return n, false, err
	}
	if pending {
		return n, true, nil
	}
	if writeErr != nil {
		err = &net.OpError{Op: "write", Net: w.conn.LocalAddr().Network(), Source: w.conn.LocalAddr(), Addr: w.conn.RemoteAddr(), Err: os.NewSyscallError("write", writeErr)}
	}
	return n, false, err
}

func (w *profileTCPSocketWriter) tryWrite(p []byte) (int, bool, error) {
	return w.send(p, false)
}

func (w *profileTCPSocketWriter) write(p []byte) (int, error) {
	n, _, err := w.send(p, true)
	return n, err
}
