// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// credit.go — WorkBuddy 积分查询（全部账号 + 总计），JSON 输出到 stdout。
//
// 用法:
//
//	go run ./cmd/credit [-pretty] [-proxies data/proxies.json]   # 或编译后 ./credit
//
// 号按网关代理池里的绑定走代理（-proxies 指网关的 proxies.json，只读）；被代理池拦着的号不查，
// error 写原因。
//
// 输出结构:
//
//	{"service":"workbuddy","ts":N,
//	 "total":{"remain":N,"used":N,"size":N,"accounts":N,"ok":N,"failed":N},
//	 "accounts":[{"uid","nickname","remain","used","size","packages","ok","error?"}]}
//
// 接口与聚合逻辑：走 internal/upstream 的官方查法——个人号并发请求
// get-user-resource-summary / paid-packages / free-packages（按 *Precise 字段算额度），
// 企业号走 /v2/billing/meter/get-enterprise-user-usage；国际版由 Client 按账号域选 base。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// billingBaseCN 国内版计费域：官方产品入口（1.1.2 以前是 www.codebuddy.cn，积分查询 10-20 实测一致）。
const billingBaseCN = "https://www.workbuddy.cn"

// billingBaseGlobal 国际版计费域。global 账号打 CN 域会得到 401：
// www.codebuddy.cn 不认 workbuddy.ai 的 token（实测 401，workbuddy.ai 同 token 为 code=0）。
const billingBaseGlobal = "https://www.workbuddy.ai"

type authFile struct {
	Auth struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"`
		Domain      string `json:"domain"`
		Realm       string `json:"realm"`
	} `json:"auth"`
	Account struct {
		UID          string `json:"uid"`
		EnterpriseID string `json:"enterpriseId"`
		Nickname     string `json:"nickname"`
	} `json:"account"`
	// Proxy、ProxyURL 号绑的代理（网关写的）和别的工具导出的凭证里带的代理地址，见 auth.Auth.proxy。
	Proxy    auth.ProxyBinding `json:"proxy"`
	ProxyURL string            `json:"proxy_url"`
}

