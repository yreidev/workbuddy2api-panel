// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// login.go — WorkBuddy OAuth 登录（设备授权流程，CN realm；--realm=global 供国际版）。
//
// 两个子命令，由 login.sh 顺序驱动：
//
//	login [--realm=cn|global] url   → POST /v2/plugin/auth/state?platform=<P>（CN workbuddy、
//	                                  global workbuddy-ai）拿 state+authUrl，state 落
//	                                  /tmp/wb2api-login-state.json，stdout 打印授权 URL
//	login [--realm=cn|global] poll  → 读 state，GET /v2/plugin/auth/token?state= 一次，
//	                                  成功再取账号（CN /v2/plugin/account，global
//	                                  /v2/plugin/login/account?state=）拿 uid/nickname，
//	                                  stdout 打印完整 token+account JSON（含 realm 键）
//
// 请求形状由 internal/upstream 统一发（对齐官方桌面端后台服务的登录，见 upstream/login.go）：
// 两个 realm 分别走 https://www.workbuddy.cn 与 https://www.workbuddy.ai，不带旧 CLI 的
// Origin/Referer。
//
// state 落盘带 realm，poll 读回校验与命令行 --realm 一致（防混域）。
// 无 PKCE（workbuddy 设备流由服务端签发 state）。
//
// 代理池（网关的 data/proxies.json，只读，--proxies 改路径）：url 先在 --pool 选的池（不写 = 这个域的
// 默认池，"-" = 不绑）里按绑号最少分一个代理（凭证目录 --auths 里的号算进绑号数、守池的上限），登录请求
// 都走它，记进 state 文件；poll 也走它，输出里带上 proxy 绑定，login.sh 写进凭证文件。
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"time"

	auth2 "github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// 登录 state 落盘路径（var 便于测试替换临时文件）
// Portable across OSes: the upstream hardcoded "/tmp/...", which on
// Windows resolves to <drive>:\tmp\... and aborts the OAuth flow with
// "The system cannot find the path specified". os.TempDir() is /tmp on Linux.
var stateFile = filepath.Join(os.TempDir(), "wb2api-login-state.json")

// exitFunc 供测试替换（默认 os.Exit；测试持临时替换为 panic 以进程内捕获 fatal）。
var exitFunc = os.Exit

func fatal(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "login: "+format+"\n", args...)
	exitFunc(1)
}

type loginState struct {
	State string              `json:"state"`
	Realm string              `json:"realm,omitempty"` // url 落盘时写回的 realm，poll 读回校验防混域
	Trace upstream.LoginTrace `json:"trace"`           // 这次登录的链路；旧文件没有时 poll 现生成
	// Pool 登录走的代理（Trace.Proxy）所在的池，poll 输出的绑定里带上；没走代理池为空。
	Pool string `json:"pool,omitempty"`
}

// realm 取值枚举（与 internal/auth 的 Realm() 归一化输出一致）。
const (
	realmCN     = "cn"
	realmGlobal = "global"
)

// parseRealmArgs 解析开头的 --realm=cn|global（或分离式 --realm <v>）flag，缺省 cn。
// 大小写不敏感归一化；非法值/缺值报错。桌椅剩余参数（子命令）顺序不变。
func parseRealmArgs(args []string) (realm string, rest []string, err error) {
	realm = realmCN
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--realm":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--realm requires a value")
			}
			v := strings.ToLower(strings.TrimSpace(args[i+1]))
			if v != realmCN && v != realmGlobal {
				return "", nil, fmt.Errorf("invalid --realm %q (want cn|global)", args[i+1])
			}
			realm = v
			i++
		case strings.HasPrefix(a, "--realm="):
			v := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(a, "--realm=")))
			if v != realmCN && v != realmGlobal {
				return "", nil, fmt.Errorf("invalid --realm %q (want cn|global)", v)
			}
			realm = v
		default:
			rest = append(rest, a)
		}
	}
	return realm, rest, nil
}

// loginOpts 代理池相关的参数（见文件头）。
type loginOpts struct {
	pool, proxies, auths string
}

// parseProxyArgs 从 args 里取出 --pool、--proxies、--auths（--name=v 或 --name v），其余按原顺序返回。
func parseProxyArgs(args []string) (o loginOpts, rest []string, err error) {
	o = loginOpts{proxies: filepath.Join("data", "proxies.json"), auths: "auths"}
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		p := map[string]*string{"--pool": &o.pool, "--proxies": &o.proxies, "--auths": &o.auths}[name]
		if p == nil {
			rest = append(rest, args[i])
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				return loginOpts{}, nil, fmt.Errorf("%s requires a value", name)
			}
			i++
			val = args[i]
		}
		*p = strings.TrimSpace(val)
	}
	return o, rest, nil
}

