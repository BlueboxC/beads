//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestIssueAssigneeConcurrentPriorityWriter(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			base, options, first, source, target, dependency := reopenFixture(t, backend)
			ctx, cancel := context.WithTimeout(base, time.Minute)
			defer cancel()
			// Ordinary server forces overlapping transactions; embedded uses its normal
			// one-session pool with concurrent callers, not an unsupported pool topology.
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
			// Writer actor names are fresh in this fixture, so all matching rows after
			// the race are from this attempt rather than prior create/Dependency events.
			var priorWriters int
			if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND actor IN ('assignment-writer','priority-writer')", source.Properties.ID).Scan(&priorWriters); err != nil || priorWriters != 0 {
				t.Fatalf("unexpected baseline actors: %d %v", priorWriters, err)
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
				kind  string
				value IssueMutationResult
				err   error
			}
			results := make(chan outcome, 2)
			var writers sync.WaitGroup
			writers.Add(2)
			defer func() { cancel(); writers.Wait(); first.afterWrite, second.afterWrite = nil, nil }()
			go func() {
				defer writers.Done()
				value, err := first.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "assignment-writer", ExpectedRevision: source.Revision, Assignee: issueEditString("new-owner")})
				results <- outcome{"assignment", value, err}
			}()
			go func() {
				defer writers.Done()
				value, err := second.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "priority-writer", ExpectedRevision: source.Revision, Priority: issuePriority(0)})
				results <- outcome{"priority", value, err}
			}()
			if backend == "server" {
				for range 2 {
					select {
					case <-reached:
					case early := <-results:
						t.Fatalf("writer exited before overlap: %+v", early)
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
				case result := <-results:
					if result.err == nil {
						if winner.kind != "" || !result.value.Changed {
							t.Fatalf("not a single changed winner: %+v", result)
						}
						winner = result
					} else {
						if !errors.Is(result.err, ErrConflict) || errors.Is(result.err, ErrOutcomeUnknown) || !reflect.DeepEqual(result.value, IssueMutationResult{}) {
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
			if winner.kind == "" || conflicts != 1 {
				t.Fatalf("winner=%s conflicts=%d", winner.kind, conflicts)
			}
			current, err := first.ShowIssue(ctx, "beads/work")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(current, winner.value.Issue) || current.Revision == source.Revision || current.Attribution.Actor != winner.kind+"-writer" {
				t.Fatal("current is not complete winning result")
			}
			wantAssignee, wantPriority := source.Properties.Assignee, source.Properties.Priority
			if winner.kind == "assignment" {
				wantAssignee = "new-owner"
			} else {
				wantPriority = 0
			}
			if current.Properties.Assignee != wantAssignee || current.Properties.Priority != wantPriority {
				t.Fatal("partial or lost winning fields")
			}
			properties := *current.Properties
			properties.Assignee = source.Properties.Assignee
			properties.Priority = source.Properties.Priority
			properties.UpdatedAt = source.Properties.UpdatedAt
			properties.ContentHash = source.Properties.ContentHash
			properties.RowVersion = source.Properties.RowVersion
			if !reflect.DeepEqual(properties, *source.Properties) || !reflect.DeepEqual(current.Owned, source.Owned) {
				t.Fatal("unrelated properties/owned state changed")
			}
			var afterEvents, winnerEvents, loserEvents int
			if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=?", source.Properties.ID).Scan(&afterEvents); err != nil {
				t.Fatal(err)
			}
			if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND actor=? AND event_type='updated'", source.Properties.ID, winner.kind+"-writer").Scan(&winnerEvents); err != nil {
				t.Fatal(err)
			}
			loser := "assignment-writer"
			if winner.kind == "assignment" {
				loser = "priority-writer"
			}
			if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND actor=?", source.Properties.ID, loser).Scan(&loserEvents); err != nil {
				t.Fatal(err)
			}
			if afterEvents != beforeEvents+1 || winnerEvents != 1 || loserEvents != 0 {
				t.Fatalf("audit leak: total %d->%d winner%d loser%d", beforeEvents, afterEvents, winnerEvents, loserEvents)
			}
			assertAssigneeLeaseCount(t, ctx, first, source.Properties.ID, 0)
			assertIssueEditCounts(t, ctx, first, source.Properties.ID, 3)
			for _, saved := range []IssueRecord{source, current} {
				assertIssueListRetained(t, ctx, first, "beads/work", saved)
			}
			if got, err := first.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
			if got, err := first.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, dependency) {
				t.Fatalf("Dependency changed: %+v %v", got, err)
			}
		})
	}
}
