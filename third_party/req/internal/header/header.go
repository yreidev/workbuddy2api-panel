// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package header

import "strings"

const (
	DefaultUserAgent     = "req/v3 (https://github.com/imroc/req)"
	UserAgent            = "User-Agent"
	Location             = "Location"
	ContentType          = "Content-Type"
	PlainTextContentType = "text/plain; charset=utf-8"
	JsonContentType      = "application/json; charset=utf-8"
	XmlContentType       = "text/xml; charset=utf-8"
	FormContentType      = "application/x-www-form-urlencoded"
	WwwAuthenticate      = "WWW-Authenticate"
	Authorization        = "Authorization"
	HeaderOderKey        = "__header_order__"
	PseudoHeaderOderKey  = "__pseudo_header_order__"
)

var reqWriteExcludeHeader = map[string]bool{
	// Host is :authority, already sent.
	// Content-Length is automatic.
	"host":           true,
	"content-length": true,
	// Per 8.1.2.2 Connection-Specific Header
	// Fields, don't send connection-specific
	// fields. We have already checked if any
	// are error-worthy so just ignore the rest.
	"connection":        true,
	"proxy-connection":  true,
	"transfer-encoding": true,
	"upgrade":           true,
	"keep-alive":        true,
	// Ignore header order keys which is only used internally.
	HeaderOderKey:       true,
	PseudoHeaderOderKey: true,
}

func IsExcluded(key string) bool {
	if reqWriteExcludeHeader[strings.ToLower(key)] {
		return true
	}
	return false
}
