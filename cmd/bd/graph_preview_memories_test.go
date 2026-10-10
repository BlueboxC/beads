package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func TestGraphPreviewMemoryDiscoveryRendering(t *testing.T) {
	result := graphMemoryDiscoveryResult{Projection: "summary", Scope: "https://example.invalid/", Complete: true,
		Items: []graphMemoryDiscoveryItem{{ID: "https://example.invalid/beads/plan", Title: "Plan\n\x1b[31m", Version: "saved'quoted",
			Attribution:   graphstore.Attribution{Actor: "actor\nname", Status: "claimed", RecordedAt: "observed"},
			MatchedFields: []string{"body"}, Excerpt: &graphMemoryDiscoveryExcerpt{Field: "body", Text: "line\nbody", Truncated: true},
			Details: &graphMemoryDiscoveryDetails{OwnedLinkCount: 2}}}}
	human, err := renderGraphMemoryDiscovery(result, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"Plan\n", "\x1b", "actor\nname", "line\nbody"} {
		if strings.Contains(human, unsafe) {
			t.Fatalf("unescaped data in human output: %q", human)
		}
	}
	if !strings.Contains(human, "--version 'saved'\"'\"'quoted'") || !strings.Contains(human, "Owned Links: 2") || !strings.Contains(human, `basis="writer-supplied"`) {
		t.Fatalf("missing exact recall or details: %s", human)
	}
	structured, err := renderGraphMemoryDiscovery(result, true, true)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(structured), &envelope); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"schemaVersion": float64(1), "preview": true, "result": map[string]any{
		"projection": "summary", "scope": "https://example.invalid/", "complete": true, "next": nil,
		"items": []any{map[string]any{
			"id": "https://example.invalid/beads/plan", "type": "", "title": "Plan\n\x1b[31m", "version": "saved'quoted",
			"attribution":   map[string]any{"actor": "actor\nname", "basis": "writer-supplied", "recordedAt": "observed"},
			"matchedFields": []any{"body"}, "excerpt": map[string]any{"field": "body", "text": "line\nbody", "truncated": true},
			"details": map[string]any{"ownedLinkCount": float64(2)},
		}},
	}}
	if !reflect.DeepEqual(envelope, want) {
		t.Fatalf("structured summary changed or disappeared under quiet: %+v", envelope)
	}
	quiet, err := renderGraphMemoryDiscovery(result, false, true)
	if err != nil || quiet != "" {
		t.Fatalf("quiet human output: %q, %v", quiet, err)
	}
	result.Items[0].Attribution = graphstore.Attribution{Status: "unknown"}
	withoutPrincipal, err := renderGraphMemoryDiscovery(result, false, false)
	if err != nil || !strings.Contains(withoutPrincipal, "Attribution: none recorded") {
		t.Fatalf("missing explicit absent-attribution disclosure: %q, %v", withoutPrincipal, err)
	}
	result.Items[0].Attribution = graphstore.Attribution{Status: "claimed"}
	if _, err := renderGraphMemoryDiscovery(result, false, false); !errors.Is(err, graphstore.ErrInvalidStore) {
		t.Fatalf("actorless claimed attribution should fail closed: %v", err)
	}
	result.Items[0].Attribution = graphstore.Attribution{Actor: "agent:writer", Status: "verified"}
	if _, err := renderGraphMemoryDiscovery(result, false, false); !errors.Is(err, graphstore.ErrInvalidStore) {
		t.Fatalf("out-of-vocabulary human attribution should fail closed: %v", err)
	}
}

func TestGraphPreviewMemoryDiscoveryRenderedByteLimit(t *testing.T) {
	result := graphMemoryDiscoveryResult{Items: []graphMemoryDiscoveryItem{{Title: strings.Repeat("\x00", graphMemoryDiscoveryOutputLimit/4)}}}
	for _, structured := range []bool{false, true} {
		output, err := renderGraphMemoryDiscovery(result, structured, false)
		if !errors.Is(err, graphstore.ErrLimitExceeded) || output != "" {
			t.Fatalf("oversized escaped output must refuse atomically: structured=%v bytes=%d err=%v", structured, len(output), err)
		}
	}
}

