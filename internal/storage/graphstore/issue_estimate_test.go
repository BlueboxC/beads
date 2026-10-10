//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

func assertIssueEstimateTransition(t *testing.T, before, after IssueRecord, minutes *int, title, actor string) {
	t.Helper()
	if after.Properties == nil || !reflect.DeepEqual(after.Properties.EstimatedMinutes, minutes) || after.Properties.Title != title || after.Revision == before.Revision || after.Version != after.Revision || after.Attribution.Actor != actor || after.ID != before.ID || after.Type != before.Type || !reflect.DeepEqual(after.Owned, before.Owned) {
		t.Fatalf("incomplete estimate transition: %+v", after)
	}
	properties := *after.Properties
	properties.EstimatedMinutes, properties.Title = before.Properties.EstimatedMinutes, before.Properties.Title
	properties.UpdatedAt, properties.RowVersion, properties.ContentHash = before.Properties.UpdatedAt, before.Properties.RowVersion, before.Properties.ContentHash
	if !reflect.DeepEqual(properties, *before.Properties) {
		t.Fatal("estimate changed unrelated properties, lease or lifecycle state")
	}
}

func TestIssueEstimateLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, dependency := reopenFixture(t, backend)
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: "Unchanged estimate context"})
			if err != nil {
				t.Fatal(err)
			}
			info, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/work", TargetPath: "beads/context", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			if original.Properties.EstimatedMinutes != nil {
				t.Fatal("fixture must start with absent estimate")
			}
			current, versions := original, 2
			// SQL INT is signed32; native validation only rejects negatives. Cover
			// its representable upper boundary without adding a new input policy.
			for _, minutes := range []int{0, 45, 1<<31 - 1} {
				got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "estimator", ExpectedRevision: current.Revision, EstimatedMinutes: &minutes})
				if err != nil || !got.Changed {
					t.Fatalf("estimate %d: %+v %v", minutes, got, err)
				}
				assertIssueEstimateTransition(t, current, got.Issue, &minutes, current.Properties.Title, "estimator")
				assertIssueEditVersion(t, ctx, s, "beads/work", current)
				assertIssueEditVersion(t, ctx, s, "beads/work", got.Issue)
				current, versions = got.Issue, versions+1
				assertIssueEditCounts(t, ctx, s, original.Properties.ID, versions)
			}
			state := reopenState(t, ctx, s)
			s.afterWrite = func(stage string) error { return errors.New("estimate no-op reached " + stage) }
			noop, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "different-actor", ExpectedRevision: current.Revision, EstimatedMinutes: current.Properties.EstimatedMinutes})
			s.afterWrite = nil
			if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, current) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("same value no-op: %+v %v", noop, err)
			}
			stale, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "stale", ExpectedRevision: original.Revision, EstimatedMinutes: current.Properties.EstimatedMinutes})
			if !errors.Is(err, ErrConflict) || !reflect.DeepEqual(stale, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("stale equal estimate bypassed guard: %+v %v", stale, err)
			}
			minutes := 90
			s.afterWrite = func(stage string) error {
				if stage == "coordination" {
					minutes = 900
				}
				return nil
			}
			mixed, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "mixed", ExpectedRevision: current.Revision, EstimatedMinutes: &minutes, Title: issueEditString("Re-estimated work")})
			s.afterWrite = nil
			if err != nil || !mixed.Changed {
				t.Fatalf("copied mixed estimate: %+v %v", mixed, err)
			}
			assertIssueEstimateTransition(t, current, mixed.Issue, issuePriority(90), "Re-estimated work", "mixed")
			assertIssueEditVersion(t, ctx, s, "beads/work", current)
			current, versions = mixed.Issue, versions+1
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, versions)
			omitted, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "text", ExpectedRevision: current.Revision, Title: issueEditString("Estimate omitted")})
			if err != nil || !omitted.Changed {
				t.Fatalf("omitted estimate: %+v %v", omitted, err)
			}
			assertIssueEstimateTransition(t, current, omitted.Issue, current.Properties.EstimatedMinutes, "Estimate omitted", "text")
			assertIssueEditVersion(t, ctx, s, "beads/work", current)
			current, versions = omitted.Issue, versions+1
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, versions)
			if got, err := s.Read(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, current) {
				t.Fatalf("current estimate: %+v %v", got, err)
			}
			assertIssueEditVersion(t, ctx, s, "beads/work", current)
			if got, err := s.Read(ctx, "beads/context"); err != nil || !reflect.DeepEqual(got, memory) {
				t.Fatalf("Memory changed: %+v %v", got, err)
			}
			if got, err := s.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
			for path, want := range map[string]LinkRecord{"links/block": dependency, "links/context": info.Link} {
				if got, err := s.ShowLink(ctx, path); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("Link %s changed: %+v %v", path, got, err)
				}
			}
		})
	}
}

