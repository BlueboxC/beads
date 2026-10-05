package main

import (
	"context"
	"errors"
	"github.com/steveyegge/beads/internal/codeindex"
	"github.com/steveyegge/beads/internal/knowledge"
	"github.com/steveyegge/beads/memoryops"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Narrow memory-role double: publication and preservation are the observable seam.
type maintenanceMemories struct {
	memoryops.Memories
	rows   map[string]string
	writes int
}

func (m *maintenanceMemories) List(context.Context, memoryops.ListRequest) (memoryops.ListResult, error) {
	return memoryops.ListResult{Memories: m.rows}, nil
}
func (m *maintenanceMemories) Remember(_ context.Context, r memoryops.RememberRequest) (memoryops.RememberResult, error) {
	m.rows[r.Key] = r.Content
	m.writes++
	return memoryops.RememberResult{Key: r.Key, Value: r.Content}, nil
}
func (m *maintenanceMemories) Recall(_ context.Context, r memoryops.RecallRequest) (memoryops.RecallResult, error) {
	v, ok := m.rows[r.Key]
	return memoryops.RecallResult{Value: v, Found: ok}, nil
}
func TestMaintenanceCorruptCatalogWithholdsPublication(t *testing.T) {
	m := &maintenanceMemories{rows: map[string]string{"@knowledge/catalog": `{"knowledge_version":2,"catalog_parts":["missing"],"catalog_bytes":70000}`, "human": "keep"}}
	if _, err := maintainDerived(context.Background(), m, t.TempDir(), codeindex.ScanOptions{}); err == nil || m.writes != 0 {
		t.Fatal("corrupt selection silently reset or published")
	}
}
func TestMaintenanceIncrementalDiscoveryRemovalAndHumanPreservation(t *testing.T) {
	root := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("DOX.md", "# Root\n")
	write("src/a.go", "package src\nfunc Existing(){}\n")
	write("src/excluded/skip.go", "package skip\nfunc Skip(){}\n")
	write("exact.go", "package exact\nfunc Exact(){}\n")
	write("docs/plan.md", "# Accepted plan\n")
	m := &maintenanceMemories{rows: map[string]string{"human": "keep original direction"}}
	r, err := codeindex.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	idx, err := r.ScanWithOptions(context.Background(), []string{"src", "exact.go"}, []string{"src/excluded"}, codeindex.Index{}, codeindex.ScanOptions{Languages: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codeindex.Save(context.Background(), m, m.rows, idx); err != nil {
		t.Fatal(err)
	}
	k, err := knowledge.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.Close() }()
	cat, err := k.Scan([]string{"docs", "DOX.md"})
	if err != nil {
		t.Fatal(err)
	}
	if err := knowledge.SaveCatalog(context.Background(), m, cat); err != nil {
		t.Fatal(err)
	}
	record, err := k.Bind(knowledge.Record{ID: "solved", Kind: "solution", Summary: "Preserve solved implementation", Scope: "inspected", Evidence: "source only", Sources: []knowledge.Source{{Path: "src/a.go"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := knowledge.SaveRecord(context.Background(), m, record); err != nil {
		t.Fatal(err)
	}
	human := m.rows["@knowledge/record/solved"]
	pass := func() maintenanceResult {
		t.Helper()
		x, err := maintainDerived(context.Background(), m, root, codeindex.ScanOptions{Python: "missing", Node: "missing"})
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	m.writes = 0
	if got := pass(); got.CodeUpdated || got.CatalogUpdated || m.writes != 0 {
		t.Fatalf("unchanged pass wrote: %+v writes=%d", got, m.writes)
	}
	write("src/a.go", "package src\nfunc Repaired(){}\n")
	write("src/new.go", "package src\nfunc Added(){}\n")
	write("neighbor.go", "package neighbor\nfunc OutsideSelection(){}\n")
	write("docs/new.md", "# New plan\n")
	write("docs/DOX.md", "# New contract\n")
	if err := os.Remove(filepath.Join(root, "exact.go")); err != nil {
		t.Fatal(err)
	}
	got := pass()
	if !got.CodeUpdated || !got.CatalogUpdated || got.Files != 2 || got.Changes.Parsed != 2 || !reflect.DeepEqual(got.Changes.Removed, []string{"exact.go"}) {
		t.Fatalf("changed pass: %+v", got)
	}
	if m.rows["@knowledge/record/solved"] != human || m.rows["human"] != "keep original direction" {
		t.Fatal("derived maintenance renewed human evidence")
	}
	state := k.Refresh(knowledge.Decode(m.rows))
	if len(state.Records) != 1 || state.Records[0].Validity != "needs_review" {
		t.Fatal("human learning incorrectly upgraded")
	}
	if err := os.Rename(filepath.Join(root, "src/new.go"), filepath.Join(root, "src/renamed.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "docs/new.md")); err != nil {
		t.Fatal(err)
	}
	got = pass()
	if len(got.Changes.Renamed) != 1 || got.Changes.Renamed[0].To != "src/renamed.go" {
		t.Fatalf("rename lost: %+v", got)
	}
	// All explicit roots remain saved even when missing, so a returning branch
	// file is recovered; a sibling outside a directory root stays unselected.
	write("exact.go", "package exact\nfunc Exact(){}\n")
	got = pass()
	if got.Files != 3 {
		t.Fatalf("returning exact root lost: %+v", got)
	}
	m.writes = 0
	if got := pass(); got.CodeUpdated || got.CatalogUpdated || m.writes != 0 {
		t.Fatal("steady state churned")
	}
	// A document failure is found before any code publication starts.
	write("src/a.go", "package src\nfunc Newer(){}\n")
	if err := os.Symlink("../DOX.md", filepath.Join(root, "docs/linked.md")); err != nil {
		t.Fatal(err)
	}
	// Explicit symlink roots refuse; directory scans continue to ignore links.
	bad := knowledge.Catalog{Roots: []string{"docs/linked.md"}}
	if err := knowledge.SaveCatalog(context.Background(), m, bad); err != nil {
		t.Fatal(err)
	}
	before, _ := codeindex.Load(m.rows)
	m.writes = 0
	if _, err := maintainDerived(context.Background(), m, root, codeindex.ScanOptions{}); err == nil {
		t.Fatal("symlink selection accepted")
	}
	after, _ := codeindex.Load(m.rows)
	if m.writes != 0 || !reflect.DeepEqual(before, after) {
		t.Fatal("failed derivation published partial selection")
	}
}

func TestMaintenanceWatchCancellationReleasesPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := watchMaintenance(ctx, 1, func(ctx context.Context) error {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded child pass")
		}
		cancel()
		return errors.New("interrupted")
	}, func(error, time.Duration) {})
	if err != nil || calls != 1 {
		t.Fatalf("shutdown: calls=%d err=%v", calls, err)
	}
}
