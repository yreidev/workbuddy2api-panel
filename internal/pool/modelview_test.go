package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// ---------------------------------------------------------------------------
// ModelLockView：把「哪些模型不能用、锁了几个号、还要锁多久」聚合成一张全清单
// （ModelBlocked 是单模型按请求回答，本视图是全清单，口径必须与选号一致）
// ---------------------------------------------------------------------------

// lockViewPool 造一个含两个 cn 账号的池，便于手搓模型级冷却。
func lockViewPool(t *testing.T) *Pool {
	t.Helper()
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	return p
}

// setModelCooldown 直接写模型级冷却（与 modelblocked_test.go 同法）。
func setModelCooldown(p *Pool, uid, model string, until time.Time, reason string, auditOnly bool) {
	p.mu.Lock()
	e := p.byUID[uid]
	if e.modelCooldowns == nil {
		e.modelCooldowns = map[string]modelCooldown{}
	}
	e.modelCooldowns[model] = modelCooldown{Until: until, Reason: reason, AuditOnly: auditOnly}
	p.mu.Unlock()
}

// TestModelLockViewEmpty 没有未过期模型级冷却 → 返回 nil（面板按空态渲染）。
func TestModelLockViewEmpty(t *testing.T) {
	p := lockViewPool(t)
	if rows := p.ModelLockView(); rows != nil {
		t.Fatalf("无冷却时应返回 nil，得到 %+v", rows)
	}
}

// TestModelLockViewPartial 部分号被锁 → partial，且带出可选/总数、最早解锁与原因。
func TestModelLockViewPartial(t *testing.T) {
	p := lockViewPool(t)
	setModelCooldown(p, "u1", "glm-5.3", time.Now().Add(time.Hour), "6004 model rate limit", false)

	rows := p.ModelLockView()
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %+v", rows)
	}
	r := rows[0]
	if r.Model != "glm-5.3" || r.Realm != "cn" {
		t.Errorf("model/realm = %q/%q want glm-5.3/cn", r.Model, r.Realm)
	}
	if r.Total != 2 || r.Locked != 1 || r.Servable != 1 {
		t.Errorf("total/locked/servable = %d/%d/%d want 2/1/1", r.Total, r.Locked, r.Servable)
	}
	if r.State != "partial" {
		t.Errorf("state = %q want partial", r.State)
	}
	if r.Reason != "6004 model rate limit" {
		t.Errorf("reason = %q want 6004 model rate limit", r.Reason)
	}
	if d := time.Until(r.UnlockAt); d < 50*time.Minute || d > 70*time.Minute {
		t.Errorf("unlock_at 应在约 1h 后，得到 %v", d)
	}
	if !r.FullyUnlockAt.Equal(r.UnlockAt) {
		t.Errorf("只有一个号被锁时 fully_unlock_at 应等于 unlock_at")
	}
}

// TestModelLockViewLocked 全部参与选号的号都被该模型挡住 → locked（与 ModelBlocked 同义），
// 最早解锁取最小 Until、全池解锁取最大 Until，原因取最早解锁账号的。
func TestModelLockViewLocked(t *testing.T) {
	p := lockViewPool(t)
	base := time.Now()
	setModelCooldown(p, "u1", "glm-5.3", base.Add(2*time.Hour), "reason-late", false)
	setModelCooldown(p, "u2", "glm-5.3", base.Add(time.Hour), "reason-early", false)

	rows := p.ModelLockView()
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %+v", rows)
	}
	r := rows[0]
	if r.State != "locked" {
		t.Errorf("state = %q want locked", r.State)
	}
	if r.Servable != 0 || r.Locked != 2 || r.Total != 2 {
		t.Errorf("servable/locked/total = %d/%d/%d want 0/2/2", r.Servable, r.Locked, r.Total)
	}
	if r.Reason != "reason-early" {
		t.Errorf("reason 应取最早解锁账号的，得到 %q", r.Reason)
	}
	if d := time.Until(r.UnlockAt); d < 50*time.Minute || d > 70*time.Minute {
		t.Errorf("unlock_at 应约 1h 后，得到 %v", d)
	}
	if d := time.Until(r.FullyUnlockAt); d < 110*time.Minute || d > 130*time.Minute {
		t.Errorf("fully_unlock_at 应约 2h 后，得到 %v", d)
	}
}

// TestModelLockViewStarved 此刻没号能服务、但不是模型冷却造成的（账号级冷却）→ starved，
// 与 locked 区分开：这种情况换模型没用，等一下就好。
func TestModelLockViewStarved(t *testing.T) {
	p := lockViewPool(t)
	setModelCooldown(p, "u1", "glm-5.3", time.Now().Add(time.Hour), "6004 model rate limit", false)
	p.Cooldown("u2", CoolHard, time.Hour, "14018 credits exhausted")

	rows := p.ModelLockView()
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %+v", rows)
	}
	r := rows[0]
	if r.Servable != 0 || r.Locked != 1 || r.Total != 2 {
		t.Errorf("servable/locked/total = %d/%d/%d want 0/1/2", r.Servable, r.Locked, r.Total)
	}
	if r.State != "starved" {
		t.Errorf("state = %q want starved（模型没全锁，只是此刻没号）", r.State)
	}
}

