package scheduler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// 签到后的连登管家自动领取一次，并在到账后立即刷新余额、解除余额冷却。
func TestStreakBonusClaimsCompensationOnce(t *testing.T) {
	claimed := false
	claims, balances := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/billing/meter/compensation-status":
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"active": true, "claimed": claimed, "credit_num": 500,
			}})
		case "/billing/meter/claim-compensation":
			claims++
			claimed = true
			w.Write([]byte(`{"code":0,"data":{}}`))
		case "/v2/billing/meter/get-user-resource":
			balances++
			w.Write([]byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CapacitySize":500,"CapacityRemain":500}]}}}}`))
		default:
			// 其他连登操作不影响本测试的补偿行为。
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"code":10001,"msg":"unavailable"}`))
		}
	}))
	defer srv.Close()
	accounts := pool.New("")
	accounts.Add(&auth.Auth{UID: "u1", AccessToken: "at"})
	accounts.Cooldown("u1", pool.CoolHard, time.Hour, "credits exhausted")
	s := New(Config{Pool: accounts, Upstream: &upstream.Client{
		HTTP: srv.Client(), BillingBaseCN: srv.URL, ChatBaseCN: srv.URL,
	}})
	s.RunStreakBonusNow()
	s.RunStreakBonusNow()
	if claims != 1 || balances != 1 {
		t.Fatalf("claims=%d balances=%d want 1/1", claims, balances)
	}
	st, _ := accounts.Status("u1")
	if st.Credits != 500 || st.Cooling {
		t.Fatalf("balance not applied: %+v", st)
	}
}
