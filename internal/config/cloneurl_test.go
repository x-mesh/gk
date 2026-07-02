package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testCloneCfg() CloneConfig {
	return CloneConfig{
		DefaultProtocol: "ssh",
		DefaultHost:     "github.com",
	}
}

func TestResolveURL_Basic(t *testing.T) {
	cases := []struct {
		name       string
		cfg        CloneConfig
		spec       string
		forceSSH   bool
		forceHTTPS bool
		want       string
		wantMeta   CloneMeta
	}{
		{
			name: "owner/repo ssh default",
			cfg:  testCloneCfg(), spec: "JINWOO-J/playground",
			want:     "git@github.com:JINWOO-J/playground.git",
			wantMeta: CloneMeta{Host: "github.com", Owner: "JINWOO-J", Repo: "playground"},
		},
		{
			name: "owner/repo https default",
			cfg:  CloneConfig{DefaultProtocol: "https", DefaultHost: "github.com"}, spec: "foo/bar",
			want:     "https://github.com/foo/bar.git",
			wantMeta: CloneMeta{Host: "github.com", Owner: "foo", Repo: "bar"},
		},
		{
			name: "force https over ssh default",
			cfg:  testCloneCfg(), spec: "foo/bar", forceHTTPS: true,
			want:     "https://github.com/foo/bar.git",
			wantMeta: CloneMeta{Host: "github.com", Owner: "foo", Repo: "bar"},
		},
		{
			name: "scheme URL passthrough",
			cfg:  testCloneCfg(), spec: "https://github.com/foo/bar.git",
			want:     "https://github.com/foo/bar.git",
			wantMeta: CloneMeta{Host: "github.com", Owner: "foo", Repo: "bar"},
		},
		{
			name: "scp URL passthrough",
			cfg:  testCloneCfg(), spec: "git@github.com:foo/bar.git",
			want:     "git@github.com:foo/bar.git",
			wantMeta: CloneMeta{Host: "github.com", Owner: "foo", Repo: "bar"},
		},
		{
			name: "unknown alias passthrough",
			cfg:  testCloneCfg(), spec: "unknown:foo/bar",
			want:     "unknown:foo/bar",
			wantMeta: CloneMeta{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, meta, err := tc.cfg.ResolveURL(tc.spec, tc.forceSSH, tc.forceHTTPS)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("url = %q, want %q", got, tc.want)
			}
			if meta != tc.wantMeta {
				t.Errorf("meta = %+v, want %+v", meta, tc.wantMeta)
			}
		})
	}
}

func TestResolveURL_MalformedErrors(t *testing.T) {
	for _, in := range []string{"", "onlyone", "a/b/c", "/bar", "foo/"} {
		if _, _, err := testCloneCfg().ResolveURL(in, false, false); err == nil {
			t.Errorf("%q: expected error, got nil", in)
		}
	}
}

