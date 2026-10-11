package pool

import (
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestCountsPausedSeparate 暂停号在两种口径下的计数（issue #125）。
//
// CountsDetailed 把 paused 并入 disabled —— 那是 /status 的「不可用」口径，对监控与
// 脚本是稳定契约；CountsDetailedWithPaused 则分开返回，供面板回答「禁用几个、
// 暂停几个」。两个口径必须**同时**成立：只满足一个，另一个就会失真（面板把暂停显示
// 成"已禁用"，或 /status 少报不可用账号）。
func TestCountsPausedSeparate(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"}) // 健康
	p.Add(&auth.Auth{UID: "u2"}) // 暂停选号
	p.Add(&auth.Auth{UID: "u3"}) // 真禁用
	if !p.Pause("u2") {
		t.Fatal("Pause 对存在的账号应返回 true")
	}
	p.Disable("u3", "手工禁用")

	// --- 合并口径（/status 契约）：暂停计入 disabled ---
	total, healthy, _, disabled, _ := p.CountsDetailed()
	if total != 3 {
		t.Fatalf("total=%d want 3", total)
	}
	if healthy != 1 {
		t.Errorf("healthy=%d want 1（只有 u1 参与选号）", healthy)
	}
	if disabled != 2 {
		t.Errorf("CountsDetailed.disabled=%d want 2（暂停 + 禁用，即 /status 的不可用口径）", disabled)
	}

	// --- 分开口径（面板概况）：禁用与暂停各自计数 ---
	t2, h2, _, dis, pz, _ := p.CountsDetailedWithPaused()
	if t2 != total || h2 != healthy {
		t.Errorf("两种口径的 total/healthy 必须一致：WithPaused=(%d,%d) Counts=(%d,%d)",
			t2, h2, total, healthy)
	}
	if dis != 1 {
		t.Errorf("WithPaused.disabled=%d want 1（只有真正禁用的 u3）", dis)
	}
	if pz != 1 {
		t.Errorf("WithPaused.paused=%d want 1（只有 u2）", pz)
	}
	if dis+pz != disabled {
		t.Errorf("拆分后必须仍能还原合并口径：%d+%d != %d", dis, pz, disabled)
	}

	// --- 按域分组（/status 的 realm 建模）同样保持合并口径 ---
	// 空 realm = 不加谓词，因此必须与 CountsDetailed 逐项一致；不能用面板的拆分口径
	// 顺手改掉它。
	totR, _, _, disR, _ := p.CountsDetailedForRealm("")
	if totR != total || disR != disabled {
		t.Errorf("CountsDetailedForRealm(\"\") 应与 CountsDetailed 同口径：(%d,%d) vs (%d,%d)",
			totR, disR, total, disabled)
	}
}
