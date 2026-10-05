//go:build cgo

package embeddeddolt_test

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/codeindex"
	"github.com/steveyegge/beads/memoryops"
)

// Distinct real persistence boundary: a cached reused blob may have been pruned
// before publication. The atomic publisher must restore it, or refuse a changed
// manifest without leaving a partial generation.
func TestAtomicCodePublicationRestoresPrunedReuseAndRejectsStaleManifest(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cpa")
	ctx := t.Context()
	mem, err := te.store.Memories()
	if err != nil {
		t.Fatal(err)
	}
	a := codeindex.Index{Version: 2, Roots: []string{"src"}, Files: []codeindex.File{{Path: "src/a.py", SHA256: strings.Repeat("a", 64)}}}
	if _, err := codeindex.Save(ctx, mem, map[string]string{}, a); err != nil {
		t.Fatal(err)
	}
	p1, err := mem.List(ctx, memoryops.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	b := a
	b.Files = []codeindex.File{{Path: "src/a.py", SHA256: strings.Repeat("b", 64)}}
	if _, err := codeindex.Save(ctx, mem, p1.Memories, b); err != nil {
		t.Fatal(err)
	}
	p2, err := mem.List(ctx, memoryops.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := codeindex.ApplyPrune(ctx, mem, p2.Memories)
	if err != nil || plan.Deleted == 0 {
		t.Fatalf("prune: %+v %v", plan, err)
	}
	// p2 still claims the old A blob exists, though pruning just removed it.
	if _, err := codeindex.Save(ctx, mem, p2.Memories, a); err != nil {
		t.Fatal(err)
	}
	p3, err := mem.List(ctx, memoryops.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := codeindex.Load(p3.Memories)
	if err != nil || loaded.Files[0].SHA256 != a.Files[0].SHA256 {
		t.Fatal("published missing reused blob")
	}
	if _, err := codeindex.Save(ctx, mem, p2.Memories, b); err == nil {
		t.Fatal("stale manifest publication accepted")
	}
	last, err := mem.List(ctx, memoryops.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = codeindex.Load(last.Memories)
	if err != nil || loaded.Files[0].SHA256 != a.Files[0].SHA256 {
		t.Fatal("refused publisher damaged active generation")
	}
	atomic := mem.(memoryops.AtomicMemories)
	_, err = atomic.Apply(ctx, memoryops.BatchRequest{Remember: map[string]string{"a-small": "must-rollback", "z-too-large": strings.Repeat("x", 70<<10)}})
	if err == nil {
		t.Fatal("oversized TEXT write did not fail")
	}
	rolled, err := mem.Recall(ctx, memoryops.RecallRequest{Key: "a-small"})
	if err != nil || rolled.Found {
		t.Fatal("failed batch left partial write")
	}
}
