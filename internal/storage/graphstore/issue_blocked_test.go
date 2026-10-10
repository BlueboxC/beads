//go:build cgo

package graphstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Match the existing retained-record oracle (assertIssueListRetained): compare
// every serialized property/owned record without depending on Go time.Location
// or Go-only current-row fields that are not part of the CLI record.
func sameBlockedJSON(t *testing.T, left, right any) bool {
	t.Helper()
	a, err := canonicalJSON(left)
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalJSON(right)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(a, b)
}

func assertGraphBlocked(t *testing.T, ctx context.Context, s *Store, source IssueRecord, targets ...IssueRecord) {
	t.Helper()
	before := reopenState(t, ctx, s)
	got, err := s.BlockedIssues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []BlockedIssue{}
	if len(targets) > 0 {
		ids := make([]string, 0, len(targets))
		for _, target := range targets {
			ids = append(ids, target.ID)
		}
		sort.Strings(ids) // Canonical IDs use their ASCII percent-encoded spelling.
		want = append(want, BlockedIssue{Issue: source, BlockedBy: ids})
	}
	if !sameBlockedJSON(t, got, want) {
		t.Fatalf("complete blocked result differs: got=%+v want=%+v", got, want)
	}
	if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
		t.Fatal("blocked read changed authoritative or retained state")
	}
}

func TestIssueBlockedLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, source, target, firstLink := reopenFixture(t, backend)
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/context", Title: "Context", Body: "Memory is not schedulable"})
			if err != nil {
				t.Fatal(err)
			}
			info, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/context", TargetPath: "beads/work", ExpectedSourceRevision: memory.Revision, Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			target2, err := s.CreateIssue(ctx, "beads/second", plainIssue("Second"))
			if err != nil {
				t.Fatal(err)
			}
			second, err := s.AddDependency(ctx, DependencyRequest{Path: "links/second", SourcePath: "beads/work", TargetPath: "beads/second", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			source = second.Source
			assertGraphBlocked(t, ctx, s, source, target, target2)
			assertDependencyReadiness(t, ctx, s, source, false)
			assertDependencyOwned(t, source, firstLink, second.Link)
			// Prerequisite status recomputation changes derived blocking only. It
			// preserves the dependent's updated_at and does not remint its graph
			// revision, so this complete source record and guard remain current.
			if _, err := s.CloseIssue(ctx, "beads/prereq", "Done", "closer"); err != nil {
				t.Fatal(err)
			}
			assertGraphBlocked(t, ctx, s, source, target2)
			assertDependencyReadiness(t, ctx, s, source, false)
			if _, err := s.CloseIssue(ctx, "beads/second", "Done", "closer"); err != nil {
				t.Fatal(err)
			}
			assertGraphBlocked(t, ctx, s, source)
			assertDependencyReadiness(t, ctx, s, source, true)
			reopened, err := s.ReopenIssue(ctx, "beads/prereq", "Retry", "reopener")
			if err != nil {
				t.Fatal(err)
			}
			assertGraphBlocked(t, ctx, s, source, reopened.Issue)
			if _, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/block", Actor: "remover", ExpectedRevision: firstLink.Revision, ExpectedSourceRevision: source.Revision}); err != nil {
				t.Fatal(err)
			}
			assertGraphBlocked(t, ctx, s, source)
			after, err := s.ShowIssue(ctx, "beads/work")
			if err != nil {
				t.Fatal(err)
			}
			assertDependencyOwned(t, after, second.Link)
			assertDependencyReadiness(t, ctx, s, after, true)
			assertIssueListRetained(t, ctx, s, "beads/work", source)
			if current, err := s.Read(ctx, "beads/context"); err != nil || !sameBlockedJSON(t, current, info.Source) {
				t.Fatalf("unrelated Memory changed: %v", err)
			}
			if current, err := s.ShowLink(ctx, "links/context"); err != nil || !sameBlockedJSON(t, current, info.Link) {
				t.Fatalf("informational Link changed: %v", err)
			}
		})
	}
}

