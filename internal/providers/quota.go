// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/providers/cosy"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// QuotaSnapshot 只记录上游明确返回的量，不按余额差推算费用，也不跨单位求和。
type QuotaSnapshot struct {
	FetchedAt time.Time      `json:"fetched_at"`
	Unit      string         `json:"unit,omitempty"`
	Balance   *float64       `json:"balance"`
	Used      *float64       `json:"used"`
	Total     *float64       `json:"total"`
	Exceeded  *bool          `json:"exceeded,omitempty"`
	ExpiresAt time.Time      `json:"expires_at,omitzero"`
	RefreshAt time.Time      `json:"refresh_at,omitzero"`
	Packages  []QuotaPackage `json:"packages,omitempty"`
}

type QuotaPackage struct {
	Name      string    `json:"name"`
	Unit      string    `json:"unit"`
	Total     *float64  `json:"total"`
	Used      *float64  `json:"used"`
	Unlimited bool      `json:"unlimited,omitempty"`
	Hidden    bool      `json:"hidden,omitempty"`
	StartsAt  time.Time `json:"starts_at,omitzero"`
	EndsAt    time.Time `json:"ends_at,omitzero"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	RefreshAt time.Time `json:"refresh_at,omitzero"`
}

// QuotaStatus 查询失败时保留上次成功快照；查询错误与推理错误、冷却独立。
type QuotaStatus struct {
	Snapshot  *QuotaSnapshot `json:"snapshot,omitempty"`
	CheckedAt time.Time      `json:"checked_at"`
	Error     string         `json:"error,omitempty"`
}

type quotaAdapter interface {
	Quota(context.Context, string, Credential) (QuotaSnapshot, error)
}

func quotaStatus(a Account, rt *runtime) *QuotaStatus {
	if rt.quotaStatus != nil {
		out := *rt.quotaStatus
		return &out
	}
	if a.Quota != nil {
		return &QuotaStatus{Snapshot: a.Quota, CheckedAt: a.Quota.FetchedAt}
	}
	return nil
}

// Quota 按账号单飞，自动查询缓存一分钟；force 仅跳过已完成的缓存。
// 查询在途期间禁止导入、改代理和删除，并复用同一令牌刷新与落盘保护。
func (m *Manager) Quota(ctx context.Context, id string, force bool) (QuotaStatus, error) {
	m.mu.Lock()
	rt := m.running[id]
	if rt == nil {
		m.mu.Unlock()
		return QuotaStatus{}, os.ErrNotExist
	}
	var account Account
	for _, a := range m.data.Accounts {
		if a.ID == id {
			account = a
			break
		}
	}
	if rt.importing {
		m.mu.Unlock()
		return QuotaStatus{}, errors.New("账号正在导入凭证，请稍后查询额度")
	}
	if done := rt.quotaDone; done != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return QuotaStatus{}, ctx.Err()
		case <-done:
			m.mu.Lock()
			defer m.mu.Unlock()
			return *rt.quotaStatus, nil
		}
	}
	if cached := quotaStatus(account, rt); !force && cached != nil && time.Since(cached.CheckedAt) < time.Minute {
		m.mu.Unlock()
		return *cached, nil
	}
	rt.quotaDone = make(chan struct{})
	rt.inFlight++
	m.mu.Unlock()

	target := account.Targets[0]
	ctx, err := m.route(ctx, account.ProxyID)
	var snapshot QuotaSnapshot
	if err == nil {
		var credential Credential
		credential, err = m.credential(ctx, id, target)
		if err == nil {
			adapter, ok := m.adapters[strings.Split(target, ":")[0]].(quotaAdapter)
			if !ok {
				err = errors.New("该平台尚未接入官方额度查询")
			} else {
				snapshot, err = adapter.Quota(ctx, target, credential)
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	status := QuotaStatus{Snapshot: account.Quota, CheckedAt: time.Now()}
	if err == nil {
		snapshot.FetchedAt = status.CheckedAt
		for i := range m.data.Accounts {
			if m.data.Accounts[i].ID == id {
				m.data.Accounts[i].Quota = &snapshot
				if err = m.saveLocked(); err != nil {
					m.data.Accounts[i].Quota = account.Quota
				} else {
					status.Snapshot = &snapshot
				}
				break
			}
		}
	}
	if err != nil {
		status.Error = "查询账号额度：" + err.Error()
	}
	rt.quotaStatus = &status
	rt.inFlight--
	close(rt.quotaDone)
	rt.quotaDone = nil
	m.wakeLocked()
	return status, nil
}

// quotaNumber 区分字段缺失和明确的零；异常类型报错，不把协议变化变成零余额。
func quotaNumber(obj map[string]any, key string) (*float64, error) {
	v, ok := obj[key]
	if !ok || v == nil {
		return nil, nil
	}
	n, ok := v.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil, fmt.Errorf("额度响应字段 %s 不是有效数字", key)
	}
	return &n, nil
}

func quotaTime(obj map[string]any, key string) (time.Time, error) {
	n, err := quotaNumber(obj, key)
	if err != nil || n == nil || *n == 0 {
		return time.Time{}, err
	}
	// 官方国际账号可能返回公元 9999 年；范围以可持久化的 RFC3339 日期为准。
	if *n < 0 || *n != math.Trunc(*n) || *n > 253402300799999 {
		return time.Time{}, fmt.Errorf("额度响应字段 %s 不是有效时间", key)
	}
	// 官方样本既有 Unix 秒，也有毫秒；按量级保留时间语义。
	stamp := int64(*n)
	if stamp > 1e12 {
		return time.UnixMilli(stamp).UTC(), nil
	}
	if stamp > 253402300799 {
		return time.Time{}, fmt.Errorf("额度响应字段 %s 超出 RFC3339 年份范围", key)
	}
	return time.Unix(stamp, 0).UTC(), nil
}

func (t *Trae) Quota(ctx context.Context, _ string, c Credential) (QuotaSnapshot, error) {
	base := t.PayBase
	if base == "" {
		base = "https://api.trae.cn"
	}
	h := traeHeaders(c, true)
	h.Set("Content-Type", "application/json")
	h.Set("Origin", "https://work.trae.cn")
	h.Set("Sec-Fetch-Site", "same-site")
	// fetchEntitlementUsage 直接调用 fetch，不经过会话请求的自定义头拦截器。
	h.Del("X-Trae-Client-Type")
	h.Del("X-Trae-User-Timezone")
	h.Del("X-Preferenced-Language")
	// Work 网页的账号额度接口独立于 TTNet 对话与 Electron 授权通道。
	ctx = upstream.WithClientProfile(ctx, upstream.ProfileTraeWork)
	obj, err := jsonRequest(ctx, t.HTTP, "POST", base+"/trae/api/v2/pay/web_user_ent_usage", map[string]any{"require_usage": true}, h)
	if err != nil {
		return QuotaSnapshot{}, fmt.Errorf("Trae 账号额度: %w", err)
	}
	return parseTraeQuota(obj)
}

func parseTraeQuota(obj map[string]any) (QuotaSnapshot, error) {
	var out QuotaSnapshot
	credits, _ := obj["is_credits_billing"].(bool)
	dollars, _ := obj["is_dollar_usage_billing"].(bool)
	if credits && !dollars {
		out.Unit = "credits"
	} else if dollars && !credits {
		out.Unit = "USD"
	}
	var err error
	if out.Balance, err = quotaNumber(obj, "total_balance"); err != nil {
		return out, err
	}
	usage := object(obj["usage_summary"])
	if out.Used, err = quotaNumber(usage, "consumed_amount"); err != nil {
		return out, err
	}
	if out.Total, err = quotaNumber(usage, "total_amount"); err != nil {
		return out, err
	}
	if out.Balance == nil && out.Used == nil && out.Total == nil {
		return out, errors.New("Trae 未返回已确认的账号额度字段")
	}
	if raw, exists := obj["user_entitlement_pack_list"]; exists && raw != nil {
		packs, ok := raw.([]any)
		if !ok {
			return out, errors.New("Trae 额度包列表格式无效")
		}
		for i, raw := range packs {
			p, ok := raw.(map[string]any)
			if !ok {
				return out, fmt.Errorf("Trae 第 %d 个额度包格式无效", i+1)
			}
			base := object(p["entitlement_base_info"])
			pack := QuotaPackage{Name: text(p["display_desc"]), Unit: "credits"}
			pack.Hidden, _ = p["is_hide"].(bool)
			if pack.Total, err = quotaNumber(object(base["quota"]), "credits_limit"); err != nil {
				return out, err
			}
			if pack.Total != nil && *pack.Total == -1 {
				pack.Total, pack.Unlimited = nil, true
			}
			if pack.Used, err = quotaNumber(object(p["usage"]), "credits_amount"); err != nil {
				return out, err
			}
			for _, field := range []struct {
				obj map[string]any
				key string
				dst *time.Time
			}{{base, "start_time", &pack.StartsAt}, {base, "end_time", &pack.EndsAt}, {p, "expire_time", &pack.ExpiresAt}, {p, "next_billing_time", &pack.RefreshAt}} {
				if *field.dst, err = quotaTime(field.obj, field.key); err != nil {
					return out, err
				}
			}
			out.Packages = append(out.Packages, pack)
		}
	}
	return out, nil
}

func (q *Qoder) Quota(ctx context.Context, target string, c Credential) (QuotaSnapshot, error) {
	ctx, err := q.withEndpoints(ctx, target, c)
	if err != nil {
		return QuotaSnapshot{}, err
	}
	h := http.Header{"Authorization": {"Bearer " + c.AccessToken}, "Accept": {"application/json"}, "Cosy-Clienttype": {"10"}, "User-Agent": {"Qoder"}}
	if target == "qoder:global" {
		h = qoderAPIHeaders(c.AccessToken)
		h.Del("Content-Type")
		// 官方 fetchUsagePresentation 经通用 server request 注入 CLI 身份，UA 使用原生 Bun。
		h.Set("User-Agent", cosy.UserAgent)
		var trace [24]byte
		rand.Read(trace[:])
		h.Set("Traceparent", fmt.Sprintf("00-%x-%x-01", trace[:16], trace[16:]))
	}
	obj, err := jsonRequest(ctx, q.HTTP, "GET", q.resolvedOpenAPI(ctx, target)+"/sash/api/v2/me/usage", nil, h)
	if err != nil {
		return QuotaSnapshot{}, fmt.Errorf("Qoder 账号额度: %w", err)
	}
	return parseQoderQuota(obj)
}

func parseQoderQuota(obj map[string]any) (QuotaSnapshot, error) {
	var out QuotaSnapshot
	usage := object(obj["qoderUsage"])
	quota := object(usage["userQuota"])
	if text(obj["displayMode"]) != "qoder" || quota == nil {
		return out, errors.New("Qoder 返回未支持的额度类型，未确认其余额")
	}
	out.Unit = text(quota["unit"])
	var err error
	if out.Balance, err = quotaNumber(quota, "remaining"); err != nil {
		return out, err
	}
	if out.Used, err = quotaNumber(quota, "used"); err != nil {
		return out, err
	}
	if out.Total, err = quotaNumber(quota, "total"); err != nil {
		return out, err
	}
	if out.Balance == nil && out.Used == nil && out.Total == nil {
		return out, errors.New("Qoder 未返回已确认的额度数字")
	}
	if v, ok := usage["isQuotaExceeded"].(bool); ok {
		out.Exceeded = &v
	}
	if out.ExpiresAt, err = quotaTime(usage, "expiresAt"); err != nil {
		return out, err
	}
	if out.RefreshAt, err = quotaTime(usage, "refreshAt"); err != nil {
		return out, err
	}
	return out, nil
}
