// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// trial 一次性批量领取 global trial 加油包：遍历 auths 下全部账号，
// 仅对 global 账号执行 /billing/ide/trial（CN 无此端点，明确提示不适用）。
//
// 用法：
//
//	# 本地：在项目根目录（需 config.json + auths/ + data/）直接 run
//	go run ./cmd/trial [-proxies data/proxies.json] [凭证目录]
//
// 号按网关代理池里的绑定走代理（-proxies 指网关的 proxies.json，只读）；被代理池拦着的号记 SKIP。
//
//	# 容器内：先 cp 进去再 exec
//	docker cp trial workbuddy2api:/tmp/trial
//	docker exec -w /app workbuddy2api /tmp/trial
//
// 结果逐账号输出到 stdout：
//
//	uid | nick | status | detail
//	----+------+--------+-------
//	... | GLOBAL | OK | trial granted
//	... | GLOBAL | ALREADY | 已领取过（幂等，不算失败）
//	... | CN     | N/A   | not applicable
//	... | GLOBAL | SKIP  | 代理：代理「甲」连不上（…）
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// classifyTrial 归一化 ClaimTrial 结果（纯函数，供 main 循环与测试直接断言）：
// err → FAIL；claimed → OK；否则（幂等码已领）→ ALREADY。
func classifyTrial(claimed bool, err error) (trialStatus, string) {
	switch {
	case err != nil:
		return trialFailed, err.Error()
	case claimed:
		return trialOK, "trial granted"
	default:
		return trialAlready, "already claimed (idempotent)"
	}
}

// trialStatus 单账号 trial 领取结果状态。
type trialStatus string

const (
	trialOK      trialStatus = "OK"
	trialAlready trialStatus = "ALREADY"
	trialNotApp  trialStatus = "N/A" // CN 账号不适用
	trialFailed  trialStatus = "FAIL"
	trialSkipped trialStatus = "SKIP" // 代理池拦着：请求出不去
)

type trialRow struct {
	uid    string
	nick   string
	status trialStatus
	detail string
}

// exitFunc 供测试替换（默认 os.Exit；测试换成 panic 以进程内捕获退出码）。
var exitFunc = os.Exit

func main() {
	exitFunc(run(os.Args[1:], os.Stdout, os.Stderr, upstream.New()))
}

func run(args []string, stdout, stderr io.Writer, up *upstream.Client) int {
	fs := flag.NewFlagSet("trial", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proxiesPath := fs.String("proxies", filepath.Join("data", "proxies.json"), "网关的代理池文件（只读），号按绑定走代理；文件不存在就都走默认线路")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	authDir := "auths"
	if fs.NArg() > 0 {
		authDir = fs.Arg(0)
	}
	files, err := filepath.Glob(filepath.Join(authDir, "workbuddy-*.json"))
	if err != nil || len(files) == 0 {
		fmt.Fprintf(stderr, "no auth files in %s\n", authDir)
		return 1
	}
	sort.Strings(files)
	reg, err := proxies.OpenReadOnly(*proxiesPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	up.SetRouter(reg)

	// trial 是 global 专属端点：必须开启 global realm 路由，否则 upstream.New() 的
	// GlobalEnabled 零值 false 会把请求路由到 CN base（codebuddy.cn）而必然失败。
	up.GlobalEnabled = true
	var rows []trialRow
	for _, f := range files {
		r := trialRow{uid: filepath.Base(f)}
		raw, err := os.ReadFile(f)
		if err != nil {
			r.status, r.detail = trialFailed, "load: "+err.Error()
			rows = append(rows, r)
			continue
		}
		a, err := auth.Parse(raw)
		if err != nil {
			r.status, r.detail = trialFailed, "parse: "+err.Error()
			rows = append(rows, r)
			continue
		}
		a.FilePath = f
		r.uid, r.nick = a.UID, a.Nickname

		// 仅 global 账号适用：CN 明确提示不适用，不发任何请求。
		if !a.IsGlobal() {
			r.status, r.detail = trialNotApp, "CN account not applicable"
			rows = append(rows, r)
			continue
		}
		reg.Track(a, false)
		if reason := reg.Blocked(a.UID); reason != "" {
			r.status, r.detail = trialSkipped, "代理："+reason
			rows = append(rows, r)
			continue
		}

		r.status, r.detail = classifyTrial(up.ClaimTrial(a))
		rows = append(rows, r)
	}

	var okN, alreadyN, notAppN, skipN, failN int
	fmt.Fprintf(stdout, "uid                                  | nick        | status  | detail\n")
	fmt.Fprintf(stdout, "-------------------------------------+-------------+---------+------------------------------\n")
	for _, r := range rows {
		fmt.Fprintf(stdout, "%-36s | %-11s | %-7s | %s\n",
			trunc(r.uid, 36), trunc(r.nick, 11), r.status, r.detail)
		switch r.status {
		case trialOK:
			okN++
		case trialAlready:
			alreadyN++
		case trialNotApp:
			notAppN++
		case trialSkipped:
			skipN++
		default:
			failN++
		}
	}
	fmt.Fprintf(stdout, "\ntotal=%d ok=%d already=%d na=%d fail=%d skip=%d\n",
		len(rows), okN, alreadyN, notAppN, failN, skipN)
	return 0
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
