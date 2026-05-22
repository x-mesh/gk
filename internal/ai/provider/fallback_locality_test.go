package provider

import "testing"

// Locality must be conservative: a chain is remote if ANY provider is
// remote (or it is empty), so the privacy gate never skips redaction on a
// chain that could fail over to a remote provider.
func TestFallbackChainLocality_RemoteIfAny(t *testing.T) {
	local := &Fake{NameVal: "l", LocalityVal: LocalityLocal}
	remote := &Fake{NameVal: "r", LocalityVal: LocalityRemote}

	if got := (&FallbackChain{Providers: []Provider{local, remote}}).Locality(); got != LocalityRemote {
		t.Errorf("mixed chain locality = %v, want %v", got, LocalityRemote)
	}
	if got := (&FallbackChain{Providers: []Provider{remote, local}}).Locality(); got != LocalityRemote {
		t.Errorf("remote-first chain locality = %v, want %v", got, LocalityRemote)
	}
	if got := (&FallbackChain{Providers: []Provider{local, local}}).Locality(); got != LocalityLocal {
		t.Errorf("all-local chain locality = %v, want %v", got, LocalityLocal)
	}
	if got := (&FallbackChain{}).Locality(); got != LocalityRemote {
		t.Errorf("empty chain locality = %v, want %v", got, LocalityRemote)
	}
}
