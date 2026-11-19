// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/providers/cosy"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

type qoderEndpointsKey struct{}
type providerRouteKey struct{}

const (
	qoderEndpointTimeout   = 2 * time.Second
	qoderEndpointFreshness = 30 * time.Second
	qoderEndpointRetention = 5 * time.Minute
)

type qoderEndpoints struct {
	inference, fast, openapi, center string
	inferenceNodes                   []string
	cacheEntry                       *qoderEndpointEntry
}

type qoderEndpointEntry struct {
	done    chan struct{}
	routes  qoderEndpoints
	expires time.Time
}

func endpointCacheKey(c Credential, proxy string) [32]byte {
	return sha256.Sum256([]byte(c.UID + "\x00" + c.MachineID + "\x00" + c.MachineToken + "\x00" + c.TokenKind + "\x00" + c.AccessToken + "\x00" + proxy))
}

// 健康检查沿用该身份及代理上已发现的推理入口，不发起新的发现请求。
func (q *Qoder) probeBase(target string, c Credential, proxy string) string {
	if target == "qoder:global" {
		q.endpointMu.Lock()
		defer q.endpointMu.Unlock()
		if entry := q.endpoints[endpointCacheKey(c, proxy)]; entry != nil && entry.routes.inference != "" {
			return entry.routes.inference
		}
	}
	return q.base(target)
}

func (q *Qoder) inferBase(ctx context.Context, target string) string {
	if routes, ok := ctx.Value(qoderEndpointsKey{}).(qoderEndpoints); ok {
		return routes.inference
	}
	return q.base(target)
}

func (q *Qoder) resolvedOpenAPI(ctx context.Context, target string) string {
	if routes, ok := ctx.Value(qoderEndpointsKey{}).(qoderEndpoints); ok {
		return routes.openapi
	}
	return q.openAPI(target)
}

// 国际版沿用官方 CLI 的 30 秒新鲜期，为容器冷连接预留两秒；同一操作固定选路。
// 缓存不落盘，键只保留身份、令牌与线路的摘要，取消不会缓存失败结果。
func (q *Qoder) withEndpoints(ctx context.Context, target string, c Credential) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return ctx, err
	}
	if target != "qoder:global" || q.Base != "" || q.OpenAPIBase != "" || ctx.Value(qoderEndpointsKey{}) != nil {
		return ctx, nil
	}
	proxy, _ := ctx.Value(providerRouteKey{}).(string)
	key := endpointCacheKey(c, proxy)
	for {
		q.endpointMu.Lock()
		entry := q.endpoints[key]
		if entry != nil && entry.done != nil {
			done := entry.done
			q.endpointMu.Unlock()
			select {
			case <-ctx.Done():
				return ctx, ctx.Err()
			case <-done:
				continue
			}
		}
		now := time.Now()
		if entry != nil && now.Before(entry.expires) {
			routes := entry.routes
			q.endpointMu.Unlock()
			return context.WithValue(ctx, qoderEndpointsKey{}, routes), nil
		}
		// 新鲜期结束后仍要保留上次节点供发现失败时回退，不能由其他账号提前清掉。
		for k, cached := range q.endpoints {
			if cached.done == nil && !now.Before(cached.expires.Add(qoderEndpointRetention)) {
				delete(q.endpoints, k)
			}
		}
		cached := q.endpoints[key]
		previous := qoderEndpoints{inference: q.base(target), openapi: q.openAPI(target), center: "https://center.qoder.sh"}
		if cached != nil {
			previous = cached.routes
		}
		if q.endpoints == nil {
			q.endpoints = make(map[[32]byte]*qoderEndpointEntry)
		}
		done := make(chan struct{})
		entry = &qoderEndpointEntry{done: done, routes: previous}
		q.endpoints[key] = entry
		q.endpointMu.Unlock()

		pollCtx, cancel := context.WithTimeout(ctx, qoderEndpointTimeout)
		routes, err := q.discoverEndpoints(pollCtx, c, previous)
		cancel()
		routeFailure := upstream.IsRouteError(err)
		if err != nil {
			routes = previous
			if ctx.Err() == nil && !routeFailure {
				log.Printf("[providers] Qoder 国际端点发现失败，保留当前官方端点: %v", err)
			}
		}
		q.endpointMu.Lock()
		callErr := ctx.Err()
		if callErr == nil && routeFailure {
			callErr = err
		}
		switch {
		case callErr != nil && cached != nil:
			q.endpoints[key] = cached
		case callErr != nil:
			delete(q.endpoints, key)
		default:
			routes.cacheEntry = entry
			entry.routes, entry.expires = routes, time.Now().Add(qoderEndpointFreshness)
			entry.done = nil
		}
		close(done)
		q.endpointMu.Unlock()
		if callErr != nil {
			return ctx, callErr
		}
		return context.WithValue(ctx, qoderEndpointsKey{}, routes), nil
	}
}

