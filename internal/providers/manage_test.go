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
	"testing"

	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

func TestManagementPreservesSettingsAndGuards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	m, _ := testManager(t, path)
	id := importFake(t, m, "qoder:cn", "one")
	ctx := context.Background()
	request := Request{Target: "qoder:cn", Model: "model", Body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
	for _, action := range []string{"pause", "resume", "disable", "revive"} {
		if err := m.Change(id, action, "", 0, nil, ""); err != nil {
			t.Fatal(err)
		}
		_, err := m.Execute(ctx, request, func(map[string]any) error { return nil })
		if blocked := action == "pause" || action == "disable"; blocked != (err != nil) {
			t.Fatalf("%s 派发错误：%v", action, err)
		}
	}
	if err := m.Change(id, "block_models", "", 0, []string{"qoder:cn:mod*"}, ""); err != nil {
		t.Fatal(err)
	}
	if out, err := m.Execute(ctx, request, func(map[string]any) error { return nil }); err == nil || out.Attempts != 0 {
		t.Fatal("被屏蔽模型仍出站", err)
	}
	if err := m.Change(id, "unblock_models", "", 0, []string{"qoder:cn:mod*"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(id, AccountInput{Name: "fixture", LivenessModel: "qoder:global:model"}, false); err == nil {
		t.Fatal("允许跨地区测活模型")
	}
	if err := m.Update(id, AccountInput{Name: "fixture", LivenessModel: "qoder:cn:model"}, false); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"join_group", "priority", "proxy"} {
		if err := m.Change(id, action, "group", 5, nil, "proxy"); err != nil {
			t.Fatal(err)
		}
	}
	before := m.List()[0]
	if err := m.Change(id, "priority", "", 100, nil, ""); err == nil {
		t.Fatal("优先级没有校验")
	}
	if err := m.Change(id, "unknown", "", 0, nil, ""); err == nil {
		t.Fatal("未知操作未报错")
	}
	reopened, _ := Open(path, nil)
	a := reopened.List()[0]
	if a.Priority != 5 || a.ProxyID != "proxy" || len(a.Groups) != 1 || a.LivenessModel != "qoder:cn:model" {
		t.Fatal("设置未持久化", a)
	}
	m.path = t.TempDir()
	if err := m.Change(id, "pause", "", 0, nil, ""); err == nil || m.List()[0].Paused != before.Paused {
		t.Fatal("保存失败未回滚", err)
	}
	m.path = path
	m.running[id].blocked = true
	if m.List()[0].ManualDisabled {
		t.Fatal("刷新保护被当成人工停用")
	}
	if err := m.Change(id, "revive", "", 0, nil, ""); err != nil || !m.List()[0].Disabled {
		t.Fatal("解冻绕过刷新保护", err)
	}
}

func TestCredentialExportRoundTripAndProtection(t *testing.T) {
	m, _ := testManager(t, "")
	id := importFake(t, m, "qoder:cn", "one")
	if err := m.Update(id, AccountInput{Name: "测试账号", Priority: 3, Paused: true, BlockedModels: []string{"qoder:cn:blocked"}, LivenessModel: "qoder:cn:model"}, false); err != nil {
		t.Fatal(err)
	}
	docs, err := m.Export([]string{id, id})
	if err != nil || len(docs) != 1 || docs[0].Credential.AccessToken == "" {
		t.Fatal("导出失败或重复", err)
	}
	other, _ := testManager(t, "")
	restored, err := other.Import(context.Background(), docs[0])
	if err != nil {
		t.Fatal(err)
	}
	a := other.List()[0]
	if restored != id || !a.Paused || a.Priority != 3 || a.LivenessModel != "qoder:cn:model" || len(a.BlockedModels) != 1 {
		t.Fatal("导入后设置丢失", a)
	}
	raw, _ := json.Marshal(m.List())
	if strings.Contains(string(raw), docs[0].Credential.AccessToken) {
		t.Fatal("普通列表泄露导出凭证")
	}
	if _, err := m.Export([]string{id, "missing"}); err == nil {
		t.Fatal("导出静默忽略不存在的账号")
	}
	m.running[id].blocked = true
	if _, err := m.Export([]string{id}); err == nil {
		t.Fatal("刷新状态不确定时导出了旧凭证")
	}
}

func TestProviderAttemptsReachTimeLedgerWithoutInventingUsage(t *testing.T) {
	m, f := testManager(t, "")
	importFake(t, m, "qoder:cn", "one")
	path := filepath.Join(t.TempDir(), "usage.json")
	recorder := usage.New(path)
	m.SetUsage(recorder)
	request := Request{Target: "qoder:cn", Model: "model", KeyID: "client", Body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
	if _, err := m.Execute(context.Background(), request, func(map[string]any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	f.chat = func(_ context.Context, _ string, _ Credential, _ string, emit func(map[string]any) error) error {
		return emit(chunk("model", map[string]any{}, "stop", nil))
	}
	if _, err := m.Execute(context.Background(), request, func(map[string]any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	f.chat = func(context.Context, string, Credential, string, func(map[string]any) error) error {
		return errors.New("fixture failure")
	}
	if _, err := m.Execute(context.Background(), request, func(map[string]any) error { return nil }); err == nil {
		t.Fatal("预期失败")
	}
	recorder.Save()
	reloaded := usage.New(path)
	snapshot := reloaded.SnapshotWindow(usage.Window{Target: "qoder:cn", Key: "client", KeySet: true}, nil, nil)
	if snapshot.Totals.Requests != 3 || snapshot.Totals.Errors != 1 || snapshot.Totals.UnknownTokens != 2 || snapshot.Totals.TotalTokens != 5 || snapshot.Totals.CreditSamples != 0 {
		t.Fatal("时序计量错误", snapshot)
	}
	if len(snapshot.ByModel) != 1 || snapshot.ByModel[0].Key != "qoder:cn:model" {
		t.Fatal("模型串平台", snapshot.ByModel)
	}
	if wrong := reloaded.SnapshotWindow(usage.Window{Target: "qoder:global"}, nil, nil); wrong.Totals.Requests != 0 {
		t.Fatal("筛选泄露其他地区")
	}
	if raw, err := os.ReadFile(path); err != nil || !strings.Contains(string(raw), `"ut":2`) {
		t.Fatal("未知 token 没有落盘", err)
	}
}
