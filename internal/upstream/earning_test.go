// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

// earning_test.go 积分获取流水的记录点：签到、两种口径的领奖、补偿、旅行、新手礼包、连登兑换领到积分时
// 各记一笔（来源、任务码或档位、积分数、账号），没领到、已领过、请求失败时不记。

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// earnings 收集 OnEarning 收到的流水。
type earnings struct {
	mu  sync.Mutex
	got []Earning
}

func (e *earnings) add(x Earning) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, x)
}

func (e *earnings) list() []Earning {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Earning(nil), e.got...)
}

func TestEarnings(t *testing.T) {
	const (
		checkinOpen  = `{"code":0,"data":{"active":true,"today_checked_in":false}}`
		compStatus   = "/billing/meter/compensation-status"
		compClaim    = "/billing/meter/claim-compensation"
		giftCheck    = "/billing/meter/check-gift-claimed"
		giftClaim    = "/billing/meter/claim-gift"
		taskClaim    = "/activity/growth/tasks/chat_5/claim"
		mpTaskClaim  = "/activity/growth/tasks/mp_task/claim"
		checkinPath  = dailyCheckinPath
		checkinState = checkinStatusPath
	)
	type want struct {
		source, detail string
		credit         int64
	}
	cases := []struct {
		name   string
		routes map[string][2]any // "METHOD path" → {状态码, 回应体}
		call   func(c *Client) error
		want   []want
		fails  bool // 这次调用本来就该失败
	}{
		{"签到", map[string][2]any{
			"POST " + checkinState: {200, checkinOpen}, "POST " + checkinPath: {200, `{"code":0,"data":{"credit":20,"streak_days":3}}`},
		}, func(c *Client) error { return c.DailyCheckin(upAuth()) }, []want{{EarnCheckin, "", 20}}, false},
		{"签到没给积分数不记", map[string][2]any{
			"POST " + checkinState: {200, checkinOpen}, "POST " + checkinPath: {200, `{"code":0,"data":{}}`},
		}, func(c *Client) error { return c.DailyCheckin(upAuth()) }, nil, false},
		{"签到回的 data 不是对象：签到照样成功，不记", map[string][2]any{
			"POST " + checkinState: {200, checkinOpen}, "POST " + checkinPath: {200, `{"code":0,"data":"ok"}`},
		}, func(c *Client) error { return c.DailyCheckin(upAuth()) }, nil, false},
		{"领奖", map[string][2]any{"POST " + taskClaim: {200, `{"code":0,"data":{"credit":100,"energy":5}}`}},
			func(c *Client) error { _, _, err := c.ClaimReward(upAuth(), "chat_5"); return err }, []want{{EarnTask, "chat_5", 100}}, false},
		{"领奖已领过不记", map[string][2]any{"POST " + taskClaim: {200, `{"code":0,"data":{"already_claimed":true,"credit":100}}`}},
			func(c *Client) error { _, _, err := c.ClaimReward(upAuth(), "chat_5"); return err }, nil, false},
		{"只给成长值的领奖不记", map[string][2]any{"POST " + taskClaim: {200, `{"code":0,"data":{"credit":0,"energy":5}}`}},
			func(c *Client) error { _, _, err := c.ClaimReward(upAuth(), "chat_5"); return err }, nil, false},
		{"小程序领奖", map[string][2]any{"POST " + mpTaskClaim: {200, `{"code":0,"data":{"credit":50,"energy":2}}`}},
			func(c *Client) error { _, _, err := c.ClaimRewardMP(upAuth(), "mp_task"); return err }, []want{{EarnTask, "mp_task", 50}}, false},
		{"小程序领奖已领过不记", map[string][2]any{"POST " + mpTaskClaim: {200, `{"code":0,"data":{"already_claimed":true,"credit":50}}`}},
			func(c *Client) error { _, _, err := c.ClaimRewardMP(upAuth(), "mp_task"); return err }, nil, false},
		{"小程序领奖失败不记", map[string][2]any{"POST " + mpTaskClaim: {200, `{"code":0,"data":"nope"}`}},
			func(c *Client) error { _, _, err := c.ClaimRewardMP(upAuth(), "mp_task"); return err }, nil, true},
		{"补偿按到账额度记（回应没带就用状态里的）", map[string][2]any{
			"POST " + compStatus: {200, `{"code":0,"data":{"active":true,"claimed":false,"credit_num":500}}`}, "POST " + compClaim: {200, `{"code":0,"data":{}}`},
		}, func(c *Client) error { _, err := c.ClaimAvailableCompensation(upAuth()); return err }, []want{{EarnCompensation, "", 500}}, false},
		{"补偿已领过不记", map[string][2]any{"POST " + compStatus: {200, `{"code":0,"data":{"active":true,"claimed":true,"credit_num":500}}`}},
			func(c *Client) error { _, err := c.ClaimAvailableCompensation(upAuth()); return err }, nil, false},
		{"旅行", map[string][2]any{"POST " + travelClaimPath: {200, `{"code":0,"data":{"reward_credit":9}}`}},
			func(c *Client) error { _, err := c.TravelClaim(upAuth()); return err }, []want{{EarnTravel, "", 9}}, false},
		{"旅行没奖励不记", map[string][2]any{"POST " + travelClaimPath: {200, `{"code":0,"data":{}}`}},
			func(c *Client) error { _, err := c.TravelClaim(upAuth()); return err }, nil, false},
		{"新手礼包", map[string][2]any{
			"POST " + giftCheck: {200, `{"code":0,"data":{"active":true,"claimed":false}}`}, "POST " + giftClaim: {200, `{"code":0,"data":{"credit":100}}`},
		}, func(c *Client) error { _, err := c.ClaimGift(upAuth()); return err }, []want{{EarnGift, "", 100}}, false},
		{"连登兑换按这一档列出的积分记", map[string][2]any{"POST " + streakRedeemPath: {200, `{"code":0,"data":{}}`}},
			func(c *Client) error { return c.GrowthRedeemTier(upAuth(), "7d", 300) }, []want{{EarnRedeem, "7d", 300}}, false},
		{"连登兑换失败不记", map[string][2]any{"POST " + streakRedeemPath: {403, `{"code":403,"msg":"连续登录天数不足"}`}},
			func(c *Client) error { return c.GrowthRedeemTier(upAuth(), "28d", 1000) }, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			for route, rep := range tc.routes {
				method, path, _ := strings.Cut(route, " ")
				f.on(method, path, rep[0].(int), rep[1].(string))
			}
			var rec earnings
			c := upClient(f)
			c.OnEarning = rec.add
			before := time.Now()
			if err := tc.call(c); (err != nil) != tc.fails {
				t.Fatalf("调用结果 err=%v，应当出错=%v", err, tc.fails)
			}
			got := rec.list()
			if len(got) != len(tc.want) {
				t.Fatalf("记了 %d 笔：%+v，应为 %d 笔", len(got), got, len(tc.want))
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Source != w.source || g.Detail != w.detail || g.Credit != w.credit {
					t.Errorf("第 %d 笔 = %s/%s/%d，应为 %s/%s/%d", i+1, g.Source, g.Detail, g.Credit, w.source, w.detail, w.credit)
				}
				if g.UID != "u-1" || g.Nickname != "测试号" || g.Realm != "cn" || g.Time.Before(before) {
					t.Errorf("第 %d 笔的账号或时间不对：%+v", i+1, g)
				}
			}
		})
	}

	// 没设 OnEarning（命令行工具）照常领，不出错。
	f := newUpFake(t)
	f.on(http.MethodPost, travelClaimPath, 200, `{"code":0,"data":{"reward_credit":9}}`)
	if n, err := upClient(f).TravelClaim(upAuth()); err != nil || n != 9 {
		t.Fatalf("没设 OnEarning 时领奖 = %d, %v", n, err)
	}
}
