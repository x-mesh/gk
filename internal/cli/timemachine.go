package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
	"github.com/x-mesh/gk/internal/timemachine"
)

func init() {
	tmCmd := &cobra.Command{
		Use:   "timemachine",
		Short: "Browse and restore historical repo states (reflog + gk backup refs)",
		Long: `timemachine surfaces every recoverable HEAD state — reflog entries, gk undo/wipe
backup refs, and (opt-in) dangling commits — and lets you restore any of
them safely. Every restore writes a fresh backup ref first, so the
operation is reversible.

This subtree ships in stages. v0.7 lands the ` + "`restore`" + ` subcommand; the
interactive TUI and ` + "`list --json`" + ` headless output follow.`,
	}

	restoreCmd := &cobra.Command{
		Use:   "restore <sha|ref>",
		Short: "Restore HEAD to the given SHA or ref (creates a backup ref first)",
		Long: `Restore moves HEAD to the given commit after writing a backup ref
at the current tip. On any failure, the backup ref is printed so you can
always reach the pre-restore state.

  --mode soft|mixed|hard|auto   reset mode (default: auto — picks via DecideStrategy)
  --dry-run                     print the plan and exit; do not touch the repo
  --autostash                   when the tree is dirty, stash before reset and pop after
  --force                       allow hard reset on a dirty tree without autostash (data loss)
`,
		Args: cobra.ExactArgs(1),
		RunE: runTimemachineRestore,
	}
	restoreCmd.Flags().String("mode", "auto", "reset mode: soft, mixed, hard, or auto")
	restoreCmd.Flags().Bool("dry-run", false, "print plan without executing")
	restoreCmd.Flags().Bool("autostash", false, "stash dirty changes before reset and pop after")
	restoreCmd.Flags().Bool("force", false, "allow hard reset on dirty tree (discards uncommitted changes)")

	listBackupsCmd := &cobra.Command{
		Use:   "list-backups",
		Short: "List gk-managed backup refs newest-first (refs/gk/*-backup/)",
		Long: `Surfaces every backup ref created by gk undo / gk wipe / gk timemachine
restore. Each entry can be restored via ` + "`gk timemachine restore <ref>`" + `.

Use --json to emit one NDJSON object per line, suitable for piping into jq.
`,
		RunE: runTimemachineListBackups,
	}
	listBackupsCmd.Flags().Bool("json", false, "emit NDJSON (one entry per line)")
	listBackupsCmd.Flags().String("kind", "", "filter by kind: undo, wipe, timemachine")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List timeline events (reflog + gk backup refs) newest-first",
		Long: `Merges HEAD reflog, optionally per-branch reflogs, and all gk backup refs
into a single timeline. Use --kinds to filter the source pool and --json to
get NDJSON output for scripting.
`,
		RunE: runTimemachineList,
	}
	listCmd.Flags().Bool("json", false, "emit NDJSON (one entry per line)")
	listCmd.Flags().String("kinds", "reflog,backup", "comma-separated source kinds: reflog, backup, stash, dangling")
	listCmd.Flags().Int("dangling-cap", 500, "max dangling commits to scan (0 = unlimited; git fsck is O(objects))")
	listCmd.Flags().Int("limit", 50, "max events (0 = unlimited)")
	listCmd.Flags().Bool("all-branches", false, "include reflogs from every local branch (default: HEAD only)")
	listCmd.Flags().String("branch", "", "filter to a single branch (or 'HEAD'); applies to reflog + backup events")
	listCmd.Flags().Duration("since", 0, "filter to events at or after (now - duration), e.g. 24h, 7d is not supported — use 168h")

	showCmd := &cobra.Command{
		Use:   "show <sha|ref>",
		Short: "Show commit details + diff stat for a timeline entry",
		Long: `Resolves the SHA or ref via git rev-parse, looks up any gk-managed backup
ref context, and prints:

  * the resolved SHA + commit subject + author + date
  * a matching gk backup-ref descriptor (kind, branch, when) if the ref is one
  * git show --stat <sha>  (or --patch when --patch is passed)

No repo state is mutated.
`,
		Args: cobra.ExactArgs(1),
		RunE: runTimemachineShow,
	}
	showCmd.Flags().Bool("patch", false, "show full diff instead of stat")

	tmCmd.AddCommand(restoreCmd, listBackupsCmd, listCmd, showCmd)
	rootCmd.AddCommand(tmCmd)
}

