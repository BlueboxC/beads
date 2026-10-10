package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

const discoveryScope = "http://localhost:9999/example/"

func discoveryMemory(path, title, body string) graphstore.Record {
	return graphstore.Record{ID: discoveryScope + "beads/" + path, Type: graphstore.MemoryTypeURL(discoveryScope), Version: "saved-" + path, Properties: graphstore.Properties{Title: title, Body: body}, Attribution: graphstore.Attribution{Status: "unknown", RecordedAt: "2026-09-27T00:00:00Z"}, Owned: []json.RawMessage{json.RawMessage(`{"properties":{"secret":"do-not-project-owned"}}`)}}
}

func TestGraphPreviewMemoryDiscoveryProjection(t *testing.T) {
	title := discoveryMemory("title", "Needle in title", "do-not-project-body")
	body := discoveryMemory("body", "Body hit", "A NEEDLE in body")
	both := discoveryMemory("both", "needle", "needle\nnext line\tcontrol\x00")
	empty := discoveryMemory("empty", "Empty", "")
	other := discoveryMemory("other", "needle", "needle")
	other.Type = graphstore.IssueTypeURL(discoveryScope)
	snapshot := graphstore.Snapshot{Records: []any{title, body, both, empty, other, graphstore.IssueRecord{ID: "issue"}, graphstore.LinkRecord{ID: "link"}}}
	before, _ := json.Marshal(snapshot)
	got, err := graphMemoryDiscovery(discoveryScope, snapshot, "needle", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Projection != "summary" || got.Scope != discoveryScope || !got.Complete || got.Next != nil || len(got.Items) != 3 {
		t.Fatalf("wrong result: %+v", got)
	}
	for i, want := range []graphstore.Record{body, both, title} {
		item := got.Items[i]
		if item.ID != want.ID || item.Type != want.Type || item.Title != want.Properties.Title || item.Version != want.Version || item.Attribution != want.Attribution || item.Details == nil || item.Details.OwnedLinkCount != 1 {
			t.Fatalf("wrong summary: %+v", item)
		}
	}
	if !reflect.DeepEqual(got.Items[0].MatchedFields, []string{"body"}) || !reflect.DeepEqual(got.Items[1].MatchedFields, []string{"title", "body"}) || !reflect.DeepEqual(got.Items[2].MatchedFields, []string{"title"}) {
		t.Fatalf("field provenance: %+v", got.Items)
	}
	if got.Items[2].Excerpt != nil || got.Items[1].Excerpt.Text != both.Properties.Body || got.Items[1].Excerpt.Truncated {
		t.Fatalf("excerpt: %+v", got.Items)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "do-not-project") || strings.Contains(string(raw), `"owned":`) || strings.Contains(string(raw), `"properties":`) || strings.Contains(string(raw), `"metadata":`) {
		t.Fatalf("payload leaked: %s", raw)
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("mutated source snapshot")
	}
	listed, err := graphMemoryDiscovery(discoveryScope, snapshot, "", false, false)
	if err != nil || len(listed.Items) != 4 {
		t.Fatalf("queryless: %+v %v", listed, err)
	}
	for _, item := range listed.Items {
		if item.Excerpt != nil || item.Details != nil || len(item.MatchedFields) != 0 {
			t.Fatalf("queryless extra exposure: %+v", item)
		}
	}
	for _, input := range []graphstore.Snapshot{{}, snapshot} {
		result, err := graphMemoryDiscovery(discoveryScope, input, "not present", false, false)
		if err != nil || result.Items == nil || len(result.Items) != 0 || !result.Complete || result.Next != nil {
			t.Fatalf("empty: %+v %v", result, err)
		}
	}
}

func TestGraphPreviewMemoryDiscoveryUnicodeLiteral(t *testing.T) {
	for _, tc := range []struct {
		name, title, body, query string
		fields                   []string
	}{
		{"sharp-s", "Straße", "Straße", "STRASSE", []string{"title", "body"}},
		{"sigma", "ΟΣ", "ος σ Σ", "οσ", []string{"title", "body"}},
		{"expanded-rune", "", "İstanbul", "\u0307s", []string{"body"}},
		{"supplementary", "𐐀", "😀𐐀雪", "𐐨", []string{"title", "body"}},
		{"combining", "", "cafe\u0301", "fé", nil},
		{"exact-combining", "", "cafe\u0301", "e\u0301", []string{"body"}},
		{"literal-regex", "", "a.*[x]?", ".*[", []string{"body"}},
		{"no-trim", "", " needle ", " needle ", []string{"body"}},
		{"space-not-empty", "x", "x", " ", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memory := discoveryMemory("one", tc.title, tc.body)
			got, err := graphMemoryDiscovery(discoveryScope, graphstore.Snapshot{Records: []any{memory}}, tc.query, false, false)
			if err != nil {
				t.Fatal(err)
			}
			if tc.fields == nil {
				if len(got.Items) != 0 {
					t.Fatalf("unexpected match: %+v", got)
				}
				return
			}
			if len(got.Items) != 1 || !reflect.DeepEqual(got.Items[0].MatchedFields, tc.fields) {
				t.Fatalf("wrong matches: %+v", got)
			}
			if got.Items[0].Excerpt != nil && got.Items[0].Excerpt.Text != tc.body {
				t.Fatalf("original Unicode lost: %+v", got.Items[0].Excerpt)
			}
		})
	}
}

