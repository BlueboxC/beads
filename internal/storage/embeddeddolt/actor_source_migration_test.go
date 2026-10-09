//go:build cgo

package embeddeddolt_test

import (
	"context"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/journalops"
	"testing"
)

func TestActorSourceUpgradePreservesJournalAndIsIdempotent(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	// Seed before the journal exists so it stays clone-local in HEAD.
	dir := seedMainSchemaAt(t, ctx, 63)
	conn, closeConn := openPinnedConn(t, ctx, dir)
	defer closeConn()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatal(err)
	}
	// Model the previous writer: both cursors and the clone-local column are old.
	execFrozenGuard(t, ctx, conn, "ALTER TABLE bd_events_journal DROP COLUMN actor_source; DELETE FROM schema_migrations WHERE version=68; DELETE FROM ignored_schema_migrations WHERE version=28; CALL DOLT_ADD('-A'); CALL DOLT_COMMIT('-m', 'previous schema fixture')")
	execFrozenGuard(t, ctx, conn, `INSERT INTO bd_events_journal (seq, ts, op, issue_id, actor, issue_json) VALUES (1, '2026-01-01 00:00:00', 'create', 'old-1', 'Owner', '{"id":"old-1"}')`)
	for pass := 0; pass < 2; pass++ {
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatal(err)
		}
		var actor, source, payload string
		if err := conn.QueryRowContext(ctx, "SELECT actor, actor_source, issue_json FROM bd_events_journal WHERE seq=1").Scan(&actor, &source, &payload); err != nil {
			t.Fatal(err)
		}
		if actor != "Owner" || source != "" || payload != `{"id":"old-1"}` {
			t.Fatalf("historical row changed: %q %q %q", actor, source, payload)
		}
	}
}

func TestActorSourceStorageKeepsCallerAndCommentOriginsSeparate(t *testing.T) {
	env := newTestEnv(t, "as")
	env.store.SetEventsJournalEnabled(true)
	ctx := context.Background()
	if err := env.store.CreateIssue(ctx, &types.Issue{ID: "as-one", Title: "one", IssueType: types.TypeTask, Status: types.StatusOpen}, "Owner"); err != nil {
		t.Fatal(err)
	}
	ctx = journalops.WithActorSource(ctx, "Owner", "git")
	if err := env.store.UpdateIssue(ctx, "as-one", map[string]any{"title": "two"}, "Owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.AddIssueComment(ctx, "as-one", "other", "note"); err != nil {
		t.Fatal(err)
	}
	rows, err := env.store.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"create": "provided", "update": "git", "comment": "provided"}
	for _, row := range rows {
		if want, ok := expected[row.Op]; ok {
			if row.ActorSource != want {
				t.Fatalf("%s source=%q want=%q", row.Op, row.ActorSource, want)
			}
			delete(expected, row.Op)
		}
	}
	if len(expected) != 0 {
		t.Fatal(expected)
	}
}
