// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"net/http"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// webRoute 假上游上的一条预设路由（一律回 code=0 与给定 data）。
type webRoute struct {
	method, path, data string
}

// webCall 一次待验证的网页请求：方法、路径、请求体（空串 = 无请求体，否则按前缀匹配）。
type webCall struct {
	method, path, bodyPrefix string
}

// TestWebCallsShape 每个改成网页形状的函数：方法、域（webBase）、路径、请求体。
func TestWebCallsShape(t *testing.T) {
	cases := []struct {
		name   string
		routes []webRoute
		run    func(c *Client, a *auth.Auth) error
		want   []webCall
	}{
		{
			name: "ListTasks 先订阅状态再列表",
			routes: []webRoute{
				{http.MethodGet, tasksSubscribeStatusPath, `{}`},
				{http.MethodGet, tasksListPath, `{"tasks":[]}`},
			},
			run: func(c *Client, a *auth.Auth) error {
				_, err := c.ListTasks(a)
				return err
			},
			want: []webCall{
				{http.MethodGet, tasksSubscribeStatusPath, ""},
				{http.MethodGet, tasksListPath, ""},
			},
		},
		{
			name:   "AcceptTasks",
			routes: []webRoute{{http.MethodPost, tasksAcceptPath, `{}`}},
			run: func(c *Client, a *auth.Auth) error {
				return c.AcceptTasks(a, []string{"a"})
			},
			want: []webCall{{http.MethodPost, tasksAcceptPath, `{"task_codes":["a"]}`}},
		},
		{
			name:   "ClaimReward",
			routes: []webRoute{{http.MethodPost, "/activity/growth/tasks/x/claim", `{"already_claimed":false,"credit":1}`}},
			run: func(c *Client, a *auth.Auth) error {
				_, _, err := c.ClaimReward(a, "x")
				return err
			},
			want: []webCall{{http.MethodPost, "/activity/growth/tasks/x/claim", ""}},
		},
		{
			name:   "TravelStatus",
			routes: []webRoute{{http.MethodGet, travelStatusPath, `{}`}},
			run: func(c *Client, a *auth.Auth) error {
				_, err := c.TravelStatus(a)
				return err
			},
			want: []webCall{{http.MethodGet, travelStatusPath, ""}},
		},
		{
			name:   "TravelDepart",
			routes: []webRoute{{http.MethodPost, travelDepartPath, `{}`}},
			run: func(c *Client, a *auth.Auth) error {
				return c.TravelDepart(a, 4)
			},
			want: []webCall{{http.MethodPost, travelDepartPath, `{"location_id":4}`}},
		},
		{
			name:   "TravelClaim 空对象",
			routes: []webRoute{{http.MethodPost, travelClaimPath, `{"reward_credit":1}`}},
			run: func(c *Client, a *auth.Auth) error {
				_, err := c.TravelClaim(a)
				return err
			},
			want: []webCall{{http.MethodPost, travelClaimPath, `{}`}},
		},
		{
			name:   "BuddyInfo",
			routes: []webRoute{{http.MethodGet, buddyInfoPath, `{"buddy":null}`}},
			run: func(c *Client, a *auth.Auth) error {
				_, err := c.BuddyInfo(a)
				return err
			},
			want: []webCall{{http.MethodGet, buddyInfoPath, ""}},
		},
		{
			name:   "BuddyFirst 无请求体",
			routes: []webRoute{{http.MethodPost, buddyFirstPath, `{}`}},
			run: func(c *Client, a *auth.Auth) error {
				return c.BuddyFirst(a)
			},
			want: []webCall{{http.MethodPost, buddyFirstPath, ""}},
		},
		{
			name:   "BuddyAgreement",
			routes: []webRoute{{http.MethodPost, buddyAgreementPath, `{}`}},
			run: func(c *Client, a *auth.Auth) error {
				return c.BuddyAgreement(a)
			},
			want: []webCall{{http.MethodPost, buddyAgreementPath, `{"agree":true}`}},
		},
		{
			name:   "GrowthStreak",
			routes: []webRoute{{http.MethodGet, streakPath, `{}`}},
			run: func(c *Client, a *auth.Auth) error {
				_, err := c.GrowthStreak(a)
				return err
			},
			want: []webCall{{http.MethodGet, streakPath, ""}},
		},
		{
			name:   "GrowthStreakFull",
			routes: []webRoute{{http.MethodGet, streakPath, `{}`}},
			run: func(c *Client, a *auth.Auth) error {
				_, err := c.GrowthStreakFull(a)
				return err
			},
			want: []webCall{{http.MethodGet, streakPath, ""}},
		},
		{
			name:   "GrowthRedeemTier 带前缀令牌",
			routes: []webRoute{{http.MethodPost, streakRedeemPath, `{}`}},
			run: func(c *Client, a *auth.Auth) error {
				return c.GrowthRedeemTier(a, "7d", 0)
			},
			want: []webCall{{http.MethodPost, streakRedeemPath, `{"client_token":"redeem-7d-`}},
		},
		{
			name:   "LotteryChances",
			routes: []webRoute{{http.MethodGet, lotterySummaryPath, `{"chances":0}`}},
			run: func(c *Client, a *auth.Auth) error {
				_, err := c.LotteryChances(a)
				return err
			},
			want: []webCall{{http.MethodGet, lotterySummaryPath, ""}},
		},
		{
			name:   "LotteryDraw 带前缀令牌",
			routes: []webRoute{{http.MethodPost, lotteryDrawPath, `null`}},
			run: func(c *Client, a *auth.Auth) error {
				_, err := c.LotteryDraw(a)
				return err
			},
			want: []webCall{{http.MethodPost, lotteryDrawPath, `{"client_token":"draw-`}},
		},
		{
			name:   "HeatmapYesterdayMissed",
			routes: []webRoute{{http.MethodGet, "/activity/growth/heatmap", `{"cells":[]}`}},
			run: func(c *Client, a *auth.Auth) error {
				_, err := c.HeatmapYesterdayMissed(a)
				return err
			},
			want: []webCall{{http.MethodGet, "/activity/growth/heatmap", ""}},
		},
		{
			name:   "UseMakeupCard",
			routes: []webRoute{{http.MethodPost, "/activity/growth/makeup-cards/use", `{}`}},
			run: func(c *Client, a *auth.Auth) error {
				return c.UseMakeupCard(a, "2026-10-19")
			},
			want: []webCall{{http.MethodPost, "/activity/growth/makeup-cards/use", `{"target_date":"2026-10-19"}`}},
		},
		{
			name:   "GetCompensationStatus 无请求体",
			routes: []webRoute{{http.MethodPost, "/billing/meter/compensation-status", `{"active":false}`}},
			run: func(c *Client, a *auth.Auth) error {
				_, err := c.GetCompensationStatus(a)
				return err
			},
			want: []webCall{{http.MethodPost, "/billing/meter/compensation-status", ""}},
		},
		{
			name:   "ClaimCompensation 无请求体",
			routes: []webRoute{{http.MethodPost, "/billing/meter/claim-compensation", `null`}},
			run: func(c *Client, a *auth.Auth) error {
				_, err := c.ClaimCompensation(a)
				return err
			},
			want: []webCall{{http.MethodPost, "/billing/meter/claim-compensation", ""}},
		},
		{
			name: "ClaimGift 先查后领，均无请求体",
			routes: []webRoute{
				{http.MethodPost, giftCheckPath, `{"active":true,"claimed":false}`},
				{http.MethodPost, giftClaimPath, `{"credit":1}`},
			},
			run: func(c *Client, a *auth.Auth) error {
				_, err := c.ClaimGift(a)
				return err
			},
			want: []webCall{
				{http.MethodPost, giftCheckPath, ""},
				{http.MethodPost, giftClaimPath, ""},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			for _, rt := range tc.routes {
				f.on(rt.method, rt.path, 200, `{"code":0,"data":`+rt.data+`}`)
			}
			if err := tc.run(upClient(f), upAuth()); err != nil {
				t.Fatalf("调用失败: %v", err)
			}
			reqs := f.bizSeen()
			if len(reqs) != len(tc.want) {
				t.Fatalf("请求数 = %d，应为 %d", len(reqs), len(tc.want))
			}
			for i, want := range tc.want {
				r := reqs[i]
				if r.method != want.method || r.path != want.path {
					t.Errorf("第 %d 个请求 = %s %s，应为 %s %s", i+1, r.method, r.path, want.method, want.path)
				}
				if !strings.HasPrefix(r.url, upBase+"/") {
					t.Errorf("第 %d 个请求 URL = %q，应发往 webBase %s", i+1, r.url, upBase)
				}
				if want.bodyPrefix == "" {
					if len(r.body) != 0 {
						t.Errorf("第 %d 个请求不应带请求体，got %q", i+1, r.body)
					}
				} else if !strings.HasPrefix(string(r.body), want.bodyPrefix) {
					t.Errorf("第 %d 个请求体 = %q，应以 %q 开头", i+1, r.body, want.bodyPrefix)
				}
			}
		})
	}
}

