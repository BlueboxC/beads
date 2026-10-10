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
	"github.com/steveyegge/beads/internal/graphpatch"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

func informationalProperties(properties map[string]any) ([]byte, error) {
	if properties == nil {
		properties = map[string]any{}
	}
	for key, value := range properties {
		note, ok := value.(string)
		if key != "note" || !ok || !utf8.ValidString(note) {
			return nil, fmt.Errorf("%w: informational Link properties admit only optional UTF-8 string note", storage.ErrValidation)
		}
	}
	return canonicalJSON(properties)
}

func checkRevisionGuard(expected string, unconditional bool, current string, required bool, subject string) error {
	if (expected != "" && unconditional) || (required && expected == "" && !unconditional) {
		return fmt.Errorf("%w: %s requires exactly one revision guard or explicit unconditional choice", storage.ErrValidation, subject)
	}
	if expected != "" && expected != current {
		return fmt.Errorf("%w: %s revision changed (current %s)", ErrConflict, subject, current)
	}
	return nil
}

// beadEndpointInTx validates complete current/retained endpoint state. Memory
// ownership is a descriptor rule; informational Issue Links stay unowned.
func (s *Store) beadEndpointInTx(ctx context.Context, tx *sql.Tx, path string) (any, string, bool, error) {
	var backing string
	err := tx.QueryRowContext(ctx, `SELECT backing FROM graph_preview_catalog WHERE path=?`, path).Scan(&backing)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, ErrNotFound
	}
	if err != nil {
		return nil, "", false, err
	}
	switch backing {
	case "generic":
		r, err := s.showMemoryInTx(ctx, tx, path)
		return r, r.Revision, true, err
	case "issue":
		r, err := s.showIssueInTx(ctx, tx, path)
		return r, r.Revision, false, err
	default:
		return nil, "", false, fmt.Errorf("%w: informational endpoints must be live local Issue or Memory Beads", storage.ErrValidation)
	}
}

// AddInformationalLink creates independent identity even for equal endpoints.
// Link payload, retained Link and owned source snapshot commit together.
func (s *Store) AddInformationalLink(ctx context.Context, request LinkCreateRequest) (LinkMutationResult, error) {
	if request.TypeURL == "" {
		request.TypeURL = RelatedTypeURL(s.ScopeURL())
	}
	if !IsInformationalTypeURL(s.ScopeURL(), request.TypeURL) {
		return LinkMutationResult{}, fmt.Errorf("%w: informational Link Type is not supported", storage.ErrValidation)
	}
	for _, path := range []string{request.SourcePath, request.TargetPath} {
		if err := validatePath(path); err != nil {
			return LinkMutationResult{}, err
		}
	}
	if request.Path != "" {
		if err := validateLinkPath(request.Path); err != nil {
			return LinkMutationResult{}, err
		}
	}
	if !utf8.ValidString(request.Actor) {
		return LinkMutationResult{}, fmt.Errorf("%w: actor must be UTF-8", storage.ErrValidation)
	}
	properties, err := informationalProperties(request.Properties)
	if err != nil {
		return LinkMutationResult{}, err
	}
	metadata, err := commonMetadata(request.Metadata)
	if err != nil {
		return LinkMutationResult{}, err
	}
	var result LinkMutationResult
	err = s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		if err := s.selectedLinkTypeInTx(ctx, tx, request.TypeURL); err != nil {
			return err
		}
		source, revision, owned, err := s.beadEndpointInTx(ctx, tx, request.SourcePath)
		if err != nil {
			return err
		}
		if err := checkRevisionGuard(request.ExpectedSourceRevision, request.UnconditionalSource, revision, owned, "source"); err != nil {
			return err
		}
		if _, _, _, err := s.beadEndpointInTx(ctx, tx, request.TargetPath); err != nil {
			return err
		}
		if memory, ok := source.(Record); ok && len(memory.Owned) >= PreviewOwnedLinkLimit {
			return fmt.Errorf("%w: preview source-owned Link limit %d exceeded", storage.ErrValidation, PreviewOwnedLinkLimit)
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
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		linkRevision, err := freshToken()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO graph_preview_catalog (path,resource_kind,type_url,revision,allocation_state,backing) VALUES (?,'link',?,?,'live','informational')`, path, request.TypeURL, linkRevision); err != nil {
			return err
		}
		if err := s.afterStage("link-catalog"); err != nil {
			return err
		}
		attribution, err := canonicalJSON(writeAttribution(request.Actor))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO graph_preview_links (path,source_path,target_path,properties,metadata,attribution) VALUES (?,?,?,?,?,?)`, path, request.SourcePath, request.TargetPath, properties, metadata, attribution); err != nil {
			return err
		}
		if err := s.afterStage("link-payload"); err != nil {
			return err
		}
		result, err = s.finishInformationalWriteInTx(ctx, tx, path, request.SourcePath, request.Actor, source)
		if err == nil {
			result.ReplacedSource = replacedMemory(source, request.UnconditionalSource)
		}
		return err
	})
	if err != nil {
		return LinkMutationResult{}, err
	}
	return result, nil
}

