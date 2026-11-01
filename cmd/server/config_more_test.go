// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package main

// config_more_test.go 配置环境变量覆盖与 normalize 的剩余分支：
// 各 WB2A_* 变量的赋值 / 非法值忽略、默认值回落、负值钳制、小时越界与
// prompt.mode 三态。已有测试文件覆盖的部分不重复。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// applyEnv：所有环境变量都覆盖到对应字段。
func TestApplyEnvAllFields(t *testing.T) {
	env := map[string]string{
		"WB2A_LISTEN":                 ":9999",
		"WB2A_API_KEY":                "env-key",
		"WB2A_AUTH_DIR":               "env-auths",
		"WB2A_STATE_FILE":             "env-state.json",
		"WB2A_SOFT_RATE":              "61s",
		"WB2A_SOFT_RATE_MAX":          "3h",
		"WB2A_TIMEOUT_SECONDS":        "33",
		"WB2A_HEADER_TIMEOUT_SECONDS": "44",
		"WB2A_IDLE_TIMEOUT_SECONDS":   "55",
		"WB2A_USER_AGENT":             "env-ua",
		"WB2A_CLIENT_VERSION":         "9.9.9",
		"WB2A_CLI_VERSION":            "8.8.8",
		"WB2A_CLIENT_NAME":            "WorkBuddy",
		"WB2A_DEVICE_TOKEN":           "dev-token",
		"WB2A_DEVICE_TOKEN_FILE":      "dev-token-file",
		"WB2A_PASSTHROUGH_IP":         "true",
		"WB2A_MODEL_OPTIMIZATION":     "true",
		"WB2A_SANITIZE_FINGERPRINTS":  "false",
		"WB2A_PROMPT_MODE":            "custom",
		"WB2A_PROMPT_FILE":            "prompt.md",
		"WB2A_EXPIRING_SOON":          "168h",
		"WB2A_PREFER_EXPIRING":        "false",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	c := Default()
	c.Features.SanitizeBlacklistFingerprints = true // 默认 true，env 显式 false 才能验证覆盖
	c.Pool.PreferExpiring = true
	applyEnv(c)

	if c.Listen != ":9999" || c.APIKey != "env-key" || c.AuthDir != "env-auths" || c.StateFile != "env-state.json" {
		t.Fatalf("基础字段未覆盖: %+v", c)
	}
	if c.Cooldown.SoftRate != "61s" || c.Cooldown.SoftRateMax != "3h" {
		t.Fatalf("冷却字段未覆盖: %+v", c.Cooldown)
	}
	u := c.Upstream
	if u.TimeoutSeconds != 33 || u.HeaderTimeoutSeconds != 44 || u.IdleTimeoutSeconds != 55 ||
		u.UserAgent != "env-ua" || u.ClientVersion != "9.9.9" || u.CliVersion != "8.8.8" ||
		u.ClientName != "WorkBuddy" || u.DeviceToken != "dev-token" || u.DeviceTokenFile != "dev-token-file" ||
		!u.PassthroughIP || !u.ModelOptimization {
		t.Fatalf("upstream 字段未覆盖: %+v", u)
	}
	if c.Features.SanitizeBlacklistFingerprints {
		t.Fatal("WB2A_SANITIZE_FINGERPRINTS=false 应生效")
	}
	if c.Prompt.Mode != "custom" || c.Prompt.File != "prompt.md" {
		t.Fatalf("prompt 字段未覆盖: %+v", c.Prompt)
	}
	if c.Pool.ExpiringSoon != "168h" || c.Pool.PreferExpiring {
		t.Fatalf("pool 字段未覆盖: %+v", c.Pool)
	}
}

// applyEnv：数值/布尔非法时忽略，保留原值。
func TestApplyEnvInvalidValuesIgnored(t *testing.T) {
	for k, v := range map[string]string{
		"WB2A_TIMEOUT_SECONDS":        "abc",
		"WB2A_HEADER_TIMEOUT_SECONDS": "abc",
		"WB2A_IDLE_TIMEOUT_SECONDS":   "abc",
		"WB2A_PASSTHROUGH_IP":         "not-bool",
		"WB2A_MODEL_OPTIMIZATION":     "not-bool",
		"WB2A_SANITIZE_FINGERPRINTS":  "not-bool",
		"WB2A_PREFER_EXPIRING":        "not-bool",
	} {
		t.Setenv(k, v)
	}
	c := Default()
	c.Upstream.TimeoutSeconds = 7
	c.Upstream.HeaderTimeoutSeconds = 8
	c.Upstream.IdleTimeoutSeconds = 9
	c.Upstream.PassthroughIP = true
	c.Upstream.ModelOptimization = true
	c.Features.SanitizeBlacklistFingerprints = true
	c.Pool.PreferExpiring = true
	applyEnv(c)
	if c.Upstream.TimeoutSeconds != 7 || c.Upstream.HeaderTimeoutSeconds != 8 || c.Upstream.IdleTimeoutSeconds != 9 {
		t.Fatalf("非法数值不应覆盖: %+v", c.Upstream)
	}
	if !c.Upstream.PassthroughIP || !c.Upstream.ModelOptimization || !c.Features.SanitizeBlacklistFingerprints || !c.Pool.PreferExpiring {
		t.Fatal("非法布尔不应覆盖")
	}
}

// normalize：空值/零值回落默认、负值钳制、listen 补冒号、排程与余额刷新默认。
func TestNormalizeDefaultsAndClamps(t *testing.T) {
	c := Default()
	c.Cooldown.SoftRateMax = ""
	c.Pool.BreakerThreshold = 0
	c.Pool.DegradeThreshold = 0
	c.Pool.MaxInFlightGlobal = 0
	c.Pool.IdleWeightPerHour = 0
	c.Pool.IdleWeightMax = 0
	c.Upstream.TimeoutSeconds = 0
	c.Upstream.HeaderTimeoutSeconds = 0
	c.Upstream.IdleTimeoutSeconds = 0
	c.Pool.CostExploreInterval = ""
	c.Pool.InFlightWait = ""
	c.Pool.ExpiringSoon = "-5m"
	c.Pool.CreditFloor = -3
	c.Pool.MaxRPM, c.Pool.MaxRPMGlobal = -1, -2
	c.Pool.RestrictionWaveThreshold = -1
	c.Pool.RestrictionWaveWindow = ""
	c.Pool.RestrictionWavePause = "-5m"
	c.Listen = "7863"
	c.Schedule.BlackcatHours = nil
	c.Schedule.GrowthHours = nil
	c.Schedule.BalanceRefreshEnabled = true
	c.Schedule.BalanceRefreshMinutes = 0
	c.Server.ReadTimeout = ""

	if err := c.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if c.Cooldown.SoftRateMax != "2h" || c.SoftRateMaxDur != 2*time.Hour {
		t.Fatalf("soft_rate_max 应回落 2h: %q %v", c.Cooldown.SoftRateMax, c.SoftRateMaxDur)
	}
	if c.Pool.BreakerThreshold != 3 || c.Pool.DegradeThreshold != 5 {
		t.Fatalf("熔断/降权阈值应回落默认: %+v", c.Pool)
	}
	if c.Pool.MaxInFlightGlobal != 2 || c.Pool.IdleWeightPerHour != 0.5 || c.Pool.IdleWeightMax != 5 {
		t.Fatalf("在途/闲置权重应回落默认: %+v", c.Pool)
	}
	if c.Pool.MaxRPM != 0 || c.Pool.MaxRPMGlobal != 0 {
		t.Fatalf("每分钟请求数上限负值应钳 0（不限）: %+v", c.Pool)
	}
	if c.Upstream.TimeoutSeconds != 120 || c.Upstream.HeaderTimeoutSeconds != 120 || c.Upstream.IdleTimeoutSeconds != 300 {
		t.Fatalf("上游超时应回落默认: %+v", c.Upstream)
	}
	if c.Pool.CostExploreInterval != "30m" || c.CostExploreIntervalDur != 30*time.Minute {
		t.Fatalf("探索窗口应回落 30m: %q %v", c.Pool.CostExploreInterval, c.CostExploreIntervalDur)
	}
	if c.Pool.InFlightWait != "30s" || c.InFlightWaitDur != 30*time.Second {
		t.Fatalf("排队等空位应回落 30s: %q %v", c.Pool.InFlightWait, c.InFlightWaitDur)
	}
	if c.Pool.RestrictionWaveThreshold != 0 || c.RestrictionWaveWindowDur != 15*time.Minute || c.RestrictionWavePauseDur != 30*time.Minute {
		t.Fatalf("风控波应阈值钳 0、时长回落默认: %d %v %v", c.Pool.RestrictionWaveThreshold, c.RestrictionWaveWindowDur, c.RestrictionWavePauseDur)
	}
	if c.Pool.ExpiringSoon != "0" || c.ExpiringSoonDur != 0 {
		t.Fatalf("负窗口应钳 0: %q %v", c.Pool.ExpiringSoon, c.ExpiringSoonDur)
	}
	if c.Pool.CreditFloor != 0 {
		t.Fatalf("负积分保底应钳 0: %d", c.Pool.CreditFloor)
	}
	if c.Listen != ":7863" {
		t.Fatalf("listen 应补冒号: %q", c.Listen)
	}
	if len(c.Schedule.BlackcatHours) != 1 || c.Schedule.BlackcatHours[0] != 23 ||
		len(c.Schedule.GrowthHours) != 1 || c.Schedule.GrowthHours[0] != 1 {
		t.Fatalf("排程小时应回落默认: %+v", c.Schedule)
	}
	if c.BalanceRefreshInterval != 5*time.Minute {
		t.Fatalf("余额刷新间隔应回落 5m: %v", c.BalanceRefreshInterval)
	}
	if c.ServerReadTimeoutDur != 300*time.Second {
		t.Fatalf("读取超时应回落 300s: %v", c.ServerReadTimeoutDur)
	}
	if c.Prompt.Mode != "passthrough" {
		t.Fatalf("prompt.mode 缺省应回落 passthrough: %q", c.Prompt.Mode)
	}
}

// normalize：各时长字段非法时 fail fast，错误信息指名字段。
func TestNormalizeDurationErrors(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Config)
		want string
	}{
		{"读取超时非法", func(c *Config) { c.Server.ReadTimeout = "bad" }, "server.read_timeout"},
		{"读取超时负数", func(c *Config) { c.Server.ReadTimeout = "-1s" }, "负时长"},
		{"软冷却非法", func(c *Config) { c.Cooldown.SoftRate = "bad" }, "cooldown.soft_rate:"},
		{"软冷却封顶非法", func(c *Config) { c.Cooldown.SoftRateMax = "bad" }, "cooldown.soft_rate_max"},
		{"熔断冷却非法", func(c *Config) { c.Pool.BreakerCooldown = "bad" }, "pool.breaker_cooldown:"},
		{"熔断封顶非法", func(c *Config) { c.Pool.BreakerCooldownMax = "bad" }, "pool.breaker_cooldown_max"},
		{"降权冷却非法", func(c *Config) { c.Pool.DegradeCooldown = "bad" }, "pool.degrade_cooldown:"},
		{"降权封顶非法", func(c *Config) { c.Pool.DegradeCooldownMax = "bad" }, "pool.degrade_cooldown_max"},
		{"会话TTL非法", func(c *Config) { c.SessionSticky.TTL = "bad" }, "session_sticky.ttl"},
		{"会话GC非法", func(c *Config) { c.SessionSticky.GCInterval = "bad" }, "session_sticky.gc_interval"},
		{"快过期窗口非法", func(c *Config) { c.Pool.ExpiringSoon = "bad" }, "pool.expiring_soon"},
		{"探索窗口非法", func(c *Config) { c.Pool.CostExploreInterval = "bad" }, "pool.cost_explore_interval"},
		{"排队上限非法", func(c *Config) { c.Pool.InFlightWait = "bad" }, "pool.in_flight_wait"},
		{"风控波窗口非法", func(c *Config) { c.Pool.RestrictionWaveWindow = "bad" }, "pool.restriction_wave_window"},
		{"风控波暂停非法", func(c *Config) { c.Pool.RestrictionWavePause = "bad" }, "pool.restriction_wave_pause"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.set(c)
			err := c.normalize()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v，应含 %q", err, tc.want)
			}
		})
	}
}

