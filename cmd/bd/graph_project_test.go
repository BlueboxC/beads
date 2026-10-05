package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/codeindex"
	"github.com/steveyegge/beads/internal/knowledge"
	"github.com/steveyegge/beads/internal/types"
)

func TestProjectGraphPreservesLearningsTasksAndCompleteIndex(t *testing.T) {
	state := knowledge.State{Records: []knowledge.View{{Record: knowledge.Record{ID: "fix", Kind: "solution", Summary: "Keep prior solution", Issue: "closed", Scope: "tested", Symbols: []string{"a/one.py::f"}, Sources: []knowledge.Source{{Path: "a/one.py", SHA256: "old"}}}, Validity: "needs_review"}}}
	index := codeindex.Index{Files: []codeindex.File{
		{Path: "a/one.py", Module: "a.one", SHA256: "new", Validity: "needs_review", Symbols: []codeindex.Symbol{{ID: "a/one.py::f", Name: "f", Kind: "function", Line: 7}}, Imports: []codeindex.Import{{Owner: "a/one.py::", Name: "b.two", Alias: "two"}}, Calls: []codeindex.Call{{Owner: "a/one.py::f", Name: "missing"}}},
		{Path: "b/two.py", Module: "b.two", SHA256: "b", Validity: "current", Imports: []codeindex.Import{{Owner: "b/two.py::", Name: "a.one", Alias: "one"}}},
	}}
	issues := []*types.Issue{{ID: "closed", Title: "Already fixed", Status: types.StatusClosed}, {ID: "next", Title: "Next", Status: types.StatusDeferred}}
	deps := []*types.Dependency{{IssueID: "next", DependsOnID: "closed", Type: types.DepBlocks}, {IssueID: "next", DependsOnID: "external:other:foreign", Type: types.DepRelated}}
	before, _ := json.Marshal([]any{state, index, issues, deps})
	page := buildProjectGraph("/workspace", state, index, issues, deps)
	if len(page.Files) != 2 || page.Files[0].Symbols[0].Line != 7 || page.Files[0].SHA256 != "new" {
		t.Fatal("overview omitted source/symbol provenance")
	}
	nodes := map[string]bool{}
	for _, node := range page.Nodes {
		nodes[node.ID] = true
		if node.ID == "issue:closed" && node.Status != "closed" {
			t.Fatal("completed task became a reference or changed status")
		}
		if node.ID == "record:fix" && node.Validity != "needs_review" {
			t.Fatal("derived overview renewed a stale solution")
		}
	}
	for _, id := range []string{"directory:a", "directory:b", "record:fix", "source:a/one.py", "issue:closed", "issue:next"} {
		if !nodes[id] {
			t.Fatalf("missing %s", id)
		}
	}
	imports, blockers, learned := 0, 0, 0
	for _, edge := range page.Links {
		if !nodes[edge.Source] || !nodes[edge.Target] {
			t.Fatal("dangling or foreign workspace edge")
		}
		switch edge.Type {
		case "imports":
			imports++
			if edge.Count != 1 || edge.Validity != "needs_review" {
				t.Fatal("syntax provenance lost")
			}
		case "blocks":
			blockers++
			if !strings.HasPrefix(edge.Source, "issue:") {
				t.Fatal("code cycle turned into task blocker")
			}
		case "learned-at":
			learned++
		}
	}
	if imports != 2 || blockers != 1 || learned != 1 {
		t.Fatalf("wrong typed relations: imports=%d blockers=%d learned=%d", imports, blockers, learned)
	}
	after, _ := json.Marshal([]any{state, index, issues, deps})
	if string(before) != string(after) {
		t.Fatal("read-only projection mutated input records")
	}
}

func TestProjectGraphDoesNotUseTheCodeQueryFileLimit(t *testing.T) {
	index := codeindex.Index{}
	for i := 0; i < 1100; i++ {
		index.Files = append(index.Files, codeindex.File{Path: strings.Repeat("a", i%10+1) + "/file" + string(rune(0x100+i)) + ".py", Validity: "current"})
	}
	page := buildProjectGraph("/workspace", knowledge.State{}, index, nil, nil)
	if len(page.Files) != 1100 {
		t.Fatalf("silently omitted %d files", 1100-len(page.Files))
	}
}
