// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

// address_test.go 地址层：可信 host、候选排序与备用 IP、失败/成功记忆、可替换点为 nil
// 时不生效、钉 IP Transport 的 Host 与 SNI 保持原域名。测试不连外网。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// addrTestClient 造一个地址层生效的测试 Client：查询返回 query，钉 IP 返回空 200。
func addrTestClient(query func(context.Context, string) []string) *Client {
	return &Client{
		HTTPDNSQuery: query,
		PinIP: func(string) http.RoundTripper {
			return rtFunc(func(*http.Request) (*http.Response, error) {
				return jsonResp(http.StatusOK, ""), nil
			})
		},
	}
}

func TestAddressCandidatesTrustedHostsOnly(t *testing.T) {
	ip1, ip2 := dnsIPv4(1), dnsIPv4(2)
	setDNSConsts(t, nil, nil, nil)
	queried := 0
	c := addrTestClient(func(_ context.Context, host string) []string {
		queried++
		if host != "www.workbuddy.cn" && host != "copilot.tencent.com" && host != "wb.tencentbuddy.com" {
			t.Errorf("对不可信 host %q 发起了查询", host)
		}
		return []string{ip1, ip2}
	})
	for _, host := range []string{"www.workbuddy.cn", "copilot.tencent.com", "wb.tencentbuddy.com"} {
		got := c.addressCandidates(context.Background(), host)
		if len(got) != 2 || got[0] != ip1 || got[1] != ip2 {
			t.Errorf("%s 候选 = %v，应为 [%s %s]", host, got, ip1, ip2)
		}
	}
	for _, host := range []string{"up.example", "192.0.2.9", "www.workbuddy.ai"} {
		if got := c.addressCandidates(context.Background(), host); got != nil {
			t.Errorf("不可信 host %q 候选 = %v，应为空", host, got)
		}
	}
	if queried != 3 {
		t.Errorf("查询次数 = %d，应只查三个可信 host", queried)
	}
}

// TestAddressFallbackOnlyBackupHost 备用 IP 只追加在备用入口上且去重。
func TestAddressFallbackOnlyBackupHost(t *testing.T) {
	ip1, fb1, fb2 := dnsIPv4(1), dnsIPv4(2), dnsIPv4(3)
	setDNSConsts(t, nil, nil, []string{ip1, fb1, fb2})
	c := addrTestClient(func(context.Context, string) []string { return []string{ip1} })

	got := c.addressCandidates(context.Background(), "wb.tencentbuddy.com")
	want := []string{ip1, fb1, fb2}
	if len(got) != len(want) {
		t.Fatalf("备用入口候选 = %v，应为 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("备用入口候选 = %v，应为 %v", got, want)
		}
	}
	for _, host := range []string{"www.workbuddy.cn", "copilot.tencent.com"} {
		got := c.addressCandidates(context.Background(), host)
		if len(got) != 1 || got[0] != ip1 {
			t.Errorf("%s 候选 = %v，不应追加备用 IP", host, got)
		}
	}
}

// TestAddressCandidatesOrder 排序：上次成功的放第一、30 秒内失败的排除、全失败退回原列表。
func TestAddressCandidatesOrder(t *testing.T) {
	ip1, ip2, ip3 := dnsIPv4(1), dnsIPv4(2), dnsIPv4(3)
	setDNSConsts(t, nil, nil, nil)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	c := addrTestClient(func(context.Context, string) []string { return []string{ip1, "bad", ip1, ip2, ip3} })
	c.addr.now = func() time.Time { return now }
	host := "www.workbuddy.cn"

	// 无记忆：去非法、去重保序。
	got := c.addressCandidates(context.Background(), host)
	want := []string{ip1, ip2, ip3}
	if !sameStrings(got, want) {
		t.Fatalf("无记忆候选 = %v，应为 %v", got, want)
	}

	// 上次成功 ip2、ip1 30 秒内失败过 → ip2 第一、ip1 排除。
	c.noteAddrSuccess(host, ip2)
	c.noteAddrFailure(host, ip1)
	if got := c.addressCandidates(context.Background(), host); !sameStrings(got, []string{ip2, ip3}) {
		t.Fatalf("有记忆候选 = %v，应为 [%s %s]", got, ip2, ip3)
	}

	// 全都 30 秒内失败过（含上次成功的 ip2）→ 退回原列表照样全试。
	c.noteAddrFailure(host, ip2)
	c.noteAddrFailure(host, ip3)
	if got := c.addressCandidates(context.Background(), host); !sameStrings(got, []string{ip1, ip2, ip3}) {
		t.Fatalf("全失败候选 = %v，应为原列表 [%s %s %s]", got, ip1, ip2, ip3)
	}

	// 30 秒后失败记忆过期，上次成功的 ip2 回到第一。
	now = now.Add(31 * time.Second)
	if got := c.addressCandidates(context.Background(), host); !sameStrings(got, []string{ip2, ip1, ip3}) {
		t.Fatalf("30 秒后候选 = %v，应为 [%s %s %s]", got, ip2, ip1, ip3)
	}
}

