// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/logarchive"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

// runtimeStores 同一个目录下的任务日志、系统日志归档，测试结束时关掉。
func runtimeStores(t *testing.T) (task, system *logarchive.Store, dir string) {
	t.Helper()
	dir = t.TempDir()
	task = logarchive.New(logarchive.Config{Dir: dir, Prefix: "task", Enabled: true})
	system = logarchive.New(logarchive.Config{Dir: dir, Prefix: "system", Enabled: true})
	t.Cleanup(func() { task.Close(); system.Close() })
	return task, system, dir
}

// readStore 关掉归档（刷盘）后读出全部条目，最新的在前。
func readStore(t *testing.T, s *logarchive.Store) []LogEntry {
	t.Helper()
	s.Close()
	rows, _, err := logarchive.Read(s, logarchive.Query[LogEntry]{Limit: 100, Time: func(e LogEntry) time.Time { return e.TS }})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func texts(rows []any) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		m, _ := r.(map[string]any)
		out = append(out, m["text"].(string))
	}
	return out
}

// 每行入环后交给 sink；一次写进来的行数超过容量时，环只留最后几行，sink 照样拿到每一行。
func TestRingSink(t *testing.T) {
	r := NewRing(2)
	var got []LogEntry
	r.SetSink(func(e LogEntry) { got = append(got, e) })
	if _, err := r.Write([]byte("2026/10/28 12:00:00 checkin 一号 签到成功\nsys a\n\nsys b\n")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Ch != ChTask || got[0].Text != "checkin 一号 签到成功" || got[2].Text != "sys b" {
		t.Fatalf("sink 应按行拿到全部 3 行（去掉行首时间、空行）: %+v", got)
	}
	if snap := r.Snapshot(); len(snap) != 2 || snap[0].Text != "sys a" {
		t.Fatalf("环只留最后 2 行: %+v", snap)
	}
}

// 面板接了运行日志归档时：任务频道的行进任务日志，其余进系统日志；没接时不落盘也不出错。
func TestPanelArchivesLogLinesByChannel(t *testing.T) {
	task, system, _ := runtimeStores(t)
	p, _ := newPanelFor(t, Config{TaskLog: task, SystemLog: system})
	if _, err := p.Logs().Write([]byte("checkin 一号 签到成功\n[server] 号 u1 冷却 10m\n")); err != nil {
		t.Fatal(err)
	}
	if rows := readStore(t, task); len(rows) != 1 || rows[0].Text != "checkin 一号 签到成功" || rows[0].Ch != ChTask {
		t.Fatalf("任务日志: %+v", rows)
	}
	if rows := readStore(t, system); len(rows) != 1 || rows[0].Text != "[server] 号 u1 冷却 10m" || rows[0].Ch != ChSys {
		t.Fatalf("系统日志: %+v", rows)
	}

	// 只接了系统日志：任务频道的行 Append 到 nil 归档，直接丢掉。
	_, system2, _ := runtimeStores(t)
	p2, _ := newPanelFor(t, Config{SystemLog: system2})
	if _, err := p2.Logs().Write([]byte("checkin 二号 签到成功\nsys only\n")); err != nil {
		t.Fatal(err)
	}
	if rows := readStore(t, system2); len(rows) != 1 || rows[0].Text != "sys only" {
		t.Fatalf("只接系统日志: %+v", rows)
	}
}

// 级别判定与面板 lib/logs.ts 同口径。
func TestMatchLevel(t *testing.T) {
	cases := []struct {
		text, level string
		want        bool
	}{
		{"签到失败", "error", true},
		{"[server] error: boom", "error", true},
		{"号 u1 冷却 10m", "error", false},
		{"号 u1 冷却 10m", "warn", true},
		{"熔断 30m", "warn", true},
		{"签到失败", "warn", true},
		{"签到成功", "warn", false},
		{"签到成功", "", true},
		{"ERROR 大写不算", "error", false}, // 与前端正则一样区分大小写
	}
	for _, c := range cases {
		if got := matchLevel(c.text, c.level); got != c.want {
			t.Fatalf("matchLevel(%q, %q)=%v want %v", c.text, c.level, got, c.want)
		}
	}
}

// 按时间范围查运行日志：两个频道合并、按时间先后排；ch / level / q / from / to / limit 都生效；
// 取最新的 limit 行并回报 more；没接归档 501，读失败 500。
func TestPanelLogsArchive(t *testing.T) {
	task, system, _ := runtimeStores(t)
	base := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
	at := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }
	for _, e := range []LogEntry{
		{TS: at(0), Ch: ChTask, Text: "checkin 一号 签到成功"},
		{TS: at(1), Ch: ChSys, Text: "[server] 号 u1 冷却 10m"},
		{TS: at(2), Ch: ChTask, Text: "checkin 二号 签到失败"},
		{TS: at(30), Ch: ChSys, Text: "[server] 第二天 熔断"},
	} {
		if e.Ch == ChTask {
			task.Append(e.TS, e)
		} else {
			system.Append(e.TS, e)
		}
	}
	task.Close()
	system.Close()
	p, _ := newPanelFor(t, Config{TaskLog: task, SystemLog: system})
	query := func(q string) map[string]any {
		t.Helper()
		rec := get(p, http.MethodGet, "/panel/api/logs/archive"+q)
		autoWantCode(t, rec, http.StatusOK)
		return autoDecode(t, rec)
	}
	sec := func(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

	all := query("")
	rows, _ := all["entries"].([]any)
	if got := strings.Join(texts(rows), "|"); got != "checkin 一号 签到成功|[server] 号 u1 冷却 10m|checkin 二号 签到失败|[server] 第二天 熔断" {
		t.Fatalf("两个频道合并、按时间先后排: %s", got)
	}
	if all["limit"] != float64(500) || all["more"] != false {
		t.Fatalf("缺省 limit 500、没有更多: %v", all)
	}

	for q, want := range map[string]string{
		"?ch=task":              "checkin 一号 签到成功|checkin 二号 签到失败",
		"?ch=sys&level=warn":    "[server] 号 u1 冷却 10m|[server] 第二天 熔断",
		"?level=error":          "checkin 二号 签到失败",
		"?q=%E4%BA%8C%E5%8F%B7": "checkin 二号 签到失败", // 二号
		"?q=SERVER":             "[server] 号 u1 冷却 10m|[server] 第二天 熔断",
		"?from=" + sec(at(1)) + "&to=" + sec(at(2)): "[server] 号 u1 冷却 10m|checkin 二号 签到失败",
		"?ch=all&from=" + sec(at(3)):                "[server] 第二天 熔断",
		"?ch=task&from=" + sec(at(3)):               "",
	} {
		rows, _ := query(q)["entries"].([]any)
		if got := strings.Join(texts(rows), "|"); got != want {
			t.Fatalf("%s: got %q want %q", q, got, want)
		}
	}

	top := query("?limit=2")
	rows, _ = top["entries"].([]any)
	if got := strings.Join(texts(rows), "|"); got != "checkin 二号 签到失败|[server] 第二天 熔断" || top["more"] != true || top["limit"] != float64(2) {
		t.Fatalf("limit=2 应取最新两行、按时间先后排并回报还有: %v", top)
	}
	if capped := query("?limit=99999"); capped["limit"] != float64(logsArchiveMax) {
		t.Fatalf("limit 最多 %d: %v", logsArchiveMax, capped["limit"])
	}

	none, _ := newPanelFor(t, Config{})
	autoWantCode(t, get(none, http.MethodGet, "/panel/api/logs/archive"), http.StatusNotImplemented)

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := logarchive.New(logarchive.Config{Dir: file, Prefix: "task", Enabled: true})
	defer bad.Close()
	badPanel, _ := newPanelFor(t, Config{TaskLog: bad})
	autoWantCode(t, get(badPanel, http.MethodGet, "/panel/api/logs/archive"), http.StatusInternalServerError)
}

// 日志留存的占用：五类各自的状态，没接上的为 null。清空：按 kind 清对应的那类并在系统日志记一行；
// kind 不认识 400，没接上 501，清不掉 500。
func TestPanelLogStorage(t *testing.T) {
	task, system, dir := runtimeStores(t)
	reqDir := filepath.Join(t.TempDir(), "request-logs")
	requests := reqlog.New(reqlog.Config{Dir: reqDir, Enabled: true, RetentionDays: 7})
	defer requests.Close()
	rec := usage.New(filepath.Join(t.TempDir(), "usage.json"))
	rec.Add(time.Now(), "cn", "u1", "glm-5.2", usage.Delta{TotalTokens: 10, HasTotal: true}, true)
	rec.SetRetentionDays(14)
	credit := logarchive.New(logarchive.Config{Dir: t.TempDir(), Prefix: "credit", Enabled: true})
	t.Cleanup(credit.Close)
	p, _ := newPanelFor(t, Config{TaskLog: task, SystemLog: system, RequestLog: requests, Usage: rec, CreditLog: credit})

	now := time.Now()
	task.Append(now, LogEntry{TS: now, Ch: ChTask, Text: "checkin 一号 签到成功"})
	requests.Record(reqlog.Event{Time: now, RequestID: "r1", Status: 200, OK: true})
	credit.Append(now, upstream.Earning{Time: now, UID: "u1", Source: upstream.EarnCheckin, Credit: 20})
	eventually := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("5 秒内没等到：%s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// 落盘之前文件已经建出来了（打开即建），等 Stats 看得到文件。
	eventually("任务日志、请求记录、积分获取落盘", func() bool {
		return task.Stats().Files == 1 && requests.ArchiveStats().Files == 1 && credit.Stats().Files == 1
	})

	st := autoDecode(t, get(p, http.MethodGet, "/panel/api/log_storage"))
	taskSt, _ := st["task"].(map[string]any)
	usageSt, _ := st["usage"].(map[string]any)
	reqSt, _ := st["requests"].(map[string]any)
	if taskSt["files"] != float64(1) || taskSt["oldest_day"] != now.Format("2006-01-02") || taskSt["enabled"] != true {
		t.Fatalf("任务日志的状态: %v", taskSt)
	}
	if reqSt["files"] != float64(1) || reqSt["retention_days"] != float64(7) {
		t.Fatalf("请求记录的状态: %v", reqSt)
	}
	if usageSt["buckets"] != float64(1) || usageSt["retention_days"] != float64(14) {
		t.Fatalf("用量统计的状态: %v", usageSt)
	}
	if _, ok := st["system"].(map[string]any); !ok {
		t.Fatalf("系统日志应有状态: %v", st)
	}
	if creditSt, _ := st["credit"].(map[string]any); creditSt["files"] != float64(1) || creditSt["enabled"] != true {
		t.Fatalf("积分获取的状态: %v", st["credit"])
	}

	oldLog := log.Writer()
	log.SetOutput(p.Logs()) // 和 main 一样把标准 log 接进面板的日志环
	t.Cleanup(func() { log.SetOutput(oldLog) })
	for _, kind := range []string{"task", "requests", "usage", "credit"} {
		autoWantCode(t, get(p, http.MethodPost, "/panel/api/log_storage/"+kind+"/clear"), http.StatusOK)
	}
	if n := credit.Stats().Files; n != 0 {
		t.Fatalf("积分获取应清空，还剩 %d 个文件", n)
	}
	if n := task.Stats().Files; n != 0 {
		t.Fatalf("任务日志应清空，还剩 %d 个文件", n)
	}
	if n := requests.ArchiveStats().Files; n != 0 {
		t.Fatalf("请求记录应清空，还剩 %d 个文件", n)
	}
	if n := rec.StoreStats().Buckets; n != 0 {
		t.Fatalf("用量统计应清空，还剩 %d 个桶", n)
	}
	// 清空在系统日志里留一行：面板的日志环里能看到，接了系统日志时也落盘。
	found := false
	for _, e := range p.Logs().Snapshot() {
		if e.Text == "panel: 已清空任务日志" && e.Ch == ChSys {
			found = true
		}
	}
	if !found {
		t.Fatalf("清空应在系统日志里记一行: %+v", p.Logs().Snapshot())
	}

	autoWantCode(t, get(p, http.MethodPost, "/panel/api/log_storage/bogus/clear"), http.StatusBadRequest)

	empty, _ := newPanelFor(t, Config{})
	st = autoDecode(t, get(empty, http.MethodGet, "/panel/api/log_storage"))
	for _, kind := range []string{"requests", "task", "system", "usage", "credit"} {
		if v, ok := st[kind]; !ok || v != nil {
			t.Fatalf("没接上的 %s 应为 null: %v", kind, st)
		}
		autoWantCode(t, get(empty, http.MethodPost, "/panel/api/log_storage/"+kind+"/clear"), http.StatusNotImplemented)
	}

	// 清不掉：归档目录的位置被文件占了。
	file := filepath.Join(dir, "occupied")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := logarchive.New(logarchive.Config{Dir: file, Prefix: "system", Enabled: true})
	defer bad.Close()
	badPanel, _ := newPanelFor(t, Config{SystemLog: bad})
	autoWantCode(t, get(badPanel, http.MethodPost, "/panel/api/log_storage/system/clear"), http.StatusInternalServerError)
}
