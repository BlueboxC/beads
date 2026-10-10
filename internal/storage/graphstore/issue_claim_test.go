//go:build cgo

package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Only claim-owned fields may differ. In particular labels and complete owned
// Dependencies must survive the existing claim role's bare Issue response.
func assertClaimRecord(t *testing.T, before, after IssueRecord, actor string) {
	t.Helper()
	p := after.Properties
	if p == nil || p.Status != types.StatusInProgress || p.Assignee != actor || p.StartedAt == nil || p.LeaseExpiresAt == nil || p.HeartbeatAt == nil || !p.LeaseExpiresAt.After(*p.HeartbeatAt) || after.Revision == before.Revision || after.Version != after.Revision || after.Attribution.Actor != actor || after.ID != before.ID || after.Type != before.Type {
		t.Fatalf("incomplete claimed record: %+v properties=%+v", after, p)
	}
	if before.Properties.StartedAt != nil && !reflect.DeepEqual(p.StartedAt, before.Properties.StartedAt) {
		t.Fatal("claim replaced original start time")
	}
	properties := *p
	properties.Status, properties.Assignee = before.Properties.Status, before.Properties.Assignee
	properties.StartedAt, properties.UpdatedAt = before.Properties.StartedAt, before.Properties.UpdatedAt
	properties.LeaseExpiresAt, properties.HeartbeatAt = before.Properties.LeaseExpiresAt, before.Properties.HeartbeatAt
	properties.LeaseGrantedNode = before.Properties.LeaseGrantedNode
	properties.ContentHash, properties.RowVersion = before.Properties.ContentHash, before.Properties.RowVersion
	if !reflect.DeepEqual(properties, *before.Properties) || !reflect.DeepEqual(after.Owned, before.Owned) {
		t.Fatal("claim changed unrelated properties or owned Dependencies")
	}
}

func assertClaimEvents(t *testing.T, ctx context.Context, s *Store, id, actor string, want int) {
	t.Helper()
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND actor=? AND event_type='claimed'", id, actor).Scan(&count); err != nil || count != want {
		t.Fatalf("claimed events=%d want%d: %v", count, want, err)
	}
}

func assertClaimLease(t *testing.T, ctx context.Context, s *Store, id, actor string) {
	t.Helper()
	assertAssigneeLeaseCount(t, ctx, s, id, 1)
	var holder string
	if err := s.db.QueryRowContext(ctx, "SELECT holder FROM leases WHERE issue_id=?", id).Scan(&holder); err != nil || holder != actor {
		t.Fatalf("lease holder=%q want%q: %v", holder, actor, err)
	}
}

func TestIssueClaimLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, options, s, before, target, dependency := reopenFixture(t, backend)
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: "Unchanged claim context"})
			if err != nil {
				t.Fatal(err)
			}
			// Existing by-ID claim does not require ready work: the open source is
			// blocked by its still-open prerequisite, and remains a valid claim.
			assertReadyIDs(t, ctx, s, target.ID)
			result, err := s.ClaimIssue(ctx, "beads/work", "rig.agent")
			if err != nil || !result.Changed {
				t.Fatalf("claim: %+v %v", result, err)
			}
			assertClaimRecord(t, before, result.Issue, "rig.agent")
			assertClaimLease(t, ctx, s, before.Properties.ID, "rig.agent")
			assertClaimEvents(t, ctx, s, before.Properties.ID, "rig.agent", 1)
			assertIssueEditCounts(t, ctx, s, before.Properties.ID, 3)
			assertReadyIDs(t, ctx, s, target.ID)
			for _, actor := range []string{"rig.agent", "rig_agent"} {
				state := reopenState(t, ctx, s)
				s.afterWrite = func(stage string) error { return errors.New("no-op unexpectedly reached " + stage) }
				noop, err := s.ClaimIssue(ctx, "beads/work", actor)
				s.afterWrite = nil
				if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, result.Issue) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
					t.Fatalf("same/equivalent holder no-op changed state: %+v %v", noop, err)
				}
			}
			state := reopenState(t, ctx, s)
			foreign, err := s.ClaimIssue(ctx, "beads/work", "other.agent")
			if !errors.Is(err, storage.ErrAlreadyClaimed) || !reflect.DeepEqual(foreign, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("foreign holder: %+v %v", foreign, err)
			}
			for _, record := range []IssueRecord{before, result.Issue} {
				assertIssueEditVersion(t, ctx, s, "beads/work", record)
			}
			if got, err := s.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
			if got, err := s.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, dependency) {
				t.Fatalf("Dependency changed: %+v %v", got, err)
			}
			if got, err := s.Read(ctx, "beads/context"); err != nil || !reflect.DeepEqual(got, memory) {
				t.Fatalf("Memory changed: %+v %v", got, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := OpenExisting(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := restarted.Close(); err != nil {
					t.Error(err)
				}
			}()
			if got, err := restarted.ShowIssue(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, result.Issue) {
				t.Fatalf("restart: %+v %v", got, err)
			}
			assertIssueEditVersion(t, ctx, restarted, "beads/work", before)
			assertIssueEditVersion(t, ctx, restarted, "beads/work", result.Issue)
		})
	}
}

func TestIssueClaimRefusalAndRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, options, s, source, _, _ := reopenFixture(t, backend)
			if _, err := s.CloseIssue(ctx, "beads/prereq", "done", "closer"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/memory", Body: "not an Issue"}); err != nil {
				t.Fatal(err)
			}
			state := reopenState(t, ctx, s)
			for _, tc := range []struct {
				name, path, actor string
				want              error
			}{
				{"empty actor", "beads/work", "", storage.ErrValidation},
				{"actor UTF8", "beads/work", string([]byte{255}), storage.ErrValidation},
				{"actor overlength", "beads/work", strings.Repeat("雪", types.MaxFieldLen+1), types.ErrFieldTooLong},
				{"closed", "beads/prereq", "actor", storage.ErrNotClaimable},
				{"missing", "beads/missing", "actor", ErrNotFound},
				{"Memory", "beads/memory", "actor", storage.ErrValidation},
				{"Link", "links/block", "actor", storage.ErrValidation},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, err := s.ClaimIssue(ctx, tc.path, tc.actor)
					if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatalf("refusal: %+v %v", got, err)
					}
				})
			}
			for _, stage := range []string{"coordination", "issue-claim", "issue-retained", "source-catalog", "source-retained"} {
				t.Run(stage, func(t *testing.T) {
					fault := errors.New("injected claim failure")
					s.afterWrite = func(at string) error {
						if at == stage {
							return fault
						}
						return nil
					}
					got, err := s.ClaimIssue(ctx, "beads/work", "claimant")
					s.afterWrite = nil
					if !errors.Is(err, fault) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatalf("claim/lease rollback: %+v %v", got, err)
					}
				})
			}
			t.Run("cancellation", func(t *testing.T) {
				canceled, cancel := context.WithCancel(ctx)
				defer cancel()
				s.afterWrite = func(stage string) error {
					if stage == "source-retained" {
						cancel()
						return canceled.Err()
					}
					return nil
				}
				got, err := s.ClaimIssue(canceled, "beads/work", "claimant")
				s.afterWrite = nil
				if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
					t.Fatalf("cancel leaked claim: %+v %v", got, err)
				}
			})
			s.options.Binding.AuthorityID = "ffffffffffffffffffffffffffffffff"
			got, err := s.ClaimIssue(ctx, "beads/work", "claimant")
			s.options = options
			if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("authority changed state: %+v %v", got, err)
			}
			// The faulted writes reach the real claim writer, which grants its lease
			// before issue-claim; a subsequent accepted boundary-length actor proves
			// the same path can commit once and leaves no earlier lease/event/version.
			actor := strings.Repeat("雪", types.MaxFieldLen)
			accepted, err := s.ClaimIssue(ctx, "beads/work", actor)
			if err != nil || !accepted.Changed {
				t.Fatalf("255-code-point actor: %+v %v", accepted, err)
			}
			assertClaimRecord(t, source, accepted.Issue, actor)
			assertClaimLease(t, ctx, s, source.Properties.ID, actor)
			assertClaimEvents(t, ctx, s, source.Properties.ID, "claimant", 0)
			assertClaimEvents(t, ctx, s, source.Properties.ID, actor, 1)
			assertIssueEditCounts(t, ctx, s, source.Properties.ID, 3)
		})
	}
}

