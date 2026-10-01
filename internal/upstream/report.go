// report.go growth 域「对话活跃上报」接口：POST {billingBase}/v2/report。
// 照抄客户端 chat_request_send 事件形状（含 conversationId/mode/inputLength 等全字段，
// 勿用最小 3 字段，防上游后续加严）。事件必须带 userId（=账号 uid），缺失则服务端
// 200 但静默丢弃（实测见 REPORT-active-map.md §2）。
//
// 一条上报同时点亮 growth 连登 + 解锁 first_buddy 任务（领养前置）。
// 风控口径：每号每天 1 次即可（activity_hours 单时点），不做多时点高频上报。
package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// reportPath 活跃上报通道（实测）。
const reportPath = "/v2/report"

// billingJSON 发 billing 域（billingBase，codebuddy.cn）请求并解信封；body 为 nil 时不带请求体。
// 与 travel.go 的 growthJSON 对称（growth 域走 chatBase + BillingHeaders；billing 域走 billingBase）。
// report/checkin 等 billing 端点共用：请求头统一 BillingHeaders，信封与错误语义同 doJSON。
func (c *Client) billingJSON(a *auth.Auth, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.billingBase(a)+path, rdr)
	if err != nil {
		return nil, err
	}
	c.BillingHeaders(req, a)
	return c.doJSON(req)
}

// billingMeterJSON 仅对 /billing/meter 族端点（get-user-resource / daily-checkin）
// 按 realm 走双路径 fallback：global 先无 /v2 前缀，ErrNotFound 时二次换有 /v2 前缀
// （上游新旧路径分叉）；cn 单路径（有 /v2）现状不变。仅 global realm 才有多路径。
func (c *Client) billingMeterJSON(a *auth.Auth, paths []string, method string, body any) (json.RawMessage, error) {
	var lastErr error
	for i, path := range paths {
		data, err := c.billingJSON(a, method, path, body)
		if err == nil {
			return data, nil
		}
		lastErr = err
		// 仅 404 换路径（路径不存在才值得 fallback）；其他错误直接返回。
		var ue *Error
		if !errors.As(err, &ue) || ue.Kind != ErrNotFound || i == len(paths)-1 {
			return nil, err
		}
	}
	return nil, lastErr
}

// billingRetryDelay 签到/余额等维护类计费调用瞬时错误重试的间隔基数。
// 独立变量供测试缩短（生产固定 2s：第 1 次重试等 2s、第 2 次等 4s）。
var billingRetryDelay = 2 * time.Second

// isTransientBillingErr 报告 err 是否值得对计费维护类调用做有界重试：
// 上游 5xx（ErrServer，实测偶发 "code 10000 / API request failed with status
// code: 500"）或网络层错误（非 *Error 的传输失败）。业务错误（code!=0 的
// 已签到/参数错、4xx、限流）不重试——重试只会原样再失败一次。
func isTransientBillingErr(err error) bool {
	if err == nil {
		return false
	}
	var ue *Error
	if errors.As(err, &ue) {
		return ue.Kind == ErrServer
	}
	return true
}

// retryBillingTransient 对签到/余额这类低频维护调用做瞬时错误有界重试：
// 最多补打 2 次（间隔 2s、4s），首次成功或非瞬时错误立即返回。chat 热路径
// 不用本策略——它有自己的换号轮转语义，重试会放大在途请求。
func (c *Client) retryBillingTransient(fn func() error) error {
	err := fn()
	if err == nil || !isTransientBillingErr(err) {
		return err
	}
	for i := 1; i <= 2; i++ {
		time.Sleep(time.Duration(i) * billingRetryDelay)
		if err = fn(); err == nil || !isTransientBillingErr(err) {
			return err
		}
	}
	return err
}

