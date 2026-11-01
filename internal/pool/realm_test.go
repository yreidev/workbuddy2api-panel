// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package pool

import (
	"reflect"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// realmPool 构造一个含 cn/global 账号的池，并确保 globalEnabled 开关开启（缺省）。
func realmPool(t *testing.T) *Pool {
	t.Helper()
	withNoPickGap(t)
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	p := New("")
	// CN 账号（显式 cn realm 或空 realm+cn domain 均可）→ Realm()=="cn"
	p.Add(&auth.Auth{UID: "cn1", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "cn2", Domain: ""}) // 空 domain → cn
	// global 账号 → Realm()=="global"
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"})
	p.Add(&auth.Auth{UID: "g2", Domain: "workbuddy.ai"})
	return p
}

// realmOf 直接读账号 realm（等价 e.a.Realm()）。
func realmOf(a *auth.Auth) string { return a.Realm() }

func TestAvailableUIDsForRealm(t *testing.T) {
	p := realmPool(t)

	// 全部 healthy → cn 集合只有 cn1/cn2，global 集合只有 g1/g2。
	got := p.AvailableUIDsForRealm("cn")
	want := []string{"cn1", "cn2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailableUIDsForRealm(cn)=%v want %v", got, want)
	}
	got = p.AvailableUIDsForRealm("global")
	want = []string{"g1", "g2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailableUIDsForRealm(global)=%v want %v", got, want)
	}

	// realm=="" 退化为现状（等价 AvailableUIDs：全部 healthy）。
	got = p.AvailableUIDsForRealm("")
	want = []string{"cn1", "cn2", "g1", "g2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailableUIDsForRealm()=%v want %v", got, want)
	}
}

func TestAvailableUIDsForModelRealm(t *testing.T) {
	p := realmPool(t)
	// 6004 模型冷却只落在 cn-1 上 → AvailableUIDsForModelRealm("glm-5.2","cn") 排除它，
	// 但 cn 集合至少还保底 cn-2；global 集合不受 cn 冷却影响。
	p.CooldownSoftForModel("cn1", 10*time.Minute, time.Now().Add(30*time.Minute), "glm-5.2", "model 6004")

	got := p.AvailableUIDsForModelRealm("glm-5.2", "cn")
	if len(got) != 1 || got[0] != "cn2" {
		t.Errorf("AvailableUIDsForModelRealm(glm-5.2,cn)=%v want [cn2]", got)
	}
	got = p.AvailableUIDsForModelRealm("glm-5.2", "global")
	want := []string{"g1", "g2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailableUIDsForModelRealm(glm-5.2,global)=%v want %v", got, want)
	}
	// realm=="" → 等价 AvailableUIDsForModel（模型豁免照常：cn1 仍被该模型冷却排除）。
	got = p.AvailableUIDsForModelRealm("glm-5.2", "")
	want = []string{"cn2", "g1", "g2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailableUIDsForModelRealm(glm-5.2)=%v want %v", got, want)
	}
}

func TestWeightedAvailableUIDsForModelRealm(t *testing.T) {
	p := realmPool(t)
	now := time.Now()
	p.SetCreditsDetailed("cn2", 100, 100, 100, now.Add(time.Hour), 100)

	got := p.WeightedAvailableUIDsForModelRealm("glm-5.2", "cn", nil)
	want := []string{"cn1", "cn2", "cn2", "cn2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("weighted cn candidates=%v want %v", got, want)
	}

	// 在途满载账号不进入候选，快过期权重不得突破现有并发上限。
	p.SetMaxInFlight(1)
	if !p.Acquire("cn2") {
		t.Fatal("failed to acquire cn2")
	}
	got = p.WeightedAvailableUIDsForModelRealm("glm-5.2", "cn", nil)
	want = []string{"cn1"}
	p.Release("cn2")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("in-flight-full weighted candidates=%v want %v", got, want)
	}

	// 关闭总开关后逐账号只出现一次，旧行为完全恢复。
	p.SetPreferExpiring(false)
	got = p.WeightedAvailableUIDsForModelRealm("glm-5.2", "cn", nil)
	want = []string{"cn1", "cn2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("disabled weighted cn candidates=%v want %v", got, want)
	}
}

