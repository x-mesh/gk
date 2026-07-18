package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

func TestPRCheckoutWith(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"fetch origin pull/98/head:pr/98": {Stdout: ""},
		"switch pr/98":                    {Stdout: ""},
	}}
	msg, err := prCheckoutWith(context.Background(), runner, "origin", "pr/98", 98)
	if err != nil {
		t.Fatalf("prCheckoutWith: %v", err)
	}
	if !strings.Contains(msg, "PR #98") || !strings.Contains(msg, "pr/98") {
		t.Errorf("unexpected message: %q", msg)
	}
	joined := ""
	for _, c := range runner.Calls {
		joined += strings.Join(c.Args, " ") + "\n"
	}
	for _, want := range []string{"fetch origin pull/98/head:pr/98", "switch pr/98"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing call %q in:\n%s", want, joined)
		}
	}
}
