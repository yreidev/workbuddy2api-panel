// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"errors"
	"net/http"
	"testing"
)

// TestNewDialer 拨号器参数按常量集中定义（建连上限与 keepalive 探测周期）。
func TestNewDialer(t *testing.T) {
	d := newDialer()
	if d.Timeout != dialTimeout {
		t.Errorf("Timeout = %v，应为 %v", d.Timeout, dialTimeout)
	}
	if d.KeepAlive != dialKeepAlive {
		t.Errorf("KeepAlive = %v，应为 %v", d.KeepAlive, dialKeepAlive)
	}
}

// TestNewTransport 出站 Transport 构造参数：真正禁 h2（非 nil 空 TLSNextProto）、
// TLS/响应头超时、连接池容量与空闲回收。
func TestNewTransport(t *testing.T) {
	tr := newTransport(nil)
	if tr.DialContext == nil {
		t.Error("DialContext 未接线")
	}
	if tr.TLSNextProto == nil {
		t.Fatal("TLSNextProto 必须为非 nil 空映射（nil 会让标准库注入默认 h2）")
	}
	if len(tr.TLSNextProto) != 0 {
		t.Errorf("TLSNextProto 应为空映射，got %d 项", len(tr.TLSNextProto))
	}
	if tr.TLSHandshakeTimeout != tlsHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v，应为 %v", tr.TLSHandshakeTimeout, tlsHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != responseHeaderTimeout {
		t.Errorf("ResponseHeaderTimeout = %v，应为 %v", tr.ResponseHeaderTimeout, responseHeaderTimeout)
	}
	if tr.MaxIdleConns != maxIdleConns || tr.MaxIdleConnsPerHost != maxIdleConnsPerHost {
		t.Errorf("连接池 = %d/%d，应为 %d/%d", tr.MaxIdleConns, tr.MaxIdleConnsPerHost, maxIdleConns, maxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout != idleConnTimeout {
		t.Errorf("IdleConnTimeout = %v，应为 %v", tr.IdleConnTimeout, idleConnTimeout)
	}
	// 只断言构造参数，不发起任何拨号。
	tr.CloseIdleConnections()
}

// idleClosingRT 记录 CloseIdleConnections 调用的 RoundTripper。
type idleClosingRT struct{ closed int }

func (r *idleClosingRT) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

func (r *idleClosingRT) CloseIdleConnections() { r.closed++ }

// TestRoundTripCloseIdle 失败后清空闲连接池：nil 与不实现 closeIdler 的
// RoundTripper 静默跳过；实现的调用一次。
func TestRoundTripCloseIdle(t *testing.T) {
	roundTripCloseIdle(nil) // 不应 panic

	plain := rtFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	roundTripCloseIdle(plain) // 未实现 closeIdler：静默

	rt := &idleClosingRT{}
	roundTripCloseIdle(rt)
	if rt.closed != 1 {
		t.Errorf("CloseIdleConnections 调用 %d 次，应为 1", rt.closed)
	}
}
