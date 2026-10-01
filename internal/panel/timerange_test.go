package panel

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
	"github.com/linguo2625469/workbuddy2api-panel/internal/usage"
)

// parseTimeParam 是「今天 / 自定义」区间的唯一入口：前端默认发 unix 秒，手工
// 调接口时可以用 RFC3339 / datetime-local；非法值与空串一律回落"不设界"，
// 不能让一个拼错的参数把整页用量打成 500。
func TestParseTimeParam(t *testing.T) {
	want := time.Date(2026, 9, 30, 14, 5, 0, 0, time.Local)
	cases := []struct {
		name string
		in   string
		want time.Time
	}{
		{"空串", "", time.Time{}},
		{"空白", "   ", time.Time{}},
		{"unix 秒", "1786000000", time.Unix(1786000000, 0)},
		{"unix 毫秒", "1786000000000", time.UnixMilli(1786000000000)},
		{"零值按不设界", "0", time.Time{}},
		{"负数按不设界", "-5", time.Time{}},
		{"RFC3339", want.Format(time.RFC3339), want},
		{"本地 datetime-local", "2026-09-30T14:05", want},
		{"非法", "not-a-time", time.Time{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseTimeParam(c.in)
			if !got.Equal(c.want) {
				t.Fatalf("parseTimeParam(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// usage 接口的区间参数必须真的作用到聚合上：from/to 生效时 hours 被忽略，
// 且响应回显 window_from/window_to 供面板确认口径。
func TestUsageHandlerAcceptsExplicitRange(t *testing.T) {
	rec := usage.New("")
	base := time.Now().Truncate(time.Hour).Add(-5 * time.Hour)
	for i := 0; i < 6; i++ {
		rec.Add(base.Add(time.Duration(i)*time.Hour), "cn", "u1", "glm-5.2",
			usage.Delta{PromptTokens: 10, HasPromptTokens: true}, true)
	}
	p := New(Config{Version: "test", APIKey: "k", Usage: rec, Pool: pool.New("")})

	get := func(query string) map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", "/panel/api/usage"+query, nil)
		req.Header.Set("Authorization", "Bearer k")
		rr := httptest.NewRecorder()
		p.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("usage%s -> %d %s", query, rr.Code, rr.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	from := base.Add(2 * time.Hour).Unix()
	to := base.Add(3 * time.Hour).Unix()
	// 同时带上 hours=720：区间参数应优先，否则会被算成 30 天全量。
	got := get("?hours=720&from=" + strconv.FormatInt(from, 10) + "&to=" + strconv.FormatInt(to, 10))
	totals := got["totals"].(map[string]any)
	if reqs := totals["requests"].(float64); reqs != 2 {
		t.Fatalf("区间请求数 = %v, want 2（from/to 应优先于 hours）", reqs)
	}
	if got["window_from"] == nil || got["window_to"] == nil {
		t.Fatalf("响应应回显 window_from/window_to: %+v", got)
	}

	// 无区间参数时回落默认 72h，且不回显区间。
	def := get("")
	if def["window_from"] != nil || def["window_to"] != nil {
		t.Fatalf("默认窗口不应回显区间: %+v", def)
	}
	if r := def["totals"].(map[string]any)["requests"].(float64); r != 6 {
		t.Fatalf("默认窗口请求数 = %v, want 6", r)
	}

	// hours=0（全部历史）同样不回显区间。
	all := get("?hours=0")
	if r := all["totals"].(map[string]any)["requests"].(float64); r != 6 {
		t.Fatalf("全部历史请求数 = %v, want 6", r)
	}
}

// 归档为空时接口必须回 []，而不是 JSON null：前端把 null 与"归档关闭"混在一起
// 会走错分支，把不满足时间区间的最近请求显示出来。
func TestRequestLogsEmptyRangeReturnsArray(t *testing.T) {
	rec := reqlog.New(reqlog.Config{Enabled: true, Dir: t.TempDir(), MaxBytes: 1 << 20, RetentionDays: 7})
	rec.Record(reqlog.Event{Time: time.Now().Add(-48 * time.Hour), RequestID: "old",
		Status: 200, OK: true, Outcome: reqlog.OutcomeSuccess})
	rec.Close()

	p := New(Config{Version: "test", APIKey: "k", Pool: pool.New(""), RequestLog: rec})
	future := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	req := httptest.NewRequest("GET", "/panel/api/request_logs?from="+future, nil)
	req.Header.Set("Authorization", "Bearer k")
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body)
	}
	var out struct {
		Entries []reqlog.Event `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Entries == nil {
		t.Fatalf("空区间应回 []，得到 null: %s", rr.Body)
	}
	if len(out.Entries) != 0 {
		t.Fatalf("未来区间不该命中任何记录: %+v", out.Entries)
	}
}
