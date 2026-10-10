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

	graph "github.com/steveyegge/beads/graphops"
)

func TestCurrentSnapshotLifecycle(t *testing.T) {
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
			empty, err := s.CurrentSnapshot(ctx)
			if err != nil || len(empty.Records) != 0 || len(empty.Types) != 6 || !authorityID.MatchString(empty.WriterToken) {
				t.Fatalf("empty inventory: %+v %v", empty, err)
			}
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Body: "Plan"})
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"beads/work", "beads/prereq"} {
				if _, err := s.CreateIssue(ctx, path, plainIssue(path)); err != nil {
					t.Fatal(err)
				}
			}
			dep, err := s.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work", TargetPath: "beads/prereq", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			related, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/plan", TargetPath: "beads/work", ExpectedSourceRevision: memory.Revision})
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.CurrentSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if current.WriterToken == empty.WriterToken {
				t.Fatal("write did not change token")
			}
			paths := []string{"beads/plan", "beads/prereq", "beads/work", "links/block", "links/context"}
			if len(current.Records) != len(paths) {
				t.Fatalf("inventory length: %d", len(current.Records))
			}
			for i, path := range paths {
				exact, err := s.Read(ctx, path)
				if err != nil || !reflect.DeepEqual(exact, current.Records[i]) {
					t.Fatalf("inventory/exact mismatch %s: %v", path, err)
				}
			}
			if !reflect.DeepEqual(current.Records[0], related.Source) || !reflect.DeepEqual(current.Records[2], dep.Source) {
				t.Fatal("complete owned state differs")
			}
			for i, descriptor := range current.Types {
				exact, err := s.ReadType(ctx, strings.TrimPrefix(descriptor.ID(), o.Binding.ScopeURL))
				if err != nil || !reflect.DeepEqual(exact, descriptor) {
					t.Fatalf("installed descriptor mismatch: %v", err)
				}
				if i > 0 && graph.CompareCodeUnits(current.Types[i-1].ID(), descriptor.ID()) >= 0 {
					t.Fatal("Types not ordered")
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := s.CurrentSnapshot(ctx)
			if err != nil || !reflect.DeepEqual(current, reopened) {
				t.Fatalf("reopen changed current state/token: %v", err)
			}
			removed, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/context", ExpectedRevision: related.Link.Revision, ExpectedSourceRevision: related.Source.(Record).Revision})
			if err != nil {
				t.Fatal(err)
			}
			after, err := s.CurrentSnapshot(ctx)
			if err != nil || len(after.Records) != 4 || after.WriterToken == current.WriterToken {
				t.Fatalf("post-unlink inventory: %+v %v", after, err)
			}
			if !reflect.DeepEqual(after.Records[0], removed.Source) {
				t.Fatal("unlink left obsolete owned Link")
			}
		})
	}
}

func TestCurrentSnapshotRefusesCorruption(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Body: "Plan"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work")); err != nil {
				t.Fatal(err)
			}
			cases := map[string]string{
				"unknown-backing":                   `UPDATE graph_preview_catalog SET backing='other'`,
				"unknown-state":                     `UPDATE graph_preview_catalog SET allocation_state='pending'`,
				"kind-path-mismatch":                `UPDATE graph_preview_catalog SET resource_kind='link'`,
				"malformed-path":                    `UPDATE graph_preview_catalog SET path='beads/../plan' WHERE path='beads/plan'`,
				"orphan-payload":                    `INSERT INTO graph_preview_payloads(path,properties) VALUES('beads/orphan','{}')`,
				"orphan-issue":                      `DELETE FROM graph_preview_catalog WHERE backing='issue'`,
				"orphan-informational-link":         `INSERT INTO graph_preview_links(path,source_path,target_path,properties,attribution) VALUES('links/orphan','beads/plan','beads/plan','{}','{}')`,
				"deleted-bead-with-current-payload": `UPDATE graph_preview_catalog SET allocation_state='deleted'`,
				"missing-payload":                   `DELETE FROM graph_preview_payloads`,
				"malformed-retained":                `UPDATE graph_preview_versions SET snapshot='{}'`,
				"changed-descriptor":                `UPDATE graph_preview_types SET fingerprint='wrong' WHERE name='memory'`,
				"unknown-installed-type":            `INSERT INTO graph_preview_types(name,descriptor,fingerprint) SELECT 'unknown',descriptor,fingerprint FROM graph_preview_types WHERE name='memory'`,
			}
			for name, statement := range cases {
				t.Run(name, func(t *testing.T) {
					// Corruption stays inside a rolled-back real-engine transaction.
					err := s.withTx(ctx, false, func(tx *sql.Tx) error {
						if _, err := tx.ExecContext(ctx, statement); err != nil {
							return err
						}
						got, err := s.currentSnapshotInTx(ctx, tx)
						if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, Snapshot{}) {
							return fmt.Errorf("corruption leaked inventory: %+v %v", got, err)
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				})
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			got, err := s.CurrentSnapshot(canceled)
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Snapshot{}) {
				t.Fatalf("canceled read leaked inventory: %+v %v", got, err)
			}
		})
	}
}

