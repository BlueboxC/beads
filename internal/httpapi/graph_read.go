package httpapi

// BDP Read routing follows gastownhall/bdp docs/specs/bdp.md and
// packages/server/src/read-request.ts at
// 5b4eb46c7f21f2272f4da5a230f042ce753cb304. The graph workspace uses the existing
// server's Host, authentication and request admission controls, not its legacy
// Issue HTTP routes. This handler owns no store and never performs a mutation.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"

	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/httpapi/bdpwire"
	"github.com/steveyegge/beads/internal/httpapi/graphread"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

const (
	graphReadTargetLimit         = 32 << 10
	graphReadRepresentationLimit = 8 << 20
	graphReadSnapshotLimit       = 7 << 20
)

// GraphRead serves one persisted, full-workspace BDP Read projection. Its caller
// MUST authenticate and authorize each request before calling ServeHTTP. It is
// not a standalone listener and is not safe to mount without those controls.
// All accepted tokens share the workspace view under the existing serve policy.
// History, aliases, mutation, hot restore and embedded serving are not admitted.
type GraphRead struct {
	reader                        *graphread.Reader
	scope, path, generation, view string
	pager                         *graphread.Pagination
	limits                        graphread.SelectorLimits
	discovery                     bdpwire.ReadDiscovery
	closed                        atomic.Bool
}

// NewGraphRead constructs process-local continuation state. Startup and requests
// still require a current ValidateAuthority check; construction does not open or
// initialize storage. A new process cannot resume another process's cursors.
func NewGraphRead(reader *graphread.Reader, scope string) (*GraphRead, error) {
	if reader == nil || reader.ScopeURL() != scope || graph.ValidateScopeURL(scope) != nil {
		return nil, errors.New("graph Read requires its reader's canonical persisted Scope")
	}
	parsed, err := url.Parse(scope)
	if err != nil {
		return nil, err
	}
	options := graphread.DefaultPaginationOptions(scope)
	// Leave one MiB for every page's envelope, separators and continuation URL
	// (at most PaginationContextLimit bytes, even if JSON escaping expands each
	// byte sixfold). Thus no already-issued page can outgrow the response cap.
	options.MaxSnapshotBytes = graphReadSnapshotLimit
	pager, err := graphread.NewPagination(options)
	if err != nil {
		return nil, err
	}
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		pager.Close()
		return nil, err
	}
	generation := hex.EncodeToString(entropy[:])
	limits := graphread.SelectorLimits{Bytes: 16384, Depth: 256, Nodes: 2048}
	g := &GraphRead{reader: reader, scope: scope, path: parsed.EscapedPath(), generation: generation, view: "full-workspace:" + generation, pager: pager, limits: limits}
	g.discovery = bdpwire.ReadDiscovery{BDPVersion: bdpwire.BDPVersion, Profile: bdpwire.ProfileRead, Scope: scope, Beads: scope + "beads/", Links: scope + "links/", Types: scope + "types/", Limits: &bdpwire.AdvertisedLimits{
		Page:      &bdpwire.PageLimits{DefaultItems: options.DefaultPageItems, MaximumItems: options.MaxPageItems},
		Request:   &bdpwire.RequestLimits{TargetBytes: graphReadTargetLimit},
		Resource:  &bdpwire.ResourceLimits{RepresentationBytes: graphReadRepresentationLimit},
		Selector:  &bdpwire.SelectorLimits{Bytes: limits.Bytes, Depth: limits.Depth, Nodes: limits.Nodes},
		Retention: &bdpwire.RetentionLimits{MaximumSnapshotLifetime: "PT5M"},
	}}
	return g, nil
}

func (g *GraphRead) validate() error {
	if g == nil || g.reader == nil || g.pager == nil || g.closed.Load() || g.reader.ScopeURL() != g.scope || graph.ValidateScopeURL(g.scope) != nil || g.generation == "" || g.view == "" {
		return errors.New("httpapi: invalid or closed graph Read source")
	}
	return nil
}

// Close releases all process-local cursors after the caller has drained requests.
// It does not close the caller-owned graph store.
func (g *GraphRead) Close() {
	if g != nil && g.pager != nil {
		g.closed.Store(true)
		g.pager.Close()
	}
}

