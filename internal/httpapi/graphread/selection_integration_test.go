//go:build cgo

package graphread

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/bdpwire"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// Called by the real-engine sequence after reopen on embedded and server Dolt.
// No payload/schema is seeded. This is an internal path, not an HTTP demo.
func checkStoredSelectionAndPages(t *testing.T, ctx context.Context, r *Reader) {
	t.Helper()
	scope := r.store.ScopeURL()
	query, err := CompileCollection(scope, "beads", url.Values{
		"conformsTo": {graphstore.MemoryTypeURL(scope)},
		"selector":   {`$[?@.properties.title]`}, "limit": {"1"},
	}, selectionLimits)
	if err != nil {
		t.Fatal(err)
	}
	items, err := r.Select(ctx, query)
	if err != nil || len(items) != 2 {
		t.Fatalf("stored Memory selection: %d %v", len(items), err)
	}
	pager, err := NewPagination(DefaultPaginationOptions(scope))
	if err != nil {
		t.Fatal(err)
	}
	defer pager.Close()
	// Explicit test authority identities. This does not derive a production
	// authorization view or epoch from the store's controlled-writer token.
	first, err := pager.FirstPage(FirstPageInput{Items: items, Limit: query.Limit(),
		AuthorizationView: "test-view", ScopeEpoch: "test-epoch", Projection: query.Projection(), ContinuationURL: query.ContinuationURL()})
	if err != nil || first.Next == nil || len(first.Items) != 1 {
		t.Fatalf("stored first page: %+v %v", first, err)
	}
	checkCollectionWire(t, "beadCollection", first, &bdpwire.BeadCollection{})
	// A real write changes the source Memory's revision and owned Link property.
	if _, err := r.store.UpdateLink(ctx, graphstore.LinkUpdateRequest{Path: "links/z", Unconditional: true, UnconditionalSource: true, Properties: map[string]any{"note": "after first page"}}); err != nil {
		t.Fatal(err)
	}
	current, err := r.Select(ctx, query)
	if err != nil || reflect.DeepEqual(items, current) {
		t.Fatalf("real write did not change current selection: %v", err)
	}
	nextURL, err := url.Parse(*first.Next)
	if err != nil {
		t.Fatal(err)
	}
	nextQuery, err := CompileCollection(scope, "beads", nextURL.Query(), selectionLimits)
	if err != nil {
		t.Fatal(err)
	}
	input := ContinuationInput{Token: nextQuery.Cursor(), AuthorizationView: "test-view", ScopeEpoch: "test-epoch", Projection: nextQuery.Projection()}
	second, err := pager.ContinuePage(input)
	if err != nil || second.Next != nil || !reflect.DeepEqual(second.Items, items[1:]) {
		t.Fatalf("continuation lost original revision/owned state: %+v %v", second, err)
	}
	checkCollectionWire(t, "beadCollection", second, &bdpwire.BeadCollection{})
	replay, err := pager.ContinuePage(input)
	if err != nil || !reflect.DeepEqual(replay, second) {
		t.Fatalf("replay changed: %v", err)
	}
	for _, collection := range []string{"links", "types"} {
		parameters := url.Values{}
		if collection == "links" {
			parameters.Set("endpoint", "beads/plan")
			parameters.Set("selector", `$[?@.properties.note == "after first page"]`)
		}
		selection, err := CompileCollection(scope, collection, parameters, selectionLimits)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := r.Select(ctx, selection)
		if err != nil {
			t.Fatal(err)
		}
		page, err := pager.FirstPage(FirstPageInput{Items: rows, AuthorizationView: "test-view", ScopeEpoch: "test-epoch", Projection: selection.Projection(), ContinuationURL: selection.ContinuationURL()})
		if err != nil || page.Next != nil {
			t.Fatalf("%s page: %v", collection, err)
		}
		if collection == "links" {
			if len(rows) != 1 {
				t.Fatal("Link predicates did not compose")
			}
			checkCollectionWire(t, "linkCollection", page, &bdpwire.LinkCollection{})
		} else {
			if len(rows) != 6 {
				t.Fatal("installed Type inventory incomplete")
			}
			checkCollectionWire(t, "typesInventory", page, &bdpwire.TypesInventory{})
		}
	}
	incident, err := CompileIncident(scope, "beads/plan", url.Values{"direction": {"outbound"}}, selectionLimits)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := r.Select(ctx, incident); err != nil || len(rows) != 1 {
		t.Fatalf("stored incident view: %d %v", len(rows), err)
	}
}

func checkCollectionWire(t *testing.T, kind string, page Page, output any) {
	t.Helper()
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if err := bdpwire.Unmarshal(raw, output); err != nil {
		t.Fatal(err)
	}
	receipt, _ := json.Marshal(map[string]any{"kind": kind, "value": page})
	t.Logf("WIRE_RECEIPT %s", receipt)
}
