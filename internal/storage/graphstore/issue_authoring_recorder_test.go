//go:build cgo

package graphstore

import (
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// Initial authoring is one native create, not create followed by field updates.
// A combined edit has one native recorder owner regardless of its field count.
func TestIssueAuthoringNativeRecorderExactlyOnce(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, s := createFieldsStore(t, backend)
			ctx = issueops.WithEventsJournal(ctx, true)
			request := createFieldsRequest("Authored")
			request.Actor, request.Issue.Owner, request.Issue.CreatedBy = "author", "owner@example.invalid", "independent-creator"
			request.Issue.Notes = "Initial — 雪\r\n"
			state := createFieldsState(t, ctx, s)
			fault := errors.New("after native initial mint")
			s.afterWrite = func(stage string) error {
				if stage == "retained" {
					return fault
				}
				return nil
			}
			refused, err := s.CreateIssue(ctx, "beads/work", request)
			s.afterWrite = nil
			if !errors.Is(err, fault) || !reflect.DeepEqual(refused, IssueRecord{}) || !reflect.DeepEqual(state, createFieldsState(t, ctx, s)) {
				t.Fatalf("initial rollback leaked effects: %+v %v", refused, err)
			}
			current, err := s.CreateIssue(ctx, "beads/work", request)
			if err != nil {
				t.Fatal(err)
			}
			initial := current
			check := func(updates int) {
				t.Helper()
				assertIssueEditCounts(t, ctx, s, current.Properties.ID, 1+updates)
				var ordinal, audits, created, edited, labels int
				if err := s.db.QueryRowContext(ctx, "SELECT current_revision FROM issues WHERE id=?", current.Properties.ID).Scan(&ordinal); err != nil || ordinal != 1+updates {
					t.Fatalf("ordinal=%d: %v", ordinal, err)
				}
				if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*), SUM(event_type='created' AND actor='author'), SUM(event_type='updated' AND actor='editor'), SUM(event_type='label_added' AND actor='author') FROM events WHERE issue_id=?", current.Properties.ID).Scan(&audits, &created, &edited, &labels); err != nil || audits != 3+updates || created != 1 || edited != updates || labels != 2 {
					t.Fatalf("native audits=%d/%d/%d/%d: %v", audits, created, edited, labels, err)
				}
				rows, err := s.db.QueryContext(ctx, "SELECT op,actor FROM bd_events_journal WHERE issue_id=? ORDER BY seq", current.Properties.ID)
				if err != nil {
					t.Fatal(err)
				}
				actual := [][2]string{}
				for rows.Next() {
					var e [2]string
					if err := rows.Scan(&e[0], &e[1]); err != nil {
						_ = rows.Close()
						t.Fatal(err)
					}
					actual = append(actual, e)
				}
				if err := errors.Join(rows.Err(), rows.Close()); err != nil {
					t.Fatal(err)
				}
				want := [][2]string{{"create", "author"}}
				for range updates {
					want = append(want, [2]string{"update", "editor"})
				}
				if !reflect.DeepEqual(actual, want) {
					t.Fatalf("journal=%v want=%v", actual, want)
				}
				if current.Properties.Owner != request.Issue.Owner || current.Properties.CreatedBy != request.Issue.CreatedBy {
					t.Fatal("actor overwrote authorship")
				}
				assertAssigneeLeaseCount(t, ctx, s, current.Properties.ID, 0)
				assertIssueEditVersion(t, ctx, s, "beads/work", current)
			}
			check(0)
			for n, patch := range []UpdateIssueRequest{
				{EstimatedMinutes: issuePriority(0), ExternalRef: issueEditString("revised"), SpecID: issueEditString("revised spec"), Design: issueEditString("Revised design"), AppendNotes: issueEditString("Progress")},
				{EstimatedMinutes: issuePriority(30), ExternalRef: issueEditString(""), SpecID: issueEditString("")},
			} {
				patch.Path, patch.Actor, patch.ExpectedRevision = "beads/work", "editor", current.Revision
				state = createFieldsState(t, ctx, s)
				s.afterWrite = func(stage string) error {
					if stage == "issue-retained" {
						return fault
					}
					return nil
				}
				failed, err := s.UpdateIssue(ctx, patch)
				s.afterWrite = nil
				if !errors.Is(err, fault) || !reflect.DeepEqual(failed, IssueMutationResult{}) || !reflect.DeepEqual(state, createFieldsState(t, ctx, s)) {
					t.Fatalf("edit rollback leaked: %+v %v", failed, err)
				}
				changed, err := s.UpdateIssue(ctx, patch)
				if err != nil || !changed.Changed {
					t.Fatalf("edit: %+v %v", changed, err)
				}
				current = changed.Issue
				check(n + 1)
				patch.ExpectedRevision = current.Revision
				patch.AppendNotes = nil
				state = createFieldsState(t, ctx, s)
				noop, err := s.UpdateIssue(ctx, patch)
				if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, current) || !reflect.DeepEqual(state, createFieldsState(t, ctx, s)) {
					t.Fatalf("identical scalar edit was not noop: %+v %v", noop, err)
				}
				check(n + 1)
			}
			if current.Properties.Notes != request.Issue.Notes+"\nProgress" || current.Properties.ExternalRef != nil || current.Properties.SpecID != "" {
				t.Fatal("initial/append or nullable clear changed")
			}
			assertIssueEditVersion(t, ctx, s, "beads/work", initial)
		})
	}
}
