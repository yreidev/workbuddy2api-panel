// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/logarchive"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

type creditLogResp struct {
	Entries   []upstream.Earning `json:"entries"`
	Limit     int                `json:"limit"`
	More      bool               `json:"more"`
	Total     int64              `json:"total"`
	Count     int                `json:"count"`
	BySource  []creditSum        `json:"by_source"`
	ByAccount []creditSum        `json:"by_account"`
}

// 积分获取流水：按时间范围、账号、来源筛，新的在前、按 limit 截；合计算区间内符合条件的全部笔数，
// 按积分从多到少排、同额按 key；按账号合计的昵称取时间上最新的那笔。没接上留存 501，读不了 500。
func TestPanelCreditLog(t *testing.T) {
	store := logarchive.New(logarchive.Config{Dir: t.TempDir(), Prefix: "credit", Enabled: true})
	t.Cleanup(store.Close)
	base := time.Date(2026, 10, 30, 8, 0, 0, 0, time.Local)
	at := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }
	for _, e := range []upstream.Earning{
		{Time: at(2), UID: "u1", Nickname: "一号", Realm: "cn", Source: upstream.EarnTask, Detail: "chat_5", Credit: 100},
		{Time: at(3), UID: "u1", Nickname: "一号", Realm: "cn", Source: upstream.EarnTravel, Credit: 20},
		{Time: at(1), UID: "u2", Nickname: "二号", Realm: "global", Source: upstream.EarnTask, Detail: "chat_5", Credit: 100},
		// 最早的一笔最后才写进来：按账号合计的昵称照样取时间上最新那笔的。
		{Time: at(0), UID: "u1", Nickname: "旧名", Realm: "cn", Source: upstream.EarnCheckin, Credit: 20},
	} {
		store.Append(e.Time, e)
	}
	store.Close() // 刷盘；关掉之后照样能读
	p, _ := newPanelFor(t, Config{CreditLog: store})
	read := func(query string) creditLogResp {
		t.Helper()
		rec := get(p, http.MethodGet, "/panel/api/credit_log"+query)
		autoWantCode(t, rec, http.StatusOK)
		var out creditLogResp
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	sources := func(rows []upstream.Earning) []string {
		out := make([]string, 0, len(rows))
		for _, e := range rows {
			out = append(out, e.UID+"/"+e.Source)
		}
		return out
	}
	sums := func(rows []creditSum) []string {
		out := make([]string, 0, len(rows))
		for _, s := range rows {
			out = append(out, s.Key+"="+strconv.FormatInt(s.Credit, 10)+"x"+strconv.Itoa(s.Count)+s.Nickname+s.Realm)
		}
		return out
	}
	eq := func(what string, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %v，应为 %v", what, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s = %v，应为 %v", what, got, want)
			}
		}
	}

	all := read("")
	eq("全部流水", sources(all.Entries), []string{"u1/travel", "u1/task", "u2/task", "u1/checkin"})
	if all.More || all.Limit != creditLogDefault || all.Total != 240 || all.Count != 4 {
		t.Fatalf("全部的合计: %+v", all)
	}
	eq("按来源合计", sums(all.BySource), []string{"task=200x2", "checkin=20x1", "travel=20x1"})
	eq("按账号合计", sums(all.ByAccount), []string{"u1=140x3一号cn", "u2=100x1二号global"})
	if e := all.Entries[1]; e.Detail != "chat_5" || e.Nickname != "一号" || !e.Time.Equal(at(2)) {
		t.Fatalf("流水的字段: %+v", e)
	}

	// limit 只截流水，合计照样算全部。
	two := read("?limit=2")
	eq("截到 2 笔", sources(two.Entries), []string{"u1/travel", "u1/task"})
	if !two.More || two.Limit != 2 || two.Total != 240 || two.Count != 4 {
		t.Fatalf("截断后的合计: %+v", two)
	}
	if got := read("?limit=999999"); got.Limit != creditLogMax {
		t.Fatalf("limit 上限 = %d", got.Limit)
	}

	u2 := read("?uid=u2")
	eq("只看 u2", sources(u2.Entries), []string{"u2/task"})
	eq("只看 u2 的按账号合计", sums(u2.ByAccount), []string{"u2=100x1二号global"})
	if u2.Total != 100 {
		t.Fatalf("只看 u2 的合计 = %d", u2.Total)
	}
	task := read("?source=task")
	eq("只看领奖", sources(task.Entries), []string{"u1/task", "u2/task"})
	if task.Total != 200 || task.Count != 2 {
		t.Fatalf("只看领奖的合计: %+v", task)
	}
	// from/to 是闭区间。
	mid := read("?from=" + strconv.FormatInt(at(1).Unix(), 10) + "&to=" + strconv.FormatInt(at(2).Unix(), 10))
	eq("区间内", sources(mid.Entries), []string{"u1/task", "u2/task"})
	if mid.Total != 200 {
		t.Fatalf("区间内的合计 = %d", mid.Total)
	}
	none := read("?uid=nobody")
	if none.Entries == nil || len(none.Entries) != 0 || none.BySource == nil || none.ByAccount == nil || none.Total != 0 {
		t.Fatalf("没有符合的：空数组，不是 null: %+v", none)
	}

	empty, _ := newPanelFor(t, Config{})
	autoWantCode(t, get(empty, http.MethodGet, "/panel/api/credit_log"), http.StatusNotImplemented)

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := logarchive.New(logarchive.Config{Dir: file, Prefix: "credit", Enabled: true})
	defer bad.Close()
	badPanel, _ := newPanelFor(t, Config{CreditLog: bad})
	autoWantCode(t, get(badPanel, http.MethodGet, "/panel/api/credit_log"), http.StatusInternalServerError)
}
