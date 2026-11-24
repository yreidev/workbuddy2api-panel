// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// Package providers 管理新增平台的账号与凭证；WorkBuddy 继续使用原有文件和账号池。
package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
	"github.com/yreidev/workbuddy2api-panel/internal/targets"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
	"golang.org/x/net/http/httpguts"
)

// Model 保留当前账号的目录；Tools 表示适配器支持工具协议，真实模型能力另需验收。
type Model struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Tools   bool           `json:"tools"`
	Context int64          `json:"context_window,omitempty"`
	Native  map[string]any `json:"-"`
}

type Credential struct {
	TokenKind       string `json:"token_kind,omitempty"`
	AccessToken     string `json:"access_token,omitempty"`
	RefreshToken    string `json:"refresh_token,omitempty"`
	PAT             string `json:"pat,omitempty"`
	ExpiresAt       int64  `json:"expires_at,omitempty"`
	UID             string `json:"uid,omitempty"`
	Name            string `json:"name,omitempty"`
	MachineID       string `json:"machine_id"`
	DeviceID        string `json:"device_id,omitempty"`
	DeviceKey       string `json:"device_key,omitempty"`
	ClientID        string `json:"client_id,omitempty"`
	MachineToken    string `json:"machine_token,omitempty"`
	MachineCode     string `json:"machine_code,omitempty"`
	MachineType     string `json:"machine_type,omitempty"`
	MachineOS       string `json:"machine_os,omitempty"`
	MachineHostname string `json:"machine_hostname,omitempty"`
	MachineLocal    bool   `json:"machine_local,omitempty"`
	UserType        string `json:"user_type,omitempty"`
}

type Account struct {
	// 两个 Trae 通道导出相同凭证。用摘要识别同一份备份，继续使用账号已保存的令牌。
	ImportDigest  string          `json:"import_digest,omitempty"`
	BindingEpoch  string          `json:"binding_epoch,omitempty"` // 删除后重新导入同一身份时，旧绑定失效。
	Paused        bool            `json:"paused,omitempty"`
	BlockedModels []string        `json:"blocked_models,omitempty"`
	LivenessModel string          `json:"liveness_model,omitempty"`
	ProxyID       string          `json:"proxy_id,omitempty"`
	ID            string          `json:"account_id"`
	Targets       []string        `json:"targets"`
	Name          string          `json:"name"`
	Groups        []string        `json:"groups,omitempty"`
	Priority      int             `json:"priority"`
	Disabled      bool            `json:"disabled"`
	Credential    Credential      `json:"credential"`
	LastLiveness  *LivenessResult `json:"last_liveness,omitempty"`
	Quota         *QuotaSnapshot  `json:"quota,omitempty"`
}

type AccountView struct {
	ManualDisabled bool            `json:"manual_disabled"`
	Paused         bool            `json:"paused"`
	BlockedModels  []string        `json:"blocked_models,omitempty"`
	LivenessModel  string          `json:"liveness_model,omitempty"`
	ProxyID        string          `json:"proxy_id,omitempty"`
	ID             string          `json:"account_id"`
	Targets        []string        `json:"targets"`
	Name           string          `json:"name"`
	Groups         []string        `json:"groups"`
	Priority       int             `json:"priority"`
	Disabled       bool            `json:"disabled"`
	InFlight       int             `json:"in_flight"`
	CoolUntil      time.Time       `json:"cool_until,omitzero"`
	Error          string          `json:"error,omitempty"`
	Models         []Model         `json:"models"`
	Validated      bool            `json:"validated"`
	LastLiveness   *LivenessResult `json:"last_liveness,omitempty"`
	Quota          *QuotaStatus    `json:"quota,omitempty"`
}

type AccountInput struct {
	Paused        bool       `json:"paused,omitempty"`
	BlockedModels []string   `json:"blocked_models,omitempty"`
	LivenessModel string     `json:"liveness_model,omitempty"`
	ProxyID       string     `json:"proxy_id,omitempty"`
	Target        string     `json:"target"`
	Name          string     `json:"name"`
	Groups        []string   `json:"groups"`
	Priority      int        `json:"priority"`
	Disabled      bool       `json:"disabled"`
	Credential    Credential `json:"credential"`
}

