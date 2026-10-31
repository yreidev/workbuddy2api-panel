// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"encoding/json"
	"testing"
)

// TestBestUsageCacheHitTokens 每个缓存命中别名单独生效；优先级按源码
// prompt_tokens_details > prompt_cache_hit_tokens > cache_read_input_tokens >
// cached_tokens > input_tokens_details。
func TestBestUsageCacheHitTokens(t *testing.T) {
	cases := []struct {
		name  string
		usage map[string]any
		want  float64
		ok    bool
	}{
		{"prompt_tokens_details", map[string]any{"prompt_tokens_details": map[string]any{"cached_tokens": 5.0}}, 5, true},
		{"prompt_cache_hit_tokens", map[string]any{"prompt_cache_hit_tokens": 6.0}, 6, true},
		{"cache_read_input_tokens", map[string]any{"cache_read_input_tokens": 7.0}, 7, true},
		{"cached_tokens", map[string]any{"cached_tokens": 8.0}, 8, true},
		{"input_tokens_details", map[string]any{"input_tokens_details": map[string]any{"cached_tokens": 9.0}}, 9, true},
		{"details 优先于顶层", map[string]any{
			"prompt_tokens_details": map[string]any{"cached_tokens": 5.0},
			"cached_tokens":         99.0,
		}, 5, true},
		{"details 不是对象则看下一别名", map[string]any{
			"prompt_tokens_details": "bad",
			"cached_tokens":         8.0,
		}, 8, true},
		{"零值不算命中", map[string]any{"cached_tokens": 0.0}, 0, false},
		{"负值不算命中", map[string]any{"cached_tokens": -3.0}, 0, false},
		{"字符串不算命中", map[string]any{"cached_tokens": "12"}, 0, false},
		{"缺失", map[string]any{"prompt_tokens": 100.0}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := bestUsageCacheHitTokens(tc.usage)
			if got != tc.want || ok != tc.ok {
				t.Errorf("got %v/%v，应为 %v/%v", got, ok, tc.want, tc.ok)
			}
		})
	}

	if _, ok := UsageCacheHitTokens(nil); ok {
		t.Error("nil usage 不应有命中")
	}
	if got, ok := UsageCacheHitTokens(map[string]any{"cached_tokens": 4.0}); !ok || got != 4 {
		t.Errorf("UsageCacheHitTokens = %v/%v", got, ok)
	}
}

