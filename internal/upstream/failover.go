// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// failover.go 国内版对话请求的失败切换：入口（NetworkFailover）与地址（HTTPDNS /
// 备用 IP），并按入口分别记熔断器。
//
// 依据：官方桌面端 5.8.3 内置 CLI 的 NetworkFailover 与地址层（产品配置
// productFeaturesConfig.NetworkFailover：mode=active，可信入口三个、熔断参数），
// 10-20 读安装包确认。规则：
//   - 可信入口：主入口 https://www.workbuddy.cn、备选入口 https://copilot.tencent.com
//     （这两个叫「前门」）、备用入口 https://wb.tencentbuddy.com。
//   - 前门出错切备用入口；备用入口自己出错不再切。每个请求最多切一次。
//   - 失败后先试哪个：DNS 解析失败先换 IP（钉 HTTPDNS 的候选地址），再换入口；
//     408/502/503 和没有 HTTP 状态的传输错误（含响应头超时、读超时、建连超时）
//     先换入口，换不了再换 IP（504 不换 IP）；2xx 空响应只换入口；响应体
//     retry_policy.retry=false 时两样都不做。
//   - 熔断器按入口记：60 秒窗口内基础设施类失败 ≥3 次就打开 15 秒，打开期间
//     再失败按 30/60/120 秒退避延长；一次成功全部清零。
//   - 只对国内版 SaaS 生效：国际版（c.globalOn）和私有化部署（chatBase 不是
//     前门，比如用户配了自己的中转）整套逻辑不生效。
package upstream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
)

const (
	// failoverAltOrigin 国内版备选入口（官方 product.json 的备用前门）。
	failoverAltOrigin = "https://copilot.tencent.com"
	// failoverBackupOrigin 国内版备用入口：前门出错时切到这里。
	failoverBackupOrigin = "https://wb.tencentbuddy.com"

	// failoverWindow 熔断统计窗口：窗口内的基础设施类失败次数达到阈值就打开。
	failoverWindow = 60 * time.Second
	// failoverThreshold 窗口内基础设施类失败达到该次数就打开熔断器。
	failoverThreshold = 3
	// failoverOpenFor 首次打开的时长。
	failoverOpenFor = 15 * time.Second
)

// failoverBackoffSteps 打开期间再次失败时，打开期依次延长到的时长
// （退避次数超过档数后一直停在最后一档 120 秒）。
var failoverBackoffSteps = [...]time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second}

// failoverState 按入口（origin）分别记的熔断器状态。零值可用（按需建 map），
// 并发安全；每个 Client 一份。
type failoverState struct {
	mu       sync.Mutex
	breakers map[string]*failoverBreaker
	// now 可替换时钟；nil = time.Now。测试推进时间用，不依赖真实等待。
	now func() time.Time
}

// failoverBreaker 单个入口的熔断状态。
type failoverBreaker struct {
	failures  []time.Time // 窗口内的基础设施类失败时刻（升序）
	openUntil time.Time   // 打开到该时刻；零值 = 从未打开
	backoff   int         // 打开期间再失败的退避档计数，一次成功才清零
}

// clock 当前时间（测试可替换 now 推进时间）。
func (s *failoverState) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// breakerLocked 取入口的熔断器，没有就新建（调用方持 s.mu）。
func (s *failoverState) breakerLocked(entry string) *failoverBreaker {
	if s.breakers == nil {
		s.breakers = make(map[string]*failoverBreaker)
	}
	b := s.breakers[entry]
	if b == nil {
		b = &failoverBreaker{}
		s.breakers[entry] = b
	}
	return b
}

// isOpen 报告入口的熔断器当前是否打开。
func (s *failoverState) isOpen(entry string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.breakers[entry]
	return b != nil && s.clock().Before(b.openUntil)
}

// noteSuccess 该入口一次成功：失败记录、退避次数、打开状态全部清零。
func (s *failoverState) noteSuccess(entry string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.breakers, entry)
}

