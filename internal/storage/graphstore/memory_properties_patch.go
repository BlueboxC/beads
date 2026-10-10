package graphstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/graphpatch"
	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

// MemoryPropertiesPatchRequest applies ordered property operations to the
// actual guarded predecessor. This private preview still stores exactly title
// and body strings; it does not expose the public BDP Update profile.
type MemoryPropertiesPatchRequest struct {
	Path             string
	Patch            []byte
	Actor            string
	ExpectedRevision string
	Unconditional    bool
	Metadata         publicops.MetadataPatch
}

func (s *Store) PatchMemoryProperties(ctx context.Context, request MemoryPropertiesPatchRequest) (MemoryMutationResult, error) {
	patch, err := graphpatch.Parse(request.Patch)
	if err != nil {
		return MemoryMutationResult{}, memoryPropertiesPatchError(err)
	}
	return s.writeMemory(ctx, memoryWriteRequest{
		path: request.Path, actor: request.Actor, expectedRevision: request.ExpectedRevision,
		unconditional: request.Unconditional, propertiesPatch: patch, metadataPatch: request.Metadata,
	})
}

func memoryPropertiesPatchError(err error) error {
	if errors.Is(err, graphpatch.ErrLimit) {
		return fmt.Errorf("%w: %v", ErrLimitExceeded, err)
	}
	return fmt.Errorf("%w: %v", storage.ErrValidation, err)
}

func applyMemoryPropertiesPatch(patch *graphpatch.Patch, before Properties) (Properties, error) {
	// Refuse obvious oversized subjects before allocating their JSON form. The
	// evaluator also charges canonical escaping and the complete object framing.
	if len(before.Title) > graphpatch.MaxInputBytes || len(before.Body) > graphpatch.MaxInputBytes-len(before.Title) {
		return Properties{}, fmt.Errorf("%w: Memory properties exceed the patch working-document limit", ErrLimitExceeded)
	}
	raw, err := canonicalJSON(before)
	if err != nil {
		return Properties{}, err
	}
	properties, err := graphops.NewProperties(raw)
	if err != nil {
		return Properties{}, err
	}
	after, err := patch.Apply(properties)
	if err != nil {
		return Properties{}, memoryPropertiesPatchError(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(after.Bytes(), &fields); err != nil {
		return Properties{}, memoryPropertiesPatchError(err)
	}
	var title, body string
	// JSON null must not be confused with the empty string by Unmarshal.
	if len(fields) != 2 || len(fields["title"]) == 0 || fields["title"][0] != '"' || len(fields["body"]) == 0 || fields["body"][0] != '"' {
		return Properties{}, fmt.Errorf("%w: final preview Memory properties require exactly title and body strings", storage.ErrValidation)
	}
	if err := json.Unmarshal(fields["title"], &title); err != nil {
		return Properties{}, memoryPropertiesPatchError(err)
	}
	if err := json.Unmarshal(fields["body"], &body); err != nil {
		return Properties{}, memoryPropertiesPatchError(err)
	}
	return Properties{Title: title, Body: body}, nil
}
