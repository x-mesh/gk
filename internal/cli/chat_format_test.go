package cli

import "testing"

func TestFormatChatTokens(t *testing.T) {
	cases := []struct {
		n      int64
		approx bool
		want   string
	}{
		{342, false, "342"},
		{3400, false, "3.4k"},
		{3400, true, "~3.4k"},
		{999, true, "~999"},
	}
	for _, c := range cases {
		if got := formatChatTokens(c.n, c.approx); got != c.want {
			t.Errorf("formatChatTokens(%d, %v) = %q, want %q", c.n, c.approx, got, c.want)
		}
	}
}
