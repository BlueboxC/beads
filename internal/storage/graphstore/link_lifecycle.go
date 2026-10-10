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
)

var (
	ErrCapabilityUnavailable = errors.New("graph preview capability is unavailable")
	ErrLimitExceeded         = errors.New("graph preview result limit exceeded")
)

// ListLinks returns complete, canonically ordered live incident state, or an
// explicit limit error. It performs no endpoint resolution outside this Scope.
func (s *Store) ListLinks(ctx context.Context, request LinksRequest) ([]LinkRecord, error) {
	if err := validatePath(request.BeadPath); err != nil {
		return nil, err
	}
	if request.Direction == "" {
		request.Direction = "both"
	}
	if request.Direction != "both" && request.Direction != "in" && request.Direction != "out" {
		return nil, fmt.Errorf("%w: direction must be in, out, or both", storage.ErrValidation)
	}
	if err := s.validateLinkType(request.TypeURL, true); err != nil {
		return nil, err
	}
	var links []LinkRecord
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		if request.TypeURL != "" {
			if err := s.selectedLinkTypeInTx(ctx, tx, request.TypeURL); err != nil {
				return err
			}
		}
		if _, _, _, err := s.beadEndpointInTx(ctx, tx, request.BeadPath); err != nil {
			return err
		}
		var err error
		links, err = s.incidentLinksInTx(ctx, tx, request, "")
		return err
	})
	if err != nil {
		return nil, err
	}
	return links, nil
}

func (s *Store) validateLinkType(typ string, allowEmpty bool) error {
	if (allowEmpty && typ == "") || IsInformationalTypeURL(s.ScopeURL(), typ) || typ == DependencyTypeURL(s.options.Binding.ScopeURL) {
		return nil
	}
	return fmt.Errorf("%w: Link Type is not installed in this preview", storage.ErrValidation)
}

func (s *Store) incidentLinksInTx(ctx context.Context, tx *sql.Tx, request LinksRequest, targetPath string) ([]LinkRecord, error) {
	if err := s.checkLinkMappingsInTx(ctx, tx); err != nil {
		return nil, err
	}
	// Each authority projection emits a self-link once, without
	// limiting either backing independently or truncating a successful response.
	rows, err := tx.QueryContext(ctx, `SELECT path FROM (
 SELECT c.path, c.type_url, l.source_path, l.target_path
 FROM graph_preview_catalog c JOIN graph_preview_links l ON l.path=c.path
 WHERE c.allocation_state='live' AND c.backing='informational'
 UNION ALL
 SELECT c.path, c.type_url, src.path, dst.path
 FROM graph_preview_catalog c JOIN dependencies d ON d.id=c.backing_key
 JOIN graph_preview_catalog src ON src.backing='issue' AND src.backing_key=d.issue_id
 JOIN graph_preview_catalog dst ON dst.backing='issue' AND dst.backing_key=d.depends_on_issue_id
 WHERE c.allocation_state='live' AND c.backing='dependency'
 ) incident WHERE ((? IN ('out','both') AND source_path=?) OR (? IN ('in','both') AND target_path=?))
 AND (?='' OR type_url=?) AND (?='' OR target_path=?) LIMIT ?`, request.Direction, request.BeadPath, request.Direction, request.BeadPath, request.TypeURL, request.TypeURL, targetPath, targetPath, PreviewIncidentLinkLimit+1)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			_ = rows.Close()
			return nil, err
		}
		paths = append(paths, path)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if len(paths) > PreviewIncidentLinkLimit {
		return nil, fmt.Errorf("%w: at most %d incident Links can be returned; preview pagination is unavailable", ErrLimitExceeded, PreviewIncidentLinkLimit)
	}
	sort.Slice(paths, func(i, j int) bool { return graph.CompareCodeUnits(paths[i], paths[j]) < 0 })
	result := make([]LinkRecord, 0, len(paths))
	for _, path := range paths {
		link, err := s.showLinkInTx(ctx, tx, path)
		if err != nil {
			return nil, err
		}
		result = append(result, link)
	}
	return result, nil
}