func TestResolveURL_OwnerCompletion(t *testing.T) {
	cfg := testCloneCfg()
	cfg.Hosts = map[string]HostAlias{
		"personal": {Host: "github.com", Owner: "JINWOO-J"},
		"work":     {Host: "github.com", Owner: "42tape", Protocol: "https"},
	}

	got, meta, err := cfg.ResolveURL("personal:repo", false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "git@github.com:JINWOO-J/repo.git" {
		t.Errorf("owner completion url = %q", got)
	}
	if meta != (CloneMeta{Host: "github.com", Owner: "JINWOO-J", Repo: "repo"}) {
		t.Errorf("meta = %+v", meta)
	}

	// Profile protocol is honoured on the completed form.
	got, _, err = cfg.ResolveURL("work:svc", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://github.com/42tape/svc.git" {
		t.Errorf("work completion url = %q", got)
	}

	// `.git` suffix on the bare repo part is tolerated.
	got, _, err = cfg.ResolveURL("personal:repo.git", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "git@github.com:JINWOO-J/repo.git" {
		t.Errorf(".git suffix url = %q", got)
	}

	// Explicit owner/repo through an owner-bearing alias keeps the
	// explicit owner (completion only fills a missing one).
	got, _, err = cfg.ResolveURL("personal:other/thing", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "git@github.com:other/thing.git" {
		t.Errorf("explicit owner url = %q", got)
	}
}

func TestResolveURL_OwnerCompletionWithoutOwnerErrors(t *testing.T) {
	cfg := testCloneCfg()
	cfg.Hosts = map[string]HostAlias{
		"gl": {Host: "gitlab.com"}, // legacy alias, no owner
	}

	_, _, err := cfg.ResolveURL("gl:repo", false, false)
	if err == nil {
		t.Fatal("expected error for ownerless alias:repo, got nil")
	}
	if !strings.Contains(err.Error(), "no owner configured") {
		t.Errorf("error should name the missing owner, got: %v", err)
	}

	// Backward compat: the same alias still works with an explicit owner.
	got, _, err := cfg.ResolveURL("gl:team/svc", false, false)
	if err != nil {
		t.Fatalf("legacy alias form broke: %v", err)
	}
	if got != "git@gitlab.com:team/svc.git" {
		t.Errorf("legacy alias url = %q", got)
	}

	// Empty rest (`gl:`) stays a malformed error, not owner completion.
	if _, _, err := cfg.ResolveURL("gl:", false, false); err == nil {
		t.Error("expected error for empty rest, got nil")
	}
}

func TestResolveURL_SSHHost(t *testing.T) {
	cfg := testCloneCfg()
	cfg.Hosts = map[string]HostAlias{
		"corp": {Host: "github.com", Owner: "acme", SSHHost: "github.com-acme"},
	}

	// ssh URLs carry the ssh_host transport alias...
	got, meta, err := cfg.ResolveURL("corp:tool", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "git@github.com-acme:acme/tool.git" {
		t.Errorf("ssh_host url = %q", got)
	}
	// ...while the structured meta keeps the canonical host.
	if meta.Host != "github.com" {
		t.Errorf("meta.Host = %q, want canonical github.com", meta.Host)
	}

	// https URLs never use the ssh transport alias.
	got, _, err = cfg.ResolveURL("corp:tool", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://github.com/acme/tool.git" {
		t.Errorf("https should ignore ssh_host, got %q", got)
	}

	// Explicit owner/repo through the alias also gets the ssh_host.
	got, _, err = cfg.ResolveURL("corp:other/repo", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "git@github.com-acme:other/repo.git" {
		t.Errorf("ssh_host with explicit owner = %q", got)
	}
}

func TestCloneHostsOrder(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.yaml")
	local := filepath.Join(dir, ".gk.yaml")

	// Deliberately non-alphabetical, with mixed case; the local file
	// overrides one alias (must keep its global position) and adds one.
	if err := os.WriteFile(global, []byte(
		"clone:\n  hosts:\n    zeta: { host: z.example }\n    Alpha: { host: a.example }\n    mid: { host: m.example }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte(
		"clone:\n  hosts:\n    alpha: { protocol: https }\n    beta: { host: b.example }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := cloneHostsOrder(global, local)
	want := []string{"zeta", "alpha", "mid", "beta"}
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}

	// Missing / malformed files contribute nothing, never error.
	if got := cloneHostsOrder(filepath.Join(dir, "nope.yaml")); got != nil {
		t.Errorf("missing file order = %v, want nil", got)
	}
	broken := filepath.Join(dir, "broken.yaml")
	os.WriteFile(broken, []byte(":\t not yaml ["), 0o644)
	if got := cloneHostsOrder(broken); got != nil {
		t.Errorf("broken file order = %v, want nil", got)
	}
	// hosts absent → nil.
	nohosts := filepath.Join(dir, "nohosts.yaml")
	os.WriteFile(nohosts, []byte("clone:\n  default_protocol: ssh\n"), 0o644)
	if got := cloneHostsOrder(nohosts); got != nil {
		t.Errorf("no-hosts order = %v, want nil", got)
	}
}

func TestLoad_PopulatesHostsOrder(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "gk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "clone:\n  hosts:\n    work:     { host: github.com, owner: acme }\n    personal: { host: github.com, owner: me }\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Clone.HostsOrder) < 2 || cfg.Clone.HostsOrder[0] != "work" || cfg.Clone.HostsOrder[1] != "personal" {
		t.Errorf("HostsOrder = %v, want [work personal ...]", cfg.Clone.HostsOrder)
	}
}

func TestResolveURL_LegacyAliasUnchanged(t *testing.T) {
	// Aliases without owner/ssh_host behave exactly as before the fields
	// existed — same URLs, same metadata, same protocol fallbacks.
	cfg := testCloneCfg()
	cfg.Hosts = map[string]HostAlias{
		"gl":   {Host: "gitlab.com", Protocol: "ssh"},
		"work": {Host: "git.company.internal", Protocol: "https"},
	}

	got, meta, err := cfg.ResolveURL("gl:team/svc", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "git@gitlab.com:team/svc.git" || meta.Host != "gitlab.com" {
		t.Errorf("gl alias: url=%q meta=%+v", got, meta)
	}

	got, _, err = cfg.ResolveURL("work:platform/api", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://git.company.internal/platform/api.git" {
		t.Errorf("work alias url = %q", got)
	}

	// Force flag still overrides the alias protocol.
	got, _, err = cfg.ResolveURL("work:platform/api", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "git@") {
		t.Errorf("--ssh should override alias protocol: %q", got)
	}
}
