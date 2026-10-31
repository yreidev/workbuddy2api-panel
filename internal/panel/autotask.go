// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// autotask.go 面板「一键完成」任务的动作实现。
//
// 设计依据：上游 scripts/task_*.py 实测结论 + 2026-09-12 桌面指纹协议逆向
// （data/desktop-task-protocol.md）——
//   - first_buddy（+300 分）：report（前置解锁）→ agreement → buddy/first
//   - chat_5（+100 分）：累计 5 条 chat_request_send 上报
//   - Model_chat_GLM5.2（+100 分）：accept → 用 glm-5.2 真实对话一次 → 上报（模型字段对齐）
//   - RichMeow_Chat：桌面指纹（workbuddy-desktop）完整对话事件链，纯 API 可点亮（三账号实测）
//   - Buddy_App / Buddy_App_QQ：buddyapp 五连事件，纯 API 可点亮（两账号实测）
//   - automation_1：automated_task_create_suc 事件，纯 API 可点亮（两账号实测）
//   - Library_read：web 域 web_element_click(library_doc_intro_click)（三账号实测）
//   - template_5 / playbook_prompt / create_canvas：asar 逆向出的判据事件
//     （template_used / playbook_prompt_send / wbx_design_canvas_*），纯 API 可点亮（三账号实测）
//   - expert_5 / Expert_team_use_3：真实专家列表 + 召唤链 + 真实 chat（服务端 requestId）
//   - expert_actual_use（三账号实测）
//   - Hp_Appearance：appearance/set + appearance_skin_apply 事件（两账号实测）
//
// 仍未破解：skill_1（疑似要求真实 Skill 工具调用）。
// 不做：Expert_lighthouse（需真实连接器授权）、Expert_Philanthropy（真实捐款）。
//
// 所有动作幂等：已 claimed/已达标的任务直接跳过，不重复消耗上游配额。
package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

// autoAction 一个可自动化的任务动作。
type autoAction struct {
	TaskCode string // 目标任务 code
	Desc     string // 展示用说明
	Attempt  bool   // true = 尝试型（上游未证实可脚本化，跑了可能不点亮）
	run      func(p *Panel, a *auth.Auth) (string, error)
}

// autoActions 已实现的任务动作表（顺序即执行顺序：先解锁依赖项）。
// first_buddy 依赖活跃上报解锁，故 chat_5/first_buddy 的执行都自带 report 步骤。
var autoActions = []autoAction{
	{
		TaskCode: "chat_5",
		Desc:     "上报 5 条对话活跃事件（自动补足差额）",
		run:      runChat5,
	},
	{
		TaskCode: "first_buddy",
		Desc:     "上报解锁 → 同意协议 → 领取第一只 Buddy（+300 分）",
		run:      runFirstBuddy,
	},
	{
		TaskCode: "Model_chat_GLM5.2",
		Desc:     "接受任务 → glm-5.2 真实对话一次 → 对齐模型上报",
		run:      runModelChat,
	},
	{
		TaskCode: "RichMeow_Chat",
		Desc:     "桌面指纹事件链上报（已验证：纯 API 可点亮，三账号实测）",
		run:      runRichMeow,
	},
	{
		TaskCode: "Buddy_App",
		Desc:     "上报「进入 Buddy 应用」事件链（已验证：纯 API 可点亮）",
		run:      runBuddyApp,
	},
	{
		TaskCode: "Buddy_App_QQ",
		Desc:     "上报「进入企鹅教师助手」事件链（已验证：纯 API 可点亮）",
		run:      runBuddyApp,
	},
	{
		TaskCode: "automation_1",
		Desc:     "上报「定时任务创建」事件（已验证：纯 API 可点亮）",
		run:      runAutomationCreate,
	},
	{
		TaskCode: "Library_read",
		Desc:     "上报「读资料库介绍」事件（已验证：纯 API 可点亮）",
		run:      runLibraryRead,
	},
	{
		TaskCode: "template_5",
		Desc:     "上报「使用模板创建任务」事件组 ×5（已验证：三账号点亮）",
		run:      runTemplateUse,
	},
	{
		TaskCode: "playbook_prompt",
		Desc:     "上报「灵感案例做同款发送 Prompt」事件组（已验证：三账号点亮）",
		run:      runPlaybookPrompt,
	},
	{
		TaskCode: "create_canvas",
		Desc:     "上报「设计创意画布创建」事件组（已验证：三账号点亮，+300 分）",
		run:      runCreateCanvas,
	},
	{
		TaskCode: "expert_5",
		Desc:     "真实专家召唤+使用链 ×5（专家市场列表+真实 chat，已验证：三账号点亮）",
		run:      runExpertUse,
	},
	{
		TaskCode: "Expert_team_use_3",
		Desc:     "真实专家团召唤+使用链 ×3（已验证：三账号点亮）",
		run:      runExpertTeamUse,
	},
	{
		TaskCode: "Hp_Appearance",
		Desc:     "设置主题 API + 皮肤生效事件（已验证：两账号点亮）",
		run:      runAppearance,
	},
	{
		TaskCode: "skill_1",
		Desc:     "真实对话 + skill_info 技能加载事件（已验证：人杰2 点亮）",
		run:      runSkillFresh,
	},
	{
		TaskCode: "Expert_lighthouse",
		Desc:     "真实轻量云专家召唤+使用链（chat 链带 has_expert，已验证：两账号点亮）",
		run:      runExpertLighthouse,
	},
	// 「体验某位专家」类任务（2026-10 官方新上），照 expert_5 的判据做；2026-10-28 生产实测都点亮
	// （四个 expert_use_* 在 1.6.0 上，WorkBuddy_Valorant 改成按名字搜索后在 1.7.0 上）。
	{
		TaskCode: "WorkBuddy_Valorant",
		Desc:     "在专家页搜「无畏契约拼豆专家」→ 召唤 + 真实对话一次（生产实测点亮）",
		run:      runExpertTask(expertTaskTarget{Name: "无畏契约拼豆专家"}),
	},
	{
		TaskCode: "expert_use_maidehao",
		Desc:     "召唤「中国电商运营专家」+ 真实对话一次（生产实测点亮）",
		run:      runExpertTask(expertTaskTarget{ID: "ex_GDl31WahyIH0", Name: "中国电商运营专家"}),
	},
	{
		TaskCode: "expert_use_douyinyin",
		Desc:     "召唤「抖音策略师」+ 真实对话一次（生产实测点亮）",
		run:      runExpertTask(expertTaskTarget{ID: "ex_FbY5nlWTqDod", Name: "抖音策略师"}),
	},
	{
		TaskCode: "expert_use_shangjia_xuanchuan",
		Desc:     "召唤「商家宣传页与海报设计师」+ 真实对话一次（生产实测点亮）",
		run:      runExpertTask(expertTaskTarget{ID: "ex_Weo3x2EZuSDm", Name: "商家宣传页与海报设计师"}),
	},
	{
		TaskCode: "expert_use_bowangwang",
		Desc:     "召唤「直播电商教练」+ 真实对话一次（生产实测点亮）",
		run:      runExpertTask(expertTaskTarget{ID: "ex_WZY136wV5aMt", Name: "直播电商教练"}),
	},
	{
		TaskCode: "black_cat",
		Desc:     "夜猫子：23:00–08:00 窗口内 glm-5.2 对话补足（窗口外提示稍后再试）",
		Attempt:  true,
		run:      runBlackCat,
	},
	{
		TaskCode: "school_season",
		Desc:     "校园日（小程序口径）：accept → mini 对话+activityId 上报 → 领奖（+100c+5e）",
		run:      runSchoolSeason,
	},
	{
		TaskCode: "Sequential_Tasks_1",
		Desc:     "小程序首对话（小程序口径）：accept → mini 对话上报 → 领奖（+100c+5e）",
		run:      runSequentialChat,
	},
	{
		TaskCode: "Sequential_Tasks_2",
		Desc:     "小程序选专家对话（小程序口径）：市场专家 id → accept → expert_actual_use 上报 → 领奖（+200c+5e）",
		run:      runMiniExpert,
	},
	{
		TaskCode: "Sequential_Tasks_3",
		Desc:     "小程序五次对话（小程序口径）：accept → mini 对话上报 ×5（自动补差额）→ 领奖（+300c+5e）",
		run:      runSequentialChat5,
	},
	{
		TaskCode: "Sequential_Tasks_4",
		Desc:     "小程序定时任务（预留，每日零点解锁一环）：accept → 定时任务创建事件（PC 同源）→ 领奖（判据待解锁验证）",
		run:      runSequentialAutomation,
	},
	{
		TaskCode: "Sequential_Tasks_5",
		Desc:     "小程序使用 GLM5.2（预留）：accept → 带模型字段的 mini 对话上报 → 领奖（判据待解锁验证）",
		run:      runSequentialModelChat,
	},
	{
		TaskCode: "Sequential_Tasks_6",
		Desc:     "小程序十次对话（预留）：accept → mini 对话上报 ×target（自动补差额）→ 领奖",
		run:      runSequentialChat10,
	},
	{
		TaskCode: "Sequential_Tasks_7",
		Desc:     "体验灵感功能（预留，疑 PC 口径 +500c+5e）：accept → 灵感事件组（PC+mp 双形态）→ 领奖（判据待解锁验证）",
		run:      runSequentialPlaybook,
	},
}

