# gk v2 Roadmap

This document is the canonical plan for gk v2 (target: v1.0.0 release) and the v1.1+ backlog. It consolidates the v2 brainstorm, `gk timemachine` design, the 62-leaf decomposition, non-functional requirements, the v1.1+ bias-corrected backlog, and the 2026-04-22 probe verdict (🔄 RETHINK) that repositioned F2/F3.

| Field | Value |
|-------|-------|
| Current version | v0.6.0 |
| Target | v1.0.0 |
| Effort (3-way parallel) | **9–11 calendar days** (post-probe revision) |
| Effort (serial, single dev) | ~28 dev-days |
| Total LoC estimate | ~11,800 (prod + tests) |
| New top-level deps | none (bubbletea 1.3.6, lipgloss 1.1.0, fsnotify 1.9.0 already in `go.mod`) |
| Critical path | Stream A+B, 9 dev-days serial |
| Probe verdict | 🔄 RETHINK (`.xm/probe/last-verdict.json`) — conditional PROCEED with F2/F3 pivot + kill criteria |

---

## Themes

v2 ships three flagship features that reinforce gk's identity: **safer pushes · ergonomic diagnostics · rich visualization**.

### F1 — `gk timemachine`

Reflog-based recovery TUI. A unified event stream (HEAD reflog + per-branch reflogs + `refs/gk/*-backup` + optional stash/dangling) with a bubbletea 3-pane viewer and a two-stage dry-run modal for restore. Replaces the memorize-undo/wipe/restore workflow with a visual surface.

### F2 — `gk guard` / policies-as-code (orchestrator positioning)

Declarative `policies:` block in `.gk.yaml` unifies protected-branch, signing, commit-size, required-trailers, cooldown, and secret-scan rules. `gk guard check` emits machine-readable JSON for CI. Auto-wires into pre-commit / pre-push hooks.

**Post-probe positioning:** gk guard is **not** a competing ecosystem — it orchestrates existing best-of-breed tools (gitleaks for secrets, commitlint for commit messages) behind a single bundled-binary surface, with native rules for policies those tools don't cover. This sidesteps the "build-vs-integrate" trap that would block enterprise adoption.

### F3 — Secret scanning (gitleaks-first, native v2 as optional air-gap backend)

**Primary backend: gitleaks adapter.** `gk guard` detects a gitleaks binary via `gk doctor`; when present, the `secret_patterns` rule invokes gitleaks with the project's config. This is the default for v1.0.

**Optional backend: native scanner v2.** Shannon entropy + curated language-aware detectors (JWT / AWS / GCP / Stripe / Slack / OpenAI / PEM) + `.gk/allow.yaml` with TTL/justification + staged-hunk walker. Ships as `--scanner=native` for air-gapped environments where gitleaks is not present or not approved.

**Post-probe cuts:** SHA-signed regex packs, `gk secrets update` (remote pack fetcher), and baseline sync (`gk secrets baseline pull/push`) are **deferred to v1.3+**. gitleaks already covers the common path; the deferred surface ships only if air-gap demand materializes.

---

## Ship Slices

Incremental releases. Each slice is independently shippable.

| Version | Contents | Incremental days |
|---------|----------|-----------------:|
| **v0.7** | Infra + Shared libs + timemachine readonly TUI (browse, `--json`, no restore) + PERF-01 frame budget | 4–5 |
| **v0.8** | timemachine restore GA + dry-run modal + PERF-03 latency budget | +1.5 |
| **v0.9** | guard MVP with **gitleaks adapter default** (`secret_patterns` → gitleaks) + hooks auto-wire + TELE-01 | +2 |
| **v1.0** | native scanner v2 optional (`--scanner=native`) + MIG-01 + PERF-02 + TELE-02 + docs | +1.5–2 |

