---
name: roadmap
description: Find git-kit improvement points from local agent session evidence — runs `gk session audit`, reads the recorded adoption trend (`~/.gk/audit-history.jsonl`), interprets the signals (turn-reduction first, then coverage gaps, adoption leaks, and trend trajectory), and produces a prioritized, evidence-backed roadmap for gk itself. Use when the user asks "what should gk improve", "find improvement points", "dogfood the sessions", "what to build next", "is adoption actually improving", or "/roadmap". Finds and ranks; it does NOT implement.
---

# roadmap — turn gk's own session audit into a prioritized improvement list

`gk session audit` reads local Codex/Claude session logs and reports where agents
fell back to raw git. This skill is the **judgment layer on top**: it runs the
audit, classifies each signal by *what kind of work it implies*, and ranks by the
only metric that matters for gk — **does fixing this reduce agent turns?** Output
is a roadmap, not edits. The user decides what to build.

## Operating principle

**gk exists to cut turns (fewer tool calls → higher accuracy + speed).** So a raw
git finding is only valuable to fix if fixing it removes turns. A 1:1 command swap
(`git clone` → `gk clone`) saves ~0 turns — it is correctness, not leverage. Lead
every report with the turn-reduction signal; treat raw occurrence counts as
secondary. Never rank by raw count alone.

## Binary resolution (do this first)

The PATH `gk`/`gk-dev` is the *previous* release and may lack `--metric=turns`.
Build the workspace binary and use it for the audit:

```bash
go build -o bin/gk ./cmd/gk
```

Every `gk ...` below means `./bin/gk ...`.

## Phase 1 — RUN

```bash
./bin/gk session audit --metric=both --full --json --max-files 2339 > /tmp/audit.json
```

`--metric=both` gives the occurrence findings AND the turn-reduction view in one
pass. `--full` is required here: the default JSON is token-lean and caps
`result.turns.runs[].commands` and `result.findings[].evidence` (the very
samples this skill cites as evidence) — `--full` restores the uncapped payload.
Raise `--max-files` to cover the whole corpus (the run is local, read-only,
and cheap). Parse the `{state, ok, result}` envelope: `result.adoption`,
`result.findings[]`, `result.turns`, `result.totals`, `result.notes`.

## Phase 1b — TREND (historical trajectory, read when available)

`~/.gk/audit-history.jsonl` accumulates one line per `gk session audit --record`
run (fields: `ts`, `files`, `git_turns`, `estimated_turns_saved`, `rate`,
`adoption_rate`, `by_group`). Read it directly:

```bash
cat ~/.gk/audit-history.jsonl   # JSONL, oldest → newest
```

`gk session audit --trend --json` also works: the envelope carries the recorded
history as `result.trend[]` (same entry shape as the JSONL lines). Either path
is fine — the file read is simplest when you only want the history; the flag
fuses it into the same call as the audit. (`--viz` remains human-only by design:
its turn graph is just an ASCII rendering of `result.turns.runs`, which the JSON
already carries.)

- **< 2 entries**: no trend to report. Say so plainly. As a *report action* (not
  a finding about gk itself), recommend seeding a cadence — `gk session audit
  --record` on a weekly cron/launchd job — so the next roadmap run has something
  to compare against.
- **≥ 2 entries**: diff the oldest vs. newest `adoption_rate` (↑ = better) and
  `rate` (= `estimated_turns_saved / git_turns`, the share of git-touching turns
  still collapsible to raw git — ↓ = better). Don't read raw `estimated_turns_saved`
  alone as a trend signal: it's an absolute count of remaining gap (class A: "how
  many round-trips gk adoption would remove"), so it scales with corpus size — a
  rise can mean more raw-git usage crept in, not less. Cross-reference entry `ts`
  against `git log`/CHANGELOG dates for guidance fixes (hook installs,
  CLAUDE.md/AGENTS.md contract edits) that landed inside that window. One
  caveat: entries recorded before vs. after the 2026-07 turn-estimator accuracy
  fix are different baselines (the fix stopped double-counting turns), so expect
  a one-time step drop in `estimated_turns_saved`/`rate` across that boundary —
  do not read that step as an adoption regression.

## Phase 2 — INTERPRET (five signal classes)

Read each class and tag every item with a **type** and a **turn-impact**.

