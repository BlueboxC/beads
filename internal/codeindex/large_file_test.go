package codeindex

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func largeFileFixture() Index {
	path := "src/contracts.json"
	file := File{Path: path, Language: "json", Parser: parserHash("json"), SHA256: digest([]byte("fixture"))}
	for i := 0; i < 6000; i++ {
		name := digest([]byte(fmt.Sprintf("field-%d", i)))
		file.Symbols = append(file.Symbols, Symbol{ID: path + "::" + name, Name: name, Kind: "key", Line: i + 1, EndLine: i + 1})
	}
	return Index{Version: 3, Languages: []string{"json"}, Roots: []string{"src"}, Files: []File{file}, Stats: Stats{Files: 1, Symbols: len(file.Symbols)}}
}

func TestLargeFileIndexPersistsThroughSmallRows(t *testing.T) {
	ctx := context.Background()
	index := largeFileFixture()
	store := &selectedStore{publicationStore: publicationStore{plane: map[string]string{"@knowledge/record/solved": "reviewed evidence", "plain": "objective"}}}
	stored, err := Save(ctx, store, store.plane, index)
	if err != nil {
		t.Fatalf("large single-file structure must persist: %v", err)
	}
	for key, value := range store.plane {
		if IsKey(key) && len(value) > maxRowBytes {
			t.Fatalf("oversized TEXT row: %s", key)
		}
	}
	loaded, err := Load(store.plane)
	if err != nil || !reflect.DeepEqual(index.Files, loaded.Files) || loaded.Stats.StoredBytes != stored {
		t.Fatalf("full roundtrip differs: %v", err)
	}
	selected, err := LoadSelected(ctx, store, []string{index.Files[0].Path})
	if err != nil || !reflect.DeepEqual(loaded.Files, selected.Files) {
		t.Fatalf("selected roundtrip differs: %v", err)
	}
	before := store.plane[manifestKey]
	changed := largeFileFixture()
	changed.Files[0].Symbols[0].Name = "changed"
	store.failManifest = true
	if _, err := Save(ctx, store, store.plane, changed); err == nil || store.plane[manifestKey] != before {
		t.Fatal("interrupted publication replaced previous manifest")
	}
	plan, err := PlanPrune(store.plane)
	if err != nil || len(plan.ObsoleteKeys) == 0 {
		t.Fatalf("fragment-aware cleanup plan: %v", err)
	}
	for _, key := range plan.ObsoleteKeys {
		delete(store.plane, key)
	}
	loaded, err = Load(store.plane)
	if err != nil || !reflect.DeepEqual(index.Files, loaded.Files) {
		t.Fatal("prune removed live fragments or interrupted publication changed data")
	}
	if store.plane["@knowledge/record/solved"] != "reviewed evidence" || store.plane["plain"] != "objective" {
		t.Fatal("human evidence changed")
	}
}

func TestLargeFileIndexRejectsMissingOrCorruptFragments(t *testing.T) {
	index := largeFileFixture()
	store := &selectedStore{publicationStore: publicationStore{plane: map[string]string{}}}
	if _, err := Save(context.Background(), store, store.plane, index); err != nil {
		t.Fatal(err)
	}
	var fragmentKey string
	for key, value := range store.plane {
		if strings.HasPrefix(value, "code-fragment-v1:") {
			fragmentKey = key
			break
		}
	}
	if fragmentKey == "" {
		t.Fatal("large structure has no fragments")
	}
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			plane := make(map[string]string, len(store.plane))
			for key, value := range store.plane {
				plane[key] = value
			}
			if missing {
				delete(plane, fragmentKey)
			} else {
				plane[fragmentKey] += "changed"
			}
			if _, err := Load(plane); err == nil {
				t.Fatal("incomplete structure accepted")
			}
			broken := &selectedStore{publicationStore: publicationStore{plane: plane}}
			if _, err := LoadSelected(context.Background(), broken, []string{index.Files[0].Path}); err == nil {
				t.Fatal("selected incomplete structure accepted")
			}
			if _, err := PlanPrune(plane); err == nil {
				t.Fatal("incomplete structure admitted cleanup")
			}
		})
	}
}

func TestLargeFileIndexRejectsInvalidDescriptor(t *testing.T) {
	blobs := map[string]string{}
	value, _, err := packFile(largeFileFixture().Files[0], blobs)
	if err != nil {
		t.Fatal(err)
	}
	var original fileFragments
	if err := unpack(value, &original); err != nil {
		t.Fatal(err)
	}
	read := func(key string) (string, bool, error) { value, found := blobs[key]; return value, found, nil }
	cases := map[string]func(*fileFragments){
		"no parts":                  func(d *fileFragments) { d.Parts = []string{} },
		"too many parts":            func(d *fileFragments) { d.Parts = make([]string, maxFileParts+1) },
		"negative size":             func(d *fileFragments) { d.EncodedBytes = -1 },
		"unbounded size":            func(d *fileFragments) { d.EncodedBytes = maxPackedFile + 1 },
		"short declared size":       func(d *fileFragments) { d.EncodedBytes-- },
		"long declared size":        func(d *fileFragments) { d.EncodedBytes++ },
		"missing aggregate hash":    func(d *fileFragments) { d.EncodedSHA = "" },
		"reordered valid fragments": func(d *fileFragments) { d.Parts[0], d.Parts[1] = d.Parts[1], d.Parts[0] },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			descriptor := original
			descriptor.Parts = append([]string{}, original.Parts...)
			change(&descriptor)
			value, err := pack(descriptor)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := readFile(value, read); err == nil {
				t.Fatal("invalid fragment descriptor accepted")
			}
		})
	}
}

func TestLargeFileIndexRetainsDecodedSizeBound(t *testing.T) {
	// A valid compressed/hash envelope cannot bypass the decoded-size limit.
	payload, err := json.Marshal(struct {
		File
		Padding string `json:"padding"`
	}{
		File: largeFileFixture().Files[0], Padding: strings.Repeat("x", maxJSONBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	encoded := "code-v1:" + base64.StdEncoding.EncodeToString(compressed.Bytes())
	if len(encoded) <= maxRowBytes {
		t.Fatal("fixture must use fragmented storage")
	}
	descriptor := fileFragments{EncodedBytes: len(encoded), EncodedSHA: digest([]byte(encoded))}
	blobs := map[string]string{}
	for start := 0; start < len(encoded); start += fragmentBytes {
		value := fragmentPrefix + encoded[start:min(start+fragmentBytes, len(encoded))]
		key := digest([]byte(value))
		blobs[Prefix+"blob/"+key] = value
		descriptor.Parts = append(descriptor.Parts, key)
	}
	value, err := pack(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	read := func(key string) (string, bool, error) { value, found := blobs[key]; return value, found, nil }
	if _, _, err := readFile(value, read); err == nil || !strings.Contains(err.Error(), "expands beyond 8 MiB") {
		t.Fatalf("oversized decoded structure accepted: %v", err)
	}
}

func TestXMLDenseAttributesReportASTLimit(t *testing.T) {
	code := "<svg>" + strings.Repeat(`<path fill="omitted" stroke="omitted" d="omitted"/>`, 20000) + "</svg>"
	got := parseXML(parserInput{"drawing.svg", code})
	if got.ParseError != "ASTLimit" || len(got.Symbols) != 0 {
		t.Fatalf("dense XML should report its structure limit without partial symbols: error=%s symbols=%d", got.ParseError, len(got.Symbols))
	}
}
