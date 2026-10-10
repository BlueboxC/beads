//go:build cgo

package graphstore

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func reopenFixture(t *testing.T, backend string) (context.Context, Options, *Store, IssueRecord, IssueRecord, LinkRecord) {
	t.Helper()
	ctx, o := issueExperimentOptions(t, backend)
	s, err := OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	source, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.CreateIssue(ctx, "beads/prereq", plainIssue("Prerequisite"))
	if err != nil {
		t.Fatal(err)
	}
	link, err := s.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work", TargetPath: "beads/prereq", Actor: "author"})
	if err != nil {
		t.Fatal(err)
	}
	if source.ID != link.Source.ID {
		t.Fatal("source identity changed")
	}
	return ctx, o, s, link.Source, target, link.Link
}

// Supplement the existing authority/retained-state fingerprint with the
// existing local journal and lease surfaces touched by the domain writer.
func reopenState(t *testing.T, ctx context.Context, s *Store) map[string]string {
	t.Helper()
	state := workflowState(t, ctx, s)
	for _, table := range []string{"bd_events_journal", "leases"} {
		rows, err := s.db.QueryContext(ctx, "SELECT * FROM "+table)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		entries := []string{}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			entries = append(entries, string(raw))
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(entries)
		state[table] = strings.Join(entries, "\n")
	}
	return state
}

func assertReopenEvents(t *testing.T, ctx context.Context, s *Store, id string, reopened, commented int) {
	t.Helper()
	for _, tc := range []struct {
		kind string
		want int
	}{{"reopened", reopened}, {"commented", commented}} {
		var got int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND event_type=?", id, tc.kind).Scan(&got); err != nil || got != tc.want {
			t.Fatalf("%s events=%d want%d: %v", tc.kind, got, tc.want, err)
		}
	}
}

func TestIssueReopenLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o, s, source, target, link := reopenFixture(t, backend)
			assertReadyIDs(t, ctx, s, target.ID)
			closed, err := s.CloseIssue(ctx, "beads/prereq", "reason A", "closer")
			if err != nil {
				t.Fatal(err)
			}
			assertReadyIDs(t, ctx, s, source.ID)
			var oldLock string
			if err := s.db.QueryRowContext(ctx, "SELECT row_lock FROM issues WHERE id=?", target.Properties.ID).Scan(&oldLock); err != nil {
				t.Fatal(err)
			}
			opened, err := s.ReopenIssue(ctx, "beads/prereq", "Needs another look — 雪", "reopener")
			if err != nil {
				t.Fatal(err)
			}
			// Recomputing this dependent's derived readiness must not mint
			// its retained version or change any projected graph bytes.
			assertIssueEditCounts(t, ctx, s, source.Properties.ID, 2)
			var sourceOrdinal int
			if err := s.db.QueryRowContext(ctx, "SELECT current_revision FROM issues WHERE id=?", source.Properties.ID).Scan(&sourceOrdinal); err != nil || sourceOrdinal != 2 {
				t.Fatalf("dependent ordinal=%d want2: %v", sourceOrdinal, err)
			}
			if dependent, err := s.ShowIssue(ctx, "beads/work"); err != nil || !reflect.DeepEqual(dependent, source) {
				t.Fatalf("readiness recompute changed dependent graph record: got=%+v want=%+v err=%v", dependent, source, err)
			}
			p := opened.Issue.Properties
			if !opened.Changed || opened.Issue.ID != target.ID || opened.Issue.Type != target.Type || opened.Issue.Version == closed.Issue.Version || p.Status != types.StatusOpen || p.ClosedAt != nil || p.CloseReason != "" || p.ClosedBySession != "" || p.DeferUntil != nil || opened.Issue.Attribution.Actor != "reopener" || !reflect.DeepEqual(opened.Issue.Owned, target.Owned) {
				t.Fatalf("incomplete reopened Issue: %+v properties=%+v", opened, p)
			}
			var newLock string
			var ordinal int
			if err := s.db.QueryRowContext(ctx, "SELECT row_lock,current_revision FROM issues WHERE id=?", target.Properties.ID).Scan(&newLock, &ordinal); err != nil || newLock == oldLock || ordinal != 3 {
				t.Fatalf("lock/ordinal old=%s new=%s ordinal=%d: %v", oldLock, newLock, ordinal, err)
			}
			assertIssueEditCounts(t, ctx, s, target.Properties.ID, 3)
			assertReopenEvents(t, ctx, s, target.Properties.ID, 1, 1)
			assertReadyIDs(t, ctx, s, target.ID)
			before := reopenState(t, ctx, s)
			noop, err := s.ReopenIssue(ctx, "beads/prereq", "different ignored reason", "another actor")
			if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, opened.Issue) || !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatalf("no-op changed persisted state: %+v %v", noop, err)
			}
			closedB, err := s.CloseIssue(ctx, "beads/prereq", "reason B", "closer")
			if err != nil || closedB.Issue.Properties.CloseReason != "reason B" {
				t.Fatalf("amend closure: %+v %v", closedB, err)
			}
			assertReadyIDs(t, ctx, s, source.ID)
			// Close the dependent while its prerequisite is still done, then
			// reopen that prerequisite before reopening the owned source.
			sourceClosed, err := s.CloseIssue(ctx, "beads/work", "pause", "closer")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReopenIssue(ctx, "beads/prereq", "", "reopener"); err != nil {
				t.Fatal(err)
			}
			assertReopenEvents(t, ctx, s, target.Properties.ID, 2, 1)
			sourceOpened, err := s.ReopenIssue(ctx, "beads/work", "resume", "reopener")
			if err != nil {
				t.Fatal(err)
			}
			if !sourceOpened.Changed || !reflect.DeepEqual(sourceOpened.Issue.Owned, source.Owned) {
				t.Fatalf("owned state lost: %+v", sourceOpened)
			}
			assertReadyIDs(t, ctx, s, target.ID)
			if got, err := s.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, link) {
				t.Fatalf("Dependency changed: %+v %v", got, err)
			}
			assertIssueEditCounts(t, ctx, s, source.Properties.ID, 4)
			for _, saved := range []IssueRecord{target, closed.Issue, opened.Issue, closedB.Issue} {
				assertIssueEditVersion(t, ctx, s, "beads/prereq", saved)
			}
			for _, saved := range []IssueRecord{source, sourceClosed.Issue, sourceOpened.Issue} {
				assertIssueEditVersion(t, ctx, s, "beads/work", saved)
			}
			if _, err := s.CurrentSnapshot(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := restarted.Close(); err != nil {
					t.Error(err)
				}
			}()
			if got, err := restarted.ShowIssue(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, sourceOpened.Issue) {
				t.Fatalf("restart: %+v %v", got, err)
			}
			assertIssueEditVersion(t, ctx, restarted, "beads/prereq", closed.Issue)
		})
	}
}