func TestIssueBlockedIsNotNotReady(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s := issueListFixture(t, backend)
			records := map[string]IssueRecord{}
			for _, status := range []types.Status{types.StatusBlocked, types.StatusDeferred, types.StatusOpen, types.StatusInProgress} {
				request := plainIssue(string(status))
				request.Issue.Status = status
				record, err := s.CreateIssue(ctx, "beads/"+string(status), request)
				if err != nil {
					t.Fatal(err)
				}
				if record.Properties == nil || record.Properties.Status != status {
					t.Fatalf("authored status changed: %+v", record.Properties)
				}
				records[record.Properties.ID] = record
			}
			control, err := s.CreateIssue(ctx, "beads/ready-control", plainIssue("Independent open control"))
			if err != nil {
				t.Fatal(err)
			}
			if control.Properties.Status != types.StatusOpen {
				t.Fatal("ready control is not open")
			}
			records[control.Properties.ID] = control
			before := reopenState(t, ctx, s)
			assertGraphBlocked(t, ctx, s, control)
			// Derive exact Ready IDs from native policy. Initial in-progress status
			// is API-authored here; this fixture makes no claim/lease assertion.
			var wantReady []string
			if err := s.withTx(ctx, false, func(tx *sql.Tx) error {
				blocked, err := issueops.GetBlockedIssuesInTx(ctx, tx, types.WorkFilter{})
				if err != nil {
					return err
				}
				if len(blocked) != 0 {
					return fmt.Errorf("unexpected native blockers: %+v", blocked)
				}
				ready, err := issueops.GetReadyWorkInTx(ctx, tx, types.WorkFilter{})
				if err != nil {
					return err
				}
				controlFound := false
				for _, issue := range ready {
					record, ok := records[issue.ID]
					if !ok {
						return fmt.Errorf("unexpected native ready ID %q", issue.ID)
					}
					if record.Properties.Status == types.StatusBlocked || record.Properties.Status == types.StatusDeferred {
						return fmt.Errorf("manual blocked/deferred fixture unexpectedly ready")
					}
					wantReady = append(wantReady, record.ID)
					controlFound = controlFound || issue.ID == control.Properties.ID
				}
				if !controlFound {
					return fmt.Errorf("independent open positive control missing from native ready")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			assertReadyIDs(t, ctx, s, wantReady...)
			for _, record := range records {
				current, err := s.ShowIssue(ctx, strings.TrimPrefix(record.ID, s.ScopeURL()))
				if err != nil || !sameBlockedJSON(t, current, record) {
					t.Fatalf("inspection changed fixture: %v", err)
				}
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("native/graph inspection changed state")
			}
		})
	}
}

func TestIssueBlockedAdmission(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, _, _, _ := reopenFixture(t, backend)
			for _, tc := range []struct{ name, change string }{
				{"mapping", "UPDATE graph_preview_catalog SET backing_key='missing' WHERE path='beads/prereq'"},
				{"dependency-type", "UPDATE dependencies SET type='relates-to'"},
				{"retained", "UPDATE graph_preview_issue_versions SET owned='[]' WHERE path='beads/work'"},
				{"authority", "UPDATE graph_preview_scope SET authority_id='different' WHERE singleton=1"},
				{"wisp", "INSERT INTO wisps(id,title) VALUES ('unmapped-wisp','Unsupported authority')"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					// These exercise currentSnapshot admission (except the explicit
					// wisp gate), not the defensive post-admission native-result checks.
					// Fixtures roll back and are never installed demonstration data.
					rollback := errors.New("rollback corruption fixture")
					before := reopenState(t, ctx, s)
					err := s.withTx(ctx, false, func(tx *sql.Tx) error {
						if _, err := tx.ExecContext(ctx, tc.change); err != nil {
							return err
						}
						got, err := s.blockedIssuesInTx(ctx, tx)
						if err == nil || got != nil {
							t.Errorf("corrupt authority returned partial/successful view: %+v %v", got, err)
						}
						return rollback
					})
					if !errors.Is(err, rollback) {
						t.Fatalf("corruption fixture did not exercise admission: %v", err)
					}
					if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
						t.Fatal("corruption refusal changed state")
					}
				})
			}
			before := reopenState(t, ctx, s)
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if got, err := s.BlockedIssues(canceled); err == nil || got != nil {
				t.Fatalf("canceled read returned data: %+v %v", got, err)
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("failed read changed state")
			}
		})
	}
}

