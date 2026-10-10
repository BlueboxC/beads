package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	graph "github.com/steveyegge/beads/graphops"
)

func TestGraphPreviewCreationDefaults(t *testing.T) {
	seen := map[string]bool{}
	for range 32 {
		path, err := graphPreviewCreateBeadPath(selectedRememberCommand(t, nil))
		if err != nil || graph.ValidateBeadPath(path) != nil || seen[path] {
			t.Fatalf("invalid or reused generated path %q: %v", path, err)
		}
		seen[path] = true
	}
	for _, id := range []string{"beads/explicit", "explicit", "team/explicit", "", "links/wrong", "../wrong"} {
		path, err := graphPreviewCreateBeadPath(selectedRememberCommand(t, []string{"--id=" + id}))
		if id == "beads/explicit" || id == "explicit" || id == "team/explicit" {
			want := id
			if id != "beads/explicit" {
				want = "beads/" + id
			}
			if err != nil || path != want {
				t.Fatalf("explicit ID changed: %q %v", path, err)
			}
		} else if err == nil {
			t.Fatalf("invalid explicit ID %q was replaced with %q", id, path)
		}
	}
	for _, tc := range []struct{ body, title string }{
		{"  Code  flow\tpolicy\r\nKeep the whole body.", "Code flow policy"},
		{"\n\t", ""},
		{strings.Repeat("雪", 80), strings.Repeat("雪", 80)},
		{strings.Repeat("雪", 81), strings.Repeat("雪", 79) + "…"},
	} {
		if got := graphPreviewMemoryTitle(tc.body); got != tc.title || !utf8.ValidString(got) {
			t.Fatalf("title=%q want=%q", got, tc.title)
		}
	}
}

// A URL is never a bare ID. The refusal names the forms that are accepted, and
// says --id takes no URL at all, instead of implying that a Scope URL works
// wherever an ID does.
func TestGraphPreviewURLSelectorRefusalNamesAcceptedForms(t *testing.T) {
	const url = "https://example.invalid/other/beads/policy"
	want := []string{
		`invalid local selector "` + url + `"`,
		"this workspace's Scope URL followed by beads/PATH or links/PATH",
		"--id accepts only a bare ID or beads/PATH",
	}
	if _, err := graphPreviewBareBeadPath(url); err == nil {
		t.Fatal("URL accepted as a bare Bead ID")
	} else {
		for _, fragment := range want {
			if !strings.Contains(err.Error(), fragment) {
				t.Errorf("selector refusal omits %q: %v", fragment, err)
			}
		}
	}

	diagnostic := captureGraphFailures(t)
	if path, err := graphPreviewCreateBeadPath(selectedRememberCommand(t, []string{"--id=" + url})); err == nil {
		t.Fatalf("--id accepted a URL as %q", path)
	}
	for _, fragment := range want {
		if !strings.Contains(diagnostic.String(), fragment) {
			t.Errorf("--id refusal omits %q: %s", fragment, diagnostic.String())
		}
	}
}
