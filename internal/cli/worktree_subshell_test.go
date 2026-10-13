package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSubshellFamilyOf(t *testing.T) {
	cases := map[string]subshellFamily{
		"/bin/bash":              shellBash,
		"/opt/homebrew/bin/bash": shellBash,
		"/bin/zsh":               shellZsh,
		"/usr/bin/fish":          shellFish,
		"/bin/sh":                shellOther,
		"/usr/bin/dash":          shellOther,
		"":                       shellOther,
	}
	for sh, want := range cases {
		if got := subshellFamilyOf(sh); got != want {
			t.Errorf("subshellFamilyOf(%q) = %v, want %v", sh, got, want)
		}
	}
}

func TestSubshellFunctionNames(t *testing.T) {
	cases := []struct {
		argv0 string
		want  []string
	}{
		{"gk", []string{"gk"}},
		{"/usr/local/bin/gk", []string{"gk"}},
		{"git-kit", []string{"gk", "git-kit"}},
		{"./bin/gk-dev", []string{"gk", "gk-dev"}},
		{"bad name", []string{"gk"}},
	}
	for _, tc := range cases {
		if got := subshellFunctionNames(tc.argv0); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("subshellFunctionNames(%q) = %v, want %v", tc.argv0, got, tc.want)
		}
	}
}

func TestParseSubshellCtl(t *testing.T) {
	cases := []struct {
		in     string
		action ctlAction
		target string
	}{
		{"", ctlExit, ""},
		{"\n", ctlExit, ""},
		{"back", ctlReturn, ""},
		{"back\n", ctlReturn, ""},
		{"/tmp/wt\n", ctlHop, "/tmp/wt"},
		{"relative/path", ctlExit, ""},
	}
	for _, tc := range cases {
		action, target := parseSubshellCtl(tc.in)
		if action != tc.action || target != tc.target {
			t.Errorf("parseSubshellCtl(%q) = (%v, %q), want (%v, %q)", tc.in, action, target, tc.action, tc.target)
		}
	}
}

func TestWriteSubshellCtl(t *testing.T) {
	t.Setenv(envWTCtl, "")
	t.Setenv(envWTFn, "1")
	if writeSubshellCtl("/x") {
		t.Fatal("wrote a control file without GK_WT_CTL")
	}
	ctl := filepath.Join(t.TempDir(), "ctl")
	t.Setenv(envWTCtl, ctl)
	// A shell started inside the subshell inherits GK_WT_CTL but not the
	// function, so gk there must nest instead of writing.
	t.Setenv(envWTFn, "")
	if writeSubshellCtl("/x") {
		t.Fatal("wrote a control file without the gk function marker")
	}
	if _, ok := worktreeBackRow(fmt.Sprint); ok {
		t.Fatal("offered the back row without the gk function marker")
	}
	t.Setenv(envWTFn, "1")
	if !writeSubshellCtl("/x") {
		t.Fatal("did not write the control file")
	}
	if got, _ := os.ReadFile(ctl); string(got) != "/x" {
		t.Fatalf("control file = %q, want /x", got)
	}
	t.Setenv(envWTCtl, filepath.Join(t.TempDir(), "gone", "ctl"))
	if writeSubshellCtl("/x") {
		t.Fatal("reported a write into a missing directory")
	}
}

