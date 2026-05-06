package update

import "testing"

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.29.0", "v0.29.1", -1},
		{"v0.29.1", "v0.29.0", 1},
		{"v0.29.1", "v0.29.1", 0},
		{"v1.0.0", "v0.99.0", 1},
		{"0.29.1", "v0.29.1", 0},
		{"v0.29.0", "v0.30.0", -1},
		{"v0.29.0-rc1", "v0.29.0", 0}, // pre-release stripped
		{"dev", "v0.29.1", -1},        // dev always older than release
		{"v0.29.1", "dev", 1},
		{"dev", "dev", 0},
	}
	for _, tc := range cases {
		t.Run(tc.a+"_vs_"+tc.b, func(t *testing.T) {
			if got := CompareSemver(tc.a, tc.b); got != tc.want {
				t.Errorf("CompareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestFormatPlan(t *testing.T) {
	if got := FormatPlan("v0.29.0", "v0.30.0"); got != "0.29.0 → 0.30.0" {
		t.Errorf("FormatPlan = %q", got)
	}
	if got := FormatPlan("0.29.0", "0.30.0"); got != "0.29.0 → 0.30.0" {
		t.Errorf("FormatPlan(without v) = %q", got)
	}
}