func TestIssueReopenCustomCategories(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			// Install categories through existing configuration tables solely as test
			// setup. Issue payloads/snapshots are authored normally, never SQL-seeded.
			for _, tc := range []struct{ name, category string }{{"archived", "done"}, {"triaged", "active"}, {"testing", "wip"}, {"on-ice", "frozen"}} {
				if _, err := s.db.ExecContext(ctx, "INSERT INTO custom_statuses (name,category) VALUES (?,?)", tc.name, tc.category); err != nil {
					t.Fatal(err)
				}
				request := plainIssue(tc.name)
				request.Issue.Status = types.Status(tc.name)
				original, err := s.CreateIssue(ctx, "beads/"+tc.name, request)
				if err != nil {
					t.Fatal(err)
				}
				before := reopenState(t, ctx, s)
				got, err := s.ReopenIssue(ctx, "beads/"+tc.name, "review category", "actor")
				if err != nil {
					t.Fatal(err)
				}
				if tc.category == "done" {
					if !got.Changed || got.Issue.Properties.Status != types.StatusOpen || got.Issue.Version == original.Version {
						t.Fatalf("custom done did not reopen: %+v", got)
					}
					assertIssueEditCounts(t, ctx, s, original.Properties.ID, 2)
					assertReopenEvents(t, ctx, s, original.Properties.ID, 1, 1)
					assertIssueEditVersion(t, ctx, s, "beads/"+tc.name, original)
				} else if got.Changed || !reflect.DeepEqual(got.Issue, original) || !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
					t.Fatalf("non-done %s changed: %+v", tc.name, got)
				}
			}
		})
	}
}

func TestIssueReopenRefusalRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o, s, _, target, _ := reopenFixture(t, backend)
			closed, err := s.CloseIssue(ctx, "beads/prereq", "closed", "closer")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/memory", Body: "unchanged"}); err != nil {
				t.Fatal(err)
			}
			before := reopenState(t, ctx, s)
			for _, tc := range []struct {
				name, path, reason, actor string
				want                      error
			}{
				{"missing", "beads/missing", "", "actor", ErrNotFound},
				{"memory", "beads/memory", "", "actor", storage.ErrValidation},
				{"link", "links/block", "", "actor", storage.ErrValidation},
				{"malformed", "beads/../x", "", "actor", storage.ErrValidation},
				{"empty-actor", "beads/prereq", "", "", storage.ErrValidation},
				{"actor-utf8", "beads/prereq", "", string([]byte{255}), storage.ErrValidation},
				{"reason-utf8", "beads/prereq", string([]byte{255}), "actor", storage.ErrValidation},
			} {
				got, err := s.ReopenIssue(ctx, tc.path, tc.reason, tc.actor)
				if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, IssueMutationResult{}) {
					t.Fatalf("%s: %+v %v", tc.name, got, err)
				}
			}
			for _, stage := range []string{"coordination", "issue-reopen", "issue-retained", "source-catalog", "source-retained"} {
				fault := errors.New("injected reopen failure")
				s.afterWrite = func(at string) error {
					if at == stage {
						return fault
					}
					return nil
				}
				got, err := s.ReopenIssue(ctx, "beads/prereq", "would reopen", "actor")
				s.afterWrite = nil
				if !errors.Is(err, fault) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
					t.Fatalf("stage %s: %+v %v", stage, got, err)
				}
			}
			canceled, cancel := context.WithCancel(ctx)
			s.afterWrite = func(stage string) error {
				if stage == "source-retained" {
					cancel()
					return canceled.Err()
				}
				return nil
			}
			got, err := s.ReopenIssue(canceled, "beads/prereq", "cancelled", "actor")
			cancel()
			s.afterWrite = nil
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, IssueMutationResult{}) {
				t.Fatalf("cancel: %+v %v", got, err)
			}
			s.options.Binding.AuthorityID = "ffffffffffffffffffffffffffffffff"
			got, err = s.ReopenIssue(ctx, "beads/prereq", "wrong authority", "actor")
			s.options = o
			if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, IssueMutationResult{}) {
				t.Fatalf("authority: %+v %v", got, err)
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("refusal changed state")
			}
			assertIssueEditVersion(t, ctx, s, "beads/prereq", closed.Issue)
			assertIssueEditCounts(t, ctx, s, target.Properties.ID, 2)
			// Corrupt retained admission must not be bypassed even for a domain verb.
			if _, err := s.db.ExecContext(ctx, "DELETE FROM graph_preview_issue_versions WHERE path=? AND version=?", "beads/prereq", closed.Issue.Version); err != nil {
				t.Fatal(err)
			}
			corrupted := reopenState(t, ctx, s)
			got, err = s.ReopenIssue(ctx, "beads/prereq", "corrupt", "actor")
			if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(corrupted, reopenState(t, ctx, s)) {
				t.Fatalf("corrupt admission: %+v %v", got, err)
			}
		})
	}
}

