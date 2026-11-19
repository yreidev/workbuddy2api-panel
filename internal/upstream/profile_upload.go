// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
)

type profileUploadChunk struct {
	data []byte
	err  error
}

// NewBufferedRequest 使用已在内存里的正文，不复制数据；回放与连接写队列共用同份字节。
func NewBufferedRequest(ctx context.Context, method, endpoint string, data []byte) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil || len(data) == 0 {
		return r, err
	}
	r.GetBody = func() (io.ReadCloser, error) { return &profileBufferedUpload{Reader: bytes.NewReader(data)}, nil }
	r.Body, _ = r.GetBody()
	return r, nil
}

type profileBufferedUpload struct{ *bytes.Reader }

func (b *profileBufferedUpload) Close() error { return nil }

func (b *profileBufferedUpload) ReadAvailable(p []byte) (int, error, bool) {
	n, err := b.Read(p)
	return n, err, true
}

// profileStreamUploadBody 将生产正文与 HTTP/2 窗口等待分开；读取时合并已到达的块。
// Work 按 Mojo 默认管道限制未读字节，TTNet 按官方 bodyProviderPair 限制三条消息。
type profileStreamUploadBody struct {
	source    io.ReadCloser
	profile   ClientProfile
	mu        sync.Mutex
	cond      *sync.Cond
	chunks    []profileUploadChunk
	current   []byte
	bytes     int
	readErr   error
	closed    error
	closeOnce sync.Once
	closeErr  error
	stop      func() bool
	done      chan struct{}
	start     chan struct{}
	startOnce sync.Once
}

func newProfileStreamUploadBody(ctx context.Context, source io.ReadCloser, profile ClientProfile) *profileStreamUploadBody {
	b := &profileStreamUploadBody{source: source, profile: profile, done: make(chan struct{}), start: make(chan struct{})}
	b.cond = sync.NewCond(&b.mu)
	b.stop = context.AfterFunc(ctx, func() { b.close(ctx.Err()) })
	if profile != ProfileTraeWork {
		b.startReading()
	}
	go b.produce()
	return b
}

func (b *profileStreamUploadBody) produce() {
	defer close(b.done)
	<-b.start
	for {
		b.mu.Lock()
		closed := b.closed != nil
		b.mu.Unlock()
		if closed {
			return
		}
		// Go Reader 不暴露 JS Uint8Array 边界；每次 Read 是一个生产块。
		buf := make([]byte, 64<<10)
		n, err := b.source.Read(buf)
		if n > 0 && !b.enqueue(profileUploadChunk{data: buf[:n]}) {
			return
		}
		if err != nil {
			if err != io.EOF {
				err = fmt.Errorf("客户端 %d 读取上传正文: %w", b.profile, err)
			}
			b.enqueue(profileUploadChunk{err: err})
			return
		}
	}
}

// 浏览器等待 HTTP/2 并发槽位时不持续拉取 ReadableStream；取得槽位、
// 上传层首次读取后才启动管道生产，随后仍与流控等待独立。
func (b *profileStreamUploadBody) startReading() {
	b.startOnce.Do(func() { close(b.start) })
}

func (b *profileStreamUploadBody) enqueue(chunk profileUploadChunk) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		for b.closed == nil && ((b.profile == ProfileTraeCode && len(b.chunks) >= 3) || (b.profile == ProfileTraeWork && chunk.err == nil && b.bytes == 64<<10)) {
			b.cond.Wait()
		}
		if b.closed != nil {
			return false
		}
		if chunk.err != nil {
			b.chunks = append(b.chunks, chunk)
			b.cond.Broadcast()
			return true
		}
		n := len(chunk.data)
		if b.profile == ProfileTraeWork {
			n = min(n, (64<<10)-b.bytes)
		}
		b.chunks = append(b.chunks, profileUploadChunk{data: chunk.data[:n]})
		b.bytes += n
		chunk.data = chunk.data[n:]
		b.cond.Broadcast()
		if len(chunk.data) == 0 {
			return true
		}
	}
}

func (b *profileStreamUploadBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	b.startReading()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readLocked(p)
}

