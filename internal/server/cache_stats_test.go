// cache_stats_test.go 钉住缓存命中率观测链路（issue #92）：SSE 末帧的
// prompt_cache_hit/miss_tokens 采集，以及「曾命中却整段未命中」的 WARN 信号判据。
package server

import (
	"strings"
	"testing"
	"time"
)

func feedStatsLines(s *chatStatsReader, lines ...string) {
	for _, l := range lines {
		s.parseSSELine(l)
	}
}

// 流式末帧带 hit/miss：CacheTokens 原样返回；只带 hit 时 miss 按 prompt-hit 推导；
// 上游完全没回缓存维度时 ok=false（不参与统计）。
func TestStatsReaderCacheTokens(t *testing.T) {
	s := newChatStatsReaderSince(strings.NewReader(""), time.Now())
	feedStatsLines(s,
		`data: {"usage":{"prompt_tokens":1000,"completion_tokens":5,"total_tokens":1005,"credit":0.1,"prompt_cache_hit_tokens":900,"prompt_cache_miss_tokens":100}}`,
	)
	if hit, miss, ok := s.CacheTokens(); !ok || hit != 900 || miss != 100 {
		t.Fatalf("hit=%d miss=%d ok=%v, want 900/100/true", hit, miss, ok)
	}

	s2 := newChatStatsReaderSince(strings.NewReader(""), time.Now())
	feedStatsLines(s2,
		`data: {"usage":{"prompt_tokens":8500,"prompt_cache_hit_tokens":8400}}`,
	)
	if hit, miss, ok := s2.CacheTokens(); !ok || hit != 8400 || miss != 100 {
		t.Fatalf("derived miss: hit=%d miss=%d ok=%v, want 8400/100/true", hit, miss, ok)
	}

	s3 := newChatStatsReaderSince(strings.NewReader(""), time.Now())
	feedStatsLines(s3,
		`data: {"usage":{"prompt_tokens":100,"completion_tokens":5}}`,
	)
	if _, _, ok := s3.CacheTokens(); ok {
		t.Fatal("无缓存字段应 ok=false")
	}
}

// WARN 信号状态机：先命中、再大前缀整段未命中 → 触发一次；冷却内重复未命中
// 不再触发；从未命中的模型（首请求天然全 miss）与小前缀不触发。
func TestCacheMissSignal(t *testing.T) {
	old := cacheMissWarnEvery
	cacheMissWarnEvery = time.Hour // 拉长冷却，验证「冷却内不重复」
	t.Cleanup(func() {
		cacheMissWarnEvery = old
		cacheMissWarn = &cacheMissSignal{everHit: map[string]bool{}, last: map[string]time.Time{}}
	})
	cacheMissWarn = &cacheMissSignal{everHit: map[string]bool{}, last: map[string]time.Time{}}

	cacheMissWarn.noteCacheTokens("glm-5.3", 8500, 8000, 500) // 命中 → 标记可命中
	if cacheMissWarn.last["glm-5.3"] != (time.Time{}) {
		t.Fatal("命中不应触发 WARN")
	}
	cacheMissWarn.noteCacheTokens("glm-5.3", 8500, 0, 8500) // 整段未命中 → 触发
	if cacheMissWarn.last["glm-5.3"].IsZero() {
		t.Fatal("曾命中的模型整段未命中应触发 WARN 记录")
	}
	first := cacheMissWarn.last["glm-5.3"]
	cacheMissWarn.noteCacheTokens("glm-5.3", 8500, 0, 8500) // 冷却内 → 不重复
	if !cacheMissWarn.last["glm-5.3"].Equal(first) {
		t.Fatal("冷却期内不应重复触发")
	}

	// 从未命中的模型（新模型首请求天然全 miss）不触发。
	cacheMissWarn.noteCacheTokens("new-model", 5000, 0, 5000)
	if !cacheMissWarn.last["new-model"].IsZero() {
		t.Fatal("从未命中的模型不应触发")
	}
	// 小前缀不触发。
	cacheMissWarn.noteCacheTokens("glm-5.3", 500, 0, 500)
	// miss 缺失（上游只回 hit=0、无 miss、无 prompt）不触发。
	cacheMissWarn.noteCacheTokens("glm-5.3", 0, 0, 0)
}