type accountResult struct {
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	Remain   *int64 `json:"remain"`
	Used     *int64 `json:"used"`
	Size     *int64 `json:"size"`
	Packages int    `json:"packages,omitempty"`
	// Unlimited 企业号不限量（上游 limitNum=-1）：remain/used/size 留空，不计入总计。
	Unlimited bool   `json:"unlimited,omitempty"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

// credentialAuth 把凭证文件转成 upstream 用的 *auth.Auth。realm 按文件里的显式值，
// 缺省按 domain 推断（与 internal/auth 同口径）；没有到期时间或已过期时按远期处理，
// 保证查询过程不会把它当成过期 token。
func credentialAuth(af *authFile) *auth.Auth {
	a := &auth.Auth{
		AccessToken:  af.Auth.AccessToken,
		Domain:       af.Auth.Domain,
		UID:          af.Account.UID,
		EnterpriseID: af.Account.EnterpriseID,
		Nickname:     af.Account.Nickname,
		ExpiresAt:    af.Auth.ExpiresAt,
	}
	if a.ExpiresAt <= time.Now().Unix() {
		a.ExpiresAt = time.Now().Add(24 * time.Hour).Unix()
	}
	// BackfillRealmFor 只接受 cn/global；文件里写了别的值时保持缺省，让 Realm() 按 domain 回落。
	_, _ = auth.BackfillRealmFor(a, auth.ResolveRealm(strings.ToLower(strings.TrimSpace(af.Auth.Realm)), af.Auth.Domain))
	a.SetProxyBinding(af.Proxy)
	a.SetProxyURL(af.ProxyURL)
	return a
}

// queryCredits 查一个账号的积分。个人号用逐包构成（packages 计数），
// 企业额度不在资源包体系里，改查企业用量接口，packages 恒 0；不限量（total<0）时 unlimited 为真。
func queryCredits(up *upstream.Client, a *auth.Auth) (remain, used, size int64, packs int, unlimited bool, err error) {
	if a.IsEnterprise() {
		var total int64
		if remain, total, err = up.UserResource(a); err != nil {
			return 0, 0, 0, 0, false, err
		}
		if total < 0 {
			return 0, 0, 0, 0, true, nil
		}
		return remain, total - remain, total, 0, false, nil
	}
	var ps []upstream.CreditPackage
	if ps, remain, size, err = up.CreditPackages(a); err != nil {
		return 0, 0, 0, 0, false, err
	}
	return remain, size - remain, size, len(ps), false, nil
}

// exitFunc 供测试替换（默认 os.Exit；测试换成 panic 以进程内捕获退出码）。
var exitFunc = os.Exit

func main() {
	up := upstream.New() // 带线路表：号按代理池的绑定走代理
	up.HTTP.Timeout = 20 * time.Second
	exitFunc(run(os.Args[1:], os.Stdout, os.Stderr, up))
}

// accountGap 账号之间的请求间隔（避免瞬时打满计量接口），测试可调 0。
var accountGap = 200 * time.Millisecond

func run(args []string, stdout, stderr io.Writer, up *upstream.Client) int {
	fs := flag.NewFlagSet("credit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pretty := fs.Bool("pretty", false, "人类可读的日报（四行汇总）")
	proxiesPath := fs.String("proxies", filepath.Join("data", "proxies.json"), "网关的代理池文件（只读），号按绑定走代理；文件不存在就都走默认线路")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	authDir := "./auths"
	if v := os.Getenv("WB2A_AUTH_DIR"); v != "" {
		authDir = v
	}
	files, _ := filepath.Glob(filepath.Join(authDir, "workbuddy-*.json"))
	sort.Strings(files)
	reg, err := proxies.OpenReadOnly(*proxiesPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	up.SetRouter(reg)
	up.BillingBaseCN, up.BillingBaseGlobal, up.GlobalEnabled = billingBaseCN, billingBaseGlobal, true

	accounts := make([]accountResult, 0, len(files))
	for _, f := range files {
		var af authFile
		raw, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(raw, &af) != nil {
			continue
		}
		res := accountResult{UID: af.Account.UID, Nickname: af.Account.Nickname}
		if af.Auth.AccessToken == "" {
			res.Error = "no accessToken"
			accounts = append(accounts, res)
			continue
		}
		a := credentialAuth(&af)
		reg.Track(a, false)
		if reason := reg.Blocked(a.UID); reason != "" {
			res.Error = "代理：" + reason
			accounts = append(accounts, res)
			continue
		}
		remain, used, size, packs, unlimited, err := queryCredits(up, a)
		switch {
		case err != nil:
			res.Error = err.Error()
		case unlimited:
			res.Unlimited = true
			res.OK = true
		default:
			res.Remain = &remain
			res.Used = &used
			res.Size = &size
			res.Packages = packs
			res.OK = true
		}
		accounts = append(accounts, res)
		time.Sleep(accountGap)
	}

	var totalRemain, totalUsed, totalSize int64
	okCount := 0
	for _, a := range accounts {
		if a.OK {
			okCount++
			if a.Remain != nil {
				totalRemain += *a.Remain
			}
			if a.Used != nil {
				totalUsed += *a.Used
			}
			if a.Size != nil {
				totalSize += *a.Size
			}
		}
	}
	out := map[string]any{
		"service": "workbuddy",
		"ts":      time.Now().Unix(),
		"total": map[string]any{
			"remain":   totalRemain,
			"used":     totalUsed,
			"size":     totalSize,
			"accounts": len(accounts),
			"ok":       okCount,
			"failed":   len(accounts) - okCount,
		},
		"accounts": accounts,
	}
	if *pretty {
		printPretty(accounts, totalRemain, totalUsed, totalSize, okCount, stdout)
		return 0
	}
	raw, _ := json.Marshal(out)
	fmt.Fprintln(stdout, string(raw))
	return 0
}

// printPretty 人类可读日报：四行汇总，无账号明细。
func printPretty(accounts []accountResult, totalRemain, totalUsed, totalSize int64, okCount int, out io.Writer) {
	withBalance := 0
	var failed []string
	for _, a := range accounts {
		if a.OK && (a.Unlimited || a.Remain != nil && *a.Remain > 0) {
			withBalance++
		}
		if !a.OK {
			name := a.Nickname
			if name == "" && len(a.UID) >= 8 {
				name = a.UID[:8]
			}
			failed = append(failed, name+" "+a.Error)
		}
	}
	pct := int64(0)
	if totalSize > 0 {
		pct = totalRemain * 100 / totalSize
	}
	fmt.Fprintf(out, "📊 WorkBuddy 积分日报\n")
	fmt.Fprintf(out, "账号: %d/%d\n", withBalance, len(accounts))
	fmt.Fprintf(out, "总计: %d/%d\n", totalRemain, totalSize)
	fmt.Fprintf(out, "剩余: %d%%\n", pct)
	for _, f := range failed {
		fmt.Fprintf(out, "⚠️ %s\n", f)
	}
}
