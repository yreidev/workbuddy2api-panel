// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// resources.go 个人号积分：照官方桌面端 5.8.3 的查法（BackendProvider.getCurrentPlan → fetchUserResources）。
//
// 三个接口同时发，两个域路径相同（不带 /v2），都带 Accept-Language（zh / en）：
//
//	POST /billing/meter/get-user-resource-summary       {}                                     各档汇总
//	POST /billing/meter/get-user-resource-paid-packages {PageNumber, PageSize: 200, PackageCodes: 付费包, Status: [0,3], NeedRenewInfo: true}
//	POST /billing/meter/get-user-resource-free-packages {PageNumber, PageSize: 200, PackageCodes: 免费包, Status: [0,3], SlicePeriodStartTime, SlicePeriodEndTime}
//
// 逐包列表每页 200 条、最多 10 页，按 ResourceId 去重。单包额度按官方 resolveResourceCapacity：
// 按天切片的包（CapacityType 4）有当天切片明细时取切片额度，否则取周期额度，用精确到小数的
// *Precise 字段。1.1.2 以前网关只调 /v2/billing/meter/get-user-resource，官方 5.8.3 已不再调用；
// 10-20 生产对比两域 4 个号，新旧接口返回同一批包，按官方算法算出的剩余与汇总一致。
package upstream

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const (
	resourceSummaryPath = "/billing/meter/get-user-resource-summary"
	resourcePaidPath    = "/billing/meter/get-user-resource-paid-packages"
	resourceFreePath    = "/billing/meter/get-user-resource-free-packages"
	resourcePageSize    = 200
	resourceMaxPages    = 10
	// sliceCapacityType 按天切片发放的包（官方 isSliceCapacityResource）。
	sliceCapacityType = 4
)

// paidPackageCodes / freePackageCodes 官方分别按付费包、免费包查询的包代码（桌面端 5.8.3 的 As / js）。
var (
	paidPackageCodes = []string{
		"TCACA_code_002_AkiJS3ZHF5", // proMon
		"TCACA_code_005_maRGyrHhw1", // proMonPlus
		"TCACA_code_003_FAnt7lcmRT", // proYear
		"TCACA_code_023_4xbGhMrE6q", // youth
		"TCACA_code_026_BaESVICNoi", // advanced
		"TCACA_code_027_0FCGVA6vSa", // flagship
		"TCACA_code_009_0XmEQc2xOf", // extra
		"TCACA_code_038_OhvqZtiPKr", // extra38
		"TCACA_code_036_lupO5WgNdG", // extraIntl
	}
	freePackageCodes = []string{
		"TCACA_code_001_PqouKr6QWV", // free
		"TCACA_code_008_cfWoLwvjU4", // freeMon
		"TCACA_code_035_ArVxJcGDsm", // freeMonIntl
		"TCACA_code_006_DbXS0lrypC", // gift
		"TCACA_code_039_KRcQj7wUat", // proTrialMon
		"TCACA_code_040_mi9rCYg46x", // proTrialYear
		"TCACA_code_007_nzdH5h4Nl0", // activity
		"TCACA_code_028_NtpWi0jzXs", // bonus28
		"TCACA_code_037_WxOD3MpI2o", // bonusIntl
		"TCACA_code_029_6wCGEWquYy", // bonus29
		"TCACA_code_030_BjSt89qTvr", // bonus30
	}

	// oneOffPackageCodes 一次性发放、不按周期刷新的包（桌面端 5.9.3 getCurrentPlan 里
	// 不显示刷新时间的那组）。签到、任务发的裂变包就是 activity。
	oneOffPackageCodes = map[string]bool{
		"TCACA_code_007_nzdH5h4Nl0": true, // activity
		"TCACA_code_009_0XmEQc2xOf": true, // extra
		"TCACA_code_038_OhvqZtiPKr": true, // extra38
		"TCACA_code_029_6wCGEWquYy": true, // bonus29
		"TCACA_code_030_BjSt89qTvr": true, // bonus30
		"TCACA_code_036_lupO5WgNdG": true, // extraIntl
	}
)

// dailyFreePackageCode 每日免费包（free），官方的到期时刻取 CycleEndTime。
const dailyFreePackageCode = "TCACA_code_001_PqouKr6QWV"

// preciseCount 上游 *Precise 字段（十进制字符串，也兼容数字）。同官方 toCount：
// 解析不了、非正数都当 0。
type preciseCount float64

func (p *preciseCount) UnmarshalJSON(b []byte) error {
	f, err := strconv.ParseFloat(strings.Trim(string(b), `"`), 64)
	if err != nil || f <= 0 || math.IsInf(f, 0) {
		f = 0
	}
	*p = preciseCount(f)
	return nil
}

