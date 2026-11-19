// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// Package apikeys 客户端密钥：管理员在面板里新建、发给别人调 /v1/* 的密钥。每个密钥带启用开关、
// 到期时间、每天和总量的用量上限（请求数、token、积分）和模型白名单，用量按密钥计数。
//
// 管理员密钥（config.json 的 api_key）不在这里：它照旧能登面板、调接口，不受上限约束。客户端密钥
// 登不了面板。只要建过一个客户端密钥，/v1/* 就必须带有效密钥，管理员密钥为空也一样（见 server）。
//
// 落盘：data/api_keys.json（0600，原子替换），密钥原文也存在里面，面板里随时能复制。增删改立即
// 写盘，写不成就撤回这次改动；用量计数防抖写盘（flushInterval），停机时再写一次。
package apikeys

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yreidev/workbuddy2api-panel/internal/glob"
	"github.com/yreidev/workbuddy2api-panel/internal/targets"
)

// flushInterval 用量计数的防抖落盘间隔（var 供测试缩短）。
var flushInterval = 10 * time.Second

// randRead 生成密钥和 id 的随机源（测试可替换成失败的实现）。
var randRead = rand.Read

// cst 每天的上限按北京时间 00:00 切日，和上游积分、签到的日切一致，不看容器时区。
var cst = time.FixedZone("CST", 8*60*60)

const (
	fileVersion = `"2"` // v1 将 version 解为整数，字符串使其在加载密钥时安全停止
	maxName     = 64
	maxNote     = 500
	maxModels   = 100
	maxPattern  = 128
)

// Limits 一组上限，0 表示这一项不限。
type Limits struct {
	Requests int64   `json:"requests,omitempty"`
	Tokens   int64   `json:"tokens,omitempty"`
	Credits  float64 `json:"credits,omitempty"`
}

// Usage 一段时间里的用量。请求数按客户端请求计（换号重试不重复计），放行时先计上、最后失败的再退还
// （Refund），所以只算成功的请求；token、积分按上游回的累计，上游没回积分的请求不计积分。
type Usage struct {
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Credits  float64 `json:"credits"`
}

// Key 一个客户端密钥。
type Key struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Note       string    `json:"note,omitempty"`
	Secret     string    `json:"secret"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at,omitzero"` // 零值 = 不过期
	DailyLimit Limits    `json:"daily_limit"`
	TotalLimit Limits    `json:"total_limit"`
	// Models 模型白名单（只认 * 通配，不区分大小写），按经模型路由改写后实际发往上游的模型名匹配，
	// 不带 cn: / global: 前缀。空 = 不限。
	Models         []string `json:"models,omitempty"`
	AllowedTargets []string `json:"allowed_targets,omitempty"`
	DefaultTarget  string   `json:"default_target,omitempty"`
	// Groups 只用这些账号分组里的号（pool.Group 的 id）。空 = 不限。分组存不存在由面板在保存时查。
	Groups     []string  `json:"groups,omitempty"`
	LastUsedAt time.Time `json:"last_used_at,omitzero"`
	Used       Usage     `json:"used"`          // 累计
	Today      Usage     `json:"today"`         // Day 那天的用量
	Day        string    `json:"day,omitempty"` // 北京时间 2006-01-02
}

// AllowsModel 白名单是否放行这个模型（裸模型名）。
func (k Key) AllowsModel(model string) bool {
	if len(k.Models) == 0 {
		return true
	}
	for _, p := range k.Models {
		if glob.Match(p, model) {
			return true
		}
	}
	return false
}

// Expired now 时是否已过期。
func (k Key) Expired(now time.Time) bool {
	return !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt)
}

// Input 新建、修改密钥时可设的字段。
type Input struct {
	Name           string    `json:"name"`
	Note           string    `json:"note"`
	Enabled        bool      `json:"enabled"`
	ExpiresAt      time.Time `json:"expires_at"`
	DailyLimit     Limits    `json:"daily_limit"`
	TotalLimit     Limits    `json:"total_limit"`
	Models         []string  `json:"models"`
	Groups         []string  `json:"groups"`
	AllowedTargets []string  `json:"allowed_targets"`
	DefaultTarget  string    `json:"default_target"`
}

