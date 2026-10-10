//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

func TestMixedCoreCreatorAdmissionBoundary(t *testing.T) {
	for _, character := range []string{"x", "界"} {
		for _, length := range []int{255, 256} {
			request := plainIssue("creator boundary")
			request.Actor = "short-actor"
			request.Issue.CreatedBy = strings.Repeat(character, length)
			err := validateIssueCreate(request)
			if length == 255 && err != nil {
				t.Fatalf("valid %d-rune creator refused: %v", length, err)
			}
			if length == 256 && !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("oversized %d-rune creator needs typed validation refusal, got %v", length, err)
			}
		}
	}
}

// Assert both recorder ownership and its graph mapping. A wrapper's accidental
// second mint can otherwise leave the final postimage looking correct while
// silently doubling native history.
func assertMixedNativeVersionCount(t *testing.T, ctx context.Context, s *Store, issue IssueRecord, want int) {
	t.Helper()
	var versions, mappings, ordinal int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_versions WHERE issue_id=?`, issue.Properties.ID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_preview_issue_versions WHERE issue_id=?`, issue.Properties.ID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT current_revision FROM issues WHERE id=?`, issue.Properties.ID).Scan(&ordinal); err != nil {
		t.Fatal(err)
	}
	if versions != want || mappings != want || ordinal != want {
		t.Fatalf("native versions=%d mappings=%d ordinal=%d; want each %d", versions, mappings, ordinal, want)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT op FROM bd_events_journal WHERE issue_id=? ORDER BY seq`, issue.Properties.ID)
	if err != nil {
		t.Fatal(err)
	}
	var operations []string
	for rows.Next() {
		var operation string
		if err := rows.Scan(&operation); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		operations = append(operations, operation)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	// Native dependency removal journals the derived is_blocked update before
	// its dep_remove row (RecomputeIsBlockedInTxWithResult), while retaining
	// exactly one new Issue version. Reopening with a reason also records a
	// comment before the update. Preserve both existing event vocabularies.
	wantOperations := []string{"create", "update", "dep_add", "update", "dep_remove", "close", "comment", "update"}
	journalCount := want
	if want >= 4 {
		journalCount++
	}
	if want >= 6 {
		journalCount++
	}
	if !reflect.DeepEqual(operations, wantOperations[:journalCount]) {
		t.Fatalf("journal operations=%v; want %v", operations, wantOperations[:journalCount])
	}
}

