// compensation.go 面板积分补偿查询与领取，批量操作限制为最多三个账号并发。
package panel

import (
	"log"
	"net/http"
	"sync"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

type compensationAccountItem struct {
	UID          string                       `json:"uid"`
	Nickname     string                       `json:"nickname"`
	Disabled     bool                         `json:"disabled,omitempty"`
	Status       *upstream.CompensationStatus `json:"status,omitempty"`
	NewlyClaimed bool                         `json:"newly_claimed"`
	Credit       int64                        `json:"credit"`
	Error        string                       `json:"error,omitempty"`
	BalanceError string                       `json:"balance_error,omitempty"`
}

const compensationBusyMessage = "该账号仍有任务执行中，请稍后重试"

func (p *Panel) compensationItem(a *auth.Auth, claim bool) compensationAccountItem {
	it := compensationAccountItem{UID: a.UID, Nickname: a.Nickname}
	if !claim {
		var err error
		it.Status, err = p.cfg.Upstream.GetCompensationStatus(a)
		if err != nil {
			it.Error = err.Error()
		}
		return it
	}
	if !p.tryLockAccount(a.UID) {
		it.Error = compensationBusyMessage
		return it
	}
	defer p.unlockAccount(a.UID)
	result, err := p.cfg.Upstream.ClaimAvailableCompensation(a)
	if err != nil {
		it.Error = err.Error()
		return it
	}
	it.Status = &result.CompensationStatus
	it.NewlyClaimed, it.Credit = result.NewlyClaimed, result.Credit
	if !result.NewlyClaimed {
		return it
	}
	log.Printf("panel: 补偿领取 uid=%s +%d分", a.UID, result.Credit)
	remain, total, expiring, earliestAt, earliestRemaining, err := p.cfg.Upstream.UserResourceDetailedWithExpiry(a, p.expiringSoonWindow())
	if err != nil {
		// 领取已成功，余额刷新失败单独提示，不能让前端诱导用户重复领奖。
		it.BalanceError = err.Error()
		return it
	}
	p.cfg.Pool.ReenableIfCredits(a.UID, remain, total)
	p.cfg.Pool.SetCreditsDetailed(a.UID, remain, total, expiring, earliestAt, earliestRemaining)
	return it
}

func (p *Panel) compensationAll(w http.ResponseWriter, r *http.Request, claim bool) {
	if p.cfg.Upstream == nil {
		writeErr(w, http.StatusNotImplemented, "upstream unavailable")
		return
	}
	// 已禁用账号照常列出、可单号领取（禁用只关选号，之后可能重新启用），但「领取全部」只查不领。
	type target struct {
		a        *auth.Auth
		disabled bool
	}
	targets := make([]target, 0)
	for _, st := range p.cfg.Pool.List() {
		a := p.cfg.Pool.AuthByUID(st.UID)
		if a != nil && !a.IsGlobal() && !a.IsEnterprise() {
			targets = append(targets, target{a, st.Disabled})
		}
	}
	items := make([]compensationAccountItem, len(targets))
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i, t := range targets {
		select {
		case sem <- struct{}{}:
		case <-r.Context().Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(i int, t target) {
			defer wg.Done()
			defer func() { <-sem }()
			items[i] = p.compensationItem(t.a, claim && !t.disabled)
			items[i].Disabled = t.disabled
		}(i, t)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accounts": items})
}

func (p *Panel) compensationStatus(w http.ResponseWriter, r *http.Request) {
	p.compensationAll(w, r, false)
}

func (p *Panel) compensationClaimAll(w http.ResponseWriter, r *http.Request) {
	p.compensationAll(w, r, true)
}

func (p *Panel) accountCompensationClaim(w http.ResponseWriter, r *http.Request) {
	a := p.accountByUID(w, r.PathValue("uid"))
	if a == nil {
		return
	}
	if a.IsGlobal() || a.IsEnterprise() {
		writeErr(w, http.StatusBadRequest, "积分补偿仅支持国内版个人账号")
		return
	}
	if p.cfg.Upstream == nil {
		writeErr(w, http.StatusNotImplemented, "upstream unavailable")
		return
	}
	it := p.compensationItem(a, true)
	if it.Error != "" {
		if it.Error == compensationBusyMessage {
			writeErr(w, http.StatusConflict, it.Error)
			return
		}
		writeErr(w, http.StatusBadGateway, it.Error)
		return
	}
	writeJSON(w, http.StatusOK, it)
}