// noteInfraFailure 记一次基础设施类失败并维护熔断器：
//   - 先丢掉 60 秒以前的失败记录；
//   - 当前仍在打开期内：打开期改成「现在 + 退避[min(退避次数, 2)]」，退避次数加一；
//   - 否则窗口内（含本次）达到 3 次就打开 15 秒。
func (s *failoverState) noteInfraFailure(entry string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	b := s.breakerLocked(entry)
	cutoff := now.Add(-failoverWindow)
	kept := b.failures[:0]
	for _, at := range b.failures {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	b.failures = append(kept, now)
	if now.Before(b.openUntil) {
		idx := b.backoff
		if idx >= len(failoverBackoffSteps) {
			idx = len(failoverBackoffSteps) - 1
		}
		b.openUntil = now.Add(failoverBackoffSteps[idx])
		b.backoff++
		return
	}
	if len(b.failures) >= failoverThreshold {
		b.openUntil = now.Add(failoverOpenFor)
	}
}

// failoverPlan 一次对话请求的入口选择结果。
type failoverPlan struct {
	active      bool   // 生效条件：国内版 + chatBase 是可信前门
	firstBase   string // 首选入口的 base（沿用 chatBase，或前门打开时直接用备用入口）
	firstOrigin string // 首选入口的 origin（熔断器键）
	canSwitch   bool   // 首选失败后能否切备用入口
}

// chatFailoverPlan 按熔断器状态选本次请求的首选入口：
//   - 不满足生效条件：chatBase 原样，不能切；
//   - 前门关着：发前门，能切；
//   - 前门打开、备用入口关着：直接发备用入口，不能切；
//   - 前门和备用入口都打开：仍先发前门（官方行为），能切；
//   - chatBase 直接配成备用入口：发备用入口，不能切，失败时只换 IP（官方对备用入口
//     也只做地址层）。
func (c *Client) chatFailoverPlan(a *auth.Auth, base string) failoverPlan {
	origin := failoverOrigin(base)
	plan := failoverPlan{firstBase: base, firstOrigin: origin}
	if c.globalOn(a) {
		return plan
	}
	if origin == failoverBackupOrigin {
		plan.active = true
		return plan
	}
	if origin != cnProductEndpoint && origin != failoverAltOrigin {
		return plan
	}
	plan.active = true
	plan.canSwitch = true
	if c.failover.isOpen(origin) {
		if c.failover.isOpen(failoverBackupOrigin) {
			return plan
		}
		plan.firstBase = failoverBackupOrigin
		plan.firstOrigin = failoverBackupOrigin
		plan.canSwitch = false
	}
	return plan
}

// failoverOrigin 取 base 的 origin（scheme+host，去掉末尾 / 与路径），
// 与三个可信入口做精确比较。解析失败返回空串（不可信）。
func failoverOrigin(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// failoverSwapOrigin 切入口时只换 base 的 scheme+host，路径/查询原样保留。
// base 或 origin 解析失败时直接返回传入的 origin（调用方传的是备用入口，
// 属兜底，正常不会走到）。
func failoverSwapOrigin(base, origin string) string {
	u, err := url.Parse(base)
	o, oerr := url.Parse(origin)
	if err != nil || oerr != nil || o.Scheme == "" || o.Host == "" {
		return origin
	}
	u.Scheme = o.Scheme
	u.Host = o.Host
	return u.String()
}

// failoverHost 取 base 的 host（日志用）；解析失败原样返回。
func failoverHost(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		return base
	}
	return u.Host
}

// failoverIsInfraStatus 报告 HTTP 状态是否属于基础设施类失败（熔断器计数口径）。
func failoverIsInfraStatus(status int) bool {
	switch status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// failoverIsInfraError 报告传输错误是否属于基础设施类失败（熔断器计数口径）：
// 连接被重置（ECONNRESET）、连接中止（ECONNABORTED）、连接被拒（ECONNREFUSED）、
// 超时（ETIMEDOUT 或 net.Error.Timeout 为真）、DNS 解析失败（*net.DNSError）。
// 调用方取消、EOF、TLS 证书类错误、号的线路问题（连不上号的代理，见 route.go）不算——它们不是入口
// 不可用的证据。
func failoverIsInfraError(err error) bool {
	if err == nil || IsRouteError(err) {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	for _, target := range [...]error{syscall.ECONNRESET, syscall.ECONNABORTED, syscall.ECONNREFUSED, syscall.ETIMEDOUT} {
		if errors.Is(err, target) {
			return true
		}
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// failoverIsFront 报告 base 是否是前门（主入口或备选入口）。
func failoverIsFront(base string) bool {
	o := failoverOrigin(base)
	return o == cnProductEndpoint || o == failoverAltOrigin
}

// failoverRetryIPStatus 报告 ≥400 的响应是否换 IP：只认 408/502/503（不含 504）。
func failoverRetryIPStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable:
		return true
	}
	return false
}

// failoverStatusRetryDenied 报告响应体是否明确要求「不重试」（retry_policy.retry=false），
// 只对 408/502/503/504 有意义。
func failoverStatusRetryDenied(status int, body []byte) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return failoverRetryDenied(body)
	}
	return false
}

// failoverRetryStatus 报告 ≥400 的响应是否改发备用入口：只认 408/502/503/504，
// 且响应体没有明确要求「不重试」（retry_policy.retry=false）。
func failoverRetryStatus(status int, body []byte) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return !failoverRetryDenied(body)
	}
	return false
}