// openProxies 只读打开代理池文件并装到 up 上（文件不存在 = 没有代理池，登录走默认线路）。
func openProxies(o loginOpts, up *upstream.Client, errOut io.Writer) *proxies.Registry {
	reg, err := proxies.OpenReadOnly(o.proxies)
	if err != nil {
		fatal(errOut, "%v", err)
	}
	up.SetRouter(reg)
	return reg
}

// trackAuths 让代理池知道凭证目录里的号各绑在哪个代理（只读，不改文件）：按绑号最少分代理、守池的上限
// 都要用。读不了的文件提示一声、不算进去。
func trackAuths(reg *proxies.Registry, dir string, errOut io.Writer) {
	files, err := filepath.Glob(filepath.Join(dir, "workbuddy-*.json"))
	if err != nil {
		fatal(errOut, "list auths %s: %v", dir, err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err == nil {
			var a *auth2.Auth
			if a, err = auth2.Parse(raw); err == nil {
				reg.Track(a, false)
				continue
			}
		}
		fmt.Fprintf(errOut, "login: 跳过 %s（不算进代理的绑号数）：%v\n", f, err)
	}
}

// holdProxy 在代理池里给这次登录分一个代理，返回代理 ID 和它所在的池（不走代理池时都为空）。
func holdProxy(reg *proxies.Registry, o loginOpts, realm string, errOut io.Writer) (proxy, pool string) {
	sel := o.pool
	if sel != "" && sel != proxies.NoPool {
		if sel = reg.PoolByName(o.pool); sel == "" {
			fatal(errOut, "代理池 %q 不存在", o.pool)
		}
	}
	_, proxy, err := reg.HoldForLogin(sel, realm)
	if err != nil {
		fatal(errOut, "%v", err)
	}
	for _, px := range reg.View().Proxies {
		if px.ID == proxy {
			pool = px.Pool
			fmt.Fprintf(errOut, "login: 登录走代理「%s」，新号绑在它上面\n", px.Name)
		}
	}
	return proxy, pool
}

// resolveRealmInput 把交互式选域的一行输入归一化为 realm（纯函数，login.sh 交互分支
// 的核心决策，可测）。规则：
//
//	"1"/"cn"（大小写不敏感）/""（回车默认）→ cn
//	"2"/"global" → global
//	其他 → ("", false)（调用方回默认 cn）
func resolveRealmInput(input string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "", "1", "cn":
		return realmCN, true
	case "2", "global":
		return realmGlobal, true
	}
	return "", false
}

// promptRealm 交互式选域：向 out 打印选项提示（out 接 stderr，stdout 留给 realm 本身），
// 从 in 读一行，返回归一化 realm。非法输入警告后回落 cn；EOF（非交互/管道）回落 cn。
func promptRealm(in io.Reader, out io.Writer) string {
	fmt.Fprintln(out, "选择登录版本: 1) 国内版(cn) 2) 国际版(global) [默认 1/cn]: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		// EOF/非交互 → 回落默认 cn
		return realmCN
	}
	if realm, ok := resolveRealmInput(line); ok {
		return realm
	}
	fmt.Fprintln(out, "无效选择，默认国内版 cn")
	return realmCN
}

// validateRealmMatch 校验 state 文件 realm 与命令行 --realm 一致（防混域）：
// state 无 realm（旧文件）放行；非空且不一致 → error。
func validateRealmMatch(stateRealm, cliRealm string) error {
	if stateRealm != "" && stateRealm != cliRealm {
		return fmt.Errorf("realm mismatch: state file realm=%q, command --realm=%q（url 与 poll 需同一 realm）", stateRealm, cliRealm)
	}
	return nil
}

// runURL 执行 url 子命令：在代理池里分一个代理，调 upstream.LoginAuthState 取授权 URL，state 落盘
// （带 realm 和代理），stdout 打印 authURL。out 接 stdout；statePath 为落盘路径（可注入临时文件便于测试）。
// 空 realm 视为缺省（调用方已归一）。
func runURL(realm, statePath string, up *upstream.Client, o loginOpts, out, errOut io.Writer) {
	reg := openProxies(o, up, errOut)
	trackAuths(reg, o.auths, errOut)
	proxy, pool := holdProxy(reg, o, realm, errOut)
	st, err := up.LoginAuthState(realm, proxy)
	if err != nil {
		// 缺 state/authUrl 与上游失败沿用旧的两条文案。
		if errors.Is(err, upstream.ErrLoginAuthStateIncomplete) {
			fatal(errOut, "%v", err)
		}
		fatal(errOut, "auth state failed: %v", err)
	}
	raw, _ := json.Marshal(loginState{State: st.State, Realm: realm, Trace: st.Trace, Pool: pool})
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		fatal(errOut, "write state: %v", err)
	}
	fmt.Fprintln(out, st.AuthURL)
}