// ServeHTTP handles requests only after the enclosing server's current security
// checks. Persisted identity, query/continuation checks and result validation all
// precede HTTP negotiation and conditional shortcuts.
func (g *GraphRead) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := g.validate(); err != nil {
		graphReadProblem(w, r, bdpwire.CodeTemporarilyUnavailable)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		graphReadResponse(w, r, http.StatusMethodNotAllowed, nil, "", "", http.Header{"Allow": []string{"GET, HEAD"}})
		return
	}
	path, query, code := g.requestTarget(r)
	if code != "" {
		graphReadProblem(w, r, code)
		return
	}
	route, err := g.compileRoute(path, query)
	if err != nil {
		graphReadFailure(w, r, err)
		return
	}
	// This must also run on Scope/discovery and retained pages. A cursor is
	// neither authority nor authentication, and must not keep a failed store live.
	if err := g.reader.ValidateAuthority(r.Context()); err != nil {
		graphReadFailure(w, r, err)
		return
	}
	switch route.kind {
	case "scope":
		graphReadResponse(w, r, http.StatusNoContent, nil, "", "", http.Header{"Link": []string{"<" + g.scope + "bdp.json>; rel=\"service-desc\"; type=\"application/json\""}})
	case "discovery":
		graphReadJSON(w, r, g.discovery, "")
	case "selection":
		page, err := g.collectionPage(r.Context(), route.selection)
		if err != nil {
			graphReadFailure(w, r, err)
			return
		}
		graphReadJSON(w, r, page, "")
	case "aggregate":
		record, err := g.aggregate(r.Context(), route.path, route.selection)
		if err != nil {
			graphReadFailure(w, r, err)
			return
		}
		// No optional aggregate validator: a Bead revision alone does not cover
		// changes to unowned incident Links or this response's continuation token.
		graphReadJSON(w, r, record, "")
	case "type":
		record, err := g.reader.Type(r.Context(), route.path)
		if err != nil {
			graphReadFailure(w, r, err)
			return
		}
		graphReadJSON(w, r, record, "")
	default:
		record, err := g.reader.Resource(r.Context(), route.path)
		if err != nil {
			graphReadFailure(w, r, err)
			return
		}
		var revision string
		var properties bdpwire.Properties
		switch v := record.(type) {
		case bdpwire.BeadRecord:
			revision, properties = v.Revision, v.Properties
		case bdpwire.LinkRecord:
			revision, properties = v.Revision, v.Properties
		default:
			graphReadFailure(w, r, graphstore.ErrInvalidStore)
			return
		}
		tag, err := graphReadETag(revision)
		if err != nil {
			graphReadFailure(w, r, err)
			return
		}
		if route.kind == "properties" {
			graphReadJSON(w, r, properties, tag)
		} else {
			graphReadJSON(w, r, record, tag)
		}
	}
}

func graphReadETag(revision string) (string, error) {
	if revision == "" {
		return "", graphstore.ErrInvalidStore
	}
	for _, c := range []byte(revision) {
		if c < 0x21 || c == 0x22 || c == 0x7f {
			return "", graphstore.ErrInvalidStore
		}
	}
	return "\"" + revision + "\"", nil
}

func graphReadJSON(w http.ResponseWriter, r *http.Request, body any, etag string) {
	raw, err := json.Marshal(body)
	if err != nil {
		graphReadFailure(w, r, err)
		return
	}
	if len(raw) > graphReadRepresentationLimit {
		graphReadProblem(w, r, bdpwire.CodeLimitExceeded)
		return
	}
	graphReadResponse(w, r, http.StatusOK, raw, "application/json", etag, nil)
}

// graphReadProblem is also used by the shared Host/authentication/admission gates.
// Only the closed BDP table decides family, status and retry; no storage details
// or attacker-supplied messages are copied into the response.
func graphReadProblem(w http.ResponseWriter, r *http.Request, code bdpwire.ReadProblemCode) {
	problem := bdpwire.NewReadProblem(code)
	if err := problem.Validate(); err != nil {
		graphReadResponse(w, r, http.StatusInternalServerError, nil, "", "", nil)
		return
	}
	raw, err := json.Marshal(problem)
	if err != nil {
		graphReadResponse(w, r, http.StatusInternalServerError, nil, "", "", nil)
		return
	}
	headers := http.Header{}
	if code == bdpwire.CodeUnauthenticated {
		headers.Set("WWW-Authenticate", `Bearer realm="beads"`)
	}
	graphReadResponse(w, r, code.Status(), raw, "application/problem+json", "", headers)
}

func graphReadFailure(w http.ResponseWriter, r *http.Request, err error) {
	if code := graphReadFailureCode(err); code != "" {
		graphReadProblem(w, r, code)
		return
	}
	graphReadResponse(w, r, http.StatusInternalServerError, nil, "", "", nil)
}

