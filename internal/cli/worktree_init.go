package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// worktreeInitOpts carries the knobs shared by `gk wt add`'s post-create
// hook and the standalone `gk wt init` command.
type worktreeInitOpts struct {
	explicitInit bool // apply without prompting (init subcommand, or `add --init`)
	prompt       bool // ask before applying (interactive `add` without --init)
	save         bool // persist a detected block into .gk.yaml
	dryRun       bool // describe actions without performing them
	fromAdd      bool // invoked as add's post-create hook (quieter when idle)
}

func runWorktreeInit(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	// Resolve the target worktree BEFORE loading config or locating the
	// main worktree. A path argument can point at a different repository
	// than the caller's cwd/--repo; config discovery (.gk.yaml) and the
	// main-worktree lookup must anchor to the TARGET's repo, otherwise
	// `gk wt init /other/repo/wt` would apply this repo's policy there.
	target := ""
	if len(args) == 1 {
		target = args[0]
	} else {
		target = currentWorktreePath(ctx, &git.ExecRunner{Dir: RepoFlag()})
	}
	if target == "" {
		return fmt.Errorf("cannot determine the target worktree; pass a path (gk worktree init <path>)")
	}
	if abs, err := filepath.Abs(target); err == nil {
		target = abs
	}
	if info, statErr := os.Stat(target); statErr != nil || !info.IsDir() {
		return fmt.Errorf("worktree path %q does not exist or is not a directory", target)
	}

	// Re-anchor git + config at the target's repo. config.Load reads the
	// --repo flag for .gk.yaml discovery (see internal/config/load.go), so
	// pointing it at target makes both the runner and the loaded policy
	// describe the target's repository rather than the caller's.
	if err := cmd.Flags().Set("repo", target); err != nil {
		return fmt.Errorf("anchor config to target worktree: %w", err)
	}
	runner := &git.ExecRunner{Dir: target}
	cfg, _ := config.Load(cmd.Flags())

	save, _ := cmd.Flags().GetBool("save")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	return bootstrapWorktree(ctx, w, runner, cfg, target, worktreeInitOpts{
		explicitInit: true,
		save:         save,
		dryRun:       dryRun,
	})
}

// bootstrapWorktree resolves the init policy (configured or detected),
// surfaces warnings/proposals, then applies link/copy/run to target.
func bootstrapWorktree(ctx context.Context, w io.Writer, runner *git.ExecRunner, cfg *config.Config, target string, opts worktreeInitOpts) error {
	mainPath, mainErr := mainWorktreePath(ctx, runner)

	// Detect against the main worktree: it holds both the tracked
	// manifests AND the untracked secrets (.env) we want to surface.
	// A fresh worktree only has the tracked half. Fall back to target
	// when the main worktree can't be located.
	detectDir := target
	if mainErr == nil && mainPath != "" {
		detectDir = mainPath
	}
	init, fromDetection := resolveWorktreeInit(cfg, detectDir)
	if isEmptyInit(init) {
		if !opts.fromAdd {
			fmt.Fprintln(w, "nothing to do: no worktree.init in .gk.yaml and no known package manifests detected")
		}
		return nil
	}

	if fromDetection {
		fmt.Fprint(w, renderDetectedProposal(init))
	}
	for _, ln := range validateWorktreeInit(init) {
		fmt.Fprintln(w, ln)
	}

	if opts.save && fromDetection {
		if opts.dryRun {
			fmt.Fprintln(w, "save: skipped (dry-run); re-run without --dry-run to write .gk.yaml")
		} else {
			saveWorktreeInitBlock(w, mainPath, init)
		}
	}

	apply := opts.explicitInit
	if !apply && opts.prompt {
		ok, perr := ui.ConfirmTUI(ctx, "Bootstrap this worktree now (link/copy/run)?", summarizeInit(init), true)
		switch {
		case errors.Is(perr, ui.ErrNonInteractive):
			fmt.Fprintln(w, "  -> run `gk wt init` or re-run with --init to bootstrap this worktree")
			return nil
		case errors.Is(perr, ui.ErrPickerAborted):
			return nil
		case perr != nil:
			return perr
		}
		apply = ok
	}
	if !apply {
		return nil
	}

	return applyWorktreeInit(ctx, w, init, mainPath, mainErr, target, opts.dryRun)
}