// runPoll 执行 poll 子命令：读 state 文件（realm 校验），调 upstream.LoginPollToken 一次，
// 成功再取账号，stdout 打印完整 token+account JSON（走了代理池的带上 proxy 绑定）。轮询、取账号走
// 发起授权时分到的代理。statePath 可注入临时文件便于测试。
func runPoll(realm, statePath string, up *upstream.Client, o loginOpts, out, errOut io.Writer) {
	raw, err := os.ReadFile(statePath)
	if err != nil {
		fatal(errOut, "read state: %v (先跑 login url)", err)
	}
	var ls loginState
	if err := json.Unmarshal(raw, &ls); err != nil {
		fatal(errOut, "parse state: %v", err)
	}
	// 防混域：state 落盘 realm 与命令行 --realm 不一致则拒绝（url 与 poll 必须同域）
	if err := validateRealmMatch(ls.Realm, realm); err != nil {
		fatal(errOut, "%v", err)
	}
	if ls.Trace.Proxy != "" {
		openProxies(o, up, errOut)
	}
	tok, pending, err := up.LoginPollToken(realm, ls.State, ls.Trace)
	if err != nil {
		// 4xx / 业务码 / 空令牌是「还没登录完」；网络错误与 5xx 才是端点故障（与旧口径一致）。
		var le *upstream.LoginError
		if errors.As(err, &le) && le.Status != 0 && le.Status < 500 {
			fatal(errOut, "登录未完成（waiting for login）。请确认已在浏览器完成登录再按 y")
		}
		fatal(errOut, "token endpoint error: %v", err)
	}
	if pending {
		fatal(errOut, "登录未完成（waiting for login）。请确认已在浏览器完成登录再按 y")
	}
	acct, _ := up.LoginFetchAccount(realm, ls.State, tok, ls.Trace)
	res := buildLoginOutput(tok, realm, acct)
	if ls.Trace.Proxy != "" {
		res["proxy"] = auth2.ProxyBinding{Pool: ls.Pool, ID: ls.Trace.Proxy}
	}
	oraw, _ := json.Marshal(res)
	fmt.Fprintln(out, string(oraw))
	os.Remove(statePath)
}

// buildLoginOutput 组装 poll 输出的完整 JSON（login.sh 据此落盘 auth 文件）。
// realm 永不空：显式 --realm 优先（ResolveRealm 处理），否则按上游返回的 domain 推断——
// 保证登录落盘的 auth 文件恒带 realm 键。
func buildLoginOutput(tok upstream.LoginToken, realm string, acct upstream.LoginAccount) map[string]any {
	out := map[string]any{
		"access_token":  tok.AccessToken,
		"refresh_token": tok.RefreshToken,
		"expires_in":    tok.ExpiresIn,
		"domain":        tok.Domain,
		"realm":         auth2.ResolveRealm(realm, tok.Domain),
		"uid":           acct.UID,
		"enterprise_id": acct.EnterpriseID,
		"nickname":      acct.Nickname,
	}
	// 部门全名（只有企业号有）和官方刷新时核对身份用的字段：上游有这个键才输出，login.sh 照样
	// 写进凭证文件（见 auth.Auth.AccountType）。
	for k, v := range map[string]*string{"department_full_name": acct.DepartmentFullName, "account_type": acct.Type,
		"oneid_account_id": acct.OneIDAccountID, "idp": acct.IDP, "token_type": tok.TokenType} {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

func main() {
	// 每个流程独立 cookie jar（oauth.go:22-29：多账号登录互不串会话）
	jar, _ := cookiejar.New(nil)
	up := upstream.New() // 带线路表：登录请求能走代理池里的代理
	up.HTTP.Timeout, up.HTTP.Jar = 30*time.Second, jar
	run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, up)
}

// loginUsage 用法（出错时附在后面）。
const loginUsage = "usage: login [--realm=cn|global] [--pool=池名] [--proxies=data/proxies.json] [--auths=auths] <url|poll|realm>"

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, up *upstream.Client) {
	realm, rest, err := parseRealmArgs(args)
	if err != nil {
		fatal(stderr, "%v (%s)", err, loginUsage)
		return
	}
	o, rest, err := parseProxyArgs(rest)
	if err != nil {
		fatal(stderr, "%v (%s)", err, loginUsage)
		return
	}
	if len(rest) < 1 {
		fatal(stderr, "%s", loginUsage)
		return
	}

	switch rest[0] {
	case "url":
		runURL(realm, stateFile, up, o, stdout, stderr)

	case "poll":
		runPoll(realm, stateFile, up, o, stdout, stderr)

	case "realm":
		// 交互式选域（login.sh 无 --realm 传参且 stdin 为 tty 时调用）。
		// 提示打到 stderr，stdout 只输出归一化 realm，供 $( ) 捕获。
		r := promptRealm(stdin, stderr)
		fmt.Fprintln(stdout, r)

	default:
		fatal(stderr, "unknown subcommand %q (want url|poll|realm)", rest[0])
	}
}
