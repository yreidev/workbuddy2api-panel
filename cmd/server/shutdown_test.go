package main

import (
	"testing"
	"time"
)

// WB2A_SHUTDOWN_TIMEOUT：未设置保持历史行为（5 秒）；合法正时长生效；非法或非正数回落 5 秒并报错。
func TestShutdownGrace(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 5 * time.Second, false},
		{"570s", 570 * time.Second, false},
		{"10m", 10 * time.Minute, false},
		{"1h30m", 90 * time.Minute, false},
		{"abc", 5 * time.Second, true},
		{"570", 5 * time.Second, true}, // 缺单位
		{"0s", 5 * time.Second, true},
		{"-1s", 5 * time.Second, true},
	}
	for _, c := range cases {
		got, err := shutdownGrace(c.in)
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("shutdownGrace(%q) = %v, err=%v; want %v, err=%v", c.in, got, err, c.want, c.wantErr)
		}
	}
}
