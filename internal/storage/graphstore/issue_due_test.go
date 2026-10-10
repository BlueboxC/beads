//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

func issueDueTime(value time.Time) *time.Time { return &value }
func issueDueField(value *time.Time) publicops.Field[*time.Time] {
	return publicops.Field[*time.Time]{Set: true, Value: value}
}

func assertIssueDueTransition(t *testing.T, before, after IssueRecord, due *time.Time, title, actor string) {
	t.Helper()
	if after.Properties == nil || !reflect.DeepEqual(after.Properties.DueAt, due) || after.Properties.Title != title || after.ID != before.ID || after.Type != before.Type || after.Revision == before.Revision || after.Version != after.Revision || after.Attribution.Actor != actor || !reflect.DeepEqual(after.Owned, before.Owned) {
		t.Fatal("due edit returned incomplete identity, normalized date, attribution or owned state")
	}
	properties := *after.Properties
	properties.DueAt, properties.Title = before.Properties.DueAt, before.Properties.Title
	properties.UpdatedAt, properties.RowVersion, properties.ContentHash = before.Properties.UpdatedAt, before.Properties.RowVersion, before.Properties.ContentHash
	if !reflect.DeepEqual(properties, *before.Properties) {
		t.Fatal("due edit changed unrelated properties or lease state")
	}
}

func TestIssueDueCreate(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, s := createFieldsStore(t, backend)
			for index, tc := range []struct {
				name        string
				input, want time.Time
			}{
				{"offset-round-down", time.Date(2030, 1, 2, 3, 4, 5, 499999999, time.FixedZone("east", 5*3600+1800)), time.Date(2030, 1, 1, 21, 34, 5, 0, time.UTC)},
				{"offset-half-second", time.Date(2030, 1, 2, 3, 4, 5, 500000000, time.FixedZone("west", -7*3600)), time.Date(2030, 1, 2, 10, 4, 6, 0, time.UTC)},
				{"midnight-carry", time.Date(2030, 1, 2, 23, 59, 59, 500000000, time.UTC), time.Date(2030, 1, 3, 0, 0, 0, 0, time.UTC)},
				{"minimum-year", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)},
				{"maximum-year", time.Date(9999, 12, 31, 23, 59, 59, 499999999, time.UTC), time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					request := initialNotesRequest(tc.name)
					input := tc.input
					request.Issue.DueAt = &input
					path := fmt.Sprintf("beads/date-%d", index)
					got, err := s.CreateIssue(ctx, path, request)
					if err != nil {
						t.Fatal(err)
					}
					assertInitialNotes(t, got, request)
					if !reflect.DeepEqual(input, tc.input) || !reflect.DeepEqual(got.Properties.DueAt, &tc.want) {
						t.Fatal("create mutated caller time or failed UTC/second representation")
					}
					assertIssueEditCounts(t, ctx, s, got.Properties.ID, 1)
					assertIssueEditVersion(t, ctx, s, path, got)
					if current, err := s.Read(ctx, path); err != nil || !reflect.DeepEqual(current, got) {
						t.Fatalf("current due create differs: %v", err)
					}
				})
			}
			t.Run("copied-input", func(t *testing.T) {
				request := plainIssue("Copied due")
				input := time.Date(2031, 2, 3, 4, 5, 6, 700000000, time.FixedZone("offset", 3600))
				request.Issue.DueAt = &input
				want := time.Date(2031, 2, 3, 3, 5, 7, 0, time.UTC)
				touched := false
				s.afterWrite = func(stage string) error {
					if stage == "coordination" {
						touched = true
						input = time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)
					}
					return nil
				}
				got, err := s.CreateIssue(ctx, "beads/copied-date", request)
				s.afterWrite = nil
				if err != nil || !touched || got.Properties == nil || !reflect.DeepEqual(got.Properties.DueAt, &want) {
					t.Fatalf("create did not capture caller due: touched=%t err=%v", touched, err)
				}
				assertIssueEditCounts(t, ctx, s, got.Properties.ID, 1)
				assertIssueEditVersion(t, ctx, s, "beads/copied-date", got)
			})
		})
	}
}

func TestIssueDueLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, dependency := reopenFixture(t, backend)
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: "Unchanged due context"})
			if err != nil {
				t.Fatal(err)
			}
			info, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/work", TargetPath: "beads/context", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := s.ClaimIssue(ctx, "beads/work", "holder")
			if err != nil || !claimed.Changed {
				t.Fatalf("claim: %v", err)
			}
			if claimed.Issue.Properties.DueAt != nil {
				t.Fatal("omitted create due date must remain absent")
			}
			current, versions := claimed.Issue, 3
			change := func(request UpdateIssueRequest, want *time.Time, title string) {
				t.Helper()
				request.Path, request.Actor = "beads/work", "holder"
				if !request.Unconditional {
					request.ExpectedRevision = current.Revision
				}
				got, err := s.UpdateIssue(ctx, request)
				if err != nil || !got.Changed {
					t.Fatalf("due change: changed=%t err=%v", got.Changed, err)
				}
				assertIssueDueTransition(t, current, got.Issue, want, title, "holder")
				assertIssueEditVersion(t, ctx, s, "beads/work", current)
				current, versions = got.Issue, versions+1
				assertIssueEditCounts(t, ctx, s, original.Properties.ID, versions)
			}
			input := time.Date(2032, 3, 4, 5, 6, 7, 750000000, time.FixedZone("east", 9*3600))
			want := time.Date(2032, 3, 3, 20, 6, 8, 0, time.UTC)
			change(UpdateIssueRequest{DueAt: issueDueField(&input)}, &want, current.Properties.Title)
			state := createFieldsState(t, ctx, s)
			equivalent := want.In(time.FixedZone("west", -8*3600)).Add(200 * time.Millisecond)
			s.afterWrite = func(stage string) error { return fmt.Errorf("normalized no-op reached %s", stage) }
			noop, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "other", ExpectedRevision: current.Revision, DueAt: issueDueField(&equivalent)})
			s.afterWrite = nil
			if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, current) || !reflect.DeepEqual(state, createFieldsState(t, ctx, s)) {
				t.Fatalf("equivalent normalized instant was not no-op: %v", err)
			}
			stale, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "holder", ExpectedRevision: original.Revision, DueAt: issueDueField(&equivalent)})
			if !errors.Is(err, ErrConflict) || !reflect.DeepEqual(stale, IssueMutationResult{}) || !reflect.DeepEqual(state, createFieldsState(t, ctx, s)) {
				t.Fatalf("stale same-date guard bypassed: %v", err)
			}
			input = time.Date(2033, 4, 5, 6, 7, 8, 600000000, time.UTC)
			captured := time.Date(2033, 4, 5, 6, 7, 9, 0, time.UTC)
			s.afterWrite = func(stage string) error {
				if stage == "coordination" {
					input = time.Date(2045, 1, 1, 0, 0, 0, 0, time.UTC)
				}
				return nil
			}
			change(UpdateIssueRequest{DueAt: issueDueField(&input), Title: issueEditString("Due and title together")}, &captured, "Due and title together")
			s.afterWrite = nil
			// A false Set ignores even a populated Value; it must preserve the due date.
			change(UpdateIssueRequest{Title: issueEditString("Due omitted"), DueAt: publicops.Field[*time.Time]{Value: issueDueTime(time.Time{})}}, &captured, "Due omitted")
			change(UpdateIssueRequest{DueAt: issueDueField(nil), Unconditional: true}, nil, current.Properties.Title)
			state = createFieldsState(t, ctx, s)
			clearNoop, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "other", ExpectedRevision: current.Revision, DueAt: issueDueField(nil)})
			if err != nil || clearNoop.Changed || !reflect.DeepEqual(clearNoop.Issue, current) || !reflect.DeepEqual(state, createFieldsState(t, ctx, s)) {
				t.Fatalf("repeated clear minted state: %v", err)
			}
			staleClear, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "holder", ExpectedRevision: original.Revision, DueAt: issueDueField(nil)})
			if !errors.Is(err, ErrConflict) || !reflect.DeepEqual(staleClear, IssueMutationResult{}) || !reflect.DeepEqual(state, createFieldsState(t, ctx, s)) {
				t.Fatalf("stale equal clear bypassed its guard: %v", err)
			}
			assertClaimLease(t, ctx, s, original.Properties.ID, "holder")
			assertDependencyOwned(t, current, dependency)
			assertIssueEditVersion(t, ctx, s, "beads/work", original)
			assertIssueEditVersion(t, ctx, s, "beads/work", current)
			for path, want := range map[string]any{"beads/work": current, "beads/prereq": target, "beads/context": memory, "links/block": dependency, "links/context": info.Link} {
				if got, err := s.Read(ctx, path); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("due edit changed complete %s: %v", path, err)
				}
			}
		})
	}
}

func TestIssueDueRefusalAndRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, _, _ := reopenFixture(t, backend)
			state := createFieldsState(t, ctx, s)
			for _, tc := range []struct {
				name  string
				value time.Time
			}{
				{"year-zero", time.Date(0, 6, 1, 0, 0, 0, 0, time.UTC)},
				{"year-10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
				{"rounding-overflow", time.Date(9999, 12, 31, 23, 59, 59, 500000000, time.UTC)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					s.afterWrite = func(stage string) error { return fmt.Errorf("invalid date reached %s", stage) }
					request := plainIssue("Invalid due must not exist")
					request.Issue.DueAt = &tc.value
					created, createErr := s.CreateIssue(ctx, "beads/refused", request)
					updated, updateErr := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "author", ExpectedRevision: original.Revision, DueAt: issueDueField(&tc.value), Title: issueEditString("Must not leak")})
					s.afterWrite = nil
					if !errors.Is(createErr, storage.ErrValidation) || !errors.Is(updateErr, storage.ErrValidation) || !reflect.DeepEqual(created, IssueRecord{}) || !reflect.DeepEqual(updated, IssueMutationResult{}) || !reflect.DeepEqual(state, createFieldsState(t, ctx, s)) {
						t.Fatalf("invalid date admitted/mutated: create=%v update=%v", createErr, updateErr)
					}
				})
			}
			for _, stage := range []string{"issue-update", "issue-retained", "source-retained"} {
				t.Run("rollback-"+stage, func(t *testing.T) {
					fault := errors.New("injected due write failure")
					s.afterWrite = func(at string) error {
						if at == stage {
							return fault
						}
						return nil
					}
					got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "author", ExpectedRevision: original.Revision, DueAt: issueDueField(issueDueTime(time.Date(2034, 1, 1, 0, 0, 0, 0, time.UTC))), Title: issueEditString("Must roll back")})
					s.afterWrite = nil
					if !errors.Is(err, fault) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, createFieldsState(t, ctx, s)) {
						t.Fatalf("due rollback leaked: %v", err)
					}
					assertIssueEditVersion(t, ctx, s, "beads/work", original)
					assertIssueEditCounts(t, ctx, s, original.Properties.ID, 2)
				})
			}
		})
	}
}