// autoActionFor 查任务对应的动作；无则返回 nil（不可自动化）。
func autoActionFor(code string) *autoAction {
	for i := range autoActions {
		if autoActions[i].TaskCode == strings.TrimSpace(code) {
			return &autoActions[i]
		}
	}
	return nil
}

// autoActionIndex 任务在 autoActions 中的顺序（队列执行按依赖序排；未知返回大值）。
func autoActionIndex(code string) int {
	for i := range autoActions {
		if autoActions[i].TaskCode == code {
			return i
		}
	}
	return 1 << 20
}

// mpTaskCodes 小程序口径专属下发的成长任务：默认（无 mp 头）列表不出现，
// accept/claim 均要求 X-Client-Platform: miniprogram。新任务出现时在此登记。
var mpTaskCodes = map[string]bool{
	"school_season":      true, // 校园日（mini chat + activityId）
	"Sequential_Tasks_1": true, // 小程序首对话（mini chat，无 activityId）
	"Sequential_Tasks_2": true, // 小程序选中专家并完成有效对话（mp 指纹 expert_actual_use）
	"Sequential_Tasks_3": true, // 小程序完成 5 次对话（与 Tasks_1 同形状，target=5 逐条累加）
	// Tasks_4..7 存在性已实测（accept 返回 prerequisite not met 链式依赖；Tasks_8 not
	// found 封顶）。链条每日零点解锁一环（task locked until 次日），判据为 issue #42
	// 描述 + mpsrc 事件形状预置，解锁后逐个实测校正。
	"Sequential_Tasks_4": true,
	"Sequential_Tasks_5": true,
	"Sequential_Tasks_6": true,
	"Sequential_Tasks_7": true,
}

// isMPTaskCode 报告任务是否小程序口径专属（决定回读/接受/领奖走 mp 变体）。
func isMPTaskCode(code string) bool { return mpTaskCodes[code] }

// claimTaskReward 按任务码的口径领奖：小程序专属码走 mp 端点，其余走网页端。
// 领奖幂等：已经领过的回 already_claimed，记 0 分 0 能。
func claimTaskReward(p *Panel, a *auth.Auth, code string) (credit, energy int64, err error) {
	if isMPTaskCode(code) {
		return p.cfg.Upstream.ClaimRewardMP(a, code)
	}
	return p.cfg.Upstream.ClaimReward(a, code)
}

// taskByCode 拉取任务列表并定位单个任务；未找到返回 nil（不视为错误）。
// 双口径：mp 专属任务在默认列表查不到，自动回落 mp 列表（仅对已登记的 mp 码，
// 未知码不多打一次上游）。
func (p *Panel) taskByCode(a *auth.Auth, code string) (*upstream.Task, error) {
	tasks, err := p.cfg.Upstream.ListTasks(a)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if tasks[i].TaskCode == code {
			return &tasks[i], nil
		}
	}
	if isMPTaskCode(code) {
		return p.taskByCodeMP(a, code)
	}
	return nil, nil
}

// claimPollAttempts / claimPollGap 达标回读的有界轮询参数。
// 背景：上游计分是**异步**的——行为事件上报后进度要数秒才刷新（实测 Model_chat
// 对话完成后立即回读仍是 0/1，约 5-8 秒后才变 1/1）。一次性回读会误判"未达标"，
// 从而跳过自动领奖。这里最多轮询 N 次、每次间隔 gap，总预算约 12 秒。
var (
	claimPollAttempts = 4
	claimPollGap      = 3 * time.Second
)

// taskByCodeWaiting 回读任务，若未达标则在有界预算内轮询等待（上游异步计分）。
// 已达标（claimable）立即返回；预算耗尽返回最后一次结果（可能仍未达标）。
func (p *Panel) taskByCodeWaiting(a *auth.Auth, code string) (*upstream.Task, error) {
	t, err := p.taskByCode(a, code)
	if err != nil || t == nil {
		return t, err
	}
	if t.Claimable || t.Claimed {
		return t, nil
	}
	for i := 1; i < claimPollAttempts; i++ {
		time.Sleep(claimPollGap)
		t2, err2 := p.taskByCode(a, code)
		if err2 != nil {
			return t, nil // 轮询期间的查询失败不覆盖已拿到的结果
		}
		if t2 != nil {
			t = t2
			if t.Claimable || t.Claimed {
				return t, nil
			}
		}
	}
	return t, nil
}

// taskByCodeMP 以小程序口径拉取任务列表并定位单个任务；未找到返回 nil。
// 小程序限定任务（school_season / Sequential_Tasks_1）在默认口径列表不出现。
func (p *Panel) taskByCodeMP(a *auth.Auth, code string) (*upstream.Task, error) {
	tasks, err := p.cfg.Upstream.ListTasksMP(a)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if tasks[i].TaskCode == code {
			return &tasks[i], nil
		}
	}
	return nil, nil
}

// acceptWithVerifyMP accept 并回读验证登记生效：上游存在 200+OK 但 accept 未真正
// 登记的形态（此时上报事件全部不归账，任务永远点不亮，上游 task_runner c793ae3
// 实测）——判定以回读 accept_status 为准，未生效重试一次。
func (p *Panel) acceptWithVerifyMP(a *auth.Auth, code string) bool {
	for attempt := 1; attempt <= 2; attempt++ {
		if err := p.cfg.Upstream.AcceptTasksMP(a, []string{code}); err != nil {
			log.Printf("autotask %s %s: accept 尝试%d: %v", logfmt.Label(a.UID, a.Nickname), code, attempt, err)
			continue
		}
		time.Sleep(mpActionGap)
		t, err := p.taskByCodeMP(a, code)
		if err == nil && t != nil && t.AcceptStatus != "not_accepted" && t.AcceptStatus != "" {
			return true
		}
		log.Printf("autotask %s %s: accept 尝试%d 未登记生效（回读=%q）", logfmt.Label(a.UID, a.Nickname), code, attempt, acceptStatusOr(t))
	}
	return false
}

// acceptStatusOr 安全读取任务 accept_status（nil 任务返回 "?"）。
func acceptStatusOr(t *upstream.Task) string {
	if t == nil {
		return "?"
	}
	if t.AcceptStatus == "" {
		return "?"
	}
	return t.AcceptStatus
}

// mpActionGap mp 任务写动作间隔（accept/上报/领奖之间，防频控）。
var mpActionGap = 2 * time.Second

// mpChatEventGap mp 对话事件（chat_request_send）的真人节奏间隔。上游对
// Sequential_Tasks_3「5 次有效对话」有反作弊校验：数秒级连发的事件会先被计入
// 进度（回读 5/5、accept_status 甚至短暂转 completed），随后被判定无效整体回滚
// （进度回落、claim 返回 400 "task not completed"）——2026-09-26 实测 2s 连发
// 4 条全灭，45s 间隔逐条上报全存活且 claim +300c+5e 成功。每条上报前
// sleep gap + 0~10s 抖动；首条也等（上一轮残留进度被回滚后立即重报同样无效）。
var mpChatEventGap = 45 * time.Second

// mpChatEventJitter 叠加在 mpChatEventGap 上的随机抖动上限。
var mpChatEventJitter = 10 * time.Second

// randDuration 返回 [0, limit) 内的随机时长；limit<=0 时为 0。
func randDuration(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(limit)))
}