func TestIssueEstimateClaimedAndClosed(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, _, _ := reopenFixture(t, backend)
			claimed, err := s.ClaimIssue(ctx, "beads/work", "holder")
			if err != nil || !claimed.Changed {
				t.Fatalf("claim: %+v %v", claimed, err)
			}
			got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "holder", ExpectedRevision: claimed.Issue.Revision, EstimatedMinutes: issuePriority(30)})
			if err != nil || !got.Changed {
				t.Fatalf("claimed estimate: %+v %v", got, err)
			}
			assertIssueEstimateTransition(t, claimed.Issue, got.Issue, issuePriority(30), claimed.Issue.Properties.Title, "holder")
			assertClaimLease(t, ctx, s, original.Properties.ID, "holder")
			assertIssueEditVersion(t, ctx, s, "beads/work", claimed.Issue)
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, 4)
			if _, err := s.CloseIssue(ctx, "beads/prereq", "done", "reviewer"); err != nil {
				t.Fatal(err)
			}
			closed, err := s.CloseIssue(ctx, "beads/work", "Delivered", "holder")
			if err != nil || !closed.Changed {
				t.Fatalf("close: %+v %v", closed, err)
			}
			got, err = s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "holder", ExpectedRevision: closed.Issue.Revision, EstimatedMinutes: issuePriority(0)})
			if err != nil || !got.Changed {
				t.Fatalf("closed estimate: %+v %v", got, err)
			}
			assertIssueEstimateTransition(t, closed.Issue, got.Issue, issuePriority(0), closed.Issue.Properties.Title, "holder")
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, 6)
			assertIssueEditVersion(t, ctx, s, "beads/work", closed.Issue)
			assertIssueEditVersion(t, ctx, s, "beads/work", got.Issue)
			assertAssigneeLeaseCount(t, ctx, s, original.Properties.ID, 0)
			assertReadyIDs(t, ctx, s)
		})
	}
}

func TestIssueEstimateRefusalAndRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, source, _, _ := reopenFixture(t, backend)
			state := reopenState(t, ctx, s)
			got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "negative", ExpectedRevision: source.Revision, EstimatedMinutes: issuePriority(-1), Title: issueEditString("Must not change")})
			if !errors.Is(err, storage.ErrValidation) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("negative estimate: %+v %v", got, err)
			}
			// Native validation admits nonnegative Go ints; SQL stores a signed
			// INT. On hosts able to represent the next integer, the adapter must
			// roll back native coercion rather than keep it or the sibling edit.
			// Conversion through a variable also keeps this test portable to 32-bit
			// Go, whose public request cannot represent that positive value.
			wide := int64(1) << 31
			overflow := int(wide)
			if int64(overflow) == wide {
				got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "overflow", ExpectedRevision: source.Revision, EstimatedMinutes: &overflow, Title: issueEditString("Overflow must not change title")})
				if !errors.Is(err, storage.ErrValidation) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
					stored := any(nil)
					if got.Issue.Properties != nil && got.Issue.Properties.EstimatedMinutes != nil {
						stored = *got.Issue.Properties.EstimatedMinutes
					}
					t.Fatalf("over-SQL-INT estimate did not roll back: requested=%d stored=%v changed=%v error=%v", overflow, stored, got.Changed, err)
				}
			}
			for _, stage := range []string{"coordination", "issue-update", "issue-retained", "source-catalog", "source-retained"} {
				t.Run(stage, func(t *testing.T) {
					fault := errors.New("estimate rollback")
					s.afterWrite = func(at string) error {
						if at == stage {
							return fault
						}
						return nil
					}
					got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "fault", ExpectedRevision: source.Revision, EstimatedMinutes: issuePriority(15), Title: issueEditString("Must roll back")})
					s.afterWrite = nil
					if !errors.Is(err, fault) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatalf("rollback: %+v %v", got, err)
					}
				})
			}
			canceled, cancel := context.WithCancel(ctx)
			s.afterWrite = func(stage string) error {
				if stage == "source-retained" {
					cancel()
					return canceled.Err()
				}
				return nil
			}
			got, err = s.UpdateIssue(canceled, UpdateIssueRequest{Path: "beads/work", Actor: "cancel", ExpectedRevision: source.Revision, EstimatedMinutes: issuePriority(15)})
			cancel()
			s.afterWrite = nil
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("cancellation: %+v %v", got, err)
			}
			assertIssueEditVersion(t, ctx, s, "beads/work", source)
		})
	}
}