// failoverRetryDenied 读响应体里的 retry_policy.retry：依次看顶层、data、error、
// error.data、error.error、error.error.data，取第一个带 retry_policy 键的对象，
// 该值是对象且 retry 为 false 时返回 true（不切）。响应体不是 JSON 时当作没有。
func failoverRetryDenied(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return false
	}
	for _, path := range [][]string{
		nil,
		{"data"},
		{"error"},
		{"error", "data"},
		{"error", "error"},
		{"error", "error", "data"},
	} {
		node := root
		for _, key := range path {
			next, ok := node[key].(map[string]any)
			if !ok {
				node = nil
				break
			}
			node = next
		}
		if node == nil {
			continue
		}
		raw, ok := node["retry_policy"]
		if !ok {
			continue
		}
		policy, ok := raw.(map[string]any)
		if !ok {
			return false // 第一个带该键的值不是对象：按官方规则不拦
		}
		retry, ok := policy["retry"].(bool)
		return ok && !retry
	}
	return false
}

// peekedBody 把 Peek 过首字节的 bufio.Reader 与底层流绑在一起交给调用方：
// Read 走缓冲（第一个字节不丢），Close 关底层流（monitorBody 的 Close 会 cancel）。
type peekedBody struct {
	*bufio.Reader
	io.Closer
}

// errChatSwitchInFlight 同一 X-Conversation-Request-ID 的另一个请求正在换入口。
// 在途去重：要换入口时发现同 ID 已在表里，或新调用开始时同 ID 正在换入口，都返回它。
var errChatSwitchInFlight = errors.New("同一请求正在切换入口")

