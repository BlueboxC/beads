package graphstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// UpdateIssueRequest admits existing Issue scalar edits, notes replacement and append.
// Nil fields preserve their properties. Empty scalar strings clear fields where
// the Issue domain permits; AppendNotes instead preserves native append semantics:
// empty on empty is a no-op, while empty on nonempty appends one newline.
// Notes replacement uses the native writer's overwrite fence; clear is an
// explicit CLI intent, not a special storage operation.
// EstimatedMinutes nil preserves the nullable estimate; zero sets a present zero.
// Clearing to null is not admitted. The existing Issue validator and SQL column
// retain their ordinary bounds; no scheduler or duration interpretation is added.
// ExternalRef and SpecID preserve literal values; empty clears the external
// reference to NULL and the spec ID to an empty string, as in ordinary update.
// DueAt distinguishes omission from nullable clear. Present dates use the existing
// whole-second storage representation before guarded no-op comparison.
// Priority zero sets P0; nil preserves priority. The guard addresses the complete
// graph revision, including owned blocking Dependencies, not the storage ordinal.
// Assignee nil preserves its value; empty clears it. Neither the graph guard nor
// Unconditional bypasses the ordinary active-assignment transfer fence.
type UpdateIssueRequest struct {
	Path, Actor, ExpectedRevision                  string
	Unconditional, ForceNotesOverwrite             bool
	PropertiesProvided, ClearEstimatedMinutes      bool
	Title, Description, Design, AcceptanceCriteria *string
	Priority                                       *int
	EstimatedMinutes                               *int
	Assignee                                       *string
	Notes, AppendNotes                             *string
	ExternalRef, SpecID                            *string
	DueAt                                          publicops.Field[*time.Time]
	Metadata                                       publicops.MetadataPatch
}

