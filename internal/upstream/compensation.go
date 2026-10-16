// compensation.go 国内版积分补偿：查询官方活动状态，仅在开放且未领取时领奖。
package upstream

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// CompensationStatus 对齐官网用量管理页 compensation-status 的返回字段。
// 额度与截止时间由上游决定，不写死某一次活动的数值。
type CompensationStatus struct {
	Active    bool   `json:"active"`
	Claimed   bool   `json:"claimed"`
	CreditNum int64  `json:"credit_num"`
	EndTime   string `json:"end_time,omitempty"`
}

// CompensationClaim 区分本次新领和此前已领，避免重复计算到账积分。
type CompensationClaim struct {
	CompensationStatus
	NewlyClaimed bool  `json:"newly_claimed"`
	Credit       int64 `json:"credit"`
}

func compensationAccount(a *auth.Auth) error {
	if a == nil || a.IsGlobal() || a.IsEnterprise() {
		return fmt.Errorf("积分补偿仅支持国内版个人账号")
	}
	return nil
}

// GetCompensationStatus 查询活动状态；官网对此只读操作使用 POST。
func (c *Client) GetCompensationStatus(a *auth.Auth) (*CompensationStatus, error) {
	if err := compensationAccount(a); err != nil {
		return nil, err
	}
	data, err := c.billingJSON(a, http.MethodPost, "/billing/meter/compensation-status", nil)
	if err != nil {
		return nil, err
	}
	var status *CompensationStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("解析补偿状态: %w", err)
	}
	if status == nil {
		return nil, fmt.Errorf("补偿状态为空")
	}
	return status, nil
}

// ClaimCompensation 领取活动补偿；业务错误保留上游提示。
func (c *Client) ClaimCompensation(a *auth.Auth) (int64, error) {
	if err := compensationAccount(a); err != nil {
		return 0, err
	}
	data, err := c.billingJSON(a, http.MethodPost, "/billing/meter/claim-compensation", nil)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Credit int64 `json:"credit"`
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &resp); err != nil {
			return 0, fmt.Errorf("解析补偿领取结果: %w", err)
		}
	}
	return resp.Credit, nil
}

// ClaimAvailableCompensation 供面板和签到排程共用，已领/活动未开放时跳过领奖。
func (c *Client) ClaimAvailableCompensation(a *auth.Auth) (*CompensationClaim, error) {
	status, err := c.GetCompensationStatus(a)
	if err != nil {
		return nil, err
	}
	result := &CompensationClaim{CompensationStatus: *status}
	if !status.Active || status.Claimed {
		return result, nil
	}
	credit, err := c.ClaimCompensation(a)
	if err != nil {
		// 其他入口可能刚领完，或响应丢失：回读确认，避免把已到账误报成失败。
		if after, readErr := c.GetCompensationStatus(a); readErr == nil && after.Claimed {
			return &CompensationClaim{CompensationStatus: *after}, nil
		}
		return nil, err
	}
	result.Claimed, result.NewlyClaimed = true, true
	result.Credit = credit
	if result.Credit == 0 {
		// 官网只依据 code=0 判断成功，部分响应不含 credit；沿用刚查询的官方额度。
		result.Credit = status.CreditNum
	}
	return result, nil
}
