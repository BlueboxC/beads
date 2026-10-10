package graphstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// These tables are deliberately preview-specific. They share the standard
// Beads database; Issue and Dependency payloads are not copied into them.
// The catalog alone owns allocation and current revision. A later specialized
// Issue provider must participate in this same transaction/identity model.
var previewDDL = []string{
	`CREATE TABLE graph_preview_links (
        path VARBINARY(1024) PRIMARY KEY, source_path VARBINARY(1024) NOT NULL,
        target_path VARBINARY(1024) NOT NULL, properties LONGBLOB NOT NULL,
        metadata LONGBLOB NULL,
        attribution LONGBLOB NOT NULL, INDEX source_links (source_path))`,
	`CREATE TABLE graph_preview_scope (
        singleton INT PRIMARY KEY, workspace LONGBLOB NOT NULL,
        scope_url LONGBLOB NOT NULL, authority_id VARBINARY(32) NOT NULL,
        schema_version INT NOT NULL, writer_token VARBINARY(32) NOT NULL)`,
	`CREATE TABLE graph_preview_types (
        name VARBINARY(64) PRIMARY KEY, descriptor LONGBLOB NOT NULL,
        fingerprint VARBINARY(64) NOT NULL)`,
	`CREATE TABLE graph_preview_catalog (
        path VARBINARY(1024) PRIMARY KEY, resource_kind VARBINARY(16) NOT NULL,
        type_url LONGBLOB NOT NULL, revision VARBINARY(32) NOT NULL,
        allocation_state VARBINARY(16) NOT NULL, backing VARBINARY(32) NOT NULL,
        backing_key VARBINARY(255) NULL, UNIQUE KEY one_backing (backing, backing_key))`,
	`CREATE TABLE graph_preview_payloads (
        path VARBINARY(1024) PRIMARY KEY, properties LONGBLOB NOT NULL,
        metadata LONGBLOB NULL)`,
	`CREATE TABLE graph_preview_versions (
        path VARBINARY(1024) NOT NULL, version VARBINARY(32) NOT NULL,
        snapshot LONGBLOB NOT NULL, actor LONGBLOB NOT NULL,
        ordinal BIGINT NOT NULL, change_at DATETIME(6) NOT NULL,
        PRIMARY KEY (path, version), UNIQUE KEY one_path_ordinal (path, ordinal))`,
	`CREATE TABLE graph_preview_issue_versions (
        path VARBINARY(1024) NOT NULL, version VARBINARY(32) NOT NULL,
        issue_id VARBINARY(255) NOT NULL, issue_revision BIGINT NOT NULL, owned LONGBLOB NOT NULL,
        PRIMARY KEY (path, version), UNIQUE KEY one_issue_version (issue_id, issue_revision))`,
}

// MemoryTypeURL is a Scope-local experimental descriptor, not the eventual
// production Memory Type URI. Its payload is title/body and it owns outgoing Links.
func MemoryTypeURL(scope string) string { return scope + "types/preview-memory-v2" }

func memoryDescriptor(scope string) (graph.TypeDescriptor, error) {
	owns, err := graph.NewWildcardOwnedLinkDecl(PreviewOwnedLinkLimit)
	if err != nil {
		return graph.TypeDescriptor{}, err
	}
	return graph.NewTypeDescriptor(graph.TypeDescriptorSpec{
		ID: MemoryTypeURL(scope), Name: "Experimental Memory v2", Describes: graph.KindBead,
		Description:  "Disposable C0 preview. The installed writer accepts exactly UTF-8 title and body strings, owns up to 1000 outgoing Links, and always records a complete retained snapshot. This descriptor does not claim the complete Memory or History contract.",
		OwnsOutgoing: []graph.OwnedLinkDecl{owns},
	})
}

// IssueTypeURL deliberately avoids settling the nominal Task/Bug discussion.
func IssueTypeURL(scope string) string { return scope + "types/preview-issue-v2" }

