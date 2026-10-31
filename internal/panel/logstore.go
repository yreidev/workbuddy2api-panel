// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// logstore.go 日志留存：运行日志按频道落盘、按时间范围查历史，以及各类留存的占用与清空。
// 请求记录、任务日志、系统日志、积分获取的归档见 logarchive，用量统计见 usage；保留天数和开关在配置里设
// （logging.*），保存即生效，由 main 转给各自的归档。
package panel

import (
	"log"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/logarchive"
)

const (
	// logsArchiveDefault / logsArchiveMax 按时间范围查运行日志一次返回的行数：缺省与最多。
	logsArchiveDefault = 500
	logsArchiveMax     = 5000
)

// logStorageLabel 日志留存的几类（清空接口的 kind 与系统日志里的名字）。
var logStorageLabel = map[string]string{
	"requests": "请求记录",
	"task":     "任务日志",
	"system":   "系统日志",
	"usage":    "用量统计",
	"credit":   "积分获取",
}

// archiveLogLine 运行日志每行入环后按频道落盘：任务频道进任务日志，其余进系统日志。
// 归档没开留存时 Append 直接丢掉。
func (p *Panel) archiveLogLine(e LogEntry) {
	if e.Ch == ChTask {
		p.cfg.TaskLog.Append(e.TS, e)
		return
	}
	p.cfg.SystemLog.Append(e.TS, e)
}

// logLevelOf 与面板 lib/logs.ts 的 logLevel 同一口径：靠关键字判级别（区分大小写）。
func logLevelOf(text string) string {
	switch {
	case strings.Contains(text, "error") || strings.Contains(text, "失败") || strings.Contains(text, "错误"):
		return "error"
	case strings.Contains(text, "warn") || strings.Contains(text, "冷却") || strings.Contains(text, "熔断"):
		return "warn"
	}
	return "info"
}

// matchLevel 级别筛选：warn = 警告及错误，error = 仅错误，其余不筛。
func matchLevel(text, level string) bool {
	switch level {
	case "error":
		return logLevelOf(text) == "error"
	case "warn":
		return logLevelOf(text) != "info"
	}
	return true
}

// logsArchive 按时间范围从任务日志、系统日志的留存里查运行日志。参数：ch = task / sys（缺省两个都查）、
// level = warn / error、q = 关键字（不分大小写）、from/to 闭区间（unix 秒或 RFC3339，不给 = 不设界）、
// limit 缺省 500、最多 5000。返回区间内最新的 limit 行，按时间先后排（和实时的最近 500 行一样从上往下读）；
// more 表示更早的还有没返回的。
func (p *Panel) logsArchive(w http.ResponseWriter, r *http.Request) {
	if p.cfg.TaskLog == nil && p.cfg.SystemLog == nil {
		writeErr(w, http.StatusNotImplemented, "runtime log archive not available")
		return
	}
	q := r.URL.Query()
	limit := logsArchiveDefault
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = min(n, logsArchiveMax)
	}
	ch, level := q.Get("ch"), q.Get("level")
	kw := strings.ToLower(strings.TrimSpace(q.Get("q")))
	query := logarchive.Query[LogEntry]{
		Limit: limit,
		From:  parseTimeParam(q.Get("from")),
		To:    parseTimeParam(q.Get("to")),
		Time:  func(e LogEntry) time.Time { return e.TS },
		Keep: func(e LogEntry) bool {
			return matchLevel(e.Text, level) && (kw == "" || strings.Contains(strings.ToLower(e.Text), kw))
		},
	}
	var rows []LogEntry
	more := false
	for _, src := range []struct {
		ch    string
		store *logarchive.Store
	}{{ChTask, p.cfg.TaskLog}, {ChSys, p.cfg.SystemLog}} {
		if ch != "" && ch != "all" && ch != src.ch {
			continue
		}
		got, m, err := logarchive.Read(src.store, query)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		rows = append(rows, got...)
		more = more || m
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].TS.After(rows[j].TS) })
	if len(rows) > limit {
		rows, more = rows[:limit], true
	}
	slices.Reverse(rows)
	if rows == nil {
		rows = []LogEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": rows, "limit": limit, "more": more})
}

// logStorage 日志留存各类的存储状态：请求记录、任务日志、系统日志、积分获取的文件数、大小、最早日期，
// 用量统计的桶数和 usage.json 大小。没接上的那类为 null。
func (p *Panel) logStorage(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"requests": nil, "task": nil, "system": nil, "usage": nil, "credit": nil}
	if p.cfg.RequestLog != nil {
		out["requests"] = p.cfg.RequestLog.ArchiveStats()
	}
	if p.cfg.TaskLog != nil {
		out["task"] = p.cfg.TaskLog.Stats()
	}
	if p.cfg.SystemLog != nil {
		out["system"] = p.cfg.SystemLog.Stats()
	}
	if p.cfg.Usage != nil {
		out["usage"] = p.cfg.Usage.StoreStats()
	}
	if p.cfg.CreditLog != nil {
		out["credit"] = p.cfg.CreditLog.Stats()
	}
	writeJSON(w, http.StatusOK, out)
}

// logStorageClear 清空一类日志留存：requests / task / system / credit 删掉全部归档文件（含今天正在写的），
// usage 清空全部用量明细。清完在系统日志里记一行，什么时候清的有据可查。
func (p *Panel) logStorageClear(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	label, ok := logStorageLabel[kind]
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown log kind: "+kind)
		return
	}
	var clear func() error
	switch {
	case kind == "requests" && p.cfg.RequestLog != nil:
		clear = p.cfg.RequestLog.ClearArchive
	case kind == "task" && p.cfg.TaskLog != nil:
		clear = p.cfg.TaskLog.Clear
	case kind == "system" && p.cfg.SystemLog != nil:
		clear = p.cfg.SystemLog.Clear
	case kind == "usage" && p.cfg.Usage != nil:
		clear = p.cfg.Usage.Clear
	case kind == "credit" && p.cfg.CreditLog != nil:
		clear = p.cfg.CreditLog.Clear
	}
	if clear == nil {
		writeErr(w, http.StatusNotImplemented, kind+" storage not available")
		return
	}
	if err := clear(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("panel: 已清空%s", label)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
