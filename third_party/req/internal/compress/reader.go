// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package compress

import "io"

type CompressReader interface {
	io.ReadCloser
	GetUnderlyingBody() io.ReadCloser
	SetUnderlyingBody(body io.ReadCloser)
}

func NewCompressReader(body io.ReadCloser, contentEncoding string) CompressReader {
	switch contentEncoding {
	case "gzip":
		return NewGzipReader(body)
	case "deflate":
		return NewDeflateReader(body)
	case "br":
		return NewBrotliReader(body)
	case "zstd":
		return NewZstdReader(body)
	}
	return nil
}
