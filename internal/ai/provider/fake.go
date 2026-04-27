package provider

import "context"

// Fake is a test double for Provider.
//
// Tests build one with canned responses, stub errors, and optional
// hooks to assert what inputs the adapter received. Calls is appended
// to on every method so tests can inspect invocation order.
type Fake struct {
	NameVal     string
	LocalityVal Locality

	AvailableErr      error
	ClassifyResponses []ClassifyResult
	ClassifyErrs      []error
	ComposeResponses  []ComposeResult
	ComposeErrs       []error

	// Summarizer support.
	SummarizeResponses []SummarizeResult
	SummarizeErrs      []error

	// Observation hooks; zero values are ignored.
	OnClassify  func(ClassifyInput)
	OnCompose   func(ComposeInput)
	OnSummarize func(SummarizeInput)

	Calls []string // "Available" | "Classify" | "Compose" | "Summarize"

	classifyIdx  int
	composeIdx   int
	summarizeIdx int
}

// NewFake returns a Fake that claims to be a local provider named
// "fake" and reports Available nil. Overwrite fields as needed.
func NewFake() *Fake {
	return &Fake{NameVal: "fake", LocalityVal: LocalityLocal}
}

func (f *Fake) Name() string       { return f.NameVal }
func (f *Fake) Locality() Locality { return f.LocalityVal }

func (f *Fake) Available(_ context.Context) error {
	f.Calls = append(f.Calls, "Available")
	return f.AvailableErr
}

func (f *Fake) Classify(_ context.Context, in ClassifyInput) (ClassifyResult, error) {
	f.Calls = append(f.Calls, "Classify")
	if f.OnClassify != nil {
		f.OnClassify(in)
	}
	idx := f.classifyIdx
	f.classifyIdx++
	var (
		resp ClassifyResult
		err  error
	)
	if idx < len(f.ClassifyResponses) {
		resp = f.ClassifyResponses[idx]
	}
	if idx < len(f.ClassifyErrs) {
		err = f.ClassifyErrs[idx]
	}
	return resp, err
}

func (f *Fake) Compose(_ context.Context, in ComposeInput) (ComposeResult, error) {
	f.Calls = append(f.Calls, "Compose")
	if f.OnCompose != nil {
		f.OnCompose(in)
	}
	idx := f.composeIdx
	f.composeIdx++
	var (
		resp ComposeResult
		err  error
	)
	if idx < len(f.ComposeResponses) {
		resp = f.ComposeResponses[idx]
	}
	if idx < len(f.ComposeErrs) {
		err = f.ComposeErrs[idx]
	}
	return resp, err
}

func (f *Fake) Summarize(_ context.Context, in SummarizeInput) (SummarizeResult, error) {
	f.Calls = append(f.Calls, "Summarize")
	if f.OnSummarize != nil {
		f.OnSummarize(in)
	}
	idx := f.summarizeIdx
	f.summarizeIdx++
	var (
		resp SummarizeResult
		err  error
	)
	if idx < len(f.SummarizeResponses) {
		resp = f.SummarizeResponses[idx]
	}
	if idx < len(f.SummarizeErrs) {
		err = f.SummarizeErrs[idx]
	}
	if resp.Provider == "" {
		resp.Provider = f.Name()
	}
	return resp, err
}

var _ Provider = (*Fake)(nil)
var _ Summarizer = (*Fake)(nil)
