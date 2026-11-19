// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBillingUnitsAndPairedTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	r := New(path)
	now := time.Now().Add(-100 * 24 * time.Hour)
	for _, realm := range []string{"cn", "qoder:cn", "qoder:global", "traework:cn"} {
		// 同一个账号 ID、模型和密钥也必须按费用单位隔离。
		r.Add(now, realm, "same", "same", Delta{Credit: 0.125, HasCredit: true, PromptTokens: 100, HasPromptTokens: true, Key: "key"}, true)
		r.Add(now.Add(time.Hour), realm, "same", "same", Delta{Credit: 1, HasCredit: true, Key: "key"}, false)
		r.Add(now, realm, "same", "same", Delta{TokensUnknown: true, Key: "key"}, false)
	}
	r.Rollup(time.Now())
	r.Save()
	loaded := New(path)
	snap := loaded.Snapshot(0, nil)
	if snap.Totals.Credits != 0 || snap.Totals.CreditSamples != 0 || len(snap.Totals.CreditByUnit) != 4 || len(snap.CreditByAccount) != 4 || len(snap.CreditByModel) != 4 {
		t.Fatalf("费用单位混算 %+v", snap)
	}
	for _, unit := range snap.Totals.CreditByUnit {
		if unit.Credits != 1.125 || unit.CreditSamples != 2 || unit.CreditTokens != 100 || unit.CreditsPer1MTokens != 1250 {
			t.Fatalf("仅费用样本污染每M费用 %+v", unit)
		}
	}
	one := loaded.SnapshotWindow(Window{Target: "qoder:cn", Key: "key", KeySet: true}, nil, nil)
	if len(one.ByAccount) != 1 || one.ByAccount[0].Realm != "qoder:cn" || len(one.ByAccount[0].Realms) != 0 {
		t.Fatal("筛选后仍包含其他上游", one.ByAccount)
	}
	if one.Totals.Credits != 1.125 || one.Totals.CreditUnit != "qoder:cn:credits" || one.Totals.Requests != 3 {
		t.Fatalf("单平台费用筛选错误 %+v", one.Totals)
	}
	if other := loaded.SnapshotWindow(Window{Target: "qoder:cn", Key: "other", KeySet: true}, nil, nil); other.Totals.Requests != 0 {
		t.Fatal("密钥费用越权")
	}
}

func TestBillingV5MigrationPreservesLegacyCredit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, []byte(`{"version":5,"buckets":[{"s":"d:2026-01-01","r":"cn","u":"fixture","m":"model","q":1,"cr":0.125,"cn":1,"ct":100}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := New(path)
	snap := r.Snapshot(0, nil)
	r.Save()
	again := New(path).Snapshot(0, nil)
	if snap.Totals.Credits != 0.125 || again.Totals.Credits != 0.125 || again.Totals.CreditsPer1MTokens != 1250 || again.Totals.CreditUnit != "workbuddy:credits" {
		t.Fatal("历史费用迁移丢失或变换单位", again.Totals)
	}
}
