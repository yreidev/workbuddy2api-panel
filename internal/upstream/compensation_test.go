package upstream

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// 查询与领取遵循官网协议，未开放/已领不请求领奖，奖励以官方响应为准。
func TestClaimAvailableCompensation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		claim      string
		wantCalls  int
		wantCredit int64
		wantNew    bool
		wantErr    bool
	}{
		{"inactive", `{"active":false,"claimed":false}`, "", 0, 0, false, false},
		{"already_claimed", `{"active":true,"claimed":true,"credit_num":500}`, "", 0, 0, false, false},
		{"claim", `{"active":true,"claimed":false,"credit_num":500,"end_time":"2026-10-22 23:59:59"}`, `{"credit":500}`, 1, 500, true, false},
		{"official_amount", `{"active":true,"credit_num":750}`, `{}`, 1, 750, true, false},
		{"claim_amount", `{"active":true,"credit_num":500}`, `{"credit":600}`, 1, 600, true, false},
		{"null_claim_data", `{"active":true,"credit_num":500}`, `null`, 1, 500, true, false},
		{"missing_status", `null`, "", 0, 0, false, true},
		{"malformed_status", `{"active":"yes"}`, "", 0, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("unexpected request: %s auth=%q", r.Method, r.Header.Get("Authorization"))
				}
				data := tc.status
				switch r.URL.Path {
				case "/billing/meter/compensation-status":
				case "/billing/meter/claim-compensation":
					calls++
					data = tc.claim
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.Write([]byte(`{"code":0,"data":` + data + `}`))
			}))
			defer srv.Close()
			c := &Client{HTTP: srv.Client(), BillingBaseCN: srv.URL}
			result, err := c.ClaimAvailableCompensation(&auth.Auth{UID: "u1", AccessToken: "test-token"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if calls != tc.wantCalls {
				t.Errorf("claim requests=%d want %d", calls, tc.wantCalls)
			}
			if err == nil && (result.NewlyClaimed != tc.wantNew || result.Credit != tc.wantCredit) {
				t.Errorf("result=%+v want new=%v credit=%d", result, tc.wantNew, tc.wantCredit)
			}
			if tc.wantNew && !result.Claimed {
				t.Error("successful claim must update claimed state")
			}
			if tc.name == "claim" && result.EndTime != "2026-10-22 23:59:59" {
				t.Errorf("deadline lost: %+v", result)
			}
		})
	}
}

// 网络响应丢失/其他入口抢先领奖时回读确认；真实业务错误保留上游文案。
func TestClaimCompensationFailureRechecksStatus(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(map[bool]string{false: "business_error", true: "concurrent_claim"}[claimed], func(t *testing.T) {
			reads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/billing/meter/claim-compensation" {
					w.WriteHeader(http.StatusBadRequest)
					w.Write([]byte(`{"code":10001,"msg":"补偿尚未开放"}`))
					return
				}
				reads++
				json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
					"active": true, "claimed": claimed && reads > 1, "credit_num": 500,
				}})
			}))
			defer srv.Close()
			c := &Client{HTTP: srv.Client(), BillingBaseCN: srv.URL}
			result, err := c.ClaimAvailableCompensation(&auth.Auth{AccessToken: "at"})
			if reads != 2 {
				t.Fatalf("status reads=%d want 2", reads)
			}
			if claimed {
				if err != nil || !result.Claimed || result.NewlyClaimed || result.Credit != 0 {
					t.Fatalf("concurrent claim: result=%+v err=%v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "补偿尚未开放") {
				t.Fatalf("upstream error not preserved: %v", err)
			}
		})
	}
}

func TestCompensationRejectsUnsupportedAccounts(t *testing.T) {
	c := &Client{} // 被门控的账号不能发起 HTTP 请求。
	for _, a := range []*auth.Auth{nil, {Domain: "https://www.workbuddy.ai"}, {EnterpriseID: "enterprise"}} {
		if _, err := c.GetCompensationStatus(a); err == nil {
			t.Fatal("status query should reject unsupported account")
		}
		if _, err := c.ClaimCompensation(a); err == nil {
			t.Fatal("claim should reject unsupported account")
		}
	}
}
