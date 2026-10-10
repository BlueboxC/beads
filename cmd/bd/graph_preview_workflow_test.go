package main

import "testing"

func TestGraphPreviewSelectorCannotChangeAuthority(t *testing.T) {
	const scope = "https://example.invalid/demo/"
	for _, tc := range []struct{ selector, path string }{
		{"work", "beads/work"},
		{"team/work", "beads/team/work"},
		{"beads/work", "beads/work"},
		{"links/edge", "links/edge"},
		{scope + "beads/work", "beads/work"},
		{scope + "links/edge", "links/edge"},
		{"https://other.invalid/demo/beads/work", ""},
		{"https://example.invalid/demo-other/beads/work", ""},
		{scope + "../beads/work", ""},
		{scope + "beads/work?version=1", ""},
		{scope + "alias/work", ""},
		{"demo-abc", "beads/demo-abc"},
		{"alias/work", ""},
		{"types/work", ""},
		{"beads/", ""},
		{"links/", ""},
		{"", ""},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			path, err := graphPreviewResourcePath(scope, tc.selector)
			if path != tc.path || (err == nil) != (tc.path != "") {
				t.Fatalf("selector %q: path=%q err=%v", tc.selector, path, err)
			}
		})
	}
}

func TestGraphPreviewLinkOnlySelector(t *testing.T) {
	const scope = "https://example.invalid/demo/"
	for _, tc := range []struct{ selector, path string }{
		{"edge", "links/edge"},
		{"team/edge", "links/team/edge"},
		{"links/edge", "links/edge"},
		{scope + "links/edge", "links/edge"},
		{"beads/edge", ""},
		{"alias/edge", ""},
		{"types/edge", ""},
		{scope + "beads/edge", ""},
		{"https://foreign.invalid/demo/links/edge", ""},
		{"links/", ""},
		{"", ""},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			path, err := graphPreviewLinkPath(scope, tc.selector)
			if path != tc.path || (err == nil) != (tc.path != "") {
				t.Fatalf("Link selector %q: path=%q err=%v", tc.selector, path, err)
			}
		})
	}
}
