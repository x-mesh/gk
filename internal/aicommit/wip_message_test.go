package aicommit

import (
	"regexp"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

func TestMarkAsWIPRewritesHeaderType(t *testing.T) {
	msgs := []Message{{
		Group:   provider.Group{Type: "feat", Scope: "remote", Files: []string{"a.go"}},
		Subject: "add the peer handshake",
		Body:    "- dial first\n- then handshake",
	}}

	got := MarkAsWIP(msgs)
	if len(got) != 1 {
		t.Fatalf("MarkAsWIP returned %d messages, want 1", len(got))
	}
	if want := "WIP(remote): add the peer handshake"; got[0].Header() != want {
		t.Errorf("Header() = %q, want %q", got[0].Header(), want)
	}
	if got[0].Body != msgs[0].Body {
		t.Errorf("body was modified: %q", got[0].Body)
	}
}

// The model frequently echoes the full Conventional prefix into the subject.
// Header() only strips a prefix matching the CURRENT type, so if MarkAsWIP
// swapped the type without stripping first the old prefix would survive as
// "WIP(remote): feat(remote): …".
func TestMarkAsWIPStripsEchoedConventionalPrefix(t *testing.T) {
	msgs := []Message{{
		Group:   provider.Group{Type: "fix", Scope: "peer", Files: []string{"a.go"}},
		Subject: "fix(peer): stop dropping the pwd",
	}}

	got := MarkAsWIP(msgs)[0]
	if want := "WIP(peer): stop dropping the pwd"; got.Header() != want {
		t.Errorf("Header() = %q, want %q", got.Header(), want)
	}
}

func TestMarkAsWIPClearsBreaking(t *testing.T) {
	msgs := []Message{{
		Group:    provider.Group{Type: "feat", Scope: "api", Files: []string{"a.go"}},
		Subject:  "drop the v1 route",
		Breaking: true,
	}}

	got := MarkAsWIP(msgs)[0]
	if got.Breaking {
		t.Error("Breaking survived MarkAsWIP; a checkpoint must not claim a breaking change")
	}
	if strings.Contains(got.Header(), "!") {
		t.Errorf("Header() = %q, want no breaking marker", got.Header())
	}
}

// The whole point of the WIP header spelling is that a later `gk commit`
// recognises it. Guard the contract against either side drifting.
func TestMarkAsWIPHeaderMatchesWIPChainPattern(t *testing.T) {
	res, err := CompileWIPPatterns(nil)
	if err != nil {
		t.Fatalf("CompileWIPPatterns: %v", err)
	}
	header := MarkAsWIP([]Message{{
		Group:   provider.Group{Type: "chore", Scope: "remote", Files: []string{"a.go"}},
		Subject: "save the session",
	}})[0].Header()

	if !matchesAny(res, header) {
		t.Errorf("header %q matches no WIP pattern — `gk commit` would not unwrap it", header)
	}
}

func TestFallbackWIPMessagePluralisesAndMatchesPattern(t *testing.T) {
	res, err := CompileWIPPatterns(nil)
	if err != nil {
		t.Fatalf("CompileWIPPatterns: %v", err)
	}

	one := FallbackWIPMessage([]FileChange{{Path: "internal/a.go"}}, "internal")
	if !strings.Contains(one.Subject, "1 file (") {
		t.Errorf("singular subject = %q, want '1 file'", one.Subject)
	}
	if !matchesAny(res, one.Header()) {
		t.Errorf("fallback header %q matches no WIP pattern", one.Header())
	}

	two := FallbackWIPMessage([]FileChange{{Path: "a.go"}, {Path: "b.go"}}, "")
	if !strings.Contains(two.Subject, "2 files") {
		t.Errorf("plural subject = %q, want '2 files'", two.Subject)
	}
	// No scope → no "(scope)" segment before the colon.
	if !strings.HasPrefix(two.Header(), "WIP: ") {
		t.Errorf("scopeless header = %q, want it to start with %q", two.Header(), "WIP: ")
	}
	if len(two.Group.Files) != 2 {
		t.Errorf("fallback covers %d files, want 2", len(two.Group.Files))
	}
}

func matchesAny(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}
