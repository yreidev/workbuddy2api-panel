// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package req

import (
	"io"
	"log"
	"os"
)

// Logger is the abstract logging interface, gives control to
// the Req users, choice of the logger.
type Logger interface {
	Errorf(format string, v ...any)
	Warnf(format string, v ...any)
	Debugf(format string, v ...any)
}

// NewLogger create a Logger wraps the *log.Logger
func NewLogger(output io.Writer, prefix string, flag int) Logger {
	return &logger{l: log.New(output, prefix, flag)}
}

func NewLoggerFromStandardLogger(l *log.Logger) Logger {
	return &logger{l: l}
}

func createDefaultLogger() Logger {
	return NewLogger(os.Stdout, "", log.Ldate|log.Lmicroseconds)
}

var _ Logger = (*logger)(nil)

type disableLogger struct{}

func (l *disableLogger) Errorf(format string, v ...any) {}
func (l *disableLogger) Warnf(format string, v ...any)  {}
func (l *disableLogger) Debugf(format string, v ...any) {}

type logger struct {
	l *log.Logger
}

func (l *logger) Errorf(format string, v ...any) {
	l.output("ERROR", format, v...)
}

func (l *logger) Warnf(format string, v ...any) {
	l.output("WARN", format, v...)
}

func (l *logger) Debugf(format string, v ...any) {
	l.output("DEBUG", format, v...)
}

func (l *logger) output(level, format string, v ...any) {
	format = level + " [req] " + format
	if len(v) == 0 {
		l.l.Print(format)
		return
	}
	l.l.Printf(format, v...)
}