func TestGraphPreviewMemoryDiscoveryExcerptOffsets(t *testing.T) {
	for _, tc := range []struct {
		name, body, query, want string
		truncated               bool
	}{
		{"bounded", strings.Repeat("雪", 160), "雪", strings.Repeat("雪", 160), false},
		{"tail-cut", strings.Repeat("雪", 161), "雪", strings.Repeat("雪", 159) + "…", true},
		{"fold-expanded-prefix", strings.Repeat("ß", 200) + "MARK" + strings.Repeat("雪", 200), "mark", "…" + strings.Repeat("ß", 40) + "MARK" + strings.Repeat("雪", 114) + "…", true},
		{"match-inside-expansion", strings.Repeat("a", 200) + "İstanbul" + strings.Repeat("雪", 200), "\u0307s", "…" + strings.Repeat("a", 40) + "İstanbul" + strings.Repeat("雪", 110) + "…", true},
		{"end-match", strings.Repeat("a", 200) + "😀END", "end", "…" + strings.Repeat("a", 39) + "😀END", true},
		{"long-query", strings.Repeat("x", 200), strings.Repeat("x", 180), strings.Repeat("x", 159) + "…", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := graphMemoryDiscovery(discoveryScope, graphstore.Snapshot{Records: []any{discoveryMemory("one", "", tc.body)}}, tc.query, false, false)
			if err != nil || len(got.Items) != 1 {
				t.Fatalf("match: %+v %v", got, err)
			}
			excerpt := got.Items[0].Excerpt
			if excerpt == nil || excerpt.Field != "body" || excerpt.Text != tc.want || excerpt.Truncated != tc.truncated || utf8.RuneCountInString(excerpt.Text) > 160 || !utf8.ValidString(excerpt.Text) {
				t.Fatalf("excerpt: %+v want %q", excerpt, tc.want)
			}
		})
	}
}

func TestGraphPreviewMemoryDiscoveryOrderAndLimits(t *testing.T) {
	// Supplementary U+10000 sorts before BMP U+E000 in canonical UTF-16 order.
	snapshot := graphstore.Snapshot{Records: []any{discoveryMemory("\ue000", "", ""), discoveryMemory("𐀀", "", "")}}
	got, err := graphMemoryDiscovery(discoveryScope, snapshot, "", false, false)
	if err != nil || got.Items[0].ID != discoveryScope+"beads/𐀀" {
		t.Fatalf("order: %+v %v", got, err)
	}
	snapshot.Records = nil
	for i := range 51 {
		snapshot.Records = append(snapshot.Records, discoveryMemory(fmt.Sprintf("%02d", i), "title", "body"))
	}
	if got, err := graphMemoryDiscovery(discoveryScope, graphstore.Snapshot{Records: snapshot.Records[:50]}, "", false, false); err != nil || len(got.Items) != 50 {
		t.Fatalf("50: %+v %v", got, err)
	}
	if got, err := graphMemoryDiscovery(discoveryScope, snapshot, "", false, false); !errors.Is(err, graphstore.ErrLimitExceeded) || !strings.Contains(err.Error(), "--all") || !reflect.DeepEqual(got, graphMemoryDiscoveryResult{}) {
		t.Fatalf("51: %+v %v", got, err)
	}
	if got, err := graphMemoryDiscovery(discoveryScope, snapshot, "", true, false); err != nil || len(got.Items) != 51 || !got.Complete {
		t.Fatalf("all: %+v %v", got, err)
	}
	for _, query := range []string{string([]byte{255}), strings.Repeat("x", 4097), strings.Repeat("雪", 1366)} {
		if got, err := graphMemoryDiscovery(discoveryScope, snapshot, query, true, false); !errors.Is(err, storage.ErrValidation) || !reflect.DeepEqual(got, graphMemoryDiscoveryResult{}) {
			t.Fatalf("invalid query: %+v %v", got, err)
		}
	}
	if _, err := graphMemoryDiscovery(discoveryScope, snapshot, strings.Repeat("x", 4096), true, false); err != nil {
		t.Fatalf("4096 query: %v", err)
	}
}

func TestGraphPreviewMemoryDiscoveryByteBudget(t *testing.T) {
	memory := discoveryMemory("large", "", "")
	snapshot := graphstore.Snapshot{Records: []any{memory}}
	// Derive exact compact envelope overhead, then straddle the limit by one
	// byte without coupling the assertion to a particular field count.
	result, err := graphMemoryDiscovery(discoveryScope, snapshot, "", true, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "preview": true, "result": result})
	if err != nil {
		t.Fatal(err)
	}
	memory.Properties.Title = strings.Repeat("x", graphMemoryDiscoveryOutputLimit-len(raw)-1)
	snapshot.Records[0] = memory
	if _, err := graphMemoryDiscovery(discoveryScope, snapshot, "", true, false); err != nil {
		t.Fatalf("exact byte boundary: %v", err)
	}
	memory.Properties.Title += "x"
	snapshot.Records[0] = memory
	if got, err := graphMemoryDiscovery(discoveryScope, snapshot, "", true, false); !errors.Is(err, graphstore.ErrLimitExceeded) || !reflect.DeepEqual(got, graphMemoryDiscoveryResult{}) {
		t.Fatalf("over budget: %+v %v", got, err)
	}
	// JSON escaping contributes bytes: rune counts alone cannot admit output.
	memory.Properties.Title = strings.Repeat("\x00", graphMemoryDiscoveryOutputLimit/6)
	snapshot.Records[0] = memory
	if _, err := graphMemoryDiscovery(discoveryScope, snapshot, "", true, false); !errors.Is(err, graphstore.ErrLimitExceeded) {
		t.Fatalf("escaped budget: %v", err)
	}
}
