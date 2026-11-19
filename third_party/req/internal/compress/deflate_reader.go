// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package compress

import (
	"compress/flate"
	"io"
)

type DeflateReader struct {
	Body io.ReadCloser // underlying Response.Body
	dr   io.ReadCloser // lazily-initialized deflate reader
	derr error         // sticky error
}

func NewDeflateReader(body io.ReadCloser) *DeflateReader {
	return &DeflateReader{Body: body}
}

func (df *DeflateReader) Read(p []byte) (n int, err error) {
	if df.derr != nil {
		return 0, df.derr
	}
	if df.dr == nil {
		df.dr = flate.NewReader(df.Body)
	}
	return df.dr.Read(p)
}

func (df *DeflateReader) Close() error {
	if df.dr != nil {
		return df.dr.Close()
	}
	return df.Body.Close()
}

func (df *DeflateReader) GetUnderlyingBody() io.ReadCloser {
	return df.Body
}

func (df *DeflateReader) SetUnderlyingBody(body io.ReadCloser) {
	df.Body = body
}
