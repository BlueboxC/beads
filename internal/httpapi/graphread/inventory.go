package graphread

import (
	"context"
	"fmt"

	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/httpapi/bdpwire"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// Inventory is an internal, complete current-state input to future collection
// selection and pagination. WriterToken is an equality-only invalidation token,
// not a public cursor, revision, Scope epoch or durable snapshot handle. Never
// serialize this structure as a BDP response.
type Inventory struct {
	WriterToken string
	Beads       []bdpwire.BeadRecord
	Links       []bdpwire.LinkRecord
	Types       []bdpwire.TypeDescriptor
}

// Inventory reads all current records and installed descriptors in one storage
// transaction, then projects only those captured values. It performs no further
// reads while projecting owned Links or Type contracts. Oversized or corrupt
// inventories return an error and no partial result. The store's operational
// bound is not a page size; selection and pagination are not implemented here.
func (r *Reader) Inventory(ctx context.Context) (Inventory, error) {
	snapshot, err := r.store.CurrentSnapshot(ctx)
	if err != nil {
		return Inventory{}, err
	}
	return projectInventory(snapshot)
}

func projectInventory(snapshot graphstore.Snapshot) (Inventory, error) {
	result := Inventory{WriterToken: snapshot.WriterToken,
		Beads: []bdpwire.BeadRecord{}, Links: []bdpwire.LinkRecord{}, Types: []bdpwire.TypeDescriptor{}}
	descriptors := make(map[string]graph.TypeDescriptor, len(snapshot.Types))
	for _, descriptor := range snapshot.Types {
		if _, exists := descriptors[descriptor.ID()]; exists {
			return Inventory{}, fmt.Errorf("%w: duplicate installed Type", graphstore.ErrInvalidStore)
		}
		descriptors[descriptor.ID()] = descriptor
		var projected bdpwire.TypeDescriptor
		if err := bdpwire.Unmarshal(descriptor.CanonicalJSON(), &projected); err != nil {
			return Inventory{}, fmt.Errorf("%w: installed Type wire shape: %v", graphstore.ErrInvalidStore, err)
		}
		result.Types = append(result.Types, projected)
	}
	for _, record := range snapshot.Records {
		switch v := record.(type) {
		case graphstore.Record:
			projected, err := bead(descriptors[v.Type], v.ID, v.Type, v.Revision, v.Properties, v.Metadata, v.Owned, v.Attribution)
			if err != nil {
				return Inventory{}, err
			}
			result.Beads = append(result.Beads, projected)
		case graphstore.IssueRecord:
			projected, err := bead(descriptors[v.Type], v.ID, v.Type, v.Revision, v.Properties, v.Metadata, v.Owned, v.Attribution)
			if err != nil {
				return Inventory{}, err
			}
			result.Beads = append(result.Beads, projected)
		case graphstore.LinkRecord:
			descriptor, exists := descriptors[v.Type]
			if !exists || descriptor.Describes() != graph.KindLink {
				return Inventory{}, fmt.Errorf("%w: Link descriptor does not match its Type", graphstore.ErrInvalidStore)
			}
			projected, err := link(v)
			if err != nil {
				return Inventory{}, err
			}
			result.Links = append(result.Links, projected)
		default:
			return Inventory{}, fmt.Errorf("%w: unsupported Resource projection", graphstore.ErrInvalidStore)
		}
	}
	return result, nil
}
