//go:build cgo

package graphstore

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// The append itself is well below the existing workspace budget. Its current
// payload plus retained copy must also fit alongside unrelated live content.
// All content is authored through the normal graph API; no SQL payload fixture
// or new per-input size contract is involved.
func TestIssueAppendNotesPreservesCurrentReadBudget(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, dependency := reopenFixture(t, backend)
			filler, err := s.Create(ctx, CreateRequest{Path: "beads/large-context", Title: "Unrelated live context", Body: strings.Repeat("m", 7<<20)})
			if err != nil {
				t.Fatal(err)
			}
			// Roughly 14MiB is charged for the Memory's current/retained copies.
			// A modest 64KiB append and its retained copy still fit comfortably.
			accepted, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "small-append", ExpectedRevision: original.Revision, AppendNotes: issueEditString(strings.Repeat("s", 64<<10))})
			if err != nil || !accepted.Changed {
				t.Fatalf("reasonable append refused: changed=%t err=%v", accepted.Changed, err)
			}
			if accepted.Issue.Properties == nil || accepted.Issue.Properties.Notes != strings.Repeat("s", 64<<10) || accepted.Issue.Revision == original.Revision || accepted.Issue.Attribution.Actor != "small-append" || !reflect.DeepEqual(accepted.Issue.Owned, original.Owned) {
				t.Fatal("reasonable append returned an incomplete record")
			}
			properties := *accepted.Issue.Properties
			properties.Notes, properties.UpdatedAt = original.Properties.Notes, original.Properties.UpdatedAt
			properties.ContentHash, properties.RowVersion = original.Properties.ContentHash, original.Properties.RowVersion
			if !reflect.DeepEqual(properties, *original.Properties) {
				t.Fatal("reasonable append changed unrelated Issue properties")
			}
			if got, err := s.Read(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, accepted.Issue) {
				t.Fatalf("accepted append is not publicly readable: %v", err)
			}
			if _, err := s.CurrentSnapshot(ctx); err != nil {
				t.Fatalf("accepted workspace inventory refused: %v", err)
			}
			state := reopenState(t, ctx, s)
			// Total authored large text remains below 9MiB, but the extra 1.5MiB
			// notes and retained copy would push whole-workspace acquisition past
			// 16MiB. Refuse the entire mutation, including its companion title.
			for _, guarded := range []bool{true, false} {
				name := "unconditional"
				if guarded {
					name = "guarded"
				}
				if !t.Run(name, func(t *testing.T) {
					request := UpdateIssueRequest{Path: "beads/work", Actor: "oversized-append", Unconditional: !guarded, AppendNotes: issueEditString(strings.Repeat("x", 1536<<10)), Title: issueEditString("Must roll back")}
					if guarded {
						request.ExpectedRevision = accepted.Issue.Revision
					}
					got, err := s.UpdateIssue(ctx, request)
					if !errors.Is(err, ErrLimitExceeded) || !reflect.DeepEqual(got, IssueMutationResult{}) {
						t.Fatalf("over-budget append must refuse with zero result: changed=%t err=%v", got.Changed, err)
					}
					// Includes live Issue notes/row token, lease, events, coordinator,
					// retained versions and graph mappings after the failed attempt.
					if !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatal("over-budget append leaked state")
					}
					if got, err := s.Read(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, accepted.Issue) {
						t.Fatalf("refusal left Issue unreadable or changed: %v", err)
					}
					if got, err := s.Read(ctx, "beads/large-context"); err != nil || !reflect.DeepEqual(got, filler) {
						t.Fatalf("refusal left unrelated Memory unreadable or changed: %v", err)
					}
					if _, err := s.CurrentSnapshot(ctx); err != nil {
						t.Fatalf("refusal left inventory unreadable: %v", err)
					}
					assertIssueEditCounts(t, ctx, s, original.Properties.ID, 3)
					assertIssueEditVersion(t, ctx, s, "beads/work", original)
					assertIssueEditVersion(t, ctx, s, "beads/work", accepted.Issue)
					if got, err := s.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
						t.Fatalf("target changed: %v", err)
					}
					if got, err := s.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, dependency) {
						t.Fatalf("Dependency changed: %v", err)
					}
				}) {
					return // Stop after the first failure; RED must not append again.
				}
			}
		})
	}
}

func TestIssueAppendNotesCannotStealActiveAssignment(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, _, _ := reopenFixture(t, backend)
			claimed, err := s.ClaimIssue(ctx, "beads/work", "holder")
			if err != nil || !claimed.Changed {
				t.Fatalf("normal claim: %+v %v", claimed, err)
			}
			state := reopenState(t, ctx, s)
			for _, guarded := range []bool{true, false} {
				name := "unconditional"
				if guarded {
					name = "guarded"
				}
				t.Run(name, func(t *testing.T) {
					request := UpdateIssueRequest{Path: "beads/work", Actor: "outsider", Unconditional: !guarded, Assignee: issueEditString("outsider"), AppendNotes: issueEditString("Must not append when transfer refuses")}
					if guarded {
						request.ExpectedRevision = claimed.Issue.Revision
					}
					got, err := s.UpdateIssue(ctx, request)
					if !errors.Is(err, storage.ErrAlreadyClaimed) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatalf("append bypassed active-assignment fence: %+v %v", got, err)
					}
					if got, err := s.Read(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, claimed.Issue) {
						t.Fatalf("refused mixed edit changed claim: %+v %v", got, err)
					}
					assertClaimLease(t, ctx, s, original.Properties.ID, "holder")
					assertIssueEditCounts(t, ctx, s, original.Properties.ID, 3)
					assertIssueEditVersion(t, ctx, s, "beads/work", original)
					assertIssueEditVersion(t, ctx, s, "beads/work", claimed.Issue)
				})
			}
		})
	}
}