// chatStreamWithFailover 国内版对话的失败切换循环（规格 §3）：每次尝试用同一份请求头、
// 同一份请求体，只换 scheme+host 或钉的 IP。失败后按 DNS / 状态码决定先换 IP 还是
// 先换入口；每个请求最多换一次入口。
func (c *Client) chatStreamWithFailover(ctx context.Context, a *auth.Auth, plan failoverPlan, header http.Header, prepared []byte) (io.ReadCloser, int, []byte, error) {
	rid := header.Get("X-Conversation-Request-ID")
	// 同请求 ID 正在换入口：第一次尝试前就直接返回（在途去重）。
	if rid != "" && c.addr.switchInFlight(rid) {
		return nil, 0, nil, errChatSwitchInFlight
	}

	curBase := plan.firstBase
	// 熔断器打开时直接发备用入口，等价于「已经换过入口」，之后仍可换 IP。
	originReplayed := !plan.canSwitch
	var slot *addrSlot
	if rid != "" && c.addrLayerOn() {
		// 钉 IP 备忘：同请求 ID 上一次调用钉过同一个 host 的 IP，这次首发就钉它。
		if m, ok := c.addr.pinFor(rid); ok && m.host == failoverHostName(failoverHost(curBase)) {
			slot = &addrSlot{host: m.host, candidates: []string{m.ip}, memo: true}
		}
	}
	registered := false
	if rid != "" {
		defer func() {
			if registered {
				c.addr.endSwitch(rid)
			}
		}()
	}

	for {
		pinned := slot.current()
		var rt http.RoundTripper
		if pinned != "" {
			rt = c.PinIP(pinned)
			if rid != "" {
				c.addr.rememberPin(rid, slot.host, pinned)
			}
		}
		res := c.chatStreamOnce(ctx, a, curBase, header, prepared, rt)
		entry := failoverOrigin(curBase)

		// 成功（<400）：读到第一个字节才算成功；2xx 空响应按「强制换入口」处理。
		if res.err == nil && res.status < 400 {
			canSwapOrigin := !originReplayed && failoverIsFront(curBase)
			if !canSwapOrigin && pinned == "" {
				c.failover.noteSuccess(entry)
				return res.rc, res.status, res.body, nil
			}
			br := bufio.NewReader(res.rc)
			if _, perr := br.Peek(1); perr == nil {
				c.failover.noteSuccess(entry)
				if pinned != "" {
					c.noteAddrSuccess(slot.host, pinned)
				}
				return &peekedBody{Reader: br, Closer: res.rc}, res.status, nil, nil
			} else if !errors.Is(perr, io.EOF) {
				// 读第一个字节出错（不是 EOF）：不切，把错误留给调用方去读。
				return &peekedBody{Reader: br, Closer: res.rc}, res.status, nil, nil
			}
			// 2xx 空响应：读完、关掉、cancel 本次 ctx，强制换入口。
			if !canSwapOrigin {
				// 换不了入口：保持改造前行为，把空流交给调用方。
				c.failover.noteSuccess(entry)
				return &peekedBody{Reader: br, Closer: res.rc}, res.status, nil, nil
			}
			res.rc.Close()
			res.cancel()
			var serr error
			slot, curBase, registered, serr = c.chatSwapOrigin(a, rid, curBase, slot, pinned, "响应体为空", registered)
			if serr != nil {
				return nil, 0, nil, serr
			}
			originReplayed = true
			continue
		}

		// 调用方取消、号的线路问题（route.go，不是入口的问题）：不再试、不记熔断，原样返回这次的结果。
		if ctx.Err() != nil || IsRouteError(res.err) {
			return res.rc, res.status, res.body, res.err
		}

		// 熔断器：每次尝试的结果照对话口径记到这次尝试的入口上。
		if res.status != 0 {
			if failoverIsInfraStatus(res.status) {
				c.failover.noteInfraFailure(entry)
			}
		} else if failoverIsInfraError(res.err) {
			c.failover.noteInfraFailure(entry)
		}

		// 响应体明确要求不重试：两样都不做。
		if failoverStatusRetryDenied(res.status, res.body) {
			return res.rc, res.status, res.body, res.err
		}

		// DNS 解析失败（只会出现在没钉 IP 的尝试上）：先换 IP，再换入口。
		var dnsErr *net.DNSError
		if pinned == "" && errors.As(res.err, &dnsErr) {
			if next := c.nextAddrSlot(ctx, slot, failoverHostName(failoverHost(curBase))); next != nil {
				slot = next
				c.logChatSwitchIP(a, slot)
				continue
			}
			if !originReplayed && failoverIsFront(curBase) {
				var serr error
				slot, curBase, registered, serr = c.chatSwapOrigin(a, rid, curBase, slot, pinned, truncate(res.err.Error(), 200), registered)
				if serr != nil {
					return nil, 0, nil, serr
				}
				originReplayed = true
				continue
			}
			return res.rc, res.status, res.body, res.err
		}

		// 其他失败：先换入口，换不了再换 IP。
		if !originReplayed && failoverIsFront(curBase) && failoverOriginRetryable(res) {
			var serr error
			slot, curBase, registered, serr = c.chatSwapOrigin(a, rid, curBase, slot, pinned, chatFailReason(res), registered)
			if serr != nil {
				return nil, 0, nil, serr
			}
			originReplayed = true
			continue
		}
		if failoverIPRetryable(res) {
			if next := c.nextAddrSlot(ctx, slot, failoverHostName(failoverHost(curBase))); next != nil {
				slot = next
				c.logChatSwitchIP(a, slot)
				continue
			}
		}
		return res.rc, res.status, res.body, res.err
	}
}

