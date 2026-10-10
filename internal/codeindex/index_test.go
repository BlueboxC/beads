package codeindex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/memoryops"
)

func source(t *testing.T, root, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func readerFor(t *testing.T, root string) *Reader {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("operator Python 3 unavailable")
	}
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestASTReferencesAreStaticCyclesAndNeverExecuteProject(t *testing.T) {
	root := t.TempDir()
	sentinel := filepath.Join(root, "must-not-exist")
	source(t, root, "pkg/a.py", "from .b import other as alias\nfrom . import b\ndef first():\n    return alias()\ndef shadow(alias):\n    return alias()\nasync def nested():\n    def inner(): return first()\n    return inner()\nclass Thing:\n    def only_method(self): return first()\n    def sibling(self): return only_method()\nopen("+"'"+sentinel+"'"+", 'w').write('executed')\n")
	source(t, root, "pkg/b.py", "from .a import first\ndef other(): return first()\n")
	r := readerFor(t, root)
	index, err := r.Scan(context.Background(), []string{"pkg"}, nil, Index{}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("project code executed")
	}
	if index.Stats.Files != 2 || index.Stats.Symbols != 8 || index.Stats.ParseErrors != 0 {
		t.Fatalf("structure lost: %+v", index.Stats)
	}
	refs := Relations(index)
	found := map[string]bool{}
	for _, ref := range refs {
		if ref.Kind == "calls" {
			found[ref.Source+"->"+ref.Target] = true
		}
		if ref.Source == "pkg/a.py::shadow" && ref.Target != "" {
			t.Fatal("parameter call guessed")
		}
		if ref.Source == "pkg/a.py::Thing.sibling" && ref.Target != "" {
			t.Fatal("class namespace treated as closure")
		}
	}
	for _, link := range []string{"pkg/a.py::first->pkg/b.py::other", "pkg/b.py::other->pkg/a.py::first", "pkg/a.py::nested.inner->pkg/a.py::first", "pkg/a.py::nested->pkg/a.py::nested.inner"} {
		if !found[link] {
			t.Fatalf("missing reference %s: %+v", link, refs)
		}
	}
}

// This narrow store models publication failure only, not Dolt semantics.
type publicationStore struct {
	plane        map[string]string
	failManifest bool
}

func (s *publicationStore) Remember(_ context.Context, req memoryops.RememberRequest) (memoryops.RememberResult, error) {
	if req.Key == manifestKey && s.failManifest {
		return memoryops.RememberResult{}, errors.New("publication failed")
	}
	_, found := s.plane[req.Key]
	s.plane[req.Key] = req.Content
	return memoryops.RememberResult{Key: req.Key, Value: req.Content, Replaced: found}, nil
}
func (s *publicationStore) Recall(_ context.Context, _ memoryops.RecallRequest) (memoryops.RecallResult, error) {
	panic("unused")
}
func (s *publicationStore) Forget(_ context.Context, _ memoryops.ForgetRequest) (memoryops.ForgetResult, error) {
	panic("unused")
}
func (s *publicationStore) List(_ context.Context, _ memoryops.ListRequest) (memoryops.ListResult, error) {
	panic("unused")
}

func TestIncrementalRenameAndInterruptedPublicationPreserveHumanKnowledge(t *testing.T) {
	root := t.TempDir()
	source(t, root, "DOX.md", "# Original contract\n")
	source(t, root, "mod/a.py", "def solved(): return 1\n")
	source(t, root, "mod/b.py", "def existing(): return 2\n")
	r := readerFor(t, root)
	first, err := r.Scan(context.Background(), []string{"mod"}, nil, Index{}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	store := &publicationStore{plane: map[string]string{"@knowledge/record/solved": "original assertion bytes", "plain": "keep me"}}
	if _, err := Save(context.Background(), store, store.plane, first); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(store.plane)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := r.Scan(context.Background(), []string{"mod"}, nil, loaded, false, "/missing-interpreter")
	if err != nil || unchanged.Changes.Parsed != 0 || unchanged.Changes.Reused != 2 {
		t.Fatalf("unchanged code reparsed: %+v %v", unchanged.Changes, err)
	}
	if err := os.Rename(filepath.Join(root, "mod/a.py"), filepath.Join(root, "mod/moved.py")); err != nil {
		t.Fatal(err)
	}
	source(t, root, "DOX.md", "# New contract\n")
	if got := r.Refresh(loaded); len(got.Warnings) != 3 {
		t.Fatalf("rename/contract drift lost: %+v", got.Warnings)
	}
	changed, err := r.Scan(context.Background(), []string{"mod"}, nil, loaded, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Changes.Parsed != 1 || changed.Changes.Reused != 1 || len(changed.Changes.Renamed) != 1 || changed.Changes.Renamed[0].From != "mod/a.py" {
		t.Fatalf("incremental rename lost: %+v", changed.Changes)
	}
	store.failManifest = true
	if _, err := Save(context.Background(), store, store.plane, changed); err == nil {
		t.Fatal("publication failure ignored")
	}
	recovered, err := Load(store.plane)
	if err != nil || recovered.Files[0].Path != "mod/a.py" {
		t.Fatalf("partial scan replaced current generation: %+v %v", recovered.Files, err)
	}
	store.failManifest = false
	if _, err := Save(context.Background(), store, store.plane, changed); err != nil {
		t.Fatal(err)
	}
	if store.plane["@knowledge/record/solved"] != "original assertion bytes" || store.plane["plain"] != "keep me" {
		t.Fatal("derived scan modified human memory")
	}
	loaded, err = Load(store.plane)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := r.Scan(context.Background(), []string{"mod"}, nil, loaded, true, "")
	if err != nil || rebuilt.Changes.Parsed != 2 {
		t.Fatalf("rebuild: %+v %v", rebuilt.Changes, err)
	}
}

func TestConfinedDiscoverySyntaxErrorsAndUncertainBindings(t *testing.T) {
	root := t.TempDir()
	source(t, root, "src/a.py", "def invalid(:\npassword='do-not-print-this'\n")
	source(t, root, "src/b.py", "def target(): pass\ndef uncertain(target): return target()\ndef assigned():\n    target = lambda: None\n    return target()\n")
	source(t, root, "src/.private/no.py", "not Python")
	source(t, root, "src/generated/no.py", "not Python")
	r := readerFor(t, root)
	index, err := r.Scan(context.Background(), []string{"src"}, []string{"src/generated"}, Index{}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if index.Stats.Files != 2 || index.Stats.ParseErrors != 1 || strings.Contains(index.Files[0].ParseError, "do-not-print") {
		t.Fatalf("partial syntax/error privacy: %+v", index)
	}
	for _, ref := range Relations(index) {
		if strings.HasSuffix(ref.Source, "uncertain") || strings.HasSuffix(ref.Source, "assigned") {
			if ref.Target != "" {
				t.Fatalf("shadowed callable guessed: %+v", ref)
			}
		}
	}
	if _, err := r.Scan(context.Background(), []string{"../outside"}, nil, Index{}, false, ""); err == nil {
		t.Fatal("escaped root")
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.py"), filepath.Join(root, "src/link.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Scan(context.Background(), []string{"src"}, nil, Index{}, false, ""); err == nil {
		t.Fatal("symlink read or silently accepted")
	}
}

func TestIndexCorruptionIsVisibleAndNotOrdinaryContext(t *testing.T) {
	if _, err := Load(map[string]string{manifestKey: "not-an-index"}); err == nil {
		t.Fatal("corrupt index accepted")
	}
	if got := Summary(map[string]string{manifestKey: "not-an-index"}); !strings.Contains(got, "unavailable") {
		t.Fatal("corrupt index omitted")
	}
	if _, err := pack(strings.Repeat("x", maxJSONBytes+1)); err == nil {
		t.Fatal("unbounded decompression input")
	}
}

// Crosses the original 1024-file ceiling and multiple parser/storage batches.
func TestLargeCatalogMigratesAndPersistsWithoutRenewingHumanRecords(t *testing.T) {
	root := t.TempDir()
	source(t, root, "DOX.md", "# Stable contract\n")
	const files = 1031
	for i := 0; i < files; i++ {
		source(t, root, fmt.Sprintf("pkg/file_%04d.py", i), fmt.Sprintf("def solved_%d(): return %d\n", i, i))
	}
	r := readerFor(t, root)
	index, err := r.Scan(context.Background(), []string{"pkg"}, nil, Index{}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if index.Stats.Files != files || index.Stats.Symbols != files || index.Changes.Parsed != files {
		t.Fatalf("incomplete catalog: %+v", index.Stats)
	}
	store := &publicationStore{plane: map[string]string{"@knowledge/record/solved": "unchanged reviewed solution"}}
	// Existing version-1 generations remain readable by the newer client.
	legacy := index
	legacy.Version = 1
	legacy.Files = legacy.Files[:1]
	legacy.Stats.Files = 1
	if _, err := Save(context.Background(), store, store.plane, legacy); err != nil {
		t.Fatal(err)
	}
	before := store.plane[manifestKey]
	if got, err := Load(store.plane); err != nil || got.Version != 1 || len(got.Files) != 1 {
		t.Fatalf("legacy read: %+v %v", got.Stats, err)
	}
	store.failManifest = true
	if _, err := Save(context.Background(), store, store.plane, index); err == nil {
		t.Fatal("failed publication accepted")
	}
	if store.plane[manifestKey] != before {
		t.Fatal("failed migration replaced legacy manifest")
	}
	store.failManifest = false
	if _, err := Save(context.Background(), store, store.plane, index); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(store.plane)
	if err != nil || loaded.Version != 2 || len(loaded.Files) != files {
		t.Fatalf("large catalog persistence: %+v %v", loaded.Stats, err)
	}
	var published manifest
	if err := unpack(store.plane[manifestKey], &published); err != nil {
		t.Fatal(err)
	}
	if len(published.Parts) < 2 || len(published.References) != 0 {
		t.Fatal("large catalog lacks bounded reference parts")
	}
	for key, value := range store.plane {
		if len(value) > maxRowBytes {
			t.Fatalf("oversized row: %s", key)
		}
	}
	reused, err := r.Scan(context.Background(), []string{"pkg"}, nil, loaded, false, "/missing-interpreter")
	if err != nil || reused.Changes.Reused != files || reused.Changes.Parsed != 0 {
		t.Fatalf("unchanged catalog required parser: %+v %v", reused.Changes, err)
	}
	partKey := Prefix + "blob/" + published.Parts[0]
	part := store.plane[partKey]
	delete(store.plane, partKey)
	if _, err := Load(store.plane); err == nil {
		t.Fatal("missing reference part silently truncated catalog")
	}
	store.plane[partKey] = part + "corrupt"
	if _, err := Load(store.plane); err == nil {
		t.Fatal("corrupt reference part accepted")
	}
	if store.plane["@knowledge/record/solved"] != "unchanged reviewed solution" {
		t.Fatal("catalog changed human record")
	}
}

func BenchmarkParserHashUnchanged200(b *testing.B) {
	parserHash("typescript")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for i := 0; i < 400; i++ {
			parserHash("typescript")
		}
	}
}

func TestParserFingerprintsRemainExactUnderConcurrency(t *testing.T) {
	languages, err := normalizeLanguages([]string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, language := range languages {
		want := computeParserHash(language)
		for range 8 {
			wg.Go(func() {
				if got := parserHash(language); got != want {
					t.Errorf("%s fingerprint changed: %s != %s", language, got, want)
				}
			})
		}
	}
	wg.Wait()
}
func TestRefreshCacheDoesNotSurviveQueryOrHideNewContracts(t *testing.T) {
	root := t.TempDir()
	source(t, root, "pkg/a.go", "package pkg\nfunc First() {}\n")
	r := readerFor(t, root)
	index, err := r.ScanWithOptions(context.Background(), []string{"pkg"}, nil, Index{}, ScanOptions{Languages: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.RefreshSelected(index); got.Files[0].Validity != "current" {
		t.Fatalf("initial state: %+v", got)
	}
	source(t, root, "pkg/DOX.md", "# Newly applicable contract\n")
	if got := r.RefreshSelected(index); got.Files[0].Validity != "needs_review" {
		t.Fatal("new DOX hidden by cache")
	}
	if err := os.Remove(filepath.Join(root, "pkg/DOX.md")); err != nil {
		t.Fatal(err)
	}
	if got := r.RefreshSelected(index); got.Files[0].Validity != "current" {
		t.Fatal("stale contract result survived query")
	}
	source(t, root, "pkg/a.go", "package pkg\nfunc Changed() {}\n")
	if got := r.RefreshSelected(index); got.Files[0].Validity != "needs_review" {
		t.Fatal("changed file hidden by cache")
	}
}

func TestIsolatedParserRunnerHonorsCancellationAndOutputBounds(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := runParser(ctx, python, []string{"-I", "-S", "-c", "import time; time.sleep(30)"}, nil); err == nil {
		t.Fatal("cancelled parser succeeded")
	}
	if ctx.Err() != context.DeadlineExceeded || time.Since(started) > 5*time.Second {
		t.Fatal("parser did not stop within bounded cleanup")
	}
	for _, stream := range []string{"stdout", "stderr"} {
		size := maxJSONBytes + 1
		if stream == "stderr" {
			size = 4097
		}
		script := fmt.Sprintf("import sys; sys.%s.write('x' * %d)", stream, size)
		if _, err := runParser(context.Background(), python, []string{"-I", "-S", "-c", script}, nil); err == nil {
			t.Fatalf("%s bound lost", stream)
		}
	}
	for _, raw := range []string{`not json`, `{"files":[]}`, `{"files":[{"path":"foreign.py"}]}`} {
		if _, err := decodeParser([]byte(raw), []parserInput{{Path: "selected.py"}}, "AST"); err == nil {
			t.Fatal("invalid or foreign parser output accepted")
		}
	}
}
