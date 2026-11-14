// Package rules holds the built-in gk guard policy rule implementations.
// Each file in this package contributes exactly one Rule to policy.Default.
package rules

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/scan"
	"github.com/x-mesh/gk/internal/secrets"
)

// SecretPatternsRule scans with the gitleaks ruleset embedded in gk and adds
// an informational Violation naming the scanner. The embedded engine follows
// the gitleaks detection steps (it matched gitleaks 8.30.1 finding for
// finding on the gk history) and scans faster because large files are split
// across cores. The gitleaks binary
// runs instead only when the repo carries a gitleaks config (.gitleaks.toml,
// .gitleaksignore, or GITLEAKS_CONFIG*), because the embedded engine does
// not read those files.
type SecretPatternsRule struct {
	// GitleaksOpts are forwarded to scan.RunGitleaks. WorkDir is filled
	// from Input.WorkDir so callers typically leave it empty.
	GitleaksOpts scan.GitleaksOptions

	// ScanDiff scans a unified diff and ScanFile one file's content with the
	// built-in patterns. The rules package cannot import the cli package that
	// owns the scanners, so the caller injects them. Either nil disables the
	// built-in scan.
	ScanDiff func(diff string) []secrets.Finding
	ScanFile func(path, content string) []secrets.Finding

	// ScannerLabel describes the injected scanner in the fallback notice. It
	// is a func so a lazily loaded ruleset is not compiled until a scan
	// actually falls back. Nil reports the built-in pattern count.
	ScannerLabel func() string

	// Verify checks the credentials the embedded engine attached to its
	// findings when Input.Verify is set, keyed by Credential.ID. The caller
	// injects it so this package makes no network calls. Nil skips
	// verification.
	Verify func(context.Context, []*secrets.Credential) map[string]policy.Verification

	// runGitleaks is a test seam; nil means scan.RunGitleaks.
	runGitleaks func(context.Context, scan.GitleaksOptions) ([]scan.GitleaksFinding, error)
	// findOCIProfiles is a test seam; nil means secrets.FindOCIProfiles.
	findOCIProfiles func(string) []secrets.OCIProfile
	// archiveLimit is a test seam; nil means defaultArchiveLimit.
	archiveLimit *archiveLimit
}

// ociProfileSink collects the OCI profiles in the text a scan reads. A nil
// sink collects nothing, which is the case unless verification asked for
// them. Scans run in parallel, so each unit of work fills its own slot and
// the slots are joined in order afterwards.
type ociProfileSink struct {
	find  func(string) []secrets.OCIProfile
	slots [][]secrets.OCIProfile
}

// grow adds n slots and returns the index of the first. The archives of a
// scan get their slots after the diff or files that named them.
func (s *ociProfileSink) grow(n int) int {
	if s == nil {
		return 0
	}
	base := len(s.slots)
	s.slots = append(s.slots, make([][]secrets.OCIProfile, n)...)
	return base
}

func (s *ociProfileSink) scan(slot int, text string) {
	if s != nil {
		s.slots[slot] = append(s.slots[slot], s.find(text)...)
	}
}

func (s *ociProfileSink) profiles() []secrets.OCIProfile {
	if s == nil {
		return nil
	}
	return slices.Concat(s.slots...)
}

// NewSecretPatternsRule returns a SecretPatternsRule with redaction enabled.
// The gitleaks mode follows the input scope; callers can add flags through
// GitleaksOpts.ExtraArgs after construction.
func NewSecretPatternsRule() *SecretPatternsRule {
	return &SecretPatternsRule{GitleaksOpts: scan.GitleaksOptions{Redact: true}}
}

// Name implements policy.Rule.
func (*SecretPatternsRule) Name() string { return "secret_patterns" }

