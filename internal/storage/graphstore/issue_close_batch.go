package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
	publicops "github.com/steveyegge/beads/issueops"
)

// IssueCloseBatchRequest keeps the native batch close and optional next claim
// inside one graph transaction. Reasons correspond to Paths in request order.
type IssueCloseBatchRequest struct {
	Paths, Reasons   []string
	Actor, Session   string
	Force, ClaimNext bool
}

type IssueCloseBatchOutcome struct {
	Path         string
	Issue        IssueRecord
	Changed      bool
	OpenChildren int
	Err          error
}

type IssueCloseBatchResult struct {
	Outcomes    []IssueCloseBatchOutcome
	ClaimedNext *IssueRecord
}

var errEmptyCloseBatch = errors.New("graph close batch changed nothing")

// CloseIssues uses the native Issue batch writer. A per-item refusal does not
// roll back successful siblings, while an all-refused or all-closed request
// rolls back even the graph coordination write and earns no next claim.
func (s *Store) CloseIssues(ctx context.Context, request IssueCloseBatchRequest) (IssueCloseBatchResult, error) {
	if len(request.Paths) == 0 || len(request.Paths) != len(request.Reasons) {
		return IssueCloseBatchResult{}, fmt.Errorf("%w: close batch requires paths and matching reasons", storage.ErrValidation)
	}
	if request.Actor == "" || !utf8.ValidString(request.Actor) || !utf8.ValidString(request.Session) {
		return IssueCloseBatchResult{}, fmt.Errorf("%w: close requires UTF-8 actor and session", storage.ErrValidation)
	}
	for i, path := range request.Paths {
		if err := validatePath(path); err != nil {
			return IssueCloseBatchResult{}, err
		}
		if !utf8.ValidString(request.Reasons[i]) {
			return IssueCloseBatchResult{}, fmt.Errorf("%w: close requires UTF-8 reason", storage.ErrValidation)
		}
	}
	result := IssueCloseBatchResult{Outcomes: make([]IssueCloseBatchOutcome, len(request.Paths))}
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		batch := publicops.CloseBatchRequest{Actor: request.Actor, Session: request.Session, Force: request.Force}
		indexes := make([]int, 0, len(request.Paths))
		anyOpen := false
		for i, path := range request.Paths {
			result.Outcomes[i].Path = path
			before, err := s.requireIssueInTx(ctx, tx, path)
			if err != nil {
				if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrGone) && !errors.Is(err, storage.ErrValidation) {
					return err
				}
				result.Outcomes[i].Err = err
				continue
			}
			if before.Properties.Status != types.StatusClosed {
				if !request.Force && before.Properties.IssueType == types.TypeGate &&
					(strings.HasPrefix(before.Properties.AwaitType, "gh:pr") || strings.HasPrefix(before.Properties.AwaitType, "gh:run") ||
						before.Properties.AwaitType == "timer" || before.Properties.AwaitType == "bead") {
					result.Outcomes[i].Err = fmt.Errorf("%w: graph close cannot yet evaluate this gate; use --force only after reviewing its condition", ErrCapabilityUnavailable)
					continue
				}
				if err := validation.Chain(validation.NotTemplate(), validation.NotPinned(request.Force), validation.AssigneeMatches(request.Actor, request.Force))(before.Properties.ID, before.Properties); err != nil {
					result.Outcomes[i].Err = fmt.Errorf("%w: %v", storage.ErrValidation, err)
					continue
				}
				anyOpen = true
			}
			result.Outcomes[i].Issue = before
			indexes = append(indexes, i)
			batch.Items = append(batch.Items, publicops.BatchCloseItem{IssueID: before.Properties.ID, Reason: request.Reasons[i]})
		}
		if !anyOpen {
			return errEmptyCloseBatch
		}
		if request.ClaimNext {
			batch.ClaimNext = &publicops.ReadyRequest{Sort: string(types.SortPolicyPriority)}
		}
		if err := issueops.ValidateCloseBatchRequest(batch); err != nil {
			return err
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		unscope := issueops.ScopeVersionedHistoryTransaction(tx, true)
		defer unscope()
		var claimFilter *types.WorkFilter
		if request.ClaimNext {
			claimFilter = &types.WorkFilter{SortPolicy: types.SortPolicyPriority}
		}
		closed, _, err := issueops.ExecuteCloseBatch(ctx, tx, batch, claimFilter)
		if err != nil {
			return err
		}
		changed := false
		for j, item := range closed.Outcomes {
			i := indexes[j]
			if item.Err != nil {
				result.Outcomes[i].Err = item.Err
				result.Outcomes[i].Issue = IssueRecord{}
				continue
			}
			result.Outcomes[i].Changed = item.Changed
			result.Outcomes[i].OpenChildren = item.OpenChildren
			if !item.Changed {
				// A duplicate ID may have been closed by an earlier batch item.
				// Report its post-state, not the open preflight snapshot.
				if result.Outcomes[i].Issue.Properties.Status != types.StatusClosed {
					after, err := s.showIssueInTx(ctx, tx, request.Paths[i])
					if err != nil {
						return err
					}
					result.Outcomes[i].Issue = after
				}
				continue
			}
			changed = true
			if err := s.recordIssueMappingInTx(ctx, tx, request.Paths[i], item.IssueID, request.Actor); err != nil {
				return err
			}
			after, err := s.showIssueInTx(ctx, tx, request.Paths[i])
			if err != nil {
				return err
			}
			result.Outcomes[i].Issue = after
		}
		if !changed {
			return errEmptyCloseBatch
		}
		if closed.ClaimedNext != nil {
			var path string
			if err := tx.QueryRowContext(ctx, `SELECT path FROM graph_preview_catalog WHERE backing='issue' AND backing_key=? AND allocation_state='live'`, closed.ClaimedNext.ID).Scan(&path); err != nil {
				return fmt.Errorf("%w: unmapped claimed Issue: %v", ErrInvalidStore, err)
			}
			if err := s.recordIssueMappingInTx(ctx, tx, path, closed.ClaimedNext.ID, request.Actor); err != nil {
				return err
			}
			claimed, err := s.showIssueInTx(ctx, tx, path)
			if err != nil {
				return err
			}
			result.ClaimedNext = &claimed
		}
		return s.afterStage("issue-close-batch")
	})
	if onlyEmptyCloseBatch(err) {
		return result, nil
	}
	if err != nil {
		return IssueCloseBatchResult{}, err
	}
	return result, nil
}

// withTx joins rollback and connection-close errors onto fn's error. Suppress
// only our deliberate no-op sentinel; a failed rollback/close must still be
// reported, even when the sentinel is one of the joined errors.
func onlyEmptyCloseBatch(err error) bool {
	if err == errEmptyCloseBatch {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !onlyEmptyCloseBatch(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyEmptyCloseBatch(wrapped.Unwrap())
	}
	return false
}
