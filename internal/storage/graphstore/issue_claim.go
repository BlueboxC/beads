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

// ClaimReadyIssue selects and claims one native ready Issue inside the graph
// writer transaction. An empty front performs no write in the claim transaction,
// so it cannot mint a claim lease, History stamp, or graph revision. The native
// lazy defer-wake preceding selection may separately version an expired Issue.
func (s *Store) ClaimReadyIssue(ctx context.Context, actor string, filter types.WorkFilter) (*IssueMutationResult, error) {
	if actor == "" || !utf8.ValidString(actor) {
		return nil, fmt.Errorf("%w: Issue claim requires a nonempty UTF-8 actor", storage.ErrValidation)
	}
	if err := types.CheckFieldLen("actor", actor); err != nil {
		return nil, fmt.Errorf("%w: %w", storage.ErrValidation, err)
	}
	s.wakeExpiredDefersAdvisory(ctx)
	var result IssueMutationResult
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		unscope := issueops.ScopeVersionedHistoryTransaction(tx, true)
		defer unscope()
		claimed, err := issueops.ClaimReadyIssueInTx(ctx, tx, filter, actor)
		if err != nil {
			return err
		}
		if claimed == nil {
			return nil
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		var path string
		if err := tx.QueryRowContext(ctx, `SELECT path FROM graph_preview_catalog WHERE backing='issue' AND backing_key=? AND allocation_state='live'`, claimed.ID).Scan(&path); err != nil {
			return fmt.Errorf("%w: unmapped claimed Issue: %v", ErrInvalidStore, err)
		}
		if err := s.afterStage("issue-claim"); err != nil {
			return err
		}
		if err := s.afterStage("issue-retained"); err != nil {
			return err
		}
		if err := s.recordIssueMappingInTx(ctx, tx, path, claimed.ID, actor); err != nil {
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
		return nil, err
	}
	if result.Issue.Properties == nil {
		return nil, nil
	}
	return &result, nil
}

// ClaimIssue reuses the ordinary atomic by-ID claim. The claimant's eligibility
// is owned by that writer, not by a new graph revision or force-transfer policy.
// A changed claim and its complete retained graph record commit together.
func (s *Store) ClaimIssue(ctx context.Context, path, actor string) (IssueMutationResult, error) {
	if err := validatePath(path); err != nil {
		return IssueMutationResult{}, fmt.Errorf("%w: %v", storage.ErrValidation, err)
	}
	if actor == "" || !utf8.ValidString(actor) {
		return IssueMutationResult{}, fmt.Errorf("%w: Issue claim requires a nonempty UTF-8 actor", storage.ErrValidation)
	}
	if err := types.CheckFieldLen("actor", actor); err != nil {
		return IssueMutationResult{}, fmt.Errorf("%w: %w", storage.ErrValidation, err)
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
		request := publicops.ClaimRequest{IssueID: before.Properties.ID, Actor: actor}
		if before.Properties.Status == types.StatusInProgress && issueops.ActorMatches(before.Properties.Assignee, actor) {
			// Keep the ordinary writer's configuration reads and refusal behavior,
			// while avoiding even a coordination write for its idempotent case.
			claimed, _, err := issueops.ExecuteClaim(ctx, tx, request)
			if err != nil {
				return err
			}
			if claimed.Changed || claimed.Issue == nil || claimed.Issue.RowVersion != before.Properties.RowVersion {
				return fmt.Errorf("%w: idempotent Issue claim unexpectedly changed", ErrInvalidStore)
			}
			// The pinned writer stages nothing here. Check its row token as well
			// as the complete retained projection: malformed configuration or a
			// future writer must not commit hidden writes while reporting no-op.
			checked, err := s.showIssueInTx(ctx, tx, path)
			if err != nil {
				return err
			}
			result = IssueMutationResult{Issue: checked}
			return nil
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		unscope := issueops.ScopeVersionedHistoryTransaction(tx, true)
		defer unscope()
		claimed, _, err := issueops.ExecuteClaim(ctx, tx, request)
		if err != nil {
			return err
		}
		if !claimed.Changed {
			return fmt.Errorf("%w: Issue claim unexpectedly became a no-op", ErrInvalidStore)
		}
		if err := s.afterStage("issue-claim"); err != nil {
			return err
		}
		// ExecuteClaim owns the native version mint. Record only the graph
		// mapping below so the lease, audit, native version and complete graph
		// record commit once in the same transaction.
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