// runMPMiniChatTask growth 域小程序限定任务通用闭环：
// mp 查询 → accept（带登记回读验证）→ mini chat 事件上报（withActivityId 决定
// 是否带开学季 activityId：school_season 必带，Sequential_Tasks_1 不带——服务端按
// source=mini_program 指纹关联）→ 回读 → 达标即领奖。
func (p *Panel) runMPMiniChatTask(a *auth.Auth, code string, withActivityId bool) (string, error) {
	t, err := p.taskByCodeMP(a, code)
	if err != nil {
		return "", err
	}
	if t == nil {
		return "mp 口径未下发该任务（活动可能已结束）", nil
	}
	if t.Claimed {
		return "已领取", nil
	}
	if t.AcceptStatus == "not_accepted" || t.AcceptStatus == "" {
		if !p.acceptWithVerifyMP(a, code) {
			return "accept 未登记生效（上游 200+OK 但未落账形态），待下次重试", nil
		}
		// accept 前的任务进度为 null（target 下发 0），兜底 target=1 会少报——
		// Tasks_6 首轮实测：accept 后真实 target=10，只补 1 条就误判达标去领奖
		// （claim 400 task not completed）。接受后回读一次拿真实 target/current。
		if t2, err := p.taskByCodeMP(a, code); err == nil && t2 != nil {
			t = t2
		}
	}
	// 已达标（含 completed 未领）：直接领奖。
	target := t.Target
	if target <= 0 {
		target = 1
	}
	if t.Current >= target || t.AcceptStatus == "completed" {
		credit, energy, err := p.cfg.Upstream.ClaimRewardMP(a, code)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已领取奖励（+%dc +%de）", credit, energy), nil
	}
	// 判据上报：按差额补 mini chat 事件。每条前 sleep mpChatEventGap+抖动——
	// 连发会被上游反作弊判无效（见 mpChatEventGap 注释），宁可慢不可白报。
	need := target - t.Current
	for i := int64(0); i < need; i++ {
		time.Sleep(mpChatEventGap + randDuration(mpChatEventJitter))
		conv := upstream.NewConversationID()
		var ev map[string]any
		if withActivityId {
			ev = upstream.SchoolSeasonChatEvent(conv)
		} else {
			ev = upstream.SchoolChatTimesEvents(conv)
		}
		if err := p.cfg.Upstream.ReportMPEvent(a, ev); err != nil {
			return fmt.Sprintf("完成 %d/%d 次上报后中断: %v", i, need, err), nil
		}
	}
	// 回读（异步计分，有界轮询复用 claimPoll 预算的紧凑版：两轮各隔 3s）。
	for i := 0; i < 2; i++ {
		time.Sleep(claimPollGap)
		t2, err2 := p.taskByCodeMP(a, code)
		if err2 != nil || t2 == nil {
			continue
		}
		t = t2
		if t.Claimable || t.Claimed || t.Current >= target {
			break
		}
	}
	if t.Claimed {
		return "本轮已入账（claimed）", nil
	}
	if t.Current < target {
		return fmt.Sprintf("已上报 %d 次但进度未达 %d/%d（异步计分未归账，下次重试）", need, t.Current, target), nil
	}
	credit, energy, err := p.cfg.Upstream.ClaimRewardMP(a, code)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("任务点亮并领取奖励（+%dc +%de）", credit, energy), nil
}

// runSchoolSeason 完成 school_season「校园日」（growth 域小程序限定）。
// 判据 = mini chat_request_send + activityId=school_open_day_2026（无 activityId
// 不点亮，与 school 域开学季同活动关联；上游 task_runner e2e 实测 +100c+5e）。
func runSchoolSeason(p *Panel, a *auth.Auth) (string, error) {
	return p.runMPMiniChatTask(a, "school_season", true)
}

// runSequentialChat 完成 Sequential_Tasks_1「小程序内完成 1 次有效对话」。
// 判据 = mini chat_request_send（无 activityId，服务端按 source=mini_program
// 指纹关联；上游 task_runner 实测 +100c+5e）。
func runSequentialChat(p *Panel, a *auth.Auth) (string, error) {
	return p.runMPMiniChatTask(a, "Sequential_Tasks_1", false)
}

// runSequentialChat5 完成 Sequential_Tasks_3「在小程序内完成 5 次有效对话」。
// 判据与 Sequential_Tasks_1 同形状（mini 指纹 chat_request_send，无 activityId），
// 仅 target=5——服务端按上报条数累加进度，但**要求真人节奏**：连发事件先计数
// 后被反作弊回滚（claim 400 "task not completed"），由 mpChatEventGap 间隔保证
// （2026-09-26 实测：45s 间隔补满 5/5 → claim +300c+5e 成功，领后 accept_status
// =claimed 稳定不回滚）。
func runSequentialChat5(p *Panel, a *auth.Auth) (string, error) {
	return p.runMPMiniChatTask(a, "Sequential_Tasks_3", false)
}

// runSequentialChat10 完成 Sequential_Tasks_6「在小程序内完成 10 次有效对话」（预留）。
// 判据假定与 Tasks_1/3 同形状（mini chat_request_send），target 由任务自带（回读），
// runMPMiniChatTask 按差额补报——issue #42 称 target=10，以解锁后实际下发为准。
// 真人节奏间隔同样适用（mpChatEventGap）：9 条 × ~50s ≈ 8 分钟/账号，夜间队列可接受。
func runSequentialChat10(p *Panel, a *auth.Auth) (string, error) {
	return p.runMPMiniChatTask(a, "Sequential_Tasks_6", false)
}

// runSequentialEventTask Sequential 链预留任务通用骨架：mp 查询 → accept（带验证）
// → 判据事件上报（primary；未点亮且 fallback 非空时补一轮）→ 回读 → 达标领奖。
// 每日零点解锁一环：locked 期间 accept 不落账，返回等下次调度（无需人工干预）。
func (p *Panel) runSequentialEventTask(a *auth.Auth, code string, primary, fallback func() error) (string, error) {
	t, err := p.taskByCodeMP(a, code)
	if err != nil {
		return "", err
	}
	if t == nil {
		return "mp 口径未下发该任务（前置任务未完成或活动未开始）", nil
	}
	if t.Claimed {
		return "已领取", nil
	}
	target := t.Target
	if target <= 0 {
		target = 1
	}
	if t.Current >= target || t.AcceptStatus == "completed" {
		credit, energy, err := p.cfg.Upstream.ClaimRewardMP(a, code)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已领取奖励（+%dc +%de）", credit, energy), nil
	}
	if t.AcceptStatus == "not_accepted" || t.AcceptStatus == "" {
		if !p.acceptWithVerifyMP(a, code) {
			return "accept 未登记生效（任务可能处于每日锁定窗口，等解锁后自动重试）", nil
		}
	}
	if err := primary(); err != nil {
		return fmt.Sprintf("判据上报失败: %v", err), nil
	}
	// 回读（两轮各隔 3s）；未点亮且有 fallback 时补报一轮再读。
	for round := 0; round < 2; round++ {
		time.Sleep(claimPollGap)
		t2, err2 := p.taskByCodeMP(a, code)
		if err2 != nil || t2 == nil {
			continue
		}
		t = t2
		if t.Claimable || t.Claimed || t.Current >= target {
			break
		}
		if round == 0 && fallback != nil {
			if err := fallback(); err != nil {
				return fmt.Sprintf("备选判据上报失败: %v", err), nil
			}
		}
	}
	if t.Claimed {
		return "本轮已入账（claimed）", nil
	}
	if t.Current < target {
		return "已上报但进度未点亮（判据形态待解锁后校正，下次重试）", nil
	}
	credit, energy, err := p.cfg.Upstream.ClaimRewardMP(a, code)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("任务点亮并领取奖励（+%dc +%de）", credit, energy), nil
}

// runSequentialAutomation 完成 Sequential_Tasks_4「创建定时任务」（预留）。
// mp 源码无 automation 事件发射点 → 判据疑为 PC 口径：复用 automation_1 同源
// 事件（DesktopAutomationCreateEvent，PC 任务三账号实测点亮）。
func runSequentialAutomation(p *Panel, a *auth.Auth) (string, error) {
	return p.runSequentialEventTask(a, "Sequential_Tasks_4",
		func() error {
			return p.cfg.Upstream.ReportDesktopEvent(a, upstream.DesktopAutomationCreateEvent(automationTaskName))
		}, nil)
}

// runSequentialModelChat 完成 Sequential_Tasks_5「使用 GLM5.2」（预留）。
// primary：mp 对话事件带 requestModelId/requestModelName=glm-5.2（mpsrc main 32904
// 发射点实测形状）；fallback：PC 域模型活跃上报（Model_chat_GLM5.2 同源）。
func runSequentialModelChat(p *Panel, a *auth.Auth) (string, error) {
	return p.runSequentialEventTask(a, "Sequential_Tasks_5",
		func() error {
			return p.cfg.Upstream.ReportMPEvent(a, upstream.MiniChatModelEvent(upstream.NewConversationID(), "glm-5.2", "GLM-5.2"))
		},
		func() error {
			return p.cfg.Upstream.ReportChatActivityModel(a, upstream.NewConversationID(), "", "glm-5.2", "GLM-5.2")
		})
}