// Denied 密钥这次不能用：停用、过期、删了，或者用量到了上限。
type Denied struct {
	Status  int    // 401 或 429
	Code    string // key_disabled / key_expired / key_deleted / key_quota_exceeded
	Message string
	// RetryAfter 每天的上限用完时到北京时间次日 00:00 还有多久；总量用完时为 0（等不来，要管理员调高）。
	RetryAfter time.Duration
}

func (d *Denied) Error() string { return d.Message }

// ErrNotFound 没有这个 id 的密钥。
var ErrNotFound = errors.New("密钥不存在")

// ErrSave 密钥文件写不进去（这次增删改已撤回）。
var ErrSave = errors.New("写密钥文件失败")

// Store 客户端密钥的存储，并发安全。
type Store struct {
	path string

	// mu 护住下面全部字段，写盘也在锁内：文件只有几 KB，写一次不到一毫秒，换来写盘顺序和内存
	// 改动顺序一致，不会让旧的计数快照盖掉刚写下的增删改。
	mu       sync.Mutex
	keys     []*Key          // 按创建顺序
	bySecret map[string]*Key // 密钥原文的 SHA-256 → 密钥
	dirty    bool            // 有没写盘的用量计数

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

type file struct {
	Version json.RawMessage `json:"version"`
	Keys    []*Key          `json:"keys"`
}

// Open 读取 path 下的密钥文件；文件不存在时从空开始，读不了或格式不对时报错（不能当成没有密钥，
// 否则「建过密钥就必须带」的规则会悄悄失效）。path 为空时只存在内存里（测试用）。
func Open(path string) (*Store, error) {
	return open(path, false)
}

// OpenWithLegacyRecovery 为自带程序仍按 v1 读取密钥的一键更新保留管理面板恢复入口。
func OpenWithLegacyRecovery(path string) (*Store, error) { return open(path, true) }

const recoveryIDPrefix = "v2-recovery-"

func open(path string, legacyRecovery bool) (*Store, error) {
	s := &Store{path: path, bySecret: map[string]*Key{}, stop: make(chan struct{}), done: make(chan struct{})}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if legacyRecovery {
			if err := s.publishRecovery(path); err != nil {
				return nil, err
			}
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取密钥文件 %s: %w", path, err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析密钥文件 %s: %w", path, err)
	}
	// 旧版保存用量时会丢弃未知 JSON 字段，故通过它原生保留的密钥 ID 关联新版文件。
	if string(f.Version) == "1" {
		for i, k := range f.Keys {
			if k == nil || !strings.HasPrefix(k.ID, recoveryIDPrefix) {
				continue
			}
			side := recoveryPath(path, k.ID)
			legacy := slices.Delete(f.Keys, i, i+1)
			f, err = readRecovery(side, legacy)
			if err != nil {
				return nil, err
			}
			legacyRecovery = len(legacy) > 0
			if !legacyRecovery {
				s.path = side
			}
			break
		}
	}
	for _, k := range f.Keys {
		if k == nil || k.ID == "" || k.Secret == "" {
			continue
		}
		s.keys = append(s.keys, k)
		s.bySecret[digest(k.Secret)] = k
	}
	switch string(f.Version) {
	case fileVersion:
		if !legacyRecovery {
			return s, nil
		}
	case "1":
		if err := prepareV1Backup(path, raw); err != nil {
			return nil, fmt.Errorf("备份旧版密钥文件: %w", err)
		}
	case "2":
		// 早期 v2 已完成过迁移，补记首次 v1 备份的摘要。
		backup, err := os.ReadFile(path + ".v1.bak")
		if err == nil {
			var old file
			if err := json.Unmarshal(backup, &old); err != nil || string(old.Version) != "1" {
				return nil, fmt.Errorf("旧版密钥备份 %s.v1.bak 无法解析为 v1", path)
			}
			if err := publishBackup(path+".v1.bak.sha256", []byte(digest(string(backup)))); err != nil {
				return nil, fmt.Errorf("校验旧版密钥备份: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("读取旧版密钥备份: %w", err)
		}
	default:
		return nil, fmt.Errorf("密钥文件 %s 的版本不支持", path)
	}
	if legacyRecovery {
		err = s.publishRecovery(path)
	} else {
		err = s.saveLocked()
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

func recoveryPath(path, id string) string {
	// 旧面板能修改密钥数据，文件名只使用 ID 的摘要。
	return path + "." + digest(id) + ".v2"
}

func readRecovery(path string, legacy []*Key) (file, error) {
	var active file
	data, err := os.ReadFile(path)
	if err != nil {
		return active, fmt.Errorf("读取新版密钥库 %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &active); err != nil {
		return active, fmt.Errorf("解析新版密钥库 %s: %w", path, err)
	}
	if string(active.Version) != fileVersion {
		return active, fmt.Errorf("新版密钥库 %s 版本不支持", path)
	}
	// 旧面板新增的密钥保留 v1 权限。由调用方先发布合并后的新文件，再切换恢复入口。
	for _, added := range legacy {
		if added == nil || strings.HasPrefix(added.ID, recoveryIDPrefix) || slices.ContainsFunc(active.Keys, func(k *Key) bool {
			return k != nil && (k.ID == added.ID || k.Secret == added.Secret)
		}) {
			return active, fmt.Errorf("回退后的密钥与 %s 中的记录冲突", path)
		}
		active.Keys = append(active.Keys, added)
	}
	return active, nil
}

// 先完整发布 v2 数据再原子切换入口。两次发布之间退出时，旧版仍读本次升级前的活跃文件。
func (s *Store) publishRecovery(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	secret, err := newSecret()
	if err != nil {
		return err
	}
	marker := &Key{ID: recoveryIDPrefix + digest(secret), Name: "新版密钥库（回退保护）", Secret: secret, Note: "请保留此停用记录。恢复新版后会重新载入客户端密钥。", ExpiresAt: time.Unix(1, 0).UTC()}
	side := recoveryPath(path, marker.ID)
	raw, err := json.MarshalIndent(file{Version: json.RawMessage(fileVersion), Keys: s.keys}, "", "  ")
	if err != nil {
		return err
	}
	if err := publishBackup(side, raw); err != nil {
		return fmt.Errorf("发布新版密钥库: %w", err)
	}
	entry, err := json.MarshalIndent(file{Version: json.RawMessage("1"), Keys: []*Key{marker}}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(path, entry); err != nil {
		return fmt.Errorf("发布旧版恢复入口: %w", err)
	}
	if err := syncParent(path); err != nil {
		return fmt.Errorf("同步密钥恢复入口: %w", err)
	}
	s.path = side
	return nil
}

// 首次逐字节核对并封存原件。降级后旧版可以继续写入，新快照按摘要另存。
func prepareV1Backup(path string, raw []byte) error {
	first := path + ".v1.bak"
	receipt, err := os.ReadFile(first + ".sha256")
	if errors.Is(err, os.ErrNotExist) {
		if err := publishBackup(first, raw); err != nil {
			return err
		}
		return publishBackup(first+".sha256", []byte(digest(string(raw))))
	}
	if err != nil {
		return err
	}
	backup, err := os.ReadFile(first)
	if err != nil {
		return err
	}
	if digest(string(backup)) != string(receipt) {
		return fmt.Errorf("首次备份 %s 与校验记录不符", first)
	}
	if bytes.Equal(raw, backup) {
		return nil
	}
	return publishBackup(path+".v1."+digest(string(raw))+".bak", raw)
}

// 发布前同步完整内容，已有同名文件必须逐字节相同。
func publishBackup(path string, raw []byte) error {
	saved, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		tmp, err := os.CreateTemp(filepath.Dir(path), ".keys-v1-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err = tmp.Write(raw); err == nil {
			err = tmp.Sync()
		}
		closeErr := tmp.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		// 不覆盖另一迁移进程刚创建的备份。
		if err = publishNoReplace(tmp.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		saved, err = os.ReadFile(path)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if !bytes.Equal(saved, raw) {
		return fmt.Errorf("%s 与原 v1 文件不一致，已停止迁移", path)
	}
	return syncParent(path)
}

func syncParent(path string) error {
	if runtime.GOOS != "windows" {
		dir, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		defer dir.Close()
		return dir.Sync()
	}
	return nil
}

// Start 启动用量计数的防抖落盘，Stop 前一直运行。
func (s *Store) Start() {
	go func() {
		defer close(s.done)
		t := time.NewTicker(flushInterval)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				s.Flush()
				return
			case <-t.C:
				s.mu.Lock()
				if s.dirty {
					s.saveLocked()
				}
				s.mu.Unlock()
			}
		}
	}()
}

// Stop 停止后台落盘并做最后一次写盘。只能在 Start 之后调用。
func (s *Store) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
}

// Flush 立即把用量计数写盘。
func (s *Store) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveLocked()
}

// Any 有没有建过客户端密钥（停用的也算）。
func (s *Store) Any() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys) > 0
}

// List 全部密钥的副本，按创建顺序。Today 换算到 now 所在的北京时间日期：过了日切还没用过的密钥
// 今天的用量是 0。
func (s *Store) List(now time.Time) []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := cstDay(now)
	out := make([]Key, 0, len(s.keys))
	for _, k := range s.keys {
		c := cloneKey(k)
		if c.Day != day {
			c.Today, c.Day = Usage{}, day
		}
		out = append(out, c)
	}
	return out
}