func issueDescriptor(scope string) (graph.TypeDescriptor, error) {
	owns, err := graph.NewOwnedLinkDecl(DependencyTypeURL(scope), "", PreviewOwnedLinkLimit)
	if err != nil {
		return graph.TypeDescriptor{}, err
	}
	return graph.NewTypeDescriptor(graph.TypeDescriptorSpec{
		ID: IssueTypeURL(scope), Name: "Experimental Issue v2", Describes: graph.KindBead,
		Description:  "Disposable specialized Issue adapter. Issue classification remains a property. Admits durable create, checked close, ready work and up to 1000 outgoing blocking Dependencies owned by their source. Complete canonical owned-Link state is retained with each Issue version. No production Type or complete History contract is established.",
		OwnsOutgoing: []graph.OwnedLinkDecl{owns},
	})
}

// DependencyTypeURL is a registered private blocking-Dependency Type.
func DependencyTypeURL(scope string) string { return scope + "types/preview-blocks-v1" }

func dependencyDescriptor(scope string) (graph.TypeDescriptor, error) {
	endpoint, err := graph.NewEndpointConstraint([]string{IssueTypeURL(scope)}, graph.ExternalNone)
	if err != nil {
		return graph.TypeDescriptor{}, err
	}
	return graph.NewTypeDescriptor(graph.TypeDescriptorSpec{
		ID: DependencyTypeURL(scope), Name: "Experimental blocking Dependency v1", Describes: graph.KindLink,
		Description: "Disposable Dependency adapter. Unpinned live local durable Issue endpoints; empty properties. The source depends on the target and owns this Link. Issue-domain pair uniqueness applies. No generic multiedge or production Dependency contract is established.",
		Source:      &endpoint, Target: &endpoint,
	})
}

// RelatedTypeURL names the private experimental informational Link descriptor.
func RelatedTypeURL(scope string) string { return scope + "types/preview-related-v2" }

func relatedDescriptor(scope string) (graph.TypeDescriptor, error) {
	// Endpoint conformsTo is a conjunction, not an alternative-Type list.
	// The preview admits only Issue and Memory Beads; an empty requirement set
	// truthfully accepts either without claiming it conforms to both Types.
	endpoint, err := graph.NewEndpointConstraint(nil, graph.ExternalNone)
	if err != nil {
		return graph.TypeDescriptor{}, err
	}
	return graph.NewTypeDescriptor(graph.TypeDescriptorSpec{
		ID: RelatedTypeURL(scope), Name: "Experimental informational Link v2", Describes: graph.KindLink,
		Description: "Disposable informational Link with live unpinned local Issue or Memory endpoints and independent multiedge identity. Closed properties record admits only optional UTF-8 string note. Memory sources own outgoing Links; Issue sources do not own this Type. No scheduling effect or production Type contract.",
		Source:      &endpoint, Target: &endpoint,
	})
}

func canonicalJSON(value any) ([]byte, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return graph.CanonicalizeJSON(b)
}

func freshToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func installPreview(ctx context.Context, conn *sql.Conn, o Options) (err error) {
	prefix := o.IssuePrefix
	if prefix == "" {
		prefix = "bd"
	}
	if !utf8.ValidString(prefix) || strings.TrimSpace(prefix) != prefix {
		return fmt.Errorf("%w: invalid Issue prefix", ErrInvalidStore)
	}
	// Provision only a fresh graph database. Ordinary workspace migrations and
	// their numbering remain unchanged.
	for _, ddl := range graphNativeHistoryDDL {
		if _, err = conn.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	for _, ddl := range previewDDL {
		if _, err = conn.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	token, err := freshToken()
	if err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	finished := false
	defer func() {
		if !finished {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	b := o.Binding
	if _, err = tx.ExecContext(ctx, `INSERT INTO graph_preview_scope
        (singleton, workspace, scope_url, authority_id, schema_version, writer_token)
        VALUES (1, ?, ?, ?, ?, ?)`, b.WorkspaceID, b.ScopeURL, b.AuthorityID, b.SchemaVersion, token); err != nil {
		return err
	}
	for _, definition := range previewTypeDefinitions() {
		descriptor, buildErr := definition.build(b.ScopeURL)
		if buildErr != nil {
			return buildErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO graph_preview_types (name, descriptor, fingerprint) VALUES (?, ?, ?)`, definition.name, descriptor.CanonicalJSON(), descriptor.Fingerprint()); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO config (`key`,value) VALUES ('issue_prefix', ?)", prefix); err != nil {
		return err
	}
	if _, err = issueops.ReadConfigPrefix(ctx, tx); err != nil {
		return err
	}
	if err = checkBinding(ctx, tx, o); err != nil {
		return err
	}
	err = tx.Commit()
	finished = true
	if err != nil {
		return errors.Join(ErrOutcomeUnknown, err)
	}
	return nil
}

func checkBinding(ctx context.Context, tx *sql.Tx, o Options) error {
	var b Binding
	var token string
	err := tx.QueryRowContext(ctx, `SELECT workspace, scope_url, authority_id,
        schema_version, writer_token FROM graph_preview_scope WHERE singleton = 1
 AND OCTET_LENGTH(workspace)=? AND OCTET_LENGTH(scope_url)=?`, len(o.Binding.WorkspaceID), len(o.Binding.ScopeURL)).Scan(
		&b.WorkspaceID, &b.ScopeURL, &b.AuthorityID, &b.SchemaVersion, &token)
	if err != nil {
		return fmt.Errorf("%w: read binding: %v", ErrInvalidStore, err)
	}
	if b != o.Binding || !authorityID.MatchString(token) {
		return fmt.Errorf("%w: persisted binding differs from workspace", ErrInvalidStore)
	}
	if _, err := installedPreviewTypes(ctx, tx, b.ScopeURL); err != nil {
		return err
	}
	if _, err := issueops.ReadConfigPrefix(ctx, tx); err != nil {
		return fmt.Errorf("%w: Issue configuration: %v", ErrInvalidStore, err)
	}
	// Reject incomplete bootstrap on open, even before a caller selects a row.
	for _, query := range []string{
		`SELECT path, resource_kind, type_url, revision, allocation_state, backing, backing_key FROM graph_preview_catalog LIMIT 0`,
		`SELECT path, properties, metadata FROM graph_preview_payloads LIMIT 0`,
		`SELECT path, source_path, target_path, properties, metadata, attribution FROM graph_preview_links LIMIT 0`,
		`SELECT path, version, snapshot, actor, ordinal, change_at FROM graph_preview_versions LIMIT 0`,
		`SELECT path, version, issue_id, issue_revision, owned FROM graph_preview_issue_versions LIMIT 0`,
		`SELECT issue_id, revision, durable_state, attribution_status FROM issue_versions LIMIT 0`,
	} {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("%w: incomplete schema: %v", ErrInvalidStore, err)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Native retained-version prerequisites adapted from the contributor schema.
// Init refuses existing databases; OpenExisting never executes these statements.
var graphNativeHistoryDDL = []string{
	`CREATE TABLE issue_versions (
issue_id VARCHAR(255) NOT NULL, revision BIGINT NOT NULL, epoch INT NOT NULL,
durable_state LONGBLOB, change_actor VARCHAR(255), change_agent VARCHAR(255),
change_message TEXT, change_at DATETIME(6) NOT NULL, removed_at DATETIME(6),
removed_reason VARCHAR(255), attribution_status VARCHAR(20) NOT NULL,
PRIMARY KEY (issue_id, revision))`,
	`CREATE TABLE store_epoch (id TINYINT NOT NULL DEFAULT 1, epoch INT NOT NULL DEFAULT 1,
bumped_at DATETIME, bumped_reason VARCHAR(255), PRIMARY KEY (id), CHECK (id = 1))`,
	`ALTER TABLE issues ADD COLUMN current_revision BIGINT NOT NULL DEFAULT 1`,
	`ALTER TABLE issues ADD COLUMN participation_generation BIGINT NULL`,
}
