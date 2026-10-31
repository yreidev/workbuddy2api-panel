// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestAppearanceThemes 主题列表照官方 listCatalog 的请求体，只取 theme，等级照官方对照表换名。
func TestAppearanceThemes(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, desktopAppearanceResources, 200, `{"code":0,"data":{"resources":[`+
		`{"id":"theme-a","kind":"theme","vip_level":"free","series":"craft"},`+
		`{"id":"theme-b","kind":"theme","vip_level":"standard","series":"collection"},`+
		`{"id":"theme-c","kind":"theme","vip_level":"flagship","series":"coop"},`+
		`{"id":"color-a","kind":"color","vip_level":"free"}]}}`)
	themes, err := upClient(f).AppearanceThemes(upAuth())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := []AppearanceTheme{
		{ID: "theme-a", VipLevel: "free", Series: "craft"},
		{ID: "theme-b", VipLevel: "pro", Series: "collection"},
		{ID: "theme-c", VipLevel: "ultimate", Series: "coop"},
	}
	if len(themes) != len(want) {
		t.Fatalf("themes = %+v", themes)
	}
	for i := range want {
		if themes[i] != want[i] {
			t.Fatalf("themes[%d] = %+v, want %+v", i, themes[i], want[i])
		}
	}
	r := f.seen()[0]
	var body map[string]string
	if err := json.Unmarshal(r.body, &body); err != nil {
		t.Fatal(err)
	}
	if body["platform"] != "client" || body["kind"] != "theme" || body["lang"] != "zh" || body["version"] != defaultClientVersionCN {
		t.Fatalf("请求体 = %v", body)
	}
	if r.header.Get("User-Agent") != testDesktopUA || r.header.Get("X-Product") != "SaaS" {
		t.Fatalf("请求头 = %v", r.header)
	}
}

