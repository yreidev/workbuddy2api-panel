// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
)

type fakeAdapter struct {
	refreshed  atomic.Int32
	chat       func(context.Context, string, Credential, string, func(map[string]any) error) error
	refresh    func(context.Context, string, Credential) (Credential, error)
	refreshErr error
}

func (f *fakeAdapter) Refresh(ctx context.Context, target string, c Credential) (Credential, error) {
	f.refreshed.Add(1)
	if f.refresh != nil {
		return f.refresh(ctx, target, c)
	}
	if f.refreshErr != nil {
		return c, f.refreshErr
	}
	c.AccessToken = "rotated"
	c.RefreshToken = "rotated-refresh"
	c.ExpiresAt = time.Now().Add(time.Hour).Unix()
	return c, nil
}
func (*fakeAdapter) Models(context.Context, string, Credential) ([]Model, error) {
	return []Model{{ID: "model", Tools: true}}, nil
}
func (f *fakeAdapter) Chat(ctx context.Context, target string, c Credential, _ Model, _ []byte, session string, emit func(map[string]any) error) error {
	if f.chat != nil {
		return f.chat(ctx, target, c, session, emit)
	}
	if err := emit(chunk("model", map[string]any{"content": "hello"}, nil, nil)); err != nil {
		return err
	}
	return emit(chunk("model", map[string]any{}, "stop", map[string]any{"prompt_tokens": float64(2), "completion_tokens": float64(3)}))
}
func testManager(t *testing.T, path string) (*Manager, *fakeAdapter) {
	t.Helper()
	m, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAdapter{}
	m.adapters["qoder"] = f
	m.adapters["traecode"] = f
	m.adapters["traework"] = f
	return m, f
}
func importFake(t *testing.T, m *Manager, target, uid string, groups ...string) string {
	t.Helper()
	id, err := m.Import(context.Background(), AccountInput{Target: target, Groups: groups, Credential: Credential{UID: uid, UserType: "teams", AccessToken: "token", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

const plainChat = `{"model":"model","messages":[{"role":"user","content":"hello"}]}`

func discardFrame(map[string]any) error { return nil }

func TestIdentityAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	m, _ := testManager(t, path)
	code := importFake(t, m, "traecode:cn", "same")
	if work := importFake(t, m, "traework:cn", "same"); work != code {
		t.Fatal("Trae 通道没有共享稳定账号身份")
	}
	cn := importFake(t, m, "qoder:cn", "same")
	global := importFake(t, m, "qoder:global", "same")
	if cn == global || cn == code || global == code {
		t.Fatal("不同平台或地区账号相撞")
	}
	raw, err := json.Marshal(m.List())
	if err != nil || strings.Contains(string(raw), "refresh") || strings.Contains(string(raw), "access_token") {
		t.Fatalf("面板泄露凭证: %s %v", raw, err)
	}
	reopened, err := Open(path, nil)
	if err != nil || len(reopened.List()) != 3 {
		t.Fatalf("账号持久化: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("凭证文件权限错误", err)
	}
	if _, err := m.Models(context.Background(), code, "qoder:cn", false); err == nil {
		t.Fatal("账号目录跨平台读取")
	}
}
func TestRefreshSingleFlightAndRecoveryGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	m, f := testManager(t, path)
	id := importFake(t, m, "qoder:cn", "one")
	m.mu.Lock()
	m.data.Accounts[0].Credential.ExpiresAt = 1
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := m.credential(context.Background(), id, "qoder:cn"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if f.refreshed.Load() != 1 {
		t.Fatalf("重复刷新 %d", f.refreshed.Load())
	}
	reopened, err := Open(path, nil)
	if err != nil || reopened.data.Accounts[0].Credential.RefreshToken != "rotated-refresh" {
		t.Fatalf("新刷新令牌未落盘: %v", err)
	}
	m.mu.Lock()
	m.data.Accounts[0].Credential.ExpiresAt = 1
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	f.refreshErr = errors.New("connection lost after rotation")
	if _, err := m.credential(context.Background(), id, "qoder:cn"); err == nil {
		t.Fatal("刷新错误被吞")
	}
	reopened, err = Open(path, nil)
	if err != nil || !reopened.List()[0].Disabled {
		t.Fatalf("重启复用了未确定状态的旧令牌: %v", err)
	}
	f.refreshErr = nil
	importFake(t, m, "qoder:cn", "one")
	if m.List()[0].Disabled {
		t.Fatal("重新导入后仍无法恢复")
	}
}

func TestRefreshRejectsChangedIdentity(t *testing.T) {
	for _, target := range []string{"traework:cn", "traecode:cn", "qoder:cn", "qoder:global"} {
		for _, uid := range []string{"foreign", ""} {
			t.Run(target+"/uid="+uid, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "providers.json")
				manager, adapter := testManager(t, path)
				id := importFake(t, manager, target, "one")
				manager.data.Accounts[0].Credential.ExpiresAt = 1
				if err := manager.saveLocked(); err != nil {
					t.Fatal(err)
				}
				original := manager.data.Accounts[0].Credential
				originalFile, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				adapter.refresh = func(_ context.Context, _ string, c Credential) (Credential, error) {
					c.UID = uid
					c.AccessToken, c.RefreshToken = "rotated", "rotated-refresh"
					c.ExpiresAt = time.Now().Add(time.Hour).Unix()
					return c, nil
				}
				if _, err := manager.credential(context.Background(), id, target); err == nil || !strings.Contains(err.Error(), "身份") {
					t.Fatal("刷新未拒绝账号身份变化或缺失", err)
				}
				if manager.data.Accounts[0].Credential != original || !manager.List()[0].Disabled || !strings.Contains(manager.List()[0].Error, "身份") {
					t.Fatal("身份异常后覆盖了原凭证，或没有停止派发并说明原因")
				}
				currentFile, err := os.ReadFile(path)
				if err != nil || string(currentFile) != string(originalFile) {
					t.Fatal("身份异常的刷新修改了账号文件", err)
				}
				if _, err := manager.credential(context.Background(), id, target); err == nil || adapter.refreshed.Load() != 1 {
					t.Fatal("账号受保护后再次重放刷新令牌")
				}
				restarted, err := Open(path, nil)
				if err != nil || !restarted.List()[0].Disabled {
					t.Fatal("身份异常导致无法重启或丢失刷新保护", err)
				}
				adapter.refreshed.Store(0)
				restarted.adapters[strings.Split(target, ":")[0]] = adapter
				if _, err := restarted.credential(context.Background(), id, target); err == nil || adapter.refreshed.Load() != 0 {
					t.Fatal("重启后复用了不确定刷新令牌")
				}
				original.AccessToken, original.RefreshToken = "new-login", "new-login-refresh"
				original.ExpiresAt = time.Now().Add(time.Hour).Unix()
				if _, err := restarted.Import(context.Background(), AccountInput{Target: target, Credential: original}); err != nil || restarted.List()[0].Disabled {
					t.Fatal("有效新凭证无法恢复账号", err)
				}
				if _, err := os.Stat(restarted.guardPath(id)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("恢复后没有清理刷新标记", err)
				}
			})
		}
	}
}

func TestCanceledCredentialDoesNotRotate(t *testing.T) {
	manager, adapter := testManager(t, filepath.Join(t.TempDir(), "providers.json"))
	id := importFake(t, manager, "qoder:cn", "one")
	manager.data.Accounts[0].Credential.ExpiresAt = 1
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.credential(ctx, id, "qoder:cn"); !errors.Is(err, context.Canceled) {
		t.Fatal("已取消的调用没有立即返回", err)
	}
	if adapter.refreshed.Load() != 0 || manager.List()[0].Disabled {
		t.Fatal("取消后仍刷新令牌或停止派发账号")
	}
	if _, err := os.Stat(manager.guardPath(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("取消后仍写入刷新标记", err)
	}
	if result, err := manager.Execute(ctx, Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame); !errors.Is(err, context.Canceled) || result.Attempts != 0 {
		t.Fatal("取消后仍选号并记为出站尝试", result, err)
	}
	if len(manager.Usage()) != 0 || manager.List()[0].InFlight != 0 {
		t.Fatal("取消后仍记账或占用账号")
	}
}

func TestImportDoesNotRotateBusyAccount(t *testing.T) {
	for _, busy := range []string{"对话中", "刷新中"} {
		t.Run(busy, func(t *testing.T) {
			manager, adapter := testManager(t, filepath.Join(t.TempDir(), "providers.json"))
			id := importFake(t, manager, "qoder:cn", "one")
			credential := manager.data.Accounts[0].Credential
			credential.ExpiresAt = 1
			started, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			wantRefreshes := int32(0)
			if busy == "对话中" {
				adapter.chat = func(_ context.Context, _ string, _ Credential, _ string, emit func(map[string]any) error) error {
					close(started)
					<-release
					return emit(chunk("model", map[string]any{"content": "ok"}, "stop", nil))
				}
				go func() {
					_, err := manager.Execute(context.Background(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame)
					done <- err
				}()
			} else {
				wantRefreshes = 1
				manager.data.Accounts[0].Credential = credential
				adapter.refresh = func(_ context.Context, _ string, c Credential) (Credential, error) {
					if adapter.refreshed.Load() > 1 {
						return c, errors.New("重复消费刷新令牌")
					}
					close(started)
					<-release
					c.AccessToken, c.RefreshToken = "rotated", "rotated-refresh"
					c.ExpiresAt = time.Now().Add(time.Hour).Unix()
					return c, nil
				}
				go func() {
					_, err := manager.credential(context.Background(), id, "qoder:cn")
					done <- err
				}()
			}
			t.Cleanup(func() {
				close(release)
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			<-started
			if _, err := manager.Import(context.Background(), AccountInput{Target: "qoder:cn", Credential: credential}); err == nil {
				t.Fatal("账号忙时仍允许重新导入")
			}
			if got := adapter.refreshed.Load(); got != wantRefreshes {
				t.Fatalf("导入拒绝前消费了刷新令牌：刷新 %d 次，应为 %d 次", got, wantRefreshes)
			}
		})
	}
}

func TestImportRefreshGuardAndRollback(t *testing.T) {
	for _, outcome := range []string{"正常保存", "刷新失败", "保存失败", "身份变化", "拨号失败"} {
		t.Run(outcome, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "providers.json")
			manager, adapter := testManager(t, path)
			id := importFake(t, manager, "qoder:cn", "one", "original")
			if err := manager.Update(id, AccountInput{Name: "原有设置", Priority: 7, Groups: []string{"original"}}, false); err != nil {
				t.Fatal(err)
			}
			original := manager.data.Accounts[0].Credential
			credential := original
			credential.ExpiresAt = 1
			credential.UID = "" // 按相同凭证定位已有账号，也必须提前占住刷新锁。
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			adapter.refresh = func(_ context.Context, _ string, c Credential) (Credential, error) {
				info, err := os.Stat(manager.guardPath(id))
				if err != nil || info.Mode().Perm() != 0600 {
					t.Error("发出刷新请求前没有安全写入刷新标记", err)
				}
				close(started)
				<-release
				if outcome == "刷新失败" {
					return c, errors.New("兑换后连接中断")
				}
				if outcome == "拨号失败" {
					return c, &upstream.RouteError{Err: errors.New("CONNECT 被拒")}
				}
				c.AccessToken, c.RefreshToken = "rotated", "rotated-refresh"
				c.ExpiresAt = time.Now().Add(time.Hour).Unix()
				if outcome == "身份变化" {
					c.UID = "foreign"
				}
				return c, nil
			}
			var importedID string
			var importErr error
			go func() {
				defer close(done)
				importedID, importErr = manager.Import(context.Background(), AccountInput{Target: "qoder:cn", Credential: credential})
			}()
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
				<-done
			})
			<-started
			if err := manager.Update(id, AccountInput{}, true); err == nil {
				t.Fatal("导入刷新中仍能删除账号")
			}
			if _, err := manager.Models(context.Background(), id, "qoder:cn", true); err == nil {
				t.Fatal("导入刷新中仍能发起目录请求")
			}
			if _, err := manager.Import(context.Background(), AccountInput{Target: "qoder:cn", Credential: credential}); err == nil || adapter.refreshed.Load() != 1 {
				t.Fatal("并发导入重复消费刷新令牌")
			}
			restarted, err := Open(path, nil)
			if err != nil || !restarted.List()[0].Disabled {
				t.Fatal("导入刷新中重启仍派发旧凭证", err)
			}
			var originalFile []byte
			if outcome == "保存失败" {
				originalFile, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			<-done
			if manager.List()[0].InFlight != 0 {
				t.Fatal("导入结束后没有释放账号槽位")
			}
			if outcome == "正常保存" {
				if importErr != nil || importedID != id {
					t.Fatal("导入刷新未保存到原账号", importedID, importErr)
				}
				account := manager.data.Accounts[0]
				if account.Name != "原有设置" || account.Priority != 7 || len(account.Groups) != 1 || account.Groups[0] != "original" {
					t.Fatal("重新导入覆盖了原有账号设置")
				}
				if _, err := os.Stat(manager.guardPath(id)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("安全保存后没有清理刷新标记", err)
				}
				restarted, err := Open(path, nil)
				if err != nil || restarted.List()[0].Disabled || restarted.data.Accounts[0].Credential.RefreshToken != "rotated-refresh" {
					t.Fatal("刷新后的凭证没有持久化", err)
				}
				return
			}
			if outcome == "拨号失败" {
				restarted, err := Open(path, nil)
				if !upstream.IsRouteError(importErr) || err != nil || manager.List()[0].Disabled || restarted.List()[0].Disabled ||
					restarted.data.Accounts[0].Credential != original {
					t.Fatalf("未发出刷新请求时未恢复原账号：import=%v open=%v", importErr, err)
				}
				return
			}
			if importErr == nil || !manager.List()[0].Disabled || manager.data.Accounts[0].Credential != original {
				t.Fatal("导入失败后复用了不确定凭证或覆盖了原账号", importErr)
			}
			if outcome == "保存失败" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, originalFile, 0600); err != nil {
					t.Fatal(err)
				}
			}
			restarted, err = Open(path, nil)
			if err != nil || !restarted.List()[0].Disabled {
				t.Fatal("失败后重启丢失刷新保护", err)
			}
			if _, err := manager.Import(context.Background(), AccountInput{Target: "qoder:cn", Credential: credential}); err == nil || adapter.refreshed.Load() != 1 {
				t.Fatal("账号已受保护时仍重放旧刷新令牌")
			}
			original.AccessToken, original.RefreshToken = "new-login", "new-login-refresh"
			if _, err := manager.Import(context.Background(), AccountInput{Target: "qoder:cn", Credential: original}); err != nil || manager.List()[0].Disabled {
				t.Fatal("有效新凭证无法恢复账号", err)
			}
		})
	}
}
func TestExecuteScopeRetryAndUsage(t *testing.T) {
	m, f := testManager(t, "")
	denied := importFake(t, m, "qoder:cn", "outside", "other")
	first := importFake(t, m, "qoder:cn", "first", "allowed")
	second := importFake(t, m, "qoder:cn", "second", "allowed")
	importFake(t, m, "qoder:global", "foreign", "allowed")
	var attempted []string
	f.chat = func(_ context.Context, target string, c Credential, _ string, emit func(map[string]any) error) error {
		if target != "qoder:cn" {
			t.Error("重试跨地区")
		}
		attempted = append(attempted, c.UID)
		if c.UID == "first" {
			return &Error{Status: 429, Code: "limited", Retry: true}
		}
		return emit(chunk("model", map[string]any{"content": "ok"}, "stop", nil))
	}
	result, err := m.Execute(context.Background(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat), KeyID: "key", Session: "session", Allow: func(_ string, groups []string) bool { return len(groups) == 1 && groups[0] == "allowed" }}, discardFrame)
	if err != nil || result.AccountID != second || result.Attempts != 2 || strings.Join(attempted, ",") != "first,second" {
		t.Fatalf("选号错误 first=%s denied=%s result=%+v calls=%v err=%v", first, denied, result, attempted, err)
	}
	if len(m.Usage()) != 2 || m.Usage()[0].Failed != 1 || m.Usage()[1].ChargeKnown != 0 || m.Usage()[1].TokensKnown != 0 {
		t.Fatal("失败或未知用量记账错误", m.Usage())
	}
}
func TestPartialStreamNeverRetries(t *testing.T) {
	for _, failure := range []error{
		&Error{Status: 502, Code: "broken", Retry: true},
		&upstream.RouteError{Err: errors.New("CONNECT 被拒")},
	} {
		m, f := testManager(t, "")
		importFake(t, m, "qoder:cn", "one")
		importFake(t, m, "qoder:cn", "two")
		var calls int
		f.chat = func(_ context.Context, _ string, _ Credential, _ string, emit func(map[string]any) error) error {
			calls++
			if err := emit(chunk("model", map[string]any{"content": "partial"}, nil, nil)); err != nil {
				return err
			}
			return failure
		}
		result, err := m.Execute(context.Background(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame)
		if err == nil || calls != 1 || !result.Started {
			t.Fatalf("部分输出后重试: %+v %v calls=%d", result, err, calls)
		}
	}
}
func TestQueueCancelAndDynamicPermission(t *testing.T) {
	m, f := testManager(t, "")
	id := importFake(t, m, "qoder:cn", "one")
	started := make(chan struct{})
	release := make(chan struct{})
	f.chat = func(ctx context.Context, _ string, _ Credential, _ string, emit func(map[string]any) error) error {
		close(started)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return emit(chunk("model", map[string]any{"content": "ok"}, "stop", nil))
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := m.Execute(context.Background(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame)
		done <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Execute(ctx, Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame); !errors.Is(err, context.Canceled) {
		t.Fatal("排队取消未生效", err)
	}
	if err := m.Update(id, AccountInput{}, true); err == nil {
		t.Fatal("删除在途账号")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if m.List()[0].InFlight != 0 {
		t.Fatal("请求槽位未释放")
	}
	if _, err := m.Execute(context.Background(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat), Allow: func(string, []string) bool { return false }}, discardFrame); err == nil {
		t.Fatal("组外账号获准执行")
	}
}
func TestSessionNamespacedAndRestarted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	m, f := testManager(t, path)
	importFake(t, m, "qoder:cn", "one")
	importFake(t, m, "qoder:global", "one")
	sessions := []string{}
	f.chat = func(_ context.Context, _ string, _ Credential, session string, emit func(map[string]any) error) error {
		sessions = append(sessions, session)
		return emit(chunk("model", map[string]any{"content": "ok"}, "stop", nil))
	}
	run := func(manager *Manager, key, target string) {
		t.Helper()
		_, err := manager.Execute(context.Background(), Request{Target: target, Model: "model", Body: []byte(plainChat), Session: "same", KeyID: key}, discardFrame)
		if err != nil {
			t.Fatal(err)
		}
	}
	run(m, "one", "qoder:cn")
	run(m, "one", "qoder:cn")
	run(m, "two", "qoder:cn")
	run(m, "one", "qoder:global")
	reopened, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	reopened.adapters["qoder"] = f
	run(reopened, "one", "qoder:cn")
	if sessions[0] != sessions[1] || sessions[0] != sessions[4] || sessions[0] == sessions[2] || sessions[0] == sessions[3] {
		t.Fatal("会话跨密钥或平台串用", sessions)
	}
}

func TestExpiredSessionRespectsPriority(t *testing.T) {
	m, _ := testManager(t, "")
	bound := importFake(t, m, "qoder:cn", "bound")
	req := Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat), Session: "session", KeyID: "key"}
	if _, err := m.Execute(context.Background(), req, discardFrame); err != nil {
		t.Fatal(err)
	}
	preferred := importFake(t, m, "qoder:cn", "preferred")
	if err := m.Update(bound, AccountInput{Priority: 9}, false); err != nil {
		t.Fatal(err)
	}
	result, err := m.Execute(context.Background(), req, discardFrame)
	if err != nil || result.AccountID != bound {
		t.Fatalf("有效会话未保持粘性: %+v %v", result, err)
	}
	m.mu.Lock()
	for key, binding := range m.data.Bindings {
		binding.ExpiresAt = time.Now().Add(-time.Second)
		m.data.Bindings[key] = binding
	}
	m.mu.Unlock()
	result, err = m.Execute(context.Background(), req, discardFrame)
	if err != nil || result.AccountID != preferred {
		t.Fatalf("过期会话覆盖账号优先级: %+v %v", result, err)
	}
}