func TestIssueBlockedReadBudget(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s := issueListFixture(t, backend)
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/large-context", Body: strings.Repeat("m", 9<<20)}); err != nil {
				t.Fatal(err)
			}
			before := reopenState(t, ctx, s)
			if got, err := s.BlockedIssues(ctx); !errors.Is(err, ErrLimitExceeded) || got != nil {
				t.Fatalf("empty blocking result bypassed acquisition bound: %+v %v", got, err)
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("bounded refusal changed state")
			}
		})
	}
}

func TestIssueBlockedConcurrentClose(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, options, s, source, target, _ := reopenFixture(t, backend)
			before := []BlockedIssue{{Issue: source, BlockedBy: []string{target.ID}}}
			if backend == "server" {
				writer, err := OpenExisting(ctx, options)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := writer.Close(); err != nil {
						t.Error(err)
					}
				}()
				if err := s.withTx(ctx, false, func(tx *sql.Tx) error {
					// Force the read snapshot before an independent ordinary-server commit.
					if _, err := s.currentSnapshotInTx(ctx, tx); err != nil {
						return err
					}
					if _, err := writer.CloseIssue(ctx, "beads/prereq", "Done", "writer"); err != nil {
						return err
					}
					during, err := s.blockedIssuesInTx(ctx, tx)
					if err != nil {
						return err
					}
					if !sameBlockedJSON(t, during, before) {
						return fmt.Errorf("read mixed pre/post-close membership or records: %+v", during)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				// Production embedded pool remains one connection. Concurrent callers must
				// observe one complete state; this makes no multi-connection engine claim.
				var wg sync.WaitGroup
				wg.Add(2)
				var read []BlockedIssue
				var readErr, writeErr error
				start := make(chan struct{})
				go func() { defer wg.Done(); <-start; read, readErr = s.BlockedIssues(ctx) }()
				go func() { defer wg.Done(); <-start; _, writeErr = s.CloseIssue(ctx, "beads/prereq", "Done", "writer") }()
				close(start)
				wg.Wait()
				if readErr != nil || writeErr != nil || (!sameBlockedJSON(t, read, before) && !sameBlockedJSON(t, read, []BlockedIssue{})) {
					t.Fatalf("concurrent read was partial: %+v %v/%v", read, readErr, writeErr)
				}
			}
			assertGraphBlocked(t, ctx, s, source)
			assertDependencyReadiness(t, ctx, s, source, true)
			assertIssueListRetained(t, ctx, s, "beads/work", source)
		})
	}
}

func TestIssueBlockedCanonicalOrder(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s := issueListFixture(t, backend)
			target, err := s.CreateIssue(ctx, "beads/target", plainIssue("Target"))
			if err != nil {
				t.Fatal(err)
			}
			expected := make([]BlockedIssue, 0, 2)
			for index, path := range []string{"beads/a", "beads/z"} {
				request := plainIssue(path)
				request.Issue.Priority = 4 - index*4
				if _, err := s.CreateIssue(ctx, path, request); err != nil {
					t.Fatal(err)
				}
				dependency, err := s.AddDependency(ctx, DependencyRequest{Path: fmt.Sprintf("links/%d", index), SourcePath: path, TargetPath: "beads/target", Actor: "author"})
				if err != nil {
					t.Fatal(err)
				}
				expected = append(expected, BlockedIssue{Issue: dependency.Source, BlockedBy: []string{target.ID}})
			}
			before := reopenState(t, ctx, s)
			for range 2 {
				got, err := s.BlockedIssues(ctx)
				if err != nil || !sameBlockedJSON(t, got, expected) {
					t.Fatalf("canonical order or complete records differ: %+v %v", got, err)
				}
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("repeat read changed state")
			}
		})
	}
}

