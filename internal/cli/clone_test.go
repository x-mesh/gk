package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
)

func defaultCloneCfg() config.CloneConfig {
	return config.CloneConfig{
		DefaultProtocol: "ssh",
		DefaultHost:     "github.com",
	}
}

func TestResolveCloneURL_OwnerRepoSSH(t *testing.T) {
	got, meta, err := resolveCloneURL("JINWOO-J/playground", defaultCloneCfg(), false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "git@github.com:JINWOO-J/playground.git"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if meta.Host != "github.com" || meta.Owner != "JINWOO-J" || meta.Repo != "playground" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestResolveCloneURL_OwnerRepoHTTPSDefault(t *testing.T) {
	cfg := defaultCloneCfg()
	cfg.DefaultProtocol = "https"
	got, _, err := resolveCloneURL("foo/bar", cfg, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://github.com/foo/bar.git"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveCloneURL_ForceFlags(t *testing.T) {
	cfg := defaultCloneCfg()
	// SSH default + --https → https
	got, _, err := resolveCloneURL("foo/bar", cfg, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "https://") {
		t.Errorf("--https override failed: %q", got)
	}

	// HTTPS default + --ssh → ssh
	cfg.DefaultProtocol = "https"
	got, _, err = resolveCloneURL("foo/bar", cfg, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "git@") {
		t.Errorf("--ssh override failed: %q", got)
	}
}

func TestResolveCloneURL_SchemeURLsPassThrough(t *testing.T) {
	cases := []string{
		"https://github.com/foo/bar.git",
		"http://example.com/x/y",
		"ssh://git@host/foo/bar",
		"git://example.org/proj",
		"file:///tmp/repo.git",
	}
	for _, in := range cases {
		got, _, err := resolveCloneURL(in, defaultCloneCfg(), false, false)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", in, err)
			continue
		}
		if got != in {
			t.Errorf("%q was rewritten to %q", in, got)
		}
	}
}

func TestResolveCloneURL_SCPStylePassesThrough(t *testing.T) {
	in := "git@github.com:foo/bar.git"
	got, meta, err := resolveCloneURL(in, defaultCloneCfg(), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Errorf("SCP URL was rewritten: %q → %q", in, got)
	}
	if meta.Host != "github.com" || meta.Owner != "foo" || meta.Repo != "bar" {
		t.Errorf("SCP meta parse failed: %+v", meta)
	}
}

func TestResolveCloneURL_AliasExpansion(t *testing.T) {
	cfg := defaultCloneCfg()
	cfg.Hosts = map[string]config.HostAlias{
		"gl":   {Host: "gitlab.com", Protocol: "ssh"},
		"work": {Host: "git.company.internal", Protocol: "https"},
	}

	got, meta, err := resolveCloneURL("gl:team/svc", cfg, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "git@gitlab.com:team/svc.git" {
		t.Errorf("gl alias got %q", got)
	}
	if meta.Host != "gitlab.com" {
		t.Errorf("gl meta host %q", meta.Host)
	}

	got, _, err = resolveCloneURL("work:platform/api", cfg, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://git.company.internal/platform/api.git" {
		t.Errorf("work alias got %q", got)
	}

	// --ssh override wins over alias protocol.
	got, _, err = resolveCloneURL("work:platform/api", cfg, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "git@") {
		t.Errorf("--ssh did not override alias protocol: %q", got)
	}
}

func TestResolveCloneURL_UnknownAliasPassesThrough(t *testing.T) {
	// Unknown "host:port/path" inputs are handed to git verbatim.
	cfg := defaultCloneCfg()
	got, _, err := resolveCloneURL("unknown:foo/bar", cfg, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "unknown:foo/bar" {
		t.Errorf("unknown alias should passthrough, got %q", got)
	}
}

func TestResolveCloneURL_StripsDotGitSuffix(t *testing.T) {
	got, _, err := resolveCloneURL("foo/bar.git", defaultCloneCfg(), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "git@github.com:foo/bar.git" {
		t.Errorf("got %q", got)
	}
}

func TestResolveCloneURL_MalformedShorthandErrors(t *testing.T) {
	cases := []string{"", "onlyone", "a/b/c", "/bar", "foo/"}
	for _, in := range cases {
		if _, _, err := resolveCloneURL(in, defaultCloneCfg(), false, false); err == nil {
			t.Errorf("%q: expected error, got nil", in)
		}
	}
}

func TestComputeCloneTarget_ExplicitWins(t *testing.T) {
	cfg := defaultCloneCfg()
	cfg.Root = "/tmp/work"
	got := computeCloneTarget(cfg, "./somewhere", cloneMeta{Host: "github.com", Owner: "x", Repo: "y"})
	if got != "./somewhere" {
		t.Errorf("explicit target should win, got %q", got)
	}
}

func TestComputeCloneTarget_RootLayout(t *testing.T) {
	cfg := defaultCloneCfg()
	cfg.Root = "/tmp/work"
	got := computeCloneTarget(cfg, "", cloneMeta{Host: "github.com", Owner: "JINWOO-J", Repo: "playground"})
	want := filepath.Join("/tmp/work", "github.com", "JINWOO-J", "playground")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComputeCloneTarget_RootUnsetReturnsEmpty(t *testing.T) {
	got := computeCloneTarget(defaultCloneCfg(), "", cloneMeta{Host: "github.com", Owner: "a", Repo: "b"})
	if got != "" {
		t.Errorf("no root → empty, got %q", got)
	}
}

func TestComputeCloneTarget_OpaqueURLSkipsRoot(t *testing.T) {
	cfg := defaultCloneCfg()
	cfg.Root = "/tmp/work"
	// No meta → we cannot synthesize the layout path, so fall back.
	got := computeCloneTarget(cfg, "", cloneMeta{})
	if got != "" {
		t.Errorf("opaque meta → empty, got %q", got)
	}
}

func TestInferClonedDir(t *testing.T) {
	cases := map[string]string{
		"git@github.com:foo/bar.git":     "bar",
		"https://github.com/foo/bar.git": "bar",
		"https://example.com/foo/bar":    "bar",
		"ssh://git@host/x/y.git":         "y",
	}
	for in, want := range cases {
		if got := inferClonedDir(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
