package provider

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

// ── Task 7.4: Unit tests ─────────────────────────────────────────────

func TestFallbackChain_ClassifyFirstSuccess(t *testing.T) {
	p1 := &Fake{
		NameVal:           "first",
		ClassifyResponses: []ClassifyResult{{Model: "m1"}},
	}
	p2 := &Fake{
		NameVal:           "second",
		ClassifyResponses: []ClassifyResult{{Model: "m2"}},
	}
	fc := &FallbackChain{Providers: []Provider{p1, p2}}

	res, err := fc.Classify(context.Background(), ClassifyInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "m1" {
		t.Errorf("Model = %q, want %q", res.Model, "m1")
	}
	// p2 should not have been called.
	for _, c := range p2.Calls {
		if c == "Classify" {
			t.Error("second provider should not have been called")
		}
	}
}

func TestFallbackChain_ClassifyMiddleFailThenSuccess(t *testing.T) {
	p1 := &Fake{
		NameVal:      "first",
		ClassifyErrs: []error{errors.New("p1 down")},
	}
	p2 := &Fake{
		NameVal:           "second",
		ClassifyResponses: []ClassifyResult{{Model: "m2"}},
	}

	var logs []string
	fc := &FallbackChain{
		Providers: []Provider{p1, p2},
		Dbg:       func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
	}

	res, err := fc.Classify(context.Background(), ClassifyInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "m2" {
		t.Errorf("Model = %q, want %q", res.Model, "m2")
	}
	if len(logs) == 0 {
		t.Error("expected debug log for first provider failure")
	}
}

func TestFallbackChain_ClassifyAllFail(t *testing.T) {
	p1 := &Fake{
		NameVal:      "first",
		ClassifyErrs: []error{errors.New("p1 down")},
	}
	p2 := &Fake{
		NameVal:      "second",
		ClassifyErrs: []error{errors.New("p2 down")},
	}
	fc := &FallbackChain{Providers: []Provider{p1, p2}}

	_, err := fc.Classify(context.Background(), ClassifyInput{})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if err.Error() != "p2 down" {
		t.Errorf("error = %q, want last provider error %q", err.Error(), "p2 down")
	}
}

func TestFallbackChain_ComposeFirstSuccess(t *testing.T) {
	p1 := &Fake{
		NameVal:          "first",
		ComposeResponses: []ComposeResult{{Subject: "s1"}},
	}
	fc := &FallbackChain{Providers: []Provider{p1}}

	res, err := fc.Compose(context.Background(), ComposeInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Subject != "s1" {
		t.Errorf("Subject = %q, want %q", res.Subject, "s1")
	}
}

func TestFallbackChain_ComposeAllFail(t *testing.T) {
	p1 := &Fake{NameVal: "a", ComposeErrs: []error{errors.New("a fail")}}
	p2 := &Fake{NameVal: "b", ComposeErrs: []error{errors.New("b fail")}}
	fc := &FallbackChain{Providers: []Provider{p1, p2}}

	_, err := fc.Compose(context.Background(), ComposeInput{})
	if err == nil || err.Error() != "b fail" {
		t.Errorf("error = %v, want 'b fail'", err)
	}
}

func TestFallbackChain_SummarizeSuccess(t *testing.T) {
	p1 := &Fake{
		NameVal:            "first",
		SummarizeResponses: []SummarizeResult{{Text: "summary"}},
	}
	fc := &FallbackChain{Providers: []Provider{p1}}

	res, err := fc.Summarize(context.Background(), SummarizeInput{Kind: "pr"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text != "summary" {
		t.Errorf("Text = %q, want %q", res.Text, "summary")
	}
}

func TestFallbackChain_SummarizeSkipsNonSummarizer(t *testing.T) {
	// nonSummarizer is a Provider that does NOT implement Summarizer.
	nonSum := &nonSummarizerFake{name: "nosumm"}
	p2 := &Fake{
		NameVal:            "second",
		SummarizeResponses: []SummarizeResult{{Text: "ok"}},
	}
	fc := &FallbackChain{Providers: []Provider{nonSum, p2}}

	res, err := fc.Summarize(context.Background(), SummarizeInput{Kind: "pr"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text != "ok" {
		t.Errorf("Text = %q, want %q", res.Text, "ok")
	}
}

func TestFallbackChain_SummarizeNoSummarizer(t *testing.T) {
	nonSum := &nonSummarizerFake{name: "nosumm"}
	fc := &FallbackChain{Providers: []Provider{nonSum}}

	_, err := fc.Summarize(context.Background(), SummarizeInput{Kind: "pr"})
	if err == nil {
		t.Fatal("expected error when no provider implements Summarizer")
	}
}

func TestFallbackChain_Name(t *testing.T) {
	fc := &FallbackChain{Providers: []Provider{
		&Fake{NameVal: "nvidia"},
	}}
	if fc.Name() != "nvidia" {
		t.Errorf("Name() = %q, want %q", fc.Name(), "nvidia")
	}

	empty := &FallbackChain{}
	if empty.Name() != "fallback" {
		t.Errorf("empty Name() = %q, want %q", empty.Name(), "fallback")
	}
}

func TestFallbackChain_Available(t *testing.T) {
	p1 := &Fake{NameVal: "a", AvailableErr: errors.New("no")}
	p2 := &Fake{NameVal: "b"}
	fc := &FallbackChain{Providers: []Provider{p1, p2}}

	if err := fc.Available(context.Background()); err != nil {
		t.Errorf("Available() = %v, want nil (second provider is available)", err)
	}
}

func TestFallbackChain_NilDbg(t *testing.T) {
	// Ensure nil Dbg doesn't panic.
	p1 := &Fake{NameVal: "a", ClassifyErrs: []error{errors.New("fail")}}
	p2 := &Fake{NameVal: "b", ClassifyResponses: []ClassifyResult{{Model: "ok"}}}
	fc := &FallbackChain{Providers: []Provider{p1, p2}}

	_, err := fc.Classify(context.Background(), ClassifyInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// nonSummarizerFake implements Provider but NOT Summarizer.
type nonSummarizerFake struct {
	name string
}

func (n *nonSummarizerFake) Name() string                      { return n.name }
func (n *nonSummarizerFake) Locality() Locality                { return LocalityLocal }
func (n *nonSummarizerFake) Available(_ context.Context) error { return nil }
func (n *nonSummarizerFake) Classify(_ context.Context, _ ClassifyInput) (ClassifyResult, error) {
	return ClassifyResult{}, errors.New("not implemented")
}
func (n *nonSummarizerFake) Compose(_ context.Context, _ ComposeInput) (ComposeResult, error) {
	return ComposeResult{}, errors.New("not implemented")
}

// ── Task 7.5: [PBT] Property 7 — Fallback Chain 순서 보장 및 단일 시도 ──
// Feature: nvidia-ai-provider, Property 7: Fallback Chain 순서 보장 및 단일 시도
// **Validates: Requirements 10.1, 10.2**

func TestProperty7_FallbackOrdering(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// N: chain 길이 (2~6), K: 처음 실패할 provider 수 (0 ≤ K < N).
		n := rapid.IntRange(2, 6).Draw(t, "chainLen")
		k := rapid.IntRange(0, n-1).Draw(t, "failCount")

		// 각 provider의 호출 여부를 추적하기 위한 슬라이스.
		called := make([]int, n) // 호출 횟수

		providers := make([]Provider, n)
		for i := 0; i < n; i++ {
			idx := i // capture
			f := &Fake{NameVal: fmt.Sprintf("p%d", i)}
			if i < k {
				f.ClassifyErrs = []error{fmt.Errorf("p%d error", i)}
			} else {
				f.ClassifyResponses = []ClassifyResult{{Model: fmt.Sprintf("model-%d", i)}}
			}
			f.OnClassify = func(_ ClassifyInput) { called[idx]++ }
			providers[i] = f
		}

		fc := &FallbackChain{Providers: providers}
		res, err := fc.Classify(context.Background(), ClassifyInput{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// K+1번째 provider의 결과가 반환되어야 한다.
		wantModel := fmt.Sprintf("model-%d", k)
		if res.Model != wantModel {
			t.Errorf("Model = %q, want %q", res.Model, wantModel)
		}

		// 처음 K개 + 성공한 1개 = K+1개만 호출되어야 한다.
		for i := 0; i <= k; i++ {
			if called[i] != 1 {
				t.Errorf("provider %d called %d times, want 1", i, called[i])
			}
		}
		// K+1 이후의 provider는 호출되지 않아야 한다.
		for i := k + 1; i < n; i++ {
			if called[i] != 0 {
				t.Errorf("provider %d called %d times, want 0", i, called[i])
			}
		}
	})
}

// ── Task 7.6: [PBT] Property 8 — Fallback Chain 전체 실패 시 마지막 에러 반환 ──
// Feature: nvidia-ai-provider, Property 8: Fallback Chain 전체 실패 시 마지막 에러 반환
// **Validates: Requirements 10.3**

func TestProperty8_FallbackAllFailLastError(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 6).Draw(t, "chainLen")

		providers := make([]Provider, n)
		var lastErrMsg string
		for i := 0; i < n; i++ {
			msg := fmt.Sprintf("error-from-p%d-%s", i,
				rapid.StringMatching(`[a-z]{3,8}`).Draw(t, fmt.Sprintf("errSuffix_%d", i)))
			f := &Fake{
				NameVal:      fmt.Sprintf("p%d", i),
				ClassifyErrs: []error{errors.New(msg)},
			}
			providers[i] = f
			lastErrMsg = msg
		}

		fc := &FallbackChain{Providers: providers}
		_, err := fc.Classify(context.Background(), ClassifyInput{})
		if err == nil {
			t.Fatal("expected error when all providers fail")
		}

		// 반환된 에러는 마지막 provider의 에러를 포함해야 한다.
		if err.Error() != lastErrMsg {
			t.Errorf("error = %q, want last provider error %q", err.Error(), lastErrMsg)
		}
	})
}
