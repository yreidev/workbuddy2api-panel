// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import (
	"encoding/binary"
	"io"

	"github.com/imroc/req/v3/http2"
)

// 物理 HEADERS／CONTINUATION 尚未收完时，已到达的 HPACK 片段可立即解码。
// 尾部 padding 仍必须消费，只有物理帧结束后才交付 END_HEADERS。
type chromiumHeadersRead struct {
	header    FrameHeader
	priority  http2.PriorityParam
	remaining uint32
	padding   uint32
	active    bool
	err       error
}

func (h2f *Framer) startChromiumHeaders(fh FrameHeader) (Frame, error) {
	r := chromiumHeadersRead{header: fh, remaining: fh.Length, active: true}
	if fh.Type == FrameHeaders {
		if fh.Flags.Has(FlagHeadersPadded) {
			var pad [1]byte
			if _, err := io.ReadFull(h2f.r, pad[:]); err != nil {
				return nil, err
			}
			r.padding = uint32(pad[0])
			r.remaining--
		}
		if fh.Flags.Has(FlagHeadersPriority) {
			var p [5]byte
			if _, err := io.ReadFull(h2f.r, p[:]); err != nil {
				return nil, err
			}
			r.priority = http2.PriorityParam{StreamDep: binary.BigEndian.Uint32(p[:4]) & 0x7fffffff, Exclusive: p[0]&0x80 != 0, Weight: p[4]}
			r.remaining -= 5
		}
		if r.padding > r.remaining {
			return nil, errChromiumPadding
		}
		r.remaining -= r.padding
		if err := h2f.checkFrameOrder(&HeadersFrame{FrameHeader: fh}); err != nil {
			return nil, err
		}
	} else if err := h2f.checkFrameOrder(&ContinuationFrame{FrameHeader: fh}); err != nil {
		return nil, err
	}
	h2f.headersRead = r
	return h2f.readChromiumHeaderFragment()
}

func (h2f *Framer) readChromiumHeaderFragment() (Frame, error) {
	r := &h2f.headersRead
	if r.err != nil {
		return nil, r.err
	}
	var fragment []byte
	if r.remaining > 0 || r.padding > 0 {
		remaining, padding := r.remaining, false
		if remaining == 0 {
			remaining, padding = r.padding, true
		}
		p := h2f.getReadBuf(min(remaining, 8*1024))
		n, err := h2f.r.Read(p)
		if n == 0 && err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if padding {
			r.padding -= uint32(n)
		} else {
			r.remaining -= uint32(n)
			fragment = p[:n]
		}
		if err != nil && r.remaining+r.padding > 0 {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			r.err = err
		}
	}
	fh := r.header
	if r.remaining+r.padding == 0 {
		r.active = false
	} else {
		fh.Flags &^= FlagHeadersEndHeaders
	}
	if fh.Type == FrameHeaders {
		return &HeadersFrame{FrameHeader: fh, Priority: r.priority, headerFragBuf: fragment}, nil
	}
	return &ContinuationFrame{FrameHeader: fh, headerFragBuf: fragment}, nil
}
