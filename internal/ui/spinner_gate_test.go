package ui

import "testing"

// A spinner draws only where its frames are erased: a terminal on stderr,
// outside CI, on a terminal that understands cursor control, and when the
// caller has not turned spinners off.
func TestSpinnerAllowedFor(t *testing.T) {
	for _, c := range []struct {
		name  string
		tty   bool
		ci    string
		term  string
		off   bool
		allow bool
	}{
		{"terminal", true, "", "xterm-256color", false, true},
		{"not a terminal", false, "", "xterm-256color", false, false},
		{"CI=true", true, "true", "xterm-256color", false, false},
		{"CI=1", true, "1", "xterm-256color", false, false},
		{"CI=false", true, "false", "xterm-256color", false, true},
		{"CI=0", true, "0", "xterm-256color", false, true},
		{"TERM=dumb", true, "", "dumb", false, false},
		{"TERM unset", true, "", "", false, true},
		{"turned off by the caller", true, "", "xterm-256color", true, false},
	} {
		if got := spinnerAllowedFor(c.tty, c.ci, c.term, c.off); got != c.allow {
			t.Errorf("%s: spinnerAllowedFor = %v, want %v", c.name, got, c.allow)
		}
	}
}

func TestSpinnerOffHook(t *testing.T) {
	prev := SpinnerOff
	t.Cleanup(func() { SpinnerOff = prev })
	SpinnerOff = nil
	if spinnerOffNow() {
		t.Error("no hook turns spinners off")
	}
	SpinnerOff = func() bool { return true }
	if !spinnerOffNow() {
		t.Error("the hook does not turn spinners off")
	}
}

func TestEnvTruthy(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0": false, "false": false, "No": false, " off ": false, "true": true, "1": true, "yes": true} {
		if EnvTruthy(v) != want {
			t.Errorf("EnvTruthy(%q) = %v, want %v", v, !want, want)
		}
	}
}
