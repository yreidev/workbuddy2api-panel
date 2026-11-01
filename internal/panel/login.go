// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// login.go 面板内嵌的 WorkBuddy CN OAuth 设备授权流程（cmd/login 的进程内移植）。
//
//	POST /panel/api/login/start → 拿 state+authUrl，state 存进程内（不再落 /tmp，
//	  原方案在 Windows 上不可用），返回授权 URL；
//	GET  /panel/api/login/poll   → 面板前端每 3s 轮询本接口；未完成返回 done=false，
//	  完成后取 uid/nickname、凭证落盘 auths/workbuddy-<uid>.json、热加载进池
//	  （pool.Add + Revive），并顺带签到 + 余额刷新 —— 免重启加载新账号。
//
// 无 PKCE（workbuddy 设备流由服务端签发 state），请求形状由 internal/upstream 统一发
// （与官方桌面端后台服务的登录一致，见 upstream/login.go），与 cmd/login 共用。
package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// validUID 校验上游返回的 uid 是否可安全用于拼文件名。
// 只放行字母、数字、下划线、连字符（腾讯侧 uid 实测为 UUID 形态），
// 长度上限 64 兜底异常超长串；拒绝 . / \ 等路径字符与空串。
func validUID(uid string) bool {
	if uid == "" || len(uid) > 64 {
		return false
	}
	for _, c := range uid {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// loginStartError 发起授权的错误文案：保留旧形状「auth state (upstream <状态码>): <原因>」，
// 传输层错误的状态码按 0 记；缺 state/authUrl 原样返回（旧路径也是这个文案）。
// 账号授权被拒（席位已满等）照官方直接给上游的提示，前面加中文原因和业务码。
func loginStartError(err error) string {
	if errors.Is(err, upstream.ErrLoginAuthStateIncomplete) {
		return err.Error()
	}
	status := 0
	var le *upstream.LoginError
	if errors.As(err, &le) {
		status = le.Status
		if reason := le.LicenseReason(); reason != "" {
			if le.Msg == "" {
				return fmt.Sprintf("登录被拒：%s（code=%d）", reason, le.Code)
			}
			return fmt.Sprintf("登录被拒：%s（code=%d）：%s", reason, le.Code, le.Msg)
		}
	}
	return fmt.Sprintf("auth state (upstream %d): %v", status, err)
}

// loginStart 发起设备授权：POST auth/state 拿授权 URL。
// body 可带 {"realm":"global"}（缺省 cn）；state 会话记 realm，poll 同 realm 落盘。
// 装了代理池时还可带 pool（空 = 这个域的默认池，"-" = 不绑，其余是池 ID）：先在池里占一个代理，
// 登录请求走它，登录成功后新号就绑在它上面。
func (p *Panel) loginStart(w http.ResponseWriter, r *http.Request) {
	realm, pool := "cn", ""
	if r.Body != nil {
		var reqBody struct {
			Realm string `json:"realm"`
			Pool  string `json:"pool"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&reqBody); err == nil {
			if reqBody.Realm == "global" {
				realm = "global"
			}
			pool = reqBody.Pool
		}
	}
	// 顺手回收过期会话，防"开弹窗走开"的 state 滞留；它们在代理池里占的位子先放掉，下面才占得到。
	p.loginMu.Lock()
	var stale []string
	for s, sess := range p.logins {
		if time.Since(sess.created) > loginTTL {
			stale = append(stale, sess.hold)
			delete(p.logins, s)
		}
	}
	p.loginMu.Unlock()
	for _, h := range stale {
		p.releaseLogin(h)
	}
	hold, proxy := "", ""
	if p.cfg.Proxies != nil {
		var err error
		if hold, proxy, err = p.cfg.Proxies.HoldForLogin(pool, realm); err != nil {
			writeProxyErr(w, err)
			return
		}
	}
	st, err := p.cfg.Upstream.LoginAuthState(realm, proxy)
	if err != nil {
		p.releaseLogin(hold)
		writeErr(w, http.StatusBadGateway, loginStartError(err))
		return
	}
	p.loginMu.Lock()
	p.logins[st.State] = loginSession{created: time.Now(), realm: realm, trace: st.Trace, pool: pool, hold: hold, proxy: proxy}
	p.loginMu.Unlock()
	log.Printf("panel: 发起 OAuth 添加账号 realm=%s（state=%s...）", realm, st.State[:min(8, len(st.State))])
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": st.AuthURL, "state": st.State, "realm": realm})
}

// loginPoll 轮询登录态。未完成 → {done:false}；完成 → 建凭证、落盘、热加载、签到。
func (p *Panel) loginPoll(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		writeErr(w, http.StatusBadRequest, "missing state")
		return
	}
	p.loginMu.Lock()
	sess, known := p.logins[state]
	p.loginMu.Unlock()
	if !known {
		writeErr(w, http.StatusNotFound, "unknown or expired state（请重新发起添加账号）")
		return
	}
	// auth/token 未登录完成时上游回业务码 11217（pending）；其他错误照旧当等待处理，
	// 面板前端继续轮询。
	tok, pending, err := p.cfg.Upstream.LoginPollToken(sess.realm, state, sess.trace)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"done": false, "message": err.Error()})
		return
	}
	if pending {
		writeJSON(w, http.StatusOK, map[string]any{"done": false, "message": tok.Msg})
		return
	}

	// 完成：取 uid/nickname（失败不阻塞，仅缺展示名）。
	acct, err := p.cfg.Upstream.LoginFetchAccount(sess.realm, state, tok, sess.trace)
	if err != nil || acct.UID == "" {
		writeErr(w, http.StatusBadGateway, "login done but no uid（token 已发但账号信息获取失败，请重试）")
		return
	}
	// UID 来自上游响应，未经校验就用于拼文件名会被路径穿越利用
	// （filepath.Join("./auths", "workbuddy-../../evil.json") → auths/evil.json）。
	// UID 是腾讯侧账号标识，实测为 UUID（十六进制与连字符），故只放行 [A-Za-z0-9_-]。
	if !validUID(acct.UID) {
		writeErr(w, http.StatusBadGateway, "上游返回的 uid 含非法字符，拒绝落盘（防路径穿越）")
		return
	}

	// 凭证落盘（嵌套形，与 auths/ 目录既有格式一致）→ 热加载进池。
	if err := os.MkdirAll(p.cfg.AuthDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "mkdir auth dir: "+err.Error())
		return
	}
	a := &auth.Auth{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix(),
		Domain:       tok.Domain,
		UID:          acct.UID,
		EnterpriseID: acct.EnterpriseID,
		Nickname:     acct.Nickname,
		FilePath:     filepath.Join(p.cfg.AuthDir, fmt.Sprintf("workbuddy-%s.json", acct.UID)),
	}
	acct.ApplyTo(a, tok)
	// global 登录：落盘 auth.realm=global（Realm() 按此判域；不写则依赖 domain 后缀回落）。
	if sess.realm == "global" {
		if _, err := auth.BackfillRealmFor(a, "global"); err != nil {
			writeErr(w, http.StatusInternalServerError, "set realm: "+err.Error())
			return
		}
	} else {
		// CN 也显式补 realm 键（幂等），让 auth 文件形态统一（与 LoadDir 存量迁移对齐）。
		_, _ = a.BackfillRealm()
	}
	// 代理：原来就在号池里的号沿用原来的绑定（重新登录只是换凭证）；新号绑在登录走的那个代理上。
	_, existing := p.cfg.Pool.Status(acct.UID)
	if p.cfg.Proxies != nil {
		if existing {
			a.SetProxyBinding(p.cfg.Proxies.Binding(acct.UID))
		} else if sess.proxy != "" {
			a.SetProxyBinding(auth.ProxyBinding{ID: sess.proxy})
		}
	}
	if err := a.SaveAtomic(); err != nil {
		writeErr(w, http.StatusInternalServerError, "save auth: "+err.Error())
		return
	}
	p.cfg.Pool.Add(a)
	p.cfg.Pool.Revive(acct.UID) // 全新登录 = 人工恢复口径：清掉旧号遗留的禁用/冷却/熔断
	var proxyView proxies.AccountView
	if p.cfg.Proxies != nil {
		if existing || sess.pool == proxies.NoPool {
			p.cfg.Proxies.Track(a, false)
		} else {
			p.cfg.Proxies.TrackInto(a, sess.pool) // 占的代理没了就在这个池（没选池就是默认池）里重新分
		}
		p.releaseLogin(sess.hold)
		proxyView = p.cfg.Proxies.AccountViews()[acct.UID]
	}

	// 顺带签到 + 余额刷新（幂等；失败不影响登录结果，只体现在返回字段里）。
	// realm 分支：CN 走 DailyCheckin；global 无 CN 签到体系，改为注册激活 + trial 领取
	// （D4 门控同 scheduler：CN 任务端点对 global 不发起任何调用）。
	// 号现在不能出站（代理不通、在等代理）就先不发，等代理好了定时任务会补上。
	checkinMsg := ""
	remain := int64(-1)
	total := int64(0)
	if proxyView.Blocked != "" {
		checkinMsg = "先不签到：" + proxyView.Blocked
	} else if sess.realm == "global" {
		// 注册激活（幂等）：region required 时自动补地区（白名单首个，HK）后重新激活。
		// 失败不阻断登录结果（auth 已落盘），只在返回字段里体现。
		if activated, err := p.cfg.Upstream.GlobalCompleteRegistration(a); err != nil {
			checkinMsg = "注册激活失败: " + err.Error()
			log.Printf("panel: global 注册激活 uid=%s: %v", acct.UID, err)
		} else if activated {
			log.Printf("panel: global 注册激活 uid=%s 完成", acct.UID)
		}
		// trial 加油包（幂等 14051 = 已领过，非错误）。
		if claimed, err := p.cfg.Upstream.ClaimTrial(a); err != nil {
			checkinMsg = joinMsg(checkinMsg, "trial 领取失败: "+err.Error())
			log.Printf("panel: global trial uid=%s: %v", acct.UID, err)
		} else if claimed {
			log.Printf("panel: global trial uid=%s 已领", acct.UID)
		}
	} else if !a.IsEnterprise() {
		// 企业版跳过签到：上游 POST /v2/billing/meter/daily-checkin 对企业号
		// 400 code 10001「企业账号不支持该操作」，发了只会把该错误写进登录返回。
		// 下方 UserResource 余额查询照常（企业额度走 get-enterprise-user-usage 口径）。
		if err := p.cfg.Upstream.DailyCheckin(a); err != nil {
			checkinMsg = err.Error()
		}
	}
	if proxyView.Blocked == "" {
		if rm, tt, err := p.cfg.Upstream.UserResource(a); err == nil {
			remain, total = rm, tt
			p.cfg.Pool.ReenableIfCredits(acct.UID, rm, tt)
		}
	}

	p.loginMu.Lock()
	delete(p.logins, state)
	p.loginMu.Unlock()
	log.Printf("panel: 新账号已热加载 uid=%s nickname=%q realm=%s（免重启生效）", acct.UID, acct.Nickname, sess.realm)
	writeJSON(w, http.StatusOK, map[string]any{
		"done":            true,
		"uid":             acct.UID,
		"nickname":        acct.Nickname,
		"realm":           sess.realm,
		"credits":         remain,
		"credits_total":   total,
		"checkin_message": checkinMsg,
		// proxy 号的代理情况（没装代理池或没绑时是零值）；proxy_kept 号原来就在号池里，沿用了原来的绑定。
		"proxy":      proxyView,
		"proxy_kept": existing && p.cfg.Proxies != nil,
	})
}

// releaseLogin 放掉登录在代理池里占的位子（没装代理池或没占位时什么都不做）。
func (p *Panel) releaseLogin(hold string) {
	if p.cfg.Proxies != nil {
		p.cfg.Proxies.ReleaseLogin(hold)
	}
}

// joinMsg 拼接 login 完成后的提示消息（多段用「；」连接，空段跳过）。
func joinMsg(parts ...string) string {
	out := ""
	for _, s := range parts {
		if s == "" {
			continue
		}
		if out != "" {
			out += "；"
		}
		out += s
	}
	return out
}

// loginRegions 返回 global 注册可选地区（panel 前端选地区弹窗用；CN 不调用）。
// 未持账号时返回白名单静态兜底（前端只读展示，不依赖上游）。
func (p *Panel) loginRegions(w http.ResponseWriter, r *http.Request) {
	// 静态白名单（对齐国际版 web 展示集）：面板前端只读展示，无需账号态。
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"regions": []map[string]string{
			{"code": "HK", "name": "Hong Kong"},
			{"code": "MO", "name": "Macao"},
			{"code": "SG", "name": "Singapore"},
			{"code": "TH", "name": "Thailand"},
			{"code": "PH", "name": "Philippines"},
			{"code": "MY", "name": "Malaysia"},
			{"code": "ID", "name": "Indonesia"},
		},
	})
}
