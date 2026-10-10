//go:build cgo

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

func TestSharedSchemaMigrateCountsVersionBump(t *testing.T) {
	port := requireSharedProxiedServer(t)
	bd := buildBDUnderTest(t)
	p := newServerModeProject(t, bd, "mig")
	setMigrateJSONConfigFalse(t, p.beadsDir)
	run := func(args ...string) []byte {
		t.Helper()
		return []byte(p.run(t, bd, args...))
	}
	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/%s", port, p.database))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execSQL := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	execSQL("DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion())
	execSQL("REPLACE INTO local_metadata (`key`,value) VALUES ('bd_version','1.0.0')")
	execSQL("CALL DOLT_COMMIT('-Am','isolated old cursor')")
	if err := os.WriteFile(filepath.Join(p.beadsDir, ".local_version"), []byte("1.0.0"), 0600); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status  string
		Applied int
	}
	if err := json.Unmarshal(run("--format=json", "migrate", "schema"), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || result.Applied != 1 {
		t.Fatalf("first migration = %+v; want applied/1", result)
	}
	db.SetMaxIdleConns(0)
	var version string
	if err := db.QueryRow("SELECT value FROM local_metadata WHERE `key`='bd_version'").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(version) != Version {
		t.Fatalf("workspace version=%q; want %s", version, Version)
	}
	execSQL("REPLACE INTO local_metadata (`key`,value) VALUES ('bd_version','1.0.0')")
	if err := json.Unmarshal(run("--format=json", "migrate", "schema"), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "current" || result.Applied != 0 {
		t.Fatalf("second migration = %+v; want current/0", result)
	}
	if err := db.QueryRow("SELECT value FROM local_metadata WHERE `key`='bd_version'").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(version) != Version {
		t.Fatalf("current schema retained stale version %q", version)
	}
	// Text mode must report a fresh migrating run too.
	execSQL("DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion())
	execSQL("CALL DOLT_COMMIT('-Am','second old cursor')")
	if out := string(run("migrate", "schema")); !strings.Contains(out, "Applied 1 schema migration") {
		t.Fatal(out)
	}
}
