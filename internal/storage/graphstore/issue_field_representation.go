package graphstore

import (
	"bytes"
	"fmt"
	"math"
	"reflect"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// maxIssueSpecIDRunes matches spec_id VARCHAR(1024) in migration 0001_create_issues.
const maxIssueSpecIDRunes = 1024

func validateIssueEstimateStorage(value *int) error {
	if value != nil && *value > math.MaxInt32 {
		return fmt.Errorf("%w: Issue estimated_minutes exceeds the storage maximum of %d", storage.ErrValidation, math.MaxInt32)
	}
	return nil
}

// Representation checks are shared; operation-specific NULL/empty conversion
// stays in the adapters. No URL, identity or tracker policy is imposed here.
func validateIssueReferenceStorage(name, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: Issue %s must be UTF-8", storage.ErrValidation, name)
	}
	if name == "external_ref" {
		if err := types.CheckFieldLen(name, value); err != nil {
			return fmt.Errorf("%w: Issue external_ref: %w", storage.ErrValidation, err)
		}
	} else if utf8.RuneCountInString(value) > maxIssueSpecIDRunes {
		return fmt.Errorf("%w: Issue spec_id exceeds the existing %d-character column", storage.ErrValidation, maxIssueSpecIDRunes)
	}
	return nil
}

func validateIssueCreateFields(issue *types.Issue) error {
	for _, field := range []struct{ name, value string }{
		{"design", issue.Design}, {"acceptance_criteria", issue.AcceptanceCriteria}, {"assignee", issue.Assignee},
		{"notes", issue.Notes}, {"owner", issue.Owner}, {"created_by", issue.CreatedBy},
	} {
		if !utf8.ValidString(field.value) {
			return fmt.Errorf("%w: Issue %s must be UTF-8", storage.ErrValidation, field.name)
		}
	}
	// Owner is checked by ValidatePublicCreateRequest. CreatedBy may differ
	// from request.Actor, whose separate length check cannot cover this value.
	if err := types.CheckFieldLen("created_by", issue.CreatedBy); err != nil {
		return fmt.Errorf("%w: Issue creator: %w", storage.ErrValidation, err)
	}
	if err := types.ValidateIssueEstimatedMinutes(issue.EstimatedMinutes); err != nil {
		return fmt.Errorf("%w: %w", storage.ErrValidation, err)
	}
	if err := validateIssueEstimateStorage(issue.EstimatedMinutes); err != nil {
		return err
	}
	if issue.ExternalRef != nil {
		if err := validateIssueReferenceStorage("external_ref", *issue.ExternalRef); err != nil {
			return err
		}
	}
	return validateIssueReferenceStorage("spec_id", issue.SpecID)
}

func sameIssueCreateFields(want, got *types.Issue) bool {
	if want == nil || got == nil {
		return false
	}
	wantMetadata, wantErr := commonMetadata(want.Metadata)
	gotMetadata, gotErr := commonMetadata(got.Metadata)
	return wantErr == nil && gotErr == nil && bytes.Equal(wantMetadata, gotMetadata) && want.Design == got.Design && want.AcceptanceCriteria == got.AcceptanceCriteria &&
		want.Assignee == got.Assignee && want.SpecID == got.SpecID &&
		sameIssueDue(want.DueAt, got.DueAt) && want.Notes == got.Notes && want.Owner == got.Owner && want.CreatedBy == got.CreatedBy &&
		reflect.DeepEqual(want.EstimatedMinutes, got.EstimatedMinutes) && reflect.DeepEqual(want.ExternalRef, got.ExternalRef)
}
