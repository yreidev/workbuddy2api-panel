// handler_credit_floor_test.go 积分保底的可观测性与端到端拦截：
// /status 透出 credit_floor；触底账号打收费模型时网关无号可选（503）。
package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestStatusCreditFloor /status 透出 pool 层的 credit_floor 生效值：
// 运维查账时一并看到保底线（结合 accounts[].credits 与 model_costs 即可判定
// 某号为何对某模型不出票）。关闭（0）时也显式透出——缺失会让人误以为没记录。
func TestStatusCreditFloor(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetCreditFloor(100)
	h := NewHandler(Config{Pool: p, Upstream: upstream.New()})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("status not json: %v", err)
	}
	if body["credit_floor"] != float64(100) {
		t.Errorf("credit_floor=%v want 100", body["credit_floor"])
	}
}

// TestStatusCreditFloorZeroOff 未配置（默认 0）同样透出 0（显式写出，零值不省略）。
func TestStatusCreditFloorZeroOff(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: upstream.New()})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("status not json: %v", err)
	}
	if body["credit_floor"] != float64(0) {
		t.Errorf("credit_floor=%v want 0 (off)", body["credit_floor"])
	}
}

// TestCreditFloorBlocksPaidRequest 端到端：触底 + 实测收费模型 → 选号无候选，
// 网关回 503（硬语义：宁 503 不打穿；免费模型照常可用）。
//
// 诚实性约束：本用例的 upstream 是打不通的假上游，floor 开与关最终都回 503——
// 若只断言 503，就无法区分「floor 拦的」与「上游不可达」，断言会被虚假原因满足。
// 真正证明拦截落在选号层的是**对照断言**：对照组（不开 floor）必须能选到 poor，
// 实验组（开 floor）必须在同一条件下选不到号。
func TestCreditFloorBlocksPaidRequest(t *testing.T) {
	const body = `{"model":"paid-model","messages":[{"role":"user","content":"hi"}]}`
	// 对照组：不设 floor，其余逐字相同（同一触底号、同一 tier 2 观测）。
	ctlPool := testPoolWith(&auth.Auth{UID: "poor", AccessToken: "at", ExpiresAt: 9999999999})
	ctlPool.SetCredits("poor", 30, 0)
	ctlPool.NoteModelCost("poor", "paid-model", 2.9, 1000)
	if a := ctlPool.PickExcludingForRealm(nil, "paid-model", ""); a == nil {
		t.Fatal("对照组前置失效：未设 floor 时应选到 poor（否则本用例不构成对照）")
	}

	// 实验组：开 floor 100。
	p := testPoolWith(&auth.Auth{UID: "poor", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetCreditFloor(100)
	p.SetCredits("poor", 30, 0)
	p.NoteModelCost("poor", "paid-model", 2.9, 1000)
	if a := p.PickExcludingForRealm(nil, "paid-model", ""); a != nil {
		t.Fatalf("floor 应在 pool 层拦住触底号，got %v（拦截必须发生在选号层，而非靠上游报错）", a.UID)
	}

	h := NewHandler(Config{Pool: p, Upstream: upstream.New()})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 503 {
		t.Fatalf("code=%d want 503（全池触底 + 收费模型 → 不放行）, body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "no_healthy_account") {
		t.Errorf("body=%s want no_healthy_account", rec.Body)
	}
}

// TestCreditFloorAllowsFreeRequest 端到端：同一触底号打免费模型照常透出（非 503）。
// 这是保底的立身之本——保完了就得还能用。
func TestCreditFloorAllowsFreeRequest(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "poor", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetCreditFloor(100)
	p.SetCredits("poor", 30, 0)
	p.NoteModelCost("poor", "free-model", 0, 1000)

	// 选号层面必须出票（上游转发失败与否不在本用例关心范围，见 TestCreditFloor* 单测）。
	if a := p.PickExcludingForRealm(nil, "free-model", ""); a == nil || a.UID != "poor" {
		t.Fatalf("触底号打免费模型应选到号，got %v", a)
	}
	// pool 层与 handler 层口径一致（避免 handler 绕过 pool 的 floor 判定）。
	if p.PickByUIDForModel("poor", "free-model") == nil {
		t.Error("粘性路径对免费模型不应被 floor 拦")
	}
}

// TestCreditFloorPersistsAcquireRelease 保底拦截不影响在途租约语义：
// 触底 + 收费 → Acquire 仍可占名额（floor 只在选号层拦），释放幂等。
func TestCreditFloorPersistsAcquireRelease(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "poor", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetCreditFloor(100)
	p.SetCredits("poor", 30, 0)
	p.NoteModelCost("poor", "paid-model", 2.9, 1000)
	p.SetMaxInFlight(2)

	if !p.Acquire("poor") {
		t.Fatal("Acquire 应成功（floor 不介入租约语义）")
	}
	p.Release("poor")
	p.Release("poor") // 幂等释放不扣成负数
}
