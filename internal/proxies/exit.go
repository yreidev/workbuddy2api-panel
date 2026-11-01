// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package proxies

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// builtinExitURLs 内置的查出口 IP 地址，照 sub2api（backend/internal/repository/proxy_probe_service.go）：
// 先 ip-api.com（带地区和运营商，中文），查不到再 ipify（只有 IP）。免费的 ip-api 只有 http，每分钟
// 限 45 次。官方没有回显客户端 IP 的接口，所以只能问第三方；地址可以在设置里换成自己的。
var builtinExitURLs = []string{
	"http://ip-api.com/json/?lang=zh-CN",
	"http://api64.ipify.org?format=json",
}

// exitBodyMax 查出口 IP 的回应最多读多少。
const exitBodyMax = 64 << 10

// probeExit 经代理 rawURL（为空就直连）按顺序请求查出口 IP 的地址，第一个查到的为准；都没查到时
// Exit.Error 记下每个地址的原因。这是发给第三方的请求，不用伪装官方的 TLS 指纹，直接用标准库。
func probeExit(ctx context.Context, rawURL string, urls []string) *Exit {
	if len(urls) == 0 {
		urls = builtinExitURLs
	}
	tr := &http.Transport{DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	if rawURL != "" {
		u, err := url.Parse(rawURL)
		if err != nil {
			return &Exit{Error: "代理地址不对"}
		}
		tr.Proxy = http.ProxyURL(u)
	}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, Timeout: 15 * time.Second}
	var errs []string
	for _, u := range urls {
		e, err := fetchExit(ctx, hc, u)
		if err == nil {
			e.Source = u
			return e
		}
		errs = append(errs, hostOf(u)+"："+describe(err))
	}
	return &Exit{Error: strings.Join(errs, "；")}
}

func fetchExit(ctx context.Context, hc *http.Client, u string) (*Exit, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, errors.New("地址不对")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, exitBodyMax))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return parseExit(body)
}

// parseExit 认三种回应：JSON（ip-api 的 query、country、regionName、city、isp，ipify 的 ip，别的带 ip
// 字段的也认）、Cloudflare trace 那样一行一个 key=value（ip=、loc=）、只有一个 IP 的纯文本。
func parseExit(body []byte) (*Exit, error) {
	body = bytes.TrimSpace(body)
	if bytes.HasPrefix(body, []byte("{")) {
		var v struct {
			Status     string `json:"status"`
			Message    string `json:"message"`
			Query      string `json:"query"`
			IP         string `json:"ip"`
			Country    string `json:"country"`
			RegionName string `json:"regionName"`
			Region     string `json:"region"`
			City       string `json:"city"`
			ISP        string `json:"isp"`
			Org        string `json:"org"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, errors.New("回应不是合法的 JSON")
		}
		if v.Status == "fail" {
			return nil, fmt.Errorf("查询失败：%s", v.Message)
		}
		e := &Exit{IP: v.Query, Country: v.Country, Region: v.RegionName, City: v.City, ISP: v.ISP}
		if e.IP == "" {
			e.IP = v.IP
		}
		if e.Region == "" {
			e.Region = v.Region
		}
		if e.ISP == "" {
			e.ISP = v.Org
		}
		return checkIP(e)
	}
	if bytes.Contains(body, []byte("ip=")) {
		e := &Exit{}
		sc := bufio.NewScanner(bytes.NewReader(body))
		for sc.Scan() {
			k, v, _ := strings.Cut(sc.Text(), "=")
			switch k {
			case "ip":
				e.IP = v
			case "loc":
				e.Country = v
			}
		}
		return checkIP(e)
	}
	return checkIP(&Exit{IP: string(body)})
}

func checkIP(e *Exit) (*Exit, error) {
	e.IP = strings.TrimSpace(e.IP)
	if net.ParseIP(e.IP) == nil {
		return nil, errors.New("回应里没有 IP")
	}
	return e, nil
}

// hostOf 地址里的主机名（错误信息用）。
func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}
