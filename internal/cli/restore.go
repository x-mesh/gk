package cli

import (
	"bufio"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore lost work (dangling commits/blobs)",
		Long:  "Use --lost to scan git fsck output for dangling commits and surface them as a restorable list.",
		RunE:  runRestore,
	}
	cmd.Flags().Bool("lost", false, "show dangling commits/blobs from git fsck --lost-found")
	cmd.Flags().Int("limit", 20, "max entries to display")
	rootCmd.AddCommand(cmd)
}

type lostEntry struct {
	Kind    string // "commit" or "blob"
	SHA     string
	Subject string // commit subject (empty for blobs)
	When    int64  // unix, 0 for blobs
}

func runRestore(cmd *cobra.Command, args []string) error {
	lost, _ := cmd.Flags().GetBool("lost")
	limit, _ := cmd.Flags().GetInt("limit")
	if !lost {
		return fmt.Errorf("only --lost is supported in v0.2.0")
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	entries, err := scanLost(ctx, runner)
	if err != nil {
		return err
	}

	// newest first
	sort.Slice(entries, func(i, j int) bool { return entries[i].When > entries[j].When })
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}

	if len(entries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no dangling objects found")
		return nil
	}

	w := cmd.OutOrStdout()
	for i, e := range entries {
		shortSHA := e.SHA
		if len(shortSHA) > 8 {
			shortSHA = shortSHA[:8]
		}
		if e.Kind == "commit" {
			fmt.Fprintf(w, "%2d) %s %s — %s\n", i+1, e.Kind, shortSHA, e.Subject)
		} else {
			fmt.Fprintf(w, "%2d) %s %s\n", i+1, e.Kind, shortSHA)
		}
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "To recover a commit: git cherry-pick <sha>  or  git branch <name> <sha>")
	return nil
}

// scanLost runs `git fsck --full --lost-found --no-reflogs --unreachable` and parses the output.
// For each dangling commit, fetch subject and author time.
func scanLost(ctx context.Context, r git.Runner) ([]lostEntry, error) {
	stdout, stderr, err := r.Run(ctx, "fsck", "--full", "--lost-found", "--no-reflogs", "--unreachable")
	if err != nil {
		return nil, fmt.Errorf("git fsck: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	var entries []lostEntry
	// Lines look like:
	//   dangling commit <sha>
	//   dangling blob <sha>
	//   unreachable commit <sha>
	//   missing blob <sha>    (skip)
	scanner := bufio.NewScanner(strings.NewReader(string(stdout)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	seen := make(map[string]bool)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		status := fields[0]
		kind := fields[1]
		sha := fields[2]
		if status != "dangling" && status != "unreachable" {
			continue
		}
		if kind != "commit" && kind != "blob" {
			continue
		}
		if seen[sha] {
			continue
		}
		seen[sha] = true
		e := lostEntry{Kind: kind, SHA: sha}
		if kind == "commit" {
			// fetch subject + author time
			out, _, logErr := r.Run(ctx, "log", "-1", "--format=%at%x00%s", sha)
			if logErr == nil {
				s := strings.TrimSpace(string(out))
				parts := strings.SplitN(s, "\x00", 2)
				if len(parts) == 2 {
					if n, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
						e.When = n
					}
					e.Subject = parts[1]
				}
			}
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
