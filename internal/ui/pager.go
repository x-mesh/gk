package ui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// PagerKind identifies which pager binary was selected.
type PagerKind string

const (
	PagerDelta PagerKind = "delta"
	PagerBat   PagerKind = "bat"
	PagerLess  PagerKind = "less"
	PagerNone  PagerKind = "none"
)

// Pager encapsulates pager selection + invocation.
type Pager struct {
	Kind     PagerKind
	Path     string   // absolute path to the binary, or "" for none
	Args     []string // flags to pass
	Disabled bool
}

// Detect returns a Pager chosen by this priority:
//  1. $GK_PAGER (explicit; value: "delta", "bat", "less", "none", or a full binary path)
//  2. $PAGER
//  3. PATH lookup: delta → bat → less
//
// When stdout is not a TTY, returns PagerNone (Disabled=true).
// Honors NO_COLOR for bat and delta (by not passing color flags).
func Detect() *Pager {
	// Non-TTY → no pager
	if !IsTerminal() {
		return &Pager{Kind: PagerNone, Disabled: true}
	}

	// Explicit kill switch
	if v := strings.TrimSpace(os.Getenv("GK_PAGER")); v != "" {
		if v == "none" || v == "off" || v == "cat" {
			return &Pager{Kind: PagerNone, Disabled: true}
		}
		if p := resolve(v); p != nil {
			return p
		}
	}

	if v := strings.TrimSpace(os.Getenv("PAGER")); v != "" {
		if p := resolve(v); p != nil {
			return p
		}
	}

	for _, name := range []string{"delta", "bat", "less"} {
		if p := resolve(name); p != nil {
			return p
		}
	}
	return &Pager{Kind: PagerNone, Disabled: true}
}

// resolve turns a user-supplied name or absolute path into a Pager with
// tuned default args for known binaries. Returns nil if not found on PATH.
func resolve(nameOrPath string) *Pager {
	// Split first token as binary; rest as user-supplied args.
	fields := strings.Fields(nameOrPath)
	if len(fields) == 0 {
		return nil
	}
	bin := fields[0]
	userArgs := fields[1:]

	path, err := exec.LookPath(bin)
	if err != nil {
		return nil
	}
	noColor := os.Getenv("NO_COLOR") != ""
	switch filepathBase(bin) {
	case "delta":
		args := []string{"--paging=always"}
		if width, ok := ttyWidth(); ok {
			args = append(args, "--width", strconv.Itoa(width))
		}
		if noColor {
			args = append(args, "--features", "no-color")
		}
		args = append(args, userArgs...)
		return &Pager{Kind: PagerDelta, Path: path, Args: args}
	case "bat":
		args := []string{"--paging=always", "--style=plain"}
		if noColor {
			args = append(args, "--color=never")
		}
		args = append(args, userArgs...)
		return &Pager{Kind: PagerBat, Path: path, Args: args}
	case "less":
		args := []string{"-R", "-F", "-X"}
		args = append(args, userArgs...)
		return &Pager{Kind: PagerLess, Path: path, Args: args}
	default:
		// unknown pager — use as-is with user args only
		return &Pager{Kind: PagerKind(filepathBase(bin)), Path: path, Args: userArgs}
	}
}

// filepathBase is a tiny wrapper so we don't import path/filepath just for Base.
func filepathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func ttyWidth() (int, bool) {
	if !IsTerminal() {
		return 0, false
	}
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 0, false
	}
	return w, true
}

// Run executes the pager and returns a pipe writer + wait function.
// Usage:
//
//	w, wait, err := pg.Run()
//	if err != nil { /* fallback to os.Stdout */ }
//	defer wait()
//	fmt.Fprintln(w, "...")
func (p *Pager) Run() (io.WriteCloser, func() error, error) {
	if p.Disabled || p.Kind == PagerNone {
		return nopWriter{os.Stdout}, func() error { return nil }, nil
	}
	cmd := exec.Command(p.Path, p.Args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return stdin, func() error {
		_ = stdin.Close()
		return cmd.Wait()
	}, nil
}

type nopWriter struct{ io.Writer }

func (nopWriter) Close() error { return nil }

// String is a short human summary used for `--verbose` output.
func (p *Pager) String() string {
	if p.Disabled || p.Kind == PagerNone {
		return "none (disabled)"
	}
	return fmt.Sprintf("%s (%s)", p.Kind, p.Path)
}
