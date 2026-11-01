// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// tasks.go growth 域「任务」接口：列表查询 / 接受 / 领取奖励。
//
// 来源：官网网页 growthSpace-*.js（路径与请求体）+ 上游 scripts/task_common.py 实测口径，
// 集成进主程序后不再需要外部脚本。
//
// 端点（全部发往 webBase，成长中心 referer）：
//   - GET  /v2/activity/growth/subscribe-task/status  订阅任务状态（网页先查，失败不影响列表）
//   - GET  /v2/activity/growth/tasks                  全量任务列表（含 progress/accept_status）
//   - POST /activity/growth/tasks/accept              {"task_codes":[...]} not_accepted → accepted
//   - POST /activity/growth/tasks/{code}/claim        完成态领奖（无请求体）
//
// 网页口径（ListTasks / AcceptTasks / ClaimReward）用网页登录态（Cookie，见 websession.go）；
// 小程序口径（*MP）是「同一份网页嵌在小程序 web-view 里」的形状：发往官网域，靠
// Authorization: Bearer 认人，X-Client-Platform 为 miniprogram，不带 Cookie。
//
// 语义要点：
//   - accept 是"报名"，不产生进度；进度由服务端行为事件点亮（如 chat_request_send 上报、
//     真实对话），故 accept 后可幂等重放。
//   - claim 仅在 progress 达标后可领；重复领返回业务错误（安全，不需要前置状态判断）。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// growth 域任务路径（与官网网页对齐）。
const (
	tasksSubscribeStatusPath = "/v2/activity/growth/subscribe-task/status"
	tasksListPath            = "/v2/activity/growth/tasks"
	// tasksAcceptPath 网页发 /activity/growth/tasks/accept（不带 /v2）；网页在小程序里打开时
	// 也是这条，所以小程序的 AcceptTasksMP 共用它。
	tasksAcceptPath = "/activity/growth/tasks/accept"
)

// mpPlatform 小程序口径头值：小程序限定任务（Sequential_Tasks_1 / school_season）
// 的列表下发、accept、claim 全链路要求 X-Client-Platform: miniprogram。
const mpPlatform = "miniprogram"

// miniProgramUA 小程序 web-view 的 UA。微信文档说小程序 web-view 的 UA 含 `miniProgram`；
// wx907c65e5e107ddcf 是官网前端里写的小程序 appid。iOS 与微信的版本号是挑的一个近期值，
// 未抓包。
const miniProgramUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 MicroMessenger/8.0.61(0x18003d2c) NetType/WIFI Language/zh_CN miniProgram/wx907c65e5e107ddcf"

// miniProgramUserAgent 小程序口径的 UA：config user_agent 显式覆盖优先，否则 miniProgramUA。
func (c *Client) miniProgramUserAgent() string {
	if c != nil && c.UserAgent != "" {
		return c.UserAgent
	}
	return miniProgramUA
}

// webMPHeaders 小程序 web-view 里那份网页的请求头：和 webHeaders 同形状，但
// X-Client-Platform 为 miniprogram、靠 Authorization: Bearer 认人（不用 Cookie），
// 不补 Sec-CH-UA*（小程序 web-view 不是桌面 Chrome）。Referer 固定成长中心页。
func (c *Client) webMPHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if req.Body != nil && req.Body != http.NoBody {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Client-Platform", mpPlatform)
	if a != nil {
		if at := a.AccessTokenValue(); at != "" {
			req.Header.Set("Authorization", "Bearer "+at)
		}
		if a.IsEnterprise() {
			// 与网页口径一致：企业号只带 X-Enterprise-Id。
			req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		}
	}
	base := c.webBase(a)
	// 浏览器同源 GET 不带 Origin；非 GET 才带。
	if req.Method != http.MethodGet {
		req.Header.Set("Origin", base)
	}
	req.Header.Set("Referer", base+webPageGrowthCenter)
	req.Header.Set("User-Agent", c.miniProgramUserAgent())
	req.Header.Set("Accept-Language", webAcceptLanguage(a, req.URL.Path))
	req.Header.Set("Accept-Encoding", acceptEncodingBrowser)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
}