// All status-bearing records and Dependencies are authored through supported
// APIs. No derived is_blocked value or retained snapshot is seeded here.
func TestIssueBlockedDependencySubjectStatuses(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s := issueListFixture(t, backend)
			request := plainIssue("In-progress subject")
			request.Issue.Status = types.StatusInProgress
			source, err := s.CreateIssue(ctx, "beads/work", request)
			if err != nil {
				t.Fatal(err)
			}
			target, err := s.CreateIssue(ctx, "beads/prereq", plainIssue("Prerequisite"))
			if err != nil {
				t.Fatal(err)
			}
			added, err := s.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work", TargetPath: "beads/prereq", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			source, dependency := added.Source, added.Link
			if source.Properties.Status != types.StatusInProgress || source.Properties.LeaseExpiresAt != nil {
				t.Fatal("initial in-progress fixture changed or acquired an unrequested lease")
			}
			closed, err := s.CreateIssue(ctx, "beads/closed", plainIssue("Closed subject"))
			if err != nil {
				t.Fatal(err)
			}
			closedResult, err := s.CloseIssue(ctx, "beads/closed", "Done before adding prerequisite", "closer")
			if err != nil {
				t.Fatal(err)
			}
			closed = closedResult.Issue
			if closed.Properties.Status != types.StatusClosed || closed.Properties.ClosedAt == nil {
				t.Fatal("normal close did not establish closed fixture")
			}
			closedLink, err := s.AddDependency(ctx, DependencyRequest{Path: "links/closed", SourcePath: "beads/closed", TargetPath: "beads/prereq", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			closed = closedLink.Source
			pinnedRequest := plainIssue("Pinned subject")
			pinnedRequest.Issue.Status = types.StatusPinned
			pinned, err := s.CreateIssue(ctx, "beads/pinned", pinnedRequest)
			if err != nil {
				t.Fatal(err)
			}
			if pinned.Properties.Status != types.StatusPinned {
				t.Fatal("pinned fixture was normalized to another status")
			}
			pinnedLink, err := s.AddDependency(ctx, DependencyRequest{Path: "links/pinned", SourcePath: "beads/pinned", TargetPath: "beads/prereq", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			pinned = pinnedLink.Source
			if closed.Properties.Status != types.StatusClosed || pinned.Properties.Status != types.StatusPinned {
				t.Fatal("Dependency authoring changed subject status")
			}
			assertDependencyOwned(t, source, dependency)
			assertDependencyOwned(t, closed, closedLink.Link)
			assertDependencyOwned(t, pinned, pinnedLink.Link)
			before := reopenState(t, ctx, s)
			assertGraphBlocked(t, ctx, s, source, target)
			if err := s.withTx(ctx, false, func(tx *sql.Tx) error {
				native, err := issueops.GetBlockedIssuesInTx(ctx, tx, types.WorkFilter{})
				if err != nil {
					return err
				}
				if len(native) != 1 || native[0].ID != source.Properties.ID || native[0].BlockedByCount != 1 || !reflect.DeepEqual(native[0].BlockedBy, []string{target.Properties.ID}) {
					return fmt.Errorf("native status membership differs: %+v", native)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			for _, record := range []IssueRecord{source, target, closed, pinned} {
				path := strings.TrimPrefix(record.ID, s.ScopeURL())
				current, err := s.ShowIssue(ctx, path)
				if err != nil || !sameBlockedJSON(t, current, record) {
					t.Fatalf("read changed complete subject: %v", err)
				}
				assertIssueListRetained(t, ctx, s, path, record)
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("status inspection mutated authority or retained state")
			}
		})
	}
}
