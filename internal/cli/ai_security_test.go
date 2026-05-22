package cli

import (
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/config"
)

// ensureRemoteAllowed must enforce ai.commit.allow_remote across every AI
// entry point: a remote provider is refused when the policy is off, while
// local providers (and a nil provider) always pass.
func TestEnsureRemoteAllowed(t *testing.T) {
	remote := &provider.Fake{NameVal: "r", LocalityVal: provider.LocalityRemote}
	local := &provider.Fake{NameVal: "l", LocalityVal: provider.LocalityLocal}

	deny := config.AIConfig{Commit: config.AICommitConfig{AllowRemote: false}}
	allow := config.AIConfig{Commit: config.AICommitConfig{AllowRemote: true}}

	if err := ensureRemoteAllowed(remote, deny); err == nil {
		t.Error("remote provider with allow_remote=false must be blocked")
	}
	if err := ensureRemoteAllowed(remote, allow); err != nil {
		t.Errorf("remote provider with allow_remote=true must pass: %v", err)
	}
	if err := ensureRemoteAllowed(local, deny); err != nil {
		t.Errorf("local provider must always pass: %v", err)
	}
	if err := ensureRemoteAllowed(nil, deny); err != nil {
		t.Errorf("nil provider must pass: %v", err)
	}
}
