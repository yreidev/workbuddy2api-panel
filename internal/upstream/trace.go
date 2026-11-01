// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// trace.go 官方后台服务登录、刷新令牌请求的链路头。
//
// 依据（10-20 读官方桌面端 5.8.3 / 5.8.4 安装包；10-31 与国内版 5.9.3、国际版 5.9.4 按函数比对，
// 没变）：这几个安装包 product.json 的 telemetry.tracing.enabled 都是 true，登录（发起授权、
// 轮询令牌、取账号）与刷新令牌都把当前 span 的 requestHeaders 展开进请求头
// （OpenTelemetrySpan.requestHeaders）：
//
//	X-Trace-ID: <traceId>
//	b3: <traceId>-<spanId>-<traceFlags>-<父 spanId>
//	X-B3-TraceId、X-B3-ParentSpanId、X-B3-SpanId、X-B3-Sampled
//
// 登录和刷新用的都是 traceService.startTrace 新开的根 span，没有父 span，所以 b3 以空的父
// span 结尾（…-1-），X-B3-ParentSpanId 是空值。从桌面端登录界面发起的登录另开「登录观测」
// （setLoginStepCallback 设上 loginSessionId），登录的每个请求再带一个子 span 的 W3C
// traceparent（loginTraceCarrier；tracestate 为空不发），国内版授权页地址也带上
// loginSessionId、traceparent、login_owner_id。网关的面板添加账号、命令行登录相当于从登录
// 界面发起，按开着登录观测处理。
package upstream

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/yreidev/workbuddy2api-panel/internal/session"
)

// daemonTrace 一个根 span：traceID 32 位小写 hex，spanID 16 位小写 hex。
type daemonTrace struct {
	traceID string
	spanID  string
}

// newDaemonTrace 新开一条 trace（刷新令牌每次一条，刷新和刷新后取账号共用）。
func newDaemonTrace() daemonTrace {
	return daemonTrace{traceID: session.NewMessageID(), spanID: session.NewMessageID()[:16]}
}

// setRoot 设根 span 的链路头（OpenTelemetrySpan.requestHeaders，traceFlags 为 1）。
func (t daemonTrace) setRoot(req *http.Request) {
	req.Header.Set("X-Trace-ID", t.traceID)
	req.Header.Set("b3", t.traceID+"-"+t.spanID+"-1-")
	req.Header.Set("X-B3-TraceId", t.traceID)
	req.Header.Set("X-B3-ParentSpanId", "")
	req.Header.Set("X-B3-SpanId", t.spanID)
	req.Header.Set("X-B3-Sampled", "1")
}

// traceparent 同一条 trace 下某个子 span 的 W3C traceparent。
func (t daemonTrace) traceparent(childSpan string) string {
	return "00-" + t.traceID + "-" + childSpan + "-01"
}

// LoginTrace 一次登录的链路。官方桌面端登录界面每次登录生成一个 loginSessionId（UUID）和一个
// journeyOwnerId（UUID，授权页的 login_owner_id），登录统计的 traceId 就是 loginSessionId
// 去掉连字符（toHexTraceId）。发起授权、轮询令牌、取账号共用这一条：调用方把 LoginAuthState
// 返回的 Trace 原样传给 LoginPollToken、LoginFetchAccount（面板存在登录会话里，命令行登录
// 存进 state 文件）。零值（旧 state 文件）在用到时现生成一条。
type LoginTrace struct {
	SessionID string `json:"sessionId"`
	OwnerID   string `json:"ownerId"`
	SpanID    string `json:"spanId"`
	// Proxy 这次登录走的代理（代理池里的 ID，见 route.go）：发起授权时定下，轮询令牌、取账号都走它；
	// 空为默认线路。
	Proxy string `json:"proxy,omitempty"`
}

// NewLoginTrace 生成一次登录的链路。
func NewLoginTrace() LoginTrace {
	return LoginTrace{SessionID: newUUID(), OwnerID: newUUID(), SpanID: session.NewMessageID()[:16]}
}

// orNew 零值（或不完整）的链路现生成一条。
func (t LoginTrace) orNew() LoginTrace {
	if t.SessionID == "" || len(t.SpanID) != 16 {
		n := NewLoginTrace()
		n.Proxy = t.Proxy
		return n
	}
	return t
}

// daemon 换成根 span：traceId 取 loginSessionId 去掉连字符（不是 32 位 hex 时取它的哈希）。
func (t LoginTrace) daemon() daemonTrace {
	id := strings.ToLower(strings.ReplaceAll(t.SessionID, "-", ""))
	if !isHex(id, 32) {
		sum := sha256.Sum256([]byte(t.SessionID))
		id = hex.EncodeToString(sum[:16])
	}
	return daemonTrace{traceID: id, spanID: t.SpanID}
}

// childSpan 某类请求的子 span id。官方每类请求（发起授权、轮询令牌、取账号、取账号列表、打开
// 授权页）开一个子 span，同一类请求的多次轮询共用它；网关的轮询分在几次调用里，按登录会话
// 和种类派生，保证同一类不变。
func (t LoginTrace) childSpan(kind string) string {
	sum := sha256.Sum256([]byte(t.SessionID + "\x00" + kind))
	return hex.EncodeToString(sum[:8])
}

// isHex 判断 s 是否为 n 位十六进制。
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// newUUID 随机 UUID v4（crypto.randomUUID 同款格式）。
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
