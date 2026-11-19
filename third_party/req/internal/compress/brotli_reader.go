// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package compress

import (
	"io"

	"github.com/andybalholm/brotli"
)

type BrotliReader struct {
	Body io.ReadCloser // underlying Response.Body
	br   io.Reader     // lazily-initialized brotli reader
	berr error         // sticky error
}

func NewBrotliReader(body io.ReadCloser) *BrotliReader {
	return &BrotliReader{Body: body}
}

func (br *BrotliReader) Read(p []byte) (n int, err error) {
	if br.berr != nil {
		return 0, br.berr
	}
	if br.br == nil {
		br.br = brotli.NewReader(br.Body)
	}
	return br.br.Read(p)
}

func (br *BrotliReader) Close() error {
	return br.Body.Close()
}

func (br *BrotliReader) GetUnderlyingBody() io.ReadCloser {
	return br.Body
}

func (br *BrotliReader) SetUnderlyingBody(body io.ReadCloser) {
	br.Body = body
}
