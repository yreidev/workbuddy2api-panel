// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// address.go 国内版可信入口的地址层：HTTPDNS 候选 IP、备用 IP、失败/成功记忆，
// 以及钉 IP 的 Transport。
//
// 依据：官方桌面端 5.8.3 内置 CLI 的地址层（10-20 读安装包确认）：
//   - 只对三个可信入口的 host 生效；本机 DNS 的结果不进候选。
//   - 候选 = 该 host 的 HTTPDNS 结果（按记忆排序）+ 备用入口 wb.tencentbuddy.com 的
//     备用 IP（failoverFallbackIPs，去重追加）。
//   - 30 秒内失败过的 IP 跳过（全失败时照样全试），上次成功的 IP 放第一个。
//   - 钉 IP：URL、Host 头、TLS 的 SNI 与证书校验都还是原域名，只是 TCP 连到指定 IP。
//
// 可替换点：Client.HTTPDNSQuery 与 Client.PinIP 任一为 nil 时地址层整体不生效
// （候选为空），现有测试的零值 Client 行为不变。
package upstream

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// addressFailTTL 失败记忆的有效期：30 秒内失败过的 IP 排到后面。
	addressFailTTL = 30 * time.Second
	// addressPinMemoMax 钉 IP 备忘最多保留的请求数，满了删最早放进去的。
	addressPinMemoMax = 256
)

// addressPin 一次对话钉过的地址（钉 IP 备忘：同请求 ID 的下一次调用首发就钉它）。
type addressPin struct {
	host string
	ip   string
}

// addressState 地址层的记忆，每个 Client 一份、并发安全、零值可用。
type addressState struct {
	mu sync.Mutex
	// failures host → IP → 最近一次失败时刻。
	failures map[string]map[string]time.Time
	// lastOK host → 上次成功的 IP（不过期）。
	lastOK map[string]string
	// switching 正在换入口的 X-Conversation-Request-ID 集合（在途去重）。
	switching map[string]struct{}
	// pins 请求 ID → 钉过的地址，pinOrder 是插入顺序（满了删最早的）。
	pins     map[string]addressPin
	pinOrder []string
	// now 可替换时钟（测试推进 30 秒窗口用）；nil = time.Now。
	now func() time.Time
}

// clock 当前时间（测试可替换 now）。
func (s *addressState) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// failoverTrustedHost 判断 host 是否是三个可信入口之一（地址层只对它们生效）。
func failoverTrustedHost(host string) bool {
	switch host {
	case "www.workbuddy.cn", "copilot.tencent.com", "wb.tencentbuddy.com":
		return true
	}
	return false
}

// failoverHostName 取 host 部分（去掉可能的端口）。
func failoverHostName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// addrLayerOn 地址层是否整体生效：两个可替换点都装了才生效。
func (c *Client) addrLayerOn() bool {
	return c != nil && c.HTTPDNSQuery != nil && c.PinIP != nil
}