// chatRequestEvent 客户端 chat_request_send 事件完整形状（与 probe_active.py chat_event 对齐）。
// userId 为必填字段（= a.UID）；conversationId 由调用方生成，无需真实会话。
type chatRequestEvent struct {
	EventCode             string `json:"eventCode"`
	Timestamp             int64  `json:"timestamp"`
	ReportDelay           int    `json:"reportDelay"`
	Mode                  string `json:"mode"`
	ConversationID        string `json:"conversationId"`
	RequestID             string `json:"requestId"`
	InputLength           int    `json:"inputLength"`
	RequestModelID        string `json:"requestModelId"`
	RequestModelName      string `json:"requestModelName"`
	IsPlan                bool   `json:"isPlan"`
	IsAutoExecuteTerminal bool   `json:"isAutoExecuteTerminal"`
	IsAutoModify          bool   `json:"isAutoModify"`
	CodebaseEnable        bool   `json:"codebaseEnable"`
	MaxToken              int    `json:"maxToken"`
	MaxSteps              int    `json:"maxSteps"`
	Temperature           int    `json:"temperature"`
	MaxRetries            int    `json:"maxRetries"`
	MentionContexts       []any  `json:"mentionContexts"`
	KnowledgeID           []any  `json:"knowledgeId"`
	KnowledgeName         []any  `json:"knowledgeName"`
	CodebaseID            string `json:"codebaseId"`
	MentionContextCount   int    `json:"mentionContextCount"`
	Command               string `json:"command"`
	ExpertID              string `json:"expertId"`
	RecommendID           string `json:"recommendId"`
	SkillID               string `json:"skillId"`
	SkillCount            int    `json:"skillCount"`
	TotalCount            int    `json:"totalCount"`
	FileURI               string `json:"fileUri"`
	PresentAt             int64  `json:"presentAt"`
	TraceID               string `json:"traceId"`
	RootRequestID         string `json:"rootRequestId"`
	ParentConversationID  string `json:"parentConversationId"`
	AgentName             string `json:"agentName"`
	AgentType             string `json:"agentType"`
	UserID                string `json:"userId"`
}

// ReportChatActivity 向上游发送一条对话活跃上报（chat_request_send）。
// conversationID 由调用方生成（如 wb2api-<ms>），无需真实会话——服务端不校验一致性。
// requestID 为本轮请求独立标识（多轮同会话上报时各条不同）；空时回落 conversationID。
// 错误语义与 doJSON 一致：HTTP 非 2xx / 业务 code != 0 → *Error。
func (c *Client) ReportChatActivity(a *auth.Auth, conversationID, requestID string) error {
	return c.ReportChatActivityModel(a, conversationID, requestID, "deepseek-v4-flash", "DeepSeek V4 Flash")
}

// ReportChatActivityModel 同上，但可指定上报携带的模型：供「体验某模型」类任务
// 对齐实际模型（如 Model_chat_GLM5.2 需 requestModelId=glm-5.2 与独立 requestID）。
func (c *Client) ReportChatActivityModel(a *auth.Auth, conversationID, requestID, modelID, modelName string) error {
	if requestID == "" {
		requestID = conversationID
	}
	if modelID == "" {
		modelID = "deepseek-v4-flash"
	}
	if modelName == "" {
		modelName = modelID
	}
	now := time.Now().UnixMilli()
	ev := chatRequestEvent{
		EventCode:             "chat_request_send",
		Timestamp:             now,
		ReportDelay:           0,
		Mode:                  "craft",
		ConversationID:        conversationID,
		RequestID:             requestID,
		InputLength:           12,
		RequestModelID:        modelID,
		RequestModelName:      modelName,
		IsPlan:                false,
		IsAutoExecuteTerminal: false,
		IsAutoModify:          false,
		CodebaseEnable:        false,
		MaxToken:              0,
		MaxSteps:              0,
		Temperature:           0,
		MaxRetries:            0,
		MentionContexts:       []any{},
		KnowledgeID:           []any{},
		KnowledgeName:         []any{},
		CodebaseID:            "",
		MentionContextCount:   0,
		Command:               "",
		ExpertID:              "",
		RecommendID:           "",
		SkillID:               "",
		SkillCount:            0,
		TotalCount:            0,
		FileURI:               "",
		PresentAt:             now,
		TraceID:               "",
		RootRequestID:         requestID,
		ParentConversationID:  conversationID,
		AgentName:             "default",
		AgentType:             "conversation",
		UserID:                a.UID,
	}
	raw, err := json.Marshal([]chatRequestEvent{ev})
	if err != nil {
		return err
	}
	_, err = c.billingJSON(a, http.MethodPost, reportPath, json.RawMessage(raw))
	return err
}