// Unlink selects, checks both guards, tombstones identity and records its owning
// Bead in one transaction. It neither replays deletion nor reuses identities.
// Blocking Dependencies currently require canonical Link ID selection.
func (s *Store) Unlink(ctx context.Context, request LinkDeleteRequest) (LinkDeleteResult, error) {
	if request.DefaultInformationalSource && (request.ExpectedSourceRevision != "" || request.UnconditionalSource) {
		return LinkDeleteResult{}, fmt.Errorf("%w: default informational source acceptance cannot accompany an explicit source guard", storage.ErrValidation)
	}
	if !utf8.ValidString(request.Actor) {
		return LinkDeleteResult{}, fmt.Errorf("%w: actor must be UTF-8", storage.ErrValidation)
	}
	if request.Path != "" {
		if err := validateLinkPath(request.Path); err != nil {
			return LinkDeleteResult{}, err
		}
		if request.SourcePath != "" || request.TargetPath != "" || request.TypeURL != "" {
			return LinkDeleteResult{}, fmt.Errorf("%w: select a Link ID or an exact typed endpoint pair", storage.ErrValidation)
		}
	} else {
		for _, path := range []string{request.SourcePath, request.TargetPath} {
			if err := validatePath(path); err != nil {
				return LinkDeleteResult{}, err
			}
		}
		if err := s.validateLinkType(request.TypeURL, false); err != nil {
			return LinkDeleteResult{}, err
		}
		if request.TypeURL == DependencyTypeURL(s.options.Binding.ScopeURL) {
			return LinkDeleteResult{}, fmt.Errorf("%w: blocking Dependency unlink requires its canonical Link ID", ErrCapabilityUnavailable)
		}
	}
	var result LinkDeleteResult
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		if request.TypeURL != "" {
			if err := s.selectedLinkTypeInTx(ctx, tx, request.TypeURL); err != nil {
				return err
			}
		}
		path, err := s.selectUnlinkPathInTx(ctx, tx, request)
		if err != nil {
			return err
		}
		link, err := s.showLinkInTx(ctx, tx, path)
		if err != nil {
			return err
		}
		if link.Type == DependencyTypeURL(s.options.Binding.ScopeURL) {
			result, err = s.unlinkDependencyInTx(ctx, tx, path, link, request)
			return err
		}
		// Only informational Links reach this point. Resolve the CLI default
		// here, after transactional selection, without a whole-workspace read.
		if request.DefaultInformationalSource {
			request.UnconditionalSource = true
		}
		if err := checkRevisionGuard(request.ExpectedRevision, request.Unconditional, link.Revision, true, "Link"); err != nil {
			return err
		}
		sourcePath := strings.TrimPrefix(link.Source, s.options.Binding.ScopeURL)
		source, revision, owned, err := s.beadEndpointInTx(ctx, tx, sourcePath)
		if err != nil {
			return err
		}
		if err := checkRevisionGuard(request.ExpectedSourceRevision, request.UnconditionalSource, revision, owned, "source"); err != nil {
			return err
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		deletionRevision, err := freshToken()
		if err != nil {
			return err
		}
		tombstone := LinkTombstone{ID: link.ID, Type: link.Type, Revision: deletionRevision, Version: deletionRevision, State: "deleted", PreviousVersion: link.Version, Attribution: writeAttribution(request.Actor)}
		if _, err := tx.ExecContext(ctx, `UPDATE graph_preview_catalog SET allocation_state='deleted',revision=? WHERE path=?`, deletionRevision, path); err != nil {
			return err
		}
		if err := s.afterStage("link-catalog"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM graph_preview_links WHERE path=?`, path); err != nil {
			return err
		}
		if err := s.afterStage("link-payload"); err != nil {
			return err
		}
		snapshot, err := canonicalJSON(tombstone)
		if err != nil {
			return err
		}
		if err := insertPreviewVersionInTx(ctx, tx, path, deletionRevision, snapshot, request.Actor); err != nil {
			return err
		}
		if err := s.afterStage("link-retained"); err != nil {
			return err
		}
		replacedSource := replacedMemory(source, request.UnconditionalSource)
		source, err = s.recordOwnedMemoryInTx(ctx, tx, sourcePath, request.Actor, source)
		if err != nil {
			return err
		}
		result = LinkDeleteResult{Link: tombstone, Source: source, Changed: true, ReplacedSource: replacedSource}
		return nil
	})
	if err != nil {
		return LinkDeleteResult{}, err
	}
	return result, nil
}

func (s *Store) selectUnlinkPathInTx(ctx context.Context, tx *sql.Tx, request LinkDeleteRequest) (string, error) {
	if request.Path != "" {
		return request.Path, nil
	}
	for _, path := range []string{request.SourcePath, request.TargetPath} {
		if _, _, _, err := s.beadEndpointInTx(ctx, tx, path); err != nil {
			return "", err
		}
	}
	links, err := s.incidentLinksInTx(ctx, tx, LinksRequest{BeadPath: request.SourcePath, Direction: "out", TypeURL: request.TypeURL}, request.TargetPath)
	if err != nil {
		return "", err
	}
	candidates := []string{}
	for _, link := range links {
		if link.Target == graph.CanonicalURL(s.options.Binding.ScopeURL, request.TargetPath) {
			candidates = append(candidates, link.ID)
		}
	}
	if len(candidates) == 0 {
		return "", ErrNotFound
	}
	if len(candidates) > 1 {
		return "", &ErrAmbiguousLink{CandidateIDs: candidates}
	}
	return strings.TrimPrefix(candidates[0], s.options.Binding.ScopeURL), nil
}

// A valid tombstone is distinct from missing or corrupt state. Missing retained
// deletion/previous state must not be disguised as ordinary gone.
func (s *Store) deletedLinkErrorInTx(ctx context.Context, tx *sql.Tx, path string) error {
	var kind, typ, revision, backing string
	var key sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT resource_kind,type_url,revision,backing,backing_key FROM graph_preview_catalog WHERE path=?`, path).Scan(&kind, &typ, &revision, &backing, &key); err != nil {
		return err
	}
	if !s.validDeletedLinkAllocation(kind, typ, backing, key) || !authorityID.MatchString(revision) {
		return fmt.Errorf("%w: invalid deleted allocation", ErrInvalidStore)
	}
	var snapshot []byte
	var actor string
	if err := tx.QueryRowContext(ctx, `SELECT snapshot,actor FROM graph_preview_versions WHERE path=? AND version=?`, path, revision).Scan(&snapshot, &actor); err != nil {
		return fmt.Errorf("%w: missing deletion state: %v", ErrInvalidStore, err)
	}
	var tombstone LinkTombstone
	if err := json.Unmarshal(snapshot, &tombstone); err != nil {
		return fmt.Errorf("%w: malformed deletion state", ErrInvalidStore)
	}
	canonical, err := canonicalJSON(tombstone)
	at, timeErr := time.Parse(time.RFC3339Nano, tombstone.Attribution.RecordedAt)
	if err != nil || !bytes.Equal(snapshot, canonical) || tombstone.ID != graph.CanonicalURL(s.options.Binding.ScopeURL, path) || tombstone.Type != typ || tombstone.State != "deleted" || tombstone.Revision != revision || tombstone.Version != revision || !authorityID.MatchString(tombstone.PreviousVersion) || tombstone.PreviousVersion == revision || actor != tombstone.Attribution.Actor || !utf8.ValidString(actor) || timeErr != nil || at.UTC().Format(time.RFC3339Nano) != tombstone.Attribution.RecordedAt || (actor == "" && tombstone.Attribution.Status != "unknown") || (actor != "" && tombstone.Attribution.Status != "claimed") {
		return fmt.Errorf("%w: inconsistent deletion state", ErrInvalidStore)
	}
	var previous []byte
	var previousActor string
	if err := tx.QueryRowContext(ctx, `SELECT snapshot,actor FROM graph_preview_versions WHERE path=? AND version=?`, path, tombstone.PreviousVersion).Scan(&previous, &previousActor); err != nil {
		return fmt.Errorf("%w: missing previous Link state: %v", ErrInvalidStore, err)
	}
	var link LinkRecord
	if json.Unmarshal(previous, &link) != nil || link.ID != tombstone.ID || link.Type != typ || link.Revision != tombstone.PreviousVersion || link.Version != tombstone.PreviousVersion {
		return fmt.Errorf("%w: invalid previous Link state", ErrInvalidStore)
	}
	if err := s.validateVersionLink(ctx, tx, link, previous, previousActor); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM graph_preview_links WHERE path=?) + (SELECT COUNT(*) FROM graph_preview_payloads WHERE path=?)`, path, path).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("%w: deleted Link has current payload", ErrInvalidStore)
	}
	return ErrGone
}

// The preview has one controlled graph writer. Refuse incomplete authority
// mappings before a join can hide them as an apparently complete collection.
func (s *Store) checkLinkMappingsInTx(ctx context.Context, tx *sql.Tx) error {
	var invalidTypes int
	ids, err := s.installedInformationalTypes(ctx, tx)
	if err != nil {
		return err
	}
	placeholders, args := informationalTypeSQL(ids)
	args = append(args, DependencyTypeURL(s.ScopeURL()))
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_preview_catalog WHERE resource_kind='link' AND allocation_state='live' AND ((backing='informational' AND type_url NOT IN (`+placeholders+`)) OR (backing='dependency' AND type_url<>?))`, args...).Scan(&invalidTypes); err != nil {
		return err
	}
	if invalidTypes != 0 {
		return fmt.Errorf("%w: Link Type differs from its authority", ErrInvalidStore)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM graph_preview_catalog c LEFT JOIN graph_preview_links l ON l.path=c.path LEFT JOIN dependencies d ON d.id=c.backing_key
   WHERE c.resource_kind='link' AND c.allocation_state='live' AND
   ((c.backing='informational' AND l.path IS NULL) OR (c.backing='dependency' AND d.id IS NULL) OR c.backing NOT IN ('informational','dependency'))`,
		`SELECT COUNT(*) FROM graph_preview_links l LEFT JOIN graph_preview_catalog c ON c.path=l.path
   LEFT JOIN graph_preview_catalog src ON src.path=l.source_path LEFT JOIN graph_preview_catalog dst ON dst.path=l.target_path
   WHERE c.path IS NULL OR c.resource_kind<>'link' OR c.allocation_state<>'live' OR c.backing<>'informational'
    OR src.path IS NULL OR dst.path IS NULL OR src.resource_kind<>'bead' OR dst.resource_kind<>'bead' OR src.allocation_state<>'live' OR dst.allocation_state<>'live'`,
		`SELECT COUNT(*) FROM dependencies d LEFT JOIN graph_preview_catalog c ON c.backing='dependency' AND c.backing_key=d.id
   LEFT JOIN graph_preview_catalog src ON src.backing='issue' AND src.backing_key=d.issue_id
   LEFT JOIN graph_preview_catalog dst ON dst.backing='issue' AND dst.backing_key=d.depends_on_issue_id
   WHERE c.path IS NULL OR c.resource_kind<>'link' OR c.allocation_state<>'live' OR src.path IS NULL OR dst.path IS NULL
    OR src.resource_kind<>'bead' OR dst.resource_kind<>'bead' OR src.allocation_state<>'live' OR dst.allocation_state<>'live'`,
	} {
		var count int
		if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%w: incomplete Link authority mapping", ErrInvalidStore)
		}
	}
	return nil
}

// Retained references are validated as stored selectors, never by joining live
// endpoints. A deleted Link's historical evidence must remain independently readable.
func (s *Store) validateRetainedInformational(link LinkRecord, snapshot []byte, actor string) error {
	canonical, err := canonicalJSON(link)
	if err != nil || !bytes.Equal(canonical, snapshot) || link.Properties == nil {
		return fmt.Errorf("%w: noncanonical retained Link", ErrInvalidStore)
	}
	for _, endpoint := range []string{link.Source, link.Target} {
		path := strings.TrimPrefix(endpoint, s.options.Binding.ScopeURL)
		if validatePath(path) != nil || graph.CanonicalURL(s.options.Binding.ScopeURL, path) != endpoint {
			return fmt.Errorf("%w: invalid retained Link endpoint", ErrInvalidStore)
		}
	}
	if _, err := informationalProperties(link.Properties); err != nil {
		return fmt.Errorf("%w: invalid retained Link properties", ErrInvalidStore)
	}
	at, err := time.Parse(time.RFC3339Nano, link.Attribution.RecordedAt)
	if err != nil || at.UTC().Format(time.RFC3339Nano) != link.Attribution.RecordedAt || actor != link.Attribution.Actor || !utf8.ValidString(actor) || (actor == "" && link.Attribution.Status != "unknown") || (actor != "" && link.Attribution.Status != "claimed") {
		return fmt.Errorf("%w: invalid retained Link attribution", ErrInvalidStore)
	}
	return nil
}