// TestAppearanceThemesErrors 接口失败与响应解析失败都报错。
func TestAppearanceThemesErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"接口失败": {400, `{"code":400,"msg":"no"}`},
		"响应不对": {200, `{"code":0,"data":{"resources":"x"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpFake(t)
			f.on(http.MethodPost, desktopAppearanceResources, tc.status, tc.body)
			if _, err := upClient(f).AppearanceThemes(upAuth()); err == nil {
				t.Fatal("应报错")
			}
		})
	}
}

// TestAccountEdition 版本档照官方：账号类型 ultimate / exclusive 直接取，企业号 ultimate；个人号先看汇总的
// 订阅包，认不出再按逐包列表里的订阅包从 BuddyOne 会员到专业试用依次判，都没有为 free。
func TestAccountEdition(t *testing.T) {
	pkg := func(code string) string { return cyclePkg("包", 100, 50, `"PackageCode":"`+code+`"`) }
	sub := func(code string) string { return `{"Packages":[],"SubscriptionPackageCode":"` + code + `"}` }
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		name    string
		auth    func(*auth.Auth)
		summary string
		packs   []string
		want    string
	}{
		{"无订阅包", nil, "", []string{pkg("TCACA_code_001_PqouKr6QWV")}, "free"},
		{"没有包", nil, "", nil, "free"},
		{"专业试用", nil, "", []string{pkg("TCACA_code_040_mi9rCYg46x")}, "pro_trial"},
		{"专业版", nil, "", []string{pkg("TCACA_code_039_KRcQj7wUat"), pkg("TCACA_code_005_maRGyrHhw1")}, "pro"},
		{"青春版", nil, "", []string{pkg("TCACA_code_002_AkiJS3ZHF5"), pkg("TCACA_code_023_4xbGhMrE6q")}, "youth"},
		{"高级版", nil, "", []string{pkg("TCACA_code_026_BaESVICNoi"), pkg("TCACA_code_023_4xbGhMrE6q")}, "advanced"},
		{"旗舰版", nil, "", []string{pkg("TCACA_code_026_BaESVICNoi"), pkg("TCACA_code_027_0FCGVA6vSa")}, "flagship"},
		{"个人号类型", func(a *auth.Auth) { a.AccountType = str("personal") }, "", []string{pkg("TCACA_code_003_FAnt7lcmRT")}, "pro"},
		{"账号类型 ultimate", func(a *auth.Auth) { a.AccountType = str("ultimate") }, "", nil, "ultimate"},
		{"账号类型 exclusive", func(a *auth.Auth) { a.AccountType = str("exclusive"); a.EnterpriseID = "e1" }, "", nil, "exclusive"},
		{"企业号", func(a *auth.Auth) { a.EnterpriseID = "e1" }, "", nil, "ultimate"},
		{"BuddyOne 会员", nil, "", []string{pkg("TCACA_code_027_0FCGVA6vSa"), pkg("TCACA_code_043_6oIkxT7Z0S")}, "buddyonevip"},
		{"汇总的订阅包优先", nil, sub("TCACA_code_002_AkiJS3ZHF5"), []string{pkg("TCACA_code_027_0FCGVA6vSa")}, "pro"},
		{"汇总的订阅包是免费包", nil, sub("TCACA_code_001_PqouKr6QWV"), []string{pkg("TCACA_code_026_BaESVICNoi")}, "free"},
		{"汇总的订阅包是体验包", nil, sub("TCACA_code_040_mi9rCYg46x"), nil, "pro_trial"},
		{"汇总的订阅包是 BuddyOne 会员", nil, sub("TCACA_code_043_6oIkxT7Z0S"), nil, "buddyonevip"},
		{"汇总的订阅包认不出看逐包", nil, sub("TCACA_code_999_unknown"), []string{pkg("TCACA_code_023_4xbGhMrE6q")}, "youth"},
		{"汇总没有订阅包看逐包", nil, `{"Packages":[]}`, []string{pkg("TCACA_code_026_BaESVICNoi")}, "advanced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpFake(t)
			f.onResources(tc.summary, tc.packs...)
			a := upAuth()
			if tc.auth != nil {
				tc.auth(a)
			}
			got, err := upClient(f).AccountEdition(a)
			if err != nil || got != tc.want {
				t.Fatalf("edition = %q, err = %v, want %q", got, err, tc.want)
			}
			if tc.auth != nil && tc.packs == nil && tc.want != "free" && len(f.seen()) != 0 {
				t.Fatalf("账号类型或企业号定了版本档，不应查积分包：%d 个请求", len(f.seen()))
			}
		})
	}
}

// TestAccountEditionError 积分包查不到时报错（官方这时不上报皮肤事件）。
func TestAccountEditionError(t *testing.T) {
	f := newUpFake(t)
	f.onResources("")
	f.on(http.MethodPost, resourceFreePath, 400, `{"code":10001,"msg":"no"}`)
	if _, err := upClient(f).AccountEdition(upAuth()); err == nil || !strings.Contains(err.Error(), "no") {
		t.Fatalf("应报错，got %v", err)
	}
}

// TestDesktopAppearanceApplyEvent 皮肤生效事件：字段照 buildSkinApplyPayload，经渲染层上报服务补
// mode LOCAL 和空的 buddyId、buddyName。
func TestDesktopAppearanceApplyEvent(t *testing.T) {
	ev := DesktopAppearanceApplyEvent(AppearanceTheme{ID: "theme-a", VipLevel: "free", Series: "craft"}, "pro")
	want := DesktopEvent{"eventCode": "appearance_skin_apply", "action": "apply", "source": "settings_close",
		"id": "theme-a", "vipLevel": "free", "series": "craft", "type": "pro",
		"mode": "LOCAL", "buddyId": "", "buddyName": ""}
	if len(ev) != len(want) {
		t.Fatalf("ev = %v", ev)
	}
	for k, v := range want {
		if ev[k] != v {
			t.Fatalf("%s = %v，应为 %v", k, ev[k], v)
		}
	}
}
