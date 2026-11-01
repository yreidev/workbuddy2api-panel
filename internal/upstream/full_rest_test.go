// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream/upstreamtest"
)

// 第七轮补齐：其余文件（desktop/global_models/travel/report/tasks/profile/
// compensation/account_catalog/device_token/school/model_catalog）。

// --- desktop.go ------------------------------------------------------------

func TestFull7ReportDesktopEventMarshalError(t *testing.T) {
	c := &Client{}
	if err := c.ReportDesktopEvent(upAuth(), DesktopEvent{"bad": func() {}}); err == nil {
		t.Fatal("不可序列化事件应返回错误")
	}
}

func TestFull7DesktopNewRequestErrors(t *testing.T) {
	a := upAuth()
	t.Run("ReportDesktopEvent", func(t *testing.T) {
		c := &Client{ChatBaseCN: "http://[::1"}
		if err := c.ReportDesktopEvent(a, DesktopEvent{"eventCode": "x"}); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
	t.Run("SetAppearanceTheme", func(t *testing.T) {
		c := &Client{ChatBaseCN: "http://[::1"}
		if err := c.SetAppearanceTheme(a, "light"); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
	t.Run("ReportWebEvent", func(t *testing.T) {
		c := &Client{WebBaseCN: "http://[::1"}
		if err := c.ReportWebEvent(a, "code", "/p", "", ""); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
	t.Run("MarketExpertList", func(t *testing.T) {
		c := &Client{ChatBaseCN: "http://[::1"}
		if _, err := c.MarketExpertList(a, "agent"); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
	t.Run("DesktopChatWithExpert", func(t *testing.T) {
		c := &Client{ChatBaseCN: "http://[::1"}
		if _, _, err := c.DesktopChatWithExpert(a, "", false, false); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
}

func TestFull7DesktopChatWithExpertTransportError(t *testing.T) {
	c := &Client{ChatBaseCN: "https://d.example", HTTP: &http.Client{Transport: rtFunc(
		func(*http.Request) (*http.Response, error) { return nil, errors.New("dial boom") },
	)}}
	if _, _, err := c.DesktopChatWithExpert(upAuth(), "", false, false); err == nil || !strings.Contains(err.Error(), "dial boom") {
		t.Fatalf("err = %v", err)
	}
}

// --- global_models.go ------------------------------------------------------

func TestFull7FetchGlobalModelsDedup(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/v3/config", 500, `{"code":500,"msg":"boom"}`)
	f.on(http.MethodGet, gmEntPath, 200, entModelsJSON(`{"id":"a","tags":["chat"]},{"id":"a","tags":["chat"]}`))
	names := upGlobalClient(f).FetchGlobalModels(globalEntAuth())
	if len(names) != 1 || names[0] != "a" {
		t.Fatalf("names = %v", names)
	}
}

func TestFull7GlobalModelsNameFallbacks(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/v3/config", 200, `{"code":0,"data":{"models":[{"id":"nes-x"}]}}`)
	f.on(http.MethodGet, gmEntPath, 200, entModelsJSON(`{"id":"ent-1","tags":["chat"]}`))
	c := upGlobalClient(f)
	names := c.FetchGlobalModels(globalEntAuth())
	if len(names) != 1 || names[0] != "ent-1" {
		t.Fatalf("names = %v", names)
	}
	if infos := c.FetchGlobalModelInfos(globalEntAuth()); len(infos) != 1 || infos[0].ID != "ent-1" {
		t.Fatalf("企业目录应产出对象条目: %+v", infos)
	}
}

// --- travel.go -------------------------------------------------------------

func TestFull7WebJSONErrors(t *testing.T) {
	a := upAuth()
	if _, err := (&Client{}).webJSON(a, http.MethodPost, "/x", webPageGrowthCenter, func() {}); err == nil {
		t.Fatal("不可序列化 body 应返回错误")
	}
	if _, err := (&Client{WebBaseCN: "http://[::1"}).webJSON(a, http.MethodGet, "/x", webPageGrowthCenter, nil); err == nil {
		t.Fatal("非法 base 应返回错误")
	}
}

func TestFull7TravelAndBuddyErrors(t *testing.T) {
	a := upAuth()
	t.Run("TravelStatus 解析失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, travelStatusPath, 200, `{"code":0,"data":"oops"}`)
		if _, err := upClient(f).TravelStatus(a); err == nil {
			t.Fatal("data 类型不符应返回解析错误")
		}
	})
	t.Run("TravelClaim 请求失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, travelClaimPath, 400, `{"code":10001,"msg":"no"}`)
		if _, err := upClient(f).TravelClaim(a); err == nil {
			t.Fatal("应返回上游错误")
		}
	})
	t.Run("BuddyInfo 请求失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, buddyInfoPath, 400, `{"code":10001,"msg":"no"}`)
		if _, err := upClient(f).BuddyInfo(a); err == nil {
			t.Fatal("应返回上游错误")
		}
	})
	t.Run("BuddyInfo 响应解析失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, buddyInfoPath, 200, `{"code":0,"data":"oops"}`)
		if _, err := upClient(f).BuddyInfo(a); err == nil {
			t.Fatal("data 类型不符应返回解析错误")
		}
	})
	t.Run("BuddyInfo buddy 字段类型不符", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, buddyInfoPath, 200, `{"code":0,"data":{"buddy":{"id":"x"}}}`)
		if _, err := upClient(f).BuddyInfo(a); err == nil {
			t.Fatal("buddy.id 类型不符应返回解析错误")
		}
	})
	t.Run("GrowthStreak 解析失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, streakPath, 200, `{"code":0,"data":"oops"}`)
		if _, err := upClient(f).GrowthStreak(a); err == nil {
			t.Fatal("data 类型不符应返回解析错误")
		}
	})
}

// --- report.go -------------------------------------------------------------

func TestFull7BillingJSONErrors(t *testing.T) {
	a := upAuth()
	if _, err := (&Client{}).billingJSON(a, http.MethodPost, "/x", func() {}); err == nil {
		t.Fatal("不可序列化 body 应返回错误")
	}
	if _, err := (&Client{BillingBaseCN: "http://[::1"}).billingJSON(a, http.MethodGet, "/x", nil); err == nil {
		t.Fatal("非法 base 应返回错误")
	}
}

func TestFull7IsTransientBillingErr(t *testing.T) {
	if isTransientBillingErr(nil) {
		t.Fatal("nil 不应判为瞬时错误")
	}
	if !isTransientBillingErr(errors.New("network")) {
		t.Fatal("网络层错误应判瞬时")
	}
	if !isTransientBillingErr(&Error{Kind: ErrServer}) {
		t.Fatal("5xx 应判瞬时")
	}
	if isTransientBillingErr(&Error{Kind: ErrClient}) {
		t.Fatal("4xx 不应判瞬时")
	}
}

func TestFull7ReportChatActivityModelDefaults(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodPost, reportPath, 200, `{"code":0}`)
	c := upClient(f)
	if err := c.ReportChatActivityModel(upAuth(), "conv-1", "", "", ""); err != nil {
		t.Fatalf("err = %v", err)
	}
	reqs := f.seen()
	if len(reqs) != 1 {
		t.Fatalf("请求数 = %d", len(reqs))
	}
	var events []map[string]any
	if err := json.Unmarshal(reqs[0].body, &events); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	ev := events[0]
	if id, _ := ev["requestId"].(string); !upstreamtest.UUIDv7Hex.MatchString(id) || ev["requestModelId"] != "deepseek-v4-flash" || ev["requestModelName"] != "deepseek-v4-flash" {
		t.Fatalf("event = %+v", ev)
	}
}

// --- tasks.go --------------------------------------------------------------

func TestFull7TasksErrors(t *testing.T) {
	a := upAuth()
	t.Run("ListTasksMP 请求失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, tasksListPath, 400, `{"code":10001,"msg":"no"}`)
		if _, err := upClient(f).ListTasksMP(a); err == nil {
			t.Fatal("应返回上游错误")
		}
	})
	t.Run("growthJSONMP 序列化失败", func(t *testing.T) {
		if _, err := (&Client{}).growthJSONMP(a, http.MethodPost, "/x", func() {}); err == nil {
			t.Fatal("不可序列化 body 应返回错误")
		}
	})
	t.Run("growthJSONMP 非法 base", func(t *testing.T) {
		// 小程序口径发往官网域（webBase），不再是 chat 域。
		if _, err := (&Client{WebBaseCN: "http://[::1"}).growthJSONMP(a, http.MethodGet, "/x", nil); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
	t.Run("ClaimRewardMP 非法 base", func(t *testing.T) {
		c := &Client{WebBaseCN: "http://[::1"}
		if _, _, err := c.ClaimRewardMP(a, "task"); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
	t.Run("任务列表解析失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, tasksSubscribeStatusPath, 200, `{"code":0,"data":{}}`)
		f.on(http.MethodGet, tasksListPath, 200, `{"code":0,"data":"oops"}`)
		if _, err := upClient(f).ListTasks(a); err == nil {
			t.Fatal("data 类型不符应返回解析错误")
		}
	})
	t.Run("ClaimReward 非法 base", func(t *testing.T) {
		c := &Client{WebBaseCN: "http://[::1"}
		if _, _, err := c.ClaimReward(a, "task"); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
}

// --- profile.go ------------------------------------------------------------

func TestFull7FetchAccountProfileErrors(t *testing.T) {
	a := upAuth()
	t.Run("非法 base", func(t *testing.T) {
		c := &Client{WebBaseCN: "http://[::1"}
		if _, err := c.FetchAccountProfile(a); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
	t.Run("解析失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodGet, "/console/account", 200, `{"code":0,"data":"oops"}`)
		if _, err := upClient(f).FetchAccountProfile(a); err == nil || !strings.Contains(err.Error(), "profile parse") {
			t.Fatalf("err = %v", err)
		}
	})
}

// --- compensation.go -------------------------------------------------------

func TestFull7CompensationErrors(t *testing.T) {
	a := upAuth()
	t.Run("状态请求失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, "/billing/meter/compensation-status", 400, `{"code":10001,"msg":"no"}`)
		if _, err := upClient(f).GetCompensationStatus(a); err == nil {
			t.Fatal("应返回上游错误")
		}
	})
	t.Run("领取结果解析失败", func(t *testing.T) {
		f := newUpFake(t)
		f.on(http.MethodPost, "/billing/meter/claim-compensation", 200, `{"code":0,"data":"oops"}`)
		if _, err := upClient(f).ClaimCompensation(a); err == nil || !strings.Contains(err.Error(), "解析补偿领取结果") {
			t.Fatalf("err = %v", err)
		}
	})
}

// --- account_catalog.go ----------------------------------------------------

func TestFull7FetchAccountCatalogProbeError(t *testing.T) {
	f := newUpFake(t)
	f.on(http.MethodGet, "/v3/config", 500, `{"code":500,"msg":"boom"}`)
	f.on(http.MethodGet, "/console/enterprises/ent-1/config/models", 500, `{"code":500,"msg":"boom"}`)
	ea := &auth.Auth{UID: "ue", AccessToken: "at", EnterpriseID: "ent-1", ExpiresAt: 9999999999}
	if _, err := upClient(f).FetchAccountCatalog(ea); err == nil {
		t.Fatal("两路探测失败应返回错误")
	}
}

func TestFull7EnterpriseOnlyModelsRealmAndSort(t *testing.T) {
	c := &Client{}
	c.realmModels = map[string]map[string]bool{"cn": {"z": true}}
	c.accountCatalogs = map[string]accountCatalog{
		"u-global": {realm: "global", infos: []ModelInfo{{ID: "g"}}},
		"u-cn":     {realm: "cn", infos: []ModelInfo{{ID: "b"}, {ID: "a"}, {ID: "z"}}},
	}
	out := c.EnterpriseOnlyModels("cn")
	if len(out) != 2 || out[0].ID != "a" || out[1].ID != "b" {
		t.Fatalf("out = %+v", out)
	}
}

// --- device_token.go -------------------------------------------------------

func TestFull7ReadTrimmedFileReadError(t *testing.T) {
	if _, err := readTrimmedFile(t.TempDir(), 100); err == nil {
		t.Fatal("读目录应返回错误")
	}
}

// --- school.go -------------------------------------------------------------

func TestFull7SchoolErrors(t *testing.T) {
	a := upAuth()
	t.Run("schoolJSON 非法 base", func(t *testing.T) {
		c := &Client{BillingBaseCN: "http://[::1"}
		if _, err := c.SchoolVouchers(a); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
	t.Run("ReportMPEvent 序列化失败", func(t *testing.T) {
		c := &Client{}
		if err := c.ReportMPEvent(a, map[string]any{"bad": func() {}}); err == nil {
			t.Fatal("不可序列化事件应返回错误")
		}
	})
	t.Run("ReportMPEvent 非法 base", func(t *testing.T) {
		c := &Client{BillingBaseCN: "http://[::1"}
		if err := c.ReportMPEvent(a, map[string]any{"eventCode": "x"}); err == nil {
			t.Fatal("非法 base 应返回错误")
		}
	})
}

// --- model_catalog.go ------------------------------------------------------

func TestFull7SaveLockedEmptyPath(t *testing.T) {
	m := &modelCatalog{}
	m.mu.Lock()
	m.saveLocked()
	m.mu.Unlock()
}

// --- idle.go ---------------------------------------------------------------

func TestFull7IdleTickFloor(t *testing.T) {
	if got := idleTick(20 * time.Millisecond); got != 10*time.Millisecond {
		t.Fatalf("idleTick(20ms) = %v，应钳到 10ms", got)
	}
}

// --- thinking.go -----------------------------------------------------------

func TestFull7ThinkingSkipsNonMapMessages(t *testing.T) {
	src := []byte(`{"model":"deepseek-v4-flash","messages":["junk",{"role":"assistant","reasoning_content":"prev"},{"role":"user","content":"hi"}]}`)
	out := PrepareBodyOpt(src, false)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("输出不是 JSON: %v", err)
	}
	msgs, ok := obj["messages"].([]any)
	if !ok || len(msgs) != 3 {
		t.Fatalf("messages = %#v", obj["messages"])
	}
	asst, _ := msgs[1].(map[string]any)
	if asst["reasoning_content"] != "prev" {
		t.Fatalf("assistant = %#v", asst)
	}
}

// --- global_register.go ----------------------------------------------------

func TestFull7GlobalRegisterReqMarshalError(t *testing.T) {
	if _, err := (&Client{}).globalRegisterReq(&auth.Auth{}, http.MethodPost, "https://x/", func() {}); err == nil {
		t.Fatal("不可序列化 body 应返回错误")
	}
}

// --- modelsdev.go ----------------------------------------------------------

// 反复解析（每次 map 迭代序不同），确保 vendor 源优先 / 同票字典序
// 两条候选选择分支都被执行。
func TestF7ModelsDevVendorPriority(t *testing.T) {
	for n := 0; n < 64; n++ {
		raw := `{"aaa":{"models":{"dup":{"limit":{"context":100,"output":10}},"u":{"limit":{"context":100,"output":10}}}},` +
			`"zai":{"models":{"dup":{"limit":{"context":100,"output":10}},"u":{"limit":{"context":200,"output":20}}}}}`
		doc, err := parseModelsDevDoc([]byte(raw))
		if err != nil {
			t.Fatalf("解析: %v", err)
		}
		if got := doc["dup"].Context; got != 100 {
			t.Fatalf("dup context = %d", got)
		}
		if got := doc["u"].Context; got != 200 {
			t.Fatalf("vendor 源应优先，u context = %d，期望 200", got)
		}
	}
}

func TestF7VendorTieBreak(t *testing.T) {
	for n := 0; n < 64; n++ {
		raw := `{"bbb":{"models":{"o":{"limit":{"context":300,"output":30}}}},` +
			`"ccc":{"models":{"o":{"limit":{"context":400,"output":40}}}}}`
		doc, err := parseModelsDevDoc([]byte(raw))
		if err != nil {
			t.Fatalf("解析: %v", err)
		}
		if got := doc["o"].Context; got != 300 {
			t.Fatalf("context = %d，期望字典序最小的 bbb 胜出（300）", got)
		}
	}
}