func TestWriteSubshellBootstrap(t *testing.T) {
	fn := subshellFunctionSh("/opt/g k/gk", []string{"gk", "git-kit"})
	for _, want := range []string{
		"unalias gk git-kit 2>/dev/null",
		"gk() {", "git-kit() {",
		`GK_WT_FN=1 '/opt/g k/gk' "$@"`,
		`[ -s "$GK_WT_CTL" ]`,
		"builtin exit 0",
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("sh function lacks %q:\n%s", want, fn)
		}
	}

	t.Run("bash", func(t *testing.T) {
		dir := t.TempDir()
		boot, err := writeSubshellBootstrap(dir, shellBash, fn, "")
		if err != nil {
			t.Fatal(err)
		}
		rc := filepath.Join(dir, "bashrc")
		if !reflect.DeepEqual(boot.Args, []string{"--rcfile", rc}) || len(boot.Env) != 0 {
			t.Fatalf("bootstrap = %+v", boot)
		}
		body, _ := os.ReadFile(rc)
		src, def := strings.Index(string(body), `. "$HOME/.bashrc"`), strings.Index(string(body), "gk() {")
		if src < 0 || def < 0 || src > def {
			t.Fatalf("rcfile must run ~/.bashrc before the function:\n%s", body)
		}
	})

	t.Run("zsh", func(t *testing.T) {
		dir := t.TempDir()
		boot, err := writeSubshellBootstrap(dir, shellZsh, fn, "/home/u/.config/zsh")
		if err != nil {
			t.Fatal(err)
		}
		if len(boot.Args) != 0 || !reflect.DeepEqual(boot.Env, []string{"ZDOTDIR=" + dir, "GK_ZDOTDIR_ORIG=/home/u/.config/zsh"}) {
			t.Fatalf("bootstrap = %+v", boot)
		}
		env, _ := os.ReadFile(filepath.Join(dir, ".zshenv"))
		if !strings.Contains(string(env), `. "${ZDOTDIR:-$HOME}/.zshenv"`) || !strings.HasSuffix(strings.TrimSpace(string(env)), "ZDOTDIR="+shellQuote(dir)) {
			t.Fatalf(".zshenv must run the user's .zshenv and point ZDOTDIR back:\n%s", env)
		}
		rc, _ := os.ReadFile(filepath.Join(dir, ".zshrc"))
		restore, src, def := strings.Index(string(rc), `ZDOTDIR="$GK_ZDOTDIR_ORIG"`), strings.Index(string(rc), `. "${ZDOTDIR:-$HOME}/.zshrc"`), strings.Index(string(rc), "gk() {")
		if restore < 0 || src < 0 || def < 0 || restore > src || src > def {
			t.Fatalf(".zshrc must restore ZDOTDIR, run the user's .zshrc, then define the function:\n%s", rc)
		}
	})

	t.Run("fish", func(t *testing.T) {
		dir := t.TempDir()
		ffn := subshellFunctionFish("/opt/gk", []string{"gk"})
		boot, err := writeSubshellBootstrap(dir, shellFish, ffn, "")
		if err != nil {
			t.Fatal(err)
		}
		init := filepath.Join(dir, "init.fish")
		if !reflect.DeepEqual(boot.Args, []string{"-C", "source " + shellQuote(init)}) {
			t.Fatalf("bootstrap = %+v", boot)
		}
		body, _ := os.ReadFile(init)
		for _, want := range []string{"function gk --wraps gk", "set -lx GK_WT_FN 1", "'/opt/gk' $argv", "builtin exit 0"} {
			if !strings.Contains(string(body), want) {
				t.Errorf("init.fish lacks %q:\n%s", want, body)
			}
		}
	})

	t.Run("other", func(t *testing.T) {
		if _, err := writeSubshellBootstrap(t.TempDir(), shellOther, fn, ""); err == nil {
			t.Fatal("expected no bootstrap for an unknown shell")
		}
	})
}

