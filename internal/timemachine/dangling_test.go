package timemachine

import (
	"context"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

func TestReadDangling_FiltersToCommitsOnly(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"fsck --lost-found --no-reflogs": {Stdout: `dangling commit aaa111
dangling blob bbb222
dangling tag ccc333
dangling commit ddd444
`},
			"show --no-patch --format=%ct%x00%s aaa111": {Stdout: "1700000000\x00first dangling subject\n"},
			"show --no-patch --format=%ct%x00%s ddd444": {Stdout: "1700000500\x00second dangling subject\n"},
		},
	}

	evs, err := ReadDangling(context.Background(), fake, DanglingOptions{})
	if err != nil {
		t.Fatalf("ReadDangling: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2 (commits only): %+v", len(evs), evs)
	}
	for _, ev := range evs {
		if ev.Kind != KindDangling {
			t.Errorf("ev.Kind = %v, want KindDangling", ev.Kind)
		}
		if ev.OID == "" || ev.OID != ev.Ref {
			t.Errorf("dangling ev should have OID == Ref; got %+v", ev)
		}
		if ev.Subject == "" {
			t.Errorf("missing Subject: %+v", ev)
		}
	}
	// Subjects must match what we seeded.
	if evs[0].Subject != "first dangling subject" && evs[1].Subject != "first dangling subject" {
		t.Errorf("subjects not resolved correctly: %+v", evs)
	}
}

func TestReadDangling_CapTruncates(t *testing.T) {
	// Produce 10 dangling commits in the fsck output.
	lines := ""
	for i := 0; i < 10; i++ {
		sha := string(rune('a'+i)) + "00000"
		lines += "dangling commit " + sha + "\n"
	}
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"fsck --lost-found --no-reflogs": {Stdout: lines},
		},
		DefaultResp: git.FakeResponse{Stdout: "1700000000\x00fallback subject\n"},
	}

	evs, err := ReadDangling(context.Background(), fake, DanglingOptions{Cap: 3})
	if err != nil {
		t.Fatalf("ReadDangling: %v", err)
	}
	if len(evs) != 3 {
		t.Errorf("cap=3: got %d events, want 3", len(evs))
	}
}

func TestReadDangling_SkipsBrokenEntries(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"fsck --lost-found --no-reflogs": {
				Stdout: "dangling commit good\ndangling commit pruned\n",
			},
			"show --no-patch --format=%ct%x00%s good":   {Stdout: "1700000000\x00ok\n"},
			"show --no-patch --format=%ct%x00%s pruned": {ExitCode: 128, Stderr: "bad object"},
		},
	}

	evs, err := ReadDangling(context.Background(), fake, DanglingOptions{})
	if err != nil {
		t.Fatalf("ReadDangling: %v", err)
	}
	if len(evs) != 1 || evs[0].OID != "good" {
		t.Errorf("expected 1 event for 'good'; got %+v", evs)
	}
}

func TestReadDangling_FsckError_Propagates(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"fsck --lost-found --no-reflogs": {ExitCode: 1, Stderr: "not a git repository"},
		},
	}
	_, err := ReadDangling(context.Background(), fake, DanglingOptions{})
	if err == nil {
		t.Fatal("expected error when fsck fails")
	}
}
