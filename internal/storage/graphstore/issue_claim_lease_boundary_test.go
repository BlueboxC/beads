//go:build cgo

package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// This pins a refusal boundary, not a supported heartbeat/recovery workflow.
// Lease fields are part of the complete retained Issue projection at this base.
// A native coordinator that changes only the local lease bypasses the graph
// writer, so current graph operations must refuse rather than invent a version
// or silently hydrate today's lease into the saved Issue record.
func TestIssueClaimExternalLeaseBoundary(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			for _, change := range []string{"heartbeat", "lease-deletion"} {
				t.Run(change, func(t *testing.T) {
					ctx, _, s, original, _, _ := reopenFixture(t, backend)
					const actor = "lease-boundary-actor"
					claimed, err := s.ClaimIssue(ctx, "beads/work", actor)
					if err != nil || !claimed.Changed {
						t.Fatalf("normal claim: %+v %v", claimed, err)
					}
					assertClaimRecord(t, original, claimed.Issue, actor)
					before := reopenState(t, ctx, s)
					noop, err := s.ClaimIssue(ctx, "beads/work", actor)
					if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, claimed.Issue) || !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
						t.Fatalf("ordinary same-owner no-op changed full snapshot: %+v %v", noop, err)
					}
					assertIssueEditVersion(t, ctx, s, "beads/work", claimed.Issue)

					// Deliberately use the native lease helper outside the admitted
					// graph writer. The test-only longer TTL makes the heartbeat's
					// projection change observable even with second-precision SQL
					// timestamps; there is no sleep, fake clock or schema fixture.
					err = s.withTx(ctx, true, func(tx *sql.Tx) error {
						if change == "heartbeat" {
							return issueops.HeartbeatIssueInTx(issueops.WithLeaseTTL(ctx, 10*time.Minute), tx, original.Properties.ID, actor)
						}
						return issueops.DeleteLeaseInTx(ctx, tx, original.Properties.ID)
					})
					if err != nil {
						t.Fatalf("external lease change: %v", err)
					}
					afterExternal := reopenState(t, ctx, s)
					if reflect.DeepEqual(before, afterExternal) {
						t.Fatal("external control did not change stored lease state")
					}
					// Both historical snapshots stay independent of today's lease.
					assertIssueEditVersion(t, ctx, s, "beads/work", original)
					assertIssueEditVersion(t, ctx, s, "beads/work", claimed.Issue)
					stages := 0
					s.afterWrite = func(string) error { stages++; return nil }
					t.Cleanup(func() { s.afterWrite = nil })
					current, err := s.ShowIssue(ctx, "beads/work")
					if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(current, IssueRecord{}) {
						t.Fatalf("current read accepted lease drift: %+v %v", current, err)
					}
					for _, operation := range []struct {
						name string
						run  func(context.Context) (IssueMutationResult, error)
					}{
						{"claim", func(ctx context.Context) (IssueMutationResult, error) {
							return s.ClaimIssue(ctx, "beads/work", actor)
						}},
						{"close", func(ctx context.Context) (IssueMutationResult, error) {
							return s.CloseIssue(ctx, "beads/work", "Must not hide lease drift", actor)
						}},
					} {
						got, err := operation.run(ctx)
						if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, IssueMutationResult{}) || stages != 0 || !reflect.DeepEqual(afterExternal, reopenState(t, ctx, s)) {
							t.Fatalf("%s failed to refuse without partial writes: %+v stages=%d %v", operation.name, got, stages, err)
						}
					}
					s.afterWrite = nil
					assertIssueEditCounts(t, ctx, s, original.Properties.ID, 3)
					assertIssueEditVersion(t, ctx, s, "beads/work", claimed.Issue)
				})
			}
		})
	}
}

// A corrupt custom-status entry must not turn the recognized no-op branch into
// an unrecorded write. The native claim helper accepts configured active states,
// but its Changed receipt is based on the original holder/status; a built-in
// in_progress collision exposes that mismatch without changing graph policy.
func TestIssueClaimRejectsInconsistentNoop(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, _, _ := reopenFixture(t, backend)
			const actor = "inconsistent-noop-actor"
			claimed, err := s.ClaimIssue(ctx, "beads/work", actor)
			if err != nil || !claimed.Changed {
				t.Fatalf("normal claim: %+v %v", claimed, err)
			}
			assertClaimRecord(t, original, claimed.Issue, actor)
			// Failure injection only: normal configuration must not redefine a
			// built-in status. This matches the actual custom_statuses columns.
			if _, err := s.db.ExecContext(ctx, "INSERT INTO custom_statuses(name,category) VALUES (?,?)", "in_progress", "active"); err != nil {
				t.Fatal(err)
			}
			before := reopenState(t, ctx, s)
			// A longer test context TTL guarantees that a mistaken fresh grant
			// changes the JSON projection even when both claims share one SQL
			// timestamp second. No new CLI TTL option or fake clock is involved.
			got, err := s.ClaimIssue(issueops.WithLeaseTTL(ctx, 10*time.Minute), "beads/work", actor)
			if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatalf("inconsistent no-op escaped rollback: %+v %v", got, err)
			}
			current, err := s.ShowIssue(ctx, "beads/work")
			if err != nil || !reflect.DeepEqual(current, claimed.Issue) {
				t.Fatalf("rejected no-op changed current record: %+v %v", current, err)
			}
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, 3)
			assertClaimEvents(t, ctx, s, original.Properties.ID, actor, 1)
			assertIssueEditVersion(t, ctx, s, "beads/work", original)
			assertIssueEditVersion(t, ctx, s, "beads/work", claimed.Issue)
		})
	}
}