// chatSwapOrigin 换入口：当前钉着 IP 就记这个 IP 失败并丢掉槽；登记「正在换入口」
// 在途表（同 ID 已在表里时返回 errChatSwitchInFlight）。返回新的槽、base 和登记状态。
func (c *Client) chatSwapOrigin(a *auth.Auth, rid, curBase string, slot *addrSlot, pinned, reason string, registered bool) (*addrSlot, string, bool, error) {
	if rid != "" {
		if !c.addr.beginSwitch(rid) {
			return slot, curBase, registered, errChatSwitchInFlight
		}
		registered = true
	}
	if slot != nil && pinned != "" {
		c.noteAddrFailure(slot.host, pinned)
	}
	backupBase := failoverSwapOrigin(curBase, failoverBackupOrigin)
	log.Printf("WARN: [upstream] chat_stream acct=%s: %s 失败（%s），改发备用入口 %s",
		logfmt.Label(a.UID, a.Nickname), failoverHost(curBase), reason, failoverHost(backupBase))
	return nil, backupBase, registered, nil
}

// chatFailReason 换入口日志里的失败原因。
func chatFailReason(res chatOnceResult) string {
	if res.status != 0 {
		return fmt.Sprintf("HTTP %d", res.status)
	}
	if res.err != nil {
		return truncate(res.err.Error(), 200)
	}
	return "响应体为空"
}

// failoverOriginRetryable 报告这次失败能否换入口：HTTP 408/502/503/504，或没有 HTTP
// 状态的传输错误（含响应头超时、读超时、建连超时）。retry_policy 拦截在调用方处理。
func failoverOriginRetryable(res chatOnceResult) bool {
	if res.status == 0 {
		return res.err != nil
	}
	switch res.status {
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// failoverIPRetryable 报告这次失败能否换 IP：HTTP 408/502/503，或没有 HTTP 状态的
// 传输错误。504 与 2xx 空响应不换 IP。
func failoverIPRetryable(res chatOnceResult) bool {
	if res.status == 0 {
		return res.err != nil
	}
	return failoverRetryIPStatus(res.status)
}

// logChatSwitchIP 换 IP 的 WARN 日志：写 host、第几个/共几个，不打印 IP 本身
// （日志会被别人看到）。
func (c *Client) logChatSwitchIP(a *auth.Auth, slot *addrSlot) {
	log.Printf("WARN: [upstream] chat_stream acct=%s: %s 改钉第 %d/%d 个地址",
		logfmt.Label(a.UID, a.Nickname), slot.host, slot.idx+1, len(slot.candidates))
}

// 编译期确认 peekedBody 满足 io.ReadCloser（bufio.Reader 提供 Read，
// io.Closer 提供 Close）。
var _ io.ReadCloser = (*peekedBody)(nil)
