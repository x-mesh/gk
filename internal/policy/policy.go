// Package policy is the gk guard policy engine.
//
// Rules implement the Rule interface and register themselves via the package
// Registry. The engine evaluates every registered rule in parallel against
// an Input (the repo + context being checked) and aggregates Violations.
//
// Design notes:
//   - Rules are stateless. Per-call state lives on Input / Violation.
//   - Severity is a stable integer — lower numeric value means more severe
//     so Violations sort naturally (Error < Warn < Info).
//   - `gk guard check` (GD-08) is the CLI driver that builds Input, runs
//     the Registry, and exits with a code derived from the worst Severity.
package policy

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/x-mesh/gk/internal/git"
)

// Severity ranks a Violation. Lower values mean more severe so sorting a
// slice of Violations puts the most important items first.
type Severity int

const (
	SeverityError Severity = iota // 0 — blocks the check
	SeverityWarn                  // 1 — surfaced, does not block
	SeverityInfo                  // 2 — informational only
)

// String returns the stable lowercase name used in JSON output and logs.
func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarn:
		return "warn"
	case SeverityInfo:
		return "info"
	default:
		return "unknown"
	}
}

// Input is the context passed to every Rule's Evaluate method. Rules pick
// only the fields they need.
type Input struct {
	// Runner is a git runner scoped to the target repo. Rules that need
	// git data (commit lists, diff ranges, log output) use this.
	Runner git.Runner
	// WorkDir is the repo root; rules that spawn non-git subprocesses
	// (scanners, linters) pass this as cwd.
	WorkDir string
	// Staged restricts evaluation to staged changes only. Pre-commit hook
	// sets this true; pre-push sets false to scan the push range.
	Staged bool
	// PushRange, when non-empty, is the `<base>..<head>` ref range pushed
	// (pre-push hook scenario). Rules that care about history scoping read
	// this; otherwise they fall back to default behavior.
	PushRange string
}

// Violation describes a single policy breach. Severity is assigned by the
// rule; the engine does not override it.
type Violation struct {
	RuleID   string   // stable identifier matching Rule.Name()
	Severity Severity // rule-assigned severity
	File     string   // empty when the violation is not file-local
	Line     int      // 0 when N/A
	Message  string   // human-readable one-liner
	Hint     string   // optional fix suggestion
}

// Rule is the contract every policy implementation satisfies.
type Rule interface {
	// Name returns a stable identifier used as RuleID in Violations and
	// surfaced in config (.gk.yaml `policies.<name>:`).
	Name() string
	// Evaluate runs the rule against Input and returns zero or more
	// Violations. An error is returned only for infrastructure failures
	// (git command failed, filesystem error); policy breaches are
	// expressed as Violations, not errors.
	Evaluate(ctx context.Context, in Input) ([]Violation, error)
}

// Registry holds rule instances. A global Default registry exists for
// convenience; callers that need isolated rule sets (tests, scoped
// evaluations) can construct a fresh Registry via NewRegistry.
type Registry struct {
	mu    sync.RWMutex
	rules map[string]Rule
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{rules: make(map[string]Rule)}
}

// Register adds a Rule. Re-registering a Rule with an existing Name returns
// an error so callers catch accidental duplication. Rules written in tests
// can bypass this by calling Replace.
func (r *Registry) Register(rule Rule) error {
	if rule == nil {
		return fmt.Errorf("policy: Register(nil)")
	}
	name := strings.TrimSpace(rule.Name())
	if name == "" {
		return fmt.Errorf("policy: rule has empty Name()")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.rules[name]; exists {
		return fmt.Errorf("policy: rule %q already registered", name)
	}
	r.rules[name] = rule
	return nil
}

// Replace installs a Rule under its Name, overwriting any existing entry.
// Useful for tests that stub out a built-in rule.
func (r *Registry) Replace(rule Rule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rules[rule.Name()] = rule
}

// Get returns the Rule registered under name, or (nil, false) if absent.
func (r *Registry) Get(name string) (Rule, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rule, ok := r.rules[name]
	return rule, ok
}

// Names returns every registered rule name sorted alphabetically.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.rules))
	for n := range r.rules {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Evaluate runs every registered rule in parallel and returns all
// Violations merged. Rule errors are collected separately — one failing
// rule does not abort the others. The Violations slice is sorted by
// (Severity asc, RuleID asc, File asc, Line asc) so the worst items come
// first and output is deterministic.
func (r *Registry) Evaluate(ctx context.Context, in Input) ([]Violation, []error) {
	r.mu.RLock()
	rules := make([]Rule, 0, len(r.rules))
	for _, rule := range r.rules {
		rules = append(rules, rule)
	}
	r.mu.RUnlock()

	type result struct {
		violations []Violation
		err        error
	}
	results := make(chan result, len(rules))
	var wg sync.WaitGroup
	for _, rule := range rules {
		wg.Add(1)
		go func(rule Rule) {
			defer wg.Done()
			v, err := rule.Evaluate(ctx, in)
			results <- result{violations: v, err: err}
		}(rule)
	}
	wg.Wait()
	close(results)

	var (
		allV []Violation
		errs []error
	)
	for r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
		}
		allV = append(allV, r.violations...)
	}

	sort.SliceStable(allV, func(i, j int) bool {
		a, b := allV[i], allV[j]
		if a.Severity != b.Severity {
			return a.Severity < b.Severity
		}
		if a.RuleID != b.RuleID {
			return a.RuleID < b.RuleID
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})

	return allV, errs
}

// Default is a process-wide Registry. Built-in rules (max_commit_size,
// required_trailers, …) register themselves here via init(). Callers that
// need isolation (tests, subcommands with limited rule sets) should build
// a fresh Registry with NewRegistry and Register rules explicitly.
var Default = NewRegistry()
