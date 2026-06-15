package cli

import (
	"testing"
	"time"
)

func TestClampWatchInterval(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{0, watchMinInterval},
		{50 * time.Millisecond, watchMinInterval},
		{watchMinInterval, watchMinInterval},
		{2 * time.Second, 2 * time.Second},
		{watchMaxInterval, watchMaxInterval},
		{2 * watchMaxInterval, watchMaxInterval},
	}
	for _, tc := range cases {
		if got := clampWatchInterval(tc.in); got != tc.want {
			t.Errorf("clampWatchInterval(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestWatchIntervalValue(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"100", 100 * time.Second, false}, // bare number → seconds (the ask)
		{"2", 2 * time.Second, false},
		{"0.5", 500 * time.Millisecond, false},
		{"500ms", 500 * time.Millisecond, false}, // duration still works
		{"2s", 2 * time.Second, false},
		{"1m", time.Minute, false},
		{" 3 ", 3 * time.Second, false}, // trimmed
		{"0", 0, true},                  // non-positive rejected
		{"-3", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		var d time.Duration
		err := (watchIntervalValue{d: &d}).Set(c.in)
		if c.err {
			if err == nil {
				t.Errorf("Set(%q): want error, got %v", c.in, d)
			}
			continue
		}
		if err != nil {
			t.Errorf("Set(%q): unexpected error %v", c.in, err)
			continue
		}
		if d != c.want {
			t.Errorf("Set(%q) = %v, want %v", c.in, d, c.want)
		}
	}
	var z time.Duration
	if got := (watchIntervalValue{d: &z}).String(); got != "2s" {
		t.Errorf("String() default = %q, want 2s", got)
	}
}

func TestTruncateForHeader(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"abcdef", 6, "abcdef"},
		{"abcdefg", 6, "abcde…"},
		{"x", 1, "x"},
		{"xy", 1, "…"},
	}
	for _, tc := range cases {
		if got := truncateForHeader(tc.in, tc.max); got != tc.want {
			t.Errorf("truncateForHeader(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}
