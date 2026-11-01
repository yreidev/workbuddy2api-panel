// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package server

// search.go 联网搜索。上游的搜索接口见 upstream/search.go，对外三个入口：
//   - Claude Code 的 WebSearch 工具：它另发一个 /v1/messages 子请求，声明 Anthropic 的服务端工具
//     web_search_20250305，唯一的用户消息是 "Perform a web search for the query: <搜索词>"，由 Anthropic 的
//     模型调这个工具搜完再写摘要。上游的模型没有这个工具，网关认出这种子请求就直接搜，按 Anthropic
//     服务端搜索的形状回（search_messages.go）。
//   - /v1/search/messages：同样的 Anthropic Messages 形状，照上游 linguo2625469 保留，给直接调搜索的客户端。
//   - /mcp：MCP 工具 web_search，给 Codex 这类能接 MCP 服务器的客户端（mcp.go）。
//
// 三处都走 runSearch：选号、在途名额、刷新令牌、换号和对话同一套，只是选号不看模型。只有说明号本身
// 有状况的错误（余额、限流、登录失效、封号、WAF）才和对话一样罚号，其余只换号，免得搜索接口自己的毛病
// 连累对话。搜索不进用量统计（没有 token），请求记录照记，模型一列写 web_search。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/apikeys"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// searchModel 请求记录和流水行里模型一列的写法；国际版的号搜的写 global:web_search。
const searchModel = "web_search"

// searchFailure 搜索最后没成功：回给客户端的状态码、错误码、说明和给运维看的提示。
type searchFailure struct {
	status int
	code   string
	msg    string
	hint   string
}

// message 说明加上提示，给只有一个说明字段的协议（Anthropic 的错误、MCP 的工具结果）。
func (f *searchFailure) message() string {
	if f.hint == "" {
		return f.msg
	}
	return f.msg + " (" + f.hint + ")"
}

// newSearchStat 搜索请求的统计，挂到请求记录上；模型一列选到号后按号的域改。
func newSearchStat(tr *requestTrace, stream bool) *chatStat {
	mode := "sync"
	if stream {
		mode = "stream"
	}
	st := &chatStat{start: time.Now(), model: searchModel, mode: mode, toks: -1}
	if tr != nil {
		tr.stat = st
		st.clientIP, st.userAgent = tr.clientIP, tr.userAgent
	}
	return st
}

// searchAdmit 客户端密钥放不放行这次搜索：不看模型白名单，按一次请求计进上限。放行时返回的 settle
// 在搜索结束后调用，没搜成就退还这一次（同对话）。
func (h *Handler) searchAdmit(p principal) (settle func(ok bool), d *apikeys.Denied) {
	now := time.Now()
	if d := h.keyDenial(p, "", now); d != nil {
		return nil, d
	}
	return func(ok bool) {
		if !ok && p.key != nil {
			h.cfg.Keys.Refund(p.key.ID, now)
		}
	}, nil
}

// searchMeta 出站的会话 id 和轮次 id：客户端带了 X-Conversation-ID / X-Conversation-Request-ID 就原样转，
// 没带就各生成一个（会话 id 带连字符、轮次 id 去连字符，同官方的形状）。换号重试用同一组。
func searchMeta(hdr http.Header) upstream.ChatMeta {
	meta := upstream.ChatMeta{
		ConversationID:        strings.TrimSpace(hdr.Get("X-Conversation-ID")),
		ConversationRequestID: strings.TrimSpace(hdr.Get("X-Conversation-Request-ID")),
	}
	if meta.ConversationID == "" {
		meta.ConversationID = session.NewUUIDv7()
	}
	if meta.ConversationRequestID == "" {
		meta.ConversationRequestID = session.NewUUIDv7Hex()
	}
	return meta
}

// searchRealm 在哪个域选号：model 带了域前缀（Claude Code 子请求的 model 是主对话的模型）照前缀；
// 否则按 pool.cross_realm，同不带前缀的对话：关着只用国内版，合池两个域一起选，国内版优先时先看国内版
// 有没有能接的号（tried 里的号不算）。关了国际版（global.enabled=false）只用国内版。
func (h *Handler) searchRealm(model string, tried map[string]bool) (realm string, cnFirst bool) {
	if realm, bare := resolveModel(model); bare != model {
		return realm, false
	}
	if !h.cfg.GlobalEnabled {
		return "cn", false
	}
	switch h.loadLive().CrossRealm {
	case livecfg.CrossRealmMerge:
		return "", false
	case livecfg.CrossRealmCNFirst:
		return h.cnFirstRealm(tried, ""), true
	}
	return "cn", false
}