### A. Turn-reduction — the primary signal (`result.turns`)
- `estimated_turns_saved`, `rate` — the headline: how many round-trips gk adoption would remove.
- `by_group` — **which single gk call, if adopted, saves the most turns.** This is the ranked roadmap for *guidance/hook* work (the command already exists; agents aren't reaching for it). The top group is usually the highest-leverage action in the whole report.
- `runs[]` — evidence: the actual collapsible turn spans + the gk call that replaces them.

### B. Coverage gaps — `uncovered-raw-git` (`result.findings[].subcommands`)
For each frequent subcommand, **check the real gk surface before judging** — never
claim a verb is missing without confirming:

```bash
./bin/gk --help                 # is there a top-level verb for this?
./bin/gk <verb> --help          # does an existing verb already cover it?
```

Then classify:
- **classifier-gap** (cheap fix, high value for the audit's own accuracy): a gk verb EXISTS but the audit reports the raw form as uncovered → fix the mapping in `internal/sessionaudit/audit.go` (`gitSegmentFinding` / `collapseGroupForKind`). `gitSegmentFinding` is the single source shared with the inline `Hint`, so one classifier improves audit + hook + turn metric at once. (Past examples: `git clone`→`gk clone`, `git filter-repo`→`gk forget` were unmapped.)
- **missing-command** (build candidate): a write op with no gk equivalent (e.g. `git remote add`/`set-url`, `git mv`). Weigh turn-impact — most are single-call ops (low turn leverage); say so.
- **noise** (refine the classifier, don't build): read-only plumbing the audit should never flag (`git remote -v`/`get-url`/`show`, anything `cd`-only). If it's slipping into the gap, propose adding it to `rawGitNonGap`.

### C. Adoption leaks — covered high-count findings
`raw-context-probes`, `raw-commit-sequence`, `raw-integration`, etc. with large
counts: the capability EXISTS, agents skip it. This is an **adoption-gap** —
guidance work (`gk agents` contract block / CLAUDE.md, or the PreToolUse hook),
**not a build task.** Cross-reference with `by_group` from class A: a covered
finding that also tops the turns-saved breakdown is the strongest adoption target.

### D. Already-one-turn — `shell-chain`
A `git … && git …` chain is already one turn, so `gk batch` saves 0 happy-path
turns (only failure-recovery turns). Low turn-impact — note it, don't over-rank.

### E. Trend — is the trajectory actually moving? (Phase 1b history)
- Flat or rising `rate` across recorded runs **despite** a guidance/hook fix
  landing in that window (regardless of what `adoption_rate` does) → the fix
  isn't shrinking the remaining raw-git gap. The report's next move should
  escalate the same lever (e.g. hook `--mode warn` → `--mode block`), not ship
  another hint on top of an unproven one.
- Rising `adoption_rate` **and** falling `rate`, correlated with a shipped
  fix's timestamp → treat that fix's *approach* as validated; prefer repeating
  the same pattern (classifier fix, hook, contract-block edit) for the next
  `by_group` target over a novel approach. Don't use a rising raw
  `estimated_turns_saved` alone as a validation signal — see 1b.
- No history yet → don't fabricate a trend from one snapshot. That's a gap in
  measurement cadence, not a gk capability gap — report it as such (see 1b).

## Phase 3 — REPORT (prioritized, evidence-backed)

Rank by turn-impact, not count. Emit a table:

| # | Improvement | Type | Turn-impact | Evidence (finding · count · sample) | Suggested action |
|---|---|---|---|---|---|

Where Phase 1b has ≥ 2 history entries, prepend a one-line trajectory note before
the headline, e.g. `adoption 54%→61%, rate 8%→5% over 4 recorded runs (↑/↓, both
good) since <fix>` or `adoption flat at ~56%, rate flat at ~8% across 5 runs
despite <fix> — escalate`. If there's no history yet, note that instead of a
trend line.

Then one headline line: **highest-leverage next move** (usually either the top
`by_group` adoption target, or a cheap classifier-gap that unblocks accurate
measurement). Each row MUST cite a concrete finding + a sample command from the
audit — no improvement without session evidence, no "missing" without a checked
`gk --help`. Trend claims (class E) cite the entry timestamps compared, not just
the delta.

## Hard boundaries
- **Find and rank only — do not implement.** Offer to build the top item; let the user choose.
- Every claim is evidence-backed (audit finding + sample) and surface-checked (`gk --help`); flag uncertainty instead of asserting.
- Distinguish the states honestly: classifier-gap (fix audit), missing-command (build), adoption-gap (guidance/hook), trend-confirmed (a shipped fix already worked — repeat it) vs. trend-stalled (a shipped fix didn't move adoption — escalate it, don't pile on a new one). Mislabeling a low-leverage 1:1 swap as a priority is the failure mode this skill exists to prevent.
