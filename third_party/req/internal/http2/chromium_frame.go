// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package http2

// 对照官方 Quiche decoder 的分类及三套运行时的 GOAWAY 原始帧。
// 这些检查只用于 ChromiumUpload；普通 Framer 保留原库的错误语义。
var (
	errChromiumStreamID       = uploadProtocolError{ConnectionError(ErrCodeProtocol), "Framer error: 1 (INVALID_STREAM_ID)."}
	errChromiumControlFrame   = uploadProtocolError{ConnectionError(ErrCodeProtocol), "Framer error: 2 (INVALID_CONTROL_FRAME)."}
	errChromiumControlPayload = uploadProtocolError{ConnectionError(ErrCodeFrameSize), "Framer error: 3 (CONTROL_PAYLOAD_TOO_LARGE)."}
	errChromiumPadding        = uploadProtocolError{ConnectionError(ErrCodeProtocol), "Framer error: 5 (INVALID_PADDING)."}
	errChromiumDataFlags      = uploadProtocolError{ConnectionError(ErrCodeProtocol), "Framer error: 6 (INVALID_DATA_FRAME_FLAGS)."}
	errChromiumFrameOrder     = uploadProtocolError{ConnectionError(ErrCodeProtocol), "Framer error: 7 (UNEXPECTED_FRAME)."}
	errChromiumControlSize    = uploadProtocolError{ConnectionError(ErrCodeFrameSize), "Framer error: 9 (INVALID_CONTROL_FRAME_SIZE)."}
	errChromiumDataPayload    = uploadProtocolError{ConnectionError(ErrCodeFrameSize), "Framer error: 10 (OVERSIZED_PAYLOAD)."}
)

func (h2f *Framer) checkChromiumFrameHeader(fh FrameHeader) error {
	if !h2f.AllowIllegalReads {
		if h2f.lastHeaderStream != 0 && fh.Type != FrameContinuation {
			return errChromiumFrameOrder
		}
	}
	switch fh.Type {
	case FrameData, FrameHeaders, FramePriority, FrameRSTStream, FramePushPromise, FrameContinuation:
		if fh.StreamID == 0 {
			return errChromiumStreamID
		}
	case FrameSettings, FramePing, FrameGoAway:
		if fh.StreamID != 0 {
			return errChromiumStreamID
		}
	}
	if !h2f.AllowIllegalReads {
		if h2f.lastHeaderStream != 0 && fh.StreamID != h2f.lastHeaderStream || h2f.lastHeaderStream == 0 && fh.Type == FrameContinuation {
			return errChromiumFrameOrder
		}
	}
	if fh.Type == FrameData && fh.Flags & ^(FlagDataEndStream|FlagDataPadded) != 0 {
		return errChromiumDataFlags
	}
	if fh.Length > h2f.maxReadSize {
		if fh.Type == FrameData {
			return errChromiumDataPayload
		}
		return errChromiumControlPayload
	}
	switch fh.Type {
	case FrameSettings:
		if fh.Length%6 != 0 || fh.Flags.Has(FlagSettingsAck) && fh.Length != 0 {
			return errChromiumControlSize
		}
	case FramePing:
		if fh.Length != 8 {
			return errChromiumControlSize
		}
	case FrameRSTStream, FrameWindowUpdate:
		if fh.Length != 4 {
			return errChromiumControlSize
		}
	case FramePriority:
		if fh.Length != 5 {
			return errChromiumControlSize
		}
	case FrameGoAway:
		if fh.Length < 8 {
			return errChromiumControlFrame
		}
	case FrameData:
		if fh.Flags.Has(FlagDataPadded) && fh.Length == 0 {
			return errChromiumDataFlags
		}
	case FrameHeaders:
		if fh.Flags.Has(FlagHeadersPadded) && fh.Length == 0 {
			return errChromiumPadding
		}
		minimum := uint32(0)
		if fh.Flags.Has(FlagHeadersPadded) {
			minimum++
		}
		if fh.Flags.Has(FlagHeadersPriority) {
			minimum += 5
		}
		if fh.Length < minimum {
			return errChromiumControlSize
		}
	}
	return nil
}