// runSequentialPlaybook 完成 Sequential_Tasks_7「体验灵感功能」（预留，疑 PC 口径）。
// primary：PC 灵感事件组（DesktopPlaybookPromptSequence，playbook_prompt 三账号
// 实测点亮）；fallback：mp 指纹灵感事件组（MiniPlaybookEvents，mpsrc 形状）。
func runSequentialPlaybook(p *Panel, a *auth.Auth) (string, error) {
	return p.runSequentialEventTask(a, "Sequential_Tasks_7",
		func() error {
			pc, err := p.cfg.Upstream.PickPlaybookCase(a)
			if err != nil {
				return fmt.Errorf("灵感案例: %w", err)
			}
			return p.cfg.Upstream.ReportDesktopEvent(a,
				upstream.DesktopPlaybookPromptSequence(upstream.NewConversationID(), upstream.NewRequestID(), pc)...)
		},
		func() error {
			return p.cfg.Upstream.ReportMPEvent(a, upstream.MiniPlaybookEvents("pm-gtm-launch-plan", "新产品上市 GTM 发布计划一页纸")...)
		})
}

// runMiniExpert 完成 Sequential_Tasks_2「在小程序内选中专家并完成有效对话」。
// 判据 = mp 指纹 expert_actual_use（**不带** activityId/conversationId、
// extVersion=2.2.8、type=send_message——小程序源码实测形状，与 school 域 expert
// 事件两套口径勿混；上游 task_runner 实测上报即 completed，claim +200c+5e）。
// 专家 id 必须是市场真实 ex_ id（空 id 服务端不入账）→ **accept 之前**先解析市场
// 列表：拉不到就整任务不动作，避免留下「已登记未上报」的半程态（上游 9a26ae7
// 的 ids 前置判定同款）。复用既有 MarketExpertList（expert_5 任务同源，实测可用）。
func runMiniExpert(p *Panel, a *auth.Auth) (string, error) {
	const code = "Sequential_Tasks_2"
	t, err := p.taskByCodeMP(a, code)
	if err != nil {
		return "", err
	}
	if t == nil {
		return "mp 口径未下发该任务（活动可能已结束）", nil
	}
	if t.Claimed {
		return "已领取", nil
	}
	target := t.Target
	if target <= 0 {
		target = 1 // 未 accept 的 mp 任务 progress 为 null，target 兜底（上游实测）
	}
	// 已达标（含 completed 未领）：直接领奖。
	if t.Current >= target || t.AcceptStatus == "completed" {
		credit, energy, err := p.cfg.Upstream.ClaimRewardMP(a, code)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已领取奖励（+%dc +%de）", credit, energy), nil
	}
	// 判据载体前置（accept 之前）：市场真实专家 id。
	experts, merr := p.cfg.Upstream.MarketExpertList(a, "")
	if merr != nil || len(experts) == 0 {
		return fmt.Sprintf("专家市场不可用（%v），跳过以防半程态", merr), nil
	}
	e := experts[0]
	name := e.DisplayNameZH
	if name == "" {
		name = e.ProfessionZH
	}
	if t.AcceptStatus == "not_accepted" || t.AcceptStatus == "" {
		if !p.acceptWithVerifyMP(a, code) {
			return "accept 未登记生效（上游 200+OK 但未落账形态），待下次重试", nil
		}
	}
	ev := upstream.MiniExpertUseEvent(e.ExpertID, name, e.ExpertType)
	if err := p.cfg.Upstream.ReportMPEvent(a, ev); err != nil {
		return fmt.Sprintf("上报 expert_actual_use 失败: %v", err), nil
	}
	// 回读（异步计分，两轮各隔 3s——与 runMPMiniChatTask 同预算）。
	for i := 0; i < 2; i++ {
		time.Sleep(claimPollGap)
		t2, err2 := p.taskByCodeMP(a, code)
		if err2 != nil || t2 == nil {
			continue
		}
		t = t2
		if t.Claimable || t.Claimed || t.Current >= target {
			break
		}
	}
	if t.Claimed {
		return "本轮已入账（claimed）", nil
	}
	if t.Current < target {
		return "已上报但进度未归账（异步计分，下次重试）", nil
	}
	credit, energy, err := p.cfg.Upstream.ClaimRewardMP(a, code)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("任务点亮并领取奖励（+%dc +%de）", credit, energy), nil
}