// TestWebHeaders 网页头表：该带的取值、该缺的头；GET 无 Origin、无请求体无 Content-Type；
// 企业号只带 X-Enterprise-Id；配了 UserAgent 时用它。
func TestWebHeaders(t *testing.T) {
	t.Run("POST 无请求体", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, "/activity/growth/tasks/x/claim", 200, `{"code":0,"data":{"already_claimed":false,"credit":1}}`)
		if _, _, err := upClient(f).ClaimReward(upAuth(), "x"); err != nil {
			t.Fatalf("ClaimReward: %v", err)
		}
		r := f.bizSeen()[0]
		for k, want := range map[string]string{
			"Accept":             "application/json, text/plain, */*",
			"X-Client-Platform":  "web",
			"Authorization":      "",
			"Origin":             upBase,
			"Referer":            upBase + webPageGrowthCenter,
			"User-Agent":         browserUA,
			"Accept-Language":    "zh-CN",
			"Sec-CH-UA-Mobile":   "?0",
			"Sec-CH-UA-Platform": `"macOS"`,
			"Sec-Fetch-Site":     "same-origin",
			"Sec-Fetch-Mode":     "cors",
			"Sec-Fetch-Dest":     "empty",
		} {
			if got := r.header.Get(k); got != want {
				t.Errorf("%s = %q，应为 %q", k, got, want)
			}
		}
		if got := r.header.Get("Cookie"); !strings.Contains(got, "session=at") {
			t.Errorf("Cookie = %q，应含登录态 session=at", got)
		}
		if got := r.header.Get("Sec-CH-UA"); got != `"Chromium";v="152", "Not?A_Brand";v="24", "Google Chrome";v="152"` {
			t.Errorf("Sec-CH-UA = %q", got)
		}
		for _, k := range []string{
			"Content-Type", "X-User-Id", "X-Domain", "X-Tenant-Id", "X-Product",
			"X-Request-ID", "X-Requested-With", "X-CodeBuddy-Request",
			"X-Device-Token",
		} {
			if got := r.header.Get(k); got != "" {
				t.Errorf("%s 不应携带，got %q", k, got)
			}
		}
	})

	t.Run("GET 不带 Origin", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, travelStatusPath, 200, `{"code":0,"data":{}}`)
		if _, err := upClient(f).TravelStatus(upAuth()); err != nil {
			t.Fatalf("TravelStatus: %v", err)
		}
		r := f.bizSeen()[0]
		if got := r.header.Get("Origin"); got != "" {
			t.Errorf("GET 不应带 Origin，got %q", got)
		}
		if got := r.header.Get("Referer"); got != upBase+webPageGrowthCenter {
			t.Errorf("Referer = %q", got)
		}
		if got := r.header.Get("Content-Type"); got != "" {
			t.Errorf("GET 不应带 Content-Type，got %q", got)
		}
	})

	t.Run("有请求体带 Content-Type", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, travelDepartPath, 200, `{"code":0,"data":{}}`)
		if err := upClient(f).TravelDepart(upAuth(), 1); err != nil {
			t.Fatalf("TravelDepart: %v", err)
		}
		r := f.bizSeen()[0]
		if got := r.header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q，应为 application/json", got)
		}
		if got := r.header.Get("Origin"); got != upBase {
			t.Errorf("Origin = %q，应为 %q", got, upBase)
		}
	})

	t.Run("企业号只带 X-Enterprise-Id", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, "/activity/growth/tasks/e/claim", 200, `{"code":0,"data":{"already_claimed":false}}`)
		a := &auth.Auth{AccessToken: "at", UID: "u-1", EnterpriseID: "ent-9", Domain: "www.workbuddy.cn"}
		if _, _, err := upClient(f).ClaimReward(a, "e"); err != nil {
			t.Fatalf("ClaimReward: %v", err)
		}
		r := f.bizSeen()[0]
		if got := r.header.Get("X-Enterprise-Id"); got != "ent-9" {
			t.Errorf("X-Enterprise-Id = %q，应为 ent-9", got)
		}
		for _, k := range []string{"X-Tenant-Id", "X-Domain", "X-User-Id"} {
			if got := r.header.Get(k); got != "" {
				t.Errorf("企业号网页形状不应带 %s，got %q", k, got)
			}
		}
	})

	t.Run("配置的 UserAgent 优先", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, "/activity/growth/tasks/x/claim", 200, `{"code":0,"data":{"already_claimed":false}}`)
		c := upClient(f)
		c.UserAgent = "custom-ua/1.0"
		if _, _, err := c.ClaimReward(upAuth(), "x"); err != nil {
			t.Fatalf("ClaimReward: %v", err)
		}
		r := f.bizSeen()[0]
		if got := r.header.Get("User-Agent"); got != "custom-ua/1.0" {
			t.Errorf("User-Agent = %q，应为配置值", got)
		}
		// 配置了 user_agent 后 UA 与内置浏览器指纹对不上，不补 Sec-CH-UA*。
		for _, k := range []string{"Sec-CH-UA", "Sec-CH-UA-Mobile", "Sec-CH-UA-Platform"} {
			if got := r.header.Get(k); got != "" {
				t.Errorf("配置 user_agent 时不应带 %s，got %q", k, got)
			}
		}
	})
}

