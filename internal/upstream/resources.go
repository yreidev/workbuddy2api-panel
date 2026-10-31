// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// resources.go 个人号积分：照官方桌面端 5.9.x 后台的查法（authority-snapshot.js 的 getPersonalUsage →
// account.js 的 fetchUserResources，国内版 5.9.3 与国际版 5.9.4 相同）。
//
// 两个域路径相同（不带 /v2），请求头是后台服务 AuthService 的那组（authServiceHeaders），另带
// Accept-Language（zh / en）：
//
//	POST /billing/meter/get-user-resource-summary       {}                                     各档汇总
//	POST /billing/meter/get-user-resource-paid-packages {PageNumber, PageSize: 200, PackageCodes: 付费包, Status: [0,3], NeedRenewInfo: true}
//	POST /billing/meter/get-user-resource-free-packages {PageNumber, PageSize: 200, PackageCodes: 免费包, Status: [0,3], SlicePeriodStartTime, SlicePeriodEndTime}
//
// 国际版三个同时发。国内版汇总和免费包同时发，付费包等汇总回来再定：汇总显示只有免费包就不查
// （官方 skipPaidForFreeUser，5.9 起有），汇总没查到照查。逐包列表每页 200 条、最多 10 页，按 ResourceId 去重。单包额度按官方 resolveResourceCapacity：
// 按天切片的包（CapacityType 4）有当天切片明细时取切片额度，否则取周期额度，用精确到小数的
// *Precise 字段。1.1.2 以前网关只调 /v2/billing/meter/get-user-resource，官方 5.8.3 已不再调用；
// 10-20 生产对比两域 4 个号，新旧接口返回同一批包，按官方算法算出的剩余与汇总一致。
package upstream

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
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

