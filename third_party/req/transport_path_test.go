// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package req

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPathWithoutIndexClone(t *testing.T) {
	tr := NewTransport()
	if tr.t2.PathWithoutIndex {
		t.Fatal("原库默认行为被改变")
	}
	tr.SetHTTP2PathWithoutIndex(true)
	clone := tr.Clone()
	if !clone.t2.PathWithoutIndex {
		t.Fatal("Clone 丢失路径索引配置")
	}
	clone.SetHTTP2PathWithoutIndex(false)
	if !tr.t2.PathWithoutIndex {
		t.Fatal("Clone 共享了传输设置")
	}
}

func TestRespectHeaderTableSizeClone(t *testing.T) {
	tr := NewTransport()
	if tr.t2.RespectHeaderTableSize {
		t.Fatal("原库默认行为被改变")
	}
	tr.SetHTTP2RespectHeaderTableSize(true)
	clone := tr.Clone()
	if !clone.t2.RespectHeaderTableSize {
		t.Fatal("Clone 丢失 HPACK 表大小配置")
	}
	clone.SetHTTP2RespectHeaderTableSize(false)
	if !tr.t2.RespectHeaderTableSize {
		t.Fatal("Clone 共享了传输设置")
	}
}

func TestIndexOversizedClone(t *testing.T) {
	tr := NewTransport()
	if tr.t2.IndexOversized {
		t.Fatal("原库默认行为被改变")
	}
	tr.SetHTTP2IndexOversized(true)
	clone := tr.Clone()
	if !clone.t2.IndexOversized {
		t.Fatal("Clone 丢失超大字段入表配置")
	}
	clone.SetHTTP2IndexOversized(false)
	if !tr.t2.IndexOversized {
		t.Fatal("Clone 共享了传输设置")
	}
}

func TestChromiumFlowControlClone(t *testing.T) {
	tr := NewTransport()
	if tr.t2.ChromiumFlowControl {
		t.Fatal("原库默认流控被改变")
	}
	tr.SetHTTP2ChromiumFlowControl(true)
	clone := tr.Clone()
	if !clone.t2.ChromiumFlowControl {
		t.Fatal("Clone 丢失 Chromium 流控设置")
	}
	clone.SetHTTP2ChromiumFlowControl(false)
	if !tr.t2.ChromiumFlowControl {
		t.Fatal("Clone 共享了流控设置")
	}
}

func TestMaxUploadFrameSizeClone(t *testing.T) {
	tr := NewTransport()
	if tr.t2.MaxUploadFrameSize != 0 {
		t.Fatal("原库默认上传分片被改变")
	}
	tr.SetHTTP2MaxUploadFrameSize(16375)
	clone := tr.Clone()
	if clone.t2.MaxUploadFrameSize != 16375 {
		t.Fatal("Clone 丢失上传分片设置")
	}
	clone.SetHTTP2MaxUploadFrameSize(0)
	if tr.t2.MaxUploadFrameSize != 16375 {
		t.Fatal("Clone 共享了上传分片设置")
	}
}

func TestChromiumRetryClone(t *testing.T) {
	tr := NewTransport()
	if tr.t2.ChromiumRetry {
		t.Fatal("原库默认重试被改变")
	}
	tr.SetHTTP2ChromiumRetry(true)
	clone := tr.Clone()
	if !clone.t2.ChromiumRetry {
		t.Fatal("Clone 丢失重试策略")
	}
	clone.SetHTTP2ChromiumRetry(false)
	if !tr.t2.ChromiumRetry {
		t.Fatal("Clone 共享了重试设置")
	}
}

func TestChromiumUploadClone(t *testing.T) {
	tr := NewTransport()
	if tr.t2.ChromiumUpload {
		t.Fatal("原库默认上传调度被改变")
	}
	tr.SetHTTP2ChromiumUpload(true)
	clone := tr.Clone()
	if !clone.t2.ChromiumUpload {
		t.Fatal("Clone 丢失上传队列设置")
	}
	clone.SetHTTP2ChromiumUpload(false)
	if !tr.t2.ChromiumUpload {
		t.Fatal("Clone 共享了上传队列设置")
	}
}

type trackingReadyTestBody struct {
	*strings.Reader
	ready bool
}

func (b *trackingReadyTestBody) Close() error { return nil }
func (b *trackingReadyTestBody) ReadAvailable(p []byte) (int, error, bool) {
	if !b.ready {
		return 0, nil, false
	}
	n, err := b.Read(p)
	return n, err, true
}

func TestReadTrackingBodyPreservesReadinessAndRewindDecision(t *testing.T) {
	body := &trackingReadyTestBody{Reader: strings.NewReader("body")}
	req := setupRewindBody(&http.Request{Body: body})
	tracked := req.Body.(*readTrackingBody)
	buf := make([]byte, 1)
	if n, err, ready := tracked.ReadAvailable(buf); n != 0 || err != nil || ready || tracked.didRead {
		t.Fatal("未就绪的正文被消费或标记为已读")
	}
	if _, err := rewindBody(req); err != nil {
		t.Fatalf("未读取的不可回放正文无法重试：%v", err)
	}
	body.ready = true
	if n, err, ready := tracked.ReadAvailable(buf); n != 1 || err != nil || !ready || !tracked.didRead || string(buf) != "b" {
		t.Fatal("同步读取接口丢失或未记录正文读取")
	}
	if _, err := rewindBody(req); !errors.Is(err, errCannotRewind) {
		t.Fatalf("已读取的不可回放正文仍获准重试：%v", err)
	}
	plain := &readTrackingBody{ReadCloser: io.NopCloser(strings.NewReader("plain"))}
	if _, _, ready := plain.ReadAvailable(buf); ready || plain.didRead {
		t.Fatal("不支持同步读取的 Reader 被误判为就绪")
	}
}
