// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"strings"
	"testing"
	"time"
)

func TestClassifyLine(t *testing.T) {
	cases := map[string]string{
		"school c8a3e793: ★ 分享任务完成":           ChTask,
		"streak-bonus 5c162cc9: 🎊 新手礼包 +100c": ChTask,
		"blackcat c8a3e793: 完成 3 次夜间对话":       ChTask,
		"checkin 5c162cc9: 已签到":               ChTask,
		"panel: 任务动作 uid=x code=chat_5":       ChTask,
		"panel: 队列启动：6 项（并发 2）":               ChTask,
		"liveness 定时测活开始：2 个号":                ChTask,
		"panel: 手动全部测活已触发（3 个号）":              ChTask,
		"panel: revive uid=x":                 ChSys,
		"workbuddy2api listening on :7863":    ChSys,
		"scheduler: 余额后台刷新每 5m0s":             ChSys,
	}
	for line, want := range cases {
		if got := classifyLine(line); got != want {
			t.Errorf("classifyLine(%q)=%q want %q", line, got, want)
		}
	}
}

func TestRingWriteStripsTimestamp(t *testing.T) {
	r := NewRing(4)
	if _, err := r.Write([]byte("2026/09/14 00:12:34 school x: done\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("scheduler: 余额后台刷新每 5m0s")); err != nil {
		t.Fatal(err)
	}
	es := r.Snapshot()
	if len(es) != 2 {
		t.Fatalf("entries=%d want 2", len(es))
	}
	if strings.HasPrefix(es[0].Text, "2026/") {
		t.Errorf("timestamp not stripped: %q", es[0].Text)
	}
	if es[0].Ch != ChTask || es[1].Ch != ChSys {
		t.Errorf("channels: %q %q", es[0].Ch, es[1].Ch)
	}
	if time.Since(es[0].TS) > 5*time.Second {
		t.Errorf("stale ts: %v", es[0].TS)
	}
}