func TestGraphPreviewMemoryDiscoveryCompactRendering(t *testing.T) {
	result := graphMemoryDiscoveryResult{Scope: "https://example.invalid/project/", Items: []graphMemoryDiscoveryItem{
		{ID: "https://example.invalid/project/beads/policy", Title: `Code "flow" policy — 雪`, Version: "saved-version", Attribution: graphstore.Attribution{Actor: "actor-secret", Status: "claimed", RecordedAt: "timestamp-secret"}},
		{ID: "https://example.invalid/project/beads/tests", Title: "Testing policy"},
	}}
	output, err := renderGraphMemoryDiscovery(result, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Memories (2):\n\n", "  beads/policy  Code \"flow\" policy — 雪\n", "  beads/tests  Testing policy\n", "bd recall <id>", "--details"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing compact content %q in %q", want, output)
		}
	}
	for _, unwanted := range []string{result.Scope, "saved-version", "actor-secret", "timestamp-secret", "Attribution:", "Owned Links:", "--version", "Matched:"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("default output exposed details %q: %q", unwanted, output)
		}
	}
}

func TestGraphPreviewMemoryDiscoverySearchRendering(t *testing.T) {
	body := strings.Repeat("leading text ", 20) + "MATCH\n\x1b[31m" + strings.Repeat(" trailing text", 20)
	excerpt := graphMemoryBodyExcerpt(body, strings.Index(body, "MATCH"))
	result := graphMemoryDiscoveryResult{Scope: "https://example.invalid/", Items: []graphMemoryDiscoveryItem{{ID: "https://example.invalid/beads/search", Title: "Search result", MatchedFields: []string{"body"}, Excerpt: excerpt}}}
	output, err := renderGraphMemoryDiscovery(result, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "MATCH\\n\\x1b[31m") || !strings.Contains(output, "(excerpt, shortened)") || strings.Contains(output, body) || strings.Contains(output, "\x1b") || len(output) > 512 {
		t.Fatalf("search excerpt must be bounded, labeled and escaped: %q", output)
	}
	// An unshortened matching body remains explicitly identified as an excerpt.
	result.Items[0].Excerpt = &graphMemoryDiscoveryExcerpt{Field: "body", Text: "Short match"}
	output, err = renderGraphMemoryDiscovery(result, false, false)
	if err != nil || !strings.Contains(output, "    Short match (excerpt)\n") || strings.Contains(output, "shortened") {
		t.Fatalf("short excerpt: %q (%v)", output, err)
	}
}

func TestGraphPreviewMemoryDiscoveryTerminalEscaping(t *testing.T) {
	result := graphMemoryDiscoveryResult{Scope: "https://example.invalid/", Items: []graphMemoryDiscoveryItem{{
		ID: "https://example.invalid/beads/id\r\t\x1b[2J", Title: "Policy\n\u202eend", Version: "version\x1b[31m\n",
		Attribution: graphstore.Attribution{Actor: "actor\x00", Status: "claimed", RecordedAt: "time\r"}, Details: &graphMemoryDiscoveryDetails{},
	}}}
	output, err := renderGraphMemoryDiscovery(result, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"\x1b", "\r", "\t", "\x00", "\u202e", "Policy\n", "version\x1b"} {
		if strings.Contains(output, unsafe) {
			t.Fatalf("unsafe terminal character %q in %q", unsafe, output)
		}
	}
	for _, escaped := range []string{`beads/id\r\t\x1b[2J`, `Policy\n\u202eend`, `version\x1b[31m\n`, `actor\x00`, `time\r`} {
		if !strings.Contains(output, escaped) {
			t.Fatalf("data vanished instead of being escaped: %q in %q", escaped, output)
		}
	}
	if !strings.Contains(output, `basis="writer-supplied"`) {
		t.Fatalf("valid stored attribution did not reach escaped output: %q", output)
	}
}
