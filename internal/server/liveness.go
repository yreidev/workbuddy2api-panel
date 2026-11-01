// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 测活：指定一个号发一条最短的真实对话，看它现在能不能用。走和真实请求同一条路径（chatCompletions：
// 刷新令牌、发上游、错误分类与罚号、6020 排队、用量与请求日志），只是不选号，钉死这个号只试一次，
// 也不碰会话粘性。测通了恢复这个号（pool.RecoverAfterLiveness），结果记进号池的「上次测活」。
// 面板的单测、全部测活和定时测活都调 TestAccount。做法参考 new-api 的渠道测试和 sub2api 的账号测试。

package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
)

const (
	// livenessPath 请求日志里测活的入口路径。
	livenessPath = "/panel/liveness"
	// livenessLabel 请求日志「密钥」一列里测活的名字，按它能筛出所有测活。
	livenessLabel = "测活"
	// livenessUsageKey 用量统计里测活的密钥维度，面板显示成「测活」（见 panel.keyLabel）。
	livenessUsageKey = "liveness"
	// livenessMaxTokens 测活回复的长度上限：上游回了字就算通，不用回完整。
	livenessMaxTokens = 16
	// livenessReplyMax 回给面板的上游回复最多多少字节。
	livenessReplyMax = 2000
	// livenessRepicks 自动挑的模型被上游拒了（11102/11158，号本身没问题）时，最多换几次模型再测。
	livenessRepicks = 2
)

// livenessKey 测活请求的 context 键，值是钉死的号的 UID。
type livenessKey struct{}

// livenessFrom 测活请求钉死的号；不是测活返回空串。
func livenessFrom(ctx context.Context) string {
	uid, _ := ctx.Value(livenessKey{}).(string)
	return uid
}

// TestAccount 测活一次。model 为空时挑这个号能接的最便宜的模型，挑的模型被上游拒了（这个号不能用这个模型，
// 号本身没问题）就换一个再测。号不存在、挑不出模型返回 error；上游的失败不算 error，写在结果里。
// 返回结果和上游回复的文字。
func (h *Handler) TestAccount(ctx context.Context, uid, model string, scheduled bool) (pool.LivenessResult, string, error) {
	a := h.cfg.Pool.AuthByUID(uid)
	before, ok := h.cfg.Pool.Status(uid)
	if a == nil || !ok {
		return pool.LivenessResult{}, "", fmt.Errorf("account %s not found", uid)
	}
	if before.ProxyBlocked != "" {
		// 被代理池拦着：请求出不去，测了也只是白记一次不通。
		return pool.LivenessResult{}, "", fmt.Errorf("account %s: %s，先处理好代理再测", uid, before.ProxyBlocked)
	}
	if model != "" {
		res, reply := h.testModel(ctx, a, before, model, scheduled)
		return res, reply, nil
	}
	for i := 0; ; i++ {
		if model = h.livenessModel(a, before.Enterprise); model == "" {
			return pool.LivenessResult{}, "", fmt.Errorf("account %s: 挑不出模型（模型目录还没拉到，或者这个号能接的模型都不能用），指定一个模型再测", uid)
		}
		res, reply := h.testModel(ctx, a, before, model, scheduled)
		if res.OK || i == livenessRepicks || !h.cfg.Pool.ModelRefused(uid, model) {
			return res, reply, nil
		}
		log.Printf("liveness %s: 这个号不能用 model=%s，换个模型再测", logfmt.Label(a.UID, a.Nickname), model)
	}
}

