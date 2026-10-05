package codeindex

import (
	"context"
	"fmt"
	"github.com/steveyegge/beads/memoryops"
	"testing"
)

type selectedStore struct {
	publicationStore
	reads []string
}

func (s *selectedStore) Recall(_ context.Context, req memoryops.RecallRequest) (memoryops.RecallResult, error) {
	s.reads = append(s.reads, req.Key)
	value, found := s.plane[req.Key]
	return memoryops.RecallResult{Key: req.Key, Value: value, Found: found}, nil
}

func selectedFixture(t testing.TB, count int) *selectedStore {
	t.Helper()
	index := Index{Version: 3, Languages: []string{"go"}, Parser: manifestParser([]string{"go"}, 3), Roots: []string{"src"}}
	for i := 0; i < count; i++ {
		path := fmt.Sprintf("src/f%04d.go", i)
		index.Files = append(index.Files, File{Path: path, Language: "go", Parser: parserHash("go"), SHA256: digest([]byte("package fixture\n")), Symbols: []Symbol{{ID: path + "::Solved", Name: "Solved", Kind: "function"}}})
	}
	store := &selectedStore{publicationStore: publicationStore{plane: map[string]string{}}}
	if _, err := Save(context.Background(), store, store.plane, index); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSelectedIndexReadsBoundedRoutingAndOnlyLinkedSource(t *testing.T) {
	store := selectedFixture(t, MaxFiles)
	root := t.TempDir()
	source(t, root, "src/f0000.go", "package fixture\n")
	reader, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	// Missing unrelated blobs and 100k retained generations cannot affect this cut.
	for key := range store.plane {
		if key == manifestKey {
			continue
		}
		var file File
		if unpack(store.plane[key], &file) == nil && file.Path != "src/f0000.go" {
			delete(store.plane, key)
		}
	}
	for i := 0; i < 101281; i++ {
		store.plane[Prefix+fmt.Sprintf("blob/retained-%d", i)] = "old generation"
	}
	index, err := LoadSelected(context.Background(), store, []string{"src/f0000.go"})
	if err != nil || len(index.Files) != 1 {
		t.Fatalf("selected load: %+v %v", index, err)
	}
	if len(store.reads) != 34 {
		t.Fatalf("read %d keys, want manifest + 32 parts + 1 file", len(store.reads))
	}
	index = reader.RefreshSelected(index)
	if len(index.Warnings) != 0 || index.Files[0].Validity != "current" {
		t.Fatalf("unrelated discovery leaked: %+v", index.Warnings)
	}
	source(t, root, "DOX.md", "# Introduced contract\n")
	if got := reader.RefreshSelected(index); got.Files[0].Validity != "needs_review" {
		t.Fatal("introduced DOX accepted")
	}
	source(t, root, "src/f0000.go", "package changed\n")
	if got := reader.RefreshSelected(index); got.Files[0].Validity != "needs_review" {
		t.Fatal("changed source accepted")
	}
}

func TestSelectedIndexRejectsCorruptRoutingAndSelectedProvenance(t *testing.T) {
	cases := []string{"part", "blob", "duplicate", "path", "language", "parser"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			store := selectedFixture(t, 2)
			var entry manifest
			_ = unpack(store.plane[manifestKey], &entry)
			var refs []reference
			_ = unpack(store.plane[Prefix+"blob/"+entry.Parts[0]], &refs)
			switch name {
			case "part":
				delete(store.plane, Prefix+"blob/"+entry.Parts[0])
			case "blob":
				delete(store.plane, Prefix+"blob/"+refs[0].Blob)
			case "duplicate", "path":
				if name == "duplicate" {
					refs[1].Path = refs[0].Path
				} else {
					refs[1].Path = "../escape.go"
				}
				encoded, _ := pack(refs)
				key := digest([]byte(encoded))
				store.plane[Prefix+"blob/"+key] = encoded
				entry.Parts = []string{key}
				store.plane[manifestKey], _ = pack(entry)
			case "language", "parser":
				var file File
				_ = unpack(store.plane[Prefix+"blob/"+refs[0].Blob], &file)
				if name == "language" {
					file.Language = "python"
				} else {
					file.Parser = "obsolete"
				}
				encoded, _ := pack(file)
				key := digest([]byte(encoded))
				store.plane[Prefix+"blob/"+key] = encoded
				refs[0].Blob = key
				encoded, _ = pack(refs)
				key = digest([]byte(encoded))
				store.plane[Prefix+"blob/"+key] = encoded
				entry.Parts = []string{key}
				store.plane[manifestKey], _ = pack(entry)
			}
			index, err := LoadSelected(context.Background(), store, []string{"src/f0000.go"})
			if name == "parser" {
				root := t.TempDir()
				source(t, root, "src/f0000.go", "package fixture\n")
				reader, e := Open(root)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = reader.Close() }()
				if err != nil || reader.RefreshSelected(index).Files[0].Validity != "needs_review" {
					t.Fatal("parser drift accepted")
				}
			} else if err == nil {
				t.Fatalf("%s accepted", name)
			}
		})
	}
}

func BenchmarkSelectedIndexLoad(b *testing.B) {
	store := selectedFixture(b, MaxFiles)
	for _, selected := range []bool{false, true} {
		name := "whole"
		if selected {
			name = "one-linked-file"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var err error
				if selected {
					store.reads = nil
					_, err = LoadSelected(context.Background(), store, []string{"src/f0000.go"})
				} else {
					_, err = Load(store.plane)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestSelectedParserIgnoresUnrelatedLanguageDrift(t *testing.T) {
	store := selectedFixture(t, 1)
	root := t.TempDir()
	source(t, root, "src/f0000.go", "package fixture\n")
	reader, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	index, err := LoadSelected(context.Background(), store, []string{"src/f0000.go"})
	if err != nil {
		t.Fatal(err)
	}
	// The aggregate includes another language; the linked Go fingerprint is current.
	index.Languages = []string{"go", "python"}
	index.Parser = "older aggregate fingerprint"
	got := reader.RefreshSelected(index)
	if got.Files[0].Validity != "current" || len(got.Warnings) != 0 {
		t.Fatal("unrelated parser invalidated current Go symbol")
	}
}
