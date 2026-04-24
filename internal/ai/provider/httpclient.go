package provider

import (
	"context"
	"net/http"
	"time"
)

// HTTPClient abstracts HTTP calls so tests can inject fakes.
// This is the HTTP counterpart of CommandRunner — providers that talk
// to REST APIs (e.g. NVIDIA) use HTTPClient instead of shelling out
// to a binary.
type HTTPClient interface {
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
}

// DefaultHTTPClient wraps net/http.Client with configurable timeout.
type DefaultHTTPClient struct {
	Client *http.Client
}

// NewDefaultHTTPClient returns a DefaultHTTPClient with the given timeout.
func NewDefaultHTTPClient(timeout time.Duration) *DefaultHTTPClient {
	return &DefaultHTTPClient{
		Client: &http.Client{Timeout: timeout},
	}
}

// Do implements HTTPClient by delegating to the underlying http.Client.
func (d *DefaultHTTPClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return d.Client.Do(req.WithContext(ctx))
}

// FakeHTTPCall records one invocation made to FakeHTTPClient.
type FakeHTTPCall struct {
	Req *http.Request
}

// FakeHTTPClient is a test double that records calls and returns canned
// responses. Responses and Errs are consumed in order; when exhausted
// the client returns nil response and nil error.
type FakeHTTPClient struct {
	Responses []*http.Response
	Errs      []error
	Calls     []FakeHTTPCall
	idx       int
}

// Do implements HTTPClient. It records the call and returns the next
// canned response/error pair.
func (f *FakeHTTPClient) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	f.Calls = append(f.Calls, FakeHTTPCall{Req: req})

	var (
		resp *http.Response
		err  error
	)
	if f.idx < len(f.Responses) {
		resp = f.Responses[f.idx]
	}
	if f.idx < len(f.Errs) {
		err = f.Errs[f.idx]
	}
	f.idx++
	return resp, err
}

// Compile-time interface checks.
var (
	_ HTTPClient = (*DefaultHTTPClient)(nil)
	_ HTTPClient = (*FakeHTTPClient)(nil)
)