// addressCandidates 返回 base 的 host 的候选 IP（按规格排序）。地址层不生效、
// host 不可信、或查询结果为空且没有备用 IP 时返回 nil。
func (c *Client) addressCandidates(ctx context.Context, host string) []string {
	if !c.addrLayerOn() {
		return nil
	}
	host = failoverHostName(strings.TrimSpace(host))
	if !failoverTrustedHost(host) {
		return nil
	}
	raw := c.HTTPDNSQuery(ctx, host)
	out := c.addr.orderCandidates(host, raw)
	if host == "wb.tencentbuddy.com" {
		out = addressAppendUnique(out, failoverFallbackIPs...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// orderCandidates 按地址记忆排序：去掉非法与重复；上次成功的 IP 若在列表里且 30 秒
// 内没失败过，放第一个；其余去掉 30 秒内失败过的；排完为空就退回原列表（全都在
// 30 秒内失败过时照样全试）。
func (s *addressState) orderCandidates(host string, raw []string) []string {
	orig := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, ip := range raw {
		ip = strings.TrimSpace(ip)
		if ip == "" || net.ParseIP(ip) == nil || seen[ip] {
			continue
		}
		seen[ip] = true
		orig = append(orig, ip)
	}
	if len(orig) == 0 {
		return nil
	}

	s.mu.Lock()
	now := s.clock()
	lastOK := s.lastOK[host]
	// 复制失败记录：解锁后不能继续读内层 map（并发请求会写）。
	failed := make(map[string]time.Time, len(s.failures[host]))
	for ip, at := range s.failures[host] {
		failed[ip] = at
	}
	s.mu.Unlock()
	failedRecently := func(ip string) bool {
		at, ok := failed[ip]
		return ok && now.Sub(at) < addressFailTTL
	}

	out := make([]string, 0, len(orig))
	first := ""
	if lastOK != "" && seen[lastOK] && !failedRecently(lastOK) {
		first = lastOK
		out = append(out, first)
	}
	for _, ip := range orig {
		if ip == first || failedRecently(ip) {
			continue
		}
		out = append(out, ip)
	}
	if len(out) == 0 {
		return orig
	}
	return out
}

// addressAppendUnique 把 extra 追加到列表末尾（去掉已在列表里的）。
func addressAppendUnique(dst []string, extra ...string) []string {
	seen := make(map[string]bool, len(dst)+len(extra))
	for _, ip := range dst {
		seen[ip] = true
	}
	for _, ip := range extra {
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		dst = append(dst, ip)
	}
	return dst
}

// noteAddrFailure 记一次 IP 失败（按 host 记，30 秒内有效）。
func (c *Client) noteAddrFailure(host, ip string) {
	if host == "" || ip == "" {
		return
	}
	s := &c.addr
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures == nil {
		s.failures = make(map[string]map[string]time.Time)
	}
	byIP := s.failures[host]
	if byIP == nil {
		byIP = make(map[string]time.Time)
		s.failures[host] = byIP
	}
	byIP[ip] = s.clock()
}

// noteAddrSuccess 记一次 IP 成功：同时删掉这个 IP 的失败记录。
func (c *Client) noteAddrSuccess(host, ip string) {
	if host == "" || ip == "" {
		return
	}
	s := &c.addr
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastOK == nil {
		s.lastOK = make(map[string]string)
	}
	s.lastOK[host] = ip
	if byIP := s.failures[host]; byIP != nil {
		delete(byIP, ip)
	}
}

// switchInFlight 报告该请求 ID 是否正在换入口。
func (s *addressState) switchInFlight(rid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.switching[rid]
	return ok
}

// beginSwitch 登记「正在换入口」；同一个请求 ID 已在表里时返回 false。
func (s *addressState) beginSwitch(rid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.switching == nil {
		s.switching = make(map[string]struct{})
	}
	if _, ok := s.switching[rid]; ok {
		return false
	}
	s.switching[rid] = struct{}{}
	return true
}

// endSwitch 删掉「正在换入口」登记。
func (s *addressState) endSwitch(rid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.switching, rid)
}

// rememberPin 记请求 ID 钉过的地址（最多 256 条，满了删最早放进去的）。
func (s *addressState) rememberPin(rid, host, ip string) {
	if rid == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pins == nil {
		s.pins = make(map[string]addressPin)
	}
	if _, ok := s.pins[rid]; ok {
		s.pins[rid] = addressPin{host: host, ip: ip}
		return
	}
	for len(s.pinOrder) >= addressPinMemoMax {
		oldest := s.pinOrder[0]
		s.pinOrder = s.pinOrder[1:]
		delete(s.pins, oldest)
	}
	s.pins[rid] = addressPin{host: host, ip: ip}
	s.pinOrder = append(s.pinOrder, rid)
}

// pinFor 取请求 ID 钉过的地址。
func (s *addressState) pinFor(rid string) (addressPin, bool) {
	if rid == "" {
		return addressPin{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pins[rid]
	return p, ok
}

// addrSlot 一次请求在当前 host 上的地址槽：候选列表 + 当前下标。
// memo 表示这个槽来自钉 IP 备忘（只含一个 IP，失败后按新建槽处理）。
type addrSlot struct {
	host       string
	candidates []string
	idx        int
	memo       bool
}

// current 槽当前钉着的 IP。
func (s *addrSlot) current() string {
	if s == nil || s.idx < 0 || s.idx >= len(s.candidates) {
		return ""
	}
	return s.candidates[s.idx]
}

// nextAddrSlot 算「换 IP」后的下一个槽：没有槽就按当前 host 建槽（钉第 1 个）；
// 备忘槽先记失败再当新建槽；有槽有下一个就记当前 IP 失败、下标加一；没有下一个
// 返回 nil（不换）。
func (c *Client) nextAddrSlot(ctx context.Context, cur *addrSlot, host string) *addrSlot {
	if cur != nil && cur.memo {
		c.noteAddrFailure(host, cur.current())
		cur = nil
	}
	if cur == nil {
		candidates := c.addressCandidates(ctx, host)
		if len(candidates) == 0 {
			return nil
		}
		return &addrSlot{host: host, candidates: candidates}
	}
	if cur.idx+1 >= len(cur.candidates) {
		return nil
	}
	c.noteAddrFailure(host, cur.current())
	cur.idx++
	return cur
}

// pinTransport 从 base Clone 一个 Transport，把拨号换成「直连 ip、端口不变」，SNI 与证书校验仍按
// 请求的原域名（每个 IP 一个，由 route.go 的线路表缓存：URL、Host 头、SNI 都还是原域名，只把 TCP 拨到
// 指定 IP，所以不能用原 Transport 的连接池——池里的连接连的是别的 IP）。https 走 DialTLSContext（uTLS 伪装官方 ClientHello + 钉 IP，见 tlsmimic.go），
// 非 https 走 DialContext（同样钉 IP，兜底）。钉 IP 的连接照官方不走代理（见 proxy.go），所以不沿用
// base 的拨号。
func pinTransport(base *http.Transport, ip string) *http.Transport {
	if base == nil {
		base = &http.Transport{}
	}
	tr := base.Clone()
	dialer := newDialer()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
	}
	// https 的 TLS 握手走 uTLS，TCP 用上面钉 IP 的拨号；SNI 仍取请求域名，RootCAs 等从 base 的
	// TLSClientConfig 继承（dialTLSMimic 内部处理）。
	tr.DialTLSContext = dialTLSMimic(tr.DialContext, tr.TLSClientConfig)
	return tr
}
