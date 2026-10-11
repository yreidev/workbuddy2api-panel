package scheduler

import (
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

// recorderUpstream 记录全部被请求的路径，并对所有端点返回成功信封。
// 用「路径是否出现」断言门控是否生效，比逐端点计数器更能覆盖新增调用。
type recorderUpstream struct {
	mu    sync.Mutex
	paths []string
}

func (r *recorderUpstream) record(p string) {
	r.mu.Lock()
	r.paths = append(r.paths, p)
	r.mu.Unlock()
}

func (r *recorderUpstream) has(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.paths {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

func (r *recorderUpstream) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.paths))
	copy(out, r.paths)
	return out
}

func (r *recorderUpstream) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.record(req.URL.Path)
		switch {
		case strings.HasSuffix(req.URL.Path, "/get-enterprise-user-usage"):
			// 企业口径：credit = 本周期已用，limitNum = 分配给本账号的额度。
			w.Write([]byte(`{"code":0,"msg":"OK","data":{"credit":812.45,"limitNum":2000,` +
				`"cycleStartTime":"2026-09-27 00:00:00","cycleEndTime":"2099-10-26 23:59:59",` +
				`"cycleResetTime":"2099-10-27 00:00:00"}}`))
		case strings.HasSuffix(req.URL.Path, "/get-user-resource"):
			w.Write([]byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":` +
				`[{"CycleCapacitySize":1000,"CycleCapacityRemain":900,"CycleCapacityUsed":100}]}}}}`))
		default:
			w.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
		}
	}))
}

// enterpriseTestScheduler 构造只含一个账号的调度器；enterprise=true 时该号带
// enterpriseId（企业版），否则是个人号（对照组）。
func enterpriseTestScheduler(t *testing.T, enterprise bool) (*Scheduler, *recorderUpstream) {
	t.Helper()
	r := &recorderUpstream{}
	srv := r.server()
	t.Cleanup(srv.Close)

	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999}
	if enterprise {
		a.EnterpriseID = "ent-1"
	}
	p := pool.New("")
	p.Add(a)

	up := &upstream.Client{
		HTTP:          srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
		WebBaseCN:     srv.URL,
	}
	s := New(Config{Pool: p, Upstream: up})
	return s, r
}

// TestEnterpriseGateSkipsGrowthTasks 企业版无个人成长体系：五类任务必须一个上游调用都不发。
// 每个子用例都配一组个人号对照（同构造、只差 enterpriseId），证明"没发请求"是门控
// 生效而不是构造有误——企业版门控与既有 IsGlobal/D4 门控同源。
func TestEnterpriseGateSkipsGrowthTasks(t *testing.T) {
	// 把夜猫子的窗口判定钉在窗口内：守卫先于账号循环，不钉住时钟则该用例的
	// 判别力取决于运行环境的墙钟（本机白天跑 = 什么都没测）。钉住后五个用例
	// 在任何环境、任何时刻都真正执行。
	restoreClock := nowForNightWindow
	nowForNightWindow = func() time.Time { return time.Date(2026, 10, 7, 23, 30, 0, 0, time.Local) }
	t.Cleanup(func() { nowForNightWindow = restoreClock })

	cases := []struct {
		name string
		run  func(*Scheduler)
		// endpoint 是该任务在**个人号**上必然发起的上游路径，用来证明对照组
		// 确实有动作（否则"企业号没发请求"可能只是构造有误）。
		endpoint string
	}{
		{"签到", func(s *Scheduler) { s.RunCheckinNow() }, "/daily-checkin"},
		{"活跃上报", func(s *Scheduler) { s.RunActivityNow() }, "/v2/report"},
		{"猫猫旅行", func(s *Scheduler) { s.RunTravelNow() }, "/activity/growth/buddy/info"},
		// 夜猫子第一跳不是 heatmap：RunBlackcatNow → BlackcatNeed → ListTasks，
		// 即 /v2/activity/growth/tasks（问"还差几次"）。mock 返回空任务表 ⇒ need=0
		// ⇒ 个人号到此为止，只发这一条。
		{"夜猫子", func(s *Scheduler) { s.RunBlackcatNow() }, "/v2/activity/growth/tasks"},
		{"连登管家", func(s *Scheduler) { s.RunStreakBonusNow() }, "/activity/growth/streak"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sEnt, rEnt := enterpriseTestScheduler(t, true)
			c.run(sEnt)
			if rEnt.has(c.endpoint) {
				t.Errorf("企业号不应发起 %s，实际请求=%v", c.endpoint, rEnt.snapshot())
			}

			sPer, rPer := enterpriseTestScheduler(t, false)
			c.run(sPer)
			if !rPer.has(c.endpoint) {
				t.Errorf("个人号对照应发起 %s，实际请求=%v", c.endpoint, rPer.snapshot())
			}
		})
	}
}

// TestEnterpriseCheckinStillRefreshesCredits 企业号虽跳过签到，但**必须继续刷新额度**
// （否则额度永远不更新，面板与 credit_floor 都失去数据源）——这正是只 skip 签到、
// 不 continue 整轮的原因。
func TestEnterpriseCheckinStillRefreshesCredits(t *testing.T) {
	s, r := enterpriseTestScheduler(t, true)
	s.RunCheckinNow()

	if !r.has("/get-enterprise-user-usage") {
		t.Fatalf("企业号应走企业口径额度查询，实际请求=%v", r.snapshot())
	}
	if r.has("/billing/meter/get-user-resource") {
		t.Errorf("企业号不应再走个人资源包端点，实际请求=%v", r.snapshot())
	}
	st, ok := s.cfg.Pool.Status("u1")
	if !ok {
		t.Fatal("status not found")
	}
	// limitNum - round(credit) = 2000 - 812 = 1188。
	if st.Credits != 1188 || st.CreditsTotal != 2000 {
		t.Errorf("企业额度映射错误：credits=%d total=%d want 1188/2000", st.Credits, st.CreditsTotal)
	}
}

// TestEnterpriseGateKeepsKeepaliveAndBalance 企业版照常参与保活与选号前置能力
// （token 会过期、且它照常接流量），不受成长体系门控影响。
func TestEnterpriseGateKeepsKeepaliveAndBalance(t *testing.T) {
	s, r := enterpriseTestScheduler(t, true)
	s.RunKeepaliveNow()
	if !r.has("/token/refresh") {
		t.Errorf("企业号必须照常保活，实际请求=%v", r.snapshot())
	}
	if s.cfg.Pool == nil {
		t.Fatal("pool missing")
	}
}