// searchPenalized 搜索失败里说明号本身有状况的几类，和对话一样罚号。
func searchPenalized(kind upstream.ErrKind) bool {
	switch kind {
	case upstream.ErrHardCredit, upstream.ErrSoftRate, upstream.ErrSessionDead, upstream.ErrAccountFault, upstream.ErrWafBlock:
		return true
	}
	return false
}

// runSearch 换着号搜一次（规则见文件头）。st 记下用了哪个号、试了几次，交给请求记录和流水行。
func (h *Handler) runSearch(ctx context.Context, q upstream.SearchQuery, model string, meta upstream.ChatMeta, st *chatStat) (*upstream.SearchOutcome, *searchFailure) {
	tried, _ := h.keyScope(ctx) // 客户端密钥绑了分组时只用组里的号
	realm, cnFirst := h.searchRealm(model, tried)
	var lastErr error
	var slot slotWait
	for i := 0; i < h.cfg.MaxRotate; i++ {
		freed := h.cfg.Pool.SlotFreed()
		if cnFirst {
			realm = h.cnFirstRealm(tried, "")
		}
		acct := h.cfg.Pool.PickExcludingForRealm(tried, "", realm)
		if acct == nil && h.waitSlot(ctx, freed, &slot, tried, "", realm) {
			i--
			continue
		}
		if acct == nil {
			break
		}
		tried[acct.UID] = true
		st.uid, st.nick, st.model = acct.UID, acct.Nickname, searchModel
		st.proxy = h.proxyName(acct.UID)
		if acct.IsGlobal() {
			st.model = "global:" + searchModel
		}
		woke := slot.woke
		slot.woke = false
		if !acquireInFlight(h.cfg.Pool, acct.UID) {
			if woke {
				// 排队醒来的几个请求抢同一个名额、没抢到：不算换号，接着排队（同对话）。
				delete(tried, acct.UID)
				i--
				continue
			}
			if !rotateBackoff(i, ctx) {
				break
			}
			continue
		}
		if acct.NeedsRefresh(h.cfg.RefreshSkew) {
			if err := h.cfg.Upstream.RefreshToken(acct); err != nil {
				h.cfg.Pool.Release(acct.UID)
				lastErr = err
				var ue *upstream.Error
				if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
					h.cfg.Pool.Disable(acct.UID, "refresh session dead")
				} else if !upstream.IsRouteError(err) {
					h.cfg.Pool.NoteError(acct.UID) // 号的代理连不上不罚号
				}
				if !rotateBackoff(i, ctx) {
					break
				}
				continue
			}
			if err := acct.SaveAtomic(); err != nil {
				log.Printf("ERR: [server] search refresh acct=%s: save auth failed: %v", logfmt.Label(acct.UID, acct.Nickname), err)
			}
		}
		st.attempts++
		out, err := h.cfg.Upstream.Search(ctx, acct, q, meta)
		h.cfg.Pool.Release(acct.UID)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break // 客户端走了：不换号，也不罚号
		}
		var ue *upstream.Error
		if upstream.IsRouteError(err) {
			// 号的代理连不上、号被代理池拦着：不罚号，只换号（同对话）。请求记录写上是哪个代理。
			st.errMsg = err.Error()
			if !rotateBackoff(i, ctx) {
				break
			}
			continue
		}
		st.errMsg = ""
		if !errors.As(err, &ue) {
			if isUpstreamTimeout(err, false) {
				// 超时不是号的问题，换号也是同一个慢上游：不罚号、不换号（同对话）。
				log.Printf("WARN: [server] search timeout acct=%s: %v (rotation stopped, account not penalized)",
					logfmt.Label(acct.UID, acct.Nickname), err)
				return nil, h.searchGaveUp(fmt.Errorf("%w: %v", errUpstreamTimeout, err), &slot)
			}
			// 连不上上游：只喂连败计数（同对话）。
			h.cfg.Pool.NoteFailures(acct.UID)
		} else if f := h.searchStop(ue); f != nil {
			return nil, f
		} else if searchPenalized(ue.Kind) {
			h.applyErrorPolicy(acct.UID, ue.Kind, ue.Msg, "", ue)
			if ue.Kind == upstream.ErrWafBlock {
				if _, stop := h.noteWafBlock(acct.UID, tried, "", realm); stop {
					break // 出口 IP 被 WAF 拦了，别的出口也没有号能接：换号不换 IP（同对话）
				}
			}
		}
		// 其余（上游故障、404、认不出的 4xx、6022）说明不了号有问题：不罚号，只换号。
		if !rotateBackoff(i, ctx) {
			break
		}
	}
	return nil, h.searchGaveUp(lastErr, &slot)
}