// UpdateIssue delegates admitted scalar edits to the existing Issue domain writer and
// retained-version recorder. Payload, opaque graph revision and complete owned
// state commit together; this adds no public History ordering or change context.
func (s *Store) UpdateIssue(ctx context.Context, request UpdateIssueRequest) (IssueMutationResult, error) {
	if err := validatePath(request.Path); err != nil {
		return IssueMutationResult{}, fmt.Errorf("%w: %v", storage.ErrValidation, err)
	}
	if request.Actor == "" || !utf8.ValidString(request.Actor) {
		return IssueMutationResult{}, fmt.Errorf("%w: Issue update requires a nonempty UTF-8 actor", storage.ErrValidation)
	}
	patch := publicops.IssuePatch{}
	count := 0
	for _, field := range []struct {
		name  string
		value *string
		dest  *publicops.Field[string]
	}{
		{"title", request.Title, &patch.Title},
		{"assignee", request.Assignee, &patch.Assignee},
		{"description", request.Description, &patch.Description},
		{"design", request.Design, &patch.Design},
		{"acceptance_criteria", request.AcceptanceCriteria, &patch.AcceptanceCriteria},
		{"notes", request.Notes, &patch.Notes},
		{"append_notes", request.AppendNotes, &patch.AppendNotes},
	} {
		if field.value == nil {
			continue
		}
		if !utf8.ValidString(*field.value) {
			return IssueMutationResult{}, fmt.Errorf("%w: Issue %s must be UTF-8", storage.ErrValidation, field.name)
		}
		*field.dest = publicops.Field[string]{Set: true, Value: *field.value}
		count++
	}
	if patch.Assignee.Set {
		if err := types.CheckFieldLen("assignee", patch.Assignee.Value); err != nil {
			return IssueMutationResult{}, fmt.Errorf("%w: Issue assignee: %w", storage.ErrValidation, err)
		}
	}
	if request.Priority != nil {
		patch.Priority = publicops.Field[int]{Set: true, Value: *request.Priority}
		count++
	}
	if request.EstimatedMinutes != nil && request.ClearEstimatedMinutes {
		return IssueMutationResult{}, fmt.Errorf("%w: Issue estimate cannot be set and cleared together", storage.ErrValidation)
	}
	if request.EstimatedMinutes != nil {
		value := *request.EstimatedMinutes
		// The existing Issue column is a signed SQL INT on both backends. Reject
		// unrepresentable input before SQL so strict and coercing engines agree.
		if err := validateIssueEstimateStorage(&value); err != nil {
			return IssueMutationResult{}, err
		}
		patch.EstimatedMinutes = publicops.Field[*int]{Set: true, Value: &value}
		count++
	} else if request.ClearEstimatedMinutes {
		patch.EstimatedMinutes = publicops.Field[*int]{Set: true}
		count++
	}
	// These limits describe the existing VARCHAR columns, not reference syntax.
	// Validate before SQL so strict and coercing engines both refuse data loss.
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"external_ref", request.ExternalRef},
		{"spec_id", request.SpecID},
	} {
		if field.value == nil {
			continue
		}
		value := *field.value
		if err := validateIssueReferenceStorage(field.name, value); err != nil {
			return IssueMutationResult{}, err
		}
		if field.name == "external_ref" {
			patch.ExternalRef = publicops.Field[*string]{Set: true}
			if value != "" {
				patch.ExternalRef.Value = &value
			}
		} else {
			patch.SpecID = publicops.Field[string]{Set: true, Value: value}
		}
		count++
	}
	if request.DueAt.Set {
		value, err := normalizeIssueDue(request.DueAt.Value)
		if err != nil {
			return IssueMutationResult{}, err
		}
		patch.DueAt = publicops.Field[*time.Time]{Set: true, Value: value}
		count++
	}
	if err := validateCommonMetadataPatch(request.Metadata); err != nil {
		return IssueMutationResult{}, err
	}
	if request.Metadata.Replace.Set || request.Metadata.Merge.Set || len(request.Metadata.Set) > 0 || len(request.Metadata.Unset) > 0 {
		patch.Metadata = request.Metadata
		count++
	}
	if count == 0 && !request.PropertiesProvided {
		return IssueMutationResult{}, fmt.Errorf("%w: Issue update requires an admitted field", storage.ErrValidation)
	}
	attempt := publicops.UpdateRequest{Actor: request.Actor, Patch: patch, IssuePlaneOnly: true}
	if err := issueops.ValidateUpdateRequest(attempt); err != nil {
		return IssueMutationResult{}, err
	}
	var result IssueMutationResult
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		current, revision, _, err := s.beadEndpointInTx(ctx, tx, request.Path)
		if err != nil {
			return err
		}
		before, ok := current.(IssueRecord)
		if !ok {
			return fmt.Errorf("%w: Issue update requires the experimental Issue Type", ErrCapabilityUnavailable)
		}
		if err := checkRevisionGuard(request.ExpectedRevision, request.Unconditional, revision, true, "Issue"); err != nil {
			return err
		}
		if patch.Notes.Set && publicops.NotesReplacement(before.Properties.Notes, patch.Notes.Value) && !request.ForceNotesOverwrite {
			return fmt.Errorf("%w: issue %s", publicops.ErrNotesOverwrite, before.Properties.ID)
		}
		beforeNative := *before.Properties
		beforeNative.Metadata = before.Metadata
		// Resolve against the checked snapshot only for no-op planning. The native
		// writer receives the original append intent and owns atomic composition.
		updates, err := issueops.ResolveMergeOps(&beforeNative, issueops.UpdateFields(patch))
		if err != nil {
			return err
		}
		updates, err = issueops.DiscardNoopIssueUpdates(&beforeNative, updates)
		if err != nil {
			return err
		}
		resolvedMetadata, metadataChanged, err := issueops.ApplyMetadataPatch(before.Metadata, patch.Metadata)
		if err != nil {
			return err
		}
		// The native Issue writer owns this update, but graph records must meet
		// the same I-JSON admission as Memory and Link metadata. Validate the
		// resolved object before either the no-op return or any write effect.
		if _, err := commonMetadata(resolvedMetadata); err != nil {
			return err
		}
		if len(updates) == 0 && !metadataChanged {
			result = IssueMutationResult{Issue: before}
			return nil
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		unscope := issueops.ScopeVersionedHistoryTransaction(tx, true)
		defer unscope()
		attempt.IssueID = before.Properties.ID
		updated, _, err := issueops.ExecuteUpdate(ctx, tx, attempt)
		if err != nil {
			return err
		}
		if !updated.Changed {
			return fmt.Errorf("%w: Issue update unexpectedly became a no-op", ErrInvalidStore)
		}
		// Non-strict SQL may coerce an oversized Go integer into the column's
		// range without returning an error. The shared writer hydrates the actual
		// stored result; never publish or retain an estimate different from the
		// accepted intent. Refusal rolls back sibling edits and audit effects too.
		if patch.EstimatedMinutes.Set {
			if updated.Issue == nil || (patch.EstimatedMinutes.Value == nil) != (updated.Issue.EstimatedMinutes == nil) ||
				(patch.EstimatedMinutes.Value != nil && *updated.Issue.EstimatedMinutes != *patch.EstimatedMinutes.Value) {
				return fmt.Errorf("%w: Issue estimate cannot be represented exactly by storage", storage.ErrValidation)
			}
		}
		if patch.ExternalRef.Set {
			if updated.Issue == nil || (patch.ExternalRef.Value == nil) != (updated.Issue.ExternalRef == nil) ||
				(patch.ExternalRef.Value != nil && *patch.ExternalRef.Value != *updated.Issue.ExternalRef) {
				return fmt.Errorf("%w: Issue external reference cannot be represented exactly by storage", storage.ErrValidation)
			}
		}
		if patch.SpecID.Set && (updated.Issue == nil || updated.Issue.SpecID != patch.SpecID.Value) {
			return fmt.Errorf("%w: Issue spec ID cannot be represented exactly by storage", storage.ErrValidation)
		}
		if patch.DueAt.Set && (updated.Issue == nil || !sameIssueDue(patch.DueAt.Value, updated.Issue.DueAt)) {
			return fmt.Errorf("%w: Issue due date cannot be represented exactly by storage", storage.ErrValidation)
		}
		if err := s.afterStage("issue-update"); err != nil {
			return err
		}
		// The target shared writer owns the one native retained version.
		if err := s.afterStage("issue-retained"); err != nil {
			return err
		}
		if err := s.recordIssueMappingInTx(ctx, tx, request.Path, before.Properties.ID, request.Actor); err != nil {
			return err
		}
		after, err := s.showIssueInTx(ctx, tx, request.Path)
		if err != nil {
			return err
		}
		result = IssueMutationResult{Issue: after, Changed: true}
		return nil
	})
	if err != nil {
		return IssueMutationResult{}, err
	}
	return result, nil
}
