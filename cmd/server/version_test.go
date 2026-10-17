package main

import (
	"testing"

	wb2api "github.com/linguo2625469/workbuddy2api-panel"
	"github.com/linguo2625469/workbuddy2api-panel/internal/changelog"
)

// 版本号和更新日志要一起改：appVersion 是完整的语义化版本号，并且就是更新日志里最新的那一版
// （CI 发版时从更新日志提取这一版的说明，面板的「本次更新」也读它）。
func TestAppVersionMatchesChangelog(t *testing.T) {
	if !changelog.Valid(appVersion) {
		t.Fatalf("appVersion %q 不是完整的语义化版本号（x.y.z，不带 v）", appVersion)
	}
	rels := changelog.Parse(wb2api.Changelog)
	if len(rels) == 0 || rels[0].Version != appVersion {
		latest := ""
		if len(rels) > 0 {
			latest = rels[0].Version
		}
		t.Fatalf("docs/CHANGELOG.md 最新版本是 %q，appVersion 是 %q：升版本号时要在更新日志顶部写好这一版", latest, appVersion)
	}
	if rels[0].Date == "" || len(rels[0].Groups) == 0 {
		t.Fatalf("更新日志里 %s 这一版缺日期或条目：%+v", appVersion, rels[0])
	}
}
