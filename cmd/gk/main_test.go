package main

import (
	"runtime/debug"
	"testing"
)

func TestVCSFallbackFromSettings_FillsFromVCS(t *testing.T) {
	settings := []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abcdef0123456789"},
		{Key: "vcs.time", Value: "2026-04-30T00:00:00Z"},
	}
	gotCommit, gotDate := vcsFallbackFromSettings("none", "unknown", settings)
	if gotCommit != "abcdef0" {
		t.Errorf("commit: want %q, got %q", "abcdef0", gotCommit)
	}
	if gotDate != "2026-04-30T00:00:00Z" {
		t.Errorf("date: want %q, got %q", "2026-04-30T00:00:00Z", gotDate)
	}
}

func TestVCSFallbackFromSettings_DirtyAppliesWhenCommitFromVCS(t *testing.T) {
	settings := []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abcdef0123456789"},
		{Key: "vcs.modified", Value: "true"},
	}
	gotCommit, _ := vcsFallbackFromSettings("none", "unknown", settings)
	if gotCommit != "abcdef0-dirty" {
		t.Errorf("commit: plain `go build` with dirty tree should mark -dirty, got %q", gotCommit)
	}
}

// Regression for the v0.21.0 release that shipped with -dirty: ldflags
// supplied the commit, but goreleaser's `go mod tidy` before-hook left
// vcs.modified=true in BuildInfo, so vcsFallback appended -dirty to a
// clean release binary. With the fromVCS guard, ldflags-set commits are
// no longer affected by vcs.modified.
func TestVCSFallbackFromSettings_DirtyIgnoredWhenCommitFromLdflags(t *testing.T) {
	settings := []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abcdef0123456789"},
		{Key: "vcs.modified", Value: "true"},
	}
	preset := "50d6101"
	gotCommit, _ := vcsFallbackFromSettings(preset, "2026-04-30T00:00:00Z", settings)
	if gotCommit != preset {
		t.Errorf("commit: ldflags-set commit must not gain -dirty from BuildInfo, want %q got %q", preset, gotCommit)
	}
}

func TestVCSFallbackFromSettings_LdflagsTakePrecedence(t *testing.T) {
	// Both vcs.revision AND a pre-set commit — pre-set wins, vcs is ignored.
	settings := []debug.BuildSetting{
		{Key: "vcs.revision", Value: "deadbeefcafe1234"},
		{Key: "vcs.time", Value: "1999-01-01T00:00:00Z"},
	}
	gotCommit, gotDate := vcsFallbackFromSettings("50d6101", "2026-04-30T00:00:00Z", settings)
	if gotCommit != "50d6101" {
		t.Errorf("ldflags commit must win over vcs.revision, got %q", gotCommit)
	}
	if gotDate != "2026-04-30T00:00:00Z" {
		t.Errorf("ldflags date must win over vcs.time, got %q", gotDate)
	}
}

func TestVCSFallbackFromSettings_NoSettings(t *testing.T) {
	gotCommit, gotDate := vcsFallbackFromSettings("none", "unknown", nil)
	if gotCommit != "none" || gotDate != "unknown" {
		t.Errorf("empty settings should leave defaults, got commit=%q date=%q", gotCommit, gotDate)
	}
}

func TestVCSFallbackFromSettings_VCSRevisionTooShort(t *testing.T) {
	// Defensive: a 6-char revision (impossible in practice for git) is
	// not used. Guards against a regression that loosens the length check.
	settings := []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}}
	gotCommit, _ := vcsFallbackFromSettings("none", "unknown", settings)
	if gotCommit != "none" {
		t.Errorf("revision shorter than 7 chars must be ignored, got %q", gotCommit)
	}
}
