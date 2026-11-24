// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
)

const traeQuotaFixture = `{"is_credits_billing":true,"is_dollar_usage_billing":false,"total_balance":0,"usage_summary":{"consumed_amount":12.125,"total_amount":99},"user_entitlement_pack_list":[{"display_desc":"测试额度包","is_hide":true,"entitlement_base_info":{"user_id":"private-user","quota":{"credits_limit":-1},"start_time":1700000000,"end_time":1700000000000},"usage":{},"expire_time":0},{"entitlement_base_info":{"quota":{"credits_limit":20}},"usage":{"credits_amount":1.25}}]}`
const qoderQuotaFixture = `{"displayMode":"qoder","qoderUsage":{"userId":"private-user","upgradeUrl":"https://private.example","isQuotaExceeded":false,"expiresAt":1700000000,"refreshAt":1700000000000,"userQuota":{"total":50,"used":12.125,"remaining":33.5,"unit":"credits"}}}`

func TestQuotaParsingKeepsOfficialValues(t *testing.T) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(traeQuotaFixture), &obj); err != nil {
		t.Fatal(err)
	}
	out, err := parseTraeQuota(obj)
	if err != nil || out.Unit != "credits" || out.Balance == nil || *out.Balance != 0 || out.Used == nil || *out.Used != 12.125 || out.Total == nil || *out.Total != 99 {
		t.Fatalf("Trae 汇总数字丢失或被逐包重算: %+v %v", out, err)
	}
	if len(out.Packages) != 2 || !out.Packages[0].Unlimited || out.Packages[0].Total != nil || out.Packages[0].Used != nil || !out.Packages[0].Hidden || !out.Packages[0].StartsAt.Equal(out.Packages[0].EndsAt) || !out.Packages[0].ExpiresAt.IsZero() || *out.Packages[1].Used != 1.25 {
		t.Fatal("Trae 包不限量、未知用量或时间单位处理错误", out.Packages)
	}
	for _, flags := range [][3]any{{true, true, ""}, {false, false, ""}, {false, true, "USD"}} {
		obj["is_credits_billing"], obj["is_dollar_usage_billing"] = flags[0], flags[1]
		parsed, err := parseTraeQuota(obj)
		if err != nil || parsed.Unit != flags[2] {
			t.Fatal("Trae 单位被猜测或混用", parsed.Unit, err)
		}
	}
	obj = map[string]any{"total_balance": float64(0)}
	out, err = parseTraeQuota(obj)
	if err != nil || out.Unit != "" || out.Used != nil || out.Total != nil {
		t.Fatal("未知单位、已用或总额被补造", out, err)
	}
	if err = json.Unmarshal([]byte(qoderQuotaFixture), &obj); err != nil {
		t.Fatal(err)
	}
	out, err = parseQoderQuota(obj)
	if err != nil || out.Unit != "credits" || *out.Balance != 33.5 || *out.Total != 50 || *out.Used != 12.125 || out.Exceeded == nil || *out.Exceeded || !out.ExpiresAt.Equal(out.RefreshAt) {
		t.Fatal("Qoder 官方剩余被总额减已用覆盖，或时间单位错误", out, err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "userId") {
		t.Fatal("额度视图包含账号身份或无关链接", string(raw))
	}
	obj = map[string]any{"displayMode": "qoder", "qoderUsage": map[string]any{"userQuota": map[string]any{"remaining": float64(0)}}}
	out, err = parseQoderQuota(obj)
	if err != nil || out.Balance == nil || *out.Balance != 0 || out.Total != nil || out.Used != nil || out.Unit != "" {
		t.Fatal("Qoder 零与未知被混淆", out, err)
	}
}

func TestQuotaRejectsUnknownAndMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		parse func(map[string]any) (QuotaSnapshot, error)
		raw   string
	}{
		{parseTraeQuota, `{}`},
		{parseTraeQuota, `{"total_balance":"0"}`},
		{parseTraeQuota, `{"total_balance":1,"usage_summary":{"consumed_amount":false}}`},
		{parseTraeQuota, `{"total_balance":1,"user_entitlement_pack_list":{}}`},
		{parseTraeQuota, `{"total_balance":1,"user_entitlement_pack_list":[null]}`},
		{parseTraeQuota, `{"total_balance":1,"user_entitlement_pack_list":[{"expire_time":-1}]}`},
		{parseQoderQuota, `{"displayMode":"enterprise","enterpriseUsage":{"remaining":0}}`},
		{parseQoderQuota, `{"displayMode":"qoder","qoderUsage":{"userQuota":{}}}`},
		{parseQoderQuota, `{"displayMode":"qoder","qoderUsage":{"userQuota":{"remaining":"0"}}}`},
		{parseQoderQuota, `{"displayMode":"qoder","qoderUsage":{"expiresAt":0.5,"userQuota":{"remaining":0}}}`},
	} {
		var obj map[string]any
		if err := json.Unmarshal([]byte(tc.raw), &obj); err != nil {
			t.Fatal(err)
		}
		if _, err := tc.parse(obj); err == nil {
			t.Error("异常额度响应被记作成功", tc.raw)
		}
	}
}

func TestQuotaTimeSupportsOfficialFarFuture(t *testing.T) {
	end := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	for _, stamp := range []int64{end.Unix(), end.UnixMilli()} {
		got, err := quotaTime(map[string]any{"expiresAt": float64(stamp)}, "expiresAt")
		if err != nil || !got.Equal(end) || got.Location() != time.UTC {
			t.Fatalf("官方远期日期未按秒／毫秒保留：%v %v", got, err)
		}
		if _, err = json.Marshal(got); err != nil {
			t.Fatal("官方远期日期不能持久化", err)
		}
	}
	for _, stamp := range []float64{253402300800, 253402300800000, -1, 1.5} {
		if _, err := quotaTime(map[string]any{"expiresAt": stamp}, "expiresAt"); err == nil {
			t.Fatal("接受了不能持久化的到期日期")
		}
	}
}

