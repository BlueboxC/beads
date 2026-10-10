package graphstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func validateResourcePath(path string) error {
	if strings.HasPrefix(path, "links/") {
		return validateLinkPath(path)
	}
	return validatePath(path)
}

func validateLinkPath(path string) error {
	if err := graph.ValidateLinkPath(path); err != nil {
		return err
	}
	if len(path) > 1024 {
		return fmt.Errorf("%w: preview Link path exceeds 1024 bytes", storage.ErrValidation)
	}
	return nil
}

// AddDependency asserts the ordinary Issue-domain blocking relation and its
// independently allocated canonical Link, atomically with source-owned History.
// Reasserting the same pair is an exact no-op; it never invokes the legacy
// same-pair metadata updater. This is not generic Link multiedge policy.
func (s *Store) AddDependency(ctx context.Context, request DependencyRequest) (DependencyResult, error) {
	for _, path := range []string{request.SourcePath, request.TargetPath} {
		if err := validatePath(path); err != nil {
			return DependencyResult{}, err
		}
	}
	if request.Path != "" {
		if err := validateLinkPath(request.Path); err != nil {
			return DependencyResult{}, err
		}
	}
	if request.Actor == "" || !utf8.ValidString(request.Actor) || request.SourcePath == request.TargetPath {
		return DependencyResult{}, fmt.Errorf("%w: Dependency requires an actor and two distinct local Issues", storage.ErrValidation)
	}
	var result DependencyResult
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		source, err := s.requireIssueInTx(ctx, tx, request.SourcePath)
		if err != nil {
			return err
		}
		if request.ExpectedSourceRevision != "" && request.ExpectedSourceRevision != source.Revision {
			return fmt.Errorf("%w: source revision changed (current %s)", ErrConflict, source.Revision)
		}
		target, err := s.requireIssueInTx(ctx, tx, request.TargetPath)
		if err != nil {
			return err
		}
		for _, raw := range source.Owned {
			var existing LinkRecord
			if err := json.Unmarshal(raw, &existing); err != nil {
				return err
			}
			if existing.Target == target.ID {
				if request.Path != "" && existing.ID != graph.CanonicalURL(s.options.Binding.ScopeURL, request.Path) {
					return fmt.Errorf("%w: Dependency pair already has canonical Link %s", storage.ErrValidation, existing.ID)
				}
				result = DependencyResult{Link: existing, Source: source, Changed: false}
				return nil
			}
		}
		if len(source.Owned) >= PreviewOwnedLinkLimit {
			return fmt.Errorf("%w: preview source-owned Link limit %d exceeded", storage.ErrValidation, PreviewOwnedLinkLimit)
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		path := request.Path
		if path == "" {
			token, err := freshToken()
			if err != nil {
				return err
			}
			path = "links/" + token
		}
		var exists int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM graph_preview_catalog WHERE path=?`, path).Scan(&exists)
		if err == nil {
			return ErrAlreadyExists
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		unscope := issueops.ScopeVersionedHistoryTransaction(tx, true)
		defer unscope()
		_, _, err = issueops.ExecuteAddDependencies(ctx, tx, publicops.AddDependenciesRequest{
			Actor: request.Actor, Edges: []publicops.DependencyEdge{{IssueID: source.Properties.ID, DependsOnID: target.Properties.ID, Type: types.DepBlocks}},
		})
		if err != nil {
			if errors.Is(err, publicops.ErrDependencyCycle) {
				return fmt.Errorf("%w: %w", storage.ErrValidation, err)
			}
			return err
		}
		if err := s.afterStage("dependency"); err != nil {
			return err
		}
		var key string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM dependencies WHERE issue_id=? AND depends_on_issue_id=?`, source.Properties.ID, target.Properties.ID).Scan(&key); err != nil {
			return err
		}
		revision, err := freshToken()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO graph_preview_catalog (path,resource_kind,type_url,revision,allocation_state,backing,backing_key) VALUES (?,'link',?,?,'live','dependency',?)`, path, DependencyTypeURL(s.options.Binding.ScopeURL), revision, key); err != nil {
			return err
		}
		if err := s.afterStage("link-catalog"); err != nil {
			return err
		}
		link, err := s.currentLinkInTx(ctx, tx, path)
		if err != nil {
			return err
		}
		snapshot, err := canonicalJSON(link)
		if err != nil {
			return err
		}
		if err := insertPreviewVersionInTx(ctx, tx, path, revision, snapshot, request.Actor); err != nil {
			return err
		}
		if err := s.afterStage("link-retained"); err != nil {
			return err
		}
		if err := s.recordIssueMappingInTx(ctx, tx, request.SourcePath, source.Properties.ID, request.Actor); err != nil {
			return err
		}
		changed, err := s.showIssueInTx(ctx, tx, request.SourcePath)
		if err != nil {
			return err
		}
		result = DependencyResult{Link: link, Source: changed, Changed: true}
		return nil
	})
	if err != nil {
		return DependencyResult{}, err
	}
	return result, nil
}

func (s *Store) requireIssueInTx(ctx context.Context, tx *sql.Tx, path string) (IssueRecord, error) {
	var backing string
	err := tx.QueryRowContext(ctx, `SELECT backing FROM graph_preview_catalog WHERE path=?`, path).Scan(&backing)
	if errors.Is(err, sql.ErrNoRows) {
		return IssueRecord{}, ErrNotFound
	}
	if err != nil {
		return IssueRecord{}, err
	}
	if backing != "issue" {
		return IssueRecord{}, fmt.Errorf("%w: operation requires a live durable Issue, not %s", storage.ErrValidation, backing)
	}
	return s.showIssueInTx(ctx, tx, path)
}

// currentLinkInTx selects the authoritative specialized or generic Link row.
// A retained snapshot never substitutes for current authority.
func (s *Store) currentLinkInTx(ctx context.Context, tx *sql.Tx, path string) (LinkRecord, error) {
	var selectedBacking string
	if err := tx.QueryRowContext(ctx, `SELECT backing FROM graph_preview_catalog WHERE path=?`, path).Scan(&selectedBacking); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LinkRecord{}, ErrNotFound
		}
		return LinkRecord{}, err
	}
	var selectedState string
	if err := tx.QueryRowContext(ctx, `SELECT allocation_state FROM graph_preview_catalog WHERE path=?`, path).Scan(&selectedState); err != nil {
		return LinkRecord{}, err
	}
	if selectedState == "deleted" {
		return LinkRecord{}, s.deletedLinkErrorInTx(ctx, tx, path)
	}
	if selectedBacking == "informational" {
		return s.currentInformationalLinkInTx(ctx, tx, path)
	}

	var kind, typ, revision, state, backing, key string
	err := tx.QueryRowContext(ctx, `SELECT resource_kind,type_url,revision,allocation_state,backing,backing_key FROM graph_preview_catalog WHERE path=?`, path).Scan(&kind, &typ, &revision, &state, &backing, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return LinkRecord{}, ErrNotFound
	}
	if err != nil {
		return LinkRecord{}, err
	}
	if kind != "link" || typ != DependencyTypeURL(s.options.Binding.ScopeURL) || !authorityID.MatchString(revision) || state != "live" || backing != "dependency" || key == "" {
		return LinkRecord{}, fmt.Errorf("%w: invalid Dependency allocation", ErrInvalidStore)
	}
	var sourcePath, targetPath, depType, metadata, thread, actor string
	var at time.Time
	err = tx.QueryRowContext(ctx, `SELECT src.path,dst.path,d.type,d.metadata,d.thread_id,d.created_by,d.created_at
 FROM dependencies d JOIN graph_preview_catalog src ON src.backing='issue' AND src.backing_key=d.issue_id AND src.allocation_state='live' AND src.resource_kind='bead' AND src.type_url=?
 JOIN graph_preview_catalog dst ON dst.backing='issue' AND dst.backing_key=d.depends_on_issue_id AND dst.allocation_state='live' AND dst.resource_kind='bead' AND dst.type_url=?
 WHERE d.id=? AND d.depends_on_wisp_id IS NULL AND d.depends_on_external IS NULL`, IssueTypeURL(s.options.Binding.ScopeURL), IssueTypeURL(s.options.Binding.ScopeURL), key).Scan(&sourcePath, &targetPath, &depType, &metadata, &thread, &actor, &at)
	if err != nil {
		return LinkRecord{}, fmt.Errorf("%w: Dependency backing: %v", ErrInvalidStore, err)
	}
	if depType != string(types.DepBlocks) || metadata != "{}" || thread != "" {
		return LinkRecord{}, fmt.Errorf("%w: Dependency exceeds preview", ErrInvalidStore)
	}
	status := "unknown"
	if actor != "" {
		status = "claimed"
	}
	return LinkRecord{ID: graph.CanonicalURL(s.options.Binding.ScopeURL, path), Type: typ, Revision: revision, Version: revision,
		Source: graph.CanonicalURL(s.options.Binding.ScopeURL, sourcePath), Target: graph.CanonicalURL(s.options.Binding.ScopeURL, targetPath), Properties: map[string]any{}, Metadata: []byte(`{}`),
		Attribution: Attribution{Actor: actor, Status: status, RecordedAt: at.UTC().Format(time.RFC3339Nano)}}, nil
}

func (s *Store) showLinkInTx(ctx context.Context, tx *sql.Tx, path string) (LinkRecord, error) {
	link, err := s.currentLinkInTx(ctx, tx, path)
	if err != nil {
		return LinkRecord{}, err
	}
	var retained []byte
	var actor string
	if err := tx.QueryRowContext(ctx, `SELECT snapshot,actor FROM graph_preview_versions WHERE path=? AND version=?`, path, link.Version).Scan(&retained, &actor); err != nil {
		return LinkRecord{}, fmt.Errorf("%w: missing Link retained state: %v", ErrInvalidStore, err)
	}
	canonical, err := canonicalJSON(link)
	if err != nil {
		return LinkRecord{}, err
	}
	if !bytes.Equal(canonical, retained) || actor != link.Attribution.Actor {
		return LinkRecord{}, fmt.Errorf("%w: current and retained Link state differ", ErrInvalidStore)
	}
	return link, nil
}

func (s *Store) ShowLink(ctx context.Context, path string) (LinkRecord, error) {
	if err := validateLinkPath(path); err != nil {
		return LinkRecord{}, err
	}
	var result LinkRecord
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		var err error
		result, err = s.showLinkInTx(ctx, tx, path)
		return err
	})
	if err != nil {
		return LinkRecord{}, err
	}
	return result, nil
}

// ownedLinksInTx refuses an unmapped or unsupported domain edge rather than
// silently omitting it. Source snapshots store these complete canonical bytes.
func (s *Store) ownedLinksInTx(ctx context.Context, tx *sql.Tx, issueID string) ([]json.RawMessage, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.path FROM dependencies d LEFT JOIN graph_preview_catalog c ON c.backing='dependency' AND c.backing_key=d.id AND c.allocation_state='live' WHERE d.issue_id=? ORDER BY c.path`, issueID)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for rows.Next() {
		var path sql.NullString
		if err := rows.Scan(&path); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if !path.Valid {
			_ = rows.Close()
			return nil, fmt.Errorf("%w: unmapped outgoing Dependency", ErrInvalidStore)
		}
		paths = append(paths, path.String)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if len(paths) > PreviewOwnedLinkLimit {
		return nil, fmt.Errorf("%w: owned Link limit exceeded", ErrInvalidStore)
	}
	sort.Slice(paths, func(i, j int) bool { return graph.CompareCodeUnits(paths[i], paths[j]) < 0 })
	owned := make([]json.RawMessage, 0, len(paths))
	for _, path := range paths {
		link, err := s.showLinkInTx(ctx, tx, path)
		if err != nil {
			return nil, err
		}
		raw, err := canonicalJSON(link)
		if err != nil {
			return nil, err
		}
		owned = append(owned, raw)
	}
	return owned, nil
}

// recordIssueMappingInTx retains the completed native mutation once, then maps
// its opaque graph identity and complete owned set. Ordinary writers do not
// activate retained history in this fork; snapshots remain in issue_versions.
func (s *Store) recordIssueMappingInTx(ctx context.Context, tx *sql.Tx, path, issueID, actor string) error {
	if err := issueops.RecordVersionInTx(ctx, tx, issueID, actor); err != nil {
		return err
	}
	var ordinal int64
	if err := tx.QueryRowContext(ctx, `SELECT current_revision FROM issues WHERE id=?`, issueID).Scan(&ordinal); err != nil {
		return err
	}
	revision, err := freshToken()
	if err != nil {
		return err
	}
	owned, err := s.ownedLinksInTx(ctx, tx, issueID)
	if err != nil {
		return err
	}
	snapshot, err := canonicalJSON(owned)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE graph_preview_catalog SET revision=? WHERE path=?`, revision, path); err != nil {
		return err
	}
	if err := s.afterStage("source-catalog"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO graph_preview_issue_versions (path,version,issue_id,issue_revision,owned) VALUES (?,?,?,?,?)`, path, revision, issueID, ordinal, snapshot); err != nil {
		return err
	}
	return s.afterStage("source-retained")
}
