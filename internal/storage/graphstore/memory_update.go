package graphstore

import (
	"context"
	"database/sql"
	"fmt"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/graphpatch"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

// MemoryUpdateRequest replaces the complete experimental title/body payload.
// Empty strings are values; the caller must distinguish omitted JSON members.
type MemoryUpdateRequest struct {
	Path             string
	Properties       Properties
	Actor            string
	ExpectedRevision string
	Unconditional    bool
	Metadata         publicops.MetadataPatch
}

type MemoryMutationResult struct {
	Memory   Record          `json:"memory"`
	Changed  bool            `json:"changed"`
	Replaced *ReplacedMemory `json:"replaced,omitempty"`
}

// UpdateMemory retains the complete accepted Memory, including unchanged owned
// Links, in the same transaction as its properties and revision. This preview
// operation establishes no public History ordering or change-context contract.
func (s *Store) UpdateMemory(ctx context.Context, request MemoryUpdateRequest) (MemoryMutationResult, error) {
	return s.writeMemory(ctx, memoryWriteRequest{
		path: request.Path, actor: request.Actor, expectedRevision: request.ExpectedRevision,
		unconditional: request.Unconditional, title: request.Properties.Title, body: request.Properties.Body,
		hasTitle: true, hasBody: true, metadataPatch: request.Metadata,
	})
}

// MemoryPatchRequest changes only supplied fields. A nil pointer preserves the
// checked predecessor's field; a pointer to an empty string clears that field.
// At least one field must be supplied. This is an internal preview API.
type MemoryPatchRequest struct {
	Path               string
	Title              *string
	Body               *string
	PropertiesProvided bool
	Actor              string
	ExpectedRevision   string
	Unconditional      bool
	Metadata           publicops.MetadataPatch
}

// PatchMemory resolves omitted fields from the actual predecessor inside the
// mutation transaction, under the same guard and retention rules as UpdateMemory.
func (s *Store) PatchMemory(ctx context.Context, request MemoryPatchRequest) (MemoryMutationResult, error) {
	patch := memoryWriteRequest{path: request.Path, actor: request.Actor,
		expectedRevision: request.ExpectedRevision, unconditional: request.Unconditional, metadataPatch: request.Metadata,
		propertiesProvided: request.PropertiesProvided}
	// Capture caller-owned pointers before entering the transaction. No pointer is
	// retained or read by the transaction callback.
	if request.Title != nil {
		patch.title, patch.hasTitle = *request.Title, true
	}
	if request.Body != nil {
		patch.body, patch.hasBody = *request.Body, true
	}
	return s.writeMemory(ctx, patch)
}

type memoryWriteRequest struct {
	path, actor, expectedRevision string
	unconditional                 bool
	title, body                   string
	hasTitle, hasBody             bool
	propertiesPatch               *graphpatch.Patch
	propertiesProvided            bool
	metadataPatch                 publicops.MetadataPatch
}

func (s *Store) writeMemory(ctx context.Context, request memoryWriteRequest) (MemoryMutationResult, error) {
	if err := validatePath(request.path); err != nil {
		return MemoryMutationResult{}, fmt.Errorf("%w: %v", storage.ErrValidation, err)
	}
	if !request.hasTitle && !request.hasBody && request.propertiesPatch == nil && !request.propertiesProvided && !hasCommonMetadataPatch(request.metadataPatch) {
		return MemoryMutationResult{}, fmt.Errorf("%w: at least one Memory field must be supplied", storage.ErrValidation)
	}
	if request.propertiesPatch != nil && (request.hasTitle || request.hasBody) {
		return MemoryMutationResult{}, fmt.Errorf("%w: ordered Memory properties patch cannot be combined with field replacement", storage.ErrValidation)
	}
	if err := validateCommonMetadataPatch(request.metadataPatch); err != nil {
		return MemoryMutationResult{}, err
	}
	if !utf8.ValidString(request.title) || !utf8.ValidString(request.body) || !utf8.ValidString(request.actor) {
		return MemoryMutationResult{}, fmt.Errorf("%w: Memory title, body and actor must be UTF-8", storage.ErrValidation)
	}
	var result MemoryMutationResult
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		result, err = s.writeMemoryInTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return MemoryMutationResult{}, err
	}
	return result, nil
}

func (s *Store) writeMemoryInTx(ctx context.Context, tx *sql.Tx, request memoryWriteRequest) (MemoryMutationResult, error) {
	var result MemoryMutationResult
	err := func() error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		current, revision, _, err := s.beadEndpointInTx(ctx, tx, request.path)
		if err != nil {
			return err
		}
		memory, ok := current.(Record)
		if !ok {
			return fmt.Errorf("%w: property replacement supports the experimental Memory Type only", ErrCapabilityUnavailable)
		}
		if err := checkRevisionGuard(request.expectedRevision, request.unconditional, revision, true, "Memory"); err != nil {
			return err
		}
		next := memory.Properties
		if request.hasTitle {
			next.Title = request.title
		}
		if request.hasBody {
			next.Body = request.body
		}
		if request.propertiesPatch != nil {
			next, err = applyMemoryPropertiesPatch(request.propertiesPatch, memory.Properties)
			if err != nil {
				return err
			}
		}
		metadata, metadataChanged, err := issueops.ApplyMetadataPatch(memory.Metadata, request.metadataPatch)
		if err != nil {
			return err
		}
		metadata, err = commonMetadata(metadata)
		if err != nil {
			return err
		}
		if err := preserveContinuityKey(memory.Metadata, metadata); err != nil {
			return err
		}
		if memory.Properties == next && !metadataChanged {
			result = MemoryMutationResult{Memory: memory}
			return nil
		}
		properties, err := canonicalJSON(next)
		if err != nil {
			return err
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE graph_preview_payloads SET properties=?, metadata=? WHERE path=?`, properties, metadata, request.path); err != nil {
			return err
		}
		if err := s.afterStage("memory-payload"); err != nil {
			return err
		}
		replaced := replacedMemory(memory, request.unconditional)
		memory.Properties = next
		memory.Metadata = metadata
		accepted, err := s.recordOwnedMemoryInTx(ctx, tx, request.path, request.actor, memory)
		if err != nil {
			return err
		}
		result = MemoryMutationResult{Memory: accepted.(Record), Changed: true, Replaced: replaced}
		return nil
	}()
	if err != nil {
		return MemoryMutationResult{}, err
	}
	return result, nil
}
