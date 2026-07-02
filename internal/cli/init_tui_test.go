package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/initx"
)

func TestRemoteAccountItems_ProfilesSortedWithPreviews(t *testing.T) {
	items := remoteAccountItems(remoteTestCfg(), false, false)

	// personal, work (owner-bearing, sorted) + direct + skip.
	// "legacy" has no owner → excluded.
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d: %+v", len(items), items)
	}
	if items[0].Key != "personal" || items[1].Key != "work" {
		t.Errorf("profiles not sorted: %q, %q", items[0].Key, items[1].Key)
	}
	if items[2].Key != remotePickDirect || items[3].Key != remotePickSkip {
		t.Errorf("direct/skip should trail: %q, %q", items[2].Key, items[3].Key)
	}

	// URL preview shows the pending name slot and honours the profile
	// protocol (personal=ssh default, work=https).
	if !strings.Contains(items[0].Display, "git@github.com:JINWOO-J/<name>.git") {
		t.Errorf("personal preview = %q", items[0].Display)
	}
	if !strings.Contains(items[1].Display, "https://github.com/42tape/<name>.git") {
		t.Errorf("work preview = %q", items[1].Display)
	}

	// Force flag flips every preview protocol.
	items = remoteAccountItems(remoteTestCfg(), false, true)
	if !strings.Contains(items[0].Display, "https://github.com/JINWOO-J/<name>.git") {
		t.Errorf("--https preview = %q", items[0].Display)
	}
}

func TestRemoteAccountItems_NoProfilesSkipFirst(t *testing.T) {
	cfg := config.CloneConfig{DefaultProtocol: "ssh", DefaultHost: "github.com"}
	items := remoteAccountItems(cfg, false, false)
	if len(items) != 2 || items[0].Key != remotePickSkip || items[1].Key != remotePickDirect {
		t.Fatalf("no-profile order should be [skip, direct]: %+v", items)
	}

	// Ownerless-only hosts behave the same as none.
	cfg.Hosts = map[string]config.HostAlias{"gl": {Host: "gitlab.com"}}
	items = remoteAccountItems(cfg, false, false)
	if len(items) != 2 || items[0].Key != remotePickSkip {
		t.Fatalf("ownerless-only order should be [skip, direct]: %+v", items)
	}
}

func TestBuildSummary_RemoteLine(t *testing.T) {
	result := &initx.AnalysisResult{}
	plan := &initx.InitPlan{}

	// nil remote plan → no Remote section.
	if s := buildSummary(result, plan, nil); strings.Contains(s, "Remote:") {
		t.Errorf("unexpected Remote section:\n%s", s)
	}

	// planned add.
	rp := &remotePlan{RemoteName: "origin", URL: "git@github.com:JINWOO-J/x.git", Action: initx.ActionCreate}
	s := buildSummary(result, plan, rp)
	if !strings.Contains(s, "origin → git@github.com:JINWOO-J/x.git (add)") {
		t.Errorf("missing add line:\n%s", s)
	}

	// existing origin.
	rp = &remotePlan{RemoteName: "origin", ExistingURL: "git@github.com:foo/bar.git", Action: initx.ActionSkip}
	s = buildSummary(result, plan, rp)
	if !strings.Contains(s, "origin → git@github.com:foo/bar.git (existing)") {
		t.Errorf("missing existing line:\n%s", s)
	}

	// skipped plan (post-cancel) → no Remote section.
	rp = &remotePlan{RemoteName: "origin", URL: "git@github.com:x/y.git", Action: initx.ActionSkip}
	if s := buildSummary(result, plan, rp); strings.Contains(s, "Remote:") {
		t.Errorf("skipped plan should not render:\n%s", s)
	}
}

func TestSkipRemote(t *testing.T) {
	if skipRemote(nil) != nil {
		t.Error("skipRemote(nil) should stay nil")
	}
	rp := &remotePlan{RemoteName: "origin", URL: "git@github.com:x/y.git", Action: initx.ActionCreate}
	got := skipRemote(rp)
	if got.Action != initx.ActionSkip {
		t.Errorf("action = %v, want skip", got.Action)
	}
	if rp.Action != initx.ActionCreate {
		t.Error("skipRemote must not mutate the original plan")
	}
}

// Regression for the confirm-cancel path: a cancelled TUI must never
// reach `git remote add`. The runner is nil on purpose — any attempt to
// execute would panic and fail the test.
func TestExecuteRemotePlan_CancelledNeverAdds(t *testing.T) {
	ctx := context.Background()
	buf := &bytes.Buffer{}
	rp := &remotePlan{RemoteName: "origin", URL: "git@github.com:x/y.git", Action: initx.ActionCreate}

	// confirmed=false (user answered No) → skipped.
	res := executeRemotePlan(ctx, buf, nil, rp, false, false)
	if res == nil || res.Status != "skipped" {
		t.Fatalf("cancelled result = %+v", res)
	}

	// Action=Skip (skipRemote applied) → skipped even when confirmed.
	res = executeRemotePlan(ctx, buf, nil, skipRemote(rp), true, false)
	if res == nil || res.Status != "skipped" {
		t.Fatalf("skip-action result = %+v", res)
	}

	// Existing origin → reported, never touched.
	existing := &remotePlan{RemoteName: "origin", ExistingURL: "git@github.com:a/b.git", Action: initx.ActionSkip}
	res = executeRemotePlan(ctx, buf, nil, existing, true, false)
	if res == nil || res.Status != "existing" || res.URL != "git@github.com:a/b.git" {
		t.Fatalf("existing result = %+v", res)
	}

	// nil plan → no result at all.
	if res := executeRemotePlan(ctx, buf, nil, nil, true, false); res != nil {
		t.Fatalf("nil plan result = %+v", res)
	}
}