// TestWebInternational 国际版账号：请求发往 webBase 的国际版域，Origin、Referer 同域。
func TestWebInternational(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, tasksAcceptPath, 200, `{"code":0,"msg":"OK"}`)
	if err := upGlobalClient(f).AcceptTasks(globalAuth(), []string{"a"}); err != nil {
		t.Fatalf("AcceptTasks: %v", err)
	}
	r := f.bizSeen()[0]
	if want := "https://www.workbuddy.ai" + tasksAcceptPath; r.url != want {
		t.Errorf("URL = %q，应为 %q", r.url, want)
	}
	if got := r.header.Get("Origin"); got != "https://www.workbuddy.ai" {
		t.Errorf("Origin = %q，应为国际版域", got)
	}
	if got := r.header.Get("Referer"); got != "https://www.workbuddy.ai"+webPageGrowthCenter {
		t.Errorf("Referer = %q，应为国际版成长中心", got)
	}
}

// TestWebAcceptLanguageGrowth 成长中心接口（含 /v2 路径）照官网拦截器发 zh-CN / en-US，
// 网页和小程序两条路径一样；其余网页请求照旧是浏览器带 q 权重的形状。
func TestWebAcceptLanguageGrowth(t *testing.T) {
	for _, tc := range []struct {
		path     string
		cn, intl string
	}{
		{"/activity/growth/tasks/x/claim", "zh-CN", "en-US"},
		{"/v2/activity/growth/tasks", "zh-CN", "en-US"},
		{"/v2/billing/meter/get-compensation-status", "zh-CN,zh;q=0.9", "en-US,en;q=0.9"},
		{"/profile/growth-center", "zh-CN,zh;q=0.9", "en-US,en;q=0.9"},
	} {
		for _, a := range []*auth.Auth{cnAuth(), globalAuth()} {
			want := tc.cn
			if a.IsGlobal() {
				want = tc.intl
			}
			c := &Client{GlobalEnabled: true}
			web, _ := http.NewRequest(http.MethodGet, c.webBase(a)+tc.path, nil)
			c.webHeaders(web, a, webPageGrowthCenter)
			mp, _ := http.NewRequest(http.MethodGet, c.webBase(a)+tc.path, nil)
			c.webMPHeaders(mp, a)
			for name, r := range map[string]*http.Request{"网页": web, "小程序": mp} {
				if got := r.Header.Get("Accept-Language"); got != want {
					t.Errorf("%s %s global=%v Accept-Language = %q，应为 %q", name, tc.path, a.IsGlobal(), got, want)
				}
			}
		}
	}
}
