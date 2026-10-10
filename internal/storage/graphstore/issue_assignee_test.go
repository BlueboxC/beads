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

func assertAssigneeLeaseCount(t *testing.T, ctx context.Context, s *Store, id string, want int) {
	t.Helper()
	var got int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM leases WHERE issue_id=?", id).Scan(&got); err != nil || got != want {
		t.Fatalf("lease count=%d want%d err=%v", got, want, err)
	}
}

func TestIssueAssigneeLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, current, target, dependency := reopenFixture(t, backend)
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: "Unrelated Memory"})
			if err != nil {
				t.Fatal(err)
			}
			original := current
			counts := 2
			for _, value := range []string{"agent.雪", strings.Repeat("é", types.MaxFieldLen), ""} {
				before := current
				supplied := value
				s.afterWrite = func(stage string) error {
					if stage == "coordination" {
						supplied = "mutated caller value"
					}
					return nil
				}
				result, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "assignee-editor", ExpectedRevision: before.Revision, Assignee: &supplied})
				s.afterWrite = nil
				if err != nil || !result.Changed || result.Issue.Properties.Assignee != value || result.Issue.Revision == before.Revision || result.Issue.Attribution.Actor != "assignee-editor" {
					t.Fatalf("set/clear=%+v err=%v", result, err)
				}
				current = result.Issue
				counts++
				properties := *current.Properties
				properties.Assignee = before.Properties.Assignee
				properties.UpdatedAt = before.Properties.UpdatedAt
				properties.ContentHash = before.Properties.ContentHash
				properties.RowVersion = before.Properties.RowVersion
				if !reflect.DeepEqual(properties, *before.Properties) || !reflect.DeepEqual(current.Owned, before.Owned) {
					t.Fatal("assignee changed unrelated properties/owned state")
				}
				assertAssigneeLeaseCount(t, ctx, s, current.Properties.ID, 0)
				assertIssueEditCounts(t, ctx, s, current.Properties.ID, counts)
				assertIssueListRetained(t, ctx, s, "beads/work", before)
				assertIssueListRetained(t, ctx, s, "beads/work", current)
				state := reopenState(t, ctx, s)
				noop, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "different-noop-actor", ExpectedRevision: current.Revision, Assignee: issueEditString(value)})
				if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, current) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
					t.Fatalf("no-op changed state: %+v %v", noop, err)
				}
			}
			before := current
			mixed, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "mixed-editor", ExpectedRevision: before.Revision, Assignee: issueEditString("mixed"), Title: issueEditString("Mixed title"), Priority: issuePriority(0)})
			if err != nil || !mixed.Changed || mixed.Issue.Properties.Assignee != "mixed" || mixed.Issue.Properties.Title != "Mixed title" || mixed.Issue.Properties.Priority != 0 || !reflect.DeepEqual(mixed.Issue.Owned, before.Owned) {
				t.Fatalf("mixed: %+v %v", mixed, err)
			}
			current = mixed.Issue
			counts++
			omitted, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "text-editor", ExpectedRevision: current.Revision, Description: issueEditString("description only")})
			if err != nil || !omitted.Changed || omitted.Issue.Properties.Assignee != "mixed" {
				t.Fatalf("omitted assignee: %+v %v", omitted, err)
			}
			counts++
			assertIssueEditCounts(t, ctx, s, current.Properties.ID, counts)
			for _, saved := range []IssueRecord{original, before, current, omitted.Issue} {
				assertIssueListRetained(t, ctx, s, "beads/work", saved)
			}
			if got, err := s.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
			if got, err := s.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, dependency) {
				t.Fatalf("owned link changed: %+v %v", got, err)
			}
			if got, err := s.Read(ctx, "beads/context"); err != nil || !reflect.DeepEqual(got, memory) {
				t.Fatalf("Memory changed: %+v %v", got, err)
			}
		})
	}
}

func TestIssueAssigneeRefusalAndRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, source, _, _ := reopenFixture(t, backend)
			edited, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: source.Revision, Assignee: issueEditString("owner")})
			if err != nil {
				t.Fatal(err)
			}
			request := UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: edited.Issue.Revision, Assignee: issueEditString("next"), Title: issueEditString("atomic text")}
			state := reopenState(t, ctx, s)
			for _, tc := range []struct {
				name   string
				mutate func(*UpdateIssueRequest)
				want   error
			}{
				{"invalid UTF8", func(r *UpdateIssueRequest) { r.Assignee = issueEditString(string([]byte{255})) }, storage.ErrValidation},
				{"overlength", func(r *UpdateIssueRequest) { r.Assignee = issueEditString(strings.Repeat("é", types.MaxFieldLen+1)) }, types.ErrFieldTooLong},
				{"stale same value", func(r *UpdateIssueRequest) {
					r.ExpectedRevision = source.Revision
					r.Title = nil
					r.Assignee = issueEditString("owner")
				}, ErrConflict},
				{"both guards", func(r *UpdateIssueRequest) { r.Unconditional = true }, storage.ErrValidation},
				{"no guard", func(r *UpdateIssueRequest) { r.ExpectedRevision = "" }, storage.ErrValidation},
			} {
				t.Run(tc.name, func(t *testing.T) {
					attempted := request
					tc.mutate(&attempted)
					result, err := s.UpdateIssue(ctx, attempted)
					if !errors.Is(err, tc.want) || !reflect.DeepEqual(result, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatalf("refusal changed state: %+v %v", result, err)
					}
				})
			}
			for _, stage := range []string{"issue-update", "issue-retained", "source-retained"} {
				t.Run(stage, func(t *testing.T) {
					fault := errors.New("assignee rollback")
					s.afterWrite = func(at string) error {
						if at == stage {
							return fault
						}
						return nil
					}
					result, err := s.UpdateIssue(ctx, request)
					s.afterWrite = nil
					if !errors.Is(err, fault) || !reflect.DeepEqual(result, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatalf("partial assignee/text/retained/audit mutation: %+v %v", result, err)
					}
				})
			}
			t.Run("cancellation", func(t *testing.T) {
				canceled, cancel := context.WithCancel(ctx)
				defer cancel()
				s.afterWrite = func(stage string) error {
					if stage == "issue-retained" {
						cancel()
						return canceled.Err()
					}
					return nil
				}
				result, err := s.UpdateIssue(canceled, request)
				s.afterWrite = nil
				if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
					t.Fatalf("cancellation leaked assignment/retention: %+v %v", result, err)
				}
			})
			assertIssueEditCounts(t, ctx, s, source.Properties.ID, 3)
			assertIssueListRetained(t, ctx, s, "beads/work", source)
			assertIssueListRetained(t, ctx, s, "beads/work", edited.Issue)
		})
	}
}

// A test-only composition of existing APIs authors a coherent active claim and
// retained mapping. This does not expose a new graph claim operation. The lease
// is part of the checked snapshot, so author and record it in the same transaction.
func seedAssigneeClaim(t *testing.T, ctx context.Context, s *Store, source IssueRecord, holder string) IssueRecord {
	t.Helper()
	var beforeCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issue_versions WHERE issue_id=?", source.Properties.ID).Scan(&beforeCount); err != nil {
		t.Fatal(err)
	}
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		if err := s.touchCoordination(ctx, tx); err != nil {
			return err
		}
		unscope := issueops.ScopeVersionedHistoryTransaction(tx, true)
		defer unscope()
		if _, err := issueops.ClaimIssueInTx(ctx, tx, source.Properties.ID, holder); err != nil {
			return err
		}
		// Jim's native claim writer owns the one retained version.
		return s.recordIssueMappingInTx(ctx, tx, "beads/work", source.Properties.ID, holder)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ShowIssue(ctx, "beads/work")
	if err != nil {
		t.Fatal(err)
	}
	if got.Properties.Status != types.StatusInProgress || got.Properties.Assignee != holder {
		t.Fatal("existing domain claim fixture failed")
	}
	assertIssueEditCounts(t, ctx, s, got.Properties.ID, beforeCount+1)
	assertAssigneeLeaseCount(t, ctx, s, got.Properties.ID, 1)
	return got
}