// Evaluate scans with the embedded ruleset unless the repo has a gitleaks
// config, in which case it runs gitleaks and maps each finding to a
// Violation with SeverityError. A missing gitleaks binary falls back to the
// embedded ruleset with a notice that the config was not read.
func (r *SecretPatternsRule) Evaluate(ctx context.Context, in policy.Input) ([]policy.Violation, error) {
	opts := r.gitleaksOptions(in)
	if in.Runner != nil && r.builtinReady() && !scan.HasGitleaksConfig(opts.WorkDir) {
		return r.evaluateBuiltin(ctx, in, r.embeddedNotice(in))
	}
	run := r.runGitleaks
	if run == nil {
		run = scan.RunGitleaks
	}
	findings, err := run(ctx, opts)
	if err != nil {
		if errors.Is(err, scan.ErrGitleaksNotInstalled) {
			return r.evaluateBuiltin(ctx, in, r.builtinNotice(in))
		}
		return nil, fmt.Errorf("secret_patterns: %w", err)
	}

	out := convertFindings(findings)
	if in.Verify {
		out = append(out, policy.Violation{
			RuleID:   r.Name(),
			Severity: policy.SeverityInfo,
			Message:  "nothing was verified: --verify checks findings of the embedded ruleset, and gitleaks ran instead",
			Hint:     "gitleaks runs because the repo has .gitleaks.toml or .gitleaksignore, or GITLEAKS_CONFIG is set",
		})
	}
	return out, nil
}

func (r *SecretPatternsRule) builtinReady() bool { return r.ScanDiff != nil && r.ScanFile != nil }

// gitleaksOptions maps the input scope onto a gitleaks invocation: the
// working directory for the default scope, staged changes, a pushed range,
// or the history of every ref.
func (r *SecretPatternsRule) gitleaksOptions(in policy.Input) scan.GitleaksOptions {
	opts := r.GitleaksOpts
	if opts.WorkDir == "" {
		opts.WorkDir = in.WorkDir
	}
	var scope []string
	switch {
	case in.Staged:
		opts.Mode, scope = "git", []string{"--staged"}
	case in.PushRange != "":
		opts.Mode, scope = "git", []string{"--log-opts=" + in.PushRange}
	case in.History:
		opts.Mode = "git"
	default:
		opts.Mode = "dir"
	}
	opts.ExtraArgs = append(scope, opts.ExtraArgs...)
	return opts
}

// scopeLabel names what a scan covered, for the notice.
func scopeLabel(in policy.Input) string {
	switch {
	case in.Staged:
		return "staged changes"
	case in.PushRange != "":
		return "push range " + in.PushRange
	case in.History:
		return "the history of every ref"
	default:
		return "tracked files"
	}
}

// evaluateBuiltin scans with the built-in patterns. Scope follows the input:
// staged changes, the pushed range, the history of every ref, or by default
// the current content of the tracked files.
func (r *SecretPatternsRule) evaluateBuiltin(ctx context.Context, in policy.Input, notice policy.Violation) ([]policy.Violation, error) {
	if in.Runner == nil || !r.builtinReady() {
		return []policy.Violation{{
			RuleID:   r.Name(),
			Severity: policy.SeverityInfo,
			Message:  "gitleaks not installed; secret_patterns is a no-op",
			Hint:     "brew install gitleaks  (gk doctor will confirm)",
		}}, nil
	}

	// An OCI key is checked with the profile that names its fingerprint, and
	// the profile is not a finding, so the scan collects profiles on the side.
	verify := in.Verify && r.Verify != nil
	var oci *ociProfileSink
	if verify {
		oci = &ociProfileSink{find: r.findOCIProfiles}
		if oci.find == nil {
			oci.find = secrets.FindOCIProfiles
		}
	}

	var (
		findings []secrets.Finding
		archives archiveReport
		err      error
	)
	readers := in.ArchiveReaders
	if readers < 1 {
		readers = DefaultArchiveReaders
	}
	switch {
	case in.Staged:
		findings, archives, err = r.scanDiff(ctx, in.Runner, oci, readers,
			append([]string{"diff", "--cached", "--no-color"}, archivePatchExclude...),
			[]string{"diff", "--cached", "--raw", "-z", "--no-abbrev", "--no-renames"})
	case in.PushRange != "":
		findings, archives, err = r.scanDiff(ctx, in.Runner, oci, readers,
			append([]string{"diff", "--no-color", in.PushRange}, archivePatchExclude...),
			[]string{"diff", "--raw", "-z", "--no-abbrev", "--no-renames", in.PushRange})
	case in.History:
		findings, archives, err = r.scanHistory(ctx, in.Runner, oci, readers)
	default:
		findings, archives, err = r.scanTrackedFiles(ctx, in.Runner, oci)
	}
	if err != nil {
		return nil, err
	}
	if archives.opened > 0 {
		notice.Message += fmt.Sprintf(", including the files in %d archive(s)", archives.opened)
	}

	var verified map[string]policy.Verification
	if verify {
		bindOCIKeys(findings, oci.profiles())
		var creds []*secrets.Credential
		for _, f := range findings {
			// Most findings carry no credential. A private-key finding
			// that no OCI profile names carries a candidate, which is not
			// checked either.
			if c := f.Credential; c != nil && c.Verifiable() {
				creds = append(creds, c)
			}
		}
		if len(creds) > 0 {
			verified = r.Verify(ctx, creds)
		}
	}

	out := []policy.Violation{notice}
	for _, f := range findings {
		v := policy.Violation{
			RuleID:   r.Name(),
			Severity: policy.SeverityError,
			File:     f.File,
			Line:     f.FileLine,
			Message:  fmt.Sprintf("secret matched built-in pattern %s (%s)", f.Kind, f.Sample),
			Hint:     "rotate the secret and add a justified allowlist entry if it was intentional",
		}
		if c := f.Credential; c != nil && c.Verifiable() && verified != nil {
			if res, ok := verified[c.ID()]; ok {
				v.Verification = &res
				if hint, ok := liveHint[res.Provider]; ok && res.Status == verifyStatusValid {
					v.Hint = hint
				}
			}
		}
		out = append(out, v)
	}
	out = append(out, archives.notices...)
	if verify {
		out = append(out, verifySummary(r.Name(), verified))
	}
	return out, nil
}