v0.9 is the **probe gate**: ship 8 weeks before committing v1.0 scanner investment. See [Kill Criteria](#kill-criteria) below.

---

## Decomposition — 62 Leaves

### L1.A Infrastructure & Testing (7 leaves, deps: none — 6-way parallel)

| ID | Leaf | Est days |
|----|------|---------:|
| INFRA-01 | `testutil.Repo` extensions — seed reflog, detached commits, backup refs, dirty tree | 0.5 |
| INFRA-02 | Hex-encoded reflog golden generator — `testdata/reflog/gen.go -update` | 0.75 |
| INFRA-03 | teatest harness + ANSI-strip snapshot helpers | 0.5 |
| INFRA-04 | FakeRunner script/record extensions for `update-ref -z` | 0.5 |
| INFRA-05 | CI matrix — macOS / Linux × git 2.30 / 2.39 / latest + `-race` + integration lane | 0.5 |
| INFRA-06 | Golden-diff snapshot helper (`testutil/golden`) | 0.25 |
| INFRA-07 | Backup-ref cross-version coexistence test (`-tags=crossver`) | 0.5 |

### L1.B Shared Libraries (7 leaves, deps: INFRA-*)

| ID | Leaf | Est days | Notes |
|----|------|---------:|-------|
| SHARED-01 | Extract `internal/gitsafe` with `Preflight` | 1.5 | Fixes `undo.go:L183-L185` type-assertion bug; adds preflight to `wipe` (currently missing) |
| SHARED-02 | `.gk.yaml` v2 schema + YAML loader + JSON Schema | 0.75 | |
| SHARED-03 | Config resolution merge with `Source` metadata | 0.5 | |
| SHARED-04 | `Restorer` interface (`Backup/Restore/Preflight/ResolveRef/HeadSHA/ListBackups`) | 1.5 | |
| SHARED-05 | `policy.Rule` interface + Registry | 0.25 | |
| SHARED-06 | `scan.Detector` interface + `Token/Match/Allowlist` types | 0.25 | |
| SHARED-07 | `BackupNamer` helper — `refs/gk/<kind>-backup/<branch>/<unix>-<nanos>-<pid>` | 0.5 | Dedupes `undo.go:L222-L230` and `wipe.go:L101-L110` |

### L1.C `gk timemachine` (19 leaves, deps: SHARED + INFRA-03)

**L2.C.1 — Data layer**

| ID | Leaf | Est days |
|----|------|---------:|
| TM-01 | HEAD reflog event source | 0.5 |
| TM-02 | Per-branch reflog event source (errgroup) | 0.5 |
| TM-03 | `refs/gk/*-backup` scanner | 0.25 |
| TM-04 | Stash + dangling scanner (`fsck --lost-found`, opt-in) | 0.5 |
| TM-05 | Event merge + dedupe + sort | 0.5 |
| TM-06 | Filter pipeline (`--branch / --since / --kinds / --head-only`) | 0.5 |
| TM-07 | Diff service — LRU(64) + 100ms debounce + `context.CancelFunc` | 0.75 |
| TM-08 | Disk cache — `$GIT_COMMON_DIR/gk/timemachine-cache.json` with watermark `{ref, oldest/newest/tip oid, mtime, size}` | 0.75 |

**L2.C.2 — TUI**

| ID | Leaf | Est days |
|----|------|---------:|
| TM-09 | bubbletea 3/2/1 responsive pane skeleton + lipgloss styles | 1.0 |
| TM-10 | Event list pane + keybindings (j/k, g/G, `/`, `f`) | 0.5 |
| TM-11 | Detail pane (Enter expand) | 0.5 |
| TM-12 | Diff pane wired to TM-07 | 0.5 |
| TM-13 | fsnotify watcher + 5s mtime poll fallback | 0.5 |
| TM-14 | Dry-run modal with hard-reset decision table (see below) | 2.5 |

**L2.C.3 — CLI & headless**

| ID | Leaf | Est days |
|----|------|---------:|
| TM-15 | `gk timemachine` cobra command + flags | 0.5 |
| TM-16 | `--no-tui --json` NDJSON renderer (one entry per line) | 0.5 |
| TM-17 | `gk timemachine restore <sha\|ref>` subcommand | 0.5 |
| TM-18 | `Restorer` impl — atomic `update-ref -z` + dirty ordering + autostash | 3.0 |
| TM-19 | E2E teatest + goldens | 1.0 |

### L1.D `gk guard` (10 leaves)

**L2.D.1 — Rule engine**

| ID | Leaf | Est days |
|----|------|---------:|
| GD-01 | `internal/policy` engine (parallel eval + aggregation) | 0.75 |
| GD-02 | Rule: `max_commit_size` | 0.25 |
| GD-03 | Rule: `required_trailers` | 0.25 |
| GD-04 | Rule: `forbid_force_push_to` (pre-push stdin) | 0.5 |
| GD-05 | Rule: `max_commit_age_days` | 0.25 |
| GD-06 | Rule: `require_signed` (`%G?`) | 0.25 |
| GD-07 | Rule: `secret_patterns` adapter (thin wrapper over SCAN-10) | 0.25 |

**L2.D.2 — CLI & hooks**

| ID | Leaf | Est days |
|----|------|---------:|
| GD-08 | `gk guard check` CLI + JSON output + exit codes | 0.5 |
| GD-09 | `gk guard init` scaffold | 0.25 |
| GD-10 | Hooks auto-wiring (pre-commit / pre-push shims) | 0.5 |

### L1.E scanner (7 active + 4 deferred, post-probe)

**L2.E.0 — gitleaks adapter (v0.9 default)**

| ID | Leaf | Est days |
|----|------|---------:|
| SCAN-00 | **`gitleaks` adapter** — doctor detection + invoke with project config + finding translation | 0.5 |
| SCAN-10 | Wire SCAN-00 into GD-07 as default `secret_patterns` evaluator | 0.25 (thin wiring) |

**L2.E.1 — Core native scanner (v1.0 optional, `--scanner=native`)**

| ID | Leaf | Est days |
|----|------|---------:|
| SCAN-01 | Shannon entropy scorer (base64/hex-aware) | 0.5 |
| SCAN-02 | Language-aware detectors (JWT, AWS, GCP, Stripe, Slack, OpenAI, PEM) — capped at 7 for v1.0 | 1.0 |
| SCAN-03 | Detector registry + dispatch | 0.25 |
| SCAN-04 | Staged-hunk walker (`git diff --cached -U0`) | 0.5 |

**L2.E.2 — Allowlist**

| ID | Leaf | Est days |
|----|------|---------:|
| SCAN-07 | `.gk/allow.yaml` loader + doctor expiry check | 0.5 |
| SCAN-11 | Replace v1 keyword scanner call sites | 0.5 |

**Deferred to v1.3+ (post-probe cuts, save ~2.5 days)**

| ID | Leaf | Rationale |
|----|------|-----------|
| ~~SCAN-05~~ | ~~SHA-signed regex pack format~~ | gitleaks packs already solve this; revisit if air-gap demand emerges |
| ~~SCAN-06~~ | ~~`gk secrets update` remote fetch~~ | same as SCAN-05 |
| ~~SCAN-08~~ | ~~HMAC-SHA256 fingerprint calculator~~ | baseline sync feature; defer |
| ~~SCAN-09~~ | ~~`gk secrets baseline pull/push`~~ | defer to v1.3, gate on scenario B probe signal |

### L1.F Non-Functional Requirements (NFR, 5 leaves)

| ID | Leaf | Stream | Est days |
|----|------|--------|---------:|
| PERF-01 | TUI frame time budget <50ms p95 (teatest + bench) | B | 1.0 |
| PERF-02 | Scanner throughput >10k LOC/s budget | E | 0.75 |
| PERF-03 | Restore latency cold <200ms / warm <80ms p95 | A | 0.5 |
| TELE-01 | NDJSON event emitter — opt-in, flock, 10MB × 10-file rotation, **no external transmit** | A | 1.5 |
| TELE-02 | `gk metrics` viewer + `gk doctor` health integration | A | 1.0 |
| MIG-01 | `.gk.yaml` v1 → v2 migration with `.v1.bak` + `gk config migrate --rollback` | A | 1.5 |

### L1.G Release (4 leaves)

| ID | Leaf | Est days |
|----|------|---------:|
| REL-01 | Docs (timemachine / guard / secrets / commands / config) | 1.0 |
| REL-02 | Migration guide v1 → v2 | 0.25 |
| REL-03 | Changelog + version bump to 1.0.0 | 0.25 |
| REL-04 | goreleaser / signed tags / secrets pack URL | 0.5 |
| REL-05 | Dogfood pass on gk repo + telemetry-captured bug list | 0.5 |

---

## Critical Path

Stream A + B serial chain (gates the release):

```
SHARED-07 (0.5d) → SHARED-01 (1.5d) → SHARED-04 (1.5d) → TM-18 (3d) → TM-14 (2.5d)
                                                                    → TM-19 (1d)
                                                                    → REL-01 → REL-03
```

Total serial: **9 dev-days** (Stream A+B).

Stream E runs in parallel on F3: SCAN-10 (3.5d) + GD-07 (0.5d) = **4 dev-days**, same owner to avoid seam thrash.

---

## Stream Ownership

To prevent coupling thrash at F2↔F3 seam (GD-07 ↔ SCAN-10) and at SHARED-02 schema lock:

| Stream | Owns | Total |
|--------|------|------:|
| **A — `gitsafe` / infra** | SHARED-01, SHARED-04, SHARED-07, TM-18, PERF-03, TELE-01/02, MIG-01, INFRA-07 | ~11.5d |
| **B — UI / UX** | TM-09..14, PERF-01 | ~3.5d |
| **C — `timemachine` data** | TM-01..08 | ~4.25d |
| **D — `guard`** | GD-01..06, GD-08..10 | ~3.5d |
| **E — scanner** | SCAN-01..11, GD-07, PERF-02 | ~9d |

Streams C and D can run in parallel once SHARED-* lands. Stream E runs independently.

---

## Hidden Coupling — Serialize or Single-Owner

1. **TM-07 diff service ↔ TM-12 diff pane** — debounce/cancel semantics must match. Single dev or tight pairing.
2. **TM-05 merge → TM-08 cache** — watermark keys leak through merge API. Land TM-05 first, freeze `Event` struct, then TM-08.
3. **SHARED-02/03/05/06 schema lock** — all read the same schema. Lock SHARED-02 before anyone starts.
4. **GD-07 ↔ SCAN-10 (F2↔F3 seam)** — single owner (Stream E) in the same PR.
5. **TM-13 fsnotify ↔ TM-08 cache** — both touch `$GIT_COMMON_DIR/gk/`. Decide cache-path ownership first.
6. **SHARED-07 ↔ TM-18** — ref-name format is the contract. Freeze SHARED-07 (incl. `<unix>-<nanos>-<pid>` collision-avoidance) before TM-18 starts.

---

## TM-18 Runner Call Map

The Restorer implementation (TM-18) is the single biggest gate. Explicit argv for each step:

| Step | `runner.Run(ctx, ...)` | Failure mode |
|-----:|------------------------|--------------|
| 1 | `rev-parse --verify HEAD^{commit}` | Abort; nothing written |
| 2 | `update-ref -z <backupRef> <sha> ''` (atomic create-only) | Abort; nothing written |
| 3a | `stash push --include-untracked -m "gk-timemachine-autostash"` (conditional) | `update-ref -d <backupRef>` rollback |
| 4 | `reset --mixed|--hard|--soft|--keep <targetSHA>` | Backup ref intact; recovery: `git reset --hard <backupRef>` |
| 5 | `stash pop --index` (iff 3a ran AND 4 succeeded) | Conflict: set `Result.AutostashRef`, skip pop |
| 6 | `rev-parse --verify HEAD^{commit}` (confirm) | Warning-only; backup ref still good |

**Ordering invariant:** step 3a MUST NOT run before step 2. Crash between stash and update-ref would leave stash orphaned with no recovery anchor.

---

## TM-14 Hard-Reset Decision Table

Codified as `func DecideStrategy(dirty, autostash bool, key rune) (Strategy, error)`:

| dirty | autostash | key | → Strategy |
|:-----:|:---------:|:---:|------------|
| false | — | `y` | Mixed |
| false | — | `R` | Hard |
| true | true | `y` | Hard + autostash |
| true | false | `y` | Keep (`--keep`) |
| true | false | `R` | **block** — require `--force-discard` |
| any | — | `r` | preview Keep (no commit) |
| any | — | `n` / `Esc` | abort |

`R` always opens a confirmation modal with a red banner showing file count + backup ref path + the exact recovery command (`gk timemachine restore <backupRef> --mode hard`). Large or history-rewriting operations require typing "hard" to confirm; small non-destructive ones accept `y/N` with default N.

---

## `gk timemachine` CLI

```
gk timemachine                                          # full TUI, all scopes
gk timemachine --branch <name> --since <dur>
gk timemachine --kinds reflog,backup,stash,dangling
gk timemachine --head-only --include-dangling
gk timemachine --no-tui --json                          # NDJSON stream
gk timemachine --format lanes                           # ASCII lanes (absorbs #1 "gk timeline")
gk timemachine restore <sha|ref> [--mode soft|mixed|hard] [--dry-run] [--autostash|--force]
```

`--include-dangling` runs `git fsck --lost-found` in a background goroutine bound to the `tea.Program` context (cancelled on exit). The 500-entry cap is partial-show (progress counter in the status bar); `--dangling-cap N` overrides, `--dangling-cap 0` is unlimited with a warning.

---

## Restorer Interface

```go
// internal/gitsafe/restorer.go
type Restorer interface {
    Backup(ctx context.Context, branch string) (refName string, err error)
    Restore(ctx context.Context, target Target, s Strategy, opts ...RestoreOption) (Result, error)
    Preflight(ctx context.Context) error
    ResolveRef(ctx context.Context, ref string) (sha string, err error)
    HeadSHA(ctx context.Context) (sha string, err error)
    ListBackups(ctx context.Context) ([]BackupRef, error)
}

type Strategy int
const (
    StrategyMixed Strategy = iota // reset --mixed (undo default)
    StrategyHard                  // reset --hard (wipe, timemachine "R")
    StrategySoft                  // reset --soft
    StrategyKeep                  // reset --keep (dirty tree)
)

type RestoreOption func(*restoreOpts)
func WithAutostash(enabled bool) RestoreOption
func WithBranch(branch string) RestoreOption
```

---

## Design Principles

1. **Backup before restore, always.** Every HEAD-moving operation writes `refs/gk/timemachine-backup/<branch>/<unix>-<nanos>-<pid>` at the current tip *before* any mutating git call. Ref creation uses `git update-ref -z <ref> <new> ''` so it fails atomically if the name already exists.

2. **Ordering is a contract, not a suggestion.** Backup → stash → abort-before-reset if stash fails. Never stash-then-backup.

3. **`R` is never a single keystroke.** Hard-reset always goes through a modal. Large operations require a typed confirmation, not just `y`.

4. **Reuse over rewrite.** Extend `internal/reflog`, `internal/git.Runner`, `internal/ui.Pager`, `internal/gitstate`. Don't fork `classifyAction`; export it if extended.

5. **Privacy in telemetry is non-negotiable.** Event logs are local-only. Bodies contain hashes, counts, and categories — never file paths, commit messages, or secret content.

6. **Shippable slices.** Each release (v0.7 → v1.0) is independently useful. v0.9 deliberately lets `gk guard` use the legacy scanner so it does not block on scanner v2.

---

## v1.1+ Backlog

The v2 brainstorm was biased toward safety + visualization. A bias-corrected 2nd pass covers the four themes absent from v2's Top 4.

### v1.1 — AI augmentation + Team light (~7–9 days, 3-way parallel)

Prerequisites (must land first):

- **A4 — Privacy Gate.** Every `gk ai *` invocation routes outbound payloads through an entropy-scanner redactor; output tokenized (`[SECRET_1]`, `[PATH_REPO_ROOT]`). `--show-prompt` inspects the exact payload. Audit trail appended to `.gk/ai-audit.jsonl`.
- **A5 — `--provider local`.** First-class Ollama (`http://localhost:11434`) and Apple MLX backends. `GK_AI_OFFLINE=1` kill-switch refuses external providers.

Flagship leaves:

| ID | Command | Description |
|----|---------|-------------|
| A1 | `gk ai commit` | Staged diff + last 20-commit style sample → CC subject + body + trailer |
| A2 | `gk ai pr` | `merge-base..HEAD` → structured PR body (Summary / Changes / Risk / Test Plan) |
| P1 | `gk review` | Local PR dry-run scorecard: commitlint, CODEOWNERS, churn, TODO/FIXME delta, coverage delta (T4 + P7 merged) |
| T8 | `gk team sync` | Pull team profile (`.gk.yaml`, rulesets, commit templates) from a profile repo/branch |

Supporting:

| ID | Command |
|----|---------|
| T1 | `gk coauthor add @handle --for 2h` — timeboxed Co-authored-by trailer |
| T6 | `gk digest --since yesterday --by-author` — standup digest |
| M4 | `gk doctor --supply-chain` — submodule pin freshness + signing + CVE alerts |
| A9 | `gk doctor --ai` — LLM-augmented remediation, offline fallback |

### v1.2 — Multi-repo + workflow depth (~8–10 days)

Prerequisite:

- **M3 — `.gk/workspaces.yaml`** — declarative repo sets with command presets

Flagship bundle:

| ID | Command | Description |
|----|---------|-------------|
| M1 | `gk meta init/sync/status <name>` | Named meta-branch across N sibling repos |
| M2 | `gk swarm <cmd>` | Fan-out any gk subcommand with `--vis grid` |
| P5 | `gk conflict` | Conflict triage TUI (ownership + churn + difficulty) |
| P6 | `gk rewrite` | Safe interactive rebase with reflog undo (optional `--ai` via A6) |

Supporting: T2 `gk handoff`, T3 `gk pair`, T9 handoff runbook, M5 `gk sub update --safe`, A7 `gk ai changelog`.

### v1.3 — Advanced automation (PR-by-PR)

13 ideas across four themes, committed individually based on demand:

- **AI:** A3 `gk ai resolve` · A8 `gk ask` (NL → plan) · A10 `gk ai eval` (local prompt quality sqlite)
- **Team:** T5 `gk review queue` · T7 `gk notify` (webhook opt-in)
- **Multi-repo:** M6 `gk xgrep` · M7 `gk xcheck` · M8 `gk release coordinate` · M9 `gk fleet` · M10 `gk clone --fleet`
- **PR:** P2 `gk thread` (forge API) · P8 `gk check` (local CI replica) · P9 `gk stack` (stacked PR)

---

## Cross-theme Synergies (to avoid duplication)

| Merge | Rationale |
|-------|-----------|
| T4 `gk review suggest` ≡ P7 `gk suggest-reviewers` | Reviewer routing is the same feature |
| A7 `gk ai changelog` ⊇ P4 `gk notes` | AI changelog is the LLM-augmented superset; P4 is the rule-based fallback |
| A2 `gk ai pr` + P3 `gk pr prep` | `gk pr prep --ai` combines scorecard + AI narrative |
| A6 `gk ai rebase` + P6 `gk rewrite` | `gk rewrite --ai` combines safe rebase + AI todo planner |
| T6 `gk digest` + A1/A2 | `gk digest --ai` for LLM-polished standup summaries |
| M3 + M1 + M2 | `workspaces.yaml` → `meta-branch` → `swarm` is a required sequence |

---

## Out of Scope

- **Cloud-hosted features.** gk remains offline-first. Any network feature (forge API, AI provider, webhook) is opt-in.
- **Managed runner / CI replacement.** `gk check` (v1.3) replicates the *local-relevant* subset of CI jobs, not the full pipeline.
- **GUI client.** gk is CLI + TUI only.
- **Competing with gitleaks/commitlint.** Per the 2026-04-22 probe, gk orchestrates these tools rather than rebuilding them. Only air-gapped users are served by the native scanner (optional, v1.0).

## Kill Criteria

The v2 plan was validated by `/xm:probe` on 2026-04-22 (verdict: 🔄 RETHINK). Two premises were refuted: P3 ("competitors have not claimed this space" — lazygit/gitui ship reflog restore) and P5 ("gk should build its own entropy scanner" — gitleaks is the industry standard).

The native scanner (F3) survives only as an optional air-gap backend, gated by the following explicit kill criteria:

**Gate:** v0.9 launch + 8 weeks.

**Trigger (any 2 of 3):**

1. GitHub issues tagged "enterprise-compliance" < 3 over the 8-week window
2. Ratio of `gk guard check --using gitleaks` to `--using native` invocations > 10:1
3. `gk doctor` reports a gitleaks binary present on > 50% of opt-in telemetry submissions

**Action on trigger:** cancel native scanner v1.0 investment (SCAN-01..04, SCAN-07, SCAN-11) and ship gitleaks adapter as the sole `secret_patterns` backend for v1.0. This saves ~3 dev-days of late-stage work on a feature with no demand signal.

**Evidence gaps to fill during v0.7–v0.9:**

- P1 (reflog difficulty) and P2 (recovery frequency) — measure via v0.7 readonly timemachine launch: GitHub stars, issue filings, opt-in telemetry of the TUI command
- P6 (v1 scanner miss-rate) — optional experiment: run `gk push` against a scraped corpus of known-leaky public commits and count false negatives

## Probe Pivots — Plan Changes from `.xm/probe/last-verdict.json`

Summary of the post-probe refactor already reflected in the sections above:

| Change | Driver | Savings |
|--------|--------|--------:|
| F3 scanner v2 → optional backend; gitleaks adapter default | P5 refuted | ~2.5 dev-days |
| SCAN-05/06 (signed regex packs + fetch) dropped | Gitleaks covers it | included above |
| SCAN-08/09 (baseline sync) deferred to v1.3 | No v1.0 demand signal | included above |
| F2 `gk guard` repositioned as orchestrator over gitleaks/commitlint | P4 weakened — compete-vs-integrate trap | 0 LoC savings, re-framing only |
| F1 `gk timemachine` unchanged | Differentiation (backup-before-restore, unified events) holds | — |
| Added explicit kill criteria | Pre-mortem scenario B (enterprise blocker) | future optionality preserved |

Total: 3-way parallel schedule **11–13d → 9–11d**; LoC **13.4k → 11.8k**.

---

## Ownership Risk Register

| Risk | Impact | Mitigation |
|------|--------|------------|
| SHARED-01 slippage | TM-18 delays, restore ships late | Timebox to 1 day; ship minimal `Preflight` surface first |
| teatest flakiness on CI | UI tests red on Linux | ANSI-strip snapshots; pin `TERM=xterm-256color`; `-tags=tui` opt-out |
| `update-ref -z` error-output variance across git 2.30 / 2.39 / latest | Restore error path unstable | INFRA-05 matrix catches it; budget +0.5d for TM-18 |
| fsnotify on macOS + linked worktrees | TM-13 unreliable on worktree setups | Include worktree case in INFRA-01; integration lane covers it |
| SCAN-02 scope creep | v1.0 scanner ships late | Cap at 7 detectors for v1.0; Azure / GitHub / Terraform deferred to v1.1 |
| TM-18 atomic rollback gaps | Data loss on partial failure | Property test: random restore sequences must always return to initial SHA |

---

## References

- Existing code extension points:
  - `internal/cli/undo.go:L178-L202` — Preflight source for SHARED-01
  - `internal/cli/undo.go:L222-L230` / `internal/cli/wipe.go:L101-L110` — Backup-ref naming for SHARED-07
  - `internal/cli/undo.go:L130-L146` — Restore dance migrated to TM-18
  - `internal/reflog/reflog.go:L14-L23` + `action.go:L23` — Reused by TM-01..03
  - `internal/secrets/patterns.go:L24-L33` — Promoted to `scan.Ruleset` by SCAN-10
  - `internal/cli/hooks.go:L31-L44` — Extension point for GD-10
  - `internal/testutil/repo.go:L1-L151` — Extended by INFRA-01

- Latent bugs incidentally fixed during v2 extraction:
  - `internal/cli/wipe.go:L38-L48` currently has no preflight; `gitsafe.Check(AllowDirty())` added in SHARED-01.
  - `internal/cli/undo.go:L183-L185` `*git.ExecRunner` type-assertion; replaced by `WithWorkDir(dir)` option in SHARED-01.