// resourceAccount 逐包接口返回的单个资源包（只取网关用到的字段）。
type resourceAccount struct {
	ResourceID     string `json:"ResourceId"`
	PackageName    string `json:"PackageName"`
	PackageCode    string `json:"PackageCode"`
	SubProductCode string `json:"SubProductCode"`
	SubProductName string `json:"SubProductName"`
	CapacityType   int    `json:"CapacityType"`

	CycleCapacitySizePrecise   preciseCount `json:"CycleCapacitySizePrecise"`
	CycleCapacityRemainPrecise preciseCount `json:"CycleCapacityRemainPrecise"`
	SlicePeriodUsageDetails    []struct {
		SizePrecise   preciseCount `json:"SlicePeriodCapacitySizePrecise"`
		RemainPrecise preciseCount `json:"SlicePeriodCapacityRemainPrecise"`
	} `json:"SlicePeriodUsageDetails"`

	// 到期时间字段的取舍见 expiry。
	ExpiredTime      string `json:"ExpiredTime"`
	CycleEndTime     string `json:"CycleEndTime"`
	DeductionEndTime int64  `json:"DeductionEndTime"`
	CreateTime       int64  `json:"CreateTime"`
}

// refreshes 包是否按周期刷新（订阅、体验这类，官方只显示 CycleEndTime 的刷新时间）。
func (r resourceAccount) refreshes() bool {
	return r.PackageCode != dailyFreePackageCode && !oneOffPackageCodes[r.PackageCode]
}

// expiry 包里剩下的积分什么时候作废，照桌面端 5.9.3 getCurrentPlan 按包类型取：
//   - 一次性发放的包：DeductionEndTime（可抵扣窗口结束；10-01 实测上游也按它先到期先扣），
//     缺失时依次回落 ExpiredTime、CycleEndTime（同官方 toExpiredCreditPackage）；
//   - 每日免费包、按周期刷新的包：CycleEndTime。刷新的包官方不显示到期、只显示刷新时间，
//     这里按「刷新时没用完的作废」算（未确认会不会结转），缺 CycleEndTime 时同上回落。
//
// raw 是给面板显示的原文（DeductionEndTime 是毫秒，转成 RFC3339），at 解析不出时 ok 为 false。
func (r resourceAccount) expiry() (raw string, at time.Time, ok bool) {
	if !oneOffPackageCodes[r.PackageCode] && r.CycleEndTime != "" {
		at, ok = parsePackageEndTime(r.CycleEndTime)
		return r.CycleEndTime, at, ok
	}
	switch {
	case r.DeductionEndTime > 0:
		at = time.UnixMilli(r.DeductionEndTime)
		return at.Format(time.RFC3339), at, true
	case r.ExpiredTime != "":
		raw = r.ExpiredTime
	default:
		raw = r.CycleEndTime
	}
	at, ok = parsePackageEndTime(raw)
	return raw, at, ok
}

// capacity 单包额度与剩余（官方 resolveResourceCapacity）。剩余超过额度的脏数据按额度算。
func (r resourceAccount) capacity() (total, left float64) {
	if r.CapacityType == sliceCapacityType && len(r.SlicePeriodUsageDetails) > 0 {
		d := r.SlicePeriodUsageDetails[0]
		total, left = float64(d.SizePrecise), float64(d.RemainPrecise)
	} else {
		total, left = float64(r.CycleCapacitySizePrecise), float64(r.CycleCapacityRemainPrecise)
	}
	return total, math.Min(left, total)
}

// resourceSummary 汇总接口各档相加（官方 sumSummaryCapacity）。
type resourceSummary struct {
	total, left float64
}

// credits 积分取整：池内积分是整数，四舍五入到最近整数（与企业号口径一致）。
func credits(v float64) int64 { return int64(math.Round(v)) }

// userResources 照官方并发查汇总与付费、免费两个逐包列表。逐包列表任一失败即报错（余额与
// 到期分桶都要用到它们）；汇总失败时 summary 为 nil，调用方用逐包合计代替（生产实测两者一致）。
func (c *Client) userResources(a *auth.Auth) ([]resourceAccount, *resourceSummary, error) {
	now := time.Now().In(softRateResetLoc) // 上游计费时间都按 UTC+8 墙钟
	day := now.Format("2006-01-02")
	var (
		wg               sync.WaitGroup
		summary          *resourceSummary
		paid, free       []resourceAccount
		paidErr, freeErr error
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		summary = c.resourceSummary(a)
	}()
	go func() {
		defer wg.Done()
		paid, paidErr = c.resourcePages(a, resourcePaidPath, paidPackageCodes, map[string]any{"NeedRenewInfo": true})
	}()
	go func() {
		defer wg.Done()
		free, freeErr = c.resourcePages(a, resourceFreePath, freePackageCodes, map[string]any{
			"SlicePeriodStartTime": day + " 00:00:00",
			"SlicePeriodEndTime":   day + " 23:59:59",
		})
	}()
	wg.Wait()
	if paidErr != nil {
		return nil, nil, paidErr
	}
	if freeErr != nil {
		return nil, nil, freeErr
	}
	out := make([]resourceAccount, 0, len(paid)+len(free))
	seen := make(map[string]bool, len(paid)+len(free))
	for _, r := range append(paid, free...) {
		if r.ResourceID != "" {
			if seen[r.ResourceID] {
				continue
			}
			seen[r.ResourceID] = true
		}
		out = append(out, r)
	}
	return out, summary, nil
}

