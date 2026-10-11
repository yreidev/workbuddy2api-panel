package upstream

import (
	"net/http"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestRealmHasModel 各域模型目录按拉取账号所在域分开记：国内版目录只进 cn，国际版探测只进 global；
// 某次拉取失败不清空上次成功的目录（免得偶发失败把跨域选号退回单域）。
func TestRealmHasModel(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	fail := false
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if fail {
			return jsonResp(500, `boom`), nil
		}
		if r.Header.Get("Authorization") == "Bearer at-global" {
			return jsonResp(200, `{"code":0,"data":{"models":[{"id":"shared"},{"id":"only-global"}],"agents":[{"name":"cli","models":["shared","only-global"]}]}}`), nil
		}
		return jsonResp(200, `{"code":0,"data":{"models":[{"id":"shared"},{"id":"only-cn"}],"agents":[{"name":"cli","models":["shared","only-cn"]}]}}`), nil
	})
	c.GlobalEnabled = true
	c.ChatBaseGlobal = "https://global.example"
	c.BillingBaseGlobal = "https://global.example"
	cn := &auth.Auth{UID: "c", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	gl := &auth.Auth{UID: "g", AccessToken: "at-global", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}

	if c.RealmHasModel("cn", "shared") || c.RealmHasModel("global", "shared") {
		t.Fatal("目录从未拉到时应一律 false")
	}
	if _, err := c.FetchModels(cn); err != nil {
		t.Fatalf("cn 目录: %v", err)
	}
	if names := c.FetchGlobalModels(gl); len(names) == 0 {
		t.Fatal("global 探测应拉到名单")
	}
	for _, tc := range []struct {
		realm, model string
		want         bool
	}{
		{"cn", "shared", true}, {"cn", "only-cn", true}, {"cn", "only-global", false},
		{"global", "shared", true}, {"global", "only-global", true}, {"global", "only-cn", false},
		{"", "only-cn", true}, // 空域按国内版
	} {
		if got := c.RealmHasModel(tc.realm, tc.model); got != tc.want {
			t.Errorf("RealmHasModel(%q,%q)=%v want %v", tc.realm, tc.model, got, tc.want)
		}
	}

	fail = true
	if _, err := c.FetchModels(cn); err == nil {
		t.Fatal("假上游 500 时 FetchModels 应报错")
	}
	if !c.RealmHasModel("cn", "only-cn") {
		t.Error("拉取失败不应清空上次成功的目录")
	}
	var nilClient *Client
	if nilClient.RealmHasModel("cn", "shared") {
		t.Error("nil Client 应返回 false")
	}
}