func TestCurrentSnapshotLimit(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			// A synthetic oversized catalog proves refusal happens before backing
			// hydration. This is a bound test, not a seeded user demonstration.
			err = s.withTx(ctx, true, func(tx *sql.Tx) error {
				for i := 0; i <= PreviewSnapshotLimit; i++ {
					if _, err := tx.ExecContext(ctx, `INSERT INTO graph_preview_catalog(path,resource_kind,type_url,revision,allocation_state,backing) VALUES (?,'bead',?,'00000000000000000000000000000000','live','generic')`, fmt.Sprintf("beads/limit%d", i), MemoryTypeURL(o.Binding.ScopeURL)); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.CurrentSnapshot(ctx)
			if !errors.Is(err, ErrLimitExceeded) || !reflect.DeepEqual(got, Snapshot{}) {
				t.Fatalf("oversized inventory: %+v %v", got, err)
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM graph_preview_catalog WHERE path=?`, fmt.Sprintf("beads/limit%d", PreviewSnapshotLimit)); err != nil {
				t.Fatal(err)
			}
			// Exactly 1000 allocations proceed to backing validation; the
			// deliberately missing payload now fails as corruption, not limit.
			got, err = s.CurrentSnapshot(ctx)
			if !errors.Is(err, ErrInvalidStore) || errors.Is(err, ErrLimitExceeded) || !reflect.DeepEqual(got, Snapshot{}) {
				t.Fatalf("at-limit validation: %+v %v", got, err)
			}

		})
	}
}

func TestCurrentSnapshotTransactionConsistency(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			reader, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := reader.Close(); err != nil {
					t.Error(err)
				}
			}()
			var writer *Store
			if backend == "embedded" {
				// One embedded connector owns the database. Use two ordinary
				// SQL sessions on that connector to exercise transaction
				// isolation without opening a second engine for the same path.
				reader.db.SetMaxOpenConns(2)
				writer = &Store{db: reader.db, options: o}
			} else {
				writer, err = OpenExisting(ctx, o)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := writer.Close(); err != nil {
						t.Error(err)
					}
				}()
			}

			before, err := reader.CurrentSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = reader.withTx(ctx, false, func(tx *sql.Tx) error {
				// Establish the reader's real database snapshot before the writer
				// session commits. Production reads use this same transaction helper.
				if err := checkBinding(ctx, tx, o); err != nil {
					return err
				}
				if _, err := writer.Create(ctx, CreateRequest{Path: "beads/arriving", Body: "After reader began"}); err != nil {
					return err
				}
				during, err := reader.currentSnapshotInTx(ctx, tx)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(during, before) {
					return fmt.Errorf("reader mixed pre/post-write state: %+v", during)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			after, err := reader.CurrentSnapshot(ctx)
			if err != nil || len(after.Records) != 1 || after.WriterToken == before.WriterToken {
				t.Fatalf("new transaction did not observe committed writer: %+v %v", after, err)
			}
		})
	}
}