func runTimemachineShow(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	target := args[0]
	patch, _ := cmd.Flags().GetBool("patch")

	runner := &git.ExecRunner{Dir: RepoFlag()}
	w := cmd.OutOrStdout()

	// Resolve the ref to a commit SHA first.
	sha, err := gitsafe.ResolveRef(ctx, runner, target)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", target, err)
	}

	// If the caller passed a backup-ref path, find the matching BackupRef so
	// we can surface kind/branch/when context.
	if strings.HasPrefix(target, "refs/gk/") {
		if backups, err := gitsafe.ListBackups(ctx, runner); err == nil {
			for _, b := range backups {
				if b.Ref == target {
					fmt.Fprintf(w, "gk backup:   kind=%s  branch=%s  when=%s\n",
						b.Kind, b.Branch, b.When.Format(time.RFC3339))
					break
				}
			}
		}
	}

	// Show commit header (one-liner) — uses short SHA for readability.
	header, stderr, err := runner.Run(ctx, "show", "--no-patch",
		"--format=commit %h%n  subject: %s%n  author:  %an <%ae>%n  date:    %aI",
		sha)
	if err != nil {
		return fmt.Errorf("git show header: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintln(w, strings.TrimRight(string(header), "\n"))

	// Diff: stat by default, patch on request.
	diffArgs := []string{"show", "--stat", "--format=", sha}
	if patch {
		diffArgs = []string{"show", "--patch", "--stat", "--format=", sha}
	}
	body, stderr, err := runner.Run(ctx, diffArgs...)
	if err != nil {
		return fmt.Errorf("git show body: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprint(w, string(body))
	return nil
}

// eventJSON is the NDJSON shape emitted by `gk timemachine list --json`.
// Optional fields are omitted when empty to keep lines compact.
type eventJSON struct {
	Kind       string `json:"kind"`
	Ref        string `json:"ref"`
	OID        string `json:"oid"`
	OldOID     string `json:"old_oid,omitempty"`
	When       int64  `json:"when_unix,omitempty"`
	ISO        string `json:"when_iso,omitempty"`
	Subject    string `json:"subject"`
	Action     string `json:"action,omitempty"`
	BackupKind string `json:"backup_kind,omitempty"`
	Branch     string `json:"branch,omitempty"`
}

func runTimemachineList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	asJSON, _ := cmd.Flags().GetBool("json")
	kindsArg, _ := cmd.Flags().GetString("kinds")
	limit, _ := cmd.Flags().GetInt("limit")
	allBranches, _ := cmd.Flags().GetBool("all-branches")

	kinds := splitKinds(kindsArg)
	runner := &git.ExecRunner{Dir: RepoFlag()}

	var groups [][]timemachine.Event

	if wantKind(kinds, "reflog") {
		head, err := timemachine.ReadHEAD(ctx, runner, 0)
		if err != nil {
			return fmt.Errorf("read HEAD reflog: %w", err)
		}
		groups = append(groups, head)
		if allBranches {
			br, err := timemachine.ReadBranches(ctx, runner, 0)
			if err != nil {
				return fmt.Errorf("read branch reflogs: %w", err)
			}
			groups = append(groups, br)
		}
	}

	if wantKind(kinds, "backup") {
		bk, err := timemachine.ReadBackups(ctx, runner)
		if err != nil {
			return fmt.Errorf("read backup refs: %w", err)
		}
		groups = append(groups, bk)
	}

	if wantKind(kinds, "stash") {
		st, err := timemachine.ReadStash(ctx, runner)
		if err != nil {
			return fmt.Errorf("read stash: %w", err)
		}
		groups = append(groups, st)
	}

	if wantKind(kinds, "dangling") {
		cap, _ := cmd.Flags().GetInt("dangling-cap")
		dg, err := timemachine.ReadDangling(ctx, runner, timemachine.DanglingOptions{Cap: cap})
		if err != nil {
			return fmt.Errorf("read dangling: %w", err)
		}
		groups = append(groups, dg)
	}

	events := timemachine.Merge(groups...)

	if branchFilter, _ := cmd.Flags().GetString("branch"); branchFilter != "" {
		events = timemachine.FilterByBranch(events, branchFilter)
	}
	if since, _ := cmd.Flags().GetDuration("since"); since > 0 {
		events = timemachine.FilterBySince(events, time.Now().Add(-since))
	}

	events = timemachine.Limit(events, limit)

	w := cmd.OutOrStdout()
	if asJSON {
		return printEventsNDJSON(w, events)
	}
	return printEventsHuman(w, events)
}

func splitKinds(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func wantKind(kinds []string, want string) bool {
	if len(kinds) == 0 {
		return true // empty = default to all
	}
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

func printEventsNDJSON(w io.Writer, events []timemachine.Event) error {
	enc := json.NewEncoder(w)
	for _, ev := range events {
		j := eventJSON{
			Kind:       ev.Kind.String(),
			Ref:        ev.Ref,
			OID:        ev.OID,
			OldOID:     ev.OldOID,
			Subject:    ev.Subject,
			Action:     ev.Action,
			BackupKind: ev.BackupKind,
			Branch:     ev.Branch,
		}
		if !ev.When.IsZero() {
			j.When = ev.When.Unix()
			j.ISO = ev.When.UTC().Format(time.RFC3339)
		}
		if err := enc.Encode(j); err != nil {
			return err
		}
	}
	return nil
}

func printEventsHuman(w io.Writer, events []timemachine.Event) error {
	if len(events) == 0 {
		fmt.Fprintln(w, "no timeline events")
		return nil
	}
	now := time.Now()
	for _, ev := range events {
		age := "—"
		if !ev.When.IsZero() {
			age = humanSince(now.Sub(ev.When))
		}
		kindTag := ev.Kind.String()
		if ev.Kind == timemachine.KindBackup {
			kindTag = "backup:" + ev.BackupKind
		}
		fmt.Fprintf(w, "%-16s  %s  %s  %s\n",
			kindTag, shortSHA(ev.OID), age, ev.Subject)
	}
	return nil
}

// backupRefJSON is the NDJSON shape emitted by `gk timemachine list-backups --json`.
// Fields are named for scripting friendliness; do not rename without bumping
// a user-visible schema version in docs.
type backupRefJSON struct {
	Ref        string `json:"ref"`
	Kind       string `json:"kind"`
	Branch     string `json:"branch"`
	SHA        string `json:"sha"`
	Unix       int64  `json:"unix,omitempty"`
	ISO        string `json:"iso,omitempty"`
	AgeSeconds int64  `json:"age_seconds,omitempty"`
}

func runTimemachineListBackups(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	kindFilter, _ := cmd.Flags().GetString("kind")
	asJSON, _ := cmd.Flags().GetBool("json")

	runner := &git.ExecRunner{Dir: RepoFlag()}
	refs, err := gitsafe.ListBackups(ctx, runner)
	if err != nil {
		return err
	}

	if kindFilter != "" {
		refs = filterBackupsByKind(refs, kindFilter)
	}

	w := cmd.OutOrStdout()
	if asJSON {
		return printBackupsNDJSON(w, refs)
	}
	return printBackupsHuman(w, refs)
}

func filterBackupsByKind(in []gitsafe.BackupRef, kind string) []gitsafe.BackupRef {
	out := in[:0]
	for _, r := range in {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func printBackupsNDJSON(w io.Writer, refs []gitsafe.BackupRef) error {
	enc := json.NewEncoder(w)
	now := time.Now()
	for _, r := range refs {
		j := backupRefJSON{
			Ref:    r.Ref,
			Kind:   r.Kind,
			Branch: r.Branch,
			SHA:    r.SHA,
		}
		if !r.When.IsZero() {
			j.Unix = r.When.Unix()
			j.ISO = r.When.UTC().Format(time.RFC3339)
			j.AgeSeconds = int64(now.Sub(r.When).Seconds())
		}
		if err := enc.Encode(j); err != nil {
			return err
		}
	}
	return nil
}

func printBackupsHuman(w io.Writer, refs []gitsafe.BackupRef) error {
	if len(refs) == 0 {
		fmt.Fprintln(w, "no backup refs found")
		return nil
	}
	now := time.Now()
	for _, r := range refs {
		age := "—"
		if !r.When.IsZero() {
			age = humanSince(now.Sub(r.When))
		}
		branch := r.Branch
		if branch == "detached" {
			branch = "(detached)"
		}
		fmt.Fprintf(w, "%-12s  %-20s  %s  %s  %s\n",
			r.Kind, branch, shortSHA(r.SHA), age, r.Ref)
	}
	return nil
}

func runTimemachineRestore(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	target := args[0]
	mode, _ := cmd.Flags().GetString("mode")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	autostash, _ := cmd.Flags().GetBool("autostash")
	force, _ := cmd.Flags().GetBool("force")

	repo := RepoFlag()
	runner := &git.ExecRunner{Dir: repo}
	client := git.NewClient(runner)

	// Resolve target to a full SHA for display + call.
	sha, err := gitsafe.ResolveRef(ctx, runner, target)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", target, err)
	}

	// Preflight — refuse during rebase/merge regardless of force.
	rep, err := gitsafe.Check(ctx, runner, gitsafe.WithWorkDir(repo))
	if err != nil {
		return err
	}
	if rep.InProgress != 0 {
		// In-progress states are never OK for a restore; AllowDirty does not mask them.
		return rep.Err()
	}

	// Pick Strategy.
	strategy, err := pickStrategy(mode, rep.Dirty, autostash, force)
	if err != nil {
		return err
	}

	// Dry-run: show the plan and exit without touching the repo.
	if dryRun {
		printRestorePlan(cmd.OutOrStdout(), target, sha, strategy, autostash, rep.Dirty)
		return nil
	}

	// Reject dirty+hard without autostash/force.
	if rep.Dirty && strategy == gitsafe.StrategyHard && !autostash && !force {
		return fmt.Errorf("refusing hard reset on dirty tree; pass --autostash or --force")
	}

	branch, _ := client.CurrentBranch(ctx)

	var opts []gitsafe.RestoreOption
	if autostash {
		opts = append(opts, gitsafe.WithAutostash(true))
	}

	r := gitsafe.NewRestorer(runner, nil, "timemachine")
	res, err := r.Restore(ctx, branch,
		gitsafe.Target{SHA: sha, Label: target, Summary: "(--to " + target + ")"},
		strategy, opts...)
	if err != nil {
		// Surface recovery hint when present.
		var rerr *gitsafe.RestoreError
		if errors.As(err, &rerr) && rerr.Recovery != "" {
			return fmt.Errorf("%w\nhint: %s", err, rerr.Recovery)
		}
		return err
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "restored to %s (%s)\n", shortSHA(res.To), target)
	fmt.Fprintf(w, "backup saved at %s\n", res.BackupRef)
	fmt.Fprintf(w, "to revert: gk timemachine restore %s --mode hard\n", res.BackupRef)
	if res.AutostashRef != "" {
		fmt.Fprintf(w, "note: autostash pop had conflicts; resolve via: git stash pop %s\n", res.AutostashRef)
	}
	return nil
}

// pickStrategy translates the --mode flag into a Strategy. Mode "auto" defers
// to DecideStrategy using the runtime dirty/autostash state, simulating the
// user pressing `y` on the confirmation modal (which is itself rendered
// elsewhere — TM-14 Phase 2).
func pickStrategy(mode string, dirty, autostash, force bool) (gitsafe.Strategy, error) {
	switch strings.ToLower(mode) {
	case "soft":
		return gitsafe.StrategySoft, nil
	case "mixed":
		return gitsafe.StrategyMixed, nil
	case "hard":
		if dirty && !autostash && !force {
			return 0, gitsafe.ErrRequiresForceDiscard
		}
		return gitsafe.StrategyHard, nil
	case "auto", "":
		s, err := gitsafe.DecideStrategy(dirty, autostash, gitsafe.KeyConfirm)
		if err != nil {
			return 0, err
		}
		return s, nil
	default:
		return 0, fmt.Errorf("unknown --mode %q (want soft|mixed|hard|auto)", mode)
	}
}

// printRestorePlan prints the dry-run banner: target + selected strategy +
// whether autostash applies. No repo state is mutated.
func printRestorePlan(w interface {
	Write([]byte) (int, error)
}, label, sha string, s gitsafe.Strategy, autostash, dirty bool) {
	fmt.Fprintf(w, "[dry-run] would restore to %s (%s)\n", shortSHA(sha), label)
	fmt.Fprintf(w, "[dry-run] strategy: reset --%s\n", s)
	if autostash {
		fmt.Fprintln(w, "[dry-run] autostash: stash push -u before reset, pop after")
	}
	if dirty {
		fmt.Fprintln(w, "[dry-run] working tree is dirty")
	}
	fmt.Fprintln(w, "[dry-run] no changes made")
}