// UpdateLink replaces the closed descriptor properties and checks both guards
// before recognizing a no-op. Endpoint/Type edits have no writer in this API.
func (s *Store) UpdateLink(ctx context.Context, request LinkUpdateRequest) (LinkMutationResult, error) {
	if err := validateLinkPath(request.Path); err != nil {
		return LinkMutationResult{}, err
	}
	if !utf8.ValidString(request.Actor) {
		return LinkMutationResult{}, fmt.Errorf("%w: actor must be UTF-8", storage.ErrValidation)
	}
	if request.MetadataOnly {
		if !hasCommonMetadataPatch(request.Metadata) {
			return LinkMutationResult{}, fmt.Errorf("%w: Link update requires metadata or properties", storage.ErrValidation)
		}
		if err := validateCommonMetadataPatch(request.Metadata); err != nil {
			return LinkMutationResult{}, err
		}
		return s.writeLinkProperties(ctx, request, nil, nil)
	}
	properties, err := informationalProperties(request.Properties)
	if err != nil {
		return LinkMutationResult{}, err
	}
	if err := validateCommonMetadataPatch(request.Metadata); err != nil {
		return LinkMutationResult{}, err
	}
	return s.writeLinkProperties(ctx, request, properties, nil)
}

// writeLinkProperties is the single checked writer for replacement and ordered
// patch intents. Parsed patches own their input; replacement bytes are captured
// before entering the transaction. Both guards precede evaluation and no-op.
func (s *Store) writeLinkProperties(ctx context.Context, request LinkUpdateRequest, properties []byte, patch *graphpatch.Patch) (LinkMutationResult, error) {
	if patch != nil && properties != nil {
		return LinkMutationResult{}, fmt.Errorf("%w: Link replacement and ordered patch are mutually exclusive", storage.ErrValidation)
	}
	if request.MergeProperties && (properties == nil || patch != nil) {
		return LinkMutationResult{}, fmt.Errorf("%w: Link property merge requires one properties object", storage.ErrValidation)
	}
	if err := validateCommonMetadataPatch(request.Metadata); err != nil {
		return LinkMutationResult{}, err
	}
	var result LinkMutationResult
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		link, err := s.showLinkInTx(ctx, tx, request.Path)
		if err != nil {
			return err
		}
		if !IsInformationalTypeURL(s.ScopeURL(), link.Type) {
			return fmt.Errorf("%w: property update requires informational Link Type", storage.ErrValidation)
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
		if patch != nil {
			if note, ok := link.Properties["note"].(string); ok && len(note) > graphpatch.MaxDocumentBytes {
				return fmt.Errorf("%w: Link properties exceed the patch working-document limit", ErrLimitExceeded)
			}
		}
		before, err := canonicalJSON(link.Properties)
		if err != nil {
			return err
		}
		nextProperties := properties
		if request.MergeProperties {
			var delta map[string]any
			if err := json.Unmarshal(properties, &delta); err != nil {
				return err
			}
			merged := make(map[string]any, len(link.Properties)+len(delta))
			for key, value := range link.Properties {
				merged[key] = value
			}
			for key, value := range delta {
				merged[key] = value
			}
			nextProperties, err = informationalProperties(merged)
			if err != nil {
				return err
			}
		}
		if nextProperties == nil && patch == nil {
			nextProperties = before
		}
		if patch != nil {
			nextProperties, err = applyLinkPropertiesPatch(patch, before)
			if err != nil {
				return err
			}
		}
		metadata, metadataChanged, err := issueops.ApplyMetadataPatch(link.Metadata, request.Metadata)
		if err != nil {
			return err
		}
		metadata, err = commonMetadata(metadata)
		if err != nil {
			return err
		}
		if bytes.Equal(before, nextProperties) && !metadataChanged {
			result = LinkMutationResult{Link: link, Source: source}
			return nil
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		linkRevision, err := freshToken()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE graph_preview_catalog SET revision=? WHERE path=?`, linkRevision, request.Path); err != nil {
			return err
		}
		if err := s.afterStage("link-catalog"); err != nil {
			return err
		}
		attribution, err := canonicalJSON(writeAttribution(request.Actor))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE graph_preview_links SET properties=?, metadata=?, attribution=? WHERE path=?`, nextProperties, metadata, attribution, request.Path); err != nil {
			return err
		}
		if err := s.afterStage("link-payload"); err != nil {
			return err
		}
		result, err = s.finishInformationalWriteInTx(ctx, tx, request.Path, sourcePath, request.Actor, source)
		if err == nil {
			result.ReplacedSource = replacedMemory(source, request.UnconditionalSource)
		}
		return err
	})
	if err != nil {
		return LinkMutationResult{}, err
	}
	return result, nil
}

