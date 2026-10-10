package graphstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
)

// PreviewCurrentReadByteLimit bounds complete current acquisition before
// filtering or decoding. It includes allocation metadata, tombstones, repeated
// owner/Link reads and 256 bytes per row, but excludes older retained versions.
// It does not measure heap or SQL-engine memory. Writers enforce it before
// commit; oversized legacy state may shrink without discarding history.
const PreviewCurrentReadByteLimit = 16 << 20

type currentReadSizeQuery struct {
	from, columns string
	copies        int
}

// All SQL fragments are package-owned constants or the shared Issue hydration
// projection. No table/column name comes from a request or persisted payload.
func currentReadSizeQueries() []currentReadSizeQuery {
	const live = "c.allocation_state='live'"
	return []currentReadSizeQuery{
		{"graph_preview_scope WHERE singleton=1", "workspace,scope_url,authority_id,writer_token", 1},
		{"graph_preview_types", "name,descriptor,fingerprint", 1},
		{"config WHERE `key`='issue_prefix'", "value", 1},
		// Exact reads inspect allocation metadata before refusing deleted/invalid
		// states. Include tombstones; deleted Memory final heads are charged below.
		{"graph_preview_catalog c", "c.path,c.type_url,c.revision,c.resource_kind,c.backing,c.backing_key", 1},
		{"graph_preview_payloads p JOIN graph_preview_catalog c ON c.path=p.path WHERE " + live, "p.properties,p.metadata", 1},
		// A live Link is read as a top-level Resource and at most once as source-owned
		// state. The owner's retained snapshot/owned JSON is charged separately.
		// Owner enumeration acquires paths before validating their allocation.
		// Count orphan rows too, so corruption cannot create an unbounded path
		// slice before the existing mapping/authority refusal.
		{"graph_preview_links l", "l.path,l.source_path,l.target_path,l.properties,l.metadata,l.attribution", 2},
		{"graph_preview_versions v JOIN graph_preview_catalog c ON c.path=v.path AND c.revision=v.version WHERE " + live + " AND c.resource_kind='bead'", "v.snapshot,v.actor", 1},
		{"graph_preview_versions v JOIN graph_preview_catalog c ON c.path=v.path AND c.revision=v.version WHERE " + live + " AND c.resource_kind='link'", "v.snapshot,v.actor", 2},
		// Deleted Memory current-state validation inspects the preserved final
		// live head; charge those bytes too, while older snapshots stay excluded.
		{"graph_preview_versions v JOIN graph_preview_catalog c ON c.path=v.path AND c.revision=v.version WHERE c.allocation_state='deleted' AND c.backing='generic'", "v.snapshot,v.actor", 1},
		{"issues i JOIN graph_preview_catalog c ON c.backing='issue' AND c.backing_key=i.id " + sqlbuild.LeaseJoin("i") + " WHERE " + live, sqlbuild.QualifyColumns(sqlbuild.IssueBaseColumns, "i.") + ", " + sqlbuild.LeaseSelectColumns, 1},
		{"graph_preview_issue_versions m JOIN graph_preview_catalog c ON c.path=m.path AND c.revision=m.version JOIN issue_versions v ON v.issue_id=m.issue_id AND v.revision=m.issue_revision WHERE " + live, "m.owned,v.durable_state,v.change_actor,v.attribution_status", 1},
		// Full hydration reads labels twice. Comments are acquired before the existing
		// unsupported-comment refusal, so their bytes must be bounded too.
		{"labels l JOIN graph_preview_catalog c ON c.backing='issue' AND c.backing_key=l.issue_id WHERE " + live, "l.label", 2},
		{"comments m JOIN graph_preview_catalog c ON c.backing='issue' AND c.backing_key=m.issue_id WHERE " + live, "m.author,m.text", 1},
		// Dependencies appear in Issue hydration, owner state, and top-level Links.
		{"dependencies d JOIN graph_preview_catalog c ON c.backing='issue' AND c.backing_key=d.issue_id WHERE " + live, "d.id,d.issue_id,d.depends_on_issue_id,d.depends_on_wisp_id,d.depends_on_external,d.type,d.created_by,d.metadata,d.thread_id", 3},
	}
}

func checkCurrentReadBytes(ctx context.Context, tx *sql.Tx) error {
	used, err := currentReadBytes(ctx, tx)
	if err != nil {
		return err
	}
	if used > PreviewCurrentReadByteLimit {
		return fmt.Errorf("%w: complete current read exceeds the %d-byte acquisition budget before payload decoding", ErrLimitExceeded, PreviewCurrentReadByteLimit)
	}
	return checkCurrentIssueAuthority(ctx, tx)
}

func checkCurrentIssueAuthority(ctx context.Context, tx *sql.Tx) error {
	// Generic Issue hydration routes relations through a same-ID active wisp.
	// Graph preview does not admit that alternative authority. Refuse it before
	// the hydrator can acquire unbudgeted wisp payload/labels/comments/edges.
	var wisps int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisps w JOIN graph_preview_catalog c ON c.backing='issue' AND c.backing_key=w.id WHERE c.allocation_state='live'`).Scan(&wisps); err != nil {
		return err
	}
	if wisps != 0 {
		return fmt.Errorf("%w: live graph Issue also names a wisp", ErrInvalidStore)
	}
	return nil
}

// Size queries return numbers, never payloads. Clamp only at an impossible
// acquisition size so an oversized legacy workspace can still shrink in steps.
func currentReadBytes(ctx context.Context, tx *sql.Tx) (uint64, error) {
	var used uint64
	for _, part := range currentReadSizeQueries() {
		fields := []string{"256"}
		for _, column := range strings.Split(part.columns, ",") {
			fields = append(fields, "COALESCE(OCTET_LENGTH("+strings.TrimSpace(column)+"),0)")
		}
		query := "SELECT CAST(LEAST(COALESCE(SUM((" + strings.Join(fields, "+") + ")*?),0),?) AS UNSIGNED) FROM " + part.from
		var size uint64
		if err := tx.QueryRowContext(ctx, query, part.copies, uint64(1)<<60).Scan(&size); err != nil {
			return 0, err
		}
		used += size
	}
	return used, nil
}
