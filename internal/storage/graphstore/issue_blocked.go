package graphstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// BlockedIssue is a private preview presentation, not a persisted or BDP type.
// Issue preserves the complete existing record (including its inherited native
// properties.id); BlockedBy contains only canonical graph Issue IDs.
type BlockedIssue struct {
	Issue     IssueRecord `json:"issue"`
	BlockedBy []string    `json:"blockedBy"`
}

// BlockedIssues projects the existing dependency-blocked query into checked
// graph identities. It is not the complement of ReadyIssues. All admission,
// records and query membership come from one read transaction; nothing repairs
// or recomputes native blocking state. The current inventory bounds apply even
// when the resulting dependency-blocked view is empty.
func (s *Store) BlockedIssues(ctx context.Context) ([]BlockedIssue, error) {
	return s.BlockedIssuesFiltered(ctx, types.WorkFilter{})
}

// BlockedIssuesFiltered uses the native blocked predicate and keeps the
// complete graph admission and canonical ID checks in the same snapshot.
func (s *Store) BlockedIssuesFiltered(ctx context.Context, filter types.WorkFilter) ([]BlockedIssue, error) {
	var result []BlockedIssue
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = s.blockedIssuesFilteredInTx(ctx, tx, filter)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) blockedIssuesInTx(ctx context.Context, tx *sql.Tx) ([]BlockedIssue, error) {
	return s.blockedIssuesFilteredInTx(ctx, tx, types.WorkFilter{})
}

func (s *Store) blockedIssuesFilteredInTx(ctx context.Context, tx *sql.Tx, filter types.WorkFilter) ([]BlockedIssue, error) {
	snapshot, err := s.currentSnapshotInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	// Keep full authority admission first; an invalid workspace is not treated
	// as an otherwise valid workspace with unsupported wisp contents.
	// Native blocking spans both planes. A graph preview cannot silently omit
	// unmapped wisp authority; absent optional tables follow the native helper's
	// existing schema rules. Do not suppress other query failures.
	for _, query := range []string{"SELECT COUNT(*) FROM wisps", "SELECT COUNT(*) FROM wisp_dependencies"} {
		var count int
		if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
			if dberrors.IsTableNotExist(err) {
				continue
			}
			return nil, err
		}
		if count != 0 {
			return nil, fmt.Errorf("%w: graph blocked does not admit wisp authority", ErrInvalidStore)
		}
	}
	// Complete snapshot admission already checks mapping and retained authority.
	// The following native-result checks are defense in depth against a future
	// helper/admission mismatch, not separately reachable corruption fixtures.
	byNativeID := make(map[string]IssueRecord)
	for _, item := range snapshot.Records {
		issue, ok := item.(IssueRecord)
		if !ok {
			continue
		}
		if issue.Properties == nil || issue.Properties.ID == "" {
			return nil, fmt.Errorf("%w: incomplete blocked Issue mapping", ErrInvalidStore)
		}
		if _, duplicate := byNativeID[issue.Properties.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate blocked Issue mapping", ErrInvalidStore)
		}
		byNativeID[issue.Properties.ID] = issue
	}
	native, err := issueops.GetBlockedIssuesInTx(ctx, tx, filter)
	if err != nil {
		return nil, err
	}
	result := make([]BlockedIssue, 0, len(native))
	seen := make(map[string]bool)
	for _, blocked := range native {
		if blocked == nil || seen[blocked.ID] || blocked.BlockedByCount != len(blocked.BlockedBy) || len(blocked.BlockedBy) == 0 {
			return nil, fmt.Errorf("%w: incomplete native blocking result", ErrInvalidStore)
		}
		issue, ok := byNativeID[blocked.ID]
		if !ok {
			return nil, fmt.Errorf("%w: unmapped blocked Issue", ErrInvalidStore)
		}
		seen[blocked.ID] = true
		item := BlockedIssue{Issue: issue, BlockedBy: make([]string, 0, len(blocked.BlockedBy))}
		// This preview admits unique local blocks-only pairs and no wisp plane.
		// A duplicate violates that admitted invariant; do not silently dedupe a
		// future broader native result into an apparently supported graph view.
		blockers := make(map[string]bool)
		for _, id := range blocked.BlockedBy {
			target, ok := byNativeID[id]
			if !ok || blockers[id] {
				return nil, fmt.Errorf("%w: unmapped or duplicate blocker", ErrInvalidStore)
			}
			blockers[id] = true
			item.BlockedBy = append(item.BlockedBy, target.ID)
		}
		sort.Slice(item.BlockedBy, func(i, j int) bool { return graph.CompareCodeUnits(item.BlockedBy[i], item.BlockedBy[j]) < 0 })
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return graph.CompareCodeUnits(result[i].Issue.ID, result[j].Issue.ID) < 0 })
	return result, nil
}
