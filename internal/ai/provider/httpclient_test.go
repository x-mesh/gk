package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDefaultHTTPClient_Timeout(t *testing.T) {
	c := NewDefaultHTTPClient(42 * time.Second)
	if c.Client.Timeout != 42*time.Second {
		t.Fatalf("want timeout 42s, got %v", c.Client.Timeout)
	}
}

func TestDefaultHTTPClient_ZeroTimeout(t *testing.T) {
	c := NewDefaultHTTPClient(0)
	if c.Client.Timeout != 0 {
		t.Fatalf("want timeout 0, got %v", c.Client.Timeout)
	}
}

func TestFakeHTTPClient_RecordsCalls(t *testing.T) {
	fake := &FakeHTTPClient{
		Responses: []*http.Response{
			{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))},
		},
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com", nil)
	resp, err := fake.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(fake.Calls))
	}
	if fake.Calls[0].Req.URL.String() != "https://example.com" {
		t.Fatalf("want https://example.com, got %s", fake.Calls[0].Req.URL)
	}
}

func TestFakeHTTPClient_ReturnsError(t *testing.T) {
	fake := &FakeHTTPClient{
		Errs: []error{io.ErrUnexpectedEOF},
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com", nil)
	_, err := fake.Do(context.Background(), req)
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("want ErrUnexpectedEOF, got %v", err)
	}
}

func TestFakeHTTPClient_MultipleCalls(t *testing.T) {
	fake := &FakeHTTPClient{
		Responses: []*http.Response{
			{StatusCode: 200, Body: io.NopCloser(strings.NewReader("first"))},
			{StatusCode: 201, Body: io.NopCloser(strings.NewReader("second"))},
		},
	}

	ctx := context.Background()
	req1, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://a.com", nil)
	req2, _ := http.NewRequestWithContext(ctx, http.MethodPut, "https://b.com", nil)

	resp1, _ := fake.Do(ctx, req1)
	resp2, _ := fake.Do(ctx, req2)

	if resp1.StatusCode != 200 {
		t.Fatalf("call 1: want 200, got %d", resp1.StatusCode)
	}
	if resp2.StatusCode != 201 {
		t.Fatalf("call 2: want 201, got %d", resp2.StatusCode)
	}
	if len(fake.Calls) != 2 {
		t.Fatalf("want 2 calls, got %d", len(fake.Calls))
	}
	if fake.Calls[0].Req.Method != http.MethodPost {
		t.Fatalf("call 1: want POST, got %s", fake.Calls[0].Req.Method)
	}
	if fake.Calls[1].Req.Method != http.MethodPut {
		t.Fatalf("call 2: want PUT, got %s", fake.Calls[1].Req.Method)
	}
}

func TestFakeHTTPClient_ExhaustedReturnsNil(t *testing.T) {
	fake := &FakeHTTPClient{} // no responses

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com", nil)
	resp, err := fake.Do(context.Background(), req)
	if resp != nil {
		t.Fatalf("want nil response, got %v", resp)
	}
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("want 1 call recorded, got %d", len(fake.Calls))
	}
}
