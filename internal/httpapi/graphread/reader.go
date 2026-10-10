// Package graphread projects the authoritative disposable graph store onto
// public BDP records. It is not an HTTP server or a complete Read profile.
package graphread

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/httpapi/bdpwire"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// Reader leaves storage lifetime with its caller. Each Resource read includes
// its complete owned state in the store's one read transaction. A subsequent
// Type read is safe because the admitted preview descriptors are immutable and
// every store operation checks their persisted bytes and fingerprints.
type Reader struct{ store *graphstore.Store }

func New(store *graphstore.Store) *Reader { return &Reader{store: store} }

// Resource reads a canonical local beads/... or links/... path. Storage absence,
// deletion and corruption errors remain distinct for the future HTTP adapter.
// The result is either bdpwire.BeadRecord or bdpwire.LinkRecord, never a private
// preview envelope. This method does not select historical versions.
func (r *Reader) Resource(ctx context.Context, path string) (any, error) {
	record, err := r.store.Read(ctx, path)
	if err != nil {
		return nil, err
	}
	switch v := record.(type) {
	case graphstore.Record:
		return r.bead(ctx, v.ID, v.Type, v.Revision, v.Properties, v.Metadata, v.Owned, v.Attribution)
	case graphstore.IssueRecord:
		return r.bead(ctx, v.ID, v.Type, v.Revision, v.Properties, v.Metadata, v.Owned, v.Attribution)
	case graphstore.LinkRecord:
		return link(v)
	default:
		return nil, fmt.Errorf("%w: unsupported Resource projection", graphstore.ErrInvalidStore)
	}
}

// Type returns the exact installed public descriptor, without reconstructing or
// renaming the contract. It does not install a Type or retrieve a remote URL.
func (r *Reader) Type(ctx context.Context, path string) (bdpwire.TypeDescriptor, error) {
	descriptor, err := r.store.ReadType(ctx, path)
	if err != nil {
		return bdpwire.TypeDescriptor{}, err
	}
	var result bdpwire.TypeDescriptor
	if err := bdpwire.Unmarshal(descriptor.CanonicalJSON(), &result); err != nil {
		return result, fmt.Errorf("%w: installed Type wire shape: %v", graphstore.ErrInvalidStore, err)
	}
	return result, nil
}

func (r *Reader) bead(ctx context.Context, id, typ, revision string, properties any, metadata json.RawMessage, owned []json.RawMessage, attribution graphstore.Attribution) (bdpwire.BeadRecord, error) {
	if !strings.HasPrefix(typ, r.store.ScopeURL()) {
		return bdpwire.BeadRecord{}, fmt.Errorf("%w: Resource Type is outside installed Scope", graphstore.ErrInvalidStore)
	}
	descriptor, err := r.store.ReadType(ctx, strings.TrimPrefix(typ, r.store.ScopeURL()))
	if err != nil {
		return bdpwire.BeadRecord{}, err
	}
	return bead(descriptor, id, typ, revision, properties, metadata, owned, attribution)
}

func bead(descriptor graph.TypeDescriptor, id, typ, revision string, properties any, metadata json.RawMessage, owned []json.RawMessage, attribution graphstore.Attribution) (bdpwire.BeadRecord, error) {
	result := bdpwire.BeadRecord{ID: id, Type: typ, Revision: revision}
	if descriptor.ID() != typ || descriptor.Describes() != graph.KindBead {
		return result, fmt.Errorf("%w: Bead descriptor does not match its Type", graphstore.ErrInvalidStore)
	}
	var err error
	result.Properties, err = projectProperties(properties)
	if err != nil {
		return result, err
	}
	result.Metadata, err = projectMetadata(metadata)
	if err != nil {
		return result, err
	}
	result.Attribution, err = projectAttribution(attribution)
	if err != nil {
		return result, err
	}
	result.OwnedLinks, err = ownedLinks(id, descriptor, owned)
	return result, err
}