// searchStop 换号也没用的上游错误，不罚号、不再换号，带上游原文回给客户端：请求本身的问题（11101、
// 11128 这类拦截）回 400；上游这一路满了（6021，以及排进队的 6020，搜索不排队）回 503。其余返回 nil。
func (h *Handler) searchStop(ue *upstream.Error) *searchFailure {
	status, code := http.StatusBadRequest, "invalid_request"
	switch ue.Kind {
	case upstream.ErrBadParams, upstream.ErrContentBlocked, upstream.ErrPromptTooLong, upstream.ErrImageInvalid:
	case upstream.ErrUpstreamBusy, upstream.ErrQueueWaiting:
		status, code = http.StatusServiceUnavailable, "upstream_busy"
	default:
		return nil
	}
	return &searchFailure{status: status, code: code, msg: ue.Msg, hint: h.hintOf(ue.Kind, ue.Msg, "", false, ue)}
}

// searchGaveUp 不能再换号时回给客户端的错误，口径同对话的末端：没选到号 503 no_healthy_account，号都在忙
// 503 并说明在忙，上游超时 503 upstream_timeout，最后一次是上游的错就带上游原文（限流 429，其余 503）。
func (h *Handler) searchGaveUp(lastErr error, slot *slotWait) *searchFailure {
	f := &searchFailure{status: http.StatusServiceUnavailable, code: "no_healthy_account",
		msg: "all accounts are temporarily unavailable, please retry later", hint: upstream.NoHealthyAccountHint()}
	var ue *upstream.Error
	switch {
	case lastErr == nil && slot.timedOut:
		f.msg, f.hint = h.busyError(slot)
	case errors.Is(lastErr, errUpstreamTimeout):
		f.code, f.msg = "upstream_timeout", "web search timed out upstream, please retry later"
	case upstream.IsRouteError(lastErr):
		f.msg = "all accounts are temporarily unavailable: the proxy of the account could not be reached, please retry later"
	case errors.As(lastErr, &ue):
		f.code, f.hint = "search_failed", h.hintOf(ue.Kind, ue.Msg, "", false, ue)
		if ue.Kind == upstream.ErrSoftRate {
			f.status, f.code = http.StatusTooManyRequests, "rate_limit_exceeded"
		}
		if s := strings.TrimSpace(ue.Msg); s != "" {
			f.msg = s
		}
	}
	return f
}

// formatSearch 搜索结果写成给模型读的文字，照官方 WebSearch 工具的 formatSearchResults；没有结果时的说法
// 也照官方（结果都因链接不安全被去掉时说「没有安全的结果」）。日期是 UTC 的。
func formatSearch(query string, out *upstream.SearchOutcome, now time.Time) string {
	if len(out.Results) == 0 {
		if out.Filtered {
			return `No safe search results found for query: "` + query + `"`
		}
		return `No search results found for query: "` + query + `"`
	}
	var b strings.Builder
	b.WriteString(`# Search Results for "` + query + "\"\n\n")
	for i, r := range out.Results {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "## %d. [%s](%s)\n\n%s\n\n**URL:** %s\n\n---", i+1, r.Title, r.URL, r.Snippet, r.URL)
	}
	plural := "s"
	if len(out.Results) == 1 {
		plural = ""
	}
	fmt.Fprintf(&b, "\n\n*Found %d result%s • Provider: %s • Search performed on %s*",
		len(out.Results), plural, out.Provider, now.UTC().Format(time.DateOnly))
	return b.String()
}
