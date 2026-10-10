package main

import (
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/graphview"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestGraphContinuityViewRetainsCanonicalIdentityAndLinkType(t *testing.T) {
	const scope = "https://example.invalid/"
	page := graphview.Page{Nodes: []graphview.Node{{ID: "record:fix", Type: "solution", Detail: map[string]string{"solution": "verified"}}}}
	memory := graphstore.Record{ID: scope + "beads/memory", Revision: "version", Metadata: json.RawMessage(`{"forkContinuityKey":"@knowledge/record/fix"}`)}
	issue := graphstore.IssueRecord{ID: scope + "beads/work", Properties: &publicops.Issue{ID: "work"}}
	link := graphstore.LinkRecord{ID: scope + "links/proof", Source: memory.ID, Target: issue.ID, Type: scope + "types/example-cites", Revision: "link-version", Properties: map[string]any{"note": "explicit evidence"}}
	mergePreviewGraph(&page, graphstore.Snapshot{Records: []any{memory, issue, link}})
	if len(page.Nodes) != 1 || len(page.Links) != 1 {
		t.Fatalf("duplicated semantic Memory or lost Link: %+v", page)
	}
	edge := page.Links[0]
	if edge.Source != "record:fix" || edge.Target != "issue:work" || edge.Type != "cites" {
		t.Fatalf("lost endpoints/type: %+v", edge)
	}
	if got, ok := edge.Detail.(graphstore.LinkRecord); !ok || got.ID != link.ID || got.Revision != link.Revision {
		t.Fatal("lost canonical Link evidence")
	}
	detail := page.Nodes[0].Detail.(map[string]any)
	if detail["solution"] != "verified" {
		t.Fatal("overwrote learning details")
	}
	if got, ok := detail["canonical_record"].(graphstore.Record); !ok || got.ID != memory.ID {
		t.Fatal("lost canonical Memory identity")
	}
}
