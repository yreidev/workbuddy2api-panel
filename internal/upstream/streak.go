// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// streak.go 成长中心连登兑换 + 抽奖 API（2026-09-12 成长中心 bundle 逆向 + 页面实测）。
//
// 机制：连登档位（7d/14d/28d）按**连续登录天数**解锁；兑换（POST /activity/growth/redeem）
// 发 credit/energy/补签卡/**抽奖次数**；抽奖（POST /activity/growth/lottery/draw）每次消耗
// 1 次 chances。未解锁兑换返回 HTTP 403「连续登录天数不足」。
// client_token 为前端生成的幂等令牌（randomUUID）。
package upstream

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"fmt"
	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"net/http"
)

// streakRedeemPath / lottery 路径（growth 域，webJSON 走网页域）。
const (
	streakRedeemPath   = "/activity/growth/redeem"
	lotterySummaryPath = "/activity/growth/lottery/summary"
	lotteryDrawPath    = "/activity/growth/lottery/draw"
)

// clientToken 幂等令牌：网页写 `${prefix}-${crypto.randomUUID()}`（兑换前缀
// "redeem-<tier>"、抽奖前缀 "draw"）；UUID 部分保持 8-4-4-4-12 小写十六进制。
// 前缀可选：school.go（不许动）调无参形式，保持只返回裸 UUID。
func clientToken(prefix ...string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	uuid := fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
	if len(prefix) > 0 && prefix[0] != "" {
		return prefix[0] + "-" + uuid
	}
	return uuid
}

// StreakFull 连登完整状态（GET /activity/growth/streak）。
type StreakFull struct {
	Streak struct {
		Days              int    `json:"days"`
		MonthTotalDays    int    `json:"month_total_days"`
		NextTier          string `json:"next_tier"`
		NextTierRemaining int    `json:"next_tier_remaining"`
	} `json:"streak"`
	MakeupCards struct {
		Balance int `json:"balance"`
		Max     int `json:"max"`
	} `json:"makeup_cards"`
	RedemptionStatus struct {
		Tier7dStatus  string `json:"tier_7d_status"`
		Tier14dStatus string `json:"tier_14d_status"`
		Tier28dStatus string `json:"tier_28d_status"`
		RemainingDays int    `json:"remaining_days"`
		Tiers         []struct {
			Tier    string `json:"tier"`
			Days    int    `json:"days"`
			Credit  int    `json:"credit"`
			Energy  int    `json:"energy"`
			Cards   int    `json:"cards"`
			Chances int    `json:"chances"`
		} `json:"tiers"`
	} `json:"redemption_status"`
}

// GrowthStreakFull 拉取连登完整状态。
func (c *Client) GrowthStreakFull(a *auth.Auth) (*StreakFull, error) {
	data, err := c.webJSON(a, http.MethodGet, streakPath, webPageGrowthCenter, nil)
	if err != nil {
		return nil, err
	}
	out := &StreakFull{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GrowthRedeemTier 兑换连登档位（tier: "7d"|"14d"|"28d"）。credit 是连登状态里这一档列出的积分，兑换成功后
// 按它记进积分获取流水（兑换的回应形状没核对过）。未解锁返回 *Error（HTTP 403「连续登录天数不足」），
// 调用方按 locked 状态跳过即可。
func (c *Client) GrowthRedeemTier(a *auth.Auth, tier string, credit int64) error {
	_, err := c.webJSON(a, http.MethodPost, streakRedeemPath, webPageGrowthCenter,
		map[string]any{"tier": tier, "client_token": clientToken("redeem-" + tier)})
	if err == nil {
		c.noteEarning(a, EarnRedeem, tier, credit)
	}
	return err
}

// LotteryChances 当前抽奖次数（GET /activity/growth/lottery/summary）。
func (c *Client) LotteryChances(a *auth.Auth) (int, error) {
	data, err := c.webJSON(a, http.MethodGet, lotterySummaryPath, webPageGrowthCenter, nil)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Chances int `json:"chances"`
		Module  struct {
			Enabled bool `json:"enabled"`
		} `json:"module"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, err
	}
	return resp.Chances, nil
}

// LotteryDraw 抽奖一次，返回原始奖品载荷（prize 字段形状由活动期决定，透传给调用方）。
func (c *Client) LotteryDraw(a *auth.Auth) (json.RawMessage, error) {
	return c.webJSON(a, http.MethodPost, lotteryDrawPath, webPageGrowthCenter,
		map[string]any{"client_token": clientToken("draw")})
}
