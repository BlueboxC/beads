//go:build cgo

package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/memoryops"
)

// This fixture models a store written by an older build. It deliberately uses a
// raw transaction, not the guarded public writer. Current and retained bytes
// still agree, so the refusal is the acquisition bound, not corruption.
func seedUnreadableMemory(t *testing.T, ctx context.Context, s *Store, path, body string) Record {
	t.Helper()
	first, err := s.Create(ctx, CreateRequest{Path: path, Body: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return replaceLegacyFixtureMemory(t, ctx, s, path, first, body)
}

func replaceLegacyFixtureMemory(t *testing.T, ctx context.Context, s *Store, path string, first Record, body string) Record {
	t.Helper()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := s.writeMemoryInTx(ctx, tx, memoryWriteRequest{path: path, hasBody: true, body: body, expectedRevision: first.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result.Memory
}

func TestGraphWritesPreserveAcquisitionBounds(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			body := strings.Repeat("x", 1<<20)
			for i := 0; i < 7; i++ {
				if _, err := s.Create(ctx, CreateRequest{Path: fmt.Sprintf("beads/m%d", i), Body: body}); err != nil {
					t.Fatal(err)
				}
			}
			small, err := s.Create(ctx, CreateRequest{Path: "beads/small", Body: "small"})
			if err != nil {
				t.Fatal(err)
			}
			before, err := s.CurrentSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := s.Create(ctx, CreateRequest{Path: "beads/excess", Body: body}); !errors.Is(err, ErrLimitExceeded) || !reflect.ValueOf(got).IsZero() {
				t.Fatalf("oversized create: nonzero=%t err=%v", !reflect.ValueOf(got).IsZero(), err)
			}
			if got, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/small", Body: &body, ExpectedRevision: small.Revision}); !errors.Is(err, ErrLimitExceeded) || !reflect.ValueOf(got).IsZero() {
				t.Fatalf("oversized patch: nonzero=%t err=%v", !reflect.ValueOf(got).IsZero(), err)
			}
			if got, err := s.Memories("test").Remember(ctx, memoryops.RememberRequest{Key: "new-knowledge", Content: body}); !errors.Is(err, ErrLimitExceeded) || !reflect.ValueOf(got).IsZero() {
				t.Fatalf("oversized continuity: nonzero=%t err=%v", !reflect.ValueOf(got).IsZero(), err)
			}
			after, err := s.CurrentSnapshot(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rejection changed snapshot: equal=%t err=%v", reflect.DeepEqual(before, after), err)
			}
			var retained, pointers int
			if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_versions").Scan(&retained); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM config WHERE `key`=?", "kv.memory.new-knowledge").Scan(&pointers); err != nil {
				t.Fatal(err)
			}
			if retained != 8 || pointers != 0 {
				t.Fatalf("rejected write retained effects: versions=%d pointers=%d", retained, pointers)
			}
		})
	}
}

func TestOversizedGraphCanShrinkWithoutDiscardingHistory(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			old := seedUnreadableMemory(t, ctx, s, "beads/large", strings.Repeat("x", PreviewCurrentReadByteLimit))
			if _, err := s.CurrentSnapshot(ctx); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("legacy fixture: %v", err)
			}
			smaller := strings.Repeat("x", PreviewCurrentReadByteLimit/2+1024)
			first, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/large", Body: &smaller, ExpectedRevision: old.Revision})
			if err != nil || !first.Changed {
				t.Fatalf("partial repair: %v", err)
			}
			if _, err := s.CurrentSnapshot(ctx); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("partial repair should remain oversized: %v", err)
			}
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/new"}); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("growth during repair: %v", err)
			}
			empty := ""
			last, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/large", Body: &empty, ExpectedRevision: first.Memory.Revision})
			if err != nil || !last.Changed {
				t.Fatalf("final repair: %v", err)
			}
			if _, err := s.CurrentSnapshot(ctx); err != nil {
				t.Fatalf("repaired read: %v", err)
			}
			var count int
			if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_versions WHERE path='beads/large'").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 4 {
				t.Fatalf("lost retained history: %d", count)
			}
		})
	}
}