func TestQuotaOfficialRequestShapes(t *testing.T) {
	for _, target := range []string{"traework:cn", "traecode:cn", "qoder:cn"} {
		t.Run(target, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if target == "qoder:cn" {
					if r.Method != "GET" || r.URL.Path != "/sash/api/v2/me/usage" || r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Cosy-Clienttype") != "10" || r.UserAgent() != "Qoder" || r.Header.Get("Content-Type") != "" {
						t.Error("Qoder 额度请求混用了 CLI 网关身份")
					}
					_, _ = w.Write([]byte(qoderQuotaFixture))
				} else {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if r.Method != "POST" || r.URL.Path != "/trae/api/v2/pay/web_user_ent_usage" || r.Header.Get("Authorization") != "Cloud-IDE-JWT fixture-token" || body["require_usage"] != true {
						t.Error("Trae 额度请求错误")
					}
					if r.UserAgent() != traeWebUA || r.Header.Get("Origin") != "https://work.trae.cn" || r.Header.Get("Sec-Fetch-Site") != "same-site" || r.Header.Get("X-Trae-Client-Type") != "" {
						t.Error("Trae 额度没有使用独立的网页请求身份")
					}
					_, _ = w.Write([]byte(traeQuotaFixture))
				}
			}))
			defer srv.Close()
			var adapter quotaAdapter = &Trae{HTTP: srv.Client(), PayBase: srv.URL}
			if target == "qoder:cn" {
				adapter = &Qoder{HTTP: srv.Client(), OpenAPIBase: srv.URL}
			}
			if _, err := adapter.Quota(context.Background(), target, Credential{AccessToken: "fixture-token"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type fakeQuotaAdapter struct {
	*fakeAdapter
	quota func(context.Context, string, Credential) (QuotaSnapshot, error)
}

func (f *fakeQuotaAdapter) Quota(ctx context.Context, target string, c Credential) (QuotaSnapshot, error) {
	return f.quota(ctx, target, c)
}

func TestQuotaSingleFlightRefreshPersistenceAndProtection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	m, f := testManager(t, path)
	id := importFake(t, m, "traecode:cn", "one")
	if importFake(t, m, "traework:cn", "one") != id {
		t.Fatal("Trae 账号被拆分")
	}
	m.data.Accounts[0].Credential.ExpiresAt = 1
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	adapter := &fakeQuotaAdapter{fakeAdapter: f, quota: func(_ context.Context, _ string, c Credential) (QuotaSnapshot, error) {
		calls.Add(1)
		if c.AccessToken != "rotated" {
			t.Error("额度请求没有使用最新令牌")
		}
		close(started)
		<-release
		return QuotaSnapshot{Unit: "credits", Balance: new(float64(0))}, nil
	}}
	m.adapters["traecode"], m.adapters["traework"] = adapter, adapter
	var wg sync.WaitGroup
	wg.Go(func() {
		if s, err := m.Quota(context.Background(), id, true); err != nil || s.Error != "" {
			t.Error(s, err)
		}
	})
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Quota(ctx, id, true); !errors.Is(err, context.Canceled) {
		t.Fatal("等待额度查询未响应取消", err)
	}
	if _, err := m.Export([]string{id}); err == nil {
		t.Fatal("额度在途仍可导出旧凭证")
	}
	if err := m.Change(id, "proxy", "", 0, nil, "other"); err == nil {
		t.Fatal("额度在途仍可换代理")
	}
	if err := m.Update(id, AccountInput{}, true); err == nil {
		t.Fatal("额度在途仍可删号")
	}
	if _, err := m.Import(context.Background(), AccountInput{Target: "traecode:cn", Credential: Credential{UID: "one", AccessToken: "replacement"}}); err == nil {
		t.Fatal("额度在途仍可重导入")
	}
	for range 12 {
		wg.Go(func() {
			if s, err := m.Quota(context.Background(), id, false); err != nil || s.Error != "" {
				t.Error(s, err)
			}
		})
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 || f.refreshed.Load() != 1 || m.List()[0].InFlight != 0 {
		t.Fatal("额度或令牌重复查询，或在途计数未释放")
	}
	reopened, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	view := reopened.List()[0]
	if view.Quota == nil || view.Quota.Snapshot == nil || view.Quota.Snapshot.Balance == nil || *view.Quota.Snapshot.Balance != 0 || view.Quota.Snapshot.FetchedAt.IsZero() || reopened.data.Accounts[0].Credential.RefreshToken != "rotated-refresh" {
		t.Fatal("额度快照或最新令牌重启后丢失", view)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("额度与凭证文件权限错误", err)
	}
}

func TestQuotaFailureRetainsSnapshotAndInferenceState(t *testing.T) {
	m, f := testManager(t, "")
	id := importFake(t, m, "qoder:cn", "one")
	balance := 7.25
	old := &QuotaSnapshot{FetchedAt: time.Now().Add(-time.Hour), Unit: "credits", Balance: &balance}
	m.data.Accounts[0].Quota = old
	rt := m.running[id]
	rt.lastError, rt.coolUntil = "推理错误", time.Now().Add(time.Hour)
	m.adapters["qoder"] = &fakeQuotaAdapter{fakeAdapter: f, quota: func(context.Context, string, Credential) (QuotaSnapshot, error) {
		return QuotaSnapshot{}, errors.New("fixture upstream unavailable")
	}}
	s, err := m.Quota(context.Background(), id, true)
	if err != nil || !strings.Contains(s.Error, "fixture upstream unavailable") || s.Snapshot != old || s.CheckedAt.IsZero() || rt.lastError != "推理错误" || rt.coolUntil.Before(time.Now()) || m.List()[0].Disabled {
		t.Fatal("查询错误抹掉快照或污染推理状态", s, err)
	}
	registry, err := proxies.Open(filepath.Join(t.TempDir(), "proxies.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.UpdateSettings(proxies.Settings{RequireBound: true}); err != nil {
		t.Fatal(err)
	}
	m.SetProxies(registry)
	m.adapters["qoder"] = &fakeQuotaAdapter{fakeAdapter: f, quota: func(context.Context, string, Credential) (QuotaSnapshot, error) {
		t.Error("额度查询绕过代理要求")
		return QuotaSnapshot{}, nil
	}}
	s, err = m.Quota(context.Background(), id, true)
	if err != nil || !strings.Contains(s.Error, "proxy_required") || s.Snapshot != old {
		t.Fatal("代理阻断未显示或丢失旧快照", s, err)
	}
	if _, err = m.Quota(context.Background(), "missing", true); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("不存在账号错误", err)
	}
	// 无法保存时不发布新的成功快照，也不覆盖已有凭证。
	m.SetProxies(nil)
	m.path = t.TempDir()
	m.adapters["qoder"] = &fakeQuotaAdapter{fakeAdapter: f, quota: func(context.Context, string, Credential) (QuotaSnapshot, error) {
		return QuotaSnapshot{Unit: "credits", Balance: new(float64(2))}, nil
	}}
	s, err = m.Quota(context.Background(), id, true)
	if err != nil || s.Error == "" || s.Snapshot != old || m.data.Accounts[0].Quota != old {
		t.Fatal("保存失败被当成成功或覆盖了旧快照", s, err)
	}
}

func TestQuotaOwnerCancelHandoffWithoutCachePoisoning(t *testing.T) {
	m, f := testManager(t, "")
	id := importFake(t, m, "qoder:cn", "owner-cancel")
	firstStarted := make(chan struct{})
	var calls atomic.Int32
	m.adapters["qoder"] = &fakeQuotaAdapter{fakeAdapter: f, quota: func(ctx context.Context, _ string, _ Credential) (QuotaSnapshot, error) {
		n := calls.Add(1)
		if n == 1 {
			close(firstStarted)
			<-ctx.Done()
			return QuotaSnapshot{}, ctx.Err()
		}
		return QuotaSnapshot{Unit: "credits", Balance: new(float64(42.5))}, nil
	}}

	ownerCtx, cancelOwner := context.WithCancel(t.Context())
	defer cancelOwner()

	ownerDone := make(chan error, 1)
	go func() {
		_, err := m.Quota(ownerCtx, id, true)
		ownerDone <- err
	}()
	<-firstStarted

	// 第二个调用者在首个航班运行期间挂起等待；首个调用者取消后，第二个调用者必须自动重查成功。
	waiterDone := make(chan struct {
		status QuotaStatus
		err    error
	}, 1)
	go func() {
		s, err := m.Quota(t.Context(), id, true)
		waiterDone <- struct {
			status QuotaStatus
			err    error
		}{s, err}
	}()
	// 给第二个调用者短暂时间挂到 flight.done，随后取消首个调用者。
	time.Sleep(20 * time.Millisecond)
	cancelOwner()

	if err := <-ownerDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("首个被取消的调用者应返回 context.Canceled: %v", err)
	}
	res := <-waiterDone
	if res.err != nil || res.status.Error != "" || res.status.Snapshot == nil || res.status.Snapshot.Balance == nil || *res.status.Snapshot.Balance != 42.5 {
		t.Fatalf("等待者接棒查询失败: status=%+v err=%v", res.status, res.err)
	}
	if calls.Load() != 2 {
		t.Fatalf("期望首个取消后等待者重查 1 次，实际 calls=%d", calls.Load())
	}

	// 后续非强制查询应命中刚写入的有效快照缓存，而不是残留的 context canceled。
	cached, err := m.Quota(t.Context(), id, false)
	if err != nil || cached.Error != "" || cached.Snapshot == nil || *cached.Snapshot.Balance != 42.5 || calls.Load() != 2 {
		t.Fatalf("有效快照未缓存或被 owner cancel 污染: status=%+v err=%v calls=%d", cached, err, calls.Load())
	}
}
