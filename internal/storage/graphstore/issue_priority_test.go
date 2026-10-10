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
	"github.com/steveyegge/beads/internal/types"
)

func issuePriority(value int) *int { return &value }

func TestIssuePriorityLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, current, target, dependency := reopenFixture(t, backend)
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: "memory context"})
			if err != nil {
				t.Fatal(err)
			}
			info, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/work", TargetPath: "beads/context", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			saved := []IssueRecord{current}
			count := 2 // creation plus the owned Dependency
			for priority := 0; priority <= 4; priority++ {
				before := current
				supplied := priority
				// The accepted request is copied before transaction work, so a
				// caller's later value change cannot alter this admitted mutation.
				s.afterWrite = func(stage string) error {
					if stage == "coordination" {
						supplied = 99
					}
					return nil
				}
				edited, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "priority-editor", ExpectedRevision: before.Revision, Priority: &supplied})
				s.afterWrite = nil
				if err != nil || !edited.Changed || edited.Issue.Properties.Priority != priority || edited.Issue.Revision == before.Revision || edited.Issue.Attribution.Actor != "priority-editor" {
					t.Fatalf("P%d: %+v %v", priority, edited, err)
				}
				// Compare all other Issue properties, allowing only writer-maintained
				// update/hash/row-lock fields and the requested priority to differ.
				properties := *edited.Issue.Properties
				properties.Priority = before.Properties.Priority
				properties.UpdatedAt = before.Properties.UpdatedAt
				properties.ContentHash = before.Properties.ContentHash
				properties.RowVersion = before.Properties.RowVersion
				if !reflect.DeepEqual(properties, *before.Properties) || !reflect.DeepEqual(edited.Issue.Owned, before.Owned) {
					t.Fatal("priority changed unrelated content or owned state")
				}
				comparison, err := s.CompareVersions(ctx, "beads/work", before.Version, edited.Issue.Version)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, change := range comparison.Changes {
					if change.Area != "properties" || (change.Member != "priority" && change.Member != "updated_at") {
						t.Fatalf("unexpected retained change: %+v", change)
					}
					found = found || change.Member == "priority"
				}
				if !found {
					t.Fatal("retained comparison omitted priority")
				}
				current = edited.Issue
				saved = append(saved, current)
				count++
				assertIssueEditCounts(t, ctx, s, current.Properties.ID, count)
			}
			// Omitted priority preserves P4; a subsequent combined edit explicitly
			// sets P0 and text in one revision, rather than two scalar writes.
			omitted, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "text-editor", ExpectedRevision: current.Revision, Title: issueEditString("Reprioritize 雪")})
			if err != nil || !omitted.Changed || omitted.Issue.Properties.Priority != 4 {
				t.Fatalf("omitted priority: %+v %v", omitted, err)
			}
			count++
			saved = append(saved, omitted.Issue)
			combined, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "combined-editor", Unconditional: true, Priority: issuePriority(0), Description: issueEditString("updated description")})
			if err != nil || !combined.Changed || combined.Issue.Properties.Priority != 0 || combined.Issue.Properties.Description != "updated description" || combined.Issue.Properties.Title != omitted.Issue.Properties.Title || !reflect.DeepEqual(combined.Issue.Owned, current.Owned) {
				t.Fatalf("combined edit: %+v %v", combined, err)
			}
			count++
			current = combined.Issue
			saved = append(saved, current)
			assertIssueEditCounts(t, ctx, s, current.Properties.ID, count)
			state := reopenState(t, ctx, s)
			for _, unconditional := range []bool{false, true} {
				request := UpdateIssueRequest{Path: "beads/work", Actor: "different-actor", Priority: issuePriority(0), Unconditional: unconditional}
				if !unconditional {
					request.ExpectedRevision = current.Revision
				}
				got, err := s.UpdateIssue(ctx, request)
				if err != nil || got.Changed || !reflect.DeepEqual(got.Issue, current) {
					t.Fatalf("no-op: %+v %v", got, err)
				}
			}
			got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: omitted.Issue.Revision, Priority: issuePriority(0)})
			if !errors.Is(err, ErrConflict) || !reflect.DeepEqual(got, IssueMutationResult{}) {
				t.Fatalf("stale no-op: %+v %v", got, err)
			}
			if !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatal("no-op/refusal changed attribution or state")
			}
			if got, err := s.ReadyIssues(ctx); err != nil || len(got) != 1 || got[0].ID != target.ID {
				t.Fatalf("priority bypassed blocker: %+v %v", got, err)
			}
			for path, want := range map[string]LinkRecord{"links/block": dependency, "links/context": info.Link} {
				if got, err := s.ShowLink(ctx, path); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("Link changed: %+v %v", got, err)
				}
			}
			if got, err := s.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
			if got, err := s.Show(ctx, "beads/context"); err != nil || !reflect.DeepEqual(got, memory) {
				t.Fatalf("Memory changed: %+v %v", got, err)
			}
			for _, record := range saved {
				assertIssueListRetained(t, ctx, s, "beads/work", record)
			}
			closed, err := s.CloseIssue(ctx, "beads/prereq", "finished", "closer")
			if err != nil {
				t.Fatal(err)
			}
			edited, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/prereq", Actor: "editor", ExpectedRevision: closed.Issue.Revision, Priority: issuePriority(0)})
			if err != nil || !edited.Changed || edited.Issue.Properties.Status != types.StatusClosed || !reflect.DeepEqual(edited.Issue.Properties.ClosedAt, closed.Issue.Properties.ClosedAt) || edited.Issue.Properties.CloseReason != closed.Issue.Properties.CloseReason {
				t.Fatalf("closed priority edit: %+v %v", edited, err)
			}
			assertIssueEditCounts(t, ctx, s, target.Properties.ID, 3)
			assertIssueListRetained(t, ctx, s, "beads/prereq", closed.Issue)
			assertIssueListRetained(t, ctx, s, "beads/prereq", edited.Issue)
		})
	}
}

func TestIssuePriorityRefusalAndRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, source, _, _ := reopenFixture(t, backend)
			if _, err := s.CreateIssue(ctx, "beads/second", plainIssue("Second prerequisite")); err != nil {
				t.Fatal(err)
			}
			added, err := s.AddDependency(ctx, DependencyRequest{Path: "links/second", SourcePath: "beads/work", TargetPath: "beads/second", Actor: "author", ExpectedSourceRevision: source.Revision})
			if err != nil {
				t.Fatal(err)
			}
			request := UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: added.Source.Revision, Priority: issuePriority(0), Title: issueEditString("Changed")}
			state := reopenState(t, ctx, s)
			for _, tc := range []struct {
				name     string
				priority int
				revision string
				want     error
			}{
				{"negative", -1, added.Source.Revision, storage.ErrValidation},
				{"above range", 5, added.Source.Revision, storage.ErrValidation},
				{"owned guard stale no-op", source.Properties.Priority, source.Revision, ErrConflict},
			} {
				r := request
				r.Priority = issuePriority(tc.priority)
				r.ExpectedRevision = tc.revision
				r.Title = nil
				result, err := s.UpdateIssue(ctx, r)
				if !errors.Is(err, tc.want) || !reflect.DeepEqual(result, IssueMutationResult{}) {
					t.Fatalf("%s: %+v %v", tc.name, result, err)
				}
			}
			// Existing broad writer tests cover other admission/authority failures.
			// Probe the three stages around the combined domain write and retention.
			for _, stage := range []string{"issue-update", "issue-retained", "source-retained"} {
				fault := errors.New("priority rollback")
				s.afterWrite = func(at string) error {
					if at == stage {
						return fault
					}
					return nil
				}
				result, err := s.UpdateIssue(ctx, request)
				s.afterWrite = nil
				if !errors.Is(err, fault) || !reflect.DeepEqual(result, IssueMutationResult{}) {
					t.Fatalf("%s: %+v %v", stage, result, err)
				}
				if !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
					t.Fatalf("%s leaked priority/text/retained state", stage)
				}
			}
			canceled, cancel := context.WithCancel(ctx)
			s.afterWrite = func(stage string) error {
				if stage == "issue-retained" {
					cancel()
					return canceled.Err()
				}
				return nil
			}
			result, err := s.UpdateIssue(canceled, request)
			cancel()
			s.afterWrite = nil
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, IssueMutationResult{}) {
				t.Fatalf("cancellation: %+v %v", result, err)
			}
			if !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatal("refused or canceled priority edit changed state")
			}
			assertIssueEditCounts(t, ctx, s, source.Properties.ID, 3)
			assertIssueListRetained(t, ctx, s, "beads/work", source)
			assertIssueListRetained(t, ctx, s, "beads/work", added.Source)
		})
	}
}

