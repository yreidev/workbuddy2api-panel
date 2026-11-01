// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

// failover_more_test.go 失败切换的几条补充规则：chatBase 直接配成备用入口时只换 IP；
// 拉配置业务码非 0 且 data 无效时照样换；登录与刷新重发时超时重新计时。不连外网。

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// TestChatFailoverBackupBase chatBase 直接配成备用入口：不换入口，502 后按地址层依次钉
// HTTPDNS 候选和备用 IP（官方对备用入口也只做地址层）。
func TestChatFailoverBackupBase(t *testing.T) {
	ip1, ipF := dnsIPv4(21), dnsIPv4(22)
	setDNSConsts(t, nil, nil, []string{ipF})
	f := newUpFake(t)
	f.onHost(foBackupHost, http.MethodPost, chatCompletionsPath, 502, `{"code":502}`)
	c := failoverClient(f)
	c.ChatBaseCN = failoverBackupOrigin
	log := foAddress(c, func(_ context.Context, host string) []string {
		if host == foBackupHost {
			return []string{ip1}
		}
		return nil
	}, func(ip string) (*http.Response, error) {
		if ip == ipF {
			return jsonResp(200, "data: ok\n\n"), nil
		}
		return jsonResp(502, `{"code":502}`), nil
	})

	rc, status, _, err := foChat(c, nil)
	if err != nil || status != 200 {
		t.Fatalf("应在备用 IP 上成功: status=%d err=%v", status, err)
	}
	if body := foReadAll(t, rc); body != "data: ok\n\n" {
		t.Fatalf("body = %q", body)
	}
	if hosts := foHosts(f); len(hosts) != 1 || hosts[0] != foBackupHost {
		t.Fatalf("未钉 IP 的请求 = %v，应只发一次备用入口", hosts)
	}
	if got := log.all(); !sameStrings(got, []string{ip1, ipF}) {
		t.Fatalf("钉 IP 顺序 = %v", got)
	}
}

// TestConfigFetchBackupBase chatBase 是备用入口：拉配置失败后直接试备用 IP（官方首选入口
// 不是前门时跳过换入口）。
func TestConfigFetchBackupBase(t *testing.T) {
	f := &cfgFake{
		backup:    cfgResp{status: http.StatusServiceUnavailable, body: `{"code":503}`},
		backupPin: cfgResp{status: http.StatusOK, body: cfgValidBody},
	}
	c := cfgClient(f)
	c.ChatBaseCN = failoverBackupOrigin
	out, err := c.fetchV3ConfigModelMap(upAuth())
	if err != nil {
		t.Fatalf("fetchV3ConfigModelMap: %v", err)
	}
	if _, ok := out["m1"]; !ok {
		t.Fatalf("目录 = %+v", out)
	}
	reqs := f.seen()
	if len(reqs) != 2 || reqs[0].pinned || !reqs[1].pinned || reqs[1].host != foBackupHost {
		t.Fatalf("请求 = %+v，应为备用入口一次、再钉备用 IP 一次", reqs)
	}
}

// TestConfigFetchBusinessCodeEmptyData 200 但业务码非 0、data 为空：官方只看 data，按无效响应
// 换入口；data 有内容而业务码非 0 时照旧报错、不换。
func TestConfigFetchBusinessCodeEmptyData(t *testing.T) {
	t.Run("data 为空就换", func(t *testing.T) {
		f := &cfgFake{
			main:   cfgResp{status: http.StatusOK, body: `{"code":11001,"msg":"busy","data":null}`},
			backup: cfgResp{status: http.StatusOK, body: cfgValidBody},
		}
		if _, err := cfgClient(f).fetchV3ConfigModelMap(upAuth()); err != nil {
			t.Fatalf("应换到备用入口成功: %v", err)
		}
		if n := len(f.seen()); n != 2 {
			t.Fatalf("请求数 = %d，应为 2", n)
		}
	})
	t.Run("data 有内容不换", func(t *testing.T) {
		f := &cfgFake{main: cfgResp{status: http.StatusOK, body: `{"code":11001,"data":{"models":[{"id":"m1"}]}}`}}
		if _, err := cfgClient(f).fetchV3ConfigModelMap(upAuth()); err == nil {
			t.Fatal("业务码非 0 应报错")
		}
		if n := len(f.seen()); n != 1 {
			t.Fatalf("请求数 = %d，应只发一次", n)
		}
	})
}

// TestLoginRetryTimeoutResets 换 IP 重发时超时重新计时：第一次用掉一段时间后，第二次的
// 剩余时间仍接近整段超时（官方每次重发都是一个新请求）。
func TestLoginRetryTimeoutResets(t *testing.T) {
	const timeout = 2 * time.Second
	const spent = 200 * time.Millisecond
	old := loginStateTimeout
	loginStateTimeout = timeout
	t.Cleanup(func() { loginStateTimeout = old })
	f := &loginFoFake{handler: func(path string, pinned bool, _ *http.Request) (*http.Response, error) {
		if !pinned {
			time.Sleep(spent)
			return jsonResp(http.StatusBadGateway, `{"code":502}`), nil
		}
		return jsonResp(http.StatusOK, loginFoStateBody), nil
	}}
	c := loginFoClient(f)
	if _, err := c.LoginAuthState("cn", ""); err != nil {
		t.Fatalf("LoginAuthState: %v", err)
	}
	reqs := f.seen()
	if len(reqs) != 2 || !reqs[1].pinned || !reqs[1].hasDeadline {
		t.Fatalf("请求 = %+v，应为 502 后钉 IP 重发", reqs)
	}
	if reqs[1].remaining < timeout-spent/4 {
		t.Fatalf("重发的剩余时间 %v，应重新计时（接近 %v）", reqs[1].remaining, timeout)
	}
}

// TestRefreshRetryTimeoutResets 刷新令牌换 IP 重发时同样重新计时。
func TestRefreshRetryTimeoutResets(t *testing.T) {
	const timeout = 2 * time.Second
	const spent = 200 * time.Millisecond
	old := refreshTimeoutCN
	refreshTimeoutCN = timeout
	t.Cleanup(func() { refreshTimeoutCN = old })
	f := &loginFoFake{handler: func(path string, pinned bool, _ *http.Request) (*http.Response, error) {
		switch path {
		case loginFoRefreshPath:
			if !pinned {
				time.Sleep(spent)
				return jsonResp(http.StatusServiceUnavailable, `{"code":503}`), nil
			}
			return jsonResp(http.StatusOK, loginFoRefreshBody), nil
		case loginFoAccountPath:
			return jsonResp(http.StatusOK, loginFoAccountBody), nil
		}
		return jsonResp(http.StatusNotFound, `{}`), nil
	}}
	c := loginFoClient(f)
	c.ChatBaseCN = cnProductEndpoint
	a := &auth.Auth{UID: "u-1", AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 111}
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	reqs := f.seen()
	if len(reqs) < 2 || reqs[1].path != loginFoRefreshPath || !reqs[1].pinned {
		t.Fatalf("请求 = %+v，应为 503 后钉 IP 重发", reqs)
	}
	if reqs[1].remaining < timeout-spent/4 {
		t.Fatalf("重发的剩余时间 %v，应重新计时（接近 %v）", reqs[1].remaining, timeout)
	}
}
