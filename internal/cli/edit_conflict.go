package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:     "edit-conflict",
		Aliases: []string{"ec"},
		Short:   "Open conflict files in $EDITOR, positioned at the first marker",
		RunE:    runEditConflict,
	}
	cmd.Flags().String("editor", "", "override $EDITOR")
	cmd.Flags().Bool("list", false, "print conflict files only (don't open editor)")
	rootCmd.AddCommand(cmd)
}

func runEditConflict(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()

	files, err := conflictFiles(ctx, runner)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no conflicts")
		return nil
	}

	listOnly, _ := cmd.Flags().GetBool("list")
	if listOnly {
		for _, f := range files {
			fmt.Fprintln(cmd.OutOrStdout(), f)
		}
		return nil
	}

	repoRoot, err := gitToplevel(ctx, runner)
	if err != nil {
		return err
	}

	editorFlag, _ := cmd.Flags().GetString("editor")
	editor := editorFlag
	if editor == "" {
		editor = os.Getenv("GIT_EDITOR")
	}
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		return fmt.Errorf("no editor set (GIT_EDITOR, VISUAL, EDITOR all empty). use --editor")
	}

	// Split editor string (it may be "code --wait")
	parts := strings.Fields(editor)
	editorBin := parts[0]
	editorExtraArgs := parts[1:]

	// Detect known editors to position the cursor.
	base := filepath.Base(editorBin)

	argv := []string{editorBin}
	argv = append(argv, editorExtraArgs...)

	for _, rel := range files {
		abs := filepath.Join(repoRoot, rel)
		line := firstMarkerLine(abs)
		argv = appendEditorTarget(argv, base, abs, line)
	}

	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// conflictFiles returns the list of files with unmerged entries
// via `git diff --name-only --diff-filter=U -z`.
func conflictFiles(ctx context.Context, r git.Runner) ([]string, error) {
	stdout, stderr, err := r.Run(ctx, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, fmt.Errorf("git diff: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	raw := strings.TrimRight(string(stdout), "\x00")
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\x00"), nil
}

func gitToplevel(ctx context.Context, r git.Runner) (string, error) {
	stdout, stderr, err := r.Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return strings.TrimSpace(string(stdout)), nil
}

// firstMarkerLine returns 1-based line of the first "<<<<<<<" marker, or 1 if not found.
func firstMarkerLine(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 1
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if strings.HasPrefix(scanner.Text(), "<<<<<<<") {
			return line
		}
	}
	return 1
}

// appendEditorTarget adds file [with line jump] to argv using editor-specific syntax.
// Known editors: nvim, vim, vi, nano, emacs, code/code-insiders, subl, hx, micro.
// Unknown editors fall back to just the file path.
func appendEditorTarget(argv []string, editorBase, file string, line int) []string {
	switch editorBase {
	case "vim", "nvim", "vi", "emacs", "nano", "micro":
		return append(argv, fmt.Sprintf("+%d", line), file)
	case "code", "code-insiders":
		// VS Code requires --goto path:line
		return append(argv, "--goto", fmt.Sprintf("%s:%d", file, line))
	case "subl":
		return append(argv, fmt.Sprintf("%s:%d", file, line))
	case "hx": // Helix
		return append(argv, fmt.Sprintf("%s:%d", file, line))
	default:
		return append(argv, file)
	}
}