func TestIssueEstimateConcurrentTextWriter(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			base, options, first, source, target, dependency := reopenFixture(t, backend)
			ctx, cancel := context.WithTimeout(base, time.Minute)
			defer cancel()
			second := &Store{db: first.db, options: options}
			if backend == "server" {
				var err error
				second, err = OpenExisting(ctx, options)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := second.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			var beforeEvents int
			if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=?", source.Properties.ID).Scan(&beforeEvents); err != nil {
				t.Fatal(err)
			}
			reached, release := make(chan struct{}, 2), make(chan struct{})
			pause := func(stage string) error {
				// Only server forces independent transaction overlap. Embedded
				// retains its production single-session pool with concurrent callers.
				if backend != "server" || stage != "source-retained" {
					return nil
				}
				reached <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			first.afterWrite, second.afterWrite = pause, pause
			type outcome struct {
				actor string
				value IssueMutationResult
				err   error
			}
			results := make(chan outcome, 2)
			var writers sync.WaitGroup
			writers.Add(2)
			defer func() { cancel(); writers.Wait(); first.afterWrite = nil; second.afterWrite = nil }()
			for i, s := range []*Store{first, second} {
				actor := []string{"estimate-writer", "text-writer"}[i]
				go func() {
					defer writers.Done()
					request := UpdateIssueRequest{Path: "beads/work", Actor: actor, ExpectedRevision: source.Revision}
					if actor == "estimate-writer" {
						request.EstimatedMinutes = issuePriority(60)
					} else {
						request.Title = issueEditString("Competing title")
					}
					value, err := s.UpdateIssue(ctx, request)
					results <- outcome{actor, value, err}
				}()
			}
			if backend == "server" {
				for range 2 {
					select {
					case <-reached:
					case early := <-results:
						t.Fatalf("before forced overlap: %+v", early)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				close(release)
			}
			var winner outcome
			conflicts := 0
			for range 2 {
				select {
				case got := <-results:
					if got.err == nil {
						if winner.actor != "" || !got.value.Changed {
							t.Fatalf("not one changed winner: %+v", got)
						}
						winner = got
					} else if errors.Is(got.err, ErrConflict) && !errors.Is(got.err, ErrOutcomeUnknown) && reflect.DeepEqual(got.value, IssueMutationResult{}) {
						conflicts++
					} else {
						t.Fatalf("bad losing outcome: %+v", got)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			writers.Wait()
			first.afterWrite = nil
			second.afterWrite = nil
			if winner.actor == "" || conflicts != 1 {
				t.Fatalf("winner=%s conflicts=%d", winner.actor, conflicts)
			}
			current, err := first.Read(ctx, "beads/work")
			if err != nil || !reflect.DeepEqual(current, winner.value.Issue) {
				t.Fatalf("not complete winner: %+v %v", current, err)
			}
			minutes, title := source.Properties.EstimatedMinutes, source.Properties.Title
			if winner.actor == "estimate-writer" {
				minutes = issuePriority(60)
			} else {
				title = "Competing title"
			}
			assertIssueEstimateTransition(t, source, winner.value.Issue, minutes, title, winner.actor)
			for _, actor := range []string{"estimate-writer", "text-writer"} {
				want := 0
				if actor == winner.actor {
					want = 1
				}
				var events int
				if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND actor=?", source.Properties.ID, actor).Scan(&events); err != nil || events != want {
					t.Fatalf("audit actor%s count%d want%d: %v", actor, events, want, err)
				}
			}
			var afterEvents int
			if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=?", source.Properties.ID).Scan(&afterEvents); err != nil || afterEvents != beforeEvents+1 {
				t.Fatalf("audit count %d->%d: %v", beforeEvents, afterEvents, err)
			}
			assertIssueEditCounts(t, ctx, first, source.Properties.ID, 3)
			assertIssueEditVersion(t, ctx, first, "beads/work", source)
			assertIssueEditVersion(t, ctx, first, "beads/work", winner.value.Issue)
			if got, err := first.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
			if got, err := first.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, dependency) {
				t.Fatalf("Dependency changed: %+v %v", got, err)
			}
		})
	}
}
