package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/initx"
)

func remoteTestCfg() config.CloneConfig {
	return config.CloneConfig{
		DefaultProtocol: "ssh",
		DefaultHost:     "github.com",
		Hosts: map[string]config.HostAlias{
			"personal": {Host: "github.com", Owner: "JINWOO-J"},
			"work":     {Host: "github.com", Owner: "42tape", Protocol: "https"},
			"legacy":   {Host: "gitlab.com"}, // no owner
		},
	}
}

func TestSanitizeRepoName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"my-service", "my-service"},
		{"MyService", "MyService"},             // GitHub allows uppercase — keep it
		{"my service name", "my-service-name"}, // whitespace → single dash
		{"a  b\tc", "a-b-c"},                   // runs collapse
		{"repo.git", "repo"},                   // .git suffix stripped
		{".hidden", "hidden"},                  // leading dot trimmed
		{"-lead", "lead"},                      // leading dash trimmed
		{"..", "my-project"},                   // dots only → fallback
		{"한글프로젝트", "my-project"},               // non-ASCII only → fallback
		{"🚀launch", "launch"},                  // emoji dropped
		{"내프로젝트-api", "api"},                   // non-ASCII dropped, leading dash trimmed
		{"a\nb\x00c", "abc"},                   // control chars dropped
		{"../../etc/passwd", "etcpasswd"},      // path separators dropped, no traversal chars survive
		{"", "my-project"},
		{"v1.2.3", "v1.2.3"},
		{"under_score", "under_score"},
	}
	for _, tc := range cases {
		if got := sanitizeRepoName(tc.in); got != tc.want {
			t.Errorf("sanitizeRepoName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// 100-rune cap.
	long := strings.Repeat("a", 150)
	if got := sanitizeRepoName(long); len(got) != 100 {
		t.Errorf("long name not truncated to 100: len=%d", len(got))
	}
}

func TestBuildRemotePlan_BareAlias(t *testing.T) {
	plan, err := buildRemotePlan(remoteTestCfg(), "personal", "my-svc", false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.URL != "git@github.com:JINWOO-J/my-svc.git" {
		t.Errorf("url = %q", plan.URL)
	}
	if plan.Alias != "personal" || plan.Name != "my-svc" || plan.RemoteName != "origin" {
		t.Errorf("plan = %+v", plan)
	}
	if plan.Action != initx.ActionCreate {
		t.Errorf("action = %v, want create", plan.Action)
	}

	// Profile protocol is honoured; force flags override it.
	plan, err = buildRemotePlan(remoteTestCfg(), "work", "svc", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.URL != "https://github.com/42tape/svc.git" {
		t.Errorf("work url = %q", plan.URL)
	}
	plan, err = buildRemotePlan(remoteTestCfg(), "work", "svc", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.URL, "git@") {
		t.Errorf("--ssh should override profile protocol: %q", plan.URL)
	}
}

func TestBuildRemotePlan_BareAliasErrors(t *testing.T) {
	// Missing project name.
	if _, err := buildRemotePlan(remoteTestCfg(), "personal", "", false, false); err == nil {
		t.Error("expected error for bare alias without name")
	}

	// Unregistered alias → error naming the available ones.
	_, err := buildRemotePlan(remoteTestCfg(), "nosuch", "svc", false, false)
	if err == nil {
		t.Fatal("expected error for unknown alias")
	}
	for _, want := range []string{"nosuch", "personal", "work"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}

	// Ownerless alias with a bare repo → ResolveURL's owner error.
	if _, err := buildRemotePlan(remoteTestCfg(), "legacy", "svc", false, false); err == nil ||
		!strings.Contains(err.Error(), "no owner configured") {
		t.Errorf("expected no-owner error, got: %v", err)
	}
}

func TestBuildRemotePlan_SpecForms(t *testing.T) {
	cfg := remoteTestCfg()

	// owner/repo — name argument is ignored, spec wins.
	plan, err := buildRemotePlan(cfg, "acme/tool", "ignored", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.URL != "git@github.com:acme/tool.git" || plan.Name != "tool" || plan.Alias != "" {
		t.Errorf("owner/repo plan = %+v", plan)
	}

	// alias:repo — owner completion path keeps the alias for JSON.
	plan, err = buildRemotePlan(cfg, "personal:repo", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.URL != "git@github.com:JINWOO-J/repo.git" || plan.Alias != "personal" {
		t.Errorf("alias:repo plan = %+v", plan)
	}

	// Full URL passthrough.
	plan, err = buildRemotePlan(cfg, "https://example.com/x/y.git", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.URL != "https://example.com/x/y.git" || plan.Alias != "" {
		t.Errorf("url plan = %+v", plan)
	}

	// SCP-style passthrough.
	plan, err = buildRemotePlan(cfg, "git@github.com:foo/bar.git", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.URL != "git@github.com:foo/bar.git" || plan.Name != "bar" {
		t.Errorf("scp plan = %+v", plan)
	}
}

func TestBuildRemotePlan_MalformedErrors(t *testing.T) {
	cfg := remoteTestCfg()
	for _, in := range []string{"", "a/b/c", "/bar", "foo/"} {
		if _, err := buildRemotePlan(cfg, in, "", false, false); err == nil {
			t.Errorf("%q: expected error, got nil", in)
		}
	}

	// Unknown alias:repo is an error for init (clone would passthrough).
	_, err := buildRemotePlan(cfg, "nosuch:repo", "", false, false)
	if err == nil {
		t.Fatal("expected error for unknown alias:repo")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Errorf("error = %v", err)
	}

	// With an empty hosts table the hint changes but it still errors.
	empty := config.CloneConfig{DefaultProtocol: "ssh", DefaultHost: "github.com"}
	if _, err := buildRemotePlan(empty, "nosuch:repo", "", false, false); err == nil ||
		!strings.Contains(err.Error(), "clone.hosts is empty") {
		t.Errorf("empty-hosts error = %v", err)
	}
}

// --- runInit integration (flag path, agent JSON) ---

// setRemoteTestConfig isolates the global config in a temp XDG dir and
// registers the test account profiles there.
func setRemoteTestConfig(t *testing.T) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "gk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "clone:\n  default_protocol: ssh\n  default_host: github.com\n  hosts:\n    personal:\n      host: github.com\n      owner: JINWOO-J\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

type remoteEnvelope struct {
	State  string `json:"state"`
	OK     bool   `json:"ok"`
	Result struct {
		Result string            `json:"result"`
		Remote *remoteResultJSON `json:"remote"`
	} `json:"result"`
}

// runInitRemoteCmd drives `gk init` through runInit with the given flag
// values and returns stdout. Flags are reset afterwards so the shared
// rootCmd does not leak state between tests.
func runInitRemoteCmd(t *testing.T, dir string, flags map[string]string, dryRun bool) (string, error) {
	t.Helper()
	cmd, _, _ := rootCmd.Find([]string{"init"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(&bytes.Buffer{})

	for k, v := range flags {
		_ = cmd.Flags().Set(k, v)
	}
	_ = cmd.Root().PersistentFlags().Set("repo", dir)
	if dryRun {
		_ = cmd.Root().PersistentFlags().Set("dry-run", "true")
	}
	t.Cleanup(func() {
		for _, k := range []string{"only", "remote", "name"} {
			_ = cmd.Flags().Set(k, "")
		}
		for _, k := range []string{"ssh", "https", "force", "kiro"} {
			_ = cmd.Flags().Set(k, "false")
		}
		_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
		_ = cmd.Root().PersistentFlags().Set("repo", "")
	})

	err := runInit(cmd, nil)
	return buf.String(), err
}

func TestInitCmd_RemoteFlagAddsOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	setRemoteTestConfig(t)
	withAgentMode(t, true)
	dir := t.TempDir()

	out, err := runInitRemoteCmd(t, dir,
		map[string]string{"only": "remote", "remote": "personal", "name": "my-svc"}, false)
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}
	var env remoteEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	r := env.Result.Remote
	if r == nil || r.Status != "added" || r.Alias != "personal" || r.Name != "my-svc" ||
		r.URL != "git@github.com:JINWOO-J/my-svc.git" {
		t.Fatalf("remote result = %+v", r)
	}

	runner := &git.ExecRunner{Dir: dir}
	got, _, err := runner.Run(context.Background(), "remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("get-url: %v", err)
	}
	if strings.TrimSpace(string(got)) != r.URL {
		t.Errorf("origin = %q, want %q", strings.TrimSpace(string(got)), r.URL)
	}

	// Idempotence: rerunning reports the existing remote and leaves it be.
	out, err = runInitRemoteCmd(t, dir,
		map[string]string{"only": "remote", "remote": "personal", "name": "other"}, false)
	if err != nil {
		t.Fatalf("second runInit: %v", err)
	}
	env = remoteEnvelope{}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	if env.Result.Remote == nil || env.Result.Remote.Status != "existing" ||
		env.Result.Remote.URL != "git@github.com:JINWOO-J/my-svc.git" {
		t.Fatalf("second run remote = %+v", env.Result.Remote)
	}
}

func TestInitCmd_RemoteDryRunDoesNotTouchGit(t *testing.T) {
	setRemoteTestConfig(t)
	withAgentMode(t, true)
	dir := t.TempDir()

	out, err := runInitRemoteCmd(t, dir,
		map[string]string{"only": "remote", "remote": "personal", "name": "my-svc"}, true)
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}
	var env remoteEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	if env.Result.Remote == nil || env.Result.Remote.Status != "dry-run" ||
		env.Result.Remote.URL != "git@github.com:JINWOO-J/my-svc.git" {
		t.Fatalf("remote = %+v", env.Result.Remote)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create .git (stat err=%v)", err)
	}
}

func TestInitCmd_RemoteSpecForms(t *testing.T) {
	setRemoteTestConfig(t)
	withAgentMode(t, true)

	cases := map[string]string{
		"acme/tool":                   "git@github.com:acme/tool.git",
		"personal:repo":               "git@github.com:JINWOO-J/repo.git",
		"https://example.com/x/y.git": "https://example.com/x/y.git",
		"git@github.com:foo/bar.git":  "git@github.com:foo/bar.git",
	}
	for spec, want := range cases {
		out, err := runInitRemoteCmd(t, t.TempDir(),
			map[string]string{"only": "remote", "remote": spec}, true)
		if err != nil {
			t.Errorf("%q: %v", spec, err)
			continue
		}
		var env remoteEnvelope
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Errorf("%q: bad JSON: %v", spec, err)
			continue
		}
		if env.Result.Remote == nil || env.Result.Remote.URL != want {
			t.Errorf("%q → %+v, want url %q", spec, env.Result.Remote, want)
		}
	}
}

func TestInitCmd_OnlyRemoteWithoutSpecBlocksNonTTY(t *testing.T) {
	setRemoteTestConfig(t)
	withAgentMode(t, true)

	_, err := runInitRemoteCmd(t, t.TempDir(), map[string]string{"only": "remote"}, false)
	if err == nil {
		t.Fatal("expected blocked error, got nil")
	}
	rendered := FormatErrorJSON(err)
	var env struct {
		State string `json:"state"`
		Error struct {
			Code     string `json:"code"`
			Remedies []struct {
				Command string `json:"command"`
			} `json:"remedies"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(rendered), &env); err != nil {
		t.Fatalf("bad error JSON: %v\n%s", err, rendered)
	}
	if env.State != "blocked" || env.Error.Code != "init_remote_spec_missing" {
		t.Fatalf("envelope = %s", rendered)
	}
	if len(env.Error.Remedies) == 0 || !strings.Contains(env.Error.Remedies[0].Command, "--remote") {
		t.Fatalf("remedies should point at --remote: %s", rendered)
	}
}

func TestInitCmd_SSHAndHTTPSMutuallyExclusive(t *testing.T) {
	setRemoteTestConfig(t)
	withAgentMode(t, true)

	_, err := runInitRemoteCmd(t, t.TempDir(),
		map[string]string{"only": "remote", "remote": "personal", "name": "x", "ssh": "true", "https": "true"}, true)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got: %v", err)
	}
}

func TestValidProfileAlias(t *testing.T) {
	valid := []string{"personal", "my-org", "a_b", "Work2", "x"}
	for _, in := range valid {
		if !validProfileAlias(in) {
			t.Errorf("%q should be valid", in)
		}
	}
	// Adversarial: dots split SetValue's key path, spaces/colons/unicode
	// break YAML keys — all must be rejected.
	invalid := []string{"", "a.b", "a b", "a:b", "한글", "a/b", "a\tb", ".hidden", "a\nb"}
	for _, in := range invalid {
		if validProfileAlias(in) {
			t.Errorf("%q should be rejected", in)
		}
	}
}

func TestSaveRemoteProfile_PreservesCommentsAndReloads(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "gk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	original := "# my precious comment\nbase_branch: main\nclone:\n  default_protocol: ssh # inline note\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := &remotePlan{
		URL:  "https://github.com/42tape/svc.git",
		Meta: config.CloneMeta{Host: "github.com", Owner: "42tape", Repo: "svc"},
	}
	if err := saveRemoteProfile(path, "tape", plan); err != nil {
		t.Fatalf("saveRemoteProfile: %v", err)
	}

	// Comments and existing keys survive the field-by-field writes.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# my precious comment", "base_branch: main", "# inline note"} {
		if !strings.Contains(string(after), want) {
			t.Errorf("lost %q after save:\n%s", want, after)
		}
	}

	// A fresh Load sees the profile with the https protocol derived from
	// the URL.
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := cfg.Clone.Hosts["tape"]
	if !ok {
		t.Fatalf("profile missing after reload: %+v", cfg.Clone.Hosts)
	}
	if got.Host != "github.com" || got.Owner != "42tape" || got.Protocol != "https" {
		t.Errorf("reloaded profile = %+v", got)
	}
}

func TestSaveRemoteProfile_Failures(t *testing.T) {
	// No structured host/owner (opaque URL) → refuse.
	if err := saveRemoteProfile("/nonexistent", "x", &remotePlan{URL: "ssh://weird/path"}); err == nil {
		t.Error("expected error for opaque plan")
	}

	// Read-only config file → error surfaces (the caller downgrades it
	// to a warning; init must not fail).
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("clone:\n  default_protocol: ssh\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	plan := &remotePlan{
		URL:  "git@github.com:foo/bar.git",
		Meta: config.CloneMeta{Host: "github.com", Owner: "foo", Repo: "bar"},
	}
	if err := saveRemoteProfile(path, "ro", plan); err == nil {
		t.Error("expected error for read-only config")
	}
}

func TestAddRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runner := &git.ExecRunner{Dir: dir}
	ctx := context.Background()
	if _, _, err := runner.Run(ctx, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if err := addRemote(ctx, runner, "origin", "git@github.com:foo/bar.git"); err != nil {
		t.Fatalf("addRemote: %v", err)
	}
	out, _, err := runner.Run(ctx, "remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("get-url: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "git@github.com:foo/bar.git" {
		t.Errorf("origin url = %q", got)
	}

	// Second add of the same name fails with git's own message — init
	// treats that as a warning, never an overwrite.
	err = addRemote(ctx, runner, "origin", "git@github.com:other/repo.git")
	if err == nil {
		t.Fatal("expected error adding duplicate remote")
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("duplicate error = %v", err)
	}
	// URL must be unchanged.
	out, _, _ = runner.Run(ctx, "remote", "get-url", "origin")
	if got := strings.TrimSpace(string(out)); got != "git@github.com:foo/bar.git" {
		t.Errorf("origin url after failed add = %q", got)
	}
}
