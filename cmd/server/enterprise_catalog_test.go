// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRefreshEnterpriseCatalogsOnce 只给国内版企业号拉目录；目录还新鲜时不重拉，过期才重拉；
// 被代理池拦着时不拉、保留；账号禁用后它的目录被清掉。
func TestRefreshEnterpriseCatalogsOnce(t *testing.T) {
	var calls atomic.Int32
	up := &upstream.Client{
		HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			body := `{"code":0,"data":{"models":[{"id":"claude-x"}],"agents":[{"name":"cli","models":["claude-x"]}]}}`
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
		ChatBaseCN:    "https://cn.fake",
		BillingBaseCN: "https://cn.fake",
	}
	p := pool.New("")
	p.Add(&auth.Auth{UID: "ent1", EnterpriseID: "ent-1", AccessToken: "at", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "per1", AccessToken: "at", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"})
	ctx := context.Background()
	now := time.Now()

	refreshEnterpriseCatalogsOnce(ctx, up, p, now)
	if _, ok := up.AccountCatalogFetched("ent1"); !ok {
		t.Fatal("企业号应拉到目录")
	}
	if _, ok := up.AccountCatalogFetched("per1"); ok {
		t.Fatal("个人号不单独拉目录")
	}
	first := calls.Load()
	if first == 0 {
		t.Fatal("应向上游拉目录")
	}

	refreshEnterpriseCatalogsOnce(ctx, up, p, now.Add(time.Minute))
	if calls.Load() != first {
		t.Error("目录还新鲜时不应重拉")
	}
	// 被代理池拦着：请求出不去，不拉也不清，留着上次的目录。
	p.SetProxyBlocked("ent1", "代理「甲」连不上")
	refreshEnterpriseCatalogsOnce(ctx, up, p, now.Add(enterpriseCatalogMaxAge+time.Minute))
	if _, ok := up.AccountCatalogFetched("ent1"); !ok || calls.Load() != first {
		t.Error("被拦的企业号不拉目录、保留上次的")
	}
	p.SetProxyBlocked("ent1", "")
	refreshEnterpriseCatalogsOnce(ctx, up, p, now.Add(enterpriseCatalogMaxAge+time.Minute))
	if calls.Load() == first {
		t.Error("目录过期后应重拉")
	}

	p.Disable("ent1", "test")
	refreshEnterpriseCatalogsOnce(ctx, up, p, time.Now())
	if _, ok := up.AccountCatalogFetched("ent1"); ok {
		t.Error("禁用的企业号目录应被清掉")
	}
}
