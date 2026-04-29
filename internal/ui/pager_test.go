package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// clearPagerEnv resets pager-related env vars so tests don't bleed into each other.
func clearPagerEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GK_PAGER", "")
	t.Setenv("PAGER", "")
	t.Setenv("NO_COLOR", "")
}

// makeFakeBinary creates an executable script named `name` inside dir and
// prepends dir to PATH so exec.LookPath finds it.
func makeFakeBinary(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	var content string
	if runtime.GOOS == "windows" {
		content = "@echo off\r\n"
		path += ".bat"
	} else {
		content = "#!/bin/sh\nexec cat\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("makeFakeBinary: %v", err)
	}
}

func prependPath(t *testing.T, dir string) {
	t.Helper()
	old := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+old)
}

// TestFilepathBase verifies basename extraction without importing path/filepath.
func TestFilepathBase(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"/usr/bin/delta", "delta"},
		{"delta", "delta"},
		{"/usr/local/bin/bat", "bat"},
		{"bat", "bat"},
		{"/less", "less"},
	}
	for _, c := range cases {
		got := filepathBase(c.input)
		if got != c.want {
			t.Errorf("filepathBase(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestDetect_NonTTY verifies that Detect() returns a disabled pager when stdout
// is not a TTY (which is always true in CI / test runs).
func TestDetect_NonTTY(t *testing.T) {
	clearPagerEnv(t)
	// In test environment IsTerminal() returns false because stdout is a pipe.
	p := Detect()
	if p.Kind != PagerNone {
		t.Errorf("expected PagerNone in non-TTY, got %q", p.Kind)
	}
	if !p.Disabled {
		t.Error("expected Disabled=true in non-TTY")
	}
}

// TestResolve_NotFound verifies that resolve returns nil for a non-existent binary.
func TestResolve_NotFound(t *testing.T) {
	clearPagerEnv(t)
	// Use a name that is highly unlikely to exist on PATH.
	got := resolve("__gk_no_such_pager_binary_xyz__")
	if got != nil {
		t.Errorf("expected nil for missing binary, got %+v", got)
	}
}

// TestResolve_KnownBinary_Delta verifies Kind, Path, and default args for delta.
func TestResolve_KnownBinary_Delta(t *testing.T) {
	clearPagerEnv(t)
	dir := t.TempDir()
	makeFakeBinary(t, dir, "delta")
	prependPath(t, dir)

	p := resolve("delta")
	if p == nil {
		t.Fatal("resolve(delta) returned nil")
		return
	}
	if p.Kind != PagerDelta {
		t.Errorf("Kind = %q, want %q", p.Kind, PagerDelta)
	}
	if !strings.Contains(p.Path, "delta") {
		t.Errorf("Path %q does not contain 'delta'", p.Path)
	}
	if !contains(p.Args, "--paging=always") {
		t.Errorf("Args missing --paging=always: %v", p.Args)
	}
}

// TestResolve_KnownBinary_Bat verifies Kind and default args for bat.
func TestResolve_KnownBinary_Bat(t *testing.T) {
	clearPagerEnv(t)
	dir := t.TempDir()
	makeFakeBinary(t, dir, "bat")
	prependPath(t, dir)

	p := resolve("bat")
	if p == nil {
		t.Fatal("resolve(bat) returned nil")
	}
	if p.Kind != PagerBat {
		t.Errorf("Kind = %q, want %q", p.Kind, PagerBat)
	}
	if !contains(p.Args, "--paging=always") {
		t.Errorf("Args missing --paging=always: %v", p.Args)
	}
	if !contains(p.Args, "--style=plain") {
		t.Errorf("Args missing --style=plain: %v", p.Args)
	}
}

// TestResolve_KnownBinary_Less verifies Kind and default args for less.
func TestResolve_KnownBinary_Less(t *testing.T) {
	clearPagerEnv(t)
	dir := t.TempDir()
	makeFakeBinary(t, dir, "less")
	prependPath(t, dir)

	p := resolve("less")
	if p == nil {
		t.Fatal("resolve(less) returned nil")
	}
	if p.Kind != PagerLess {
		t.Errorf("Kind = %q, want %q", p.Kind, PagerLess)
	}
	if !contains(p.Args, "-R") {
		t.Errorf("Args missing -R: %v", p.Args)
	}
	if !contains(p.Args, "-F") {
		t.Errorf("Args missing -F: %v", p.Args)
	}
}

// TestResolve_UserArgs verifies that extra tokens after the binary name are
// appended to Args.
func TestResolve_UserArgs(t *testing.T) {
	clearPagerEnv(t)
	dir := t.TempDir()
	makeFakeBinary(t, dir, "delta")
	prependPath(t, dir)

	p := resolve("delta --line-numbers")
	if p == nil {
		t.Fatal("resolve returned nil")
	}
	if !contains(p.Args, "--line-numbers") {
		t.Errorf("expected --line-numbers in args: %v", p.Args)
	}
}

// TestResolve_NoColor verifies that NO_COLOR=1 causes bat to include --color=never.
func TestResolve_NoColor(t *testing.T) {
	clearPagerEnv(t)
	t.Setenv("NO_COLOR", "1")
	dir := t.TempDir()
	makeFakeBinary(t, dir, "bat")
	prependPath(t, dir)

	p := resolve("bat")
	if p == nil {
		t.Fatal("resolve(bat) returned nil")
	}
	if !contains(p.Args, "--color=never") {
		t.Errorf("expected --color=never in args when NO_COLOR=1: %v", p.Args)
	}
}

// TestResolve_NoColor_Delta verifies delta gets --features no-color when NO_COLOR=1.
func TestResolve_NoColor_Delta(t *testing.T) {
	clearPagerEnv(t)
	t.Setenv("NO_COLOR", "1")
	dir := t.TempDir()
	makeFakeBinary(t, dir, "delta")
	prependPath(t, dir)

	p := resolve("delta")
	if p == nil {
		t.Fatal("resolve(delta) returned nil")
	}
	if !contains(p.Args, "--features") {
		t.Errorf("expected --features in args when NO_COLOR=1: %v", p.Args)
	}
	idx := indexOf(p.Args, "--features")
	if idx < 0 || idx+1 >= len(p.Args) || p.Args[idx+1] != "no-color" {
		t.Errorf("expected --features no-color in args: %v", p.Args)
	}
}

// TestRun_Disabled verifies that a disabled Pager's Run() returns a wrapper
// around os.Stdout and a no-op wait function.
func TestRun_Disabled(t *testing.T) {
	p := &Pager{Kind: PagerNone, Disabled: true}
	w, wait, err := p.Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if w == nil {
		t.Fatal("Run() returned nil writer")
	}
	if wait == nil {
		t.Fatal("Run() returned nil wait func")
	}
	if err := wait(); err != nil {
		t.Errorf("wait() returned non-nil error: %v", err)
	}
}

// TestPager_String verifies human-readable output.
func TestPager_String(t *testing.T) {
	disabled := &Pager{Kind: PagerNone, Disabled: true}
	if s := disabled.String(); s != "none (disabled)" {
		t.Errorf("disabled String() = %q", s)
	}

	active := &Pager{Kind: PagerLess, Path: "/usr/bin/less"}
	got := active.String()
	if !strings.Contains(got, "less") || !strings.Contains(got, "/usr/bin/less") {
		t.Errorf("active String() = %q, want it to contain kind and path", got)
	}
}

// contains reports whether slice contains s.
func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// indexOf returns the first index of s in slice, or -1.
func indexOf(slice []string, s string) int {
	for i, v := range slice {
		if v == s {
			return i
		}
	}
	return -1
}