func TestIssueReopenConcurrentWriters(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		for _, other := range []string{"reopen", "text"} {
			t.Run(backend+"/"+other, func(t *testing.T) {
				baseCtx, o, first, source, _, link := reopenFixture(t, backend)
				if _, err := first.CloseIssue(baseCtx, "beads/prereq", "prerequisite done", "closer"); err != nil {
					t.Fatal(err)
				}
				closed, err := first.CloseIssue(baseCtx, "beads/work", "closed", "closer")
				if err != nil {
					t.Fatal(err)
				}
				// Exercise empty readiness deterministically before either writer wins.
				assertReadyIDs(t, baseCtx, first)
				ctx, cancel := context.WithCancel(baseCtx)
				defer cancel()
				second := &Store{db: first.db, options: o}
				if backend == "server" {
					second, err = OpenExisting(ctx, o)
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := second.Close(); err != nil {
							t.Error(err)
						}
					}()
				}
				type outcome struct {
					result IssueMutationResult
					err    error
				}
				results := make(chan outcome, 2)
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
				var writers sync.WaitGroup
				writers.Add(2)
				defer func() { cancel(); writers.Wait(); first.afterWrite = nil; second.afterWrite = nil }()
				go func() {
					defer writers.Done()
					got, err := first.ReopenIssue(ctx, "beads/work", "again", "one")
					results <- outcome{got, err}
				}()
				go func() {
					defer writers.Done()
					var got IssueMutationResult
					var err error
					if other == "reopen" {
						got, err = second.ReopenIssue(ctx, "beads/work", "other reason", "two")
					} else {
						got, err = second.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "two", Unconditional: true, Title: issueEditString("edited while reopening")})
					}
					results <- outcome{got, err}
				}()
				if backend == "server" {
					for range 2 {
						select {
						case <-reached:
						case got := <-results:
							t.Fatalf("before forced overlap: %+v", got)
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					close(release)
				}
				changed, conflicts := 0, 0
				for range 2 {
					select {
					case got := <-results:
						if got.err == nil {
							if got.result.Changed {
								changed++
							}
						} else if errors.Is(got.err, ErrConflict) && !errors.Is(got.err, ErrOutcomeUnknown) && reflect.DeepEqual(got.result, IssueMutationResult{}) {
							conflicts++
						} else {
							t.Fatalf("writer: %+v", got)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				writers.Wait()
				first.afterWrite, second.afterWrite = nil, nil
				expectedChanges := 1
				if backend == "embedded" && other == "text" {
					expectedChanges = 2
				}
				expectedConflicts := 0
				if backend == "server" {
					expectedConflicts = 1
				}
				if changed != expectedChanges || conflicts != expectedConflicts {
					t.Fatalf("changed=%d conflicts=%d", changed, conflicts)
				}
				current, err := first.ShowIssue(ctx, "beads/work")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(current.Owned, source.Owned) {
					t.Fatal("race lost owned state")
				}
				if other == "reopen" && current.Properties.Status != types.StatusOpen {
					t.Fatalf("reopen winner not open: %+v", current)
				}
				if other == "text" {
					opened := current.Properties.Status == types.StatusOpen
					edited := current.Properties.Title == "edited while reopening"
					if backend == "server" && opened == edited {
						t.Fatalf("expected one complete winning effect: %+v", current.Properties)
					}
					if backend == "embedded" && (!opened || !edited) {
						t.Fatalf("serialized effects lost: %+v", current.Properties)
					}
				}
				// The prerequisite is closed throughout this race. An open
				// winner is ready and has exactly one accepted reopen audit;
				// a winning text edit leaves the Issue closed with no reopen.
				if current.Properties.Status == types.StatusOpen {
					assertReadyIDs(t, ctx, first, source.ID)
					assertReopenEvents(t, ctx, first, source.Properties.ID, 1, 1)
				} else {
					assertReadyIDs(t, ctx, first)
					assertReopenEvents(t, ctx, first, source.Properties.ID, 0, 0)
				}
				assertIssueEditCounts(t, ctx, first, source.Properties.ID, 3+changed)
				assertIssueEditVersion(t, ctx, first, "beads/work", closed.Issue)
				assertIssueEditVersion(t, ctx, first, "beads/work", current)
				if got, err := first.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, link) {
					t.Fatalf("race changed dependency: %+v %v", got, err)
				}
				if _, err := first.CurrentSnapshot(ctx); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestIssueReopenLostCommitResponse(t *testing.T) {
	ctx, o, direct, source, _, link := reopenFixture(t, "server")
	if _, err := direct.CloseIssue(ctx, "beads/prereq", "prerequisite done", "closer"); err != nil {
		t.Fatal(err)
	}
	closed, err := direct.CloseIssue(ctx, "beads/work", "closed", "closer")
	if err != nil {
		t.Fatal(err)
	}
	proxyPort, observed := startCommitLossProxy(t, net.JoinHostPort(o.ServerHost, strconv.Itoa(o.ServerPort)))
	proxied := o
	proxied.ServerPort = proxyPort
	s, err := OpenExisting(ctx, proxied)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err := s.ReopenIssue(ctx, "beads/work", "commit reply lost", "actor")
	if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrConflict) || !reflect.DeepEqual(got, IssueMutationResult{}) {
		t.Fatalf("lost response: %+v %v", got, err)
	}
	select {
	case packet := <-observed:
		if len(packet) == 0 || packet[0] != 0 {
			t.Fatalf("no server commit witness: %x", packet)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// This success witness is private to the fault harness. The writer did not
	// know its outcome and returned no partial success or automatic replay.
	current, err := direct.ShowIssue(ctx, "beads/work")
	if err != nil || current.Properties.Status != types.StatusOpen || current.Version == closed.Issue.Version || !reflect.DeepEqual(current.Owned, source.Owned) {
		t.Fatalf("witnessed complete reopen missing: %+v %v", current, err)
	}
	assertIssueEditCounts(t, ctx, direct, source.Properties.ID, 4)
	assertReopenEvents(t, ctx, direct, source.Properties.ID, 1, 1)
	assertIssueEditVersion(t, ctx, direct, "beads/work", closed.Issue)
	assertIssueEditVersion(t, ctx, direct, "beads/work", current)
	if actual, err := direct.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(actual, link) {
		t.Fatalf("dependency changed: %+v %v", actual, err)
	}
}
