package graphstore

import (
	"context"
	"database/sql"
	"fmt"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ReopenIssue uses the existing Issue-domain done-category transition and keeps
// its retained state and complete owned graph mapping in the same transaction.
// Like the current CloseIssue preview, it introduces no new guard contract.
func (s *Store) ReopenIssue(ctx context.Context, path, reason, actor string) (IssueMutationResult, error) {
	if err := validatePath(path); err != nil {
		return IssueMutationResult{}, fmt.Errorf("%w: %v", storage.ErrValidation, err)
	}
	if actor == "" || !utf8.ValidString(actor) || !utf8.ValidString(reason) {
		return IssueMutationResult{}, fmt.Errorf("%w: reopen requires a nonempty UTF-8 actor and UTF-8 reason", storage.ErrValidation)
	}
	var result IssueMutationResult
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		before, err := s.requireIssueInTx(ctx, tx, path)
		if err != nil {
			return err
		}
		category, err := issueops.ReopenCategoryInTx(ctx, tx, before.Properties.Status)
		if err != nil {
			return err
		}
		if category != types.CategoryDone {
			result = IssueMutationResult{Issue: before}
			return nil
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		unscope := issueops.ScopeVersionedHistoryTransaction(tx, true)
		defer unscope()
		reopened, _, err := issueops.ExecuteReopen(ctx, tx, publicops.ReopenRequest{IssueID: before.Properties.ID, Reason: reason, Actor: actor})
		if err != nil {
			return err
		}
		if !reopened.Changed {
			return fmt.Errorf("%w: reopen unexpectedly became a no-op", ErrInvalidStore)
		}
		if err := s.afterStage("issue-reopen"); err != nil {
			return err
		}
		// The target shared writer owns the one native retained version.
		if err := s.afterStage("issue-retained"); err != nil {
			return err
		}
		if err := s.recordIssueMappingInTx(ctx, tx, path, before.Properties.ID, actor); err != nil {
			return err
		}
		after, err := s.showIssueInTx(ctx, tx, path)
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