// accountTaskAuto 一键完成单个任务：执行对应动作 → 回读进度 → 汇报结果。
func (p *Panel) accountTaskAuto(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.accountByUID(w, uid)
	if a == nil {
		return
	}
	var body struct {
		TaskCode string `json:"task_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TaskCode == "" {
		writeErr(w, http.StatusBadRequest, "task_code required")
		return
	}
	act := autoActionFor(body.TaskCode)
	if act == nil {
		writeErr(w, http.StatusNotImplemented,
			"该任务需要客户端内交互（无对应接口），无法自动完成；请按任务说明在官方客户端操作")
		return
	}
	// per-account 互斥：同账号的任务动作正在跑（单任务或全量）时直接 409，
	// 不并发重跑（动作幂等但 expert/skill 系含真实对话，重跑浪费配额）。
	if !p.tryLockAccount(uid) {
		writeErr(w, http.StatusConflict, "该账号有任务动作正在执行中，请等本轮结束后再试")
		return
	}
	defer p.unlockAccount(uid)
	// 前置读取：已完成的任务直接跳过（幂等，不浪费上游调用）。
	// taskByCode 已双口径（mp 专属码自动回落 mp 列表）。
	before, err := p.taskByCode(a, act.TaskCode)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "list tasks: "+err.Error())
		return
	}
	if before == nil {
		writeErr(w, http.StatusNotFound, "该账号没有此任务")
		return
	}
	isMP := isMPTaskCode(act.TaskCode)
	if before.Claimed {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "skipped": true, "message": "该任务已领取过奖励"})
		return
	}
	// 先接受：上游不给 not_accepted 的任务记进度。任务中心队列和「一键完成可自动任务」都先批量
	// 接受，单个任务这里此前漏了。小程序任务的动作自己按小程序口径接受。失败不阻塞，动作照跑。
	if !isMP && !before.Locked && before.AcceptStatus != "accepted" && before.AcceptStatus != "completed" {
		if err := p.cfg.Upstream.AcceptTasks(a, []string{act.TaskCode}); err != nil {
			log.Printf("panel: accept %s uid=%s: %v（继续执行动作）", act.TaskCode, uid, err)
		} else {
			time.Sleep(reportGap)
		}
	}
	msg, err := act.run(p, a)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "执行失败: "+err.Error())
		return
	}
	// 回读验证：上报 200 ≠ 计分（上游可能静默丢弃 + 计分异步），
	// 用有界轮询等异步计时落定，再决定是否自动领奖（mp 任务同口径回读）。
	var after *upstream.Task
	var aerr error
	if isMP {
		after, aerr = p.taskByCodeMP(a, act.TaskCode)
	} else {
		after, aerr = p.taskByCodeWaiting(a, act.TaskCode)
	}
	progressBefore, progressAfter := taskProgressText(before), ""
	claimable := false
	if aerr == nil && after != nil {
		progressAfter = taskProgressText(after)
		claimable = after.Claimable
	}
	resp := map[string]any{
		"ok":               true,
		"message":          msg,
		"progress_before":  progressBefore,
		"progress_after":   progressAfter,
		"claimable":        claimable,
		"attempt":          act.Attempt,
		"verify_supported": true,
	}
	// 达标即自动领奖（Web 端 claim）：把"完成→领奖"收敛成一步，无需用户再点一次。
	if claimable {
		var credit, energy int64
		var cerr error
		if isMP {
			credit, energy, cerr = p.cfg.Upstream.ClaimRewardMP(a, act.TaskCode)
		} else {
			credit, energy, cerr = p.cfg.Upstream.ClaimReward(a, act.TaskCode)
		}
		if cerr == nil {
			resp["claimed"] = true
			resp["credit"] = credit
			resp["energy"] = energy
			if credit > 0 || energy > 0 {
				resp["message"] = msg + fmt.Sprintf("；已自动领奖 +%d 分 +%d 能", credit, energy)
			} else {
				resp["message"] = msg + "；奖励此前已领取"
			}
		} else {
			resp["claim_error"] = cerr.Error()
			resp["message"] = msg + "；达标但领奖失败，可在任务列表手动点「领取」重试"
		}
	}
	log.Printf("panel: 任务动作 uid=%s code=%s progress %s -> %s claimable=%v claimed=%v",
		uid, act.TaskCode, progressBefore, progressAfter, claimable, resp["claimed"])
	writeJSON(w, http.StatusOK, resp)
}

// taskProgressText 任务进度的可读表示（回读对比用）。
func taskProgressText(t *upstream.Task) string {
	if t == nil {
		return "?"
	}
	if t.Target > 0 {
		return fmt.Sprintf("%d/%d", t.Current, t.Target)
	}
	if t.Claimed {
		return "claimed"
	}
	return t.AcceptStatus
}

// truncateStr 截断错误文本（避免把上游长响应原样透给前端）。
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------------------------------------------------------------------------
// 各任务动作实现
// ---------------------------------------------------------------------------

// reportGap 连续上报之间的间隔（对齐上游脚本实测的 1.05s 口径，避免风控）。
var reportGap = 1050 * time.Millisecond

// runChat5 补足 chat_5 的进度：按差额上报 chat_request_send。
func runChat5(p *Panel, a *auth.Auth) (string, error) {
	t, err := p.taskByCode(a, "chat_5")
	if err != nil {
		return "", err
	}
	if t == nil {
		return "", fmt.Errorf("任务不存在")
	}
	target := t.Target
	if target <= 0 {
		target = 5
	}
	need := target - t.Current
	if need <= 0 {
		return "进度已达标，无需上报", nil
	}
	for i := int64(0); i < need; i++ {
		if err := p.cfg.Upstream.ReportChatActivity(a, upstream.NewConversationID(), ""); err != nil {
			return fmt.Sprintf("上报第 %d/%d 条失败: %v", i+1, need, err), nil
		}
		if i < need-1 {
			time.Sleep(reportGap)
		}
	}
	return fmt.Sprintf("已补报 %d 条对话事件", need), nil
}

// runFirstBuddy 领养：report（解锁前置）→ agreement → first。
func runFirstBuddy(p *Panel, a *auth.Auth) (string, error) {
	if err := p.cfg.Upstream.ReportChatActivity(a, upstream.NewConversationID(), ""); err != nil {
		return "", fmt.Errorf("前置上报: %w", err)
	}
	time.Sleep(reportGap) // 给上游事件处理留时间（脚本实测口径）
	if err := p.cfg.Upstream.BuddyAgreement(a); err != nil {
		return "", fmt.Errorf("同意协议: %w", err)
	}
	if err := p.cfg.Upstream.BuddyFirst(a); err != nil {
		if upstream.IsBuddyTaskIncomplete(err) {
			return "前置已上报，但领养门槛未过（上游要求当日活跃），请稍后重试", nil
		}
		return "", fmt.Errorf("领取 Buddy: %w", err)
	}
	return "已领取 Buddy（+300 分 +8 能量）", nil
}

// runModelChat 完成 Model_chat_GLM5.2：accept → 真实对话 → 对齐模型上报。
func runModelChat(p *Panel, a *auth.Auth) (string, error) {
	const code, modelID, modelName = "Model_chat_GLM5.2", "glm-5.2", "GLM-5.2"
	// 1. accept（报名；失败不阻塞——行为事件才是判据）
	if err := p.cfg.Upstream.AcceptTasks(a, []string{code}); err != nil {
		log.Printf("panel: accept %s: %v（继续走行为链路）", code, err)
	}
	time.Sleep(reportGap)
	// 2. 真实对话一次（判据的最直接证据）
	body, _ := json.Marshal(map[string]any{
		"model": modelID,
		"messages": []map[string]any{
			// 和夜间对话同一句：对话活跃上报的 inputLength 固定按它的长度 12。
			{"role": "user", "content": "1+1等于几？直接回答。"},
		},
		"stream": true,
	})
	// 上报和这次对话用同一个会话 id、轮次 id。
	conv, req := upstream.NewConversationID(), upstream.NewRequestID()
	rc, status, respBody, err := p.cfg.Upstream.ChatStream(a, body, "", upstream.ChatMeta{ConversationID: conv, ConversationRequestID: req})
	if err != nil {
		return "", fmt.Errorf("对话请求: %w", err)
	}
	if status >= 400 {
		rc.Close()
		return "", fmt.Errorf("对话失败 http=%d: %s", status, truncateStr(string(respBody), 160))
	}
	// 读干 SSE（网关对上游强制 flow：不读完会残留连接）
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, 1<<20))
	rc.Close()
	time.Sleep(reportGap)
	// 3. 对齐模型的上报（触发进度）
	if err := p.cfg.Upstream.ReportChatActivityModel(a, conv, req, modelID, modelName); err != nil {
		return "对话已完成，但进度上报失败：" + err.Error(), nil
	}
	return "已完成 glm-5.2 对话并上报", nil
}

// runRichMeow 完成 RichMeow_Chat（桌面端对话1次）。
// 2026-09-12 三账号实测验证：以桌面指纹（extName=workbuddy-desktop）向
// copilot.tencent.com/v2/report 上报完整对话事件链（agent_task_created →
// chat_message_response isSuccessful=true 等 6 事件），纯 API 即可点亮并领奖
// （紫川/人杰2 两账号无桌面客户端登录状态下 3 秒内 0/1 → 1/1）。
// Hp_Appearance 的纯 API set 不计分（需客户端在主题下活跃），区别对待。
func runRichMeow(p *Panel, a *auth.Auth) (string, error) {
	events := upstream.DesktopChatSequence(upstream.NewConversationID(), upstream.NewRequestID(), upstream.NewRequestID(), "fast-model", "fast-model")
	if err := p.cfg.Upstream.ReportDesktopEvent(a, events...); err != nil {
		return "", err
	}
	return "已按桌面端指纹上报完整对话事件链（agent_task_created→chat_response）", nil
}

// runBuddyApp 完成 Buddy_App / Buddy_App_QQ（进入 Buddy 应用）。
// 2026-09-12 两账号实测：buddyapp 五连事件（discover→show→enter→auth_confirm→
// bind_skip）以 workbuddy-desktop 指纹上报即点亮，服务端不校验真实授权。
// 用企鹅教师助手（Buddy_App_QQ 判据应用）作载体，同一组事件同时满足
// Buddy_App「进入任一应用」——两个表项共用本 run，幂等由任务状态跳过兜底。
func runBuddyApp(p *Panel, a *auth.Auth) (string, error) {
	events := upstream.DesktopBuddyAppSequence("cb_y5Dy46tPQGGWtueMxXbe", "企鹅教师助手")
	if err := p.cfg.Upstream.ReportDesktopEvent(a, events...); err != nil {
		return "", err
	}
	return "已上报 buddyapp 进入五连事件（同时覆盖 Buddy_App 与 Buddy_App_QQ）", nil
}

// automationTaskName 定时任务创建事件的任务名。官方是用户在表单里填的名字，网关取一个普通用户会起的
// 名字（典型值），不带网关自己的名字。
const automationTaskName = "每日资讯简报"

// runAutomationCreate 完成 automation_1（设置自动化任务）。
// 2026-09-12 两账号实测：automated_task_create_suc 事件纯 API 上报即点亮，
// 无需真实创建定时任务。
func runAutomationCreate(p *Panel, a *auth.Auth) (string, error) {
	if err := p.cfg.Upstream.ReportDesktopEvent(a,
		upstream.DesktopAutomationCreateEvent(automationTaskName)); err != nil {
		return "", err
	}
	return "已上报定时任务创建事件", nil
}

// runLibraryRead 完成 Library_read（体验资料库）。
// 2026-09-12 三账号实测：web 域 /v2/report 上报 web_element_click
// (elementId=library_doc_intro_click) 即点亮（space 的 open/WS/inlong 都不是判据）。
func runLibraryRead(p *Panel, a *auth.Auth) (string, error) {
	const docURL = "https://www.workbuddy.cn/space/d/o0KWYeynteVv06UnAZqIFm"
	if err := p.cfg.Upstream.ReportWebEvent(a, "web_element_click", docURL,
		"library_doc_intro_click", "WorkBuddy资料库介绍"); err != nil {
		return "", err
	}
	return "已上报资料库介绍阅读事件", nil
}

// runBlackCat 完成 black_cat（夜猫子，夜间 23:00–08:00 计数）。
// 判据 = 夜间窗口内 glm-5.2 真实对话 + chat 事件上报（WorkBuddy-Daily 实测口径）。
// 窗口外不做（提示等排程）；网关 blackcat_hours（默认 23 点）排程会自动补足。
// nightNow runBlackCat 判断夜间窗口用的时钟（测试里固定）。
var nightNow = time.Now

func runBlackCat(p *Panel, a *auth.Auth) (string, error) {
	if !upstream.InNightWindow(nightNow()) {
		return "当前不在 23:00–08:00 计数窗口，行为不计分；网关会在每日 23 点自动补足", nil
	}
	need, err := p.cfg.Upstream.BlackcatNeed(a)
	if err != nil {
		return "", err
	}
	if need <= 0 {
		return "进度已达标，无需补足", nil
	}
	ok, err := p.cfg.Upstream.RunNightChats(a, int(need))
	if err != nil {
		return fmt.Sprintf("完成 %d/%d 次后中断: %v", ok, need, err), nil
	}
	return fmt.Sprintf("已完成 %d 次夜间对话并上报", ok), nil
}

// runSkillFresh 完成 skill_1（尝鲜热门技能）。
// 2026-09-12 判据（紫川手动完成抓包 row 209）：`skill_info` 事件（桌面指纹）——
// {id:<技能名>, skillId, skillVersion, toolStatus:"success", fileCount,
// source:"workbuddy-desktop"} JOIN 真实会话（conversationId/requestId=服务端
// id）。此前的 skill_request_send/skill_installed/skill_action 全是错误方向。
// 人杰2 实测 0/1 → 1/1 点亮。
func runSkillFresh(p *Panel, a *auth.Auth) (string, error) {
	conv, req, err := p.cfg.Upstream.DesktopChatWithExpert(a, "", false, false)
	if err != nil {
		return "", fmt.Errorf("真实对话: %w", err)
	}
	msgID := upstream.NewRequestID()
	events := upstream.DesktopChatSequence(conv, req, msgID, "fast-model", "fast-model")
	for _, ev := range events {
		if ev["eventCode"] == "chat_message_response" {
			ev["finishReason"] = "tool_calls" // 模型发起工具调用（技能加载）语义
		}
	}
	events = append(events, upstream.DesktopEvent{
		"eventCode":    "skill_info",
		"id":           "润泽小馆·日报撰写",
		"skillId":      "skill_2097350077599879168",
		"skillVersion": "1.0.0",
		"toolStatus":   "success",
		// 官方是产品配置里的技能条数（内置的加装上的）：5.8.3 安装包内置 48 个，加上这个技能共 49。
		// 抓包样本是 56，推断是那台机器多装了别的技能。
		"fileCount":      49,
		"source":         "workbuddy-desktop",
		"conversationId": conv, "requestId": req, "messageId": msgID,
		"requestModelId": "fast-model", "requestModelName": "fast-model",
		"traceId": req,
	})
	if err := p.cfg.Upstream.ReportDesktopEvent(a, events...); err != nil {
		return "", fmt.Errorf("skill_info 事件: %w", err)
	}
	return "已上报真实对话 + skill_info 技能加载事件", nil
}

// runExpertLighthouse 完成 Expert_lighthouse（体验「腾讯轻量云」专家）。
// 2026-09-12 判据（真实样本 Sunny row 868）：与 expert_5 同构，但两处差异——
// chat 链的 agent_task_created 需带 has_expert:true + expert_id（我们默认 false），
// expert_actual_use 的 mode 为 "LOCAL"（非 craft）。id 固定为轻量云专家
// ex_2cvvUZQhDyeJ；requestId 必须是真实 chat 的服务端 id。紫川/人杰2 实测点亮。
func runExpertLighthouse(p *Panel, a *auth.Auth) (string, error) {
	const lhID = "ex_2cvvUZQhDyeJ"
	lh := upstream.MarketExpert{
		ExpertID: lhID, ExpertType: "agent",
		DisplayNameZH: "腾讯轻量云专家", ProfessionZH: "腾讯轻量云专家", Version: "1.0.2",
	}
	// 市场列表若命中真实条目则用其信息（version 等以服务端为准）。
	if experts, err := p.cfg.Upstream.MarketExpertList(a, "agent"); err == nil {
		for _, e := range experts {
			if e.ExpertID == lhID {
				lh = e
				break
			}
		}
	}
	if err := p.cfg.Upstream.ReportDesktopEvent(a, upstream.DesktopExpertSummonSequence(lh)...); err != nil {
		return "", fmt.Errorf("召唤链: %w", err)
	}
	summonedAt := time.Now()
	conv, req, err := p.cfg.Upstream.DesktopChatWithExpert(a, lhID, lh.ExpertType == "team", lh.Protected())
	if err != nil {
		return "", fmt.Errorf("真实对话: %w", err)
	}
	msgID := upstream.NewRequestID()
	events := upstream.DesktopExpertChatSequence(lh, conv, req, msgID)
	for _, ev := range events {
		if ev["eventCode"] == "agent_task_created" {
			ev["expert_industry_id"] = "" // 同下面 actual_use 的 type
		}
	}
	events = append(events, upstream.DesktopExpertActualUseLocal(lh, conv, req, msgID, summonedAt))
	// 对齐真实样本细节：轻量云专家 actual_use 的 type 为空、cost=0。
	events[len(events)-1]["type"] = ""
	events[len(events)-1]["cost"] = 0
	if err := p.cfg.Upstream.ReportDesktopEvent(a, events...); err != nil {
		return "", fmt.Errorf("使用事件: %w", err)
	}
	return "已上报轻量云专家召唤+使用链（真实对话 requestId）", nil
}

// runAppearance 完成 Hp_Appearance（换主题）。
// 2026-09-12 紫川/人杰2 实测（desktopverify12）：判据是 appearance_skin_apply
// 事件 {action:"apply",source:"settings_close",id:<resourceKey>,...}（客户端在
// 主题生效状态下离开设置页时上报）——早期"纯 API set 不计分"的结论不准确，
// 真相是当时只调了 appearance/set 没发事件。组合：set API 留痕 + 事件上报。
//
// 1.1.2 起照官方 5.8.3 的外观设置页走：打开时拉主题列表，选中后 set，离开时上报。事件字段
// 照 buildSkinApplyPayload：vipLevel、series 取主题列表里这个主题的值，type 是账号的版本档
// （editionType）。官方版本档查不到时不上报（canUse 为假），网关同样报错不上报。
// appearanceGap 设置主题与上报皮肤生效事件之间的间隔。
var appearanceGap = 2 * time.Second

func runAppearance(p *Panel, a *auth.Auth) (string, error) {
	const themeKey = "theme-tkmw7j" // 和平精英激战金秋（Hp_Appearance 判据主题）
	themes, err := p.cfg.Upstream.AppearanceThemes(a)
	if err != nil {
		return "", fmt.Errorf("主题列表: %w", err)
	}
	var theme *upstream.AppearanceTheme
	for i := range themes {
		if themes[i].ID == themeKey {
			theme = &themes[i]
			break
		}
	}
	if theme == nil {
		// 10-20 生产查到它限时到 2026-11-02，下架后客户端里选不到。
		return "", fmt.Errorf("主题列表里没有 %s（可能已下架）", themeKey)
	}
	edition, err := p.cfg.Upstream.AccountEdition(a)
	if err != nil {
		return "", fmt.Errorf("账号版本: %w", err)
	}
	if err := p.cfg.Upstream.SetAppearanceTheme(a, themeKey); err != nil {
		return "", fmt.Errorf("设置主题: %w", err)
	}
	time.Sleep(appearanceGap)
	if err := p.cfg.Upstream.ReportDesktopEvent(a, upstream.DesktopAppearanceApplyEvent(*theme, edition)); err != nil {
		return "", err
	}
	return "已设置主题并上报皮肤生效事件", nil
}

// runTemplateUse 完成 template_5（使用 5 个模板创建任务）。
// 2026-09-12 三账号实测：agent_task_created_with_template + template_used 事件组
// （JOIN chat 链）一次上报 5 组即 5/5 点亮。template_id 服务端不校验真实性，1.1.2 起仍照官方首页
// 现拉模板列表，取工作模式下前 5 个按钮的真实 id 和名字。
// templateUseGap 相邻两组模板事件之间的间隔。
var templateUseGap = 300 * time.Millisecond

func runTemplateUse(p *Panel, a *auth.Auth) (string, error) {
	scenes, err := p.cfg.Upstream.PickWorkingScenes(a, 5)
	if err != nil {
		return "", fmt.Errorf("首页模板: %w", err)
	}
	for i, sc := range scenes {
		conv, req := upstream.NewConversationID(), upstream.NewRequestID()
		events := upstream.DesktopTemplateUseSequence(conv, req, sc)
		if err := p.cfg.Upstream.ReportDesktopEvent(a, events...); err != nil {
			return fmt.Sprintf("第 %d 组模板事件上报失败: %v", i+1, err), nil
		}
		time.Sleep(templateUseGap)
	}
	return "已上报 template_used ×5", nil
}

// runPlaybookPrompt 完成 playbook_prompt（灵感案例 Dialog 中发送 Prompt）。
// 判据是 playbook_prompt_send（Dialog 发送）而非卡片曝光/点击——asar 逆向确认。
func runPlaybookPrompt(p *Panel, a *auth.Auth) (string, error) {
	// 案例从官方静态站点现拉（同桌面端发现页），事件里的类型、分类、提示词长度是它的真实值。
	pc, err := p.cfg.Upstream.PickPlaybookCase(a)
	if err != nil {
		return "", fmt.Errorf("灵感案例: %w", err)
	}
	events := upstream.DesktopPlaybookPromptSequence(upstream.NewConversationID(), upstream.NewRequestID(), pc)
	if err := p.cfg.Upstream.ReportDesktopEvent(a, events...); err != nil {
		return "", err
	}
	return "已上报 playbook_cta_click + playbook_prompt_send（案例 " + pc.Title + "）", nil
}

// runCreateCanvas 完成 create_canvas（设计创意模式创建画布，+300 分）。
// 判据是 wbx_design_canvas_task_create/open（Ardot create_design 工具完成遥测）。
func runCreateCanvas(p *Panel, a *auth.Auth) (string, error) {
	events := upstream.DesktopDesignCanvasSequence(upstream.NewConversationID(), upstream.NewRequestID())
	if err := p.cfg.Upstream.ReportDesktopEvent(a, events...); err != nil {
		return "", err
	}
	return "已上报 wbx_design_canvas_task_create/open", nil
}

// expertSummonGap 专家召唤链的间隔（真实使用节奏，v11 实测 8s 成功率 100%）。
var expertSummonGap = 6 * time.Second

// runExpertUse 完成 expert_5（使用 5 个平台专家）。
// 2026-09-12 三账号实测公式：真实专家列表（id 必须真实存在）→ 召唤链
// （summon_click/summoned）→ 真实 chat 拿服务端 requestId → expert_actual_use。
// 自造专家 id 或自造 requestId 均不计数。
func runExpertUse(p *Panel, a *auth.Auth) (string, error) {
	return runExpertBatch(p, a, "agent", 5)
}

// runExpertTeamUse 完成 Expert_team_use_3（使用 3 个专家团，expertType=team）。
func runExpertTeamUse(p *Panel, a *auth.Auth) (string, error) {
	return runExpertBatch(p, a, "team", 3)
}

// runExpertBatch 专家召唤+使用的公共实现。失败逐个继续，返回汇总信息。
func runExpertBatch(p *Panel, a *auth.Auth, expertType string, count int) (string, error) {
	experts, err := p.cfg.Upstream.MarketExpertList(a, expertType)
	if err != nil {
		return "", fmt.Errorf("拉取专家列表: %w", err)
	}
	if len(experts) == 0 {
		return "", fmt.Errorf("专家市场列表为空")
	}
	ok, fail := 0, 0
	for i, e := range experts {
		if ok >= count {
			break
		}
		if err := p.summonAndUseExpert(a, e); err != nil {
			fail++
			continue
		}
		ok++
		if i < len(experts)-1 {
			time.Sleep(expertSummonGap)
		}
	}
	_ = fail
	return fmt.Sprintf("已对 %d 位真实专家完成召唤+使用链（类型 %s）", ok, expertType), nil
}

// summonAndUseExpert 召唤一位专家并真实使用一次：召唤链（web_element_click + summon_click +
// summoned）→ 带 X-Expert-Id 的真实 chat 拿服务端 requestId → 带专家的 chat 链 + 使用事件
// （JOIN 服务端 requestId）。expert_5、Expert_team_use_3 和「体验某位专家」类任务共用。
func (p *Panel) summonAndUseExpert(a *auth.Auth, e upstream.MarketExpert) error {
	if err := p.cfg.Upstream.ReportDesktopEvent(a, upstream.DesktopExpertSummonSequence(e)...); err != nil {
		return fmt.Errorf("召唤链: %w", err)
	}
	summonedAt := time.Now()
	conv, req, err := p.cfg.Upstream.DesktopChatWithExpert(a, e.ExpertID, e.ExpertType == "team", e.Protected())
	if err != nil {
		return fmt.Errorf("真实对话: %w", err)
	}
	msgID := upstream.NewRequestID()
	events := append(upstream.DesktopExpertChatSequence(e, conv, req, msgID),
		upstream.DesktopExpertActualUseEvent(e, conv, req, msgID, summonedAt))
	if err := p.cfg.Upstream.ReportDesktopEvent(a, events...); err != nil {
		return fmt.Errorf("使用事件: %w", err)
	}
	return nil
}

// expertTaskTarget 「体验某位专家」类任务指定的专家。
type expertTaskTarget struct {
	ID   string // 任务跳转链接 deeplink 里的 expertId；空表示跳转链接只到专家页，按名字找
	Name string // 任务标题「」里的专家名
}

// expertTaskSearchPages 跳转链接带 id 时，在专家列表里找这位专家最多看几页。
const expertTaskSearchPages = 5

// runExpertTask 完成「体验某位专家」类任务（2026-10 官方新上，任务说明都是「在专家页搜索并
// 召唤「某专家」，完成一次对话」）：找到这位专家后走 expert_5 那条召唤 + 使用链。
//
// 跳转链接带 id 的在列表里按 id 找，拿服务端的版本号和分类，找不到就用 id 和任务里的名字照样
// 做（2026-10-28 生产实测四个 expert_use_* 都点亮）；只到专家页的（WorkBuddy_Valorant）照任务
// 说明在专家页搜名字（见 searchTaskExpert），找不到就报错，不召唤别的专家。
func runExpertTask(target expertTaskTarget) func(p *Panel, a *auth.Auth) (string, error) {
	return func(p *Panel, a *auth.Auth) (string, error) {
		e, err := p.findTaskExpert(a, target)
		if err != nil {
			return "", err
		}
		if err := p.summonAndUseExpert(a, e); err != nil {
			return "", err
		}
		return fmt.Sprintf("已召唤「%s」并完成一次真实对话", target.Name), nil
	}
}

// findTaskExpert 找任务指定的专家。带 id 的在专家列表（单专家，按推荐排序）里逐页找，最多看
// expertTaskSearchPages 页，某页不满一页就是最后一页；只有名字的在专家页搜。
func (p *Panel) findTaskExpert(a *auth.Auth, target expertTaskTarget) (upstream.MarketExpert, error) {
	if target.ID == "" {
		return p.searchTaskExpert(a, target.Name)
	}
	for page := 1; page <= expertTaskSearchPages; page++ {
		experts, err := p.cfg.Upstream.MarketExpertListPage(a, "agent", page)
		if err != nil {
			break
		}
		for _, e := range experts {
			if e.ExpertID == target.ID {
				return e, nil
			}
		}
		if len(experts) < upstream.MarketExpertPageSize {
			break
		}
	}
	// id 来自任务本身，列表里没翻到也照样能召唤；版本号缺省 1.0.0、分类缺省 all（同列表缺字段时）。
	return upstream.MarketExpert{ExpertID: target.ID, ExpertType: "agent", DisplayNameZH: target.Name, ProfessionZH: target.Name}, nil
}

// searchTaskExpert 照任务说明「在专家页搜索」：在专家页的单专家页签搜这个名字，报 expert_search，
// 隔一会儿再召唤；没搜到再不分单专家和专家团搜一次（官方对话框里选专家时的搜法）。名字或职业名
// 和任务里的一致才算，两次都没有就报错，不召唤别的专家。
//
// 2026-10-28 生产上按推荐排序翻了 5 页没找到「无畏契约拼豆专家」，网关当时也只认响应里的
// experts、不认 items。改成搜索后，同日升级 1.7.0 在两个国内版号上实测搜得到、任务点亮；
// 上面两个原因哪个起作用、是哪一次搜索搜到的，没有单独确认。
func (p *Panel) searchTaskExpert(a *auth.Auth, name string) (upstream.MarketExpert, error) {
	experts, err := p.cfg.Upstream.MarketExpertSearch(a, "agent", name)
	if err != nil {
		return upstream.MarketExpert{}, fmt.Errorf("搜索专家: %w", err)
	}
	if err := p.cfg.Upstream.ReportDesktopEvent(a, upstream.DesktopExpertSearchEvent(name, len(experts))); err != nil {
		return upstream.MarketExpert{}, fmt.Errorf("搜索事件: %w", err)
	}
	e, ok := expertNamed(experts, name)
	if !ok {
		all, err := p.cfg.Upstream.MarketExpertSearch(a, "", name)
		if err != nil {
			return upstream.MarketExpert{}, fmt.Errorf("搜索专家: %w", err)
		}
		if e, ok = expertNamed(all, name); !ok {
			return upstream.MarketExpert{}, fmt.Errorf("在专家页搜「%s」没有找到（活动可能已结束），未召唤其他专家", name)
		}
	}
	time.Sleep(expertSummonGap)
	return e, nil
}

// expertNamed 按名字认专家：显示名或职业名去掉两边空白后和 name 相同。
func expertNamed(experts []upstream.MarketExpert, name string) (upstream.MarketExpert, bool) {
	for _, e := range experts {
		if strings.TrimSpace(e.DisplayNameZH) == name || strings.TrimSpace(e.ProfessionZH) == name {
			return e, true
		}
	}
	return upstream.MarketExpert{}, false
}

// ---------------------------------------------------------------------------
// 全量自动完成
// ---------------------------------------------------------------------------

// runAutoAll 对单账号依次执行所有可自动化任务，返回逐项结果。
// 供「一键完成全部可自动任务」使用；单项失败不影响后续项。
//
// 流程：先把所有未接受的任务批量 accept（规范状态机；上游脚本建议"先 accept"），
// 再逐项执行行为链路。accept 不是进度产生的必要条件，但让后续状态流转规范。
func (p *Panel) runAutoAll(a *auth.Auth) []map[string]any {
	var out []map[string]any

	// 阶段 0：批量接受尚未接受的任务（失败不阻塞——行为事件才是进度唯一判据）。
	if tasks, err := p.cfg.Upstream.ListTasks(a); err == nil {
		var codes []string
		for _, t := range tasks {
			if !t.Claimed && !t.Locked && t.AcceptStatus != "accepted" && t.AcceptStatus != "completed" {
				codes = append(codes, t.TaskCode)
			}
		}
		if len(codes) > 0 {
			if err := p.cfg.Upstream.AcceptTasks(a, codes); err != nil {
				out = append(out, map[string]any{
					"task_code": "(批量接受)", "status": "error",
					"message": "接受任务失败（不阻塞后续）: " + err.Error(),
				})
			} else {
				out = append(out, map[string]any{
					"task_code": "(批量接受)", "status": "done",
					"message": fmt.Sprintf("已接受 %d 个任务", len(codes)),
				})
				time.Sleep(reportGap)
			}
		}
	}

	// 阶段 0b：小程序口径任务单独接受（默认列表不含 mp 码；失败不阻塞）。
	if mpTasks, err := p.cfg.Upstream.ListTasksMP(a); err == nil {
		var mpCodes []string
		for _, t := range mpTasks {
			if !t.Claimed && !t.Locked && t.AcceptStatus != "accepted" && t.AcceptStatus != "completed" {
				mpCodes = append(mpCodes, t.TaskCode)
			}
		}
		if len(mpCodes) > 0 {
			if err := p.cfg.Upstream.AcceptTasksMP(a, mpCodes); err != nil {
				out = append(out, map[string]any{
					"task_code": "(批量接受-mp)", "status": "error",
					"message": "接受小程序任务失败（不阻塞后续）: " + err.Error(),
				})
			} else {
				out = append(out, map[string]any{
					"task_code": "(批量接受-mp)", "status": "done",
					"message": fmt.Sprintf("已接受 %d 个小程序任务", len(mpCodes)),
				})
				time.Sleep(reportGap)
			}
		}
	}

	for _, act := range autoActions {
		item := map[string]any{"task_code": act.TaskCode, "desc": act.Desc}
		before, err := p.taskByCode(a, act.TaskCode)
		if err != nil {
			item["status"] = "error"
			item["message"] = "查询失败: " + err.Error()
			out = append(out, item)
			continue
		}
		if before == nil {
			item["status"] = "skipped"
			item["message"] = "该账号无此任务"
			out = append(out, item)
			continue
		}
		if before.Claimed || before.Current >= before.Target && before.Target > 0 {
			item["status"] = "skipped"
			msg := "已完成（" + taskProgressText(before) + "）"
			// 已达标还没领：先补领再跳过（上游 #174）。靠真实对话点亮的任务不经过自动动作，
			// 这里只跳过的话奖就一直没人领。判据用进度，不看 accept_status=="completed"
			// （回读时会短暂变成 completed 再回滚，按它领会领错）。
			if !before.Claimed {
				credit, energy, cerr := claimTaskReward(p, a, act.TaskCode)
				switch {
				case cerr != nil:
					item["claim_error"] = cerr.Error()
					msg += "；补领失败（可在列表手动重试）"
				case credit > 0 || energy > 0:
					item["claimed"] = true
					item["credit"] = credit
					item["energy"] = energy
					msg += fmt.Sprintf("；已补领 +%d 分 +%d 能", credit, energy)
				default:
					msg += "；奖励此前已领取"
				}
			}
			item["message"] = msg
			out = append(out, item)
			continue
		}
		msg, err := act.run(p, a)
		if err != nil {
			item["status"] = "error"
			item["message"] = err.Error()
			out = append(out, item)
			continue
		}
		var after *upstream.Task
		if isMPTaskCode(act.TaskCode) {
			after, _ = p.taskByCodeMP(a, act.TaskCode)
		} else {
			after, _ = p.taskByCodeWaiting(a, act.TaskCode)
		}
		item["status"] = "done"
		item["message"] = msg
		item["progress_after"] = taskProgressText(after)
		// 进度达标即自动领奖（mp 任务走 chat 域 mp 口径，其余 Web 端接口）。
		// 领奖失败不掩盖主流程结果：status 仍为 done，附加 claim_error 供前端提示。
		if after != nil && after.Claimable {
			item["claimable"] = true
			credit, energy, cerr := claimTaskReward(p, a, act.TaskCode)
			if cerr == nil {
				item["claimed"] = true
				item["credit"] = credit
				item["energy"] = energy
				if credit > 0 || energy > 0 {
					item["message"] = msg + fmt.Sprintf("；已自动领奖 +%d 分 +%d 能", credit, energy)
				} else {
					item["message"] = msg + "；奖励此前已领取"
				}
			} else {
				item["claim_error"] = cerr.Error()
				item["message"] = msg + "；达标但领奖失败（可在列表手动重试）"
			}
		}
		out = append(out, item)
		time.Sleep(reportGap) // 项间节流
	}
	return out
}

// autoAllTimeout 一键完成的 HTTP 等待上限；超时后流水线仍在后台跑完。
var autoAllTimeout = 5 * time.Minute

// accountTaskAutoAll 一键完成该账号全部可自动任务。
func (p *Panel) accountTaskAutoAll(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.accountByUID(w, uid)
	if a == nil {
		return
	}
	// per-account 互斥（与单任务动作共用一把锁）：重复点击 409。
	if !p.tryLockAccount(uid) {
		writeErr(w, http.StatusConflict, "该账号有任务动作正在执行中，请等本轮结束后再试")
		return
	}
	// 用 context 兜底超时（多项任务串联 + 每项含真实对话，可能耗时较长）。
	ctx, cancel := context.WithTimeout(r.Context(), autoAllTimeout)
	defer cancel()
	done := make(chan []map[string]any, 1)
	go func() {
		defer p.unlockAccount(uid) // 流水线真正结束（而非 HTTP 超时返回）才放锁
		done <- p.runAutoAll(a)
	}()
	select {
	case results := <-done:
		log.Printf("panel: 一键完成可自动任务 uid=%s 共 %d 项", uid, len(results))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": results})
	case <-ctx.Done():
		// HTTP 侧超时返回，但后台流水线仍在跑——锁在流水线 goroutine 内释放，
		// 期间重复点击会被 409 挡住，不会出现两轮并发。
		writeErr(w, http.StatusGatewayTimeout, "执行超时（任务仍在后台继续）")
	}
}