// archivePatchExclude leaves the archives out of the patches, because the
// archive scan reads them. Git decompresses a whole blob to call it binary,
// and on histories with many committed jars that took up to half of the
// scan time. A patch shows an archive only as "Binary files differ", and
// no built-in path rule matches an archive name.
var archivePatchExclude = func() []string {
	args := []string{"--"}
	for _, ext := range secrets.ZipArchiveExts {
		args = append(args, ":(top,exclude,glob,icase)**/*"+ext)
	}
	return args
}()

// scanDiff scans the patch of one `git diff` and the archives that the raw
// form of the same diff adds or changes.
func (r *SecretPatternsRule) scanDiff(ctx context.Context, runner git.Runner, oci *ociProfileSink, readers int, patchArgs, rawArgs []string) ([]secrets.Finding, archiveReport, error) {
	diff, err := scanCommand(ctx, runner, patchArgs...)
	if err != nil {
		return nil, archiveReport{}, err
	}
	findings := r.ScanDiff(diff)
	oci.scan(oci.grow(1), diff)
	raw, err := scanCommand(ctx, runner, rawArgs...)
	if err != nil {
		return nil, archiveReport{}, err
	}
	found, report, err := r.scanArchiveBlobs(ctx, runner, oci, readers, rawArchives(raw))
	return append(findings, found...), report, err
}

// bindOCIKeys replaces the private-key candidates that a profile names with
// OCI credentials. The match is by key fingerprint, so the profile and the
// key can be in different files or commits.
func bindOCIKeys(findings []secrets.Finding, profiles []secrets.OCIProfile) {
	if len(profiles) == 0 {
		return
	}
	for i := range findings {
		if c := findings[i].Credential; c != nil && !c.Verifiable() {
			if bound := secrets.BindOCI(c, profiles); bound != nil {
				findings[i].Credential = bound
			}
		}
	}
}

// verifyStatusValid mirrors verify.StatusValid without importing the
// network package here.
const verifyStatusValid = "valid"

// liveHint replaces the generic hint on a finding the issuer accepted. A
// live GCP API key can be a client key that is public by design, so its
// hint points at restrictions instead of deletion only.
var liveHint = map[string]string{
	secrets.ProviderAWS:   "the key is live: deactivate it in IAM now, then rotate it",
	secrets.ProviderSlack: "the token is live: revoke it now (auth.revoke, or reinstall the app), then issue a new one",
	secrets.ProviderGCP:   "the key exists: delete it, or if it is a client key that must stay public, restrict it to its APIs and apps in the Google Cloud console",
	secrets.ProviderOCI:   "the key is live: delete this API key from its user in the OCI console now, then add a new key",
}

