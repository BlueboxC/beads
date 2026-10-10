//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

func assertIssueAppendRecorder(t *testing.T, ctx context.Context, s *Store, current IssueRecord, appendCount int) {
	t.Helper()
	// One create, one claim, then one native mint per accepted append operation.
	want := 2 + appendCount
	assertIssueEditCounts(t, ctx, s, current.Properties.ID, want)
	var ordinal int
	if err := s.db.QueryRowContext(ctx, "SELECT current_revision FROM issues WHERE id=?", current.Properties.ID).Scan(&ordinal); err != nil || ordinal != want {
		t.Fatalf("native ordinal=%d want=%d: %v", ordinal, want, err)
	}
	type event struct{ kind, actor, comment string }
	expected := map[event]int{
		{"created", "creator-actor", ""}:                       1,
		{"label_added", "creator-actor", "Added label: demo"}:  1,
		{"label_added", "creator-actor", "Added label: graph"}: 1,
		{"claimed", "rig.agent", ""}:                           1,
	}
	if appendCount > 0 {
		expected[event{"updated", "rig.agent", ""}] = appendCount
	}
	rows, err := s.db.QueryContext(ctx, "SELECT event_type, actor, COALESCE(comment, '') FROM events WHERE issue_id=?", current.Properties.ID)
	if err != nil {
		t.Fatal(err)
	}
	actual := map[event]int{}
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.kind, &e.actor, &e.comment); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		actual[e]++
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("audit=%v want=%v", actual, expected)
	}
	rows, err = s.db.QueryContext(ctx, "SELECT op, actor FROM bd_events_journal WHERE issue_id=? ORDER BY seq", current.Properties.ID)
	if err != nil {
		t.Fatal(err)
	}
	var operations [][2]string
	for rows.Next() {
		var e [2]string
		if err := rows.Scan(&e[0], &e[1]); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		operations = append(operations, e)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	expectedOps := [][2]string{{"create", "creator-actor"}, {"update", "rig.agent"}}
	for range appendCount {
		expectedOps = append(expectedOps, [2]string{"update", "rig.agent"})
	}
	if !reflect.DeepEqual(operations, expectedOps) {
		t.Fatalf("journal=%v want=%v", operations, expectedOps)
	}
	assertClaimLease(t, ctx, s, current.Properties.ID, "rig.agent")
}

func TestIssueAppendNotesNativeRecorderExactlyOnce(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, options := issueExperimentOptions(t, backend)
			ctx = issueops.WithEventsJournal(ctx, true) // Exercise opt-in journaling, not a CLI policy change.
			s, err := OpenExisting(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			request := plainIssue("Progress")
			request.Actor, request.Issue.Owner, request.Issue.CreatedBy = "creator-actor", "owner@example.invalid", "independent-creator"
			original, err := s.CreateIssue(ctx, "beads/work", request)
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := s.ClaimIssue(ctx, "beads/work", "rig.agent")
			if err != nil || !claimed.Changed {
				t.Fatalf("claim: %+v %v", claimed, err)
			}
			current := claimed.Issue
			assertIssueAppendRecorder(t, ctx, s, current, 0)
			state := workflowState(t, ctx, s)
			noop, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "different-noop-actor", ExpectedRevision: current.Revision, AppendNotes: issueEditString("")})
			if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, current) || !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
				t.Fatalf("empty append noop changed state: %+v %v", noop, err)
			}
			for i, change := range []UpdateIssueRequest{
				{AppendNotes: issueEditString("First — 雪\r\n")},
				{AppendNotes: issueEditString("Second"), Title: issueEditString("Progress recorded"), Priority: issuePriority(0)},
			} {
				change.Path, change.Actor, change.ExpectedRevision = "beads/work", "rig.agent", current.Revision
				before := current
				state := workflowState(t, ctx, s)
				fault := errors.New("after native append mint before graph mapping")
				s.afterWrite = func(stage string) error {
					if stage == "issue-retained" {
						return fault
					}
					return nil
				}
				rejected, err := s.UpdateIssue(ctx, change)
				s.afterWrite = nil
				if !errors.Is(err, fault) || !reflect.DeepEqual(rejected, IssueMutationResult{}) || !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
					t.Fatalf("append rollback leaked native/graph/audit/journal/lease: %+v %v", rejected, err)
				}
				changed, err := s.UpdateIssue(ctx, change)
				if err != nil || !changed.Changed {
					t.Fatalf("append: %+v %v", changed, err)
				}
				current = changed.Issue
				wantNotes := before.Properties.Notes
				if wantNotes != "" {
					wantNotes += "\n"
				}
				wantNotes += *change.AppendNotes
				comparison := before
				properties := *before.Properties
				if change.Title != nil {
					properties.Title = *change.Title
				}
				if change.Priority != nil {
					properties.Priority = *change.Priority
				}
				comparison.Properties = &properties
				assertIssueAppendTransition(t, comparison, current, wantNotes, "rig.agent")
				assertIssueAppendRecorder(t, ctx, s, current, i+1)
				assertIssueEditVersion(t, ctx, s, "beads/work", before)
				assertIssueEditVersion(t, ctx, s, "beads/work", current)
				state = workflowState(t, ctx, s)
				if _, err := s.UpdateIssue(ctx, change); !errors.Is(err, ErrConflict) || !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
					t.Fatalf("stale append changed state: %v", err)
				}
			}
			// Exercise the post-mapping budget failure with machine journaling enabled.
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: strings.Repeat("m", 7<<20)}); err != nil {
				t.Fatal(err)
			}
			state = workflowState(t, ctx, s)
			refused, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "rig.agent", ExpectedRevision: current.Revision, AppendNotes: issueEditString(strings.Repeat("x", 1536<<10)), Title: issueEditString("Must roll back")})
			if !errors.Is(err, ErrLimitExceeded) || !reflect.DeepEqual(refused, IssueMutationResult{}) || !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
				t.Fatalf("read-budget refusal leaked effects: %+v %v", refused, err)
			}
			assertIssueAppendRecorder(t, ctx, s, current, 2)
			assertIssueEditVersion(t, ctx, s, "beads/work", original)
		})
	}
}