func TestGraphIssueListDue(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, s := createFieldsStore(t, backend)
			// Keep overdue examples a full day away from wall-clock boundaries.
			center := time.Now().UTC().Truncate(time.Second).Add(-24 * time.Hour)
			records := map[string]IssueRecord{}
			for _, entry := range []struct {
				name   string
				due    *time.Time
				closed bool
			}{
				{"before", issueDueTime(center.Add(-time.Second)), false},
				{"equal", &center, false},
				{"after", issueDueTime(center.Add(time.Second)), false},
				{"future", issueDueTime(center.Add(48 * time.Hour)), false},
				{"closed", issueDueTime(center.Add(-time.Hour)), true},
				{"none", nil, false},
			} {
				request := plainIssue(entry.name)
				request.Issue.DueAt = entry.due
				got, err := s.CreateIssue(ctx, "beads/"+entry.name, request)
				if err != nil {
					t.Fatal(err)
				}
				if entry.closed {
					closed, err := s.CloseIssue(ctx, "beads/"+entry.name, "done", "closer")
					if err != nil {
						t.Fatal(err)
					}
					got = closed.Issue
				}
				records[entry.name] = got
			}
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: "Not an Issue date match"})
			if err != nil {
				t.Fatal(err)
			}
			state := createFieldsState(t, ctx, s)
			offsetFraction := center.In(time.FixedZone("east", 5*3600+1800)).Add(900 * time.Millisecond)
			for _, tc := range []struct {
				name    string
				request publicops.ListRequest
				want    []string
			}{
				{"strict-before", publicops.ListRequest{DueBefore: &center}, []string{"before"}},
				{"strict-after", publicops.ListRequest{DueAfter: &center}, []string{"after", "future"}},
				{"offset-truncated-before", publicops.ListRequest{DueBefore: &offsetFraction}, []string{"before"}},
				{"offset-truncated-after", publicops.ListRequest{DueAfter: &offsetFraction}, []string{"after", "future"}},
				{"exclusive-intersection", publicops.ListRequest{DueAfter: issueDueTime(center.Add(-time.Second)), DueBefore: issueDueTime(center.Add(time.Second))}, []string{"equal"}},
				{"overdue", publicops.ListRequest{OverdueFlag: true}, []string{"before", "equal", "after"}},
				{"overdue-all-excludes-closed", publicops.ListRequest{OverdueFlag: true, AllFlag: true}, []string{"before", "equal", "after"}},
				{"closed-before", publicops.ListRequest{DueBefore: &center, Status: "closed"}, []string{"closed"}},
				{"closed-overdue-intersection", publicops.ListRequest{OverdueFlag: true, Status: "closed"}, nil},
				{"all-before", publicops.ListRequest{DueBefore: &center, AllFlag: true}, []string{"before", "closed"}},
				{"false-overdue", publicops.ListRequest{OverdueFlag: false}, []string{"before", "equal", "after", "future", "none"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, err := s.ListIssues(ctx, tc.request)
					if err != nil || got.HasMore || got.Items == nil || len(got.Items) != len(tc.want) {
						t.Fatalf("list due %s: count=%d want=%d more=%t err=%v", tc.name, len(got.Items), len(tc.want), got.HasMore, err)
					}
					expected := map[string]IssueRecord{}
					for _, name := range tc.want {
						expected[records[name].ID] = records[name]
					}
					for _, item := range got.Items {
						want, ok := expected[item.ID]
						if !ok || !reflect.DeepEqual(item, want) {
							t.Fatal("due list returned unexpected/incomplete record")
						}
						delete(expected, item.ID)
					}
					if len(expected) != 0 {
						t.Fatal("due list omitted expected record")
					}
				})
			}
			if !reflect.DeepEqual(state, createFieldsState(t, ctx, s)) {
				t.Fatal("due listing mutated state")
			}
			for name, record := range records {
				assertIssueEditVersion(t, ctx, s, "beads/"+name, record)
			}
			if got, err := s.Read(ctx, "beads/context"); err != nil || !reflect.DeepEqual(got, memory) {
				t.Fatalf("due listing changed Memory: %v", err)
			}
			if !reflect.DeepEqual(offsetFraction, center.In(time.FixedZone("east", 5*3600+1800)).Add(900*time.Millisecond)) {
				t.Fatal("query mutated caller bound")
			}
		})
	}
}

func TestGraphIssueListDueAdmission(t *testing.T) {
	for _, value := range []time.Time{time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		for _, before := range []bool{true, false} {
			request := publicops.ListRequest{DueAfter: &value}
			if before {
				request = publicops.ListRequest{DueBefore: &value}
			}
			var s *Store
			got, err := s.ListIssues(context.Background(), request)
			if !errors.Is(err, storage.ErrValidation) || !reflect.DeepEqual(got, IssueListPage{}) {
				t.Fatalf("invalid bound reached storage: %v", err)
			}
		}
	}
}

func TestIssueDueConcurrentScalarWriter(t *testing.T) {
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
			if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND actor IN ('due-writer','priority-writer')", source.Properties.ID).Scan(&priorWriters); err != nil || priorWriters != 0 {
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
				value, err := first.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "due-writer", ExpectedRevision: source.Revision, DueAt: issueDueField(issueDueTime(time.Date(2035, 1, 2, 3, 4, 5, 0, time.UTC)))})
				results <- outcome{"due", value, err}
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
			wantDue, wantPriority := source.Properties.DueAt, source.Properties.Priority
			if winner.kind == "due" {
				wantDue = issueDueTime(time.Date(2035, 1, 2, 3, 4, 5, 0, time.UTC))
			} else {
				wantPriority = 0
			}
			if !reflect.DeepEqual(current.Properties.DueAt, wantDue) || current.Properties.Priority != wantPriority {
				t.Fatal("partial or lost winning fields")
			}
			properties := *current.Properties
			properties.DueAt = source.Properties.DueAt
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
			loser := "due-writer"
			if winner.kind == "due" {
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