func TestAcquisitionGrowthBoundaries(t *testing.T) {
	at := acquisitionUsage{PreviewSnapshotLimit, PreviewCurrentReadByteLimit, PreviewContinuityReadRowLimit, PreviewContinuityReadByteLimit}
	if err := checkAcquisitionGrowth(acquisitionUsage{}, at); err != nil {
		t.Fatal(err)
	}
	for _, after := range []acquisitionUsage{{at.resources + 1, at.currentBytes, at.continuityRows, at.continuityBytes}, {at.resources, at.currentBytes + 1, at.continuityRows, at.continuityBytes}, {at.resources, at.currentBytes, at.continuityRows + 1, at.continuityBytes}, {at.resources, at.currentBytes, at.continuityRows, at.continuityBytes + 1}} {
		if err := checkAcquisitionGrowth(at, after); !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("growth admitted: %+v %v", after, err)
		}
		if err := checkAcquisitionGrowth(after, after); err != nil {
			t.Fatalf("no-op refused: %v", err)
		}
		if err := checkAcquisitionGrowth(after, at); err != nil {
			t.Fatalf("repair refused: %v", err)
		}
	}
}

func TestContinuityAcquisitionBeforeTransferAndAtomicWrite(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			const key = "@knowledge/code/repair-0000"
			// Build the old over-budget config generation on the SQL side: no payload
			// is transferred to Go. Settings and differently cased namespaces do not count.
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			const rows = 1120
			for i := 0; i < rows; i++ {
				if _, err := tx.ExecContext(ctx, "INSERT INTO config (`key`,value) VALUES (?,REPEAT('x',60000))", fmt.Sprintf("kv.memory.@knowledge/code/repair-%04d", i)); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			m := s.Memories("test")
			if got, err := m.List(ctx, memoryops.ListRequest{}); !errors.Is(err, ErrLimitExceeded) || len(got.Memories) != 0 {
				t.Fatalf("list: rows=%d err=%v", len(got.Memories), err)
			}
			if plane, snap, err := s.ReadContinuity(ctx); !errors.Is(err, ErrLimitExceeded) || len(plane.Memories) != 0 || len(snap.Records) != 0 {
				t.Fatalf("joint read: rows=%d err=%v", len(plane.Memories), err)
			}
			var token string
			if err := s.db.QueryRowContext(ctx, "SELECT writer_token FROM graph_preview_scope").Scan(&token); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Remember(ctx, memoryops.RememberRequest{Key: "@knowledge/code/rejected", Content: "new"}); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("growth: %v", err)
			}
			var after string
			if err := s.db.QueryRowContext(ctx, "SELECT writer_token FROM graph_preview_scope").Scan(&after); err != nil {
				t.Fatal(err)
			}
			if token != after {
				t.Fatal("rejected generation changed writer token")
			}
			if _, err := m.Remember(ctx, memoryops.RememberRequest{Key: key, Content: "small"}); err != nil {
				t.Fatalf("repair: %v", err)
			}
			// Still oversized: shrinking only one row is allowed without pretending the
			// complete generation is now readable. Shrink the rest atomically.
			replacements := map[string]string{}
			for i := 1; i < rows; i++ {
				replacements[fmt.Sprintf("@knowledge/code/repair-%04d", i)] = "small"
			}
			if _, err := m.Apply(ctx, memoryops.BatchRequest{Remember: replacements}); err != nil {
				t.Fatalf("batch repair: %v", err)
			}
			got, err := m.List(ctx, memoryops.ListRequest{})
			if err != nil || len(got.Memories) != rows || got.Memories[key] != "small" {
				t.Fatalf("repaired continuity: rows=%d err=%v", len(got.Memories), err)
			}
		})
	}
}

// Row-count overflow is injected in a rolled-back raw transaction, so it does
// not need thousands of actual payloads or leave a corrupt persisted fixture.
func TestGraphInventoryWriteCountBound(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			err = s.withTx(ctx, true, func(tx *sql.Tx) error {
				for i := 0; i <= PreviewSnapshotLimit; i++ {
					if _, err := tx.ExecContext(ctx, "INSERT INTO graph_preview_catalog(path,resource_kind,type_url,revision,allocation_state,backing) VALUES (?,'bead',?,'00000000000000000000000000000000','live','generic')", fmt.Sprintf("beads/limit%d", i), MemoryTypeURL(s.ScopeURL())); err != nil {
						return err
					}
				}
				return nil
			})
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("count admission: %v", err)
			}
			var count int
			if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_catalog").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("count rejection left %d allocations", count)
			}
		})
	}
}
