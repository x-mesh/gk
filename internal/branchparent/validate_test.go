package branchparent

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

func TestValidateSet_Empty(t *testing.T) {
	c := git.NewClient(&git.FakeRunner{})
	if err := ValidateSet(context.Background(), c, "feat/x", ""); err == nil {
		t.Fatal("empty parent must be rejected")
	}
}

func TestValidateSet_SelfParent(t *testing.T) {
	c := git.NewClient(&git.FakeRunner{})
	err := ValidateSet(context.Background(), c, "feat/x", "feat/x")
	if err == nil || !strings.Contains(err.Error(), "own parent") {
		t.Fatalf("self-parent must be rejected, got: %v", err)
	}
}

func TestValidateSet_RemoteLikeRejected(t *testing.T) {
	c := git.NewClient(&git.FakeRunner{})
	err := ValidateSet(context.Background(), c, "feat/x", "origin/main")
	if err == nil || !strings.Contains(err.Error(), "remote-tracking") {
		t.Fatalf("origin/main must be rejected, got: %v", err)
	}
}

func TestValidateSet_MalformedRef(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"check-ref-format --branch ..bad": {ExitCode: 1, Stderr: "not a valid ref"},
		},
	}
	c := git.NewClient(r)
	err := ValidateSet(context.Background(), c, "feat/x", "..bad")
	if err == nil || !strings.Contains(err.Error(), "invalid parent name") {
		t.Fatalf("malformed ref must be rejected, got: %v", err)
	}
}

func TestValidateSet_NonExistentBranchSuggestion(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"check-ref-format --branch mian":              {},
			"rev-parse --verify --quiet refs/heads/mian":  {ExitCode: 1},
			"rev-parse --verify --quiet refs/tags/mian":   {ExitCode: 1},
			"for-each-ref --format=%(refname:short) refs/heads": {
				Stdout: "main\nfeat/x\nfeat/parent\n",
			},
		},
	}
	c := git.NewClient(r)
	err := ValidateSet(context.Background(), c, "feat/x", "mian")
	if err == nil {
		t.Fatal("non-existent branch must be rejected")
	}
	if !strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("expected fuzzy suggestion, got: %v", err)
	}
	if !strings.Contains(err.Error(), `"main"`) {
		t.Fatalf("expected suggestion to be 'main', got: %v", err)
	}
}

func TestValidateSet_TagRejected(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"check-ref-format --branch v1.0":              {},
			"rev-parse --verify --quiet refs/heads/v1.0":  {ExitCode: 1},
			"rev-parse --verify --quiet refs/tags/v1.0":   {Stdout: "abc\n"},
		},
	}
	c := git.NewClient(r)
	err := ValidateSet(context.Background(), c, "feat/x", "v1.0")
	if err == nil || !strings.Contains(err.Error(), "is a tag") {
		t.Fatalf("tag must be rejected with tag message, got: %v", err)
	}
}

func TestValidateSet_HappyPath(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"check-ref-format --branch feat/parent":              {},
			"rev-parse --verify --quiet refs/heads/feat/parent":  {Stdout: "abc\n"},
			"config --get branch.feat/parent.gk-parent":          {ExitCode: 1},
		},
	}
	c := git.NewClient(r)
	err := ValidateSet(context.Background(), c, "feat/x", "feat/parent")
	if err != nil {
		t.Fatalf("happy path must succeed, got: %v", err)
	}
}

func TestDetectCycle_Direct(t *testing.T) {
	// branch=feat/x, parent=feat/y, but branch.feat/y.gk-parent already = feat/x
	// → cycle.
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/y.gk-parent": {Stdout: "feat/x\n"},
		},
	}
	c := git.NewClient(r)
	got := detectCycle(context.Background(), c, "feat/x", "feat/y")
	if got == "" {
		t.Fatal("must detect direct cycle")
	}
}

func TestDetectCycle_Indirect(t *testing.T) {
	// feat/x → feat/y → feat/z → feat/x (3-deep)
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/y.gk-parent": {Stdout: "feat/z\n"},
			"config --get branch.feat/z.gk-parent": {Stdout: "feat/x\n"},
		},
	}
	c := git.NewClient(r)
	got := detectCycle(context.Background(), c, "feat/x", "feat/y")
	if got == "" {
		t.Fatal("must detect indirect cycle")
	}
}

func TestDetectCycle_NoCycle(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get branch.feat/y.gk-parent": {Stdout: "main\n"},
			"config --get branch.main.gk-parent":   {ExitCode: 1},
		},
	}
	c := git.NewClient(r)
	got := detectCycle(context.Background(), c, "feat/x", "feat/y")
	if got != "" {
		t.Errorf("must not detect cycle on linear chain, got: %s", got)
	}
}

func TestDetectCycle_DepthCap(t *testing.T) {
	// Chain longer than maxParentDepth without revisiting — still flagged.
	resp := map[string]git.FakeResponse{}
	for i := 0; i < maxParentDepth+5; i++ {
		key := "config --get branch.b" + intToStr(i) + ".gk-parent"
		resp[key] = git.FakeResponse{Stdout: "b" + intToStr(i+1) + "\n"}
	}
	r := &git.FakeRunner{Responses: resp}
	c := git.NewClient(r)
	got := detectCycle(context.Background(), c, "root", "b0")
	if got == "" {
		t.Fatal("must reject chains exceeding maxParentDepth")
	}
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	var s string
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"main", "main", 0},
		{"mian", "main", 2},
		{"main", "develop", 7},
		{"feat/x", "feat/y", 1},
		{"abc", "", 3},
	}
	for _, tc := range cases {
		got := levenshtein(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("levenshtein(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSuggestSimilar_NoMatch(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"for-each-ref --format=%(refname:short) refs/heads": {
				Stdout: "completely-different\n",
			},
		},
	}
	c := git.NewClient(r)
	got := suggestSimilarBranch(context.Background(), c, "main")
	if got != "" {
		t.Errorf("must not suggest distant matches, got %q", got)
	}
}

func TestIsRemoteLike(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"origin/main", true},
		{"upstream/develop", true},
		{"fork/feature", true},
		{"feat/x", false},
		{"main", false},
		{"my-org/main", false}, // unknown prefix → not flagged
	}
	for _, tc := range cases {
		if got := isRemoteLike(tc.in); got != tc.want {
			t.Errorf("isRemoteLike(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
