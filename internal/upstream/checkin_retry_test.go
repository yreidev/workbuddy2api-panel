// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// checkin_retry_test.go 钉住签到/余额维护类计费调用的瞬时错误有界重试：
// 上游 5xx（实测偶发 code 10000 / http 500）与网络抖动重试，业务错误（已签到/
// 参数错）不重试——重试只会原样再失败一次。
package upstream

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// shortBillingRetry 测试用重试间隔（生产 2s 会让单测秒级膨胀）。
func shortBillingRetry(t *testing.T) {
	t.Helper()
	old := billingRetryDelay
	billingRetryDelay = time.Millisecond
	t.Cleanup(func() { billingRetryDelay = old })
}

// checkinClient 签到状态固定回「活动进行中、今天未签」，其余请求交给 fn（只数签到本身）。
func checkinClient(fn func(r *http.Request) (*http.Response, error)) *Client {
	return testClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == checkinStatusPath {
			return jsonResp(200, `{"code":0,"data":{"active":true,"today_checked_in":false}}`), nil
		}
		return fn(r)
	})
}

func TestDailyCheckinRetriesTransient500(t *testing.T) {
	shortBillingRetry(t)
	var calls int32
	c := checkinClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != dailyCheckinPath {
			return nil, errors.New("wrong path")
		}
		if atomic.AddInt32(&calls, 1) == 1 {
			return jsonResp(500, `{"code":10000,"msg":"API request failed with status code: 500"}`), nil
		}
		return jsonResp(200, `{"code":0,"data":{}}`), nil
	})
	if err := c.DailyCheckin(&auth.Auth{AccessToken: "at"}); err != nil {
		t.Fatalf("首次 500 应重试成功，err=%v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("calls=%d want 2（1 次失败 + 1 次重试）", n)
	}
}

func TestDailyCheckinRetriesExhausted(t *testing.T) {
	shortBillingRetry(t)
	var calls int32
	c := checkinClient(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return jsonResp(500, `{"code":10000,"msg":"API request failed with status code: 500"}`), nil
	})
	err := c.DailyCheckin(&auth.Auth{AccessToken: "at"})
	if err == nil {
		t.Fatal("持续 500 应返回错误")
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Fatalf("calls=%d want 3（1 次 + 2 次重试封顶）", n)
	}
}

func TestDailyCheckinNoRetryOnBusinessError(t *testing.T) {
	shortBillingRetry(t)
	var calls int32
	c := checkinClient(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		// 「今天已签到」是业务幂等拒绝（code!=0），重试无意义。
		return jsonResp(200, `{"code":14001,"msg":"今日已签到"}`), nil
	})
	err := c.DailyCheckin(&auth.Auth{AccessToken: "at"})
	if err == nil || !IsAlreadyCheckin(err) {
		t.Fatalf("err=%v want already-checkin business error", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("calls=%d want 1（业务错误不重试）", n)
	}
}

func TestDailyCheckinNoRetryOn4xx(t *testing.T) {
	shortBillingRetry(t)
	var calls int32
	c := checkinClient(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return jsonResp(403, `{"code":11140,"msg":"request illegal"}`), nil
	})
	if err := c.DailyCheckin(&auth.Auth{AccessToken: "at"}); err == nil {
		t.Fatal("403 应返回错误")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("calls=%d want 1（4xx 非瞬时，不重试）", n)
	}
}

func TestUserResourceDetailedRetriesTransient500(t *testing.T) {
	shortBillingRetry(t)
	var calls int32
	c := testClient(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case resourceSummaryPath:
			return jsonResp(200, `{"code":0,"data":{"Packages":[{"CycleTotalCapacity":"100","CycleRemainCapacity":"60"}]}}`), nil
		case resourcePaidPath:
			return jsonResp(200, resourceList()), nil
		case resourceFreePath:
		default:
			return nil, errors.New("wrong path")
		}
		if atomic.AddInt32(&calls, 1) == 1 {
			return jsonResp(500, `{"code":10000,"msg":"API request failed with status code: 500"}`), nil
		}
		return jsonResp(200, resourceList(`{"PackageName":"p","CycleCapacitySizePrecise":"100","CycleCapacityRemainPrecise":"60"}`)), nil
	})
	remain, _, _, _, _, err := c.UserResourceDetailedWithExpiry(&auth.Auth{AccessToken: "at"}, 0)
	if err != nil || remain != 60 {
		t.Fatalf("remain=%d err=%v, want 60 nil", remain, err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("calls=%d want 2（1 次失败 + 1 次重试）", n)
	}
}

func TestDailyCheckinNoRetryOnRouteErrorAndCanceled(t *testing.T) {
	shortBillingRetry(t)
	routeNetErr := &RouteError{Proxy: "p1", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}
	routeTimeoutErr := &RouteError{Proxy: "p1", Err: context.DeadlineExceeded}
	if isTransientBillingErr(routeNetErr) || isTransientBillingErr(routeTimeoutErr) {
		t.Fatal("RouteError 无论内部包何种网络错都不得视为可重试瞬时错")
	}
	if isTransientBillingErr(context.Canceled) {
		t.Fatal("context.Canceled 不得重试")
	}

	var calls int32
	c := checkinClient(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return nil, routeTimeoutErr
	})
	err := c.DailyCheckin(&auth.Auth{AccessToken: "at"})
	if !IsRouteError(err) {
		t.Fatalf("err=%v want RouteError", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("calls=%d want 1（代理线路错误不得重试）", n)
	}
}
