package auth

import "testing"

// TestIsEnterprise 企业版判定：enterpriseId 非空即企业版（纯空白视为非企业）。
// 该判定驱动 scheduler 五类任务与 panel 成长任务/签到入口的门控——企业版在这些
// 端点上被上游一律拒绝（400 code 10001 / 403 growth-only），误判为个人号会导致
// 每日对注定失败的端点空发请求。
func TestIsEnterprise(t *testing.T) {
	cases := []struct {
		name string
		ent  string
		want bool
	}{
		{"个人号（空）", "", false},
		{"纯空白", "   ", false},
		{"企业号", "fn7xvyc51dkw", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Auth{UID: "u1", EnterpriseID: c.ent}
			if got := a.IsEnterprise(); got != c.want {
				t.Errorf("EnterpriseID=%q IsEnterprise()=%v want %v", c.ent, got, c.want)
			}
		})
	}
}

// TestIsEnterpriseAndIsGlobalIndependent 企业标识与 realm 是正交维度：
// 企业号恒为 CN realm，但两者判定互不干扰（门控引用处并列书写）。
func TestIsEnterpriseAndIsGlobalIndependent(t *testing.T) {
	a := &Auth{UID: "u1", EnterpriseID: "ent-1"}
	if !a.IsEnterprise() {
		t.Error("应判为企业版")
	}
	if a.IsGlobal() {
		t.Error("企业号不应被判为 global")
	}
}