// runBootstrappedShell starts sh interactively with the bootstrap for its
// family, feeds it script on stdin and returns stdout and the exit code.
// files are the user's startup files under a fresh HOME. The gk the
// function calls is a fake that writes hop into the control file when hop
// is set, which is what gk wt does when the user picks a worktree.
func runBootstrappedShell(t *testing.T, sh string, files map[string]string, hop, script string) (string, int) {
	t.Helper()
	home := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(home, "fake-gk")
	fake := "#!/bin/sh\n[ -n \"$GK_WT_HOP\" ] && [ \"$GK_WT_FN\" = 1 ] && printf '%s' \"$GK_WT_HOP\" > \"$GK_WT_CTL\"\nexit 0\n"
	if err := os.WriteFile(exe, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}

	fam := subshellFamilyOf(sh)
	names := []string{"gk", "git-kit"}
	fn := subshellFunctionSh(exe, names)
	if fam == shellFish {
		fn = subshellFunctionFish(exe, names)
	}
	dir := t.TempDir()
	boot, err := writeSubshellBootstrap(dir, fam, fn, "")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, append(boot.Args, "-i")...)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "HOME", "ZDOTDIR", "GK_WT_CTL", "GK_WT_FN", "GK_WT_HOP", "GK_ZDOTDIR_ORIG", "PS1", "PROMPT":
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "HOME="+home, envWTCtl+"="+filepath.Join(dir, "ctl"), "GK_WT_HOP="+hop, "PS1=", "PROMPT=")
	cmd.Env = append(cmd.Env, boot.Env...)
	cmd.Stdin = strings.NewReader(script)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	var ee *exec.ExitError
	if err != nil && !errorsAs(err, &ee) {
		t.Fatalf("%s: %v\nstderr:\n%s", sh, err, errOut.String())
	}
	return out.String(), cmd.ProcessState.ExitCode()
}

func errorsAs(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

const subshellProbe = "echo \"mark=$GK_TEST_MARK zdot=$ZDOTDIR\"\ngk\necho after\n"

// TestSubshellBootstrap_Bash runs the real bash with the bootstrap: the
// user's ~/.bashrc still runs, an alias named gk no longer wins over the
// function, and the shell ends after a hop but not after a plain call.
func TestSubshellBootstrap_Bash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	files := map[string]string{".bashrc": "GK_TEST_MARK=rc-ran\nalias gk='echo aliased'\n"}

	out, code := runBootstrappedShell(t, bash, files, "/tmp/next-wt", subshellProbe)
	if !strings.Contains(out, "mark=rc-ran") || strings.Contains(out, "aliased") || strings.Contains(out, "after") || code != 0 {
		t.Fatalf("hop: want rc marker, no alias, no 'after', exit 0; got code %d, stdout:\n%s", code, out)
	}

	out, _ = runBootstrappedShell(t, bash, files, "", subshellProbe)
	if !strings.Contains(out, "mark=rc-ran") || !strings.Contains(out, "after") {
		t.Fatalf("no hop: the shell must continue; stdout:\n%s", out)
	}
}

// TestSubshellBootstrap_Zsh covers the ZDOTDIR hand-off: the user's
// .zshenv moves ZDOTDIR to an XDG directory, and the shell must still run
// the .zshrc there, see that ZDOTDIR afterwards, and define the function.
func TestSubshellBootstrap_Zsh(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	files := map[string]string{
		".zshenv":    "ZDOTDIR=\"$HOME/cfg\"\n",
		"cfg/.zshrc": "GK_TEST_MARK=rc-ran\nalias gk='echo aliased'\n",
	}

	out, code := runBootstrappedShell(t, zsh, files, "/tmp/next-wt", subshellProbe)
	if !strings.Contains(out, "mark=rc-ran") || !strings.Contains(out, "zdot=") || strings.Contains(out, "aliased") || strings.Contains(out, "after") || code != 0 {
		t.Fatalf("hop: want rc marker, no alias, no 'after', exit 0; got code %d, stdout:\n%s", code, out)
	}
	if line := strings.SplitN(out, "\n", 2)[0]; !strings.HasSuffix(line, "/cfg") {
		t.Fatalf("ZDOTDIR must be the user's XDG directory after startup, got %q", line)
	}

	out, _ = runBootstrappedShell(t, zsh, files, "", subshellProbe)
	if !strings.Contains(out, "after") {
		t.Fatalf("no hop: the shell must continue; stdout:\n%s", out)
	}
}
