package main

import (
	"fmt"
	"github.com/steveyegge/beads/internal/memoryapi"
	"testing"
)

func TestKnowledgeSelectionExcludesRetainedCodeAndPreservesOrigins(t *testing.T) {
	plane := map[string]string{"@knowledge/record/solved": "accepted", "@knowledge/proposal/pending": "pending", "@knowledge/catalog": "catalog", "@knowledge/catalog-part/x": "part", "@activity/event/event": "origin", "ordinary": "plain"}
	for i := 0; i < 101281; i++ {
		plane[fmt.Sprintf("@knowledge/code/blob/%d", i)] = "retained"
	}
	got := memoryapi.SelectMemories(plane, knowledgeMemorySelection())
	if len(got) != 5 || got["@activity/event/event"] != "origin" || got["@knowledge/record/solved"] != "accepted" || got["@knowledge/proposal/pending"] != "pending" {
		t.Fatal("knowledge/origin selection changed")
	}
	if len(plane) != 101287 {
		t.Fatal("selection mutated source")
	}
}
