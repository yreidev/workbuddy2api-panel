// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// signin 一次性批量签到工具：遍历 ./auths/workbuddy-*.json 全部账号，
// 自动 RefreshToken（过期时），逐个调 daily-checkin，顺手查余额。
//
//	signin [-proxies data/proxies.json] [凭证目录]
//
// 号按网关代理池里的绑定走代理（-proxies 指网关的 proxies.json，只读）；代理不通、在等代理、开着
// 「只许走号的代理」却没绑的号不发请求，记 SKIP。
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

type row struct {
	file     string
	uid      string
	nick     string
	status   string // OK | ALREADY | FAIL | AUTH_INVALID | LOAD_ERR | SKIP（代理池拦着）
	detail   string
	remain   int64
	hasQuota bool
}

// exitFunc 供测试替换（默认 os.Exit；测试换成 panic 以进程内捕获退出码）。
var exitFunc = os.Exit

func main() {
	exitFunc(run(os.Args[1:], os.Stdout, os.Stderr, upstream.New()))
}

func run(args []string, stdout, stderr io.Writer, up *upstream.Client) int {
	fs := flag.NewFlagSet("signin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proxiesPath := fs.String("proxies", filepath.Join("data", "proxies.json"), "网关的代理池文件（只读），号按绑定走代理；文件不存在就都走默认线路")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "auths"
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	files, err := filepath.Glob(filepath.Join(dir, "workbuddy-*.json"))
	if err != nil || len(files) == 0 {
		fmt.Fprintf(stderr, "no auth files in %s\n", dir)
		return 1
	}
	sort.Strings(files)
	reg, err := proxies.OpenReadOnly(*proxiesPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	up.SetRouter(reg)

	var rows []row
	okN, alreadyN, failN, skipN := 0, 0, 0, 0
	for _, f := range files {
		r := row{file: filepath.Base(f)}
		raw, err := os.ReadFile(f)
		if err != nil {
			r.status, r.detail = "LOAD_ERR", err.Error()
			rows = append(rows, r)
			failN++
			continue
		}
		a, err := auth.Parse(raw)
		if err != nil {
			r.status, r.detail = "LOAD_ERR", err.Error()
			rows = append(rows, r)
			failN++
			continue
		}
		a.FilePath = f
		r.uid, r.nick = a.UID, a.Nickname
		reg.Track(a, false)
		if reason := reg.Blocked(a.UID); reason != "" {
			r.status, r.detail = "SKIP", "代理："+reason // 不用 short：按字节截会截坏中文
			rows = append(rows, r)
			skipN++
			continue
		}

		// 2 小时内过期的 token 先刷新
		if a.NeedsRefresh(2 * time.Hour) {
			if err := up.RefreshToken(a); err != nil {
				if ue, ok := err.(*upstream.Error); ok && ue.Kind == upstream.ErrSessionDead {
					r.status = "AUTH_INVALID"
				} else {
					r.status = "FAIL"
				}
				r.detail = "refresh: " + short(err.Error())
				rows = append(rows, r)
				failN++
				continue
			}
			// refresh 后写回文件（权限问题已修复）；落盘失败必须暴露，否则重启回旧 token
			if err := a.SaveAtomic(); err != nil {
				log.Printf("signin %s save: %v", a.UID, err)
			}
		}

		err = up.DailyCheckin(a)
		switch {
		case err == nil:
			r.status = "OK"
			okN++
		case isAlready(err.Error()):
			// DailyCheckin 已签到返回 code!=0 错误
			r.status = "ALREADY"
			r.detail = short(err.Error())
			alreadyN++
		default:
			r.status = "FAIL"
			r.detail = short(err.Error())
			failN++
		}
		// 顺手查余额
		if remain, _, qerr := up.UserResource(a); qerr == nil {
			r.remain, r.hasQuota = remain, true
		}
		rows = append(rows, r)
	}

	// 报告
	fmt.Fprintf(stdout, "uid                                  | nick        | status       | remain | detail\n")
	fmt.Fprintf(stdout, "-------------------------------------+-------------+--------------+--------+------------------------------\n")
	for _, r := range rows {
		remain := "-"
		if r.hasQuota {
			remain = fmt.Sprintf("%d", r.remain)
		}
		fmt.Fprintf(stdout, "%-36s | %-11s | %-12s | %-6s | %s\n",
			trunc(r.uid, 36), trunc(r.nick, 11), r.status, remain, r.detail)
	}
	fmt.Fprintf(stdout, "\ntotal=%d ok=%d already=%d fail=%d skip=%d\n", len(rows), okN, alreadyN, failN, skipN)
	return 0
}

// 已签判定：code 非 0 且含 "已签到"/"already"/"checkin" 等字样
func isAlready(msg string) bool {
	s := strings.ToLower(msg)
	return strings.Contains(s, "已签到") ||
		strings.Contains(s, "already") ||
		strings.Contains(s, "checkin") ||
		strings.Contains(s, "code=400")
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func short(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 60 {
		return s[:60]
	}
	return s
}
