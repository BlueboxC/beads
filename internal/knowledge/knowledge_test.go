package knowledge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSource(t *testing.T, root, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSourceChangesRequireReviewWithoutUpgradingEvidence(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "DOX.md", "# Root contract\n")
	writeSource(t, root, "module/fix.py", "def fix(): pass\n")
	reader, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	record, err := reader.Bind(Record{ID: "repair", Kind: "solution", Summary: "Discard stale evidence", Scope: "inspected", Evidence: "Read the implementation; tests not run", Sources: []Source{{Path: "module/fix.py"}}})
	if err != nil {
		t.Fatal(err)
	}
	state := State{Records: []View{{Record: record}}}
	if got := reader.Refresh(state).Records[0]; got.Validity != "current" || got.Scope != "inspected" {
		t.Fatalf("unexpected evidence: %+v", got)
	}
	writeSource(t, root, "module/DOX.md", "# Newly introduced contract\n")
	if got := reader.Refresh(state).Records[0]; got.Validity != "needs_review" || !strings.Contains(strings.Join(got.Changed, ","), "module/DOX.md") {
		t.Fatalf("new DOX was ignored: %+v", got)
	}
	if err := os.Remove(filepath.Join(root, "module/DOX.md")); err != nil {
		t.Fatal(err)
	}
	writeSource(t, root, "DOX.md", "# Changed root contract\n")
	if got := reader.Refresh(state).Records[0]; got.Validity != "needs_review" || got.Scope != "inspected" {
		t.Fatalf("DOX change upgraded evidence: %+v", got)
	}
	if err := os.Remove(filepath.Join(root, "module/fix.py")); err != nil {
		t.Fatal(err)
	}
	if got := reader.Refresh(state).Records[0]; got.Validity != "needs_review" {
		t.Fatal("missing source remained current")
	}
}

func TestScanDiscoveryIsBoundedAndDoesNotFollowExternalSources(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeSource(t, root, "DOX.md", "# Root\n")
	writeSource(t, root, "docs/plan.md", "# Plan\n## Evidence\n")
	writeSource(t, root, "docs/node_modules/ignored.md", "ignored")
	writeSource(t, outside, "private.md", "outside")
	if err := os.Symlink(filepath.Join(outside, "private.md"), filepath.Join(root, "docs/escape.md")); err != nil {
		t.Fatal(err)
	}
	reader, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	catalog, err := reader.Scan([]string{"docs"})
	if err != nil || len(catalog.Sources) != 2 {
		t.Fatalf("scan: %+v, %v", catalog, err)
	}
	for _, path := range []string{"../private.md", "docs/escape.md"} {
		if _, err := reader.Snapshot(path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	writeSource(t, root, "docs/new-plan.md", "# New plan\n")
	state := reader.Discover(State{Catalog: catalog})
	if len(state.Catalog.Sources) != 2 || len(state.Warnings) != 1 {
		t.Fatalf("discovery mutated catalog or lost new source: %+v", state)
	}
	for i := 0; i < MaxSources; i++ {
		writeSource(t, root, fmt.Sprintf("docs/file-%d.md", i), "# Doc\n")
	}
	if _, err := reader.Scan([]string{"docs"}); err == nil {
		t.Fatal("unbounded catalog accepted")
	}
}

func TestKnowledgeContextAndGraphKeepAssertionsExplicit(t *testing.T) {
	state := State{Catalog: Catalog{Sources: []Source{{Path: "DOX.md", SHA256: "abc"}}}, Records: []View{}}
	for i := 0; i < MaxRecords; i++ {
		state.Records = append(state.Records, View{Record: Record{ID: fmt.Sprintf("fix-%d", i), Kind: "solution", Summary: strings.Repeat("s", 1000), Scope: "recorded", Sources: []Source{{Path: "DOX.md"}}}, Validity: "needs_review"})
	}
	state.Records = append(state.Records, View{Record: Record{ID: "goal", Kind: "objective", Summary: "Continue the approved plan", Issue: "test-1", Scope: "recorded", Related: []string{"fix-0"}, Sources: []Source{{Path: "DOX.md"}}}, Validity: "current"})
	context := Context(state)
	if len(context) > 6500 || !strings.Contains(context, "goal") || !strings.Contains(context, "needs_review") || strings.Contains(context, "knowledge_version") {
		t.Fatalf("unbounded or ambiguous context: %s", context)
	}
	graph := BuildGraph(state)
	if len(graph.Nodes) != len(state.Records)+2 {
		t.Fatalf("unexpected graph nodes: %d", len(graph.Nodes))
	}
	relations := make(map[string]bool)
	for _, edge := range graph.Edges {
		relations[edge.Kind] = true
	}
	for _, kind := range []string{"related", "supported-by", "recorded-for"} {
		if !relations[kind] {
			t.Fatalf("missing %s relationship", kind)
		}
	}
	graph.Nodes[0].Label = "</script><script>throw 'unsafe'</script>"
	var out bytes.Buffer
	if err := WriteHTML(&out, graph); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "<script>throw 'unsafe'") || strings.Contains(out.String(), "<script src=") {
		t.Fatal("unsafe or network-dependent graph")
	}
}

func TestDecodeDoesNotTreatMalformedKnowledgeAsOrdinaryMemory(t *testing.T) {
	record := Record{ID: "decision", Kind: "decision", Summary: "Keep one database", Scope: "recorded", Sources: []Source{{Path: "DOX.md"}}}
	data, err := json.Marshal(envelope{Version: 1, Record: &record})
	if err != nil {
		t.Fatal(err)
	}
	state := Decode(map[string]string{Prefix + "record/decision": string(data), Prefix + "broken": "not-json", "normal": "plain memory"})
	if len(state.Records) != 1 || len(state.Warnings) != 1 {
		t.Fatalf("decode: %+v", state)
	}
	record.Scope = "tested"
	if Validate(record) == nil {
		t.Fatal("tested assertion without evidence accepted")
	}
}

func TestLargeExplicitCatalogSurvivesDecodeAndRefresh(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "DOX.md", "# Root contract\n")
	paths := make([]string, 80)
	for i := range paths {
		paths[i] = fmt.Sprintf("docs/contract-%d.md", i)
		writeSource(t, root, paths[i], "# Contract\n")
	}
	reader, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	catalog, err := reader.Scan(paths)
	if err != nil || len(catalog.Sources) != 81 {
		t.Fatalf("large selection: %d sources, %v", len(catalog.Sources), err)
	}
	encoded, err := json.Marshal(envelope{Version: 1, Catalog: &catalog})
	if err != nil {
		t.Fatal(err)
	}
	state := Decode(map[string]string{Prefix + "catalog": string(encoded)})
	if len(state.Catalog.Sources) != 81 {
		t.Fatal("large catalog lost on reload")
	}
	writeSource(t, root, paths[0], "# Changed contract\n")
	state = reader.Refresh(state)
	if len(state.Catalog.Sources) != 81 || len(state.Warnings) == 0 {
		t.Fatal("refresh lost catalog or ignored changed source")
	}
	if _, err := reader.Scan(make([]string, MaxSources+1)); err == nil {
		t.Fatal("unbounded explicit selection accepted")
	}
}
