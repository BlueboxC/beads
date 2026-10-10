//go:build cgo

package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Options{Backend: "embedded", DataDir: filepath.Join(workspace, "dolt"),
		Database: "graph_preview_test", Branch: "main", Binding: Binding{
			WorkspaceID: workspace, ScopeURL: "https://example.test/c0/",
			AuthorityID: "0123456789abcdef0123456789abcdef", SchemaVersion: SchemaVersion}}
}

func TestEmbeddedBootstrapCreateReopen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	o := testOptions(t)
	if err := Init(ctx, o); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	// Standard schema was created by the ordinary migrations, not seeded here.
	var issues int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if issues != 0 {
		t.Fatalf("new workspace has %d Issues", issues)
	}
	created, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan — 記憶", Body: "", Actor: "test-author"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "https://example.test/c0/beads/plan" || created.Type != MemoryTypeURL(o.Binding.ScopeURL) || created.Owned == nil {
		t.Fatalf("incomplete record: %+v", created)
	}
	shown, err := s.Show(ctx, "beads/plan")
	if err != nil || !reflect.DeepEqual(shown, created) {
		t.Fatalf("show=%+v err=%v; want %+v", shown, err, created)
	}
	if _, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "overwrite"}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate create: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	shown, err = s.Show(ctx, "beads/plan")
	if err != nil || !reflect.DeepEqual(shown, created) {
		t.Fatalf("reopen=%+v err=%v", shown, err)
	}
	var actor string
	var retained []byte
	if err := s.db.QueryRowContext(ctx, `SELECT snapshot, actor FROM graph_preview_versions WHERE path=? AND version=?`, "beads/plan", created.Version).Scan(&retained, &actor); err != nil {
		t.Fatal(err)
	}
	want, _ := canonicalJSON(created)
	if string(retained) != string(want) || actor != "test-author" {
		t.Fatalf("retained=%s actor=%q", retained, actor)
	}
	if _, err = s.Show(ctx, "beads/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	wrong := o
	wrong.Binding.ScopeURL = "https://example.test/wrong/"
	// Recheck within the mutation as well as the initial open.
	s.options = wrong
	if _, err = s.Create(ctx, CreateRequest{Path: "beads/wrong"}); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("wrong binding mutation: %v", err)
	}
	s.options = o
	if _, err = s.Show(ctx, "beads/wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong binding changed data: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if unexpected, err := OpenExisting(ctx, wrong); !errors.Is(err, ErrInvalidStore) {
		if unexpected != nil {
			_ = unexpected.Close()
		}
		t.Fatalf("wrong binding open: %v", err)
	}
}

func TestEmbeddedCreateRollbackEveryStage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	o := testOptions(t)
	if err := Init(ctx, o); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	runRollbackControls(t, ctx, s)
}

func runRollbackControls(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	fault := errors.New("injected write failure")
	for _, stage := range []string{"coordination", "allocation", "payload", "retained"} {
		t.Run(stage, func(t *testing.T) {
			var before string
			if err := s.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			s.afterWrite = func(at string) error {
				if at == stage {
					return fault
				}
				return nil
			}
			_, err := s.Create(ctx, CreateRequest{Path: "beads/rollback", Title: stage, Body: "must disappear"})
			s.afterWrite = nil
			if !errors.Is(err, fault) {
				t.Fatalf("want injected failure, got %v", err)
			}
			if err := s.withTx(ctx, false, func(tx *sql.Tx) error {
				for _, table := range []string{"graph_preview_catalog", "graph_preview_payloads", "graph_preview_versions"} {
					var count int
					if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
						return err
					}
					if count != 0 {
						t.Errorf("partial commit in %s: %d", table, count)
					}
				}
				var after string
				if err := tx.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&after); err != nil {
					return err
				}
				if after != before {
					t.Error("coordination update escaped rollback")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := s.Create(ctx, CreateRequest{Path: "beads/rollback", Title: "now accepted"}); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedCancelBeforeCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	o := testOptions(t)
	if err := Init(ctx, o); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	opCtx, abort := context.WithCancel(ctx)
	s.afterWrite = func(stage string) error {
		if stage == "retained" {
			abort()
			return opCtx.Err()
		}
		return nil
	}
	_, err = s.Create(opCtx, CreateRequest{Path: "beads/cancelled"})
	abort()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// A new connector must observe no part of the cancelled transaction.
	s, err = OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"graph_preview_catalog", "graph_preview_payloads", "graph_preview_versions"} {
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("cancelled writer persisted %d rows in %s", count, table)
		}
	}
	if _, err := s.Create(ctx, CreateRequest{Path: "beads/successor"}); err != nil {
		t.Fatal(err)
	}
}

// The CLI uses its operation context for both opening the connector and the
// write. Unlike the child-context control, this cancels the embedded driver's
// inherited connection context as well as database/sql's transaction context.
func TestEmbeddedCancelSharedCLIContext(t *testing.T) {
	baseCtx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	o := testOptions(t)
	if err := Init(baseCtx, o); err != nil {
		t.Fatal(err)
	}
	ctx, abort := context.WithCancel(baseCtx)
	defer abort()
	s, err := OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	s.afterWrite = func(stage string) error {
		if stage == "retained" {
			abort()
			return ctx.Err()
		}
		return nil
	}
	_, err = s.Create(ctx, CreateRequest{Path: "beads/cancelled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("shared-context cancellation: %v", err)
	}
	t.Logf("cancelled operation outcome: %v", err)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(baseCtx, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"graph_preview_catalog", "graph_preview_payloads", "graph_preview_versions"} {
		var count int
		if err := s.db.QueryRowContext(baseCtx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("cancelled CLI-context transaction persisted %d rows in %s", count, table)
		}
	}
	if _, err := s.Create(baseCtx, CreateRequest{Path: "beads/successor"}); err != nil {
		t.Fatal(err)
	}
}
