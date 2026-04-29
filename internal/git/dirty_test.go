package git

import "testing"

func TestParsePorcelainV1(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want DirtyFlags
	}{
		{"empty", "", DirtyFlags{}},
		{"modified only", " M file.txt\n", DirtyFlags{Modified: true}},
		{"staged only", "M  file.txt\n", DirtyFlags{Staged: true}},
		{"both", "MM file.txt\n", DirtyFlags{Modified: true, Staged: true}},
		{"untracked only ignored", "?? newfile\n", DirtyFlags{}},
		{"ignored only ignored", "!! ignored\n", DirtyFlags{}},
		{"conflict UU", "UU conflict.txt\n", DirtyFlags{Conflict: true}},
		{"conflict AA", "AA conflict.txt\n", DirtyFlags{Conflict: true}},
		{"conflict mix overrides", " M m.txt\nUU c.txt\n", DirtyFlags{Modified: true, Conflict: true}},
		{"rename staged", "R  old -> new\n", DirtyFlags{Staged: true}},
		{"deleted unstaged", " D removed.txt\n", DirtyFlags{Modified: true}},
		{"deleted staged", "D  removed.txt\n", DirtyFlags{Staged: true}},
		{"multi-line mix", " M a.txt\nM  b.txt\n?? c.txt\n", DirtyFlags{Modified: true, Staged: true}},
		{"NUL separated modified", " M file.txt\x00", DirtyFlags{Modified: true}},
		{"NUL rename skip target", "R  new\x00old\x00", DirtyFlags{Staged: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParsePorcelainV1([]byte(c.in))
			if got != c.want {
				t.Errorf("ParsePorcelainV1(%q) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

func TestDirtyFlags_Clean(t *testing.T) {
	t.Parallel()
	if !(DirtyFlags{}).Clean() {
		t.Error("zero value should be Clean")
	}
	if (DirtyFlags{Modified: true}).Clean() {
		t.Error("Modified should not be Clean")
	}
	if (DirtyFlags{Staged: true}).Clean() {
		t.Error("Staged should not be Clean")
	}
	if (DirtyFlags{Conflict: true}).Clean() {
		t.Error("Conflict should not be Clean")
	}
}
