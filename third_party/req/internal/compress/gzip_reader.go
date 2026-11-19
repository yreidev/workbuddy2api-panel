// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package compress

import (
	"compress/gzip"
	"io"
	"io/fs"
)

// GzipReader wraps a response body so it can lazily
// call gzip.NewReader on the first call to Read
type GzipReader struct {
	Body io.ReadCloser // underlying Response.Body
	zr   *gzip.Reader  // lazily-initialized gzip reader
	zerr error         // sticky error
}

func NewGzipReader(body io.ReadCloser) *GzipReader {
	return &GzipReader{Body: body}
}

func (gz *GzipReader) Read(p []byte) (n int, err error) {
	if gz.zerr != nil {
		return 0, gz.zerr
	}
	if gz.zr == nil {
		gz.zr, err = gzip.NewReader(gz.Body)
		if err != nil {
			gz.zerr = err
			return 0, err
		}
	}
	return gz.zr.Read(p)
}

func (gz *GzipReader) Close() error {
	if err := gz.Body.Close(); err != nil {
		return err
	}
	gz.zerr = fs.ErrClosed
	return nil
}

func (gz *GzipReader) GetUnderlyingBody() io.ReadCloser {
	return gz.Body
}

func (gz *GzipReader) SetUnderlyingBody(body io.ReadCloser) {
	gz.Body = body
}
