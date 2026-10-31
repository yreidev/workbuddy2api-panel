// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// TestGrowthStreakParsesDays 解析 data.streak.days（probe_active.py 同口径）。
func TestGrowthStreakParsesDays(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamtest.WebSession(w, r) {
			return
		}
		if r.URL.Path != "/activity/growth/streak" {
			t.Errorf("path=%s want /activity/growth/streak", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method=%s want GET", r.Method)
		}
		w.Write([]byte(`{"code":0,"msg":"ok","data":{"streak":{"days":3,"total_rewards":1}}}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}
	days, err := c.GrowthStreak(&auth.Auth{AccessToken: "at", UID: "u1"})
	if err != nil {
		t.Fatalf("GrowthStreak: %v", err)
	}
	if days != 3 {
		t.Errorf("days=%d want 3", days)
	}
}

// TestGrowthStreakDefaultZero 缺 streak/days 字段 → 0（days==0 即自检告警信号）。
func TestGrowthStreakDefaultZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamtest.WebSession(w, r) {
			return
		}
		w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}
	days, err := c.GrowthStreak(&auth.Auth{AccessToken: "at", UID: "u1"})
	if err != nil {
		t.Fatalf("GrowthStreak: %v", err)
	}
	if days != 0 {
		t.Errorf("days=%d want 0（缺字段零值）", days)
	}
}

// TestGrowthStreakServerError HTTP 非 2xx → *Error。
func TestGrowthStreakServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamtest.WebSession(w, r) {
			return
		}
		w.WriteHeader(500)
		w.Write([]byte(`boom`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}
	if _, err := c.GrowthStreak(&auth.Auth{AccessToken: "at", UID: "u1"}); err == nil {
		t.Fatal("want error on 500")
	}
}

// TestGrowthStreakBusinessCode 业务 code 非 0 → 错误（非 2xx 之外的静默失败也要能被观测）。
func TestGrowthStreakBusinessCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamtest.WebSession(w, r) {
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"code":12000,"msg":"internal"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}
	if _, err := c.GrowthStreak(&auth.Auth{AccessToken: "at", UID: "u1"}); err == nil {
		t.Fatal("want error on non-zero business code")
	}
}

// TestGrowthStreakFull 连登完整状态：GET growth 域，解析 streak/makeup_cards/
// redemption_status 全字段。
func TestGrowthStreakFull(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, streakPath, 200, `{"code":0,"data":{
			"streak":{"days":9,"month_total_days":12,"next_tier":"14d","next_tier_remaining":5},
			"makeup_cards":{"balance":2,"max":3},
			"redemption_status":{"tier_7d_status":"redeemed","tier_14d_status":"available","tier_28d_status":"locked",
				"remaining_days":5,"tiers":[{"tier":"7d","days":7,"credit":100,"energy":5,"cards":1,"chances":2}]}
		}}`)
		c := upClient(f)
		st, err := c.GrowthStreakFull(upAuth())
		if err != nil {
			t.Fatalf("GrowthStreakFull: %v", err)
		}
		if st.Streak.Days != 9 || st.Streak.MonthTotalDays != 12 || st.Streak.NextTier != "14d" || st.Streak.NextTierRemaining != 5 {
			t.Errorf("streak = %+v", st.Streak)
		}
		if st.MakeupCards.Balance != 2 || st.MakeupCards.Max != 3 {
			t.Errorf("makeup_cards = %+v", st.MakeupCards)
		}
		if st.RedemptionStatus.Tier7dStatus != "redeemed" || st.RedemptionStatus.Tier14dStatus != "available" ||
			st.RedemptionStatus.Tier28dStatus != "locked" || st.RedemptionStatus.RemainingDays != 5 {
			t.Errorf("redemption_status = %+v", st.RedemptionStatus)
		}
		if len(st.RedemptionStatus.Tiers) != 1 || st.RedemptionStatus.Tiers[0].Chances != 2 {
			t.Errorf("tiers = %+v", st.RedemptionStatus.Tiers)
		}
		r := f.bizSeen()[0]
		if r.method != http.MethodGet || r.path != streakPath {
			t.Errorf("请求 = %s %s", r.method, r.path)
		}
	})

	t.Run("缺字段返回零值", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, streakPath, 200, `{"code":0,"data":{}}`)
		st, err := upClient(f).GrowthStreakFull(upAuth())
		if err != nil || st.Streak.Days != 0 || len(st.RedemptionStatus.Tiers) != 0 {
			t.Fatalf("st=%+v err=%v", st, err)
		}
	})

	t.Run("HTTP 400", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, streakPath, 400, `{"code":400,"msg":"boom"}`)
		if _, err := upClient(f).GrowthStreakFull(upAuth()); err == nil {
			t.Fatal("上游 400 应返回错误")
		}
	})

	t.Run("data 类型不符", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, streakPath, 200, `{"code":0,"data":"nope"}`)
		if _, err := upClient(f).GrowthStreakFull(upAuth()); err == nil {
			t.Fatal("data 类型不符应返回错误")
		}
	})
}

// TestGrowthRedeemTier 兑换连登档位：POST redeem，body 带 tier 与 UUID 形态的
// client_token；403 未解锁返回错误。
func TestGrowthRedeemTier(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, streakRedeemPath, 200, `{"code":0,"msg":"OK"}`)
	c := upClient(f)
	if err := c.GrowthRedeemTier(upAuth(), "14d", 0); err != nil {
		t.Fatalf("GrowthRedeemTier: %v", err)
	}
	r := f.bizSeen()[0]
	if r.method != http.MethodPost || r.path != streakRedeemPath {
		t.Errorf("请求 = %s %s", r.method, r.path)
	}
	var body map[string]any
	if err := json.Unmarshal(r.body, &body); err != nil {
		t.Fatalf("请求体: %v", err)
	}
	if body["tier"] != "14d" {
		t.Errorf("tier = %v", body["tier"])
	}
	token, _ := body["client_token"].(string)
	if !redeemTokenShape.MatchString(token) || !strings.HasPrefix(token, "redeem-14d-") {
		t.Errorf("client_token 应形如 redeem-<tier>-<uuid>: %q", token)
	}

	f2 := newUpFake(t)
	f2.on(http.MethodPost, streakRedeemPath, 403, `{"code":403,"msg":"连续登录天数不足"}`)
	err := upClient(f2).GrowthRedeemTier(upAuth(), "28d", 0)
	if err == nil || !strings.Contains(err.Error(), "连续登录天数不足") {
		t.Fatalf("应返回未解锁错误，got %v", err)
	}
}

// TestLotteryChances 抽奖次数：GET summary 解析 chances；缺字段为 0；失败返回错误。
func TestLotteryChances(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, lotterySummaryPath, 200, `{"code":0,"data":{"chances":3,"module":{"enabled":true}}}`)
		chances, err := upClient(f).LotteryChances(upAuth())
		if err != nil || chances != 3 {
			t.Fatalf("chances=%d err=%v，应为 3/nil", chances, err)
		}
		r := f.bizSeen()[0]
		if r.method != http.MethodGet || r.path != lotterySummaryPath {
			t.Errorf("请求 = %s %s", r.method, r.path)
		}
	})

	t.Run("缺字段为零", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, lotterySummaryPath, 200, `{"code":0,"data":{}}`)
		chances, err := upClient(f).LotteryChances(upAuth())
		if err != nil || chances != 0 {
			t.Fatalf("chances=%d err=%v", chances, err)
		}
	})

	t.Run("HTTP 400", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, lotterySummaryPath, 400, `{"code":400,"msg":"boom"}`)
		if _, err := upClient(f).LotteryChances(upAuth()); err == nil {
			t.Fatal("上游 400 应返回错误")
		}
	})

	t.Run("data 类型不符", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, lotterySummaryPath, 200, `{"code":0,"data":[]}`)
		if _, err := upClient(f).LotteryChances(upAuth()); err == nil {
			t.Fatal("data 类型不符应返回错误")
		}
	})
}

// TestLotteryDraw 抽奖：POST draw，body 带 client_token，返回原始奖品载荷。
func TestLotteryDraw(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, lotteryDrawPath, 200, `{"code":0,"data":{"prize":{"name":"50 积分","credit":50}}}`)
	raw, err := upClient(f).LotteryDraw(upAuth())
	if err != nil {
		t.Fatalf("LotteryDraw: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("返回载荷: %v", err)
	}
	if prize, _ := data["prize"].(map[string]any); prize["name"] != "50 积分" {
		t.Errorf("prize = %v", data["prize"])
	}
	r := f.bizSeen()[0]
	if r.method != http.MethodPost || r.path != lotteryDrawPath {
		t.Errorf("请求 = %s %s", r.method, r.path)
	}
	var body map[string]any
	if err := json.Unmarshal(r.body, &body); err != nil {
		t.Fatalf("请求体: %v", err)
	}
	if token, _ := body["client_token"].(string); !drawTokenShape.MatchString(token) {
		t.Errorf("client_token 应形如 draw-<uuid>: %q", token)
	}

	f2 := newUpFake(t)
	f2.on(http.MethodPost, lotteryDrawPath, 400, `{"code":400,"msg":"no chances"}`)
	if _, err := upClient(f2).LotteryDraw(upAuth()); err == nil || !strings.Contains(err.Error(), "no chances") {
		t.Fatalf("应返回错误，got %v", err)
	}
}

// TestClientToken clientToken 按网页写法 `${prefix}-${uuid}` 生成，UUID 部分保持
// 8-4-4-4-12 小写 hex 且每次不同；无前缀时（school.go）仍返回裸 UUID。
func TestClientToken(t *testing.T) {
	first := clientToken("draw")
	second := clientToken("draw")
	if !drawTokenShape.MatchString(first) {
		t.Errorf("token 形态应为 draw-<uuid>: %q", first)
	}
	if first == second {
		t.Errorf("两次生成不应相同: %q", first)
	}
	if redeem := clientToken("redeem-7d"); !strings.HasPrefix(redeem, "redeem-7d-") {
		t.Errorf("兑换令牌应带 redeem-<tier>- 前缀: %q", redeem)
	}
	if bare := clientToken(); !uuidV4Shape.MatchString(bare) {
		t.Errorf("无前缀应返回裸 UUID: %q", bare)
	}
}

// uuidV4Shape 与 clientToken 的 8-4-4-4-12 hex 形态一致（版本位不强制 4）。
var uuidV4Shape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// drawTokenShape / redeemTokenShape 网页抽奖、兑换的 client_token 形态。
var (
	drawTokenShape   = regexp.MustCompile(`^draw-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	redeemTokenShape = regexp.MustCompile(`^redeem-[0-9]+d-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// TestStreakBusinessErrors 兑换/抽奖的错误语义：业务 code≠0 带 msg；非 JSON
// 由 doJSON 报解析错误。
func TestStreakBusinessErrors(t *testing.T) {
	t.Run("兑换业务码非 0", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, streakRedeemPath, 200, `{"code":9,"msg":"redeem boom"}`)
		err := upClient(f).GrowthRedeemTier(upAuth(), "7d", 0)
		if err == nil || !strings.Contains(err.Error(), "redeem boom") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("兑换非 JSON", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, streakRedeemPath, 200, `oops`)
		if err := upClient(f).GrowthRedeemTier(upAuth(), "7d", 0); err == nil {
			t.Fatal("非 JSON 应返回错误")
		}
	})
	t.Run("抽奖次数业务码非 0", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, lotterySummaryPath, 200, `{"code":9,"msg":"summary boom"}`)
		if _, err := upClient(f).LotteryChances(upAuth()); err == nil || !strings.Contains(err.Error(), "summary boom") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("抽奖非 JSON", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, lotteryDrawPath, 200, `oops`)
		if _, err := upClient(f).LotteryDraw(upAuth()); err == nil {
			t.Fatal("非 JSON 应返回错误")
		}
	})
}