func TestIssuePriorityConcurrentWriters(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		for _, other := range []string{"text", "dependency"} {
			t.Run(backend+"/"+other, func(t *testing.T) {
				base, options, first := issueListFixture(t, backend)
				ctx, cancel := context.WithTimeout(base, time.Minute)
				defer cancel()
				source, err := first.CreateIssue(ctx, "beads/work", plainIssue("Original"))
				if err != nil {
					t.Fatal(err)
				}
				target, err := first.CreateIssue(ctx, "beads/prereq", plainIssue("Prerequisite"))
				if err != nil {
					t.Fatal(err)
				}
				// Embedded shares the production single-session pool; only ordinary
				// server cases force overlapping transactions before commit.
				second := &Store{db: first.db, options: options}
				if backend == "server" {
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
				reached, release := make(chan struct{}, 2), make(chan struct{})
				pause := func(stage string) error {
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
					kind       string
					issue      IssueMutationResult
					dependency DependencyResult
					err        error
				}
				results := make(chan outcome, 2)
				var writers sync.WaitGroup
				writers.Add(2)
				defer func() { cancel(); writers.Wait() }()
				go func() {
					defer writers.Done()
					value, err := first.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "priority-writer", ExpectedRevision: source.Revision, Priority: issuePriority(0)})
					results <- outcome{kind: "priority", issue: value, err: err}
				}()
				go func() {
					defer writers.Done()
					result := outcome{kind: other}
					if other == "text" {
						result.issue, result.err = second.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "text-writer", ExpectedRevision: source.Revision, Title: issueEditString("Text winner")})
					} else {
						result.dependency, result.err = second.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work", TargetPath: "beads/prereq", Actor: "dependency-writer", ExpectedSourceRevision: source.Revision})
					}
					results <- result
				}()
				if backend == "server" {
					for range 2 {
						select {
						case <-reached:
						case early := <-results:
							t.Fatalf("writer exited before forced overlap: %+v", early)
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					close(release)
				}
				winner := ""
				conflicts := 0
				for range 2 {
					select {
					case result := <-results:
						if result.err == nil {
							if winner != "" {
								t.Fatal("both guarded mutations committed")
							}
							winner = result.kind
							if result.kind != "dependency" && !result.issue.Changed {
								t.Fatal("accepted mutation was incorrectly a no-op")
							}
						} else {
							if !errors.Is(result.err, ErrConflict) || errors.Is(result.err, ErrOutcomeUnknown) || !reflect.DeepEqual(result.issue, IssueMutationResult{}) || !reflect.DeepEqual(result.dependency, DependencyResult{}) {
								t.Fatalf("non-atomic conflict: %+v", result)
							}
							conflicts++
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				writers.Wait()
				first.afterWrite, second.afterWrite = nil, nil
				if winner == "" || conflicts != 1 {
					t.Fatalf("winner=%s conflicts=%d", winner, conflicts)
				}
				current, err := first.ShowIssue(ctx, "beads/work")
				if err != nil {
					t.Fatal(err)
				}
				wantPriority, wantTitle, wantOwned := source.Properties.Priority, source.Properties.Title, 0
				switch winner {
				case "priority":
					wantPriority = 0
				case "text":
					wantTitle = "Text winner"
				case "dependency":
					wantOwned = 1
				}
				if current.Properties.Priority != wantPriority || current.Properties.Title != wantTitle || len(current.Owned) != wantOwned {
					t.Fatalf("partial/lost winning mutation: %+v", current)
				}
				if winner == "dependency" {
					if _, err := first.ShowLink(ctx, "links/block"); err != nil {
						t.Fatal(err)
					}
				} else if _, err := first.ShowLink(ctx, "links/block"); !errors.Is(err, ErrNotFound) {
					t.Fatalf("losing/unrequested Dependency persisted: %v", err)
				}
				if got, err := first.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
					t.Fatalf("target changed: %+v %v", got, err)
				}
				assertIssueEditCounts(t, ctx, first, source.Properties.ID, 2)
				assertIssueListRetained(t, ctx, first, "beads/work", source)
				assertIssueListRetained(t, ctx, first, "beads/work", current)
			})
		}
	}
}
