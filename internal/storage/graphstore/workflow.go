package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
	publicops "github.com/steveyegge/beads/issueops"
)

// CloseIssue delegates to the checked Issue-domain close, in the same fenced
// transaction as retained state and graph identity.
func (s *Store) CloseIssue(ctx context.Context, path, reason, actor string) (IssueMutationResult, error) {
	return s.CloseIssueWithOptions(ctx, IssueCloseRequest{Path: path, Reason: reason, Actor: actor})
}

// IssueCloseRequest carries the ordinary close policy into the graph writer.
// Force bypasses only the native blocker/child and pinned/holder policies.
type IssueCloseRequest struct {
	Path, Reason, Actor, Session string
	ExpectedRevision             string
	Force                        bool
}

func (s *Store) CloseIssueWithOptions(ctx context.Context, request IssueCloseRequest) (IssueMutationResult, error) {
	if err := validatePath(request.Path); err != nil {
		return IssueMutationResult{}, err
	}
	if request.Actor == "" || !utf8.ValidString(request.Actor) || !utf8.ValidString(request.Reason) || !utf8.ValidString(request.Session) {
		return IssueMutationResult{}, fmt.Errorf("%w: close requires UTF-8 actor, reason and session", storage.ErrValidation)
	}
	var result IssueMutationResult
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		before, err := s.requireIssueInTx(ctx, tx, request.Path)
		if err != nil {
			return err
		}
		// The graph token guards the complete Issue/owned-Link projection. Pass
		// the native row lock too, so the Issue writer remains the sole owner
		// of lifecycle validation and its exactly-once History stamp.
		if err := checkRevisionGuard(request.ExpectedRevision, false, before.Revision, false, "Issue"); err != nil {
			return err
		}
		if before.Properties.Status == types.StatusClosed {
			result = IssueMutationResult{Issue: before}
			return nil
		}
		if !request.Force && before.Properties.IssueType == types.TypeGate &&
			(strings.HasPrefix(before.Properties.AwaitType, "gh:pr") || strings.HasPrefix(before.Properties.AwaitType, "gh:run") ||
				before.Properties.AwaitType == "timer" || before.Properties.AwaitType == "bead") {
			return fmt.Errorf("%w: graph close cannot yet evaluate this gate; use --force only after reviewing its condition", ErrCapabilityUnavailable)
		}
		if err := validation.Chain(validation.NotTemplate(), validation.NotPinned(request.Force), validation.AssigneeMatches(request.Actor, request.Force))(before.Properties.ID, before.Properties); err != nil {
			return fmt.Errorf("%w: %v", storage.ErrValidation, err)
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		unscope := issueops.ScopeVersionedHistoryTransaction(tx, true)
		defer unscope()
		closeRequest := publicops.CloseRequest{IssueID: before.Properties.ID, Reason: request.Reason, Actor: request.Actor, Session: request.Session, Force: request.Force}
		if request.ExpectedRevision != "" {
			closeRequest.ExpectedVersion = &before.Properties.RowVersion
		}
		closed, _, err := issueops.ExecuteClose(ctx, tx, closeRequest)
		if err != nil {
			return err
		}
		if !closed.Changed {
			return fmt.Errorf("%w: close unexpectedly became a no-op", ErrInvalidStore)
		}
		if err := s.afterStage("close"); err != nil {
			return err
		}
		if err := s.recordIssueMappingInTx(ctx, tx, request.Path, before.Properties.ID, request.Actor); err != nil {
			return err
		}
		after, err := s.showIssueInTx(ctx, tx, request.Path)
		if err != nil {
			return err
		}
		result = IssueMutationResult{Issue: after, Changed: true, OpenChildren: closed.OpenChildren}
		return nil
	})
	if err != nil {
		return IssueMutationResult{}, err
	}
	return result, nil
}