// resolveWorktreeInit returns the configured init block, or — when none is
// configured — a freshly detected one. The bool reports detection so the
// caller knows to print a "save this to .gk.yaml" proposal.
func resolveWorktreeInit(cfg *config.Config, target string) (*config.WorktreeInit, bool) {
	if cfg != nil && cfg.Worktree.Init != nil && !isEmptyInit(cfg.Worktree.Init) {
		return cfg.Worktree.Init, false
	}
	return detectWorktreeInit(target), true
}

func isEmptyInit(init *config.WorktreeInit) bool {
	return init == nil || (len(init.Link) == 0 && len(init.Copy) == 0 && len(init.Run) == 0)
}

// applyWorktreeInit performs link → copy → run in order. link/copy source
// is the main worktree; run executes inside target. Each step is
// idempotent so re-running fixes only what's missing.
func applyWorktreeInit(ctx context.Context, w io.Writer, init *config.WorktreeInit, mainPath string, mainErr error, target string, dryRun bool) error {
	sameAsMain := mainPath != "" && sameDir(mainPath, target)

	if len(init.Link) > 0 || len(init.Copy) > 0 {
		switch {
		case mainErr != nil:
			fmt.Fprintf(w, "skip link/copy: cannot locate the main worktree: %v\n", mainErr)
		case sameAsMain:
			fmt.Fprintln(w, "skip link/copy: target is the main worktree (nothing to copy from)")
		default:
			for _, rel := range init.Link {
				if err := linkFromMain(w, mainPath, target, rel, dryRun); err != nil {
					return err
				}
			}
			for _, rel := range init.Copy {
				if err := copyFromMain(w, mainPath, target, rel, dryRun); err != nil {
					return err
				}
			}
		}
	}

	if len(init.Run) > 0 {
		if err := runInitCommands(ctx, w, init.Run, target, dryRun); err != nil {
			return err
		}
	}
	if !dryRun {
		fmt.Fprintf(w, "worktree initialized: %s\n", target)
	}
	return nil
}

// linkFromMain symlinks <main>/<rel> into <target>/<rel>. Idempotent: an
// already-correct symlink is left alone; a pre-existing non-link target is
// reported and skipped rather than clobbered.
func linkFromMain(w io.Writer, mainPath, target, rel string, dryRun bool) error {
	src := filepath.Join(mainPath, rel)
	dst := filepath.Join(target, rel)

	if _, err := os.Lstat(src); err != nil {
		fmt.Fprintf(w, "link  %s: skipped (not present in main worktree)\n", rel)
		return nil
	}
	if cur, err := os.Readlink(dst); err == nil {
		if cur == src {
			fmt.Fprintf(w, "link  %s: ok (already linked)\n", rel)
			return nil
		}
	}
	if _, err := os.Lstat(dst); err == nil {
		fmt.Fprintf(w, "link  %s: skipped (target already exists and is not our symlink)\n", rel)
		return nil
	}
	if dryRun {
		fmt.Fprintf(w, "link  %s -> %s (dry-run)\n", rel, src)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("link %s: %w", rel, err)
	}
	if err := os.Symlink(src, dst); err != nil {
		return fmt.Errorf("link %s: %w", rel, err)
	}
	fmt.Fprintf(w, "link  %s -> %s\n", rel, src)
	return nil
}

// copyFromMain copies <main>/<rel> into <target>/<rel>. Idempotent: an
// existing destination is left untouched (use `gk wt remove` to reset).
func copyFromMain(w io.Writer, mainPath, target, rel string, dryRun bool) error {
	src := filepath.Join(mainPath, rel)
	dst := filepath.Join(target, rel)

	if _, err := os.Lstat(src); err != nil {
		fmt.Fprintf(w, "copy  %s: skipped (not present in main worktree)\n", rel)
		return nil
	}
	if _, err := os.Lstat(dst); err == nil {
		fmt.Fprintf(w, "copy  %s: ok (already present)\n", rel)
		return nil
	}
	if dryRun {
		fmt.Fprintf(w, "copy  %s (dry-run)\n", rel)
		return nil
	}
	if err := copyPath(src, dst); err != nil {
		return fmt.Errorf("copy %s: %w", rel, err)
	}
	fmt.Fprintf(w, "copy  %s\n", rel)
	return nil
}