func writeAttribution(actor string) Attribution {
	status := "unknown"
	if actor != "" {
		status = "claimed"
	}
	return Attribution{Actor: actor, Status: status, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)}
}

func (s *Store) finishInformationalWriteInTx(ctx context.Context, tx *sql.Tx, path, sourcePath, actor string, source any) (LinkMutationResult, error) {
	link, err := s.currentInformationalLinkInTx(ctx, tx, path)
	if err != nil {
		return LinkMutationResult{}, err
	}
	snapshot, err := canonicalJSON(link)
	if err != nil {
		return LinkMutationResult{}, err
	}
	if err := insertPreviewVersionInTx(ctx, tx, path, link.Version, snapshot, actor); err != nil {
		return LinkMutationResult{}, err
	}
	if err := s.afterStage("link-retained"); err != nil {
		return LinkMutationResult{}, err
	}
	source, err = s.recordOwnedMemoryInTx(ctx, tx, sourcePath, actor, source)
	if err != nil {
		return LinkMutationResult{}, err
	}
	return LinkMutationResult{Link: link, Source: source, Changed: true}, nil
}

func (s *Store) recordOwnedMemoryInTx(ctx context.Context, tx *sql.Tx, sourcePath, actor string, source any) (any, error) {
	var err error
	if memory, ok := source.(Record); ok {
		memory.Owned, err = s.memoryOwnedLinksInTx(ctx, tx, sourcePath)
		if err != nil {
			return nil, err
		}
		memory.Revision, err = freshToken()
		if err != nil {
			return nil, err
		}
		memory.Version = memory.Revision
		memory.Attribution = writeAttribution(actor)
		if _, err := tx.ExecContext(ctx, `UPDATE graph_preview_catalog SET revision=? WHERE path=?`, memory.Revision, sourcePath); err != nil {
			return nil, err
		}
		if err := s.afterStage("source-catalog"); err != nil {
			return nil, err
		}
		snapshot, err := canonicalJSON(memory)
		if err != nil {
			return nil, err
		}
		if err := insertPreviewVersionInTx(ctx, tx, sourcePath, memory.Version, snapshot, actor); err != nil {
			return nil, err
		}
		if err := s.afterStage("source-retained"); err != nil {
			return nil, err
		}
		source, err = s.showMemoryInTx(ctx, tx, sourcePath)
		if err != nil {
			return nil, err
		}
	}
	return source, nil
}