func graphReadFailureCode(err error) bdpwire.ReadProblemCode {
	var parameter *graphread.ParameterError
	var selector *graphread.SelectorError
	var page *graphread.PaginationError
	var network net.Error
	switch {
	case errors.As(err, &parameter):
		return bdpwire.CodeInvalidParameter
	case errors.As(err, &selector):
		if selector.Code == "syntax" || selector.Code == "unsupported-feature" {
			return bdpwire.CodeInvalidParameter
		}
		return bdpwire.CodeLimitExceeded
	case errors.As(err, &page):
		switch page.Code {
		case "cursor-expired":
			return bdpwire.CodeCursorExpired
		case "foreign-view":
			return bdpwire.CodeForeignView
		case "foreign-projection", "invalid-input":
			return bdpwire.CodeInvalidParameter
		case "invalid-limit":
			return bdpwire.CodeLimitExceeded
		case "capacity-exceeded":
			return bdpwire.CodeTemporarilyUnavailable
		case "configuration-error":
			return bdpwire.CodeTemporarilyUnavailable
		}
	case errors.Is(err, graphstore.ErrGone), errors.Is(err, graphstore.ErrNotFound):
		return bdpwire.CodeResourceNotFound
	case errors.Is(err, graphstore.ErrLimitExceeded):
		return bdpwire.CodeLimitExceeded
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, sql.ErrConnDone), errors.As(err, &network):
		return bdpwire.CodeTemporarilyUnavailable
	}
	return ""
}

func (g *GraphRead) collectionPage(ctx context.Context, q *graphread.Selection) (graphread.Page, error) {
	if q.Cursor() != "" {
		return g.pager.ContinuePage(graphread.ContinuationInput{Token: q.Cursor(), AuthorizationView: g.view, ScopeEpoch: g.generation, Projection: q.Projection()})
	}
	records, err := g.reader.Select(ctx, q)
	if err != nil {
		return graphread.Page{}, err
	}
	return g.firstPage(q, records)
}
func (g *GraphRead) firstPage(q *graphread.Selection, records []json.RawMessage) (graphread.Page, error) {
	return g.pager.FirstPage(graphread.FirstPageInput{Items: records, Limit: q.Limit(), AuthorizationView: g.view, ScopeEpoch: g.generation, Projection: q.Projection(), ContinuationURL: q.ContinuationURL()})
}
func (g *GraphRead) aggregate(ctx context.Context, path string, q *graphread.Selection) (bdpwire.BeadRecord, error) {
	inventory, err := g.reader.Inventory(ctx)
	if err != nil {
		return bdpwire.BeadRecord{}, err
	}
	return g.aggregateInventory(path, q, inventory)
}
func (g *GraphRead) aggregateInventory(path string, q *graphread.Selection, inventory graphread.Inventory) (bdpwire.BeadRecord, error) {
	var record *bdpwire.BeadRecord
	for _, bead := range inventory.Beads {
		if bead.ID == g.scope+path {
			copy := bead
			record = &copy
			break
		}
	}
	if record == nil {
		return bdpwire.BeadRecord{}, graphstore.ErrNotFound
	}
	records, err := q.Select(inventory)
	if err != nil {
		return bdpwire.BeadRecord{}, err
	}
	limit, err := g.pager.ValidateLimit(q.Limit())
	if err != nil {
		return bdpwire.BeadRecord{}, err
	}
	if err := graphReadAggregateBound(*record, records, limit); err != nil {
		return bdpwire.BeadRecord{}, err
	}
	page, err := g.firstPage(q, records)
	if err != nil {
		return bdpwire.BeadRecord{}, err
	}
	links := bdpwire.LinkCollection{Items: bdpwire.LinkRecords{}, Next: page.Next}
	for _, raw := range page.Items {
		var link bdpwire.LinkRecord
		if err := bdpwire.Unmarshal(raw, &link); err != nil {
			return bdpwire.BeadRecord{}, fmt.Errorf("%w: invalid projected Link", graphstore.ErrInvalidStore)
		}
		links.Items = append(links.Items, link)
	}
	record.Links = &links
	return *record, nil
}

// Refuse an oversized aggregate before allocating an unreachable continuation.
// This conservative bound includes the first page's complete records, the Bead,
// the envelope, and (only when needed) the worst-case encoded next URL.
func graphReadAggregateBound(record bdpwire.BeadRecord, records []json.RawMessage, limit int) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	size := len(raw) + 64
	for _, item := range records[:min(limit, len(records))] {
		if len(item) > graphReadRepresentationLimit-size {
			return graphstore.ErrLimitExceeded
		}
		size += len(item) + 1
	}
	if len(records) > limit {
		size += 6*graphread.PaginationContextLimit + 2
	}
	if size > graphReadRepresentationLimit {
		return graphstore.ErrLimitExceeded
	}
	return nil
}