// paidPackageCodes / freePackageCodes 官方分别按付费包、免费包查询的包代码（桌面端 5.9.x account.js 的
// Fs / Is）。付费包里的 buddyOneVip 是 5.9 起加的，5.8.3 / 5.8.4 没有。
var (
	paidPackageCodes = []string{
		"TCACA_code_002_AkiJS3ZHF5", // proMon
		"TCACA_code_005_maRGyrHhw1", // proMonPlus
		"TCACA_code_003_FAnt7lcmRT", // proYear
		"TCACA_code_023_4xbGhMrE6q", // youth
		"TCACA_code_026_BaESVICNoi", // advanced
		"TCACA_code_027_0FCGVA6vSa", // flagship
		"TCACA_code_043_6oIkxT7Z0S", // buddyOneVip
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

	// oneOffPackageCodes 一次性发放、不按周期刷新的包（官方 getPersonalUsage 里不显示刷新时间的
	// 那组）。签到、任务发的裂变包就是 activity。
	oneOffPackageCodes = map[string]bool{
		"TCACA_code_007_nzdH5h4Nl0": true, // activity
		"TCACA_code_009_0XmEQc2xOf": true, // extra
		"TCACA_code_038_OhvqZtiPKr": true, // extra38
		"TCACA_code_029_6wCGEWquYy": true, // bonus29
		"TCACA_code_030_BjSt89qTvr": true, // bonus30
		"TCACA_code_036_lupO5WgNdG": true, // extraIntl
	}
	// globalOneOffPackageCodes 国际版另算一次性的包（官方按 isOversea 加进上面那组，5.8.3 起如此）。
	globalOneOffPackageCodes = map[string]bool{
		"TCACA_code_008_cfWoLwvjU4": true, // freeMon
		"TCACA_code_006_DbXS0lrypC": true, // gift
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

	// global 账号是国际版（userResources 填）：哪些包算一次性的两域不同。
	global bool
}

// oneOff 包是一次性发放、不按周期刷新的。
func (r resourceAccount) oneOff() bool {
	return oneOffPackageCodes[r.PackageCode] || r.global && globalOneOffPackageCodes[r.PackageCode]
}

// refreshes 包是否按周期刷新（订阅、体验这类，官方只显示 CycleEndTime 的刷新时间）。
func (r resourceAccount) refreshes() bool {
	return r.PackageCode != dailyFreePackageCode && !r.oneOff()
}

// expiry 包里剩下的积分什么时候作废，照官方 getPersonalUsage 按包类型取：
//   - 一次性发放的包：DeductionEndTime（可抵扣窗口结束；10-01 实测上游也按它先到期先扣），
//     缺失时依次回落 ExpiredTime、CycleEndTime（同官方 toExpiredCreditPackage）；
//   - 每日免费包、按周期刷新的包：CycleEndTime。刷新的包官方不显示到期、只显示刷新时间，
//     这里按「刷新时没用完的作废」算（未确认会不会结转），缺 CycleEndTime 时同上回落。
//
// raw 是给面板显示的原文（DeductionEndTime 是毫秒，转成 RFC3339），at 解析不出时 ok 为 false。
func (r resourceAccount) expiry() (raw string, at time.Time, ok bool) {
	if !r.oneOff() && r.CycleEndTime != "" {
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

// resourceSummary 汇总接口（官方 parseResourceSummary）：各档额度相加（sumSummaryCapacity），以及判断
// 纯免费号、版本档用的订阅包、是否付费用户和各档的包代码。
type resourceSummary struct {
	total, left  float64
	subscription string   // SubscriptionPackageCode
	paidUser     bool     // IsPaidUser
	codes        []string // 各档的 PackageCode
}

// freeOnly 汇总显示账号只有免费包（官方 isFreeOnlySummary）：不是付费用户、订阅包（有的话）是免费包、
// 各档都是免费包；一档都没有不算。
func (s *resourceSummary) freeOnly() bool {
	if s == nil || s.paidUser || s.subscription != "" && !slices.Contains(freePackageCodes, s.subscription) || len(s.codes) == 0 {
		return false
	}
	for _, code := range s.codes {
		if !slices.Contains(freePackageCodes, code) {
			return false
		}
	}
	return true
}

// jsTruthy 上游的布尔字段，照官方 `!!value` 判真假（实际类型没见过，不因类型对不上丢掉整个汇总）。
type jsTruthy bool

func (t *jsTruthy) UnmarshalJSON(b []byte) error {
	var v any
	_ = json.Unmarshal(b, &v) // 解码器交来的都是合法的 JSON 值
	switch x := v.(type) {
	case bool:
		*t = jsTruthy(x)
	case float64:
		*t = x != 0
	case string:
		*t = x != ""
	default: // null 为假，对象、数组为真
		*t = x != nil
	}
	return nil
}

// planBaseRank 汇总没查到时官方给订阅档排的先后（5.9.4 account.js 的 Os；5.9.3 没有体验包那两档）。
// freeMonIntl 也是订阅档，排 0。
var planBaseRank = map[string]int{
	"TCACA_code_043_6oIkxT7Z0S": 6, // buddyOneVip
	"TCACA_code_027_0FCGVA6vSa": 5, // flagship
	"TCACA_code_026_BaESVICNoi": 4, // advanced
	"TCACA_code_002_AkiJS3ZHF5": 3, // proMon
	"TCACA_code_005_maRGyrHhw1": 3, // proMonPlus
	"TCACA_code_003_FAnt7lcmRT": 3, // proYear
	"TCACA_code_023_4xbGhMrE6q": 2, // youth
	"TCACA_code_039_KRcQj7wUat": 1, // proTrialMon
	"TCACA_code_040_mi9rCYg46x": 1, // proTrialYear
	"TCACA_code_035_ArVxJcGDsm": 0, // freeMonIntl
}

// planBase 包是不是订阅档（官方 isPlanBaseCode）：上表的包，国内版再加 freeMon。
func (r resourceAccount) planBase() bool {
	_, ok := planBaseRank[r.PackageCode]
	return ok || !r.global && r.PackageCode == "TCACA_code_008_cfWoLwvjU4"
}

// packageTotals 逐包合计（汇总没查到时用，官方 getPersonalUsage 的回退）：同时有几个订阅档只算排在
// 最前的一个（excludeSuppressedPlanBase），其余包照算。官方 5.9.4 起两域都这样，5.9.3 及以前只有国内版。
func packageTotals(packs []resourceAccount) (total, left float64) {
	best := -1
	for i, p := range packs {
		if p.planBase() && (best < 0 || planBaseAhead(p, packs[best])) {
			best = i
		}
	}
	for i, p := range packs {
		if p.planBase() && i != best {
			continue
		}
		t, l := p.capacity()
		total += t
		left += l
	}
	return total, left
}

// planBaseAhead 订阅档 a 是否排在 b 前（官方 compare）：先比档位，再让没用完的优先，再比额度大小；
// 都一样时官方按到期先后排好再挑，先到期的在前。
func planBaseAhead(a, b resourceAccount) bool {
	if ra, rb := planBaseRank[a.PackageCode], planBaseRank[b.PackageCode]; ra != rb {
		return ra > rb
	}
	at, al := a.capacity()
	bt, bl := b.capacity()
	if ua, ub := at > 0 && al <= 0, bt > 0 && bl <= 0; ua != ub {
		return ub
	}
	if at != bt {
		return at > bt
	}
	return a.DeductionEndTime < b.DeductionEndTime
}

// credits 积分取整：池内积分是整数，四舍五入到最近整数（与企业号口径一致）。
func credits(v float64) int64 { return int64(math.Round(v)) }

// userResources 照官方查汇总与付费、免费两个逐包列表（国内版纯免费号不查付费包，见文件头）。逐包
// 列表任一失败即报错（余额与到期分桶都要用到它们）；汇总失败时 summary 为 nil，调用方用逐包合计
// 代替（生产实测两者一致；官方这时整次算失败，网关从宽）。
func (c *Client) userResources(a *auth.Auth) ([]resourceAccount, *resourceSummary, error) {
	now := time.Now().In(softRateResetLoc) // 上游计费时间都按 UTC+8 墙钟
	day := now.Format("2006-01-02")
	global := a.IsGlobal()
	var (
		wg               sync.WaitGroup
		summary          *resourceSummary
		paid, free       []resourceAccount
		paidErr, freeErr error
	)
	summed := make(chan struct{})
	wg.Add(3)
	go func() {
		defer wg.Done()
		defer close(summed)
		summary = c.resourceSummary(a)
	}()
	go func() {
		defer wg.Done()
		if !global {
			<-summed
			if summary.freeOnly() {
				return
			}
		}
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
		r.global = global
		out = append(out, r)
	}
	return out, summary, nil
}

// resourceSummary 查汇总；失败返回 nil（官方同样只记一条告警，积分改用逐包数据）。
func (c *Client) resourceSummary(a *auth.Auth) *resourceSummary {
	var data json.RawMessage
	err := c.retryBillingTransient(func() error {
		var e error
		data, e = c.authServiceJSON(a, http.MethodPost, resourceSummaryPath, map[string]any{}, withBillingLanguage(a))
		return e
	})
	if err != nil {
		return nil
	}
	var resp struct {
		Packages []struct {
			Code  string       `json:"PackageCode"`
			Total preciseCount `json:"CycleTotalCapacity"`
			Left  preciseCount `json:"CycleRemainCapacity"`
		} `json:"Packages"`
		Subscription string   `json:"SubscriptionPackageCode"`
		PaidUser     jsTruthy `json:"IsPaidUser"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return nil
	}
	s := &resourceSummary{subscription: resp.Subscription, paidUser: bool(resp.PaidUser)}
	for _, p := range resp.Packages {
		s.total += float64(p.Total)
		s.left += float64(p.Left)
		s.codes = append(s.codes, p.Code)
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
			data, e = c.authServiceJSON(a, http.MethodPost, path, body, withBillingLanguage(a))
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

// editionPackages 官方 getPersonalUsage 判版本档用的订阅包，按判定顺序排（BuddyOne 会员、旗舰、高级、
// 青春、专业、专业试用；BuddyOne 会员是 5.9 起加的）。
var editionPackages = []struct {
	edition string
	codes   []string
}{
	{"buddyonevip", []string{"TCACA_code_043_6oIkxT7Z0S"}},
	{"flagship", []string{"TCACA_code_027_0FCGVA6vSa"}},
	{"advanced", []string{"TCACA_code_026_BaESVICNoi"}},
	{"youth", []string{"TCACA_code_023_4xbGhMrE6q"}},
	{"pro", []string{"TCACA_code_002_AkiJS3ZHF5", "TCACA_code_005_maRGyrHhw1", "TCACA_code_003_FAnt7lcmRT"}},
	{"pro_trial", []string{"TCACA_code_039_KRcQj7wUat", "TCACA_code_040_mi9rCYg46x"}},
}

// subscriptionEditions 汇总的订阅包对应的版本档（官方 resolveEditionFlagsBySubscriptionCode 再经
// getEditionDisplayType）。不在表里的代码不算数，改看逐包列表。
var subscriptionEditions = map[string]string{
	"TCACA_code_043_6oIkxT7Z0S": "buddyonevip",
	"TCACA_code_027_0FCGVA6vSa": "flagship",
	"TCACA_code_026_BaESVICNoi": "advanced",
	"TCACA_code_023_4xbGhMrE6q": "youth",
	"TCACA_code_002_AkiJS3ZHF5": "pro",       // proMon
	"TCACA_code_005_maRGyrHhw1": "pro",       // proMonPlus
	"TCACA_code_003_FAnt7lcmRT": "pro",       // proYear
	"TCACA_code_039_KRcQj7wUat": "pro_trial", // proTrialMon
	"TCACA_code_040_mi9rCYg46x": "pro_trial", // proTrialYear
	"TCACA_code_001_PqouKr6QWV": "free",      // free
	"TCACA_code_008_cfWoLwvjU4": "free",      // freeMon
	"TCACA_code_035_ArVxJcGDsm": "free",      // freeMonIntl
	"TCACA_code_006_DbXS0lrypC": "free",      // gift
}

// AccountEdition 账号的版本档，照官方外观设置的 editionType（皮肤事件的 type）：账号类型是
// ultimate / exclusive 时就是它；企业号取 ultimate（resolveIntrinsicEdition 的默认值）；个人号
// 照 5.9.x 的 getPersonalUsage / getEditionDisplayType，先看汇总的订阅包（5.9 起），认不出再看
// 逐包列表里有哪档订阅包，都没有为 free。官方这一步查不到时不上报皮肤事件，所以出错时调用方也
// 不要上报。
func (c *Client) AccountEdition(a *auth.Auth) (string, error) {
	switch t := a.AccountTypeValue(); t {
	case "ultimate", "exclusive":
		return t, nil
	}
	if a.IsEnterprise() {
		return "ultimate", nil
	}
	packs, summary, err := c.userResources(a)
	if err != nil {
		return "", err
	}
	if summary != nil {
		if e, ok := subscriptionEditions[summary.subscription]; ok {
			return e, nil
		}
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