// ReadAvailable 只消费已到达的正文；返回 false 时由传输层异步等待读取完成。
func (b *profileStreamUploadBody) ReadAvailable(p []byte) (int, error, bool) {
	// CronetUploadDataStream::ReadInternal 总是返回 ERR_IO_PENDING；
	// provider 经 Tokio executor 读取，再把完成通知投回网络线程。
	// 即使消息已到达，也不能沿用 Work 的同步管道读取。
	if b.profile == ProfileTraeCode {
		return 0, nil, false
	}
	if len(p) > 0 {
		b.startReading()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) > 0 && b.closed == nil && b.readErr == nil && len(b.chunks) == 0 {
		return 0, nil, false
	}
	if len(p) == 0 {
		return 0, nil, true
	}
	n, err := b.readLocked(p)
	return n, err, true
}

func (b *profileStreamUploadBody) readLocked(p []byte) (int, error) {
	if b.profile == ProfileTraeCode {
		return b.readTTNetLocked(p)
	}
	for len(b.chunks) == 0 && b.readErr == nil && b.closed == nil {
		b.cond.Wait()
	}
	if b.closed != nil {
		return 0, b.closed
	}
	n := 0
	for len(b.chunks) > 0 {
		chunk := &b.chunks[0]
		if chunk.err != nil {
			b.readErr = chunk.err
			b.chunks[0] = profileUploadChunk{}
			b.chunks = b.chunks[1:]
			break
		}
		if n == len(p) {
			break
		}
		copied := copy(p[n:], chunk.data)
		n += copied
		b.bytes -= copied
		chunk.data = chunk.data[copied:]
		if len(chunk.data) == 0 {
			b.chunks[0] = profileUploadChunk{}
			b.chunks = b.chunks[1:]
		}
	}
	b.cond.Broadcast()
	return n, b.readErr
}

// 官方 UploadDataProvider 先复制当前消息的残片，再接收至多一条新消息；
// 残片用完时即使已复制部分字节，也会等待下一条消息或完成通知。
func (b *profileStreamUploadBody) readTTNetLocked(p []byte) (int, error) {
	n := copy(p, b.current)
	b.current = b.current[n:]
	b.bytes -= n
	if len(b.current) > 0 {
		return n, nil
	}
	for len(b.chunks) == 0 && b.readErr == nil && b.closed == nil {
		b.cond.Wait()
	}
	if b.closed != nil {
		return 0, b.closed
	}
	if len(b.chunks) > 0 {
		chunk := b.chunks[0]
		b.chunks[0] = profileUploadChunk{}
		b.chunks = b.chunks[1:]
		b.readErr = chunk.err
		copied := copy(p[n:], chunk.data)
		n += copied
		b.bytes -= copied
		b.current = chunk.data[copied:]
		b.cond.Broadcast()
	}
	return n, b.readErr
}

func (b *profileStreamUploadBody) close(reason error) {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = reason
		b.chunks = nil
		b.current = nil
		b.bytes = 0
		b.cond.Broadcast()
		b.mu.Unlock()
		b.startReading()
		b.closeErr = b.source.Close()
	})
}

func (b *profileStreamUploadBody) Close() error {
	b.stop()
	b.close(io.ErrClosedPipe)
	<-b.done
	return b.closeErr
}

func prepareProfileUpload(req *http.Request, profile ClientProfile) error {
	if req.Body == nil || req.Body == http.NoBody || req.GetBody != nil {
		return nil
	}
	switch profile {
	case ProfileTraeCode, ProfileTraeWork:
		req.Body = newProfileStreamUploadBody(req.Context(), req.Body, profile)
	case ProfileTraeAuth:
		// Electron 默认 ClientRequest 的 SlurpStream 在 finish 后才启动请求。
		source := req.Body
		var once sync.Once
		var closeErr error
		closeSource := func() { once.Do(func() { closeErr = source.Close() }) }
		stop := context.AfterFunc(req.Context(), closeSource)
		data, err := io.ReadAll(source)
		stop()
		closeSource()
		if req.Context().Err() != nil {
			return req.Context().Err()
		}
		if err != nil {
			return fmt.Errorf("授权客户端收取上传正文: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("授权客户端关闭上传正文: %w", closeErr)
		}
		req.GetBody = func() (io.ReadCloser, error) {
			if len(data) == 0 {
				return http.NoBody, nil
			}
			return &profileBufferedUpload{Reader: bytes.NewReader(data)}, nil
		}
		req.Body, _ = req.GetBody()
		req.ContentLength = int64(len(data))
	}
	return nil
}