func TestPickExcludingForRealm(t *testing.T) {
	p := realmPool(t)
	// realm=cn → 只从 cn 集合选。
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		a := p.PickExcludingForRealm(nil, "", "cn")
		if a == nil {
			t.Fatal("PickExcludingForRealm(cn) returned nil")
		}
		seen[a.UID] = true
		if realmOf(a) != "cn" {
			t.Fatalf("PickExcludingForRealm(cn) returned global account %s", a.UID)
		}
	}
	// global 同样只从 global 集合。
	for i := 0; i < 50; i++ {
		a := p.PickExcludingForRealm(nil, "", "global")
		if a == nil {
			t.Fatal("PickExcludingForRealm(global) returned nil")
		}
		if realmOf(a) != "global" {
			t.Fatalf("PickExcludingForRealm(global) returned cn account %s", a.UID)
		}
	}
	// realm=="" → 退化现状：所有 healthy 都能选。
	for i := 0; i < 50; i++ {
		a := p.PickExcludingForRealm(nil, "", "")
		if a == nil {
			t.Fatal("PickExcludingForRealm() returned nil")
		}
	}
}

func TestPickExcludingForRealmTried(t *testing.T) {
	p := realmPool(t)
	// tried 排除在 realm 过滤之后（维持请求级轮换语义）。
	a := p.PickExcludingForRealm(map[string]bool{"cn1": true}, "", "cn")
	if a == nil {
		t.Fatal("tried cn1 → nil")
	}
	if a.UID == "cn1" {
		t.Fatalf("tried cn1 still picked: %s", a.UID)
	}
	if realmOf(a) != "cn" {
		t.Fatalf("picked non-cn %s", a.UID)
	}
}

// TestModelServesFilter 按目录的选号过滤在各条选号路径上都生效：企业专属模型只派给企业号
// （普通选号、粘性候选、粘性号校验、可用列表），ModelBlocked 不把接不了的号算作「能服务」。
// 拉域目录用的号优先个人号；EnterpriseUIDs 只列没禁用的企业号。
func TestModelServesFilter(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "a-ent", EnterpriseID: "ent-1", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "b-per", Domain: "www.codebuddy.cn"})
	p.SetModelServes(func(uid, realm string, enterprise bool, model string) bool {
		return model != "claude-x" || enterprise
	})

	if a := p.PickExcludingForRealm(nil, "claude-x", "cn"); a == nil || a.UID != "a-ent" {
		t.Fatalf("企业专属模型应选到企业号，got %v", a)
	}
	if a := p.PickExcludingForRealm(map[string]bool{"a-ent": true}, "claude-x", "cn"); a != nil {
		t.Fatalf("企业号试过后不应退到个人号，got %s", a.UID)
	}
	if got := p.WeightedAvailableUIDsForModelRealm("claude-x", "cn", nil); !reflect.DeepEqual(got, []string{"a-ent"}) {
		t.Errorf("粘性候选=%v want [a-ent]", got)
	}
	if got := p.AvailableUIDsForModel("claude-x"); !reflect.DeepEqual(got, []string{"a-ent"}) {
		t.Errorf("AvailableUIDsForModel=%v want [a-ent]", got)
	}
	if a := p.PickByUIDForModel("b-per", "claude-x"); a != nil {
		t.Error("绑在个人号上的会话请求企业专属模型时，粘性号应判为不可用")
	}
	if got := p.WeightedAvailableUIDsForModelRealm("shared", "cn", nil); !reflect.DeepEqual(got, []string{"a-ent", "b-per"}) {
		t.Errorf("普通模型两个号都可选，got %v", got)
	}

	p.BlockModelBackoff("a-ent", "claude-x", "11102 model not found")
	if st := p.ModelBlocked("claude-x"); !st.Blocked || st.Count != 1 {
		t.Errorf("唯一能接的企业号被模型级冷却时应判为模型阻塞，got %+v", st)
	}

	if uid := p.CatalogUIDForRealm("cn"); uid != "b-per" {
		t.Errorf("拉域目录应优先个人号，got %q", uid)
	}
	if got := p.EnterpriseUIDs("cn"); !reflect.DeepEqual(got, []string{"a-ent"}) {
		t.Errorf("EnterpriseUIDs=%v", got)
	}
	p.Disable("a-ent", "test")
	if got := p.EnterpriseUIDs("cn"); len(got) != 0 {
		t.Errorf("禁用的企业号不列出，got %v", got)
	}
}

