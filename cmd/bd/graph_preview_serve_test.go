//go:build cgo

package main

import "testing"

// This uses normal embedded initialization and a new installed CLI process.
// Refusal must happen before opening storage or binding a persistent listener.
func TestGraphPreviewServeRefusesEmbedded(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	graphPolicyCLI(t, bd, work, home, nil, "", "init", "--graph-mode", "link", "--scope-url", "https://example.invalid/serve/", "--skip-hooks", "--skip-agents", "--non-interactive", "--json")
	before := legacyUpgradeTreeDigest(t, work)
	graphPolicyCLI(t, bd, work, home, nil, "capability_unavailable", "serve", "--readonly", "--addr", "127.0.0.1:0", "--json")
	if after := legacyUpgradeTreeDigest(t, work); after != before {
		t.Fatal("refused embedded serve changed the workspace")
	}
}