// TestPositiveUsageNumber 各数值类型的正数判定；非正/非数值/缺失都是 false。
func TestPositiveUsageNumber(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  float64
		ok    bool
	}{
		{"float64 正数", 1.5, 1.5, true},
		{"float64 零", 0.0, 0, false},
		{"float64 负数", -2.0, -2, false},
		{"float32 正数", float32(2.5), 2.5, true},
		{"float32 负数", float32(-1), -1, false},
		{"int 正数", 3, 3, true},
		{"int 零", 0, 0, false},
		{"int64 正数", int64(4), 4, true},
		{"int64 负数", int64(-4), -4, false},
		{"int32 正数", int32(5), 5, true},
		{"int32 零", int32(0), 0, false},
		{"uint 正数", uint(6), 6, true},
		{"uint 零", uint(0), 0, false},
		{"uint64 正数", uint64(7), 7, true},
		{"uint32 正数", uint32(8), 8, true},
		{"uint32 零", uint32(0), 0, false},
		{"字符串", "9", 0, false},
		{"nil", nil, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := positiveUsageNumber(tc.value)
			if got != tc.want || ok != tc.ok {
				t.Errorf("got %v/%v，应为 %v/%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestNormalizeUsageCacheAliases 有命中时克隆并回写全部别名；无命中原样返回同一 map；
// input_tokens_details 只在原本存在时更新；原 map 不被修改。
func TestNormalizeUsageCacheAliases(t *testing.T) {
	t.Run("无命中原样返回", func(t *testing.T) {
		usage := map[string]any{"prompt_tokens": 10.0, "cached_tokens": 0.0}
		got := normalizeUsageCacheAliases(usage)
		got["marker"] = "same"
		if usage["marker"] != "same" {
			t.Error("无命中时应返回原 map（同一对象）")
		}
	})

	t.Run("顶层命中补齐别名与 details", func(t *testing.T) {
		usage := map[string]any{"prompt_tokens": 10.0, "cached_tokens": 7.0}
		got := normalizeUsageCacheAliases(usage)
		for _, k := range []string{"cache_read_input_tokens", "cached_tokens", "prompt_cache_hit_tokens"} {
			if got[k] != 7.0 {
				t.Errorf("%s = %v，应为 7", k, got[k])
			}
		}
		details, ok := got["prompt_tokens_details"].(map[string]any)
		if !ok || details["cached_tokens"] != 7.0 {
			t.Errorf("prompt_tokens_details = %v", got["prompt_tokens_details"])
		}
		if _, exists := got["input_tokens_details"]; exists {
			t.Error("Chat 客户端不应凭空补 input_tokens_details")
		}
		if _, exists := usage["prompt_tokens_details"]; exists {
			t.Error("不得修改原 map")
		}
	})

	t.Run("已有 input_tokens_details 时克隆更新", func(t *testing.T) {
		inputDetails := map[string]any{"cached_tokens": 1.0, "audio_tokens": 2.0}
		promptDetails := map[string]any{"cached_tokens": 0.0}
		usage := map[string]any{
			"prompt_cache_hit_tokens": 3.0,
			"input_tokens_details":    inputDetails,
			"prompt_tokens_details":   promptDetails,
		}
		got := normalizeUsageCacheAliases(usage)
		gotInput := got["input_tokens_details"].(map[string]any)
		if gotInput["cached_tokens"] != 3.0 || gotInput["audio_tokens"] != 2.0 {
			t.Errorf("input_tokens_details = %v", gotInput)
		}
		gotPrompt := got["prompt_tokens_details"].(map[string]any)
		if gotPrompt["cached_tokens"] != 3.0 {
			t.Errorf("prompt_tokens_details = %v", gotPrompt)
		}
		// 原嵌套 map 不被改。
		if inputDetails["cached_tokens"] != 1.0 || promptDetails["cached_tokens"] != 0.0 {
			t.Error("不应修改原嵌套 details")
		}
	})

	t.Run("details 不是对象时补成新对象", func(t *testing.T) {
		usage := map[string]any{"cached_tokens": 4.0, "prompt_tokens_details": "bad"}
		got := normalizeUsageCacheAliases(usage)
		details, ok := got["prompt_tokens_details"].(map[string]any)
		if !ok || details["cached_tokens"] != 4.0 {
			t.Errorf("prompt_tokens_details = %v", got["prompt_tokens_details"])
		}
	})
}

// TestCloneUsage 克隆辅助：副本独立，非对象/缺失的 details 克隆为空 map。
func TestCloneUsage(t *testing.T) {
	orig := map[string]any{"a": 1, "nested": map[string]any{"x": 2}}
	cp := cloneUsageMap(orig)
	cp["a"] = 9
	if orig["a"] != 1 {
		t.Error("cloneUsageMap 应复制顶层")
	}
	cp["nested"].(map[string]any)["x"] = 9
	if orig["nested"].(map[string]any)["x"] != 9 {
		t.Error("cloneUsageMap 是浅拷贝，嵌套值应共享")
	}

	if d := cloneUsageDetails(map[string]any{"prompt_tokens_details": "bad"}, "prompt_tokens_details"); d == nil || len(d) != 0 {
		t.Errorf("非对象 details 应克隆为空 map，got %#v", d)
	}
	if d := cloneUsageDetails(map[string]any{}, "missing"); d == nil || len(d) != 0 {
		t.Errorf("缺失 details 应克隆为空 map，got %#v", d)
	}
	src := map[string]any{"details": map[string]any{"k": "v"}}
	if d := cloneUsageDetails(src, "details"); d["k"] != "v" {
		t.Errorf("应复制 details 键值，got %v", d)
	}
}

// TestUsageCacheMissTokens 未命中数：显式字段优先；否则 prompt−命中；为负/缺字段
// 视为不可信返回 false。
func TestUsageCacheMissTokens(t *testing.T) {
	cases := []struct {
		name  string
		usage map[string]any
		want  float64
		ok    bool
	}{
		{"nil", nil, 0, false},
		{"显式 miss 优先", map[string]any{"prompt_cache_miss_tokens": 5.0, "prompt_tokens": 100.0}, 5, true},
		{"推导 miss", map[string]any{"prompt_tokens": 100.0, "cached_tokens": 30.0}, 70, true},
		{"prompt 等于命中", map[string]any{"prompt_tokens": 30.0, "cached_tokens": 30.0}, 0, true},
		{"命中大于 prompt 不可信", map[string]any{"prompt_tokens": 20.0, "cached_tokens": 30.0}, 0, false},
		{"缺 prompt", map[string]any{"cached_tokens": 30.0}, 0, false},
		{"缺命中", map[string]any{"prompt_tokens": 100.0}, 0, false},
		{"显式 miss 非数值时回退推导", map[string]any{"prompt_cache_miss_tokens": "5", "prompt_tokens": 100.0, "cached_tokens": 40.0}, 60, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := UsageCacheMissTokens(tc.usage)
			if got != tc.want || ok != tc.ok {
				t.Errorf("got %v/%v，应为 %v/%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestUsageCacheTokens 统计层的缓存观测：命中为 0 也算有观测（整段未命中要计入命中率）；命中为 0
// 且没有显式 miss 时按 prompt_tokens 全量算未命中；一个缓存字段都没有时 ok=false。
func TestUsageCacheTokens(t *testing.T) {
	cases := []struct {
		name      string
		usage     map[string]any
		hit, miss float64
		ok        bool
	}{
		{"nil", nil, 0, 0, false},
		{"没有缓存字段", map[string]any{"prompt_tokens": 100.0}, 0, 0, false},
		{"命中与显式 miss", map[string]any{"prompt_tokens": 100.0, "prompt_cache_hit_tokens": 90.0, "prompt_cache_miss_tokens": 10.0}, 90, 10, true},
		{"命中推导 miss", map[string]any{"prompt_tokens": 100.0, "prompt_tokens_details": map[string]any{"cached_tokens": 60.0}}, 60, 40, true},
		{"命中为 0、无 miss，按 prompt 全量", map[string]any{"prompt_tokens": 288109.0, "prompt_cache_hit_tokens": 0.0}, 0, 288109, true},
		{"只有 miss 字段", map[string]any{"prompt_tokens": 50.0, "prompt_cache_miss_tokens": 50.0}, 0, 50, true},
		{"命中为 0、miss 远小于 prompt：反推命中", map[string]any{"prompt_tokens": 52703.0, "prompt_cache_hit_tokens": 0.0, "prompt_cache_miss_tokens": 3.0}, 52700, 3, true},
		{"没有命中字段、只有较小的 miss：反推命中", map[string]any{"prompt_tokens": 8500.0, "prompt_cache_miss_tokens": 100.0}, 8400, 100, true},
		{"miss 比 prompt 大：命中仍为 0", map[string]any{"prompt_tokens": 50.0, "prompt_cache_miss_tokens": 60.0}, 0, 60, true},
		{"只有 miss、没有 prompt", map[string]any{"prompt_cache_miss_tokens": 7.0}, 0, 7, true},
		{"别名都是 0", map[string]any{"cache_read_input_tokens": 0.0, "cached_tokens": 0.0}, 0, 0, true},
		{"Responses 形态的 0", map[string]any{"input_tokens_details": map[string]any{"cached_tokens": 0.0}}, 0, 0, true},
		{"details 里不是数值", map[string]any{"prompt_tokens_details": map[string]any{"cached_tokens": "x"}}, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hit, miss, ok := UsageCacheTokens(tc.usage)
			if hit != tc.hit || miss != tc.miss || ok != tc.ok {
				t.Errorf("got %v/%v/%v，应为 %v/%v/%v", hit, miss, ok, tc.hit, tc.miss, tc.ok)
			}
		})
	}
}

// TestUsageNumber 顶层数值字段解析：float64/int/int64/json.Number；非法与缺失 false。
func TestUsageNumber(t *testing.T) {
	cases := []struct {
		name  string
		usage map[string]any
		key   string
		want  float64
		ok    bool
	}{
		{"float64", map[string]any{"n": 1.5}, "n", 1.5, true},
		{"int", map[string]any{"n": 2}, "n", 2, true},
		{"int64", map[string]any{"n": int64(3)}, "n", 3, true},
		{"json.Number", map[string]any{"n": json.Number("4.5")}, "n", 4.5, true},
		{"json.Number 非法", map[string]any{"n": json.Number("x")}, "n", 0, false},
		{"缺失", map[string]any{}, "n", 0, false},
		{"字符串", map[string]any{"n": "5"}, "n", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := usageNumber(tc.usage, tc.key)
			if got != tc.want || ok != tc.ok {
				t.Errorf("got %v/%v，应为 %v/%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
