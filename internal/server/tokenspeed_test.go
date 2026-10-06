package server

import (
	"math"
	"testing"
	"time"
)

// tokensPerSecond：分母应是「生成耗时」= 端到端 - TTFB（issue #34）。
//
// 这条测试的价值在于把"没有观测就不扣"这一条钉住：非流式回复没有首个 data 帧，
// 若实现改成"没测到就按某个默认 TTFB 扣"，速率会虚高，而单看代码很难发现。
func TestTokensPerSecond(t *testing.T) {
	cases := []struct {
		name      string
		tokens    int64
		total     time.Duration
		ttfb      time.Duration
		want      float64
		wantOk    bool
		tolerance float64
	}{
		{"无 TTFB 观测（非流式）按端到端算", 100, 2 * time.Second, 0, 50, true, 0.01},
		{"扣掉 TTFB：1200ms 里等了 200ms", 100, 1200 * time.Millisecond, 200 * time.Millisecond, 100, true, 0.01},
		{"TTFB 占总耗时一半", 50, time.Second, 500 * time.Millisecond, 100, true, 0.01},
		{"TTFB 超过总耗时 → 退回端到端，不得负/零分母", 100, 100 * time.Millisecond, 500 * time.Millisecond, 1000, true, 0.01},
		{"TTFB 恰好等于总耗时 → 退回端到端", 100, 100 * time.Millisecond, 100 * time.Millisecond, 1000, true, 0.01},
		{"token 为负哨兵（观测缺失）", -1, time.Second, 100 * time.Millisecond, 0, false, 0},
		{"零耗时", 10, 0, 0, 0, false, 0},
		{"零 token 但有效耗时", 0, time.Second, 0, 0, true, 0.01},
	}
	for _, c := range cases {
		got, ok := tokensPerSecond(c.tokens, c.total, c.ttfb)
		if ok != c.wantOk {
			t.Errorf("%s: ok=%v want %v", c.name, ok, c.wantOk)
			continue
		}
		if !ok {
			continue
		}
		if math.Abs(got-c.want) > c.tolerance {
			t.Errorf("%s: got %.3f want %.3f", c.name, got, c.want)
		}
	}
}

// TestTokensPerSecondTTFBMonotonic 语义回归：TTFB 越大，扣减越多、速率越高。
// 这正是 issue #34 的核心——修复前两者相等（TTFB 完全不影响结果）。
func TestTokensPerSecondTTFBMonotonic(t *testing.T) {
	const tok = 200
	total := 2 * time.Second
	without, _ := tokensPerSecond(tok, total, 0)
	with200, _ := tokensPerSecond(tok, total, 200*time.Millisecond)
	with800, _ := tokensPerSecond(tok, total, 800*time.Millisecond)

	if !(without < with200 && with200 < with800) {
		t.Fatalf("速率应随 TTFB 增大而上升：ttfb=0 -> %.2f, 200ms -> %.2f, 800ms -> %.2f",
			without, with200, with800)
	}
	// 修复前的公式正是 without（等于端到端口径），确认它确实更低。
	if math.Abs(without-100) > 0.01 {
		t.Errorf("无 TTFB 时应等于端到端口径 100，得到 %.2f", without)
	}
	if math.Abs(with200-111.11) > 0.05 {
		t.Errorf("扣 200ms 后应约 111.11，得到 %.2f", with200)
	}
}