func TestIssueAssigneeActiveHolderFenceAndLease(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, source, target, dependency := reopenFixture(t, backend)
			held := seedAssigneeClaim(t, ctx, s, source, "rig.agent")
			state := reopenState(t, ctx, s)
			for _, value := range []string{"thief", ""} {
				for _, unconditional := range []bool{false, true} {
					request := UpdateIssueRequest{Path: "beads/work", Actor: "outsider", ExpectedRevision: held.Revision, Unconditional: unconditional, Assignee: issueEditString(value), Title: issueEditString("must rollback")}
					if unconditional {
						request.ExpectedRevision = ""
					}
					result, err := s.UpdateIssue(ctx, request)
					if !errors.Is(err, storage.ErrAlreadyClaimed) || !reflect.DeepEqual(result, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatalf("active holder fence bypassed: %+v %v", result, err)
					}
				}
			}
			// The authorized transfer clears the existing lease before retention.
			// Refusing after that point must restore the complete claimed snapshot,
			// including its local lease, rather than merely undoing the assignee.
			fault := errors.New("authorized transfer rollback after lease deletion")
			s.afterWrite = func(stage string) error {
				if stage == "issue-retained" {
					return fault
				}
				return nil
			}
			failed, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "rig.agent", ExpectedRevision: held.Revision, Assignee: issueEditString("new-holder")})
			s.afterWrite = nil
			if !errors.Is(err, fault) || !reflect.DeepEqual(failed, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("authorized rollback lost claim/lease state: %+v %v", failed, err)
			}
			if got, err := s.ShowIssue(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, held) {
				t.Fatalf("claimed current state changed after rollback: %+v %v", got, err)
			}
			assertAssigneeLeaseCount(t, ctx, s, source.Properties.ID, 1)
			assertIssueListRetained(t, ctx, s, "beads/work", held)
			noop, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "outsider", ExpectedRevision: held.Revision, Assignee: issueEditString("rig.agent")})
			if err != nil || noop.Changed || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("same assignment no-op: %+v %v", noop, err)
			}
			claimed := held
			retitled, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "rig.agent", ExpectedRevision: held.Revision, Title: issueEditString("holder text edit")})
			if err != nil || !retitled.Changed || !reflect.DeepEqual(retitled.Issue.Properties.LeaseExpiresAt, held.Properties.LeaseExpiresAt) || !reflect.DeepEqual(retitled.Issue.Properties.HeartbeatAt, held.Properties.HeartbeatAt) || retitled.Issue.Properties.LeaseGrantedNode != held.Properties.LeaseGrantedNode {
				t.Fatalf("unrelated edit disturbed holder lease: %+v %v", retitled, err)
			}
			held = retitled.Issue
			assertAssigneeLeaseCount(t, ctx, s, source.Properties.ID, 1)
			// Existing identity equivalence lets the holder transfer using another layer's
			// spelling. Transfer clears its lease and never grants a lease to the recipient.
			moved, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "rig_agent", ExpectedRevision: held.Revision, Assignee: issueEditString("crew-pool")})
			if err != nil || !moved.Changed || moved.Issue.Properties.Assignee != "crew-pool" || moved.Issue.Properties.Status != types.StatusInProgress {
				t.Fatalf("self transfer: %+v %v", moved, err)
			}
			assertAssigneeLeaseCount(t, ctx, s, source.Properties.ID, 0)
			if err := s.withTx(ctx, true, func(tx *sql.Tx) error { return issueops.SetConfigInTx(ctx, tx, "claim.pools", "crew-pool") }); err != nil {
				t.Fatal(err)
			}
			taken, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "pool-member", ExpectedRevision: moved.Issue.Revision, Assignee: issueEditString("pool-member")})
			if err != nil || !taken.Changed || taken.Issue.Properties.Assignee != "pool-member" {
				t.Fatalf("existing literal pool transfer: %+v %v", taken, err)
			}
			assertAssigneeLeaseCount(t, ctx, s, source.Properties.ID, 0)
			cleared, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "pool-member", ExpectedRevision: taken.Issue.Revision, Assignee: issueEditString("")})
			if err != nil || !cleared.Changed || cleared.Issue.Properties.Assignee != "" || cleared.Issue.Properties.Status != types.StatusInProgress {
				t.Fatalf("self clear: %+v %v", cleared, err)
			}
			assertAssigneeLeaseCount(t, ctx, s, source.Properties.ID, 0)
			for _, saved := range []IssueRecord{source, claimed, held, moved.Issue, taken.Issue, cleared.Issue} {
				assertIssueListRetained(t, ctx, s, "beads/work", saved)
			}
			assertIssueEditCounts(t, ctx, s, source.Properties.ID, 7)
			if !reflect.DeepEqual(cleared.Issue.Owned, source.Owned) {
				t.Fatal("assignment changed owned state")
			}
			if got, err := s.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
			if got, err := s.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, dependency) {
				t.Fatalf("Dependency changed: %+v %v", got, err)
			}
		})
	}
}