// Task 单个任务的对外视图（字段名与上游 JSON 对齐，多余字段不透出）。
type Task struct {
	TaskCode     string `json:"task_code"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"` // 操作指引（含跳转说明）
	TaskDesc     string `json:"task_desc,omitempty"`   // 达成条件简述
	Credit       int64  `json:"credit,omitempty"`      // 奖励积分（上游 reward_credit）
	Energy       int64  `json:"energy,omitempty"`      // 奖励能量（上游 reward_energy）
	HasReward    bool   `json:"has_reward,omitempty"`  // 是否带奖励
	RewardBuddy  bool   `json:"reward_buddy,omitempty"`
	TaskType     string `json:"task_type,omitempty"` // single（一次性）/ 累计型
	Tag          string `json:"tag,omitempty"`       // 端标记（PC 等）
	JumpURL      string `json:"jump_url,omitempty"`  // 客户端跳转协议（workbuddy://...）
	Locked       bool   `json:"locked,omitempty"`    // 上游标记未解锁
	Target       int64  `json:"target"`              // 目标次数（恒输出：0 是有效进度值）
	Current      int64  `json:"current"`             // 当前进度（恒输出：0 是有效进度值）
	AcceptStatus string `json:"accept_status,omitempty"`
	Status       string `json:"status,omitempty"`    // 上游任务状态（complete 等）
	Claimable    bool   `json:"claimable,omitempty"` // 进度达标且未领取（本地推算）
	Claimed      bool   `json:"claimed,omitempty"`   // 已领取（accept_status == claimed）
}

// ListTasks 拉取全量任务列表（网页口径，无端标记头）。网页先查订阅任务状态再拉列表；
// 订阅状态只是让网页刷新按钮态，失败不影响拉列表（忽略它的错误），列表的错误照旧返回。
// 响应形如 data.tasks[]，元素字段随任务类型变化（progress 可能是 {current,target} 或平铺），
// 这里做宽松解析：两种形状都尝试。
func (c *Client) ListTasks(a *auth.Auth) ([]Task, error) {
	_, _ = c.webJSON(a, http.MethodGet, tasksSubscribeStatusPath, webPageGrowthCenter, nil)
	data, err := c.webJSON(a, http.MethodGet, tasksListPath, webPageGrowthCenter, nil)
	if err != nil {
		return nil, err
	}
	return parseGrowthTasks(data)
}

// ListTasksMP 拉取小程序口径的任务列表（X-Client-Platform: miniprogram）。
// 小程序限定任务（Sequential_Tasks_1「小程序首对话」/ school_season「校园日」等）
// 仅在该口径下发——默认列表不出现，accept/claim 同样要求该头（缺头 accept 返回
// task not found，上游 task_runner 实测）。**实测 mp 列表是默认口径的超集**
// （含 RichMeow/Model_chat 等常规任务 + wb_wechat_oa_subscribe_task 等 mp 专属），
// 合并时调用方须按 task_code 去重。
func (c *Client) ListTasksMP(a *auth.Auth) ([]Task, error) {
	data, err := c.growthJSONMP(a, http.MethodGet, tasksListPath, nil)
	if err != nil {
		return nil, err
	}
	return parseGrowthTasks(data)
}

// growthJSONMP 发 growth 域请求（小程序 web-view 口径：发往官网域，Bearer 认人，
// X-Client-Platform: miniprogram）并解信封。语义同 webJSON，但不用 Cookie。
func (c *Client) growthJSONMP(a *auth.Auth, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := newAccountRequest(context.Background(), a, method, c.webBase(a)+path, rdr)
	if err != nil {
		return nil, err
	}
	c.webMPHeaders(req, a)
	return c.doJSON(req)
}

// AcceptTasksMP 接受小程序限定任务（mp 头；缺头实测 task not found）。
// 幂等语义同 AcceptTasks。
func (c *Client) AcceptTasksMP(a *auth.Auth, taskCodes []string) error {
	_, err := c.growthJSONMP(a, http.MethodPost, tasksAcceptPath, map[string]any{"task_codes": taskCodes})
	return err
}

// ClaimRewardMP 领取小程序限定任务奖励：官网域 /activity/growth/tasks/{code}/claim
// + 小程序 web-view 头（上游 task_runner claim_one(mp=True) 同款）。请求本来就发往官网域，
// 官方小程序只有这一种形状，不做降级。返回 (credit, energy, err)。
func (c *Client) ClaimRewardMP(a *auth.Auth, taskCode string) (credit, energy int64, err error) {
	data, err := c.growthJSONMP(a, http.MethodPost,
		"/activity/growth/tasks/"+url.PathEscape(taskCode)+"/claim", nil)
	if err != nil {
		return 0, 0, err
	}
	credit, energy, already, err := parseClaimReward(data)
	if err == nil && !already {
		c.noteEarning(a, EarnTask, taskCode, credit)
	}
	return credit, energy, err
}

