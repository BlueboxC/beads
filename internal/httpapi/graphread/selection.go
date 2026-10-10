package graphread

// Collection semantics follow gastownhall/bdp at
// 5b4eb46c7f21f2272f4da5a230f042ce753cb304, docs/specs/bdp.md and
// packages/server/src/{read-request,authority-read}.ts. These are internal
// mechanics, not an authorization policy or an advertised Read profile.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/httpapi/bdpwire"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// ParameterError is mapped by a future HTTP adapter to invalid-parameter.
// Selector failures retain their more specific classified error.
type ParameterError struct{ Name string }

func (e *ParameterError) Error() string { return "invalid collection parameter: " + e.Name }

// Selection is a validated request over one complete, already authorized
// inventory. Its private fields prevent callers from bypassing query validation.
// It owns no storage, cursors, authorization view or Scope epoch.
type Selection struct {
	scope, collection, typ, conformsTo, source, target, endpoint string
	bead, direction, cursor, continuationURL                     string
	limit                                                        int
	selector                                                     *Selector
}

// CompileCollection rejects unknown and repeated parameters, including on a
// continuation. It preserves exact Selector literals and external URI spelling.
func CompileCollection(scope, collection string, parameters url.Values, limits SelectorLimits) (*Selection, error) {
	if collection != "beads" && collection != "links" && collection != "types" {
		return nil, &ParameterError{Name: "collection"}
	}
	return compileSelection(scope, collection, "", parameters, limits)
}

// CompileIncident selects the Link view of a canonical local Bead. The view
// parameter is optional here (the caller has already selected this view); when
// supplied it must equal links. Unlike collections, incident views accept only
// direction, limit and cursor controls, as well as view=links.
func CompileIncident(scope, path string, parameters url.Values, limits SelectorLimits) (*Selection, error) {
	if err := graph.ValidateBeadPath(path); err != nil {
		return nil, &ParameterError{Name: "bead"}
	}
	return compileSelection(scope, "links", path, parameters, limits)
}

func compileSelection(scope, collection, bead string, parameters url.Values, limits SelectorLimits) (*Selection, error) {
	if err := graph.ValidateScopeURL(scope); err != nil {
		return nil, &ParameterError{Name: "scope"}
	}
	q := &Selection{scope: scope, collection: collection, bead: bead, direction: "both"}
	allowed := map[string]bool{"limit": true, "cursor": true}
	if bead != "" {
		allowed["direction"], allowed["view"] = true, true
	} else if collection != "types" {
		allowed["type"], allowed["conformsTo"], allowed["selector"] = true, true, true
		if collection == "links" {
			allowed["source"], allowed["target"], allowed["endpoint"] = true, true, true
		}
	}
	for name, values := range parameters {
		if !allowed[name] || len(values) != 1 {
			return nil, &ParameterError{Name: name}
		}
		value := values[0]
		switch name {
		case "limit":
			if value == "" || value[0] < '1' || value[0] > '9' || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				return nil, &ParameterError{Name: name}
			}
			limit, err := strconv.ParseUint(value, 10, 53)
			if err != nil || uint64(int(limit)) != limit {
				return nil, &ParameterError{Name: name}
			}
			q.limit = int(limit)
		case "cursor":
			if value == "" {
				return nil, &ParameterError{Name: name}
			}
			q.cursor = value
		case "view":
			if value != "links" {
				return nil, &ParameterError{Name: name}
			}
		case "direction":
			if value != "both" && value != "inbound" && value != "outbound" {
				return nil, &ParameterError{Name: name}
			}
			q.direction = value
		case "type", "conformsTo":
			if err := validateCollectionType(scope, value); err != nil {
				return nil, &ParameterError{Name: name}
			}
			if name == "type" {
				q.typ = value
			} else {
				q.conformsTo = value
			}
		case "source", "target", "endpoint":
			ref, err := graph.ParseRef(scope, value, "")
			if err != nil {
				return nil, &ParameterError{Name: name}
			}
			switch name {
			case "source":
				q.source = ref.URL(scope)
			case "target":
				q.target = ref.URL(scope)
			case "endpoint":
				q.endpoint = ref.URL(scope)
			}
		case "selector":
			selector, err := CompileSelector(value, limits)
			if err != nil {
				return nil, err
			}
			q.selector = selector
		}
	}
	// Canonical parameter ordering binds the continuation to this exact request.
	// Cursor is not part of projection identity; all other controls are retained.
	query := url.Values{}
	for name, values := range parameters {
		if name != "cursor" {
			query.Set(name, values[0])
		}
	}
	path := collection + "/"
	if bead != "" {
		path = bead
		query.Set("view", "links")
	}
	q.continuationURL = scope + path
	if encoded := query.Encode(); encoded != "" {
		q.continuationURL += "?" + encoded
	}
	return q, nil
}

func validateCollectionType(scope, id string) error {
	if err := graph.ValidateTypeURL(id); err != nil {
		return err
	}
	if strings.HasPrefix(id, scope) {
		path := strings.TrimPrefix(id, scope)
		if !strings.HasPrefix(path, "types/") {
			return &ParameterError{Name: "type"}
		}
		return graph.ValidateBeadPath("beads/" + strings.TrimPrefix(path, "types/"))
	}
	return nil
}