// normalize：排程小时越界 fail fast，错误信息指向对应开关。
func TestNormalizeScheduleHourErrors(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Config)
		want string
	}{
		{"签到", func(c *Config) { c.Schedule.CheckinHours = []int{24} }, "schedule.checkin_hours"},
		{"旅行", func(c *Config) { c.Schedule.TravelHours = []int{-1} }, "schedule.travel_hours"},
		{"活跃", func(c *Config) { c.Schedule.ActivityHours = []int{99} }, "schedule.activity_hours"},
		{"保活", func(c *Config) { c.Schedule.KeepaliveHours = []int{24} }, "schedule.keepalive_hours"},
		{"夜猫子", func(c *Config) { c.Schedule.BlackcatHours = []int{24} }, "schedule.blackcat_hours"},
		{"成长", func(c *Config) { c.Schedule.GrowthHours = []int{-1} }, "schedule.growth_hours"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.set(c)
			err := c.normalize()
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "0-23") {
				t.Fatalf("err=%v，应含 %q 与 0-23", err, tc.want)
			}
		})
	}
}

// normalize：prompt.mode 三态（append/custom 加载文本，非法值报错）。
func TestNormalizePromptModes(t *testing.T) {
	t.Run("append 用内置默认文本", func(t *testing.T) {
		c := Default()
		c.Prompt.Mode = "append"
		if err := c.normalize(); err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if c.Prompt.Mode != "append" || c.PromptText == "" {
			t.Fatalf("append 应加载文本: mode=%q len=%d", c.Prompt.Mode, len(c.PromptText))
		}
	})

	t.Run("custom 读文件", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "prompt.md")
		if err := os.WriteFile(path, []byte("自定义提示词"), 0o600); err != nil {
			t.Fatal(err)
		}
		c := Default()
		c.Prompt.Mode = "custom"
		c.Prompt.File = path
		if err := c.normalize(); err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if c.PromptText != "自定义提示词" {
			t.Fatalf("PromptText=%q", c.PromptText)
		}
	})

	t.Run("非法模式报错", func(t *testing.T) {
		c := Default()
		c.Prompt.Mode = "weird"
		if err := c.normalize(); err == nil || !strings.Contains(err.Error(), "prompt.mode") {
			t.Fatalf("err=%v，应含 prompt.mode", err)
		}
	})
}

// TestRestrictionWaveConfig 风控波默认开（2 个号 / 15m / 30m），显式 0 关闭，时长可改。
func TestRestrictionWaveConfig(t *testing.T) {
	c, err := ParseConfig([]byte(`{"pool": {"max_in_flight": 3}}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if c.Pool.RestrictionWaveThreshold != 2 || c.RestrictionWaveWindowDur != 15*time.Minute || c.RestrictionWavePauseDur != 30*time.Minute {
		t.Fatalf("缺省应为 2 / 15m / 30m: %d %v %v", c.Pool.RestrictionWaveThreshold, c.RestrictionWaveWindowDur, c.RestrictionWavePauseDur)
	}
	c, err = ParseConfig([]byte(`{"pool": {"restriction_wave_threshold": 0, "restriction_wave_window": "10m", "restriction_wave_pause": "1h"}}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if c.Pool.RestrictionWaveThreshold != 0 || c.RestrictionWaveWindowDur != 10*time.Minute || c.RestrictionWavePauseDur != time.Hour {
		t.Fatalf("显式值应生效: %d %v %v", c.Pool.RestrictionWaveThreshold, c.RestrictionWaveWindowDur, c.RestrictionWavePauseDur)
	}
}