// Get 按 id 取密钥副本（Today 同 List）。
func (s *Store) Get(id string, now time.Time) (Key, bool) {
	for _, k := range s.List(now) {
		if k.ID == id {
			return k, true
		}
	}
	return Key{}, false
}

// Lookup 按密钥原文查。
func (s *Store) Lookup(secret string) (Key, bool) {
	if s == nil || secret == "" {
		return Key{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.bySecret[digest(secret)]
	if k == nil {
		return Key{}, false
	}
	return cloneKey(k), true
}

// Create 新建密钥，随机生成 id 和原文。
func (s *Store) Create(in Input, now time.Time) (Key, error) {
	in, err := normalize(in)
	if err != nil {
		return Key{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.uniqueNameLocked(in.Name, ""); err != nil {
		return Key{}, err
	}
	id, err := s.newIDLocked()
	if err != nil {
		return Key{}, err
	}
	secret, err := newSecret()
	if err != nil {
		return Key{}, err
	}
	k := &Key{ID: id, Secret: secret, CreatedAt: now, Day: cstDay(now)}
	apply(k, in)
	s.keys = append(s.keys, k)
	s.bySecret[digest(secret)] = k
	if err := s.saveLocked(); err != nil {
		s.keys = s.keys[:len(s.keys)-1]
		delete(s.bySecret, digest(secret))
		return Key{}, err
	}
	return cloneKey(k), nil
}

// Update 改密钥的设置，原文和用量不变。
func (s *Store) Update(id string, in Input) (Key, error) {
	in, err := normalize(in)
	if err != nil {
		return Key{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.findLocked(id)
	if k == nil {
		return Key{}, ErrNotFound
	}
	if err := s.uniqueNameLocked(in.Name, id); err != nil {
		return Key{}, err
	}
	old := cloneKey(k)
	apply(k, in)
	if err := s.saveLocked(); err != nil {
		*k = old
		return Key{}, err
	}
	return cloneKey(k), nil
}

// Regenerate 换一个新原文，旧原文立即失效；设置和用量不变。
func (s *Store) Regenerate(id string) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.findLocked(id)
	if k == nil {
		return Key{}, ErrNotFound
	}
	secret, err := newSecret()
	if err != nil {
		return Key{}, err
	}
	old := k.Secret
	k.Secret = secret
	delete(s.bySecret, digest(old))
	s.bySecret[digest(secret)] = k
	if err := s.saveLocked(); err != nil {
		k.Secret = old
		delete(s.bySecret, digest(secret))
		s.bySecret[digest(old)] = k
		return Key{}, err
	}
	return cloneKey(k), nil
}

// Delete 删除密钥，原文立即失效。用量统计里的历史数据保留。
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.keys, func(k *Key) bool { return k.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	k := s.keys[i]
	s.keys = slices.Delete(s.keys, i, i+1)
	delete(s.bySecret, digest(k.Secret))
	if err := s.saveLocked(); err != nil {
		s.keys = slices.Insert(s.keys, i, k)
		s.bySecret[digest(k.Secret)] = k
		return err
	}
	return nil
}

// Check 密钥能不能用：删了、停用、过期都不行。不看用量上限（见 Admit）。
func (s *Store) Check(id string, now time.Time) *Denied {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkLocked(s.findLocked(id), now)
}

// Admit 放行一个对话请求：先查 Check 的几项，再查每天和总量的上限，都没到就计一次请求、记下
// 最后使用时间。token、积分要等上游回了才知道（见 Record），所以并发的请求可能让用量略超上限。
func (s *Store) Admit(id string, now time.Time) *Denied {
	return s.AdmitTarget(id, "workbuddy:cn", "", now)
}

// AdmitTarget 在同一锁内检查平台权限、模型白名单与用量，避免设置变更绕过限制。
func (s *Store) AdmitTarget(id, target, model string, now time.Time) *Denied {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.findLocked(id)
	if d := s.checkLocked(k, now); d != nil {
		return d
	}
	if !k.AllowsTarget(target) {
		return &Denied{Status: 403, Code: "target_not_allowed", Message: "密钥不允许使用该上游"}
	}
	if model != "" && !k.AllowsTargetModel(target, model) {
		return &Denied{Status: 403, Code: "model_not_allowed", Message: fmt.Sprintf("密钥「%s」不能用模型 %s，允许的模型：%s", k.Name, model, strings.Join(k.Models, "、"))}
	}
	rollDay(k, now)
	daily, total := k.DailyLimit, k.TotalLimit
	if !targets.WorkBuddy(target) {
		daily.Credits = 0
		total.Credits = 0
	}
	if what, ok := over(k.Today, daily); ok {
		return &Denied{
			Status: http.StatusTooManyRequests, Code: "key_quota_exceeded",
			Message:    fmt.Sprintf("密钥「%s」今天的用量已到上限（%s），北京时间 00:00 重置", k.Name, what),
			RetryAfter: nextCSTMidnight(now).Sub(now),
		}
	}
	if what, ok := over(k.Used, total); ok {
		return &Denied{
			Status: http.StatusTooManyRequests, Code: "key_quota_exceeded",
			Message: fmt.Sprintf("密钥「%s」的总用量已到上限（%s），请联系管理员调高", k.Name, what),
		}
	}
	k.Today.Requests++
	k.Used.Requests++
	k.LastUsedAt = now
	s.dirty = true
	return nil
}

// Record 记上一次上游回复的 token 和积分。hasCredit=false（上游没回积分）时不计积分。
func (s *Store) Record(id string, now time.Time, tokens int64, credits float64, hasCredit bool) {
	if tokens <= 0 && (!hasCredit || credits <= 0) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.findLocked(id)
	if k == nil {
		return
	}
	rollDay(k, now)
	if tokens > 0 {
		k.Today.Tokens += tokens
		k.Used.Tokens += tokens
	}
	if hasCredit && credits > 0 {
		k.Today.Credits += credits
		k.Used.Credits += credits
	}
	s.dirty = true
}

// Refund 退还 Admit 计的那一次请求：请求最后失败了（没号可用、上游报错）就不算次数，免得号池出问题时
// 客户端自动重试把每天的次数烧光。admitted 是 Admit 时的时刻：过了北京时间日切就只退累计，不动新一天的计数。
func (s *Store) Refund(id string, admitted time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.findLocked(id)
	if k == nil {
		return
	}
	if k.Used.Requests > 0 {
		k.Used.Requests--
	}
	if k.Day == cstDay(admitted) && k.Today.Requests > 0 {
		k.Today.Requests--
	}
	s.dirty = true
}

func (s *Store) checkLocked(k *Key, now time.Time) *Denied {
	switch {
	case k == nil:
		return &Denied{Status: http.StatusUnauthorized, Code: "key_deleted", Message: "密钥已被删除"}
	case !k.Enabled:
		return &Denied{Status: http.StatusUnauthorized, Code: "key_disabled", Message: fmt.Sprintf("密钥「%s」已停用", k.Name)}
	case k.Expired(now):
		return &Denied{Status: http.StatusUnauthorized, Code: "key_expired",
			Message: fmt.Sprintf("密钥「%s」已于北京时间 %s 到期", k.Name, k.ExpiresAt.In(cst).Format("2006-01-02 15:04"))}
	}
	return nil
}

func (s *Store) findLocked(id string) *Key {
	for _, k := range s.keys {
		if k.ID == id {
			return k
		}
	}
	return nil
}

func (s *Store) uniqueNameLocked(name, selfID string) error {
	for _, k := range s.keys {
		if k.ID != selfID && strings.EqualFold(k.Name, name) {
			return fmt.Errorf("已经有叫「%s」的密钥了", name)
		}
	}
	return nil
}

func (s *Store) newIDLocked() (string, error) {
	for range 8 {
		var b [4]byte
		if _, err := randRead(b[:]); err != nil {
			return "", fmt.Errorf("生成密钥 id: %w", err)
		}
		id := "k" + hex.EncodeToString(b[:])
		if s.findLocked(id) == nil {
			return id, nil
		}
	}
	return "", errors.New("生成密钥 id: 连续撞号")
}

// normalize 整理并校验输入：去掉首尾空白，白名单去空行、去重。
func normalize(in Input) (Input, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Note = strings.TrimSpace(in.Note)
	switch {
	case in.Name == "":
		return in, errors.New("密钥名称不能为空")
	case utf8.RuneCountInString(in.Name) > maxName:
		return in, fmt.Errorf("密钥名称最多 %d 个字", maxName)
	case utf8.RuneCountInString(in.Note) > maxNote:
		return in, fmt.Errorf("备注最多 %d 个字", maxNote)
	}
	for _, l := range []Limits{in.DailyLimit, in.TotalLimit} {
		if l.Requests < 0 || l.Tokens < 0 || l.Credits < 0 || math.IsNaN(l.Credits) || math.IsInf(l.Credits, 0) {
			return in, errors.New("用量上限不能是负数")
		}
	}
	var models []string
	for _, m := range in.Models {
		m = strings.TrimSpace(m)
		switch {
		case m == "":
			continue
		case strings.Contains(m, ":") && !validQualifiedPattern(m):
			return in, fmt.Errorf("模型白名单按模型名匹配，不带 cn: / global: 前缀：%s", m)
		case len(m) > maxPattern:
			return in, fmt.Errorf("模型白名单的一项最长 %d 个字符", maxPattern)
		}
		if !slices.ContainsFunc(models, func(x string) bool { return strings.EqualFold(x, m) }) {
			models = append(models, m)
		}
	}
	if len(models) > maxModels {
		return in, fmt.Errorf("模型白名单最多 %d 项", maxModels)
	}
	in.Models = models
	var groups []string
	for _, g := range in.Groups {
		if g = strings.TrimSpace(g); g != "" && !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	in.Groups = groups
	var allowed []string
	for _, t := range in.AllowedTargets {
		if !targets.Valid(t) {
			return in, fmt.Errorf("未知上游：%s", t)
		}
		if !slices.Contains(allowed, t) {
			allowed = append(allowed, t)
		}
	}
	in.AllowedTargets = allowed
	if in.DefaultTarget != "" && (!targets.Valid(in.DefaultTarget) || !allowsTarget(allowed, in.DefaultTarget)) {
		return in, errors.New("默认上游必须在允许的上游中")
	}
	return in, nil
}

func apply(k *Key, in Input) {
	k.Name, k.Note, k.Enabled = in.Name, in.Note, in.Enabled
	k.ExpiresAt = in.ExpiresAt
	k.DailyLimit, k.TotalLimit = in.DailyLimit, in.TotalLimit
	k.Models = in.Models
	k.Groups = in.Groups
	k.AllowedTargets = in.AllowedTargets
	k.DefaultTarget = in.DefaultTarget
}

// saveLocked 写盘（原子替换）。成功后清掉 dirty；失败只记日志并返回错误，计数留到下次再写。
func (s *Store) saveLocked() error {
	if s.path == "" {
		s.dirty = false
		return nil
	}
	raw, _ := json.MarshalIndent(file{Version: json.RawMessage(fileVersion), Keys: s.keys}, "", "  ") // 字段都是可序列化的基本类型
	if err := writeAtomic(s.path, raw); err != nil {
		log.Printf("[apikeys] 写 %s 失败: %v", s.path, err)
		return fmt.Errorf("%w：%v", ErrSave, err)
	}
	s.dirty = false
	return nil
}

// writeAtomic 先写同目录的临时文件再改名，权限 0600（文件里有密钥原文）。
func writeAtomic(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// over 用量是否已到某项上限，到了就返回是哪一项和上限值，如「积分 100」。
func over(u Usage, l Limits) (string, bool) {
	switch {
	case l.Requests > 0 && u.Requests >= l.Requests:
		return "请求次数 " + strconv.FormatInt(l.Requests, 10), true
	case l.Tokens > 0 && u.Tokens >= l.Tokens:
		return "token " + strconv.FormatInt(l.Tokens, 10), true
	case l.Credits > 0 && u.Credits >= l.Credits:
		return "积分 " + strconv.FormatFloat(l.Credits, 'f', -1, 64), true
	}
	return "", false
}

// rollDay 过了北京时间日切就把今天的用量清零。
func rollDay(k *Key, now time.Time) {
	if day := cstDay(now); k.Day != day {
		k.Today, k.Day = Usage{}, day
	}
}

func cstDay(t time.Time) string { return t.In(cst).Format("2006-01-02") }

func nextCSTMidnight(t time.Time) time.Time {
	c := t.In(cst)
	return time.Date(c.Year(), c.Month(), c.Day()+1, 0, 0, 0, 0, cst)
}

func cloneKey(k *Key) Key {
	c := *k
	c.Models = slices.Clone(k.Models)
	c.Groups = slices.Clone(k.Groups)
	c.AllowedTargets = slices.Clone(k.AllowedTargets)
	return c
}

// UsingGroup 限定了这个账号分组的密钥的名字，删分组前查。
func (s *Store) UsingGroup(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for _, k := range s.keys {
		if slices.Contains(k.Groups, id) {
			names = append(names, k.Name)
		}
	}
	return names
}

// newSecret sk- 开头的 32 字节随机串，和首次运行生成的管理员密钥同一格式。
func newSecret() (string, error) {
	raw := make([]byte, 24)
	if _, err := randRead(raw); err != nil {
		return "", fmt.Errorf("生成密钥: %w", err)
	}
	return "sk-" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// AllowsTarget 旧密钥缺少平台字段时只放行 WorkBuddy，新增账号不会扩大历史权限。
func (k Key) AllowsTarget(target string) bool { return allowsTarget(k.AllowedTargets, target) }
func allowsTarget(allowed []string, target string) bool {
	if len(allowed) == 0 {
		return targets.WorkBuddy(target)
	}
	return slices.Contains(allowed, target)
}
func validQualifiedPattern(pattern string) bool {
	target, native, explicit := targets.Parse(pattern)
	return explicit && !targets.WorkBuddy(target) && native != ""
}

// AllowsTargetModel 新平台只用完整模型名匹配，裸白名单仍属于 WorkBuddy。
func (k Key) AllowsTargetModel(target, model string) bool {
	if !k.AllowsTarget(target) {
		return false
	}
	if targets.WorkBuddy(target) {
		return k.AllowsModel(model)
	}
	if len(k.Models) == 0 {
		return true
	}
	for _, p := range k.Models {
		if validQualifiedPattern(p) && glob.Match(p, targets.Public(target, model)) {
			return true
		}
	}
	return false
}
