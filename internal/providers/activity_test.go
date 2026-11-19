// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestActivityMigratesAndRetriesWithoutReplaying(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	m, _ := testManager(t, path)
	id := importFake(t, m, "qoder:cn", "one")
	original := m.data.Accounts[0]
	original.BindingEpoch = "" // v1 尚无此字段。
	legacy := state{Version: 1, Accounts: []Account{original}, Usage: []Usage{{Target: "qoder:cn", AccountID: id, Model: "model", Requests: 3, ChargeKnown: 2, Charge: .25}}, Bindings: map[string]Binding{"legacy": {AccountID: id, SessionID: "fixture", ExpiresAt: time.Now().Add(time.Hour)}}}
	if err := writePlatformFile(path, legacy); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".activity.json"); err != nil {
		t.Fatal(err)
	}
	batchPath := path + ".attempts.json"
	if err := os.Remove(batchPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(batchPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, nil); err == nil {
		t.Fatal("账本不可写时迁移不应成功")
	}
	var prior state
	if err := readActivity(path, &prior); err != nil || prior.Version != 1 || len(prior.Usage) != 1 {
		t.Fatal("失败迁移损坏旧账号文件", err)
	}
	if err := os.Remove(batchPath); err != nil {
		t.Fatal(err)
	}
	m, f := testManager(t, path)
	if !reflect.DeepEqual(m.Usage(), legacy.Usage) || m.data.Bindings["legacy"].SessionID != "fixture" {
		t.Fatal("旧用量或绑定丢失")
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cfgBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(cfgBefore, &cfg); err != nil || string(cfg["version"]) != "2" || cfg["usage"] != nil || cfg["bindings"] != nil {
		t.Fatalf("配置仍包含请求状态：%v", err)
	}
	if err := os.Remove(batchPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(batchPath, 0700); err != nil {
		t.Fatal(err)
	}
	req := Request{Target: "qoder:cn", Model: "model", RequestID: "fixture-request", Body: []byte(plainChat), Session: "s"}
	result, err := m.Execute(t.Context(), req, discardFrame)
	if err == nil || !result.LedgerPending || m.Usage()[0].Requests != 4 {
		t.Fatal("完成的上游尝试未进入待补写账本", result, err)
	}
	var pe *Error
	result, err = m.Execute(t.Context(), req, discardFrame)
	if !errors.As(err, &pe) || pe.Code != "ledger_unavailable" || result.Attempts != 0 || m.Usage()[0].Requests != 4 {
		t.Fatal("账本不健康时仍发起新尝试", result, err)
	}
	if err := os.Remove(batchPath); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := m.FlushActivity(); err != nil {
			t.Fatal(err)
		}
	}
	var attempts []attemptRecord
	if err := readActivity(batchPath, &attempts); err != nil || len(attempts) != 1 || attempts[0].RequestID != req.RequestID || attempts[0].Attempt != 1 {
		t.Fatal("补写未保留唯一请求尝试", err, attempts)
	}
	afterInfo, err := os.Stat(path)
	if err != nil || !os.SameFile(beforeInfo, afterInfo) {
		t.Fatal("对话重新写入凭证文件", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(cfgBefore) != string(after) {
		t.Fatal("请求更改了凭证文件", err)
	}
	m, _ = testManager(t, path)
	m.adapters["qoder"] = f
	if len(m.Usage()) != 1 || m.Usage()[0].Requests != 4 || m.Usage()[0].Charge != .25 || m.Usage()[0].CompletionTokens != 3 || len(m.data.Bindings) != 2 {
		t.Fatalf("恢复后重复记账或丢失会话：%+v", m.Usage())
	}
	if err := m.Update(id, AccountInput{}, true); err != nil {
		t.Fatal(err)
	}
	importFake(t, m, "qoder:cn", "one")
	m, _ = testManager(t, path)
	if len(m.data.Bindings) != 0 || m.Usage()[0].Requests != 4 {
		t.Fatal("重新导入身份复活旧会话或删除历史用量")
	}
}

func TestActivityCheckpointRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	m, _ := testManager(t, path)
	id := importFake(t, m, "qoder:cn", "one")
	req := Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat), Session: "s"}
	for range checkpointAttempts - 1 {
		if _, err := m.Execute(t.Context(), req, discardFrame); err != nil {
			t.Fatal(err)
		}
	}
	oldBatch, err := os.ReadFile(path + ".attempts.json")
	if err != nil {
		t.Fatal(err)
	}
	// 在快照写入点注入故障，恢复条件后只补写一次。
	snapshotPath := path + ".activity.json"
	if err := os.Rename(snapshotPath, snapshotPath+".prior"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(snapshotPath, 0700); err != nil {
		t.Fatal(err)
	}
	if result, err := m.Execute(t.Context(), req, discardFrame); err == nil || !result.LedgerPending {
		t.Fatal(result, err)
	}
	if err := os.Remove(snapshotPath); err != nil {
		t.Fatal(err)
	}
	if err := m.FlushActivity(); err != nil {
		t.Fatal(err)
	}
	// 模拟快照已发布但清空旧批次前退出。重启只能重放快照之后的记录。
	if err := os.WriteFile(path+".attempts.json", oldBatch, 0600); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int64{checkpointAttempts, checkpointAttempts + 1} {
		m, _ = testManager(t, path)
		if got := m.Usage(); len(got) != 1 || got[0].AccountID != id || got[0].Requests != n || got[0].PromptTokens != 2*n || len(m.data.Bindings) != 1 {
			t.Fatalf("快照与批次重放出错：%+v，期望 %d", got, n)
		}
		if n == checkpointAttempts {
			if _, err := m.Execute(t.Context(), req, discardFrame); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(path+".attempts.json", []byte("[{}]"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, nil); err == nil {
		t.Fatal("无效流水被接受")
	}
	if err := writePlatformFile(path+".attempts.json", []attemptRecord{}); err != nil {
		t.Fatal(err)
	}
	// 缺文件不能作为空账本重建。
	if err := os.Remove(snapshotPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, nil); err == nil {
		t.Fatal("缺少快照静默丢失旧数据")
	}
}

func TestFreshActivityAndMixedBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newdir", "providers.json")
	m, _ := testManager(t, path)
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("空账号库在首次导入前写了文件", err)
	}
	id := importFake(t, m, "qoder:cn", "one")
	legacy := state{Version: 1, Accounts: m.data.Accounts, Usage: []Usage{}, Bindings: map[string]Binding{}}
	if _, err := m.Execute(t.Context(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat)}, discardFrame); err != nil {
		t.Fatal(err)
	}
	v2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePlatformFile(path, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, nil); err == nil {
		t.Fatal("用旧账号文件覆盖了已有新账本")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, nil); err == nil {
		t.Fatal("缺失账号文件时覆盖了孤立的账本")
	}
	if err := os.WriteFile(path, v2, 0600); err != nil {
		t.Fatal(err)
	}
	m, _ = testManager(t, path)
	if rows := m.Usage(); len(rows) != 1 || rows[0].AccountID != id || rows[0].Requests != 1 {
		t.Fatal("混合备份破坏了当前账本", rows)
	}
}

func TestConcurrentActivitySurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	m, _ := testManager(t, path)
	for _, uid := range []string{"a", "b", "c", "d"} {
		importFake(t, m, "qoder:cn", uid)
	}
	const count = 16
	done := make(chan error, count)
	for i := range count {
		go func() {
			_, err := m.Execute(t.Context(), Request{Target: "qoder:cn", Model: "model", Body: []byte(plainChat), KeyID: "same", RequestID: fmt.Sprint("request-", i)}, discardFrame)
			done <- err
		}()
	}
	for range count {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	var attempts []attemptRecord
	if err := readActivity(path+".attempts.json", &attempts); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for i, e := range attempts {
		if e.Sequence != uint64(i+1) || e.Attempt != 1 || ids[e.RequestID] {
			t.Fatalf("重复或缺失请求尝试：%+v", e)
		}
		ids[e.RequestID] = true
	}
	if len(ids) != count {
		t.Fatal("落盘尝试数错误", len(ids))
	}
	reopened, _ := testManager(t, path)
	if !reflect.DeepEqual(reopened.Usage(), m.Usage()) {
		t.Fatal("并发记账重启后与内存不一致")
	}
}