func (q *Qoder) discoverEndpoints(ctx context.Context, c Credential, previous qoderEndpoints) (qoderEndpoints, error) {
	const v3 = "/algo/api/v3/service/region/endpoints"
	anonymous := func() (*http.Response, error) {
		h := cosy.CredentialHeaders(c.MachineID, c.MachineToken, c.MachineType, c.MachineOS)
		if c.MachineToken == "" {
			h.Set("Cosy-MachineToken", c.MachineID)
		}
		if c.MachineType == "" {
			h.Set("Cosy-MachineType", "5")
		}
		h.Set("Accept", "application/json")
		h.Set("Cosy-Business-Product", "cli")
		h.Set("Cosy-Business-Type", "agent")
		h.Set("Cosy-Scene", "assistant")
		h.Set("Cosy-ClientIp", c.MachineID)
		return request(ctx, q.HTTP, http.MethodGet, previous.center+v3, nil, h)
	}
	var resp *http.Response
	var err error
	if c.TokenKind == "device" && c.AccessToken != "" && c.UID != "" {
		h := http.Header{}
		h.Set("Authorization", "Bearer "+c.AccessToken)
		h.Set("Cosy-MachineId", c.MachineID)
		h.Set("Cosy-MachineToken", c.MachineToken)
		h.Set("X-Gw-User-Id", c.UID)
		h.Set("User-Agent", cosy.UserAgent)
		h.Set("Cosy-ClientType", "5")
		h.Set("Cosy-Version", cosy.CosyVersion)
		h.Set("Cosy-MachineOS", "x86_64_linux")
		if c.MachineOS != "" {
			h.Set("Cosy-MachineOS", c.MachineOS)
		}
		h.Set("Accept-Encoding", "gzip, deflate, br, zstd")
		if c.MachineToken == "" {
			h.Set("Cosy-MachineToken", c.MachineID)
		}
		if c.MachineType != "" {
			h.Set("Cosy-MachineType", c.MachineType)
		}
		resp, err = request(ctx, q.HTTP, http.MethodGet, previous.center+"/algo/api/v5/service/region/endpoints", nil, h)
		var upstreamErr *Error
		if errors.As(err, &upstreamErr) && (upstreamErr.Status == 401 || upstreamErr.Status == 403) {
			resp, err = anonymous()
		}
	} else {
		resp, err = anonymous()
	}
	if err != nil {
		return previous, fmt.Errorf("查询地区端点: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return previous, fmt.Errorf("读取地区端点: %w", err)
	}
	if len(raw) > 1<<20 {
		return previous, errors.New("地区端点响应超过 1 MiB")
	}
	if !json.Valid(raw) {
		raw, err = cosy.Decode(strings.TrimSpace(string(raw)))
		if err != nil {
			return previous, fmt.Errorf("解码地区端点: %w", err)
		}
	}
	var root map[string]any
	if err = json.Unmarshal(raw, &root); err != nil {
		return previous, fmt.Errorf("解析地区端点: %w", err)
	}
	data, err := qoderBody(root)
	if err != nil {
		return previous, fmt.Errorf("地区端点包络: %w", err)
	}
	if data["inferNodes"] == nil && data["openapiNodes"] == nil && data["centerNodes"] == nil {
		return previous, errors.New("地区端点响应缺少 inferNodes、openapiNodes 和 centerNodes")
	}
	routes := previous
	for _, field := range []struct {
		name string
		dst  *string
	}{{"inferNodes", &routes.inference}, {"openapiNodes", &routes.openapi}, {"centerNodes", &routes.center}, {"fastMode", &routes.fast}} {
		nodes, err := qoderEndpointNodes(data[field.name], field.name == "fastMode")
		if err != nil {
			return previous, fmt.Errorf("地区端点 %s: %w", field.name, err)
		}
		if len(nodes) == 0 && field.name == "fastMode" {
			*field.dst = ""
		}
		if len(nodes) == 0 && field.name == "openapiNodes" {
			*field.dst = q.openAPI("qoder:global")
		}
		if len(nodes) > 0 {
			selected := nodes[0]
			for _, node := range nodes {
				if node == *field.dst {
					selected = node
					break
				}
			}
			*field.dst = selected
		}
		if field.name == "inferNodes" && data[field.name] != nil {
			routes.inferenceNodes = nodes
		}
	}
	return routes, nil
}

// 目录 GET 可重放，业务错误、权限与限流不作为节点故障。
func qoderEndpointFailure(err error) bool {
	if upstream.IsRouteError(err) {
		return false
	}
	var upstreamErr *Error
	if errors.As(err, &upstreamErr) {
		return upstreamErr.Status == 502 || upstreamErr.Status == 503 || upstreamErr.Status == 504
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (q *Qoder) rejectInferenceEndpoint(routes qoderEndpoints, failed string) {
	q.endpointMu.Lock()
	defer q.endpointMu.Unlock()
	entry := routes.cacheEntry
	if entry == nil || entry.routes.inference != failed {
		return
	}
	// 创建新列表，已开始的生成与队列请求继续持有原选路。
	remaining := make([]string, 0, len(entry.routes.inferenceNodes))
	for _, node := range entry.routes.inferenceNodes {
		if node != failed {
			remaining = append(remaining, node)
		}
	}
	entry.routes.inferenceNodes = remaining
	if len(remaining) > 0 {
		entry.routes.inference = remaining[0]
	} else {
		entry.routes.inference = q.base("qoder:global")
		entry.expires = time.Time{}
	}
}

// 只接受官方 HTTPS 域名，避免发现响应把账号凭证转发至任意地址。
// 企业专用节点与自定义端点不属于当前接入范围，按官方默认设置跳过。
func qoderEndpointNodes(value any, fast bool) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, errors.New("端点列表必须为数组")
	}
	var nodes []string
	for _, item := range list {
		raw, ok := item.(string)
		if !ok {
			obj := object(item)
			if text(obj["type"]) == "enterprise" {
				continue
			}
			if obj == nil || text(obj["type"]) != "public" && (!fast || obj["type"] != nil) {
				return nil, errors.New("端点缺少有效的 public 类型")
			}
			raw = text(obj["url"])
		}
		raw = strings.TrimSpace(raw)
		if fast && !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".qoder.sh") || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" && u.Path != "/algo" {
			return nil, errors.New("端点必须为官方 qoder.sh 的 HTTPS 地址")
		}
		nodes = append(nodes, "https://"+u.Host)
	}
	return nodes, nil
}
