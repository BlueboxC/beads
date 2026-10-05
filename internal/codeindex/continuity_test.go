package codeindex

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/knowledge"
)

func TestImpactTransitiveCyclesBoundsAndExplicitTestEvidence(t *testing.T) {
	root := t.TempDir()
	source(t, root, "pkg/fix.py", "def solve(): return 42\n")
	source(t, root, "pkg/use.py", "from .fix import solve\ndef use(): return solve()\n")
	source(t, root, "tests/test_use.py", "from pkg.use import use\ndef test_use(): return use()\n")
	source(t, root, "pkg/other.py", "def unrelated(): return unknown()\n")
	r := readerFor(t, root)
	index, err := r.Scan(context.Background(), []string{"pkg", "tests"}, nil, Index{}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	index = r.Refresh(index)
	state := knowledge.State{Records: []knowledge.View{{Record: knowledge.Record{ID: "solved", Issue: "task-closed", Scope: "inspected", Sources: []knowledge.Source{{Path: "pkg/fix.py"}, {Path: "tests/test_regression.py"}}}, Validity: "needs_review"}}}
	report, err := Impact(index, state, "pkg/fix.py::solve", 8, 200)
	if err != nil || len(report.Files) != 3 || len(report.Tests) != 2 || len(report.Knowledge) != 1 || report.Knowledge[0].Validity != "needs_review" || len(report.Issues) != 1 || report.UnresolvedTotal == 0 {
		t.Fatalf("impact lost dependency/evidence boundaries: %+v %v", report, err)
	}
	if report.Files[2].Depth != 2 || len(report.Files[2].Via) != 2 {
		t.Fatal("missing transitive witness path")
	}
	short, err := Impact(index, state, "pkg/fix.py", 1, 1)
	if err != nil || short.Omitted != 1 || !short.DepthLimited {
		t.Fatalf("bounds hidden: %+v %v", short, err)
	}
	if _, err := Impact(index, state, "not-indexed", 8, 100); err == nil {
		t.Fatal("unknown selector accepted")
	}
	// A file cycle must terminate and must not become a blocker.
	index.Files[0].Imports = append(index.Files[0].Imports, Import{Owner: "pkg/fix.py::", Name: "pkg.use", Alias: "use"})
	cycle, err := Impact(index, state, "pkg/fix.py", 8, 200)
	if err != nil || len(cycle.Files) != 3 {
		t.Fatalf("cycle: %+v %v", cycle, err)
	}
}

func TestRelinkDraftRetainsEvidenceAndRefusesAmbiguousOrEditedMove(t *testing.T) {
	root := t.TempDir()
	source(t, root, "DOX.md", "# Original contract\n")
	source(t, root, "src/fix.py", "def solved(): return 42\n")
	r := readerFor(t, root)
	index, err := r.Scan(context.Background(), []string{"src"}, nil, Index{}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	record, err := r.sources.Bind(knowledge.Record{ID: "fix", Kind: "solution", Summary: "Preserve fix", Scope: "tested", Evidence: "Historical verified result", Symbols: []string{"src/fix.py::solved"}, Sources: []knowledge.Source{{Path: "src/fix.py"}}})
	if err != nil {
		t.Fatal(err)
	}
	state := knowledge.State{Records: []knowledge.View{{Record: record, Validity: "needs_review"}}}
	if err := os.Rename(filepath.Join(root, "src/fix.py"), filepath.Join(root, "src/moved.py")); err != nil {
		t.Fatal(err)
	}
	source(t, root, "src/DOX.md", "# New contract requires review\n")
	index, err = r.Scan(context.Background(), []string{"src"}, nil, index, false, "")
	if err != nil {
		t.Fatal(err)
	}
	report, err := r.PrepareRelink(r.Refresh(index), state, "src/fix.py", "src/moved.py")
	if err != nil || len(report.Drafts) != 1 {
		t.Fatalf("relink: %+v %v", report, err)
	}
	draft := report.Drafts[0]
	if !reflect.DeepEqual(draft.Previous, record) || draft.Draft.Scope != "recorded" || draft.Draft.Symbols[0] != "src/moved.py::solved" || !reflect.DeepEqual(state.Records[0].Record, record) {
		t.Fatal("draft renewed or mutated original learning")
	}
	contracts := map[string]bool{}
	for _, s := range draft.Draft.Sources {
		contracts[s.Path] = true
	}
	if !contracts["src/DOX.md"] {
		t.Fatal("new DOX omitted")
	}
	source(t, root, "src/duplicate.py", "def solved(): return 42\n")
	index, err = r.Scan(context.Background(), []string{"src"}, nil, index, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PrepareRelink(r.Refresh(index), state, "src/fix.py", "src/moved.py"); err == nil {
		t.Fatal("ambiguous identical move accepted")
	}
	if err := os.Remove(filepath.Join(root, "src/duplicate.py")); err != nil {
		t.Fatal(err)
	}
	source(t, root, "src/moved.py", "def solved(): return 43\n")
	index, err = r.Scan(context.Background(), []string{"src"}, nil, index, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PrepareRelink(r.Refresh(index), state, "src/fix.py", "src/moved.py"); err == nil {
		t.Fatal("edited move rebound evidence")
	}
}

func TestPrunePlanRetainsManifestReferencedBlobsAndRefusesCorruption(t *testing.T) {
	root := t.TempDir()
	source(t, root, "src/fix.py", "def solved(): return 42\n")
	r := readerFor(t, root)
	index, err := r.Scan(context.Background(), []string{"src"}, nil, Index{}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	s := &publicationStore{plane: map[string]string{"@knowledge/record/fix": "human", "@activity/event/fix": "event", "plain": "memory"}}
	if _, err := Save(context.Background(), s, s.plane, index); err != nil {
		t.Fatal(err)
	}
	source(t, root, "src/fix.py", "def solved(): return 43\n")
	index, err = r.Scan(context.Background(), []string{"src"}, nil, index, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Save(context.Background(), s, s.plane, index); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanPrune(s.plane)
	if err != nil || len(plan.ObsoleteKeys) == 0 || !plan.HistoryRetained {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	for _, key := range plan.ObsoleteKeys {
		delete(s.plane, key)
	}
	loaded, err := Load(s.plane)
	if err != nil || loaded.Files[0].SHA256 != index.Files[0].SHA256 || s.plane["plain"] != "memory" || s.plane["@knowledge/record/fix"] != "human" {
		t.Fatal("plan selected live or human data")
	}
	for key := range s.plane {
		if key != manifestKey && IsKey(key) {
			delete(s.plane, key)
			break
		}
	}
	if _, err := PlanPrune(s.plane); err == nil {
		t.Fatal("corrupt generation allowed cleanup")
	}
}
