// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// capqueue.go 上游回 6020（排进容量队列了）时在这个号上照官方排队，到号了原样重发。请求形状与
// 查询节奏见 upstream/capqueue.go。
package server

import (
	"context"
	"errors"
	"io"
	"log"
	"strconv"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// chatAttempt 发一次上游对话的结果（ChatStreamContext 的返回值和发出的时刻）。
type chatAttempt struct {
	rc      io.ReadCloser
	status  int
	body    []byte
	err     error
	started time.Time
}

// queuePollInterval 两次排队查询之间等多久（按排位，见 upstream.QueuePollInterval）；测试换成很短的间隔。
var queuePollInterval = upstream.QueuePollInterval

// isQueueWaiting 报告这次上游对话是不是回了 6020。
func isQueueWaiting(err error) bool {
	var ue *upstream.Error
	return errors.As(err, &ue) && ue.Kind == upstream.ErrQueueWaiting
}

// capacityQueue 上游回 6020 时在 acct 上排队：等 retry_after 后查 /v2/chat/queue/status，之后按排位
// 隔一会儿查一次，到号（ready）就用同一个号原样重发（同一份请求体和会话头），重发又回 6020 就接着排。
// 排队期间不给客户端发任何数据，不罚号、不换号，号的在途名额一直占着。
//
// 不等了就返回最后那次 6020，由调用方回 503：一个请求从第一次 6020 起合计等满 pool.in_flight_wait、
// 或者客户端断开时，先取消排队（官方在使用者点取消时发）再返回；上游说排队过期（expired）时直接
// 返回，同官方不取消。in_flight_wait 设成 0 时不排队，取消后直接返回。
func (h *Handler) capacityQueue(ctx context.Context, acct *auth.Auth, body []byte, clientIP string, meta upstream.ChatMeta, model string, last chatAttempt) chatAttempt {
	wait := h.cfg.Pool.InFlightWait()
	since := time.Now()
	label := logfmt.Label(acct.UID, acct.Nickname)
	cancelQueue := func(why string) chatAttempt {
		// 客户端断开时 ctx 已取消，取消请求不能跟着被掐。
		if err := h.cfg.Upstream.CancelCapacityQueue(context.WithoutCancel(ctx), acct, model, meta); err != nil {
			log.Printf("WARN: [server] capacity queue: model=%s acct=%s 取消排队失败：%v", model, label, err)
		}
		log.Printf("WARN: [server] capacity queue: model=%s acct=%s %s，已取消排队，等了 %.1fs", model, label, why, time.Since(since).Seconds())
		return last
	}
	for {
		q := upstream.ParseQueueWaiting(string(last.body))
		pos, delay := q.Position, q.RetryAfter
		log.Printf("INFO: [server] capacity queue: model=%s acct=%s 上游排队（6020，排位 %s），%.1fs 后查", model, label, queuePosText(pos), delay.Seconds())
		for ready := false; !ready; {
			left := wait - time.Since(since)
			if left <= 0 {
				return cancelQueue("等满 pool.in_flight_wait " + wait.String() + " 仍没到号")
			}
			t := time.NewTimer(min(delay, left))
			select {
			case <-ctx.Done():
				t.Stop()
				return cancelQueue("客户端断开")
			case <-t.C:
			}
			if delay >= left {
				continue // 等到的是上限，没到查询的时候：上面按等满处理
			}
			st, err := h.cfg.Upstream.CapacityQueueStatus(ctx, acct, model, meta)
			if err != nil {
				if ctx.Err() != nil {
					return cancelQueue("客户端断开")
				}
				log.Printf("WARN: [server] capacity queue: model=%s acct=%s 查排队状态失败，稍后再查：%v", model, label, err)
				delay = queuePollInterval(pos)
				continue
			}
			if st.Position >= 0 {
				pos = st.Position
			}
			switch st.Status {
			case "ready":
				ready = true
			case "expired":
				log.Printf("WARN: [server] capacity queue: model=%s acct=%s 上游说排队过期（%s），等了 %.1fs", model, label, st.Message, time.Since(since).Seconds())
				return last
			default:
				// waiting，或者上游给了没见过的状态：接着等，间隔按这次回的排位（官方 waiting 的做法）。
				delay = queuePollInterval(st.Position)
			}
		}
		log.Printf("INFO: [server] capacity queue: model=%s acct=%s 排了 %.1fs 到号，原样重发", model, label, time.Since(since).Seconds())
		started := time.Now()
		rc, status, respBody, err := h.cfg.Upstream.ChatStreamContext(ctx, acct, body, clientIP, meta)
		last = chatAttempt{rc: rc, status: status, body: respBody, err: err, started: started}
		if !isQueueWaiting(err) {
			return last
		}
	}
}

// queuePosText 排位的日志写法，-1 写「未知」。
func queuePosText(pos int) string {
	if pos < 0 {
		return "未知"
	}
	return strconv.Itoa(pos)
}