// TestCatalogUIDForRealm 拉域目录优先用个人号（企业号的名单各自单独记）；只有企业号时退回企业号；
// 该域没有可用账号时为空。
func TestCatalogUIDForRealm(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	if got := p.CatalogUIDForRealm("cn"); got != "" {
		t.Fatalf("空池应返回空串，got %q", got)
	}
	p.Add(&auth.Auth{UID: "a-ent", EnterpriseID: "ent-1", Domain: "www.codebuddy.cn"})
	if got := p.CatalogUIDForRealm("cn"); got != "a-ent" {
		t.Fatalf("只有企业号时用企业号，got %q", got)
	}
	p.Add(&auth.Auth{UID: "b-per", Domain: "www.codebuddy.cn"})
	if got := p.CatalogUIDForRealm("cn"); got != "b-per" {
		t.Fatalf("有个人号时优先个人号，got %q", got)
	}
	p.Disable("b-per", "test")
	if got := p.CatalogUIDForRealm("cn"); got != "a-ent" {
		t.Fatalf("个人号不可用时退回企业号，got %q", got)
	}
	if got := p.CatalogUIDForRealm("global"); got != "" {
		t.Fatalf("没有国际版号时应为空，got %q", got)
	}
}

// TestCheapestWeightedUIDsForModelRealm 新会话分配只留最便宜那一层：实测免费 > 无观测 > 实测收费；
// 条件探索窗口到了切到无观测层；模型为空不分层；快过期虚拟实例权重照常。
func TestCheapestWeightedUIDsForModelRealm(t *testing.T) {
	p := realmPool(t)
	p.SetCostExploreInterval(0)
	p.NoteModelCost("cn1", "glm-5.2", 0, 1000) // 免费
	p.NoteModelCost("cn2", "glm-5.2", 5, 1000) // 收费
	if got := p.CheapestWeightedUIDsForModelRealm("glm-5.2", "cn", nil); !reflect.DeepEqual(got, []string{"cn1"}) {
		t.Fatalf("有免费号时只留免费号，got %v", got)
	}
	// 续用校验不分层：收费号仍算可用，绑在它上面的会话不会被挪走。
	if got := p.WeightedAvailableUIDsForModelRealm("glm-5.2", "cn", nil); !reflect.DeepEqual(got, []string{"cn1", "cn2"}) {
		t.Fatalf("续用校验不该分层，got %v", got)
	}
	// 合池：国际版两个号都没观测，排在收费号之前、免费号之后。
	if got := p.CheapestWeightedUIDsForModelRealm("glm-5.2", "", nil); !reflect.DeepEqual(got, []string{"cn1"}) {
		t.Fatalf("合池时仍只留免费号，got %v", got)
	}
	p.NoteModelCost("cn1", "kimi", 3, 1000)
	if got := p.CheapestWeightedUIDsForModelRealm("kimi", "", nil); !reflect.DeepEqual(got, []string{"cn2", "g1", "g2"}) {
		t.Fatalf("没有免费号时留无观测的号，got %v", got)
	}
	if got := p.CheapestWeightedUIDsForModelRealm("", "cn", nil); !reflect.DeepEqual(got, []string{"cn1", "cn2"}) {
		t.Fatalf("模型为空不分层，got %v", got)
	}
	// 条件探索：免费层垄断、存在无观测的号、窗口已到 → 这一次切到无观测层。
	p.SetCostExploreInterval(time.Hour)
	if got := p.CheapestWeightedUIDsForModelRealm("glm-5.2", "", nil); !reflect.DeepEqual(got, []string{"g1", "g2"}) {
		t.Fatalf("探索窗口到了应切到无观测层，got %v", got)
	}
	if got := p.CheapestWeightedUIDsForModelRealm("glm-5.2", "", nil); !reflect.DeepEqual(got, []string{"cn1"}) {
		t.Fatalf("同一窗口内不再探索，got %v", got)
	}
	// 快过期虚拟实例权重照常。
	p.SetCreditsDetailed("cn1", 100, 100, 100, time.Now().Add(time.Hour), 100)
	if got := p.CheapestWeightedUIDsForModelRealm("glm-5.2", "cn", nil); !reflect.DeepEqual(got, []string{"cn1", "cn1", "cn1"}) {
		t.Fatalf("快过期号应出现 3 次，got %v", got)
	}
}