type Usage struct {
	Target           string  `json:"target"`
	AccountID        string  `json:"account_id"`
	Model            string  `json:"model"`
	KeyID            string  `json:"key_id,omitempty"`
	Requests         int64   `json:"requests"`
	Failed           int64   `json:"failed"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TokensKnown      int64   `json:"tokens_known"`
	Charge           float64 `json:"charge"`
	ChargeKnown      int64   `json:"charge_known"`
	Unit             string  `json:"unit,omitempty"`
}

type state struct {
	Version  int                `json:"version"`
	Accounts []Account          `json:"accounts"`
	Usage    []Usage            `json:"usage,omitempty"`
	Bindings map[string]Binding `json:"bindings,omitempty"`
}

type runtime struct {
	refresh        sync.Mutex
	importing      bool
	blocked        bool
	inFlight       int
	coolUntil      time.Time
	lastUsed       time.Time
	catalogs       map[string]modelCatalog
	catalogRequest uint64
	catalogFlights map[string][]*catalogFlight
	lastError      string
	quotaFlight    *quotaFlight
	quotaStatus    *QuotaStatus
}

// quotaFlight 合并同账号并发额度查询；发起者取消时由仍在等待的调用者接手，不污染缓存。
type quotaFlight struct {
	ownerCanceled bool
	done          chan struct{}
	status        QuotaStatus
}

// 普通查询共享最新的在途结果；手动刷新始终发起一次新查询。
type catalogFlight struct {
	ownerCanceled bool
	done          chan struct{}
	models        []Model
	err           error
}

type modelCatalog struct {
	models      []Model
	request     uint64
	fetchedAt   time.Time
	unavailable error
}

func (c modelCatalog) fresh(now time.Time) bool {
	return !c.fetchedAt.IsZero() && now.Sub(c.fetchedAt) < 30*time.Minute
}

type Adapter interface {
	Refresh(context.Context, string, Credential) (Credential, error)
	Models(context.Context, string, Credential) ([]Model, error)
	Chat(context.Context, string, Credential, Model, []byte, string, func(map[string]any) error) error
}

type Manager struct {
	activity       activityLedger
	waiters        int
	waitersByKey   map[string]int
	mu             sync.Mutex
	importing      sync.Mutex
	path           string
	data           state
	running        map[string]*runtime
	adapters       map[string]Adapter
	changed        chan struct{}
	logins         map[string]*Login
	callbackServer *http.Server
	callbackStop   chan struct{}
	proxies        *proxies.Registry
	usage          *usage.Recorder
	qoderMachine   *qoderMachine
}

func Open(path string, client *http.Client) (*Manager, error) {
	if client == nil {
		client = upstream.New().ChatHTTP
	}
	m := &Manager{path: path, data: state{Accounts: []Account{}, Usage: []Usage{}, Bindings: map[string]Binding{}}, running: map[string]*runtime{}, adapters: map[string]Adapter{}, changed: make(chan struct{}), waitersByKey: map[string]int{}, logins: map[string]*Login{}}
	m.adapters["traework"] = &Trae{HTTP: upstream.HTTPClientForProfile(client, upstream.ProfileTraeWork)}
	m.adapters["traecode"] = &Trae{HTTP: upstream.HTTPClientForProfile(client, upstream.ProfileTraeCode)}
	m.adapters["qoder"] = &Qoder{HTTP: upstream.HTTPClientForProfile(client, upstream.ProfileQoder)}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("读取平台账号文件 %s: %w", path, err)
		}
		if err == nil {
			if err = json.Unmarshal(raw, &m.data); err != nil {
				return nil, fmt.Errorf("解析平台账号文件 %s: %w", path, err)
			}
			if m.data.Version != 1 && m.data.Version != 2 {
				return nil, fmt.Errorf("平台账号文件版本不支持：%d", m.data.Version)
			}
			seen := map[string]bool{}
			for _, a := range m.data.Accounts {
				if len(a.ID) != 27 || !strings.HasPrefix(a.ID, "pa_") || strings.ContainsAny(a.ID, "/\\") || seen[a.ID] || len(a.Targets) == 0 {
					return nil, errors.New("平台账号文件包含无效或重复账号")
				}
				seen[a.ID] = true
				for _, t := range a.Targets {
					if !targets.Valid(t) || targets.WorkBuddy(t) {
						return nil, fmt.Errorf("平台账号 %s 的上游无效", a.ID)
					}
					if a.Credential.UID == "" || stableAccountID(t, a.Credential.UID) != a.ID {
						return nil, fmt.Errorf("平台账号 %s 的身份与产品／地区不一致", a.ID)
					}
				}
			}
		}
	}
	for _, a := range m.data.Accounts {
		m.running[a.ID] = newRuntime()
		if path != "" {
			_, err := os.Stat(m.guardPath(a.ID))
			if err == nil {
				m.running[a.ID].lastError = "上次令牌刷新未安全落盘，请重新导入凭证"
				m.running[a.ID].blocked = true
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("读取账号 %s 的刷新标记: %w", a.ID, err)
			}
		}
	}
	if m.data.Bindings == nil {
		m.data.Bindings = map[string]Binding{}
	}
	if err := m.openActivity(); err != nil {
		return nil, fmt.Errorf("打开平台账本 %s: %w", path, err)
	}
	return m, nil
}

func newRuntime() *runtime {
	return &runtime{catalogs: map[string]modelCatalog{}, catalogFlights: map[string][]*catalogFlight{}}
}

func (m *Manager) saveLocked() error {
	if m.path == "" {
		return nil
	}
	if m.data.Version == 0 {
		// 首次保存先留下可恢复的空 v1 文件。成功发布 v2 后才对外确认账号导入。
		if err := writePlatformFile(m.path, state{Version: 1, Accounts: []Account{}}); err != nil {
			return err
		}
		if err := m.writeInitialActivity(activitySnapshot{Version: 1, Usage: []Usage{}, Bindings: map[string]Binding{}}); err != nil {
			return err
		}
	}
	if err := writePlatformFile(m.path, state{Version: 2, Accounts: m.data.Accounts}); err != nil {
		return err
	}
	m.data.Version = 2
	return nil
}

func writePlatformFile(path string, value any) error {
	if path == "" {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeProviderFile(path, raw, 0600)
}

// writeProviderFile 先把内容写到同目录临时文件，再持久化替换目标文件。
func writeProviderFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建平台数据目录: %w", err)
	}
	f, err := os.CreateTemp(dir, ".providers-*")
	if err != nil {
		return fmt.Errorf("创建平台临时文件: %w", err)
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("写平台文件 %s: %w", path, err)
	}
	return replaceProviderFile(f.Name(), path)
}

func (m *Manager) List() []AccountView {
	if m == nil {
		return []AccountView{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AccountView, 0, len(m.data.Accounts))
	for _, a := range m.data.Accounts {
		rt := m.running[a.ID]
		models := []Model{}
		for _, t := range a.Targets {
			for _, md := range rt.catalogs[t].models {
				md.ID = targets.Public(t, md.ID)
				md.Native = nil
				models = append(models, md)
			}
		}
		out = append(out, AccountView{ID: a.ID, ManualDisabled: a.Disabled, Paused: a.Paused, BlockedModels: slices.Clone(a.BlockedModels), LivenessModel: a.LivenessModel, ProxyID: a.ProxyID, Targets: slices.Clone(a.Targets), Name: a.Name, Groups: append([]string{}, a.Groups...), Priority: a.Priority, Disabled: a.Disabled || rt.blocked, InFlight: rt.inFlight, CoolUntil: rt.coolUntil, Error: rt.lastError, Models: models, LastLiveness: a.LastLiveness, Quota: quotaStatus(a, rt)})
	}
	return out
}

func (m *Manager) Usage() []Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Usage{}, m.data.Usage...)
}

func (m *Manager) Import(ctx context.Context, in AccountInput) (string, error) {
	if !m.importing.TryLock() {
		return "", errors.New("已有账号正在导入，请稍后重试")
	}
	defer m.importing.Unlock()
	if !targets.Valid(in.Target) || targets.WorkBuddy(in.Target) {
		return "", errors.New("请选择 TraeWork、Trae Code 或 Qoder 的地区")
	}
	if in.Priority < 0 || in.Priority > 99 {
		return "", errors.New("优先级必须在 0～99 之间")
	}
	if in.LivenessModel != "" {
		target, native, explicit := targets.Parse(in.LivenessModel)
		traeSibling := strings.HasPrefix(target, "trae") && strings.HasPrefix(in.Target, "trae") && strings.HasSuffix(target, ":cn")
		if !explicit || native == "" || target != in.Target && !traeSibling {
			return "", errors.New("导入的测活模型不属于该账号的产品与地区")
		}
	}
	c := in.Credential
	for name, value := range map[string]string{"machine_id": c.MachineID, "machine_token": c.MachineToken, "machine_code": c.MachineCode, "machine_type": c.MachineType, "machine_os": c.MachineOS, "machine_hostname": c.MachineHostname} {
		if !httpguts.ValidHeaderFieldValue(value) {
			return "", fmt.Errorf("设备字段 %s 包含非法 HTTP 头字符", name)
		}
	}
	if c.MachineHostname != "" {
		for _, ch := range c.MachineHostname {
			if ch < 0x20 || ch > 0x7e {
				return "", errors.New("machine_hostname 需要官方客户端已规范化的 ASCII 值")
			}
		}
		if len(c.MachineHostname) > 96 {
			return "", errors.New("machine_hostname 超出官方 96 字节上限")
		}
	}
	if c.DeviceKey != "" {
		if _, err := traePrivateKey(c.DeviceKey); err != nil {
			return "", err
		}
	}
	if c.AccessToken == "" && c.RefreshToken == "" && c.PAT == "" {
		return "", errors.New("凭证需要 access_token、refresh_token 或 Qoder PAT")
	}
	if c.MachineID == "" {
		c.MachineID = uuid.NewString()
	}
	if c.DeviceID == "" {
		c.DeviceID = uuid.NewString()
	}
	if c.ClientID == "" {
		c.ClientID = "ono9krqynydwx5"
	}

	// 先占住已有账号，再做可能消费一次性刷新令牌的认证；缺 UID 时按原凭证定位。
	var reserved *runtime
	var existingID, importDigest, existingRefreshToken string
	proxyID := in.ProxyID
	updateProxy := false
	if strings.HasPrefix(in.Target, "trae") && c.RefreshToken != "" {
		raw, err := json.Marshal(in.Credential)
		if err != nil {
			return "", fmt.Errorf("识别导入凭证: %w", err)
		}
		digest := sha256.Sum256(raw)
		importDigest = hex.EncodeToString(digest[:])
	}
	m.mu.Lock()
	for _, a := range m.data.Accounts {
		if stableAccountID(in.Target, a.Credential.UID) != a.ID {
			continue
		}
		matched := c.UID != "" && c.UID == a.Credential.UID
		if c.UID == "" {
			matched = c.RefreshToken != "" && c.RefreshToken == a.Credential.RefreshToken ||
				c.PAT != "" && c.PAT == a.Credential.PAT ||
				c.AccessToken != "" && c.AccessToken == a.Credential.AccessToken ||
				importDigest != "" && importDigest == a.ImportDigest
		}
		if !matched {
			continue
		}
		rt := m.running[a.ID]
		if !rt.refresh.TryLock() {
			m.mu.Unlock()
			return "", errors.New("账号正在刷新凭证，请稍后重新导入")
		}
		if rt.inFlight > 0 {
			rt.refresh.Unlock()
			m.mu.Unlock()
			return "", errors.New("账号仍有在途请求，请稍后重新导入")
		}
		rt.importing = true
		rt.inFlight++
		reserved, existingID, existingRefreshToken = rt, a.ID, a.Credential.RefreshToken
		proxyID = a.ProxyID
		if importDigest != "" && importDigest == a.ImportDigest && !rt.blocked {
			c = a.Credential
		}
		if c.UID == "" {
			c.UID = a.Credential.UID
		}
		if strings.HasPrefix(in.Target, "qoder:") {
			if in.Credential.MachineID == "" {
				c.MachineID = a.Credential.MachineID
			}
			if in.Credential.DeviceID == "" {
				c.DeviceID = a.Credential.DeviceID
			}
			if c.MachineID == a.Credential.MachineID && c.MachineToken == "" && c.MachineCode == "" && c.MachineType == "" {
				c.MachineToken, c.MachineType, c.MachineCode = a.Credential.MachineToken, a.Credential.MachineType, a.Credential.MachineCode
				c.MachineOS, c.MachineHostname, c.MachineLocal = a.Credential.MachineOS, a.Credential.MachineHostname, a.Credential.MachineLocal
			}
		}
		break
	}
	m.mu.Unlock()
	if reserved != nil {
		defer func() {
			m.mu.Lock()
			reserved.inFlight--
			reserved.importing = false
			reserved.refresh.Unlock()
			m.wakeLocked()
			m.mu.Unlock()
		}()
	}

	// 若已有账号的原绑定代理已删除或不可用，且本次显式选择了可用新代理（如重新浏览器授权），允许切到新代理。
	if reserved != nil && proxyID != "" && in.ProxyID != "" && in.ProxyID != proxyID && m.proxies != nil {
		if _, _, oldUsable, _ := m.proxies.CheckProxy(proxyID); !oldUsable {
			if _, _, newUsable, _ := m.proxies.CheckProxy(in.ProxyID); newUsable {
				proxyID = in.ProxyID
				updateProxy = true
			}
		}
	}

	needsQoderUMID := m.qoderMachine != nil && strings.HasPrefix(in.Target, "qoder:") && (c.MachineLocal || c.MachineToken == "" || c.MachineToken == c.MachineID)
	needsRefresh := c.AccessToken == "" || c.ExpiresAt > 0 && c.ExpiresAt < time.Now().Add(5*time.Minute).Unix()
	needsUser := (c.UID == "" && strings.HasPrefix(in.Target, "trae")) || (strings.HasPrefix(in.Target, "qoder:") && c.UserType == "")
	needsOutbound := needsQoderUMID || needsRefresh || needsUser

	// 离线完整凭证重导入到原绑失效代理的已有账号时无需出站，允许直接落盘恢复凭证；
	// 新增账号、或需要出站的导入仍严格校验代理线路与 RequireBound。
	skipOfflineRoute := reserved != nil && !needsOutbound && proxyID != ""
	if !skipOfflineRoute {
		routed, routeErr := m.route(ctx, proxyID)
		if routeErr != nil {
			return "", routeErr
		}
		ctx = routed
	}
	adapter := m.adapters[strings.Split(in.Target, ":")[0]]
	var err error
	if needsQoderUMID {
		c, err = m.qoderIdentity(ctx, in.Target, proxyID, c)
		if err != nil {
			return "", err
		}
	}
	if needsRefresh {
		wasBlocked := false
		if reserved != nil {
			m.mu.Lock()
			wasBlocked = reserved.blocked
			m.mu.Unlock()
			// 受刷新保护时仅拒绝重放原同一刷新令牌；提供新 refresh_token 或可复用的 Qoder PAT 允许原地恢复。
			replayingOld := in.Credential.PAT == "" && (c.RefreshToken == "" || c.RefreshToken == existingRefreshToken)
			if wasBlocked && replayingOld {
				return "", errors.New("账号刷新状态不确定，请重新授权或导入有效的新凭证")
			}
			if !wasBlocked {
				if err := m.markRefreshing(existingID); err != nil {
					return "", err
				}
			}
			m.mu.Lock()
			reserved.blocked = true
			reserved.lastError = "导入时的凭证刷新尚未安全保存，请重新授权或导入有效的新凭证"
			m.mu.Unlock()
		}
		c, err = adapter.Refresh(ctx, in.Target, c)
		if err != nil {
			if reserved != nil && !wasBlocked && canRetryRouteError(err) {
				if clearErr := m.clearRefreshGuard(existingID); clearErr != nil {
					return "", fmt.Errorf("导入账号认证（%v）: %w", err, clearErr)
				}
				m.mu.Lock()
				reserved.blocked = false
				reserved.lastError = err.Error()
				m.mu.Unlock()
			}
			return "", fmt.Errorf("导入账号认证: %w", err)
		}
	}
	if c.UID == "" && strings.HasPrefix(in.Target, "trae") {
		c, err = m.adapters[strings.Split(in.Target, ":")[0]].(*Trae).user(ctx, c)
		if err != nil {
			return "", err
		}
	}
	if strings.HasPrefix(in.Target, "qoder:") && c.UserType == "" {
		c, err = m.adapters["qoder"].(*Qoder).user(ctx, in.Target, c)
		if err != nil {
			return "", err
		}
	}
	if c.UID == "" {
		return "", errors.New("凭证缺少稳定 uid，请从完整登录凭证导入")
	}
	id := stableAccountID(in.Target, c.UID)
	if reserved != nil && id != existingID || in.Credential.UID != "" && c.UID != in.Credential.UID {
		return "", errors.New("刷新后的账号身份与导入前不一致")
	}
	var reprobe string
	defer func() { m.reprobeIfDown(reprobe) }()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, a := range m.data.Accounts {
		if a.ID == id {
			rt := m.running[id]
			if reserved == nil {
				if a.ProxyID != proxyID {
					oldBroken := a.ProxyID != "" && m.proxies != nil
					if oldBroken {
						_, _, usable, _ := m.proxies.CheckProxy(a.ProxyID)
						oldBroken = !usable
					}
					if !oldBroken {
						return "", errors.New("已绑定代理的账号需携带原 UID 或可识别的原凭证重新导入")
					}
					updateProxy = true
				}
				if !rt.refresh.TryLock() {
					return "", errors.New("账号正在刷新凭证，请稍后重新导入")
				}
				defer rt.refresh.Unlock()
				if rt.inFlight > 0 {
					return "", errors.New("账号仍有在途请求，请稍后重新导入")
				}
			}
			old := a
			if !slices.Contains(a.Targets, in.Target) {
				a.Targets = append(slices.Clone(a.Targets), in.Target)
			}
			a.Credential = c
			a.ImportDigest = importDigest
			if updateProxy {
				a.ProxyID = proxyID
			}
			if in.Name != "" {
				a.Name = in.Name
			}
			m.data.Accounts[i] = a
			if err = m.saveLocked(); err != nil {
				m.data.Accounts[i] = old
				return "", err
			}
			clear(rt.catalogs)
			rt.coolUntil = time.Time{}
			reprobe = a.ProxyID
			return m.finishImportLocked(id, rt)
		}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = c.Name
	}
	if name == "" {
		name = "新平台账号"
	}
	a := Account{ImportDigest: importDigest, ID: id, BindingEpoch: uuid.NewString(), ProxyID: in.ProxyID, Name: name, Targets: []string{in.Target}, Credential: c, Groups: slices.Clone(in.Groups), Priority: in.Priority, Disabled: in.Disabled, Paused: in.Paused, BlockedModels: slices.Clone(in.BlockedModels), LivenessModel: in.LivenessModel}
	m.data.Accounts = append(m.data.Accounts, a)
	if err = m.saveLocked(); err != nil {
		m.data.Accounts = m.data.Accounts[:len(m.data.Accounts)-1]
		return "", err
	}
	rt := newRuntime()
	m.running[id] = rt
	reprobe = a.ProxyID
	return m.finishImportLocked(id, rt)
}

// 新凭证已落盘后解除保护，也覆盖同一身份删除再导入时遗留的标记。
func (m *Manager) finishImportLocked(id string, rt *runtime) (string, error) {
	defer m.wakeLocked()
	if err := m.clearRefreshGuard(id); err != nil && !errors.Is(err, os.ErrNotExist) {
		rt.blocked, rt.lastError = true, err.Error()
		return "", err
	}
	rt.blocked, rt.lastError = false, ""
	return id, nil
}

func (m *Manager) Update(id string, in AccountInput, remove bool) error {
	var reprobe string
	err := m.update(id, func(a *Account) error {
		if in.Priority < 0 || in.Priority > 99 {
			return errors.New("优先级必须在 0～99 之间")
		}
		if in.LivenessModel != "" {
			target, native, explicit := targets.Parse(in.LivenessModel)
			if !explicit || native == "" || !slices.Contains(a.Targets, target) {
				return errors.New("批量与定时测活模型必须属于该账号的产品与地区")
			}
		}
		if in.ProxyID != "" && in.ProxyID != a.ProxyID {
			reprobe = in.ProxyID
		}
		a.ProxyID, a.Name = in.ProxyID, strings.TrimSpace(in.Name)
		a.Groups, a.Priority, a.Disabled = slices.Clone(in.Groups), in.Priority, in.Disabled
		a.Paused, a.BlockedModels, a.LivenessModel = in.Paused, slices.Clone(in.BlockedModels), in.LivenessModel
		return nil
	}, remove, false)
	if err == nil && !remove {
		m.reprobeIfDown(reprobe)
	}
	return err
}

// RemoveGroup 删分组时同步把组内所有平台账号移出该组（与 WorkBuddy 行为一致），写盘失败回滚。
func (m *Manager) RemoveGroup(groupID string) error {
	if m == nil || groupID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	old := make([]Account, len(m.data.Accounts))
	for i, a := range m.data.Accounts {
		old[i] = a
		if slices.Contains(a.Groups, groupID) {
			next := slices.DeleteFunc(slices.Clone(a.Groups), func(g string) bool { return g == groupID })
			m.data.Accounts[i].Groups = next
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := m.saveLocked(); err != nil {
		m.data.Accounts = old
		return err
	}
	m.wakeLocked()
	return nil
}

func (m *Manager) update(id string, edit func(*Account) error, remove, recover bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, a := range m.data.Accounts {
		if a.ID == id {
			rt := m.running[id]
			if !rt.refresh.TryLock() {
				return errors.New("账号正在刷新凭证，暂不能修改或移除")
			}
			defer rt.refresh.Unlock()
			if rt.inFlight > 0 {
				return errors.New("账号有在途请求，暂不能修改或移除")
			}
			old := slices.Clone(m.data.Accounts)
			oldBindings := m.data.Bindings
			if remove {
				m.data.Accounts = slices.Delete(slices.Clone(m.data.Accounts), i, i+1)
				m.data.Bindings = maps.Clone(m.data.Bindings)
				for key, binding := range m.data.Bindings {
					if binding.AccountID == id {
						delete(m.data.Bindings, key)
					}
				}
			} else {
				if err := edit(&a); err != nil {
					return err
				}
				m.data.Accounts[i] = a
			}
			if err := m.saveLocked(); err != nil {
				m.data.Accounts = old
				m.data.Bindings = oldBindings
				return err
			}
			if remove {
				delete(m.running, id)
			} else if recover {
				m.running[id].coolUntil = time.Time{}
				m.running[id].lastError = ""
			}
			m.wakeLocked()
			return nil
		}
	}
	return os.ErrNotExist
}
func (m *Manager) wakeLocked() { close(m.changed); m.changed = make(chan struct{}) }

func (m *Manager) credential(ctx context.Context, id, target string) (Credential, error) {
	m.mu.Lock()
	rt := m.running[id]
	m.mu.Unlock()
	if rt == nil {
		return Credential{}, os.ErrNotExist
	}
	rt.refresh.Lock()
	defer rt.refresh.Unlock()
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	m.mu.Lock()
	var c Credential
	var proxyID string
	found := false
	for _, a := range m.data.Accounts {
		if a.ID == id && slices.Contains(a.Targets, target) {
			if rt.blocked {
				m.mu.Unlock()
				return Credential{}, errors.New("账号凭证刷新未安全落盘，请重新导入")
			}
			c = a.Credential
			proxyID = a.ProxyID
			found = true
			break
		}
	}
	m.mu.Unlock()
	if !found {
		return c, os.ErrNotExist
	}
	withMachine, err := m.qoderIdentity(ctx, target, proxyID, c)
	if err != nil {
		m.mu.Lock()
		rt.lastError = err.Error()
		m.mu.Unlock()
		return c, err
	}
	if withMachine != c {
		m.mu.Lock()
		for i := range m.data.Accounts {
			if m.data.Accounts[i].ID == id {
				m.data.Accounts[i].Credential = withMachine
				if err := m.saveLocked(); err != nil {
					m.data.Accounts[i].Credential = c
					m.mu.Unlock()
					return c, fmt.Errorf("保存 Qoder 设备身份: %w", err)
				}
				break
			}
		}
		m.mu.Unlock()
		c = withMachine
	}
	if c.AccessToken != "" && (c.ExpiresAt == 0 || c.ExpiresAt > time.Now().Add(5*time.Minute).Unix()) {
		return c, nil
	}
	if err := m.markRefreshing(id); err != nil {
		return Credential{}, err
	}
	fresh, err := m.adapters[strings.Split(target, ":")[0]].Refresh(ctx, target, c)
	if err == nil && (fresh.UID == "" || stableAccountID(target, fresh.UID) != id) {
		err = fmt.Errorf("账号 %s 刷新后的身份与原账号不一致", id)
	}
	if err != nil {
		// CONNECT 失败发生在发送刷新操作之前，原刷新令牌仍可用。
		if canRetryRouteError(err) {
			if clearErr := m.clearRefreshGuard(id); clearErr == nil {
				return c, err
			} else {
				err = fmt.Errorf("刷新凭证（%v）: %w", err, clearErr)
			}
		}
		m.mu.Lock()
		rt.blocked = true
		rt.lastError = "刷新凭证失败，请重新导入：" + err.Error()
		m.mu.Unlock()
		return c, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, a := range m.data.Accounts {
		if a.ID == id {
			m.data.Accounts[i].Credential = fresh
			if err = m.saveLocked(); err != nil {
				m.data.Accounts[i].Credential = c
				rt.blocked = true
				rt.lastError = "刷新后的凭证写盘失败，账号已停止派发"
				return Credential{}, err
			}
			if err := m.clearRefreshGuard(id); err != nil {
				rt.blocked = true
				return Credential{}, err
			}
			return fresh, nil
		}
	}
	return Credential{}, os.ErrNotExist
}

func (m *Manager) Models(ctx context.Context, id, target string, force bool) ([]Model, error) {
	for {
		m.mu.Lock()
		rt := m.running[id]
		if rt == nil {
			m.mu.Unlock()
			return nil, os.ErrNotExist
		}
		proxyID := ""
		valid := false
		for _, a := range m.data.Accounts {
			if a.ID == id && slices.Contains(a.Targets, target) {
				valid = true
				proxyID = a.ProxyID
			}
		}
		if !valid {
			m.mu.Unlock()
			return nil, errors.New("账号不属于该上游")
		}
		cached := rt.catalogs[target]
		if !force && cached.fresh(time.Now()) {
			out := slices.Clone(cached.models)
			m.mu.Unlock()
			return out, cached.unavailable
		}
		if rt.importing {
			m.mu.Unlock()
			return nil, errors.New("账号正在导入凭证，请稍后刷新模型")
		}
		flights := rt.catalogFlights[target]
		if !force && len(flights) > 0 {
			flight := flights[len(flights)-1]
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-flight.done:
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if flight.ownerCanceled {
					// 发起者断开时，由仍需目录的调用者接手；同样只发一个新查询。
					continue
				}
				return slices.Clone(flight.models), flight.err
			}
		}
		flight := &catalogFlight{done: make(chan struct{}), err: errors.New("平台目录查询异常退出")}
		rt.catalogFlights[target] = append(flights, flight)
		rt.catalogRequest++
		request := rt.catalogRequest
		rt.inFlight++
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			rt.catalogFlights[target] = slices.DeleteFunc(rt.catalogFlights[target], func(f *catalogFlight) bool { return f == flight })
			flight.ownerCanceled = flight.err != nil && ctx.Err() != nil
			close(flight.done)
			rt.inFlight--
			m.wakeLocked()
			m.mu.Unlock()
		}()
		flight.models, flight.err = m.fetchModels(ctx, id, target, proxyID, rt, request)
		return slices.Clone(flight.models), flight.err
	}
}

func (m *Manager) fetchModels(ctx context.Context, id, target, proxyID string, rt *runtime, request uint64) ([]Model, error) {
	ctx, err := m.route(ctx, proxyID)
	if err != nil {
		return nil, err
	}
	c, err := m.credential(ctx, id, target)
	if err != nil {
		return nil, err
	}
	models, err := m.adapters[strings.Split(target, ":")[0]].Models(ctx, target, c)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running[id] != rt {
		return nil, os.ErrNotExist
	}
	// 较晚发起的有效刷新已落地，先前的响应不能改写目录或状态。
	if current := rt.catalogs[target]; request < current.request {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return current.models, current.unavailable
	}
	if err != nil {
		if errors.Is(err, errQoderCatalogUnavailable) {
			rt.catalogs[target] = modelCatalog{request: request, unavailable: err, fetchedAt: time.Now()}
		}
		rt.lastError = err.Error()
		return nil, err
	}
	if len(models) == 0 {
		return nil, errors.New("平台模型目录为空")
	}
	seen := map[string]bool{}
	unique := make([]Model, 0, len(models))
	for _, model := range models {
		if !seen[model.ID] {
			unique = append(unique, model)
			seen[model.ID] = true
		}
	}
	rt.catalogs[target] = modelCatalog{models: unique, request: request, fetchedAt: time.Now()}
	rt.lastError = ""
	return unique, nil
}

func (m *Manager) guardPath(id string) string { return m.path + ".refresh-" + id }

func (m *Manager) clearRefreshGuard(id string) error {
	if m.path != "" {
		if err := os.Remove(m.guardPath(id)); err != nil {
			return fmt.Errorf("清理账号刷新标记: %w", err)
		}
	}
	return nil
}

func (m *Manager) markRefreshing(id string) error {
	if m.path == "" {
		return nil
	}
	if err := writeProviderFile(m.guardPath(id), []byte("refresh pending\n"), 0600); err != nil {
		return fmt.Errorf("写账号刷新标记: %w", err)
	}
	return nil
}

// SetProxies 在启动时装配；平台账号直接选择已有代理，登录、刷新、目录和推理保持同一线路。
func (m *Manager) SetProxies(registry *proxies.Registry) {
	if m.proxies != nil {
		m.proxies.SetProviderHosts(nil)
	}
	m.proxies = registry
	if registry != nil {
		registry.SetProviderHosts(m.proxyHosts)
	}
}

func (m *Manager) proxyHosts() map[string][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	hosts := map[string][]string{}
	for _, a := range m.data.Accounts {
		if a.ProxyID == "" {
			continue
		}
		for _, target := range a.Targets {
			family, _, _ := strings.Cut(target, ":")
			var base string
			switch adapter := m.adapters[family].(type) {
			case *Trae:
				base = adapter.codeBase()
				if family == "traework" {
					base = adapter.workBase()
				}
			case *Qoder:
				base = adapter.probeBase(target, a.Credential, a.ProxyID)
			}
			if u, err := url.Parse(base); err == nil && u.Hostname() != "" && !slices.Contains(hosts[a.ProxyID], u.Hostname()) {
				hosts[a.ProxyID] = append(hosts[a.ProxyID], u.Hostname())
			}
		}
	}
	return hosts
}

// SetUsage 在开始服务前接入共用时序账本；平台累计账本仍保留，旧记录不伪造时间。
func (m *Manager) SetUsage(recorder *usage.Recorder) { m.usage = recorder }

type unboundRouteKey struct{}
type boundRouteKey struct{}

type boundRouteCheck struct {
	registry *proxies.Registry
	proxyID  string
}

func (m *Manager) proxyName(proxyID string) string {
	if proxyID == "" || m.proxies == nil {
		return ""
	}
	name, _, _, code := m.proxies.CheckProxy(proxyID)
	if code == "proxy_not_found" {
		return ""
	}
	return name
}

// reprobeIfDown 平台账号新绑到曾因探测别域入口失败而标 down 的代理时，尽快触发一次重测。
func (m *Manager) reprobeIfDown(proxyID string) {
	if proxyID == "" || m.proxies == nil {
		return
	}
	if _, _, usable, code := m.proxies.CheckProxy(proxyID); !usable && code == "proxy_not_available" {
		m.proxies.MarkDue(proxyID)
	}
}

func (m *Manager) route(ctx context.Context, proxyID string) (context.Context, error) {
	ctx = context.WithValue(ctx, providerRouteKey{}, proxyID)
	if m.proxies == nil {
		if proxyID != "" {
			return ctx, &upstream.RouteError{Blocked: true, Err: errors.New("代理管理未启用")}
		}
		return ctx, nil
	}
	if proxyID == "" {
		if m.proxies.Settings().RequireBound {
			return ctx, proxyUnavailable("proxy_required")
		}
		return context.WithValue(ctx, unboundRouteKey{}, m.proxies), nil
	}
	_, _, usable, code := m.proxies.CheckProxy(proxyID)
	if !usable {
		return ctx, proxyUnavailable(code)
	}
	ctx = upstream.WithProxy(ctx, proxyID)
	return context.WithValue(ctx, boundRouteKey{}, boundRouteCheck{registry: m.proxies, proxyID: proxyID}), nil
}

func proxyUnavailable(code string) error {
	return &upstream.RouteError{Blocked: true, Err: &Error{Status: 503, Code: code, Retry: true}}
}

func stableAccountID(target, uid string) string {
	family, region, _ := strings.Cut(target, ":")
	if family == "traework" || family == "traecode" {
		family = "trae"
	}
	sum := sha256.Sum256([]byte(family + ":" + region + ":" + uid))
	return "pa_" + hex.EncodeToString(sum[:12])
}
