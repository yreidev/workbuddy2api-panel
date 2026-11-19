// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package apikeys

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTargetPermissionsAndModelWhitelist(t *testing.T) {
	old := Key{Models: []string{"*"}}
	if !old.AllowsTargetModel("workbuddy:cn", "model") || !old.AllowsTargetModel("workbuddy:global", "model") || old.AllowsTargetModel("qoder:cn", "model") {
		t.Fatal("旧密钥平台权限扩大")
	}
	mixed := Key{AllowedTargets: []string{"workbuddy:cn", "qoder:global"}, Models: []string{"model*", "qoder:global:claude-*"}}
	for _, tc := range []struct {
		target, model string
		allow         bool
	}{{"qoder:global", "model", false}, {"qoder:global", "claude-test", true}, {"qoder:cn", "claude-test", false}, {"workbuddy:cn", "model", true}, {"workbuddy:global", "model", false}} {
		if got := mixed.AllowsTargetModel(tc.target, tc.model); got != tc.allow {
			t.Fatalf("%s %s: %v", tc.target, tc.model, got)
		}
	}
	s, _ := newStore(t)
	if _, err := s.Create(Input{Name: "bad", AllowedTargets: []string{"qoder:cn"}, DefaultTarget: "traecode:cn"}, t0); err == nil {
		t.Fatal("接受组外默认上游")
	}
	if _, err := s.Create(Input{Name: "bad", AllowedTargets: []string{"unknown:cn"}}, t0); err == nil {
		t.Fatal("接受未知上游")
	}
}
func TestTargetAdmitQuotaAndLiveChanges(t *testing.T) {
	s, _ := newStore(t)
	in := Input{Name: "multi", Enabled: true, AllowedTargets: []string{"workbuddy:cn", "qoder:cn"}, TotalLimit: Limits{Credits: 1, Tokens: 100, Requests: 10}}
	key := mustCreate(t, s, in)
	s.Record(key.ID, t0, 1, 1, true)
	if denied := s.AdmitTarget(key.ID, "workbuddy:cn", "model", t0); denied == nil || denied.Status != 429 {
		t.Fatal("WorkBuddy 积分上限无效", denied)
	}
	if denied := s.AdmitTarget(key.ID, "qoder:cn", "model", t0); denied != nil {
		t.Fatal("WorkBuddy 积分上限污染 Qoder", denied)
	}
	in.AllowedTargets = []string{"workbuddy:cn"}
	if _, err := s.Update(key.ID, in); err != nil {
		t.Fatal(err)
	}
	if denied := s.AdmitTarget(key.ID, "qoder:cn", "model", t0); denied == nil || denied.Status != 403 {
		t.Fatal("权限修改未立即生效", denied)
	}
	in.AllowedTargets = []string{"qoder:cn"}
	in.TotalLimit = Limits{Tokens: 1}
	if _, err := s.Update(key.ID, in); err != nil {
		t.Fatal(err)
	}
	if denied := s.AdmitTarget(key.ID, "qoder:cn", "model", t0); denied == nil || denied.Status != 429 {
		t.Fatal("跨平台 token 限额无效", denied)
	}
}
func TestKeyV1BackupAndMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api_keys.json")
	original := []byte(`{"version":1,"keys":[{"id":"k1","name":"旧密钥","secret":"sk-fixture","enabled":true,"models":["*"]}]}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".v1.bak")
	if err != nil || string(backup) != string(original) {
		t.Fatal("旧密钥备份不完整", err)
	}
	key, ok := store.Get("k1", time.Now())
	if !ok || key.AllowsTarget("qoder:cn") || !key.AllowsTarget("workbuddy:global") {
		t.Fatal("迁移扩权")
	}
	if file := readFile(t, path); string(file.Version) != fileVersion {
		t.Fatal("未升级密钥文件版本")
	}
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(path + ".v1.bak")
	if err != nil || string(again) != string(original) {
		t.Fatal("再次启动覆盖备份")
	}
	if err := os.WriteFile(path, []byte(`{"version":999,"keys":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("未知版本被当作空库")
	}
}