func TestIssueClaimExistingPolicies(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, source, _, _ := reopenFixture(t, backend)
			for _, tc := range []struct {
				name, category string
				allowed        bool
			}{{"triaged", "active", true}, {"testing", "wip", false}, {"archived", "done", false}, {"on-ice", "frozen", false}} {
				// Test-only configuration setup, following existing custom-status
				// tests; all Issue payloads and retained mappings use normal APIs.
				if _, err := s.db.ExecContext(ctx, "INSERT INTO custom_statuses(name,category) VALUES (?,?)", tc.name, tc.category); err != nil {
					t.Fatal(err)
				}
				request := plainIssue(tc.name)
				request.Issue.Status = types.Status(tc.name)
				before, err := s.CreateIssue(ctx, "beads/"+tc.name, request)
				if err != nil {
					t.Fatal(err)
				}
				state := reopenState(t, ctx, s)
				got, err := s.ClaimIssue(ctx, "beads/"+tc.name, "policy-actor")
				if tc.allowed {
					if err != nil || !got.Changed {
						t.Fatalf("custom active: %+v %v", got, err)
					}
					assertClaimRecord(t, before, got.Issue, "policy-actor")
					assertIssueEditCounts(t, ctx, s, before.Properties.ID, 2)
					assertIssueEditVersion(t, ctx, s, "beads/"+tc.name, before)
				} else if !errors.Is(err, storage.ErrNotClaimable) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
					t.Fatalf("custom state refusal: %+v %v", got, err)
				}
			}
			assigned, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "fixture", ExpectedRevision: source.Revision, Assignee: issueEditString("crew-pool")})
			if err != nil {
				t.Fatal(err)
			}
			state := reopenState(t, ctx, s)
			if got, err := s.ClaimIssue(ctx, "beads/work", "worker"); !errors.Is(err, storage.ErrAlreadyClaimed) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("ordinary preassignment stolen: %+v %v", got, err)
			}
			if err := s.withTx(ctx, true, func(tx *sql.Tx) error { return issueops.SetConfigInTx(ctx, tx, "claim.pools", "crew-pool") }); err != nil {
				t.Fatal(err)
			}
			got, err := s.ClaimIssue(ctx, "beads/work", "worker")
			if err != nil || !got.Changed {
				t.Fatalf("pool claim: %+v %v", got, err)
			}
			assertClaimRecord(t, assigned.Issue, got.Issue, "worker")
			assertClaimLease(t, ctx, s, source.Properties.ID, "worker")
			assertIssueEditCounts(t, ctx, s, source.Properties.ID, 4)
			assertIssueEditVersion(t, ctx, s, "beads/work", assigned.Issue)
		})
	}
}

func TestIssueClaimRejectsUnmappedOutgoingDependency(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, source, _, _ := reopenFixture(t, backend)
			// Existing native writer can author an external edge, but this is not
			// a supported canonical graph Link. Claim must fail before mutation,
			// not silently bypass the contributor-owned foreign policy layer.
			if err := s.withTx(ctx, true, func(tx *sql.Tx) error {
				_, err := issueops.AddDependencyInTx(ctx, tx, &types.Dependency{IssueID: source.Properties.ID, DependsOnID: "external:other:capability", Type: types.DepBlocks}, "fixture", issueops.AddDependencyOpts{})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			state := reopenState(t, ctx, s)
			stages := 0
			s.afterWrite = func(string) error { stages++; return nil }
			got, err := s.ClaimIssue(ctx, "beads/work", "claimant")
			s.afterWrite = nil
			if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, IssueMutationResult{}) || stages != 0 || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("unsupported outgoing edge mutated: %+v stages%d %v", got, stages, err)
			}
			assertAssigneeLeaseCount(t, ctx, s, source.Properties.ID, 0)
		})
	}
}