func (s *Store) currentInformationalLinkInTx(ctx context.Context, tx *sql.Tx, path string) (LinkRecord, error) {
	var kind, typ, revision, state, backing string
	var key sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT resource_kind,type_url,revision,allocation_state,backing,backing_key FROM graph_preview_catalog WHERE path=?`, path).Scan(&kind, &typ, &revision, &state, &backing, &key); err != nil {
		return LinkRecord{}, err
	}
	if kind != "link" || !IsInformationalTypeURL(s.ScopeURL(), typ) || !authorityID.MatchString(revision) || state != "live" || backing != "informational" || key.Valid {
		return LinkRecord{}, fmt.Errorf("%w: invalid informational Link allocation", ErrInvalidStore)
	}
	if err := s.informationalTypeInTx(ctx, tx, typ); err != nil {
		return LinkRecord{}, err
	}
	var source, target string
	var properties, metadata, attribution []byte
	if err := tx.QueryRowContext(ctx, `SELECT source_path,target_path,properties,metadata,attribution FROM graph_preview_links WHERE path=?`, path).Scan(&source, &target, &properties, &metadata, &attribution); err != nil {
		return LinkRecord{}, fmt.Errorf("%w: missing informational backing: %v", ErrInvalidStore, err)
	}
	for _, endpoint := range []string{source, target} {
		var kind, typ, state, backing string
		if err := tx.QueryRowContext(ctx, `SELECT resource_kind,type_url,allocation_state,backing FROM graph_preview_catalog WHERE path=?`, endpoint).Scan(&kind, &typ, &state, &backing); err != nil {
			return LinkRecord{}, fmt.Errorf("%w: missing endpoint: %v", ErrInvalidStore, err)
		}
		if validatePath(endpoint) != nil || kind != "bead" || state != "live" || !((backing == "generic" && typ == MemoryTypeURL(s.options.Binding.ScopeURL)) || (backing == "issue" && typ == IssueTypeURL(s.options.Binding.ScopeURL))) {
			return LinkRecord{}, fmt.Errorf("%w: invalid informational endpoint", ErrInvalidStore)
		}
	}
	r := LinkRecord{ID: graph.CanonicalURL(s.options.Binding.ScopeURL, path), Type: typ, Revision: revision, Version: revision, Source: graph.CanonicalURL(s.options.Binding.ScopeURL, source), Target: graph.CanonicalURL(s.options.Binding.ScopeURL, target)}
	canonicalMetadata, err := commonMetadata(metadata)
	if err != nil || !bytes.Equal(canonicalMetadata, metadata) {
		return LinkRecord{}, fmt.Errorf("%w: invalid informational metadata", ErrInvalidStore)
	}
	r.Metadata = canonicalMetadata
	if json.Unmarshal(properties, &r.Properties) != nil || json.Unmarshal(attribution, &r.Attribution) != nil {
		return LinkRecord{}, fmt.Errorf("%w: malformed informational payload", ErrInvalidStore)
	}
	p, err := informationalProperties(r.Properties)
	if err != nil || !bytes.Equal(p, properties) {
		return LinkRecord{}, fmt.Errorf("%w: invalid informational properties", ErrInvalidStore)
	}
	a, err := canonicalJSON(r.Attribution)
	if err != nil || !bytes.Equal(a, attribution) {
		return LinkRecord{}, fmt.Errorf("%w: invalid informational attribution", ErrInvalidStore)
	}
	at, err := time.Parse(time.RFC3339Nano, r.Attribution.RecordedAt)
	if err != nil || at.UTC().Format(time.RFC3339Nano) != r.Attribution.RecordedAt || !utf8.ValidString(r.Attribution.Actor) || (r.Attribution.Actor == "" && r.Attribution.Status != "unknown") || (r.Attribution.Actor != "" && r.Attribution.Status != "claimed") {
		return LinkRecord{}, fmt.Errorf("%w: invalid informational attribution", ErrInvalidStore)
	}
	return r, nil
}

func (s *Store) memoryOwnedLinksInTx(ctx context.Context, tx *sql.Tx, sourcePath string) ([]json.RawMessage, error) {
	rows, err := tx.QueryContext(ctx, `SELECT l.path FROM graph_preview_links l WHERE l.source_path=?`, sourcePath)
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
