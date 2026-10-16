package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// 批量领取隔离单号失败，包含暂停账号，过滤国际/企业/禁用账号并刷新权威余额。
func TestCompensationBulkClaim(t *testing.T) {
	var mu sync.Mutex
	claims := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if uid == "global" || uid == "enterprise" || uid == "disabled" {
			t.Errorf("unsupported account contacted upstream: %s", uid)
		}
		if uid == "failed" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"code":10001,"msg":"session expired"}`))
			return
		}
		switch r.URL.Path {
		case "/billing/meter/compensation-status":
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"active": uid != "inactive", "claimed": uid == "already", "credit_num": 500,
			}})
		case "/billing/meter/claim-compensation":
			mu.Lock()
			claims[uid]++
			mu.Unlock()
			w.Write([]byte(`{"code":0,"data":{}}`))
		case "/v2/billing/meter/get-user-resource":
			w.Write([]byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CapacitySize":800,"CapacityRemain":700}]}}}}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	accounts := pool.New("")
	for _, uid := range []string{"ready", "paused", "already", "inactive", "failed", "global", "enterprise", "disabled"} {
		a := &auth.Auth{UID: uid, AccessToken: uid}
		if uid == "global" {
			a.Domain = "https://www.workbuddy.ai"
		}
		if uid == "enterprise" {
			a.EnterpriseID = "enterprise"
		}
		accounts.Add(a)
	}
	accounts.Pause("paused")
	accounts.Disable("disabled", "test")
	accounts.Cooldown("ready", pool.CoolHard, time.Hour, "credits exhausted")
	p := New(Config{Pool: accounts, Upstream: &upstream.Client{HTTP: srv.Client(), BillingBaseCN: srv.URL}, APIKey: "key"})
	request := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer key")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	// 状态查询不能产生领取副作用。
	request(http.MethodGet, "/panel/api/compensation")
	if len(claims) != 0 {
		t.Fatal("read-only query claimed compensation")
	}
	w := request(http.MethodPost, "/panel/api/compensation/claim_all")
	var response struct {
		Accounts []compensationAccountItem `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Accounts) != 5 {
		t.Fatalf("accounts=%d want 5", len(response.Accounts))
	}
	if len(claims) != 2 || claims["ready"] != 1 || claims["paused"] != 1 {
		t.Fatalf("claims=%v", claims)
	}
	for _, it := range response.Accounts {
		if it.UID == "failed" && !strings.Contains(it.Error, "session expired") {
			t.Errorf("individual failure lost: %+v", it)
		}
		if it.UID == "ready" || it.UID == "paused" {
			if !it.NewlyClaimed || it.Credit != 500 || !it.Status.Claimed || it.BalanceError != "" {
				t.Errorf("claim result: %+v", it)
			}
		}
	}
	st, _ := accounts.Status("ready")
	if st.Credits != 700 || st.Cooling {
		t.Errorf("balance/cooldown not refreshed: %+v", st)
	}
	paused, _ := accounts.Status("paused")
	if !paused.Paused {
		t.Error("claim must preserve paused selection")
	}
	// 单号与其他任务入口共用账号锁，冲突不能请求上游。
	if !p.tryLockAccount("ready") {
		t.Fatal("lock failed")
	}
	defer p.unlockAccount("ready")
	r := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/ready/compensation/claim", nil)
	r.Header.Set("Authorization", "Bearer key")
	busy := httptest.NewRecorder()
	p.ServeHTTP(busy, r)
	if busy.Code != http.StatusConflict || claims["ready"] != 1 {
		t.Errorf("busy response=%d claims=%v", busy.Code, claims)
	}
}

func TestCompensationEndpointsRequireAuth(t *testing.T) {
	p := newTestPanel()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/panel/api/compensation"},
		{http.MethodPost, "/panel/api/compensation/claim_all"},
		{http.MethodPost, "/panel/api/accounts/u1/compensation/claim"},
	} {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s auth status=%d", tc.path, w.Code)
		}
	}
}

// 领取已成功但查余额失败时，HTTP 仍为成功，避免用户重复领取。
func TestCompensationSingleClaimWithBalanceFailure(t *testing.T) {
	claims := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/billing/meter/compensation-status":
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"active": true, "claimed": claims > 0, "credit_num": 500,
			}})
		case "/billing/meter/claim-compensation":
			claims++
			w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"code":10001,"msg":"balance unavailable"}`))
		}
	}))
	defer srv.Close()
	accounts := pool.New("")
	accounts.Add(&auth.Auth{UID: "u1", AccessToken: "at"})
	p := New(Config{Pool: accounts, Upstream: &upstream.Client{HTTP: srv.Client(), BillingBaseCN: srv.URL}})
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/panel/api/accounts/u1/compensation/claim", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var result compensationAccountItem
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Status.Claimed || result.Error != "" || result.NewlyClaimed != (i == 0) {
			t.Errorf("claim %d: %+v", i, result)
		}
		if i == 0 && !strings.Contains(result.BalanceError, "balance unavailable") {
			t.Errorf("balance warning missing: %+v", result)
		}
	}
	if claims != 1 {
		t.Errorf("claimed %d times", claims)
	}
}
