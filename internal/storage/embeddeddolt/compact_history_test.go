//go:build cgo

package embeddeddolt_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// Real history is required: SQL text mocks cannot prove author/committer or time-travel semantics.
func TestCompactPreservesHistoryAndSyncMarker(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, zone := range []string{"+00:00", "-07:00"} {
		t.Run(zone, func(t *testing.T) { testCompactHistory(t, zone) })
	}
}

func testCompactHistory(t *testing.T, zone string) {
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "dolt")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec("CREATE DATABASE compact_test")
	exec("USE compact_test")
	exec("SET @@time_zone = ?", zone)
	exec("CALL DOLT_COMMIT('--amend', '--allow-empty', '--date', '2026-01-01T00:00:00Z')")
	var initial string
	if err := conn.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&initial); err != nil {
		t.Fatal(err)
	}
	exec("CREATE TABLE issues (id VARCHAR(50) PRIMARY KEY, external_ref VARCHAR(200), title VARCHAR(200))")
	exec("CALL DOLT_COMMIT('-Am','schema','--date','2026-08-01T00:00:00Z')")
	exec("INSERT INTO issues VALUES ('test-1','https://test/OLD','old')")
	exec("CALL DOLT_COMMIT('-Am','boundary','--date','2026-09-01T00:00:00Z')")
	var boundary string
	if err := conn.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&boundary); err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, step := range []struct{ msg, date, mutation string }{
		{"link", "2026-10-01T09:00:00.125Z", "UPDATE issues SET external_ref='https://test/EXT-1'"},
		{"empty", "2026-10-01T09:00:00.250Z", ""},
		{"local edit", "2026-10-01T09:00:00.900Z", "UPDATE issues SET title='unpublished local correction'"},
	} {
		if step.mutation != "" {
			exec(step.mutation)
		}
		exec("SET @@dolt_committer_name = ?, @@dolt_committer_email = ?, @@dolt_committer_date = ?", "Jane (work) <tag>", "jane@example.com", step.date)
		exec("CALL DOLT_COMMIT('-Am',?, '--allow-empty','--date','2026-08-15T09:00:00.100Z','--author','Carol <carol@example.com>')", step.msg)
		var hash string
		if err := conn.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&hash); err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, hash)
	}
	exec("SET @@dolt_committer_name = 'operator', @@dolt_committer_email = 'operator@example.com', @@dolt_committer_date = '2026-10-05T10:00:00Z'")
	readMetadata := func() map[string][]string {
		t.Helper()
		rows, err := conn.QueryContext(ctx, "SELECT message, committer, email, DATE_FORMAT(date,'%Y-%m-%dT%H:%i:%s.%fZ'), author, author_email, DATE_FORMAT(author_date,'%Y-%m-%dT%H:%i:%s.%fZ') FROM dolt_log WHERE message IN ('link','empty','local edit')")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := make(map[string][]string)
		for rows.Next() {
			var m string
			v := make([]string, 6)
			if err := rows.Scan(&m, &v[0], &v[1], &v[2], &v[3], &v[4], &v[5]); err != nil {
				t.Fatal(err)
			}
			result[m] = v
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(result) != 3 {
			t.Fatalf("kept metadata: %v", result)
		}
		return result
	}
	before := readMetadata()
	marker, err := time.Parse(time.RFC3339Nano, "2026-10-01T09:00:00.300Z")
	if err != nil {
		t.Fatal(err)
	}
	previous := func() string {
		t.Helper()
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		ref, found, err := issueops.PreviousExternalRefInTx(ctx, tx, "test-1", marker)
		if err != nil || !found {
			t.Fatalf("last_sync no longer resolves: found=%v, err=%v", found, err)
		}
		return ref
	}
	if got := previous(); got != "https://test/EXT-1" {
		t.Fatalf("before reference: %q", got)
	}
	for pass := 0; pass < 2; pass++ {
		if err := versioncontrolops.Compact(ctx, conn, initial, boundary, 3, hashes); err != nil {
			t.Fatal(err)
		}
		if got := readMetadata(); !reflect.DeepEqual(got, before) {
			t.Fatalf("pass %d changed retained metadata:\nbefore=%v\nafter=%v", pass, before, got)
		}
		if got := previous(); got != "https://test/EXT-1" {
			t.Fatalf("pass %d changed previous reference: %q", pass, got)
		}
		var title, name, email, date string
		if err := conn.QueryRowContext(ctx, "SELECT title FROM issues WHERE id='test-1'").Scan(&title); err != nil {
			t.Fatal(err)
		}
		if title != "unpublished local correction" {
			t.Fatalf("local edit lost: %q", title)
		}
		if err := conn.QueryRowContext(ctx, "SELECT @@dolt_committer_name,@@dolt_committer_email,@@dolt_committer_date").Scan(&name, &email, &date); err != nil {
			t.Fatal(err)
		}
		if name != "operator" || email != "operator@example.com" || date != "2026-10-05T10:00:00Z" {
			t.Fatalf("session overrides leaked: %q,%q,%q", name, email, date)
		}
		if err := conn.QueryRowContext(ctx, "SELECT commit_hash FROM dolt_log WHERE message LIKE 'compact: squash %'").Scan(&boundary); err != nil {
			t.Fatal(err)
		}
		rows, err := conn.QueryContext(ctx, "SELECT commit_hash FROM dolt_log WHERE message IN ('link','empty','local edit') ORDER BY date")
		if err != nil {
			t.Fatal(err)
		}
		hashes = nil
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				t.Fatal(err)
			}
			hashes = append(hashes, h)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
	}
	exec("CALL DOLT_BRANCH('topic')")
	exec("CALL DOLT_COMMIT('--allow-empty','-m','main parent')")
	exec("CALL DOLT_CHECKOUT('topic')")
	exec("UPDATE issues SET title='topic correction'")
	exec("CALL DOLT_COMMIT('-Am','topic parent')")
	exec("CALL DOLT_CHECKOUT('main')")
	exec("CALL DOLT_MERGE('topic','-m','merge parent')")
	var head string
	if err := conn.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&head); err != nil {
		t.Fatal(err)
	}
	if err := versioncontrolops.Compact(ctx, conn, initial, boundary, 3, []string{head}); err == nil {
		t.Fatal("merge commit should refuse replay")
	}
	var after string
	if err := conn.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != head {
		t.Fatalf("failed replay changed main: %s -> %s", head, after)
	}
	var remaining int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_branches WHERE name='compact-tmp'").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("failed replay stranded temporary branch")
	}
	var name, email, date string
	if err := conn.QueryRowContext(ctx, "SELECT @@dolt_committer_name,@@dolt_committer_email,@@dolt_committer_date").Scan(&name, &email, &date); err != nil {
		t.Fatal(err)
	}
	if name != "operator" || email != "operator@example.com" || date != "2026-10-05T10:00:00Z" {
		t.Fatalf("failed replay leaked session overrides: %q,%q,%q", name, email, date)
	}
}