// testModel 用这个模型给号发一次测活请求，记下结果。before 是测之前号的状态。
func (h *Handler) testModel(ctx context.Context, a *auth.Auth, before pool.Status, model string, scheduled bool) (pool.LivenessResult, string) {
	uid := a.UID
	name := model
	if before.Realm == "global" {
		name = "global:" + model
	}
	body, _ := json.Marshal(map[string]any{
		"model":      name,
		"stream":     true,
		"max_tokens": livenessMaxTokens,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	tr := &requestTrace{id: reqlog.NewRequestID(), start: time.Now(), path: livenessPath, apiKey: livenessLabel}
	ctx = context.WithValue(context.WithValue(ctx, requestTraceKey{}, tr), livenessKey{}, uid)
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	w := &livenessWriter{header: http.Header{}}
	obs := &responseObserver{ResponseWriter: w}
	if h.cfg.RequestLog != nil {
		h.cfg.RequestLog.Begin()
	}
	h.chatCompletions(obs, r)
	// 什么都没写出来就是没测成：状态码 0 照不通记，不像入口那样按 200 算
	ev := tr.event(obs.status, obs.errBody)
	if h.cfg.RequestLog != nil {
		h.cfg.RequestLog.Record(ev)
	}

	res := pool.LivenessResult{
		At: tr.start, OK: ev.OK, Model: model, Status: ev.Status, Error: ev.Error,
		TTFBMs: ev.TTFBMs, DurationMs: ev.DurationMs, Credit: ev.Credit, Scheduled: scheduled,
	}
	if res.OK {
		now := time.Now()
		res.Recovered = h.cfg.Pool.RecoverAfterLiveness(uid, model) ||
			now.Before(before.BreakerUntil) || now.Before(before.DegradeUntil)
	}
	h.cfg.Pool.RecordLiveness(uid, res)
	label := logfmt.Label(a.UID, a.Nickname)
	if res.OK {
		log.Printf("liveness %s: ok model=%s ttfb=%dms total=%dms credit=%g recovered=%v scheduled=%v",
			label, model, res.TTFBMs, res.DurationMs, res.Credit, res.Recovered, scheduled)
	} else {
		log.Printf("liveness %s: WARN 失败 model=%s status=%d scheduled=%v: %s", label, model, res.Status, scheduled, res.Error)
	}
	return res, livenessReply(w.body.Bytes())
}

// livenessModel 这个号能接的最便宜的模型，models.hidden 里藏起来的、这个号确定不能用的（不接的模型名单、
// 上游回过 11102 还在避让）不算。目录还没拉到（启动预热失败）时先照 /v1/models 的口径拉一次再挑。
func (h *Handler) livenessModel(a *auth.Auth, enterprise bool) string {
	realm := a.Realm()
	hidden := h.loadLive().HiddenModels
	skip := func(m string) bool {
		_, hide := hidden[realm+":"+m]
		return hide || h.cfg.Pool.ModelRefused(a.UID, m)
	}
	if m := h.cfg.Upstream.CheapestModel(a.UID, realm, enterprise, skip); m != "" {
		return m
	}
	if realm == "global" {
		h.fetchGlobalModels()
	} else {
		h.fetchDynamicModels()
	}
	return h.cfg.Upstream.CheapestModel(a.UID, realm, enterprise, skip)
}

// livenessReply 从测活的流式响应里拼出上游回的文字（没有正文时用思考内容），最多 livenessReplyMax 字节。
func livenessReply(raw []byte) string {
	var text, reasoning strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			text.WriteString(c.Delta.Content)
			reasoning.WriteString(c.Delta.ReasoningContent)
		}
	}
	out := text.String()
	if strings.TrimSpace(out) == "" {
		out = reasoning.String()
	}
	return logfmt.Truncate(strings.TrimSpace(out), livenessReplyMax)
}

// livenessWriter 收下测活请求的响应：流式正文留着拼回复，Flush 什么都不做。
type livenessWriter struct {
	header http.Header
	body   bytes.Buffer
}

func (w *livenessWriter) Header() http.Header { return w.header }

func (w *livenessWriter) Write(p []byte) (int, error) {
	if w.body.Len() < 256*1024 {
		w.body.Write(p)
	}
	return len(p), nil
}

func (w *livenessWriter) WriteHeader(int) {}

func (w *livenessWriter) Flush() {}
