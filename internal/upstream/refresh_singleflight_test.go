// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// refreshOnlyClient 刷新后取账号（国内版 /v2/plugin/account）统一回本账号，fn 只收刷新请求。
func refreshOnlyClient(fn rtFunc) *Client {
	return testClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v2/plugin/account" {
			return jsonResp(200, `{"code":0,"data":{"uid":"uid-a"}}`), nil
		}
		return fn(r)
	})
}

// TestRefreshTokenSingleFlightPerAccount 验证同一账号并发 refresh 只打一次上游（照官方
// scheduleRefreshSession 的 refreshInFlight）：上游 linguo2625469 #215 报告第二发带已用过的
// RT 会撞 12153 并把整条 RT 链作废，账号只能重新登录。
func TestRefreshTokenSingleFlightPerAccount(t *testing.T) {
	var hits, inFlight, maxInFlight int32
	c := refreshOnlyClient(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&hits, 1)
		n := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if n <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond) // 拉长窗口：无单飞时 8 个调用必然并发
		atomic.AddInt32(&inFlight, -1)
		return jsonResp(200, `{"code":0,"msg":"ok","data":{"accessToken":"at1","refreshToken":"rt1","expiresIn":3600}}`), nil
	})
	a := &auth.Auth{UID: "uid-a", AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 1}

	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = c.RefreshToken(a)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("upstream refresh hits = %d, want 1 (single-flight)", got)
	}
	if got := atomic.LoadInt32(&maxInFlight); got != 1 {
		t.Errorf("max concurrent refresh = %d, want 1", got)
	}
	if a.AccessToken != "at1" || a.RefreshToken != "rt1" {
		t.Errorf("tokens not updated: at=%q rt=%q", a.AccessToken, a.RefreshToken)
	}
}

// TestRefreshTokenSingleFlightIsPerAccount 验证单飞按账号隔离：不同 UID 的刷新
// 并行执行，不被同一把锁串行化（否则 keepalive 全量刷新会退化成串行慢刷）。
func TestRefreshTokenSingleFlightIsPerAccount(t *testing.T) {
	var inFlight, maxInFlight int32
	c := refreshOnlyClient(func(r *http.Request) (*http.Response, error) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if n <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return jsonResp(200, `{"code":0,"data":{"accessToken":"at1","refreshToken":"rt1","expiresIn":3600}}`), nil
	})
	accounts := []*auth.Auth{
		{UID: "uid-a", AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 1},
		{UID: "uid-b", AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 1},
		{UID: "uid-c", AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 1},
	}
	var wg sync.WaitGroup
	for _, a := range accounts {
		wg.Add(1)
		go func(a *auth.Auth) {
			defer wg.Done()
			if err := c.RefreshToken(a); err != nil {
				t.Errorf("refresh %s: %v", a.UID, err)
			}
		}(a)
	}
	wg.Wait()
	if got := atomic.LoadInt32(&maxInFlight); got < 2 {
		t.Errorf("max concurrent refresh across accounts = %d, want >= 2 (per-account single-flight)", got)
	}
}

// TestRefreshAdoptsNewerDiskTokenAfterSessionDead 验证跨进程兜底：内存 RT 已被上游
// 判 12153，但磁盘上的 auth 文件已被另一进程（如 signin 工具）rotate 过时，采纳
// 磁盘 token 重试一次即可自救，而不是直接把账号判死。
func TestRefreshAdoptsNewerDiskTokenAfterSessionDead(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "uid-a.json")
	disk := `{"auth":{"accessToken":"at-disk","refreshToken":"rt-disk","expiresAt":1999999999,"domain":"copilot.tencent.com","realm":"cn"},"account":{"uid":"uid-a"}}`
	if err := os.WriteFile(fp, []byte(disk), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var seen []string
	c := refreshOnlyClient(func(r *http.Request) (*http.Response, error) {
		rt := r.Header.Get("X-Refresh-Token")
		mu.Lock()
		seen = append(seen, rt)
		mu.Unlock()
		if rt != "rt-disk" {
			return &http.Response{
				StatusCode: 401,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"code":12153,"msg":"refresh token is invalid"}`)),
			}, nil
		}
		return jsonResp(200, `{"code":0,"data":{"accessToken":"at2","refreshToken":"rt2","expiresIn":3600}}`), nil
	})
	a := &auth.Auth{UID: "uid-a", AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 1, FilePath: fp}

	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("refresh should recover from disk token: %v", err)
	}
	if a.RefreshToken != "rt2" || a.AccessToken != "at2" {
		t.Errorf("tokens not updated after retry: at=%q rt=%q", a.AccessToken, a.RefreshToken)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "rt0" || seen[1] != "rt-disk" {
		t.Errorf("refresh attempts = %v, want [rt0 rt-disk]", seen)
	}
}

// TestRefreshDoesNotRetryWhenDiskTokenUnchanged 验证兜底不改变失败语义：磁盘 RT 与
// 内存一致（无跨进程刷新）时，12153 原样返回，不会无限重试打上游。
func TestRefreshDoesNotRetryWhenDiskTokenUnchanged(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "uid-a.json")
	disk := `{"auth":{"accessToken":"at0","refreshToken":"rt0","expiresAt":1,"domain":"copilot.tencent.com","realm":"cn"},"account":{"uid":"uid-a"}}`
	if err := os.WriteFile(fp, []byte(disk), 0o600); err != nil {
		t.Fatal(err)
	}
	var hits int32
	c := refreshOnlyClient(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&hits, 1)
		return &http.Response{
			StatusCode: 401,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"code":12153,"msg":"refresh token is invalid"}`)),
		}, nil
	})
	a := &auth.Auth{UID: "uid-a", AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 1, FilePath: fp}

	err := c.RefreshToken(a)
	if err == nil {
		t.Fatal("want error")
	}
	if !isSessionDead(err) {
		t.Errorf("want ErrSessionDead, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("refresh attempts = %d, want 1 (no retry when disk token unchanged)", got)
	}
}

// TestRefreshDiskFallbackUnreadable 凭证文件读不到或解析不了时不重试，原样回 12153。
func TestRefreshDiskFallbackUnreadable(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, fp := range []string{filepath.Join(dir, "missing.json"), bad} {
		var hits int32
		c := refreshOnlyClient(func(r *http.Request) (*http.Response, error) {
			atomic.AddInt32(&hits, 1)
			return jsonResp(401, `{"code":12153,"msg":"refresh token is invalid"}`), nil
		})
		a := &auth.Auth{UID: "uid-a", AccessToken: "at0", RefreshToken: "rt0", ExpiresAt: 1, FilePath: fp}
		if err := c.RefreshToken(a); !isSessionDead(err) {
			t.Errorf("%s: want ErrSessionDead, got %v", fp, err)
		}
		if got := atomic.LoadInt32(&hits); got != 1 {
			t.Errorf("%s: refresh attempts = %d, want 1", fp, got)
		}
	}
}
