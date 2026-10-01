package logfmt

import "testing"

func TestShortUA(t *testing.T) {
	cases := []struct {
		name string
		ua   string
		want string
	}{
		{"empty", "", ""},
		{"blank", "   ", ""},
		{"curl", "curl/8.4.0", "curl/8.4.0"},
		{"python", "python-requests/2.31.0", "python-requests/2.31.0"},
		{"openai", "OpenAI/Python 1.30.0", "OpenAI/Python"},
		{"workbuddy", "WorkBuddy/5.5.6 WorkBuddy/5.5.6 CLI/2.137.1", "WorkBuddy/5.5.6"},
		{
			"chrome skips engine tokens",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			"Chrome/120.0.0.0",
		},
		{
			"firefox",
			"Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0",
			"Firefox/121.0",
		},
		{"no version token", "node", "node"},
		{"only engine tokens falls back to whole", "Mozilla/5.0 AppleWebKit/537.36", "Mozilla/5.0 AppleWebKit/537.36"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShortUA(tc.ua); got != tc.want {
				t.Fatalf("ShortUA(%q) = %q want %q", tc.ua, got, tc.want)
			}
		})
	}
}

// ShortUA 的返回值必须有界：UA 是客户端可控自由文本，超长值不能原样带进日志列。
func TestShortUABounded(t *testing.T) {
	long := "VeryLongClientNameThatKeepsGoingAndGoing/1.2.3"
	if got := ShortUA(long); len(got) > maxShortUALen {
		t.Fatalf("len(ShortUA) = %d want <= %d (%q)", len(got), maxShortUALen, got)
	}
	// 没有 name/version token 时回落整串，同样受上限约束。
	got := ShortUA("x" + string(make([]byte, 0)) + "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy")
	if len(got) > maxShortUALen {
		t.Fatalf("fallback not truncated: %d", len(got))
	}
}
