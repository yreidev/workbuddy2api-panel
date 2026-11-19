// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package req

import (
	"github.com/imroc/req/v3/internal/dump"
	"io"
	"os"
)

// DumpOptions controls the dump behavior.
type DumpOptions struct {
	Output               io.Writer
	RequestOutput        io.Writer
	ResponseOutput       io.Writer
	RequestHeaderOutput  io.Writer
	RequestBodyOutput    io.Writer
	ResponseHeaderOutput io.Writer
	ResponseBodyOutput   io.Writer
	RequestHeader        bool
	RequestBody          bool
	ResponseHeader       bool
	ResponseBody         bool
	Async                bool
}

// Clone return a copy of DumpOptions
func (do *DumpOptions) Clone() *DumpOptions {
	if do == nil {
		return nil
	}
	d := *do
	return &d
}

type dumpOptions struct {
	*DumpOptions
}

func (o dumpOptions) Output() io.Writer {
	if o.DumpOptions.Output == nil {
		return os.Stdout
	}
	return o.DumpOptions.Output
}

func (o dumpOptions) RequestHeaderOutput() io.Writer {
	if o.DumpOptions.RequestHeaderOutput != nil {
		return o.DumpOptions.RequestHeaderOutput
	}
	if o.DumpOptions.RequestOutput != nil {
		return o.DumpOptions.RequestOutput
	}
	return o.Output()
}

func (o dumpOptions) RequestBodyOutput() io.Writer {
	if o.DumpOptions.RequestBodyOutput != nil {
		return o.DumpOptions.RequestBodyOutput
	}
	if o.DumpOptions.RequestOutput != nil {
		return o.DumpOptions.RequestOutput
	}
	return o.Output()
}

func (o dumpOptions) ResponseHeaderOutput() io.Writer {
	if o.DumpOptions.ResponseHeaderOutput != nil {
		return o.DumpOptions.ResponseHeaderOutput
	}
	if o.DumpOptions.ResponseOutput != nil {
		return o.DumpOptions.ResponseOutput
	}
	return o.Output()
}

func (o dumpOptions) ResponseBodyOutput() io.Writer {
	if o.DumpOptions.ResponseBodyOutput != nil {
		return o.DumpOptions.ResponseBodyOutput
	}
	if o.DumpOptions.ResponseOutput != nil {
		return o.DumpOptions.ResponseOutput
	}
	return o.Output()
}

func (o dumpOptions) RequestHeader() bool {
	return o.DumpOptions.RequestHeader
}

func (o dumpOptions) RequestBody() bool {
	return o.DumpOptions.RequestBody
}

func (o dumpOptions) ResponseHeader() bool {
	return o.DumpOptions.ResponseHeader
}

func (o dumpOptions) ResponseBody() bool {
	return o.DumpOptions.ResponseBody
}

func (o dumpOptions) Async() bool {
	return o.DumpOptions.Async
}

func (o dumpOptions) Clone() dump.Options {
	return dumpOptions{o.DumpOptions.Clone()}
}

func newDefaultDumpOptions() *DumpOptions {
	return &DumpOptions{
		Output:         os.Stdout,
		RequestBody:    true,
		ResponseBody:   true,
		ResponseHeader: true,
		RequestHeader:  true,
	}
}

func newDumper(opt *DumpOptions) *dump.Dumper {
	if opt == nil {
		opt = newDefaultDumpOptions()
	}
	if opt.Output == nil {
		opt.Output = os.Stderr
	}
	return dump.NewDumper(dumpOptions{opt})
}
