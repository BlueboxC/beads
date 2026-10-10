//go:build cgo

package main

import "testing"

func TestGraphPreviewLifecycleLegacyRefusal(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, args := range [][]string{
		{"links", "beads/plan"},
		{"unlink", "links/context", "--unconditional", "--unconditional-source"},
	} {
		work, home := t.TempDir(), t.TempDir()
		before := legacyUpgradeTreeDigest(t, work)
		graphPolicyCLI(t, bd, work, home, nil, "capability_unavailable", append(args, "--json")...)
		if after := legacyUpgradeTreeDigest(t, work); after != before {
			t.Fatalf("legacy refusal changed workspace: %v", args)
		}
	}
}
