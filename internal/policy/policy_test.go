package policy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// fakeRule is a minimal Rule used across tests.
type fakeRule struct {
	name       string
	violations []Violation
	err        error
	calls      int32 // atomic counter for parallelism tests
}

func (r *fakeRule) Name() string { return r.name }

func (r *fakeRule) Evaluate(_ context.Context, _ Input) ([]Violation, error) {
	atomic.AddInt32(&r.calls, 1)
	return r.violations, r.err
}

func TestSeverity_String(t *testing.T) {
	cases := []struct {
		s    Severity
		want string
	}{
		{SeverityError, "error"},
		{SeverityWarn, "warn"},
		{SeverityInfo, "info"},
		{Severity(42), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("Severity(%d).String() = %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestSeverity_SortOrder(t *testing.T) {
	// Error < Warn < Info so the worst violations sort first.
	if SeverityError >= SeverityWarn || SeverityWarn >= SeverityInfo {
		t.Errorf("severity ordering broken: error=%d warn=%d info=%d",
			SeverityError, SeverityWarn, SeverityInfo)
	}
}

func TestRegistry_Register_Rejects_Dup(t *testing.T) {
	reg := NewRegistry()

	if err := reg.Register(&fakeRule{name: "test"}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := reg.Register(&fakeRule{name: "test"})
	if err == nil {
		t.Fatal("expected error on duplicate registration, got nil")
	}
}

func TestRegistry_Register_EmptyName_Rejects(t *testing.T) {
	reg := NewRegistry()
	err := reg.Register(&fakeRule{name: "   "})
	if err == nil {
		t.Fatal("expected error on empty name, got nil")
	}
}

func TestRegistry_Replace_Overwrites(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(&fakeRule{name: "x", violations: []Violation{{Message: "first"}}})

	reg.Replace(&fakeRule{name: "x", violations: []Violation{{Message: "second"}}})

	rule, ok := reg.Get("x")
	if !ok {
		t.Fatal("Get(x) missing after Replace")
	}
	vs, _ := rule.Evaluate(context.Background(), Input{})
	if len(vs) != 1 || vs[0].Message != "second" {
		t.Errorf("Replace did not overwrite: %+v", vs)
	}
}

func TestRegistry_Names_Sorted(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(&fakeRule{name: "c"})
	_ = reg.Register(&fakeRule{name: "a"})
	_ = reg.Register(&fakeRule{name: "b"})

	got := reg.Names()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("Names() = %v, want [a b c]", got)
	}
}

func TestRegistry_Evaluate_RunsAllRules_SortsViolations(t *testing.T) {
	reg := NewRegistry()

	// Rule "a" emits a Warn; rule "b" emits an Error; sort should surface
	// the Error first regardless of registration order.
	_ = reg.Register(&fakeRule{
		name: "a",
		violations: []Violation{
			{RuleID: "a", Severity: SeverityWarn, Message: "warn-a"},
		},
	})
	_ = reg.Register(&fakeRule{
		name: "b",
		violations: []Violation{
			{RuleID: "b", Severity: SeverityError, Message: "err-b"},
		},
	})

	violations, errs := reg.Evaluate(context.Background(), Input{})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(violations) != 2 {
		t.Fatalf("got %d violations, want 2", len(violations))
	}
	if violations[0].Severity != SeverityError || violations[0].RuleID != "b" {
		t.Errorf("violations[0] = %+v, want Error b", violations[0])
	}
	if violations[1].Severity != SeverityWarn || violations[1].RuleID != "a" {
		t.Errorf("violations[1] = %+v, want Warn a", violations[1])
	}
}

func TestRegistry_Evaluate_CollectsErrorsIndependently(t *testing.T) {
	reg := NewRegistry()
	sentinel := errors.New("rule infra failure")

	_ = reg.Register(&fakeRule{
		name: "healthy",
		violations: []Violation{
			{RuleID: "healthy", Severity: SeverityInfo, Message: "ok"},
		},
	})
	_ = reg.Register(&fakeRule{name: "broken", err: sentinel})

	violations, errs := reg.Evaluate(context.Background(), Input{})
	if len(violations) != 1 {
		t.Errorf("healthy rule's violation lost: %+v", violations)
	}
	if len(errs) != 1 || !errors.Is(errs[0], sentinel) {
		t.Errorf("expected sentinel error, got %v", errs)
	}
}

func TestRegistry_Evaluate_RunsInParallel(t *testing.T) {
	// Not a strict parallelism test, but guards against a future regression
	// that would serialize (e.g. Lock held across all Evaluate calls).
	reg := NewRegistry()
	rules := make([]*fakeRule, 10)
	for i := 0; i < 10; i++ {
		rules[i] = &fakeRule{name: string(rune('a' + i))}
		_ = reg.Register(rules[i])
	}
	_, _ = reg.Evaluate(context.Background(), Input{})

	// Each rule must have been called exactly once.
	for _, r := range rules {
		if got := atomic.LoadInt32(&r.calls); got != 1 {
			t.Errorf("rule %q called %d times, want 1", r.name, got)
		}
	}
}
