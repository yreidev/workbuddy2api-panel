// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// earning.go 积分获取流水：签到、领奖、积分补偿、猫猫旅行、新手礼包、连登兑换领到积分时，把这一笔交给
// Client.OnEarning（网关接到面板的「积分获取」留存，见 cmd/server）。只记领到的积分，不记余额变动；
// 积分数取官方回应里的值，连登兑换的回应形状没核对过，取连登状态里那一档列出的积分。
package upstream

import (
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// 积分从哪来（Earning.Source）。
const (
	EarnCheckin      = "checkin"      // 每日签到
	EarnTask         = "task"         // 成长任务领奖（桌面端、小程序两种口径）
	EarnCompensation = "compensation" // 积分补偿
	EarnTravel       = "travel"       // 猫猫旅行
	EarnGift         = "gift"         // 新手礼包
	EarnRedeem       = "redeem"       // 连登兑换档位
)

// Earning 领到的一笔积分，也是积分获取留存里一行的形状。
type Earning struct {
	Time     time.Time `json:"t"`
	UID      string    `json:"uid"`
	Nickname string    `json:"nickname,omitempty"`
	Realm    string    `json:"realm,omitempty"`
	Source   string    `json:"source"`
	// Detail 领奖是任务码，连登兑换是档位（7d / 14d / 28d），其余为空。
	Detail string `json:"detail,omitempty"`
	Credit int64  `json:"credit"`
}

// noteEarning 领到了积分（credit > 0）就交给 OnEarning；没设 OnEarning（命令行工具）什么都不做。
func (c *Client) noteEarning(a *auth.Auth, source, detail string, credit int64) {
	if c.OnEarning == nil || credit <= 0 {
		return
	}
	c.OnEarning(Earning{
		Time: time.Now(), UID: a.UID, Nickname: a.Nickname, Realm: a.Realm(),
		Source: source, Detail: detail, Credit: credit,
	})
}
