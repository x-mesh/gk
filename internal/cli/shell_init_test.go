package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestShellInitScript(t *testing.T) {
	for _, sh := range []string{"zsh", "bash"} {
		s, err := shellInitScript(sh, "gk", "git-kit")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"unalias gk 2>/dev/null",
			"function gk {",
			`GK_CD_FILE="$__gk_cd" command git-kit "$@"`,
			`builtin cd -- "$(<"$__gk_cd")"`,
			`command rm -f -- "$__gk_cd"`,
		} {
			if !strings.Contains(s, want) {
				t.Errorf("%s script lacks %q:\n%s", sh, want, s)
			}
		}
	}
	s, err := shellInitScript("fish", "gk", "gk")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"function gk --wraps gk", "GK_CD_FILE=$cdfile command gk $argv", "builtin cd (cat $cdfile)"} {
		if !strings.Contains(s, want) {
			t.Errorf("fish script lacks %q:\n%s", want, s)
		}
	}
	if _, err := shellInitScript("tcsh", "gk", "gk"); err == nil {
		t.Error("expected an error for an unsupported shell")
	}
	if _, err := shellInitScript("zsh", "gk; rm -rf ~", "gk"); err == nil {
		t.Error("expected an error for a name that is not a plain command name")
	}
	if _, err := shellInitScript("zsh", "gk", "$(id)"); err == nil {
		t.Error("expected an error for a bin that is not a plain command name")
	}
}

// TestEnterWorktreeSubshell_CdFile pins the hand-off to the shell function:
// with GK_CD_FILE set, the path goes into the file and no shell starts.
func TestEnterWorktreeSubshell_CdFile(t *testing.T) {
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skip("false not installed")
	}
	// If a shell were started anyway, false exits at once and the file
	// stays empty, so the test fails instead of hanging on a shell.
	t.Setenv("SHELL", falseBin)
	t.Setenv(envWTCtl, "")
	cdFile := filepath.Join(t.TempDir(), "cd")
	if err := os.WriteFile(cdFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envCdFile, cdFile)
	wt := t.TempDir()

	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&stderr)
	if err := enterWorktreeSubshell(cmd, wt); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(cdFile); string(got) != wt {
		t.Fatalf("cd file = %q, want %q", got, wt)
	}
	if !strings.Contains(stderr.String(), wt) {
		t.Fatalf("expected the target on stderr, got %q", stderr.String())
	}
}

// runShellInit evals the gk function in an interactive sh, with a fake gk
// first on PATH, feeds it script and returns stdout. The fake writes
// target into GK_CD_FILE when target is set and exits with code.
func runShellInitScript(t *testing.T, sh, rc, target, code, script string) string {
	t.Helper()
	home, bin, tmp := t.TempDir(), t.TempDir(), t.TempDir()
	fake := "#!/bin/sh\n[ -n \"$GK_FAKE_TARGET\" ] && [ -n \"$GK_CD_FILE\" ] && printf '%s' \"$GK_FAKE_TARGET\" > \"$GK_CD_FILE\"\nexit ${GK_FAKE_CODE:-0}\n"
	if err := os.WriteFile(filepath.Join(bin, "gk"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	init, err := shellInitScript(filepath.Base(sh), "gk", "gk")
	if err != nil {
		t.Fatal(err)
	}
	rcName := ".bashrc"
	if filepath.Base(sh) == "zsh" {
		rcName = ".zshrc"
	}
	if err := os.WriteFile(filepath.Join(home, rcName), []byte(rc+init), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, sh, "-i")
	c.Dir = home
	c.Env = []string{
		"HOME=" + home, "PATH=" + bin + ":" + os.Getenv("PATH"), "TMPDIR=" + tmp, "TERM=dumb",
		"GK_FAKE_TARGET=" + target, "GK_FAKE_CODE=" + code, "PS1=", "PROMPT=",
	}
	c.Stdin = strings.NewReader(script)
	var out bytes.Buffer
	c.Stdout = &out
	_ = c.Run()
	if left, _ := filepath.Glob(filepath.Join(tmp, "gk-cd.*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
	return out.String()
}

func TestShellInit_CdsTheCallingShell(t *testing.T) {
	for _, name := range []string{"bash", "zsh"} {
		t.Run(name, func(t *testing.T) {
			sh, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s not installed", name)
			}
			target := t.TempDir()
			probe := "echo \"lvl=$SHLVL\"\ngk wt\necho \"rc=$? pwd=$PWD lvl=$SHLVL\"\n"

			// An alias defined earlier in the rc file must not win over the
			// function; the fake writes the target, and the same shell cds.
			out := runShellInitScript(t, sh, "alias gk='echo aliased'\n", target, "0", probe)
			before := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
			if strings.Contains(out, "aliased") || !strings.Contains(out, "rc=0 pwd="+target+" "+before) {
				t.Fatalf("want cd into %s in the same shell (%s), got:\n%s", target, before, out)
			}

			// Without a target the shell stays put and the exit code passes through.
			out = runShellInitScript(t, sh, "", "", "3", probe)
			if !strings.Contains(out, "rc=3 pwd=") || strings.Contains(out, "pwd="+target) {
				t.Fatalf("want rc=3 and no cd, got:\n%s", out)
			}
		})
	}
}