func TestMixedCoreNativeRecorderExactlyOnce(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			// The native journal is optional and off by default. Exercise its
			// supported operation scope explicitly; this does not enable a new
			// graph CLI configuration contract.
			ctx = issueops.WithEventsJournal(ctx, true)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			request := plainIssue("source")
			request.Actor, request.Issue.Owner, request.Issue.CreatedBy = "operation-actor", "owner@example.invalid", "independent-creator"
			source, err := s.CreateIssue(ctx, "beads/source", request)
			if err != nil {
				t.Fatal(err)
			}
			if source.Properties.Owner != request.Issue.Owner || source.Properties.CreatedBy != request.Issue.CreatedBy || source.Attribution.Actor != request.Actor {
				t.Fatalf("authorship conflated: %+v", source)
			}
			target, err := s.CreateIssue(ctx, "beads/target", plainIssue("target"))
			if err != nil {
				t.Fatal(err)
			}
			assertMixedNativeVersionCount(t, ctx, s, source, 1)
			assertMixedNativeVersionCount(t, ctx, s, target, 1)
			beforeFailure := workflowState(t, ctx, s)
			fault := errors.New("after native version and journal")
			s.afterWrite = func(stage string) error {
				if stage == "issue-retained" {
					return fault
				}
				return nil
			}
			_, updateErr := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/source", Actor: "editor", ExpectedRevision: source.Revision, Title: issueEditString("must roll back")})
			s.afterWrite = nil
			if !errors.Is(updateErr, fault) || !reflect.DeepEqual(beforeFailure, workflowState(t, ctx, s)) {
				t.Fatalf("failed update leaked native state, journal or allocation: %v", updateErr)
			}

			updated, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/source", Actor: "editor", ExpectedRevision: source.Revision, Title: issueEditString("edited")})
			if err != nil || !updated.Changed {
				t.Fatalf("update: %+v %v", updated, err)
			}
			prior := source
			source = updated.Issue
			assertMixedNativeVersionCount(t, ctx, s, source, 2)
			state := workflowState(t, ctx, s)
			noop, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/source", Actor: "editor", ExpectedRevision: source.Revision, Title: issueEditString("edited")})
			if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, source) {
				t.Fatalf("update noop: %+v %v", noop, err)
			}
			if _, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/source", Actor: "editor", ExpectedRevision: prior.Revision, Title: issueEditString("edited")}); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale equal update: %v", err)
			}
			if !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
				t.Fatal("no-op/stale update changed state")
			}

			dependency, err := s.AddDependency(ctx, DependencyRequest{Path: "links/blocks", SourcePath: "beads/source", TargetPath: "beads/target", Actor: "linker", ExpectedSourceRevision: source.Revision})
			if err != nil {
				t.Fatal(err)
			}
			source = dependency.Source
			assertMixedNativeVersionCount(t, ctx, s, source, 3)
			state = workflowState(t, ctx, s)
			same, err := s.AddDependency(ctx, DependencyRequest{Path: "links/blocks", SourcePath: "beads/source", TargetPath: "beads/target", Actor: "other", ExpectedSourceRevision: source.Revision})
			if err != nil || same.Changed {
				t.Fatalf("dependency noop: %+v %v", same, err)
			}
			if !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
				t.Fatal("dependency noop changed state")
			}
			removed, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/blocks", Actor: "unlinker", ExpectedRevision: dependency.Link.Revision, ExpectedSourceRevision: source.Revision})
			if err != nil {
				t.Fatal(err)
			}
			source = removed.Source.(IssueRecord)
			assertMixedNativeVersionCount(t, ctx, s, source, 4)

			closed, err := s.CloseIssue(ctx, "beads/source", "done", "closer")
			if err != nil || !closed.Changed {
				t.Fatalf("close: %+v %v", closed, err)
			}
			source = closed.Issue
			assertMixedNativeVersionCount(t, ctx, s, source, 5)
			state = workflowState(t, ctx, s)
			closed, err = s.CloseIssue(ctx, "beads/source", "already done", "closer")
			if err != nil || closed.Changed {
				t.Fatalf("close noop: %+v %v", closed, err)
			}
			if !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
				t.Fatal("close noop changed state")
			}
			reopened, err := s.ReopenIssue(ctx, "beads/source", "again", "reopener")
			if err != nil || !reopened.Changed {
				t.Fatalf("reopen: %+v %v", reopened, err)
			}
			source = reopened.Issue
			assertMixedNativeVersionCount(t, ctx, s, source, 6)
			state = workflowState(t, ctx, s)
			reopened, err = s.ReopenIssue(ctx, "beads/source", "already open", "reopener")
			if err != nil || reopened.Changed {
				t.Fatalf("reopen noop: %+v %v", reopened, err)
			}
			if !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
				t.Fatal("reopen noop changed state")
			}

			memory, err := s.Create(ctx, CreateRequest{Path: "beads/memory", Body: "knowledge"})
			if err != nil {
				t.Fatal(err)
			}
			relation, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/cites", SourcePath: "beads/source", TargetPath: "beads/memory", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/cites", ExpectedRevision: relation.Link.Revision, Actor: "author"}); err != nil {
				t.Fatal(err)
			}
			owned, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/knows", SourcePath: "beads/memory", TargetPath: "beads/source", ExpectedSourceRevision: memory.Revision, Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/knows", ExpectedRevision: owned.Link.Revision, ExpectedSourceRevision: owned.Source.(Record).Revision, Actor: "author"}); err != nil {
				t.Fatal(err)
			}
			unchanged, err := s.ShowIssue(ctx, "beads/source")
			if err != nil || !reflect.DeepEqual(unchanged, source) {
				t.Fatalf("informational Links changed Issue: %+v %v", unchanged, err)
			}
			assertMixedNativeVersionCount(t, ctx, s, source, 6)
			assertMixedNativeVersionCount(t, ctx, s, target, 1)
		})
	}
}
