//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

func assertIssueAssignmentRecorder(t *testing.T, ctx context.Context, s *Store, issue IssueRecord, want int) {
	t.Helper()
	assertIssueEditCounts(t, ctx, s, issue.Properties.ID, want)
	var ordinal int
	if err := s.db.QueryRowContext(ctx, "SELECT current_revision FROM issues WHERE id=?", issue.Properties.ID).Scan(&ordinal); err != nil {
		t.Fatal(err)
	}
	if ordinal != want {
		t.Fatalf("ordinal=%d; want %d", ordinal, want)
	}
	// plainIssue authors two initial labels. Native creation records one
	// created audit event plus one label_added event for each label, while
	// retaining only one complete version and one machine-journal create.
	// Owner and CreatedBy are persisted properties, not extra audit actors.
	type auditEvent struct{ kind, actor, comment string }
	auditRows, err := s.db.QueryContext(ctx, "SELECT event_type, actor, COALESCE(comment, '') FROM events WHERE issue_id=?", issue.Properties.ID)
	if err != nil {
		t.Fatal(err)
	}
	audit := map[auditEvent]int{}
	for auditRows.Next() {
		var event auditEvent
		if err := auditRows.Scan(&event.kind, &event.actor, &event.comment); err != nil {
			_ = auditRows.Close()
			t.Fatal(err)
		}
		audit[event]++
	}
	if err := errors.Join(auditRows.Err(), auditRows.Close()); err != nil {
		t.Fatal(err)
	}
	expectedAudit := map[auditEvent]int{
		{"created", "creator-actor", ""}:                       1,
		{"label_added", "creator-actor", "Added label: demo"}:  1,
		{"label_added", "creator-actor", "Added label: graph"}: 1,
	}
	if want > 1 {
		// Combined scalar edits produce one updated event, not one per field.
		expectedAudit[auditEvent{"updated", "editor", ""}] = want - 1
	}
	if !reflect.DeepEqual(audit, expectedAudit) {
		t.Fatalf("audit=%v; want %v", audit, expectedAudit)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT op FROM bd_events_journal WHERE issue_id=? ORDER BY seq", issue.Properties.ID)
	if err != nil {
		t.Fatal(err)
	}
	var operations []string
	for rows.Next() {
		var op string
		if err := rows.Scan(&op); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		operations = append(operations, op)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	expected := make([]string, want)
	expected[0] = "create"
	for i := 1; i < want; i++ {
		expected[i] = "update"
	}
	if !reflect.DeepEqual(operations, expected) {
		t.Fatalf("journal=%v want=%v", operations, expected)
	}
	assertAssigneeLeaseCount(t, ctx, s, issue.Properties.ID, 0)
}

// Exercise the native writer's optional journal without enabling it in the CLI.
// One combined operation must create one version/mapping, not one for each field.
func TestIssueAssignmentNativeRecorderExactlyOnce(t *testing.T) {
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
			request := plainIssue("Initial")
			request.Actor, request.Issue.Owner, request.Issue.CreatedBy = "creator-actor", "owner@example.invalid", "independent-creator"
			current, err := s.CreateIssue(ctx, "beads/work", request)
			if err != nil {
				t.Fatal(err)
			}
			if current.Properties.Owner != request.Issue.Owner || current.Properties.CreatedBy != request.Issue.CreatedBy || current.Attribution.Actor != request.Actor {
				t.Fatal("creation conflated actor/owner/creator")
			}
			assertIssueAssignmentRecorder(t, ctx, s, current, 1)
			changes := []UpdateIssueRequest{
				{Priority: issuePriority(0)},
				{Assignee: issueEditString("alice")},
				{Priority: issuePriority(1), Assignee: issueEditString("bob"), Title: issueEditString("Combined")},
				{Assignee: issueEditString("")},
			}
			for i, change := range changes {
				change.Path, change.Actor, change.ExpectedRevision = "beads/work", "editor", current.Revision
				before := current
				beforeState := workflowState(t, ctx, s)
				injected := errors.New("after native version before graph mapping")
				s.afterWrite = func(stage string) error {
					if stage == "issue-retained" {
						return injected
					}
					return nil
				}
				_, err := s.UpdateIssue(ctx, change)
				s.afterWrite = nil
				if !errors.Is(err, injected) || !reflect.DeepEqual(beforeState, workflowState(t, ctx, s)) {
					t.Fatalf("rollback changed native/journal/graph/lease state: %v", err)
				}
				updated, err := s.UpdateIssue(ctx, change)
				if err != nil || !updated.Changed {
					t.Fatalf("update: %+v %v", updated, err)
				}
				current = updated.Issue
				if current.Properties.Owner != request.Issue.Owner || current.Properties.CreatedBy != request.Issue.CreatedBy || current.Attribution.Actor != "editor" {
					t.Fatal("actor/owner/creator changed roles")
				}
				assertIssueAssignmentRecorder(t, ctx, s, current, i+2)
				assertIssueEditVersion(t, ctx, s, "beads/work", before)
				assertIssueEditVersion(t, ctx, s, "beads/work", current)
				state := workflowState(t, ctx, s)
				if _, err := s.UpdateIssue(ctx, change); !errors.Is(err, ErrConflict) {
					t.Fatalf("stale same-value edit: %v", err)
				}
				change.ExpectedRevision, change.Actor = current.Revision, "different-noop-actor"
				noop, err := s.UpdateIssue(ctx, change)
				if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, current) {
					t.Fatalf("noop: %+v %v", noop, err)
				}
				change.ExpectedRevision, change.Unconditional = "", true
				noop, err = s.UpdateIssue(ctx, change)
				if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, current) || !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
					t.Fatalf("noop/stale changed state: %+v %v", noop, err)
				}
				assertIssueAssignmentRecorder(t, ctx, s, current, i+2)
			}
		})
	}
}
