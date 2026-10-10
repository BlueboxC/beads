package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/bdpwire"
	"github.com/steveyegge/beads/internal/httpapi/graphread"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// These pure tests cover route compilation, error shaping and assembly from an
// already-captured inventory. They do not claim a real store or CLI HTTP demo.
func graphRouteFixture(t *testing.T) *GraphRead {
	t.Helper()
	const scope = "https://canonical.example/team/"
	options := graphread.DefaultPaginationOptions(scope)
	options.MaxSnapshotBytes = graphReadSnapshotLimit
	pager, err := graphread.NewPagination(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pager.Close)
	return &GraphRead{scope: scope, path: "/team/", generation: "test-generation", view: "test-full-view", pager: pager, limits: graphread.SelectorLimits{Bytes: 16384, Depth: 256, Nodes: 2048}}
}
func TestGraphReadRouteCompilation(t *testing.T) {
	g := graphRouteFixture(t)
	for _, tc := range []struct{ target, kind, code string }{
		{"/team/", "scope", ""}, {"/team/bdp.json", "discovery", ""},
		{"/team/beads/", "selection", ""}, {"/team/links/", "selection", ""}, {"/team/types/", "selection", ""},
		{"/team/beads/deep/plan", "resource", ""}, {"/team/beads/%C3%A9", "resource", ""},
		{"/team/links/a", "resource", ""}, {"/team/types/preview-memory-v2", "type", ""},
		{"/team/beads/a?view=properties", "properties", ""}, {"/team/links/a?view=properties", "properties", ""},
		{"/team/beads/a?view=links&direction=inbound&limit=1", "selection", ""},
		{"/team/beads/a?include=links&direction=outbound&limit=1", "aggregate", ""},
		{"/team/beads/?type=https%3A%2F%2Fcanonical.example%2Fteam%2Ftypes%2Fpreview-memory-v2&selector=%24%5B%3F%40.id%5D", "selection", ""},
		{"/team/beads/?cursor=opaque&limit=1", "selection", ""},
		{"/team/beads/?limit=1001", "", "limit-exceeded"},
		{"/team/beads/?limit=0", "", "invalid-parameter"}, {"/team/beads/?limit=01", "", "invalid-parameter"},
		{"/team/beads/?limit=1&limit=2", "", "invalid-parameter"},
		{"/team/beads/?source=beads%2Fa", "", "invalid-parameter"},
		{"/team/types/?selector=x", "", "invalid-parameter"},
		{"/team/beads/?include=links", "", "invalid-parameter"},
		{"/team/beads/?selector=%24%5B%3F%40..id%5D", "", "invalid-parameter"},
		{"/team/beads/a?include=links&cursor=x", "", "invalid-parameter"},
		{"/team/beads/a?include=links&view=links", "", "invalid-parameter"},
		{"/team/beads/a?include=links&include=links", "", "invalid-parameter"},
		{"/team/beads/a?include=other", "", "invalid-parameter"},
		{"/team/beads/a?view=properties&limit=1", "", "invalid-parameter"},
		{"/team/beads/a?view=links&direction=sideways", "", "invalid-parameter"},
		{"/team/links/a?view=links", "", "invalid-parameter"}, {"/team/links/a?include=links", "", "invalid-parameter"},
		{"/team/types/a?view=properties", "", "invalid-parameter"},
		{"/team/beads/a?revision=old", "", "invalid-parameter"},
		{"/team/beads/a?view=versions", "", "invalid-parameter"},
		{"/team/beads/a?view=events", "", "invalid-parameter"},
		{"/team/bdp.json?x=1", "", "invalid-parameter"}, {"/team/?x=1", "", "invalid-parameter"},
		{"/team/alias/latest?x=1", "", "resource-not-found"},
		{"/team/alias/latest?bad=%zz", "", "resource-not-found"},
		{"/team/operations/create-bead", "", "resource-not-found"},
		{"/team/beads/a/", "", "resource-not-found"}, {"/team/beads//a", "", "resource-not-found"},
		{"/team/beads/../a", "", "resource-not-found"}, {"/team/beads/%2e%2e/a", "", "resource-not-found"},
		{"/team/beads/%61", "", "resource-not-found"}, {"/team/beads/a%2Fb", "", "resource-not-found"},
		{"/team/beads/a%5Cb", "", "resource-not-found"}, {"/team/beads/caf%C3%a9", "", "resource-not-found"},
		{"/team/beads/café", "", "resource-not-found"}, {"/team/beads/a?bad=%zz", "", "invalid-parameter"},
		{"/team/beads/a?bad=%FF", "", "invalid-parameter"}, {"/team/beads/a?bad=x;y", "", "invalid-parameter"},
		{"/team2/beads/a", "", "resource-not-found"}, {"/v0/beads/issues", "", "resource-not-found"},
		{"//canonical.example/team/beads/a", "", "resource-not-found"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.target, nil)
			path, params, code := g.requestTarget(r)
			var route graphReadRoute
			if code == "" {
				var err error
				route, err = g.compileRoute(path, params)
				if err != nil {
					code = graphReadFailureCode(err)
				}
			}
			if string(code) != tc.code || code == "" && route.kind != tc.kind {
				t.Fatalf("route=%+v code=%s; want %s %s", route, code, tc.kind, tc.code)
			}
		})
	}
}