// Limit returns zero for the pager's configured default.
func (q *Selection) Limit() int              { return q.limit }
func (q *Selection) Cursor() string          { return q.cursor }
func (q *Selection) ContinuationURL() string { return q.continuationURL }
func (q *Selection) Projection() string      { return q.continuationURL }

// Select reads one transaction-consistent inventory. The future HTTP caller
// must restrict it to the captured Authorization View before serving it. A
// continuation must use retained pages; it may never restart against this read.
func (r *Reader) Select(ctx context.Context, q *Selection) ([]json.RawMessage, error) {
	if q == nil || q.scope != r.store.ScopeURL() || q.cursor != "" {
		return nil, &ParameterError{Name: "selection"}
	}
	inventory, err := r.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	return q.Select(inventory)
}

// Select applies every predicate before pagination and returns complete JSON
// records in canonical URI code-unit order. The input must be a complete,
// validated inventory for this Scope and authorization projection. This method
// neither broadens visibility nor performs remote Type retrieval.
func (q *Selection) Select(inventory Inventory) ([]json.RawMessage, error) {
	if q == nil || q.cursor != "" {
		return nil, &ParameterError{Name: "cursor"}
	}
	if q.bead != "" {
		found := false
		for _, bead := range inventory.Beads {
			found = found || bead.ID == q.scope+q.bead
		}
		if !found {
			return nil, graphstore.ErrNotFound
		}
	}
	conformance, err := inventoryConformance(inventory.Types)
	if err != nil {
		return nil, err
	}
	type item struct {
		id  string
		raw json.RawMessage
	}
	selected := []item{}
	appendRecord := func(id, typ string, value any, endpoints *bdpwire.LinkRecord) error {
		if typ != "" {
			effective, exists := conformance[typ]
			if !exists {
				return fmt.Errorf("%w: Resource Type absent from inventory", graphstore.ErrInvalidStore)
			}
			if (q.typ != "" && typ != q.typ) || (q.conformsTo != "" && !effective[q.conformsTo]) {
				return nil
			}
		}
		if endpoints != nil && !q.matchesEndpoints(*endpoints) {
			return nil
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("%w: collection encoding: %v", graphstore.ErrInvalidStore, err)
		}
		if q.selector != nil {
			var candidate map[string]any
			if err := json.Unmarshal(raw, &candidate); err != nil {
				return fmt.Errorf("%w: Selector candidate encoding: %v", graphstore.ErrInvalidStore, err)
			}
			if !q.selector.Matches(candidate) {
				return nil
			}
		}
		selected = append(selected, item{id, raw})
		return nil
	}
	switch q.collection {
	case "beads":
		for _, record := range inventory.Beads {
			if err := appendRecord(record.ID, record.Type, record, nil); err != nil {
				return nil, err
			}
		}
	case "links":
		for _, record := range inventory.Links {
			if err := appendRecord(record.ID, record.Type, record, &record); err != nil {
				return nil, err
			}
		}
	case "types":
		for _, record := range inventory.Types {
			summary := bdpwire.TypeSummary{ID: record.ID, Name: record.Name, Describes: record.Describes}
			if err := appendRecord(record.ID, "", summary, nil); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(selected, func(i, j int) bool { return graph.CompareCodeUnits(selected[i].id, selected[j].id) < 0 })
	result := make([]json.RawMessage, 0, len(selected))
	for i, item := range selected {
		if i > 0 && selected[i-1].id == item.id {
			return nil, fmt.Errorf("%w: duplicate collection identity", graphstore.ErrInvalidStore)
		}
		result = append(result, item.raw)
	}
	return result, nil
}

func (q *Selection) matchesEndpoints(link bdpwire.LinkRecord) bool {
	source, target := link.Source.URI, link.Target.URI
	if q.bead != "" {
		id := q.scope + q.bead
		return (q.direction != "inbound" && source == id) || (q.direction != "outbound" && target == id)
	}
	return (q.source == "" || source == q.source) && (q.target == "" || target == q.target) &&
		(q.endpoint == "" || source == q.endpoint || target == q.endpoint)
}

// All parents must be captured, never fetched during selection. The current
// immutable preview descriptors have no parents; this closure also preserves
// the specified reflexive/transitive meaning for validated captured contracts.
func inventoryConformance(types []bdpwire.TypeDescriptor) (map[string]map[string]bool, error) {
	byID := map[string]bdpwire.TypeDescriptor{}
	for _, typ := range types {
		if _, exists := byID[typ.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate Type identity", graphstore.ErrInvalidStore)
		}
		byID[typ.ID] = typ
	}
	result := map[string]map[string]bool{}
	for _, typ := range types {
		seen := map[string]bool{}
		pending := []string{typ.ID}
		for len(pending) > 0 {
			id := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if seen[id] {
				continue
			}
			parent, exists := byID[id]
			if !exists || parent.Describes != typ.Describes {
				return nil, fmt.Errorf("%w: incomplete or incompatible Type ancestry", graphstore.ErrInvalidStore)
			}
			seen[id] = true
			pending = append(pending, parent.ConformsTo...)
		}
		result[typ.ID] = seen
	}
	return result, nil
}