// TestAddressCandidatesDisabled 任一可替换点为 nil 时候选为空。
func TestAddressCandidatesDisabled(t *testing.T) {
	called := false
	query := func(context.Context, string) []string {
		called = true
		return []string{dnsIPv4(1)}
	}
	pin := func(string) http.RoundTripper { return nil }
	for _, c := range []*Client{
		{HTTPDNSQuery: query},
		{PinIP: pin},
		{},
	} {
		if got := c.addressCandidates(context.Background(), "www.workbuddy.cn"); got != nil {
			t.Errorf("地址层不生效时候选 = %v，应为空", got)
		}
	}
	if called {
		t.Error("地址层不生效时不应调用查询函数")
	}
}

// TestPinTransportKeepsHostAndSNI 钉 IP 只换 TCP 目标：Host 头与 TLS SNI 仍是原域名。
func TestPinTransportKeepsHostAndSNI(t *testing.T) {
	var gotHost, gotSNI string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotSNI = r.TLS.ServerName
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().(*net.TCPAddr)

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	base := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	rt := pinTransport(base, addr.IP.String())
	url := fmt.Sprintf("https://example.com:%d/v3/config", addr.Port)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("钉 IP 请求: %v", err)
	}
	resp.Body.Close()
	if want := fmt.Sprintf("example.com:%d", addr.Port); gotHost != want {
		t.Errorf("Host = %q，应为 %q", gotHost, want)
	}
	if gotSNI != "example.com" {
		t.Errorf("TLS SNI = %q，应为 example.com", gotSNI)
	}
}

// TestPinTransportNilBase base 为 nil 时照样能建；拨号地址没端口时报错。
func TestPinTransportNilBase(t *testing.T) {
	tr := pinTransport(nil, "127.0.0.1")
	if _, err := tr.DialContext(context.Background(), "tcp", "example.com"); err == nil {
		t.Error("拨号地址没端口应报错")
	}
}

// TestAddressPinMemoMax 钉 IP 备忘最多 256 条，满了删最早放进去的。
func TestAddressPinMemoMax(t *testing.T) {
	var c Client
	for i := 0; i < addressPinMemoMax+10; i++ {
		c.addr.rememberPin(fmt.Sprintf("rid-%d", i), "wb.tencentbuddy.com", dnsIPv4(1))
	}
	if _, ok := c.addr.pinFor("rid-0"); ok {
		t.Error("最早的备忘应被删掉")
	}
	if p, ok := c.addr.pinFor(fmt.Sprintf("rid-%d", addressPinMemoMax+9)); !ok || p.host != "wb.tencentbuddy.com" {
		t.Errorf("最新备忘 = %+v ok=%v", p, ok)
	}
	c.addr.mu.Lock()
	n := len(c.addr.pins)
	c.addr.mu.Unlock()
	if n != addressPinMemoMax {
		t.Errorf("备忘条数 = %d，应为 %d", n, addressPinMemoMax)
	}
}

// TestAddressSwitchInFlight 在途换入口表的登记与删除。
func TestAddressSwitchInFlight(t *testing.T) {
	var c Client
	if c.addr.switchInFlight("rid") {
		t.Fatal("未登记时不应在途")
	}
	if !c.addr.beginSwitch("rid") {
		t.Fatal("首次登记应成功")
	}
	if !c.addr.switchInFlight("rid") {
		t.Fatal("登记后应在途")
	}
	if c.addr.beginSwitch("rid") {
		t.Fatal("同 ID 重复登记应失败")
	}
	c.addr.endSwitch("rid")
	if c.addr.switchInFlight("rid") {
		t.Fatal("删除后不应在途")
	}
}

// TestAddressConcurrent 并发读写地址记忆、在途表与备忘，-race 下不报竞态。
func TestAddressConcurrent(t *testing.T) {
	ip1, ip2, ip3 := dnsIPv4(1), dnsIPv4(2), dnsIPv4(3)
	setDNSConsts(t, nil, nil, nil)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	c := addrTestClient(func(context.Context, string) []string { return []string{ip1, ip2, ip3} })
	c.addr.now = func() time.Time { return now }
	host := "www.workbuddy.cn"

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = c.addressCandidates(context.Background(), host)
				c.noteAddrFailure(host, ip1)
				c.noteAddrSuccess(host, ip2)
				c.addr.rememberPin("rid", host, ip1)
				c.addr.beginSwitch("rid")
				c.addr.endSwitch("rid")
			}
		}()
	}
	wg.Wait()
}

// sameStrings 按顺序比较字符串列表。
func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