func TestDeletePersistsSessionCleanupAndRollsBackOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	m, _ := testManager(t, path)
	id := importFake(t, m, "qoder:cn", "one")
	if _, err := m.Execute(context.Background(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat), Session: "session"}, discardFrame); err != nil {
		t.Fatal(err)
	}
	m.path = t.TempDir()
	if err := m.Update(id, AccountInput{}, true); err == nil {
		t.Fatal("删除账号写盘失败被忽略")
	}
	if len(m.List()) != 1 || len(m.data.Bindings) != 1 || len(m.running) != 1 {
		t.Fatal("删除失败未回滚账号或会话")
	}
	m.path = path
	if err := m.Update(id, AccountInput{}, true); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.List()) != 0 || len(reopened.data.Bindings) != 0 || len(reopened.Usage()) != 1 {
		t.Fatal("账号删除未持久清理会话，或误删历史用量")
	}
}

func TestUnsupportedContentAndWorkTools(t *testing.T) {
	for _, body := range []string{`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://invalid.test/image"}}]}]}`, `{"messages":[{"role":"tool","content":"result"}]}`} {
		if err := validateChat("traework:cn", []byte(body)); err == nil {
			t.Fatal("不支持的输入被静默丢弃", body)
		}
	}
}

func TestProxyRequirementStopsEveryOperation(t *testing.T) {
	manager, _ := testManager(t, "")
	id := importFake(t, manager, "qoder:cn", "one")
	registry, err := proxies.Open(filepath.Join(t.TempDir(), "proxies.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.UpdateSettings(proxies.Settings{RequireBound: true}); err != nil {
		t.Fatal(err)
	}
	manager.SetProxies(registry)
	if _, err := manager.Models(context.Background(), id, "qoder:cn", true); err == nil {
		t.Fatal("必须绑定代理时目录仍出站")
	}
	if _, err := manager.Execute(context.Background(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame); err == nil {
		t.Fatal("必须绑定代理时推理仍出站")
	}
	if _, err := manager.StartLogin("qoder:cn", ""); err == nil {
		t.Fatal("必须绑定代理时登录仍出站")
	}
	if _, err := manager.Import(context.Background(), AccountInput{Target: "qoder:cn", Credential: Credential{UID: "two", UserType: "teams", AccessToken: "token"}}); err == nil {
		t.Fatal("必须绑定代理时导入绕过策略")
	}
}

func TestInvalidUsageCannotReduceTokenLimits(t *testing.T) {
	manager, adapter := testManager(t, "")
	importFake(t, manager, "qoder:cn", "one")
	adapter.chat = func(_ context.Context, _ string, _ Credential, _ string, emit func(map[string]any) error) error {
		return emit(chunk("model", map[string]any{}, "stop", map[string]any{"prompt_tokens": float64(-10), "completion_tokens": float64(2)}))
	}
	result, err := manager.Execute(context.Background(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame)
	if err == nil || result.TokensKnown || manager.Usage()[0].TokensKnown != 0 {
		t.Fatal("非法 token 用量被计入额度", result, err)
	}
}

func TestOpenRejectsMixedIdentityAndInvalidState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	manager, _ := testManager(t, path)
	importFake(t, manager, "qoder:cn", "fixture")
	manager.mu.Lock()
	manager.data.Accounts[0].Targets = append(manager.data.Accounts[0].Targets, "qoder:global")
	err := manager.saveLocked()
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, nil); err == nil {
		t.Fatal("混合地区账号文件被接受")
	}
	if err := os.WriteFile(path, []byte(`{"version":999,"accounts":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, nil); err == nil {
		t.Fatal("未知平台文件版本被接受")
	}
}

func TestExpiredTraeExportRestoresBothTargetsOnce(t *testing.T) {
	m, _ := testManager(t, "")
	in := AccountInput{Target: "traework:cn", Credential: Credential{UID: "fixture", AccessToken: "old", RefreshToken: "single-use"}}
	id, err := m.Import(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Target = "traecode:cn"
	if _, err = m.Import(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	m.data.Accounts[0].Credential.ExpiresAt = 1
	records, err := m.Export([]string{id})
	if err != nil || len(records) != 2 {
		t.Fatal(records, err)
	}
	path := filepath.Join(t.TempDir(), "restored.json")
	m, f := testManager(t, path)
	if _, err = m.Import(t.Context(), records[0]); err != nil || f.refreshed.Load() != 1 {
		t.Fatal(err)
	}
	m, f = testManager(t, path)
	f.refreshErr = errors.New("旧令牌已用过")
	if _, err = m.Import(t.Context(), records[1]); err != nil || f.refreshed.Load() != 0 {
		t.Fatalf("第二通道重复刷新: %v", err)
	}
	if a := m.data.Accounts[0]; len(a.Targets) != 2 || a.Credential.RefreshToken != "rotated-refresh" || m.List()[0].Disabled {
		t.Fatal("恢复的共享账号不完整")
	}
}

func TestReimportUsesBoundProxyBeforeRefresh(t *testing.T) {
	m, f := testManager(t, "")
	reg, err := proxies.Open(filepath.Join(t.TempDir(), "proxies.json"))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := reg.CreatePool(proxies.PoolInput{Name: "测试"})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := reg.Import("http://proxy-fixture.invalid:8888", "http", pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy := bindings.Added[0]
	m.SetProxies(reg)
	in := AccountInput{Target: "traecode:cn", ProxyID: proxy, Credential: Credential{UID: "fixture", AccessToken: "old", RefreshToken: "single-use"}}
	_, err = m.Import(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	f.refresh = func(ctx context.Context, _ string, c Credential) (Credential, error) {
		if ctx.Value(providerRouteKey{}) != proxy {
			t.Fatal("刷新未使用原绑定")
		}
		c.ExpiresAt = time.Now().Add(time.Hour).Unix()
		return c, nil
	}
	in.Credential.UID = ""
	in.Credential.ExpiresAt = 1
	in.ProxyID = ""
	if _, err = m.Import(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	in.ProxyID = "不存在的另一条线路"
	if _, err = m.Import(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if m.List()[0].ProxyID != proxy || f.refreshed.Load() != 1 {
		t.Fatal("原代理或凭证未保留")
	}
}

type catalogTestAdapter struct {
	*fakeAdapter
	models func(string) ([]Model, error)
}

func (a catalogTestAdapter) Models(_ context.Context, _ string, c Credential) ([]Model, error) {
	return a.models(c.UID)
}

func TestCatalogUsesCurrentGroupsForLookupAndFinalResults(t *testing.T) {
	m, f := testManager(t, "")
	const target = "traecode:cn"
	first := importFake(t, m, target, "first", "allowed")
	next := importFake(t, m, target, "next", "allowed")
	importFake(t, m, target, "last", "allowed")
	m.adapters["traecode"] = catalogTestAdapter{f, func(uid string) ([]Model, error) {
		switch uid {
		case "first":
			if err := m.Change(next, "leave_group", "allowed", 0, nil, ""); err != nil {
				t.Fatal(err)
			}
		case "next":
			t.Fatal("查询了已移出分组的账号")
		case "last":
			if err := m.Change(first, "leave_group", "allowed", 0, nil, ""); err != nil {
				t.Fatal(err)
			}
		}
		return []Model{{ID: uid}, {ID: "shared", Name: uid}}, nil
	}}
	models := m.Catalog(t.Context(), func(_ string, groups []string) bool { return len(groups) == 1 && groups[0] == "allowed" })
	if len(models) != 2 || models[0]["id"] != "traecode:cn:last" || models[1]["name"] != "last" {
		t.Fatalf("返回了已撤权来源的数据：%v", models)
	}
}

func TestEmptyNativeModelLeavesLedgerReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	m, _ := testManager(t, path)
	id := importFake(t, m, "qoder:cn", "fixture")
	for _, name := range []string{"", "  "} {
		r, err := m.Execute(t.Context(), Request{Target: "qoder:cn", Model: name, Body: []byte(plainChat)}, discardFrame)
		var e *Error
		if !errors.As(err, &e) || e.Status != 400 || e.Code != "invalid_model" || r.Attempts != 0 || m.running[id].catalogRequest != 0 || len(m.Usage()) != 0 {
			t.Fatal("空模型进入选号或记账", r, err)
		}
	}
	if _, err := Open(path, nil); err != nil {
		t.Fatal("网关重启失败", err)
	}
}

func TestRecreatedIdentityClearsRefreshGuard(t *testing.T) {
	for _, cannotClear := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "providers.json")
		m, _ := testManager(t, path)
		id := importFake(t, m, "qoder:cn", "same")
		if err := m.markRefreshing(id); err != nil {
			t.Fatal(err)
		}
		if err := m.Update(id, AccountInput{}, true); err != nil {
			t.Fatal(err)
		}
		if cannotClear {
			if err := os.Remove(m.guardPath(id)); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(m.guardPath(id), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(m.guardPath(id), "fixture"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		_, err := m.Import(t.Context(), AccountInput{Target: "qoder:cn", Credential: Credential{UID: "same", AccessToken: "fixture-new", UserType: "teams", ExpiresAt: time.Now().Add(time.Hour).Unix()}})
		if (err != nil) != cannotClear || m.running[id].blocked != cannotClear {
			t.Fatal("刷新保护清理结果错误", err)
		}
		loaded, err := Open(path, nil)
		if err != nil || loaded.running[id].blocked != cannotClear || loaded.data.Accounts[0].Credential.AccessToken != "fixture-new" {
			t.Fatal("重启后的新凭证或保护状态错误", err)
		}
	}
}

func TestChatPanicReleasesAccountSlot(t *testing.T) {
	m, _ := testManager(t, "")
	id := importFake(t, m, "qoder:cn", "fixture")
	req := Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("异常未传出")
			}
		}()
		_, _ = m.Execute(t.Context(), req, func(map[string]any) error { panic("fixture") })
	}()
	if m.running[id].inFlight != 0 {
		t.Fatal("异常退出泄漏账号槽位")
	}
	if _, err := m.Execute(t.Context(), req, discardFrame); err != nil {
		t.Fatal(err)
	}
}
