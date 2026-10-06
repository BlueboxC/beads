//go:build cgo

package embeddeddolt_test

import (
	"github.com/steveyegge/beads/internal/storage/schema"
	"os"
	"testing"
	"time"
)

func TestEmbeddedBlockedParentRepairMigration(t *testing.T) {
	requireEmbedded(t)
	dataDir := seedMigratedStore(t, t.Context())
	conn, closeConn := openDepConn(t, t.Context(), dataDir)
	defer closeConn()

	ctx := t.Context()
	fixture, err := os.ReadFile("../schema/testdata/blocked-parent-propagation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, string(fixture)); err != nil {
		t.Fatal(err)
	}
	mainOld, err := schema.MigrationSQL("0059_recompute_null_gate_is_blocked.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	ignoredOld, err := schema.IgnoredMigrationSQL("0015_recompute_null_gate_wisp_is_blocked.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, sqlText := range []string{mainOld, ignoredOld} {
		if _, err := conn.ExecContext(ctx, sqlText); err != nil {
			t.Fatal(err)
		}
	}
	var historical int
	for _, table := range []string{"issues", "wisps"} {
		var n int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE title IN ('related','cross-related') AND is_blocked = 1").Scan(&n); err != nil {
			t.Fatal(err)
		}
		historical += n
	}
	t.Logf("false blockers reproduced by frozen migrations: %d", historical)
	mainRepair, err := schema.MigrationSQL("0067_repair_blocked_parent_propagation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	ignoredRepair, err := schema.IgnoredMigrationSQL("0027_repair_blocked_parent_propagation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		for _, sqlText := range []string{mainRepair, ignoredRepair} {
			if _, err := conn.ExecContext(ctx, sqlText); err != nil {
				t.Fatal(err)
			}
		}
		for _, table := range []string{"issues", "wisps"} {
			rows, err := conn.QueryContext(ctx, "SELECT title,is_blocked,updated_at FROM "+table+" WHERE id LIKE 'test-mig-%' ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var title string
				var blocked bool
				var updated time.Time
				if err := rows.Scan(&title, &blocked, &updated); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				want := title == "root" || title == "child" || title == "cross-child"
				if blocked != want {
					t.Errorf("%s/%s blocked=%v, want %v", table, title, blocked, want)
				}
				if !updated.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
					t.Errorf("%s/%s timestamp changed: %s", table, title, updated)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			rows.Close()
		}
	}
}