// NewlyUnblockedByClose projects the native post-close dependency query into
// canonical graph records. It is a separate read: a suggestion failure never
// rolls back a close that has already committed.
func (s *Store) NewlyUnblockedByClose(ctx context.Context, path string) ([]IssueRecord, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}
	result := []IssueRecord{}
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		closed, err := s.requireIssueInTx(ctx, tx, path)
		if err != nil {
			return err
		}
		if closed.Properties.Status != types.StatusClosed {
			return fmt.Errorf("%w: suggestions require a closed Issue", storage.ErrValidation)
		}
		unblocked, err := issueops.GetNewlyUnblockedByCloseInTx(ctx, tx, closed.Properties.ID)
		if err != nil {
			return err
		}
		for _, issue := range unblocked {
			var candidatePath string
			if err := tx.QueryRowContext(ctx, `SELECT path FROM graph_preview_catalog WHERE backing='issue' AND backing_key=? AND allocation_state='live'`, issue.ID).Scan(&candidatePath); err != nil {
				return fmt.Errorf("%w: unmapped newly unblocked Issue: %v", ErrInvalidStore, err)
			}
			candidate, err := s.showIssueInTx(ctx, tx, candidatePath)
			if err != nil {
				return err
			}
			result = append(result, candidate)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// IssueDeferralRequest changes one durable Issue's deferral state. An optional
// graph revision guard is checked before the native Issue writer records the
// sole retained successor.
type IssueDeferralRequest struct {
	Path, Actor, ExpectedRevision string
	Unconditional, Deferred       bool
	Until                         *time.Time
	Reason                        string
}

// SetIssueDeferred uses the existing native Issue update funnel. The native
// writer owns its History stamp; graphstore records only the resulting graph
// projection in the same transaction.
func (s *Store) SetIssueDeferred(ctx context.Context, request IssueDeferralRequest) (IssueMutationResult, error) {
	if err := validatePath(request.Path); err != nil {
		return IssueMutationResult{}, fmt.Errorf("%w: %v", storage.ErrValidation, err)
	}
	if request.Actor == "" || !utf8.ValidString(request.Actor) {
		return IssueMutationResult{}, fmt.Errorf("%w: deferral requires a nonempty UTF-8 actor", storage.ErrValidation)
	}
	if !utf8.ValidString(request.Reason) || (!request.Deferred && (request.Until != nil || request.Reason != "")) {
		return IssueMutationResult{}, fmt.Errorf("%w: invalid deferral options", storage.ErrValidation)
	}
	var result IssueMutationResult
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		before, err := s.requireIssueInTx(ctx, tx, request.Path)
		if err != nil {
			return err
		}
		if err := checkRevisionGuard(request.ExpectedRevision, request.Unconditional, before.Revision, false, "Issue"); err != nil {
			return err
		}
		untilMatches := (request.Until == nil && before.Properties.DeferUntil == nil) ||
			(request.Until != nil && before.Properties.DeferUntil != nil && request.Until.Equal(*before.Properties.DeferUntil))
		if (request.Deferred && before.Properties.Status == types.StatusDeferred && untilMatches && request.Reason == "") ||
			(!request.Deferred && before.Properties.Status != types.StatusDeferred && before.Properties.DeferUntil == nil) {
			result = IssueMutationResult{Issue: before}
			return nil
		}
		patch := publicops.IssuePatch{DeferUntil: publicops.Field[*time.Time]{Set: true}}
		if request.Deferred {
			patch.Status = publicops.Field[types.Status]{Set: true, Value: types.StatusDeferred}
			patch.DeferUntil.Value = request.Until
			if request.Reason != "" {
				patch.AppendNotes = publicops.Field[string]{Set: true, Value: request.Reason}
			}
		} else if before.Properties.Status == types.StatusDeferred {
			patch.Status = publicops.Field[types.Status]{Set: true, Value: types.StatusOpen}
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		unscope := issueops.ScopeVersionedHistoryTransaction(tx, true)
		defer unscope()
		updated, _, err := issueops.ExecuteUpdate(ctx, tx, publicops.UpdateRequest{
			IssueID: before.Properties.ID, Actor: request.Actor, Patch: patch, IssuePlaneOnly: true,
		})
		if err != nil {
			return err
		}
		if !updated.Changed {
			return fmt.Errorf("%w: deferral unexpectedly became a no-op", ErrInvalidStore)
		}
		if err := s.afterStage("issue-deferral"); err != nil {
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

// wakeExpiredDefersAdvisory mirrors the native ready-front behavior. The
// Issue-domain sweep owns each wake event and History successor; this adapter
// only retains the corresponding graph projection in the same transaction.
// Failure is advisory to the subsequent read, as in the ordinary backends.
func (s *Store) wakeExpiredDefersAdvisory(ctx context.Context) {
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		var due int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE status='deferred' AND defer_until IS NOT NULL AND defer_until<=UTC_TIMESTAMP()`).Scan(&due); err != nil {
			return err
		}
		if due == 0 {
			return nil
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		unscope := issueops.ScopeVersionedHistoryTransaction(tx, true)
		defer unscope()
		woke, err := issueops.WakeExpiredDefersInTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range woke.Issues {
			var path string
			if err := tx.QueryRowContext(ctx, `SELECT path FROM graph_preview_catalog WHERE backing='issue' AND backing_key=? AND allocation_state='live'`, id).Scan(&path); err != nil {
				return fmt.Errorf("%w: unmapped woken Issue: %v", ErrInvalidStore, err)
			}
			if err := s.recordIssueMappingInTx(ctx, tx, path, id, issueops.DeferWakeActor); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "warning: graph defer-wake sweep skipped: %s\n", strings.TrimSpace(err.Error()))
	}
}

// ReadyIssues uses the native lazy defer wake and scheduling query, then
// resolves every result into the same canonical graph.
func (s *Store) ReadyIssues(ctx context.Context) ([]IssueRecord, error) {
	return s.ReadyIssuesFiltered(ctx, types.WorkFilter{})
}

// ReadyIssuesFiltered applies the ordinary ready predicate before projecting
// the selected native Issues into their canonical graph records.
func (s *Store) ReadyIssuesFiltered(ctx context.Context, filter types.WorkFilter) ([]IssueRecord, error) {
	s.wakeExpiredDefersAdvisory(ctx)
	result := []IssueRecord{}
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		ready, err := issueops.GetReadyWorkInTx(ctx, tx, filter)
		if err != nil {
			return err
		}
		for _, issue := range ready {
			var path string
			if err := tx.QueryRowContext(ctx, `SELECT path FROM graph_preview_catalog WHERE backing='issue' AND backing_key=? AND allocation_state='live'`, issue.ID).Scan(&path); err != nil {
				return fmt.Errorf("%w: unmapped ready Issue: %v", ErrInvalidStore, err)
			}
			record, err := s.showIssueInTx(ctx, tx, path)
			if err != nil {
				return err
			}
			result = append(result, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