// runInitCommands executes each command via `sh -c` inside dir, streaming
// output. The first failure aborts with a hint to fix-and-retry, since the
// idempotent design makes a re-run safe.
func runInitCommands(ctx context.Context, w io.Writer, cmds []string, dir string, dryRun bool) error {
	for _, c := range cmds {
		if dryRun {
			fmt.Fprintf(w, "run   %s (dry-run)\n", c)
			continue
		}
		fmt.Fprintf(w, "run   %s\n", c)
		ex := exec.CommandContext(ctx, "sh", "-c", c)
		ex.Dir = dir
		ex.Stdout = w
		ex.Stderr = os.Stderr
		ex.Stdin = nil
		if err := ex.Run(); err != nil {
			return fmt.Errorf("run %q failed in %s: %w\n  fix the cause, then re-run `gk wt init %s` to resume", c, dir, err, dir)
		}
	}
	return nil
}

// copyPath copies a file or directory tree from src to dst, preserving
// file modes. Symlinks are recreated as symlinks.
func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		linkTarget, lerr := os.Readlink(src)
		if lerr != nil {
			return lerr
		}
		return os.Symlink(linkTarget, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, derr := os.ReadDir(src)
		if derr != nil {
			return derr
		}
		for _, e := range entries {
			if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	default:
		return copyFile(src, dst, info.Mode().Perm())
	}
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// sameDir reports whether two paths point at the same directory, resolving
// symlinks first so macOS's /var → /private/var aliasing (and similar)
// doesn't make a worktree look distinct from itself.
func sameDir(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// mainWorktreePath returns the path of the repository's main worktree —
// always the first entry in `git worktree list --porcelain`.
func mainWorktreePath(ctx context.Context, runner *git.ExecRunner) (string, error) {
	out, stderr, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(stderr)))
	}
	entries := parseWorktreePorcelain(string(out))
	if len(entries) == 0 {
		return "", fmt.Errorf("no worktrees found")
	}
	return entries[0].Path, nil
}

// manifestRule maps a manifest filename to the install command that
// reconstructs its dependency tree against this checkout's lockfile.
type manifestRule struct {
	file string
	cmd  string
}

// detectionRules is ordered so the most specific lockfile wins (uv.lock
// before requirements.txt, pnpm/yarn before bare package.json).
var detectionRules = []manifestRule{
	{"pnpm-lock.yaml", "pnpm install --frozen-lockfile"},
	{"yarn.lock", "yarn install --frozen-lockfile"},
	{"package-lock.json", "npm ci"},
	{"package.json", "npm install"},
	{"uv.lock", "uv sync"},
	{"poetry.lock", "poetry install"},
	{"requirements.txt", "uv venv && uv pip install -r requirements.txt"},
	{"pyproject.toml", "uv venv && uv pip install -e ."},
	{"Gemfile.lock", "bundle install"},
	{"go.mod", "go mod download"},
	{"composer.lock", "composer install"},
}

// ecosystemGroups lets one detected install command cancel the more
// generic fallback in the same ecosystem (e.g. uv.lock suppresses the
// requirements.txt/pyproject.toml proposals).
var ecosystemGroups = [][]string{
	{"pnpm-lock.yaml", "yarn.lock", "package-lock.json", "package.json"},
	{"uv.lock", "poetry.lock", "requirements.txt", "pyproject.toml"},
}

// detectWorktreeInit inspects dir for known package manifests and common
// secret files, proposing a run command per ecosystem and link entries for
// untracked .env files.
func detectWorktreeInit(dir string) *config.WorktreeInit {
	present := map[string]bool{}
	for _, r := range detectionRules {
		if fileExists(filepath.Join(dir, r.file)) {
			present[r.file] = true
		}
	}
	// Within each ecosystem keep only the first (most specific) manifest.
	for _, group := range ecosystemGroups {
		seen := false
		for _, f := range group {
			if present[f] {
				if seen {
					present[f] = false
				}
				seen = true
			}
		}
	}

	out := &config.WorktreeInit{}
	for _, r := range detectionRules {
		if present[r.file] {
			out.Run = append(out.Run, r.cmd)
		}
	}
	for _, env := range []string{".env", ".env.local"} {
		if fileExists(filepath.Join(dir, env)) {
			out.Link = append(out.Link, env)
		}
	}
	return out
}

// venvLikePaths are entries that MUST be regenerated (run), never
// linked/copied: virtualenvs bake absolute paths into pyvenv.cfg/shebangs,
// and node_modules can carry a different branch's lockfile.
var venvLikePaths = map[string]string{
	".venv":        "virtualenv (absolute paths in pyvenv.cfg/shebangs break when linked/copied)",
	"venv":         "virtualenv (absolute paths in pyvenv.cfg/shebangs break when linked/copied)",
	"env":          "virtualenv (absolute paths in pyvenv.cfg/shebangs break when linked/copied)",
	"node_modules": "node_modules (a different branch may pin a different lockfile → cross-contamination)",
}

