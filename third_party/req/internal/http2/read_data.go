// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

import "io"

// Chromium 的 decoder 按收到的片段交付 DATA，并按实际收到的正文、padding
// 逐段记账。只有当前物理帧的全部 payload 收完，才交付 END_STREAM。
type chromiumDataRead struct {
	header  FrameHeader
	body    uint32
	padding uint32
	padByte bool
	active  bool
	err     error
}

func (h2f *Framer) startChromiumData(fh FrameHeader) (Frame, error) {
	if fh.StreamID == 0 {
		h2f.countError("frame_data_stream_0")
		return nil, h2f.connError(ErrCodeProtocol, "DATA frame with stream ID 0")
	}
	if err := h2f.checkFrameOrder(&DataFrame{FrameHeader: fh}); err != nil {
		return nil, err
	}
	h2f.dataRead = chromiumDataRead{header: fh, body: fh.Length, padByte: fh.Flags.Has(FlagDataPadded), active: true}
	return h2f.readChromiumDataFragment()
}

// 仅供启用 ChromiumUpload 的连接使用；普通 Framer 仍一帧返回一次。
// 分段帧的 Length 是本次实际消费量，首个 padding 字节另带 PADDED 标志，
// 使既有接收流控按相同的实际字节记账，而不是预扣尚未收到的整个 payload。
func (h2f *Framer) readChromiumDataFragment() (Frame, error) {
	r := &h2f.dataRead
	if r.err != nil {
		return nil, r.err
	}
	f := h2f.frameCache.getDataFrame()
	f.FrameHeader = r.header
	f.Flags &^= FlagDataEndStream | FlagDataPadded
	f.Length = 0
	f.data = nil
	if r.padByte {
		if r.body == 0 {
			h2f.countError("frame_data_pad_byte_short")
			return nil, errChromiumDataFlags
		}
		var pad [1]byte
		if _, err := io.ReadFull(h2f.r, pad[:]); err != nil {
			return nil, err
		}
		r.body--
		r.padByte = false
		if uint32(pad[0]) > r.body {
			h2f.countError("frame_data_pad_too_big")
			return nil, errChromiumPadding
		}
		r.padding = uint32(pad[0])
		r.body -= r.padding
		f.Flags |= FlagDataPadded
		f.Length = 1
	} else if r.body > 0 || r.padding > 0 {
		remaining := r.body
		padding := remaining == 0
		if padding {
			remaining = r.padding
		}
		p := h2f.getReadBuf(min(remaining, 8*1024))
		n, err := h2f.r.Read(p)
		if n == 0 && err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		f.Length = uint32(n)
		if padding {
			r.padding -= uint32(n)
		} else {
			r.body -= uint32(n)
			f.data = p[:n]
		}
		if r.body+r.padding > 0 && err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			r.err = err
		}
	}
	if r.body+r.padding == 0 {
		r.active = false
		f.Flags |= r.header.Flags & FlagDataEndStream
	}
	h2f.lastFrame = f
	if h2f.logReads {
		h2f.debugReadLoggerf("http2: Framer %p: read DATA fragment %v", h2f, summarizeFrame(f))
	}
	return f, nil
}