var providerLabel = map[string]string{
	secrets.ProviderAWS:   "AWS",
	secrets.ProviderSlack: "Slack",
	secrets.ProviderGCP:   "GCP",
	secrets.ProviderOCI:   "OCI",
}

// verifySummary counts the distinct credentials per outcome, so a value
// repeated across files or commits counts once.
func verifySummary(rule string, verified map[string]policy.Verification) policy.Violation {
	if len(verified) == 0 {
		return policy.Violation{RuleID: rule, Severity: policy.SeverityInfo, Message: "verify: no AWS, Slack, GCP, or OCI credential to check"}
	}
	counts := map[string]map[string]int{}
	for _, v := range verified {
		if counts[v.Provider] == nil {
			counts[v.Provider] = map[string]int{}
		}
		counts[v.Provider][v.Status]++
	}
	var groups []string
	for _, p := range []string{secrets.ProviderAWS, secrets.ProviderSlack, secrets.ProviderGCP, secrets.ProviderOCI} {
		var parts []string
		for _, s := range []string{"valid", "rejected", "unknown", "unverifiable"} {
			if n := counts[p][s]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, s))
			}
		}
		if len(parts) > 0 {
			groups = append(groups, providerLabel[p]+" "+strings.Join(parts, ", "))
		}
	}
	v := policy.Violation{
		RuleID:   rule,
		Severity: policy.SeverityInfo,
		Message:  fmt.Sprintf("verify: checked %d credential(s): %s", len(verified), strings.Join(groups, "; ")),
	}
	var hints []string
	if counts[secrets.ProviderAWS]["rejected"] > 0 {
		hints = append(hints, "AWS rejected means the global STS endpoint refused the key; a China or GovCloud key is refused there too")
	}
	if counts[secrets.ProviderOCI]["rejected"] > 0 {
		hints = append(hints, "OCI rejected means OCI answered 401 for the key in the profile's region; a tenancy that is not subscribed to that region gets the same answer")
	}
	v.Hint = strings.Join(hints, "; ")
	return v
}

func scanCommand(ctx context.Context, runner git.Runner, args ...string) (string, error) {
	stdout, _, err := runner.Run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("secret_patterns: built-in scan: %w", err)
	}
	return string(stdout), nil
}

