//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// Native creation emits a created audit and one audit per initial label, but
// only one version and journal entry. A standalone claim adds exactly one of
// each. Optional machine journaling is enabled in this test context only.
func assertIssueClaimRecorder(t *testing.T, ctx context.Context, s *Store, issue IssueRecord, claimed bool) {
	t.Helper()
	want := 1
	if claimed {
		want++
	}
	assertIssueEditCounts(t, ctx, s, issue.Properties.ID, want)
	var ordinal int
	if err := s.db.QueryRowContext(ctx, "SELECT current_revision FROM issues WHERE id=?", issue.Properties.ID).Scan(&ordinal); err != nil || ordinal != want {
		t.Fatalf("native ordinal=%d want=%d: %v", ordinal, want, err)
	}
	type audit struct{ kind, actor, comment string }
	expected := map[audit]int{
		{"created", "creator-actor", ""}:                       1,
		{"label_added", "creator-actor", "Added label: demo"}:  1,
		{"label_added", "creator-actor", "Added label: graph"}: 1,
	}
	if claimed {
		expected[audit{"claimed", "rig.agent", ""}] = 1
	}
	rows, err := s.db.QueryContext(ctx, "SELECT event_type, actor, COALESCE(comment, '') FROM events WHERE issue_id=?", issue.Properties.ID)
	if err != nil {
		t.Fatal(err)
	}
	actual := map[audit]int{}
	for rows.Next() {
		var event audit
		if err := rows.Scan(&event.kind, &event.actor, &event.comment); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		actual[event]++
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("native audit=%v want=%v", actual, expected)
	}
	rows, err = s.db.QueryContext(ctx, "SELECT op, actor FROM bd_events_journal WHERE issue_id=? ORDER BY seq", issue.Properties.ID)
	if err != nil {
		t.Fatal(err)
	}
	var operations [][2]string
	for rows.Next() {
		var event [2]string
		if err := rows.Scan(&event[0], &event[1]); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		operations = append(operations, event)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	wantOperations := [][2]string{{"create", "creator-actor"}}
	if claimed {
		wantOperations = append(wantOperations, [2]string{"update", "rig.agent"})
	}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Fatalf("journal=%v want=%v", operations, wantOperations)
	}
	if claimed {
		assertClaimLease(t, ctx, s, issue.Properties.ID, "rig.agent")
	} else {
		assertAssigneeLeaseCount(t, ctx, s, issue.Properties.ID, 0)
	}
}

func TestIssueClaimNativeRecorderExactlyOnce(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, options := issueExperimentOptions(t, backend)
			ctx = issueops.WithEventsJournal(ctx, true)
			s, err := OpenExisting(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			request := plainIssue("Claim once")
			request.Actor, request.Issue.Owner, request.Issue.CreatedBy = "creator-actor", "owner@example.invalid", "independent-creator"
			before, err := s.CreateIssue(ctx, "beads/work", request)
			if err != nil {
				t.Fatal(err)
			}
			assertIssueClaimRecorder(t, ctx, s, before, false)
			state := workflowState(t, ctx, s)
			fault := errors.New("after native claim mint before graph mapping")
			s.afterWrite = func(stage string) error {
				if stage == "issue-retained" {
					return fault
				}
				return nil
			}
			rejected, err := s.ClaimIssue(ctx, "beads/work", "rig.agent")
			s.afterWrite = nil
			if !errors.Is(err, fault) || !reflect.DeepEqual(rejected, IssueMutationResult{}) || !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
				t.Fatalf("rollback leaked native/graph/audit/journal/lease state: %+v %v", rejected, err)
			}
			assertIssueClaimRecorder(t, ctx, s, before, false)
			claimed, err := s.ClaimIssue(ctx, "beads/work", "rig.agent")
			if err != nil || !claimed.Changed {
				t.Fatalf("claim: %+v %v", claimed, err)
			}
			current := claimed.Issue
			assertClaimRecord(t, before, current, "rig.agent")
			if current.Properties.Owner != request.Issue.Owner || current.Properties.CreatedBy != request.Issue.CreatedBy {
				t.Fatal("claim conflated owner/creator/actor")
			}
			if current.Properties.LeaseExpiresAt.Sub(*current.Properties.HeartbeatAt) != 5*time.Minute {
				t.Fatal("claim changed the native five-minute lease")
			}
			assertIssueClaimRecorder(t, ctx, s, current, true)
			assertIssueEditVersion(t, ctx, s, "beads/work", before)
			assertIssueEditVersion(t, ctx, s, "beads/work", current)
			state = workflowState(t, ctx, s)
			for _, actor := range []string{"rig.agent", "rig_agent"} {
				// A different test-only TTL makes accidental renewal observable without
				// sleeping. This is not a new public lease configuration.
				noop, err := s.ClaimIssue(issueops.WithLeaseTTL(ctx, 10*time.Minute), "beads/work", actor)
				if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, current) {
					t.Fatalf("same-holder noop: %+v %v", noop, err)
				}
			}
			refused, err := s.ClaimIssue(ctx, "beads/work", "foreign-actor")
			if !errors.Is(err, storage.ErrAlreadyClaimed) || !reflect.DeepEqual(refused, IssueMutationResult{}) || !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
				t.Fatalf("noop/refusal changed state: %+v %v", refused, err)
			}
			assertIssueClaimRecorder(t, ctx, s, current, true)
		})
	}
}
