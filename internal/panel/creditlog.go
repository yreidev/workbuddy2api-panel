// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// creditlog.go 积分获取：签到、领奖、积分补偿、猫猫旅行、新手礼包、连登兑换领到的积分逐笔落盘
// （upstream.Earning，main 接到 Config.CreditLog），面板「积分获取」页按时间范围查流水和合计。
package panel

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/logarchive"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

const (
	// creditLogDefault / creditLogMax 按时间范围查积分获取流水一次返回的笔数：缺省与最多。
	creditLogDefault = 500
	creditLogMax     = 5000
)

// creditSum 一个来源或一个账号领到的积分合计。
type creditSum struct {
	Key string `json:"key"`
	// Nickname / Realm 只有按账号合计才有，取这个号最新一笔记下的。
	Nickname string `json:"nickname,omitempty"`
	Realm    string `json:"realm,omitempty"`
	Credit   int64  `json:"credit"`
	Count    int    `json:"count"`
	last     time.Time
}

// creditSums 合计按积分从多到少排，同额按 key。
func creditSums(m map[string]*creditSum) []*creditSum {
	out := make([]*creditSum, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b *creditSum) int {
		return cmp.Or(cmp.Compare(b.Credit, a.Credit), cmp.Compare(a.Key, b.Key))
	})
	return out
}

// creditLog 积分获取流水。参数：from/to 闭区间（unix 秒或 RFC3339，不给 = 不设界）、uid、
// source（upstream.Earn*）、limit 缺省 500、最多 5000。返回区间内符合条件的最新 limit 笔，新的在前；
// more 表示更早的还有没返回的。total、count 和按来源、按账号的合计算的是区间内符合条件的全部笔数，
// 不受 limit 限制。
func (p *Panel) creditLog(w http.ResponseWriter, r *http.Request) {
	if p.cfg.CreditLog == nil {
		writeErr(w, http.StatusNotImplemented, "credit log not available")
		return
	}
	q := r.URL.Query()
	limit := creditLogDefault
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = min(n, creditLogMax)
	}
	uid, source := q.Get("uid"), q.Get("source")
	var total int64
	count := 0
	bySource, byAccount := map[string]*creditSum{}, map[string]*creditSum{}
	sum := func(m map[string]*creditSum, key string, e upstream.Earning) *creditSum {
		s := m[key]
		if s == nil {
			s = &creditSum{Key: key}
			m[key] = s
		}
		s.Credit += e.Credit
		s.Count++
		return s
	}
	rows, more, err := logarchive.Read(p.cfg.CreditLog, logarchive.Query[upstream.Earning]{
		Limit: limit,
		From:  parseTimeParam(q.Get("from")),
		To:    parseTimeParam(q.Get("to")),
		Time:  func(e upstream.Earning) time.Time { return e.Time },
		// Keep 对区间内每一笔都会调到（截断在它之后），合计在这里累加。
		Keep: func(e upstream.Earning) bool {
			if (uid != "" && e.UID != uid) || (source != "" && e.Source != source) {
				return false
			}
			total += e.Credit
			count++
			sum(bySource, e.Source, e)
			if a := sum(byAccount, e.UID, e); !e.Time.Before(a.last) {
				a.last, a.Nickname, a.Realm = e.Time, e.Nickname, e.Realm
			}
			return true
		},
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []upstream.Earning{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": rows, "limit": limit, "more": more, "total": total, "count": count,
		"by_source": creditSums(bySource), "by_account": creditSums(byAccount),
	})
}