func ownedLinks(source string, descriptor graph.TypeDescriptor, records []json.RawMessage) (bdpwire.OwnedLinks, error) {
	var result bdpwire.OwnedLinks
	if len(descriptor.OwnsOutgoing()) > 0 {
		result = bdpwire.OwnedLinks{}
	}
	for _, declaration := range descriptor.OwnsOutgoing() {
		if !declaration.Wildcard() {
			result[declaration.TypeURL()] = bdpwire.LinkRecords{}
		} else if len(records) > declaration.Max() {
			return nil, fmt.Errorf("%w: wildcard owned set exceeds descriptor", graphstore.ErrInvalidStore)
		}
	}
	seen := map[string]bool{}
	for _, raw := range records {
		var stored graphstore.LinkRecord
		if err := json.Unmarshal(raw, &stored); err != nil {
			return nil, fmt.Errorf("%w: malformed owned Link: %v", graphstore.ErrInvalidStore, err)
		}
		declaration, owns := descriptor.Owns(stored.Type)
		if !owns || stored.Source != source || seen[stored.ID] {
			return nil, fmt.Errorf("%w: inconsistent owned Link membership", graphstore.ErrInvalidStore)
		}
		seen[stored.ID] = true
		projected, err := link(stored)
		if err != nil {
			return nil, err
		}
		result[stored.Type] = append(result[stored.Type], projected)
		if len(result[stored.Type]) > declaration.Max() {
			return nil, fmt.Errorf("%w: owned group exceeds descriptor", graphstore.ErrInvalidStore)
		}
	}
	for _, group := range result {
		sort.Slice(group, func(i, j int) bool { return graph.CompareCodeUnits(group[i].ID, group[j].ID) < 0 })
	}
	return result, nil
}

func link(v graphstore.LinkRecord) (bdpwire.LinkRecord, error) {
	result := bdpwire.LinkRecord{ID: v.ID, Type: v.Type, Revision: v.Revision,
		Source: bdpwire.Reference{URI: v.Source}, Target: bdpwire.Reference{URI: v.Target}}
	var err error
	result.Properties, err = projectProperties(v.Properties)
	if err != nil {
		return result, err
	}
	result.Metadata, err = projectMetadata(v.Metadata)
	if err != nil {
		return result, err
	}
	result.Attribution, err = projectAttribution(v.Attribution)
	return result, err
}

// Inputs are complete properties returned by the validated store, not a field
// whitelist. In particular an Issue's domain ID stays inside its properties;
// the graph's canonical identity is the separate Resource ID.
func projectProperties(value any) (bdpwire.Properties, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: properties encoding: %v", graphstore.ErrInvalidStore, err)
	}
	var result bdpwire.Properties
	if err := bdpwire.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("%w: properties shape: %v", graphstore.ErrInvalidStore, err)
	}
	return result, nil
}

func projectMetadata(raw json.RawMessage) (bdpwire.Metadata, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var result bdpwire.Metadata
	if err := bdpwire.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("%w: metadata shape: %v", graphstore.ErrInvalidStore, err)
	}
	return result, nil
}

func projectAttribution(value graphstore.Attribution) (*bdpwire.Attribution, error) {
	if value.Actor == "" {
		if value.Status == "unknown" || value.Status == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: unsupported attribution", graphstore.ErrInvalidStore)
	}
	var basis bdpwire.AttributionBasis
	switch value.Status {
	case "claimed":
		basis = bdpwire.AttributionWriterSupplied
	case "unknown":
		// Current graphstore validation cannot persist a principal with unknown
		// status; retain the wire mapping for a future admitted import path.
		basis = bdpwire.AttributionUnknown
	default:
		return nil, fmt.Errorf("%w: unsupported attribution", graphstore.ErrInvalidStore)
	}
	return &bdpwire.Attribution{Principal: value.Actor, Basis: basis}, nil
}