// scanTrackedFiles scans the working-tree content of every tracked file, so
// uncommitted edits count and ignored files do not. Files are read and
// scanned in parallel; findings keep `git ls-files` order. A binary file is
// matched by path only, which is what path rules such as pkcs12-file need.
func (r *SecretPatternsRule) scanTrackedFiles(ctx context.Context, runner git.Runner, oci *ociProfileSink) ([]secrets.Finding, archiveReport, error) {
	top, err := scanCommand(ctx, runner, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, archiveReport{}, err
	}
	// Only the newline: a repo path may start or end with a space.
	root := strings.TrimSuffix(top, "\n")
	// The :/ pathspec lists the whole repo even when gk runs in a subdirectory.
	listing, err := scanCommand(ctx, runner, "ls-files", "-z", "--full-name", "--", ":/")
	if err != nil {
		return nil, archiveReport{}, err
	}
	var paths []string
	for p := range strings.SplitSeq(listing, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}

	results := make([][]secrets.Finding, len(paths))
	archives := make([]archiveResult, len(paths))
	oci.grow(len(paths))
	var g errgroup.Group
	g.SetLimit(runtime.GOMAXPROCS(0))
	for i, p := range paths {
		g.Go(func() error {
			full := filepath.Join(root, filepath.FromSlash(p))
			info, err := os.Lstat(full)
			if errors.Is(err, fs.ErrNotExist) {
				// Deleted in the working tree: nothing left to scan.
				return nil
			}
			if err != nil {
				// An unreadable path is not a clean one.
				return fmt.Errorf("secret_patterns: stat %s: %w", p, err)
			}
			if !info.Mode().IsRegular() {
				// Symlinks and submodule directories have no content of their own.
				return nil
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return fmt.Errorf("secret_patterns: read %s: %w", p, err)
			}
			content := string(data)
			if secrets.LooksBinary(data) {
				content = ""
			}
			results[i] = r.ScanFile(p, content)
			oci.scan(i, content)
			if secrets.IsZipArchive(p) {
				archives[i] = r.scanArchive(p, data, oci, i)
				results[i] = append(results[i], archives[i].findings...)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, archiveReport{}, err
	}
	var out []secrets.Finding
	for _, f := range results {
		out = append(out, f...)
	}
	return out, joinArchives(archives), nil
}

// History chunk bounds. Each chunk is one `git log --no-walk` process, so a
// chunk too small spends more on process start than on diffs, and the upper
// bound keeps the SHA list well under the OS argument-length limit.
const (
	historyChunkMin = 32
	historyChunkMax = 1000
)

// scanHistory scans every commit reachable from any ref. A single
// `git log -p --all` produces the diffs on one core and took longer than
// the scan itself, so the commits are split into chunks that git diffs in
// parallel, and each chunk is scanned as soon as its diff arrives. The
// options match gitleaks' git mode (-U0, all refs), and findings keep
// rev-list order.
func (r *SecretPatternsRule) scanHistory(ctx context.Context, runner git.Runner, oci *ociProfileSink, readers int) ([]secrets.Finding, archiveReport, error) {
	revs, err := scanCommand(ctx, runner, "rev-list", "--all")
	if err != nil {
		return nil, archiveReport{}, err
	}
	shas := strings.Fields(revs)
	if len(shas) == 0 {
		return nil, archiveReport{}, nil
	}
	workers := runtime.GOMAXPROCS(0)
	per := min(max((len(shas)+workers-1)/workers, historyChunkMin), historyChunkMax)
	var chunks [][]string
	for lo := 0; lo < len(shas); lo += per {
		chunks = append(chunks, shas[lo:min(lo+per, len(shas))])
	}

	results := make([][]secrets.Finding, len(chunks))
	refs := make([][]archiveRef, len(chunks))
	oci.grow(len(chunks))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for i, chunk := range chunks {
		g.Go(func() error {
			args := append([]string{"log", "-p", "-U0", "--no-color", "--no-walk=unsorted"}, chunk...)
			diff, err := scanCommand(gctx, runner, append(args, archivePatchExclude...)...)
			if err != nil {
				return err
			}
			results[i] = r.ScanDiff(diff)
			oci.scan(i, diff)
			// A patch shows only "Binary files differ" for an archive, so
			// the blob ids come from a raw diff. Without rename detection a
			// renamed archive is listed as added, and its new content is
			// scanned even when the rename changed it.
			args = append([]string{"log", "--raw", "-z", "--no-abbrev", "--no-renames", "--format=", "--no-walk=unsorted"}, chunk...)
			raw, err := scanCommand(gctx, runner, args...)
			if err != nil {
				return err
			}
			refs[i] = rawArchives(raw)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, archiveReport{}, err
	}
	var out []secrets.Finding
	for _, f := range results {
		out = append(out, f...)
	}
	found, report, err := r.scanArchiveBlobs(ctx, runner, oci, readers, slices.Concat(refs...))
	return append(out, found...), report, err
}

// archiveLimit bounds the archives the built-in scan opens. An archive over
// a limit is reported as not scanned instead of failing the check, because a
// blob deep in the history cannot be fixed by a new commit.
type archiveLimit struct {
	// bytes is the largest archive read into memory. A Spring Boot jar with
	// its dependencies is often 50 to 200 MB.
	bytes int64
	zip   secrets.ZipLimits
}

var defaultArchiveLimit = archiveLimit{
	bytes: 512 << 20,
	// A jar shaded with its dependencies lists tens of thousands of classes.
	// TotalBytes bounds the work one archive can cause: a zip of many files
	// at the entry limit would otherwise expand without end.
	zip: secrets.ZipLimits{Entries: 1 << 18, EntryBytes: 64 << 20, TotalBytes: 1 << 30},
}

func (r *SecretPatternsRule) limit() archiveLimit {
	if r.archiveLimit != nil {
		return *r.archiveLimit
	}
	return defaultArchiveLimit
}

// archiveWorkers bounds how many archives are scanned at once. Each holds its
// whole blob in memory, because a zip file is read from its end.
const archiveWorkers = 4

// DefaultArchiveReaders is how many cat-file processes read the archive
// blobs. Each one holds the blob it rebuilds. On histories of 132 to 202
// jars where the archives took most of the time, two readers took 16-27%
// less time than one for up to 0.25 GB more memory, and four took 22-39%
// less for up to 0.6 GB more.
const DefaultArchiveReaders = 2

// archiveRef is an archive blob and the path it was committed at.
type archiveRef struct{ blob, path string }

// rawArchives returns the zip archives that a `git diff --raw -z` or
// `git log --raw -z` listing adds or changes.
func rawArchives(raw string) []archiveRef {
	var out []archiveRef
	fields := strings.Split(raw, "\x00")
	for i := 0; i < len(fields); i++ {
		head, ok := strings.CutPrefix(fields[i], ":")
		if !ok {
			continue
		}
		// :old-mode new-mode old-blob new-blob status, then one path, or
		// two for a rename or copy.
		meta := strings.Fields(head)
		if len(meta) != 5 {
			continue
		}
		status := meta[4][0]
		paths := 1
		if status == 'R' || status == 'C' {
			paths = 2
		}
		if i+paths >= len(fields) {
			break
		}
		i += paths
		mode, blob, p := meta[1], meta[3], fields[i]
		// A deleted file has no new blob, and a symlink or submodule has no
		// archive content.
		if status == 'D' || (mode != "100644" && mode != "100755") || strings.Trim(blob, "0") == "" {
			continue
		}
		if secrets.IsZipArchive(p) {
			out = append(out, archiveRef{blob: blob, path: p})
		}
	}
	return out
}

// archiveResult is what scanning one archive found.
type archiveResult struct {
	findings []secrets.Finding
	opened   bool
	notice   *policy.Violation
}

// archiveReport sums the archives of a scan for the notices.
type archiveReport struct {
	opened  int
	notices []policy.Violation
}

func joinArchives(results []archiveResult) archiveReport {
	var rep archiveReport
	for _, a := range results {
		if a.opened {
			rep.opened++
		}
		if a.notice != nil {
			rep.notices = append(rep.notices, *a.notice)
		}
	}
	return rep
}

// scanArchiveBlobs reads each archive blob once, however many commits carry
// it, and scans the files inside it under each distinct path it was committed
// at: a path allowlist that skips the blob at one path must not hide it at
// another. Findings keep the order of refs. Up to readers `cat-file --batch`
// processes read the blobs. The versions of a jar are stored as deltas of
// each other, and a process per blob rebuilt each delta chain from the start
// (114s of CPU against 29s for 192 jars of 14 GB). So each process reads a
// contiguous run of refs in history order: in hash order, the same jars took
// 102s to read.
func (r *SecretPatternsRule) scanArchiveBlobs(ctx context.Context, runner git.Runner, oci *ociProfileSink, readers int, refs []archiveRef) ([]secrets.Finding, archiveReport, error) {
	// targets are the distinct (blob, path) pairs; blobs lists each blob
	// once with the indexes of its targets.
	type blobTargets struct {
		blob    string
		targets []int
	}
	var targets []archiveRef
	var blobs []blobTargets
	seenTarget := make(map[archiveRef]bool, len(refs))
	blobAt := make(map[string]int, len(refs))
	for _, ref := range refs {
		if seenTarget[ref] {
			continue
		}
		seenTarget[ref] = true
		i, ok := blobAt[ref.blob]
		if !ok {
			i = len(blobs)
			blobAt[ref.blob] = i
			blobs = append(blobs, blobTargets{blob: ref.blob})
		}
		blobs[i].targets = append(blobs[i].targets, len(targets))
		targets = append(targets, ref)
	}
	if len(blobs) == 0 {
		return nil, archiveReport{}, nil
	}
	stream, ok := runner.(git.Streamer)
	if !ok {
		return nil, archiveReport{}, errors.New("secret_patterns: the git runner cannot stream the archive blobs")
	}
	results := make([]archiveResult, len(targets))
	base := oci.grow(len(targets))

	// Sizes first, so an archive over the limit is never inflated.
	var ids strings.Builder
	for _, b := range blobs {
		ids.WriteString(b.blob + "\n")
	}
	var check bytes.Buffer
	if err := stream.Stream(ctx, strings.NewReader(ids.String()), &check, "cat-file", "--batch-check"); err != nil {
		return nil, archiveReport{}, fmt.Errorf("secret_patterns: built-in scan: %w", err)
	}
	var keep []blobTargets
	var sizes []int64
	for _, b := range blobs {
		size, err := batchHeader(&check, b.blob)
		if err != nil {
			return nil, archiveReport{}, err
		}
		if r.limit().bytes < size {
			for _, t := range b.targets {
				results[t].notice = r.tooLarge(targets[t].path, size)
			}
			continue
		}
		keep = append(keep, b)
		sizes = append(sizes, size)
	}

	// A failed reader cancels the others, so the scan fails without reading
	// the rest of their runs. The cause is the error to return: the errors of
	// the cancelled readers only echo it.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	// A slot is taken before a blob is read, so at most archiveWorkers
	// blobs are in memory at once, whatever the number of readers.
	slots := make(chan struct{}, archiveWorkers)
	var scans sync.WaitGroup
	read := func(run []blobTargets) error {
		var ids strings.Builder
		for _, b := range run {
			ids.WriteString(b.blob + "\n")
		}
		pr, pw := io.Pipe()
		done := make(chan error, 1)
		go func() {
			err := stream.Stream(ctx, strings.NewReader(ids.String()), pw, "cat-file", "--batch")
			pw.CloseWithError(err)
			done <- err
		}()
		in := bufio.NewReaderSize(pr, 1<<16)
		var readErr error
	reading:
		for _, b := range run {
			if ctx.Err() != nil {
				readErr = context.Cause(ctx)
				break
			}
			size, err := batchHeader(in, b.blob)
			if err != nil {
				readErr = err
				break
			}
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				readErr = context.Cause(ctx)
				break reading
			}
			// The content is followed by a newline.
			data := make([]byte, size+1)
			if _, err := io.ReadFull(in, data); err != nil {
				<-slots
				readErr = fmt.Errorf("secret_patterns: read %s: %w", targets[b.targets[0]].path, err)
				break
			}
			scans.Go(func() {
				defer func() { <-slots }()
				for _, t := range b.targets {
					results[t] = r.scanArchive(targets[t].path, data[:size], oci, base+t)
				}
			})
		}
		// Closing the read end ends a git process that a read error left
		// blocked on a full pipe. Close of a PipeReader always returns nil.
		_ = pr.Close()
		if err := <-done; err != nil && readErr == nil {
			readErr = fmt.Errorf("secret_patterns: built-in scan: %w", err)
		}
		return readErr
	}
	var reads sync.WaitGroup
	lo := 0
	for _, hi := range splitRuns(sizes, min(readers, len(keep))) {
		run := keep[lo:hi]
		lo = hi
		reads.Go(func() {
			if err := read(run); err != nil {
				cancel(err)
			}
		})
	}
	reads.Wait()
	scans.Wait()
	// A context that the caller cancelled ends the scan too, and a scan
	// that stopped early must not pass for a complete one.
	if err := context.Cause(ctx); err != nil {
		return nil, archiveReport{}, err
	}
	var out []secrets.Finding
	for _, a := range results {
		out = append(out, a.findings...)
	}
	return out, joinArchives(results), nil
}

// splitRuns cuts items with these sizes, in order, into at most n runs of
// about the same number of bytes, and returns the end index of each run. No
// run is empty.
func splitRuns(sizes []int64, n int) []int {
	var total int64
	for _, s := range sizes {
		total += s
	}
	var ends []int
	var acc int64
	for i, s := range sizes[:max(len(sizes)-1, 0)] {
		acc += s
		if len(ends) < n-1 && acc*int64(n) >= total*int64(len(ends)+1) {
			ends = append(ends, i+1)
		}
	}
	if len(sizes) > 0 {
		ends = append(ends, len(sizes))
	}
	return ends
}

// batchHeader reads the `<blob> blob <size>` line that `git cat-file
// --batch` and --batch-check print for blob and returns the size.
func batchHeader(r interface{ ReadString(byte) (string, error) }, blob string) (int64, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("secret_patterns: read the header of blob %s: %w", blob, err)
	}
	f := strings.Fields(line)
	if len(f) != 3 || f[0] != blob || f[1] != "blob" {
		return 0, fmt.Errorf("secret_patterns: blob %s: git cat-file printed %q", blob, strings.TrimSpace(line))
	}
	size, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("secret_patterns: size of blob %s: %w", blob, err)
	}
	return size, nil
}

// scanArchive scans each file in one zip archive under the path
// archive!file. An archive that cannot be opened is reported, not failed:
// a corrupt or truncated archive in the history stays there.
func (r *SecretPatternsRule) scanArchive(name string, data []byte, oci *ociProfileSink, slot int) archiveResult {
	if n := r.tooLarge(name, int64(len(data))); n != nil {
		return archiveResult{notice: n}
	}
	// The history holds only the pointer of an archive that Git LFS stores,
	// so there is nothing to open.
	if secrets.IsLFSPointer(data) {
		return archiveResult{}
	}
	var res archiveResult
	limit := r.limit().zip
	unread, err := secrets.WalkZip(data, limit, func(file, content string) {
		res.findings = append(res.findings, r.ScanFile(name+secrets.ArchiveSeparator+file, content)...)
		oci.scan(slot, content)
	})
	if err != nil {
		return archiveResult{notice: r.archiveNotice(name, "archive not scanned: "+err.Error(),
			"the files in it were not checked for secrets")}
	}
	res.opened = true
	if unread > 0 {
		res.notice = r.archiveNotice(name, fmt.Sprintf("archive partly scanned: %d file(s) in it could not be read", unread),
			fmt.Sprintf("a file larger than %d MiB once decompressed, a file past the first %d MiB that the archive decompresses to, a file compressed with a method other than store or deflate, or a corrupt file is not checked for secrets", mib(limit.EntryBytes), mib(limit.TotalBytes)))
	}
	return res
}

// tooLarge returns the notice for an archive over the size limit, or nil.
func (r *SecretPatternsRule) tooLarge(name string, size int64) *policy.Violation {
	if limit := r.limit().bytes; size > limit {
		return r.archiveNotice(name, fmt.Sprintf("archive not scanned: %d MiB, more than the limit of %d MiB", mib(size), mib(limit)),
			"the files in it were not checked for secrets")
	}
	return nil
}

func (r *SecretPatternsRule) archiveNotice(name, msg, hint string) *policy.Violation {
	return &policy.Violation{RuleID: r.Name(), Severity: policy.SeverityInfo, File: name, Message: msg, Hint: hint}
}

// mib rounds up, so a size over a limit never prints as equal to it.
func mib(n int64) int64 { return (n + 1<<20 - 1) >> 20 }

func (r *SecretPatternsRule) scannerLabel() string {
	if r.ScannerLabel != nil {
		return r.ScannerLabel()
	}
	return fmt.Sprintf("%d built-in patterns", len(secrets.BuiltinPatterns))
}

func (r *SecretPatternsRule) embeddedNotice(in policy.Input) policy.Violation {
	return policy.Violation{
		RuleID:   r.Name(),
		Severity: policy.SeverityInfo,
		Message:  "scanned " + scopeLabel(in) + " with " + r.scannerLabel(),
		Hint:     "gitleaks runs instead when .gitleaks.toml or .gitleaksignore is present",
	}
}

func (r *SecretPatternsRule) builtinNotice(in policy.Input) policy.Violation {
	return policy.Violation{
		RuleID:   r.Name(),
		Severity: policy.SeverityInfo,
		Message:  "gitleaks not installed; scanned " + scopeLabel(in) + " with " + r.scannerLabel(),
		Hint:     "brew install gitleaks for the latest rules and .gitleaks.toml support  (gk doctor will confirm)",
	}
}

// convertFindings maps gitleaks findings into policy.Violations. Exported
// within the package so unit tests can verify the mapping without spawning
// gitleaks.
func convertFindings(findings []scan.GitleaksFinding) []policy.Violation {
	out := make([]policy.Violation, 0, len(findings))
	for _, f := range findings {
		msg := f.Description
		if msg == "" {
			msg = "secret matched rule " + f.RuleID
		}
		out = append(out, policy.Violation{
			RuleID:   "secret_patterns",
			Severity: policy.SeverityError,
			File:     f.File,
			Line:     f.StartLine,
			Message:  fmt.Sprintf("%s (%s)", msg, f.RuleID),
			Hint:     "rotate the secret and add a justified allowlist entry if it was intentional",
		})
	}
	return out
}
