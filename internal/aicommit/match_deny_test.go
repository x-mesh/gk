package aicommit

import "testing"

func TestMatchDenyBasename(t *testing.T) {
	got := matchDeny("foo/bar/.env", []string{".env"})
	if got != ".env" {
		t.Errorf("got %q", got)
	}
}

func TestMatchDenyGlobBasename(t *testing.T) {
	got := matchDeny("path/to/server.pem", []string{"*.pem"})
	if got != "*.pem" {
		t.Errorf("got %q", got)
	}
}

func TestMatchDenyFullPath(t *testing.T) {
	got := matchDeny(".aws/credentials", []string{".aws/credentials"})
	if got != ".aws/credentials" {
		t.Errorf("got %q", got)
	}
}

// TestMatchDenyNestedPycache is the regression: filepath.Match alone
// can't express "any depth __pycache__/*", so without component scanning
// nested .pyc files would leak through deny_paths and into the LLM
// payload.
func TestMatchDenyNestedPycache(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"__pycache__/foo.pyc", true},
		{"internal/x/__pycache__/foo.pyc", true},
		{"deeply/nested/__pycache__/bar.pyc", true},
		{"src/foo.py", false},
	}
	patterns := []string{"__pycache__/*"}
	for _, tc := range cases {
		got := matchDeny(tc.path, patterns) != ""
		if got != tc.want {
			t.Errorf("matchDeny(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestMatchDenyDirectoryComponentAnywhere(t *testing.T) {
	got := matchDeny("a/b/__pycache__/c.pyc", []string{"__pycache__"})
	if got == "" {
		t.Error("__pycache__ alone should match component anywhere in path")
	}
}

func TestMatchDenyNoMatch(t *testing.T) {
	got := matchDeny("internal/foo.go", []string{"*.pem", ".env"})
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestMatchDenyEmptyPatternIgnored(t *testing.T) {
	got := matchDeny("foo.pem", []string{"", "*.pem"})
	if got != "*.pem" {
		t.Errorf("got %q", got)
	}
}

func TestMatchDenyMultiSegmentNested(t *testing.T) {
	// .aws/credentials should match .aws/credentials wherever it sits,
	// e.g. when a vendored copy lives under tools/.aws/credentials.
	got := matchDeny("tools/.aws/credentials", []string{".aws/credentials"})
	if got == "" {
		t.Error("nested .aws/credentials should match component-wise")
	}
}