// validateWorktreeInit returns human-readable warnings for link/copy
// entries that should really be regenerated via run instead.
func validateWorktreeInit(init *config.WorktreeInit) []string {
	var warns []string
	check := func(verb string, paths []string) {
		for _, p := range paths {
			base := strings.TrimSuffix(filepath.Base(filepath.Clean(p)), "/")
			if reason, bad := venvLikePaths[base]; bad {
				warns = append(warns, fmt.Sprintf("warning: %s %q is risky — %s; prefer a `run:` install command", verb, p, reason))
			}
		}
	}
	check("link", init.Link)
	check("copy", init.Copy)
	return warns
}

func summarizeInit(init *config.WorktreeInit) string {
	parts := make([]string, 0, 3)
	if n := len(init.Link); n > 0 {
		parts = append(parts, fmt.Sprintf("%d link", n))
	}
	if n := len(init.Copy); n > 0 {
		parts = append(parts, fmt.Sprintf("%d copy", n))
	}
	if n := len(init.Run); n > 0 {
		parts = append(parts, fmt.Sprintf("%d run", n))
	}
	return strings.Join(parts, " · ")
}

// renderDetectedProposal prints the detected block as a copy-pasteable
// .gk.yaml snippet so the user can persist it (or pass --save).
func renderDetectedProposal(init *config.WorktreeInit) string {
	var b strings.Builder
	b.WriteString("detected project setup — add to .gk.yaml (or pass --save):\n\n")
	b.WriteString(renderWorktreeInitYAML(init))
	b.WriteString("\n")
	return b.String()
}

// renderWorktreeInitYAML builds the worktree.init YAML block by hand to
// avoid a marshaller dependency and to keep comment-free, stable output.
func renderWorktreeInitYAML(init *config.WorktreeInit) string {
	var b strings.Builder
	b.WriteString("worktree:\n")
	b.WriteString("  init:\n")
	writeList := func(key string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "    %s:\n", key)
		for _, it := range items {
			fmt.Fprintf(&b, "      - %s\n", yamlScalar(it))
		}
	}
	writeList("link", init.Link)
	writeList("copy", init.Copy)
	writeList("run", init.Run)
	return b.String()
}

// yamlScalar double-quotes a scalar only when YAML plain-scalar rules
// would otherwise mis-parse it: a leading indicator character, a ": "
// mapping marker, a " #" comment, a trailing ":", or surrounding
// whitespace. Install commands ("npm ci", "uv venv && uv sync") stay bare.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	needsQuote := s != strings.TrimSpace(s) ||
		strings.Contains(s, ": ") || strings.HasSuffix(s, ":") ||
		strings.Contains(s, " #")
	if !needsQuote {
		switch s[0] {
		case '&', '*', '!', '|', '>', '%', '@', '`', '"', '\'', '#', '?', '-', '[', ']', '{', '}', ',', ' ':
			needsQuote = true
		}
	}
	if needsQuote {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// saveWorktreeInitBlock appends the detected block to <mainPath>/.gk.yaml,
// but only when the file has no existing top-level `worktree:` key — a
// safe append avoids the YAML duplicate-key corruption that blind
// concatenation would cause. Otherwise it tells the user to merge by hand.
func saveWorktreeInitBlock(w io.Writer, mainPath string, init *config.WorktreeInit) {
	if mainPath == "" {
		fmt.Fprintln(w, "save: skipped (could not locate the main worktree)")
		return
	}
	path := filepath.Join(mainPath, ".gk.yaml")
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(w, "save: skipped (%v)\n", err)
		return
	}
	if hasTopLevelWorktreeKey(string(existing)) {
		fmt.Fprintf(w, "save: %s already has a `worktree:` block — merge the snippet above by hand\n", path)
		return
	}
	block := renderWorktreeInitYAML(init)
	var buf strings.Builder
	buf.WriteString(string(existing))
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		buf.WriteString("\n")
	}
	if len(existing) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString(block)
	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		fmt.Fprintf(w, "save: failed (%v)\n", err)
		return
	}
	fmt.Fprintf(w, "save: wrote worktree.init to %s\n", path)
}

// hasTopLevelWorktreeKey reports whether the YAML text already defines a
// top-level `worktree:` mapping (column-0 key), to avoid duplicating it.
func hasTopLevelWorktreeKey(yaml string) bool {
	for _, ln := range strings.Split(yaml, "\n") {
		if strings.HasPrefix(ln, "worktree:") {
			return true
		}
	}
	return false
}
