// profile_test.go 钉住账号资料拉取（issue #94）：Bearer + web 平台头可访问
// /console/account；只解析 nickname，uid 不一致防串号；业务错误原样返回。
package upstream

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func TestFetchAccountProfile(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/console/account") {
			return nil, errors.New("wrong path")
		}
		if r.Header.Get("Authorization") == "" || r.Header.Get("x-client-platform") != "web" {
			return nil, errors.New("missing bearer / web platform header")
		}
		// 响应刻意带 phoneNumber：方法必须只解析 nickname/uid，敏感字段不得进入返回值。
		return jsonResp(200, `{"code":0,"msg":"OK","data":{"uid":"u1","nickname":"新名字","phoneNumber":"13800000000"}}`), nil
	})
	c.WebBaseCN = "https://web.example"
	nick, err := c.FetchAccountProfile(&auth.Auth{UID: "u1", AccessToken: "at"})
	if err != nil || nick != "新名字" {
		t.Fatalf("nick=%q err=%v, want 新名字 nil", nick, err)
	}
}

func TestFetchAccountProfileUIDMismatch(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"code":0,"data":{"uid":"someone-else","nickname":"x"}}`), nil
	})
	c.WebBaseCN = "https://web.example"
	if _, err := c.FetchAccountProfile(&auth.Auth{UID: "u1", AccessToken: "at"}); err == nil {
		t.Fatal("uid 不一致应报错（防串号）")
	}
}

func TestFetchAccountProfileBusinessError(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(401, `{"code":1002,"msg":"unauthorized"}`), nil
	})
	c.WebBaseCN = "https://web.example"
	if _, err := c.FetchAccountProfile(&auth.Auth{UID: "u1", AccessToken: "bad"}); err == nil {
		t.Fatal("401 应返回错误")
	}
}