func TestGraphReadPersistedScopeAndTargetBound(t *testing.T) {
	g := graphRouteFixture(t)
	r := httptest.NewRequest("GET", "http://transport-alias.invalid/team/beads/a", nil)
	r.Header.Set("Forwarded", "host=attacker.example;proto=https")
	r.Header.Set("X-Forwarded-Host", "attacker.example")
	path, params, code := g.requestTarget(r)
	if code != "" {
		t.Fatal(code)
	}
	route, err := g.compileRoute(path, params)
	if err != nil || route.path != "beads/a" || g.scope != "https://canonical.example/team/" {
		t.Fatal(route, err, g.scope)
	}
	r = httptest.NewRequest("GET", "/team/beads/?selector="+strings.Repeat("x", graphReadTargetLimit), nil)
	if _, _, code := g.requestTarget(r); code != bdpwire.CodeRequestTooLarge {
		t.Fatal(code)
	}
	r = httptest.NewRequest("GET", "/team/beads/a", nil)
	r.RequestURI = "/team/beads/a#fragment"
	if _, _, code := g.requestTarget(r); code != bdpwire.CodeResourceNotFound {
		t.Fatal(code)
	}
}

func TestGraphReadAggregateUsesCapturedInventory(t *testing.T) {
	g := graphRouteFixture(t)
	beadType, linkType := g.scope+"types/memory", g.scope+"types/related"
	bead := bdpwire.BeadRecord{ID: g.scope + "beads/plan", Type: beadType, Revision: "bead-before", Properties: bdpwire.Properties{"title": json.RawMessage(`"before"`)}}
	links := []bdpwire.LinkRecord{
		{ID: g.scope + "links/a", Type: linkType, Revision: "link-before-a", Source: bdpwire.Reference{URI: bead.ID}, Target: bdpwire.Reference{URI: g.scope + "beads/other"}, Properties: bdpwire.Properties{}},
		{ID: g.scope + "links/b", Type: linkType, Revision: "link-before-b", Source: bdpwire.Reference{URI: g.scope + "beads/other"}, Target: bdpwire.Reference{URI: bead.ID}, Properties: bdpwire.Properties{}},
	}
	inventory := graphread.Inventory{WriterToken: "never-wire", Beads: []bdpwire.BeadRecord{bead}, Links: links, Types: []bdpwire.TypeDescriptor{{ID: beadType, Name: "Memory", Describes: bdpwire.DescribesBead}, {ID: linkType, Name: "Related", Describes: bdpwire.DescribesLink}}}
	route, err := g.compileRoute("beads/plan", url.Values{"include": {"links"}, "limit": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := g.aggregateInventory("beads/plan", route.selection, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Revision != "bead-before" || aggregate.Links == nil || len(aggregate.Links.Items) != 1 || aggregate.Links.Items[0].Revision != "link-before-a" || aggregate.Links.Next == nil {
		t.Fatalf("%+v", aggregate)
	}
	if inventory.Beads[0].Links != nil {
		t.Fatal("aggregate mutated captured Bead")
	}
	next, err := url.Parse(*aggregate.Links.Next)
	if err != nil {
		t.Fatal(err)
	}
	if next.Scheme+"://"+next.Host+next.Path != bead.ID || next.Query().Get("view") != "links" || next.Query().Has("include") {
		t.Fatal(next)
	}
	// Later caller mutations cannot change the retained snapshot bytes.
	inventory.Links[1].Revision = "link-after-b"
	inventory.Beads[0].Revision = "bead-after"
	continuation, err := g.compileRoute("beads/plan", next.Query())
	if err != nil {
		t.Fatal(err)
	}
	page, err := g.collectionPage(context.Background(), continuation.selection)
	if err != nil {
		t.Fatal(err)
	}
	var last bdpwire.LinkRecord
	if len(page.Items) != 1 || page.Next != nil {
		t.Fatalf("%+v", page)
	}
	if err := bdpwire.Unmarshal(page.Items[0], &last); err != nil {
		t.Fatal(err)
	}
	if last.Revision != "link-before-b" {
		t.Fatal("snapshot refreshed", last)
	}
	// The aggregate deliberately has no optional ETag; wildcard semantics still apply.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/team/beads/plan?include=links", nil)
	r.Header.Set("If-None-Match", "*")
	graphReadJSON(w, r, aggregate, "")
	if w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("ETag") != "" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	if _, err := g.aggregateInventory("beads/missing", route.selection, inventory); !errors.Is(err, graphstore.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestGraphReadProblemsAndInternalFaults(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code bdpwire.ReadProblemCode
	}{
		{graphstore.ErrNotFound, bdpwire.CodeResourceNotFound}, {graphstore.ErrGone, bdpwire.CodeResourceNotFound},
		{graphstore.ErrLimitExceeded, bdpwire.CodeLimitExceeded},
		{&graphread.ParameterError{Name: "cursor"}, bdpwire.CodeInvalidParameter},
		{&graphread.SelectorError{Code: "syntax"}, bdpwire.CodeInvalidParameter},
		{&graphread.SelectorError{Code: "ast-depth-limit-exceeded"}, bdpwire.CodeLimitExceeded},
		{&graphread.PaginationError{Code: "foreign-projection"}, bdpwire.CodeInvalidParameter},
		{&graphread.PaginationError{Code: "foreign-view"}, bdpwire.CodeForeignView},
		{&graphread.PaginationError{Code: "cursor-expired"}, bdpwire.CodeCursorExpired},
		{&graphread.PaginationError{Code: "capacity-exceeded"}, bdpwire.CodeTemporarilyUnavailable},
		{context.DeadlineExceeded, bdpwire.CodeTemporarilyUnavailable},
		{graphstore.ErrInvalidStore, ""}, {errors.New("SQL secret credential must not leak"), ""},
	} {
		t.Run(fmt.Sprint(tc.err), func(t *testing.T) {
			for _, method := range []string{"GET", "HEAD"} {
				r := httptest.NewRequest(method, "/team/beads/a", nil)
				r.Header.Set("Accept", "image/png")
				r.Header.Set("If-None-Match", "*")
				w := httptest.NewRecorder()
				graphReadFailure(w, r, tc.err)
				want := 500
				if tc.code != "" {
					want = tc.code.Status()
				}
				if w.Code != want {
					t.Fatal(w.Code, want)
				}
				if method == "HEAD" || tc.code == "" {
					if w.Body.Len() != 0 {
						t.Fatal("failure body leaked", w.Body.String())
					}
					continue
				}
				var problem bdpwire.ReadProblem
				if err := bdpwire.Unmarshal(w.Body.Bytes(), &problem); err != nil || problem.Code != tc.code {
					t.Fatal(problem, err)
				}
				if strings.Contains(w.Body.String(), "secret") {
					t.Fatal("internal error leaked")
				}
			}
		})
	}
	for _, revision := range []string{"", "line\n", "quote\"", "space ", "\x7f"} {
		if _, err := graphReadETag(revision); err == nil {
			t.Fatal("accepted invalid tag", revision)
		}
	}
	if tag, err := graphReadETag("opaque\\backslash"); err != nil || tag != `"opaque\backslash"` {
		t.Fatal(tag, err)
	}
}

func TestGraphReadRejectsUninitializedSource(t *testing.T) {
	if _, err := NewGraphRead(nil, "https://canonical.example/team/"); err == nil {
		t.Fatal("accepted nil reader")
	}
	if (&GraphRead{}).validate() == nil {
		t.Fatal("accepted zero handler")
	}
	w := httptest.NewRecorder()
	(&GraphRead{}).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestGraphReadPageEnvelopeBound(t *testing.T) {
	// MaxSnapshotBytes counts already-encoded records. Even a maximum-size
	// retained set plus worst-case URL escaping fits the public response bound.
	if graphReadSnapshotLimit+6*graphread.PaginationContextLimit+1000+64 >= graphReadRepresentationLimit {
		t.Fatal("retained snapshots no longer leave enough room for a complete response")
	}
	g := graphRouteFixture(t)
	q, err := graphread.CompileCollection(g.scope, "beads", url.Values{"limit": {"1"}}, g.limits)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"id": g.scope + "beads/large", "properties": strings.Repeat("x", graphReadSnapshotLimit)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.firstPage(q, []json.RawMessage{raw}); graphReadFailureCode(err) != bdpwire.CodeTemporarilyUnavailable {
		t.Fatalf("oversized retained payload error: %v", err)
	}
	bead := bdpwire.BeadRecord{ID: g.scope + "beads/plan", Type: g.scope + "types/memory", Revision: "r", Properties: bdpwire.Properties{"body": json.RawMessage(`"` + strings.Repeat("x", graphReadRepresentationLimit) + `"`)}}
	if err := graphReadAggregateBound(bead, nil, 1); !errors.Is(err, graphstore.ErrLimitExceeded) {
		t.Fatal("oversized Bead admitted", err)
	}
	bead.Properties = bdpwire.Properties{}
	if err := graphReadAggregateBound(bead, []json.RawMessage{json.RawMessage(`{}`)}, 1); err != nil {
		t.Fatal(err)
	}
	// The next URL reserve applies only when this first aggregate has more pages.
	nearLimit := json.RawMessage(`"` + strings.Repeat("x", graphReadRepresentationLimit-1024) + `"`)
	if err := graphReadAggregateBound(bead, []json.RawMessage{nearLimit}, 1); err != nil {
		t.Fatal("final page was charged a next URL", err)
	}
	if err := graphReadAggregateBound(bead, []json.RawMessage{nearLimit, json.RawMessage(`{}`)}, 1); !errors.Is(err, graphstore.ErrLimitExceeded) {
		t.Fatal("continuation overhead not reserved", err)
	}
}
