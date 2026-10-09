package versioncontrolops

import (
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const testCompactMetadataQuery = "SELECT commit_hash, committer, email, DATE_FORMAT(date, '%Y-%m-%dT%H:%i:%s.%fZ') FROM dolt_log"
const testCompactIdentityQuery = "SELECT @@dolt_committer_name, @@dolt_committer_email, @@dolt_committer_date"
const testCompactIdentitySet = "SET @@dolt_committer_name = ?, @@dolt_committer_email = ?, @@dolt_committer_date = ?"

// Adapted from uschtwill's #7296; committer overrides follow bee-ghosttrack's review.
func TestCompactRestoresCommitMetadata(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		failReplay, failRestore bool
	}{
		{name: "success"}, {name: "replay_error", failReplay: true}, {name: "restore_error", failRestore: true}, {name: "both_errors", failReplay: true, failRestore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failReplay := tc.failReplay
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery(testCompactMetadataQuery).WillReturnRows(sqlmock.NewRows([]string{"hash", "committer", "email", "date"}).
				AddRow("boundary", "base", "base@example.com", "2026-09-01T07:00:00.000000Z").
				AddRow("recent", "Jane (work) <tag>", "jane@example.com", "2026-10-01T08:00:00.125000Z"))
			mock.ExpectQuery(testCompactIdentityQuery).WillReturnRows(sqlmock.NewRows([]string{"name", "email", "date"}).AddRow("owner", "owner@example.com", "2026-10-05T10:00:00Z"))
			mock.ExpectExec("CALL DOLT_BRANCH('compact-tmp', ?)").WithArgs("boundary").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec("CALL DOLT_CHECKOUT('compact-tmp')").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec("CALL DOLT_RESET('--soft', ?)").WithArgs("initial").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(testCompactIdentitySet).WithArgs("owner", "owner@example.com", "2026-09-01T07:00:00.000000Z").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec("CALL DOLT_COMMIT('-Am', ?, '--date', ?)").WithArgs("compact: squash 2 commits into base snapshot", "2026-09-01T07:00:00.000000Z").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(testCompactIdentitySet).WithArgs("Jane (work) <tag>", "jane@example.com", "2026-10-01T08:00:00.125000Z").WillReturnResult(sqlmock.NewResult(0, 0))
			replay := mock.ExpectExec("CALL DOLT_CHERRY_PICK('--allow-empty', ?)").WithArgs("recent")
			want := errors.New("replay failed")
			if failReplay {
				replay.WillReturnError(want)
			} else {
				replay.WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectExec("CALL DOLT_CHECKOUT('main')").WillReturnResult(sqlmock.NewResult(0, 0))
			if !failReplay {
				mock.ExpectExec("CALL DOLT_RESET('--hard', 'compact-tmp')").WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectExec("CALL DOLT_BRANCH('-D', 'compact-tmp')").WillReturnResult(sqlmock.NewResult(0, 0))
			restore := mock.ExpectExec(testCompactIdentitySet).WithArgs("owner", "owner@example.com", "2026-10-05T10:00:00Z")
			restoreErr := errors.New("restore failed")
			if tc.failRestore {
				restore.WillReturnError(restoreErr)
			} else {
				restore.WillReturnResult(sqlmock.NewResult(0, 0))
			}
			err = Compact(t.Context(), db, "initial", "boundary", 2, []string{"recent"})
			if failReplay && !errors.Is(err, want) {
				t.Fatalf("lost replay cause: %v", err)
			}
			if tc.failRestore && !errors.Is(err, restoreErr) {
				t.Fatalf("lost restoration error: %v", err)
			}
			if !failReplay && !tc.failRestore && err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Retain the contributor's refusal before any branch or session mutation.
func TestCompactRefusesUnknownRecentCommit(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(testCompactMetadataQuery).WillReturnRows(sqlmock.NewRows([]string{"hash", "committer", "email", "date"}).AddRow("boundary", "Ann", "ann@example.com", "2026-09-01T07:00:00.000000Z"))
	if err := Compact(t.Context(), db, "initial", "boundary", 1, []string{"missing"}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing commit refusal, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompactRefusesUnreadableMetadata(t *testing.T) {
	for _, tc := range []string{"query_error", "bad_row", "unknown_boundary"} {
		t.Run(tc, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			q := mock.ExpectQuery(testCompactMetadataQuery)
			switch tc {
			case "query_error":
				q.WillReturnError(errors.New("unavailable"))
			case "bad_row":
				q.WillReturnRows(sqlmock.NewRows([]string{"hash", "committer", "email", "date"}).AddRow("boundary", nil, "email", "date"))
			case "unknown_boundary":
				q.WillReturnRows(sqlmock.NewRows([]string{"hash", "committer", "email", "date"}))
			}
			if err := Compact(t.Context(), db, "initial", "boundary", 2, nil); err == nil {
				t.Fatal("unreadable metadata reached branch mutation")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
