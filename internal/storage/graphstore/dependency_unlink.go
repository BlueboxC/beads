package graphstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

// validDeletedLinkAllocation separates reserved canonical identity from current
// authority. Dependency row IDs are deterministic per pair; clearing the deleted
// allocation's backing key permits a later pair assertion under a NEW Link ID.
func (s *Store) validDeletedLinkAllocation(kind, typ, backing string, key sql.NullString) bool {
	return kind == "link" && !key.Valid &&
		((backing == "informational" && IsInformationalTypeURL(s.ScopeURL(), typ)) ||
			(backing == "dependency" && typ == DependencyTypeURL(s.ScopeURL())))
}

// unlinkDependencyInTx extends the existing guarded unlink transaction only for
// a checked live canonical blocking Link. The Issue-domain removal remains the
// sole writer of Dependency rows and derived readiness state.
func (s *Store) unlinkDependencyInTx(ctx context.Context, tx *sql.Tx, path string, link LinkRecord, request LinkDeleteRequest) (LinkDeleteResult, error) {
	if err := checkRevisionGuard(request.ExpectedRevision, request.Unconditional, link.Revision, true, "Link"); err != nil {
		return LinkDeleteResult{}, err
	}
	sourcePath := strings.TrimPrefix(link.Source, s.ScopeURL())
	targetPath := strings.TrimPrefix(link.Target, s.ScopeURL())
	source, err := s.requireIssueInTx(ctx, tx, sourcePath)
	if err != nil {
		return LinkDeleteResult{}, err
	}
	target, err := s.requireIssueInTx(ctx, tx, targetPath)
	if err != nil {
		return LinkDeleteResult{}, err
	}
	// Issue informational Links remain unowned. This blocking Type is owned,
	// so the source guard is required independently of beadEndpointInTx's flag.
	if err := checkRevisionGuard(request.ExpectedSourceRevision, request.UnconditionalSource, source.Revision, true, "source"); err != nil {
		return LinkDeleteResult{}, err
	}
	removal := publicops.RemoveDependencyRequest{Actor: request.Actor, IssueID: source.Properties.ID, DependsOnID: target.Properties.ID}
	if err := issueops.ValidateRemoveDependencyRequest(removal); err != nil {
		return LinkDeleteResult{}, err
	}
	// A specialized Dependency must not also have a generic Bead or Link payload.
	var payloads int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM graph_preview_links WHERE path=?) + (SELECT COUNT(*) FROM graph_preview_payloads WHERE path=?)`, path, path).Scan(&payloads); err != nil {
		return LinkDeleteResult{}, err
	}
	if payloads != 0 {
		return LinkDeleteResult{}, fmt.Errorf("%w: Dependency has a generic payload", ErrInvalidStore)
	}
	if err := s.touchCoordination(ctx, tx); err != nil {
		return LinkDeleteResult{}, err
	}
	unscope := issueops.ScopeVersionedHistoryTransaction(tx, true)
	defer unscope()
	removed, _, err := issueops.ExecuteRemoveDependency(ctx, tx, removal)
	if err != nil {
		return LinkDeleteResult{}, err
	}
	if !removed.Removed {
		return LinkDeleteResult{}, fmt.Errorf("%w: checked live Dependency disappeared", ErrInvalidStore)
	}
	if err := s.afterStage("dependency-remove"); err != nil {
		return LinkDeleteResult{}, err
	}
	revision, err := freshToken()
	if err != nil {
		return LinkDeleteResult{}, err
	}
	tombstone := LinkTombstone{ID: link.ID, Type: link.Type, Revision: revision, Version: revision, State: "deleted", PreviousVersion: link.Version, Attribution: writeAttribution(request.Actor)}
	if _, err := tx.ExecContext(ctx, `UPDATE graph_preview_catalog SET allocation_state='deleted',revision=?,backing_key=NULL WHERE path=?`, revision, path); err != nil {
		return LinkDeleteResult{}, err
	}
	if err := s.afterStage("link-catalog"); err != nil {
		return LinkDeleteResult{}, err
	}
	snapshot, err := canonicalJSON(tombstone)
	if err != nil {
		return LinkDeleteResult{}, err
	}
	if err := insertPreviewVersionInTx(ctx, tx, path, revision, snapshot, request.Actor); err != nil {
		return LinkDeleteResult{}, err
	}
	if err := s.afterStage("link-retained"); err != nil {
		return LinkDeleteResult{}, err
	}
	// The target shared writer owns the one native retained version.
	if err := s.afterStage("issue-retained"); err != nil {
		return LinkDeleteResult{}, err
	}
	if err := s.recordIssueMappingInTx(ctx, tx, sourcePath, source.Properties.ID, request.Actor); err != nil {
		return LinkDeleteResult{}, err
	}
	current, err := s.showIssueInTx(ctx, tx, sourcePath)
	if err != nil {
		return LinkDeleteResult{}, err
	}
	return LinkDeleteResult{Link: tombstone, Source: current, Changed: true}, nil
}