// parseClaimReward 解析领奖响应 data：{"already_claimed":bool,"credit":n,"energy":n}。already 为真时积分不是这次
// 到账的，积分获取流水不记（返回给调用方的数照旧）。
func parseClaimReward(data json.RawMessage) (credit, energy int64, already bool, err error) {
	var resp struct {
		AlreadyClaimed bool  `json:"already_claimed"`
		Credit         int64 `json:"credit"`
		Energy         int64 `json:"energy"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, 0, false, err
	}
	return resp.Credit, resp.Energy, resp.AlreadyClaimed, nil
}

// parseGrowthTasks 解析 growth 任务列表 data.tasks[]（默认与 mp 口径共用）。
func parseGrowthTasks(data json.RawMessage) ([]Task, error) {
	var resp struct {
		Tasks []struct {
			TaskCode     string          `json:"task_code"`
			Title        string          `json:"title"`
			Description  string          `json:"description"`
			TaskDesc     string          `json:"task_desc"`
			RewardCredit int64           `json:"reward_credit"` // 上游实际字段名（reward_ 前缀）
			RewardEnergy int64           `json:"reward_energy"`
			HasReward    bool            `json:"has_reward"`
			RewardBuddy  bool            `json:"reward_buddy"`
			TaskType     string          `json:"task_type"`
			Tag          string          `json:"tag"`
			JumpURL      string          `json:"jump_url"`
			Locked       bool            `json:"locked"`
			AcceptStatus string          `json:"accept_status"`
			Status       string          `json:"status"`
			Target       int64           `json:"target"`
			Current      int64           `json:"current"`
			Progress     json.RawMessage `json:"progress"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(resp.Tasks))
	for _, t := range resp.Tasks {
		cur, tgt := t.Current, t.Target
		// progress 可能是 {current,target} 对象（实测口径），覆盖平铺字段。
		if len(t.Progress) > 0 && string(t.Progress) != "null" {
			var pr struct {
				Current int64 `json:"current"`
				Target  int64 `json:"target"`
			}
			if json.Unmarshal(t.Progress, &pr) == nil && (pr.Target > 0 || pr.Current > 0) {
				cur, tgt = pr.Current, pr.Target
			}
		}
		claimed := t.AcceptStatus == "claimed"
		out = append(out, Task{
			TaskCode:     t.TaskCode,
			Title:        t.Title,
			Description:  t.Description,
			TaskDesc:     t.TaskDesc,
			Credit:       t.RewardCredit,
			Energy:       t.RewardEnergy,
			HasReward:    t.HasReward,
			RewardBuddy:  t.RewardBuddy,
			TaskType:     t.TaskType,
			Tag:          t.Tag,
			JumpURL:      t.JumpURL,
			Locked:       t.Locked,
			Target:       tgt,
			Current:      cur,
			AcceptStatus: t.AcceptStatus,
			Status:       t.Status,
			Claimable:    !claimed && tgt > 0 && cur >= tgt,
			Claimed:      claimed,
		})
	}
	return out, nil
}

// AcceptTasks 接受任务（幂等：已 accepted 时上游返回成功或业务提示，均不视为致命错误）。
func (c *Client) AcceptTasks(a *auth.Auth, taskCodes []string) error {
	_, err := c.webJSON(a, http.MethodPost, tasksAcceptPath, webPageGrowthCenter,
		map[string]any{"task_codes": taskCodes})
	return err
}

// ClaimReward 领取单个任务奖励。
//
// 端点来源（实测）：Web 成长中心的领奖请求 ——
//
//	POST https://www.workbuddy.cn/activity/growth/tasks/<task_code>/claim
//	（任务码在**路径**里，无 body；带 x-client-platform: web 头，网页登录态 Cookie 认人）
//
// 关键区别：此前误用 CLI 域 copilot.tencent.com 的
// /v2/activity/growth/tasks/reward/claim（task_code 放 body），该路径**不存在**，
// 一直返回 400 "task not completed"，是此前领奖失败的真实原因。
// 本实现返回 (credit, energy, err)：credit/energy 为本次到账奖励（已领取过时为 0）。
func (c *Client) ClaimReward(a *auth.Auth, taskCode string) (credit, energy int64, err error) {
	// 网页请求头形状见 web.go：无请求体，因此不带 Content-Type。
	data, err := c.webJSON(a, http.MethodPost,
		"/activity/growth/tasks/"+url.PathEscape(taskCode)+"/claim", webPageGrowthCenter, nil)
	if err != nil {
		return 0, 0, err
	}
	// 响应 data：{"already_claimed":bool,"credit":100,"energy":5,...}
	var resp struct {
		AlreadyClaimed bool  `json:"already_claimed"`
		Credit         int64 `json:"credit"`
		Energy         int64 `json:"energy"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, 0, err
	}
	if resp.AlreadyClaimed {
		return 0, 0, nil // 幂等：重复领取不算错误，但无新增奖励
	}
	c.noteEarning(a, EarnTask, taskCode, resp.Credit)
	return resp.Credit, resp.Energy, nil
}