// resourceSummary 查汇总；失败返回 nil（官方同样只记一条告警，积分改用逐包数据）。
func (c *Client) resourceSummary(a *auth.Auth) *resourceSummary {
	var data json.RawMessage
	err := c.retryBillingTransient(func() error {
		var e error
		data, e = c.billingJSON(a, http.MethodPost, resourceSummaryPath, map[string]any{}, withBillingLanguage(a))
		return e
	})
	if err != nil {
		return nil
	}
	var resp struct {
		Packages []struct {
			Total preciseCount `json:"CycleTotalCapacity"`
			Left  preciseCount `json:"CycleRemainCapacity"`
		} `json:"Packages"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return nil
	}
	s := &resourceSummary{}
	for _, p := range resp.Packages {
		s.total += float64(p.Total)
		s.left += float64(p.Left)
	}
	return s
}

// resourcePages 逐页拉一个逐包列表（官方 fetchAllPages：每页 200、最多 10 页）。
func (c *Client) resourcePages(a *auth.Auth, path string, codes []string, extra map[string]any) ([]resourceAccount, error) {
	var all []resourceAccount
	for page := 1; page <= resourceMaxPages; page++ {
		body := map[string]any{
			"PageNumber":   page,
			"PageSize":     resourcePageSize,
			"PackageCodes": codes,
			"Status":       []int{0, 3},
		}
		for k, v := range extra {
			body[k] = v
		}
		var data json.RawMessage
		err := c.retryBillingTransient(func() error {
			var e error
			data, e = c.billingJSON(a, http.MethodPost, path, body, withBillingLanguage(a))
			return e
		})
		if err != nil {
			return nil, err
		}
		var resp struct {
			Accounts   []resourceAccount `json:"Accounts"`
			TotalCount int               `json:"TotalCount"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("resource parse: %w", err)
		}
		all = append(all, resp.Accounts...)
		if len(resp.Accounts) < resourcePageSize || len(all) >= resp.TotalCount {
			break
		}
	}
	return all, nil
}

// editionPackages 官方 getPersonalUsage 判版本档用的订阅包，按判定顺序排（旗舰、高级、青春、
// 专业、专业试用）。
var editionPackages = []struct {
	edition string
	codes   []string
}{
	{"flagship", []string{"TCACA_code_027_0FCGVA6vSa"}},
	{"advanced", []string{"TCACA_code_026_BaESVICNoi"}},
	{"youth", []string{"TCACA_code_023_4xbGhMrE6q"}},
	{"pro", []string{"TCACA_code_002_AkiJS3ZHF5", "TCACA_code_005_maRGyrHhw1", "TCACA_code_003_FAnt7lcmRT"}},
	{"pro_trial", []string{"TCACA_code_039_KRcQj7wUat", "TCACA_code_040_mi9rCYg46x"}},
}

// AccountEdition 账号的版本档，照官方 5.8.3 外观设置的 editionType（皮肤事件的 type）：
// 账号类型是 ultimate / exclusive 时就是它；企业号取 ultimate（resolveIntrinsicEdition 的
// 默认值）；个人号按 getPersonalUsage / getEditionDisplayType，逐包列表里有哪档订阅包就是
// 哪档，都没有为 free。官方这一步查不到时不上报皮肤事件，所以出错时调用方也不要上报。
func (c *Client) AccountEdition(a *auth.Auth) (string, error) {
	switch t := a.AccountTypeValue(); t {
	case "ultimate", "exclusive":
		return t, nil
	}
	if a.IsEnterprise() {
		return "ultimate", nil
	}
	packs, _, err := c.userResources(a)
	if err != nil {
		return "", err
	}
	have := make(map[string]bool, len(packs))
	for _, p := range packs {
		have[p.PackageCode] = true
	}
	for _, e := range editionPackages {
		for _, code := range e.codes {
			if have[code] {
				return e.edition, nil
			}
		}
	}
	return "free", nil
}