func TestMigrationRequiresCompleteBackup(t *testing.T) {
	raw := []byte(`{"version":1,"keys":[{"id":"fixture","secret":"fixture"}]}`)
	for _, backup := range []string{string(raw[:15]), `{"version":1,"keys":[]}`, string(raw)} {
		dir := t.TempDir()
		path := filepath.Join(dir, "keys.json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".v1.bak", []byte(backup), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(path)
		if (err == nil) != (backup == string(raw)) {
			t.Fatalf("迁移结果错误: %v", err)
		}
		if err != nil {
			after, e := os.ReadFile(path)
			if e != nil || string(after) != string(raw) {
				t.Fatal("原密钥文件已被覆盖", e)
			}
		}
	}
}

func TestV2FilesRejectLegacyReaders(t *testing.T) {
	for _, version := range []string{"2", `"2"`} {
		path := filepath.Join(t.TempDir(), "keys.json")
		raw := []byte(`{"version":` + version + `,"keys":[{"id":"k","secret":"fixture","enabled":true,"allowed_targets":["qoder:cn"]}]}`)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		key, ok := s.Get("k", time.Now())
		if !ok || !key.AllowsTarget("qoder:cn") || key.AllowsTarget("workbuddy:cn") {
			t.Fatal("升级改变权限")
		}
		for _, flush := range []bool{false, true} {
			if flush {
				s.Flush()
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// 与 v1.13.2 的文件头类型相同，启动时解析错误会返回给主程序。
			var legacy struct {
				Version int
				Keys    []*Key
			}
			if err := json.Unmarshal(data, &legacy); err == nil {
				t.Fatal("旧读取器接受了 v2 权限文件")
			}
		}
	}
}

func TestDowngradedV1CanMigrateAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	first := []byte(`{"version":1,"keys":[{"id":"old","secret":"fixture-old"}]}`)
	changed := []byte(`{"version":1,"keys":[{"id":"new","secret":"fixture-new","used":{"requests":7}}]}`)
	write := func(name string, raw []byte) {
		t.Helper()
		if err := os.WriteFile(name, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(path, first)
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	write(path, changed)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	keys := s.List(time.Now())
	if len(keys) != 1 || keys[0].ID != "new" || keys[0].Used.Requests != 7 {
		t.Fatal("再次升级丢失旧版写入")
	}
	for name, want := range map[string][]byte{path + ".v1.bak": first, path + ".v1." + digest(string(changed)) + ".bak": changed} {
		raw, err := os.ReadFile(name)
		if err != nil || string(raw) != string(want) {
			t.Fatalf("备份不完整: %s: %v", name, err)
		}
	}
	write(path, changed)
	write(path+".v1.bak", []byte(`{"version":1,"keys":[]}`))
	if _, err := Open(path); err == nil {
		t.Fatal("首次备份损坏后仍放行迁移")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != string(changed) {
		t.Fatal("核验失败覆盖了待迁移文件", err)
	}
}

func TestNumericV2RecognizesEarlierV1Backup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	first := []byte(`{"version":1,"keys":[]}`)
	if err := os.WriteFile(path+".v1.bak", first, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"keys":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"keys":[{"id":"new","secret":"fixture"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil || !s.Any() {
		t.Fatal("历史 v2 降级后不能再次升级", err)
	}
	backup, err := os.ReadFile(path + ".v1.bak")
	if err != nil || string(backup) != string(first) {
		t.Fatal("覆盖了历史 v1 备份", err)
	}
}

func TestLegacyRecoveryDoesNotRestoreRevokedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api_keys.json")
	save := func(f file) {
		t.Helper()
		raw, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save(file{Version: json.RawMessage("1"), Keys: []*Key{{ID: "old", Secret: "fixture-old", Enabled: true}}})
	s, err := OpenWithLegacyRecovery(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("old"); err != nil {
		t.Fatal(err)
	}
	key, err := s.Create(Input{Name: "新平台", Enabled: true, AllowedTargets: []string{"qoder:cn"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.Flush()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		Version int
		Keys    []*Key
	}
	if err := json.Unmarshal(raw, &legacy); err != nil || legacy.Version != 1 || len(legacy.Keys) != 1 {
		t.Fatal("旧程序无法恢复", err)
	}
	marker := legacy.Keys[0]
	if marker.ID == "old" || marker.Secret == key.Secret || marker.Enabled || !marker.Expired(time.Now()) {
		t.Fatal("回退入口暴露了有效密钥")
	}
	// 旧程序正常退出会按 v1 格式重写文件。再次启用新版必须使用当前权限及计数。
	save(file{Version: json.RawMessage("1"), Keys: legacy.Keys})
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Lookup(key.Secret)
	if !ok || got.AllowsTarget("workbuddy:cn") || !got.AllowsTarget("qoder:cn") {
		t.Fatal("丢失 v2 权限")
	}
	if _, ok := reopened.Lookup("fixture-old"); ok {
		t.Fatal("恢复了已撤销的密钥")
	}
	// 回退期间在旧面板新建的密钥也一并带回，合并发布后再次启动不重复。
	save(file{Version: json.RawMessage("1"), Keys: []*Key{marker, {ID: "added", Secret: "fixture-added", Enabled: true}}})
	for range 2 {
		reopened, err = OpenWithLegacyRecovery(path)
		if err != nil {
			t.Fatal(err)
		}
		added, ok := reopened.Lookup("fixture-added")
		if !ok || added.AllowsTarget("qoder:cn") || len(reopened.List(time.Now())) != 2 {
			t.Fatal("旧面板写入丢失或扩权")
		}
	}
	if err := os.Remove(reopened.path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("恢复入口缺少目标文件却打开了空密钥库")
	}
}

func TestLegacyRecoveryPublicationFailurePreservesLiveV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api_keys.json")
	raw := []byte(`{"version":1,"keys":[{"id":"current","secret":"fixture"}]}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWithLegacyRecovery(path); err == nil {
		t.Fatal("无法发布恢复入口却完成迁移")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("发布前失败导致旧程序失去活跃密钥", err)
	}
}

func TestBackupPublicationNeverReplacesWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.bak")
	start := make(chan struct{})
	result := make(chan error, 2)
	for _, raw := range []string{"first", "second"} {
		go func() { <-start; result <- publishBackup(path, []byte(raw)) }()
	}
	close(start)
	first, second := <-result, <-result
	if (first == nil) == (second == nil) {
		t.Fatalf("排他发布结果 %v / %v", first, second)
	}
	winner, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishBackup(path, winner); err != nil {
		t.Fatal("重试不能核验既有备份", err)
	}
	if err := publishBackup(path, []byte("third")); err == nil {
		t.Fatal("不同内容覆盖成功")
	}
}