// TestModelLockViewSkipsAuditOnly AuditOnly 台账只审计不拦路由 → 既不产生行，
// 也不计入 locked（口径与 modelCooled 一致，否则会把其实能调的模型报成不可用）。
func TestModelLockViewSkipsAuditOnly(t *testing.T) {
	p := lockViewPool(t)
	setModelCooldown(p, "u1", "glm-5.3", time.Now().Add(time.Hour), "audit", true)
	if rows := p.ModelLockView(); rows != nil {
		t.Fatalf("AuditOnly 条目不应产生行：%+v", rows)
	}
}

// TestModelLockViewSkipsExpired 已过期的冷却不算锁。
func TestModelLockViewSkipsExpired(t *testing.T) {
	p := lockViewPool(t)
	setModelCooldown(p, "u1", "glm-5.3", time.Now().Add(-time.Minute), "6004", false)
	if rows := p.ModelLockView(); rows != nil {
		t.Fatalf("已过期冷却不应产生行：%+v", rows)
	}
}

// TestModelLockViewSkipsDisabledAndPaused 禁用号/暂停号的不可用与模型无关：
// 既不计入分母 Total，也不该被算成「还能服务的证明」。
func TestModelLockViewSkipsDisabledAndPaused(t *testing.T) {
	p := lockViewPool(t)
	setModelCooldown(p, "u1", "glm-5.3", time.Now().Add(time.Hour), "6004", false)
	p.mu.Lock()
	p.byUID["u2"].disabled = true
	p.mu.Unlock()

	rows := p.ModelLockView()
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %+v", rows)
	}
	if rows[0].Total != 1 {
		t.Errorf("total = %d want 1（禁用号不计入分母）", rows[0].Total)
	}
	if rows[0].State != "locked" {
		t.Errorf("state = %q want locked（唯一参与选号的号被锁）", rows[0].State)
	}

	p.mu.Lock()
	p.byUID["u2"].disabled = false
	p.byUID["u2"].paused = true
	p.mu.Unlock()

	rows = p.ModelLockView()
	if len(rows) != 1 || rows[0].Total != 1 {
		t.Errorf("暂停号同样不计入分母：%+v", rows)
	}
}

// TestModelLockViewRealmIsolation 同名模型在 cn/global 各自独立计数与原因。
func TestModelLockViewRealmIsolation(t *testing.T) {
	p := realmPool(t)
	setModelCooldown(p, "cn1", "glm-5.3", time.Now().Add(time.Hour), "6004 cn", false)
	setModelCooldown(p, "g1", "glm-5.3", time.Now().Add(time.Hour), "6004 global", false)

	rows := p.ModelLockView()
	if len(rows) != 2 {
		t.Fatalf("同名模型在两个域应各出一行，得到 %+v", rows)
	}
	byRealm := map[string]ModelLockRow{}
	for _, r := range rows {
		byRealm[r.Realm] = r
	}
	cn, ok := byRealm["cn"]
	if !ok || cn.Total != 2 || cn.Locked != 1 || cn.Reason != "6004 cn" {
		t.Errorf("cn 行 = %+v want total 2 locked 1 reason \"6004 cn\"", cn)
	}
	g, ok := byRealm["global"]
	if !ok || g.Total != 2 || g.Locked != 1 || g.Reason != "6004 global" {
		t.Errorf("global 行 = %+v want total 2 locked 1 reason \"6004 global\"", g)
	}
}

// TestModelLockViewSortsLockedFirst 整池不可用排在部分限流前面，方便一眼看到最该处理的。
func TestModelLockViewSortsLockedFirst(t *testing.T) {
	p := lockViewPool(t)
	base := time.Now()
	setModelCooldown(p, "u1", "glm-5.3", base.Add(time.Hour), "6004", false)
	setModelCooldown(p, "u2", "glm-5.3", base.Add(time.Hour), "6004", false)
	setModelCooldown(p, "u1", "kimi-k2", base.Add(time.Hour), "6004", false)

	rows := p.ModelLockView()
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %+v", rows)
	}
	if rows[0].State != "locked" || rows[1].State != "partial" {
		t.Errorf("整池不可用应排在部分限流前：%+v", rows)
	}
	if rows[0].Model != "glm-5.3" {
		t.Errorf("首行应为 glm-5.3，得到 %q", rows[0].Model)
	}
}

// TestModelLockViewAfterClear 走真实写入/清除通道（而非只手搓 map）：
// BlockModelBackoff 后应出 partial 行，BlockModelClear 后应回到 nil。
func TestModelLockViewAfterClear(t *testing.T) {
	p := lockViewPool(t)
	p.BlockModelBackoff("u1", "glm-5.3", "11102 model [glm-5.3] service info not found")
	rows := p.ModelLockView()
	if len(rows) != 1 || rows[0].State != "partial" {
		t.Fatalf("写入负缓存后应有一行 partial：%+v", rows)
	}
	p.BlockModelClear("u1", "glm-5.3")
	if rows := p.ModelLockView(); rows != nil {
		t.Fatalf("清除后应回到 nil：%+v", rows)
	}
}
